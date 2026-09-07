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
// type, rate, firewall (see pct.conf(5)); `multi_bridge`/`macvlan_mode`
// are NOT in PVE's netX schema (PVE 9.2 probe-verified: rejected by the
// create API).
type LXCNetwork struct {
	// Iface is PVE's `name=<iface>` (guest-side interface name, e.g.
	// "wired0" default when PVE's UI creates a net, "net0" legacy). When
	// empty, pveconform defaults to "net<i>" to be deterministic.
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

// LXCMountOptions are per-mountpoint LXC options. PVE 9.2 LXC accepts mpN
// form-values: storage, size, mountpoint, mpN=storage,size=... for
// rootdir-allocation mountpoints, plus `fssize=` and `quota=` on LVM
// pools. The declarative schema exposes only the owned fields.
type LXCMountOptions struct {
	// Quota is PVE's `quota=<bytes or N%>` for dir-based storage.
	Quota string `yaml:"quota,omitempty" json:"quota,omitempty"`
	// ReadOnly attaches the mount read-only.
	ReadOnly bool `yaml:"read-only,omitempty" json:"read-only,omitempty"`
}

// LXCOptions captures PVE LXC common options panel.
type LXCOptions struct {
	// Unprivileged is PVE's `unprivileged` (PCT 1 = default in PVE 9.x).
	// When true, pveconform emits `unprivileged=1`.
	Unprivileged bool `yaml:"unprivileged,omitempty" json:"unprivileged,omitempty"`
	// Protection is PVE's `protection` (prevents accidental destroy).
	Protection bool `yaml:"protection,omitempty" json:"protection,omitempty"`
	// Nesting is PVE's `nesting` (allows nested LXC/VM).
	Nesting bool `yaml:"nesting,omitempty" json:"nesting,omitempty"`
	// KeyCtl is PVE's `keyctl` (allows keyctl in guest).
	KeyCtl bool `yaml:"keyctl,omitempty" json:"keyctl,omitempty"`
	// Fuse is PVE's `fuse` (allows FUSE mounts inside guest).
	Fuse bool `yaml:"fuse,omitempty" json:"fuse,omitempty"`
	// OnBoot is PVE's `onboot` (auto-start on node boot).
	OnBoot bool `yaml:"onboot,omitempty" json:"onboot,omitempty"`
	// Startup is PVE's `startup` (e.g. "order=10", "start=1").
	Startup string `yaml:"startup,omitempty" json:"startup,omitempty"`
	// TTYCount is PVE's `ttys=` (PVE 9.2 create rejects this; only
	// settable on /config update).
	// pveconform records the intent and applies it at update time if
	// the container needs a TTY change.
	// When the field is left empty, pveconform does NOT send it.
	TTYCount int `yaml:"ttys,omitempty" json:"ttys,omitempty"`
	// Console enables/updates PVE's `console=` option (PVE 9.2 create
	// rejects a non-boolean `console=tty`).
	Console bool `yaml:"console,omitempty" json:"console,omitempty"`
}

// LXCUnprivileged is PVE's "unprivileged" (1).
type LXCUnprivileged int

// LXCExtra mirrors VM's extra: freeform PVE passthrough keys.
type LXCExtra = map[string]string

// LXCMount is one additional LXC mountpoint (mp0, mp1, ...).
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
}

// LXCSpec is the declarative LXC body.
type LXCSpec struct {
	// Node is the PVE node hosting this container.
	Node string `yaml:"node" json:"node"`
	// VMID is the PVE id (cid) — pinned.
	VMID int `yaml:"vmid" json:"vmid"`

	// Template is a pveconform CTTemplate metadata.name that bootstraps the
	// container's rootfs. It is REQUIRED on create: PVE's /lxc create needs
	// an `ostemplate` volume and the declarative schema expresses that as a
	// reference to a CTTemplate manifest rather than a raw storage path. The
	// planner resolves it to `ostemplate=<storage>:vztmpl/<filename>` and
	// injects it into the create params.
	Template string `yaml:"template" json:"template"`

	// PveDescription is PVE's `description` (valid on create + update).
	PveDescription string `yaml:"pve-description,omitempty" json:"pve-description,omitempty"`
	// Tags are PVE user tags (pveconform tag is auto-appended).
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
		"start": true,
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
//   - `ostemplate=<pool>:vztmpl/<filename>` is required; pveconform does
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
	// "<storage>:vztmpl/<filename>" and pveconform emits it here.
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
		val := lxcLVMAlloc(m.Storage, m.Size)
		if m.MountPoint != "" {
			val += ",mp=" + m.MountPoint
		}
		p[slot] = val
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
	// Container options.
	if l.Spec.Options.Unprivileged {
		p["unprivileged"] = "1"
	}
	if l.Spec.Options.Protection {
		p["protection"] = "1"
	}
	if l.Spec.Options.Nesting {
		p["nesting"] = "1"
	}
	if l.Spec.Options.KeyCtl {
		p["keyctl"] = "1"
	}
	if l.Spec.Options.Fuse {
		p["fuse"] = "1"
	}
	if l.Spec.Options.OnBoot {
		p["onboot"] = "1"
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
}

// parseLXCNetFields parses an LXC netX property string. PVE's report form
// (create-time is the same grammar) is "name=wired0,bridge=vmbr0,hwaddr=
// 52:...,type=veth". PVE always normalizes type to veth unless a
// non-veth type was requested.
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
	// iface: if user did not declare one, pveconform defaulted to net<N>
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
// used "name" — pveconform only targets PVE 9.x.

// DriftAnomalies surfaces live-only LXC mount-point slots (mp*) that the
// manifest does not declare. Same semantics as VM.DriftAnomalies for disks:
// pveconform will not automatically delete a live-only mount point (PVE's
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
	out := make([]string, 0, 2)
	for k, raw := range current {
		if !isMPSlot(k) {
			continue
		}
		if !want[k] && !isNoneSlot(pveStr(raw)) {
			out = append(out, fmt.Sprintf("live-only LXC mountpoint slot %s=%s is not in spec.mount-points; pveconform will not automatically remove it", k, pveStr(raw)))
		}
	}
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
	// pveconform sends metadata.name and PVE stores it verbatim, so the
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
	// rootfs pool+size
	wantRoot := parseDiskInfo(lxcLVMAlloc(l.Spec.Root.Storage, l.Spec.Root.Size))
	if !diskMatches(parseDiskInfo(pveStr(current["rootfs"])), wantRoot) {
		upd["rootfs"] = lxcLVMAlloc(l.Spec.Root.Storage, l.Spec.Root.Size)
		stop = true
	}
	// mount points pool+size
	for i, m := range l.Spec.MountPoints {
		slot := m.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", i)
		}
		want := parseDiskInfo(lxcLVMAlloc(m.Storage, m.Size))
		got := parseDiskInfo(pveStr(current[slot]))
		// PVE's mountpoint report is "<pool>:vm-<cid>-disk-<n>[,...]"
		// (no size) or "<pool>:<volid>,size=<binary>". Our pveDiskInfo
		// parser handles both. If PVE's size is unreadable, we fall back
		// to pool-only comparison; a size mismatch still drifts.
		if !diskMatches(got, want) {
			upd[slot] = lxcLVMAlloc(m.Storage, m.Size)
			stop = true
		}
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
	// options: PVE stores 0 as "absent"; comparing int-0==absent avoids
	// false drift.
	if o := &l.Spec.Options; o != nil {
		if o.Unprivileged && pveInt(current["unprivileged"]) != 1 {
			upd["unprivileged"] = "1"
		}
		if o.Protection && pveInt(current["protection"]) != 1 {
			upd["protection"] = "1"
		}
		if o.Nesting && pveInt(current["nesting"]) != 1 {
			upd["nesting"] = "1"
		}
		if o.KeyCtl && pveInt(current["keyctl"]) != 1 {
			upd["keyctl"] = "1"
		}
		if o.Fuse && pveInt(current["fuse"]) != 1 {
			upd["fuse"] = "1"
		}
		if o.OnBoot && pveInt(current["onboot"]) != 1 {
			upd["onboot"] = "1"
		}
		if o.Startup != "" && pveStr(current["startup"]) != o.Startup {
			upd["startup"] = o.Startup
		}
	}
	if len(upd) == 0 {
		return nil, false, false
	}
	return upd, stop, true
}

// --- helpers ---

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

func itoa(i int) string {
	return strconv.Itoa(i)
}
