# GAPS — PVE configuration pveconform does not fully model, reconcile, or adopt

This is a **living compatibility backlog**: PVE configuration and features that
pveconform encounters (during reconciliation, drift, or `adopt`) but does not
currently fully model/reconcile/adopt. Each entry records enough to prioritise
future implementation.

It is source-controlled project documentation, **not runtime state**: pveconform
never writes to this file. `adopt` surfaces per-run gaps in its report (the
`Gaps` list + `Incomplete` manifests); the operator reviews and (if warranted)
adds or updates entries here.

## Statuses

| Status | Meaning |
|---|---|
| `discovered` | seen in live PVE / adoption output; not yet analysed |
| `investigated` | analysed: pveconform deliberately does not model it today, impact understood |
| `planned` | a milestone will implement it |
| `deliberate` | pveconform will NOT model this (by design) |

## Conventions

- **PVE field**: the wire key as PVE reports it (or the API form-value pveconform
  would need).
- **Priority**: `low` / `medium` / `high` — weighted by data-loss risk, drift
  flapping risk, and adoption-fidelity impact.

## Gaps

### VM: live-only disk properties beyond pool/size/iothread

- **Resource/area**: VM disks (`scsi*`, `virtio*, `sata*`)
- **PVE configuration/API field**: drive property options other than
  `iothread` — e.g. `discard=on`, `ssd=1`, `mbcache=force`, `aio`
- **Status**: discovered
- **Priority**: low
- **What is unsupported**: pveconform's owned disk surface is pool + size +
  iothread + slot + controller. PVE reports additional drive options; adopted
  manifests do not carry them, and `Drift` ignores them.
- **Impact/risk**: none today (the option is not written back, so it neither
  flaps nor mutates). An adopted VM is incomplete; an in-place PVE change of
  `discard=` would not be visible to pveconform.
- **Discovery source**: live conformance-dev `/qemu/{id}/config` reports during
  M8 adopt; `schema.parseDiskInfo`.
- **Notes**: adding a structured `Disk.Options` (map of passthrough tokens) is a
  natural M9 candidate. Until then such options are treated as PVE-owned.

### LXC: `ostemplate` is unrecoverable after create

- **Resource/area**: LXC root source (`spec.template`)
- **PVE configuration/API field**: `ostemplate` (create-only form-value)
- **Status**: investigated
- **Priority**: medium
- **What is unsupported**: PVE does not persist the ostemplate a container was
  booted from; `GET /nodes/{n}/lxc/{id}/config` omits it. pveconform can adopt
  every other owned LXC field, but `spec.template` requires a human.
- **Impact/risk**: adoption output is explicit about this (a Gap + an
  `Incomplete` manifest that `resources.yaml` must not list until the operator
  sets `spec.template`); no silent loss. A re-created LXC (after manual delete)
  would need the template again, which a manifest-less round-trip cannot supply.
- **Discovery source**: M8 live adopt of conformance-dev CT 9200; PVE API
  documentation (`ostemplate` is a /lxc create parameter).
- **Notes**: the operator's review step is part of the M8 contract
  (docs/OPERATIONS.md → "Adopting existing PVE objects").

### LXC: container mount points with pre-existing paths

- **Resource/area**: LXC mount points (`mp*`)
- **PVE configuration/API field**: `mpN=<path>:/<path>` (bind mounts) vs
  `mpN=<pool>:<size>` (allocated volumes)
- **Status**: discovered
- **Priority**: low
- **What is unsupported**: pveconform models only *allocated* LVM/dir volumes
  (`mpN=<pool>:<size>,mp=<mountpoint>`). PVE bind-mounts of a host path
  (`mp0=/mnt/share:/srv/data`) are a different shape not owned by pveconform.
- **Impact/risk**: a bind mount on a live LXC is not adoptable and not
  reconciled; it will not flap or be removed (pveconform never owns it).
- **Discovery source**: PVE pct.conf(5) mpN grammar; M8 schema review.
- **Notes**: a future `LXCBinding` shape (path + read-only + optional
  bind-options) would close this.

### ISO / CTTemplate: download URLs are not part of PVE's state

- **Resource/area**: ISO, CTTemplate (artifact kinds)
- **PVE configuration/API field**: (none — PVE has no concept of
  `spec.url` for an already-present file)
- **Status**: investigated
- **Priority**: low
- **What is unsupported**: pveconform's artifact manifests carry `spec.url`
  (plus optional `spec.checksum`) as the *download source* pveconform uses when
  the file is absent on a node. PVE does not report where a present file came
  from, so `adopt` emits `spec.url: https://placeholder.invalid/...` and marks
  the URL as a Gap.
- **Impact/risk**: an adopted ISO/CTTemplate is *placement-faithful* (presence
  on the observed nodes) but *re-seed-incomplete*: on a fresh node pveconform
  would fail the download until the operator replaces the placeholder URL.
  This is deliberate: inventing a URL from a PVE name would be guesswork.
- **Discovery source**: M8 live adopt; PVE storage content listing shape.
- **Notes**: the operator's review step replaces the placeholder URL before the
  artifact is listed in `clusters/<cluster>/resources.yaml`.

## Deliberately not modelled (out of scope by design)

These are *deliberate* non-goals recorded so future readers know they were
considered:

- **VM replication** (`repl1`, `replN`, and the replication `schedule`): pveconform
  does not model PVE replication. It is a PVE-side DR feature with its own target
  + schedule + failover semantics; pveconform's ownership model (idempotent
  converge-to-git) does not map to it. Discovery source: known PVE API, recorded
  during M8 as a pre-existing gap. Status: `deliberate`.
- **PVE storage resources** (creating/disabling storage backends): the
  P9 scope control keeps pveconform off storage administration; it only reads
  storage (content listing, downloads). Status: `deliberate` for M8+.
- **PVE firewall / netfilter**: per-VM/per-LXC firewalls and the cluster
  firewall are untouched. pveconform models NIC `firewall=1` toggle only.
  Status: `deliberate`.
- **HA resources, pools, users, roles, SDN**: out of the GitOps model. Status:
  `deliberate`.

## M9 (SOPS-backed cluster configuration) — new gaps / deliberate limitations

Entries added during the M9 implementation pass:

- **External `sops` binary dependency (`internal/secrets`)**
  - **Resource/area**: configuration layer
  - **PVE configuration/API field**: n/a
  - **Status**: `deliberate`
  - **What is unsupported**: pveconform shells out to `sops --decrypt`
    (age backend); it does NOT link the SOPS Go module. When a cluster
    references `secrets-file`, `sops` + `age` MUST be on PATH on the host
    pveconform runs on; otherwise `ErrSOPSBinaryMissing` is surfaced at
    agent construction. No KMS or GCP/Azure/Ali/Huawei/PGP backends are
    supported — only `age`.
  - **Discovery source**: M9 design decision (task §16). The Go module
    `github.com/getsops/sops/v3` would pull ~160 transitive deps; the SOPS
    CLI is already mandatory on any host where SOPS-encrypted secrets are
    managed (the operator encrypts with it). Documented in
    `docs/OPERATIONS.md` § "Per-cluster SOPS secrets (M9)".

- **SOPS identity (age private key) lifecycle / rotation**
  - **Resource/area**: credentials
  - **Status**: `planned` (out of M9 scope, task §20 "automatic key rotation"
    and "automatic secret rotation" explicitly excluded)
  - **What is unsupported**: pveconform does NOT rotate SOPS recipients.
    Rotation is an operator workflow: add new recipient to SOPS command
    line, re-encrypt the file, commit both. The private age key has no
    built-in "grace period / revoke" — it is whatever the operator's
    SOPS_AGE_KEY_FILE points at.
  - **Discovery source**: task §16 "do not add automatic key rotation"
    and task §20 (SCOPE CONTROL).

- **No SOPS file watching / hot reload of credentials**
  - **Resource/area**: runtime
  - **Status**: `planned`
  - **What is unsupported**: SOPS decryption happens once at agent
    construction. A rotated PVE token in the SOPS file is not picked up
    without a process restart. Same as M8 env-var credentials: the
    operator's job is to `systemctl restart pveconform` after rotating.
    `poll-interval` re-diffs git + PVE but does NOT re-decrypt secrets.
  - **Discovery source**: M9 design decision; no reason to watch a
    SOPS file during a reconcile cycle when the same behaviour applies
    to env vars.

- **SOPS `git-token` conflict handling**
  - **Resource/area**: git source
  - **Status**: `investigated`
  - **What is unsupported**: If TWO cluster SOPS files both name
    `git.token` keys that resolve to DIFFERENT values, pveconform fails
    closed with "git token conflict" at agent construction. This is
    deliberate: the pveconform git source is single, one worktree, one
    fetch token — two different values would mean "clone two different
    git repos" which is out of scope. When two clusters share the same
    git token value, that value is used for all.
  - **Discovery source**: M9 implementation of `app.EffectiveGitToken`.

- **No per-cluster CA pinning via SOPS**
  - **Resource/area**: TLS
  - **Status**: `deliberate`
  - **What is unsupported**: `pve.ca-file` is a per-pve level setting,
    shared across clusters. To pin a different CA per cluster, the
    operator must use M8 style (a global config that does not reference
    SOPS for that cluster). SOPS does not (today) have a "ca-file"
    credential field. If multi-CA pinning becomes necessary, add a
    `PVECredentialKeys.CAFile` + SOPS `pve-ca-file` entry and resolve it
    alongside the user/token fields.
  - **Discovery source**: M9 design decision; task §20 "PVE Storage
    resources (out of scope)" implies CA pinning is a TLS concern, not
    a PVE API concern, and the M8 global CA is sufficient for most
    deployments.
