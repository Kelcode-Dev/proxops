# ProxOps manifest schema

Manifests are YAML documents, one or more per file.

## Repository layout

The manifest tree is organised around the **GitOps composition model**
(see ARCHITECTURE.md → "Multi-cluster composition"):

```
clusters/<cluster>/resources.yaml   # what <cluster> consumes (explicit list)
<kind>/{base|<cluster>}/....yaml    # resource definitions, kind in vm, lxc,
                                     #   iso, ctt, templatevm, diskimage,
                                     #   templatect
```

The cluster's ProxOps **configuration** and **credentials** are committed
cluster-locally in the same directory (see ARCHITECTURE.md →
"Cluster-local configuration (SOPS)"):

```
clusters/<cluster>/
  config.yaml         # proxops --config for THIS cluster (full app config
                      #   scoped to one pve.clusters.<name> entry + SOPS
                      #   reference). git.path: "." resolves to the worktree
                      #   that contains this file.
  secrets.sops.yaml   # SOPS/age-encrypted PVE + git credentials. Public age
                      #   recipient in sops: metadata; private key OUTSIDE
                      #   the repo (see OPERATIONS.md → "Per-cluster SOPS
                      #   secrets").
  resources.yaml      # resource composition
```

Resource manifests (under `vm/`, `lxc/`, `iso/`, `ctt/`, `templatevm/`,
`diskimage/`, `templatect/`) do NOT carry credentials — a secret is never a
spec field on a resource.

A manifest file is only reconciled if some cluster's
`clusters/<cluster>/resources.yaml` lists it; the cluster boundary is the
safety model. Resources placed directly under a kind root (a flat
`vm/foo.yaml` layout) are a **validation error** — ProxOps never invents
a default cluster.

Every manifest has this envelope:

```yaml
apiVersion: proxops/v1alpha1   # only supported value
kind: VM | LXC | CTTemplate | ISO | TemplateVM | DiskImage | TemplateCT
metadata:
  name: human-readable-name      # required, [a-z0-9](-[a-z0-9])*
  labels:                        # optional, free-form
    role: workers
  annotations:                   # optional, proxops/* hints
---
spec: {...}                      # kind-specific (below)
```

`metadata.name` is the ProxOps identity within a kind. PVE identity is
additionally pinned by `spec` fields (below) — **the agent never invents PVE
ids** for objects that have one. The kinds that have **no** PVE numeric id
(ISO, CTTemplate, and DiskImage — all *storage artifacts*) are identified on
PVE by `(node, storage, filename)` and on ProxOps by `metadata.name`.

The four kinds that carry a PVE numeric id (VM, LXC, TemplateVM and
TemplateCT) share PVE's per-node integer pool: a `spec.vmid` collision
inside one cluster's composition is a parse error. A TemplateVM and a VM can
never claim the same `(node, vmid)` (PVE lists both as `type="qm"`); a
TemplateCT and an LXC can never claim the same `(node, vmid)` (PVE lists both
as `type="lxc"`).

---

## Dependencies (inferred + explicit)

ProxOps builds a **dependency graph** from two sources and schedules
creates topologically so prerequisites finish **before** their dependants:

1. **Structured references (inferred, preferred).** The schema knows about
   these cross-kind edges:
   - `VM.spec.hardware.cdrom.iso` → an `ISO`
   - `VM.spec.disks[].image` → a `DiskImage`
   - `VM.spec.clone` → a `TemplateVM` (the VM is provisioned by cloning the
     template; the template must exist and be marked before the clone runs)
   - `LXC.spec.template` → a `CTTemplate`
   - `TemplateVM.spec.hardware.cdrom.iso` → an `ISO` (TemplateVM re-uses the
     VM surface, so the same edge applies)

   These are detected automatically — you do **not** have to repeat them with
   an annotation. The planner downloads the ISO / disk image / container
   template before it creates the VM / LXC that references it.
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
| `spec.tags` | no | `tags` | PVE user tags; `proxops` is added automatically. |
| `spec.extra` | no | passthrough | Free-form PVE keys for unmodeled properties. |
| `spec.clone` | no | clone endpoint | **A TemplateVM `metadata.name`.** When set, the VM is provisioned by a PVE **full clone** of that template instead of a fresh create, then configured with this manifest's own values. See *Provisioning from a TemplateVM* below. A clone-backed VM MUST NOT declare `spec.disks` (it inherits the template's disk layout). |

### Disk

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `storage` | yes | `<slot>=<storage>:<GiB>` | PVE storage backend id. |
| `size` | yes* | size in `<slot>` | Human size (`50GiB`); the number after the pool is **GiB** on the PVE 9.2 wire. *Must be empty when `image` is set (an imported disk takes its size from the image). |
| `image` | no | `<slot>=<storage>:0,import-from=<img-storage>:import/<filename>` | **A DiskImage `metadata.name`.** Seeds this disk at create from a downloadable cloud image via PVE 9's `import-from` (the size token is forced to `0`). Creates a structured `VM → DiskImage` dependency: the image is downloaded on the VM's node before the VM is created. See `kind: DiskImage`. |
| `interface` | no (default `scsi0`) | — | Explicit PVE slot (`scsi0`, `scsi1`, `sata2`, `virtio0`, …). |
| `controller` | no | VM-wide `scsihw` | PVE `scsihw` value; first disk declaring it wins. |
| `iothread` | no | `,iothread=1` inline | Dedicated I/O thread. |
| `discard` | no | `,discard=<ignore\|on>` inline | TRIM/discard passthrough (PVE 9.2: accepted on every bus). Omitted = not owned. |
| `ssd` | no | `,ssd=<0\|1>` inline | Advertise an SSD to the guest. **scsi/sata/ide only** — PVE 9.2 rejects `ssd=` on virtio/nvme (`property is not defined in schema`), so `Validate` fails closed. Tri-state: omitted = not owned; `false` = pinned `ssd=0` (PVE retains an explicit 0 in the report). |
| `aio` | no | `,aio=<native\|threads\|io_uring>` inline | Async I/O engine (PVE 9.2: accepted on every bus). Omitted = not owned. |

> `mbcache` is **not modelled**: PVE 9.2's drive schema rejects it (probe-
> verified 400 `property is not defined in schema`). It was removed from the
> PVE 9 docs; see GAPS.md.

Image-seeded disks are imported **once** (at create, or when the slot is
empty). PVE does not re-report the `import-from` option after create (the
/config shows a plain `local-lvm:vm-N-disk-0,size=3G`), and the volume size
comes from the image's virtual size, so ProxOps compares only the **pool**
on a live image-seeded disk. A live volume at a different pool is a
non-destructive anomaly (the data-loss guard), never a re-import.

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
| `cdrom.iso` = `none` (detach) | no | `ide2`/`ide3` = `none` | **Detach sentinel.** ProxOps owns the slot and writes `none`. Use to model "no CD ever", or remove a previously-attached ISO (change `cdrom.iso: <name>` → `cdrom.iso: none`, reconcile to detach). |
| (block absent) | no | — | ProxOps does **not** own the PVE IDE slot; PVE keeps its default. No dependency inferred. |
| `cdrom.media` | no | `,media=` | `cdrom` (default) \| `disk`. |
| `efi-disk` | no | `efidisk0` | Valid with `bios: ovmf`. Owns pool + size; PVE volume name not owned. PVE clamps small EFI sizes to 4 MiB. `template` pins the OVMF vars type (`efitype=`: `byos` \| `2m` \| `4m` \| `8m`). `secure-boot` (`enabled` \| `disabled`; omitted = not owned) maps to PVE's `pre-enrolled-keys=<0\|1>` token on `efidisk0` — fully convergent (create + live-form toggle, stopped or running; enrollment takes effect at the guest's next boot). PVE 9.2 has **no** `/qemu/{id}/security` endpoint (probe-verified 501) and rejects a `secure-boot=` token (400); the earlier GAPS premise was wrong. PVE auto-adds an `ms-cert=` token when keys are enrolled: ProxOps treats it as PVE-owned (preserved verbatim on rewrites, never compared). |
| `cloud-init` | no | `ide2` = `<storage>:cloudinit,size=…` | When enabled, ProxOps claims `ide2` for cloud-init and shifts the CD/DVD slot to `ide3` (demonstrated coexistence on PVE 9.2). The storage MUST carry `images` content or the VM fails at start (see GAPS.md). |
| `tpm` | no | `tpm0` | `version`: `v1.2` \| `v2.0` (default v2.0). With `bios: ovmf` + `machine: q35`. |
| `serial0` | no | `serial0` | e.g. `socket`. |
| `sockets` | no | `sockets` | CPU socket count (default 1). |
| `numa` | no | `numa=1` | Turn PVE NUMA on. |

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

### Cloud-Init Data

A ProxOps VM's top-level PVE keys `ciuser`, `sshkeys`, `nameserver`,
`searchdomain`, `ipconfig<N>` are modelled under `spec.cloud-init-data`:

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `ci-user` | no | `ciuser` | PVE cloud-init user. Empty = not owned. |
| `ssh-keys` | no | `sshkeys` | PVE cloud-init public keys. Empty = not owned. A single `"*"` sentinel = PVE owns the live value; ProxOps does not write `sshkeys`. On the wire ProxOps percent-encodes the value and joins keys with `%0A` (PVE 9.2 requires the field value itself to be urlencoded — a raw key is rejected with "invalid urlencoded string"; probed on conformance-dev 2026-09-13). Drift compares the **decoded key set**, so a re-encode or key reorder is never drift. |
| `nameservers` | no | `nameserver` (space-separated) | PVE cloud-init DNS server CSV. Set-compared on /config vs. desired — order/duplicates are not semantics. |
| `search-domains` | no | `searchdomain` (space-separated) | PVE cloud-init DNS search domain CSV. Same set semantics. |
| `ipconfigs` | no | `ipconfig<N>` (`ip=<cidr>[,gw=<addr>]`) | PVE cloud-init static-IP. One entry per proxops-owned NIC; `nic` = PVE slot index. PVE's `dhcp` form is **not** modelled. |

Drift semantics: ProxOps owns a PVE key only when the desired field is
non-empty. Empty desired values mean "ProxOps does not write the PVE
key and does NOT surface drift for it" — so a PVE-side `ciuser` that
ProxOps has no way of knowing was set by `qm set` does not flap. The
`ssh-keys: ["*"]` sentinel is the same non-write shape: ProxOps does not
write the PVE `sshkeys` field while the sentinel is present; PVE's live
value survives. Mixing `"*"` with real keys fails `Validate()`
(ambiguous intent).

Adoption: PVE-side `sshkeys` are redacted to `["*"]` in emitted
manifests (public-key material is treated as credential-adjacent). `cipassword` /
`cicustom` / `ciupgrade` are NOT adopted — secret or PVE-side-only — and
stay in the gap report.

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

## `kind: TemplateVM`

A PVE qemu object promoted to a PVE template (PVE `template=1`). ProxOps
owns the template lifecycle end-to-end: create + mark, config drift,
ownership-tag prune. The ProxOps schema surface is **identical to
`kind: VM`** (a `TemplateVM` manifest re-uses every `spec` field a VM
manifest supports, plus `spec.state` constrained to "stopped").

Differences from `kind: VM`:

| Concern | ProxOps behaviour |
|---|---|
| `spec.state` | MUST be `stopped` (or absent → "stopped"). PVE refuses to start a template (`state: started` fails `Validate()` at parse time). |
| PVE id space | PVE's per-node qm id space is shared with `kind: VM` — a TemplateVM and a VM cannot both claim the same `(node, vmid)`. ProxOps enforces this as any other in-cluster id collision. |
| PVE /template endpoint | `POST /qemu/{id}/template` (mark). The planner emits a `MarkTemplate` action when a desired TemplateVM matches a PVE object at the same `(node, vmid)` that is NOT template-flagged. |
| PVE /untemplate endpoint | **PVE 9.2 has no `/qemu/{id}/untemplate` endpoint** (probe-verified `HTTP 501 "not implemented"` on conformance-dev 2026-09-11). The planner therefore surfaces a **non-destructive anomaly** when a ProxOps `kind: VM` desired matches a PVE-side template at the same `(node, vmid)`: ProxOps will not attempt a kind-flip write. The operator either changes the manifest to `kind: TemplateVM` (the right ProxOps representation of PVE's state) or manually demotes the PVE object (`qm` from the PVE host, or PVE's Web UI). |
| Create | `POST /qemu` with `start=0` + `POST /qemu/{id}/template`. The executor combines both into a single `Create` action. |
| Delete | `DELETE /qemu/{id}`. PVE accepts delete on a templated object. |
| Cloud-init | `spec.cloud-init-data` is fully supported (see `kind: VM` § Cloud-Init Data above). |

Adoption: PVE objects reporting `template=1` produce `kind: TemplateVM`
manifests under `templatevm/<cluster>/`. PVE-side `sshkeys` are redacted
to `["*"]`; `cipassword` / `cicustom` remain in the gap report.

---

## Provisioning a VM from a TemplateVM (`spec.clone`)

A `kind: VM` may name a `kind: TemplateVM` on `spec.clone`. ProxOps then
provisions the VM with PVE's **full-clone** endpoint instead of a fresh
create, and applies the VM's own configuration on top. The lifecycle is:

```
TemplateVM  →  clone (full=1)  →  configure (VM's own values)  →  Cloud-Init  →  optional start  →  reconcile
```

```yaml
apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: app-01
spec:
  node: pve01
  vmid: 9161
  clone: golden-tpl        # a TemplateVM metadata.name
  state: started
  memory: 2GiB             # the VM's OWN value, not the template's
  cpu: {type: host, cores: 2}
  networks:
    - model: virtio
      bridge: vmbr0
  hardware:
    cloud-init: {enabled: true, storage: local-lvm}
  cloud-init-data:         # the VM's OWN identity — see below
    ci-user: deploy
    ipconfigs:
      - nic: 0
        ip: 192.168.0.50/24
        gateway: 192.168.0.1
```

### Rules

- **`spec.disks` MUST be empty.** A clone inherits the template's disk
  layout; re-stating a create-form disk over a cloned live volume is the
  data-loss shape ProxOps forbids, so declaring both fails `Validate()` at
  parse time. The template owns the disk layout (including `scsihw` — see
  the controller note below).
- **The clone source is the TemplateVM's own `spec.vmid`,** resolved through
  the structured `VM → TemplateVM` edge. A manifest can never point a clone
  at an arbitrary PVE id: the target is named by `metadata.name`, and its
  vmid comes from the TemplateVM's spec. A clone whose resolved source equals
  its own target vmid is rejected (would clone onto itself).
- **Missing / invalid references fail closed.** `spec.clone` naming an
  unknown resource, a non-`TemplateVM`, or a template not placed on the VM's
  node is a **parse error** that aborts the cluster's cycle before any PVE
  call — ProxOps never partially converges a clone.
- **A clone is created once, never re-cloned.** The clone runs only when the
  VM is absent from PVE. Drift is corrected through config writes; ProxOps
  never re-clones over a live VM (PVE also refuses a clone onto an existing
  id).

### Identity: a clone must not keep the template's hostname / IP / keys

PVE's full clone **copies the template's entire `/config`**, including the
cloud-init DATA block (`ciuser`, `sshkeys`, `ipconfig<N>`, `nameserver`,
`searchdomain`) — probe-verified on conformance-dev PVE 9.2.2. Left alone, a
clone would boot with the **template's** identity. ProxOps therefore, in the
post-clone config write:

- **overwrites** every cloud-init DATA key the VM manifest declares (its own
  user, keys, static IP, DNS); and
- **clears** (via PVE's `delete=` form-value) every inherited identity key the
  VM manifest does **not** declare, so an undeclared `searchdomain` /
  `sshkeys` / `ipconfig<N>` from the template never leaks into the guest.

The set map and the `delete=` list are **disjoint by construction**: PVE 9.2
rejects setting and deleting the same key in one request. The clearing is
**convergent** — it rides the normal Drift path too, so a clone whose create
failed partway (clone landed, config write did not) is repaired on the next
cycle rather than leaking the template's identity forever.

`name` is never leaked (the clone's own `name=` always wins), and PVE
regenerates `vmgenid` / `smbios1` (UUID) per clone. `tags` are overwritten
with the VM's own (ownership tag included).

### The cloud-init DRIVE is inherited, not rewritten

The cloud-init **drive** (`ide2=<storage>:cloudinit`) is a storage-backed
volume, so a clone gets its own copy from the template. Re-sending the
create-form drive over the clone's live volume makes PVE `lvcreate` a volume
that already exists and the task **fails** ("Logical Volume
`vm-<id>-cloudinit` already exists" — probe-verified 2026-09-15). ProxOps
therefore writes `ide2` only into an **empty** slot (a template that carried
no drive); a live inherited drive is left as-is, and a drive on a different
pool than the manifest asks for is surfaced as a non-destructive anomaly
(moving a live volume is a storage migration, not a config write). The
cloud-init **DATA** keys above are always reconciled — they are plain config
values, not volumes.

### Controller / boot caveat

A clone inherits the template's `scsihw`. Some cloud images (e.g. the Ubuntu
*minimal* images) ship an initramfs without PVE's default `lsi53c897a`
driver and **kernel-panic** ("VFS: Unable to mount root fs on
unknown-block(0,0)") unless the controller is `virtio-scsi-single`. Because a
clone-backed VM declares no disks, the controller is owned by the **template**
(set it on the template's `spec.disks[].controller`); the clone inherits it.
See GAPS.md.

### Power

PVE's clone endpoint rejects `start` (probe-verified), so a clone-backed VM
with `state: started` is powered on by the planner's normal power step on the
following cycle — the same convergence guarantee as any other VM, one cycle
later than a plain create.

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
| `spec.mount-points` | no | `mp0`,`mp1`,… | Additional LXC **allocated** volumes; see Mount Point. |
| `spec.bind-mounts` | no | `mpN` (host-path form) | Host-path bind mounts (M13); see Bind Mount. Shares the `mpN` slot namespace with `mount-points` (a slot collision is a parse error). |
| `spec.networks` | ≥1 | `net0`,`net1`,… | See LXC NIC. |
| `spec.dns` | no | `hostname`/`nameserver`/`searchdomain` | See DNS. |
| `spec.arch` | no | `arch` | `amd64` (default) \| `arm64`. |
| `spec.options` | no | see Options | See LXC Options. |
| `spec.pve-description` | no | `description` | PVE description. |
| `spec.tags` | no | `tags` | PVE tags; `proxops` auto-added. |
| `spec.extra` | no | passthrough | Escape hatch. |

### LXC NIC

PVE 9.x LXC NICs use a **different grammar** than VM NICs. The wire value is
`name=<iface>[,type=veth][,bridge=BRIDGE][,tag=NN][,hwaddr=xx][,rate=NN][,firewall=1][,ip=<cidr>][,gw=<addr>]`
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
| `ip` | no | `,ip=<addr/prefix>` | Static address; empty → PVE/DHCP decides (not owned). |
| `gw` | no | `,gw=<addr>` | Static gateway; empty → not owned. |

### Mount Point

An **allocated** LXC volume on an `mpN` slot (PVE's `mpN=<storage>:<GiB>,mp=<path>`
form). PVE 9.2 rewrites the report to `mpN=<storage>:vm-<ctid>-disk-<n>,mp=<path>,size=<binary>`.

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `storage` | yes | `mp<N>=<storage>:<GiB>` | PVE storage id. |
| `size` | yes | size | `<GiB>` PVE create form. |
| `mount-point` | no | `,mp=PATH` | In-guest path. |
| `options.read-only` | no | `,ro=<0\|1>` | Mount read-only (PVE default 0). Tri-state: omitted = not owned. |
| `options.backup` | no | `,backup=<0\|1>` | Include in vzdump (PVE default 1). |
| `options.acl` | no | `,acl=<0\|1>` | ACL support (PVE default 0). |
| `options.quota` | no | `,quota=<0\|1>` | User quotas (PVE default 0). |
| `options.shared` | no | `,shared=<0\|1>` | Cluster-shared volume (PVE default 0). |
| `options.mount-options` | no | `,mountoptions=OPTS` | Free-form `mount(8)` options (e.g. `noatime`). |

All option tokens converge **in place** on a live volume via the live drive
form (probe-verified PVE 9.2.2: PUT `mpN=<volid>,size=…,mp=…,ro=…` toggles
without recreating the volume). The guest path (`mp=`) also converges
in place. A pool/size change on a live volume stays a non-destructive
anomaly (the data-loss guard).

### Bind Mount

A host-path bind mount on an `mpN` slot (PVE's `mpN=<host-path>,mp=<guest>`
form). Bind mounts and allocated volumes are **distinct shapes** on the same
slot namespace: adoption classifies each live `mpN` by its first token
(`<storage>:` prefix = allocated; leading `/` = bind).

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `host-path` | yes | `mp<N>=<host-path>` | Absolute directory on the PVE node. PVE requires it to exist and contain no symlinks. ProxOps **refuses system-critical roots** (`/etc`, `/var`, `/usr`, `/boot`, `/dev`, `/root`, `/run`, `/srv`, `/sys`, `/bin`, `/sbin`, `/lib*`, `/proc`, `/`) at validation — pct.conf warns binding system dirs can damage the host. |
| `mount-point` | yes | `,mp=<guest-path>` | Absolute in-guest path. |
| `slot` | no | `mpN` | Slot override (defaults continue after `mount-points`). |
| `read-only` | no | `,ro=<0\|1>` | Tri-state; omitted = not owned. |

> **Permission boundary (probe-verified PVE 9.2.2):** PVE restricts bind-mount
> writes to `root@pam` — an API-token request is rejected with HTTP 403
> `mount point type bind is only allowed for root@pam` at BOTH create and
> `/config` PUT. ProxOps models bind mounts faithfully (declarative +
> adopted + drift-detected) and lets PVE enforce the permission: with a
> token identity the write action **fails closed** with PVE's 403 (never a
> silent skip or false convergence). Use a ticket-auth cluster credential
> (`pve.auth: ticket`, user `root@pam`) when bind-mount convergence is
> required.
>
> **Safety:** ProxOps never re-points a live bind mount's host path
> automatically (exposing a different host directory to a running container
> can damage host data) — that divergence is a non-destructive anomaly. A
> bind↔allocated swap on one slot is likewise an anomaly, never automatic.

### DNS

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `hostname` | no | `hostname` | Kernel host name (defaults to `metadata.name`). |
| `nameservers` | no | `nameserver` (CSV) | List of DNS server IPs. |
| `domain` | no | `searchdomain` | DNS search domain. |

> PVE 9.2 `/lxc` create **rejects** bare `dns=`, `name=`, `os=`, `ttys=`,
> `hwclock=` — use the four valid keys above.

### LXC Options

Boolean options are **tri-state** (`*bool`): unset (nil) = "PVE decides"
(never written); `true` = `=1`; `false` = `=0`.

| Field | PVE wire | Semantics |
|---|---|---|
| `unprivileged` | `unprivileged=1` | Unprivileged container. **Create-only** on PVE 9.x (PUT → 500): a divergence surfaces a recreate-required anomaly, never a write. |
| `protection` | `protection=1` | Blocks accidental destroy. |
| `nesting` | `features=nesting=<0\|1>` | Nested LXC/VM. Rides the PVE 9.x `features` composite — a top-level `nesting=` is rejected (400). |
| `keyctl` | (no accepted form) | Allow keyctl. **Adoptable but not convergable** on PVE 9.2 (403 at create + PUT; the `features` composite only carries `nesting`). A desired=on / live=off mismatch is a non-destructive anomaly. |
| `fuse` | (no accepted form) | Allow FUSE. Same adopt-only caveat as `keyctl`. |
| `onboot` | `onboot=1` | Auto-start on node boot. |
| `startup` | `startup` | PVE startup ordering. |
| `console` | `console=<0\|1>` | Console enable (create + PUT accepted). |
| `ttys` | (not sent) | Accepted in the manifest but **inert**: ProxOps never writes it (PVE 9.2 rejects `ttys=` at create) and `adopt` does not capture it (it surfaces as a gap). |

Reconcile semantics for `spec.template`:
- **Create** — the referenced CTTemplate is downloaded on `spec.node` **first**
  (structured dependency); the LXC is then created with the resolved
  `ostemplate` volume.
- **Absent prerequisite** — if the template has not been downloaded yet, the
  LXC create is deferred until a cycle where the template is present.

---

## `kind: TemplateCT`

A PVE LXC container promoted to a PVE template (PVE `template=1` on the
`/lxc/{id}/config` report). It is the LXC analogue of `kind: TemplateVM`
(M11) and is **distinct from `kind: CTTemplate`**: a CTTemplate is a
downloadable vztmpl *storage artifact* (no numeric id), while a TemplateCT
is a live container object with a numeric CTID that was customized and then
promoted with `pct template` (`POST /lxc/{id}/template`).

The ProxOps schema surface is **identical to `kind: LXC`** (a `TemplateCT`
manifest re-uses every `spec` field an LXC manifest supports, plus
`spec.state` constrained to "stopped").

Differences from `kind: LXC`:

| Concern | ProxOps behaviour |
|---|---|
| `spec.state` | MUST be `stopped` (or absent → "stopped"). ProxOps never starts a template CT (`state: started` fails `Validate()` at parse time). |
| PVE id space | PVE's per-node lxc id space is shared with `kind: LXC` — a TemplateCT and an LXC cannot both claim the same `(node, vmid)`. ProxOps enforces this as any other in-cluster id collision. PVE lists both as `type="lxc"`; the `template` flag in the per-object `/config` report is what distinguishes them. |
| PVE /template endpoint | `POST /lxc/{id}/template` (mark). Unlike the qemu mark, PVE responds **synchronously with a NULL data** (no task UPID — probe-verified PVE 9.2.2); the executor treats the empty UPID as immediate success. The planner emits a `MarkTemplate` action when a desired TemplateCT matches a PVE CT at the same `(node, vmid)` that is NOT template-flagged. |
| PVE /untemplate endpoint | **PVE 9.2 has no `/lxc/{id}/untemplate` endpoint** (probe-verified `HTTP 501 "not implemented"` on conformance-dev 2026-10-14, same as the qemu side). The planner surfaces a **non-destructive anomaly** when a ProxOps `kind: LXC` desired matches a PVE-side template CT at the same `(node, vmid)`: ProxOps will not attempt a kind-flip write. The operator either changes the manifest to `kind: TemplateCT` or manually demotes the object on the PVE host. |
| Create | `POST /lxc` with `start=0` + `POST /lxc/{id}/template`. The executor combines both into a single `Create` action. |
| Delete | `DELETE /lxc/{id}`. PVE accepts destroy on a template CT. |
| Volume rename | Promotion RENAMES the rootfs volume `vm-<ctid>-disk-0` → `base-<ctid>-disk-0` (probe-verified). ProxOps never compares volume names, so drift is stable across the rename. |
| `spec.template` | Still names a CTTemplate (the vztmpl the container is bootstrapped from before promotion). The structured `TemplateCT → CTTemplate` dependency edge is inherited from LXC. |

Adoption: PVE CTs reporting `template=1` produce `kind: TemplateCT`
manifests under `templatect/<cluster>/` (the `template` key is owned by the
kind, not a gap). The `ostemplate` gap still applies (PVE does not persist
it), so `spec.template` needs a human before the manifest is listed — the
same contract as `kind: LXC`.

---

## `kind: CTTemplate`

A **downloadable PVE container-template archive** (`.tar.zst` / `vztmpl`)
living on a storage backend. It is a **storage artifact**: it has **no PVE
numeric id**, is **not a VM**, and is **not an LXC**. It is also distinct
from `kind: TemplateCT` (M13): a CTTemplate is the downloadable vztmpl
*file* an LXC is bootstrapped from, while a TemplateCT is a live container
object that was customized and *promoted* to a PVE template
(`pct template`).

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.nodes` | yes | — | List of PVE nodes the archive must exist on. Legacy single `spec.node` also accepted. |
| `spec.storage` | yes | — | PVE storage id with `vztmpl` content (e.g. `local`). |
| `spec.filename` | yes | `filename=` | On-storage name (e.g. `debian-13-standard_13.6.1-1_amd64.tar.zst`). |
| `spec.url` | yes | `url=` | HTTPS URL PVE fetches. |
| `spec.checksum` | no | `checksum` + `checksum_algorithm` | `algorithm` (`sha256`\|`sha1`\|`sha512`\|`md5`) + `value`; sent on the `download-url` request when set (PVE 9.2's exact parameter acceptance is not live-verified — see GAPS.md). |

Reconcile semantics:
- **Absent on a node** → `POST /nodes/{n}/storage/{s}/download-url` with
  `content=vztmpl`. One download **per declared node**.
- **Present** → zero actions.
- **Multi-node** — each `spec.nodes` entry is planned independently.
- **Never pruned** — removing the manifest does not delete the
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
on proxops.

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.nodes` | yes | — | List of PVE nodes the ISO must exist on. Legacy `spec.node` also accepted. |
| `spec.storage` | yes | — | PVE storage id with ISO content (e.g. `local`, `isos`). |
| `spec.filename` | yes | `filename=` | On-storage name (e.g. `debian-13.1.0-amd64-netinst.iso`). |
| `spec.url` | yes | `url=` | HTTPS URL PVE fetches. |
| `spec.checksum` | no | `checksum` + `checksum_algorithm` | `algorithm` (`sha256`\|`sha1`\|`sha512`\|`md5`) + `value`; sent on the `download-url` request when set (see GAPS.md). |

Reconcile semantics mirror CTTemplate (download per missing node, idempotent,
never pruned, fail-closed on unreadable listing).

VMs reference an ISO by `metadata.name` via `spec.hardware.cdrom.iso` (attach state); that
edge is a structured dependency (so the ISO is downloaded on the VM's node
before the VM is created) and requires no `depends-on`.

---

## `kind: DiskImage`

A downloadable **disk image** on a PVE storage backend (PVE 9's `import`
content pool: qcow2 / vmdk / raw). Like ISO and CTTemplate it is a **storage
artifact** with **no** PVE numeric id; identity is `(node, storage, filename)`
on PVE, `metadata.name` on proxops.

This kind is what makes a `kind: VM` bootable from a cloud image **without a
template**: a VM disk references it via `spec.disks[].image`, and proxops
seeds that disk at create with PVE's `import-from` form.

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `spec.nodes` | yes | — | List of PVE nodes the image must exist on. Legacy `spec.node` also accepted. |
| `spec.storage` | yes | — | PVE storage id with `import` content (e.g. `local`). |
| `spec.filename` | yes | `filename=` | On-storage name. PVE 9.2's import pool accepts `.qcow2` \| `.vmdk` \| `.raw` (probed on conformance-dev 2026-09-13; `.qcow`/`.img`/`.iso` are rejected at download). |
| `spec.url` | yes | `url=` | HTTPS URL PVE fetches. |
| `spec.checksum` | no | `checksum` + `checksum_algorithm` | `algorithm` (`sha256`\|`sha1`\|`sha512`\|`md5`) + `value`; sent on the `download-url` request when set (see GAPS.md). |

Reconcile semantics mirror ISO/CTTemplate (download per missing node via
`POST /storage/{s}/download-url` with `content=import`, idempotent, never
pruned, fail-closed on unreadable listing). The file lands at
`<storage>:import/<filename>`.

VMs reference a DiskImage by `metadata.name` via `spec.disks[].image`; that
edge is a structured `VM → DiskImage` dependency (the image is downloaded on
the VM's node before the VM is created) and requires no `depends-on`.

**End-to-end validation (2026-09-13, conformance-dev PVE 9.2.2):** a
DiskImage + a cloud-init VM (`ci-user`, `ssh-keys`, `nameservers`,
`search-domains`, static `ipconfigs`, cloud-init drive on `ide2`, guest
agent) was created and booted by `proxops apply`; the guest's
`cloud-init status` reported `done` with `DataSourceNoCloud`, and hostname,
static IP + gateway, DNS servers/search domain, the `ci-user` account, and
the SSH key in `authorized_keys` all matched the manifest. A second apply
planned zero actions (idempotent), and out-of-band PVE-side edits to
`ciuser`/`sshkeys` were detected and corrected on the next cycle.

---

## Ownership tag

On create, ProxOps adds the PVE tag `proxops` to VM / LXC / TemplateVM
objects. This is the gate the agent uses before **deleting** anything:
live PVE objects **without** this tag are never touched. Objects tagged
by the pre-rename build (`pveconform`) are treated as untagged — never
deleted, and claimed with the `proxops` tag on the first managed update
(see OPERATIONS.md → "Ownership-tag migration"). **Storage artifacts
(ISO, CTTemplate, DiskImage) carry no ownership tag and are never
deleted.**

## Escape hatch: `spec.extra`

`VM` and `LXC` have an `extra` map of free PVE keys merged into the create and
drift-update form values after the structured fields. Use it for properties the
schema doesn't model first-class (`bootspeed`, `rtc`, `watchdog`, unusual
device slots, …). Values are sent verbatim; ProxOps performs no validation on
`extra`.

`extra` is **not** used for the ownership tag — ProxOps always controls
`tags` itself. Keys that clash with a structured field (e.g. `scsi0`, `net0`,
`memory`, `onboot`) are rejected at parse time.
