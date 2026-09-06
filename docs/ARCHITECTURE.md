# Architecture

`pveconform` is a single-process, cluster-wide GitOps reconciler:

```
                 +----------------------------------------------+
                 |                pveconform agent              |
  git repo       |                                              |
 (HTTPS or local)|  +-------+  +--------+  +----+  +--------+  |
  +------------->|  | gitx  |  | parse  |  |    |  | server |  |
  |              |  +---+---+  +---+----+  |plan|  | /healthz| |
  |              |      |            |     +----+  | /metrics| |
  |              |  Index   +-------+-------+      | /status | |
  |              |       |   |   LoadLive      |    +--------+ |
  |              v       v   v        v        |               |
  |          +---------------------------+     |               |
  |          |    PlanActions (pure)     |     |               |
  |          +-------------+-------------+     |               |
  |                        |  Plan            |               |
  |                        v                  |               |
  |              +---------------------------+ |               |
  |              |  Executor (apply mode)  | |               |
  |              +-------------+-------------+               |
  |                        | PVE form values + task UPIDs    |
  +------------------------+---------------------------------+
                             |
                             v
                     Proxmox VE API (8006)
                     /api2/json/...
```

Every cycle is: **fetch → parse → load-live → plan → execute → report**.
Statelessness is deliberate: restart = full re-diff. There is no local state
file beyond the git cache.

## The layers

### gitx — source of truth

- **URL mode** (default): `go-git` clone into `data-dir`, shallow `main`
  checkout, `fetch` on each cycle. Fetch failures are *advisory*: the agent
  keeps the last-good tree and flags `desired-stale` on that cycle (fail-open
  on stale reads, never on garbage).
- **Local mode** (`--git-path` / `git.path`): the work tree is authoritative;
  the agent never writes it (MVP: no local commit — for air-gapped hosts that
  maintain their own git).

### parse — manifest tree → typed Index

Walks `*.yaml`/`*.yml`, decodes each document into the right `schema.Resource`
(VM / LXC / CTTemplate / ISO), validates it (including pinned-`vmid` > 0,
node-name shape, quantity units), checks for duplicate refs AND duplicate
PVE-ids within a node (the id space is shared), and resolves the `depends-on`
DAG (cycle → error → cycle aborts). Parse errors are **fail-closed**: the whole
cycle aborts, last-good tree retained.

### pveclient — thin PVE JSON API client

No third-party PVE library. Only what the reconciler actually calls.

**One endpoint, all traffic.** PVE exposes its complete JSON API on every node,
so the agent talks to a *single* base URL (`pve.base-url`, e.g.
`https://pve-dev-01.example.invalid:8006`) for everything — cluster-wide reads
(`/cluster/resources`, `/version`), ticket exchange (`/access/ticket`), and
per-node object calls. The PVE node identity (e.g. `pve-dev-01`) appears **only
in the request path** (`/nodes/pve-dev-01/...`), never in the host. This is what
makes the agent work when a node name is not a resolvable DNS hostname (the
conformance-dev case). `pve.nodes` is an optional allowlist of known node names
used to validate `spec.node` in manifests; it is not a set of hosts to dial.

- Auth: PVE API token (`Authorization: PVEAPIToken=user@realm!id=value`) —
  default; or username+password ticket exchange (`POST /access/ticket` →
  `PVEAuthCookie`, with `X-CSRF-Token`).
- Read: `GET /version`, `GET /cluster/resources`, per-object
  `GET /qemu|lxc/{id}/config`, `GET /nodes/{n}/{qemu|lxc}/{id}/status/current`,
  `GET /nodes/{n}/storage/{s}/content/iso`.
- Write: `POST /qemu|lxc` (create), `POST .../config` (update),
  `POST .../status/{start|stop|shutdown|reboot}` (power), `POST .../vmdelete`
  (VM delete), `DELETE /lxc/{id}` (CT delete), `POST .../resize` (VM),
  `POST /lxc/{src}/clone` + `POST /lxc/{dst}/template` (CTT),
  `POST /nodes/{n}/storage/{s}/download` (ISO).
- Async: any mutating call that returns a string `data` is a task UPID; the
  `TaskWaiter` polls `GET /tasks/{upid}/status` until `stopped`+`OK`
  (bounded by `reconcile.task-timeout`).
- Resilience: retry with jittered backoff on 5xx/transient; PVE's
  `HTTP 500 "no such vm"` maps to `IsNotFound`; a write circuit breaker
  halts further writes after N consecutive failures; PVE "operation already
  in progress" is a non-retried failure the planner re-diffs next cycle.

The mock server (`internal/pveclient/mock`) is a stateful httptest PVE:
token+ticket auth, per-node object store (qemu/lxc share the PVE id space),
ISO storage, async task settle-after-N-ticks, PVE-shape error envelopes.
Tests run against it for pveclient unit tests and, in `internal/reconcile`,
against a **real git work tree** for the full e2e pipe.

### plan — pure planner + safety model

`plan.LoadLive(ctx, client, desired)` snapshots PVE: cluster listing →
per-object config + power; for every desired ISO, a `HasISO` storage probe →
`{present: bool}` at the ISO key.

`plan.PlanActions(ctx, desired, live, opts)` is a free function returning
`*Plan` — **it never writes**. It emits:

1. **Tier 0, pass A** — for each desired object:
   - absent → `Create` (with `ToCreateParams()`);
   - present → `Drift(current)` → `Update` (optionally `StopFirst`);
   - power verb → `Start`/`Stop` (independent of config drift);
   - CTT: `StopFirst` forced when re-templating a running CT;
   - ISO: no numeric id; "create" is a download; "present" → zero actions;
     unreadable storage → fail-closed skip.
2. **Tier 9, pass B (prune)** — for each live PVE-listing entry of a managed
   kind, NOT claimed by desired, tagged `pveconform`:
   - untagged → `Skipped` (never touched);
   - tagged → prune candidate.
   Membership is checked two ways: PVE id+node AND ref string. A live LXC
   whose cid is a desired CTT is **not** an orphan (`isCTTOfDesired`) even
   though PVE lists it as `lxc`.

The safety model:

- **Ownership gate**: `pveconform` tag is required for any delete; the tag is
  added automatically at create time.
- **Prune budget**: max N deletes per cycle (default 3); extras go to
  `Deferred` and surface on `/status`.
- **Empty-desired anomaly guard**: if a kind has 0 desired and more
  pveconform-tagged live objects than the budget, ALL prunes for that kind are
  suppressed, `Plan.Anomaly` is set, and the cycle logs it. This catches the
  classic "someone deleted all VM manifests by accident" shape without losing
  the safety model for ordinary small-scale deletions.
- **Determinism**: within tier 0, creates before updates before power;
  prunes are tier 9 and always last. Ordering inside a tier is by node, then
  id, so runs are reproducible.
- **depends-on syntax**: annotation value is a comma-separated string of
  `Kind:name` references (`proxops/depends-on:`).
- **depends-on**: the parser resolves the `depends-on` annotation graph and
  rejects cycles (fail-closed, whole-cycle abort). The annotation currently
  documents intent and validates the graph; PVE id-pinning plus clone-source
  semantics are what make creation ordering safe in practice (a VM created
  from a template is only ever planned after the source template exists).
  Topological scheduling of dependent creates is a post-MVP planner
  enhancement and is NOT relied on today.

### exec — serial executor

One cycle uses exactly one executor (the second reconciler instance is
read-only and shares the plan). Actions run in plan order; each records
`statusx` state transitions (`in_progress` → `converged` / `failed`), PVE
task UPIDs are awaited; a stop-required flow: `Stop` → `Update` →
`Start` (restore, only when `DesiredPower=started`). Failures never abort the
cycle — the result is logged and statused; the next cycle re-diffs.

### statusx + server

`statusx.Store` is an in-memory convergence table keyed
`node|kind|id` or `node|ISO|storage:filename`. The server exposes:

- `GET /healthz` — 200 when a cycle has run within 2 minutes; 503 when stale
  (used by systemd `WatchdogSec`-style monitoring and load balancers).
- `GET /metrics` — Prometheus text format.
- `GET /status` — JSON convergence table (per-object kind/name/id/state/
  last-error/reason + cycle-level anomaly + desired-stale + commit).

### app / CLI

`internal/app` is the composition root: builds pveclient (BaseURL override for
tests), gitx (URL or local), a status store, an apply reconciler, a dry
reconciler, and the HTTP server. The cobra command surface:

- `pveconform run` — daemon: watch loop (poll interval from config),
  tolerates cycle aborts.
- `pveconform diff [--dry-run]` — read-only plan for this tree + PVE.
- `pveconform apply` — one convergence cycle, exit non-zero on any action
  failure (abort semantics: operator must intervene).
- `pveconform status` — print the `/status` JSON table.
- `pveconform adopt` — scaffolds `VM`/`LXC`/`CTTemplate` YAML for live
  tagged objects in the tree (post-MVP: ISO too).

All commands accept a `--config` path; credentials come only from the
environment (see README).

## Failure modes — what happens, by design

| Failure | Behavior |
|---|---|
| git fetch fails (first) | Abort cycle; no PVE reads; `/healthz` 503 after 2 min |
| git fetch fails (subsequent) | Keep last-good tree, cycle marked `stale` |
| Parse fails on a manifest | Abort cycle; keep last-good tree |
| PVE inventory read fails | Abort cycle |
| PVE create/update returns "in progress" | Action failed (logged); next cycle re-diffs |
| PVE task fails | Action failed; statused `failed` |
| PVE 5xx on read (transient) | Retried w/ backoff; eventually aborts the cycle |
| Anomaly: 0 desired + many tagged | Prunes suppressed, `anomaly` on `/status` |
| ISO storage unreadable | Download skipped; next cycle retries |
| CTT running + re-template | Stop-first; PVE refuses the stop → cycle-level failure logged |

## Extending the set of kinds

To add a new kind:

1. `internal/schema/<kind>.go` — struct + `Resource`
   interface impl (`ToCreateParams`, `Drift`, `Validate`; `Kind` constant,
   `Ref()`).
2. `internal/parse/parse.go` — `switch kind` case + `metadataOf` case.
3. `internal/pveclient/` — wire primitive(s) (usually `POST /nodes/{n}/...`).
4. `internal/pveclient/mock/mock.go` — routes + accessors (kind-agnostic id
   space if PVE lists it alongside LXCs, else its own).
5. `internal/plan/plan.go` — register in LoadLive (config/power + optional
   storage probe), in PlanActions pass A/B, and any kind-specific safety.
6. `internal/exec/exec.go` — apply() switch.
7. `docs/SCHEMA.md` + `examples/` + an e2e test in
   `internal/reconcile/reconcile_m4_test.go`-style.

The `CTTemplate` kind is the reference example of a kind whose PVE
**presence** is decoupled from its PVE **type** (LXC-listed, CTT-claimed).
`ISO` is the reference example of a kind with **no PVE numeric id** (identity
is a storage-backend key, and the live "config" is built from a storage listing
probe, not an object read).
