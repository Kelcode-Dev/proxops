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

`pveconform adopt` (M8; production-hardened in M10) reverse-engineers live
PVE objects into pveconform YAML. It is READ-ONLY with respect to PVE —
it performs only GET requests and asserts zero PVE writes at the end of
the run — and requires an explicit cluster:

```sh
# In the GitOps work tree, with the operator's age identity exported:
export SOPS_AGE_KEY_FILE=~/.local/share/pveconform/<cluster>.age   # outside the repo
pveconform adopt --cluster conformance-dev --config clusters/conformance-dev/config.yaml
```

What it does:

- uses that cluster's configured endpoint + node allowlist (or, without an
  allowlist, PVE's `/cluster/nodes` listing); only allowlisted nodes are
  ever read — this is the cluster-isolation guarantee;
- writes one manifest per live object under `<kind>/<cluster>/` in the git
  work tree (VM, LXC, ISO, CTTemplate);
- surfaces **unsupported PVE configuration explicitly** (a `gap` line per
  live key pveconform does not model; `INCOMPLETE` for generated manifests
  missing a value PVE cannot re-report, e.g. the LXC `ostemplate`;
  `SKIPPED` for objects pveconform deliberately does not generate — PVE
  *template* VMs, which a pveconform-managed manifest would wrongly claim
  ownership of);
- **redacts sensitive PVE fields** in the gap report: `sshkeys` and
  `cipassword` values are emitted as `<redacted>` (the field name still
  reports, so the operator knows pveconform does not model it);
- prints the exact `resources.yaml` lines to add. It does NOT modify
  `clusters/<cluster>/resources.yaml` — listing the generated files is a
  deliberate, reviewable operator step.

#### Determinism (M10)

Two `adopt` runs against an unchanged PVE produce **byte-identical**
manifests and gap reports: the manifest set, filenames, field ordering,
gap ordering, and the INCOMPLETE/SKIPPED lists are all total-ordered. No
timestamps, no PVE-assigned randomness, no credentials appear in the
output. A second run therefore produces no meaningless git diff.

#### Production safety expectations

When the adopted cluster is a production PVE:

- `adopt` is the **only** pveconform command safe to run against it
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
- The generated manifests are stripped of pveconform's ownership tag from
  `spec.tags` (adopt never invents tags); the tag is re-appended at create
  time, and on the first reconcile pveconform claims the live object by
  adding that tag. Untagged live objects are never modified or deleted.

#### Round-trip verification

The acceptance round-trip is:

```
PVE -> adopt -> YAML -> (human review) -> clusters/<cluster>/resources.yaml
     -> pveconform diff -> zero unexpected drift
```

Every remaining drift line must map to a documented M10 expectation:

- `update ... config drift` on every adopted VM: the ownership-tag claim
  (PVE objects carry no `pveconform` tag; pveconform adds one when it
  manages an object). This is expected and is the first write the operator
  consciously approves — it is not applied by `diff`.
- `anomaly ... live-only disk slot scsiN=...-cloudinit,media=cdrom` on VMs
  whose cloud-init volume sits on a non-IDE slot (PVE 9.x places cloud-init
  on `scsi1` when `ide2` is not used): pveconform does not own non-IDE
  cdrom slots; adopt documents them as a gap and leaves them PVE-managed.
- `skipped (no pveconform tag)` for objects not yet composed
  (INCOMPLETE LXC resources, PVE template VMs).

Live-only data disks are preserved: adopt interrogates the PVE /config
report, so a live second data disk is represented in the adopted manifest
rather than silently dropped. PVE cloud-init volumes on non-IDE slots, by
contrast, are PVE-owned and are explicitly reported. See docs/GAPS.md for
the gap backlog (the source of new entries is exactly this adopt report).

### Per-cluster SOPS secrets (M9)

`clusters/<cluster>/` carries this cluster's pveconform configuration AND its
encrypted credentials:

```
clusters/<cluster>/config.yaml        # cluster-local pveconform config (the
                                      #   --config argument)
clusters/<cluster>/secrets.sops.yaml  # SOPS/age-encrypted PVE + git creds
clusters/<cluster>/resources.yaml     # M8 resource composition
```

**Why SOPS + age?** Mozilla SOPS with the `age` backend encrypts each scalar
individually and records the public age recipient inside the file's `sops:`
metadata — so the *public* key is safe to commit, while the *private* key
stays outside the repository. pveconform shells out to the `sops`
executable (age backend) rather than linking the SOPS Go module: the module
would pull ~160 transitive dependencies (multi-cloud KMS backends, gRPC,
Azure/GCP/Ali/Huawei SDKs) into a standalone single-binary tool; the `sops`
executable the operator already has for encrypting secrets is the deliberate,
justified choice (task §16). A run that configures NO `secrets-file`
never invokes sops at all.

**age key handling (bootstrap, task §7/§8).** The private age key MUST live
outside the GitOps repository. pveconform spawns `sops --decrypt` inheriting
its own environment, so the operator supplies the key via the standard SOPS
age identity mechanism:

```sh
export SOPS_AGE_KEY_FILE=$HOME/.local/share/pveconform/conformance-dev.age
# SOPS_AGE_KEY / AGE_KEY_FILE work too; whatever sops' age backend reads.
```

Generate a disposable key (development only):

```sh
age-keygen -o ~/.local/share/pveconform/conformance-dev.age
# the private key now lives ONLY in that file. Never commit it, never echo it.
```

**The encrypted repository must not contain the private decryption
identity.** It may contain the public age recipient — inside
`secrets.sops.yaml`'s `sops:` metadata. That is how authorized operators are
added without re-encrypting. pveconform never reads, writes, or manages the
private key; it only sets `sops`'s environment to what the operator already
has.

**Credentials precedence (per cluster, highest first, task §6):**

```
SOPS-decrypted value referenced by pve.clusters.<c>.secrets   (explicit cluster secret)
  > PVECONFORM_PVE_* / PVECONFORM_GIT_TOKEN environment vars  (bootstrap)
  > global pve.* fields in config.yaml                        (bootstrap)
```

When a cluster's `secrets-file` is configured, pveconform requires every
field referenced under that cluster's `secrets:` block to be present and
non-empty in the decrypted document — it does NOT silently fall back to
env/YAML for that field (fail closed; an empty/missing SOPS secret cannot
result in an unintended credential being used). For `pve.auth=token`, a
SOPS cluster additionally suppresses `pve.token-value` for that cluster's
PVE params (see the PVEParams precedence in ARCHITECTURE.md): a global
pre-composed `PVECONFORM_PVE_TOKEN_VALUE` must never shadow a cluster's SOPS
reference. Clusters with no `secrets-file` keep the M8 env/YAML behaviour
exactly.

**Create/update the conformance-dev secret** (the plaintext value must only
exist in your editor + this shell session):

```sh
AGE_KEY=~/.local/share/pveconform/conformance-dev.age
PUB=$(grep 'public key:' "$AGE_KEY" | cut -d' ' -f6)   # e.g. age1...
# 1) write the PLAINTEXT into a scratch file OUTSIDE the git worktree:
cat > /tmp/conformance-dev-secrets-plain.yaml <<'EOF'
secrets:
  pveconform-user: root@pam
  pveconform-token-id: pveconform
  pveconform-token: <PASTE PVE token uuid>
  pveconform-password: ""
  pve-git-token: <PASTE git fetch token>
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
an operator = drop their key from the list, re-encrypt, commit. pveconform
has no auto-rotation: re-encryption is the operator's step.

**Run pveconform against the cluster-local config:**

```sh
git clone <gitops repo> && cd <gitops repo>
export SOPS_AGE_KEY_FILE=~/.local/share/pveconform/conformance-dev.age   # outside the repo
pveconform diff   --config clusters/conformance-dev/config.yaml
pveconform apply  --config clusters/conformance-dev/config.yaml
pveconform status --config clusters/conformance-dev/config.yaml
pveconform run    --config clusters/conformance-dev/config.yaml   # daemon
```

`git.path: "."` in `config.yaml` means "the git worktree containing this
config file": pveconform resolves it by walking up from the config path to
the nearest `.git` marker. It never guesses; if the config file is copied
out of a worktree it fails at Load with a clear error (task §5 "no implicit
magic").

**Security guarantees:**

- Decryption happens in memory only: `sops --decrypt` stdout → parsed into
  the in-memory `Config.SopsResolved` map → applied to PVE auth + git fetch
  headers. pveconform writes no decrypted file to disk, ever.
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
  `secrets-file` (task §16: a plain env-credential deployment never invokes
  sops).

**Limitations (deliberate, task §20):**

- No automatic key or secret rotation: re-encryption is the operator's
  step on key change (a recipient can be added and the SOPS file re-encrypted
  without rotating the secret values, as long as the values haven't changed).
- One SOPS file per cluster: `clusters/<cluster>/secrets-file` is a
  path-resolved relative path from the config file's directory. Two
  clusters can point at the same file if they genuinely share secrets;
  pveconform will decrypt each reference exactly once per cluster.
- The SOPS document is only read at agent startup; pveconform does not watch
  the file for changes or poll it (re-reconcile = re-load the config with
  `systemctl restart pveconform` or a new invocation). The same applies to
  env credentials.
- SOPS supports KMS/PGP/GCP/Azure/Huawei/Ali backends; pveconform uses
  `age` only (task §2). The operator is responsible for keeping the age
  key file available before the pveconform process starts.
- No in-band audit log of SOPS decryption events (task §20 explicitly
  excludes this). A pveconform log line will say "resolve SOPS for cluster
  X" — that's all.

**M8 compatibility:** configurations without `secrets-file` are byte-
identical to M8 (env-over-YAML-over-defaults); pveconform does not even
locate the sops binary during `Load` or `Validate` — only during
`ResolveSOPS` at agent construction, and only for SOPS-referenced clusters.

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
