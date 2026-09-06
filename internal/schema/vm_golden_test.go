package schema_test

import (
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// talosSample is the user's canonical example (exact, including interface and
// controller on the disk).
const talosSample = `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: talos-worker-01
spec:
  node: pve01
  vmid: 142
  cpu:
    type: host
    cores: 4
  memory: 8GiB
  disks:
    - storage: vm_disks
      size: 50GiB
      interface: scsi0
      controller: virtio-scsi-single
      iothread: true
  networks:
    - bridge: vmbr2
      model: virtio
`

func mustParseVM(t *testing.T) *schema.VM {
	t.Helper()
	v := schema.NewVM()
	if err := schema.YAMLTo(talosSample, v); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return v
}

func TestTalosSampleValidates(t *testing.T) {
	v := mustParseVM(t)
	if v.Ref().String() != "VM/talos-worker-01" {
		t.Errorf("ref = %s", v.Ref())
	}
	if v.ID() != 142 || v.Node() != "pve01" {
		t.Errorf("id/node = %d/%s", v.ID(), v.Node())
	}
	if v.DesiredState() != "started" {
		t.Errorf("state = %q", v.DesiredState())
	}
}

func TestTalosSampleToCreateParams(t *testing.T) {
	v := mustParseVM(t)
	p, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	// PVE wire units.
	if p["memory"] != "8192M" && p["memory"] != int64(8*1024*1024) && p["memory"] != "8388608K" {
		t.Errorf("memory = %T %v — expected 8GiB in PVE units", p["memory"], p["memory"])
	}
	if p["cpu"] != "host" {
		t.Errorf("cpu = %v", p["cpu"])
	}
	if p["cores"] != 4 && p["cores"] != "4" {
		t.Errorf("cores = %v", p["cores"])
	}
	if p["vmid"] != 142 && p["vmid"] != "142" {
		t.Errorf("vmid = %v", p["vmid"])
	}
	// PVE name defaults to metadata.name.
	if p["name"] != "talos-worker-01" {
		t.Errorf("name = %v", p["name"])
	}
	// ownership tag must be present.
	tags, _ := p["tags"].(string)
	if !strings.Contains(tags, "pveconform") {
		t.Errorf("tags missing pveconform: %q", tags)
	}
	// scsi0: PVE create-time volume spec "<pool>:<size>"; PVE allocates the
	// volume name. NOT a config-report form ("pool:vmid-volid,size=...") and
	// NOT the declarative "pool,size=<bytes>" either — those would fail
	// PVE's "Parameter verification".
	disk, _ := p["scsi0"].(string)
	if disk != "vm_disks:50G" {
		t.Errorf("scsi0 = %q, want vm_disks:50G (PVE create-time volume spec)", disk)
	}
	if !strings.HasSuffix(disk, ":50G") || !strings.HasPrefix(disk, "vm_disks:") {
		t.Errorf("scsi0 = %q, not in PVE volume-spec form", disk)
	}
	// nicString: with an unpinned (empty) MAC, PVE expects "virtio,bridge=..."
	// — NOT "virtio=,bridge=..." (empty key after '=' trips PVE's
	// "missing key in comma-separated list property" guard).
	net0, _ := p["net0"].(string)
	if net0 != "virtio,bridge=vmbr2,firewall=0" {
		t.Errorf("net0 = %q, want virtio,bridge=vmbr2,firewall=0 (no MAC → bare model name)", net0)
	}
	if strings.Contains(net0, "virtio=") {
		t.Errorf("net0 = %q: empty-MAC NIC must not emit 'virtio=' (missing key in comma-separated property)", net0)
	}
	// scsihw controller is wired from the first disk's Controller.
	if p["scsihw"] != "virtio-scsi-single" {
		t.Errorf("scsihw = %v, want virtio-scsi-single", p["scsihw"])
	}
	// iothread set on scsi0.
	if p["scsi0.iothread"] != "1" {
		t.Errorf("scsi0.iothread = %v, want 1", p["scsi0.iothread"])
	}
	// Memory normalization: 8GiB → 8388608 KiB (int64), as PVE's wire form.
	if p["memory"] != int64(8*1024*1024) {
		t.Errorf("memory = %T %v, want int64(8388608) — PVE wire form is KiB", p["memory"], p["memory"])
	}
}

// TestTalosSampleCreateExactValues — pin the exact create-wire values for the
// four regression items the user called out:
//  1. scsi0 on LVM-thin
//  2. iothread=true
//  3. virtio NIC + bridge (when MAC is not pinned)
//  4. memory normalization (GiB → KiB int)
//
// A future refactor that changes the volume-spec form, drops iothread, adds a
// spurious '=' on the NIC line, or sends memory in bytes would break each
// of these assertions.
func TestTalosSampleCreateExactValues(t *testing.T) {
	v := mustParseVM(t)
	v.Spec.Disks[0].Storage = "local-lvm"
	v.Spec.Disks[0].Size = "8GiB"
	v.Spec.Disks[0].Controller = "virtio-scsi"
	v.Spec.CPU.Type = "kvm64"
	v.Spec.Memory = "1GiB"
	v.Spec.CPU.Cores = 1

	p, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}

	if got, want := p["scsi0"].(string), "local-lvm:8G"; got != want {
		t.Errorf("scsi0 on LVM-thin = %q, want %q", got, want)
	}
	if got := p["scsi0.iothread"]; got != "1" {
		t.Errorf("scsi0.iothread = %T %v, want \"1\" (iothread=true pinned in wire form)", got, got)
	}
	if got, want := p["net0"].(string), "virtio,bridge=vmbr2,firewall=0"; got != want {
		t.Errorf("net0 = %q, want %q (virtio NIC + bridge, no =<empty-MAC>)", got, want)
	}
	if got, want := p["memory"], int64(1024*1024); got != want {
		t.Errorf("memory = %T %v, want int64(1048576) — GiB normalized to KiB int", got, got)
	}
	// Bonus: cpu type + cores round-trip as expected.
	if got := p["cpu"]; got != "kvm64" {
		t.Errorf("cpu = %v, want kvm64", got)
	}
	if got := p["cores"]; got != 1 {
		t.Errorf("cores = %T %v, want 1", got, got)
	}

	// Drift side: PVE's config report uses "pool:vmid-vol-id,size=<bytes>";
	// our create wire form is "pool:<size>". diskMatches normalizes both to
	// (pool, size-bytes) so a freshly-created VM does NOT drift back.
	// (8 GiB = 8589934592 bytes, 1 GiB mem = 1048576 KiB.)
	desiredDisk8G := int64(8 * 1024 * 1024 * 1024)
	_ = desiredDisk8G
	current := map[string]any{
		"vmid":           int64(142),
		"name":           "x",
		"cpu":            "kvm64",
		"cores":          1,
		"memory":         int64(1024 * 1024),
		"scsihw":         "virtio-scsi",
		"scsi0":          "local-lvm:vm-142-disk-0,size=8589934592",
		"scsi0.iothread": "1",
		"net0":           "virtio=52:54:00:aa:bb:cc,bridge=vmbr2",
		"tags":           []any{"pveconform"},
	}
	if _, _, changed := v.Drift(current); changed {
		t.Errorf("Drift against a just-created VM reported change; wire form <-> PVE report shape mismatch")
	}
}

// TestDriftNoChange: a PVE config that already matches desired returns no update.
func TestDriftNoChange(t *testing.T) {
	v := mustParseVM(t)
	// The mock returns config with PVE's native spellings. Build a "matching"
	// current state and assert Drift reports no change.
	// PVE-native spellings: tags is a JSON array, memory a number, disks a
	// "pool:vol,size=<bytes>" string.
	current := map[string]any{
		"cpu":            "host",
		"cores":          4,
		"memory":         8388608,
		"tags":           []any{"pveconform"},
		"name":           "talos-worker-01",
		"scsi0":          "vm_disks:vm-142-disk-0,size=53687091200",
		"scsi0.iothread": "1",
		"scsihw":         "virtio-scsi-single",
		"net0":           "virtio=52:54:00:aa:bb:cc,bridge=vmbr2,firewall=0",
	}
	upd, stop, changed := v.Drift(current)
	if changed {
		t.Errorf("Drift reported change against matching state: upd=%v stop=%v", upd, stop)
	}
}

// TestDriftMemoryChange: bumping memory must produce a stopped-required update.
func TestDriftMemoryChange(t *testing.T) {
	v := mustParseVM(t)
	// Desired is 8GiB but PVE reports 16GiB → must converge down.
	current := map[string]any{
		"cpu":    "host",
		"cores":  "4",
		"memory": "16777216", // 16 GiB in KiB
		"tags":   "pveconform",
		"scsi0":  "vm_disks:0,size=53687091200",
		"net0":   "virtio=,bridge=vmbr2,firewall=0",
	}
	upd, stop, changed := v.Drift(current)
	if !changed {
		t.Fatal("Drift did not detect memory change")
	}
	if !stop {
		t.Error("memory change requires stop=true")
	}
	if _, ok := upd["memory"]; !ok {
		t.Errorf("update params missing memory: %+v", upd)
	}
}

// TestDriftMissingVM: nil current (VM not on PVE) → no update params; the
// planner is expected to create. Drift returns changed=false for nil current.
func TestDriftMissingVM(t *testing.T) {
	v := mustParseVM(t)
	upd, stop, changed := v.Drift(nil)
	if stop {
		t.Error("stop must be false when current is nil")
	}
	if changed || upd != nil {
		t.Errorf("expected no drift on nil current: upd=%v changed=%v", upd, changed)
	}
}
