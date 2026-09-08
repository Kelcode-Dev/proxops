// M6 schema coverage for the artifact-resolver, VM hardware / options,
// LXC network + storage + DNS, ISO / CTTemplate multi-node placement.
//
// These tests exercise only the schema layer — the same production code
// paths that parse.go (BuildIndex) and plan.go (PlanActions) consume. A
// separate planner-level test (plan_m6_test.go) exercises the full
// BuildIndex → Levels → PlanActions chain.
package schema_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// cttForTests returns a resolvable CTTemplate manifest string.
func cttForTests(name, storage, filename, url string) string {
	return "apiVersion: " + schema.APIVersion + `
kind: CTTemplate
metadata:
  name: ` + name + `
spec:
  nodes: [pve01]
  storage: ` + storage + `
  filename: ` + filename + `
  url: ` + url + `
`
}

// isoForTests returns a resolvable ISO manifest string.
func isoForTests(name, storage, filename, url string) string {
	return "apiVersion: " + schema.APIVersion + `
kind: ISO
metadata:
  name: ` + name + `
spec:
  nodes: [pve01]
  storage: ` + storage + `
  filename: ` + filename + `
  url: ` + url + `
`
}

// TestLXCTemplateResolution: an LXC manifest referencing a CTTemplate must,
// after ResolveArtifactRefs, carry the resolved PVE ostemplate volume
// ("local:vztmpl/<filename>") in its create params.
func TestLXCTemplateResolution(t *testing.T) {
	// build the CTT first; keep it alongside
	cttRaw := cttForTests("debian-13", "local", "debian-13-standard_13.6.1-1_amd64.tar.zst",
		"https://example.com/debian-13.tar.zst")
	lxcRaw := `apiVersion: ` + schema.APIVersion + `
kind: LXC
metadata:
  name: lxc-1
spec:
  node: pve01
  vmid: 9100
  memory: 1GiB
  cpu: {cores: 1}
  template: debian-13
  root: {storage: local, size: 4GiB}
`
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo LXC: %v", err)
	}
	ctt := schema.NewCTTemplate()
	if err := schema.YAMLTo(cttRaw, ctt); err != nil {
		t.Fatalf("YAMLTo CTT: %v", err)
	}
	// Pre-validate both (so LXC Dips resolution happens against a valid CTT).
	if err := ctt.Validate(); err != nil {
		t.Fatalf("CTT validate: %v", err)
	}
	if err := lxc.Validate(); err != nil {
		t.Fatalf("LXC validate: %v", err)
	}
	// resolve artifact refs — the lxc's spec.template points at ctt's ref.
	if err := schema.ResolveArtifactRefs([]schema.Resource{ctt, lxc}); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}
	// Now the LXC should have a resolved "ostemplate" in create params.
	p, err := lxc.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	want := "local:vztmpl/debian-13-standard_13.6.1-1_amd64.tar.zst"
	if got, _ := p["ostemplate"].(string); got != want {
		t.Errorf("ostemplate = %q, want %q", got, want)
	}
}

// TestLXCTemplateUnknownFails: an LXC that references a CTTemplate that does
// not exist in the same resource set must fail resolution (fail-closed).
func TestLXCTemplateUnknownFails(t *testing.T) {
	lxcRaw := `apiVersion: ` + schema.APIVersion + `
kind: LXC
metadata:
  name: lxc-1
spec:
  node: pve01
  vmid: 9100
  memory: 1GiB
  cpu: {cores: 1}
  template: ghost
  root: {storage: local, size: 4GiB}
`
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo LXC: %v", err)
	}
	if err := schema.ResolveArtifactRefs([]schema.Resource{lxc}); err == nil {
		t.Fatalf("expected ResolveArtifactRefs to fail for unknown template ref")
	}
}

// TestVMCdromISORefFailsClosed: a VM referencing an ISO that is not in the
// resource set must fail resolution.
func TestVMCdromISORefFailsClosed(t *testing.T) {
	vmRaw := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: vm-1
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  hardware:
    cdrom:
      iso: missing-iso
`
	vm := schema.NewVM()
	if err := schema.YAMLTo(vmRaw, vm); err != nil {
		t.Fatalf("YAMLTo VM: %v", err)
	}
	if err := schema.ResolveArtifactRefs([]schema.Resource{vm}); err == nil {
		t.Fatalf("expected ResolveArtifactRefs to fail for missing ISO ref")
	}
}

// TestVMCdromISOResolution: a VM with a resolvable ISO ref carries the
// PVE cdrom wire value after ResolveArtifactRefs + ToCreateParams.
func TestVMCdromISOResolution(t *testing.T) {
	isoRaw := isoForTests("boot-iso", "local", "boot.iso", "https://example.com/boot.iso")
	vmRaw := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: vm-1
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  hardware:
    cdrom:
      iso: boot-iso
`
	iso := schema.NewISO()
	if err := schema.YAMLTo(isoRaw, iso); err != nil {
		t.Fatalf("YAMLTo ISO: %v", err)
	}
	vm := schema.NewVM()
	if err := schema.YAMLTo(vmRaw, vm); err != nil {
		t.Fatalf("YAMLTo VM: %v", err)
	}
	if err := schema.ResolveArtifactRefs([]schema.Resource{iso, vm}); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}
	// After resolution the VM's CdromWireValue should be "local:iso/boot.iso,
	// media=cdrom" (no cloud-init → PVE places the CD/DVD at ide2).
	if got := vm.CdromWireValue(); got != "local:iso/boot.iso,media=cdrom" {
		t.Errorf("CdromWireValue = %q, want local:iso/boot.iso,media=cdrom", got)
	}
	if slot := vm.CdromSlot(); slot != "ide2" {
		t.Errorf("CdromSlot = %q, want ide2 (no cloud-init)", slot)
	}
	// Also verify ToCreateParams emits it under that slot.
	p, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if p["ide2"] != "local:iso/boot.iso,media=cdrom" {
		t.Errorf("ide2 in create = %v, want 'local:iso/boot.iso,media=cdrom'", p["ide2"])
	}

	// Verify the structured dep edge is emitted by VM.Deps().
	deps := vm.Deps()
	if len(deps) != 1 || deps[0].Kind != schema.KindISO || deps[0].Name != "boot-iso" {
		t.Errorf("Deps() = %v, want [ISO/boot-iso]", deps)
	}
}

// TestVMCloudInitShiftsCdromSlot: a VM with cloud-init enabled shifts the cdrom
// slot from ide2 to ide3 (PVE 9.2 quirk: cloud-init claims ide2).
func TestVMCloudInitShiftsCdromSlot(t *testing.T) {
	isoRaw := isoForTests("boot-iso", "local", "boot.iso", "https://example.com/boot.iso")
	vmRaw := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: vm-1
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  hardware:
    cdrom:
      iso: boot-iso
    cloud-init:
      enabled: true
      storage: local
`
	iso := schema.NewISO()
	if err := schema.YAMLTo(isoRaw, iso); err != nil {
		t.Fatalf("YAMLTo ISO: %v", err)
	}
	vm := schema.NewVM()
	if err := schema.YAMLTo(vmRaw, vm); err != nil {
		t.Fatalf("YAMLTo VM: %v", err)
	}
	if err := schema.ResolveArtifactRefs([]schema.Resource{iso, vm}); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}
	if slot := vm.CdromSlot(); slot != "ide3" {
		t.Errorf("CdromSlot = %q, want ide3 (cloud-init claims ide2)", slot)
	}
	p, _ := vm.ToCreateParams()
	if p["ide2"] != "local:cloudinit,size=4M" {
		t.Errorf("ide2 (cloud-init) = %v, want 'local:cloudinit,size=4M'", p["ide2"])
	}
	if p["ide3"] != "local:iso/boot.iso,media=cdrom" {
		t.Errorf("ide3 (cdrom) = %v, want 'local:iso/boot.iso,media=cdrom'", p["ide3"])
	}
}

// TestVMBIOSMachineWire: BIOS + machine + display + tablet + agent wire
// values must round-trip cleanly.
func TestVMBIOSMachineWire(t *testing.T) {
	vmRaw := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: vm-1
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  hardware:
    bios: ovmf
    machine: q35
    display: std
    efi-disk:
      storage: local-lvm
      size: 4MiB
    tpm:
      version: v2.0
    serial0: socket
  options:
    onboot: true
    agent: true
    tablet: true
    acpi: true
    startup: "order=10"
    hidden: true
    nested-virt: true
`
	vm := schema.NewVM()
	if err := schema.YAMLTo(vmRaw, vm); err != nil {
		t.Fatalf("YAMLTo VM: %v", err)
	}
	if err := vm.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	p, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	// expected wire values
	checks := map[string]any{
		"bios":       "ovmf",
		"machine":    "q35",
		"vga":        "std",
		"efidisk0":   "local-lvm:0.00390625",
		"tpm0":       "endpoint=host,version=v2.0",
		"serial0":    "socket",
		"onboot":     "1",
		"agent":      "1",
		"tablet":     "1",
		"acpi":       "1",
		"startup":    "order=10",
		"hidden":     "1",
		"nestedvirt": "1",
	}
	for k, v := range checks {
		if got, _ := p[k]; got != v {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}

	// Drift against a PVE report that matches these same values: expect no
	// owned-field changes (i.e. Drift returns changed=false).
	// PVE clamps the 4MiB EFI disk to its minimum and reports the PVE-assigned
	// LVM volume name; we own only the pool (+ efitype when pinned), so the
	// report's size/volume-name must not trip drift.
	live := map[string]any{}
	for k, v := range checks {
		live[k] = v
	}
	// PVE reports the core fields; include them so Drift does not false-positive.
	live["memory"] = int64(1024)
	live["cpu"] = "host"
	live["cores"] = 1
	live["name"] = "vm-1"
	live["tags"] = []any{"pveconform"}
	// PVE reports the owned scsi0 disk with a PVE-assigned LVM volume name; we
	// own pool + size only.
	live["scsi0"] = "local-lvm:vm-100-disk-0,size=4G"
	live["efidisk0"] = "local-lvm:vm-100-disk-1,size=4M"
	if _, _, changed := vm.Drift(live); changed {
		t.Errorf("Drift reported change on a matching VM config: got changed=true")
	}
}

// TestLXCNetPinnedWire: LXC NIC with pinned iface + tag + rate + firewall.
func TestLXCNetPinnedWire(t *testing.T) {
	lxcRaw := `apiVersion: ` + schema.APIVersion + `
kind: LXC
metadata:
  name: lxc-1
spec:
  node: pve01
  vmid: 9101
  memory: 1GiB
  cpu: {cores: 1}
  template: base-ctt
  root: {storage: local, size: 4GiB}
  networks:
    - iface: wired0
      bridge: vmbr0
      tag: 100
      rate-limit: 50
      firewall: true
      hwaddr: "aa:bb:cc:dd:ee:90"
`
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo LXC: %v", err)
	}
	if err := lxc.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	// No need to run ResolveArtifactRefs since template is not a real CTT;
	// we're testing NIC wire only (ostemplate is not emitted because no
	// resolvable CTT was in the resource set).
	p, err := lxc.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got, _ := p["net0"].(string)
	want := "name=wired0,bridge=vmbr0,tag=100,hwaddr=aa:bb:cc:dd:ee:90,rate=50,firewall=1"
	if got != want {
		t.Errorf("net0 = %q, want %q", got, want)
	}
}

// TestLXCDNSTypeWire: LXC DNS block must emit hostname / nameserver /
// searchdomain in the PVE 9.2 /lxc create grammar (NOT bare "dns=").
func TestLXCDNSTypeWire(t *testing.T) {
	lxcRaw := `apiVersion: ` + schema.APIVersion + `
kind: LXC
metadata:
  name: lxc-1
spec:
  node: pve01
  vmid: 9101
  memory: 1GiB
  cpu: {cores: 1}
  template: base-ctt
  root: {storage: local, size: 4GiB}
  dns:
    hostname: my-lxc
    nameservers: [1.1.1.1, 8.8.8.8]
    domain: example.com
`
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo LXC: %v", err)
	}
	// We don't need to resolve the template to test DNS keys.
	p, err := lxc.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	// PVE 9.2 /lxc create: hostname, nameserver (CSV), searchdomain.
	if got, _ := p["hostname"].(string); got != "my-lxc" {
		t.Errorf("hostname = %q, want 'my-lxc'", got)
	}
	if got, _ := p["nameserver"].(string); got != "1.1.1.1,8.8.8.8" {
		t.Errorf("nameserver = %q, want '1.1.1.1,8.8.8.8'", got)
	}
	if got, _ := p["searchdomain"].(string); got != "example.com" {
		t.Errorf("searchdomain = %q, want 'example.com'", got)
	}
	// PVE 9.2 LXC create rejects bare `dns`/`name`/`os`/`ttys`/`hwclock` —
	// verify none appear in ToCreateParams:
	for _, banned := range []string{"dns", "name", "os", "ttys", "hwclock"} {
		if _, exists := p[banned]; exists {
			t.Errorf("%s must not appear on PVE 9.2 /lxc create (probe: 'property is not defined')", banned)
		}
	}
}

// TestLXCMountPointsWire: additional mp* volumes are emitted in PVE 9.2 LXC
// create form `mp0=<pool>:<GiB>`.
func TestLXCMountPointsWire(t *testing.T) {
	lxcRaw := `apiVersion: ` + schema.APIVersion + `
kind: LXC
metadata:
  name: lxc-1
spec:
  node: pve01
  vmid: 9101
  memory: 1GiB
  cpu: {cores: 1}
  template: base-ctt
  root: {storage: local, size: 4GiB}
  mount-points:
    - storage: local-lvm
      size: 8GiB
      mount-point: /mnt/data
`
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo LXC: %v", err)
	}
	p, err := lxc.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got, _ := p["mp0"].(string)
	want := "local-lvm:8,mp=/mnt/data"
	if got != want {
		t.Errorf("mp0 = %q, want %q", got, want)
	}
}

// TestLXCOptionsWire + LXC dep: container-side options and template ref.
func TestLXCOptionsWire(t *testing.T) {
	lxcRaw := `apiVersion: ` + schema.APIVersion + `
kind: LXC
metadata:
  name: lxc-1
spec:
  node: pve01
  vmid: 9101
  memory: 1GiB
  cpu: {cores: 1}
  template: base-ctt
  root: {storage: local, size: 4GiB}
  options:
    unprivileged: true
    protection: true
    nesting: true
    keyctl: true
    fuse: true
    onboot: true
    startup: "order=20"
`
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo LXC: %v", err)
	}
	p, err := lxc.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	for _, k := range []string{"unprivileged", "protection", "nesting", "keyctl", "fuse", "onboot"} {
		if got, _ := p[k]; got != "1" {
			t.Errorf("%s = %v, want 1", k, got)
		}
	}
	if got, _ := p["startup"].(string); got != "order=20" {
		t.Errorf("startup = %q, want order=20", got)
	}
	// LXC's structured dep edge points at the CTTemplate.
	deps := lxc.Deps()
	if len(deps) != 1 || deps[0].Kind != schema.KindCTTemplate || deps[0].Name != "base-ctt" {
		t.Errorf("Deps() = %v, want [CTTemplate/base-ctt]", deps)
	}
}

// TestISOAndCTTemplateNodes: ISO / CTT must expose both spec.nodes (new) and
// spec.node (legacy) — Nodes() returns the union.
func TestISOAndCTTemplateNodes(t *testing.T) {
	// new form: only spec.nodes
	isoNew := `apiVersion: ` + schema.APIVersion + `
kind: ISO
metadata:
  name: i
spec:
  nodes: [a, b]
  storage: local
  filename: x.iso
  url: https://e.com/x.iso
`
	iso := schema.NewISO()
	if err := schema.YAMLTo(isoNew, iso); err != nil {
		t.Fatalf("YAMLTo ISO new: %v", err)
	}
	got := iso.Nodes()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ISO.Nodes (new form) = %v, want [a b]", got)
	}
	if iso.Node() != "a" {
		t.Errorf("ISO.Node (primary) = %q, want 'a'", iso.Node())
	}
	if got := iso.ID(); got != 0 {
		t.Errorf("ISO.ID = %d, want 0 (artifact)", got)
	}

	// legacy: only spec.node
	isoOld := `apiVersion: ` + schema.APIVersion + `
kind: ISO
metadata:
  name: i
spec:
  node: legacy
  storage: local
  filename: x.iso
  url: https://e.com/x.iso
`
	iso2 := schema.NewISO()
	if err := schema.YAMLTo(isoOld, iso2); err != nil {
		t.Fatalf("YAMLTo ISO legacy: %v", err)
	}
	got2 := iso2.Nodes()
	if len(got2) != 1 || got2[0] != "legacy" {
		t.Errorf("ISO.Nodes (legacy) = %v, want [legacy]", got2)
	}

	// CTT: only spec.nodes
	ctt := `apiVersion: ` + schema.APIVersion + `
kind: CTTemplate
metadata:
  name: c
spec:
  nodes: [pve01, pve02]
  storage: local
  filename: x.tar.zst
  url: https://e.com/x.tar.zst
`
	c := schema.NewCTTemplate()
	if err := schema.YAMLTo(ctt, c); err != nil {
		t.Fatalf("YAMLTo CTT: %v", err)
	}
	got3 := c.Nodes()
	if len(got3) != 2 || got3[0] != "pve01" || got3[1] != "pve02" {
		t.Errorf("CTT.Nodes = %v, want [pve01 pve02]", got3)
	}
	// CTT artifact: no numeric id.
	if c.ID() != 0 {
		t.Errorf("CTT.ID = %d, want 0 (artifact, not LXC cid)", c.ID())
	}
	// CTT ToCreateParams emits the download payload including content=vztmpl.
	p, err := c.ToCreateParams()
	if err != nil {
		t.Fatalf("CTT.ToCreateParams: %v", err)
	}
	if got, _ := p["content"].(string); got != "vztmpl" {
		t.Errorf("CTT download payload content = %q, want 'vztmpl'", got)
	}
	if got, _ := p["storage"].(string); got != "local" {
		t.Errorf("CTT download payload storage = %q, want 'local'", got)
	}
}

// TestVMOptionsDrift: PVE returns `onboot=1`, our desired is onboot=true →
// no change; onboot missing on current, desired is onboot=true → drift.
func TestVMOptionsDrift(t *testing.T) {
	vmRaw := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: v
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  options:
    onboot: true
    protection: true
`
	vm := schema.NewVM()
	if err := schema.YAMLTo(vmRaw, vm); err != nil {
		t.Fatalf("YAMLTo VM: %v", err)
	}

	// PVE report with matching options → no change.
	live := map[string]any{
		"memory": int64(1024),
		"cpu":    "host", "cores": 1,
		"tags":   []any{"pveconform"},
		"scsi0":  "local-lvm:vm-100-disk-0,size=4G",
		"onboot": "1", "protection": "1",
	}
	if _, _, changed := vm.Drift(live); changed {
		t.Errorf("Drift on matching options reported change")
	}

	// PVE report missing onboot → drift on onboot.
	delete(live, "onboot")
	upd, _, changed := vm.Drift(live)
	if !changed {
		t.Fatalf("Drift expected change for missing onboot")
	}
	if upd["onboot"] != "1" {
		t.Errorf("Drift(upd[onboot]) = %v, want '1'", upd["onboot"])
	}
	// Protection is still present in the live report → should NOT be in upd.
	if _, ok := upd["protection"]; ok {
		t.Errorf("Drift did not report protection (still matches live)")
	}
}

// TestVMCdromDrift: a VM with a cdrom ref — the PVE /config report shows
// the cdrom at the ide slot. Drift must compare only owned fields
// (pool, filename, media) and treat the PVE-assigned size as unknown.
func TestVMCdromDrift(t *testing.T) {
	isoRaw := isoForTests("boot-iso", "local", "boot.iso", "https://example.com/boot.iso")
	iso := schema.NewISO()
	if err := schema.YAMLTo(isoRaw, iso); err != nil {
		t.Fatalf("YAMLTo ISO: %v", err)
	}
	vmRaw := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: v
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  hardware:
    cdrom:
      iso: boot-iso
`
	vm := schema.NewVM()
	if err := schema.YAMLTo(vmRaw, vm); err != nil {
		t.Fatalf("YAMLTo VM: %v", err)
	}
	if err := schema.ResolveArtifactRefs([]schema.Resource{iso, vm}); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}
	// PVE's report for the cdrom slot with a PVE-assigned size token
	// appended. PVE 9.2 stores the cdrom at ide2 for a default i440fx VM
	// (no cloud-init).
	live := map[string]any{
		"memory": int64(1024),
		"cpu":    "host", "cores": 1,
		"tags":  []any{"pveconform"},
		"scsi0": "local-lvm:vm-100-disk-0,size=4G",
		"ide2":  "local:iso/boot.iso,media=cdrom,size=755M",
	}
	_, _, changed := vm.Drift(live)
	if changed {
		t.Errorf("Drift on matching cdrom (PVE size token ignored) reported change")
	}

	// Different ISO → drift on that slot.
	live["ide2"] = "local:iso/other.iso,media=cdrom,size=755M"
	if _, _, changed = vm.Drift(live); !changed {
		t.Errorf("Drift should report change when PVE has a different ISO")
	}
}

// TestVMNICFirewallWire: a NIC with firewall enabled must get the
// `firewall=1` option; without it, no token.
func TestVMNICFirewallWire(t *testing.T) {
	firewall := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: v
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  networks:
    - model: virtio
      bridge: vmbr0
      vlan: 100
      rate-limit: 50
      firewall: true
`
	noFirewall := `apiVersion: ` + schema.APIVersion + `
kind: VM
metadata:
  name: v
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 4GiB}
  networks:
    - model: virtio
      bridge: vmbr0
`
	withFW := schema.NewVM()
	if err := schema.YAMLTo(firewall, withFW); err != nil {
		t.Fatalf("YAMLTo firewall: %v", err)
	}
	p, _ := withFW.ToCreateParams()
	// PVE 9.2 wire: the VLAN tag on a QEMU NIC is `tag=N`, not `vlan=N`
	// (the latter is rejected with "property is not defined in schema").
	if got, _ := p["net0"].(string); got != "virtio,bridge=vmbr0,tag=100,rate=50,firewall=1" {
		t.Errorf("net0 (with firewall) = %q, want virtio,bridge=vmbr0,tag=100,rate=50,firewall=1", got)
	}
	withoutFW := schema.NewVM()
	if err := schema.YAMLTo(noFirewall, withoutFW); err != nil {
		t.Fatalf("YAMLTo no-firewall: %v", err)
	}
	p, _ = withoutFW.ToCreateParams()
	if got, _ := p["net0"].(string); got != "virtio,bridge=vmbr0" {
		t.Errorf("net0 (no firewall) = %q, want virtio,bridge=vmbr0", got)
	}

	// Drift: PVE report with firewall=1 (and a PVE-assigned MAC), desired
	// no-firewall/unpinned → no drift. We don't own the firewall token (not
	// requested) or the PVE-assigned MAC, so PVE setting them must not trip
	// drift.
	live := map[string]any{
		"memory": int64(1024),
		"cpu":    "host", "cores": 1,
		"tags":  []any{"pveconform"},
		"scsi0": "local-lvm:vm-100-disk-0,size=4G",
		"net0":  "virtio=52:54:00:90:0C:59,bridge=vmbr0,firewall=1",
	}
	if _, _, changed := withoutFW.Drift(live); changed {
		t.Errorf("Drift reported change when PVE added firewall=1 / MAC that we didn't request")
	}
}
