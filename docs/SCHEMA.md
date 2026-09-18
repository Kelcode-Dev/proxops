# Resource reference

This page is the **resource overview** — one row per kind ProxOps manages,
with its PVE object, identity, and where the authoritative field reference
lives. The kind-level schema detail has moved to dedicated reference pages
so each stays authoritative in one place; this page is the map.

Manifests are YAML documents, one or more per file, each carrying a
`metadata.name` (ProxOps identity) and `spec` (PVE wire translation).

## Kinds at a glance

| Kind | PVE object | Identity | Field reference |
|---|---|---|---|
| `VM` | qemu VM | `(node, vmid)` | [VM & TemplateVM](ref-vm.md#kind-vm) |
| `LXC` | container | `(node, vmid)` | [LXC & TemplateCT](ref-lxc.md#kind-lxc) |
| `TemplateVM` | qemu VM promoted to template (`template=1`) | `(node, vmid)` (qm pool) | [VM & TemplateVM](ref-vm.md#kind-templatevm) |
| `TemplateCT` | LXC promoted to template (`template=1`) | `(node, vmid)` (lxc pool) | [LXC & TemplateCT](ref-lxc.md#kind-templatect) |
| `ISO` | ISO on `iso` storage | `(node, storage, filename)` — no PVE id | [Storage artifacts](reference-artifacts.md#kind-iso) |
| `CTTemplate` | `vztmpl` storage archive | `(node, storage, filename)` — no PVE id | [Storage artifacts](reference-artifacts.md#kind-cttemplate) |
| `DiskImage` | qcow2/vmdk/raw on `import` storage | `(node, storage, filename)` — no PVE id | [Storage artifacts](reference-artifacts.md#kind-diskimage) |

The four kinds that carry a PVE numeric id (VM, LXC, TemplateVM,
TemplateCT) share PVE's per-node integer pool: a `spec.vmid` collision
inside one cluster's composition is a parse error. The three artifact
kinds (ISO, CTTemplate, DiskImage) have **no** PVE numeric id — their only
identity is `(node, storage, filename)` on PVE and `metadata.name` on
ProxOps; they are storage artifacts and are never pruned.

## Provisioning relationships

| From | To | Field | Semantics |
|---|---|---|---|
| VM | ISO | `spec.hardware.cdrom.iso` | Attach an ISO to a CD/DVD slot |
| VM | DiskImage | `spec.disks[].image` | Seed a disk from a downloadable image |
| VM | TemplateVM | `spec.clone` | Provision the VM by PVE full-clone |
| LXC | CTTemplate | `spec.template` | Bootstrap an LXC from a container-template archive |
| TemplateVM | ISO | `spec.hardware.cdrom.iso` | TemplateVM re-uses the VM surface |
| TemplateCT | CTTemplate | `spec.template` | Inherited from LXC |
| Cloud-init data | VM / TemplateVM | `spec.cloud-init-data` | Identity block on first boot |

See [Repository layout → Dependency model](repo-layout.md#dependency-model)
for how these edges become the planner's topological order, and
[Cloud-init](cloudinit.md) for the drive-vs-data split and clone-identity
clearing.

## Cross-cutting guarantees

- **Ownership tag.** On create, ProxOps adds the PVE tag `proxops` to VM /
  LXC / TemplateVM / TemplateCT objects. This is the gate the agent uses
  before **deleting** anything: live PVE objects **without** this tag are
  never touched. Storage artifacts carry no ownership tag and are never
  deleted. Objects created by the pre-rename build (`pveconform`) are
  treated as **untagged** — never pruned, then claimed with the `proxops`
  tag on the first managed update (see OPERATIONS → "Ownership-tag
  migration").
- **Determinism.** ProxOps emits stable ordering for resources, plans,
  adoption results, and status reports. A second `proxops diff` on a
  converged cluster is byte-identical.
- **Fail-closed on unknowns.** An unmodelled PVE config key during
  adoption becomes an explicit `gap`/`INCOMPLETE`, not a silent drop. See
  [Compatibility / Gaps](GAPS.md).
- **Escape hatch `spec.extra`.** `VM` and `LXC` carry a free-form PVE key
  map merged into the create/drift-update form values after the structured
  fields. Used for PVE properties the schema does not model first-class
  (`bootspeed`, `rtc`, `watchdog`, unusual device slots, …). Values are
  sent verbatim; ProxOps performs no validation on `extra`. Keys that
  clash with a structured field (e.g. `scsi0`, `net0`, `memory`, `onboot`)
  are rejected at parse time. `extra` is not used for the ownership tag —
  ProxOps always controls `tags` itself.
- **Resource layout.** Where each manifest lives (`vm/`, `lxc/`, `iso/`,
  `ctt/`, `templatevm/`, `diskimage/`, `templatect/`), how bases and
  cluster-specific files compose, and the repository-level invariants:
  [Repository layout](repo-layout.md).
- **Config for a cluster.** The endpoint + node allowlist + SOPS reference
  that each cluster reconciles against lives in
  `clusters/<name>/config.yaml` (one `pve.clusters` entry, named after the
  directory). Process-wide settings (log, reconcile, listen, data-dir,
  bootstrap credentials) live in the optional `proxops.yaml`. See
  [Configuration reference](reference-config.md).
