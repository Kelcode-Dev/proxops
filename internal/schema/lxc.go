package schema

import (
	"fmt"
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
type LXCNetwork struct {
	// Model: "veth" (default), "macvlan", "bridged".
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
	// Bridge, e.g. "vmbr0".
	Bridge string `yaml:"bridge" json:"bridge"`
	// HWAddr is a fixed MAC; empty lets PVE pick one.
	HWAddr string `yaml:"hwaddr,omitempty" json:"hwaddr,omitempty"`
	// Tag is a VLAN tag.
	Tag int `yaml:"tag,omitempty" json:"tag,omitempty"`
}

// LXCUnprivileged is PVE's "unprivileged" (1).
type LXCUnprivileged int

// LXCExtra mirrors VM's extra: freeform PVE passthrough keys.
type LXCExtra = map[string]string

// LXCSpec is the declarative LXC body.
type LXCSpec struct {
	// Node is the PVE node hosting this container.
	Node string `yaml:"node" json:"node"`
	// VMID is the PVE id (cid) — pinned.
	VMID int `yaml:"vmid" json:"vmid"`

	// PveName is PVE's `name` (defaults to metadata.name).
	PveName string `yaml:"pve-name,omitempty" json:"pve-name,omitempty"`
	// PveDescription.
	PveDescription string `yaml:"pve-description,omitempty" json:"pve-description,omitempty"`
	// Tags are PVE user tags (pveconform tag is auto-appended).
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// CPU.
	CPU LXCCPU `yaml:"cpu" json:"cpu"`
	// Memory (e.g. "2GiB") in PVE bytes.
	Memory string `yaml:"memory" json:"memory"`
	// Swap (PVE "swap", in PVE bytes; default 128MiB).
	Swap string `yaml:"swap,omitempty" json:"swap,omitempty"`
	// Rootfs.
	Root LXCRoot `yaml:"root" json:"root"`
	// Networks.
	Networks []LXCNetwork `yaml:"networks" json:"networks"`
	// OS sets PVE's "os" field (only valid on create) — defaults to "linux".
	OS string `yaml:"os,omitempty" json:"os,omitempty"`
	// Arch sets PVE's "arch" (only on create) — defaults to "amd64".
	Arch string `yaml:"arch,omitempty" json:"arch,omitempty"`
	// Features: PVE's "features" key (optional, create-only).
	Features string `yaml:"features,omitempty" json:"features,omitempty"`

	// Extra freeform PVE passthrough.
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
}

// NewLXC returns an empty LXC.
func NewLXC() *LXC {
	return &LXC{Kind: KindLXC, APIVersion: APIVersion}
}

// Ref implements Resource.
func (l *LXC) Ref() Ref { return Ref{Kind: l.Kind, Name: l.Metadata.Name} }

// Node implements Resource.
func (l *LXC) Node() string { return l.Spec.Node }

// ID implements Resource.
func (l *LXC) ID() int { return l.Spec.VMID }

// DesiredState implements Resource; returns spec.state or "started".
func (l *LXC) DesiredState() string {
	if s, err := ParseState(l.Spec.State); err == nil {
		return string(s)
	}
	return "started"
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
	if _, err := MemoryKiB(l.Spec.Memory); err != nil {
		return fmt.Errorf("%s: spec.memory: %w", l.Ref(), err)
	}
	if l.Spec.Root.Storage == "" {
		return fmt.Errorf("%s: spec.root.storage must be set", l.Ref())
	}
	if l.Spec.Root.Size == "" {
		return fmt.Errorf("%s: spec.root.size must be set (e.g. 32GiB)", l.Ref())
	}
	if _, err := DiskBytes(l.Spec.Root.Size); err != nil {
		return fmt.Errorf("%s: spec.root.size: %w", l.Ref(), err)
	}
	if len(l.Spec.Networks) > 0 {
		if l.Spec.Networks[0].Bridge == "" {
			return fmt.Errorf("%s: spec.networks[0].bridge must be set", l.Ref())
		}
	}
	for k := range l.Spec.Extra {
		if k == "vmid" || k == "name" || k == "memory" || k == "cores" || strings.HasPrefix(k, "net") {
			return fmt.Errorf("%s: spec.extra key %q conflicts with a structured field", l.Ref(), k)
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
func (l *LXC) ToCreateParams() (map[string]any, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	p := map[string]any{
		"vmid":   l.Spec.VMID,
		"name":   l.pveName(),
		"cores":  l.Spec.CPU.Cores,
		"memory": memKiB(l.Spec.Memory),
		"tags":   strings.Join(l.allTags(), ","),
		"start":  "0",
	}
	if l.Spec.PveDescription != "" {
		p["description"] = l.Spec.PveDescription
	}
	// Rootfs: "storage/size".
	rootSize := FormatDiskBytes(diskBytes(l.Spec.Root.Size))
	p["rootfs"] = fmt.Sprintf("%s:%s", l.Spec.Root.Storage, rootSize)
	if l.Spec.OS != "" {
		p["os"] = l.Spec.OS
	}
	if l.Spec.Arch != "" {
		p["arch"] = l.Spec.Arch
	}
	if l.Spec.Swap != "" {
		p["swap"] = memKiB(l.Spec.Swap)
	}
	// Network: PVE uses "net0: veth[,hwaddr=...],bridge=...,tag=..." (see
	// lxcNetString for the device-type/empty-MAC semantics).
	for i, n := range l.Spec.Networks {
		spec := lxcNetString(n.Model, strings.ToLower(n.HWAddr), n.Bridge, n.Tag)
		p[fmt.Sprintf("net%d", i)] = spec
	}
	for k, v := range l.Spec.Extra {
		p[k] = v
	}
	return p, nil
}

// lxcNetString builds PVE's LXC "netN" value: "<model>[=<hwaddr>][,bridge=..][,tag=N]".
// A bare model name (no MAC pinned) is a valid PVE device type ("veth,bridge=x");
// "veth=<empty>" would fail PVE's comma-separated property parser with
// "missing key in comma-separated list property" (same class of error that
// hit the VM virtio NIC path).
func lxcNetString(model, hwAddr, bridge string, tag int) string {
	m := model
	if m == "" {
		m = "veth"
	}
	spec := m
	if hwAddr != "" {
		spec += "=" + hwAddr
	}
	spec += ","
	if bridge != "" {
		spec += "bridge=" + bridge + ","
	}
	if tag > 0 {
		spec += "tag=" + itoa(tag) + ","
	}
	return strings.TrimSuffix(spec, ",")
}

// lxcNetMatches reports whether PVE's reported LXC net string satisfies the
// owned fields of the desired LXCNetwork. PVE reports the full form
// ("veth=52:54:00:aa:bb:cc,bridge=vmbr0,tag=1"). We compare:
//
//   - model (first token before '='; a pinned-MAC report carries it)
//   - bridge (PVE-owned)
//   - tag   (only when we pinned one)
//   - MAC   (only when we pinned one; PVE assigns a random one otherwise)
//
// This avoids false-drift for PVE-assigned MACs we did not request.
func lxcNetMatches(cur, model, hwAddr, bridge string, tag int) bool {
	cur = strings.TrimSpace(cur)
	if cur == "" {
		return false
	}
	wantModel := model
	if wantModel == "" {
		wantModel = "veth"
	}
	head := cur
	if i := strings.IndexByte(head, ','); i >= 0 {
		head = head[:i]
	}
	gotModel, mac, hasMac := head, "", false
	if eq := strings.IndexByte(head, '='); eq >= 0 {
		gotModel = head[:eq]
		mac = head[eq+1:]
		hasMac = true
	}
	if gotModel != wantModel {
		return false
	}
	if bridge != "" && !lxcNetHasKV(cur, "bridge", bridge) {
		return false
	}
	if tag > 0 && !lxcNetHasKV(cur, "tag", itoa(tag)) {
		return false
	}
	if hwAddr != "" && (!hasMac || !strings.EqualFold(mac, hwAddr)) {
		return false
	}
	return true
}

// lxcNetHasKV reports whether the comma list contains "k=v".
func lxcNetHasKV(list, k, v string) bool {
	for _, kv := range strings.Split(list, ",") {
		if strings.TrimPrefix(strings.TrimSpace(kv), k+"=") == v && strings.HasPrefix(strings.TrimSpace(kv), k+"=") {
			return true
		}
	}
	return false
}

// Drift implements Resource.
func (l *LXC) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		return nil, false, false
	}
	upd := map[string]any{}
	stop := false

	// cores
	if pveInt(current["cores"]) != l.Spec.CPU.Cores {
		upd["cores"] = l.Spec.CPU.Cores
		stop = true
	}
	// memory
	if want, err := MemoryKiB(l.Spec.Memory); err == nil && pveInt(current["memory"]) != int(want) {
		upd["memory"] = want
		stop = true
	}
	// tags: PVE stores as a JSON array; compare as a set.
	if !tagsEqual(current["tags"], l.allTags()) {
		upd["tags"] = strings.Join(l.allTags(), ",")
	}
	// rootfs: PVE reports it as "storage/volid"; we don't own the volid.
	if wantRoot := fmt.Sprintf("%s:", l.Spec.Root.Storage); pveStr(current["rootfs"]) != "" && !strings.HasPrefix(pveStr(current["rootfs"]), l.Spec.Root.Storage+":") {
		upd["rootfs"] = wantRoot
		stop = true
	}
	// nics — compare owned fields (model, hwaddr, bridge, tag).
	for i, n := range l.Spec.Networks {
		hw := strings.ToLower(n.HWAddr)
		slot := fmt.Sprintf("net%d", i)
		if !lxcNetMatches(pveStr(current[slot]), n.Model, hw, n.Bridge, n.Tag) {
			upd[slot] = lxcNetString(n.Model, hw, n.Bridge, n.Tag)
			stop = true
		}
	}
	_ = stop
	if len(upd) == 0 {
		return nil, false, false
	}
	return upd, stop, true
}

// --- helpers ---

func (l *LXC) pveName() string {
	if l.Spec.PveName != "" {
		return l.Spec.PveName
	}
	return l.Metadata.Name
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

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
