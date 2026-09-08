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
(VM / LXC / CTTemplate / ISO), validates it (including pinned-`vmid` > 0 for
ids, node-name shape, quantity units, and that LXCs reference a template),
checks for duplicate refs AND duplicate PVE-ids within a node (the id space is
shared for VM/LXC; artifacts have no id), then resolves the **dependency DAG**:

- **structured edges** — `schema.Resource.Deps()` yields inferred edges
  (`VM → ISO` via `spec.hardware.cdrom.iso`, `LXC → CTTemplate` via
  `spec.template`);
- **annotation edges** — the `proxops/depends-on` `Kind:name` list.

`parse.ResolveArtifactRefs` (via `schema.ResolveArtifactRefs`) validates both
edge sets: unknown targets and reference cycles fail closed and abort the whole
cycle; it also rewrites the referencing VM/LXC so the resolved PVE storage
volume (`local:iso/…`, `local:vztmpl/…`) is embedded and the referencing node is
a declared placement node of the artifact. `Index.Levels()` computes the
topological create-level (Kahn) of every ref; the planner consumes it.

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
  `GET /nodes/{n}/storage/{s}/content` (artifact listing).
- Write: `POST /qemu|lxc` (create), `POST .../config` (update),
  `POST .../status/{start|stop|shutdown|reboot}` (power),
  `DELETE /qemu|lxc/{id}` (PVE 9.x delete), `POST .../resize` is **501 / not-implemented** on PVE 9.2 — pveconform does not call it; disk pool/size drift is instead surfaced as a non-destructive anomaly (data-loss guard).
  `POST /nodes/{n}/storage/{s}/download-url` (ISO **and** CTTemplate vztmpl —
  routed by the `content` form parameter). The PVE 8-era `POST /nodes/{n}/storage/{s}/download` path returns 501 "Method not implemented" on PVE 9.2 dir storage. Filename extension is validated at parse time against PVE 9.2 contract: `vztmpl` accepts `.tar | .tar.zst | .tar.xz | .tar.gz`, `iso` accepts `.iso | .img` (see `schema/artifact_ext.go`).

> **PVE 9.2 storage quirk.** `GET /nodes/{n}/storage/{s}/content/iso` and
> `…/content/vztmpl` return `500 "unable to parse directory volume name"`
> because PVE's dir-storage *listing* treats the type segment as a volume id.
> The bare `GET …/content` (no type) works and returns entries tagged with a
> `content` field; `Storage.HasContent` filters on it. See OPERATIONS.md.
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

`plan.LoadLive(ctx, client, desired)` snapshots PVE:
- cluster listing → per-object config + power for every **VM** and **LXC**;
- for every desired **artifact** (an ISO and a CTTemplate — see
  `schema.ArtifactKind`), probes the PVE storage content listing once per
  `(node, storage, content)` and records `{present: bool}` at
  `artifactKey(node, storage, filename, kind)` in the inventory. `HasContent`
  filters the bare `GET …/content` result (PVE 9.2 quirk noted above).

`plan.PlanActions(ctx, desired, live, opts)` is a free function returning
`*Plan` — **it never writes**. It emits:

1. **Tier 0, pass A** — for each desired object:
   - artifact → `planArtifact`: for each node in `spec.nodes`, presence from
     `live.Configs` → `Create` (a `Storage().Download`) when missing, zero
     actions when present, `Skipped` when the storage listing is unreadable.
     One create per missing node; artifacts are never pruned.
   - VM/LXC → standard flow: absent → `Create`; present →
     `Drift(current)` → `Update` (optionally `StopFirst`); power verb →
     `Start`/`Stop` (independent of config drift). The VM/LXC `Create` action
     carries **`Level`** (topological, from `Index.Levels`) and **`Deps`**
     (from `Index.EdgesFor`) so the executor can defer the dependant when a
     prerequisite fails in-cycle.
2. **Tier 9, pass B (prune)** — for each live PVE-listing entry of a managed
   **VM** or **LXC**, NOT claimed by desired, tagged `pveconform`:
   - untagged → `Skipped` (never touched);
   - tagged → prune candidate.
   Membership is by PVE id+node AND ref string. Artifacts are not PVE
   listing entries, so they are invisible to prune (conservative-no-delete).

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
- **Determinism + topological order**: within tier 0 the plan sorts by
  `(Tier, Level, What, Node, ID, Name)`. Level 0 resources (artifacts:
  ISOs + CTTemplates with no PVE reference) are always planned before
  level-1 resources that reference them, so PVE's storage download completes
  before any VM/LXC that embeds its volume id. Prunes (tier 9) use level
  descending — dependants are deleted first.
- **Structured dependencies**: pveconform infers edges from first-class
  schema fields: `VM.spec.hardware.cdrom.iso → ISO`,
  `LXC.spec.template → CTTemplate`. These are merged with the
  `proxops/depends-on` annotation edges at parse time.
- **Dependancy deferral (in-cycle)**: the executor tracks which
  Refs failed earlier this cycle; any later action whose `Deps` intersect
  that set is recorded as `Skipped: deferred: prerequisite <ref> failed
  this cycle` instead of being attempted. The next cycle re-derives from
  live state; no persistent bookkeeping.
- **Unknown-ref + cycle**: both reject the whole cycle at parse time, no
  PVE writes.

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
| ISO / vztmpl storage listing unreadable | Download skipped; next cycle retries (fail-closed) |
| ISO / vztmpl download fails | Action failed; any LXC / VM that references it is **deferred** this cycle |
| LXC create fails (missing template etc.) | Action failed; next cycle retries |
| VM create fails | Action failed; next cycle retries |

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
