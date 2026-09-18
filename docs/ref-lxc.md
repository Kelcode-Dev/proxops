# Reference: LXC & TemplateCT

Container resources ProxOps reconciles on PVE.

- **`LXC`** — a PVE container ProxOps owns end-to-end: rootfs, mount
  points (allocated + bind), networks, DNS, options and power state. An
  `LXC` **must** name a `kind: CTTemplate` on `spec.template` — the PVE
  `ostemplate` volume it bootstraps from (a structured `LXC → CTTemplate`
  dependency; see [CTTemplate](reference-artifacts.md#kind-cttemplate)).
- **`TemplateCT`** — a `kind: LXC` promoted to a PVE template
  (`template=1`) with `POST /lxc/{id}/template` (synchronous, no UPID).
  Its schema surface is identical to `LXC` (state must be `stopped`);
  distinct from `kind: CTTemplate` (a downloadable vztmpl artifact).

**Identity & id-space** — `(spec.node, spec.vmid)` (PVE's `ctid`). Same
shared per-node pool as the qm kinds. ProxOps never invents PVE ids.

LXCs live under `lxc/`; TemplateCTs under `templatect/`.

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

