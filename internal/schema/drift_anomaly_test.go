package schema_test

import (
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

const vmStdDisks = "  disks:\n    - {storage: local-lvm, size: 4GiB}\n"
const vmStdNics = "  networks:\n    - {model: virtio, bridge: vmbr0}\n"

// makeVM returns a valid schema.Resource VM from YAML. disksBlock /
// networksBlock must be the *full* spec sub-blocks (yaml mapping under
// `spec`); Validate() requires both to be present.
func makeVM(t *testing.T, disksBlock, networksBlock string) *schema.VM {
	t.Helper()
	src := "apiVersion: " + schema.APIVersion + "\n" +
		"kind: VM\n" +
		"metadata:\n  name: anomalous-vm\n" +
		"spec:\n" +
		"  node: pve01\n" +
		"  vmid: 100\n" +
		"  memory: 1GiB\n" +
		"  cpu: {type: host, cores: 1}\n" +
		disksBlock +
		networksBlock
	vm := schema.NewVM()
	if err := schema.YAMLTo(src, vm); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	return vm
}

// TestVMDriftAnomalyLiveOnlyDisk is the regression test for the "live-only
// disk not detected as drift" bug discovered on conformance-dev PVE 9.2.
//
// A PVE-side scsi1 that the manifest does not declare must surface on
// DriftAnomalies — AND must NOT be auto-deleted by Drift (PVE's
// `scsi1=none` only detaches; the LVM volume survives; the only safe
// auto-action is none, so proxops reports and leaves it alone).
func TestVMDriftAnomalyLiveOnlyDisk(t *testing.T) {
	vm := makeVM(t, vmStdDisks, vmStdNics)
	live := map[string]any{
		"memory":  "1024",
		"cpu":     "host",
		"cores":   "1",
		"scsi0":   "local-lvm:vm-100-disk-0,size=4G",
		"scsi1":   "local-lvm:vm-100-disk-1,size=2G", // live-only
		"net0":    "virtio=52:54:00:AA:BB:01,bridge=vmbr0",
		"tags":    "proxops",
		"smbios1": "uuid=00000000-0000-0000-0000-000000000000",
		"vmgenid": "00000000-0000-0000-0000-000000000000",
		"ide2":    "none,media=cdrom",
		"onboot":  "1",
		"agent":   "1",
	}

	anoms := vm.DriftAnomalies(live)
	if len(anoms) != 1 {
		t.Fatalf("DriftAnomalies = %d, want 1: %v", len(anoms), anoms)
	}
	if !strings.Contains(anoms[0], "scsi1") {
		t.Errorf("anomaly must name the slot: %q", anoms[0])
	}
	if !strings.Contains(anoms[0], "not in spec.disks") {
		t.Errorf("anomaly must state the slot is not in spec.disks: %q", anoms[0])
	}
	if !strings.Contains(anoms[0], "will not automatically remove") {
		t.Errorf("anomaly must state proxops will NOT auto-remove: %q", anoms[0])
	}

	// Drift must NOT emit scsi1=none or any scsi1 update — that would be a
	// non-reversible data-adjacent write (PVE leaves the LVM volume behind).
	upd, stop, changed := vm.Drift(live)
	if v, ok := upd["scsi1"]; ok {
		t.Errorf("Drift emitted scsi1=%v; proxops must not auto-touch live-only disks (stop=%v changed=%v)", v, stop, changed)
	}
}

// TestVMDriftAnomalyLiveOnlyNIC: a net1 not declared in spec.networks is an
// anomaly, reported but not auto-removed.
func TestVMDriftAnomalyLiveOnlyNIC(t *testing.T) {
	vm := makeVM(t, vmStdDisks, vmStdNics)
	live := map[string]any{
		"memory": "1024", "cpu": "host", "cores": "1",
		"scsi0": "local-lvm:vm-100-disk-0,size=4G",
		"net0":  "virtio=52:54:00:AA:BB:01,bridge=vmbr0",
		"net1":  "e1000=DD:EE:FF:00:11:22,bridge=vmbr1",
		"tags":  "proxops",
	}
	anoms := vm.DriftAnomalies(live)
	if len(anoms) != 1 {
		t.Fatalf("DriftAnomalies = %d, want 1: %v", len(anoms), anoms)
	}
	if !strings.Contains(anoms[0], "net1") {
		t.Errorf("anomaly should name net1: %q", anoms[0])
	}
}

// TestVMDriftAnomalyNoneSlotIsNotAnomaly: PVE's empty form
// ("none,media=cdrom" or just "none") for a detached slot is NOT a
// live-only device — it is PVE's "no device here" report.
func TestVMDriftAnomalyNoneSlotIsNotAnomaly(t *testing.T) {
	vm := makeVM(t, vmStdDisks, vmStdNics)
	live := map[string]any{
		"memory": "1024", "cpu": "host", "cores": "1",
		"scsi0": "local-lvm:vm-100-disk-0,size=4G",
		"scsi1": "none,media=cdrom",
		"net0":  "virtio=52:54:00:AA:BB:01,bridge=vmbr0",
		"tags":  "proxops",
	}
	if anoms := vm.DriftAnomalies(live); len(anoms) != 0 {
		t.Fatalf("DriftAnomalies = %v, want none (scsi1=none is PVE empty slot)", anoms)
	}
}

// TestVMDriftAnomalyDeclaredDiskIsNotAnomaly: a disk the manifest does
// declare must not produce an anomaly AND must not be seen as drift when
// PVE's on-disk form is the expected normalisation of our wire form.
func TestVMDriftAnomalyDeclaredDiskIsNotAnomaly(t *testing.T) {
	twoDisk := "  disks:\n    - {storage: local-lvm, size: 4GiB}\n    - {storage: local-lvm, size: 2GiB}\n"
	vm := makeVM(t, twoDisk, vmStdNics)
	live := map[string]any{
		"memory": "1024", "cpu": "host", "cores": "1",
		"scsi0": "local-lvm:vm-100-disk-0,size=4G",
		"scsi1": "local-lvm:vm-100-disk-1,size=2G",
		"net0":  "virtio=52:54:00:AA:BB:01,bridge=vmbr0",
		"tags":  "proxops",
	}
	if anoms := vm.DriftAnomalies(live); len(anoms) != 0 {
		t.Fatalf("DriftAnomalies = %v, want none", anoms)
	}
	_, _, changed := vm.Drift(live)
	if changed {
		t.Errorf("Drift should not report changed on fully-matching declared disks (PVE-assigned volume id not owned)")
	}
}

// TestVMDriftAnomalyNilCurrent: nil live → nil anomalies (defensive).
func TestVMDriftAnomalyNilCurrent(t *testing.T) {
	vm := makeVM(t, vmStdDisks, vmStdNics)
	if anoms := vm.DriftAnomalies(nil); anoms != nil {
		t.Fatalf("DriftAnomalies(nil) = %v, want nil", anoms)
	}
}

// TestLXCDriftAnomalyLiveOnlyMP: a PVE-side mp1 the LXC manifest does not
// declare surfaces as an anomaly; Drift does not emit mp1=none.
func TestLXCDriftAnomalyLiveOnlyMP(t *testing.T) {
	src := "apiVersion: " + schema.APIVersion + "\n" +
		"kind: LXC\n" +
		"metadata:\n  name: anomalous-lxc\n" +
		"spec:\n" +
		"  node: pve01\n" +
		"  vmid: 200\n" +
		"  memory: 512MiB\n" +
		"  cpu: {cores: 1}\n" +
		"  template: base-ctt\n" +
		"  root: {storage: local-lvm, size: 4GiB}\n" +
		"  networks:\n    - {bridge: vmbr0}\n"
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(src, lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	// The LXC does not reference any mount-points; a live mp1 is an anomaly.
	live := map[string]any{
		"memory":       "512",
		"cores":        "1",
		"hostname":     "anomalous-lxc",
		"rootfs":       "local-lvm:vm-200-disk-0,mp=/",
		"mp1":          "local-lvm:vm-200-disk-1,mp=/mnt/data",
		"net0":         "name=net0,bridge=vmbr0,hwaddr=AA:BB:CC:DD:EE:FF,type=veth",
		"ostype":       "debian",
		"ostemplate":   "local:vztmpl/base.tar.zst",
		"unprivileged": "1",
		"tags":         "proxops",
	}
	// Resolve the template ref against a real CTT so the manifest is valid.
	ctt := schema.NewCTTemplate()
	if err := schema.YAMLTo("apiVersion: "+schema.APIVersion+"\nkind: CTTemplate\nmetadata:\n  name: base-ctt\nspec:\n  nodes: [pve01]\n  storage: local\n  filename: base.tar.zst\n  url: https://example.com/base.tar.zst\n", ctt); err != nil {
		t.Fatalf("YAMLTo CTT: %v", err)
	}
	if err := schema.ResolveArtifactRefs([]schema.Resource{ctt, lxc}); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}

	anoms := lxc.DriftAnomalies(live)
	if len(anoms) != 1 {
		t.Fatalf("DriftAnomalies = %d, want 1: %v", len(anoms), anoms)
	}
	if !strings.Contains(anoms[0], "mp1") {
		t.Errorf("anomaly should name mp1: %q", anoms[0])
	}
	// And Drift must not emit mp1=none.
	upd, _, _ := lxc.Drift(live)
	if v, ok := upd["mp1"]; ok {
		t.Errorf("Drift emitted mp1=%v; proxops must not auto-remove live-only mount points", v)
	}
}

// TestArtifactDriftAnomalies: ISO and CTTemplate always return nil anomalies
// (storage artifacts have no per-slot live devices).
func TestArtifactDriftAnomalies(t *testing.T) {
	iso := schema.NewISO()
	if anoms := iso.DriftAnomalies(map[string]any{"present": true}); anoms != nil {
		t.Errorf("ISO DriftAnomalies = %v, want nil", anoms)
	}
	ctt := schema.NewCTTemplate()
	if anoms := ctt.DriftAnomalies(map[string]any{"present": true}); anoms != nil {
		t.Errorf("CTTemplate DriftAnomalies = %v, want nil", anoms)
	}
}
