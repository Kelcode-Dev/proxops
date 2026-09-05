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
	// scsi0 carries the disk with size.
	disk, _ := p["scsi0"].(string)
	if disk == "" || !strings.Contains(disk, "vm_disks") || !strings.Contains(disk, "size=") {
		t.Errorf("scsi0 = %q", disk)
	}
	// net0 carries virtio + bridge.
	net0, _ := p["net0"].(string)
	if !strings.Contains(net0, "virtio") || !strings.Contains(net0, "bridge=vmbr2") {
		t.Errorf("net0 = %q", net0)
	}
	// scsihw controller is wired from the first disk's Controller.
	if p["scsihw"] != "virtio-scsi-single" {
		t.Errorf("scsihw = %v, want virtio-scsi-single", p["scsihw"])
	}
	// iothread set on scsi0.
	if p["scsi0.iothread"] != "1" {
		t.Errorf("scsi0.iothread = %v, want 1", p["scsi0.iothread"])
	}
}

// TestDriftNoChange: a PVE config that already matches desired returns no update.
func TestDriftNoChange(t *testing.T) {
	v := mustParseVM(t)
	// The mock returns config with PVE's native spellings. Build a "matching"
	// current state and assert Drift reports no change.
	current := map[string]any{
		"cpu":     "host",
		"cores":   "4",
		"memory":  "8388608",
		"tags":    "pveconform",
		"name":    "talos-worker-01",
		"scsi0":     "vm_disks:vm-142-disk-0,size=53687091200",
		"scsi0.iothread": "1",
		"scsihw":  "virtio-scsi-single",
		"net0":    "virtio=52:54:00:aa:bb:cc,bridge=vmbr2,firewall=0",
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
		"cpu":     "host",
		"cores":   "4",
		"memory":  "16777216", // 16 GiB in KiB
		"tags":    "pveconform",
		"scsi0":   "vm_disks:0,size=53687091200",
		"net0":    "virtio=,bridge=vmbr2,firewall=0",
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
