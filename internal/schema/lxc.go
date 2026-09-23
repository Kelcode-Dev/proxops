package schema

import (
	"fmt"
	"strconv"
	"strings"
)

// LXCCPU is PVE's cores config (no "type" for containers; PVE uses cores).
type LXCCPU struct {
	// Cores is the vCPU count.
	Cores int `yaml:"cores" json:"cores"`
	// Limits are PVE's "units=..." style, optional.
	Units int `yaml:"units,omitempty" json:"units,omitempty"`
}

// LXCRoot is PVE's "rootfs" (the container's /).
type LXCRoot struct {
	// Storage is the PVE storage id, e.g. "vm_disks".
	Storage string `yaml:"storage" json:"storage"`
	// Size is the rootfs size, e.g. "32GiB".
	Size string `yaml:"size" json:"size"`
}

// LXCNetwork is one LXC NIC.
//
// PVE 9.2 LXC /lxc create requires `name=<iface>` in the per-NIC property
// list (bare "veth,bridge=vmbr0" is rejected with "value without key, but
// schema does not define a default key"). The default `name` is `net<i>` to
// match the slot — but PVE actually stores it as a guest-visible interface
// name (wired0 by default in PVE 9.x web UI).
//
// PVE 9.2 LXC netX valid form-values: name, bridge, tag, vlan, hwaddr,
// type, rate, firewall, ip, gw (see pct.conf(5)); `multi_bridge`/
// `macvlan_mode` are NOT in PVE's netX schema (PVE 9.2 probe-verified:
// rejected by the create API). `ip=`/`gw=` are user-set static address /
// gateway tokens — they are not PVE-assigned, so proxops may own them.
type LXCNetwork struct {
	// Iface is PVE's `name=<iface>` (guest-side interface name, e.g.
	// "wired0" default when PVE's UI creates a net, "net0" legacy). When
	// empty, proxops defaults to "net<i>" to be deterministic.
	Iface string `yaml:"iface,omitempty" json:"iface,omitempty"`
	// Bridge, e.g. "vmbr0".
	Bridge string `yaml:"bridge" json:"bridge"`
	// Type is PVE's `type=` on LXC NICs: "veth" default. Probe-verified
	// that PVE 9.2 LXC accepts "type=veth".
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	// HWAddr is a fixed MAC; empty lets PVE pick one.
	HWAddr string `yaml:"hwaddr,omitempty" json:"hwaddr,omitempty"`
	// Tag is a VLAN tag applied to the LXC NIC.
	Tag int `yaml:"tag,omitempty" json:"tag,omitempty"`
	// RateLimit is PVE's rate= option (MBit/s).
	RateLimit int `yaml:"rate-limit,omitempty" json:"rate-limit,omitempty"`
	// Firewall enables PVE's LXC firewall on this NIC.
	Firewall bool `yaml:"firewall,omitempty" json:"firewall,omitempty"`
	// Ip is the user-set static IP / prefix (e.g. "192.168.192.110/18") that
	// PVE assigns to the guest interface. Empty = PVE will not set one /
	// the container uses DHCP or the bridge's default.
	Ip string `yaml:"ip,omitempty" json:"ip,omitempty"`
	// Gw is the user-set gateway (e.g. "192.168.192.5"). Empty = PVE will
	// not set one.
	Gw string `yaml:"gw,omitempty" json:"gw,omitempty"`
	// Slot overrides the default net<i>.
	Slot string `yaml:"slot,omitempty" json:"slot,omitempty"`
}

// LXCVolume is an LXC storage volume entry (rootfs or an additional
// mountpoint mp0/mp1/...).
type LXCVolume struct {
	// Storage is the PVE storage id with `rootdir` content (e.g. "local-lvm").
	Storage string `yaml:"storage" json:"storage"`
	// Size is the volume size, e.g. "8GiB". Only valid for rootfs and
	// mountpoints that PVE allocates; ignored for pre-existing paths.
	Size string `yaml:"size,omitempty" json:"size,omitempty"`
	// MountPoint overrides the PVE default mountpoint path. For rootfs
	// this is "/"; for mp0+ PVE defaults to /mnt/mp<i>.
	MountPoint string `yaml:"mount-point,omitempty" json:"mount-point,omitempty"`
}

// LXCDNS is PVE's LXC DNS configuration. PVE 9.2 accepts the following
// form-values on /lxc create + /config update (probe-verified):
//   - host        (settable on create and update)
//   - hostname    (settable on create; becomes pct.conf `hostname`)
//   - nameserver  (settable on create; "ns1,ns2" CSV form)
//   - searchdomain (settable on create; PVE normalizes to search-domain)
//
// The bare `dns=` and `ttys=` form-values are rejected on PVE 9.2 create
// (probe-verified) — those are UI helpers that PVE expands to the
// above four.
type LXCDNS struct {
	// HostName is PVE's `hostname` (the guest kernel hostname).
	// Defaults to metadata.name when unset.
	HostName string `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	// Nameservers is PVE's `nameserver` (CSV of IP addresses).
	Nameservers []string `yaml:"nameservers,omitempty" json:"nameservers,omitempty"`
	// Domain is PVE's `searchdomain` / `domain` (the DNS domain appended to
	// relative names).
	Domain string `yaml:"domain,omitempty" json:"domain,omitempty"`
}

// LXCMountOptions are per-mountpoint LXC options (M13). PVE 9.2's mpN
// grammar accepts these tokens inline on an allocated volume
// ("mp0=local-lvm:1,mp=/mnt/data,ro=1,backup=0,..."); the report re-echoes
// each token that differs from PVE's default (probe-verified on
// conformance-dev: acl/backup/mountoptions/quota/ro/shared all accepted and
// retained). Tri-state pointer-bools: nil = not owned (proxops neither sends
// the token nor drifts against a live value); non-nil = pinned 0/1.
type LXCMountOptions struct {
	// ReadOnly is PVE's `ro=` (default 0 = read-write).
	ReadOnly *bool `yaml:"read-only,omitempty" json:"read-only,omitempty"`
	// Backup is PVE's `backup=` (default 1 = included in vzdump).
	Backup *bool `yaml:"backup,omitempty" json:"backup,omitempty"`
	// ACL is PVE's `acl=` (default 0).
	ACL *bool `yaml:"acl,omitempty" json:"acl,omitempty"`
	// Quota is PVE's `quota=` (default 0).
	Quota *bool `yaml:"quota,omitempty" json:"quota,omitempty"`
	// Shared is PVE's `shared=` (default 0; marks the volume cluster-shared).
	Shared *bool `yaml:"shared,omitempty" json:"shared,omitempty"`
	// MountOptions is PVE's `mountoptions=` free-form mount(8) option list
	// (e.g. "noatime"). "" = not owned. PVE's default is "defaults".
	MountOptions string `yaml:"mount-options,omitempty" json:"mount-options,omitempty"`
}

// LXCOptions captures PVE LXC common options panel.
//
// Boolean PVE flags use pointer-bool tri-state: nil = "PVE decides" (not
// sent), true = `=1`, false = `=0`. This lets adopt faithfully capture an
// explicit `unprivileged=0` (LXC 111 on prod-a) instead of silently
// dropping it, which would otherwise make PVE fall back to its own default
// (= `unprivileged=1` on PVE 9.x) on the next recreate.
type LXCOptions struct {
	// Unprivileged is PVE's `unprivileged` (PCT 1 = default in PVE 9.x when
	// not set). nil = PVE decides; true = 1; false = 0.
	Unprivileged *bool `yaml:"unprivileged,omitempty" json:"unprivileged,omitempty"`
	// Protection is PVE's `protection` (prevents accidental destroy).
	Protection *bool `yaml:"protection,omitempty" json:"protection,omitempty"`
	// Nesting is PVE's `nesting` (allows nested LXC/VM).
	Nesting *bool `yaml:"nesting,omitempty" json:"nesting,omitempty"`
	// KeyCtl is PVE's `keyctl` (allows keyctl in guest).
	KeyCtl *bool `yaml:"keyctl,omitempty" json:"keyctl,omitempty"`
	// Fuse is PVE's `fuse` (allows FUSE mounts inside guest).
	Fuse *bool `yaml:"fuse,omitempty" json:"fuse,omitempty"`
	// OnBoot is PVE's `onboot` (auto-start on node boot).
	OnBoot *bool `yaml:"onboot,omitempty" json:"onboot,omitempty"`
	// Startup is PVE's `startup` (e.g. "order=10", "start=1").
	Startup string `yaml:"startup,omitempty" json:"startup,omitempty"`
	// TTYCount is PVE's `ttys=` (PVE 9.2 create rejects this; only
	// settable on /config update). proxops records the intent and
	// applies it at update time. When 0, proxops does NOT send it.
	TTYCount int `yaml:"ttys,omitempty" json:"ttys,omitempty"`
	// Console enables/updates PVE's `console=` option. nil = PVE decides;
	// true = `console=1`; false = `console=0`.
	Console *bool `yaml:"console,omitempty" json:"console,omitempty"`
}

// boolToPVE renders a tri-state LXC option into PVE's wire "0"/"1" /
// "" (absent). nil = not sent; caller checks separately.
func boolToPVE(b *bool) string {
	if b == nil {
		return ""
	}
	if *b {
		return "1"
	}
	return "0"
}

// LXCUnprivileged is PVE's "unprivileged" (1).
type LXCUnprivileged int

// LXCExtra mirrors VM's extra: freeform PVE passthrough keys.
type LXCExtra = map[string]string

// LXCMount is one additional LXC mountpoint (mp0, mp1, ...): an ALLOCATED
// storage volume (PVE's `mpN=<pool>:<GiB>,mp=<guest>` form). Host-path bind
// mounts are a distinct shape — see LXCBinding / spec.bind-mounts.
type LXCMount struct {
	// Storage is the PVE storage id with rootdir content.
	Storage string `yaml:"storage" json:"storage"`
	// Size is the allocated volume size, e.g. "10GiB".
	Size string `yaml:"size" json:"size"`
	// MountPoint is the in-guest path, e.g. "/mnt/data". PVE's default for
	// mp<i> is /mnt/mp<i>; set this to override.
	MountPoint string `yaml:"mount-point,omitempty" json:"mount-point,omitempty"`
	// Slot overrides the default mp<i>.
	Slot string `yaml:"slot,omitempty" json:"slot,omitempty"`
	// Options are the per-mount PVE tokens (ro/backup/acl/quota/shared/
	// mountoptions). nil = none owned.
	Options *LXCMountOptions `yaml:"options,omitempty" json:"options,omitempty"`
}

// LXCBinding is one host-path bind mount (M13): PVE's `mpN=<host-path>,
// mp=<guest-path>` form, where the "volume" is an existing directory on the
// PVE HOST rather than a volume proxops allocates.
//
// SAFETY (probe-verified PVE 9.2.2, conformance-dev): PVE restricts bind
// mountpoint writes to root@pam — an API-token request is rejected with
// HTTP 403 "mount point type bind is only allowed for root@pam" at BOTH
// create and /config PUT. proxops therefore:
//   - models bind mounts faithfully (declarative + adopted + drift-detected);
//   - NEVER deletes or re-points a live bind mount automatically (host data
//     could be damaged); divergences that would overwrite live state are
//     surfaced as anomalies;
//   - lets PVE enforce the permission: a write proxops cannot perform fails
//     closed with PVE's 403 on the action, not a silent skip.
//
// A bind mount shares the mpN slot namespace with allocated mount points:
// one slot is either an allocated volume or a bind, never both.
type LXCBinding struct {
	// HostPath is the directory on the PVE node that is bind-mounted into
	// the container. Must be absolute; PVE requires it to exist and to
	// contain no symlinks. proxops additionally refuses system-critical
	// roots (docs/pct.conf warning: never bind system dirs).
	HostPath string `yaml:"host-path" json:"host-path"`
	// MountPoint is the in-guest path, e.g. "/shared". Required.
	MountPoint string `yaml:"mount-point" json:"mount-point"`
	// Slot overrides the default mp<i> (i = index within bind-mounts,
	// continuing after spec.mount-points when both lists are present).
	Slot string `yaml:"slot,omitempty" json:"slot,omitempty"`
	// ReadOnly is PVE's `ro=` token on the bind (default 0 = read-write).
	// nil = not owned.
	ReadOnly *bool `yaml:"read-only,omitempty" json:"read-only,omitempty"`
}

// LXCSpec is the declarative LXC body.
type LXCSpec struct {
	// Node is the PVE node hosting this container.
	Node string `yaml:"node" json:"node"`
	// VMID is the PVE id (cid) — pinned.
	VMID int `yaml:"vmid" json:"vmid"`

	// Template is a proxops CTTemplate metadata.name that bootstraps the
	// container's rootfs. It is REQUIRED on create: PVE's /lxc create needs
	// an `ostemplate` volume and the declarative schema expresses that as a
	// reference to a CTTemplate manifest rather than a raw storage path. The
	// planner resolves it to `ostemplate=<storage>:vztmpl/<filename>` and
	// injects it into the create params.
	Template string `yaml:"template" json:"template"`

	// PveDescription is PVE's `description` (valid on create + update).
	PveDescription string `yaml:"pve-description,omitempty" json:"pve-description,omitempty"`
	// Tags are PVE user tags (proxops tag is auto-appended).
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// CPU.
	CPU LXCCPU `yaml:"cpu" json:"cpu"`
	// Memory (e.g. "2GiB"); PVE wire: MiB int.
	Memory string `yaml:"memory" json:"memory"`
	// Swap (PVE "swap"); default 16MiB if unset.
	Swap string `yaml:"swap,omitempty" json:"swap,omitempty"`
	// Root is the container's / volume.
	Root LXCRoot `yaml:"root" json:"root"`
	// MountPoints are additional LXC mp* volumes.
	MountPoints []LXCMount `yaml:"mount-points,omitempty" json:"mount-points,omitempty"`
	// BindMounts are host-path bind mounts declared on mp* slots (M13).
	// They share the mpN slot namespace with MountPoints; a slot is either
	// an allocated volume or a bind, never both. See LXCBinding for the
	// root@pam write restriction and proxops's fail-closed posture.
	BindMounts []LXCBinding `yaml:"bind-mounts,omitempty" json:"bind-mounts,omitempty"`
	// Networks.
	Networks []LXCNetwork `yaml:"networks" json:"networks"`
	// DNS is PVE's container DNS + hostname.
	DNS LXCDNS `yaml:"dns,omitempty" json:"dns,omitempty"`
	// Arch sets PVE's "arch" (create) — defaults to "amd64".
	Arch string `yaml:"arch,omitempty" json:"arch,omitempty"`
	// Options are PVE's common LXC options panel.
	Options LXCOptions `yaml:"options,omitempty" json:"options,omitempty"`

	// Extra freeform PVE passthrough (escape hatch).
	Extra map[string]string `yaml:"extra,omitempty" json:"extra"`

	// State is the desired power state: "started" (default) or "stopped".
	State string `yaml:"state,omitempty" json:"state,omitempty"`
}

// LXC is a schema.Resource for Kind=LXC.
type LXC struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       Kind     `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       LXCSpec  `yaml:"spec" json:"spec"`

	// ostemplateVolid is the PVE ostemplate wire value
	// ("local:vztmpl/x.tar.zst"), populated by ResolveArtifactRefs before
	// the planner runs.
	ostemplateVolid string

	// lxcDiskAnoms accumulates rootfs/mp pool/size drift anomalies during
	// Drift() that we refuse to auto-apply (data-loss guard).
	lxcDiskAnoms []string
}

// NewLXC returns an empty LXC.
func NewLXC() *LXC {
	return &LXC{Kind: KindLXC, APIVersion: APIVersion}
}

// Ref implements Resource.
func (l *LXC) Ref() Ref { return Ref{Kind: l.Kind, Name: l.Metadata.Name} }

// Node implements Resource.
func (l *LXC) Node() string { return l.Spec.Node }

// Nodes implements Resource — an LXC is pinned to a single PVE node.
func (l *LXC) Nodes() []string {
	if l.Spec.Node == "" {
		return nil
	}
	return []string{l.Spec.Node}
}

// ID implements Resource.
func (l *LXC) ID() int { return l.Spec.VMID }

// DesiredState implements Resource; returns spec.state or "started".
func (l *LXC) DesiredState() string {
	if s, err := ParseState(l.Spec.State); err == nil {
		return string(s)
	}
	return "started"
}

// Deps implements Resource. An LXC declares exactly one structured
// dependency: spec.template → a CTTemplate manifest. This edge is what
// makes "LXC cannot be created until the template has been downloaded".
func (l *LXC) Deps() []Ref {
	if l.Spec.Template == "" {
		return nil
	}
	return []Ref{{Kind: KindCTTemplate, Name: l.Spec.Template}}
}

// Validate implements Resource.
func (l *LXC) Validate() error {
	if l.Spec.Node == "" || !ValidNodeName(l.Spec.Node) {
		return fmt.Errorf("%s: spec.node must be a valid PVE node", l.Ref())
	}
	if l.Spec.VMID <= 0 {
		return fmt.Errorf("%s: spec.vmid must be > 0", l.Ref())
	}
	if l.Spec.CPU.Cores <= 0 {
		return fmt.Errorf("%s: spec.cpu.cores must be > 0", l.Ref())
	}
	if l.Spec.Memory == "" {
		return fmt.Errorf("%s: spec.memory must be set (e.g. 2GiB)", l.Ref())
	}
	if _, err := MemoryMiB(l.Spec.Memory); err != nil {
		return fmt.Errorf("%s: spec.memory: %w", l.Ref(), err)
	}
	if l.Spec.Swap != "" {
		if _, err := MemoryMiB(l.Spec.Swap); err != nil {
			return fmt.Errorf("%s: spec.swap: %w", l.Ref(), err)
		}
	}
	if l.Spec.Root.Storage == "" {
		return fmt.Errorf("%s: spec.root.storage must be set (a PVE storage id with rootdir content)", l.Ref())
	}
	if l.Spec.Root.Size == "" {
		return fmt.Errorf("%s: spec.root.size must be set (e.g. 32GiB)", l.Ref())
	}
	if _, err := DiskBytes(l.Spec.Root.Size); err != nil {
		return fmt.Errorf("%s: spec.root.size: %w", l.Ref(), err)
	}
	// template is required for creates; PVE's /lxc create needs an
	// ostemplate volume. The planner injects the resolved wire value.
	if l.Spec.Template == "" {
		return fmt.Errorf("%s: spec.template must reference a CTTemplate manifest (PVE /lxc create requires an ostemplate)", l.Ref())
	}
	if !ValidName(l.Spec.Template) {
		return fmt.Errorf("%s: spec.template %q is not a valid resource name", l.Ref(), l.Spec.Template)
	}
	// networks
	seenNet := map[string]bool{}
	for i := range l.Spec.Networks {
		n := &l.Spec.Networks[i]
		if n.Bridge == "" {
			return fmt.Errorf("%s: spec.networks[%d].bridge must be set", l.Ref(), i)
		}
		if n.Tag < 0 || n.Tag > 4094 {
			return fmt.Errorf("%s: spec.networks[%d].tag must be in [0, 4094]", l.Ref(), i)
		}
		if n.RateLimit < 0 {
			return fmt.Errorf("%s: spec.networks[%d].rate-limit must be non-negative", l.Ref(), i)
		}
		slot := n.Slot
		if slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		if seenNet[slot] {
			return fmt.Errorf("%s: duplicate net slot %q", l.Ref(), slot)
		}
		seenNet[slot] = true
		if !lxcValidNetSlot(slot) {
			return fmt.Errorf("%s: spec.networks[%d] slot %q invalid (use net0, net1, ...)", l.Ref(), i, slot)
		}
		n.Slot = slot
	}
	// mount points
	seenMp := map[string]bool{}
	for i := range l.Spec.MountPoints {
		m := &l.Spec.MountPoints[i]
		if m.Storage == "" {
			return fmt.Errorf("%s: spec.mount-points[%d].storage must be set", l.Ref(), i)
		}
		if m.Size == "" {
			return fmt.Errorf("%s: spec.mount-points[%d].size must be set", l.Ref(), i)
		}
		if _, err := DiskBytes(m.Size); err != nil {
			return fmt.Errorf("%s: spec.mount-points[%d].size: %w", l.Ref(), i, err)
		}
		if m.MountPoint != "" && !strings.HasPrefix(m.MountPoint, "/") {
			return fmt.Errorf("%s: spec.mount-points[%d].mount-point %q must be an absolute path", l.Ref(), i, m.MountPoint)
		}
		slot := m.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", i)
		}
		if seenMp[slot] {
			return fmt.Errorf("%s: duplicate mount point %q", l.Ref(), slot)
		}
		seenMp[slot] = true
		m.Slot = slot
	}
	// bind mounts (M13): host-path binds on mp* slots. They share the mpN
	// slot namespace with spec.mount-points, so slot collisions across the
	// two lists fail closed.
	for i := range l.Spec.BindMounts {
		b := &l.Spec.BindMounts[i]
		if !strings.HasPrefix(b.HostPath, "/") {
			return fmt.Errorf("%s: spec.bind-mounts[%d].host-path %q must be an absolute path on the PVE node", l.Ref(), i, b.HostPath)
		}
		if !strings.HasPrefix(b.MountPoint, "/") {
			return fmt.Errorf("%s: spec.bind-mounts[%d].mount-point %q must be an absolute path in the guest", l.Ref(), i, b.MountPoint)
		}
		if lxcBindHostPathUnsafe(b.HostPath) {
			return fmt.Errorf("%s: spec.bind-mounts[%d].host-path %q is a system-critical host directory; PVE's pct.conf warns never to bind-mount system dirs (a misconfigured container can damage the host) — choose a dedicated directory", l.Ref(), i, b.HostPath)
		}
		slot := b.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", len(l.Spec.MountPoints)+i)
		}
		if seenMp[slot] {
			return fmt.Errorf("%s: duplicate mount point slot %q (spec.mount-points and spec.bind-mounts share the mpN namespace)", l.Ref(), slot)
		}
		seenMp[slot] = true
		b.Slot = slot
	}
	// arch
	if l.Spec.Arch != "" && l.Spec.Arch != "amd64" && l.Spec.Arch != "i686" && l.Spec.Arch != "arm64" {
		return fmt.Errorf("%s: spec.arch %q must be amd64|i686|arm64 (PVE 9.2)", l.Ref(), l.Spec.Arch)
	}
	// extra keys must not collide with PVE core keys we already handle.
	structuredReserved := map[string]bool{
		"vmid": true, "ctid": true, "name": true, "memory": true, "core_count": true,
		"cores": true, "rootfs": true, "ostemplate": true, "ostype": true, "arch": true,
		"hostname": true, "nameserver": true, "searchdomain": true, "unprivileged": true,
		"onboot": true, "protection": true, "nesting": true, "keyctl": true, "fuse": true,
		"start": true, "tags": true,
	}
	for k := range l.Spec.Extra {
		if strings.HasPrefix(k, "net") || strings.HasPrefix(k, "mp") {
			return fmt.Errorf("%s: spec.extra key %q conflicts with a structured field; remove it", l.Ref(), k)
		}
		if structuredReserved[k] {
			return fmt.Errorf("%s: spec.extra key %q conflicts with a structured field; remove it", l.Ref(), k)
		}
	}
	if l.Spec.State != "" {
		if _, err := ParseState(l.Spec.State); err != nil {
			return fmt.Errorf("%s: %w", l.Ref(), err)
		}
	}
	return nil
}

// ToCreateParams returns PVE wire form-values for POST /nodes/{n}/lxc.
//
// PVE 9.2 /lxc create grammar (probe-verified on conformance-dev):
//   - `name` and `os` are NOT valid create form-values (rejected with
//     "property is not defined in schema" — PVE 9.x renamed the field
//     surface; `hostname` is the valid create-time field; `ostype` is
//     auto-inferred from `ostemplate`).
//   - `dns`, `ttys`, `hwclock` are also rejected on create (they are PVE
//     UI helpers that expand to `hostname` + `nameserver` + `searchdomain`);
//     use the three valid form keys instead.
//   - `ostemplate=<pool>:vztmpl/<filename>` is required; proxops does
//     NOT emit it here — the planner resolves LXC.spec.template against
//     the CTTemplate manifest and injects the value (see plan.resolveDeps).
//   - net<i> must carry `name=<iface>` first (probe-verified: PVE 9.x
//     rejects bare-model LXC NICs with "value without key, but schema does
//     not define a default key").
func (l *LXC) ToCreateParams() (map[string]any, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	p := map[string]any{
		"vmid":   l.Spec.VMID,
		"cores":  l.Spec.CPU.Cores,
		"memory": memMiB(l.Spec.Memory),
		"tags":   strings.Join(l.allTags(), ","),
		"start":  "0",
	}
	// ostemplate: REQUIRED on PVE 9.2 /lxc create. When the planner
	// resolves the LXC's template ref, it sets OstemplateVolid to
	// "<storage>:vztmpl/<filename>" and proxops emits it here.
	if l.ostemplateVolid != "" {
		p["ostemplate"] = l.ostemplateVolid
	}
	// hostname: PVE's /lxc create `hostname` (NOT `name` on PVE 9.2).
	// Defaults to metadata.name so PVE's display name matches git.
	if dnsHost := strings.TrimSpace(l.Spec.DNS.HostName); dnsHost != "" {
		p["hostname"] = dnsHost
	} else {
		p["hostname"] = l.Metadata.Name
	}
	if len(l.Spec.DNS.Nameservers) > 0 {
		p["nameserver"] = strings.Join(l.Spec.DNS.Nameservers, ",")
	}
	if l.Spec.DNS.Domain != "" {
		p["searchdomain"] = l.Spec.DNS.Domain
	}
	if l.Spec.PveDescription != "" {
		p["description"] = l.Spec.PveDescription
	}
	// Rootfs: PVE's LXC create-time allocation form is "STORAGE_ID:SIZE_
	// IN_GiB" (pct.conf(5)); PVE rewrites to "<pool>:vm-<cid>-disk-0,size=
	// <binary>" after allocation. (Probe-verified: the bare number is GiB.)
	p["rootfs"] = lxcLVMAlloc(l.Spec.Root.Storage, l.Spec.Root.Size)
	if l.Spec.Arch != "" {
		p["arch"] = l.Spec.Arch
	}
	if l.Spec.Swap != "" {
		p["swap"] = memMiB(l.Spec.Swap)
	}
	// Additional mount points.
	for i, m := range l.Spec.MountPoints {
		slot := m.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", i)
		}
		p[slot] = lxcMountCreateWire(m)
	}
	// Bind mounts (M13): PVE's `mpN=<host-path>,mp=<guest>` form. PVE
	// restricts bind writes to root@pam (probe-verified 403 with an API
	// token); proxops submits the declaration and fails closed on PVE's
	// rejection rather than pretending convergence.
	for i, b := range l.Spec.BindMounts {
		slot := b.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", len(l.Spec.MountPoints)+i)
		}
		p[slot] = lxcBindCreateWire(b)
	}
	// LXC netX: PVE 9.x requires name=<iface> at the head of the property
	// list. See lxcNetString.
	for i, n := range l.Spec.Networks {
		slot := n.Slot
		if slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		p[slot] = lxcNetString(n)
	}
	// Container options. Tri-state pointer-bools: nil = PVE decides (not
	// sent); non-nil = emit the explicit 0 or 1.
	//
	// PVE 9.2 LXC create wire grammar (probe-verified on conformance-dev
	// 2026-09-10, disposable CTs 9870-9877 / 9880-9882, all destroyed):
	//   - TOP-LEVEL create keys accepted: unprivileged, protection, onboot,
	//     console (all as "0"/"1").
	//   - TOP-LEVEL create keys REJECTED: nesting (400 "property is not
	//     defined in schema"), keyctl (403), fuse (403). The PVE 9.x
	//     `features` composite is the ONLY accepted nesting form:
	//     `features=nesting=1` creates fine; the report re-echoes the same
	//     token.
	// So the create-time form:
	//   - nesting (when set) is folded into `features=nesting=<0|1>`;
	//   - keyctl/fuse set TRUE are a manifest error (no PVE 9.x create form
	//     exists to satisfy them) — fail closed instead of submitting a
	//     400/403 create;
	//   - keyctl/fuse set FALSE are PVE defaults and are not sent.
	if s := boolToPVE(l.Spec.Options.Unprivileged); s != "" {
		p["unprivileged"] = s
	}
	if s := boolToPVE(l.Spec.Options.Protection); s != "" {
		p["protection"] = s
	}
	if s := boolToPVE(l.Spec.Options.OnBoot); s != "" {
		p["onboot"] = s
	}
	if s := boolToPVE(l.Spec.Options.Console); s != "" {
		p["console"] = s
	}
	if l.Spec.Options.Nesting != nil {
		p["features"] = lxcFeaturesCreate(l.Spec.Options.Nesting)
	}
	if l.Spec.Options.KeyCtl != nil && *l.Spec.Options.KeyCtl {
		return nil, fmt.Errorf("%s: spec.options.keyctl=true has no PVE 9.x LXC create form (POST /lxc rejects top-level keyctl with 403 and the features composite has no keyctl token); create the container and enable keyctl on PVE, or drop spec.options.keyctl", l.Ref())
	}
	if l.Spec.Options.Fuse != nil && *l.Spec.Options.Fuse {
		return nil, fmt.Errorf("%s: spec.options.fuse=true has no PVE 9.x LXC create form (POST /lxc rejects top-level fuse with 403 and the features composite has no fuse token); create the container and enable fuse on PVE, or drop spec.options.fuse", l.Ref())
	}
	if l.Spec.Options.Startup != "" {
		p["startup"] = l.Spec.Options.Startup
	}
	for k, v := range l.Spec.Extra {
		p[k] = v
	}
	return p, nil
}

// lxcLVMAlloc renders PVE's LXC rootfs/mp create-time allocation form
// "<pool>:<GiB>" (same unit contract as the QEMU `scsiN` volume spec).
func lxcLVMAlloc(pool, size string) string {
	return fmt.Sprintf("%s:%s", pool, GiBString(diskBytes(size)))
}

// lxcMountCreateWire renders an allocated mpN create-form value:
// "<pool>:<GiB>[,mp=<path>][,<options>]".
func lxcMountCreateWire(m LXCMount) string {
	val := lxcLVMAlloc(m.Storage, m.Size)
	if m.MountPoint != "" {
		val += ",mp=" + m.MountPoint
	}
	return val + lxcMountOptionTokens(m.Options)
}

// lxcMountOptionTokens renders the owned per-mount option tokens in a stable
// order. nil / unset = not owned → omitted (PVE decides).
func lxcMountOptionTokens(o *LXCMountOptions) string {
	if o == nil {
		return ""
	}
	var b strings.Builder
	if o.ReadOnly != nil {
		b.WriteString(",ro=" + lxcBoolWire(*o.ReadOnly))
	}
	if o.Backup != nil {
		b.WriteString(",backup=" + lxcBoolWire(*o.Backup))
	}
	if o.ACL != nil {
		b.WriteString(",acl=" + lxcBoolWire(*o.ACL))
	}
	if o.Quota != nil {
		b.WriteString(",quota=" + lxcBoolWire(*o.Quota))
	}
	if o.Shared != nil {
		b.WriteString(",shared=" + lxcBoolWire(*o.Shared))
	}
	if o.MountOptions != "" {
		b.WriteString(",mountoptions=" + o.MountOptions)
	}
	return b.String()
}

// lxcBindCreateWire renders a bind-mount mpN create-form value:
// "<host-path>,mp=<guest-path>[,ro=<0|1>]".
func lxcBindCreateWire(b LXCBinding) string {
	s := b.HostPath + ",mp=" + b.MountPoint
	if b.ReadOnly != nil {
		s += ",ro=" + lxcBoolWire(*b.ReadOnly)
	}
	return s
}

// lxcBindHostPathUnsafe reports whether a host path is a system-critical
// directory that pct.conf(5) warns must never be bind-mounted into a
// container (a misconfigured or escaped container could damage the host).
// proxops fails closed on these at validation.
func lxcBindHostPathUnsafe(p string) bool {
	clean := strings.TrimRight(p, "/")
	if clean == "" {
		return true // "/"
	}
	switch clean {
	case "/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32",
		"/proc", "/root", "/run", "/sbin", "/srv", "/sys", "/usr", "/var":
		return true
	}
	return false
}

// lxcBindParseLive splits PVE's live bind-mount mpN report value into
// (hostPath, guestPath, ro, ok). PVE reports a bind as
// "<host-path>,mp=<guest-path>[,ro=1]". ok=false when the value is not in
// bind form (an allocated volume).
func lxcBindParseLive(raw string) (host, guest string, ro *bool, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || !isLXCBindMount(raw) {
		return "", "", nil, false
	}
	toks := strings.Split(raw, ",")
	host = strings.TrimSpace(toks[0])
	for _, t := range toks[1:] {
		t = strings.TrimSpace(t)
		if v, found := strings.CutPrefix(t, "mp="); found {
			guest = strings.TrimSpace(v)
		} else if v, found := strings.CutPrefix(t, "ro="); found {
			b := v == "1" || v == "true"
			ro = &b
		} else if t == "ro" {
			// Legacy bare-flag form ("mpN=<host>:<guest>,ro").
			b := true
			ro = &b
		}
	}
	// Legacy/colon bind spelling ("<host>:<guest>" with no mp= token): split
	// the leading token on its first colon so the host path is still
	// recovered. PVE 9.2 reports the comma + mp= form; this keeps adoption
	// faithful against older reports.
	if guest == "" {
		if h, g, found := strings.Cut(host, ":"); found && strings.HasPrefix(g, "/") {
			host, guest = strings.TrimSpace(h), strings.TrimSpace(g)
		}
	}
	return host, guest, ro, true
}

// lxcNetString builds PVE's LXC "netN" value. PVE 9.2 create requires
// `name=<iface>` as the first option (bare model names are rejected:
// "invalid format - value without key, but schema does not define a default
// key"). PVE auto-fills `type=veth` and assigns a random hwaddr when they
// are omitted.
//
//   - iface   = <user-declared> || "net<i>" (default PVE wire name)
//   - bridge  = REQUIRED on LXC networks (validated in Validate)
//   - tag     = PVE 8.2+ VLAN tag on the LXC NIC (optional)
//   - hwaddr  = pinned MAC (optional)
//   - rate    = MBit/s rate limit (optional)
//   - firewall= PVE LXC firewall on/off (optional)
func lxcNetString(n LXCNetwork) string {
	iface := strings.TrimSpace(n.Iface)
	if iface == "" {
		iface = n.Slot
		if iface == "" {
			iface = "net0"
		}
	}
	var sb strings.Builder
	sb.WriteString("name=" + iface)
	if t := strings.TrimSpace(n.Type); t != "" {
		sb.WriteString(",type=" + t)
	}
	if n.Bridge != "" {
		sb.WriteString(",bridge=" + n.Bridge)
	}
	if n.Tag > 0 {
		fmt.Fprintf(&sb, ",tag=%d", n.Tag)
	}
	if m := strings.ToLower(strings.TrimSpace(n.HWAddr)); m != "" {
		sb.WriteString(",hwaddr=" + m)
	}
	if n.RateLimit > 0 {
		fmt.Fprintf(&sb, ",rate=%d", n.RateLimit)
	}
	if n.Firewall {
		sb.WriteString(",firewall=1")
	}
	// ip / gw are user-set static addressing; PVE echoes them back on the
	// /config report when set. Not emitted when empty (PVE decides).
	if s := strings.TrimSpace(n.Ip); s != "" {
		sb.WriteString(",ip=" + s)
	}
	if s := strings.TrimSpace(n.Gw); s != "" {
		sb.WriteString(",gw=" + s)
	}
	return sb.String()
}

// lxcNetFields is the parsed form of an LXC netX property string.
type lxcNetFields struct {
	iface    string
	typ      string
	bridge   string
	tag      int
	hwaddr   string
	rate     int
	firewall bool
	ip       string
	gw       string
}

// parseLXCNetFields parses an LXC netX property string. PVE's report form
// (create-time is the same grammar) is "name=wired0,bridge=vmbr0,hwaddr=
// 52:...,type=veth,ip=192.168.192.110/18,gw=192.168.192.5". PVE always
// normalizes type to veth unless a non-veth type was requested. PVE's own
// LXC create API accepts ip=/gw= as static address + gateway; the web UI
// exposes both on a per-NIC basis.
func parseLXCNetFields(s string) lxcNetFields {
	out := lxcNetFields{}
	for _, kv := range strings.Split(s, ",") {
		k, v, found := strings.Cut(strings.TrimSpace(kv), "=")
		if !found {
			continue
		}
		switch k {
		case "name":
			out.iface = v
		case "type":
			out.typ = v
		case "bridge":
			out.bridge = v
		case "tag":
			out.tag, _ = strconv.Atoi(v)
		case "hwaddr":
			out.hwaddr = strings.ToLower(v)
		case "rate":
			out.rate, _ = strconv.Atoi(v)
		case "firewall":
			out.firewall = v == "1" || v == "true"
		case "ip":
			out.ip = v
		case "gw":
			out.gw = v
		}
	}
	return out
}

// lxcNetMatches reports whether PVE's LXC netX report satisfies the owned
// fields of the desired LXCNetwork. PVE auto-fills type=veth and assigns a
// random hwaddr; we don't own those unless the user pinned them.
//
// Ownership model:
//   - iface (only when desired set it; PVE defaults to "net0" → own)
//   - bridge (only when desired set it; validated as required)
//   - tag / rate / firewall (only when desired set them non-zero)
//   - hwaddr (only when desired pinned it — PVE auto-assigns otherwise)
func lxcNetMatches(cur string, n LXCNetwork) bool {
	got := parseLXCNetFields(cur)
	// iface: if user did not declare one, proxops defaulted to net<N>
	// and PVE likely defaulted differently ("wired0" on PVE 9.2 web UI).
	// We only compare when explicit.
	if strings.TrimSpace(n.Iface) != "" && got.iface != strings.TrimSpace(n.Iface) {
		return false
	}
	if n.Bridge != "" && got.bridge != n.Bridge {
		return false
	}
	if n.Tag > 0 && got.tag != n.Tag {
		return false
	}
	if m := strings.ToLower(strings.TrimSpace(n.HWAddr)); m != "" && got.hwaddr != m {
		return false
	}
	if n.RateLimit > 0 && got.rate != n.RateLimit {
		return false
	}
	if n.Firewall && !got.firewall {
		return false
	}
	// ip / gw are user-set static addressing (probe-verified PVE 9.x
	// netX property form: net0=name=eth0,bridge=vmbr2,ip=192.168.192.110/
	// 18,gw=192.168.192.5,...). They round-trip cleanly so we compare them
	// as owned tokens — an empty desired Ip/Gw does NOT assert "PVE must
	// have no static addressing" (PVE omits the token when unset, so
	// comparing empty-desired-vs-absent live = match; empty-desired-vs-
	// present-live = PVE had a static address we did not want, but that
	// would be a PVE-side hand-set not proxops-side: treat as owned
	// only when desired was set).
	if s := strings.TrimSpace(n.Ip); s != "" && got.ip != s {
		return false
	}
	if s := strings.TrimSpace(n.Gw); s != "" && got.gw != s {
		return false
	}
	return true
}

// lxcValidNetSlot reports whether s is a valid LXC net slot name.
func lxcValidNetSlot(s string) bool {
	return strings.HasPrefix(s, "net") && len(strings.TrimPrefix(s, "net")) > 0
}

// Drift implements Resource.
//
// LXC's owned-field set is narrower than VM's because PVE 9.2 auto-
// synthesizes several values PVE owns (hwaddr, ostype, rootfs volume
// name, netX type). The owned fields are:
//
//   - cores, memory, swap, arch (PVE's LXC /config)
//   - hostname, nameserver, searchdomain (PVE's "name" was renamed; the
//     wire key on /config is still "hostname")
//   - rootfs pool + size
//   - netX bridge / tag / rate / firewall / pinned hwaddr
//   - unprivileged, onboot, protection, startup, nesting, keyctl, fuse
//   - tags
//
// PVE's LXC /lxc/{cid}/config report uses "hostname", "nameserver",
// "searchdomain" as field names (probe-verified on PVE 9.2, identical to
// the create-side keys). This is different from PVE 8, where the report
// used "name" — proxops only targets PVE 9.x.

// lxcDiskSlotDrift classifies one LXC storage slot (rootfs or mpN) against
// the desired pool/size, applying the same data-loss guard as VM disks:
//   - no live volume at the slot      -> safe create write
//   - live volume, different pool/size -> NON-destructive anomaly: PVE
//     /config re-creates the LVM volume on a pool/size write (the old
//     volume and its data are deleted). proxops refuses to do that.
func (l *LXC) lxcDiskSlotDrift(slot, wantWire, curWire string) (map[string]any, bool, []string) {
	upd := map[string]any{}
	anoms := make([]string, 0, 1)
	cur := parseDiskInfo(curWire)
	want := parseDiskInfo(wantWire)
	// "New slot" == PVE has NOT allocated storage at this slot (report
	// value absent/empty, or PVE's bare "none" form). ONLY then is a
	// create-form write safe. Any non-empty PVE value — a live volume id,
	// a bare "pool:2G"-form allocation without a PVE-assigned volume
	// name, even a malformed string — is a live allocation: an auto
	// rewrite would recreate the volume and destroy its data (probed on
	// conformance-dev, PVE 9.2), so pool/size/storage mismatch below is
	// a non-destructive anomaly, not a write.
	if isNewStorageSlot(curWire) {
		if !diskMatches(cur, want) {
			upd[slot] = wantWire
		}
		return upd, len(upd) > 0, anoms
	}
	poolChanged := cur.pool != want.pool
	sizeChanged := want.sizeSet && (cur.sizeSet && cur.sizeBytes != want.sizeBytes)
	if poolChanged || sizeChanged {
		// Surface the desired pool+size for the operator, not the live wire.
		desiredPool := want.pool
		desiredSize := ""
		if want.sizeSet {
			desiredSize = fmt.Sprintf("%d bytes", want.sizeBytes)
		}
		anoms = append(anoms, fmt.Sprintf(
			"%s storage/size drift (live=%q; desired pool=%s size=%s); proxops will NOT auto-resize or re-pool a live LXC volume (PVE /config would recreate the volume and lose its data) — resize deliberately on PVE, then update the manifest",
			slot, curWire, desiredPool, desiredSize))
	}
	return upd, len(upd) > 0, anoms
}

// lxcMountDrift classifies one ALLOCATED mount-point slot (mpN) against the
// desired LXCMount. Same data-loss guard as rootfs (pool/size drift on a
// live volume = anomaly), plus the M13 option surface:
//   - guest mount path (`mp=`) and the per-mount option tokens (ro/backup/
//     acl/quota/shared/mountoptions) converge with a LIVE-form rewrite
//     that preserves PVE's volume id + size spelling (probe-verified PVE
//     9.2.2: the live form toggles mp path and options in place, 200, no
//     volume recreation).
//   - a live bind mount at a slot the manifest declares as allocated is an
//     anomaly (replacing a bind with an allocated volume would destroy the
//     bind's configuration and could invite writes onto the wrong target).
func lxcMountDrift(slot string, m LXCMount, curWire string) (map[string]any, bool, []string) {
	upd := map[string]any{}
	anoms := make([]string, 0, 1)
	if isNewStorageSlot(curWire) {
		upd[slot] = lxcMountCreateWire(m)
		return upd, true, anoms
	}
	if isLXCBindMount(curWire) {
		anoms = append(anoms, fmt.Sprintf(
			"%s is a live host-path bind mount (%q) but spec.mount-points declares an allocated volume — proxops will not replace a live bind mount (host data); reconcile the manifest with PVE or remove the bind deliberately",
			slot, curWire))
		return upd, false, anoms
	}
	// pool/size guard first (reuses the shared classifier's logic inline so
	// the option rewrite below only runs on a storage-matched volume).
	cur := parseDiskInfo(curWire)
	want := parseDiskInfo(lxcMountCreateWire(m))
	if cur.pool != want.pool || (want.sizeSet && cur.sizeSet && cur.sizeBytes != want.sizeBytes) {
		desiredSize := ""
		if want.sizeSet {
			desiredSize = fmt.Sprintf("%d bytes", want.sizeBytes)
		}
		anoms = append(anoms, fmt.Sprintf(
			"%s storage/size drift (live=%q; desired pool=%s size=%s); proxops will NOT auto-resize or re-pool a live LXC volume (PVE /config would recreate the volume and lose its data) — resize deliberately on PVE, then update the manifest",
			slot, curWire, want.pool, desiredSize))
		return upd, false, anoms
	}
	// mp path + option tokens.
	rewrite, need := lxcMountLiveRewrite(curWire, m)
	if need {
		upd[slot] = rewrite
		return upd, true, anoms
	}
	return upd, false, anoms
}

// lxcMountLiveRewrite compares PVE's live allocated-mp value against the
// desired mount path/options and, when any OWNED piece diverges, renders the
// safe live-form rewrite (PVE's volume id + size spelling preserved, owned
// tokens overridden, non-owned tokens carried verbatim).
func lxcMountLiveRewrite(curWire string, m LXCMount) (string, bool) {
	cur := parseDiskInfo(curWire)
	// mp= and the option tokens land in cur.rawExtra via parseDiskInfo;
	// re-extract the ones we own from the raw string.
	liveMP := lxcRawToken(curWire, "mp=")
	need := m.MountPoint != "" && liveMP != "" && m.MountPoint != liveMP
	if o := m.Options; o != nil {
		for _, c := range []struct {
			want *bool
			tok  string
			def  bool
		}{
			{o.ReadOnly, "ro=", false},
			{o.Backup, "backup=", false},
			{o.ACL, "acl=", false},
			{o.Quota, "quota=", false},
			{o.Shared, "shared=", false},
		} {
			if c.want == nil {
				continue
			}

			lv, present := lxcRawBoolToken(curWire, c.tok)
			eff := c.def
			if present {
				eff = lv
			}

			if eff != *c.want {
				need = true
			}
		}

		if o.MountOptions != "" {
			if lv := lxcRawToken(curWire, "mountoptions="); lv != "" && lv != o.MountOptions {
				need = true
			}
		}
	}

	if !need {
		return "", false
	}
	s := cur.pool + ":" + cur.volumeName
	if cur.sizeSet {
		s += ",size=" + cur.sizeToken
	}
	mp := liveMP
	if m.MountPoint != "" {
		mp = m.MountPoint
	}
	if mp != "" {
		s += ",mp=" + mp
	}
	s += lxcMountOptionTokens(m.Options)
	// Carry every live token we did NOT own or override.
	s += lxcRawTokensExcept(curWire, "mp", "ro", "backup", "acl", "quota", "shared", "mountoptions", "size")
	return s, true
}

// lxcRawToken returns the value of a "k=v" token in PVE's mpN report value,
// "" when absent.
func lxcRawToken(raw, kv string) string {
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if v, found := strings.CutPrefix(t, kv); found {
			return v
		}
	}
	return ""
}

// lxcRawBoolToken returns the bool value of a "k=<0|1>" token plus whether
// the token was present at all.
func lxcRawBoolToken(raw, kv string) (bool, bool) {
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if v, found := strings.CutPrefix(t, kv); found {
			return v == "1" || v == "true", true
		}
	}
	return false, false
}

// lxcRawTokensExcept re-emits every "k=v" token of PVE's mpN value except
// the named keys (and the leading volume token / size token), preserving
// PVE's verbatim spelling for the tokens proxops does not own.
func lxcRawTokensExcept(raw string, except ...string) string {
	skip := map[string]bool{"mp": true}
	for _, e := range except {
		skip[e] = true
	}
	var out []string
	for i, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if i == 0 {
			continue // the volume token (pool:volid)
		}
		eq := strings.IndexByte(t, '=')
		if eq <= 0 {
			out = append(out, t)
			continue
		}
		if skip[t[:eq]] {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return ""
	}
	return "," + strings.Join(out, ",")
}

// lxcBindDrift classifies one BIND-mount slot against the desired
// LXCBinding. Posture (M13, probe-verified PVE 9.2.2):
//   - empty slot            -> create-form write. PVE restricts bind writes
//     to root@pam (API tokens get HTTP 403), so with a token identity the
//     action FAILS CLOSED at apply time and surfaces as a failed action —
//     proxops never reports false convergence.
//   - live allocated volume -> anomaly: swapping a bind for a volume (or
//     vice versa) touches data-bearing storage; never automatic.
//   - live bind, different HOST path -> anomaly: re-pointing a bind exposes
//     a different host directory to the container (potential host-data
//     damage); operator must reconcile.
//   - live bind, same host path     -> guest path + ro converge in place.
func lxcBindDrift(slot string, b LXCBinding, curWire string) (map[string]any, bool, []string) {
	upd := map[string]any{}
	anoms := make([]string, 0, 1)
	if isNewStorageSlot(curWire) {
		upd[slot] = lxcBindCreateWire(b)
		return upd, true, anoms
	}
	if !isLXCBindMount(curWire) {
		anoms = append(anoms, fmt.Sprintf(
			"%s is a live allocated volume (%q) but spec.bind-mounts declares a host-path bind — proxops will not swap a bind mount and a storage volume on one slot (data safety); reconcile the manifest or move one off this slot",
			slot, curWire))
		return upd, false, anoms
	}
	liveHost, liveGuest, liveRO, _ := lxcBindParseLive(curWire)
	if liveHost != "" && strings.TrimRight(liveHost, "/") != strings.TrimRight(b.HostPath, "/") {
		anoms = append(anoms, fmt.Sprintf(
			"%s bind host path drift (live=%q; desired=%q); proxops will NOT re-point a live bind mount — exposing a different host directory to a running container can damage host data; reconcile deliberately on PVE",
			slot, liveHost, b.HostPath))
		return upd, false, anoms
	}
	need := b.MountPoint != "" && liveGuest != "" && b.MountPoint != liveGuest
	if b.ReadOnly != nil {
		eff := false
		if liveRO != nil {
			eff = *liveRO
		}
		if eff != *b.ReadOnly {
			need = true
		}
	}
	if need {
		upd[slot] = lxcBindCreateWire(b)
		return upd, true, anoms
	}
	return upd, false, anoms
}

// DriftAnomalies surfaces live-only LXC mount-point slots (mp*) that the
// manifest does not declare. Same semantics as VM.DriftAnomalies for disks:
// proxops will not automatically delete a live-only mount point (PVE's
// `mpN=none` is detach-only, not volume-destroy), and a manifest-author
// unaware of a hand-added mount point is exactly the shape we want to
// surface on /status rather than silently delete.
func (l *LXC) DriftAnomalies(current map[string]any) []string {
	if current == nil {
		return nil
	}
	want := map[string]bool{}
	for i, m := range l.Spec.MountPoints {
		slot := m.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", i)
		}
		want[slot] = true
	}
	for i, b := range l.Spec.BindMounts {
		slot := b.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", len(l.Spec.MountPoints)+i)
		}
		want[slot] = true
	}
	out := make([]string, 0, 2)
	for k, raw := range current {
		if !isMPSlot(k) {
			continue
		}
		if !want[k] && !isNoneSlot(pveStr(raw)) {
			out = append(out, fmt.Sprintf("live-only LXC mountpoint slot %s=%s is not in spec.mount-points or spec.bind-mounts; proxops will not automatically remove it", k, pveStr(raw)))
		}
	}
	out = append(out, l.lxcDiskAnoms...)
	return out
}

// isMPSlot matches PVE's LXC mount-point slot naming: mp*.
func isMPSlot(k string) bool {
	if len(k) < 3 || k[:2] != "mp" {
		return false
	}
	for _, c := range k[2:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (l *LXC) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		return nil, false, false
	}
	upd := map[string]any{}
	stop := false

	// cores
	if got := pveInt(current["cores"]); got != l.Spec.CPU.Cores {
		upd["cores"] = l.Spec.CPU.Cores
		stop = true
	}
	// memory: PVE's LXC /config integer MiB count (pct.conf: "in MB").
	if want, err := MemoryMiB(l.Spec.Memory); err == nil {
		if got := pveInt(current["memory"]); got != int(want) {
			upd["memory"] = want
			stop = true
		}
	}
	// swap
	if l.Spec.Swap != "" {
		if want, err := MemoryMiB(l.Spec.Swap); err == nil {
			if got := pveInt(current["swap"]); got != int(want) {
				upd["swap"] = want
				stop = true
			}
		}
	}
	// arch: PVE creates the container with arch=amd64 even when the user
	// did not set it. Compare only when declared.
	if l.Spec.Arch != "" && pveStr(current["arch"]) != l.Spec.Arch {
		upd["arch"] = l.Spec.Arch
		stop = true
	}
	// hostname: PVE's wire key is hostname on both create and /config
	// (probe-verified PVE 9.2). When the user did not declare a hostname,
	// proxops sends metadata.name and PVE stores it verbatim, so the
	// comparison is exact.
	wantHost := strings.TrimSpace(l.Spec.DNS.HostName)
	if wantHost == "" {
		wantHost = l.Metadata.Name
	}
	if pveStr(current["hostname"]) != wantHost {
		upd["hostname"] = wantHost
	}
	// nameserver: PVE accepts comma-separated CSV on create/update
	// ("1.1.1.1,8.8.8.8") but reports space-separated on /config
	// ("1.1.1.1 8.8.8.8"). Normalize both sides and compare as a set
	// so PVE's whitespace reformat does not trip drift.
	if len(l.Spec.DNS.Nameservers) > 0 {
		want := strings.Join(l.Spec.DNS.Nameservers, ",")
		if !nameserverEquals(pveStr(current["nameserver"]), want) {
			upd["nameserver"] = want
		}
	}
	// searchdomain
	if l.Spec.DNS.Domain != "" {
		if pveStr(current["searchdomain"]) != l.Spec.DNS.Domain {
			upd["searchdomain"] = l.Spec.DNS.Domain
		}
	}
	// description: PVE reports descriptions with a trailing newline when
	// the manifest set a newline; trim both sides so a trailing \n does
	// not trip drift.
	if l.Spec.PveDescription != "" {
		if strings.TrimSpace(pveStr(current["description"])) != strings.TrimSpace(l.Spec.PveDescription) {
			upd["description"] = l.Spec.PveDescription
		}
	}
	// tags
	if !tagsEqual(current["tags"], l.allTags()) {
		upd["tags"] = strings.Join(l.allTags(), ",")
	}
	// rootfs + mount points: data-loss guard (PVE 9.2, probed 2026-09-08).
	// Changing a live LXC rootfs/mp pool or size via /config re-creates the
	// LVM volume (the old one is deleted → data loss). A pool/size drift on a
	// LIVE volume is therefore reported as a NON-destructive anomaly; proxops
	// never auto-resizes a data-bearing LXC rootfs/mountpoint. Adding a brand-
	// new volume (no live one at the slot) is safe and is applied.
	// Re-derive anomalies on every Drift call (Drift may run more than once
	// per cycle; lxcDiskAnoms must not accumulate duplicates).
	l.lxcDiskAnoms = nil
	rootUpd, rootStop, rootAnoms := l.lxcDiskSlotDrift("rootfs", lxcLVMAlloc(l.Spec.Root.Storage, l.Spec.Root.Size), pveStr(current["rootfs"]))
	if v, ok := rootUpd["rootfs"]; ok {
		upd["rootfs"] = v
	}
	if rootStop {
		stop = true
	}
	l.lxcDiskAnoms = append(l.lxcDiskAnoms, rootAnoms...)
	for i, m := range l.Spec.MountPoints {
		slot := m.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", i)
		}
		du, ds, da := lxcMountDrift(slot, m, pveStr(current[slot]))
		for k, v := range du {
			upd[k] = v
		}
		if ds {
			stop = true
		}
		l.lxcDiskAnoms = append(l.lxcDiskAnoms, da...)
	}
	// bind mounts (M13): host-path binds on mp* slots. See lxcBindDrift for
	// the fail-closed posture (PVE restricts bind writes to root@pam; a
	// re-point of a live bind's host path is an anomaly, never automatic).
	for i, b := range l.Spec.BindMounts {
		slot := b.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", len(l.Spec.MountPoints)+i)
		}
		du, ds, da := lxcBindDrift(slot, b, pveStr(current[slot]))
		for k, v := range du {
			upd[k] = v
		}
		if ds {
			stop = true
		}
		l.lxcDiskAnoms = append(l.lxcDiskAnoms, da...)
	}
	// networks — compare owned fields (bridge, tag, rate, firewall,
	// pinned hwaddr; iface only when declared).
	for i, n := range l.Spec.Networks {
		slot := n.Slot
		if slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		if !lxcNetMatches(pveStr(current[slot]), n) {
			upd[slot] = lxcNetString(n)
			stop = true
		}
	}
	// options: tri-state pointer-bool compare.
	//
	// PVE 9.2 wire grammar (probe-verified on conformance-dev 2026-09-10,
	// disposable CTs 9870-9882, all destroyed; pinned against PVE 9.2):
	//   - create top-level: unprivileged, protection, onboot, console,
	//     features (composite); nested top-level nesting= / keyctl= /
	//     fuse= → 400/403 ("property is not defined").
	//   - PUT /config top-level: protection, onboot, console, features;
	//     unprivileged → 500 (create-only), nested keys → 400.
	//   - keyctl / fuse: NO accepted wire form at create OR /config on
	//     PVE 9.2 (403/400; the features composite only carries nesting).
	//     They are adoptable (reported faithfully) but NOT convergable by
	//     proxops.
	//
	// Comparison model: proxops owns a key ONLY when the manifest sets
	// it (non-nil desired). A nil desired means "PVE decides" — proxops
	// never writes that key, so any live value is PVE-owned and not
	// drifted. When non-nil, desired "0/1" must equal PVE's effective
	// value; PVE reports an explicit 0 key when set off (probe: LXC 111
	// protection=0), and absent means the PVE default (off for every key
	// here except unprivileged, whose PVE default is ON=1).
	if o := &l.Spec.Options; o != nil {
		// unprivileged is PVE 9.x create-only (PUT /config → 500). The
		// live default when PVE omits the key is unprivileged=1; only an
		// explicit 0 is "privileged". When it diverges, proxops cannot
		// converge in place — surface a recreate-required anomaly.
		if o.Unprivileged != nil {
			effective := true
			if v, present := current["unprivileged"]; present {
				effective = pveInt(v) == 1
			}
			if effective != *o.Unprivileged {
				l.lxcDiskAnoms = append(l.lxcDiskAnoms, "spec.options.unprivileged diverges from PVE's live value (effective live="+lxcBoolWire(effective)+"); unprivileged is a PVE 9.x create-only flag (PUT /config returns HTTP 500) — proxops cannot flip it in place, a recreate is required to converge")
			}
		}
		compareLXCBoolOption(upd, &stop, "protection", o.Protection, current)
		compareLXCBoolOption(upd, &stop, "onboot", o.OnBoot, current)
		compareLXCBoolOption(upd, &stop, "console", o.Console, current)
		// nesting is convergable only through the composite features=
		// property (top-level nesting= → 400 on PVE 9.2; probe-verified).
		// PVE 9.2's features composite recognizes ONLY the nesting token
		// (features=keyctl=... / features=fuse=... → 403), so emitting the
		// single-token form "features=nesting=<0|1>" cannot clobber any
		// PVE-owned other feature.
		if o.Nesting != nil {
			s := lxcBoolWire(*o.Nesting)
			if lxcFeaturesNestingWire(current) != s {
				upd["features"] = "nesting=" + s
				stop = true
			}
		}
		// keyctl / fuse: no convergable wire form on PVE 9.2. Desired
		// false + live off/absent = converged. Any other mismatch is a
		// non-destructive anomaly (proxops records the intent but
		// cannot apply it).
		lxcNonconvergableOptionAnomaly(&l.lxcDiskAnoms, "keyctl", o.KeyCtl, current["keyctl"])
		lxcNonconvergableOptionAnomaly(&l.lxcDiskAnoms, "fuse", o.Fuse, current["fuse"])
		if o.Startup != "" && pveStr(current["startup"]) != o.Startup {
			upd["startup"] = o.Startup
			stop = true
		}
	}
	if len(upd) == 0 {
		return nil, false, false
	}
	return upd, stop, true
}

// --- helpers ---

// lxcBoolWire renders a tri-state LXC boolean option into PVE's wire
// "0" / "1". Used by Drift to compare + emit.
func lxcBoolWire(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// compareLXCBoolOption compares one PVE /config-PUT'able top-level LXC
// boolean (protection/keyctl/fuse/onboot/console) against PVE's report
// and, when drifted, adds the update to `upd` and sets `*stop`.
//
// PVE reports the value when the operator set it; absence means "PVE
// default" which is off for every one of these keys. An absent live value
// therefore equals desired "0".
//
// NOTE on `unprivileged`: it cannot be /config-PUT'ed on PVE 9.x
// (HTTP 500), so it is handled with a dedicated anomaly path in Drift
// and is NOT routed through this helper.
func compareLXCBoolOption(upd map[string]any, stop *bool, field string, want *bool, current map[string]any) {
	if want == nil {
		return // proxops does not own this key.
	}
	liveStr := pveStr(current[field])
	if liveStr == "1" || liveStr == "true" {
		if !*want {
			upd[field] = "0"
			*stop = true
		}
		return
	}
	if liveStr == "0" || liveStr == "false" {
		if *want {
			upd[field] = "1"
			*stop = true
		}
		return
	}
	// Absent live: PVE default off. Desired "0" = no-op; desired "1" = drift.
	if *want {
		upd[field] = "1"
		*stop = true
	}
}

// lxcFeaturesNestingWire renders PVE's effective live nesting value from
// the composite `features=nesting=<0|1>` form (PVE 9.x) or the legacy
// top-level `nesting=<0|1>` form. Floor is "0": when PVE reports no
// nesting signal at all, the effective value is the PVE default (off), so
// a desired nesting=false must not re-emit features=nesting=0.
func lxcFeaturesNestingWire(current map[string]any) string {
	if s := pveStr(current["nesting"]); s != "" {
		return s
	}
	f := pveStr(current["features"])
	if f != "" {
		for _, tok := range strings.Split(f, ",") {
			tok = strings.TrimSpace(tok)
			if k, v, found := strings.Cut(tok, "="); found && k == "nesting" {
				return v
			}
		}
	}
	return "0"
}

// lxcFeaturesCreate renders the PVE 9.x composite create-time `features`
// token for a desired nesting. PVE 9.2's /lxc create accepts ONLY
// `features=nesting=<0|1>` for nesting; top-level `nesting=` is 400 and
// `features=keyctl=...`/`features=fuse=...` are 403 (probe-verified
// 2026-09-10).
func lxcFeaturesCreate(nesting *bool) string {
	if nesting == nil {
		return ""
	}
	if *nesting {
		return "nesting=1"
	}
	return "nesting=0"
}

// lxcNonconvergableOptionAnomaly records a non-destructive anomaly when a
// desired LXC option (keyctl / fuse) has no PVE 9.2 convergable wire form
// (probe: top-level 400, features composite 403). Desired=true + live
// off/absent is a manifest intent proxops cannot apply; desired=false
// (or nil) + live on/absent is PVE-owned (not drifted). The anomaly
// surfaces on /status + /metrics so the operator knows a manual toggle on
// PVE is required to converge.
func lxcNonconvergableOptionAnomaly(anoms *[]string, field string, want *bool, live any) {
	if want == nil || !*want {
		// nil / false desired: proxops does NOT own an enabled value
		// (absent live = PVE default is the only state proxops can
		// produce); a present live "1"/"true" would be PVE hand-set and
		// is surfaced elsewhere. Not a proxops-convergence concern.
		return
	}
	// Desired=true.
	if pveInt(live) == 1 {
		return // already converged.
	}
	// Desired=true but PVE is off/absent: no wire form exists to flip
	// this on PVE 9.x. Surface an anomaly (non-destructive, no write).
	*anoms = append(*anoms, "spec.options."+field+"=true has no PVE 9.x convergable wire form: top-level `"+field+"` is rejected by /lxc create AND /config (403/400 probe-verified 2026-09-10), and the PVE 9.x `features` composite only carries `nesting`. Toggle "+field+" on the PVE host (e.g. `pct set <ctid> -"+field+" 1`) and proxops will observe converged on the next cycle")
}

func (l *LXC) allTags() []string {
	out := make([]string, 0, len(l.Spec.Tags)+1)
	seen := map[string]bool{}
	for _, t := range l.Spec.Tags {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if !seen[PveOwnershipTag] {
		out = append(out, PveOwnershipTag)
	}
	return out
}
