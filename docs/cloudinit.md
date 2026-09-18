# Cloud-init

ProxOps models PVE's cloud-init surface on VMs and TemplateVMs: the
**drive** (a `images`-content volume claiming `ide2`) plus the **data**
keys PVE feeds to cloud-init — `ciuser`, `sshkeys`, `nameserver`,
`searchdomain`, `ipconfig<N>`.

The split matters:

- **Drive** is a storage-backed volume: ProxOps writes `ide2` only into
  an empty slot; a live volume at a different pool is a non-destructive
  anomaly (moving a live volume is a storage migration, not a config
  write).
- **Data** is plain `/config`: ProxOps overwrites every key it declares
  on drift, and — for `spec.clone` VMs — *clears* inherited-but-undeclared
  keys so a clone never boots with its template's identity.
- The `ssh-keys: ["*"]` sentinel means "PVE owns the live value": ProxOps
  does not write `sshkeys` while it is present.

The cloud-init drive needs a storage with `images` content; an `ide2`
drive on a storage without it will let the VM **create** but **fail at
start** ("storage does not support content-type 'images'").

The `cloud-init` hardware block:

| Field | Required | PVE wire | Semantics |
|---|---|---|---|
| `enabled` | yes (of the block) | 1 | Claim a cloud-init drive. |
| `storage` | yes | `ide2=<storage>:cloudinit,size=…` | PVE storage with `images` content. |

The CD/DVD slot shifts to `ide3` while cloud-init owns `ide2` (demonstrated
coexistence on PVE 9.2; ProxOps writes direct IDE slot keys, never PVE's
`cdrom=` alias).

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


---

## Clone identity (TemplateVM `spec.clone`)

A PVE **full clone copies the template's entire `/config`**, including
the cloud-init DATA block — probe-verified PVE 9.2.2. Left alone, a
clone would boot with the *template's* hostname, keys and static IPs.
The post-clone config write therefore:

- **overwrites** every cloud-init DATA key the VM manifest declares;
- **clears** (PVE `delete=` form) every inherited-but-undeclared DATA key;
- **never re-sends the cloud-init drive** over the clone's inherited live
  volume (it would make PVE's lvcreate fail on the existing
  `vm-<id>-cloudinit` volume — the drive is written only when the slot
  is empty).

The set/delete split is disjoint by construction (PVE 9.2 rejects
setting and deleting the same key in one request) and is **convergent**:
the same clearing rides the normal drift path, so a half-created clone
(clone landed, config write failed) is repaired on the next cycle —
the template's identity never leaks permanently.

---

## Adoption of cloud-init

PVE-side `sshkeys` are **redacted** to the `["*"]` sentinel in emitted
manifests (public-key material is credential-adjacent): the operator
fills in the real key(s) — or leaves the sentinel if PVE owns them —
before listing an adopted manifest in a composition. `cipassword`,
`cicustom` and `ciupgrade` are NOT adopted (secret / PVE-side-only):
they stay in the gap report. Non-IDE cloud-init volumes are PVE-owned
and are reported as a gap + live-only anomaly, never rewritten.

---

