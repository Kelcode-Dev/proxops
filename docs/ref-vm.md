# Reference: VM & TemplateVM

Machine resources ProxOps reconciles on PVE.

- **`VM`** — a PVE qemu object ProxOps owns end-to-end: hardware, disks,
  networks, options, cloud-init data and power state.
- **`TemplateVM`** — a `kind: VM` promoted to a PVE template
  (`template=1`). Its schema surface is identical to `VM` (state must be
  `stopped`); ProxOps creates it with `POST /qemu` +
  `POST /qemu/{id}/template` and manages it through the standard
  `/config` surface. A VM provisions from one with `spec.clone`
  (see [Provisioning a VM from a TemplateVM](#provisioning-a-vm-from-a-templatevm-spec-clone)).

**Identity & id-space** — `(spec.node, spec.vmid)`. PVE's per-node integer
pool is shared by the qm and lxc kinds: a VM/TemplateVM and an
LXC/TemplateCT can never claim the same `(node, vmid)` in one cluster.
ProxOps never invents PVE ids.

VMs live under `vm/`; TemplateVMs under `templatevm/`.

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
| `pci-devices` | no | `hostpci<N>=<bdf>[,pcie=N]` | M13.2: structured host PCI passthrough. Each entry has a `slot` (PVE's `hostpci<N>`, `N` 0–999), a `device` (PVE PCI BDF, `domain:bus:slot[.func]`; ProxOps lower-cases it on the wire so the canonical form is stable), and an optional `pcie` (renders the PVE `pcie=1`/`pcie=0` token; omitted = ProxOps does not own the token). ProxOps validates slots (unique, well-formed) and BDF syntax on parse — an unparseable device or duplicate slot fails closed. Drift requires **stop** to add/change/remove a `hostpci<N>` (changing host PCI passthrough on a running guest is not a safe hot operation). A `hostpci<N>` present in PVE that the manifest does NOT declare is surfaced in the **Drift anomaly** report — ProxOps does NOT delete PVE-managed PCI (an operator-added passthrough must not disappear just because it is unstated in the manifest). PVE rejects `hostpci` for devices not in a PCI pool on most clusters ("only root can set 'hostpci<N>' config for non-mapped devices") — on those clusters ProxOps surfaces the PVE task failure, not a config write. See GAPS.md for additional PVE-side option tokens ProxOps does not model (`x-vga`, `rombar`, `mdev`). |

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
| `ssh-key-refs` | no | `sshkeys` | M13.2: PLURAL list of SOPS dot-paths (`cloud-init.ssh-keys.<name>`), each resolved against the cluster's decrypted SOPS document. The resolved key material is concatenated **in manifest order** and written to PVE's `sshkeys` (same percent-encoded `%0A`-joined shape as `ssh-keys`). A missing ref, an empty SOPS entry, or an empty resolved set fails closed BEFORE any PVE mutation — ProxOps does NOT treat a missing ref as "no keys". **Mutual exclusion:** `ssh-keys` and `ssh-key-refs` are exclusive; both non-empty is a `Validate()` error. The sentinel `"*"` is only meaningful on the `ssh-keys` field (PVE owns the live keys). |
| `ci-password-ref` | no | `cipassword` | M13.2: SINGULAR SOPS dot-path (`cloud-init.passwords.<name>`), resolved at reconcile time. **There is no plaintext `ci-password` field.** The resolved value is written to PVE's `cipassword` field on create and on update, but PVE 9.2 masks the value on read-back as `**********` (probe-verified; the plaintext is unrecoverable). The Drift rule: ProxOps writes `cipassword` only when the live `cipassword` is ABSENT; when present (even as `**********`), ProxOps treats it as satisfied and does NOT rewrite. The live value is never compared, and ProxOps never `delete=`s it — an unowned or already-set `cipassword` is not a drift. A malformed ref (not starting with `cloud-init.passwords.`), an empty entry, or a missing SOPS name fails closed. |
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
manifests (public-key material is treated as credential-adjacent).
M13.2: when the cluster is SOPS-backed AND the live PVE `sshkeys` line is
present VERBATIM in the cluster's SOPS document as `cloud-init.ssh-keys.<name>`,
adopt emits the manifest with **`ssh-key-refs`** (a SOPS dot-path) instead of
the sentinel — the committed manifest names the ref, never the key. When
nothing matches, the manifest keeps the `"*"` sentinel. `cipassword`,
`cicustom` / `ciupgrade` are NOT adopted (PVE 9.2 masks cipassword on
read-back; secret or PVE-side-only) and stay in the gap report.
`proxops adopt --adopt-secrets` additionally IMPORTS new SOPS names
(deterministic `adopted-<fingerprint>`); see `adopt.md` + `sops-credentials.md`.

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
| Cloud-init | `spec.cloud-init-data` is fully supported (see `kind: VM` § Cloud-Init Data above), including M13.2 SOPS refs. **PCI**: `spec.hardware.pci-devices` is supported but a template with host PCI is unusual (a GPU-VM clone-source is a valid shape); M13.2 drift/stop rules apply. |

Adoption: PVE objects reporting `template=1` produce `kind: TemplateVM`
manifests under `templatevm/<cluster>/`. M13.2: PVE-side `sshkeys` are
SOPS-matched to `ssh-key-refs` (verbatim match on the decrypted SOPS doc);
on no match they are redacted to `["*"]` (see `kind: VM` § Cloud-Init
adoption). `cipassword` / `cicustom` remain in the gap report.
`spec.hardware.pci-devices` adopts from live `hostpci<N>` keys.

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

