# CLI reference

`proxops` is a single-binary CLI. The commands below run **from inside** a
ProxOps GitOps repository (repository-first mode) unless stated otherwise.

## Commands

| Command | Writes to PVE? | Behaviour |
|---|---|---|
| `proxops diff` | no | Read-only: render the would-be plan for every cluster (labelled `=== <cluster> ===`), including would-be deletes. |
| `proxops apply` | yes | One converge cycle for every cluster; non-zero exit when any cycle aborts. |
| `proxops apply --dry-run` / `-n` | no | Same as `diff`, through the full pipeline. |
| `proxops apply --diff` | (with apply) | Also render per-field diffs for each planned action. |
| `proxops status` | no | Per-cluster convergence table (runs a read-only cycle first if none has completed). |
| `proxops run` | yes | Daemon: continuously reconcile all clusters every `poll-interval`. |
| `proxops adopt --cluster <name>` | PVE: no / SOPS: optional | Reverse-engineer one cluster's live PVE objects into ProxOps YAML under `<kind>/<cluster>/` (see [Adoption](adopt.md)). **Always requires `--cluster`.** PVE is read-only unless `--adopt-secrets` is set, in which case the cluster's SOPS file is re-encrypted in place to import new `cloud-init.ssh-keys.<name>` entries (see [SOPS & credentials](sops-credentials.md#m132--cloud-init-sops-material)). |
| `proxops --version` | no | Build version. |

Multi-cluster: `diff`/`apply`/`status`/`run` process **every** discovered
cluster in sorted-name order; a failing cluster never blocks the others.
`adopt` operates on exactly one named cluster (unknown names fail closed).

## Global flags (all commands)

| Flag | Env equivalent | Meaning |
|---|---|---|
| `--config <path>` | `PROXOPS_CONFIG` | Load a single configuration file (advanced, pre-M13.1 style): a URL-mode process config (`git.url`) or a cluster-local `clusters/<name>/config.yaml`, optionally merged from the repository's `proxops.yaml` when one is present and the invocation is local. The canonical workflow does not need it. |
| `--git-path <path>` | `PROXOPS_GIT_PATH` | **Explicit repository/work-tree override.** Repository discovery normally walks up from the CWD; this flag pins the tree instead, for development, automation and tests. |
| `--log-level <debug\|info\|warn\|error>` | — | Override the process log level. |
| `--listen <addr>` | — | Override the `/healthz`/`/metrics`/`/status` bind address. |
| `--git-url <url>` | — | URL mode: reconcile a REMOTE repository instead of the local work tree (see [configuration model](reference-config.md#git-source-modes)). |
| `--git-branch <ref>` | — | Branch to reconcile (URL mode). |
| `--git-poll-sec <n>` | — | Poll interval override (watch mode). |
| `--prune-budget <n>` | — | Max deletions per cycle, per cluster (`0` = unlimited). |
| `--pve-user <id>` | — | Bootstrap PVE user override. |
| `--pve-auth <token\|ticket>` | — | Bootstrap auth method override. |
| `--ca-file <path>` | — | PVE cluster CA override. |

## Cluster selection

```sh
proxops adopt --cluster conformance-dev
```

`--cluster <name>` accepts exactly the names discovered from the
repository's `clusters/<name>/config.yaml` files. An unknown name fails
closed:

```
error: --cluster "ghost" is not in pve.clusters (known: [conformance-dev])
```

### `adopt` flags

| Flag | Meaning |
|---|---|
| `--cluster <name>` | The named PVE cluster to adopt from (required). |
| `--adopt-secrets` | M13.2: in addition to generating manifests, import PVE-recoverable cloud-init secret material into the cluster's SOPS file. New `sshkeys` (public keys) become `cloud-init.ssh-keys.adopted-<digest>` entries, written atomically (sidecar + rename) with all existing age recipients preserved. Passwords are NEVER imported (PVE masks them; see [adopt.md § M13.2](adopt.md#m132--cloud-init-secrets-ssh-keys--cipassword-census--adopt-secrets)). Requires the cluster to declare a `secrets-file`. |

## Environment variables (credentials)

ProxOps reads **no** credential from a config file or a flag. Secrets come
from the process environment: `PROXOPS_PVE_USER`, `PROXOPS_PVE_TOKEN`,
`PROXOPS_PVE_TOKEN_VALUE`, `PROXOPS_PVE_PASSWORD` (bootstrap) and
`PROXOPS_GIT_TOKEN` (URL mode), plus `SOPS_AGE_KEY_FILE` for SOPS
decryption (consumed by the `sops` binary, never by ProxOps itself).
See [SOPS & credentials](sops-credentials.md) for precedence and the
fail-closed guarantees.
