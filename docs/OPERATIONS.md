# Operations

This is the day-to-day operator's guide. The data-plane concepts are in
[SCHEMA.md](SCHEMA.md); the why's and how's of each layer are in
[ARCHITECTURE.md](ARCHITECTURE.md). This file covers deploying, monitoring,
and recovering.

## Deployment

### Systemd (recommended for the agent)

`config/pveconform.service` is a template. Install it:

```sh
sudo systemctl edit pveconform   # if you want overrides
# or simply:
sudo install -Dm644 config/pveconform.service /etc/systemd/system/pveconform.service
sudo systemctl daemon-reload
sudo systemctl enable --now pveconform
```

Environment credentials (see README) must be set either via
`Environment=` lines in the unit, a `Drop-In` file under
`/etc/systemd/system/pveconform.service.d/`, or a `systemctl set-environment`
call. Never bake secrets into `pveconform.yaml`.

Example drop-in `/etc/systemd/system/pveconform.service.d/cred.env`:

```ini
[Service]
Environment="PVECONFORM_PVE_TOKEN_VALUE=root@pam!pveconform=0f63b28d-...."
Environment="PVECONFORM_GIT_TOKEN=ghp_...."
```

### Config file

`config/pveconform.yaml` is the canonical layout. All fields optional;
defaults are sensible. `internal/config.Load` merges over
`internal/config.Defaults()`; env vars overwrite credentials last.

```yaml
log:
  level: info            # debug | info | warn | error
pve:
  auth: token            # token | ticket (credentials are SHARED by all clusters)
  user: root@pam
  token-id: pveconform   # part of user@realm!tokenid=value
  # token: <uuid>       # prefer PVECONFORM_PVE_TOKEN env
  # token-value: <full> # prefer PVECONFORM_PVE_TOKEN_VALUE env — overrides pair
  ca-file:               # optional PVE cluster CA (shared by every endpoint)
  clusters:              # NAMED PVE clusters — the M8 multi-cluster model
    conformance-dev:     #   name MUST match a GitOps composition dir
      base-url: https://pve-dev-01.example:8006   # endpoint for ALL traffic
                                         # to THIS cluster
      nodes:             #   per-cluster node allowlist = the cluster boundary
        - pve-dev-01     #   (a manifest spec.node outside it aborts the
        - pve-dev-02     #   cluster's cycle BEFORE any PVE call)
    # prod-a: ...  #   more clusters added as they come online
git:
  url: https://github.com/you/pveconform-manifests.git
  branch: main
  # path: /var/lib/pveconform/tree   # air-gapped mode (mutually exclusive)
  # token: <ghp_...>       # prefer PVECONFORM_GIT_TOKEN env
reconcile:
  poll-interval: 30s
  task-timeout: 30m
  prune-budget: 3        # max deletions per cycle, PER CLUSTER
listen: 127.0.0.1:9494   # or 0.0.0.0:9494
data-dir: ~/.local/share/pveconform
```

Rules the config enforces (fail-closed): at least one named cluster; each name
must be a valid composition identity (lowercase alnum + `-`); each `base-url`
must parse as `http(s)://host`; **two clusters may not share one endpoint**
(prune scoping is endpoint-based, and two compositions sharing a live
inventory would let one prune the other); every `pve.clusters` entry must have
a composition at `clusters/<name>/resources.yaml` in the git tree, and every
composition must have a configured endpoint.

### Air-gapped / local mode

Point `git.path` at a work tree you maintain yourself (or mount). The agent
never writes it. On the first cycle, `gitx.New` initializes the local branch
if it is not yet checked out. Use this mode when the cluster has no git
access but `rsync`/`ssh` is allowed.

## Observability

- `/healthz` — `200` when a reconcile cycle has finished within the last 2
  minutes; `503` otherwise. Use for liveness probes and load balancers.
- `/metrics` — Prometheus (labels: `result` ∈ {ok, error, aborted},
  `kind`, `what`).
- `/status` — JSON. Top-level fields:
  - `process_start`: RFC3339 UTC.
  - `last_cycle`: `{commit, started_at, finished_at, objects, actions_ok,
    actions_error, pruned, prune_deferred, desired_stale, read_only,
    aborted, abort_reason}` — counters of the most recent completed cycle.
  - `objects`: array of `{cluster, kind, name, node, id, state, last_action,
    last_error, last_converged_at, prune_reason, updated_at}` — one entry
    per cluster-scoped object (M8 tags every record with its cluster).
State values: `desired`, `drift`, `converged`, `in_progress`, `failed`,
`skipped`, `pruned`, `anomalous`. The agent never marks anything
`converged` without a round-trip read back from PVE.

Useful `systemd` / shell checks:

```sh
curl -s 127.0.0.1:9494/healthz && echo OK
curl -s 127.0.0.1:9494/status | jq .counters
# Alert when anomalies > 0 (empty-desired guard or desired-stale on PVE read)
journalctl -u pveconform -f | grep 'anomaly\|abort\|stale'
```

## PVE 9.2 storage listing quirk (ISO + CTTemplate presence)

On a node-local `dir` storage, PVE 9.2's per-type content-listing endpoint
500s with `unable to parse directory volume name 'iso'` (or `'vztmpl'`):

```sh
GET /nodes/{n}/storage/local/content/iso      # → 500 (PVE 9.2 dir storage)
GET /nodes/{n}/storage/local/content/vztmpl   # → 500
GET /nodes/{n}/storage/local/content         # → 200 — use this
```

The bare listing returns every pool on that storage with a `content` field per
entry; pveconform's `Storage.HasContent(ctx, node, storage, contentType,
filename)` filters on it. This is why ISO / CTTemplate presence detection does
**not** use `…/content/iso` or `…/content/vztmpl`. Downloads still go through
`POST /nodes/{n}/storage/{s}/download-url` with the `content=iso|vztmpl` form
parameter. pveconform treats a listing read failure as **fail-closed**: it
skips the download for that node this cycle (a `Skipped` record) rather than
blindly re-downloading, and retries next cycle.

## ISO and CTTemplate are storage artifacts

Both kinds:
- have **no PVE numeric id** — PVE-side identity is `(node, storage, filename)`;
- reconcile via a PVE storage `download` task + a bare `content` listing for
  presence;
- are **never pruned** by pveconform — removing a manifest stops re-downloads
  but does not delete the PVE-side file;
- support `spec.nodes` (a node list). The legacy single `spec.node` is still
  honored as a one-element list; every declared node is checked independently.

Practical consequences: add a new PVE node and add it to the artifact's
`spec.nodes` — pveconform downloads the file onto that node's storage.
Removing a node from `spec.nodes` stops checking that node but does not remove
the file there.

## Structured dependencies (inferred — no `depends-on` needed)

pveconform infers two cross-kind edges from the manifest itself:

```text
VM.spec.hardware.cdrom.iso   →  ISO.metadata.name
LXC.spec.template            →  CTTemplate.metadata.name
```

Behaviour:
- **Unknown references** abort the whole cycle at parse time (fail-closed); no
  PVE writes happen.
- **Reference cycles** abort the whole cycle at parse time.
- **Creation order** is topological: artifacts plan at level 0, the VMs/LXCs
  that reference them at level 1, and so on. Prunes use the reverse order
  (dependants deleted before prerequisites).
- **In-cycle deferral**: if a prerequisite fails (e.g. a CTT download), a
  dependant that references it (e.g. an LXC) is **not attempted** this cycle.
  The next cycle re-derives everything from live state and retries. No
  persistent state file is introduced.
- The `proxops/depends-on` annotation remains an **escape hatch** for
  relationships that cannot be expressed in a structured field; it is merged
  with the inferred edges.

## Pruning safety and artifact conservatism

The ownership gate remains: only PVE objects tagged `pveconform` are eligible
for pruning. The per-cycle **prune budget** (default 3, per cluster) caps
deletions, and the **empty-desired anomaly guard** suppresses prunes when a
kind has 0 manifests but more tagged live objects than the budget.
**All of this is per cluster**: the candidate set, the budget, and the
anomaly guard are scoped to one cluster's composition + node allowlist —
an object belonging to cluster A is never pruned because it is absent from
cluster B's composition, and an empty cluster triggers no destructive
behaviour on its configured nodes. For ISO / CTTemplate, pveconform
**never plans a delete** (the "conservative artifact deletion" guarantee):
PVE storage content may be shared with tooling the agent does not manage, and
PVE has no "delete by pveconform name" semantics.

## Recovery

### A cycle aborted with an anomaly

`pveconform status | jq '.objects'` and `/metrics` `pveconform_cycles_total{result="aborted"}`
both point at which kind. Most common cause: you deleted all `VM` manifests
but PVE still has tagged live VMs. Confirm the intent:

- If the intent was *keep the VMs*, restore the manifests.
- If the intent was *delete*, raise `reconcile.prune-budget` temporarily,
  verify with `diff`, then `apply`.

### PVE task failed, one object stuck

`pveconform status` will show the offending object with `state=failed` and
`lastError` from PVE's exit status. The agent will re-diff on next cycle;
if PVE's task is still in progress, the next attempt will return
`operation in progress` and be logged as a transient failure — the following
cycle re-diffs. There is no local retry state.

### `desired-stale` on every cycle

The git fetch is failing. `journalctl -u pveconform` will show the reason.
Common: git credentials rotated, DNS broke, or the remote deleted the branch.
Fix the cause; the agent returns to normal on the next successful fetch. No
manual intervention needed.

### PVE token rotated

Set `PVECONFORM_PVE_TOKEN_VALUE` (or `PVECONFORM_PVE_TOKEN`+`PVECONFORM_PVE_USER`
for token auth) to the new value, then `sudo systemctl restart pveconform`.
The agent does not pick up env changes live.

### `pveconform apply` exits non-zero

`apply` is a one-shot command with strict abort semantics: any failed action
returns non-zero. This is what you *want* in runbooks and CI. In daemon
mode (`run`) the loop tolerates cycle aborts and keeps ticking; check the
`/status` anomaly counter and `journalctl` for the reason.

## Multi-cluster operation

`diff`, `apply`, and `status` process **every configured cluster** in
deterministic (sorted name) order and label each cluster's section
(`=== conformance-dev ===`, `[prod-a]` ...). A failure or abort on one
cluster never blocks the others; `apply` exits non-zero when any cluster
aborted. Each cluster gets its own PVE endpoint + node allowlist, its own
per-cycle prune budget, and its own empty-desired anomaly guard. The same PVE
id and the same name can exist on different clusters (id/name spaces are
cluster-scoped).

To add a cluster: add `pve.clusters.<name>` to the config AND add
`clusters/<name>/resources.yaml` to the git tree. Both sides must agree; a
mismatch fails closed at config validation.

## Working with PVE

### What pveconform does *not* do in MVP

- **No VM replication.** PVE's `repl*` properties are not modelled
  (deliberate; see docs/GAPS.md).
- **No PVE pool management.** `pool` is not a schema field.
- **No `qm`/`pct` shell-outs.** PVE API is the only interface.
- **No deletion of ISOs.** ISO manifests only create (download). Remove the
  manifest to stop re-downloading; PVE keeps the file.
- **No PVE user/role management.** The PVE token used by pveconform is
  assumed to already exist with the right roles (see README).

### Idempotency by construction

Because `Drift` compares desired vs live by PVE-wire fields (memory in MiB,
disk size in PVE binary-suffix units, NIC model+bridge (auto Mac ignored),
scsihw + iothread + pool + size for disks; **disk pool/size/storage drift on a live PVE volume is NOT auto-applied** (PVE 9.2 /config re-creates the volume → data loss); pveconform surfaces such drift as a non-destructive `anomalous` object state on /status + `pveconform_anomalies_total{type="live_only_slot"}` on /metrics; resize deliberately on PVE then update the manifest), you
should **never** have to `pveconform apply` twice in a row: the second cycle
produces a zero-action plan for a converged cluster. Any non-empty plan is
drift, not state-machine confusion.

### Adopting existing PVE objects

`pveconform adopt` (M8) reverse-engineers live PVE objects into pveconform
YAML. It is READ-ONLY with respect to PVE (it asserts zero PVE writes) and
requires an explicit cluster:

```sh
pveconform adopt --cluster conformance-dev --config .config.yaml
```

What it does:

- uses that cluster's configured endpoint + node allowlist;
- writes one manifest per live object under `<kind>/<conformance-dev>/` in
  the git work tree (VM, LXC, ISO, CTTemplate);
- surfaces **unsupported PVE configuration explicitly** (a `gap` line per
  live key pveconform does not model; `INCOMPLETE` for generated manifests
  missing a value PVE cannot re-report, e.g. the LXC `ostemplate`);
- prints the exact `resources.yaml` lines to add. It does NOT modify
  `clusters/<cluster>/resources.yaml` — listing the generated files is a
  deliberate, reviewable operator step.

The M8 acceptance round-trip is:

```
PVE -> adopt -> YAML -> clusters/<cluster>/resources.yaml -> pveconform diff
     -> zero unexpected drift (for everything pveconform models)
```

Live-only disk anomalies are preserved: adopt interrogates the PVE /config
report, so a fixture like the conformance-dev VM 9101 live-only `scsi1` is
represented in the adopted manifest rather than silently dropped. See
docs/GAPS.md for the seeded gap backlog (the source of new entries is exactly
this adopt report).

### Future: per-cluster secrets (SOPS)

`clusters/<cluster>/config.yaml` is the documented home for cluster-specific
configuration. The intended future shape:

```
clusters/<cluster>/config.yaml       # cluster-specific non-secret config
clusters/<cluster>/secrets.sops.yaml # SOPS-encrypted per-cluster credentials
                                     # (decrypted at runtime; NEVER committed
                                     #                              in cleartext)
```

M8 does NOT implement SOPS: credentials remain shared across clusters in the
process environment (`PVECONFORM_PVE_TOKEN*`). The composition model already
expects one named cluster = one endpoint = one secret set.

## Runbook (typical incident)

```
1. pveconform status                       # which objects are in what state
2. journalctl -u pveconform -n 200         # last cycle's log lines
3. pveconform diff                         # would-be plan
4. pveconform apply --dry-run              # same as 3, through the full pipe
5. Fix the cause (manifest / PVE token / git)
6. pveconform apply                        # one-shot convergence
7. Verify: pveconform status again, /metrics counters reset
```

The agent is intentionally not a "runbook engine": it converges to a known
state every cycle. If it does not converge, the diff *is* the
runbook (it tells you exactly what PVE will do to reach state).
