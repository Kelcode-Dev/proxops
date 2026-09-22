package schema_test

import (
	"strings"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/schema"
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
	// PVE wire units: memory is an integer MiB count (qm.conf(5): "in MiB",
	// verified live on PVE 9.2). 8GiB → 8192.
	if p["memory"] != int64(8*1024) {
		t.Errorf("memory = %T %v, want int64(%d) — PVE MiB form", p["memory"], p["memory"], 8*1024)
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
	if !strings.Contains(tags, "proxops") {
		t.Errorf("tags missing proxops: %q", tags)
	}
	// scsi0: PVE 9.2 LVM/LVM-thin create-time wire form is
	// "<pool>:<size-GiB>" with options inline. PVE reads the bare number in
	// GiB (verified live: local-lvm:8589934592 → "Volume too large (8.00
	// EiB)"; local-lvm:1 → a 1 GiB LV). 50GiB → "vm_disks:50".
	disk, _ := p["scsi0"].(string)
	if disk != "vm_disks:50,iothread=1" {
		t.Errorf("scsi0 = %q, want vm_disks:50,iothread=1 (pool:<GiB>, iothread inline)", disk)
	}
	// nicString: with an unpinned (empty) MAC, PVE expects "virtio,bridge=..."
	// — NOT "virtio=,bridge=..." (empty key after '=' trips PVE's
	// "missing key in comma-separated list property" guard). PVE 9.2 omits
	// `firewall=0` from NIC reports when it is not enabled, so proxops
	// likewise leaves that token off the wire until the user sets
	// spec.networks[].firewall (drift-safe: both sides see it as absent).
	net0, _ := p["net0"].(string)
	if net0 != "virtio,bridge=vmbr2" {
		t.Errorf("net0 = %q, want virtio,bridge=vmbr2 (no MAC → bare model name)", net0)
	}
	if strings.Contains(net0, "virtio=") {
		t.Errorf("net0 = %q: empty-MAC NIC must not emit 'virtio=' (missing key in comma-separated property)", net0)
	}
	// scsihw controller is wired from the first disk's Controller.
	if p["scsihw"] != "virtio-scsi-single" {
		t.Errorf("scsihw = %v, want virtio-scsi-single", p["scsihw"])
	}
	// iothread is INLINE in the drive string, NOT a sibling "<slot>.iothread"
	// key: PVE's create schema rejects the sibling form with "property is
	// not defined in schema and the schema does not allow additional
	// properties".
	if _, ok := p["scsi0.iothread"]; ok {
		t.Errorf("scsi0.iothread = %v — iothread must be inline in scsi0, not a sibling key", p["scsi0.iothread"])
	}
	// Memory normalization: 8GiB → 8192 MiB (int64), as PVE's wire form.
	if p["memory"] != int64(8*1024) {
		t.Errorf("memory = %T %v, want int64(8192) — PVE wire form is MiB", p["memory"], p["memory"])
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

	if got, want := p["scsi0"].(string), "local-lvm:8,iothread=1"; got != want {
		t.Errorf("scsi0 on LVM-thin = %q, want %q (pool:<GiB>, iothread inline — NOT bytes, NOT 8G)", got, want)
	}
	if got := p["scsi0.iothread"]; got != nil {
		t.Errorf("scsi0.iothread = %T %v, want absent (iothread belongs inline in the drive string; PVE rejects the sibling key)", got, got)
	}
	if got, want := p["net0"].(string), "virtio,bridge=vmbr2"; got != want {
		t.Errorf("net0 = %q, want %q (virtio NIC + bridge; no firewall token when unset)", got, want)
	}
	if got, want := p["memory"], int64(1024); got != want {
		t.Errorf("memory = %T %v, want int64(1024) — GiB normalized to PVE MiB int", got, got)
	}
	// Bonus: cpu type + cores round-trip as expected.
	if got := p["cpu"]; got != "kvm64" {
		t.Errorf("cpu = %v, want kvm64", got)
	}
	if got := p["cores"]; got != 1 {
		t.Errorf("cores = %T %v, want 1", got, got)
	}

	// Drift side: PVE's LVM disk config report is
	// "pool:vmid-volname,iothread=1,size=<binary>" (PVE-assigned volume name,
	// binary-suffix size, iothread inline in the drive string). Our
	// create-time wire form "pool:<GiB>[,iothread=1]" normalizes to the same
	// owned (pool, size-bytes, iothread) triple, so a freshly-created VM
	// reports no drift.
	current := map[string]any{
		"vmid":   int64(142),
		"name":   "x",
		"cpu":    "kvm64",
		"cores":  1,
		"memory": 1024, // PVE MiB count
		"scsihw": "virtio-scsi",
		"scsi0":  "local-lvm:vm-142-disk-0,iothread=1,size=8G",
		"net0":   "virtio=52:54:00:aa:bb:cc,bridge=vmbr2",
		"tags":   []any{"proxops"},
	}
	if upd, stop, changed := v.Drift(current); changed {
		t.Errorf("Drift against PVE's LVM disk report reported a change: upd=%v stop=%v — size/shape mismatch", upd, stop)
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
		"cpu":    "host",
		"cores":  4,
		"memory": 8192, // PVE MiB count (8GiB)
		"tags":   []any{"proxops"},
		"name":   "talos-worker-01",
		"scsi0":  "vm_disks:vm-142-disk-0,iothread=1,size=50G",
		"scsihw": "virtio-scsi-single",
		"net0":   "virtio=52:54:00:aa:bb:cc,bridge=vmbr2,firewall=0",
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
		"memory": "16384", // 16 GiB in PVE MiB
		"tags":   "proxops",
		"scsi0":  "vm_disks:0,size=50G",
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
