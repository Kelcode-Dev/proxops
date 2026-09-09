# pveconform manifest schema

Manifests are YAML documents, one or more per file.

## M8 repository layout

Since M8 the manifest tree is organised around the **GitOps composition
model** (see ARCHITECTURE.md → "Multi-cluster composition"):

```
clusters/<cluster>/resources.yaml   # what <cluster> consumes (explicit list)
<kind>/{base|<cluster>}/....yaml    # resource definitions, kind in vm, lxc, iso, ctt
```

A manifest file is only reconciled if some cluster's
`clusters/<cluster>/resources.yaml` lists it; the cluster boundary is the
safety model. Resources placed directly under a kind root (the pre-M8
`vm/foo.yaml` layout) are a **validation error** — pveconform never invents
a default cluster.

Every manifest has this envelope:

```yaml
apiVersion: proxops/v1alpha1   # only supported value
kind: VM | LXC | CTTemplate | ISO
metadata:
  name: human-readable-name      # required, [a-z0-9](-[a-z0-9])*
  labels:                        # optional, free-form
    role: workers
  annotations:                   # optional, proxops/* hints
---
spec: {...}                      # kind-specific (below)
```

`metadata.name` is the pveconform identity within a kind. PVE identity is
additionally pinned by `spec` fields (below) — **the agent never invents PVE
ids** for objects that have one. The two kinds that have **no** PVE numeric id
(ISO and CTTemplate, both *storage artifacts*) are identified on PVE by
`(node, storage, filename)` and on pveconform by `metadata.name`.

---

## Dependencies (inferred + explicit)

pveconform builds a **dependency graph** from two sources and schedules
creates topologically so prerequisites finish **before** their dependants:

1. **Structured references (inferred, preferred).** The schema knows about
   two cross-kind edges:
   - `VM.spec.hardware.cdrom.iso` → an `ISO`
   - `LXC.spec.template` → a `CTTemplate`

   These are detected automatically — you do **not** have to repeat them with
   an annotation. The planner downloads the ISO / container template before
   it creates the VM / LXC that references it.
2. **`depends-on` annotation (escape hatch).** For relationships that cannot
   be expressed in a structured field:

   ```yaml
   metadata:
     name: app-vm
     annotations:
       proxops/depends-on: "ISO:app-iso,VM:other-vm"
   ```

   The value is a comma-separated list of `Kind:name` refs.

Behaviour:

- **Unknown references fail closed** — a `cdrom.iso`, `template`, or
  `depends-on` target that is not defined *in the cluster's own
  composition* aborts that cluster's cycle. There is **no cross-cluster
  dependency resolution**: a VM in cluster `A` referencing an ISO that
  only cluster `B` lists is a parse error for `A`. (A dependency becomes
  valid only when the target is in the referencing cluster's index — which
  is always the case for a shared base, since both clusters list the same
  file.)
- **Cycles fail closed** — a dependency cycle aborts the whole cycle.
- **Deterministic order** — creates are ordered by topological *level*
  (artifacts at level 0, the VM/LXC that reference them at level 1, and so
  on), then node + id, so plans are stable across cycles.
- **A failed prerequisite defers dependants in-cycle** — if a prerequisite's
  PVE download fails this cycle, dependants are not attempted; they are
  retried on the next cycle (reconciliation is re-derived from live state each
  cycle; there is no persistent state).
- **Prune order respects reverse dependencies** — dependants are deleted
  before prerequisites, so an ISO/template is never pruned out from under a
  live VM/LXC that references it.

---

## `kind: VM`

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.node` | yes | — | Proxmox node hosting the VM. **Node identity, never a DNS host.** |
| `spec.vmid` | yes | `vmid` | PVE VM ID. Pinned; PVE's per-node id space is one integer pool shared across VMs and LXCs. |
| `spec.state` | no (default `started`) | power | `started` \| `stopped`. Drift reconciles power. |
| `spec.memory` | yes | `memory` (MiB) | e.g. `8GiB`, `512MiB`. PVE stores an integer MiB count. |
| `spec.cpu.type` | yes | `cpu` | `host`, `kvm64`, `x86-64-v2-Aes`… |
| `spec.cpu.cores` | yes | `cores` | PVE `cores` int. |
| `spec.disks` | ≥1 | one `scsiX`/`...` per entry | Ordered; see Disk. |
| `spec.networks` | ≥1 | one `netX` per entry | Ordered; see NIC. |
| `spec.hardware` | no | see Hardware | First-class hardware (bios, machine, display, cdrom/ISO, efi, cloud-init, serial, tpm). |
| `spec.options` | no | see Options | VM Options panel (onboot, protection, agent, …). |
| `spec.pve-name` | no | `name` | Defaults to `metadata.name`. |
| `spec.pve-description` | no | `description` | PVE description string. |
| `spec.tags` | no | `tags` | PVE user tags; `pveconform` is added automatically. |
| `spec.extra` | no | passthrough | Free-form PVE keys for unmodeled properties. |

### Disk

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `storage` | yes | `<slot>=<storage>:<GiB>` | PVE storage backend id. |
| `size` | yes | size in `<slot>` | Human size (`50GiB`); the number after the pool is **GiB** on the PVE 9.2 wire. |
| `interface` | no (default `scsi0`) | — | Explicit PVE slot (`scsi0`, `scsi1`, `sata2`, `virtio0`, …). |
| `controller` | no | VM-wide `scsihw` | PVE `scsihw` value; first disk declaring it wins. |
| `iothread` | no | `,iothread=1` inline | Dedicated I/O thread. |

### NIC

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `model` | yes (default `virtio`) | `netX` | `virtio`, `e1000`, `rtl8139`, `vmxnet3`… |
| `bridge` | yes | `,bridge=BRIDGE` | PVE bridge (e.g. `vmbr0`). |
| `mac` | no | `model=MAC,…` | Pinned MAC; empty → PVE assigns one (not reported as drift). |
| `vlan` | no | `,tag=NN` | 802.1Q tag (1–4094). PVE wire key is `tag` (probed PVE 9.2: `vlan` is rejected with “property is not defined in schema”). |
| `rate-limit` | no | `,rate=NN` | PVE MBit/s rate limit. |
| `firewall` | no | `,firewall=1` | PVE per-NIC firewall. |
| `slot` | no | `netN` | NIC slot (default: order). |

### Hardware

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `machine` | no | `machine` | `i440fx` (default) \| `q35`. Stop-required to change. |
| `bios` | no | `bios` | `seabios` (default) \| `ovmf` on PVE 9.2. Stop-required. |
| `display` | no | `vga` | e.g. `std`. |
| `cdrom` (block) | no | `ide2` / `ide3` | Three-state CD/DVD ownership (see below). |
| `cdrom.iso` (attach) | no | `ide2`/`ide3` = `<storage>:iso/<filename>,media=cdrom` | **An ISO `metadata.name`.** Attaches the ISO to the CD/DVD slot and creates a structured `VM → ISO` dependency: the VM is not created until the ISO has been downloaded on that node. |
| `cdrom.iso` = `none` (detach) | no | `ide2`/`ide3` = `none` | **Detach sentinel.** pveconform owns the slot and writes `none`. Use to model "no CD ever", or remove a previously-attached ISO (change `cdrom.iso: <name>` → `cdrom.iso: none`, reconcile to detach). |
| (block absent) | no | — | pveconform does **not** own the PVE IDE slot; PVE keeps its default. No dependency inferred. |
| `cdrom.media` | no | `,media=` | `cdrom` (default) \| `disk`. |
| `efi-disk` | no | `efidisk0` | Valid with `bios: ovmf`. Owns pool + size; PVE volume name not owned. PVE clamps small EFI sizes to 4 MiB. |
| `cloud-init` | no | `ide2` = `<storage>:cloudinit,size=…` | When enabled, pveconform claims `ide2` for cloud-init and shifts the CD/DVD slot to `ide3` (demonstrated coexistence on PVE 9.2). |
| `tpm` | no | `tpm0` | `v1.2` \| `v2.0`. With `bios: ovmf` + `machine: q35`. |
| `serial0` | no | `serial0` | e.g. `socket`. |

### Options

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `onboot` | no | `onboot=1` | Auto-start on node boot. |
| `startup` | no | `startup` | PVE `startup` (e.g. `order=10`). |
| `protection` | no | `protection=1` | Blocks accidental destroy. |
| `agent` | no | `agent=1` | Virtio guest agent. |
| `acpi` | no | `acpi=1` | ACPI enabled. |
| `tablet` | no | `tablet=1` | Pointer integration. |
| `hotplug` | no | `hotplug` | Comma list (`disk,network,usb`). |
| `boot-order` | no | `boot=order=…` | Explicit PVE boot order. Unset → PVE default (not owned). |
| `nested-virt` | no | `nestedvirt=1` | Nested KVM. |
| `hidden` | no | `hidden=1` | Hide KVM from the guest. |

**Deliberately not modelled:** replication jobs (source/destination/schedule
is a separate concern — a future dedicated resource), `bootspeed`, `netboot`
(rejected on PVE 9.2 config), and Secure Boot policy (separate PVE
`/security` endpoint) remain in `spec.extra`.

Drift semantics on disks/NICs/hardware:
- **Disks** matched by **slot**, compared on pool + size (PVE-assigned volume
  names are never treated as drift).
- **NICs** compared on model + bridge + (pinned MAC / vlan / rate / firewall
  when requested).
- Power transitions are independent of config drift.

---

## `kind: LXC`

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.node` | yes | — | PVE node. |
| `spec.vmid` | yes | `ctid` | PVE CT ID (shared integer pool with VMs). |
| `spec.template` | **yes** | `ostemplate` | **A `CTTemplate` `metadata.name`.** Resolved against the referenced manifest and translated to `<storage>:vztmpl/<filename>`. Creates a structured `LXC → CTTemplate` dependency. |
| `spec.state` | no (default `started`) | power | `started` \| `stopped`. |
| `spec.memory` | yes | `memory` | MiB. |
| `spec.swap` | no | `swap` | MiB. |
| `spec.cpu.cores` | yes | `cores` | PVE `cores` int. |
| `spec.root.storage` | yes | `rootfs` | PVE storage id with `rootdir` content. |
| `spec.root.size` | yes | size in `rootfs` | `<storage>:<GiB>` PVE create form. |
| `spec.mount-points` | no | `mp0`,`mp1`,… | Additional LXC volumes; see Mount Point. |
| `spec.networks` | ≥1 | `net0`,`net1`,… | See LXC NIC. |
| `spec.dns` | no | `hostname`/`nameserver`/`searchdomain` | See DNS. |
| `spec.arch` | no | `arch` | `amd64` (default) \| `arm64`. |
| `spec.options` | no | see Options | See LXC Options. |
| `spec.pve-description` | no | `description` | PVE description. |
| `spec.tags` | no | `tags` | PVE tags; `pveconform` auto-added. |
| `spec.extra` | no | passthrough | Escape hatch. |

### LXC NIC

PVE 9.x LXC NICs use a **different grammar** than VM NICs. The wire value is
`name=<iface>[,type=veth][,bridge=BRIDGE][,tag=NN][,hwaddr=xx][,rate=NN][,firewall=1]`
and **`name=` is required** (PVE 9.2 rejects a bare model string).

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `bridge` | yes | `,bridge=BRIDGE` | PVE bridge. |
| `iface` | no | `name=IFACE` (default `net<N>`) | Guest-visible interface name. |
| `type` | no | `,type=` | `veth` (PVE default). |
| `tag` | no | `,tag=NN` | 802.1Q tag. |
| `hwaddr` | no | `,hwaddr=xx` | Pinned MAC; empty → PVE assigns (not drift). |
| `rate-limit` | no | `,rate=NN` | MBit/s. |
| `firewall` | no | `,firewall=1` | Per-NIC firewall. |

### Mount Point

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `storage` | yes | `mp<N>=<storage>:<GiB>` | PVE storage id. |
| `size` | yes | size | `<GiB>` PVE create form. |
| `mount-point` | no | `,mp=PATH` | In-guest path. |

### DNS

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `hostname` | no | `hostname` | Kernel host name (defaults to `metadata.name`). |
| `nameservers` | no | `nameserver` (CSV) | List of DNS server IPs. |
| `domain` | no | `searchdomain` | DNS search domain. |

> PVE 9.2 `/lxc` create **rejects** bare `dns=`, `name=`, `os=`, `ttys=`,
> `hwclock=` — use the four valid keys above.

### LXC Options

| Field | PVE wire | Semantics |
|---|---|---|
| `unprivileged` | `unprivileged=1` | Unprivileged container. |
| `protection` | `protection=1` | Blocks accidental destroy. |
| `nesting` | `nesting=1` | Nested LXC/VM. |
| `keyctl` | `keyctl=1` | Allow keyctl. |
| `fuse` | `fuse=1` | Allow FUSE. |
| `onboot` | `onboot=1` | Auto-start on node boot. |
| `startup` | `startup` | PVE startup ordering. |

Reconcile semantics for `spec.template`:
- **Create** — the referenced CTTemplate is downloaded on `spec.node` **first**
  (structured dependency); the LXC is then created with the resolved
  `ostemplate` volume.
- **Absent prerequisite** — if the template has not been downloaded yet, the
  LXC create is deferred until a cycle where the template is present.

---

## `kind: CTTemplate`

A **downloadable PVE container-template archive** (`.tar.zst` / `vztmpl`)
living on a storage backend. It is a **storage artifact**: it has **no PVE
numeric id**, is **not a VM**, is **not an LXC**, and is **not** "a clone of
an existing CT marked as a template". That latter concept can later be modelled
by a separate, more accurate kind if a real use case appears.

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.nodes` | yes | — | List of PVE nodes the archive must exist on. Legacy single `spec.node` also accepted. |
| `spec.storage` | yes | — | PVE storage id with `vztmpl` content (e.g. `local`). |
| `spec.filename` | yes | `filename=` | On-storage name (e.g. `debian-13-standard_13.6.1-1_amd64.tar.zst`). |
| `spec.url` | yes | `url=` | HTTPS URL PVE fetches. |
| `spec.checksum` | no | (advisory) | `algorithm` + `value`; PVE 9.2's download API has no `verify` param, so this is carried for review/future use. |

Reconcile semantics:
- **Absent on a node** → `POST /nodes/{n}/storage/{s}/download-url` with
  `content=vztmpl`. One download **per declared node**.
- **Present** → zero actions.
- **Multi-node** — each `spec.nodes` entry is planned independently.
- **Never pruned in MVP** — removing the manifest does not delete the
  PVE-side file (a shared template can back many LXCs).
- **Fail-closed** — an unreadable storage listing skips the download for that
  node this cycle; the next cycle retries.

LXC manifests reference a CTTemplate by `metadata.name` via
`spec.template`; that edge is a structured dependency (so the template is
downloaded before the LXC is created) and requires no `depends-on`.

---

## `kind: ISO`

An installer ISO on a PVE storage backend. A **storage artifact** with **no**
PVE numeric id; identity is `(node, storage, filename)` on PVE, `metadata.name`
on pveconform.

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.nodes` | yes | — | List of PVE nodes the ISO must exist on. Legacy `spec.node` also accepted. |
| `spec.storage` | yes | — | PVE storage id with ISO content (e.g. `local`, `isos`). |
| `spec.filename` | yes | `filename=` | On-storage name (e.g. `debian-13.1.0-amd64-netinst.iso`). |
| `spec.url` | yes | `url=` | HTTPS URL PVE fetches. |
| `spec.checksum` | no | (advisory) | `algorithm` + `value`. |

Reconcile semantics mirror CTTemplate (download per missing node, idempotent,
never pruned, fail-closed on unreadable listing).

VMs reference an ISO by `metadata.name` via `spec.hardware.cdrom.iso` (attach state); that
edge is a structured dependency (so the ISO is downloaded on the VM's node
before the VM is created) and requires no `depends-on`.

---

## Ownership tag

On create, pveconform adds the PVE tag `pveconform` to VM/LXC objects. This is
the gate the agent uses before **deleting** anything: live PVE objects
**without** this tag are never touched. **Storage artifacts (ISO, CTTemplate)
carry no ownership tag and are never deleted** by pveconform in MVP.

## Escape hatch: `spec.extra`

`VM` and `LXC` have an `extra` map of free PVE keys merged into the create and
drift-update form values after the structured fields. Use it for properties the
schema doesn't model first-class (`bootspeed`, `rtc`, `watchdog`, unusual
device slots, …). Values are sent verbatim; pveconform performs no validation on
`extra`.

`extra` is **not** used for the ownership tag — pveconform always controls
`tags` itself. Keys that clash with a structured field (e.g. `scsi0`, `net0`,
`memory`, `onboot`) are rejected at parse time.
