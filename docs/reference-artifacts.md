# Reference: storage artifacts (ISO, CTTemplate, DiskImage)

Storage artifacts ProxOps reconciles on PVE. These kinds hold
**downloaded files** on PVE storage; they have **no PVE numeric id**
(identity is `(node, storage, filename)` on PVE, `metadata.name` on
ProxOps) and are **never pruned** — removing a manifest stops
re-downloads; it does not delete the PVE-side file.

- **`ISO`** — an installer ISO on an `iso` storage (`iso/`). A VM attaches
  it with `spec.hardware.cdrom.iso` (structured `VM → ISO` dependency).
- **`CTTemplate`** — a PVE container-template archive (`vztmpl` storage,
  `ctt/`). An LXC bootstraps from it with `spec.template`.
- **`DiskImage`** — a disk image (qcow2/vmdk/raw) on an `import` storage
  (`diskimage/`). A VM disk seeds from it with `spec.disks[].image`
  (PVE's `import-from` at create).

Downloads are planned **before** the VM/LXC that references them
(topological level 0 → 1), so the reference edges above are automatic.

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
