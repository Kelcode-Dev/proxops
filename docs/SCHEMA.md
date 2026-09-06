# pveconform manifest schema

Manifests are YAML documents, one or more per file; files may be grouped
however you like (`*.yaml` / `*.yml` anywhere under the work tree).

Every manifest has this envelope:

```yaml
apiVersion: proxops/v1alpha1   # only supported value
kind: VM | LXC | CTTemplate | ISO
metadata:
  name: human-readable-name      # required, [a-z0-9](-[a-z0-9])*
  labels:                        # optional, free-form
    role: workers
---
spec: {...}                      # kind-specific (below)
```

`metadata.name` is the pveconform identity within a kind. PVE identity is
additionally pinned by `spec` fields (below) — **the agent never invents PVE
ids**.

### Ownership tag

On create, pveconform adds the PVE tag `pveconform` to the object's `tags`
(VM/LXC/CTTemplate). This is the only gate the agent uses before deleting
anything: live PVE objects WITHOUT this tag are never touched, even if they
match a manifest name.

### depends-on

Any manifest may declare creation-order dependencies via the annotation
`proxops/depends-on`, e.g.:

```yaml
metadata:
  name: app-vm
  annotations:
    proxops/depends-on: "CTT:base-ctt,ISO:app-iso"
```

The value is a comma-separated string of `Kind:name` refs (whitespace
around commas is trimmed; names are `[A-Za-z0-9.-_]`, no spaces), e.g.
`CTT:base-ctt,ISO:app-iso,VM:talos`. The `depends-on`
graph is validated at parse time: unknown targets and cycles abort the whole
cycle (fail-closed). It documents intent and is a precondition for safe
creation order (e.g. a VM cloned from a CTT source: the CTT must exist
first). The MVP planner does not yet topologically schedule dependent
creates — it enforces determinism by node/id within a tier — so use
`depends-on` to *declare* intent and rely on id-pinning plus the per-cycle
re-diff: a dependent object whose source is missing will fail its create and
converge on a later cycle.

---

## `kind: VM`

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.node` | yes | — | Proxmox node hosting the VM. |
| `spec.vmid` | yes | `vmid` | PVE VM ID. Pinned; PVE's per-node id space is one integer pool shared across all object kinds. |
| `spec.state` | no (default `started`) | power | `started` \| `stopped`. Drift reconciles power. |
| `spec.memory` | yes | `memory` (MiB) | e.g. `8GiB`, `512MiB`, `4096`. Must be a whole number of MiB; PVE stores it as an integer MiB count. |
| `spec.cpu.type` | yes | `cpu` | `host`, `kvm64`, `x86-64-v2-Aes`… |
| `spec.cpu.cores` | yes | `sockets`? no — plain cores | PVE `cores` int (sockets=1 in MVP). |
| `spec.disks` | ≥1 | one `sdX`/`scsiX` per entry | Ordered list; see Disk. |
| `spec.networks` | ≥1 | one `netX` per entry | Ordered list; see NIC. |
| `spec.pve-name` | no | `name` | Defaults to `metadata.name`. |
| `spec.pve-description` | no | `description` | PVE description string. |
| `spec.tags` | no | `tags` (comma-separated) | PVE user tags. `pveconform` tag is added automatically. |
| `spec.extra` | no | passthrough | Free-form PVE keys for anything the schema doesn't model yet (e.g. `tablet0`, `vga0`, `boot`). See "escape hatch". |

### Disk

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `storage` | yes | `<slot>=<storage>:<size>` | PVE storage backend id. |
| `size` | yes | size in `<slot>` | Human bytes (`50GiB`); coerced to bytes on the wire. |
| `interface` | no (default `scsi0`) | — | Explicit PVE slot letter for this disk (`scsi0`, `scsi1`, `sata2`, `virtio0`, …). |
| `controller` | no | VM-wide `scsihw` | PVE's `scsihw` value. Only `scsi*` disks honor it; the first disk that declares it wins for the whole VM. Allowed: `virtio-scsi-single` (recommended), `virtio-scsi`, `lsi`, `pv-scsi`. |
| `iothread` | no | `,iothread=1` on the disk | Set to 1 for I/O threads. |

### NIC

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `model` | yes | `netX` | `virtio`, `e1000`, `rtl8139`… |
| `bridge` | yes | `,bridge=BRIDGE` | PVE switch name (e.g. `vmbr0`). |
| `mac` | no | `,MAC=xx:..` | Pinned MAC; leave empty to let PVE assign one on create. |
| `slot` | no | `netN` | NIC slot (default: order of list). |

Drift semantics on disks/NICs:
- **Disks** are matched by **slot letter** (`scsi0`, `sata2`, …), then
  compared on the **owned pieces** — storage pool and size in bytes. PVE
  assigns volume names (`pool:vm-XXX-disk-0`), so those are never treated as
  drift.
- **NICs** match by **model+bridge+MAC** (PVE's `netN` form string).
- Power transitions are independent of config drift: `state: stopped` on a
  running VM triggers a Plan-level `Stop` even when config has converged.

---

## `kind: LXC`

Same envelope. Spec:

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.node` | yes | — | PVE node. |
| `spec.vmid` | yes | `ctid` | PVE CT ID (shared integer pool with VMs). |
| `spec.state` | no (default `started`) | power | `started` \| `stopped`. |
| `spec.memory` | yes | `memory` | MiB (PVE documents this as MB). |
| `spec.swap` | no | `swap` | MiB (PVE documents this as MB). |
| `spec.cpu.cores` | yes | `cores` | PVE `cores` int. |
| `spec.cpu.units` | no | `cpulimit` | PVE CPU limit in percentage points (optional; 0 = unset). |
| `spec.root.storage` | yes | `rootfs` | PVE storage id. |
| `spec.root.size` | yes | `size` in `rootfs` | GiB in PVE's create-time `STORAGE_ID:SIZE_IN_GiB` form. |
| `spec.networks` | ≥1 | one `netN` per entry | Model, bridge, `hwaddr`, `tag`. |
| `spec.os` | no | `ostype` | PVE ostype hint. |
| `spec.arch` | no | `arch` | PVE arch (`i386`, `x86_64`). |
| `spec.features` | no | `features` | Free PVE features string (e.g. `kvm=1,nesting=1`). |
| `spec.pve-name`, `pve-description`, `tags` | no | — | Same as VM. |
| `spec.extra` | no | passthrough | Escape hatch. |

### LXC NIC

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `model` | no | `netN` link | `veth` (default). |
| `bridge` | yes | `,bridge=BRIDGE` | PVE switch. |
| `hwaddr` | no | `,hwaddr=xx:..` | Pinned MAC. |
| `tag` | no | `,tag=NN` | VTEP-style 802.1Q tag. |

---

## `kind: CTTemplate`

A "CTTemplate" is a PVE CT flagged `template=1`. In PVE's listing it appears
with `type=lxc` (the cid space is shared); the pveconform kind is only logical.

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.node` | yes | — | PVE node. |
| `spec.vmid` | yes | `newid` (clone target) | Pinned destination cid. |
| `spec.source` | yes | `newid` is the target; source is the FROM cid | Existing PVE (templated) CT to clone from. Clone is CoW by default; `full: true` forces a full copy. |
| `spec.full` | no | `full` | 1 forces full clone. |
| `spec.pve-name`, `pve-description`, `tags` | no | — | Same as LXC. |

Reconcile semantics:

- **Absent cid** → PVE clone `source → vmid`, then `POST /lxc/{dst}/template`.
- **Present, templated** → no action.
- **Present, NOT templated** → drift. If running, the plan stops it first,
  then re-issues `POST /lxc/{dst}/template`. A re-template of a running CT is
  rejected by PVE, so the stop-first is mandatory.
- **Prune membership** — a live LXC at a `vmid` that is a desired CTT is
  never pruned even though PVE lists it as `type=lxc`.

---

## `kind: ISO`

An ISO lives on a PVE storage backend, not in the object pool. It has no PVE
numeric id; identity is `(spec.node, spec.storage, spec.filename)`.

| Field | Required | Semantics |
|---|---|---|
| `spec.node` | yes | PVE node hosting the storage. |
| `spec.storage` | yes | PVE storage backend id (e.g. `local`, `isos`). |
| `spec.filename` | yes | On-storage filename (typically ends in `.iso`; alnum + `._-`), no path. |
| `spec.url` | yes | Public HTTPS download URL PVE will fetch from. |

Reconcile semantics:

- **Absent** (no file on storage) → `POST /nodes/{n}/storage/{s}/download`
  with `url={spec.url}` and `filename={spec.filename}` PVE-side. The task
  is awaited; the file becomes visible on next `/content/iso` listing poll.
- **Present** → zero actions (an ISO download is a one-shot).
- **Never pruned in MVP** — delete of an ISO manifest removes the *intent*,
  but the file stays on PVE storage. This is deliberate; delete-ISO is a
  post-MVP concern because PVE has no "delete by reference" semantics for
  storage files and we do not want a mis-edit to nuke ISOs in bulk.
- **Fail-closed** — when the storage `/content/iso` listing is unreadable
  (PVE error), pveconform plans NO download; next cycle retries.

---

## Escape hatch: `spec.extra`

Both `VM` and `LXC` have an `extra` map of free string/string PVE keys,
merged into the create and drift-update form values after the structured
fields. This is the extension path for anything the MVP schema doesn't yet
model (e.g. `tablet0`, `vga`, `boot`, PVE flags, `cores` variants,
`onboot`, `protection`). Values are sent verbatim on the wire; pveconform
performs no validation on `extra`.

`extra` is **not** used for the ownership tag — pveconform always controls
`tags` itself.
