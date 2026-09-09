package schema

// This file exposes a small, adopt-only surface for turning PVE wire
// representations (as returned by GET /qemu/{id}/config and
// GET /lxc/{id}/config) back into pveconform schema fields.
//
// The helpers are intentionally narrow: they only cover the fields that
// pveconform actually models. Anything PVE reports but pveconform does not
// understand is out of scope and surfaces on docs/GAPS.md instead.
//
// Naming convention: <Kind>Pve<Thing>. Each function takes a PVE raw value
// and returns (pveconform-friendly form, ok). ok=false means the value
// could not be mapped to a pveconform field.

import (
	"sort"
	"strconv"
	"strings"
)

// --- VM ---

// PveMemoryToGiB converts PVE's reported memory (integer MiB, as a string or
// int) to a pveconform "human GiB" string ("4GiB", "0.5GiB"). PVE reports
// memory MiB values in /config (integer, not bytes — see MemoryMiB).
//
// PVE 9.2 reports memory in MiB on /config. The input may arrive as int
// (JSON number) or string.
func PveMemoryToHuman(v any) (string, bool) {
	b, ok := pveAnyBytesFromMiB(v)
	if !ok {
		return "", false
	}
	return HumanFromBytes(b)
}

// PveDisksFromPVE parses PVE's VM /config report into pveconform disks.
//
// PVE reports data disks in the form "local-lvm:vm-9101-disk-0,iothread=1,
// size=8G" (pool:volume[,iothread=1],size=<binary>). This helper extracts
// the owned fields: pool (spec.disk.storage), size (spec.disk.size in
// human GiB), iothread (spec.disk.iothread). The PVE-assigned volume name
// and the binary size token are PVE-managed and NOT part of the manifest.
//
// Slot is the scsi/virtio/sata device name PVE reports ("scsi0", "virtio1").
func PveDisksFromPVE(current map[string]any) ([]Disk, bool) {
	var out []Disk
	for k, v := range current {
		if !isDiskSlot(k) {
			continue
		}
		raw := pveStr(v)
		if isNewStorageSlot(raw) {
			continue // detached/none slot; pveconform does not model it
		}
		info := parseDiskInfo(raw)
		if info.pool == "" {
			continue
		}
		d := Disk{
			Slot:       k,
			Storage:    info.pool,
			IOThread:   info.iothread,
		}
		if info.sizeSet {
			if s, ok := HumanFromBytes(info.sizeBytes); ok {
				d.Size = s
			} else {
				// Not representable in pveconform quantity grammar.
				d.Size = ""
			}
		}
		// Controller: PVE reports the VM-wide "scsihw" on a sibling key,
		// not per-disk. We attach it to slot-0 so ToCreateParams picks it up
		// at the expected position (drives the VM-wide scsihw).
		if k == "scsi0" {
			if hw, ok := current["scsihw"].(string); ok && hw != "" {
				d.Controller = hw
			}
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return slotSortKey(out[i].Slot) < slotSortKey(out[j].Slot) })
	return out, len(out) > 0
}

// PveNICsFromPVE parses PVE's VM /config report into pveconform NICs.
//
// PVE reports NICs as "virtio=52:54:00:E6:5D:C7,bridge=vmbr0[,firewall=0]".
// The owned fields are: model, bridge, MAC (only when pinned), vlan, rate,
// firewall. PVE assigns the MAC; a manifest that does not pin MAC should
// omit it in round-trip to avoid owning PVE's random value.
//
// adopt treats PVE's MAC as "observed, not owned" and only includes it when
// the live MAC does not look like a PVE-assigned "mac:..." token (a pinned
// MAC). PVE-assigned MACs all start with "BC:" (mock) or are generated;
// to stay conservative, adopt does NOT pin a MAC on generated YAML —
// operators can pin one if they want. This keeps PVE's random-MAC drift
// from producing a churn loop.
func PveNICsFromPVE(current map[string]any) ([]NIC, bool) {
	var out []NIC
	for k, v := range current {
		if !isNICSlot(k) {
			continue
		}
		f := parseNICFields(pveStr(v))
		if f.model == "" {
			continue
		}
		n := NIC{
			Slot:     k,
			Model:    f.model,
			Bridge:   f.bridge,
			VLAN:     f.vlan,
			RateLimit: f.rate,
			Firewall: f.firewall,
		}
		// adopt does not pin PVE's auto-assigned MAC; leave MAC empty.
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return slotSortKey(out[i].Slot) < slotSortKey(out[j].Slot) })
	return out, len(out) > 0
}

// PveCDROMFromPVE examines the VM's cdrom IDE slot (ide2/ide3 depending on
// cloud-init) and returns (isoVolid, ok) — "" when no ISO is attached. The
// caller then maps isoVolid to the ISO resource name via the index.
func PveCDROMFromPVE(current map[string]any) (string, bool) {
	for _, slot := range []string{"ide3", "ide2"} {
		v, present := current[slot]
		if !present {
			continue
		}
		raw := pveStr(v)
		if isNewStorageSlot(raw) {
			return "", true // explicit detach; represent as "none"
		}
		c, ok := parseCdrom(raw)
		if !ok {
			continue
		}
		// Return the pve volid (pool:iso/filename); the caller resolves it
		// to a pveconform ISO metadata.name.
		return c.pool + ":iso/" + c.filename, true
	}
	return "", false
}

// PveCpuType returns PVE's VM `cpu` string. PVE reports this verbatim.
func PveCpuType(current map[string]any) string {
	return pveStr(current["cpu"])
}

// PveCpuCores returns PVE's VM `cores` int (JSON number tolerated as
// string).
func PveCpuCores(current map[string]any) int { return pveInt(current["cores"]) }

// PveVMOptionsFromPVE extracts the owned VM options PVE reports. Each
// entry is only included when PVE actually set it.
//
// Returns:
//
//	- onboot (bool, ok)
//	- protection (bool, ok)
//	- agent (bool, ok)
//	- acpi (bool, ok)
//	- tablet (bool, ok)
//	- nestedvirt (bool, ok)
//	- hidden (bool, ok)
//	- startup (string, ok)
//	- hotplug ([]string, ok)
//	- boot-order ([]string, ok) — parsed from PVE's "order=scsi0;ide2;net0"
//	- numa (bool, ok)
//	- sockets (int, ok)
func PveVMOptionsFromPVE(current map[string]any) VMOpts {
	o := VMOpts{}
	if b, ok := pveBool(current["onboot"]); ok {
		o.OnBoot = b
	}
	if b, ok := pveBool(current["protection"]); ok {
		o.Protection = b
	}
	if b, ok := pveBool(current["agent"]); ok {
		o.Agent = b
	}
	if b, ok := pveBool(current["acpi"]); ok {
		o.Acpi = b
	}
	if b, ok := pveBool(current["tablet"]); ok {
		o.Tablet = b
	}
	if b, ok := pveBool(current["nestedvirt"]); ok {
		o.NestedVirt = b
	}
	if b, ok := pveBool(current["hidden"]); ok {
		o.Hidden = b
	}
	if s := pveStr(current["startup"]); s != "" {
		o.Startup = s
	}
	if s := pveStr(current["hotplug"]); s != "" {
		o.Hotplug = strings.Split(s, ",")
	}
	if s := pveStr(current["boot"]); s != "" {
		if body, ok := strings.CutPrefix(s, "order="); ok {
			o.BootOrder = strings.Split(body, ";")
		}
	}
	return o
}

// PveVMHardwareFromPVE extracts the owned hardware PVE reports. Fields that
// PVE defaults to (machine=i440fx, bios=seabios, vga=std, sockets=1) are NOT
// included when uninteresting; adopt only sets them when pveconform would
// own them.
func PveVMHardwareFromPVE(current map[string]any) VMHardware {
	h := VMHardware{}
	if s := pveStr(current["machine"]); s != "" {
		h.Machine = s
	}
	if s := pveStr(current["bios"]); s != "" {
		h.BIOS = s
	}
	if s := pveStr(current["vga"]); s != "" {
		h.Display = s
	}
	// efidisk0
	if s := pveStr(current["efidisk0"]); s != "" && !isNewStorageSlot(s) {
		if info := parseEFIDiskPVE(s); info != nil {
			h.EFIDisk = info
		}
	}
	// cloud-init (ide2): PVE owns the ide2 cloud-init slot. pveconform's
	// schema declares spec.hardware.cloud-init.{enabled,storage,size}. We
	// only adopt it when PVE reports the PVE-managed cloud-init pool token
	// (":cloudinit") on ide2.
	if s := pveStr(current["ide2"]); s != "" {
		if ci, ok := parsePVECloudInitIDE2(s); ok {
			h.CloudInit = ci
		}
	}
	// numa + sockets live on the VM-wide PVE config keys, not per-device.
	if b, ok := pveBool(current["numa"]); ok {
		h.NUMA = b
	}
	if n := pveInt(current["sockets"]); n != 0 {
		h.Sockets = n
	}
	// tpm0
	if s := pveStr(current["tpm0"]); s != "" {
		if ver := extractTPMVersion(s); ver != "" {
			h.TPM = &TPM{Version: ver}
		}
	}
	// serial0
	if s := pveStr(current["serial0"]); s != "" && s != "none" {
		h.Serial0 = s
	}
	return h
}

// --- LXC ---

// PveLXCMemoryToHuman, PveLXCSwapToHuman: PVE reports LXC memory/swap in MiB
// on /config (integer). Convert to human GiB.
func PveLXCMemoryToHuman(v any) (string, bool) { return PveMemoryToHuman(v) }
func PveLXCSwapToHuman(v any) (string, bool)   { return PveMemoryToHuman(v) }



// PveLXCDisksFromPVE parses PVE's LXC /config report into rootfs + additional
// mount points.
//
// PVE reports LXC rootfs/mp as either:
//   - "local-lvm:vm-9200-disk-0"          (bare pool+volid; size NOT here)
//   - "local-lvm:vm-9200-disk-0,size=4G"  (pool+volid + explicit size)
//
// The size token appears when PVE wrote an explicit size and re-echoes it back
// in the /config report — e.g. after pveconform's LXC create form
// ("local-lvm:4", PVE's documented GiB-integer allocation form). The live
// PVE state on conformance-dev confirms both spellings.
//
// pveconform owns `root.storage` + `root.size` and, for each mount point,
// `storage` + `size` + `mount-point`. When PVE did not report a size token,
// the caller (adopt) should fall back to a PVE storage-list lookup keyed on
// the volid.
func PveLXCDisksFromPVE(current map[string]any) (root LXCRoot, mps []LXCMount, ok bool, rootVolid string, volidHasSize bool) {
	root, mps = LXCRoot{}, nil
	rootVolid = ""
	volidHasSize = false
	if s := pveStr(current["rootfs"]); s != "" && !isNewStorageSlot(s) {
		if info := parseDiskInfo(s); info.pool != "" {
			root.Storage = info.pool
			rootVolid = s
			if info.sizeSet {
				volidHasSize = true
				if str, okH := HumanFromBytes(info.sizeBytes); okH {
					root.Size = str
				}
			}
		}
	}
	for k, v := range current {
		if !isMPSlot(k) {
			continue
		}
		raw := pveStr(v)
		if isNewStorageSlot(raw) {
			continue
		}
		info := parseDiskInfo(raw)
		if info.pool == "" {
			continue
		}
		mp := LXCMount{Slot: k, Storage: info.pool, MountPoint: pveLxcDefaultMP(k)}
		// PVE reports an "mp=<path>" token on mp* when a mount-point was
		// explicitly set.
		if idx := strings.Index(raw, ",mp="); idx >= 0 {
			rest := raw[idx+len(",mp="):]
			if end := strings.IndexByte(rest, ','); end >= 0 {
				mp.MountPoint = rest[:end]
			} else {
				mp.MountPoint = rest
			}
		}
		if info.sizeSet {
			if str, okH := HumanFromBytes(info.sizeBytes); okH {
				mp.Size = str
			}
		}
		mps = append(mps, mp)
	}
	sort.Slice(mps, func(i, j int) bool { return slotSortKey(mps[i].Slot) < slotSortKey(mps[j].Slot) })
	ok = root.Storage != "" || len(mps) > 0
	return root, mps, ok, rootVolid, volidHasSize
}

// PveLXCNetworksFromPVE parses PVE's LXC /config report into pveconform LXC
// networks. PVE reports "name=wired0,bridge=vmbr0,hwaddr=BC:...,type=veth".
//
// pveconform owns bridge and optionally vlan/rate/firewall/hwaddr. adopt
// does NOT pin PVE's auto-generated hwaddr (would cause churn), and does
// not own `type` (PVE normalizes to veth).
func PveLXCNetworksFromPVE(current map[string]any) ([]LXCNetwork, bool) {
	var out []LXCNetwork
	for k, v := range current {
		if !lxcValidNetSlot(k) {
			continue
		}
		f := parseLXCNetFields(pveStr(v))
		n := LXCNetwork{Slot: k, Bridge: f.bridge, Type: "", Tag: f.tag, RateLimit: f.rate, Firewall: f.firewall}
		// adopt does not pin PVE's auto-assigned MAC; leave HWAddr empty.
		// iface: only when PVE does NOT use the default "wired<N>" / "net<N>"
		// slot naming (i.e. a human set a custom name).
		if f.iface != "" && f.iface != k {
			n.Iface = f.iface
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return slotSortKey(out[i].Slot) < slotSortKey(out[j].Slot) })
	return out, len(out) > 0
}

// PveLXCDNSFromPVE extracts LXC DNS + hostname from PVE's /config.
//
// PVE stores:
//
//	hostname:     the guest kernel hostname (pveconform's spec.dns.hostname)
//	nameserver:   space/comma separated IPs (spec.dns.nameservers)
//	searchdomain: the DNS search domain (spec.dns.domain)
func PveLXCDNSFromPVE(current map[string]any) (LXCDNS, bool) {
	d := LXCDNS{}
	if s := pveStr(current["hostname"]); s != "" {
		d.HostName = s
	}
	if s := pveStr(current["nameserver"]); s != "" {
		d.Nameservers = splitNameserver(s)
	}
	if s := pveStr(current["searchdomain"]); s != "" {
		d.Domain = s
	}
	ok := d.HostName != "" || len(d.Nameservers) > 0 || d.Domain != ""
	return d, ok
}

// PveLXCOptionsFromPVE extracts the owned LXC options PVE reports.
func PveLXCOptionsFromPVE(current map[string]any) LXCOptions {
	o := LXCOptions{}
	if b, ok := pveBool(current["unprivileged"]); ok {
		o.Unprivileged = b
	}
	if b, ok := pveBool(current["protection"]); ok {
		o.Protection = b
	}
	if b, ok := pveBool(current["nesting"]); ok {
		o.Nesting = b
	}
	if b, ok := pveBool(current["keyctl"]); ok {
		o.KeyCtl = b
	}
	if b, ok := pveBool(current["fuse"]); ok {
		o.Fuse = b
	}
	if b, ok := pveBool(current["onboot"]); ok {
		o.OnBoot = b
	}
	if s := pveStr(current["startup"]); s != "" {
		o.Startup = s
	}
	return o
}

// PveLXCDescriptionFromPVE returns PVE's LXC `description` trimmed. PVE
// appends a trailing "\n" to LXC descriptions; Drift must TrimSpace both
// sides — adopt does the same.
func PveLXCDescriptionFromPVE(current map[string]any) string {
	return strings.TrimSpace(pveStr(current["description"]))
}

// PveLXCArchFromPVE returns PVE's `arch` (default "amd64").
func PveLXCArchFromPVE(current map[string]any) string {
	if s := pveStr(current["arch"]); s != "" {
		return s
	}
	return "amd64"
}

// PveLXCTemplateFromPVE extracts the ostemplate PVE reports, in the form
// "pool:vztmpl/<filename>".
func PveLXCTemplateFromPVE(current map[string]any) (storage, filename string, ok bool) {
	s := pveStr(current["ostemplate"])
	if s == "" {
		return "", "", false
	}
	// Form: "pool:vztmpl/<filename>[,...]"
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return "", "", false
	}
	storage = s[:colon]
	rest := s[colon+1:]
	const prefix = "vztmpl/"
	if !strings.HasPrefix(rest, prefix) {
		return "", "", false
	}
	rest = rest[len(prefix):]
	// strip trailing options
	if i := strings.IndexByte(rest, ','); i >= 0 {
		rest = rest[:i]
	}
	return storage, rest, true
}

// parseEFIDiskPVE extracts an EFIDisk from PVE's /config "efidisk0" value
// (e.g. "local-lvm:vm-9100-disk-3,size=4M"). The pool is the storage; the
// size is PVE-reported in binary-suffix form. PVE clamps very small sizes
// (see M6 notes); adopt reports the clamped value. Returns nil when the
// shape isn't recognised.
func parseEFIDiskPVE(s string) *EFIDisk {
	info := parseDiskInfo(strings.TrimSpace(s))
	if info.pool == "" {
		return nil
	}
	e := &EFIDisk{Storage: info.pool}
	if info.sizeSet {
		if strH, ok := HumanFromBytes(info.sizeBytes); ok {
			e.Size = strH
		}
	}
	// PVE doesn't report efitype on /config; leave Template empty.
	return e
}

// parsePVECloudInitIDE2 extracts the CloudInit block from PVE's /config
// "ide2" value. PVE owns a cloud-init slot when the value contains the
// ":cloudinit" pool token that pveconform emitted at create
// ("<pool>:cloudinit,size=<n>"). The live report form is
// "<pool>:<vmid>/<vmid>-cloudinit.qcow2,...", which pveconform recognises
// via the pool + ":cloudinit" create token OR the ".qcow2" + ",size="
// pair on the live form. We only adopt when PVE actually stores a
// cloud-init volume; an ide2 with a plain ISO (media=cdrom) is NOT
// cloud-init.
//
// The size is PVE-reported in binary suffixes ("4K"). adopt maps it to
// pveconform's human form.
func parsePVECloudInitIDE2(s string) (CloudInit, bool) {
	out := CloudInit{}
	s = strings.TrimSpace(s)
	if s == "" || s == "none" {
		return out, false
	}
	// PVE live form for cloud-init: "local:100/vm-100-cloudinit.qcow2,media=cdrom,size=4K"
	// or our create form:  "local:cloudinit,size=4M" (when PVE rewrites it).
	// Look for ":cloudinit" pool token OR "/vm-<N>-cloudinit" volume name.
	ciToken := false
	colons := strings.Count(s, ":")
	if strings.Contains(s, ":cloudinit") && colons == 1 {
		ciToken = true
	} else if strings.Contains(s, "-cloudinit") {
		ciToken = true
	}
	if !ciToken {
		return out, false
	}
	cp, cs, ok := splitCloudInit(s)
	if !ok {
		return out, false
	}
	out.Enabled = true
	out.Storage = cp
	if cs != "" {
		if bH, ok := pveDiskSizeBytesHuman(cs); ok {
			out.Size = bH
		}
	}
	if out.Size == "" {
		// PVE default cloud-init volume is 4M — pveconform's Validate
		// requires a size; adopt uses the PVE default.
		out.Size = "4MiB"
	}
	return out, true
}

// pveDiskSizeBytesHuman converts PVE's binary-suffix size token ("4K", "4M",
// "8G", "0.5G") to a pveconform quantity ("4KiB", "4MiB", "8GiB"). PVE's
// pve-size suffixes are binary (K=2^10, M=2^20, G=2^30, T=2^40), matching
// pveconform's ParseBytes binary units; this helper just converts to bytes
// and re-renders.
func pveDiskSizeBytesHuman(s string) (string, bool) {
	b, ok := pveDiskSizeBytes(s)
	if !ok || b <= 0 {
		return "", false
	}
	return HumanFromBytes(b)
}

// --- shared helpers ---

// HumanFromBytes converts a byte count to a pveconform-friendly quantity
// string ("4GiB", "512MiB", "512KiB"). pveconform's ParseBytes accepts
// binary KiB/MiB/GiB/TiB suffixes. ok=false when the value is zero or
// negative; otherwise the largest exact binary unit is chosen.
func HumanFromBytes(b int64) (string, bool) {
	if b <= 0 {
		return "", false
	}
	const (kib = int64(1) << 10
		mib = int64(1) << 20
		gib = int64(1) << 30
		tib = int64(1) << 40)
	switch {
	case b%tib == 0:
		return strconv.FormatInt(b/tib, 10) + "TiB", true
	case b%gib == 0:
		return strconv.FormatInt(b/gib, 10) + "GiB", true
	case b%mib == 0:
		return strconv.FormatInt(b/mib, 10) + "MiB", true
	case b%kib == 0:
		return strconv.FormatInt(b/kib, 10) + "KiB", true
	default:
		// Non-round value: fall back to bytes. pveconform's ParseBytes
		// accepts a bare integer as bytes, so this is round-trippable.
		return strconv.FormatInt(b, 10), true
	}
}

// pveAnyBytesFromMiB converts a PVE MiB value (int or string) to bytes.
func pveAnyBytesFromMiB(v any) (int64, bool) {
	mib := pveInt(v)
	if mib <= 0 {
		// PVE reports some values as float (e.g. 0.5 is not allowed on
		// memory but tolerate it on swap).
		switch x := v.(type) {
		case float64:
			if x <= 0 {
				return 0, false
			}
			mib = int(x)
		default:
			return 0, false
		}
	}
	return int64(mib) * (1 << 20), true
}

// pveBool coerces PVE config values to bool. PVE reports 0/1/absent;
// JSON numbers tolerated.
func pveBool(v any) (bool, bool) {
	if v == nil {
		return false, false
	}
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		b, err := strconv.ParseBool(x)
		if err != nil {
			return false, false
		}
		return b, true
	default:
		return pveInt(v) == 1, true
	}
}

// slotSortKey gives a deterministic ordering for scsi0/scsi1/.../virtio0/...
// and for net0/net1/.../mp0/mp1/...: the sort key is "slot name with the
// prefix stripped, then the prefix". This puts scsi0 < scsi1 < scsi2, and
// scsi* < sata* < virtio* (alphabetical prefix).
func slotSortKey(s string) string {
	for _, prefix := range []string{"scsi", "sata", "virtio", "net", "mp", "ide", "nvme"} {
		if strings.HasPrefix(s, prefix) {
			rest := s[len(prefix):]
			return prefix + "\x00" + rest
		}
	}
	return s
}

// pveLxcDefaultMP returns PVE's default mp<N> mount path "/mnt/mp<N>".
func pveLxcDefaultMP(slot string) string {
	return "/mnt/" + slot
}
