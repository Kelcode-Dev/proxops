# ProxOps

ProxOps is a **repository-first GitOps reconciler for Proxmox VE**. A
ProxOps GitOps repository (plain git + YAML) is the source of truth for one
or more PVE clusters; ProxOps converges each cluster's live state to the
repository's declared state — idempotently, deterministically, and with
explicit safety rails on deletion.

> Not a Kubernetes operator, not Terraform, not Kustomize. One agent
> process, one git repository, N Proxmox clusters. No persistent state
> file: the live PVE cluster **is** the state, re-read every cycle.

## How it works

```
                       ProxOps GitOps repository
                       ┌─────────────────────────────┐
                       │ proxops.yaml   (optional)   │
                       │ clusters/                   │
                       │   dev/config.yaml           │
                       │   dev/secrets.sops.yaml     │
                       │   dev/resources.yaml        │
                       │ vm/base/…  lxc/dev/… …      │
                       └──────────────┬──────────────┘
                                      │ diff (desired)
                                      ▼
              proxops agent (single binary)
         discover → configure → decrypt (SOPS, memory-only)
                                      │
              per cluster (sorted, isolated)
                                      │
                                      ▼
                     PVE API (token / ticket)
        read live state → plan → apply → assert zero-drift
```

See [Architecture](ARCHITECTURE.md) for the full pipeline.

## Quick links

- **[Getting started](getting-started.md)** — from fresh clone to first `diff` / `apply`
- **[Repository layout](repo-layout.md)** — what a ProxOps repository looks like
- **[Resource reference](SCHEMA.md)** — every kind ProxOps manages
- **[Configuration reference](reference-config.md)** — process-wide vs cluster-local vs env
- **[CLI reference](cli.md)** — commands, flags, overrides
- **[SOPS & credentials](sops-credentials.md)** — secret handling, precedence, fail-closed semantics
- **[Adoption](adopt.md)** — bring an existing PVE cluster under ProxOps
- **[Operations](OPERATIONS.md)** — deployment, observability, recovery
- **[Compatibility / Gaps](GAPS.md)** — known limitations

## Canonical workflow

```sh
cd ~/git/proxops-gitops
proxops diff                 # read-only: what PVE must change
proxops apply                # converge
proxops status               # verification
proxops run                  # continuous reconciliation (daemon)
proxops adopt --cluster dev  # bring live objects into the repo (read-only)
```

Repository-first: ProxOps discovers the repository from the current
directory — no `--config`, no work-tree path. The explicit overrides
(`--git-path`, `--config`, env credentials) exist for automation,
multi-host URL mode, and unusual deployments.

## Experimental

ProxOps is **not yet validated for production PVE management**. The
production testing performed to date is deliberately restricted to
**read-only adoption/audit** (`proxops adopt` asserts zero PVE writes) —
that validates observation and reverse-translation fidelity, not
management. All create/update/delete lifecycle validation is against a
disposable development cluster (PVE 9.2).
