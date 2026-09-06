package schema

import (
	"fmt"
	"strconv"
	"strings"
)

// PveOwnershipTag is the PVE tag the agent adds to every object it manages.
// Pruning only ever acts on tagged objects (see plan §10).
const PveOwnershipTag = "pveconform"

// Cpu is PVE's `cpu`,`cores`,`args` triad.
type Cpu struct {
	// Type is the PVE cpu argument: "host", "qemu64", "x86-64-v2-AES", etc.
	Type string `yaml:"type" json:"type"`
	// Cores is the vCPU count.
	Cores int `yaml:"cores" json:"cores"`
	// Flags are extra cpu argument tokens (e.g. "invarclock").
	Flags []string `yaml:"flags,omitempty" json:"flags,omitempty"`
}

// Disk is one PVE VM disk.
type Disk struct {
	// Storage is the PVE storage id (pool), e.g. "local-lvm", "vm_disks".
	Storage string `yaml:"storage" json:"storage"`
	// Size is the disk size, e.g. "50GiB".
	Size string `yaml:"size" json:"size"`
	// Slot is the PVE slot, e.g. "scsi0". Defaults to scsi<i> in order.
	// Named "interface" in the declarative YAML to match user docs.
	Slot string `yaml:"interface,omitempty" json:"interface,omitempty"`
	// Controller applies PVE's scsihw (VM-wide). The first disk carrying a
	// controller sets PVE's "scsihw" key.
	Controller string `yaml:"controller,omitempty" json:"controller,omitempty"`
	// IOThread requests a dedicated I/O thread for this disk.
	IOThread bool `yaml:"iothread,omitempty" json:"iothread,omitempty"`
}

// NIC is one PVE VM network device.
type NIC struct {
	// Model is PVE's nic model: "virtio", "e1000", "rtl8139", "vmxnet3".
	Model string `yaml:"model" json:"model"`
	// Bridge is the PVE bridge, e.g. "vmbr0".
	Bridge string `yaml:"bridge" json:"bridge"`
	// MAC pins a specific MAC; empty lets PVE pick one.
	MAC string `yaml:"mac,omitempty" json:"mac,omitempty"`
	// Slot overrides the default net<i>.
	Slot string `yaml:"slot,omitempty" json:"slot,omitempty"`
}

// VMSpec is the VM's declarative body.
type VMSpec struct {
	// Node is the PVE node hosting this VM.
	Node string `yaml:"node" json:"node"`
	// VMID is the PVE id (required; the agent never invents ids).
	VMID int `yaml:"vmid" json:"vmid"`

	// PveName is PVE's `name` display name. Defaults to metadata.name.
	PveName string `yaml:"pve-name,omitempty" json:"pve-name,omitempty"`
	// PveDescription is PVE's `description` field.
	PveDescription string `yaml:"pve-description,omitempty" json:"pve-description,omitempty"`
	// Tags are PVE user tags (in addition to the pveconform tag).
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// CPU configures PVE cpu/cores/args.
	CPU Cpu `yaml:"cpu" json:"cpu"`
	// Memory is PVE memory, e.g. "8GiB" or "4GiB".
	Memory string `yaml:"memory" json:"memory"`
	// Disks are PVE disk devices (scsi*/virtio*/sata*/ide*).
	Disks []Disk `yaml:"disks" json:"disks"`
	// Networks are PVE NIC devices (net*). Named "networks" in the YAML to
	// match the declarative docs; Go field is NICs.
	NICs []NIC `yaml:"networks" json:"networks"`

	// Extra is a freeform PVE key=value map (escape hatch for anything not
	// modeled above; e.g. bios, vga, tablet).
	Extra map[string]string `yaml:"extra,omitempty" json:"extra,omitempty"`

	// State is the desired power state: "started" (default) or "stopped".
	State string `yaml:"state,omitempty" json:"state,omitempty"`
}

// VM is a schema.Resource for Kind=VM.
type VM struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       Kind     `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       VMSpec   `yaml:"spec" json:"spec"`
}

// NewVM returns an empty VM.
func NewVM() *VM {
	return &VM{Kind: KindVM, APIVersion: APIVersion}
}

// Ref implements Resource.
func (v *VM) Ref() Ref { return Ref{Kind: v.Kind, Name: v.Metadata.Name} }

// Node implements Resource.
func (v *VM) Node() string { return v.Spec.Node }

// ID implements Resource.
func (v *VM) ID() int { return v.Spec.VMID }

// DesiredState implements Resource; returns spec.state or "started".
func (v *VM) DesiredState() string {
	if s, err := ParseState(v.Spec.State); err == nil {
		return string(s)
	}
	return "started"
}

// Validate implements Resource.
func (v *VM) Validate() error {
	if v.Spec.Node == "" {
		return fmt.Errorf("%s: spec.node is empty", v.Ref())
	}
	if !ValidNodeName(v.Spec.Node) {
		return fmt.Errorf("%s: spec.node %q invalid", v.Ref(), v.Spec.Node)
	}
	if v.Spec.VMID <= 0 {
		return fmt.Errorf("%s: spec.vmid must be > 0", v.Ref())
	}
	// cpu
	if v.Spec.CPU.Type == "" {
		return fmt.Errorf("%s: spec.cpu.type must be set", v.Ref())
	}
	if v.Spec.CPU.Cores <= 0 {
		return fmt.Errorf("%s: spec.cpu.cores must be > 0", v.Ref())
	}
	// memory
	if v.Spec.Memory == "" {
		return fmt.Errorf("%s: spec.memory must be set (e.g. 8GiB)", v.Ref())
	}
	if _, err := MemoryMiB(v.Spec.Memory); err != nil {
		return fmt.Errorf("%s: spec.memory: %w", v.Ref(), err)
	}
	// disks
	if len(v.Spec.Disks) == 0 {
		return fmt.Errorf("%s: spec.disks must be present", v.Ref())
	}
	seenSlots := map[string]bool{}
	for i := range v.Spec.Disks {
		d := &v.Spec.Disks[i]
		if d.Storage == "" {
			return fmt.Errorf("%s: spec.disks[%d].storage must be set", v.Ref(), i)
		}
		if d.Size == "" {
			return fmt.Errorf("%s: spec.disks[%d].size must be set (e.g. 50GiB)", v.Ref(), i)
		}
		if _, err := DiskBytes(d.Size); err != nil {
			return fmt.Errorf("%s: spec.disks[%d].size: %w", v.Ref(), i, err)
		}
		slot := d.Slot
		if slot == "" {
			slot = fmt.Sprintf("scsi%d", i)
		}
		if seenSlots[slot] {
			return fmt.Errorf("%s: duplicate disk slot %q", v.Ref(), slot)
		}
		seenSlots[slot] = true
		if !validDiskSlot(slot) {
			return fmt.Errorf("%s: spec.disks[%d] slot %q is not a valid PVE disk slot (use scsi0, virtio0, sata0, ...)", v.Ref(), i, slot)
		}
		d.Slot = slot
	}
	// nics
	seenNet := map[string]bool{}
	for i := range v.Spec.NICs {
		n := &v.Spec.NICs[i]
		if n.Model == "" {
			n.Model = "virtio"
		}
		if n.Bridge == "" {
			return fmt.Errorf("%s: spec.nics[%d].bridge must be set", v.Ref(), i)
		}
		slot := n.Slot
		if slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		if seenNet[slot] {
			return fmt.Errorf("%s: duplicate net slot %q", v.Ref(), slot)
		}
		seenNet[slot] = true
		if !validNetSlot(slot) {
			return fmt.Errorf("%s: spec.nics[%d] slot %q invalid (use net0, net1, ...)", v.Ref(), i, slot)
		}
		n.Slot = slot
	}
	// extra keys must not collide with PVE core keys we already handle.
	for k := range v.Spec.Extra {
		if k == "vmid" || k == "name" || k == "memory" || k == "cpu" || k == "cores" || strings.HasPrefix(k, "scsi") || strings.HasPrefix(k, "net") {
			return fmt.Errorf("%s: spec.extra key %q conflicts with a structured field; remove it", v.Ref(), k)
		}
	}
	if v.Spec.State != "" {
		if _, err := ParseState(v.Spec.State); err != nil {
			return fmt.Errorf("%s: %w", v.Ref(), err)
		}
	}
	return nil
}

// ToCreateParams implements Resource. It returns PVE wire form-values for
// POST /nodes/{n}/qemu.
func (v *VM) ToCreateParams() (map[string]any, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	p := map[string]any{
		"vmid":   v.Spec.VMID,
		"name":   pveName(v),
		"memory": memMiB(v.Spec.Memory),
		"cpu":    v.Spec.CPU.Type,
		"cores":  v.Spec.CPU.Cores,
		"tags":   strings.Join(v.allTags(), ","),
		"start":  "0",
	}
	if v.Spec.PveDescription != "" {
		p["description"] = v.Spec.PveDescription
	}
	// PVE's scsi controller (scsihw) is VM-wide; the declarative schema puts it
	// on the first disk for locality. Map disk.Controller → scsihw.
	if hw := vmScsiHW(v.Spec.Disks); hw != "" {
		p["scsihw"] = hw
	}
	for i, d := range v.Spec.Disks {
		slot := d.Slot
		if slot == "" {
			slot = fmt.Sprintf("scsi%d", i)
		}
		p[slot] = driveVolumeString(d)
	}
	for i := range v.Spec.NICs {
		slot := v.Spec.NICs[i].Slot
		if slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		p[slot] = nicString(v.Spec.NICs[i])
	}
	for k, val := range v.Spec.Extra {
		p[k] = val
	}
	return p, nil
}

// Drift implements Resource. Given a current PVE config (as returned by
// GET /qemu/{vmid}/config), it returns the owned-field diff.
func (v *VM) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		// No current → we'd have to create; the planner handles that.
		return nil, false, false
	}
	upd := map[string]any{}
	stop := false

	// cpu.
	if want := v.Spec.CPU.Type; want != "" && pveStr(current["cpu"]) != want {
		upd["cpu"] = want
		stop = true
	}
	if want := v.Spec.CPU.Cores; want > 0 && pveInt(current["cores"]) != want {
		upd["cores"] = want
		stop = true
	}
	// memory: PVE's config stores an integer MiB count ("memory: ..." in
	// qm.conf is documented in MiB; a create-time `memory=100` is stored and
	// reported as `100`). The live-status endpoint reports bytes, but Drift
	// only ever sees the /config form.
	if want, err := MemoryMiB(v.Spec.Memory); err == nil {
		if pveInt(current["memory"]) != int(want) {
			upd["memory"] = want
			stop = true
		}
	}
	// tags: PVE stores as a JSON array of strings; we compare as a set, and
	// emit a comma-joined string on update (PVE accepts both forms).
	if !tagsEqual(current["tags"], v.allTags()) {
		upd["tags"] = strings.Join(v.allTags(), ",")
	}
	// scsihw (VM-wide; owned when a disk declares a controller).
	if wantHW := vmScsiHW(v.Spec.Disks); wantHW != "" && pveStr(current["scsihw"]) != wantHW {
		upd["scsihw"] = wantHW
		stop = true
	}
	// disks. PVE's /config report is "<pool>:vm-<vmid>-disk-<n>[,iothread=1],size=<binary>"
	// (PVE-assigned volume name, binary-suffix size, iothread inline in the
	// drive property string). We own pool, size, and iothread; compare in
	// pveDiskInfo.
	for i, d := range v.Spec.Disks {
		slot := d.Slot
		if slot == "" {
			slot = fmt.Sprintf("scsi%d", i)
		}
		cur := parseDiskInfo(pveStr(current[slot]))
		want := parseDiskInfo(driveVolumeString(d))
		if !diskMatches(cur, want) {
			// Re-emit the full owned drive string; PVE keeps the volume name
			// it allocated and only reinterprets the new pool/size/options.
			upd[slot] = driveVolumeString(d)
			stop = true
		}
	}
	// nics.
	for i, n := range v.Spec.NICs {
		slot := n.Slot
		if slot == "" {
			slot = fmt.Sprintf("net%d", i)
		}
		cur := pveStr(current[slot])
		want := nicString(n)
		if !nicMatches(cur, want) {
			upd[slot] = want
			// model/bridge changes require stop.
			stop = true
		}
	}
	if len(upd) == 0 {
		return nil, stop, false
	}
	return upd, stop, true
}

// --- helpers ---

func pveName(v *VM) string {
	if v.Spec.PveName != "" {
		return v.Spec.PveName
	}
	return v.Metadata.Name
}

func (v *VM) allTags() []string {
	out := make([]string, 0, len(v.Spec.Tags)+1)
	seen := map[string]bool{}
	for _, t := range v.Spec.Tags {
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

// vmScsiHW derives PVE's VM-wide scsihw key from the first disk that declares
// a controller. Returns "" when no disk sets one.
func vmScsiHW(disks []Disk) string {
	for _, d := range disks {
		if d.Controller != "" {
			return d.Controller
		}
	}
	return ""
}

// memMiB returns PVE memory in MiB (int64). PVE's create-time `memory` and
// LXC's `memory`/`swap` fields are integer megabyte counts ("in MiB" per
// qm.conf / "in MB" per pct.conf): 1GiB → 1024.
func memMiB(human string) int64 {
	b, _ := MemoryMiB(human)
	return b
}

// diskVolumeString builds PVE's create-time disk device value as
// "<pool>:<size-in-GiB>" (e.g. "local-lvm:8"). The number after the storage
// id is GiB, not bytes: PVE's LVM/LVM-thin "allocate volume of this size"
// path multiplies by 10^30 (confirmed on PVE 9.2 — sending 8589934592
// produced "Volume too large (8.00 EiB)"; sending 1 allocated a 1073741824-
// byte thin volume). Fractional GiB is accepted ("0.5" → a 512 MiB volume),
// matching PVE's web UI which exposes a 3-decimal "Disk size (GiB)" field.
// Other storage backends (dir, cifs) use different volume grammars, but the
// GiB number is the common allocation form PVE's storage plugins understand
// at create time. PVE rewrites the field after allocate to
// "<pool>:vm-<vmid>-disk-<n>,size=<binary-suffix>", which is why Drift
// compares on pool + size — see splitDiskOwned.
func diskVolumeString(d Disk, slot string) string {
	_ = slot
	return fmt.Sprintf("%s:%s", d.Storage, GiBString(diskBytes(d.Size)))
}

// driveVolumeString renders the full PVE QEMU drive property string used by
// the create endpoint: "<pool>:<GiB>[,iothread=1]". PVE's create schema
// rejects a separate "<slot>.iothread=1" sibling parameter
// ("property is not defined in schema and the schema does not allow
// additional properties") — iothread is owned by the drive string itself
// (qm.conf documents scsi[n] with the inline option list that includes
// `iothread=`), and PVE's post-create /config report embeds it in the same
// string ("local-lvm:vm-9100-disk-0,iothread=1,size=8G").
func driveVolumeString(d Disk) string {
	s := diskVolumeString(d, d.Slot)
	if d.IOThread {
		s += ",iothread=1"
	}
	return s
}
func diskBytes(human string) int64 {
	b, _ := DiskBytes(human)
	return b
}

// pveDiskInfo is the owned-field view of a PVE QEMU drive property string.
// Both sides are parsed from the same grammar:
//
//	create/report: "local-lvm:vm-9100-disk-0,iothread=1,size=8G"
//	our desired:   "local-lvm:8,iothread=1"
//
// Fields we own and compare: pool (volume id), size (bytes), iothread.
type pveDiskInfo struct {
	pool      string
	sizeBytes int64
	sizeSet   bool
	iothread  bool
}

// pveDiskInfo parses a PVE QEMU drive property string into (pool, size,
// iothread). The pool is the token before the first ':'; options are the
// comma-separated k=v (or bare-flag) tokens after it. PVE reports `size` in
// binary suffix form ("8G", "512M"); our desired form embeds the volume-size
// number right after the pool in GiB — both parse to the same bytes.
func parseDiskInfo(s string) pveDiskInfo {
	out := pveDiskInfo{}
	s = strings.TrimSpace(s)
	if s == "" {
		return out
	}
	if ci := strings.IndexByte(s, ':'); ci > 0 {
		out.pool = s[:ci]
		rest := s[ci+1:]
		// Split the rest on commas; the first token may be either a
		// volume name ("vm-9100-disk-0") or a size number ("8", "0.5").
		toks := strings.Split(rest, ",")
		for i, t := range toks {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if eq := strings.IndexByte(t, '='); eq > 0 {
				k, v := t[:eq], t[eq+1:]
				switch k {
				case "size":
					if b, ok := pveDiskSizeBytes(v); ok {
						out.sizeBytes, out.sizeSet = b, true
					}
				case "iothread":
					out.iothread = v == "1" || v == "on" || v == "true"
				}
				continue
			}
			if i == 0 && !strings.Contains(t, "=") {
				// Bare first token: "local-lvm:8" create form (volume-size
				// number in GiB) or "local-lvm:vm-...-disk-0" report form.
				if f, err := strconv.ParseFloat(t, 64); err == nil && f > 0 {
					out.sizeBytes = int64(f * float64(int64(1)<<30))
					out.sizeSet = true
					continue
				}
				// Treat as the PVE-assigned volume name → not owned.
				continue
			}
		}
	}
	return out
}

// diskMatches reports whether two PVE drive property strings agree on the
// owned pieces (pool, size, iothread). a is the PVE-side (current) report;
// b is our desired state. A CURRENT report that omits its size token (LXC
// rootfs/mp reports; cdrom/none media; pre-allocate) is treated as
// compatible — PVE simply is not telling us a number to compare against —
// so idempotent re-runs stay quiet.
func diskMatches(a, b pveDiskInfo) bool {
	if a.pool != b.pool {
		return false
	}
	if a.iothread != b.iothread {
		return false
	}
	if !a.sizeSet {
		return true
	}
	return b.sizeSet && a.sizeBytes == b.sizeBytes
}

// nicString builds PVE's "net0" value string. Two forms:
//
//   - MAC pinned:  "virtio=aa:bb:cc:dd:ee:ff,bridge=vmbr0,firewall=0"
//   - MAC unpinned: "virtio,bridge=vmbr0,firewall=0"   (bare model name;
//     PVE will assign a MAC. The "virtio=<empty>" form is invalid: PVE's
//     comma-separated property parser rejects a key with no value, exactly
//     the "missing key in comma-separated list property" error.)
//
// Bridge is required (validated upstream); firewall=0 is PVE's default and
// is emitted explicitly so a later Drift comparison is self-describing.
func nicString(n NIC) string {
	model := n.Model
	if model == "" {
		model = "virtio"
	}
	bridge := n.Bridge
	var sb strings.Builder
	if mac := strings.ToLower(n.MAC); mac != "" {
		fmt.Fprintf(&sb, "%s=%s,bridge=%s,firewall=0", model, mac, bridge)
	} else {
		fmt.Fprintf(&sb, "%s,bridge=%s,firewall=0", model, bridge)
	}
	return sb.String()
}

// nicMatches compares two PVE NIC strings on the owned fields: model,
// bridge, and MAC (only when the desired NIC pins one — PVE assigns a
// random MAC at create time otherwise, so the unpinned-MAC report must not
// count as drift).
func nicMatches(cur, want string) bool {
	cm, cb, cMac := splitNIC(cur)
	wm, wb, wMac := splitNIC(want)
	if cm != wm {
		return false
	}
	if cb != wb {
		return false
	}
	if wMac != "" && !strings.EqualFold(wMac, cMac) {
		return false
	}
	return true
}

// splitNIC parses "model=MAC,bridge=BR[,firewall=0|1],...".
func splitNIC(s string) (model, bridge string, mac string) {
	// First token before "=" is the model (or "model=MAC").
	// "virtio=52:54:00:aa:bb:cc,bridge=vmbr0,firewall=0"
	parts := strings.Split(s, ",")
	for _, p := range parts {
		if p == "" {
			continue
		}
		// first: model=MAC
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 && (model == "") {
			model = kv[0]
			mac = kv[1]
			continue
		}
		if p == "virtio" || p == "e1000" || p == "rtl8139" || p == "vmxnet3" || p == "ne2k_pci" || p == "i82551" || p == "i82552" || p == "e1000-82544gc" || p == "e1000-82545em" || p == "i82546" {
			if model == "" {
				model = p
			}
			continue
		}
		kv2 := strings.SplitN(p, "=", 2)
		if len(kv2) == 2 && kv2[0] == "bridge" {
			bridge = kv2[1]
		}
	}
	return
}

func pveStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	// PVE list-valued config fields (tags, args, ...) arrive as JSON arrays.
	// Join them with "," to match the form the schema layer produces.
	if arr, ok := v.([]any); ok {
		parts := make([]string, 0, len(arr))
		for _, e := range arr {
			parts = append(parts, pveStr(e))
		}
		return strings.Join(parts, ",")
	}
	return fmt.Sprintf("%v", v)
}

// pveInt coerces PVE config values to int. PVE returns numerics as Go int
// (JSON number) or occasionally string; tolerate both.
func pveInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

// tagsEqual reports whether PVE's current tags (array or comma string) contain
// exactly the desired set, order-independent. This is the owned-field
// comparison for tags: PVE appends/permutes tag order internally, so a
// positional compare would false-positive on every managed object.
func tagsEqual(current any, desired []string) bool {
	got := map[string]bool{}
	switch c := current.(type) {
	case []any:
		for _, e := range c {
			if s, ok := e.(string); ok && s != "" {
				got[s] = true
			}
		}
	case string:
		for _, t := range strings.Split(c, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				got[t] = true
			}
		}
	}
	des := map[string]bool{}
	for _, t := range desired {
		if t != "" {
			des[t] = true
		}
	}
	if len(got) != len(des) {
		return false
	}
	for t := range des {
		if !got[t] {
			return false
		}
	}
	return true
}

func validDiskSlot(s string) bool {
	return strings.HasPrefix(s, "scsi") || strings.HasPrefix(s, "virtio") ||
		strings.HasPrefix(s, "sata") || strings.HasPrefix(s, "ide") ||
		strings.HasPrefix(s, "sata") || strings.HasPrefix(s, "nvme")
}

func validNetSlot(s string) bool {
	return strings.HasPrefix(s, "net") && len(strings.TrimPrefix(s, "net")) > 0
}
