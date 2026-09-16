# Operations

This is the day-to-day operator's guide. The data-plane concepts are in
[SCHEMA.md](SCHEMA.md); the why's and how's of each layer are in
[ARCHITECTURE.md](ARCHITECTURE.md). This file covers deploying, monitoring,
and recovering.

## Deployment

### Systemd (recommended for the agent)

`config/proxops.service` is a template. Install it:

```sh
sudo systemctl edit proxops   # if you want overrides
# or simply:
sudo install -Dm644 config/proxops.service /etc/systemd/system/proxops.service
sudo systemctl daemon-reload
sudo systemctl enable --now proxops
```

Environment credentials (see README) must be set either via
`Environment=` lines in the unit, a `Drop-In` file under
`/etc/systemd/system/proxops.service.d/`, or a `systemctl set-environment`
call. Never bake secrets into `proxops.yaml`.

Example drop-in `/etc/systemd/system/proxops.service.d/cred.env`:

```ini
[Service]
Environment="PROXOPS_PVE_TOKEN_VALUE=root@pam!proxops=0f63b28d-...."
Environment="PROXOPS_GIT_TOKEN=ghp_...."
```

### Config file

`config/proxops.yaml` is the canonical layout. All fields optional;
defaults are sensible. `internal/config.Load` merges over
`internal/config.Defaults()`; env vars overwrite credentials last.

```yaml
log:
  level: info            # debug | info | warn | error
pve:
  auth: token            # token | ticket (credentials are SHARED by all clusters)
  user: root@pam
  token-id: proxops   # part of user@realm!tokenid=value
  # token: <uuid>       # prefer PROXOPS_PVE_TOKEN env
  # token-value: <full> # prefer PROXOPS_PVE_TOKEN_VALUE env — overrides pair
  ca-file:               # optional PVE cluster CA (shared by every endpoint)
  clusters:              # NAMED PVE clusters — the multi-cluster model
    conformance-dev:     #   name MUST match a GitOps composition dir
      base-url: https://pve-dev-01.example:8006   # endpoint for ALL traffic
                                         # to THIS cluster
      nodes:             #   per-cluster node allowlist = the cluster boundary
        - pve-dev-01     #   (a manifest spec.node outside it aborts the
        - pve-dev-02     #   cluster's cycle BEFORE any PVE call)
    # prod-a: ...  #   more clusters added as they come online
git:
  url: https://github.com/you/proxops-manifests.git
  branch: main
  # path: /var/lib/proxops/tree   # air-gapped mode (mutually exclusive)
  # token: <ghp_...>       # prefer PROXOPS_GIT_TOKEN env
reconcile:
  poll-interval: 30s
  task-timeout: 30m
  prune-budget: 3        # max deletions per cycle, PER CLUSTER
listen: 127.0.0.1:9494   # or 0.0.0.0:9494
data-dir: ~/.local/share/proxops
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

To add a cluster: add `pve.clusters.<name>` to the config AND add
`clusters/<name>/resources.yaml` to the git tree. Both sides must agree; a
mismatch fails closed at config validation.

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

### Adopting existing PVE objects

`proxops adopt` reverse-engineers live
PVE objects into ProxOps YAML. It is READ-ONLY with respect to PVE —
it performs only GET requests and asserts zero PVE writes at the end of
the run — and requires an explicit cluster:

```sh
# In the GitOps work tree, with the operator's age identity exported:
export SOPS_AGE_KEY_FILE=~/.local/share/proxops/<cluster>.age   # outside the repo
proxops adopt --cluster conformance-dev --config clusters/conformance-dev/config.yaml
```

What it does:

- uses that cluster's configured endpoint + node allowlist (or, without an
  allowlist, PVE's `/cluster/nodes` listing); only allowlisted nodes are
  ever read — this is the cluster-isolation guarantee;
- writes one manifest per live object under `<kind>/<cluster>/` in the git
  work tree (VM, LXC, ISO, CTTemplate, TemplateVM, TemplateCT); `import`
  content (DiskImage) is not adopted — see GAPS.md;
- surfaces **unsupported PVE configuration explicitly** (a `gap` line per
  live key ProxOps does not model; `INCOMPLETE` for generated manifests
  missing a value PVE cannot re-report, e.g. the LXC `ostemplate`). PVE
  *template* VMs (`template=1` on a `type=qm` object) are adopted as
  `kind: TemplateVM` manifests under `templatevm/<cluster>/`, and PVE
  *template* containers (`template=1` on a `type=lxc` object, M13) as
  `kind: TemplateCT` manifests under `templatect/<cluster>/`, both with the
  full lifecycle owned (create + mark, config drift, prune). PVE-side
  `sshkeys` in the template's cloud-init are redacted to the `["*"]`
  sentinel so the operator fills in the real key(s) before apply;
  `cipassword` / `cicustom` stay as gap lines. A ProxOps `kind: VM` (or
  `kind: LXC`) desired against a live PVE-side template at the same
  `(node, vmid)` is surfaced as a non-destructive anomaly (no kind-flip
  write).
- **redacts sensitive PVE fields** in the gap report: `sshkeys` and
  `cipassword` values are emitted as `<redacted>` (the field name still
  reports, so the operator knows ProxOps does not model it);
- prints the exact `resources.yaml` lines to add. It does NOT modify
  `clusters/<cluster>/resources.yaml` — listing the generated files is a
  deliberate, reviewable operator step.

#### Determinism

Two `adopt` runs against an unchanged PVE produce **byte-identical**
manifests and gap reports: the manifest set, filenames, field ordering,
gap ordering, and the INCOMPLETE/SKIPPED lists are all total-ordered. No
timestamps, no PVE-assigned randomness, no credentials appear in the
output. A second run therefore produces no meaningless git diff.

#### Production safety expectations

When the adopted cluster is a production PVE:

- `adopt` is the **only** proxops command safe to run against it
  unattended: it issues GETs to `/cluster/nodes`,
  `/nodes/{n}/{qemu,lxc}`, `/nodes/{n}/storage`,
  `/nodes/{n}/storage/{s}/content`, `/nodes/{n}/qemu/{v}/config`,
  `/nodes/{n}/lxc/{c}/config` — and nothing else. Post-run, the client's
  write counter must read 0 or the run aborts.
- **Do NOT run `apply` / `run` / a normal reconcile cycle against a
  freshly adopted production cluster.** Adoption output is reviewed,
  completed (INCOMPLETE resources), and composed into
  `resources.yaml` by a human first; only then is `diff` used to verify
  zero unexpected drift.
- The generated manifests are stripped of ProxOps's ownership tag from
  `spec.tags` (adopt never invents tags); the tag is re-appended at create
  time, and on the first reconcile ProxOps claims the live object by
  adding that tag. Untagged live objects are never modified or deleted.

#### Round-trip verification

The acceptance round-trip is:

```
PVE -> adopt -> YAML -> (human review) -> clusters/<cluster>/resources.yaml
     -> proxops diff -> zero unexpected drift
```

Every remaining drift line must map to a documented expectation:

- `update ... config drift` on every adopted VM / LXC / TemplateVM /
  TemplateCT: the ownership-tag claim
  (PVE objects carry no `proxops` tag; ProxOps adds one when it
  manages an object). This is expected and is the first write the operator
  consciously approves — it is not applied by `diff`.
- `anomaly ... live-only disk slot scsiN=...-cloudinit,media=cdrom` on VMs
  whose cloud-init volume sits on a non-IDE slot (PVE 9.x places cloud-init
  on `scsi1` when `ide2` is not used): ProxOps does not own non-IDE
  cdrom slots; adopt documents them as a gap and leaves them PVE-managed.
- `skipped (no proxops tag)` for LXC resources whose `spec.template`
  could not be recovered (INCOMPLETE). PVE-side `template=1` objects are
  adopted as `kind: TemplateVM` under `templatevm/<cluster>/` (qemu) or
  `kind: TemplateCT` under `templatect/<cluster>/` (container).

Live-only data disks are preserved: adopt interrogates the PVE /config
report, so a live second data disk is represented in the adopted manifest
rather than silently dropped. PVE cloud-init volumes on non-IDE slots, by
contrast, are PVE-owned and are explicitly reported. See docs/GAPS.md for
the gap backlog (the source of new entries is exactly this adopt report).

### Per-cluster SOPS secrets

`clusters/<cluster>/` carries this cluster's ProxOps configuration AND its
encrypted credentials:

```
clusters/<cluster>/config.yaml        # cluster-local proxops config (the
                                      #   --config argument)
clusters/<cluster>/secrets.sops.yaml  # SOPS/age-encrypted PVE + git creds
clusters/<cluster>/resources.yaml     # resource composition
```

**Why SOPS + age?** Mozilla SOPS with the `age` backend encrypts each scalar
individually and records the public age recipient inside the file's `sops:`
metadata — so the *public* key is safe to commit, while the *private* key
stays outside the repository. ProxOps shells out to the `sops`
executable (age backend) rather than linking the SOPS Go module: the module
would pull ~160 transitive dependencies (multi-cloud KMS backends, gRPC,
Azure/GCP/Ali/Huawei SDKs) into a standalone single-binary tool; the `sops`
executable the operator already has for encrypting secrets is the deliberate,
justified choice. A run that configures NO `secrets-file`
never invokes sops at all.

**age key handling (bootstrap).** The private age key MUST live
outside the GitOps repository. ProxOps spawns `sops --decrypt` inheriting
its own environment, so the operator supplies the key via the standard SOPS
age identity mechanism:

```sh
export SOPS_AGE_KEY_FILE=$HOME/.local/share/proxops/conformance-dev.age
# SOPS_AGE_KEY / AGE_KEY_FILE work too; whatever sops' age backend reads.
```

Generate a disposable key (development only):

```sh
age-keygen -o ~/.local/share/proxops/conformance-dev.age
# the private key now lives ONLY in that file. Never commit it, never echo it.
```

**The encrypted repository must not contain the private decryption
identity.** It may contain the public age recipient — inside
`secrets.sops.yaml`'s `sops:` metadata. That is how authorized operators are
added without re-encrypting. ProxOps never reads, writes, or manages the
private key; it only sets `sops`'s environment to what the operator already
has.

**Credentials precedence (per cluster, highest first):**

```
SOPS-decrypted value referenced by pve.clusters.<c>.secrets   (explicit cluster secret)
  > PROXOPS_PVE_* / PROXOPS_GIT_TOKEN environment vars  (bootstrap)
  > global pve.* fields in config.yaml                        (bootstrap)
```

When a cluster's `secrets-file` is configured, ProxOps requires every
field referenced under that cluster's `secrets:` block to be present and
non-empty in the decrypted document — it does NOT silently fall back to
env/YAML for that field (fail closed; an empty/missing SOPS secret cannot
result in an unintended credential being used). For `pve.auth=token`, a
SOPS cluster additionally suppresses `pve.token-value` for that cluster's
PVE params (see the PVEParams precedence in ARCHITECTURE.md): a global
pre-composed `PROXOPS_PVE_TOKEN_VALUE` must never shadow a cluster's SOPS
reference. Clusters with no `secrets-file` keep the plain env/YAML behaviour
exactly.

**Create/update the conformance-dev secret** (the plaintext value must only
exist in your editor + this shell session):

```sh
AGE_KEY=~/.local/share/proxops/conformance-dev.age
PUB=$(grep 'public key:' "$AGE_KEY" | cut -d' ' -f6)   # e.g. age1...
# 1) write the PLAINTEXT into a scratch file OUTSIDE the git worktree:
cat > /tmp/conformance-dev-secrets-plain.yaml <<'EOF'
secrets:
  proxops-user: root@pam
  proxops-token-id: proxops
  proxops-token: <PASTE PVE token uuid>
  proxops-password: ""
  proxops-git-token: <PASTE git fetch token>
EOF
# 2) encrypt against the public recipient (age backend only):
sops --encrypt --age "$PUB" --input-type yaml --output-type yaml \
  /tmp/conformance-dev-secrets-plain.yaml > \
  clusters/conformance-dev/secrets.sops.yaml
# 3) shred the plaintext and verify no cleartext leaked into the worktree:
shred -u /tmp/conformance-dev-secrets-plain.yaml
grep -Rn "<PASTE" clusters/ && echo "LEAK: plaintext still in worktree"
git add clusters/conformance-dev/secrets.sops.yaml && git commit
```

For a production cluster / multi-operator: add each operator's public age
key to the recipient list (`sops --encrypt --age "PUB1,PUB2"`). Rotating
an operator = drop their key from the list, re-encrypt, commit. ProxOps
has no auto-rotation: re-encryption is the operator's step.

**Run proxops against the cluster-local config:**

```sh
git clone <gitops repo> && cd <gitops repo>
export SOPS_AGE_KEY_FILE=~/.local/share/proxops/conformance-dev.age   # outside the repo
proxops diff   --config clusters/conformance-dev/config.yaml
proxops apply  --config clusters/conformance-dev/config.yaml
proxops status --config clusters/conformance-dev/config.yaml
proxops run    --config clusters/conformance-dev/config.yaml   # daemon
```

`git.path: "."` in `config.yaml` means "the git worktree containing this
config file": ProxOps resolves it by walking up from the config path to
the nearest `.git` marker. It never guesses; if the config file is copied
out of a worktree it fails at Load with a clear error (no implicit magic).

**Security guarantees:**

- Decryption happens in memory only: `sops --decrypt` stdout → parsed into
  the in-memory `Config.SopsResolved` map → applied to PVE auth + git fetch
  headers. ProxOps writes no decrypted file to disk, ever.
- Decrypted values appear in NONE of: `diff` / `apply` / `status` / `run`
  stdout, the `/status` JSON, the `/metrics` labels or log output, error
  messages, or the `Agent.Config()` accessor. (The in-memory SopsResolved
  map is deliberately `json:"-" yaml:"-"`.)
- Unencrypted `secrets.sops.yaml` (no sops metadata) → refused before any
  PVE call. Malformed SOPS document, missing age identity, wrong age
  identity → refused, with a clear message that names the *class* of
  failure (and only the file path, never the secret).
- The private age key file MUST live outside the git worktree.
- The SOPS binary is only located/inherited when a cluster actually names a
  `secrets-file` (a plain env-credential deployment never invokes sops).

**Limitations (deliberate):**

- No automatic key or secret rotation: re-encryption is the operator's
  step on key change (a recipient can be added and the SOPS file re-encrypted
  without rotating the secret values, as long as the values haven't changed).
- One SOPS file per cluster: `clusters/<cluster>/secrets-file` is a
  path-resolved relative path from the config file's directory. Two
  clusters can point at the same file if they genuinely share secrets;
  ProxOps will decrypt each reference exactly once per cluster.
- The SOPS document is only read at agent startup; ProxOps does not watch
  the file for changes or poll it (re-reconcile = re-load the config with
  `systemctl restart proxops` or a new invocation). The same applies to
  env credentials.
- SOPS supports KMS/PGP/GCP/Azure/Huawei/Ali backends; ProxOps uses
  `age` only. The operator is responsible for keeping the age
  key file available before the ProxOps process starts.
- No in-band audit log of SOPS decryption events. A ProxOps log line will
  say "resolve SOPS for cluster X" — that's all.

**Compatibility:** configurations without `secrets-file` behave exactly
like a plain env-credential deployment (env-over-YAML-over-defaults);
ProxOps does not even locate the sops binary during `Load` or `Validate` —
only during `ResolveSOPS` at agent construction, and only for
SOPS-referenced clusters.

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
