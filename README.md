# ProxOps — multi-cluster GitOps reconciler for Proxmox VE

> ## ⚠️ Experimental — Not for Production Use
>
> ProxOps is currently under active development and has **not been validated
> for production Proxmox management**. Do not use ProxOps to manage production
> Proxmox systems at this stage.
>
> The production testing the project *has* performed is deliberately
> restricted to **read-only adoption/audit workflows** (`proxops adopt`,
> which asserts zero PVE writes). That validates observation and
> reverse-translation fidelity only — it does **not** constitute production
> management validation. All create/update/delete lifecycle validation has
> been done against a disposable development cluster (`conformance-dev`,
> PVE 9.2), not production.

`proxops` continuously reconciles one or more Proxmox VE clusters to the
state declared in a git repository. **Git is the source of truth**: the agent
polls (or is pointed at) a git work tree, discovers every
`clusters/<name>/resources.yaml` composition, parses that cluster's ProxOps
manifests, diffs them against the live PVE API on that cluster's own endpoint,
and converges PVE to the desired state — idempotently, serially per cluster,
and with explicit safety rails on deletion.

> Not a Kubernetes operator, not Kustomize. One agent process reconciles
> multiple PVE clusters; each cluster has its own PVE endpoint + node
> allowlist, and each manifest is owned by exactly the cluster(s) whose
> `resources.yaml` lists its file. No Terraform, no state files: the live
> PVE cluster IS the state, re-read every cycle.

The project was previously named `pveconform`; the binary, configuration,
environment variables, metrics, and the PVE ownership tag are now
`proxops`. See [docs/OPERATIONS.md](docs/OPERATIONS.md#ownership-tag-migration)
for what the ownership-tag rename means for objects already tagged by an
older build.

## Resource kinds

| Kind | PVE object | Identity | Notes |
|---|---|---|---|
| `VM` | qemu VM | `(node, vmid)` | Fixed `spec.vmid`; disks (incl. image-seeded via `spec.disks[].image`), networks, CPU, memory, hardware (bios/machine/EFI/cloud-init/TPM/serial/cdrom), options, cloud-init data (`spec.cloud-init-data`: ci-user/ssh-keys/nameservers/search-domains/ipconfigs), power state. May be provisioned by cloning a `TemplateVM` via `spec.clone` (full clone + the VM's own config; inherited identity is overwritten/cleared). |
| `LXC` | container | `(node, vmid)` | Fixed `spec.vmid`; root FS (`spec.root`), optional mount points (`mp*`), template ref (`spec.template`), networks (incl. static `ip`/`gw`), DNS, options, power state |
| `TemplateVM` | qemu VM promoted to a PVE template (`template=1`) | `(node, vmid)` (shares PVE's qm id space with VM) | Schema identical to `VM`; `spec.state` must be `stopped`. ProxOps creates it via `POST /qemu` + `POST /qemu/{id}/template` and manages it via the standard `/config` surface. PVE 9.2 has no `/qemu/{id}/untemplate` endpoint; a `kind: VM` desired against a live PVE-side template surfaces a non-destructive anomaly instead of a kind-flip write. |
| `CTTemplate` | downloadable PVE vztmpl pool file | no PVE id; PVE identity `(node, storage, filename)` | PVE storage artifact ProxOps DOWNLOADS from `spec.url` when missing on a node. Never pruned. |
| `ISO` | ISO on ISO storage pool | no PVE id; PVE identity `(node, storage, filename)` | Same artifact shape as CTTemplate but content=`iso`. Never pruned. |
| `DiskImage` | downloadable disk image (qcow2/vmdk/raw) on PVE's `import` pool | no PVE id; PVE identity `(node, storage, filename)` | Same artifact shape but content=`import`. A `VM` disk references it via `spec.disks[].image` and ProxOps seeds the disk at create with PVE's `import-from` form — the way to boot a real VM from a cloud image without a template. Never pruned. |

## Repository layout

The manifest tree is a multi-cluster GitOps repository
(see [docs/SCHEMA.md](docs/SCHEMA.md#repository-layout)):

```
clusters/<cluster>/resources.yaml   # what <cluster> consumes (explicit list)
<kind>/{base|<cluster>}/...yaml     # resource definitions
                                    # (kind ∈ vm, lxc, iso, ctt, templatevm, diskimage)
```

The cluster's OWN ProxOps configuration + credentials also live in the
GitOps repository, cluster-local and SOPS-encrypted:

```
clusters/<cluster>/
  config.yaml         # ProxOps config for THIS cluster: pve endpoint,
                      #   node allowlist, SOPS secrets-file reference,
                      #   git source, reconcile knobs. The `--config` arg.
  secrets.sops.yaml   # SOPS-encrypted PVE + git credentials for THIS
                      #   cluster. The public age recipient is in the
                      #   file's sops: metadata; the PRIVATE age key
                      #   lives OUTSIDE this repo (see OPERATIONS.md).
  resources.yaml      # resource composition
```

A resource file is reconciled by exactly the cluster(s) whose composition
lists it. Bases are shared by reference; cluster-specific files live under
`<kind>/<cluster>/`.

## Secrets (SOPS + age)

PVE & git credentials for a cluster are kept OUT of the global ProxOps
config. Each cluster declares:

- `pve.clusters.<name>.secrets-file` — the path to its SOPS-encrypted file
  (a relative path resolves against the config file's own directory), and
- `pve.clusters.<name>.secrets` — the per-cluster reference block naming
  which SOPS top-level keys in the decrypted file supply which PVE fields
  (`user`, `token-id`, `token`, `password`) and which SOPS key supplies
  the git token (`git.token`).

The SOPS file is encrypted with Mozilla SOPS, age backend; one SOPS file =
one age identity. ProxOps decrypts each SOPS file ONCE at agent
construction into a per-cluster in-memory `Config.SopsResolved` map.
Precedence for a cluster's PVE credentials: SOPS-resolved value >
`PROXOPS_PVE_*` env > global YAML. The SOPS-resolved value is NEVER
serialized into `diff` / `apply` / `status` / `/metrics` / `/status` /
log lines / error text, and ProxOps never writes the decrypted value to
disk (only `sops` itself prints to stdout, which ProxOps reads into a
memory map and discards).

Unencrypted SOPS files (no `sops:` metadata in the committed file) are
refused with `ErrUnencryptedSecrets`. A wrong/absent age identity is
refused with `ErrNoIdentity`/`ErrIdentityMismatch`. A sops binary that
disappears from PATH is refused with `ErrSOPSBinaryMissing`. These are the
"fail closed where credentials are required" guarantees.

## Core guarantees

- **Safe deletion** — the agent only deletes PVE objects tagged `proxops`
  (the ownership tag is added automatically at creation). Anything untagged
  is never touched. Deletions are capped by a per-cycle **prune budget**
  (default 3; `0` means unlimited). An **empty-desired anomaly guard**
  suppresses prunes when a kind has zero manifests but more tagged live
  objects than the budget — probable bad push.
- **Idempotency** — a converged cluster produces a zero-action plan; a second
  cycle does nothing.
- **Fail-closed** — a parse error in the manifest tree aborts the whole cycle
  (last-good tree kept for reads); PVE inventory read failures abort the
  cycle; artifact downloads are skipped when storage presence is unreadable;
  the agent never force-destroys a running object (stop first); a live data
  volume whose pool/size differs from the manifest is surfaced as an
  anomaly, never rewritten (PVE re-creating a volume means data loss).
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
- **Adopt = PVE → YAML (read-only)** — `proxops adopt --cluster <name>`
  reverse-engineers live PVE objects into ProxOps manifests under
  `<kind>/<cluster>/`, without ever calling POST/PUT/DELETE on PVE (the run
  asserts zero PVE writes). Unsupported PVE config is surfaced as explicit
  `gap`/`INCOMPLETE` entries; see [docs/GAPS.md](docs/GAPS.md).

## Layout

```
cmd/proxops/          cobra CLI: run, diff, apply, status, adopt
internal/schema/      desired-state types + PVE wire mapping (Drift/Create)
internal/parse/       manifest tree -> typed Index (validation, id-space
                      dedup, depends-on DAG)
internal/gitx/        git source of truth: HTTPS fetch or local work tree
internal/pveclient/   thin PVE JSON API client (token/ticket auth, retry,
                      circuit breaker, async task waiter) + stateful in-memory
                      mock for tests
internal/plan/        pure planner: desired vs live -> ordered actions; owns
                      the safety model
internal/exec/        serial executor: applies actions, awaits task UPIDs,
                      records status/metrics
internal/statusx/     convergence store (exposed on /status)
internal/server/      /healthz /metrics /status
internal/reconcile/   one cycle: git fetch -> parse -> load live -> plan -> execute
internal/adopt/       PVE -> ProxOps YAML (read-only; cluster-scoped;
                      gap reporting)
internal/composition/ GitOps boundary: discover + validate
                      clusters/*/resources.yaml
internal/app/         composition root; watch loop; dry/apply per cluster
internal/config/      YAML config + cluster-local SOPS credential
                      resolution + env-var bootstrap overlay
internal/secrets/     SOPS age-file decryption, in-memory only: never writes
                      plaintext to disk, never logs values, distinct error
                      classes for missing sops binary / unencrypted file /
                      missing / wrong age identity / malformed document
docs/                 ARCHITECTURE, SCHEMA, OPERATIONS, GAPS
examples/             ready-to-adapt manifest sets
config/               proxops.yaml + systemd unit templates
```

## Quick start

`proxops` supports two credential styles. Choose the one that fits
your GitOps layout:

### Style 1 (recommended): cluster-local SOPS config

Clone your GitOps repo, point ProxOps at the cluster's local config, and
provide your SOPS age key out-of-band (see
[docs/OPERATIONS.md](docs/OPERATIONS.md#per-cluster-sops-secrets) for the
full workflow):

```sh
git clone https://git.example/your/gitops-repo && cd gitops-repo
export SOPS_AGE_KEY_FILE=$HOME/.local/share/proxops/conformance-dev.age
# age key file lives OUTSIDE the git repo (never committed).
proxops diff --config clusters/conformance-dev/config.yaml
proxops apply --config clusters/conformance-dev/config.yaml
proxops status --config clusters/conformance-dev/config.yaml
proxops run --config clusters/conformance-dev/config.yaml
proxops adopt --cluster conformance-dev \
              --config clusters/conformance-dev/config.yaml
```

`clusters/conformance-dev/config.yaml` is a full ProxOps config (log, pve,
git, reconcile, listen, data-dir) scoped to exactly ONE cluster.
`clusters/conformance-dev/secrets.sops.yaml` sits next to it and carries
this cluster's PVE token / git token, encrypted via Mozilla SOPS (age
backend). **Private age key never in the repo.**

### Style 2 (bootstrap): global config + env creds

Use the shared `config/proxops.yaml` + environment variables:

```sh
export PROXOPS_PVE_TOKEN_VALUE="root@pam!proxops=<uuid>"
export PROXOPS_GIT_TOKEN="ghp_..."
proxops diff --config config/proxops.yaml
```

A repository that has not declared a `clusters/<name>/secrets-file` for any
cluster will not require sops to be installed on the host.

`config/proxops.yaml` (style 2) carries **one or more named PVE clusters**
(`pve.clusters.<name>.{base-url,nodes}`); `clusters/<name>/config.yaml`
(style 1) carries exactly one. Every command above processes all named
clusters in deterministic order, except `adopt` which requires one explicit
`--cluster`.

### Credentials & precedence

| Env var | Purpose | Precedence |
|---|---|---|
| `SOPS_AGE_KEY_FILE` | age private key file path for SOPS decryption | consumed by `sops` itself (ProxOps never reads it) |
| `PROXOPS_PVE_TOKEN` | PVE API token UUID | bootstrap; overridden by SOPS in a SOPS cluster |
| `PROXOPS_PVE_TOKEN_VALUE` | fully-composed `user@realm!tokenid=uuid` | bootstrap; suppressed for SOPS clusters |
| `PROXOPS_PVE_PASSWORD` | password for ticket auth | bootstrap; overridden by SOPS in a SOPS cluster |
| `PROXOPS_PVE_USER` | PVE user id (ticket auth convenience) | bootstrap; overridden by SOPS in a SOPS cluster |
| `PROXOPS_GIT_TOKEN` | git HTTPS token | bootstrap; overridden by SOPS in SOPS clusters that reference `git.token` |
| `PROXOPS_CONFIG` | default `--config` path | convenience default only |

A config that declares **no** `pve.clusters.<name>.secrets-file` keeps the
env-over-YAML-over-defaults chain exactly. A config that **does** declare
SOPS for a cluster uses, for that cluster only, the SOPS-decrypted value.
The two are never mixed across clusters: cluster A's SOPS token cannot end
up on cluster B's PVE credentials.

When creating the SOPS file (Style 1), the PVE role needs at least:
`VM.Allocate`, `VM.Configure`, `VM.Create`, `VM.Delete`, `VM.PowerMgmt`,
the `VZ.*` equivalents for containers, `Sys.Audit`, and
`Datastore.Use`/`Datastore.AllocateSpace` on your ISO/template/import
storage. The PVE user (typically `root@pam` or a dedicated `proxops@pam`)
owns the token. The git token (for URL-mode worktrees) should have read
access to the manifest repo.

## Building

```sh
make build      # -> bin/proxops
make test       # go test -count=1 ./...
make cross      # static linux/amd64 binary for a PVE host -> dist/
```

## Documentation

- [docs/SCHEMA.md](docs/SCHEMA.md) — manifest reference for VM / LXC /
  TemplateVM / CTTemplate / ISO / DiskImage (fields, semantics, examples)
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the reconcile pipeline, the
  safety model, and how each layer fits together
- [docs/OPERATIONS.md](docs/OPERATIONS.md) — deployment (systemd),
  observability (/healthz /metrics /status), operations (adopt, prune budget,
  anomaly handling, air-gapped local mode, SOPS workflow)
- [docs/GAPS.md](docs/GAPS.md) — living compatibility backlog: PVE config
  ProxOps does not model, with status/priority/notes per entry
