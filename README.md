# ProxOps — repository-first GitOps reconciler for Proxmox VE

[![GitHub Release](https://img.shields.io/github/v/release/Kelcode-Dev/proxops)](https://github.com/Kelcode-Dev/proxops/releases/latest)
[![CI](https://github.com/Kelcode-Dev/proxops/actions/workflows/ci.yml/badge.svg)](https://github.com/Kelcode-Dev/proxops/actions/workflows/ci.yml)
[![Docs](https://github.com/Kelcode-Dev/proxops/actions/workflows/pages.yml/badge.svg)](https://github.com/Kelcode-Dev/proxops/actions/workflows/pages.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/Kelcode-Dev/proxops)](https://github.com/Kelcode-Dev/proxops/blob/main/go.mod)
[![License](https://img.shields.io/github/license/Kelcode-Dev/proxops)](https://github.com/Kelcode-Dev/proxops/blob/main/LICENSE)

> ## ⚠️ Pre-1.0 — experimental. Read `docs/GAPS.md` before using in anger
>
> - **Maturity.** ProxOps is a pre-1.0, actively-developed tool. It is not
>   yet a drop-in for unattended production infrastructure management.
> - **Versioning.** The application ships versioned releases (the first
>   public release is `v0.6.0`), but the **manifest schema is still
>   alpha** — manifests use `apiVersion: proxops/v1alpha1` and may change
>   until the schema is declared stable. Supported features are tested and
>   dogfooded; unsupported PVE surface is documented in
>   [docs/GAPS.md](docs/GAPS.md).
> - **What has been tested.** Destructive create/update/delete lifecycle
>   testing has been performed against a disposable PVE cluster
>   (`conformance-dev`, PVE 9.2). Read-only adoption/audit
>   (`proxops adopt`, which asserts zero PVE writes) has additionally been
>   exercised against real PVE estates. That is **observation** coverage,
>   not **lifecycle** coverage.
> - **Review before you apply.** Always run `proxops diff` first and read the
>   plan before `proxops apply`. Convergence is only reached when a second
>   `proxops diff` reports zero actions.
> - **No silent guessing.** Live PVE state ProxOps cannot model or recover is
>   surfaced as a `gap`, `INCOMPLETE`, or anomaly — it is never silently
>   normalised, dropped, or "completed" for you.
> - **Ownership is enforced.** Only PVE objects ProxOps created (carrying its
>   `proxops` ownership tag) are ever deleted; prunings are bounded per
>   cycle and per cluster.
>
> The full compatibility and known-limitations list lives in
> [docs/GAPS.md](docs/GAPS.md) — keep that page in front of you until the
> project reaches 1.0.

ProxOps converges one or more Proxmox VE clusters to the state declared in
a **git repository — the ProxOps GitOps repository is the unit of
operation**. Git is the desired state, live PVE is the observed state, and
reconciliation is deterministic, idempotent, and stateless (no Terraform
file; the PVE cluster itself is re-read every cycle).

One agent process reconciles every cluster the repository declares; each
cluster has its own PVE endpoint, node allowlist, and credentials —
isolated, never cross-pruned.

## What it manages

| Kind | PVE object | Notes |
| ---- | ---------- | ----- |
| VM | qemu VM | Disks (incl. image-seeded from a `DiskImage`), networks, CPU/memory, hardware (bios/EFI/Secure Boot/TPM/cloud-init drive), options, cloud-init data, power state |
| TemplateVM | qemu VM marked `template=1` | Identical schema to VM; provision VMs from it with `VM.spec.clone` (PVE full-clone + own config) |
| LXC | PVE container | Root FS, allocated mount points, host-path bind mounts, networks (incl. static ip/gw), DNS, options |
| TemplateCT | LXC marked `template=1` | Identical schema to LXC, promoted via `POST /lxc/{id}/template` |
| ISO | installer image on `iso` storage | Downloadable artifact; VMs attach it as CD/DVD |
| CTTemplate | `vztmpl` archive on `vztmpl` storage | Downloadable artifact; LXCs bootstrap from it |
| DiskImage | cloud disk image on `import` storage | Downloadable artifact; VM disks seed from it (`import-from`) |

Artifacts (ISO / CTTemplate / DiskImage) have **no PVE id** and are
**never pruned** — removing a manifest stops re-downloads, not the PVE
file.

## The repository-first workflow

Everything a cluster needs lives in the repository — no external config
file that points at the repository itself:

```sh
proxops.yaml                     # OPTIONAL process-wide config
clusters/<cluster>/
  config.yaml                    # the cluster's PVE endpoint, node allowlist,
                                 #   SOPS credentials reference
  secrets.sops.yaml              # SOPS/age-encrypted PVE + git credentials
  resources.yaml                 # which resource files this cluster consumes
vm/ lxc/ iso/ ctt/               # resource definitions
templatevm/ templatect/ diskimage/
```

From inside the repository, run ProxOps with **no flags**:

```sh
cd ~/git/proxops-gitops

proxops diff          # read-only: what PVE must change, per cluster
proxops apply         # converge
proxops status        # per-cluster convergence table
proxops run           # daemon: reconcile continuously
proxops adopt --cluster <name>   # PVE -> YAML, read-only
```

ProxOps discovers the repository from the current directory (nearest `.git`
ancestor → load the optional `proxops.yaml` → load every
`clusters/<name>/config.yaml` → reconcile that work tree in place, no
fetch, no state file). Credentials are SOPS-encrypted in the repository;
the private age identity lives outside it.

A complete, copy-able example repository (every kind, one cluster,
synthetic secrets) ships in [`examples/`](examples/README.md).

## Install / build

```sh
# from a released tag (binaries also attach to each GitHub Release):
go install github.com/Kelcode-Dev/proxops/cmd/proxops@latest

# or build from source:
git clone https://github.com/Kelcode-Dev/proxops && cd proxops
make build          # -> bin/proxops
```

Versioned releases follow SemVer tags (`vX.Y.Z`); `@main` installs the
current main-branch build instead. For PVE hosts without a Go toolchain,
each GitHub Release also carries static `proxops_vX.Y.Z_<os>_<arch>`
binaries (`make cross` produces both Linux amd64 and arm64 binaries). See
`docs/getting-started.md`.

SOPS-backed credentials additionally require `sops` (+ `age`) on PATH.

## First `diff` and `apply`

The fastest path (one cluster, no SOPS) is a git repo containing
`clusters/<name>/config.yaml` (endpoint + node allowlist) and
`clusters/<name>/resources.yaml` — then:

```sh
export PROXOPS_PVE_TOKEN_VALUE="root@pam!proxops=<uuid>"   # token credential
cd <your-gitops-repo>
proxops diff        # inspect the plan
proxops apply       # converge
proxops status      # verify: zero drift on a second diff means converged
```

For the SOPS-backed workflow (recommended; encrypted credentials in the
repo), see [Getting started](docs/getting-started.md).

## Safety guarantees

- **Safe deletion.** Only PVE objects carrying the `proxops` ownership tag
  (added automatically on create) are ever deleted. Prunings are capped
  per cycle (`--prune-budget`, default 3), and per cluster. An
  "empty-desired" anomaly guard suppresses prunes when a kind's manifest
  set vanishes while its tagged live objects remain — a probable bad push.
- **Ownership boundaries and cluster isolation.** Each cluster has its own
  endpoint + node allowlist; a resource is reconciled only against the
  cluster whose composition lists it. Two clusters may not share an
  endpoint; a node outside the allowlist aborts the cluster's cycle before
  any PVE call.
- **Idempotent + deterministic.** A converged cluster yields a zero-action
  plan; plans are total-ordered and byte-stable across runs.
- **Fail-closed.** Malformed or ambiguous repositories/configs reject before
  a PVE call; unmodelled values are surfaced as `gap`/`INCOMPLETE`/anomaly,
  never silently dropped or normalised; disk pool/size drift on a live
  volume is an anomaly, not a write.
- **No secrets in output.** SOPS/age encrypted credentials live in the
  work tree, private identities live outside it; decrypted values never
  reach logs, `diff`, `status`, generated manifests, or errors.
- **Read-only `adopt`.** `proxops adopt` performs GETs only and asserts a
  zero PVE write counter — safe on production as an audit tool, never a
  management tool.

## Where to go next

Documentation is generated from source-controlled Markdown in
[`docs/`](docs) and published to
([GitHub Pages](https://kelcode-dev.github.io/proxops/), built by
[`.github/workflows/pages.yml`](https://github.com/Kelcode-Dev/proxops/blob/main/.github/workflows/pages.yml)):

- **[docs/getting-started.md](docs/getting-started.md)** — first diff & apply, SOPS setup
- **[docs/SCHEMA.md](docs/SCHEMA.md)** — resource reference / cross-cutting guarantees
- **[docs/repo-layout.md](docs/repo-layout.md)** — the ProxOps repository shape, composition & dependencies
- **[docs/reference-config.md](docs/reference-config.md)** — configuration model (process vs cluster-local vs env vs flags)
- **[docs/cli.md](docs/cli.md)** — CLI reference (commands, flags, overrides)
- **[docs/sops-credentials.md](docs/sops-credentials.md)** — SOPS / age credentials, precedence, fail-closed semantics
- **[docs/OPERATIONS.md](docs/OPERATIONS.md)** — deployment, observability, recovery, runbook
- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — pipeline and safety model
- **[docs/GAPS.md](docs/GAPS.md)** — compatibility & known gaps
