package adopt

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// This file converts PVE /qemu/{id}/config and /lxc/{cid}/config reports into
// proxops VM / LXC manifests. The conversion covers only the fields
// proxops actually models (the "owned-field" surface of the schema). Every
// PVE key that appears in the /config report but is not (a) owned by
// proxops or (b) PVE bookkeeping (digest, meta, ...) is surfaced on
// Result.Gaps so operators see exactly what is not represented in the
// generated YAML.

// VMKeysOwned lists every PVE /config key that proxops's VM schema models
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
	"ide2":        true, // cloud-init OR cdrom (proxops's 3-state)
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
// proxops owns: scsiN / virtioN / sataN / netN. The "ide" slots are
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

// LXCKeysOwned lists every PVE /lxc/{id}/config key that proxops's LXC
// schema models. mpN (dynamic) and netN are handled by LXCKeyIsDynamic.
//
// M10 additions (prod-a live-verified):
//   - `console`: PVE 9.x top-level LXC console enable, captured in
//     LXCOptions.Console (tri-state *bool).
//   - `features`: PVE 9.x composite token ("features=nesting=1,..."); the
//     `nesting` value inside is captured in LXCOptions.Nesting. The legacy
//     top-level `nesting` key is also accepted.
var LXCKeysOwned = map[string]bool{
	"cores":         true,
	"memory":        true,
	"swap":          true,
	"arch":          true,
	"hostname":      true,
	"nameserver":    true,
	"searchdomain":  true,
	"search-domain": true,
	"description":   true,
	"tags":          true,
	"rootfs":        true,
	"unprivileged":  true,
	"protection":    true,
	"nesting":       true,
	"keyctl":        true,
	"fuse":          true,
	"onboot":        true,
	"startup":       true,
	"console":       true,
	"features":      true,
	// note: `ostype` stays in PVEBookkeepingKeys (PVE-inferred from the
	// ostemplate; never a create form-value, and never a gap).
}

// LXCKeyIsDynamic reports whether a PVE LXC /config key is a dynamic device
// slot proxops owns: mpN / netN.
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
	if !strings.HasPrefix(k, "net") || len(k) == 3 {
		return false
	}
	for _, c := range k[3:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// adoptVM reads one VM's /config and writes a single proxops manifest
// under vm/<cluster>/. Returns nil on success; an error when PVE's /config
// could not be read.
//
// PVE template VMs (raw "template" == 1) are NEVER adopted as manifests:
// proxops has no template-VM resource kind and the VM's disks are the
// clone source data that a proxops-managed manifest would wrongly
// claim ownership of. The template VM is recorded on Result.Skipped so the
// fleet census stays complete.
func (ac *adoptContext) adoptVM(ctx context.Context, node string, e pveclient.VMListEntry) error {
	cfg, gErr := ac.pve.VM().Get(ctx, node, e.VMID)
	if gErr != nil {
		return fmt.Errorf("adopt: VM %s#%d /config: %w", node, e.VMID, gErr)
	}
	raw := map[string]any{}
	for k, v := range cfg {
		raw[k] = v
	}

	// M11: PVE reports a template (template=1); reverse-translate it to
	// an adoption of the proxops TemplateVM kind. See M10 Skipped
	// census notes in docs/GAPS.md: M11 REPLACES the M10 "skip + record
	// SkippedObject" behavior by producing a first-class manifest.
	if pveTemplateValue(raw["template"]) {
		return ac.adoptTemplateVM(ctx, node, e, raw)
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

	// disks. PVE's cloud-init volume on non-IDE slots (probe-verified on
	// prod-a: "vm-999-cloudinit,media=cdrom" on scsi1 on every VM
	// with cloud-init configured via qm) is a PVE-managed cdrom that
	// proxops does not own in its schema (cloud-init attaches to
	// ide2/ide3 only). Excluding it from spec.disks keeps the manifest
	// valid without pretending proxops owns a volume it cannot
	// recreate. The exclusion is recorded below if PVE reported one.
	rawDisks, ok := schema.PveDisksFromPVE(raw)
	if ok {
		kept := rawDisks[:0]
		for _, d := range rawDisks {
			if schema.PveDiskMedia(vm.Spec.VMID, raw, d.Slot) == "cdrom" {
				// Recorded in gaps below.
				continue
			}
			kept = append(kept, d)
		}
		vm.Spec.Disks = kept
	}
	for _, d := range rawDisks {
		if schema.PveDiskMedia(vm.Spec.VMID, raw, d.Slot) == "cdrom" {
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindVM,
				Node:  node,
				ID:    e.VMID,
				Field: d.Slot,
				Value: pveStr(raw[d.Slot]),
				Note:  "PVE reports a cloud-init / cdrom-style volume on a proxops non-owned slot (" + d.Slot + "). proxops does not own non-IDE cdrom slots; this disk is excluded from spec.disks. Review whether it matters for your workload — if PVE's cloud-init is on IDE (ide2/ide3), it is owned via spec.hardware.cloud-init and no action is needed.",
			})
		}
	}

	// networks.
	if nics, ok := schema.PveNICsFromPVE(raw); ok {
		vm.Spec.NICs = nics
	}

	// hardware.
	hw := schema.PveVMHardwareFromPVE(raw)
	// cdrom: resolved against artifact names so the reference is a
	// proxops ISO metadata.name (the M8 composition cross-reference).
	// An empty volid means PVE reports an explicit detach
	// ("ide2 = none" form) → proxops's 3-state iso: none.
	if isoVolid, cdromOK := schema.PveCDROMFromPVE(raw); cdromOK {
		if isoVolid == "" {
			hw.Cdrom = schema.CDDrive{Iso: schema.CDROMNone}
		} else if isoName, known := ac.names.iso[isoVolid]; known {
			hw.Cdrom.Iso = isoName
		} else {
			// PVE reports a cdrom that references an ISO with no proxops
			// manifest in this compose set: surface it as a gap so the
			// operator adds the ISO first.
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindVM,
				Node:  node,
				ID:    e.VMID,
				Field: "ide2/ide3",
				Value: isoVolid,
				Note:  "PVE reports a cdrom that references an ISO with no proxops manifest in this adopt pass; generate the ISO first, then re-point spec.hardware.cdrom.iso to it",
			})
		}
	}
	vm.Spec.Hardware = hw

	// options.
	vm.Spec.Options = schema.PveVMOptionsFromPVE(raw)

	// tags: strip proxops's ownership tag (the schema re-appends it).
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

	// M11: PVE-side cloud-init data fields. PVE reports ciuser,

	// ipconfig<N>, nameserver, searchdomain + sshkeys on any VM that

	// has ever had cloud-init configured. proxops adopts them:

	//   - ciuser / nameserver / searchdomain / ipconfig<N>: as-is.

	//   - sshkeys: M13.2. Adopted via SOPS-matched cloud-init.ssh-keys.<name>
	//     references when the live material matches the cluster's SOPS
	//     document; otherwise "PVE-owned" sentinel (see ac.cloudInitDataAdopt).

	//   - cipassword / cicustom / ciupgrade: NOT adopted. PVE 9.2 masks
	//     cipassword on readback (M13.2 probe: plaintext unrecoverable);
	//     census-only.
	vm.Spec.CloudInitData = ac.cloudInitDataAdopt(raw)

	if wErr := ac.writeManifest(schema.KindVM, vm, vm.Metadata.Name); wErr != nil {
		return wErr
	}

	// Gap detection: every PVE /config key proxops does not model AND
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
		// M13.2: hostpciN keys are owned by the VM hardware when they
		// parse as BDF+pcie (PvePCIDevicesFromPVE already extracted them
		// into spec.hardware.pci-devices). PVE also reports non-parsable
		// hostpciN values (x-vga-only without a BDF, rejected mdev/pool
		// forms, ...) — those stay in the gap report so the operator sees
		// an unmodelled PVE-side PCI slot rather than a silent adoption.
		if schema.PvePCIKeyIsOwned(k) {
			if _, ok := schema.PvePCIDeviceFromPVE(k, raw[k]); ok {
				continue
			}
			// non-parsable -> fall through to gap.
		}
		// M11: cloud-init data keys owned by proxops (ciuser, sshkeys,
		// nameserver, searchdomain, ipconfig*) are NOT gaps; they are
		// represented in the manifest under spec.cloud-init-data.
		if isM11OwnedCloudInitDataKey(k) {
			continue
		}
		gap := Gap{Kind: schema.KindVM, Node: node, ID: e.VMID, Field: k, Value: redactGapValue(k, pveStr(raw[k])), Note: gapNoteFor(k, "live PVE config proxops does not model for VMs; not represented in the generated manifest")}
		gaps = append(gaps, gap)
	}
	ac.res.Gaps = append(ac.res.Gaps, gaps...)
	return nil
}

// adoptLXC reads one LXC's /config and writes a single proxops manifest
// under lxc/<cluster>/. A PVE CT reporting template=1 is routed to
// adoptTemplateCT (M13) and produces a templatect/<cluster>/ manifest.
func (ac *adoptContext) adoptLXC(ctx context.Context, node string, e pveclient.LXCListEntry) error {
	cfg, gErr := ac.pve.LXC().Get(ctx, node, e.CID)
	if gErr != nil {
		return fmt.Errorf("adopt: LXC %s#%d /config: %w", node, e.CID, gErr)
	}
	raw := map[string]any{}
	for k, v := range cfg {
		raw[k] = v
	}

	// M13: a PVE-promoted CT (template=1) reverse-translates to the
	// TemplateCT kind, mirroring adoptVM's TemplateVM routing (M11).
	if pveTemplateValue(raw["template"]) {
		return ac.adoptTemplateCT(ctx, node, e, raw)
	}
	return ac.writeLXCManifest(ctx, node, e, raw, schema.KindLXC)
}

// adoptTemplateCT writes the TemplateCT manifest for a promoted CT (M13).
// The owned-field surface is identical to an LXC (TemplateCT embeds LXC);
// the manifest is written under templatect/<cluster>/ and carries the
// template=1 gap note (PVE does not persist the ostemplate a CT was
// created from, so spec.template needs a human — same contract as LXC).
func (ac *adoptContext) adoptTemplateCT(ctx context.Context, node string, e pveclient.LXCListEntry, raw map[string]any) error {
	return ac.writeLXCManifest(ctx, node, e, raw, schema.KindTemplateCT)
}

// writeLXCManifest is the shared reverse-translation body for kind=LXC and
// kind=TemplateCT (M13). kind selects the manifest root + gap attribution;
// everything else (owned fields, bind mounts, gaps) is identical because
// TemplateCT embeds LXC on the wire.
func (ac *adoptContext) writeLXCManifest(ctx context.Context, node string, e pveclient.LXCListEntry, raw map[string]any, kind schema.Kind) error {

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
				Kind:  kind,
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
				Kind:  kind,
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
					Kind:  kind,
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
	// Bind mounts (M13): PVE reports host-path bind mp's ("mpN=<host>,mp=
	// <guest>" or the legacy "mpN=<host>:<guest>"). These are now a
	// first-class declarative shape (spec.bind-mounts), so adopt writes them
	// into the manifest. A bind whose host path is a system-critical
	// directory (which proxops's Validate refuses to declare) is surfaced as
	// a gap instead, so the operator reconciles it deliberately.
	for _, bm := range schema.PveLXCBindMountsFromPVE(raw) {
		b := schema.LXCBinding{Slot: bm.Slot, HostPath: bm.HostPath, MountPoint: bm.MountPoint, ReadOnly: bm.ReadOnly}
		if b.HostPath == "" || !strings.HasPrefix(b.HostPath, "/") || b.MountPoint == "" || !strings.HasPrefix(b.MountPoint, "/") {
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  kind,
				Node:  node,
				ID:    e.CID,
				Field: bm.Slot,
				Value: redactGapValue(bm.Slot, bm.Raw),
				Note:  "LXC mount-point is a host-path bind mount in a shape proxops could not parse into spec.bind-mounts (host + guest path both required); reconcile on PVE after review",
			})
			continue
		}
		lxc.Spec.BindMounts = append(lxc.Spec.BindMounts, b)
	}

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
				Kind:  kind,
				Node:  node,
				ID:    e.CID,
				Field: "spec.template",
				Value: voltmpl,
				Note:  "PVE reports an ostemplate that has no proxops CTTemplate manifest in this adopt pass; generate the CTTemplate first, then re-point spec.template to it",
			})
		}
	} else {
		ac.res.Gaps = append(ac.res.Gaps, Gap{
			Kind:  kind,
			Node:  node,
			ID:    e.CID,
			Field: "spec.template",
			Value: "unrecoverable (PVE /config does not report ostemplate)",
			Note:  "PVE's /lxc/{id}/config does not persist the ostemplate: after review, set spec.template to the CTTemplate this container was booted from",
		})
	}

	// state.
	lxc.Spec.State = pveStatusToState(e.Status)

	// Write the manifest under the routed kind root. A TemplateCT shares the
	// LXC wire surface, so the same populated struct is wrapped in the
	// TemplateCT resource (embedded LXC) before marshalling.
	var doc any = lxc
	if kind == schema.KindTemplateCT {
		t := schema.NewTemplateCT()
		t.LXC = *lxc
		t.Kind = schema.KindTemplateCT
		doc = t
	}
	if wErr := ac.writeManifest(kind, doc, lxc.Metadata.Name); wErr != nil {
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
		// M13: "template" = "1" is owned by the TemplateCT kind itself (the
		// manifest's kind IS the template flag), so it is not a gap there.
		if k == "template" && kind == schema.KindTemplateCT {
			continue
		}
		gap := Gap{Kind: kind, Node: node, ID: e.CID, Field: k, Value: redactGapValue(k, pveStr(raw[k])), Note: gapNoteFor(k, "live PVE LXC config proxops does not model; not represented in the generated manifest")}
		gaps = append(gaps, gap)
	}
	ac.res.Gaps = append(ac.res.Gaps, gaps...)
	return nil
}

// pveStatusToState maps PVE's live power state to proxops's desired-state
// grammar ("started" | "stopped"). PVE reports "running", "stopped", "paused";
// any value not clearly "running" maps to "stopped" (fail-closed: a paused
// VM / LXC is not running, so proxops will plan a start to converge).
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

// adoptTemplateVM (M11): a PVE-templated qemu object is reverse-translated
// into a proxops TemplateVM manifest. This REPLACES M10's "skip + record
// in Skipped" behavior: proxops now owns the template lifecycle
// (create + mark, config drift, ownership-tag prunes) and the manifest
// captures the same owned-field surface as a regular proxops VM,
// PLUS M11's cloud-init data fields.
//
// PVE-side sshkeys are redacted: PveCISshKeysFromPVE returns a single
// {"*"} sentinel, so the emitted manifest's ssh-keys entry is ["*"] —
// the operator replaces it with real public key(s) before first apply.
// PVE-side cipassword and cicustom are NOT adopted (secret and
// PVE-side-only respectively); they stay in the gap report.
//
// The manifest's spec.state is always "stopped" (PVE refuses to start a
// template; DesiredState() returns "stopped" unconditionally — see
// schema.TemplateVM).
func (ac *adoptContext) adoptTemplateVM(ctx context.Context, node string, e pveclient.VMListEntry, raw map[string]any) error {
	// M11: PVE-templated object. Emit a proxops TemplateVM manifest
	// (not a VM manifest) so the parse layer routes through the TemplateVM
	// kind + plan/exec use kind=TemplateVM.
	t := schema.NewTemplateVM()
	t.Metadata.Name = nameForPVE(pveStr(raw["name"]), e.VMID, "vm")
	t.Spec.Node = node
	t.Spec.VMID = e.VMID

	// memory.
	if mem, ok := schema.PveMemoryToHuman(raw["memory"]); ok {
		t.Spec.Memory = mem
	}
	// cpu.
	if cpuType := schema.PveCpuType(raw); cpuType != "" {
		t.Spec.CPU.Type = cpuType
	}
	cores := schema.PveCpuCores(raw)
	if cores > 0 {
		t.Spec.CPU.Cores = cores
	}
	// disks: exclude PVE-side cloud-init / cdrom-style volumes on
	// data buses (same rule adoptVM uses; PVE owns the non-IDE
	// cloud-init CDROM that qm cloud-init sets on the template's own
	// data-bus slot, and proxops cannot recreate it — see M10 GAP entry
	// "VM: cloud-init volume on a non-IDE slot is PVE-owned").
	rawDisks, ok := schema.PveDisksFromPVE(raw)
	if ok {
		kept := rawDisks[:0]
		for _, d := range rawDisks {
			if schema.PveDiskMedia(t.Spec.VMID, raw, d.Slot) == "cdrom" {
				// Recorded in gaps below (the gap note names the slot).
				continue
			}
			kept = append(kept, d)
		}
		t.Spec.Disks = kept
	}
	for _, d := range rawDisks {
		if schema.PveDiskMedia(t.Spec.VMID, raw, d.Slot) == "cdrom" {
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindTemplateVM,
				Node:  node,
				ID:    e.VMID,
				Field: d.Slot,
				Value: pveStr(raw[d.Slot]),
				Note:  "PVE reports a cloud-init / cdrom-style volume on a data-bus slot (" + d.Slot + "). proxops does not own non-IDE cloud-init slots; this disk is excluded from spec.disks. Review whether it matters for your workload — if PVE's cloud-init is on IDE (ide2/ide3), it is owned via spec.hardware.cloud-init and no action is needed.",
			})
		}
	}
	// networks.
	if nics, ok := schema.PveNICsFromPVE(raw); ok {
		t.Spec.NICs = nics
	}
	// hardware.
	hw := schema.PveVMHardwareFromPVE(raw)
	if isoVolid, cdromOK := schema.PveCDROMFromPVE(raw); cdromOK {
		if isoVolid == "" {
			hw.Cdrom = schema.CDDrive{Iso: schema.CDROMNone}
		} else if isoName, known := ac.names.iso[isoVolid]; known {
			hw.Cdrom.Iso = isoName
		} else {
			ac.res.Gaps = append(ac.res.Gaps, Gap{
				Kind:  schema.KindTemplateVM,
				Node:  node,
				ID:    e.VMID,
				Field: "ide2/ide3",
				Value: isoVolid,
				Note:  "PVE reports a cdrom that references an ISO with no proxops manifest in this adopt pass; generate the ISO first, then re-point spec.hardware.cdrom.iso to it",
			})
		}
	}
	t.Spec.Hardware = hw
	// options.
	t.Spec.Options = schema.PveVMOptionsFromPVE(raw)
	// tags: strip proxops's ownership tag (the schema re-appends it).
	if tags := pveStr(raw["tags"]); tags != "" {
		kept := []string{}
		for _, tk := range strings.Split(tags, ",") {
			tk = strings.TrimSpace(tk)
			if tk == "" || tk == schema.PveOwnershipTag {
				continue
			}
			kept = append(kept, tk)
		}
		t.Spec.Tags = kept
	}
	// M11 + M13.2: cloud-init data fields. ssh-keys are SOPS-matched
	// (or sentinel when the SOPS document is empty / no matching
	// existing ref + --adopt-secrets off); cipassword is census-only
	// (PVE 9.2 masks it on readback).
	t.Spec.CloudInitData = ac.cloudInitDataAdopt(raw)
	// state: always "stopped" for a PVE template (PVE refuses to start).
	t.Spec.State = "stopped"

	if wErr := ac.writeManifest(schema.KindTemplateVM, t, t.Metadata.Name); wErr != nil {
		return wErr
	}

	// Gap detection: every PVE /config key proxops does not model AND
	// is not PVE bookkeeping. "template" = "1" is explicitly owned by the
	// proxops TemplateVM kind (this function is only called when PVE
	// reports template=1), so it is NOT a gap. Cloud-init data keys owned
	// by M11 (ciuser, sshkeys, nameserver, searchdomain, ipconfig*<N>)
	// are also not gaps. cipassword / cicustom / ciupgrade are M11's
	// explicit non-modelled fields.
	var gaps []Gap
	for k := range raw {
		if PVEBookkeepingKeys[k] {
			continue
		}
		if VMKeysOwned[k] {
			continue
		}
		if k == "template" {
			// M11: proxops's TemplateVM kind IS the PVE-side template
			// flag. Owned.
			continue
		}
		if isM11OwnedCloudInitDataKey(k) {
			// M11: proxops's cloud-init data surface owns these keys.
			// (sshkeys IS adopted, via the "*" sentinel; cipassword /
			// cicustom / ciupgrade are M10 PII / PVE-side fields,
			// deliberately not modelled in M11.)
			continue
		}
		if VMKeyIsDynamic(k) {
			continue
		}
		// M13.2: hostpciN keys on a TemplateVM are owned by the
		// embedded VM surface when they parse; non-parsable PVE-side
		// forms stay gaps (same rule as ordinary VM).
		if schema.PvePCIKeyIsOwned(k) {
			if _, ok := schema.PvePCIDeviceFromPVE(k, raw[k]); ok {
				continue
			}
		}
		gap := Gap{Kind: schema.KindTemplateVM, Node: node, ID: e.VMID, Field: k, Value: redactGapValue(k, pveStr(raw[k])), Note: gapNoteFor(k, "live PVE config proxops does not model for TemplateVMs; not represented in the generated manifest")}
		gaps = append(gaps, gap)
	}
	ac.res.Gaps = append(ac.res.Gaps, gaps...)
	return nil
}

// isM11OwnedCloudInitDataKey reports whether a PVE /config key is in M11's
// owned cloud-init data surface: ciuser, sshkeys (M13.2: now SOPS-referenced
// or sentinel), nameserver, searchdomain, ipconfig<N>. cipassword, cicustom,
// ciupgrade are NOT in this set — cipassword remains a redacted gap with an
// M13.2 note (PVE masks it on readback; the census in Result.CloudInit is the
// M13.2 operator signal), cicustom/ciupgrade are PVE-side-only.
func isM11OwnedCloudInitDataKey(k string) bool {
	switch k {
	case "ciuser", "sshkeys", "nameserver", "searchdomain":
		return true
	}
	return strings.HasPrefix(k, "ipconfig")
}

// M13.2: cloud-init data adoption with SOPS ssh-keys matching.
//
// Semantics (VM + TemplateVM):
//   - ciuser / nameservers / search-domains / ipconfigs: adopted as-is
//     (M11 behaviour, inherited from PveCloudInitDataFromPVE).
//   - sshkeys: M13.2 SOPS-matching.
//     * For every live ssh-key line, look up an EXISTING SOPS name whose
//       key material matches exactly (byte-for-byte line equality):
//       emit `cloud-init.ssh-keys.<existing-name>`.
//     * If opts.AdoptSecrets is true and no existing match exists, assign
//       a deterministic new SOPS name (`adopted-<fingerprint>` — see
//       schema.SSHKeyFingerprint) and record it on Result.NewSSHKeys
//       (for the CLI to write into the encrypted SOPS document) AND on
//       Result.AdoptedSSHRefs; the manifest's ssh-key-refs entry carries
//       the NEW name so a subsequent plain-run adopt reuses it.
//     * Otherwise (no match + --adopt-secrets not set) → fall back to the
//       M10 redacted-sentinel ssh-keys: ["*"] (PVE-owned, never written).
//   - cipassword: census only. PVE 9.2 masks it on readback (M13.2
//     probe-verified: '**********', plaintext unrecoverable). No manifest
//     field; the gap report carries a "manual ci-password-ref mapping"
//     note + the Result.CloudInit census counts the resource.
//
// Determinism: new-name assignment is keyed by the SSHKeyFingerprint so
// two resources sharing the same key land on the SAME SOPS entry regardless
// of order. The fingerprint is 16 lowercase hex chars of
// sha256("sshpki\x00<type>\x00<blob>") — stable across adopt runs AND
// stable when the operator renames the key later (the user can rename the
// SOPS key freely; the manifest's ref will be the NEW name after they re-adopt,
// but the PVE material is still recoverable via the old key until they
// re-adopt; that is a manual step, documented in sops-credentials.md).
func (ac *adoptContext) cloudInitDataAdopt(raw map[string]any) schema.CloudInitData {
	cd := schema.PveCloudInitDataFromPVE(raw)
	// M10 sentinel was filled by PveCloudInitDataFromPVE when PVE carried
	// non-empty sshkeys; M13.2 now refines that: replace the sentinel with
	// SOPS refs whenever EVERY live key line resolves to one (existing SOPS
	// name match, or a freshly generated "adopted-<fingerprint>" name under
	// --adopt-secrets). The all-or-nothing rule is what makes this safe:
	// schema.Validate rejects mixing ssh-keys + ssh-key-refs, so a partially
	// matched resource keeps the M10 ["*"] sentinel (PVE-owned, no loss)
	// rather than dropping unadopted keys. The store's SSHKeys map may be
	// EMPTY: matchOrAdoptLines still imports under --adopt-secrets.
	if len(cd.SSHKeys) > 0 {
		lines, present := schema.PveCISshLinesFromPVE(raw)
		if present {
			ac.sshConfigured++
			// Live-key census, independent of whether matching resolved the
			// lines to SOPS refs: "N unique live keys across M resources"
			// describes PVE material, not the SOPS store.
			ac.recordLiveSSHKeys(lines)
			refs, allMatched := ac.matchOrAdoptLines(lines)
			if allMatched {
				cd.SSHKeys = nil
				cd.SSHKeyRefs = refs
				ac.sshRefResources++
			}
			// else: keep the sentinel (M10 shape)
		}
	}
	if schema.PveCIPasswordPresent(raw) {
		ac.recordPasswordPresent()
	}
	return cd
}

// matchOrAdoptLines maps live ssh-key lines to SOPS refs. Returns (refs,
// allMatched). When opts.AdoptSecrets is false, every line MUST already
// have an existing SOPS name or allMatched=false (sentinel fallback).
// When opts.AdoptSecrets is true, lines without an existing name are
// assigned deterministic "adopted-<fingerprint>" names into ac.newNames.
//
// The census is committed ONLY on the allMatched=true path: a failed match
// MUST NOT pollute Result.CloudInit with SOPS names the resource did not
// actually reference (that would mislead operators about their SOPS store).
func (ac *adoptContext) matchOrAdoptLines(lines []string) (refs []string, allMatched bool) {
	// Build the existing SOPS name -> line map for matching.
	existingByLine := map[string]string{}
	for name, line := range ac.opts.SOPS.SSHKeys {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		existingByLine[line] = name
	}

	localUnique := map[string]bool{}
	localReused := map[string]bool{}
	localNew := map[string]string{}   // adopted name -> key line (for ac.newNames)
	seen := map[string]bool{}
	refs = make([]string, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == schema.CloudInitRedactedSentinel {
			// A sentinel in LIVE PVE material should NOT happen (PVE does
			// not store "*" as a key); treat as unresolvable → fall back
			// to sentinel for the WHOLE resource (no partial census).
			return nil, false
		}
		if name, existing := existingByLine[line]; existing {
			localUnique[name] = true
			localReused[name] = true
			if !seen[name] {
				seen[name] = true
				refs = append(refs, schema.CloudInitSSHRefPrefix+name)
			}
			continue
		}
		// No existing SOPS entry for this material.
		if !ac.opts.AdoptSecrets {
			// Without --adopt-secrets, partial adoption is not allowed
			// (schema rejects mixing refs + sentinel). Fall back to the
			// all-keys sentinel. Census stays empty (no refs emitted).
			return nil, false
		}
		// --adopt-secrets: deterministic new name = adopted-<fingerprint>.
		//
		// Fingerprint is derived from the OpenSSH key line (type + base64
		// body; comment is intentionally EXCLUDED so "the same key under a
		// different comment" is still deduped to one SOPS entry).
		fp := schema.SSHKeyFingerprint(line)
		if fp == "" {
			// line is not a parseable OpenSSH key → fail closed: leave the
			// resource on the sentinel; census stays empty.
			return nil, false
		}
		name := "adopted-" + fp
		localUnique[name] = true
		localNew[name] = line
		if !seen[name] {
			seen[name] = true
			refs = append(refs, schema.CloudInitSSHRefPrefix+name)
		}
	}

	// All lines resolved: commit to the context census + import map.
	for k := range localUnique {
		ac.sshUniqueNames[k] = true
	}
	for k := range localReused {
		ac.sshReusedNames[k] = true
	}
	for k, v := range localNew {
		// Belt-and-braces: adopt may be called once per run but the same
		// fingerprint name could recur from a prior resource in the same
		// run; keep the existing entry (first-writer wins; the lines are
		// deduped by fingerprint anyway).
		if _, existed := ac.newNames[k]; !existed {
			ac.newNames[k] = v
		}
	}
	return refs, true
}

func (ac *adoptContext) recordPasswordPresent() {
	ac.pwConfigured++
}

// collectCensus finalises Result.CloudInit + Result.AdoptedSSHRefs at the
// tail of runWithOptions. Called ONCE per run.
//
// Field semantics (M13.2 UX cleanup):
//   - SSHUniqueLiveKeys: distinct LIVE PVE ssh-key lines observed in this run,
//     deduped by SSHKeyFingerprint (type + base64 body; the OpenSSH comment
//     is NOT part of the identity — matching PVE's storage semantics, so
//     "the same key under two comments" still counts once). This is the "N
//     unique across M resources" number operators expect to see even in a
//     plain SOPS-less adoption: it describes PVE material, not the SOPS store.
//   - SSHReused: distinct SOPS names already present in the store that the
//     live material matched (0 unless SOPS matching produced refs).
//   - SSHAdded: distinct SOPS names newly created by this run (--adopt-secrets
//     only).
//   - SSHResources: resources that had live ssh-keys.
//   - PwResources: resources that had a live cipassword value (PVE 9.2 masks
//     the value; presence is the only signal — the plaintext is NOT
//     recoverable, manual ci-password-ref mapping required).
//   - SOPSBacked: whether the cluster carried a decrypted SOPS store this run
//     (SOPS.Loaded=true). Independent of whether the ssh-keys / passwords
//     blocks are populated: a valid but initially-empty SOPS document is still
//     SOPS-backed — the correct operator instruction is "a store is configured;
//     re-run with --adopt-secrets to import unmatched keys", not "cluster not
//     SOPS-backed".
func (ac *adoptContext) collectCensus() {
	ac.res.CloudInit = CloudInitSummary{
		AdoptSecrets:         ac.opts.AdoptSecrets,
		SSHUniqueLiveKeys:    len(ac.liveKeyFPs),
		SSHReused:            len(ac.sshReusedNames),
		SSHAdded:             len(ac.newNames),
		SSHResources:         ac.sshConfigured,
		SSHResourcesWithRefs: ac.sshRefResources,
		PwResources:          ac.pwConfigured,
		SOPSBacked:           ac.opts.SOPS.Loaded,
	}
	ac.res.NewSSHKeys = ac.newNames
	// AdoptedSSHRefs = every SOPS name this run actually referenced on a
	// manifest (existing AND adopted), sorted. NOT just new ones: that is
	// what Result.NewSSHKeys is for.
	for k := range ac.sshUniqueNames {
		ac.res.AdoptedSSHRefs = append(ac.res.AdoptedSSHRefs, k)
	}
	sort.Strings(ac.res.AdoptedSSHRefs)
}

// recordLiveSSHKeys commits the live-key census for one resource. Distinct
// lines are deduped by schema.SSHKeyFingerprint (type + base64 body; the
// OpenSSH comment is NOT part of the identity — matching PVE's own storage
// semantics, so the same key under two comments counts once). A line PVE
// reported that is not a parseable OpenSSH key (fingerprint "") is deduped
// verbatim under "raw:<line>" so distinct unparseable lines still count as
// distinct live material; matching fails closed for them regardless. This runs
// whether or not SOPS matching resolved the lines: the "N unique live keys
// across M resources" number describes PVE material, not the SOPS store.
func (ac *adoptContext) recordLiveSSHKeys(lines []string) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == schema.CloudInitRedactedSentinel {
			continue
		}
		fp := schema.SSHKeyFingerprint(line)
		if fp == "" {
			fp = "raw:" + line
		}
		ac.liveKeyFPs[fp] = true
	}
}
