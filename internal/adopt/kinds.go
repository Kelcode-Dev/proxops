package adopt

import (
	"context"
	"fmt"
	"strings"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// This file converts PVE /qemu/{id}/config and /lxc/{cid}/config reports into
// pveconform VM / LXC manifests. The conversion covers only the fields
// pveconform actually models (the "owned-field" surface of the schema). Every
// PVE key that appears in the /config report but is not (a) owned by
// pveconform or (b) PVE bookkeeping (digest, meta, ...) is surfaced on
// Result.Gaps so operators see exactly what is not represented in the
// generated YAML.

// VMKeysOwned lists every PVE /config key that pveconform's VM schema models
// (used for gap detection: a key NOT in this set AND NOT in
// PVEBookkeepingKeys becomes a Gap). Dynamic disk / NIC slots ("scsi0",
// "virtio4", "net1", ...) are matched by prefix in VMKeyIsDynamic, not
// listed here.
var VMKeysOwned = map[string]bool{
	"name":        true,
	"memory":      true,
	"cpu":         true,
	"cores":       true,
	"tags":        true,
	"description": true,
	"scsihw":      true, // per-disk controller; attached to scsi0 in the manifest
	"machine":     true,
	"bios":        true,
	"vga":         true,
	"sockets":     true,
	"numa":        true,
	"efidisk0":    true,
	"tpm0":        true,
	"serial0":     true,
	"ide2":        true, // cloud-init OR cdrom (pveconform's 3-state)
	"ide3":        true, // cdrom when cloud-init is on ide2
	"onboot":      true,
	"startup":     true,
	"protection":  true,
	"agent":       true,
	"acpi":        true,
	"tablet":      true,
	"hotplug":     true,
	"boot":        true,
	"nestedvirt":  true,
	"hidden":      true,
}

// VMKeyIsDynamic reports whether a PVE /config key is a dynamic device slot
// pveconform owns: scsiN / virtioN / sataN / netN. The "ide" slots are
// handled statically (cdrom + cloud-init are the two owned shapes).
func VMKeyIsDynamic(k string) bool {
	for _, p := range []string{"scsi", "virtio", "sata", "net"} {
		if strings.HasPrefix(k, p) {
			rest := k[len(p):]
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

// LXCKeysOwned lists every PVE /lxc/{id}/config key that pveconform's LXC
// schema models. mpN (dynamic) and netN are handled by LXCKeyIsDynamic.
var LXCKeysOwned = map[string]bool{
	"cores":        true,
	"memory":       true,
	"swap":         true,
	"arch":         true,
	"hostname":     true,
	"nameserver":   true,
	"searchdomain": true,
	"description":  true,
	"tags":         true,
	"rootfs":       true,
	"unprivileged": true,
	"protection":   true,
	"nesting":      true,
	"keyctl":       true,
	"fuse":         true,
	"onboot":       true,
	"startup":      true,
}

// LXCKeyIsDynamic reports whether a PVE LXC /config key is a dynamic device
// slot pveconform owns: mpN / netN.
func LXCKeyIsDynamic(k string) bool {
	return isMPSlotKey(k) || isNICSlotKey(k)
}

func isMPSlotKey(k string) bool {
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

func isNICSlotKey(k string) bool {
	if !strings.HasPrefix(k, "net") || len(k)==3 {
		return false
	}
	for _, c := range k[3:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// adoptVM reads one VM's /config and writes a single pveconform manifest
// under vm/<cluster>/. Returns nil on success; an error when PVE's /config
// could not be read.
func (ac *adoptContext) adoptVM(ctx context.Context, node string, e pveclient.VMListEntry) error {
	cfg, gErr := ac.pve.VM().Get(ctx, node, e.VMID)
	if gErr != nil {
		return fmt.Errorf("adopt: VM %s#%d /config: %w", node, e.VMID, gErr)
	}
	raw := map[string]any{}
	for k, v := range cfg {
		raw[k] = v
	}

	vm := schema.NewVM()
	vm.Metadata.Name = nameForPVE(pveStr(raw["name"]), e.VMID, "vm")
	vm.Spec.Node = node
	vm.Spec.VMID = e.VMID

	// memory.
	if mem, ok := schema.PveMemoryToHuman(raw["memory"]); ok {
		vm.Spec.Memory = mem
	}

	// cpu.
	if cpuType := schema.PveCpuType(raw); cpuType != "" {
		vm.Spec.CPU.Type = cpuType
	}
	cores := schema.PveCpuCores(raw)
	if cores > 0 {
		vm.Spec.CPU.Cores = cores
	}

	// disks.
	if disks, ok := schema.PveDisksFromPVE(raw); ok {
		vm.Spec.Disks = disks
	}

	// networks.
	if nics, ok := schema.PveNICsFromPVE(raw); ok {
		vm.Spec.NICs = nics
	}

	// hardware.
	hw := schema.PveVMHardwareFromPVE(raw)
	// cdrom: resolved against artifact names so the reference is a
	// pveconform ISO metadata.name (the M8 composition cross-reference).
	// An empty volid means PVE reports an explicit detach
	// ("ide2 = none" form) → pveconform's 3-state iso: none.
	if isoVolid, cdromOK := schema.PveCDROMFromPVE(raw); cdromOK {
		if isoVolid == "" {
			hw.Cdrom = schema.CDDrive{Iso: schema.CDROMNone}
		} else if isoName, known := ac.names.iso[isoVolid]; known {
			hw.Cdrom.Iso = isoName
		} else {
			// PVE reports a cdrom that references an ISO with no pveconform
			// manifest in this compose set: surface it as a gap so the
			// operator adds the ISO first.
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindVM,
				Node:  node,
				ID:    e.VMID,
				Field: "ide2/ide3",
				Value: isoVolid,
				Note:  "PVE reports a cdrom that references an ISO with no pveconform manifest in this adopt pass; generate the ISO first, then re-point spec.hardware.cdrom.iso to it",
			})
		}
	}
	vm.Spec.Hardware = hw

	// options.
	vm.Spec.Options = schema.PveVMOptionsFromPVE(raw)

	// tags: strip pveconform's ownership tag (the schema re-appends it).
	if tags := pveStr(raw["tags"]); tags != "" {
		kept := []string{}
		for _, t := range strings.Split(tags, ",") {
			t = strings.TrimSpace(t)
			if t == "" || t == schema.PveOwnershipTag {
				continue
			}
			kept = append(kept, t)
		}
		vm.Spec.Tags = kept
	}

	// state.
	vm.Spec.State = pveStatusToState(e.Status)

	if wErr := ac.writeManifest(schema.KindVM, vm, vm.Metadata.Name); wErr != nil {
		return wErr
	}

	// Gap detection: every PVE /config key pveconform does not model AND
	// is not PVE bookkeeping is a finding.
	//
	// PVE-assigned MACs (PVE's random "hwaddr=...") are intentionally NOT
	// pinned in the manifest: they look like a "gap" to the operator, so
	// we surface them explicitly — but with a "review" note that says
	// "do not pin unless desired" rather than auto-failing adoption.
	var gaps []Gap
	for k := range raw {
		if PVEBookkeepingKeys[k] {
			continue
		}
		if VMKeysOwned[k] {
			continue
		}
		if VMKeyIsDynamic(k) {
			continue
		}
		gap := Gap{Kind: schema.KindVM, Node: node, ID: e.VMID, Field: k, Value: pveStr(raw[k]), Note: "live PVE config pveconform does not model for VMs; not represented in the generated manifest"}
		gaps = append(gaps, gap)
	}
	ac.res.Gaps = append(ac.res.Gaps, gaps...)
	return nil
}

// adoptLXC reads one LXC's /config and writes a single pveconform manifest
// under lxc/<cluster>/.
func (ac *adoptContext) adoptLXC(ctx context.Context, node string, e pveclient.LXCListEntry) error {
	cfg, gErr := ac.pve.LXC().Get(ctx, node, e.CID)
	if gErr != nil {
		return fmt.Errorf("adopt: LXC %s#%d /config: %w", node, e.CID, gErr)
	}
	raw := map[string]any{}
	for k, v := range cfg {
		raw[k] = v
	}

	lxc := schema.NewLXC()
	lxc.Metadata.Name = nameForPVE(pveStr(raw["hostname"]), e.CID, "ct")
	lxc.Spec.Node = node
	lxc.Spec.VMID = e.CID

	// memory + swap.
	if mem, ok := schema.PveMemoryToHuman(raw["memory"]); ok {
		lxc.Spec.Memory = mem
	}
	if sw, ok := schema.PveMemoryToHuman(raw["swap"]); ok {
		lxc.Spec.Swap = sw
	}
	// cpu.
	if cores := schema.PveCpuCores(raw); cores > 0 {
		lxc.Spec.CPU.Cores = cores
	}

	// rootfs + mount points: PVE's LXC /config report is "pool:<volid>" and,
	// when the volume was allocated with a documented size, carries an
	// explicit "size=<binary>" token (probe PVE 9.2: "rootfs:
	// local-lvm:vm-9200-disk-0,size=4G"). PveLXCDisksFromPVE parses both.
	// When the report lacks the size token (bare "pool:<volid>"), the size
	// is recovered from the node's storage content listing keyed by the
	// bare volid.
	root, mps, _, rootVolid, volidHasSize := schema.PveLXCDisksFromPVE(raw)
	var volumeSizes map[string]int64
	if !volidHasSize || len(mps) > 0 {
		// Fetch the storage listing only when a size is actually missing
		// from the report (mp slots or a bare rootfs).
		var vErr error
		volumeSizes, vErr = ac.nodeVolumeSizes(ctx, node)
		if vErr != nil {
			// Unreadable storage listing AND a size the report did not
			// provide: fail closed — a manifest with an unknown size would
			// risk data loss on the first apply.
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindLXC,
				Node:  node,
				ID:    e.CID,
				Field: "spec.root.size / spec.mount-points",
				Value: "storage listing unreadable",
				Note:  "PVE storage content listing could not be read; LXC volume sizes unknown — re-run adopt when storage is reachable",
			})
		}
	}
	sizeOf := func(bareVolid string) string {
		if volumeSizes == nil {
			return ""
		}
		b, okK := volumeSizes[bareVolid]
		if !okK {
			return ""
		}
		s, _ := schema.HumanFromBytes(b)
		return s
	}
	// The report's volid may carry a trailing ",size=..." — the storage
	// listing is keyed by the bare "pool:volid" prefix.
	bareVolidRoot := func() string {
		v := rootVolid
		if i := strings.IndexByte(v, ','); i >= 0 {
			v = v[:i]
		}
		return v
	}
	if root.Storage != "" {
		lxc.Spec.Root.Storage = root.Storage
		if volidHasSize && root.Size != "" {
			lxc.Spec.Root.Size = root.Size
		} else if s := sizeOf(bareVolidRoot()); s != "" {
			lxc.Spec.Root.Size = s
		} else {
			// Size genuinely unrecoverable: leave it empty + surface the
			// gap so the operator fills it after review. The generated
			// manifest will not Validate() until spec.root.size is set —
			// see Result.Incomplete.
			lxc.Spec.Root.Size = ""
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindLXC,
				Node:  node,
				ID:    e.CID,
				Field: "spec.root.size",
				Value: root.Storage,
				Note:  "LXC rootfs size could not be recovered from PVE's storage listing; fill spec.root.size after review",
			})
			ac.res.Incomplete = append(ac.res.Incomplete, fmt.Sprintf("lxc/%s/%s: spec.root.size unknown (storage listing had no matching volume)", node, lxc.Metadata.Name))
		}
	}
	for i := range mps {
		mp := &mps[i]
		if mp.Storage == "" {
			continue
		}
		slot := mp.Slot
		if slot == "" {
			slot = fmt.Sprintf("mp%d", i)
		}
		mp.Slot = slot
		// mp.Size was already parsed from the ",size=…" token when PVE's
		// /config report carried it; the storage-list lookup below covers
		// the bare "pool:volid" reports.
		if mp.Size == "" {
			bare := pveStr(raw[slot])
			if j := strings.IndexByte(bare, ','); j >= 0 {
				bare = bare[:j]
			}
			if s := sizeOf(bare); s != "" {
				mp.Size = s
			} else {
				ac.res.Gaps = append(ac.res.Gaps, Gap{
					Kind:  schema.KindLXC,
					Node:  node,
					ID:    e.CID,
					Field: "spec.mount-points[" + slot + "].size",
					Value: mp.Storage,
					Note:  "LXC mount-point size could not be recovered from PVE's /config report or its storage listing; fill in after review",
				})
				ac.res.Incomplete = append(ac.res.Incomplete, fmt.Sprintf("lxc/%s: spec.mount-points[%s].size unknown", lxc.Metadata.Name, slot))
			}
		}
	}
	lxc.Spec.MountPoints = mps

	// networks.
	if nets, ok := schema.PveLXCNetworksFromPVE(raw); ok {
		lxc.Spec.Networks = nets
	}

	// dns.
	if dns, ok := schema.PveLXCDNSFromPVE(raw); ok {
		lxc.Spec.DNS = dns
	}
	// arch.
	if arch := schema.PveLXCArchFromPVE(raw); arch != "" {
		lxc.Spec.Arch = arch
	}
	// options.
	lxc.Spec.Options = schema.PveLXCOptionsFromPVE(raw)
	// description.
	if desc := schema.PveLXCDescriptionFromPVE(raw); desc != "" {
		lxc.Spec.PveDescription = desc
	}
	// tags.
	if tags := pveStr(raw["tags"]); tags != "" {
		kept := []string{}
		for _, t := range strings.Split(tags, ",") {
			t = strings.TrimSpace(t)
			if t == "" || t == schema.PveOwnershipTag {
				continue
			}
			kept = append(kept, t)
		}
		lxc.Spec.Tags = kept
	}

	// template: resolve PVE's ostemplate against discovered vztmpl names.
	// NOTE: PVE's /lxc/{id}/config does NOT report ostemplate (it is a
	// create-time-only parameter the PVE storage layer consumed). When it is
	// absent, adopt CANNOT bind the LXC to a CTTemplate and records an
	// explicit gap so the operator fills spec.template after review (the
	// generated manifest stays INCOMPLETE until then: schema.Validate
	// requires the reference).
	if storage, filename, ok := schema.PveLXCTemplateFromPVE(raw); ok {
		voltmpl := storage + ":vztmpl/" + filename
		if tplName, known := ac.names.vzt[voltmpl]; known {
			lxc.Spec.Template = tplName
		} else {
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindLXC,
				Node:  node,
				ID:    e.CID,
				Field: "spec.template",
				Value: voltmpl,
				Note:  "PVE reports an ostemplate that has no pveconform CTTemplate manifest in this adopt pass; generate the CTTemplate first, then re-point spec.template to it",
			})
		}
	} else {
		ac.res.Gaps = append(ac.res.Gaps, Gap{
			Kind:  schema.KindLXC,
			Node:  node,
			ID:    e.CID,
			Field: "spec.template",
			Value: "unrecoverable (PVE /config does not report ostemplate)",
			Note:  "PVE's /lxc/{id}/config does not persist the ostemplate: after review, set spec.template to the CTTemplate this container was booted from",
		})
	}

	// state.
	lxc.Spec.State = pveStatusToState(e.Status)

	if wErr := ac.writeManifest(schema.KindLXC, lxc, lxc.Metadata.Name); wErr != nil {
		return wErr
	}

	// Gap detection for LXC. PVE-assigned MACs are intentionally NOT
	// pinned in the manifest; adopt surfaces them in a "review" gap.
	var gaps []Gap
	for k := range raw {
		if PVEBookkeepingKeys[k] {
			continue
		}
		if LXCKeysOwned[k] {
			continue
		}
		if LXCKeyIsDynamic(k) {
			continue
		}
		gap := Gap{Kind: schema.KindLXC, Node: node, ID: e.CID, Field: k, Value: pveStr(raw[k]), Note: "live PVE LXC config pveconform does not model; not represented in the generated manifest"}
		gaps = append(gaps, gap)
	}
	ac.res.Gaps = append(ac.res.Gaps, gaps...)
	return nil
}

// pveStatusToState maps PVE's live power state to pveconform's desired-state
// grammar ("started" | "stopped"). PVE reports "running", "stopped", "paused";
// any value not clearly "running" maps to "stopped" (fail-closed: a paused
// VM / LXC is not running, so pveconform will plan a start to converge).
func pveStatusToState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "running", "started":
		return "started"
	default:
		return "stopped"
	}
}

// pveStr converts a PVE /config value to a string for YAML emission / gap
// text. Mirrors the semantics the schema layer already uses: JSON arrays are
// comma-joined, nil is "", and numbers/stringify via %v.
func pveStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if arr, ok := v.([]any); ok {
		parts := make([]string, 0, len(arr))
		for _, e := range arr {
			parts = append(parts, pveStr(e))
		}
		return strings.Join(parts, ",")
	}
	return fmt.Sprintf("%v", v)
}
