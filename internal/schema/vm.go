package schema

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// PveOwnershipTag is the PVE tag the agent adds to every object it manages.
// Pruning only ever acts on tagged objects (see plan §10).
const PveOwnershipTag = "proxops"

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
	// Size is the disk size, e.g. "50GiB". Required unless Image is set
	// (an image-seeded disk takes its size from the image — PVE derives it
	// at import time and proxops does not own the number).
	Size string `yaml:"size" json:"size"`
	// Image references a DiskImage artifact by metadata.name. When set,
	// proxops seeds this disk at create via PVE 9's
	// `<pool>:0,import-from=<storage>:import/<filename>` form (the only
	// supported way to boot a VM from a cloud image without a template).
	// Image-seeded disks are imported ONCE (at create or when the slot is
	// empty); proxops never re-imports over a live volume (data loss).
	Image string `yaml:"image,omitempty" json:"image,omitempty"`
	// Slot is the PVE slot, e.g. "scsi0". Defaults to scsi<i> in order.
	// Named "interface" in the declarative YAML to match user docs.
	Slot string `yaml:"interface,omitempty" json:"interface,omitempty"`
	// Controller applies PVE's scsihw (VM-wide). The first disk carrying a
	// controller sets PVE's "scsihw" key.
	Controller string `yaml:"controller,omitempty" json:"controller,omitempty"`
	// IOThread requests a dedicated I/O thread for this disk.
	IOThread bool `yaml:"iothread,omitempty" json:"iothread,omitempty"`
	// Discard is PVE's drive `discard=` option: "ignore" | "on" (TRIM/
	// discard passthrough). "" = not owned (proxops does not send the token
	// and does not drift against a live value). Probe-verified PVE 9.2.2:
	// accepted inline on every bus (scsi/virtio/sata/ide), toggled in place
	// on stopped AND running VMs via the live drive form.
	Discard string `yaml:"discard,omitempty" json:"discard,omitempty"`
	// SSD is PVE's drive `ssd=` option (1|0): advertise an SSD to the guest.
	// nil = not owned. Probe-verified PVE 9.2.2: accepted inline on
	// scsi/sata/ide ONLY — virtio (and nvme) REJECT the token ("property is
	// not defined in schema"), so Validate fails closed on those buses.
	// PVE retains an explicit `ssd=0` in the report, so the tri-state is
	// needed to distinguish "not owned" from "pinned off".
	SSD *bool `yaml:"ssd,omitempty" json:"ssd,omitempty"`
	// AIO is PVE's drive `aio=` option: "native" | "threads" | "io_uring".
	// "" = not owned. Probe-verified PVE 9.2.2: accepted inline on every
	// bus, toggled in place via the live drive form.
	AIO string `yaml:"aio,omitempty" json:"aio,omitempty"`

	// imageVolid holds the resolved DiskImage PVE volume id
	// ("<storage>:import/<filename>") after ResolveArtifactRefs; "" before.
	imageVolid string
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
//   - mount an ISO declared as another proxops resource
//     (spec.iso = the ISO's metadata.name), or
//   - be "none" (no CD attached), in which case iso is empty and proxops
//     renders `cdrom=none`.
//
// The ISO reference creates an inferred dependency only in the attach
// state; both "absent" and "none" leave VM.Deps() empty.
const CDROMNone = "none"

type CDDrive struct {
	// Iso is a proxops ISO metadata.name, or the CDROMNone sentinel.
	// An empty Iso is used only when the manifest has an intentionally
	// empty `hardware.cdrom` block — proxops then treats the PVE IDE
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
	// PVE reports it as the `efitype=` token on efidisk0.
	Template string `yaml:"template,omitempty" json:"template,omitempty"`
	// SecureBoot enables/disables Secure Boot key pre-enrollment:
	// "enabled" | "disabled" ("" = not owned → proxops does not send the
	// token and does not drift against a live value).
	//
	// M13 (probe-verified on conformance-dev, PVE 9.2.2): the earlier
	// assumption of a separate `/qemu/{id}/security` endpoint is FALSE —
	// that endpoint returns HTTP 501 (not implemented) on GET/PUT/POST, and
	// a `secure-boot=` token is rejected (400) both inline on efidisk0 and
	// top-level. The real wire form is the `pre-enrolled-keys=<0|1>` token
	// on efidisk0: "enabled" → pre-enrolled-keys=1, "disabled" → =0. PVE
	// additionally auto-adds an `ms-cert=<2011|2023|2023k|2023w>` token to
	// the report when keys are pre-enrolled; proxops treats ms-cert as
	// PVE-owned (preserved verbatim on live-form rewrites, never compared).
	// The toggle converges in place on stopped AND running VMs (enrollment
	// takes effect at the next boot).
	SecureBoot string `yaml:"secure-boot,omitempty" json:"secure-boot,omitempty"`
}

// CloudInit models PVE's cloud-init drive on `ide2` and optional cicustom.
// When Enabled, proxops renders `ide2:<storage>:cloudinit,size=<size>`
// at create and manages PVE's auto-named cloud-init drive idempotently.
//
// This is the cloud-init DRIVE (ide2 storage volume). It is distinct from
// the top-level cloud-init DATA fields that PVE's cloud-init generates
// configdrive content from — see CloudInitData below.
type CloudInit struct {
	// Enabled turns cloud-init on; when disabled proxops does not
	// manage a cloud-init device at all.
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Storage is the PVE storage backend for the cloud-init drive
	// ("local" common default on dir storage with `import` content).
	Storage string `yaml:"storage,omitempty" json:"storage,omitempty"`
	// Size is the cloud-init drive size, e.g. "4MiB". Default 4MiB.
	Size string `yaml:"size,omitempty" json:"size,omitempty"`
}

// CloudInitRedactedSentinel is the marker proxops uses in ssh-keys entries
// to signal "PVE owns this value; proxops must not overwrite it". adopt
// uses it for ssh-keys when PVE reports a non-empty value (public-key
// material is treated as credential-adjacent; M10 redaction rule).
//
// Semantics on wire:
//   - ToCreateParams omits the sshkeys field entirely when the sole ssh-keys
//     entry is the sentinel (or when ssh-keys is empty): the VM is created
//     without a proxops-owned cloud-init sshkeys set, and PVE's existing
//     value survives untouched.
//   - Drift is a no-op on sshkeys whenever the desired contains the sentinel:
//     proxops will not write over PVE's keys.
//
// The sentinel is a single character that operators search for in committed
// manifests: `ssh-keys: ["*"]` means "fill me in before apply; until then
// proxops does not touch PVE's sshkeys".
const CloudInitRedactedSentinel = "*"

// CloudInitIPConfig is one PVE `ipconfig<N>` entry. proxops's declarative
// form: NIC (int, PVE's "ipconfig<N>" — i.e. which physical NIC index the
// static-IP applies to), IP (CIDR like "192.168.192.199/18") and optional
// Gateway (like "192.168.192.5"). PVE also supports `ipconfig<N>=dhcp`
// (no static config); that form is not modelled in M11 — operators use
// `spec.extra` if they need dhcp per-nic.
type CloudInitIPConfig struct {
	// NIC is PVE's `ipconfig<N>` slot index (0-based, matching net<N>).
	// Default when zero: 0.
	NIC int `yaml:"nic,omitempty" json:"nic,omitempty"`
	// IP is a CIDR (e.g. "192.168.192.199/18"). Empty → no static IP for
	// this NIC slot (proxops will not write ipconfig<N> at all).
	IP string `yaml:"ip,omitempty" json:"ip,omitempty"`
	// Gateway is the default-gateway address (e.g. "192.168.192.5").
	// Empty → omitted from the wire value (PVE only sets static IP).
	Gateway string `yaml:"gateway,omitempty" json:"gateway,omitempty"`
}

// CloudInitData models PVE's top-level cloud-init DATA fields: the configdrive
// content PVE generates from these. Separate from spec.hardware.cloud-init
// (which owns the ide2 drive itself).
//
// Wire keys (PVE /config report + create/POST form-values):
//
//	ciuser        (string; e.g. "operator")
//	sshkeys       (comma-separated public-key material; credential-adjacent —
//	                M10 gap-value redaction rule applies in adopt)
//	nameserver    (space-separated IPv4/IPv6 CSV)
//	searchdomain  (space-separated IPv4/IPv6 CSV)
//	ipconfig<N>   ("ip=<cidr>[,gw=<addr>]" static or "dhcp")
//
// proxops owns these fields on a VM manifest only when the corresponding
// struct value is non-empty. Empty desired values mean "proxops does not
// own this PVE key; PVE's live state survives untouched".
//
// M13.2: two new fields carry SOPS-referenced secret material.
//   - SSHKeyRefs (plural): cloud-init.ssh-keys.<name> dot-paths that resolve
//     to OpenSSH public keys in the cluster's SOPS document.
//   - CIPasswordRef (singular): cloud-init.passwords.<name> dot-path that
//     resolves to PVE's cipassword value. ProxOps NEVER stores the plaintext
//     password in a Git-managed manifest; PVE 9.2 masks the password on
//     readback (probe-verified M13.2: fixed '**********' — the plaintext is
//     unrecoverable and cannot be compared on drift).
//
// Not modelled (still in GAPS.md): cicustom, ciupgrade.
type CloudInitData struct {
	// CIUser is PVE's `ciuser`: the first configdrive username PVE creates
	// for cloud-init. Empty → not owned.
	CIUser string `yaml:"ci-user,omitempty" json:"ci-user,omitempty"`
	// SSHKeys is PVE's `sshkeys` (each entry is one OpenSSH public key line;
	// PVE joins them with the \n equivalent `%0A` at wire time). Empty →
	// not owned. A single `*` entry is the redacted-sentinel and means
	// "PVE owns this; do not write it".
	//
	// M13.2 backwards-compat: existing manifests that carry plaintext keys
	// remain valid. New manifests SHOULD use SSHKeyRefs (SOPS-resolved).
	// Mixing SSHKeys + SSHKeyRefs on the same manifest fails closed at
	// Validate() (ambiguous intent).
	SSHKeys []string `yaml:"ssh-keys,omitempty" json:"ssh-keys,omitempty"`
	// SSHKeyRefs is M13.2: a PLURAL list of SOPS doc dot-paths, each in the
	// form "cloud-init.ssh-keys.<name>". The cluster's SOPS document must
	// carry a `cloud-init.ssh-keys` mapping with matching keys; the
	// referenced values are concatenated IN MANIFEST ORDER into PVE's
	// `sshkeys` field at create/diff time. Empty → no SOPS-resolved keys.
	SSHKeyRefs []string `yaml:"ssh-key-refs,omitempty" json:"ssh-key-refs,omitempty"`
	// CIPasswordRef is M13.2 (singular): a SOPS doc dot-path of the form
	// "cloud-init.passwords.<name>". Resolved to PVE's `cipassword` wire
	// value. The PVE readback mask (`**********`) means ProxOps CANNOT
	// verify password equality on update; the Drift rule is:
	//   - create: if ref set and no live cipassword yet, PVE receives the
	//     resolved password.
	//   - update: if ref set and live reports the mask form (any
	//     non-empty `cipassword=` value is treated as "a password exists"),
	//     PROXOPS DOES NOT REWRITE — PVE has stored something the operator
	//     previously set; proxops has no way to verify which password it
	//     is, and rewriting would clobber it. The resolved value is still
	//     emitted in the create-form params (so a from-scratch recreate
	//     reuses the manifest's declared password), but the update path
	//     is satisfied.
	//   - if ref is EMPTY, ProxOps does NOT own the PVE cipassword field and
	//     leaves a live mask alone.
	// The SOPS-resolved value lives ONLY in memory for the lifetime of the
	// reconcile operation; it never reaches logs, diffs, manifests, or
	// errors.
	CIPasswordRef string `yaml:"ci-password-ref,omitempty" json:"ci-password-ref,omitempty"`
	// Nameservers is PVE's `nameserver` (space-separated CSV on the wire).
	// Empty → not owned.
	Nameservers []string `yaml:"nameservers,omitempty" json:"nameservers,omitempty"`
	// SearchDomains is PVE's `searchdomain` (space-separated CSV on the wire).
	// Empty → not owned.
	SearchDomains []string `yaml:"search-domains,omitempty" json:"search-domains,omitempty"`
	// IPConfigs is PVE's `ipconfig<N>` static-IP set — one entry per NIC slot
	// proxops wants a cloud-init static IP on. Empty NIC defaults to 0.
	IPConfigs []CloudInitIPConfig `yaml:"ipconfigs,omitempty" json:"ipconfigs,omitempty"`

	// resolvedSshKeys (M13.2) is the SOPS-resolved concatenation of
	// CloudInitData.SSHKeyRefs, populated by schema.ResolveCloudInitSecrets
	// at the start of every reconcile cycle. Empty before resolution.
	// Unexported so the manifest does not round-trip SOPS values through
	// YAML/JSON (secret safety: plaintext / public keys must never appear
	// in a committed manifest — only the reference name does).
	resolvedSshKeys []string
	// resolvedCIPassword (M13.2) is the SOPS-resolved password for
	// CloudInitData.CIPasswordRef. Populated by ResolveCloudInitSecrets.
	// Unexported for the same reason as resolvedSshKeys.
	resolvedCIPassword string
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
	// PCIDevices are PVE's host PCI passthrough slots (`hostpci<N>`).
	// M13.2 (the reference estate Talos GPU VMs). The structured shape intentionally
	// models only PVE's BDF + `pcie=` token; PVE's other optional tokens
	// (x-vga, rombar, mdev, boot, dimmable, sub-vfid, legacy-irr-qworkaround)
	// are out of schema scope — see docs/GAPS.md. The schema validates every
	// `hostpciN` slot + BDF syntactically and fails closed on duplicates.
	PCIDevices []PCIDevice `yaml:"pci-devices,omitempty" json:"pci-devices,omitempty"`
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
	// Tags are PVE user tags (in addition to the proxops tag).
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
	// CloudInitData are PVE's top-level cloud-init DATA fields (ciuser,
	// sshkeys, nameserver, searchdomain, ipconfig<N>). M11. Distinct from
	// spec.hardware.cloud-init (which owns the cloud-init DRIVE on ide2).
	// Empty values mean proxops does not own that PVE key.
	CloudInitData CloudInitData `yaml:"cloud-init-data,omitempty" json:"cloud-init-data,omitempty"`

	// Extra is a freeform PVE key=value map (escape hatch for anything not
	// modeled above; e.g. bootspeed, watchdog, rtc).
	Extra map[string]string `yaml:"extra,omitempty" json:"extra,omitempty"`

	// State is the desired power state: "started" (default) or "stopped".
	State string `yaml:"state,omitempty" json:"state,omitempty"`

	// Clone references a TemplateVM by metadata.name (M12). When set, the
	// VM is provisioned with PVE's full-clone endpoint
	// (POST /nodes/{n}/qemu/{template-vmid}/clone, full=1) instead of a
	// fresh create, then configured with the manifest's own values. The
	// clone creates the VM ONLY — it is never re-run to update an
	// existing VM (drift is corrected via config writes, never by
	// re-cloning over live data).
	//
	// A clone-backed VM MUST NOT declare spec.disks: the clone inherits
	// the template's disk layout, and writing a create-form disk over a
	// cloned live volume is the exact data-loss shape ProxOps forbids.
	// Identity fields the clone copies from the template (hostname,
	// cloud-init user/keys/IP, onboot, ...) are overwritten by the
	// VM's own declared values; keys the VM does not declare are
	// explicitly cleared post-clone (PVE's delete= option) so a clone
	// never silently keeps the template's identity.
	Clone string `yaml:"clone,omitempty" json:"clone,omitempty"`
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

	// cloneSourceID holds the pinned spec.vmid of the referenced TemplateVM
	// after ResolveArtifactRefs (M12). 0 before resolution or when the VM
	// is not clone-backed. The planner reads it to emit a clone-create
	// action; it is never trusted from the manifest itself (the clone
	// target is identified by metadata.name, and its vmid comes from the
	// TemplateVM's own spec — fail-closed against VMID confusion).
	cloneSourceID int
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

// Deps implements Resource. A VM has structured dependencies:
// an ISO reference on spec.hardware.cdrom.iso, and one DiskImage reference
// per image-seeded disk on spec.disks[].image. Returns [] when the VM does
// not reference an ISO (either cdrom omitted or cdrom.iso = "none").
//
// The planner merges Deps with the metadata.depends-on annotation edge and
// enforces the DAG (unknown references fail closed; cycles fail closed).
func (v *VM) Deps() []Ref {
	var refs []Ref
	if iso := strings.TrimSpace(v.Spec.Hardware.Cdrom.Iso); iso != "" && iso != CDROMNone {
		refs = append(refs, Ref{Kind: KindISO, Name: iso})
	}
	for _, d := range v.Spec.Disks {
		if img := strings.TrimSpace(d.Image); img != "" {
			refs = append(refs, Ref{Kind: KindDiskImage, Name: img})
		}
	}
	// M12: clone-backed VM → structured TemplateVM edge. The template must
	// exist (and be marked) before the clone runs; the planner orders the
	// create levels and the executor defers this VM when the template's
	// create failed earlier in the same cycle.
	if tpl := strings.TrimSpace(v.Spec.Clone); tpl != "" {
		refs = append(refs, Ref{Kind: KindTemplateVM, Name: tpl})
	}
	return refs
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
	if len(v.Spec.Disks) == 0 && !v.IsCloneBacked() {
		return fmt.Errorf("%s: spec.disks must be present", v.Ref())
	}
	// M12: a clone-backed VM inherits the template's disk layout. Declaring
	// spec.disks alongside spec.clone would either be ignored (silent drift
	// against a live volume) or, worse, re-state a create-form disk over the
	// cloned volume — the exact data-loss shape ProxOps forbids. Fail closed.
	if v.IsCloneBacked() && len(v.Spec.Disks) > 0 {
		return fmt.Errorf("%s: spec.disks must be empty when spec.clone is set (a clone inherits the template %q's disk layout; proxops will not re-state a disk over a cloned live volume)", v.Ref(), v.Spec.Clone)
	}
	if tpl := strings.TrimSpace(v.Spec.Clone); tpl != "" {
		if tpl == v.Metadata.Name {
			return fmt.Errorf("%s: spec.clone references itself", v.Ref())
		}
		for i := range v.Spec.Disks {
			if v.Spec.Disks[i].Image != "" {
				return fmt.Errorf("%s: spec.disks[%d].image cannot be combined with spec.clone (a clone seeds its disks from the template)", v.Ref(), i)
			}
		}
	}
	seenSlots := map[string]bool{}
	for i := range v.Spec.Disks {
		d := &v.Spec.Disks[i]
		if d.Storage == "" {
			return fmt.Errorf("%s: spec.disks[%d].storage must be set", v.Ref(), i)
		}
		if d.Image != "" {
			// Image-seeded disk: PVE derives the volume size from the image
			// (import-from always uses the :0 size token). A declared size
			// would be silently ignored → fail closed.
			if d.Size != "" {
				return fmt.Errorf("%s: spec.disks[%d].size must be empty when image is set (the imported disk takes its size from the DiskImage %q)", v.Ref(), i, d.Image)
			}
		} else if d.Size == "" {
			return fmt.Errorf("%s: spec.disks[%d].size must be set (e.g. 50GiB)", v.Ref(), i)
		} else if _, err := DiskBytes(d.Size); err != nil {
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
		// M13 drive options (probe-verified PVE 9.2.2, conformance-dev):
		// discard/aio are accepted on every bus; ssd is accepted ONLY on
		// scsi/sata/ide — PVE's virtio (and nvme) drive schema rejects the
		// token with "property is not defined in schema", so fail closed at
		// validation instead of submitting a create/update that 400s.
		switch strings.ToLower(d.Discard) {
		case "", "ignore", "on":
		default:
			return fmt.Errorf("%s: spec.disks[%d].discard %q must be 'ignore' or 'on' (PVE 9.2)", v.Ref(), i, d.Discard)
		}
		switch strings.ToLower(d.AIO) {
		case "", "native", "threads", "io_uring":
		default:
			return fmt.Errorf("%s: spec.disks[%d].aio %q must be 'native', 'threads' or 'io_uring' (PVE 9.2)", v.Ref(), i, d.AIO)
		}
		if d.SSD != nil && !diskBusSupportsSSD(slot) {
			return fmt.Errorf("%s: spec.disks[%d].ssd is not supported on slot %q (PVE 9.2 accepts ssd= only on scsi/sata/ide drives; virtio/nvme reject it)", v.Ref(), i, slot)
		}
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
		case "", "enabled", "disabled":
		default:
			return fmt.Errorf("%s: spec.hardware.efi-disk.secure-boot %q must be 'enabled' or 'disabled' (PVE 9.2 pre-enrolled-keys; see docs/GAPS.md M13)", v.Ref(), hw.EFIDisk.SecureBoot)
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
	// M13.2: PCI passthrough (hostpciN). Validate slot/BDF grammar +
	// deterministic ordering + duplicate slots. (Validates + normalises in
	// place so the wire render reads a stable list.)
	if err := ValidatePCIDevices(v.Ref().String(), hw.PCIDevices); err != nil {
		return err
	}
	// M13.2: Cloud-Init SOPS references (ssh-key-refs / ci-password-ref).
	if err := ValidateCloudInitRefs(v.Ref().String(), v.Spec.CloudInitData); err != nil {
		return err
	}
	// M11: cloud-init data — ssh-keys sentinel rule + static IPs must be
	// CIDRs, gateways plain IPs.
	{
		// A sentinel "*" is exclusive: either the slice is exactly {"*"}
		// (PVE owns the live sshkeys; proxops must not write) or the
		// slice has zero sentinels (proxops writes the manifest's keys).
		// Any mix is ambiguous → fail closed at parse time.
		sentinelCount := 0
		for _, k := range v.Spec.CloudInitData.SSHKeys {
			if k == CloudInitRedactedSentinel {
				sentinelCount++
			}
		}
		if sentinelCount > 0 && sentinelCount != len(v.Spec.CloudInitData.SSHKeys) {
			return fmt.Errorf("%s: spec.cloud-init-data.ssh-keys mixes the redacted sentinel %q with real keys; use ONLY the sentinel (PVE owns the value) or ONLY real keys (proxops writes them)", v.Ref(), CloudInitRedactedSentinel)
		}
	}
	for i, c := range v.Spec.CloudInitData.IPConfigs {
		if c.NIC < 0 {
			return fmt.Errorf("%s: spec.cloud-init-data.ipconfigs[%d].nic must be >= 0", v.Ref(), i)
		}
		if c.IP != "" {
			if _, ipNet, err := net.ParseCIDR(c.IP); err != nil {
				return fmt.Errorf("%s: spec.cloud-init-data.ipconfigs[%d].ip: %v", v.Ref(), i, err)
			} else if ipNet == nil {
				// unreachable but keep lint happy
				_ = i
			}
		}
		if c.IP == "" && c.Gateway != "" {
			return fmt.Errorf("%s: spec.cloud-init-data.ipconfigs[%d]: gateway requires ip", v.Ref(), i)
		}
		if c.Gateway != "" {
			if ip := net.ParseIP(c.Gateway); ip == nil {
				return fmt.Errorf("%s: spec.cloud-init-data.ipconfigs[%d].gateway %q is not an IP address", v.Ref(), i, c.Gateway)
			}
		}
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
		"nestedvirt", "hidden", "tags",
		// M11: top-level cloud-init data fields.
		"ciuser", "sshkeys", "nameserver", "searchdomain",
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
		if strings.HasPrefix(k, "ipconfig") {
			// ipconfig<N> is the dynamic cloud-init static-IP slot proxops
			// owns via spec.cloud-init-data.ipconfigs (M11).
			return fmt.Errorf("%s: spec.extra key %q conflicts with a structured field (use spec.cloud-init-data.ipconfigs); remove it", v.Ref(), k)
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
	// cdrom slot: proxops only owns the IDE cdrom slot when
	// spec.hardware.cdrom is declared. When declared and iso=<artifact>,
	// proxops emits the resolved PVE volid (or the ISO name when
	// ResolveArtifactRefs hasn't run yet — unit tests); when declared
	// and iso=none, proxops emits "none" (detach). When the manifest
	// has no cdrom block, proxops leaves the PVE slot alone.
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
		// M13 Secure Boot: the wire token is `pre-enrolled-keys=<0|1>`
		// (probe-verified PVE 9.2.2 — the /qemu/{id}/security endpoint does
		// not exist; 501). "" = not owned → token omitted, PVE decides.
		if tok, ok := secureBootWire(hw.EFIDisk.SecureBoot); ok {
			efiStr += ",pre-enrolled-keys=" + tok
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
	// M13.2: PCI passthrough hostpci<N>.
	for _, pd := range hw.PCIDevices {
		p[pd.Slot] = pciDeviceWire(pd)
	}
	// M11: top-level cloud-init DATA fields.
	//
	// proxops uses "empty = not owned" semantics: if a CloudInitData
	// value is empty, proxops will NOT send the PVE key AND will NOT
	// rewrite an existing PVE value (Drift below enforces the same).
	// `ssh-keys` containing only the sentinel `*` is owned-but-do-not-
	// write (adopt fills the sentinel for PVE-owned sshkeys so the
	// operator sees the shape without leaking key material).
	if v.Spec.CloudInitData.CIUser != "" {
		p["ciuser"] = v.Spec.CloudInitData.CIUser
	}
	if sshkeys := cloudInitSSHKeysWire(CloudInitSSHKeysEffective(v.Spec.CloudInitData)); sshkeys != "" {
		p["sshkeys"] = sshkeys
	}
	// M13.2: cloud-init root password via SOPS reference. ProxOps writes the
	// resolved password on CREATE so the guest has its declared root password;
	// the value exists only in memory for the lifetime of this call and never
	// lands in logs / status / diff.
	if pw := CloudInitCIPasswordEffective(v.Spec.CloudInitData); pw != "" {
		p["cipassword"] = pw
	}
	if len(v.Spec.CloudInitData.Nameservers) > 0 {
		p["nameserver"] = strings.Join(v.Spec.CloudInitData.Nameservers, " ")
	}
	if len(v.Spec.CloudInitData.SearchDomains) > 0 {
		p["searchdomain"] = strings.Join(v.Spec.CloudInitData.SearchDomains, " ")
	}
	if ips := cloudInitIPConfigWire(v.Spec.CloudInitData); ips != nil {
		for k, wireVal := range ips {
			p[k] = wireVal
		}
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
// When boot-order is not declared, proxops does not manage PVE's
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

// CloneInheritedKeys are the PVE /config keys a full clone copies from the
// template that carry IDENTITY or SECRET semantics — the cloud-init DATA
// block. M12 probe-verified on conformance-dev PVE 9.2.2: a full clone of a
// template reporting ciuser / sshkeys / ipconfig0 / nameserver / searchdomain
// reproduces every one of them verbatim on the clone's /config, so without
// intervention a clone boots with the TEMPLATE's cloud-init user, keys and
// static IP.
//
// proxops clears the ones the clone-backed VM's manifest does not own (see
// CloneDeleteKeys), so a clone never silently keeps the template's identity.
// ipconfig<N> slots are handled per-slot alongside this list.
//
// Deliberately NOT in the list:
//   - name / hostname: the clone's own name= parameter always wins (PVE does
//     not copy the template's name — probe-verified), and the guest hostname
//     derives from it via cloud-init.
//   - smbios1 / vmgenid: PVE regenerates these per clone (probe-verified).
//   - tags: PVE DOES copy the template's tags, but proxops always writes the
//     clone's own tags (ownership tag included) in the post-clone config
//     pass, so the inherited value is overwritten, never leaked.
//   - operational posture (onboot/startup/protection) + description + the
//     hardware layout: these follow the normal "empty = not owned" contract.
//     A clone inheriting the template's non-identity posture is the point of
//     cloning; any manifest-declared value rides the config write, and live
//     state the manifest does not describe is surfaced by Drift /
//     DriftAnomalies rather than silently erased. See docs/GAPS.md.
var CloneInheritedKeys = []string{
	"ciuser", "sshkeys", "cipassword", "cicustom",
	"nameserver", "searchdomain",
}

// IsCloneBacked reports whether the manifest declares spec.clone (M12).
func (v *VM) IsCloneBacked() bool { return strings.TrimSpace(v.Spec.Clone) != "" }

// CloneSourceID exposes the resolved clone-source template VMID (M12) to the
// planner. 0 when the VM is not clone-backed or resolution has not run.
// Resolution failure aborts the cycle before planning, so a clone-backed VM
// reaching the planner always has a non-zero source.
func (v *VM) CloneSourceID() int { return v.cloneSourceID }

// CloneConfigSet narrows a VM's create params to the post-clone config write
// (M12): vmid, start and the disk slots are removed. A clone allocates its
// disks from the template (re-stating a create-form disk over a cloned live
// volume is the data-loss shape ProxOps forbids), and PVE's clone endpoint
// rejects `start` outright (probe-verified: "property is not defined in
// schema"), so power is applied as a separate status/start after the config
// write.
//
// The cloud-init DRIVE (ide2) is NOT stripped here: whether it must be
// written depends on the clone's LIVE state (a clone of a template that
// carried a cloud-init drive inherits the volume and re-sending it fails the
// task with "lvcreate ... vm-<id>-cloudinit already exists" — probe-verified
// on conformance-dev 2026-09-15; a clone of a template WITHOUT a drive has an
// empty ide2 and writing it is safe). The executor makes that call after
// reading the clone's config (see exec.cloneCreate). The cloud-init DATA
// (ciuser / sshkeys / ipconfig<N> / nameserver / searchdomain) always rides
// the write — those are plain config keys, not volumes.
func CloneConfigSet(params map[string]any) map[string]any {
	out := make(map[string]any, len(params))
	for k, val := range params {
		if k == "vmid" || k == "start" || isDiskSlot(k) {
			continue
		}
		out[k] = val
	}
	return out
}

// CloneConfigWrite builds the post-clone config body from the VM's create
// params and the clone's LIVE config (read after the clone task settled):
// CloneConfigSet, minus the cloud-init drive when the clone already carries
// a live cloud-init volume (inherited from the template). Writing the drive
// into an empty slot (a template without one) is safe and keeps the first
// cycle convergent — the guest gets its cloud-init data AND drive before the
// power-on step.
func CloneConfigWrite(params map[string]any, live map[string]any) map[string]any {
	out := CloneConfigSet(params)
	if _, ok := out[CloudInitDriveSlot]; ok {
		if cur, _ := live[CloudInitDriveSlot].(string); !isNewStorageSlot(cur) {
			// Live cloud-init volume present — the clone inherited it; a
			// re-send would lvcreate over it (task failure, partial apply).
			delete(out, CloudInitDriveSlot)
		}
	}
	return out
}

// CloudInitDriveSlot is the PVE config key proxops writes the cloud-init
// drive to (always ide2; the cdrom shifts to ide3 when cloud-init is on).
const CloudInitDriveSlot = "ide2"

// ipconfigSlotCount is the number of ipconfig<N> slots a clone could have
// inherited from the template: the wider of the VM's NIC count and its
// declared ipconfig slots. PVE tolerates deleting an ABSENT key
// (probe-verified), so deriving the range from the manifest alone keeps the
// delete list deterministic and independent of what the template carried.
func (v *VM) ipconfigSlotCount() int {
	n := len(v.Spec.NICs)
	for _, c := range v.Spec.CloudInitData.IPConfigs {
		if c.NIC+1 > n {
			n = c.NIC + 1
		}
	}
	return n
}

// cloneClearKeys returns the inherited identity keys that are PRESENT in the
// clone's live config but NOT owned by the manifest (M12). Drift uses this so
// the "never keep the template's identity" guarantee is CONVERGENT, not
// create-time-only: if a clone-create fails after the clone landed (the exact
// shape of the lvcreate bug above), the next cycle clears the leak through the
// normal update path instead of leaving the template's ciuser / sshkeys /
// static IP live forever.
//
// Only PRESENT keys are returned — emitting delete= for absent keys every
// cycle would make Drift never converge. The result is disjoint from the keys
// the manifest owns (PVE 400s on set+delete overlap — probe-verified).
func (v *VM) cloneClearKeys(current map[string]any) []string {
	if current == nil {
		return nil
	}
	var out []string
	add := func(k string, owned bool) {
		if owned {
			return
		}
		if s := strings.TrimSpace(pveStr(current[k])); s != "" && !isNoneSlot(s) {
			out = append(out, k)
		}
	}
	add("ciuser", v.Spec.CloudInitData.CIUser != "")
	add("sshkeys", cloudInitSSHKeysWire(CloudInitSSHKeysEffective(v.Spec.CloudInitData)) != "")
	add("nameserver", len(v.Spec.CloudInitData.Nameservers) > 0)
	add("searchdomain", len(v.Spec.CloudInitData.SearchDomains) > 0)
	// M13.2: cipassword is now cloud-init SOPS-reference owned when the
	// manifest declares a ci-password-ref (the clone's own declared
	// password overrides the template's). Otherwise it remains template-
	// owned → cleared.
	add("cipassword", CloudInitCIPasswordEffective(v.Spec.CloudInitData) != "")
	add("cicustom", false)
	for i := 0; i < v.ipconfigSlotCount(); i++ {
		k := "ipconfig" + strconv.Itoa(i)
		owned := false
		for _, c := range v.Spec.CloudInitData.IPConfigs {
			if c.NIC == i && c.IP != "" {
				owned = true
			}
		}
		add(k, owned)
	}
	sort.Strings(out)
	return out
}

// CloneDeleteKeys returns the PVE keys to clear via the `delete=` form-value
// after cloning (M12): every inherited identity/secret/host-coupled key that
// the manifest does NOT own, plus every ipconfig<N> slot it does not own.
//
// PVE 9.2 constraint (probe-verified): one /config request cannot both set
// and delete the SAME key (400 "you can't use '-ciuser' and '-delete ciuser'
// at the same time"), so the result is disjoint from the set map by
// construction: a key the manifest owns rides the set, a key it does not own
// rides the delete list.
//
// Deleting an ABSENT key is tolerated by PVE (probe-verified), so the
// ipconfig range is derived from the manifest alone — deterministic and
// independent of what the template happened to carry.
func (v *VM) CloneDeleteKeys(params map[string]any) []string {
	owned := make(map[string]bool, len(params))
	for k := range params {
		owned[k] = true
	}
	var del []string
	for _, k := range CloneInheritedKeys {
		if !owned[k] {
			del = append(del, k)
		}
	}
	for i := 0; i < v.ipconfigSlotCount(); i++ {
		k := "ipconfig" + strconv.Itoa(i)
		if !owned[k] {
			del = append(del, k)
		}
	}
	sort.Strings(del)
	return del
}

// DriftAnomalies surfaces live-only disk and NIC slots that proxops's
// manifest does not declare but PVE reports on this VM. These are
// "manual drift" — a human added a drive/NIC in the PVE GUI that the
// manifest doesn't know about. proxops NEVER auto-deletes them:
//
//   - PVE's `scsiN=none` detach does not delete the LVM volume
//     (probe-verified on PVE 9.2) — the only reliable deletion is
//     `DELETE /qemu/{id}/storage/{pool}/{volid}`, which is a host-level
//     data-destroying action we must not take without explicit user intent.
//   - Deleting a NIC is less destructive but still user-visible.
//
// The planner records each anomaly as a `Skipped` action so the
// operator sees it on /status and /metrics without proxops changing
// PVE. Removing the live-only device is a separate, explicit operator
// step (e.g. `qm set` or `pvesm` in the PVE Web UI).
func (v *VM) DriftAnomalies(current map[string]any) []string {
	if current == nil {
		return nil
	}
	// Desired disk slots: every spec.disks entry + any slot the manifest
	// uses as a default (scsi<i> when slot is empty).
	wantDisks := map[string]bool{}
	for i, d := range v.Spec.Disks {
		slot := d.Slot
		if slot == "" {
			slot = fmt.Sprintf("scsi%d", i)
		}
		wantDisks[slot] = true
	}
	out := make([]string, 0, 4)
	// M12: a clone-backed VM declares NO spec.disks — the template's disk
	// layout is inherited by the clone and is EXPECTED to be live. Flagging
	// those slots as "live-only disks proxops will not remove" would be
	// noise on every converged clone, so the disk-slot anomaly class is
	// suppressed for clone-backed VMs. (NIC slots stay checked.)
	cloneBacked := v.IsCloneBacked()
	for k, raw := range current {
		switch {
		case isDiskSlot(k):
			if !cloneBacked && !wantDisks[k] && !isNoneSlot(pveStr(raw)) {
				out = append(out, fmt.Sprintf("live-only disk slot %s=%s is not in spec.disks; proxops will not automatically remove a live-only disk", k, pveStr(raw)))
			}
		case isNICSlot(k):
			want := map[string]bool{}
			for i, n := range v.Spec.NICs {
				slot := n.Slot
				if slot == "" {
					slot = fmt.Sprintf("net%d", i)
				}
				want[slot] = true
			}
			if !want[k] {
				out = append(out, fmt.Sprintf("live-only NIC slot %s=%s is not in spec.networks; proxops will not automatically remove it", k, pveStr(raw)))
			}
		case PvePCIKeyIsOwned(k):
			// M13.2: a live hostpciN slot that the manifest does not own is
			// a PVE-side passthrough. proxops NEVER strips a live PCI device
			// (pulling the token can drop a physical GPU off a running VM,
			// and PVE gates hostpci writes on "only root ... for
			// non-mapped devices"). Surface, don't touch.
			if raw != nil && pveStr(raw) != "" && !isNoneSlot(pveStr(raw)) {
				wanted := map[string]bool{}
				for _, pd := range v.Spec.Hardware.PCIDevices {
					wanted[pd.Slot] = true
				}
				if !wanted[k] {
					out = append(out, fmt.Sprintf("live-only PCI slot %s=%s is not in spec.hardware.pci-devices; proxops will not automatically remove a PVE-side hostpciN device", k, pveStr(raw)))
				}
			}
		}
	}
	// Disk pool/size drift on live data volumes is non-destructive too:
	// proxops will not auto-resize/re-pool. Surface alongside
	// live-only-slot anomalies. Clone-backed VMs have no desired disks, so
	// diskSlotDrift yields nothing — skip the call for clarity.
	if !cloneBacked {
		if _, _, danoms := v.diskSlotDrift(current); danoms != nil {
			out = append(out, danoms...)
		}
	} else if v.Spec.Hardware.CloudInit.Enabled {		// M12: a clone inherits the template's cloud-init DRIVE volume. When
		// the manifest asks for a different pool than the clone ended up with,
		// proxops will NOT move the volume (that is a storage migration, not a
		// config write) — surface it instead of silently failing the task.
		cur := parseDiskInfo(pveStr(current[CloudInitDriveSlot]))
		if cur.pool != "" && cur.pool != v.Spec.Hardware.CloudInit.Storage {
			out = append(out, fmt.Sprintf(
				"clone cloud-init drive lives on pool %q (inherited from template %q) but spec.hardware.cloud-init.storage is %q; proxops will not move a live cloud-init volume — align the manifest with the template or recreate the VM",
				cur.pool, strings.TrimSpace(v.Spec.Clone), v.Spec.Hardware.CloudInit.Storage))
		}
	}
	// M13: an EFI disk pool change on a LIVE efidisk0 volume is data-loss
	// territory (a create-form write recreates the volume), so Drift does
	// not write it — surface it instead.
	if hw := &v.Spec.Hardware; hw.EFIDisk != nil {
		curRaw := pveStr(current["efidisk0"])
		if !isNewStorageSlot(curRaw) {
			if cur := parseDiskInfo(curRaw); cur.pool != "" && cur.pool != hw.EFIDisk.Storage {
				out = append(out, fmt.Sprintf(
					"efidisk0 storage drift (live pool=%q; desired pool=%s); proxops will NOT move a live EFI vars volume (PVE /config would recreate it) — recreate the VM or migrate deliberately on PVE, then update the manifest",
					cur.pool, hw.EFIDisk.Storage))
			}
		}
	}
	// Sort for determinism.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// isDiskSlot matches PVE's QEMU data-disk slot naming: scsi*, virtio*, sata*.
// `ide0`/`ide1` are cloud-init + EFI-vars slots; `ide2`/`ide3` are CD/DVD
// slots and are matched by DriftAnomalies only via the CDDrive state
// machine (proxops models cdrom via `spec.hardware.cdrom`, never via a
// disk entry), so we exclude them here.
func isDiskSlot(k string) bool {
	for _, prefix := range []string{"scsi", "virtio", "sata"} {
		if strings.HasPrefix(k, prefix) {
			rest := k[len(prefix):]
			if rest == "" {
				return false
			}
			for _, c := range rest {
				if c < '0' || c > '9' {
					return false
				}
			}
			return true
		}
	}
	return false
}

// isNICSlot matches PVE's QEMU NIC slot naming: net*.
func isNICSlot(k string) bool {
	if !strings.HasPrefix(k, "net") {
		return false
	}
	rest := k[len("net"):]
	if rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isNoneSlot reports whether PVE reported the slot in its "detached / none"
// form. PVE reports `scsiN = none,media=cdrom` for a slot it detached via
// `scsiN=none`; it reports the bare `scsiN = none` when it cleared via the
// UI "delete" button. Both are treated as no-live-device, not an anomaly.
func isNoneSlot(s string) bool {
	s = strings.TrimSpace(s)
	return s == "none" || strings.HasPrefix(s, "none,") || strings.HasPrefix(s, "none;")
}

// isNewStorageSlot reports whether PVE has NOT allocated any storage at
// the given slot (report value absent/empty, or PVE's "none" form). Only
// such slots are safe to write a create-form drive to; a live volume at
// the slot must NOT be auto-rewritten (data loss).
func isNewStorageSlot(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return trimmed == "" || isNoneSlot(trimmed)
}

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

// CdromWireValue returns the full PVE cdrom wire value proxops will put
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
	// disks. PVE's /config report is "<pool>:vm-<vmid>-disk-<n>[,iothread=1],
	// size=<binary>" (PVE-assigned volume name, binary-suffix size).
	//
	// SAFETY (data-loss guard, PVE 9.2 probed 2026-09-08): PVE's
	// /qemu/{id}/config accepts a drive slot in two forms. The CREATE form
	// ("scsi0=local-lvm:8") allocates a fresh volume; on an EXISTING data
	// disk, sending the create form RECREATES the volume (the old one is
	// deleted → data loss). The only safe in-place rewrite of an existing
	// slot is the LIVE form ("scsi0=local-lvm:vm-9100-disk-0,size=8G"), which
	// preserves PVE's volume id. This class therefore:
	//   - NEW slot (no live volume)          -> safe CREATE-form write
	//   - pool/size/storage CHANGED on a live slot -> NON-destructive anomaly
	//     (proxops will NOT auto-resize/re-pool a data-bearing disk; the
	//     operator must do it deliberately, e.g. `qm set` + `qmresize`)
	//   - iothread toggled, pool+size same   -> safe LIVE-form write (volume
	//     id preserved)
	// Disk slots (see diskSlotDrift / SAFETY note): apply only the
	// non-destructive updates; pool/size drift is NOT written here.
	du, dstop, _ := v.diskSlotDrift(current)
	for k, val := range du {
		upd[k] = val
	}
	if dstop {
		stop = true
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
	//
	// M12: the cloud-init DRIVE is a storage-backed volume, so a clone
	// INHERITS it from the template (the clone's own vmid names the volume).
	// Re-sending the create form over an inherited volume makes PVE lvcreate a
	// volume that already exists and the task FAILS (probe-verified on
	// conformance-dev 2026-09-15). For a clone-backed VM the drive is therefore
	// written only into an EMPTY slot (a template that carried no drive); a
	// live drive on a different pool is surfaced by DriftAnomalies instead of
	// rewritten. The cloud-init DATA below is always reconciled — those are
	// plain config keys, not volumes.
	if hw.CloudInit.Enabled {
		slot := CloudInitDriveSlot
		cloudInitWant := fmt.Sprintf("%s:cloudinit,size=%s", hw.CloudInit.Storage, hw.CloudInit.Size)
		curDrive := pveStr(current[slot])
		if !cloudInitMatches(curDrive, cloudInitWant) {
			if !v.IsCloneBacked() || isNewStorageSlot(curDrive) {
				upd[slot] = cloudInitWant
				stop = true
			}
		}
	}
	// EFI disk: PVE owns the efidisk0 volume name; we own pool + efitype +
	// the M13 Secure Boot token (pre-enrolled-keys). PVE auto-adds an
	// `ms-cert=` token to the report when keys are pre-enrolled at create
	// time — proxops treats ms-cert as PVE-owned: preserved verbatim on
	// live-form rewrites, never compared (probe-verified PVE 9.2.2: a
	// live-form write WITHOUT ms-cert drops it from the report, so the
	// rewrite must carry it through rawExtra).
	//
	// Data-loss guard (same as data disks): a create-form write
	// ("<pool>:<GiB>") over a LIVE efidisk0 recreates the volume; only an
	// absent/empty slot gets a create-form write. A pool change on a live
	// EFI volume is surfaced as an anomaly by DriftAnomalies, not written.
	// The secure-boot/efitype toggle converges in place on stopped AND
	// running VMs via the live form (probe-verified: enrollment takes
	// effect at the guest's next boot).
	if hw.EFIDisk != nil {
		curRaw := pveStr(current["efidisk0"])
		cur := parseDiskInfo(curRaw)
		wantKeys, keysOwned := secureBootWire(hw.EFIDisk.SecureBoot)
		wantEFIT := strings.ToLower(hw.EFIDisk.Template)
		if isNewStorageSlot(curRaw) {
			// No live EFI volume: the create-form write is safe.
			efi := fmt.Sprintf("%s:%s", hw.EFIDisk.Storage, GiBString(diskBytes(hw.EFIDisk.Size)))
			if wantEFIT != "" {
				efi += ",efitype=" + wantEFIT
			}
			if keysOwned {
				efi += ",pre-enrolled-keys=" + wantKeys
			}
			upd["efidisk0"] = efi
			stop = true
		} else if cur.pool != "" && cur.pool == hw.EFIDisk.Storage {
			// Live EFI volume on the desired pool: option drift only.
			efitypeDrift := wantEFIT != "" && cur.efitype != "" && cur.efitype != wantEFIT
			keysDrift := false
			if keysOwned {
				keysDrift = (cur.preEnrolled != nil && *cur.preEnrolled) != (wantKeys == "1")
			}
			if efitypeDrift || keysDrift {
				s := cur.pool + ":" + cur.volumeName
				if cur.sizeSet {
					s += ",size=" + cur.sizeToken
				}
				switch {
				case cur.efitype != "":
					s += ",efitype=" + cur.efitype
				case wantEFIT != "":
					s += ",efitype=" + wantEFIT
				}
				switch {
				case keysOwned:
					s += ",pre-enrolled-keys=" + wantKeys
				case cur.preEnrolled != nil:
					if *cur.preEnrolled {
						s += ",pre-enrolled-keys=1"
					} else {
						s += ",pre-enrolled-keys=0"
					}
				}
				if cur.rawExtra != "" {
					s += "," + cur.rawExtra
				}
				upd["efidisk0"] = s
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
	// M13.2: PCI passthrough hostpci<N>. PVE replaces the whole hostpciN
	// value on a config write, so compare the owned token set (device +
	// pcie when owned) against the live report. A live value with only
	// PVE-side tokens proxops does not model (x-vga/rombar/mdev) reads as
	// drift once the slot is in the manifest — the write owns the slot
	// end-to-end. Device topology changes require a stop.
	for _, pd := range hw.PCIDevices {
		cur := pveStr(current[pd.Slot])
		if !hostpciTokensEqual(cur, pd) {
			upd[pd.Slot] = pciDeviceWire(pd)
			stop = true
		}
	}

	// M11: top-level cloud-init DATA fields.
	//
	// Ownership rule: proxops owns a PVE key only when the desired
	// value is non-empty. Empty desired → proxops does not write AND
	// does NOT surface drift against a live PVE value (live may hold a
	// PVE-side value that proxops has no way of knowing was set by
	// qm/cloud-init; treating it as drift would flap or clobber it).
	//
	// sshkeys: the sentinel `*` means "PVE owns this; do not write". A
	// live value of any shape coexists with the sentinel without drift.
	// Empty desired means "proxops does not model this" too (same as
	// sentinel, no write, no anomaly).
	//
	// Nameservers / search-domains / ipconfig: set-based comparison when
	// PVE's CSV is space-separated; per-<N> when PVE's shape is
	// "ip=<cidr>[,gw=<ip>]". PVE may store the static-IP CSV with a
	// trailing "," or whitespace; we normalise before comparing.
	if v.Spec.CloudInitData.CIUser != "" {
		if pveStr(current["ciuser"]) != v.Spec.CloudInitData.CIUser {
			upd["ciuser"] = v.Spec.CloudInitData.CIUser
		}
	}
	if sshkeys := cloudInitSSHKeysWire(CloudInitSSHKeysEffective(v.Spec.CloudInitData)); sshkeys != "" {
		if !sshKeysWireMatch(pveStr(current["sshkeys"]), sshkeys) {
			upd["sshkeys"] = sshkeys
		}
	}
	// M13.2: PVE's cipassword drift rule. When ProxOps OWNS a cloud-init
	// password (spec.cloud-init-data.ci-password-ref declared + SOPS-resolved),
	// PVE 9.2 reports that value on /config as a fixed '**********' mask
	// (probe-verified M13.2 — the plaintext is unrecoverable and not
	// comparable). The convergence rule:
	//   - if the live config HAS a non-empty cipassword key, PVE has stored
	//     SOME password; ProxOps considers the manifest's declared password
	//     satisfied (there is no way to verify it matches). No write, no drift.
	//   - if the live config has NO cipassword key, PVE is missing one; the
	//     manifest's declared password writes on update (safe: PVE sets it
	//     once and then the next cycle satisfies the "has cipassword" rule).
	// The manifest NEVER deletes a live cipassword (doing so can lock the
	// operator out of a running guest).
	if pw := CloudInitCIPasswordEffective(v.Spec.CloudInitData); pw != "" {
		if pveStr(current["cipassword"]) == "" {
			upd["cipassword"] = pw
		}
	}
	if wantNS := strings.Join(v.Spec.CloudInitData.Nameservers, " "); wantNS != "" {
		if !csvSetsEqual(pveStr(current["nameserver"]), wantNS) {
			upd["nameserver"] = wantNS
		}
	}
	if wantSD := strings.Join(v.Spec.CloudInitData.SearchDomains, " "); wantSD != "" {
		if !csvSetsEqual(pveStr(current["searchdomain"]), wantSD) {
			upd["searchdomain"] = wantSD
		}
	}
	for k, wantIPCfg := range cloudInitIPConfigWire(v.Spec.CloudInitData) {
		// Compare as PVE reports: "ip=<cidr>[,gw=<ip>]" verbatim. PVE may
		// reorder or add whitespace; treat as set-of-tokens comparison.
		// A live value that is a different static-IP set is drift.
		if pveStr(current[k]) != wantIPCfg {
			upd[k] = wantIPCfg
		}
	}

	// cdrom: PVE stores the CD/DVD at the ide slot (PVE 9.2: always ide2,
	// regardless of cloud-init placement — verified via the `cdrom=<vol>,
	// media=cdrom` form alias on i440fx AND q35 machines, both with and
	// without a cloud-init drive). PVE reports the cdrom with a PVE-assigned
	// `media=` option + a size token. proxops owns pool + filename +
	// media when spec.hardware.cdrom is declared; when the manifest has no
	// cdrom block, proxops does NOT own the PVE slot.
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
	// `media=` option + `size=` token. proxops owns the slot when
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

	// M12: a clone-backed VM must never keep the template's cloud-init
	// identity. The clear rides the NORMAL update path so the guarantee is
	// convergent: a clone-create that failed after the clone landed (the
	// lvcreate shape above) leaves a half-configured clone that the next
	// cycle repairs, instead of a permanent template-identity leak.
	if v.IsCloneBacked() {
		if clear := v.cloneClearKeys(current); len(clear) > 0 {
			upd["delete"] = strings.Join(clear, ",")
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
	if d.Image != "" {
		// PVE 9 import-from create form: the size token MUST be 0 and the
		// source is the DiskImage's volid (probed on conformance-dev
		// 2026-09-13: any other size → 400 "'import-from' requires special
		// syntax - use <storage ID>:0,import-from=<source>").
		volid := d.imageVolid
		if volid == "" {
			volid = d.Image
		}
		s = d.Storage + ":0,import-from=" + volid
	}
	if d.IOThread {
		s += ",iothread=1"
	}
	s += driveOptionTokens(d)
	return s
}

// driveOptionTokens renders the M13 drive options (discard/ssd/aio) in
// PVE's inline grammar, in a stable order. Empty/nil = not owned → omitted.
func driveOptionTokens(d Disk) string {
	var b strings.Builder
	if d.Discard != "" {
		b.WriteString(",discard=" + strings.ToLower(d.Discard))
	}
	if d.SSD != nil {
		if *d.SSD {
			b.WriteString(",ssd=1")
		} else {
			b.WriteString(",ssd=0")
		}
	}
	if d.AIO != "" {
		b.WriteString(",aio=" + strings.ToLower(d.AIO))
	}
	return b.String()
}

// diskBusSupportsSSD reports whether PVE 9.2's drive schema accepts the
// `ssd=` token on a slot's bus. Probe-verified on conformance-dev: scsi/sata/
// ide accept it; virtio/nvme reject it ("property is not defined in schema").
func diskBusSupportsSSD(slot string) bool {
	return strings.HasPrefix(slot, "scsi") || strings.HasPrefix(slot, "sata") ||
		strings.HasPrefix(slot, "ide")
}

// secureBootWire maps the declarative secure-boot value to PVE's
// `pre-enrolled-keys` token ("1"/"0"); ok=false when the field is not owned.
func secureBootWire(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "enabled":
		return "1", true
	case "disabled":
		return "0", true
	default:
		return "", false
	}
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
// Fields we own and compare: pool (volume id), size (bytes), iothread, and
// the M13 drive options (discard/ssd/aio).
type pveDiskInfo struct {
	pool       string
	volumeName string
	sizeBytes  int64
	sizeToken  string // PVE's raw size spelling ("8G", "0.5", "8589934592"),
	//                      preserved for safe in-place rewrites
	sizeSet  bool
	iothread bool
	// M13 drive options. Empty/nil = token absent from the string.
	discard string
	ssd     *bool
	aio     string
	// M13 EFI tokens (efidisk0 grammar shares the drive parser).
	efitype     string
	preEnrolled *bool
	// rawExtra carries every non-owned option token (PVE-assigned volume
	// names aside) verbatim, comma-joined, so a live-form rewrite can
	// preserve PVE-owned tokens (e.g. efidisk0's ms-cert) untouched.
	rawExtra string
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
						out.sizeBytes, out.sizeSet, out.sizeToken = b, true, v
					} else {
						out.rawExtra = addRawToken(out.rawExtra, t)
					}
				case "iothread":
					out.iothread = v == "1" || v == "on" || v == "true"
				case "discard":
					out.discard = strings.ToLower(v)
				case "ssd":
					b := v == "1" || v == "on" || v == "true"
					out.ssd = &b
				case "aio":
					out.aio = strings.ToLower(v)
				case "efitype":
					out.efitype = strings.ToLower(v)
				case "pre-enrolled-keys":
					b := v == "1" || v == "on" || v == "true"
					out.preEnrolled = &b
				default:
					out.rawExtra = addRawToken(out.rawExtra, t)
				}
				continue
			}
			if i == 0 && !strings.Contains(t, "=") {
				// Bare first token: "local-lvm:8" create form (volume-size
				// number in GiB) or "local-lvm:vm-...-disk-0" report form.
				if f, err := strconv.ParseFloat(t, 64); err == nil && f > 0 {
					out.sizeBytes = int64(f * float64(int64(1)<<30))
					out.sizeSet = true
					out.sizeToken = t
					continue
				}
				// Treat as the PVE-assigned volume name → not owned. Captured
				// so a safe in-place iothread toggle can preserve the live
				// volume id (PVE 9.2 rejects the size/pool create-form on an
				// existing data disk and recreates the volume → data loss).
				out.volumeName = t
				continue
			}
			// Bare flag token we do not own → preserve verbatim.
			out.rawExtra = addRawToken(out.rawExtra, t)
		}
	}
	return out
}

// addRawToken appends a PVE-owned option token to the rawExtra accumulator,
// keeping PVE's verbatim spelling and stable order.
func addRawToken(cur, tok string) string {
	if cur == "" {
		return tok
	}
	return cur + "," + tok
}

// diskSlotDrift classifies every spec disk against PVE's live report and
// returns ONLY the safe updates: (a) new slots that PVE has not allocated yet
// (safe to create) and (b) iothread toggles on an existing volume (safe
// in-place rewrite that preserves PVE's live volume id). Disk pool/size
// storage changes on a live data volume are returned as anomalies instead
// of updates, because PVE 9.2 /config RE-CREATES the volume on a pool/size
// write (data loss). Returns (updates, stopRequired, anomalies).
func (v *VM) diskSlotDrift(current map[string]any) (map[string]any, bool, []string) {
	upd := map[string]any{}
	stop := false
	anoms := make([]string, 0, 2)
	for i, d := range v.Spec.Disks {
		slot := d.Slot
		if slot == "" {
			slot = fmt.Sprintf("scsi%d", i)
		}
		curRaw := pveStr(current[slot])
		cur := parseDiskInfo(curRaw)
		want := parseDiskInfo(driveVolumeString(d))
		// "New slot" == PVE has NOT allocated any storage at this
		// slot (report value absent/empty, or PVE's bare "none" form).
		// ONLY then is a create-form write safe. A non-empty PVE
		// value is a live allocation: an auto rewrite would
		// recreate the volume and DESTROY its data (PVE 9.2 has
		// no safe /resize endpoint), so pool/size/storage
		// mismatch is reported as a non-destructive anomaly
		// below instead of written.
		if isNewStorageSlot(curRaw) {
			// Empty slot: PVE allocated nothing here, so the create-form
			// write is safe. Only this case auto-writes.
			upd[slot] = driveVolumeString(d)
			stop = true
			continue
		}
		// Live allocation present at the slot: a pool/size/storage rewrite
		// would recreate the volume and destroy its data, so proxops
		// reports it (poolChanged/sizeChanged below) instead of writing.
		poolChanged := cur.pool != want.pool
		sizeChanged := want.sizeSet && (cur.sizeSet && cur.sizeBytes != want.sizeBytes)
		if poolChanged || sizeChanged {
			desiredSize := d.Size
			if d.Image != "" {
				desiredSize = "from image " + d.Image
			}
			anoms = append(anoms, fmt.Sprintf(
				"disk %s storage/size drift (live=%q; desired pool=%s size=%s); proxops will NOT auto-resize or re-pool a live data disk (PVE /config would recreate the volume and lose its data) — resize deliberately on PVE (qm set/qmresize) or via a new disk, then update the manifest",
				slot, curRaw, want.pool, desiredSize))
			continue
		}
		if driveOptionsDrift(cur, want) {
			// Preserve PVE's live volume id + exact size spelling + every
			// option token the manifest does not own, so PVE treats this as
			// an in-place option toggle, not a recreation.
			upd[slot] = liveDriveRewrite(cur, want)
			stop = true
		}
	}
	return upd, stop, anoms
}

// driveOptionsDrift reports whether any owned drive option (iothread + the
// M13 discard/ssd/aio tokens) differs between PVE's live report and our
// desired state. Tokens the manifest does not own (empty/nil desired) never
// drift. PVE omits an option that equals its default, so a live token
// absence is compared against the documented default: iothread=off,
// discard=ignore, ssd=off. aio has a kernel-dependent PVE default
// (io_uring when supported, else native), so a live absence with a pinned
// desired is treated as drift and materialised once — PVE retains the
// explicit token in the report afterwards (probe-verified PVE 9.2.2 for
// aio=threads; aio=native retention pinned by TestM13_AN_DriveOptions...).
func driveOptionsDrift(cur, want pveDiskInfo) bool {
	if cur.iothread != want.iothread {
		return true
	}
	if want.discard != "" {
		curD := cur.discard
		if curD == "" {
			curD = "ignore"
		}
		if !strings.EqualFold(curD, want.discard) {
			return true
		}
	}
	if want.ssd != nil {
		curSSD := false
		if cur.ssd != nil {
			curSSD = *cur.ssd
		}
		if curSSD != *want.ssd {
			return true
		}
	}
	if want.aio != "" {
		if cur.aio == "" || !strings.EqualFold(cur.aio, want.aio) {
			return true
		}
	}
	return false
}

// liveDriveRewrite renders the safe LIVE form for a drive slot: PVE's own
// volume id + size spelling, plus every option token — owned tokens take the
// desired value, non-owned tokens are preserved verbatim from the live
// string (including PVE-owned extras like backup=0 or ms-cert=).
func liveDriveRewrite(cur, want pveDiskInfo) string {
	s := cur.pool + ":" + cur.volumeName
	if cur.sizeSet {
		s += ",size=" + cur.sizeToken
	}
	if want.iothread {
		s += ",iothread=1"
	}
	discard := cur.discard
	if want.discard != "" {
		discard = want.discard
	}
	if discard != "" {
		s += ",discard=" + discard
	}
	ssd := cur.ssd
	if want.ssd != nil {
		ssd = want.ssd
	}
	if ssd != nil {
		if *ssd {
			s += ",ssd=1"
		} else {
			s += ",ssd=0"
		}
	}
	aio := cur.aio
	if want.aio != "" {
		aio = want.aio
	}
	if aio != "" {
		s += ",aio=" + aio
	}
	if cur.rawExtra != "" {
		s += "," + cur.rawExtra
	}
	return s
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
// PVE wire: "virtio,bridge=vmbr0,tag=100,rate=500,firewall=1".
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
		// PVE's QEMU NIC wire key is "tag" (PVE 9.2 verified: "vlan" is
		// rejected with "property is not defined in schema").
		fmt.Fprintf(&sb, ",tag=%d", n.VLAN)
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
			case "tag":
				out.vlan, _ = strconv.Atoi(v)
			case "rate":
				out.rate, _ = strconv.Atoi(v)
			case "vlan":
				// PVE 8-era alias, kept for parse-tolerance. The writer
				// only ever emits "tag"; "vlan" in the current field map
				// means PVE stored a VLAN tag under that key.
				out.vlan, _ = strconv.Atoi(v)
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

// cloudInitSSHKeysWire renders PVE's `sshkeys` wire value from the
// declarative SSHKeys slice. Empty → "" (proxops does not own the PVE key).
// A single `*` sentinel entry → "" (PVE owns the live value; do not write).
//
// PVE 9.2 wire grammar (probed on conformance-dev 2026-09-13): the sshkeys
// FIELD VALUE must itself be percent-encoded, with one public key per line.
// PVE's API schema declares sshkeys as a urlencoded string and decodes it
// before writing the config, so a raw `ssh-ed25519 AAAA... user@host` value is
// REJECTED at create/update with:
//
//	"invalid format - invalid urlencoded string: ssh-ed25519 AAAA...\n"
//
// and PVE's /config report returns the encoded form verbatim
// (`ssh-ed25519%20AAAA...%20user%40host`). Keys are joined with \n (encoded
// %0A), NOT commas — a comma is not a key separator in PVE's grammar.
//
// The sentinel is exclusive: Validate() rejects mixed sentinel+real keys, so
// this helper assumes the input already passed validation. Mixed input
// (if ever reached) returns "" — no write, no flap.
// cloudInitSSHKeysWire renders PVE's `sshkeys` wire value from a list of
// public key lines. The `*` redacted sentinel (any entry) returns ""
// (PVE owns the live sshkeys — never rewrite).
//
// M13.2: callers pass CloudInitSSHKeysEffective(v.Spec.CloudInitData),
// which is the EFFECTIVE key list — SOPS-resolved keys when
// `ssh-key-refs` are present, else the manifest's plaintext `ssh-keys`
// (M11 legacy shape), else nil.
//
// PVE 9.2 wire grammar (probed on conformance-dev PVE 9.2.2, M13.2):
// the sshkeys FIELD VALUE must itself be percent-encoded — PVE's API schema
// declares sshkeys as a urlencoded string and decodes it before writing the
// config, so a raw `ssh-ed25519 AAAA...` value is REJECTED with "invalid
// urlencoded string". Keys are joined with \n (encoded %0A).
func cloudInitSSHKeysWire(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	sentinelSeen := false
	for _, k := range keys {
		if k == CloudInitRedactedSentinel {
			sentinelSeen = true
		}
	}
	if sentinelSeen {
		return ""
	}
	out := make([]string, 0, len(keys))
	seen := map[string]bool{}
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return pveURLEncode(strings.Join(out, "\n"))
}

// pveURLEncode percent-encodes every byte outside RFC 3986's unreserved set
// (A-Z a-z 0-9 - . _ ~), using uppercase hex — the encoding PVE's own
// `sshkeys` reader expects and the shape PVE reports back. Go's url.QueryEscape
// is NOT usable here: it renders spaces as `+`, which PVE's decoder does not
// map back to a space inside this field.
func pveURLEncode(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) * 3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0xF])
	}
	return b.String()
}

// sshKeysWireMatch reports whether PVE's reported `sshkeys` value and our
// desired wire value carry the same SET of public keys. Both sides are decoded
// first (PVE reports the percent-encoded form verbatim; older PVE releases and
// hand-edited configs may differ only in encoding or key order), so a
// re-encode never reads as drift. Comparison is order-insensitive and
// duplicate-collapsing, matching cloud-init's own authorized_keys semantics.
func sshKeysWireMatch(cur, want string) bool {
	c := sshKeySet(cur)
	w := sshKeySet(want)
	if len(c) != len(w) {
		return false
	}
	for k := range w {
		if !c[k] {
			return false
		}
	}
	return true
}

// sshKeySet decodes a PVE sshkeys value into a set of individual keys.
func sshKeySet(v string) map[string]bool {
	out := map[string]bool{}
	if s, err := url.QueryUnescape(strings.ReplaceAll(v, "+", "%2B")); err == nil {
		v = s
	}
	for _, line := range strings.Split(v, "\n") {
		if k := strings.TrimSpace(line); k != "" {
			out[k] = true
		}
	}
	return out
}

// cloudInitIPConfigWire returns PVE "ipconfig<N>" form-values from the
// declarative IPConfigs. Only slots with a non-empty CIDR are owned.
//
// PVE static-IP form: "ip=<cidr>[,gw=<addr>]".
// PVE also accepts the "dhcp" token: proxops does NOT model that form
// in M11 (documented — operators use spec.extra if they want dhcp
// semantics per-NIC).
func cloudInitIPConfigWire(cd CloudInitData) map[string]string {
	if len(cd.IPConfigs) == 0 {
		return nil
	}
	out := map[string]string{}
	for _, ipc := range cd.IPConfigs {
		if ipc.IP == "" {
			continue // empty slot → not owned on the wire
		}
		slot := "ipconfig" + strconv.Itoa(ipc.NIC)
		wire := "ip=" + ipc.IP
		if ipc.Gateway != "" {
			wire += ",gw=" + ipc.Gateway
		}
		out[slot] = wire
	}
	return out
}

// csvSetsEqual reports whether two PVE CSV strings (whitespace-separated)
// carry the same set of tokens. PVE's cloud-init `nameserver` and
// `searchdomain` fields are space-separated on the wire; order and
// duplicates are not semantics — only membership is.
func csvSetsEqual(cur, want string) bool {
	c := csvTokens(cur)
	w := csvTokens(want)
	if len(c) != len(w) {
		return false
	}
	seen := map[string]bool{}
	for _, t := range c {
		seen[t] = true
	}
	for _, t := range w {
		if !seen[t] {
			return false
		}
	}
	return true
}

// csvTokens splits a PVE CSV on whitespace, dropping empty entries.
// PVE allows comma-AND-space-separated on some fields; M11's declarative
// form normalises to space-only.
func csvTokens(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, tok := range strings.Fields(s) {
		tok = strings.TrimSpace(tok)
		if tok != "" {
			out = append(out, tok)
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
	// A live report that omits its size token (PVE created the drive without
	// one — e.g. `ide2=local-lvm:cloudinit` reports
	// "local-lvm:vm-N-cloudinit,media=cdrom" with NO size) is compatible:
	// same rule as diskMatches — PVE is not telling us a number to compare
	// against, and re-writing the drive to "fix" a size PVE never reported
	// is a pointless stop-start cycle. (conformance-dev probe 2026-09-13.)
	if cs == "" {
		return true
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
