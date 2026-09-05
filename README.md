# pveconform — GitOps reconciler for Proxmox VE

`pveconform` continuously reconciles a group of Proxmox VE nodes to the state
declared in a git repository. **Git is the source of truth**: the agent polls
(or is pointed at) a git work tree, parses pveconform manifests, diffs them
against the live PVE API, and converges PVE to the desired state — idempotently,
serially, and with explicit safety rails on deletion.

> Not a Kubernetes operator. One agent process ("cluster-wide") talks to the
> PVE API over HTTPS; each manifest pins the target node with `spec.node`.
> No Terraform, no state files: the live PVE cluster IS the state, re-read
> every cycle.

## MVP resource kinds

| Kind | PVE object | Identity | Notes |
|---|---|---|---|
| `VM` | qemu VM | `(node, vmid)` | pinned `spec.vmid`; disks, networks, CPU, memory, power state |
| `LXC` | container | `(node, vmid)` | pinned `spec.vmid`; root FS, networks, power state |
| `CTTemplate` | template CT | `(node, vmid)` | created by PVE clone from `spec.source` + mark-template; re-templates on drift |
| `ISO` | ISO on storage | `(node, storage, filename)` | PVE-side download from `spec.url`; no PVE id; never pruned in MVP |

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
- **Explicit** — `diff` / `--dry-run` report the full would-be plan,
  including would-be deletes, without writing anything. `adopt` scaffolds
  manifests for existing PVE objects instead of delete-then-create.

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
internal/app/          composition root; watch loop; dry/apply dual reconcilers
internal/config/       YAML config + env-var credential overlay
docs/                  ARCHITECTURE, SCHEMA, OPERATIONS
examples/              ready-to-adapt manifest sets
config/                pveconform.yaml + systemd unit templates
```

## Quick start

```sh
# 1. build
go build -o pveconform ./cmd/pveconform

# 2. inspect drift without touching anything
./pveconform diff --config config/pveconform.yaml

# 3. one convergence cycle (apply)
./pveconform apply --config config/pveconform.yaml

# 4. convergence table
./pveconform status --config config/pveconform.yaml

# 5. daemon mode (continuous watch)
./pveconform run --config config/pveconform.yaml
```

Credentials (PVE API token, PVE password, git token) are read **only from the
environment** — never store them in YAML or pass them on the command line:

| Env var | Purpose |
|---|---|
| `PVECONFORM_PVE_TOKEN` | PVE API token UUID |
| `PVECONFORM_PVE_TOKEN_VALUE` | fully-composed credential `user@realm!tokenid=uuid` |
| `PVECONFORM_PVE_PASSWORD` | password for ticket auth |
| `PVECONFORM_PVE_USER` | PVE user id (ticket auth convenience) |
| `PVECONFORM_GIT_TOKEN` | git HTTPS token |

For a PVE API token: create `root@pam!pveconform` with the `Permissions` role
(`VM.Allocate`, `VM.Configure`, `VM.Create`, `VM.Delete`, `VM.PowerMgmt`,
`VZ.*` equivalents, `Sys.Audit`, and `Datastore.Use`/`Datastore.AllocateSpace`
on your ISO storage for the ISO kind), then set
`PVECONFORM_PVE_TOKEN_VALUE="root@pam!pveconform=<uuid>"`.

## Documentation

- [docs/SCHEMA.md](docs/SCHEMA.md) — manifest reference for VM / LXC /
  CTTemplate / ISO (fields, semantics, examples)
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the reconcile pipeline, the
  safety model, and how each layer fits together
- [docs/OPERATIONS.md](docs/OPERATIONS.md) — deployment (systemd),
  observability (/healthz /metrics /status), operations (adopt, prune budget,
  anomaly handling, air-gapped local mode)
