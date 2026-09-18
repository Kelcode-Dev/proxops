# Architecture

`proxops` is a single-process, multi-cluster GitOps reconciler for
Proxmox VE. One agent reads one git repository and converges **every
configured PVE cluster**:

```
                          proxops agent
        +-----------------------------------------------------------+
        |  discovery (repository-first): CWD -> .git root          |
        |    load optional proxops.yaml + clusters/*/config.yaml   |
        |    -> pve.clusters = {conformance-dev, ...}              |
        |                                                          |
  git   |        +---------+   clusters/<c>/resources.yaml        |
  repo  +------->|   gitx  |   (composition: which files each     |
 (HTTPS or  |    +----+----+    cluster consumes)                |
  local)    |         |                                         |
            |    +-----v-----+                                  |
            |    | composition  |  (repo boundary, fail-closed) |
            |    +------+-------+                              |
            |        |  per cluster (deterministic order)      |
            |   +----v------------------------------------------+
            |   |  for each cluster <name>:                     |
            |   |    parse.BuildClusterIndex                    |
            |   |      clusters/<name>/resources.yaml           |
            |   |      <kind>/{base|<name>}/...yaml             |
            |   |    pveclient[<name>]  (own endpoint +         |
            |   |       own node allowlist)                    |
            |   |    plan.LoadLive + PlanActions                |
            |   |      (live inventory scoped to allowlist)     |
            |   |    executor (apply mode)                      |
            |   +-----------------------------------------------+
            +---------------------------------------------------+
                                   |
                    Proxmox VE API per cluster (8006)
                    /api2/json/...
```

Every cluster cycle is: **fetch -> compose -> parse -> load-live -> plan ->
execute -> report**. Clusters are processed in deterministic (name-sorted)
order. Statelessness is deliberate: restart = full re-diff.

## Multi-cluster composition

The cluster boundary is the invariant that keeps multi-cluster operation
safe:

```
Git composition     ->  clusters/<name>/resources.yaml
named cluster       ->  config pve.clusters.<name> (fail-closed cross-check)
PVE endpoint        ->  that cluster's base-url (one pveclient per cluster)
PVE node            ->  that cluster's node allowlist (fail-closed pre-PVE)
PVE resource        ->  only objects on those nodes are ever read/written/pruned
```

- **Composition is explicit**: a cluster reconciles exactly the resource
  files its `resources.yaml` lists. There is no overlay, inheritance, or
  merge (ProxOps does not use Kustomize and deliberately avoids
  Kustomize semantics).
- **Reusable bases**: `<kind>/base/*.yaml` are shared *by reference*: any
  number of clusters may list the same file. Ownership is by composition,
  not by directory.
- **Cluster-specific resources**: `<kind>/<cluster>/*.yaml` are only
  consumed by the cluster that lists them.
- **Fail-closed identity**: a composition with no `pve.clusters` entry (or a
  configured cluster with no composition) aborts at start -- ProxOps can
  never reconcile an endpoint whose desired set is unknown.
- **No cross-cluster dependencies**: a structured edge
  (`VM.cdrom.iso`, `LXC.template`) or `depends-on` annotation resolves
  inside the referring cluster's index. A target listed only by another
  cluster is a parse error. (A dependency is valid whenever the target is in
  the referencing cluster's index -- always true for a shared base, since
  both clusters list the same file.)
- **Same PVE id / same name across clusters is fine**; same `(node, PVE id)`
  inside one cluster is not (PVE's per-node integer pool is shared by VM+LXC).
- **Prune scoping**: the planner's live inventory and prune candidates are
  filtered by the cluster's node allowlist, so one cluster can never prune
  objects that live on another cluster's nodes. (Config additionally forbids
  two clusters sharing one endpoint -- prune scoping is endpoint-based, and
  merging inventories would be a footgun.)

**Repository-first configuration (M13.1):** the ProxOps GitOps
repository carries all of its own configuration:

```
proxops.yaml                     # OPTIONAL process-wide config
clusters/<cluster>/config.yaml   # ONE pve.clusters entry, named after the dir
clusters/<cluster>/secrets.sops.yaml
clusters/<cluster>/resources.yaml
```

Running `proxops` from inside the repository (the canonical workflow)
does NOT require a `--config`. The CLI:
- walks up from the CWD to the nearest `.git` ancestor (fail-closed if
  none — ProxOps never invents a repository);
- loads the optional `<root>/proxops.yaml` when present (log, reconcile
  knobs, listen, data-dir, bootstrap credentials, git.url URL-mode
  overrides) — defaults when absent;
- loads each `<root>/clusters/<name>/config.yaml` and merges its single
  `pve.clusters.<name>` entry into the process configuration. A
  cluster-local file that declares a different cluster key than its
  directory name is refused (the "key == dir" invariant);
- pins `git.path` to the discovered root (local mode: no fetch, no
  token, the working tree the operator is standing in IS the source of
  truth).

The advanced pre-M13.1 path (`--config <file>`) remains: it loads a
single configuration (URL-mode process config, or a cluster-local
config), and anchors any tree-less cluster-local config to its own
`.git` ancestor. When the `--config` file is a cluster-local config
inside a repository, the repository's optional `proxops.yaml` is merged
*under* it (explicit operator values win; the process file only fills
defaults and adds sibling clusters).

Layer behaviour:
- **config.Load** resolves `secrets-file` relative to the config file's
  own directory, so the same cluster-local config works from any CWD
  (no silent CWD dependence; no implicit magic, no cluster guessing).
- **Repository discovery (M13.1)**: `config.LoadLocal(dir)` walks up
  from `dir` to the nearest `.git` marker (file or directory), and
  fails closed when none exists. `DiscoverGitRoot(dir)` is the
  low-level helper. The `.git`-marker walk-up is
  CWD-anchored, not config-anchored: the same `proxops` binary behaves
  identically no matter what directory it is launched from, as long
  as that directory is inside the same work tree. An explicit
  `--git-path <path>` / `PROXOPS_GIT_PATH` pins the tree for
  automation / tests, skipping discovery.
- **`pve.clusters.<name>.secrets`** is a closed reference block:
  `pve.{user,token-id,token,password}` and `git.token`, each naming one
  top-level key under the decrypted SOPS document's `secrets:` mapping.
  It is NOT a templating language; every referenced key must
  exist non-empty in the SOPS file or ProxOps fails closed.
- **Config.ResolveSOPS** (in `internal/app.New`) decrypts every SOPS
  cluster's file, in memory, at startup, and populates
  `c.SopsResolved[<cluster>]` with the PVE user / token-id / token /
  password + the git token. Decryption happens BEFORE
  `cfg.Validate()`, so the SOPS-aware credential-coverage check sees
  the resolved values (a SOPS-only config passes; a SOPS-declared cluster
  whose SOPS call failed still fails the check).
- **PVEParamsFrom** merges per-cluster SOPS values OVER the global pve
  shared creds. `pve.token-value` (typically the env composed
  credential `user@realm!tokenid=uuid`) is SUPPRESSED for any SOPS
  cluster — the SOPS-resolved trio is authoritative. One PVE user +
  token can serve multiple SOPS clusters, but the SOPS values are
  per-cluster (cluster A cannot accidentally consume cluster B's
  secret configuration).
- **EffectiveGitToken** picks the single git fetch token the shared
  `gitx.Source` uses: SOPS-resolved first; env / YAML otherwise. Two
  different SOPS-resolved git tokens fail closed with a "git token
  conflict" error.
- **`internal/secrets`** owns the SOPS age-identity + binary
  call. It invokes the external `sops` (age backend) binary via
  `exec.LookPath("sops")` in a 30s-budgeted `exec.CommandContext`. The
  command runs with a copy of the operator's `os.Environ()` so
  `SOPS_AGE_KEY_FILE` / `SOPS_AGE_KEY` / `AGE_KEY_FILE` reach sops' age
  backend untouched. ProxOps never sets or inspects those variables
  itself. The sops child process's STDOUT (the decrypted
  YAML) goes through a JSON parse into a
  `map[string]string` — the in-memory `Config.SopsResolved` map is
  populated from that and is tagged `json:"-" yaml:"-"` so no
  serialisation surface can emit it. Errors from sops are classified
  into a fixed sentinel set: `ErrSOPSBinaryMissing`,
  `ErrUnencryptedSecrets`, `ErrNoIdentity`, `ErrIdentityMismatch`,
  `ErrMalformedDocument` + a redacted exit-code + hint for anything
  else. sops's OWN stderr is NOT re-emitted verbatim (only a <= 120-char,
  token-redacted hint) — defense against a sops version / hostile
  document leaking secret material into ProxOps's error text
  (error messages identify the problem without printing
  secret contents).
- **The gitx source** is built AFTER SOPS resolution, so its
  `gitx.Options.Token` carries the SOPS-resolved git token
  (when one was referenced).

A `pve.clusters.<name>` entry is `base-url` + `nodes` (+ optional
`secrets-file` / `secrets`). A cluster that sets `secrets-file` adds the
SOPS shape but does NOT re-declare the shared `pve.user` /
`pve.token-id` / `pve.token` in the SOPS reference — the SOPS document
supplies them via the `secrets:` block. The shared `pve.*` fields (the
optional process-wide `proxops.yaml`, the env overlay, or a global
bootstrap config) still act as bootstrap credentials for any SOPS-less
cluster in the same invocation.

## The layers

### gitx -- source of truth

One git source (URL or local work tree) serves **all** clusters: the
composition is cluster-scoped at parse time, not at fetch time. URL mode:
`go-git` clone into `data-dir`, `fetch` on each tick; fetch failures are
*advisory* -- the agent keeps the last-good tree and flags `desired-stale`
(fail-open on stale reads, never on garbage). Local mode: the work tree is
authoritative; the agent never writes it.

### composition -- the GitOps boundary

Read-only. Discovers `clusters/*/resources.yaml`, resolves + validates every
resource path (exists, inside repo root, `.yaml`, no duplicates), enforces
the repo shape (rejects legacy `<kind>/foo.yaml` placed directly under a kind
root), and cross-checks compositions against configured clusters (both
directions fail closed).

### parse -- per-cluster desired index

`parse.BuildClusterIndex(root, cluster, configuredClusters)` builds the
Index for exactly that cluster's composed files:

- routing to typed resources (VM / LXC / CTTemplate / ISO / TemplateVM /
  DiskImage / TemplateCT) + `Validate()`;
- duplicate `(kind, name)` refs -> error;
- duplicate PVE id on a node **within the cluster** -> error;
- structured edges (`VM -> ISO` via `hardware.cdrom.iso`,
  `VM -> DiskImage` via `spec.disks[].image`,
  `VM -> TemplateVM` via `spec.clone`,
  `LXC -> CTTemplate` via `spec.template`,
  `TemplateCT -> CTTemplate` via `spec.template`) + `depends-on` annotation
  edges;
- unknown edge targets / cycles fail the cluster's cycle (no cross-cluster
  resolution, by construction).

`parse.BuildIndex` (whole-tree walk) still exists for the examples check and
test tooling; the runtime always uses the cluster-scoped builders.

### pveclient -- thin PVE JSON API client

No third-party PVE library. **One endpoint per cluster**: each configured
cluster gets its own `pveclient.Client` pinned to that cluster's
`base-url`. PVE exposes its complete JSON API on every node, so one endpoint
reaches every node/object of *that* cluster; the PVE node name lives only in
the request path. Auth is shared across clusters (token or ticket,
credentials from the environment).

Adoption additionally uses read-only listing surfaces: `GET
/cluster/nodes`, `GET /nodes/{n}/qemu`, `GET /nodes/{n}/lxc`,
`GET /nodes/{n}/storage`, `GET /nodes/{n}/storage/{s}/content`. The client
counts every POST/PUT/DELETE (`WritesPerformed`) so `adopt` can assert the
read-only invariant (zero writes).

### plan -- pure planner + safety model (cluster-scoped)

`plan.LoadLive(ctx, client, desired, allowedNodes)` snapshots PVE:
- cluster listing -> per-object config + power, **only for nodes in
  `allowedNodes`**;
- for every desired artifact, probes storage content per declared
  `(node, storage, content)` and records `{present: bool}`.

`plan.PlanActions(ctx, desired, live, opts)` is pure (never writes).
`opts.NodeAllowlist` restricts **prune candidates** to the cluster's nodes
-- a tagged live object on a node outside the allowlist is invisible to this
cluster's planner (neither a prune candidate nor a skip entry). The rest of
the safety model is unchanged:

- **Ownership gate**: `proxops` tag required for any delete.
- **Prune budget**: max N *per cluster* per cycle (default 3).
- **Empty-desired anomaly guard**: 0 desired of a kind + more tagged live
  objects than the budget -> prunes for that kind suppressed,
  `Plan.Anomaly` set.
- **Conservative artifacts**: ISO / CTTemplate / DiskImage are never pruned.
- **Live-only disk anomalies**: VM/LXC slots present on PVE but not in the
  manifest are surfaced as non-destructive anomalies and never auto-removed.
- **Data-loss guards**: pool/size drift on a live data volume is anomaly +
  stop, never an auto-rewrite.
- **Deterministic + topological order**: tier/level/what/node/id/name;
  dependants create after prerequisites; prunes reverse-ordered;
  in-cycle prerequisite deferral via `Action.Deps`.

### exec -- serial executor (per cluster)

One executor per cluster (bound to that cluster's PVE client and status tag).
Actions run in plan order; stop-required flows: Stop -> Update -> Start
(restored only when `DesiredPower=started`). Failures never abort the cycle;
the next cycle re-diffs. Every `statusx.Object` record is tagged with the
cluster.

**Clone-backed VM creates** (`VM.spec.clone`, M12) are a two-write sequence
inside one `Create` action: `POST /qemu/{template-vmid}/clone` (`full=1`,
`newid`, `name`) followed by a `/config` write that applies the VM's own
values and `delete=`s the inherited cloud-init identity keys the manifest
does not declare (PVE's full clone copies the template's whole `/config`,
hostname/IP/keys included — see GAPS.md). The clone source vmid comes from
the resolved TemplateVM, never from the referencing manifest. The cloud-init
DRIVE is written only when the clone's live slot is empty (re-sending it over
the clone's inherited volume fails the task). A failed clone leaves the
action Failed — never reported as converged — and the next cycle re-diffs the
half-configured clone through the normal Drift path (which also clears any
leaked identity); ProxOps never re-clones over a live VM.

**Template creates** (`kind: TemplateVM` M11, `kind: TemplateCT` M13) are a
two-write sequence inside one `Create` action: the object `POST` (`start=0`)
followed by the mark endpoint (`POST /qemu/{id}/template` or
`POST /lxc/{id}/template`). The qemu mark returns a task UPID (drained
serially); the LXC mark is synchronous with a NULL data response (no UPID —
probe-verified PVE 9.2.2), which the executor treats as immediate success. A
failed mark leaves the object created-but-unmarked, surfaced as drift next
cycle (the planner's `MarkTemplate` action fires when a desired template
matches a live non-template object at the same id). Neither kind has an
`/untemplate` endpoint (501), so a desired `kind: VM`/`kind: LXC` against a
live PVE-side template is a non-destructive anomaly, never a kind-flip write.

### adopt -- PVE -> ProxOps YAML (read-only)

`proxops adopt --cluster <name>`:

1. Resolves the cluster in `pve.clusters`; uses that cluster's endpoint and
   node allowlist (unknown cluster -> error; the command does not pick a
   cluster for you).
2. Enumerates nodes (allowlist; only when the allowlist is empty does it
   query `/cluster/nodes`).
3. Reverse-engineers every VM and LXC on those nodes from their PVE
   `/config` report into a ProxOps manifest, every PVE-side qemu template
   (`template=1` on a `type=qm` object) into a `kind: TemplateVM` manifest,
   every PVE-side container template (`template=1` on a `type=lxc` object,
   M13) into a `kind: TemplateCT` manifest, and every
   `iso`/`vztmpl` storage artifact into an ISO/CTTemplate manifest (same
   filename on several allowed nodes -> one manifest with `spec.nodes`
   covering them). `import` content (DiskImage) is not adopted — see
   GAPS.md.
4. Writes each manifest under `<kind>/<cluster>/` (cluster-specific output by
   design -- the object was observed on that cluster; promoting something to
   `<kind>/base/` is a deliberate human refactoring decision, never done by
   adopt).
5. **Gap reporting**: every PVE `/config` key ProxOps does not model (and
   that is not PVE bookkeeping: `digest`, `meta`, `vmgenid`, `smbios1`, ...)
   is listed as a `Gap`. PVE-assigned MACs are not pinned (round-trip
   contract: PVE owns random MACs); things PVE does not report back
   (`ostemplate`, artifact download URLs) are explicit gaps.
6. **INCOMPLETE manifests**: when a required value cannot be recovered
   (e.g. `ostemplate`), the manifest is still written (so the operator sees
   its shape) but named in the summary's `INCOMPLETE` list; the operator
   must complete it before listing it in `resources.yaml`. `adopt` never
   modifies `clusters/<cluster>/resources.yaml` -- the summary prints the
   exact lines to add.
7. **Read-only assertion**: the PVE client's write counter must be
   unchanged at the end; adoption never creates/updates/deletes/tags
   anything in PVE.

The generated YAML is suitable for a **round-trip**: PVE -> adopt -> YAML ->
`resources.yaml` -> `proxops diff` -> zero unexpected drift for anything
ProxOps models. Live-only data disks are captured into `spec.disks` so the
round-trip represents them; PVE-owned cloud-init volumes on non-IDE slots
are deliberately EXCLUDED from `spec.disks` and reported as gaps instead
(ProxOps cannot recreate such a slot, so claiming it would be a lie the
planner could not honour).

### statusx + server

`statusx.Store` is an in-memory convergence table keyed
`cluster|kind|name` so every record carries its cluster. `GET /status` shows
the multi-cluster object table; `GET /healthz` is ready once a full
(all-clusters) cycle has run; `GET /metrics` is process-wide.

### app / CLI

`internal/app` builds the agent: one git source, one status store, one
pveclient + reconciler pair **per configured cluster** (deterministic
sorted order), one HTTP server. The M13.1 CLI is repository-first:

- `proxops run` -- daemon: every tick, all clusters in order; a cluster's
  abort does not block the others.
- `proxops diff` -- read-only plan per cluster, labelled
  `=== <cluster> ===`.
- `proxops apply` -- one converge cycle per cluster; non-zero exit when
  any cluster aborts.
- `proxops status` -- per-cluster convergence table.
- `proxops adopt --cluster <name>` -- PVE -> YAML for exactly one named
  cluster (required flag; no implicit default).

Repository-first means each command discovers the tree from the CWD:
walks up to the nearest `.git` marker, loads the optional
`<root>/proxops.yaml`, loads every `clusters/<name>/config.yaml`, and
pins `git.path` to the discovered root. The advanced overrides live on
the same command (`--config`, `--git-path`, plus the `PROXOPS_CONFIG`
/ `PROXOPS_GIT_PATH` env vars).

`buildAgent` in `cmd/proxops/main.go` is the only place the CLI and the
app layer meet: it picks `config.LoadLocal` (repository-first) or
`config.LoadCluster` / `config.Load` (advanced / URL mode) depending on
the invocation, applies flag + env overlays, and hands the result to
`app.New`. `app.New` runs the `ResolveSOPS -> Validate -> gitx.New ->
pveclient.New -> reconciler.New` chain for every cluster.

All configuration and credential semantics are documented in
[Configuration reference](reference-config.md) and
[SOPS & credentials](sops-credentials.md).

## Failure modes -- what happens, by design

| Failure | Behavior |
|---|---|
| No `.git` ancestor from the CWD (repository-first mode) | CLI fails closed before any PVE call ("no ProxOps GitOps repository found") |
| Cluster-local config's key does not match its directory name | Discovery fails closed before any PVE call |
| Malformed `proxops.yaml` (or `clusters/<name>/config.yaml`) | Discovery fails closed; the invocation does not run |
| git fetch fails (URL mode, first) | Cluster cycles abort; no PVE reads |
| git fetch fails (URL mode, subsequent) | Keep last-good tree; cycle `stale` |
| Composition invalid (legacy layout, missing ref, unknown cluster) | Parse fails **closed** for the affected cluster(s); no PVE writes |
| Parse fails on a manifest | That cluster's cycle aborts; other clusters unaffected |
| PVE inventory read fails (one cluster) | That cluster's cycle aborts |
| PVE create/update/task fails | Action failed (statused); next cycle re-diffs |
| 0 desired + many tagged live | Prunes suppressed for that kind on that cluster |
| Unknown PVE config on adopt | Gap report + INCOMPLETE manifest; never silently dropped |

## Extending the set of kinds

1. `internal/schema/<kind>.go` -- struct + `Resource` impl.
2. `internal/parse/parse.go` -- `switch kind` routing + `metadataOf`.
3. `internal/composition` -- kind roots (repo-shape validation).
4. `internal/pveclient/` -- wire primitives; `internal/pveclient/mock` --
   routes + accessors (kind-agnostic PVE id space if PVE lists it alongside
   LXCs).
5. `internal/plan/plan.go` -- LoadLive + PlanActions pass A/B + any
   kind-specific safety.
6. `internal/exec/exec.go` -- `apply()` switch.
7. `internal/adopt/` -- reverse-engineering + gap detection.
8. `docs/SCHEMA.md` + `examples/` + e2e tests.
