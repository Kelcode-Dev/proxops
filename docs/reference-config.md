# Reference: configuration model

How a ProxOps process is configured, and where each concern lives.

## The configuration layers

Repository-first invocation (`cd <repo> && proxops <cmd>`) resolves its
configuration from four layers, lowest to highest precedence:

1. **Built-in defaults** — log level, reconcile cadence, listen address,
   data dir, PVE auth=token.
2. **The repository's optional process-wide `proxops.yaml`** — process
   concerns shared by every invocation: `log`, `reconcile`, `listen`,
   `data-dir`, bootstrap PVE credentials, git URL-mode overrides.
   Absent = defaults apply (convention over configuration).
3. **CLI flags** — set the same fields one-off
   (`--git-url`, `--git-branch`, `--git-poll-sec`, `--prune-budget`,
   `--pve-user`, `--pve-auth`, `--ca-file`, `--listen`, `--log-level`).
4. **Environment variables** — credentials only
   (`PROXOPS_PVE_USER`, `PROXOPS_PVE_TOKEN`, `PROXOPS_PVE_TOKEN_VALUE`,
   `PROXOPS_PVE_PASSWORD`, `PROXOPS_GIT_TOKEN`), plus `SOPS_AGE_KEY_FILE`
   which ProxOps passes to `sops` (it never reads the key itself).

Cluster-local settings live **below** the process layer, on the cluster
side: each `clusters/<name>/config.yaml` declares `pve.clusters.<name>`
— endpoint, node allowlist, SOPS reference — for its own PVE cluster only.

## The optional process-wide `proxops.yaml`

| Section | Field | Meaning |
|---|---|---|
| `log` | `level` | `debug` \| `info` \| `warn` \| `error` (default `info`) |
| `pve` | `auth` | `token` (default) \| `ticket` |
| `pve` | `user`, `token-id`, `token`, `token-value`, `password` | **bootstrap** PVE credentials, used only by clusters that carry no SOPS reference |
| `pve` | `ca-file` | PVE cluster CA (shared; default = system trust store) |
| `pve` | `clusters` | *(leave empty)* — clusters are declared cluster-locally, not here |
| `git` | `url`, `branch`, `token` | URL mode: reconcile a REMOTE git repository (optional override; the local work tree is the default source) |
| `reconcile` | `poll-interval` | watch-mode cadence (default `30s`) |
| `reconcile` | `task-timeout` | max wait per PVE async task (default `30m`) |
| `reconcile` | `prune-budget` | max deletions per cycle **per cluster** (default `3`; `0` = unlimited) |
| `listen` | — | `/healthz`/`/metrics`/`/status` bind (default `127.0.0.1:9494`) |
| `data-dir` | — | git cache + scratch (default `~/.local/share/proxops`) |

## The cluster-local `clusters/<name>/config.yaml`

Declares **exactly one** `pve.clusters` entry, named after its own
directory (the `key == dir` invariant; ProxOps fails closed otherwise):

| Field | Meaning |
|---|---|
| `base-url` | the cluster's single PVE API endpoint (`https://<host>[:8006]`); every PVE node of the cluster is reached through it (node names live only in request paths). Two clusters may never share an endpoint. |
| `nodes` | the cluster's node allowlist = its boundary. A manifest `spec.node` outside it aborts that cluster's cycle before any PVE call; every read, write and prune candidate is scoped to it. Empty = no allowlist check. |
| `secrets-file` | path (relative to this file's directory) to the SOPS/age-encrypted credential file — see [SOPS & credentials](sops-credentials.md). |
| `secrets` | closed reference block: which decrypted SOPS keys carry `pve.{user,token-id,token,password}` and `git.token`. |

A pre-M13.1 cluster-local file may still carry the full app config
(`log`, `git.path: "."`, `reconcile`, `listen`, `data-dir`); those fields
keep working unchanged — only the *normal* layout moved.

## Git source modes

- **Local (default)** — the work tree the process is launched from (or
  `--git-path <path>` / `PROXOPS_GIT_PATH`). ProxOps never fetches and
  never writes it. This is the repository-first model: the operator's
  working tree IS the desired state.
- **URL (advanced)** — a `git.url` in the process config (or
  `--git-url <url>`) makes ProxOps clone/fetch the remote into
  `<data-dir>/git-cache` and reconcile its branch head; `git.token`
  (or the SOPS git token / `PROXOPS_GIT_TOKEN`) authenticates. The
  cluster composition still comes from that tree's
  `clusters/<name>/` — the URL supplies the TREE, not the clusters.

## Environment variables

| Variable | Effect |
|---|---|
| `PROXOPS_PVE_USER` | bootstrap PVE user id |
| `PROXOPS_PVE_TOKEN` | bootstrap PVE API token (token auth) |
| `PROXOPS_PVE_TOKEN_VALUE` | fully-composed `user@realm!tokenid=uuid` credential (wins over the pair) |
| `PROXOPS_PVE_PASSWORD` | PVE password (ticket auth) |
| `PROXOPS_GIT_TOKEN` | git HTTPS token (URL mode) |
| `PROXOPS_CONFIG` | convenience default for `--config` when not passed |
| `PROXOPS_GIT_PATH` | convenience default for `--git-path` |
| `SOPS_AGE_KEY_FILE` (or `SOPS_AGE_KEY`, `AGE_KEY_FILE`) | age identity for SOPS decryption — consumed by the `sops` binary ProxOps invokes; the private key never touches ProxOps's process memory |

Credential precedence per cluster: SOPS-decrypted `> env > bootstrap
YAML`, with the rule that a SOPS cluster never receives the global
`token-value` composed credential (it must match exactly one field
set). See [SOPS & credentials](sops-credentials.md#precedence) for the
full semantics.

## File locations in the tree

| Path | Role |
|---|---|
| `<repo>/proxops.yaml` | optional process-wide config |
| `<repo>/clusters/<name>/config.yaml` | cluster-local config (one per cluster) |
| `<repo>/clusters/<name>/secrets.sops.yaml` | SOPS-encrypted credentials (referenced by `secrets-file`) |
| `<repo>/clusters/<name>/resources.yaml` | the cluster's resource composition |
| `<repo>/<kind>/{base,<name>}/` | resource manifests (`kind ∈ vm, lxc, iso, ctt, templatevm, diskimage, templatect`) |

