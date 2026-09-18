# Operations

This is the day-to-day operator's guide. The data-plane concepts are in
[SCHEMA.md](SCHEMA.md); the why's and how's of each layer are in
[ARCHITECTURE.md](ARCHITECTURE.md). This file covers deploying, monitoring,
and recovering.

## Deployment

### Systemd (recommended for the agent)

`config/proxops.service` is a template for the repository-first deployment:
it sets `WorkingDirectory=` to the ProxOps GitOps checkout and execs
`proxops run` with no `--config` (the unit condition checks that checkout
exists). Install it:

```sh
sudo systemctl edit proxops   # if you want overrides
# or simply:
sudo install -Dm644 config/proxops.service /etc/systemd/system/proxops.service
sudo systemctl daemon-reload
sudo systemctl enable --now proxops
```

Adjust `WorkingDirectory` to your checkout location. Environment credentials
must be set either via `Environment=` lines in the unit, a `Drop-In` file
under `/etc/systemd/system/proxops.service.d/`, or a `systemctl
set-environment` call. Never bake secrets into a committed config: SOPS keys
are supplied via `SOPS_AGE_KEY_FILE` (see [SOPS &
credentials](sops-credentials.md)) or `PROXOPS_*` env vars (see
[CLI reference](cli.md)).

Example drop-in `/etc/systemd/system/proxops.service.d/cred.env`:

```ini
[Service]
Environment="PROXOPS_PVE_TOKEN_VALUE=root@pam!proxops=0f63b28d-...."
Environment="PROXOPS_GIT_TOKEN=ghp_...."
```

### Configuration (repository-first)

The normal deployment has **no standalone config file at all**: the ProxOps
GitOps repository carries its own configuration. From inside the repository:

```sh
cd <gitops repo>
proxops diff    # discovers the repo from the CWD, no --config needed
```

ProxOps discovers:

1. the repository root (the nearest `.git` ancestor of the CWD — or of
   `--git-path <path>` / `PROXOPS_GIT_PATH` when set);
2. an **optional** process-wide `<root>/proxops.yaml`
   (`log` / `reconcile` / `listen` / `data-dir` / bootstrap credentials / git
   URL-mode); absent = defaults; and
3. **every** `<root>/clusters/<name>/config.yaml` — each declaring exactly
   one `pve.clusters.<name>` entry (endpoint + node allowlist + SOPS
   reference), named after its directory.

The full field reference lives in
[Configuration reference](reference-config.md); the SOPS/credential model in
[SOPS & credentials](sops-credentials.md).

**Fail-closed configuration rules:** at least one named cluster; each name a
valid composition identity (lowercase alnum + `-`); each `base-url` parses as
`http(s)://host`; **two clusters may not share one endpoint** (prune scoping
is endpoint-based); every `pve.clusters` entry must have a composition at
`clusters/<name>/resources.yaml` in the tree, and every composition a
configured endpoint; a cluster-local config key that does not match its
directory name is refused.

### URL mode (remote git source)

For hosts that are NOT inside the repository (multi-host management, CI
read-only, mount-based checkouts): the process-wide config declares
`git.url` (+ `branch`, `token` via env), and ProxOps clones/fetches the
remote into the data-dir and reconciles its head:

```yaml
git:
  url: https://github.com/you/proxops-manifests.git
  branch: main
```

The clusters still come from THAT tree's `clusters/<name>/` directories —
URL mode supplies the source tree, not the endpoints. Pass it with
`proxops run --config /etc/proxops/proxops.yaml` (a config that sets
`git.url` is never repository-discovered).

### Air-gapped / local mode

An explicit work tree you maintain yourself (or mount) is selected with
`--git-path <path>` / `PROXOPS_GIT_PATH=<path>` — for automation, tests and
hosts where the checkout path is not the CWD. The agent never writes the
tree. In this mode there is no fetch: the tree the operator maintains IS the
desired state.

## Observability

- `/healthz` — `200` when a reconcile cycle has finished within the last 2
  minutes; `503` otherwise. Use for liveness probes and load balancers.
- `/metrics` — Prometheus (labels: `result` ∈ {ok, error, aborted},
  `kind`, `what`).
- `/status` — JSON. Top-level fields:
  - `process_start`: RFC3339 UTC.
  - `last_cycle`: `{commit, started_at, finished_at, objects, actions_ok,
    actions_error, pruned, prune_deferred, skipped, anomalies,
    desired_stale, read_only, aborted, abort_reason}` — counters of the
    most recent completed cycle.
  - `objects`: array of `{cluster, kind, name, node, id, state, last_action,
    last_error, last_converged_at, prune_reason, updated_at}` — one entry
    per cluster-scoped object (every record carries its cluster).
State values: `desired`, `drift`, `converged`, `in_progress`, `failed`,
`skipped`, `pruned`, `anomalous`. The agent never marks anything
`converged` without a round-trip read back from PVE.

Useful `systemd` / shell checks:

```sh
curl -s 127.0.0.1:9494/healthz && echo OK
curl -s 127.0.0.1:9494/status | jq .last_cycle
# Alert when anomalies > 0 (empty-desired guard or desired-stale on PVE read)
journalctl -u proxops -f | grep 'anomaly\|abort\|stale'
```

## PVE 9.2 storage listing quirk (ISO + CTTemplate + DiskImage presence)

On a node-local `dir` storage, PVE 9.2's per-type content-listing endpoint
500s with `unable to parse directory volume name 'iso'` (or `'vztmpl'` /
`'import'`):

```sh
GET /nodes/{n}/storage/local/content/iso      # → 500 (PVE 9.2 dir storage)
GET /nodes/{n}/storage/local/content/vztmpl   # → 500
GET /nodes/{n}/storage/local/content         # → 200 — use this
```

The bare listing returns every pool on that storage with a `content` field per
entry; ProxOps's `Storage.HasContent(ctx, node, storage, contentType,
filename)` filters on it. This is why ISO / CTTemplate / DiskImage presence
detection does **not** use `…/content/iso`, `…/content/vztmpl` or
`…/content/import`. Downloads still go through
`POST /nodes/{n}/storage/{s}/download-url` with the
`content=iso|vztmpl|import` form parameter. ProxOps treats a listing read
failure as **fail-closed**: it skips the download for that node this cycle (a
`Skipped` record) rather than blindly re-downloading, and retries next cycle.

## ISO, CTTemplate and DiskImage are storage artifacts

All three kinds:
- have **no PVE numeric id** — PVE-side identity is `(node, storage, filename)`;
- reconcile via a PVE storage `download` task + a bare `content` listing for
  presence;
- are **never pruned** by ProxOps — removing a manifest stops re-downloads
  but does not delete the PVE-side file;
- support `spec.nodes` (a node list). The legacy single `spec.node` is still
  honored as a one-element list; every declared node is checked independently.

Practical consequences: add a new PVE node and add it to the artifact's
`spec.nodes` — ProxOps downloads the file onto that node's storage.
Removing a node from `spec.nodes` stops checking that node but does not remove
the file there.

## Structured dependencies (inferred — no `depends-on` needed)

ProxOps infers these cross-kind edges from the manifest itself:

```text
VM.spec.hardware.cdrom.iso          →  ISO.metadata.name
VM.spec.disks[].image               →  DiskImage.metadata.name
VM.spec.clone                       →  TemplateVM.metadata.name
LXC.spec.template                   →  CTTemplate.metadata.name
TemplateVM.spec.hardware.cdrom.iso  →  ISO.metadata.name
TemplateCT.spec.template            →  CTTemplate.metadata.name
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

## Ownership-tag migration

Before the rename to ProxOps, the agent's PVE ownership tag was
`pveconform`; it is now `proxops`. The rename is deliberately
**fail-closed**:

- An object tagged only `pveconform` is treated as **untagged**: ProxOps
  never prunes it, and `diff` reports it as skipped with the reason
  `live object without proxops tag; never touched`.
- Once a manifest for that object is composed into `resources.yaml`, the
  first managed **update** rewrites `tags` to include `proxops` (the
  schema always appends the ownership tag), so the object is claimed
  with a single non-destructive write. Until then it is unmanaged.
- Objects created by the renamed build carry `proxops` from the start.

To migrate a cluster that an older build managed, either let the claim
happen naturally on the first reconcile of each object, or add the tag
out of band (`qm set <vmid> --tags <existing>,proxops` / `pct set`). The
stale `pveconform` tag is harmless and can be removed at leisure —
ProxOps ignores it.

## Pruning safety and artifact conservatism

The ownership gate remains: only PVE objects tagged `proxops` are eligible
for pruning. The per-cycle **prune budget** (default 3, per cluster) caps
deletions, and the **empty-desired anomaly guard** suppresses prunes when a
kind has 0 manifests but more tagged live objects than the budget.
**All of this is per cluster**: the candidate set, the budget, and the
anomaly guard are scoped to one cluster's composition + node allowlist —
an object belonging to cluster A is never pruned because it is absent from
cluster B's composition, and an empty cluster triggers no destructive
behaviour on its configured nodes. For ISO / CTTemplate / DiskImage, proxops
**never plans a delete** (the "conservative artifact deletion" guarantee):
PVE storage content may be shared with tooling the agent does not manage, and
PVE has no "delete by proxops name" semantics.

## Recovery

### A cycle aborted with an anomaly

`proxops status | jq '.objects'` and `/metrics` `proxops_cycles_total{result="aborted"}`
both point at which kind. Most common cause: you deleted all `VM` manifests
but PVE still has tagged live VMs. Confirm the intent:

- If the intent was *keep the VMs*, restore the manifests.
- If the intent was *delete*, raise `reconcile.prune-budget` temporarily,
  verify with `diff`, then `apply`.

### PVE task failed, one object stuck

`proxops status` will show the offending object with `state=failed` and
`lastError` from PVE's exit status. The agent will re-diff on next cycle;
if PVE's task is still in progress, the next attempt will return
`operation in progress` and be logged as a transient failure — the following
cycle re-diffs. There is no local retry state.

### `desired-stale` on every cycle

The git fetch is failing. `journalctl -u proxops` will show the reason.
Common: git credentials rotated, DNS broke, or the remote deleted the branch.
Fix the cause; the agent returns to normal on the next successful fetch. No
manual intervention needed.

### PVE token rotated

Set `PROXOPS_PVE_TOKEN_VALUE` (or `PROXOPS_PVE_TOKEN`+`PROXOPS_PVE_USER`
for token auth) to the new value, then `sudo systemctl restart proxops`.
The agent does not pick up env changes live.

### `proxops apply` exits non-zero

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

To add a cluster: create `clusters/<name>/` with a `config.yaml` declaring
`pve.clusters.<name>` (endpoint + node allowlist + optional SOPS
reference) AND a `resources.yaml` composition. Both sides must agree; a
mismatch fails closed at discovery.

## Working with PVE

### What ProxOps does *not* do

- **No VM replication.** PVE's `repl*` properties are not modelled
  (deliberate; see docs/GAPS.md).
- **No PVE pool management.** `pool` is not a schema field.
- **No `qm`/`pct` shell-outs.** PVE API is the only interface.
- **No deletion of storage artifacts.** ISO / CTTemplate / DiskImage
  manifests only create (download). Remove the manifest to stop
  re-downloading; PVE keeps the file.
- **No PVE user/role management.** The PVE token used by ProxOps is
  assumed to already exist with the right roles (see README).
- **No PVE-kind demotion of a template back to a plain VM/CT.** PVE 9.2 has
  no `/qemu/{id}/untemplate` and no `/lxc/{id}/untemplate` endpoint (probe:
  HTTP 501 "not implemented" on both). ProxOps therefore surfaces a
  `kind: VM` (or `kind: LXC`) desired against a live PVE-side `template=1`
  as a non-destructive anomaly; the demotion is an operator's manual step on
  the PVE host.
- **No re-cloning of an existing VM.** A `spec.clone` VM is cloned from its
  TemplateVM **only when the VM is absent from PVE**. Once the clone exists,
  every change is a config write (never a fresh clone over live data — PVE
  also refuses a clone onto an existing id). To reprovision a clone from an
  updated template, delete the VM manifest (prune), then re-add it.

### Idempotency by construction

Because `Drift` compares desired vs live by PVE-wire fields (memory in MiB,
disk size in PVE binary-suffix units, NIC model+bridge (auto MAC ignored),
scsihw + iothread + pool + size for disks), you should **never** have to
`proxops apply` twice in a row: the second cycle produces a zero-action plan
for a converged cluster. Any non-empty plan is drift, not state-machine
confusion. **Disk pool/size drift on a live PVE volume is NOT auto-applied**
(PVE 9.2 re-creates the volume on a config write → data loss): ProxOps
surfaces such drift as a non-destructive `anomalous` object state on
`/status` + `proxops_anomalies_total{type="live_only_slot"}` on `/metrics`.
Resize deliberately on PVE, then update the manifest.

## Adoption

Adopting live PVE objects into ProxOps YAML, the PVE round-trip
invariants, and production safety expectations are documented in
[Adoption](adopt.md).

## SOPS & credentials

The per-cluster SOPS/age credential workflow (creating the encrypted
file, the age key bootstrap, the precedence chain, the fail-closed
guarantees, and the limitations) is documented in
[SOPS & credentials](sops-credentials.md).

## Runbook (typical incident)

```
1. proxops status                       # which objects are in what state
2. journalctl -u proxops -n 200         # last cycle's log lines
3. proxops diff                         # would-be plan
4. proxops apply --dry-run              # same as 3, through the full pipe
5. Fix the cause (manifest / PVE token / git)
6. proxops apply                        # one-shot convergence
7. Verify: proxops status again, /metrics counters reset
```

The agent is intentionally not a "runbook engine": it converges to a known
state every cycle. If it does not converge, the diff *is* the
runbook (it tells you exactly what PVE will do to reach state).
