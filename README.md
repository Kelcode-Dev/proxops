# ProxOps — repository-first GitOps reconciler for Proxmox VE

> ## ⚠️ Experimental — Not for Production Use
>
> ProxOps is currently under active development and has **not been validated
> for production Proxmox management**. Do not use ProxOps to manage
> production Proxmox systems at this stage.
>
> The production testing the project *has* performed is deliberately
> restricted to **read-only adoption/audit** (`proxops adopt`, which
> asserts zero PVE writes). That validates observation and
> reverse-translation fidelity only — it does **not** constitute
> production **management** validation. All create/update/delete
> lifecycle validation has been done against a disposable development
> cluster (`conformance-dev`, PVE 9.2), not production.

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
|---|---|---|
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

```
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
go install github.com/GizzmoShifu/proxmox-operator/cmd/proxops@latest
# or from a checkout:
make build          # -> bin/proxops
make cross          # static linux/amd64 binary for a PVE host -> dist/
```

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
[GitHub Pages](https://github.com/operatorshifu/proxmox-operator/tree/main/.github/workflows/pages.yml):

- **[docs/getting-started.md](docs/getting-started.md)** — first diff & apply, SOPS setup
- **[docs/SCHEMA.md](docs/SCHEMA.md)** — resource reference / cross-cutting guarantees
- **[docs/repo-layout.md](docs/repo-layout.md)** — the ProxOps repository shape, composition & dependencies
- **[docs/reference-config.md](docs/reference-config.md)** — configuration model (process vs cluster-local vs env vs flags)
- **[docs/cli.md](docs/cli.md)** — CLI reference (commands, flags, overrides)
- **[docs/sops-credentials.md](docs/sops-credentials.md)** — SOPS / age credentials, precedence, fail-closed semantics
- **[docs/OPERATIONS.md](docs/OPERATIONS.md)** — deployment, observability, recovery, runbook
- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — pipeline and safety model
- **[docs/GAPS.md](docs/GAPS.md)** — compatibility & known gaps
