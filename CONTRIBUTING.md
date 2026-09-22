# Contributing to ProxOps

Thanks for looking at the project. This file is short on purpose — it says
what a contributor needs to know to make a safe, reviewable change.
Everything else is in `AGENTS.md` (the engineering principles this project
operates under) and the authoritative docs under `docs/`.

- Canonical repository: <https://github.com/Kelcode-Dev/proxops>
- Go module: `github.com/Kelcode-Dev/proxops`
- First public release: `v0.6.0` (the manifest schema remains
  `proxops/v1alpha1` — see `docs/index.md` on the two version streams).

## What ProxOps is

A stateless GitOps reconciler for Proxmox VE: a git repository describes
desired state, ProxOps converges each declared PVE cluster to it over the
PVE API. It is **not** Kubernetes, not Terraform, not Kustomize — do not
introduce those models. See `docs/ARCHITECTURE.md`.

## Getting started

### Prerequisites

- **Go 1.26+** (the toolchain version in `go.mod`)
- **sops** + **age** on PATH — only needed for the credential-resolution and
  SOPS tests; the build itself has no SOPS dependency
- Optional: `golangci-lint` (the lint gate; the repository ships a
  `.golangci.yml`), `mkdocs` (docs gate)

### Build, test, lint

```sh
make build     # -> bin/proxops
go test ./...  # full unit + integration suite (uses an in-memory mock PVE; no real cluster needed)
go test -race ./...
golangci-lint run
mkdocs build --strict   # docs gate
```

All of this runs without a PVE cluster, a SOPS identity, or network access.
Tests that need "real PVE behaviour" run against the disposable
`conformance-dev` cluster described in `AGENTS.md` — CI never does; that is
a manual, opt-in workflow for compatibility investigations.

## Conventions

- **Commits**: Conventional Commit subjects are used (`feat(vm): …`,
  `fix(plan): …`, `test(adopt): …`, `docs(schema): …`, `chore(ci): …`).
  Keep each commit coherent: one behaviour change + its tests + its docs.
- **Tests with behaviour.** A change to schema, planner, reconcile, or PVE
  wire behaviour comes with the regression test that pins it. The mock PVE
  in `internal/pveclient/mock` exists to record observed wire behaviour;
  when you discover a PVE quirk, encode it both in the mock and in a test.
- **Docs stay in sync.** `docs/GAPS.md` is a living compatibility backlog:
  when you discover new PVE wire behaviour (accepted or rejected forms,
  version differences, task semantics), record it there — either as a
  pinned "wire finding" (behaviour ProxOps now handles) or as an open gap.
  The PVE-9.2 findings section is the model for the entry shape.
- **Determinism.** Plans, adoption output, and generated manifests are the
  contract: no map-iteration order, no timestamps, no PVE-assigned values
  leaking into desired state.

## Working with PVE: safety rules

- **Production PVE is read-only by default.** Adoption and investigation
  never mutate. If you are not sure whether the target cluster is a
  disposable `conformance-dev`, verify before touching it.
- **Destructive or lifecycle testing happens on `conformance-dev` only**,
  using the reserved test VMID range documented in `AGENTS.md`. Clean up
  after yourself.
- **Fail closed.** Unknown API behaviour means: investigate, or surface it
  (gap/anomaly/defer) — never write the PVE side with a guessed value.
- **No credentials in fixtures or commits.** Test PVE credentials are
  synthetic (`root@pam!proxops=<obviously-fake-uuid>` style). The only
  encrypted credentials that ship in-repo are the synthetic examples under
  `examples/` (encrypted to a throwaway age identity; see
  `docs/sops-credentials.md`). Never commit a real age private key, a real
  PVE token, or a real password.

## Releases

ProxOps releases are SemVer git tags (`vX.Y.Z`). The release workflow
(`.github/workflows/release.yml`) triggers on a tag push, explicitly
validates the tag as SemVer, runs the full quality gate suite on the exact
tagged tree, builds the `linux/amd64` binary with the tag name embedded,
generates a `SHA256SUMS` manifest, and creates the GitHub Release with the
binary + manifest attached. `go install` at a tag works because the Go
module IS the repository — a tag is a module release.

Contributors do NOT tag releases; maintainers cut a tag from an approved
`main` commit. Prereleases use `vX.Y.Z-rc.N` and are flagged as GitHub
prereleases automatically.

## License

ProxOps is licensed under the **Apache License 2.0** — see
[LICENSE](LICENSE). Contributions submitted to this repository are licensed
under the same terms.

