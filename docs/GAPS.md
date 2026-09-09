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
