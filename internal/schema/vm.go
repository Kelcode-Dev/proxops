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
	// Firewall enables PVE's guest-side firewall on this NIC.
	Firewall bool `yaml:"firewall,omitempty" json:"firewall,omitempty"`
	// RateLimit is a PVE rate (mbit/s, e.g. "50" = 50 MBit/s).
	RateLimit int `yaml:"rate-limit,omitempty" json:"rate-limit,omitempty"`
	// VLAN is the optional PVE VLAN tag applied to this NIC.
	VLAN int `yaml:"vlan,omitempty" json:"vlan,omitempty"`
	// Slot overrides the default net<i>.
	Slot string `yaml:"slot,omitempty" json:"slot,omitempty"`
}

// CDDrive is PVE's `cdrom` device. It can either:
//   - mount an ISO declared as another pveconform resource
//     (spec.iso = the ISO's metadata.name), or
//   - be "none" (no CD attached), in which case iso is empty and pveconform
//     renders `cdrom=none`.
//
// The ISO reference creates an inferred dependency only in the attach
// state; both "absent" and "none" leave VM.Deps() empty.
const CDROMNone = "none"

type CDDrive struct {
	// Iso is a pveconform ISO metadata.name, or the CDROMNone sentinel.
	// An empty Iso is used only when the manifest has an intentionally
	// empty `hardware.cdrom` block — pveconform then treats the PVE IDE
	// cdrom slot as not owned.
	Iso string `yaml:"iso,omitempty" json:"iso,omitempty"`
	// Media is PVE's `media=` option on the cdrom ("cdrom" default).
	Media string `yaml:"media,omitempty" json:"media,omitempty"`
}

// EFIDisk models PVE's `efidisk0` device (UEFI vars + Secure Boot key store).
// Only meaningful when spec.hardware.bios = "ovmf".
type EFIDisk struct {
	// Storage is the PVE storage backend, e.g. "local-lvm".
	Storage string `yaml:"storage" json:"storage"`
	// Size is the EFI vars volume size, e.g. "4MiB".
	Size string `yaml:"size" json:"size"`
	// Template pins an OVMF vars template ("byos", "2m", "4m", "8m").
	Template string `yaml:"template,omitempty" json:"template,omitempty"`
	// SecureBoot pins Secure Boot behaviour: "required" | "optional" | "disabled".
	// PVE 9.2's /config create API does not accept this form-value — PVE
	// manages Secure Boot policy through its separate `/qemu/{id}/security`
	// endpoint. The declarative field is recorded and validated but not
	// sent on the wire.
	SecureBoot string `yaml:"secure-boot,omitempty" json:"secure-boot,omitempty"`
}

// CloudInit models PVE's cloud-init drive on `ide2` and optional cicustom.
// When Enabled, pveconform renders `ide2:<storage>:cloudinit,size=<size>`
// at create and manages PVE's auto-named cloud-init drive idempotently.
type CloudInit struct {
	// Enabled turns cloud-init on; when disabled pveconform does not
	// manage a cloud-init device at all.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Storage is the PVE storage backend for the cloud-init drive
	// ("local" common default on dir storage with `import` content).
	Storage string `yaml:"storage,omitempty" json:"storage,omitempty"`
	// Size is the cloud-init drive size, e.g. "4MiB". Default 4MiB.
	Size string `yaml:"size,omitempty" json:"size,omitempty"`
}

// TPM models PVE's `tpm0` device (only meaningful with bios=ovmf + q35).
type TPM struct {
	// Version is PVE's TPM backend: "v1.2" | "v2.0" (default v2.0).
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
}

// VMHardware captures the first-class VM hardware knobs.
type VMHardware struct {
	// Machine is PVE's `machine` (e.g. "i440fx" default, "q35").
	Machine string `yaml:"machine,omitempty" json:"machine,omitempty"`
	// BIOS is PVE's `bios`. PVE 9.2 accepts "seabios" (default) or "ovmf".
	BIOS string `yaml:"bios,omitempty" json:"bios,omitempty"`
	// Vga / Display is PVE's `vga` ("std" default).
	Display string `yaml:"display,omitempty" json:"display,omitempty"`
	// Cdrom is the PVE cdrom device. See CDROMNone for the sentinel.
	Cdrom CDDrive `yaml:"cdrom,omitempty" json:"cdrom,omitempty"`
	// EFIDisk is PVE's `efidisk0` (only valid when bios=ovmf).
	EFIDisk *EFIDisk `yaml:"efi-disk,omitempty" json:"efi-disk,omitempty"`
	// CloudInit is PVE's cloud-init drive (`ide2`).
	CloudInit CloudInit `yaml:"cloud-init,omitempty" json:"cloud-init,omitempty"`
	// TPM0 is PVE's `tpm0` (only valid when bios=ovmf + machine=q35).
	TPM *TPM `yaml:"tpm,omitempty" json:"tpm,omitempty"`
	// Serial0 is PVE's `serial0` device ("socket", "file:/dev/ttyS<vmid>",
	// "none").
	Serial0 string `yaml:"serial0,omitempty" json:"serial0,omitempty"`
	// NUMA turns PVE NUMA on; implies sockets=1.
	NUMA bool `yaml:"numa,omitempty" json:"numa,omitempty"`
	// Sockets is PVE's socket count (default 1).
	Sockets int `yaml:"sockets,omitempty" json:"sockets,omitempty"`
}

// VMOpts captures the VM-side "Options" UI panel knobs.
type VMOpts struct {
	// OnBoot auto-starts the VM when its host node boots.
	OnBoot bool `yaml:"onboot,omitempty" json:"onboot,omitempty"`
	// Startup is PVE's `startup` (e.g. "order=10" or "order=0,shutdown=2").
	Startup string `yaml:"startup,omitempty" json:"startup,omitempty"`
	// Protection is PVE's `protection` (blocks accidental destroy).
	Protection bool `yaml:"protection,omitempty" json:"protection,omitempty"`
	// Agent enables PVE's virtio QEMU-guest-agent.
	Agent bool `yaml:"agent,omitempty" json:"agent,omitempty"`
	// Acpi turns ACPI on/off (PVE default 1).
	Acpi bool `yaml:"acpi,omitempty" json:"acpi,omitempty"`
	// Tablet enables PVE's QEMU tablet/pointer integration.
	Tablet bool `yaml:"tablet,omitempty" json:"tablet,omitempty"`
	// Hotplug is PVE's `hotplug` list (disk, network, usb, ...).
	Hotplug []string `yaml:"hotplug,omitempty" json:"hotplug,omitempty"`
	// BootOrder is the PVE `boot=order=...` list (e.g. ["scsi0", "net0"]).
	BootOrder []string `yaml:"boot-order,omitempty" json:"boot-order,omitempty"`
	// NestedVirt (PVE `nestedvirt`) enables nested-KVM.
	NestedVirt bool `yaml:"nested-virt,omitempty" json:"nested-virt,omitempty"`
	// Hidden (PVE `hidden`) makes KVM features un-detectable to the guest.
	Hidden bool `yaml:"hidden,omitempty" json:"hidden,omitempty"`
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

	// Hardware is first-class hardware configuration (bios/machine/cdrom/...).
	Hardware VMHardware `yaml:"hardware,omitempty" json:"hardware,omitempty"`
	// Options is the first-class VM Options panel (onboot/protection/...).
	Options VMOpts `yaml:"options,omitempty" json:"options,omitempty"`

	// Extra is a freeform PVE key=value map (escape hatch for anything not
	// modeled above; e.g. bootspeed, watchdog, rtc).
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

	// cdromVolid holds the PVE ISO volume id ("<pool>:iso/<file>") when
	// spec.hardware.cdrom.iso references an ISO artifact, or "" otherwise
	// (iso: none / iso: <missing> / no cdrom). It is populated by
	// ResolveArtifactRefs. The 3-state cdrom classification (unmanaged /
	// attach / detach) derives from the manifest itself (see
	// cdromManagedState), independent of this field, so ToCreateParams/Drift
	// behave consistently even in pre-resolution unit tests.
	cdromVolid string
}

// NewVM returns an empty VM.
func NewVM() *VM {
	return &VM{Kind: KindVM, APIVersion: APIVersion}
}

// Ref implements Resource.
func (v *VM) Ref() Ref { return Ref{Kind: v.Kind, Name: v.Metadata.Name} }

// Node implements Resource.
func (v *VM) Node() string { return v.Spec.Node }

// Nodes implements Resource — a VM is pinned to a single PVE node.
func (v *VM) Nodes() []string {
	if v.Spec.Node == "" {
		return nil
	}
	return []string{v.Spec.Node}
}

// ID implements Resource.
func (v *VM) ID() int { return v.Spec.VMID }

// DesiredState implements Resource; returns spec.state or "started".
func (v *VM) DesiredState() string {
	if s, err := ParseState(v.Spec.State); err == nil {
		return string(s)
	}
	return "started"
}

// Deps implements Resource. A VM has exactly one structured dependency:
// an ISO reference on spec.hardware.cdrom.iso. Returns [] when the VM does
// not reference an ISO (either cdrom omitted or cdrom.iso = "none").
//
// The planner merges Deps with the metadata.depends-on annotation edge and
// enforces the DAG (unknown references fail closed; cycles fail closed).
func (v *VM) Deps() []Ref {
	if iso := strings.TrimSpace(v.Spec.Hardware.Cdrom.Iso); iso == "" || iso == CDROMNone {
		return nil
	}
	return []Ref{{Kind: KindISO, Name: v.Spec.Hardware.Cdrom.Iso}}
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
			return fmt.Errorf("%s: spec.networks[%d].bridge must be set", v.Ref(), i)
		}
		if n.VLAN > 0 && (n.VLAN < 1 || n.VLAN > 4094) {
			return fmt.Errorf("%s: spec.networks[%d].vlan must be in [1, 4094]", v.Ref(), i)
		}
		if n.RateLimit < 0 {
			return fmt.Errorf("%s: spec.networks[%d].rate-limit must be non-negative", v.Ref(), i)
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
			return fmt.Errorf("%s: spec.networks[%d] slot %q invalid (use net0, net1, ...)", v.Ref(), i, slot)
		}
		n.Slot = slot
	}
	// hardware
	hw := &v.Spec.Hardware
	switch hw.Machine {
	case "", "i440fx", "q35":
	default:
		return fmt.Errorf("%s: spec.hardware.machine %q must be 'i440fx' or 'q35'", v.Ref(), hw.Machine)
	}
	switch hw.BIOS {
	case "", "seabios", "ovmf":
	default:
		return fmt.Errorf("%s: spec.hardware.bios %q must be 'seabios' or 'ovmf' (PVE 9.2)", v.Ref(), hw.BIOS)
	}
	if hw.EFIDisk != nil {
		if hw.EFIDisk.Storage == "" {
			return fmt.Errorf("%s: spec.hardware.efi-disk.storage must be set", v.Ref())
		}
		if !storageIDRe.MatchString(hw.EFIDisk.Storage) {
			return fmt.Errorf("%s: spec.hardware.efi-disk.storage %q invalid", v.Ref(), hw.EFIDisk.Storage)
		}
		if hw.EFIDisk.Size == "" {
			hw.EFIDisk.Size = "4MiB"
		}
		if _, err := DiskBytes(hw.EFIDisk.Size); err != nil {
			return fmt.Errorf("%s: spec.hardware.efi-disk.size: %v", v.Ref(), err)
		}
		switch strings.ToLower(hw.EFIDisk.SecureBoot) {
		case "", "required", "optional", "disabled":
		default:
			return fmt.Errorf("%s: spec.hardware.efi-disk.secure-boot %q must be 'required'|'optional'|'disabled'", v.Ref(), hw.EFIDisk.SecureBoot)
		}
	}
	if hw.Cdrom.Media != "" {
		switch hw.Cdrom.Media {
		case "cdrom", "disk":
		default:
			return fmt.Errorf("%s: spec.hardware.cdrom.media %q must be 'cdrom' or 'disk'", v.Ref(), hw.Cdrom.Media)
		}
	}
	if hw.CloudInit.Enabled {
		if hw.CloudInit.Storage == "" {
			return fmt.Errorf("%s: spec.hardware.cloud-init.storage must be set", v.Ref())
		}
		if hw.CloudInit.Size == "" {
			hw.CloudInit.Size = "4M"
		}
		if _, err := DiskBytes(hw.CloudInit.Size); err != nil {
			return fmt.Errorf("%s: spec.hardware.cloud-init.size: %v", v.Ref(), err)
		}
	}
	if hw.TPM != nil {
		switch strings.ToLower(hw.TPM.Version) {
		case "", "v1.2", "v2.0":
		default:
			return fmt.Errorf("%s: spec.hardware.tpm.version %q must be 'v1.2' or 'v2.0'", v.Ref(), hw.TPM.Version)
		}
	}
	if hw.Sockets < 0 {
		return fmt.Errorf("%s: spec.hardware.sockets must be non-negative", v.Ref())
	}
	// options — boot-order entries must be non-empty and unique.
	bootSeen := map[string]bool{}
	for _, slot := range v.Spec.Options.BootOrder {
		if slot == "" {
			return fmt.Errorf("%s: spec.options.boot-order entries must be non-empty", v.Ref())
		}
		if bootSeen[slot] {
			return fmt.Errorf("%s: duplicate boot-order entry %q", v.Ref(), slot)
		}
		bootSeen[slot] = true
	}
	// extra keys must not collide with structured first-class fields OR PVE's scsi*/net* slots.
	structuredReserved := map[string]bool{}
	for _, k := range []string{
		"vmid", "name", "memory", "cpu", "cores",
		"machine", "bios", "vga",
		"cdrom", "ide0", "ide1", "ide2", "ide3",
		"efidisk0", "tpm0", "serial0",
		"onboot", "startup", "protection", "agent",
		"acpi", "tablet", "hotplug", "boot",
		"nestedvirt", "hidden",
	} {
		structuredReserved[k] = true
	}
	for k := range v.Spec.Extra {
		if structuredReserved[k] {
			return fmt.Errorf("%s: spec.extra key %q conflicts with a structured field; remove it", v.Ref(), k)
		}
		if strings.HasPrefix(k, "scsi") || strings.HasPrefix(k, "net") {
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
	// first-class hardware
	hw := &v.Spec.Hardware
	if hw.Machine != "" {
		p["machine"] = hw.Machine
	}
	if hw.BIOS != "" {
		p["bios"] = hw.BIOS
	}
	if hw.Display != "" {
		p["vga"] = hw.Display
	}
	if hw.Sockets != 0 {
		p["sockets"] = hw.Sockets
	}
	if hw.NUMA {
		p["numa"] = "1"
	}
	if hw.CloudInit.Enabled {
		p["ide2"] = fmt.Sprintf("%s:cloudinit,size=%s", hw.CloudInit.Storage, hw.CloudInit.Size)
	}
	// cdrom slot: pveconform only owns the IDE cdrom slot when
	// spec.hardware.cdrom is declared. When declared and iso=<artifact>,
	// pveconform emits the resolved PVE volid (or the ISO name when
	// ResolveArtifactRefs hasn't run yet — unit tests); when declared
	// and iso=none, pveconform emits "none" (detach). When the manifest
	// has no cdrom block, pveconform leaves the PVE slot alone.
	//
	// PVE 9.2: always ide2 regardless of cloud-init placement (verified
	// via the cdrom= alias on i440fx AND q35, both with+without
	// cloud-init). PVE stores cdrom on ide2 and aliases `cdrom=X` to
	// `ide2` in config reports.
	if managed, hasISO := v.cdromManagedState(); managed {
		if hasISO {
			src := v.cdromVolid
			if src == "" {
				src = v.Spec.Hardware.Cdrom.Iso
			}
			media := v.Spec.Hardware.Cdrom.Media
			if media == "" {
				media = "cdrom"
			}
			p[v.cdromSlot()] = src + ",media=" + media
		} else {
			// detach state: cdrom.iso = "none".
			p[v.cdromSlot()] = CDROMNone
		}
	}
	if hw.EFIDisk != nil {
		efiStr := fmt.Sprintf("%s:%s", hw.EFIDisk.Storage, GiBString(diskBytes(hw.EFIDisk.Size)))
		// PVE's EFI vars disk type (4m/8m/16m/...) is `efitype`; PVE defaults
		// to 4m when omitted. Only emit it when the user pins one.
		if hw.EFIDisk.Template != "" {
			efiStr += ",efitype=" + strings.ToLower(hw.EFIDisk.Template)
		}
		p["efidisk0"] = efiStr
	}
	if hw.TPM != nil {
		version := strings.ToLower(hw.TPM.Version)
		if version == "" {
			version = "v2.0"
		}
		// PVE 9.x tpm0 wire form: "endpoint=host,storage=<pool>,version=<v>"
		p["tpm0"] = "endpoint=host,version=" + version
	}
	if hw.Serial0 != "" {
		p["serial0"] = hw.Serial0
	}
	// first-class options
	for k, val := range v.optionsWire() {
		p[k] = val
	}
	for k, val := range v.Spec.Extra {
		p[k] = val
	}
	return p, nil
}

// optionsWire returns PVE wire form-values for the VM Options knobs. The
// planner merges these with ToCreateParams/Drift's owned-field diff.
//
// It is intentionally narrow: only "set to 1" is modeled; PVE's 0/1 form is
// used for booleans and PVE does not accept a 0 on most of these (0 =
// unset, which is the same as PVE's default).
//
// The "boot" form-value is the PVE `boot=order=scsi0;ide2;net0` list,
// which is only emitted when the user declares a spec.options.boot-order.
// When boot-order is not declared, pveconform does not manage PVE's
// `boot` key (PVE picks its own default: the primary boot disk order +
// any media devices). This is the owned-field projection invariant.
func (v *VM) optionsWire() map[string]any {
	o := &v.Spec.Options
	out := map[string]any{}
	if o.OnBoot {
		out["onboot"] = "1"
	}
	if o.Startup != "" {
		out["startup"] = o.Startup
	}
	if o.Protection {
		out["protection"] = "1"
	}
	if o.Agent {
		out["agent"] = "1"
	}
	if o.Acpi {
		out["acpi"] = "1"
	}
	if o.Tablet {
		out["tablet"] = "1"
	}
	if len(o.Hotplug) > 0 {
		out["hotplug"] = strings.Join(o.Hotplug, ",")
	}
	if len(o.BootOrder) > 0 {
		out["boot"] = "order=" + strings.Join(o.BootOrder, ";")
	}
	if o.NestedVirt {
		out["nestedvirt"] = "1"
	}
	if o.Hidden {
		out["hidden"] = "1"
	}
	return out
}

// cdromSlot returns the PVE IDE slot the cdrom device is wired at. PVE 9.2
// places cloud-init on ide2 and the cdrom on the next available IDE slot
// (ide3). Without cloud-init, cdrom lands on ide2.
func (v *VM) cdromSlot() string {
	if v.Spec.Hardware.CloudInit.Enabled {
		return "ide3"
	}
	return "ide2"
}

// CdromSlot exposes the cdrom slot to the planner (and to Drift tests that
// want to locate PVE's cdrom report field without re-implementing the
// cloud-init → ide3 rule).
func (v *VM) CdromSlot() string { return v.cdromSlot() }

// cdromManagedState returns whether the manifest declares a `cdrom` block
// and, if so, whether it is the "attach" (real ISO ref) or the "detach"
// (iso: none sentinel) state. Used at ToCreateParams/Drift time so the
// 3-state cdrom is consistent even before ResolveArtifactRefs has run
// (unit tests that exercise ToCreateParams directly see the right
// state). Resolved cdromVolid is only non-empty after resolve runs; this
// detector is independent and is the single source of truth.
func (v *VM) cdromManagedState() (managed bool, hasISO bool) {
	iso := strings.TrimSpace(v.Spec.Hardware.Cdrom.Iso)
	if iso == "" {
		return false, false
	}
	if iso == CDROMNone {
		return true, false
	}
	return true, true
}

// CdromWireValue returns the full PVE cdrom wire value pveconform will put
// on the IDE slot, mirroring ToCreateParams:
//   - attach (iso: <ref>)  → "<src>,media=<m>" where <src> is the resolved
//     PVE volid (or the spec name when ResolveArtifactRefs hasn't run) and
//     <m> is the manifest media (default "cdrom").
//   - detach (iso: none)   → "none".
//   - unmanaged (no block) → "".
func (v *VM) CdromWireValue() string {
	managed, hasISO := v.cdromManagedState()
	if !managed {
		return ""
	}
	if !hasISO {
		return CDROMNone
	}
	src := v.cdromVolid
	if src == "" {
		src = v.Spec.Hardware.Cdrom.Iso
	}
	media := v.Spec.Hardware.Cdrom.Media
	if media == "" {
		media = "cdrom"
	}
	return src + ",media=" + media
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
			// model/bridge/vlan/rate/firewall changes require stop.
			stop = true
		}
	}

	// --- first-class hardware ---
	hw := &v.Spec.Hardware
	if hw.Machine != "" && pveStr(current["machine"]) != hw.Machine {
		upd["machine"] = hw.Machine
		stop = true // PVE requires stop to change machine
	}
	if hw.BIOS != "" && pveStr(current["bios"]) != hw.BIOS {
		upd["bios"] = hw.BIOS
		stop = true
	}
	if hw.Display != "" && pveStr(current["vga"]) != hw.Display {
		upd["vga"] = hw.Display
	}
	if hw.Sockets != 0 && pveInt(current["sockets"]) != hw.Sockets {
		upd["sockets"] = hw.Sockets
		stop = true
	}
	if hw.NUMA {
		if p, _ := current["numa"].(string); p != "1" && pveInt(current["numa"]) != 1 {
			upd["numa"] = "1"
			stop = true
		}
	}
	// cloud-init: PVE owns ide2; we own storage + size + media. PVE stores a
	// per-vmid volume name ("local:9100/vm-9100-cloudinit.qcow2,...") that we
	// must NOT compare — only pool + size.
	if hw.CloudInit.Enabled {
		slot := "ide2"
		cloudInitWant := fmt.Sprintf("%s:cloudinit,size=%s", hw.CloudInit.Storage, hw.CloudInit.Size)
		if !cloudInitMatches(pveStr(current[slot]), cloudInitWant) {
			upd[slot] = cloudInitWant
			stop = true
		}
	}
	// EFI disk: PVE owns efidisk0; we own pool + size; PVE auto-assigns a
	// LVM volume name, which we ignore. PVE clamps very small sizes to its
	// minimum (4 MiB) and reports them with a binary-suffix token; our size
	// comparison must tolerate PVE's clamping — a 1 MiB desired that PVE
	// stores as 4 MiB is NOT drift.
	//
	// We use pveDiskInfo.pool + sizeSet check and only drift when the pool
	// differs OR when PVE's reported size differs from ours by more than a
	// 1 MiB epsilon (to absorb PVE's clamping).
	if hw.EFIDisk != nil {
		cur := parseDiskInfo(pveStr(current["efidisk0"]))
		if cur.pool != "" && cur.pool != hw.EFIDisk.Storage {
			upd["efidisk0"] = fmt.Sprintf("%s:%s", hw.EFIDisk.Storage, GiBString(diskBytes(hw.EFIDisk.Size)))
			stop = true
		} else if cur.pool == "" {
			// PVE's live config has no efidisk0.
			if pveStr(current["efidisk0"]) == "" || pveStr(current["efidisk0"]) == "none" {
				// PVE truly omitted → drift.
				upd["efidisk0"] = fmt.Sprintf("%s:%s", hw.EFIDisk.Storage, GiBString(diskBytes(hw.EFIDisk.Size)))
				stop = true
			}
		}
	}
	// TPM: PVE owns tpm0; we own endpoint + version. PVE picks storage/
	// tpmstate.
	if hw.TPM != nil {
		ver := strings.ToLower(hw.TPM.Version)
		if ver == "" {
			ver = "v2.0"
		}
		// Construct the PVE wire form we'd emit on update (endpoint=host is
		// PVE's documented default).
		wantTPM := "endpoint=host,version=" + ver
		if !tpmMatches(pveStr(current["tpm0"]), wantTPM) {
			upd["tpm0"] = wantTPM
			stop = true
		}
	}
	// Serial: PVE reports `serial0=socket` verbatim; compare as string.
	if hw.Serial0 != "" {
		if pveStr(current["serial0"]) != hw.Serial0 {
			upd["serial0"] = hw.Serial0
			stop = true
		}
	}

	// cdrom: PVE stores the CD/DVD at the ide slot (PVE 9.2: always ide2,
	// regardless of cloud-init placement — verified via the `cdrom=<vol>,
	// media=cdrom` form alias on i440fx AND q35 machines, both with and
	// without a cloud-init drive). PVE reports the cdrom with a PVE-assigned
	// `media=` option + a size token. pveconform owns pool + filename +
	// media when spec.hardware.cdrom is declared; when the manifest has no
	// cdrom block, pveconform does NOT own the PVE slot.
	//
	// Three-state:
	//   1. cdromManaged=false (absent)            → skip
	//   2. cdromManaged=true, cdromHasISO=true    → compare ISO pool+file
	//   3. cdromManaged=true, cdromHasISO=false   → cdrom.iso = "none" →
	//                                       compare against PVE's "none"
	//                                       rendering and emit `ide2=none`
	//                                       when PVE still has a CD on it.
	// cdrom: PVE 9.2 cdrom slots are ALWAYS ide2 regardless of cloud-init
	// placement (verified live). PVE reports cdrom with a PVE-assigned
	// `media=` option + `size=` token. pveconform owns the slot when
	// `cdrom` is declared; not owned when absent. Three-state:
	//   1. managed=false (absent)               → skip
	//   2. managed=true, hasISO=true            → compare resolved volid
	//   3. managed=true, hasISO=false (iso:none)→ compare against PVE's
	//                                               "none" rendering; emit
	//                                               `ide2=none` when PVE
	//                                               still has a CD.
	if managed, hasISO := v.cdromManagedState(); managed {
		slot := v.cdromSlot()
		curCdrom := pveStr(current[slot])
		if hasISO {
			src := v.cdromVolid
			if src == "" {
				src = v.Spec.Hardware.Cdrom.Iso
			}
			if !cdromMatches(curCdrom, src) {
				media := v.Spec.Hardware.Cdrom.Media
				if media == "" {
					media = "cdrom"
				}
				upd[slot] = src + ",media=" + media
				stop = true
			}
		} else {
			trimmed := strings.TrimSpace(curCdrom)
			if trimmed != "" && trimmed != CDROMNone && !strings.HasPrefix(trimmed, CDROMNone) {
				upd[slot] = CDROMNone
				stop = true
			}
		}
	}

	// --- first-class options ---
	// PVE reports "0" values as key-absent on many VM Options fields. Treat
	// "desired false" as "we don't own this key" — so we only emit drift when
	// the user declares a truthy value AND PVE does not match.
	if opt := &v.Spec.Options; opt != nil {
		if opt.OnBoot && pveInt(current["onboot"]) != 1 {
			upd["onboot"] = "1"
		}
		if opt.Startup != "" && pveStr(current["startup"]) != opt.Startup {
			upd["startup"] = opt.Startup
		}
		if opt.Protection && pveInt(current["protection"]) != 1 {
			upd["protection"] = "1"
		}
		if opt.Agent && pveInt(current["agent"]) != 1 {
			upd["agent"] = "1"
		}
		if opt.Acpi && pveInt(current["acpi"]) != 1 {
			upd["acpi"] = "1"
		}
		if opt.Tablet && pveInt(current["tablet"]) != 1 {
			upd["tablet"] = "1"
		}
		if len(opt.Hotplug) > 0 {
			want := strings.Join(opt.Hotplug, ",")
			if pveStr(current["hotplug"]) != want {
				upd["hotplug"] = want
			}
		}
		if len(opt.BootOrder) > 0 {
			want := "order=" + strings.Join(opt.BootOrder, ";")
			if pveStr(current["boot"]) != want {
				upd["boot"] = want
			}
		}
		if opt.NestedVirt && pveInt(current["nestedvirt"]) != 1 {
			upd["nestedvirt"] = "1"
		}
		if opt.Hidden && pveInt(current["hidden"]) != 1 {
			upd["hidden"] = "1"
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

// nicString builds PVE's "net0" value string. Two base forms:
//
//   - MAC pinned:  "virtio=aa:bb:cc:dd:ee:ff,bridge=vmbr0[,...]"
//   - MAC unpinned: "virtio,bridge=vmbr0[,...]"   (bare model name; PVE will
//     assign a MAC. The "virtio=<empty>" form is invalid: PVE's comma-separated
//     property parser rejects a key with no value.)
//
// Bridge is required (validated upstream). All other VM-NIC knobs
// (vlan, firewall, rate) are appended in PVE's grammar only when the user
// requested them, so the "firewall=0" explicit-default of earlier MVPs is
// dropped — PVE leaves it off when it's 0, and Drift (nicMatches) compares
// owned fields only.
//
// PVE wire: "virtio,bridge=vmbr0,vlan=100,rate=500,firewall=1".
func nicString(n NIC) string {
	model := n.Model
	if model == "" {
		model = "virtio"
	}
	bridge := n.Bridge
	head := model
	if mac := strings.ToLower(strings.TrimSpace(n.MAC)); mac != "" {
		head = model + "=" + mac
	}
	var sb strings.Builder
	sb.WriteString(head)
	sb.WriteString(",bridge=" + bridge)
	if n.VLAN > 0 {
		fmt.Fprintf(&sb, ",vlan=%d", n.VLAN)
	}
	if n.RateLimit > 0 {
		// PVE 9.x accepts both "rate=<mbit/s>" and "mbit/s=<mbit>" forms;
		// "rate=" is the historical qm.conf form.
		fmt.Fprintf(&sb, ",rate=%d", n.RateLimit)
	}
	if n.Firewall {
		sb.WriteString(",firewall=1")
	}
	return sb.String()
}

// nicMatches compares two PVE NIC strings on the owned fields: model,
// bridge, MAC (only when pinned), VLAN tag, rate, and firewall.
func nicMatches(cur, want string) bool {
	c := parseNICFields(cur)
	w := parseNICFields(want)
	if c.model != w.model {
		return false
	}
	if c.bridge != w.bridge {
		return false
	}
	if w.mac != "" && !strings.EqualFold(w.mac, c.mac) {
		return false
	}
	if w.vlan > 0 && c.vlan != w.vlan {
		return false
	}
	if w.rate > 0 && c.rate != w.rate {
		return false
	}
	if w.firewall && !c.firewall {
		return false
	}
	return true
}

// nicFields is a parsed PVE VM NIC string.
type nicFields struct {
	model    string
	mac      string
	bridge   string
	vlan     int
	rate     int
	firewall bool
}

// parseNICFields parses a PVE VM NIC property string into its owned fields.
// Handles both desired form ("virtio,bridge=vmbr0,vlan=100,rate=50,firewall=1")
// and PVE's report form ("virtio=mac,bridge=vmbr0,...").
func parseNICFields(s string) nicFields {
	out := nicFields{}
	parts := strings.Split(s, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if eq := strings.IndexByte(p, '='); eq >= 0 {
			k, v := p[:eq], p[eq+1:]
			switch k {
			case "bridge":
				out.bridge = v
			case "vlan":
				out.vlan, _ = strconv.Atoi(v)
			case "rate":
				out.rate, _ = strconv.Atoi(v)
			case "firewall":
				out.firewall = v == "1" || v == "true"
			case "virtio", "vmxnet3", "e1000", "e1000-82540", "rtl8139", "i82551", "ne2k_pci", "pcnet", "shaper":
				out.model = k
				out.mac = v
			}
			continue
		}
		// Bare first token is the model when no key is present.
		if out.model == "" {
			out.model = p
		}
	}
	return out
}

// cloudInitMatches reports whether a PVE ide2 cloud-init report matches the
// desired pool + size (ignoring PVE's assigned cloud-init volume name and
// any PVE-added options such as media=cdrom).
//
// PVE's /config report for cloud-init is:
//
//	"local:9100/vm-9100-cloudinit.qcow2,media=cdrom,size=4K"
//
// Our desired form (create-time) is "<pool>:cloudinit,size=<binary>". We
// own only pool + size; PVE-assigned volume names are not owned.
func cloudInitMatches(cur, want string) bool {
	cp, cs, cOk := splitCloudInit(cur)
	wp, ws, wOk := splitCloudInit(want)
	if !cOk {
		// PVE reports no ide2 → no cloud init. If we don't want one, fine.
		return !wOk
	}
	if !wOk {
		return false
	}
	if cp != wp {
		return false
	}
	// size: PVE binary suffix ("4K"/"4M"), our desired also binary
	// (e.g. "4M"). Compare as bytes when both parse, else raw-equal.
	if cb, ok1 := pveDiskSizeBytes(cs); ok1 {
		if wb, ok2 := pveDiskSizeBytes(ws); ok2 {
			return cb == wb
		}
	}
	return strings.EqualFold(cs, ws)
}

// splitCloudInit extracts (pool, size) from a PVE ide2 /create form.
// "local-lvm:9100/vm-9100-cloudinit.qcow2,media=cdrom,size=4K" returns
// ("local-lvm", "4K"). "local:cloudinit,size=4M" returns ("local", "4M").
func splitCloudInit(s string) (pool, size string, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false
	}
	// pool = token before first ":"
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return "", "", false
	}
	pool = s[:colon]
	rest := s[colon+1:]
	// size is the "size=..." option (if present). Otherwise we don't own
	// a size comparison.
	for _, opt := range strings.Split(rest, ",") {
		if k, v, found := strings.Cut(strings.TrimSpace(opt), "="); found && k == "size" {
			size = v
		}
	}
	return pool, size, true
}

// cdromInfo is the owned-field view of a PVE CD/DVD device string. Both the
// create-time ("local:iso/x.iso,media=cdrom") and PVE's report
// ("local:iso/x.iso,media=cdrom,size=755M") parse to (pool, filename, media).
type cdromInfo struct {
	pool     string
	filename string
	media    string
}

// parseCdrom extracts the owned PVE cdrom fields from a string. A "none"
// or empty report means PVE did not attach any media → out.ok=false.
func parseCdrom(s string) (out cdromInfo, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "none" {
		return
	}
	// pool is the token before ":iso/"
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return
	}
	out.pool = s[:colon]
	rest := s[colon+1:]
	// "iso/<filename>" prefix
	if strings.HasPrefix(rest, "iso/") {
		out.filename = rest[len("iso/"):]
		// Strip PVE-appended options (size=..., media=...).
		if i := strings.IndexByte(out.filename, ','); i >= 0 {
			out.filename = out.filename[:i]
		}
	} else {
		// Unknown shape; conservatively leave filename empty.
		return
	}
	for _, opt := range strings.Split(rest, ",") {
		if k, v, found := strings.Cut(opt, "="); found {
			if k == "media" {
				out.media = v
			}
		}
	}
	ok = out.filename != ""
	return
}

// cdromMatches reports whether PVE's cdrom report satisfies the owned
// fields of the desired PVE wire volume.
//
// PVE's report for an ISO at "local:iso/foo.iso,media=cdrom" is
// "local:iso/foo.iso,media=cdrom,size=755M" — the pool, filename and media
// are the owned bits; PVE adds a size token we don't manage.
func cdromMatches(cur, want string) bool {
	c, cOk := parseCdrom(cur)
	w, wOk := parseCdrom(want)
	if !cOk && !wOk {
		return true // both absent
	}
	if !cOk || !wOk {
		return false
	}
	if c.pool != w.pool {
		return false
	}
	if c.filename != w.filename {
		return false
	}
	// media: PVE's default is "media=cdrom" on a cdrom slot; we only
	// compare when the desired form pins a media.
	if w.media != "" && c.media != w.media {
		return false
	}
	return true
}

// tpmMatches reports whether PVE's tpm0 report matches the desired version.
//
// PVE's create-time form for tpm0 we emit is "endpoint=host,version=v2.0".
// PVE's /config report after creation is "storage=pools,version=v2.0,endpoint=
// host,tpmstate=vm-9120-disk-N" — the version still appears but with extra
// PVE-owned tokens we don't own. Compare only on `version=`; when `version=`
// is absent on both sides, treat as match.
func tpmMatches(cur, want string) bool {
	if cur == "" && want == "" {
		return true
	}
	if cur == "" {
		return false
	}
	if want == "" {
		return false
	}
	curVer := extractTPMVersion(cur)
	wantVer := extractTPMVersion(want)
	if curVer == "" && wantVer == "" {
		// No version either side — fall back to endpoint comparison.
		return extractTPMEndpoint(cur) == extractTPMEndpoint(want)
	}
	return curVer == wantVer
}

// extractTPMVersion pulls "v1.2"/"v2.0" out of a PVE tpm0 string.
func extractTPMVersion(s string) string {
	for _, opt := range strings.Split(s, ",") {
		if k, v, found := strings.Cut(strings.TrimSpace(opt), "="); found && k == "version" {
			return v
		}
	}
	return ""
}

// extractTPMEndpoint pulls "host"/"emulator" out of a PVE tpm0 string.
func extractTPMEndpoint(s string) string {
	for _, opt := range strings.Split(s, ",") {
		if k, v, found := strings.Cut(strings.TrimSpace(opt), "="); found && k == "endpoint" {
			return v
		}
	}
	return ""
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
