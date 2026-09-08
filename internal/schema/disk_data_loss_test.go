// Package schema_test: PVE 9.2 disk DATA-LOSS guard regression tests.
package schema_test

import (
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// PVE 9.2 data-loss guard (probed 2026-09-08 on conformance-dev):
//
//	- writing scsiN=<pool>:<size> over an EXISTING data disk makes PVE
//	  RECREATE the LVM volume (the old one + its data are deleted);
//	- the only safe in-place option write preserves PVE's live volume id,
//	  i.e. scsi0=<pool>:vm-9100-disk-0,size=8G[,iothread=1];
//	- PVE's /qemu/{id}/resize and /lxc/{id}/resize JSON endpoints return
//	  501 on PVE 9.2, so there is no safe API resize path.
//
// pveconform therefore classifies: pool/size/storage drift on a live
// disk -> NON-destructive anomaly; iothread toggle -> safe in-place
// write; brand-new slot -> safe create write.

func vmWithOneDisk(t *testing.T, iothread bool) *schema.VM {
	t.Helper()
	it := "false"
	if iothread {
		it = "true"
	}
	src := "apiVersion: " + schema.APIVersion + "\n" +
		"kind: VM\n" +
		"metadata:\n  name: disk-guard-vm\n" +
		"spec:\n" +
		"  node: pve01\n" +
		"  vmid: 100\n" +
		"  memory: 1GiB\n" +
		"  cpu: {type: host, cores: 1}\n" +
		"  disks:\n" +
		"    - {storage: local-lvm, size: 8GiB, interface: scsi0, controller: virtio-scsi-single, iothread: " + it + "}\n" +
		"  networks:\n" +
		"    - {model: virtio, bridge: vmbr0}\n"
	vm := schema.NewVM()
	if err := schema.YAMLTo(src, vm); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	return vm
}

const liveDisk8G = "local-lvm:vm-100-disk-0,size=8G"

// liveBaseline is a PVE /config report that fully converges with
// vmWithOneDisk EXCEPT the disk-under-test passed in (scsi0). It carries the
// other owned fields (memory, cpu, cores, scsihw, net0, tags) so that any
// drift pveconform reports in a test is attributable to the disk, not to
// unrelated fields.
func liveBaseline(disk string) map[string]any {
	return map[string]any{
		"memory": 1024,
		"cpu":    "host",
		"cores":  1,
		"scsihw": "virtio-scsi-single",
		"scsi0":  disk,
		"net0":   "virtio=AA:BB:CC:00:00:01,bridge=vmbr0",
		"tags":   []any{"pveconform"},
	}
}

// TestVMDiskSizeDriftIsAnomalyNotWrite pins the data-loss guard: a live
// data disk PVE reports at a different size must NOT be auto-resized via
// /config (PVE would recreate the volume, losing its data); it must
// surface as a non-destructive anomaly instead.
func TestVMDiskSizeDriftIsAnomalyNotWrite(t *testing.T) {
	vm := vmWithOneDisk(t, false)
	live := liveBaseline("local-lvm:vm-100-disk-0,size=16G")
	upd, _, changed := vm.Drift(live)
	if changed {
		t.Fatalf("Drift wrote a config for a live-disk size drift; pveconform must NOT auto-resize a data-bearing disk")
	}
	if _, ok := upd["scsi0"]; ok {
		t.Fatalf("Drift emitted scsi0=%v; must not write desired size over a live disk", upd["scsi0"])
	}
	anoms := vm.DriftAnomalies(live)
	if len(anoms) != 1 {
		t.Fatalf("anomalies = %d, want 1: %v", len(anoms), anoms)
	}
	if !strings.Contains(anoms[0], "scsi0") || !strings.Contains(anoms[0], "NOT auto-resize") {
		t.Errorf("anomaly must name scsi0 + the data-loss guard: %q", anoms[0])
	}
}

// TestVMDiskPoolDriftIsAnomalyNotWrite: same guard for a pool/storage move.
func TestVMDiskPoolDriftIsAnomalyNotWrite(t *testing.T) {
	vm := vmWithOneDisk(t, false)
	live := liveBaseline("otherpool:vm-100-disk-0,size=8G")
	upd, _, changed := vm.Drift(live)
	if changed {
		t.Fatalf("Drift auto-wrote a pool change on a live data disk; must be non-destructive")
	}
	if _, ok := upd["scsi0"]; ok {
		t.Fatalf("pool-change drift must not emit an update")
	}
	if anoms := vm.DriftAnomalies(live); len(anoms) != 1 {
		t.Fatalf("anomalies = %d, want 1: %v", len(anoms), anoms)
	}
}

// TestVMDiskIothreadDriftIsSafeInPlaceWrite: an iothread toggle on a live
// disk IS safe; pveconform rewrites the slot in PVE's LIVE form (keeping
// the PVE-assigned volume id + exact size token) so PVE updates the option
// in place rather than recreating the volume.
func TestVMDiskIothreadDriftIsSafeInPlaceWrite(t *testing.T) {
	vm := vmWithOneDisk(t, true)
	live := liveBaseline(liveDisk8G)
	upd, stop, changed := vm.Drift(live)
	if !changed {
		t.Fatalf("iothread-only drift must produce a safe update")
	}
	if !stop {
		t.Errorf("iothread toggle is stop-required")
	}
	got, _ := upd["scsi0"].(string)
	if !strings.Contains(got, "vm-100-disk-0") {
		t.Errorf("safe iothread write lost PVE's live volume id: %q", got)
	}
	if !strings.Contains(got, "iothread=1") {
		t.Errorf("safe iothread write did not set iothread=1: %q", got)
	}
	liveOK := liveBaseline(liveDisk8G + ",iothread=1")
	if _, _, changed := vm.Drift(liveOK); changed {
		t.Errorf("converged iothread state must not drift")
	}
}

// TestVMDiskNewSlotIsSafeCreateWrite: no live volume at the slot yet ->
// a create-form write is safe and expected.
func TestVMDiskNewSlotIsSafeCreateWrite(t *testing.T) {
	vm := vmWithOneDisk(t, false)
	live := map[string]any{}
	upd, _, changed := vm.Drift(live)
	if !changed {
		t.Fatalf("empty slot must produce a safe create write")
	}
	if got, ok := upd["scsi0"].(string); !ok || strings.Contains(got, "vm-") {
		t.Errorf("new-slot write must be the create form (no live volume id), got %v", upd["scsi0"])
	}
}

// TestLXCRootfsSizeDriftIsAnomaly: the same data-loss guard applies to
// LXC rootfs - pveconform must not auto-resize a live LXC volume.
func TestLXCRootfsSizeDriftIsAnomaly(t *testing.T) {
	src := "apiVersion: " + schema.APIVersion + "\n" +
		"kind: LXC\n" +
		"metadata:\n  name: disk-guard-lxc\n" +
		"spec:\n" +
		"  node: pve01\n" +
		"  vmid: 200\n" +
		"  memory: 512MiB\n" +
		"  cpu: {cores: 1}\n" +
		"  template: some-template\n" +
		"  root: {storage: local-lvm, size: 4GiB}\n" +
		"  networks:\n" +
		"    - {bridge: vmbr0}\n"
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(src, lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := map[string]any{
		"memory": "512", "cores": "1",
		"rootfs": "local-lvm:vm-200-disk-0,mp=/,size=8G",
		"net0":   "net0,bridge=vmbr0,type=veth",
		"tags":   []any{"pveconform"},
	}
	upd, _, _ := lxc.Drift(live)
	if _, ok := upd["rootfs"]; ok {
		t.Fatalf("LXC rootfs size drift must NOT auto-write rootfs (PVE recreates the LVM volume)")
	}
	anoms := lxc.DriftAnomalies(live)
	found := false
	for _, m := range anoms {
		if strings.Contains(m, "rootfs") && strings.Contains(m, "NOT auto-resize") {
			found = true
		}
	}
	if !found {
		t.Errorf("LXC rootfs pool/size drift must surface as a data-loss anomaly; got %v", anoms)
	}
}
