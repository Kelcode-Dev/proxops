# pveconform — multi-cluster GitOps reconciler for Proxmox VE

`pveconform` continuously reconciles one or more Proxmox VE clusters to the
state declared in a git repository. **Git is the source of truth**: the agent
polls (or is pointed at) a git work tree, discovers every
`clusters/<name>/resources.yaml` composition, parses that cluster's pveconform
manifests, diffs them against the live PVE API on that cluster's own endpoint,
and converges PVE to the desired state — idempotently, serially per cluster,
and with explicit safety rails on deletion.

> Not a Kubernetes operator, not Kustomize. One agent process reconciles
> multiple PVE clusters; each cluster has its own PVE endpoint + node
> allowlist, and each manifest is owned by exactly the cluster(s) whose
> `resources.yaml` lists its file. No Terraform, no state files: the live
> PVE cluster IS the state, re-read every cycle.

## Resource kinds

| Kind | PVE object | Identity | Notes |
|---|---|---|---|
| `VM` | qemu VM | `(node, vmid)` | Fixed `spec.vmid`; disks, networks, CPU, memory, hardware (bios/machine/EFI/cloud-init/TPM/serial/dcdrom), options, power state |
| `LXC` | container | `(node, vmid)` | Fixed `spec.vmid`; root FS (`spec.root`), optional mount points (`mp*`), template ref (`spec.template`), networks, power state |
| `CTTemplate` | downloadable PVE vztmpl pool file | no PVE id; PVE identity `(node, storage, filename)` | ISO- and template-like: PVE storage artifact pveconform DOWNLOADS from `spec.url` when missing on a node. Never pruned. |
| `ISO` | ISO on ISO storage pool | no PVE id; PVE identity `(node, storage, filename)` | Same artifact shape as CTTemplate but content=`iso`. Never pruned. |

Since M8 the manifest tree is a multi-cluster GitOps repository
(see SCHEMA.md § M8 repository layout):

```
clusters/<cluster>/resources.yaml   # what <cluster> consumes (explicit list)
<kind>/{base|<cluster>}/...yaml     # resource definitions (kind ∈ vm, lxc, iso, ctt)
```

Since M9, the cluster's OWN pveconform configuration + credentials also live
in the GitOps repository, cluster-local and SOPS-encrypted:

```
clusters/<cluster>/
  config.yaml         # pveconform config for THIS cluster: pve endpoint,
                      #   node allowlist, SOPS secrets-file reference,
                      #   git source, reconcile knobs. The `--config` arg.
  secrets.sops.yaml   # SOPS-encrypted PVE + git credentials for THIS
                      #   cluster. The public age recipient is in the
                      #   file's sops: metadata; the PRIVATE age key lives
                      #   OUTSIDE this repo (task §7/§8 — see OPERATIONS.md).
  resources.yaml      # M8 resource composition (unchanged by M9)
```

A resource file is reconciled by exactly the cluster(s) whose composition
lists it. Bases are shared by reference; cluster-specific files live under
`<kind>/<cluster>/`.

## Secrets (M9)

PVE & git credentials for a cluster are kept OUT of the global pveconform
config. Each cluster declares:

- `pve.clusters.<name>.secrets-file` — the path to its SOPS-encrypted file
  (relative to `clusters/<name>/`, resolved at Load time to the worktree
  root), and
- `pve.clusters.<name>.secrets` — the per-cluster reference block naming
  which SOPS top-level keys in the decrypted file supply which PVE fields
  (`user`, `token-id`, `token`, `password`) and which SOPS key supplies
  the git token (`git.token`).

The SOPS file is encrypted with Mozilla SOPS, age backend; one SOPS key =
one age identity. pveconform decrypts each SOPS file ONCE at agent
construction into a per-cluster in-memory `Config.SopsResolved` map.
Precedence for a cluster's PVE credentials: SOPS-resolved value >
`PVECONFORM_PVE_*` env > global YAML. The SOPS-resolved value is NEVER
serialized into `diff` / `apply` / `status` / `/metrics` / `/status` /
log lines / error text (task §2; §12), and pveconform never writes the
decrypted value to disk (only sops itself prints to stdout, which
pveconform's Decrypter reads into a memory map and discards).

Unencrypted SOPS files (no `sops:` metadata in the committed file) are
refused with `ErrUnencryptedSecrets`. A wrong/absent age identity is
refused with `ErrNoIdentity`/`ErrIdentityMismatch`. A sops binary that
disappears from PATH is refused with `ErrSOPSBinaryMissing`. These are the
"fail closed where credentials are required" guarantees (task §6).
## Core guarantees

- **Safe deletion** — the agent only deletes PVE objects tagged `pveconform`
  (the ownership tag is added automatically at creation). Anything untagged is
  never touched. Deletions are capped by a per-cycle **prune budget**
  (default 3). An **empty-desired anomaly guard** suppresses prunes when a kind
  has zero manifests but more tagged live objects than the budget — probable
  bad push.
- **Idempotency** — a converged cluster produces a zero-action plan; a second
  cycle does nothing.
- **Fail-closed** — a parse error in the manifest tree aborts the whole cycle
  (last-good tree kept for reads); PVE inventory read failures abort the
  cycle; ISO downloads are skipped when presence is unreadable; the agent
  never force-destroys a running object (stop first).
- **Explicit** — `diff` / `--dry-run` report the full would-be plan per
  cluster, including would-be deletes, without writing anything.
- **Multi-cluster fail-closed** — every cluster has its own PVE endpoint and
  its own node allowlist (config `pve.clusters.<name>`). The allowlist is
  the cluster boundary: a resource's `spec.node` must be in the list for
  its composition, otherwise the cluster's cycle aborts before any PVE
  call. Unknown cluster names (in git but not in config, or the reverse)
  fail closed. Prune candidates are limited to the cluster's allowlist,
  so one cluster can never delete another's objects.
- **One PVE endpoint per cluster** — a cluster's `base-url` serves every
  PVE request *for that cluster*. Because PVE exposes its full API on
  each node in a cluster, the PVE node name (e.g. `pve-dev-01` in
  `spec.node`) is carried only in the request path, never in the host.
  This works even when a node name is not a resolvable DNS hostname.
  (Two clusters sharing one endpoint is refused at config validation.)
- **Adopt = PVE → YAML (read-only)** — `pveconform adopt --cluster
  <name>` reverse-engineers live PVE objects into pveconform manifests
  under `<kind>/<cluster>/`, without ever calling POST/PUT/DELETE on PVE
  (the run asserts zero PVE writes). Unsupported PVE config is surfaced
  as explicit `gap`/`INCOMPLETE` entries; see docs/GAPS.md.
## Layout

```
cmd/pveconform/        cobra CLI: run, diff, apply, status, adopt
internal/schema/       desired-state types + PVE wire mapping (Drift/Create)
internal/parse/        manifest tree -> typed Index (validation, id-space
                       dedup, depends-on DAG)
internal/gitx/         git source of truth: HTTPS fetch or local work tree
internal/pveclient/    thin PVE JSON API client (token/ticket auth, retry,
                       circuit breaker, async task waiter) + stateful in-memory
                       mock for tests
internal/plan/         pure planner: desired vs live -> ordered actions; owns
                       the safety model
internal/exec/         serial executor: applies actions, awaits task UPIDs,
                       records status/metrics
internal/statusx/      convergence store (exposed on /status)
internal/server/       /healthz /metrics /status
internal/reconcile/    one cycle: git fetch -> parse -> load live -> plan -> execute
internal/adopt/        PVE -> pveconform YAML (read-only; cluster-scoped;
                       gap reporting)
internal/composition/  M8 GitOps boundary: discover + validate
                       clusters/*/resources.yaml
internal/app/          composition root; watch loop; dry/apply per cluster
internal/config/       YAML config + M9 cluster-local SOPS credential
                       resolution + env-var bootstrap overlay
internal/secrets/      M9 SOPS age-file decryption, in-memory only (task 2,
                       task 13): never writes plaintext to disk, never logs
                       values, distinct error classes for missing sops
                       binary / unencrypted file / missing / wrong age
                       identity / malformed document
docs/                  ARCHITECTURE, SCHEMA, OPERATIONS
examples/              ready-to-adapt manifest sets
config/                pveconform.yaml + systemd unit templates
```

## Quick start

`pveconform` supports two credential styles. Choose the one that fits
your GitOps layout:

### Style 1 (M9, recommended): cluster-local SOPS config

Clone your GitOps repo, point pveconform at the cluster's local config, and
provide your SOPS age key out-of-band (see [docs/OPERATIONS.md](docs/OPERATIONS.md
#per-cluster-sops-secrets-m9) for the full workflow):

```sh
git clone https://git.example/your/gitops-repo && cd gitops-repo
export SOPS_AGE_KEY_FILE=$HOME/.local/share/pveconform/conformance-dev.age
# age key file lives OUTSIDE the git repo (never committed).
./pveconform diff --config clusters/conformance-dev/config.yaml
./pveconform apply --config clusters/conformance-dev/config.yaml
./pveconform status --config clusters/conformance-dev/config.yaml
./pveconform run --config clusters/conformance-dev/config.yaml
./pveconform adopt --cluster conformance-dev \
                   --config clusters/conformance-dev/config.yaml
```

`clusters/conformance-dev/config.yaml` is a full pveconform config (log, pve,
git, reconcile, listen, data-dir) scoped to exactly ONE cluster:
`conformance-dev`. `clusters/conformance-dev/secrets.sops.yaml` sits next
to it and carries this cluster's PVE token / git token, encrypted via
Mozilla SOPS (age backend). **Private age key never in the repo.**

### Style 2 (M8 bootstrap, legacy): global config + env creds

Use the shared `config/pveconform.yaml` + environment variables:

```sh
export PVECONFORM_PVE_TOKEN_VALUE="root@pam!pveconform=<uuid>"
export PVECONFORM_GIT_TOKEN="ghp_..."
./pveconform diff --config config/pveconform.yaml
```

Style 2 still works; a repository that has not declared a
`clusters/<name>/secrets-file` for any cluster will not require sops to be
installed on the host (task 16).

`config/pveconform.yaml` (style 2) carries **one or more named PVE clusters**
(`pve.clusters.<name>.{base-url,nodes}`); `clusters/<name>/config.yaml`
(style 1) carries exactly one. Every command above processes all named
clusters in deterministic order, except `adopt` which requires one explicit
`--cluster`.

### Credentials & precedence

| Env var | Purpose | Precedence (M9) |
|---|---|---|
| `SOPS_AGE_KEY_FILE` | age private key file path for SOPS decryption | highest, in the SOPS cluster |
| `PVECONFORM_PVE_TOKEN` | PVE API token UUID | bootstrap; overridden by SOPS in a SOPS cluster |
| `PVECONFORM_PVE_TOKEN_VALUE` | fully-composed `user@realm!tokenid=uuid` | bootstrap; overridden by SOPS in a SOPS cluster |
| `PVECONFORM_PVE_PASSWORD` | password for ticket auth | bootstrap; overridden by SOPS in a SOPS cluster |
| `PVECONFORM_PVE_USER` | PVE user id (ticket auth convenience) | bootstrap; overridden by SOPS in a SOPS cluster |
| `PVECONFORM_GIT_TOKEN` | git HTTPS token | bootstrap; overridden by SOPS in SOPS clusters that reference `git.token` |

A config that declares **no** `pve.clusters.<name>.secrets-file` keeps the
M8 env-over-YAML-over-defaults chain exactly. A config that **does** declare
SOPS for a cluster uses, for that cluster only, the SOPS-decrypted value.
The two are never mixed across clusters: cluster A's SOPS token cannot end
up on cluster B's PVE credentials (task 12).

When creating the SOPS file (Style 1), use the `Permissions` role:
`VM.Allocate`, `VM.Configure`, `VM.Create`, `VM.Delete`, `VM.PowerMgmt`,
`VZ.*` equivalents, `Sys.Audit`, and `Datastore.Use`/`Datastore.AllocateSpace`
on your ISO storage. The PVE user (typically `root@pam` or a dedicated
`proxops@pam`) owns the token. The git token (for URL-mode worktrees) should
have read access to the manifest repo.

## Documentation

- [docs/SCHEMA.md](docs/SCHEMA.md) — manifest reference for VM / LXC /
  CTTemplate / ISO (fields, semantics, examples)
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the reconcile pipeline, the
  safety model, and how each layer fits together
- [docs/OPERATIONS.md](docs/OPERATIONS.md) — deployment (systemd),
  observability (/healthz /metrics /status), operations (adopt, prune budget,
  anomaly handling, air-gapped local mode)
- [docs/GAPS.md](docs/GAPS.md) — living compatibility backlog: PVE config
  pveconform does not model, with status/priority/notes per entry
