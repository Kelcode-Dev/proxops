package schema

// This file exposes a small, adopt-only surface for turning PVE wire
// representations (as returned by GET /qemu/{id}/config and
// GET /lxc/{id}/config) back into proxops schema fields.
//
// The helpers are intentionally narrow: they only cover the fields that
// proxops actually models. Anything PVE reports but proxops does not
// understand is out of scope and surfaces on docs/GAPS.md instead.
//
// Naming convention: <Kind>Pve<Thing>. Each function takes a PVE raw value
// and returns (proxops-friendly form, ok). ok=false means the value
// could not be mapped to a proxops field.

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// --- VM ---

// PveMemoryToGiB converts PVE's reported memory (integer MiB, as a string or
// int) to a proxops "human GiB" string ("4GiB", "0.5GiB"). PVE reports
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

// PveDisksFromPVE parses PVE's VM /config report into proxops disks.
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
			continue // detached/none slot; proxops does not model it
		}
		info := parseDiskInfo(raw)
		if info.pool == "" {
			continue
		}
		d := Disk{
			Slot:     k,
			Storage:  info.pool,
			IOThread: info.iothread,
		}
		// M13: adopt the structured drive options PVE reports. Tokens PVE
		// omits are PVE defaults and stay unowned ("" / nil) so Drift does
		// not materialise them on the next apply.
		if info.discard != "" {
			d.Discard = info.discard
		}
		if info.ssd != nil {
			ssd := *info.ssd
			d.SSD = &ssd
		}
		if info.aio != "" {
			d.AIO = info.aio
		}
		if info.sizeSet {
			if s, ok := HumanFromBytes(info.sizeBytes); ok {
				d.Size = s
			} else {
				// Not representable in proxops quantity grammar.
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

// PveNICsFromPVE parses PVE's VM /config report into proxops NICs.
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
			Slot:      k,
			Model:     f.model,
			Bridge:    f.bridge,
			VLAN:      f.vlan,
			RateLimit: f.rate,
			Firewall:  f.firewall,
		}
		// adopt does not pin PVE's auto-assigned MAC; leave MAC empty.
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return slotSortKey(out[i].Slot) < slotSortKey(out[j].Slot) })
	return out, len(out) > 0
}

// PveDiskMedia inspects one PVE disk-slot report value and returns the
// "media" token if PVE reports one, else "". Adopt uses this to detect
// PVE's cloud-init volume on non-IDE slots (probe-verified on
// prod-a: "vm-NNN-cloudinit,media=cdrom" on scsi1 of every VM that
// was provisioned with qm cloud-init) which proxops does not own in its
// schema (cloud-init attaches to ide2/ide3 only).
func PveDiskMedia(vmid int, current map[string]any, slot string) string {
	_ = vmid
	raw := pveStr(current[slot])
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.TrimSpace(tok)
		if strings.HasPrefix(tok, "media=") {
			return strings.TrimPrefix(tok, "media=")
		}
	}
	// The PVE cloud-init volume naming convention also applies when no
	// media token is present: a slot whose PVE-assigned volume name
	// carries "-cloudinit" is a PVE-owned cdrom.
	if strings.Contains(raw, "-cloudinit") {
		return "cdrom"
	}
	return ""
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
		// to a proxops ISO metadata.name.
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

// --- M11: cloud-init DATA fields (reverse-translation) ---
//
// These helpers read PVE's /config cloud-init data keys back into proxops
// CloudInitData. Ownership + redaction follow the schema contract:
//   - ciuser / nameserver / searchdomain / ipconfig<N>: adopted verbatim.
//   - sshkeys: PVE stores public-key material; M10 redaction rule applies.
//     adopt returns a single-entry {"*"} sentinel slice when PVE reported a
//     non-empty sshkeys, so the committed manifest shows the shape
//     (ssh-keys: ["*"]) without leaking the keys. The operator replaces the
//     sentinel with real keys before first apply; proxops refuses to mix
//     sentinel + real keys at parse time.
//   - cipassword / cicustom / ciupgrade: NOT adopted (secret / PVE-owned /
//     out-of-model) — they stay in the gap report by default.

// PveCIUserFromPVE returns PVE's `ciuser` (string, ok). ok=false when the key
// is absent or blank.
func PveCIUserFromPVE(current map[string]any) (string, bool) {
	s := strings.TrimSpace(pveStr(current["ciuser"]))
	if s == "" {
		return "", false
	}
	return s, true
}

// PveCISshKeysFromPVE returns the SSH-keys slice for adoption. When PVE reports
// a non-empty `sshkeys`, a single {"*"} redacted-sentinel slice is returned
// (the actual keys never reach the manifest). Empty when PVE has no sshkeys.
func PveCISshKeysFromPVE(current map[string]any) []string {
	if strings.TrimSpace(pveStr(current["sshkeys"])) == "" {
		return nil
	}
	return []string{CloudInitRedactedSentinel}
}

// PveCINameserversFromPVE returns PVE's `nameserver` CSV (space-separated on
// the wire) split into a token slice. Empty when absent/blank.
func PveCINameserversFromPVE(current map[string]any) []string {
	return pveCSVField(current["nameserver"])
}

// PveCISshLinesFromPVE decodes PVE's `sshkeys` /config value (urlencoded,
// newline-joined) into plain OpenSSH public-key lines. It returns the
// decoded lines + whether ANY key was present.
//
// PVE percent-encodes the whole field (M11 wire finding: spaces -> %20,
// '@' -> %40; Go's url.QueryUnescape treats '+' as a literal plus, matching
// PVE's encoding). The comment column of each line is preserved.
//
// No PII leaves this function: callers (adopt) may pass the decoded lines to
// SOPS-matching (which fingerprints them) or redact them. Adopt never logs
// the lines directly.
func PveCISshLinesFromPVE(current map[string]any) (lines []string, present bool) {
	raw := pveStr(current["sshkeys"])
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	decoded := raw
	if s, err := url.QueryUnescape(strings.ReplaceAll(raw, "+", "%2B")); err == nil {
		decoded = s
	}
	for _, l := range strings.Split(decoded, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			lines = append(lines, t)
		}
	}
	return lines, len(lines) > 0
}

// PveCIPasswordPresent reports whether PVE's /config carries a
// cipassword value. PVE 9.2 always masks it (probe-verified M13.2: a fixed
// '********' string, plaintext unrecoverable); presence itself is the only
// signal adopt can use. Used for the M13.2 cloud-init secret census.
func PveCIPasswordPresent(current map[string]any) bool {
	return strings.TrimSpace(pveStr(current["cipassword"])) != ""
}

// PveCISearchDomainsFromPVE returns PVE's `searchdomain` CSV split.
func PveCISearchDomainsFromPVE(current map[string]any) []string {
	return pveCSVField(current["searchdomain"])
}

// PveCIIPConfigsFromPVE returns PVE's `ipconfig<N>` static-IP slots parsed into
// CloudInitIPConfig. Only PVE slots that carry an "ip=<cidr>" token are
// adopted; "dhcp" slots are skipped (dhcp is not modelled in M11). The slice
// is sorted by NIC index for determinism.
func PveCIIPConfigsFromPVE(current map[string]any) []CloudInitIPConfig {
	var out []CloudInitIPConfig
	for k, v := range current {
		if !strings.HasPrefix(k, "ipconfig") {
			continue
		}
		idStr := strings.TrimPrefix(k, "ipconfig")
		nic, err := strconv.Atoi(idStr)
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(pveStr(v))
		if raw == "" || raw == "dhcp" {
			continue
		}
		// PVE static form: "ip=<cidr>[,gw=<addr>]"
		ip := ""
		gw := ""
		for _, tok := range strings.Split(raw, ",") {
			tok = strings.TrimSpace(tok)
			if eq, ok := strings.CutPrefix(tok, "ip="); ok {
				ip = eq
			} else if g, ok := strings.CutPrefix(tok, "gw="); ok {
				gw = g
			}
		}
		if ip == "" {
			continue
		}
		out = append(out, CloudInitIPConfig{NIC: nic, IP: ip, Gateway: gw})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NIC < out[j].NIC })
	return out
}

// PveCloudInitDataFromPVE assembles the full CloudInitData from a PVE /config
// report. This is the single adoption entry-point used by adopt for both VM
// and TemplateVM.
func PveCloudInitDataFromPVE(current map[string]any) CloudInitData {
	cd := CloudInitData{}
	if u, ok := PveCIUserFromPVE(current); ok {
		cd.CIUser = u
	}
	if keys := PveCISshKeysFromPVE(current); keys != nil {
		cd.SSHKeys = keys
	}
	if ns := PveCINameserversFromPVE(current); ns != nil {
		cd.Nameservers = ns
	}
	if sd := PveCISearchDomainsFromPVE(current); sd != nil {
		cd.SearchDomains = sd
	}
	if ipc := PveCIIPConfigsFromPVE(current); ipc != nil {
		cd.IPConfigs = ipc
	}
	return cd
}

// pveCSVField splits a PVE whitespace-separated CSV report value into tokens,
// dropping empties. Returns nil when the value is absent/blank.
func pveCSVField(v any) []string {
	s := strings.TrimSpace(pveStr(v))
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

// PveVMOptionsFromPVE extracts the owned VM options PVE reports. Each
// entry is only included when PVE actually set it.
//
// Returns:
//
//   - onboot (bool, ok)
//   - protection (bool, ok)
//   - agent (bool, ok)
//   - acpi (bool, ok)
//   - tablet (bool, ok)
//   - nestedvirt (bool, ok)
//   - hidden (bool, ok)
//   - startup (string, ok)
//   - hotplug ([]string, ok)
//   - boot-order ([]string, ok) — parsed from PVE's "order=scsi0;ide2;net0"
//   - numa (bool, ok)
//   - sockets (int, ok)
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
// included when uninteresting; adopt only sets them when proxops would
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
	// cloud-init (ide2): PVE owns the ide2 cloud-init slot. proxops's
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
	// M13.2: hostpciN PCI passthrough. Adopt every hostpciN PVE reports;
	// PvePCIDevicesFromPVE collects them in deterministic slot order,
	// modelling only the BDF + the pcie token (PVE-side-only tokens are
	// surfaced by the adopt caller as PVE-owned gaps).
	h.PCIDevices = PvePCIDevicesFromPVE(current)
	return h
}

// --- LXC ---

// PveLXCMemoryToHuman, PveLXCSwapToHuman: PVE reports LXC memory/swap in MiB
// on /config (integer). Convert to human GiB.
func PveLXCMemoryToHuman(v any) (string, bool) { return PveMemoryToHuman(v) }
func PveLXCSwapToHuman(v any) (string, bool)   { return PveMemoryToHuman(v) }

// LXC mount shapes proxops's adopt knows about.
type LXCDiskShape struct {
	Root    LXCRoot
	Mounts  []LXCMount
	HasRoot bool
	// BindMountSlots are mp slots PVE reports as host-path bind mounts
	// ("mp0=/mnt/host-share,mp=/srv/data"). M13: bind mounts are now a
	// first-class declarative shape (LXC.spec.bind-mounts), so adopt
	// populates the parsed HostPath/MountPoint/ReadOnly here and the
	// caller (adoptLXC) writes them into the manifest. Raw is kept for the
	// gap/fidelity text when a bind cannot be represented (e.g. an
	// unsupported extra token).
	BindMountSlots []LXCBindMount
}

// LXCBindMount is one PVE LXC host-path bind mount (mpN=<host>,mp=<guest>).
type LXCBindMount struct {
	Slot       string
	Raw        string // PVE wire value, e.g. "/mnt/host-share,mp=/srv/data"
	HostPath   string // the host directory (first token)
	MountPoint string // the in-guest path (mp= token)
	ReadOnly   *bool  // ro= token (nil = PVE default read-write)
}

// PveLXCBindMountsFromPVE returns the host-path bind mounts PVE reports on
// mp* slots (M13). Allocated volumes are excluded (they land in
// spec.mount-points). The result is sorted by slot for determinism.
func PveLXCBindMountsFromPVE(current map[string]any) []LXCBindMount {
	return pveLXCDisksFromPVEInternal(current).BindMountSlots
}

// PveLXCDisksFromPVE parses PVE's LXC /config report into rootfs + additional
// mount points.
//
// PVE reports LXC rootfs/mp as either:
//   - "local-lvm:vm-9200-disk-0"          (bare pool+volid; size NOT here)
//   - "local-lvm:vm-9200-disk-0,size=4G"  (pool+volid + explicit size)
//
// The size token appears when PVE wrote an explicit size and re-echoes it back
// in the /config report — e.g. after proxops's LXC create form
// ("local-lvm:4", PVE's documented GiB-integer allocation form). The live
// PVE state on conformance-dev confirms both spellings.
//
// proxops owns `root.storage` + `root.size` and, for each mount point,
// `storage` + `size` + `mount-point`. When PVE did not report a size token,
// the caller (adopt) should fall back to a PVE storage-list lookup keyed on
// the volid.
func PveLXCDisksFromPVE(current map[string]any) (root LXCRoot, mps []LXCMount, ok bool, rootVolid string, volidHasSize bool) {
	return pveLXCDisksFromPVEInternal(current).unpack()
}

type lxcDiskShapeInternal struct {
	LXCDiskShape
	rootVolid    string
	volidHasSize bool
}

func (s lxcDiskShapeInternal) unpack() (LXCRoot, []LXCMount, bool, string, bool) {
	return s.Root, s.Mounts, s.HasRoot || len(s.Mounts) > 0, s.rootVolid, s.volidHasSize
}

func pveLXCDisksFromPVEInternal(current map[string]any) lxcDiskShapeInternal {
	out := lxcDiskShapeInternal{}
	rootVolid := ""
	volidHasSize := false
	if s := pveStr(current["rootfs"]); s != "" && !isNewStorageSlot(s) {
		if info := parseDiskInfo(s); info.pool != "" {
			out.Root.Storage = info.pool
			out.HasRoot = true
			rootVolid = s
			if info.sizeSet {
				volidHasSize = true
				if str, okH := HumanFromBytes(info.sizeBytes); okH {
					out.Root.Size = str
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
		// Host-path bind mount ("mp0=/mnt/host-share,mp=/srv/data" or the
		// legacy "mp0=/mnt/host-share:/srv/data"): the value starts with a
		// host path, not a "pool:" storage token. M13: bind mounts are a
		// first-class declarative shape (spec.bind-mounts); parse the host
		// path + guest path + ro token, keeping the raw value for the gap
		// text when the shape is unparseable.
		if isLXCBindMount(raw) {
			bm := LXCBindMount{Slot: k, Raw: raw}
			if host, guest, ro, ok := lxcBindParseLive(raw); ok {
				bm.HostPath, bm.MountPoint, bm.ReadOnly = host, guest, ro
			}
			out.BindMountSlots = append(out.BindMountSlots, bm)
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
		// M13: adopt the per-mount option tokens PVE actually reports. A
		// token PVE omits equals its documented default, so it stays nil
		// (unowned) — that keeps Drift from materialising a token the
		// operator never asked for.
		if opts := lxcMountOptionsFromLive(raw); opts != nil {
			mp.Options = opts
		}
		out.Mounts = append(out.Mounts, mp)
	}
	sort.Slice(out.Mounts, func(i, j int) bool { return slotSortKey(out.Mounts[i].Slot) < slotSortKey(out.Mounts[j].Slot) })
	sort.Slice(out.BindMountSlots, func(i, j int) bool {
		return slotSortKey(out.BindMountSlots[i].Slot) < slotSortKey(out.BindMountSlots[j].Slot)
	})
	out.rootVolid = rootVolid
	out.volidHasSize = volidHasSize
	return out
}

// isLXCBindMount reports whether PVE's LXC mp* report value is a host-path
// bind mount ("mpN=<hostpath>:<guestpath>[,ro]") rather than an allocated
// storage volume ("mpN=<pool>:<volid>[,mp=<guestpath>]"). The discriminator
// is that the storage form's first token contains a colon AND has a valid
// storage-id prefix before it; the host-path form is either a bare path or a
// "<path>:<path>" pair. proxops's owned LXC mpN shape is allocated
// volumes only (docs/GAPS.md); bind mounts are reported by adopt, not owned.
func isLXCBindMount(raw string) bool {
	trim := strings.TrimSpace(raw)
	// Allocated-volume form always starts with a storage id:
	// "pool:..." where pool is an alpha/dash storage id. But host paths CAN
	// contain colons (e.g. "/mnt/a:b:/srv" — rare in practice). The PVE
	// storage-id grammar is [a-zA-Z0-9_-]+; a host path starts with '/'.
	if strings.HasPrefix(trim, "/") {
		return true
	}
	pool, _, found := strings.Cut(trim, ":")
	if !found {
		// No "pool:" at all. PVE allocated volumes always carry one.
		return false
	}
	pool = strings.TrimSpace(pool)
	if pool == "" || strings.Contains(pool, "/") {
		// Storage ids never contain '/'. Pool-with-slash means this is a
		// host path with a slash in it — a bind mount.
		return true
	}
	// A pool token is [A-Za-z0-9_-]+. If the pool part is anything else
	// (e.g. contains '.' like a hostname, or is just a number), we treat
	// it as a host path and thus a bind mount.
	for _, c := range pool {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			continue
		default:
			// Not a storage-id character → the token is not a pool. PVE
			// reports a host path like "mnt/share:/srv/data" (no leading
			// '/'). Treat as a bind mount.
			return true
		}
	}
	// A colon-lead "pool:<volid>" form with a valid pool and PVE-style
	// volume token is an allocated volume → not a bind mount.
	return false
}

// PveLXCNetworksFromPVE parses PVE's LXC /config report into proxops LXC
// networks. PVE reports "name=wired0,bridge=vmbr0,hwaddr=BC:...,type=veth".
//
// proxops owns bridge and optionally vlan/rate/firewall/hwaddr. adopt
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
		// ip=/gw= are user-set static addressing PVE reports back when a
		// container has them (probe-verified on prod-a LXC 110/111:
		// net0=...,ip=192.168.192.110/18,gw=192.168.192.5, type=veth).
		// They are OWNED by proxops — PVE does not auto-assign them — so
		// adopt captures them faithfully.
		if f.ip != "" {
			n.Ip = f.ip
		}
		if f.gw != "" {
			n.Gw = f.gw
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return slotSortKey(out[i].Slot) < slotSortKey(out[j].Slot) })
	return out, len(out) > 0
}

// parseLXCFeaturesNesting detects PVE 9.x's composite features= form for
// `nesting=1`. PVE 8.x reported nesting as a top-level key; 9.x moved it
// into a single `features=` property with a comma-separated `k=v` body.
// Probe source: prod-a LXC 203 /config reports `features = nesting=1`.
//
// PVE /config PUT accepts `features=nesting=1` to update it (probe-verified
// on conformance-dev 2026-09-10), but the top-level `nesting=` form is
// rejected there with "property is not defined in schema". So proxops's
// LXC.Drift must also emit `features=` (handled separately).
func parseLXCFeaturesNesting(current map[string]any) bool {
	s := pveStr(current["features"])
	if s == "" {
		return false
	}
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		if k, v, found := strings.Cut(tok, "="); found && k == "nesting" && v == "1" {
			return true
		}
	}
	return false
}

// PveLXCDNSFromPVE extracts LXC DNS + hostname from PVE's /config.
//
// PVE stores:
//
//	hostname:     the guest kernel hostname (proxops's spec.dns.hostname)
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
//
// Every boolean is a tri-state pointer: PVE's /config report only carries
// keys the operator actually set (PVE omits `protection=0` etc.), so a
// `0` report value is meaningful (an explicit unset) and must be captured.
// The PVE 9.x composite `features=` form also lands here: `nesting=1`
// appears inside features= on PVE 9.x reports (top-level `nesting=` was
// moved into the composite in PVE 9.x).
func PveLXCOptionsFromPVE(current map[string]any) LXCOptions {
	o := LXCOptions{}
	// Tri-state capture: PVE only reports keys the operator actually set,
	// so every observed value — INCLUDING an explicit "0" — is owned
	// (pointer-bool) and must round-trip. Absent keys stay nil
	// ("PVE decides") so adopt never invents a value PVE did not report.
	// Order is deterministic (PVE's /config report ordering is not stable
	// across nodes; the captured values are scalars, so the resulting
	// LXCOptions is independent of observation order).
	setField := func(v **bool, key string) {
		if b, ok := pveBool(current[key]); ok {
			pb := b
			*v = &pb
		}
	}
	setField(&o.Unprivileged, "unprivileged")
	setField(&o.Protection, "protection")
	setField(&o.KeyCtl, "keyctl")
	setField(&o.Fuse, "fuse")
	setField(&o.OnBoot, "onboot")
	setField(&o.Console, "console")
	// nesting: PVE 8.x reports a top-level `nesting=` key; PVE 9.x moved
	// it into the composite `features=` property (`features=nesting=1`,
	// probe-verified on prod-a LXC 203). A `features=` key present
	// at all means nesting has an explicit (possibly off) value; capture
	// it. Neither key present → nil ("PVE decides").
	if b, ok := pveBool(current["nesting"]); ok {
		pb := b
		o.Nesting = &pb
	} else if pveStr(current["features"]) != "" {
		pb := parseLXCFeaturesNesting(current)
		o.Nesting = &pb
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

// lxcMountOptionsFromLive builds an LXCMountOptions from PVE's allocated-mp
// report value, adopting ONLY the option tokens PVE actually reports. A token
// PVE omits equals its documented pct.conf default (ro=0, backup=1, acl=0,
// quota=0, shared=0), so it stays nil (unowned) and Drift will not
// materialise it. Returns nil when no owned token is present.
func lxcMountOptionsFromLive(raw string) *LXCMountOptions {
	var o *LXCMountOptions
	if v, p := lxcRawBoolToken(raw, "ro="); p {
		if o == nil {
			o = &LXCMountOptions{}
		}
		b := v
		o.ReadOnly = &b
	}
	if v, p := lxcRawBoolToken(raw, "backup="); p {
		if o == nil {
			o = &LXCMountOptions{}
		}
		b := v
		o.Backup = &b
	}
	if v, p := lxcRawBoolToken(raw, "acl="); p {
		if o == nil {
			o = &LXCMountOptions{}
		}
		b := v
		o.ACL = &b
	}
	if v, p := lxcRawBoolToken(raw, "quota="); p {
		if o == nil {
			o = &LXCMountOptions{}
		}
		b := v
		o.Quota = &b
	}
	if v, p := lxcRawBoolToken(raw, "shared="); p {
		if o == nil {
			o = &LXCMountOptions{}
		}
		b := v
		o.Shared = &b
	}
	if mo := lxcRawToken(raw, "mountoptions="); mo != "" {
		if o == nil {
			o = &LXCMountOptions{}
		}
		o.MountOptions = mo
	}
	return o
}

// parseEFIDiskPVE extracts an EFIDisk from PVE's /config "efidisk0" value
// (e.g. "local-lvm:vm-9100-disk-3,efitype=4m,pre-enrolled-keys=1,size=4M").
// The pool is the storage; the size is PVE-reported in binary-suffix form.
// PVE clamps very small sizes (see M6 notes); adopt reports the clamped
// value. Returns nil when the shape isn't recognised.
//
// M13: PVE DOES report `efitype=` and `pre-enrolled-keys=` on /config
// (probe-verified PVE 9.2.2 — the earlier "PVE doesn't report efitype" note
// was wrong; live reports carry both tokens). adopt maps efitype → Template
// and pre-enrolled-keys → SecureBoot ("enabled"/"disabled"). The
// PVE-managed `ms-cert=` token is NOT modelled (proxops treats it as
// PVE-owned; Drift preserves it verbatim).
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
	if info.efitype != "" {
		e.Template = info.efitype
	}
	if info.preEnrolled != nil {
		if *info.preEnrolled {
			e.SecureBoot = "enabled"
		} else {
			e.SecureBoot = "disabled"
		}
	}
	return e
}

// parsePVECloudInitIDE2 extracts the CloudInit block from PVE's /config
// "ide2" value. PVE owns a cloud-init slot when the value contains the
// ":cloudinit" pool token that proxops emitted at create
// ("<pool>:cloudinit,size=<n>"). The live report form is
// "<pool>:<vmid>/<vmid>-cloudinit.qcow2,...", which proxops recognises
// via the pool + ":cloudinit" create token OR the ".qcow2" + ",size="
// pair on the live form. We only adopt when PVE actually stores a
// cloud-init volume; an ide2 with a plain ISO (media=cdrom) is NOT
// cloud-init.
//
// The size is PVE-reported in binary suffixes ("4K"). adopt maps it to
// proxops's human form.
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
		// PVE default cloud-init volume is 4M — proxops's Validate
		// requires a size; adopt uses the PVE default.
		out.Size = "4MiB"
	}
	return out, true
}

// pveDiskSizeBytesHuman converts PVE's binary-suffix size token ("4K", "4M",
// "8G", "0.5G") to a proxops quantity ("4KiB", "4MiB", "8GiB"). PVE's
// pve-size suffixes are binary (K=2^10, M=2^20, G=2^30, T=2^40), matching
// proxops's ParseBytes binary units; this helper just converts to bytes
// and re-renders.
func pveDiskSizeBytesHuman(s string) (string, bool) {
	b, ok := pveDiskSizeBytes(s)
	if !ok || b <= 0 {
		return "", false
	}
	return HumanFromBytes(b)
}

// --- shared helpers ---

// HumanFromBytes converts a byte count to a proxops-friendly quantity
// string ("4GiB", "512MiB", "512KiB"). proxops's ParseBytes accepts
// binary KiB/MiB/GiB/TiB suffixes. ok=false when the value is zero or
// negative; otherwise the largest exact binary unit is chosen.
func HumanFromBytes(b int64) (string, bool) {
	if b <= 0 {
		return "", false
	}
	const (
		kib = int64(1) << 10
		mib = int64(1) << 20
		gib = int64(1) << 30
		tib = int64(1) << 40
	)
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
		// Non-round value: fall back to bytes. proxops's ParseBytes
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
