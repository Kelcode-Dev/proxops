package schema_test

import (
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// userFailingManifest is the exact manifest that produced the PVE 9
// "Parameter verification failed. errors: net0: invalid format - missing key in
// comma-separated list property; scsi0: invalid format - format error;
// scsi0.file: invalid format - unable to parse volume ID 'local-lvm'" on the
// conformance-dev cluster.
const userFailingManifest = `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: test-vm-01
spec:
  node: pve-dev-01
  vmid: 9100
  state: stopped
  memory: 1GiB
  cpu:
    type: kvm64
    cores: 1
  disks:
    - storage: local-lvm
      size: 8GiB
      interface: scsi0
      controller: virtio-scsi-single
      iothread: true
  networks:
    - model: virtio
      bridge: vmbr0
`

func mustParseUserVM(t *testing.T) *schema.VM {
	t.Helper()
	v := schema.NewVM()
	if err := schema.YAMLTo(userFailingManifest, v); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if err := v.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return v
}

// TestUserFailingManifestCreateWire — the wire values PVE actually rejects
// are now pinned here. Each sub-case corresponds to one line of PVE's
// "Parameter verification failed" report:
//   - scsi0: must be a <pool>:<size> volume spec (not <pool>,size=<bytes>),
//     so PVE can allocate the volume on local-lvm.
//   - scsi0.iothread: must be present (iothread=true) — PVE reads it as a
//     sibling form key, so we pin it.
//   - net0: must be a valid comma-separated property list; the unpinned-MAC
//     form is the bare device name "virtio" followed by bridge=… — NOT
//     "virtio=,bridge=…" (empty key → PVE "missing key in comma-separated
//     list property").
//   - memory: emitted in PVE's wire form (KiB, integer).
func TestUserFailingManifestCreateWire(t *testing.T) {
	v := mustParseUserVM(t)
	p, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	// scsi0: PVE 9.2 LVM/LVM-thin create-time form is "<pool>:<GiB>", with
	// iothread embedded inline. Empirically verified on conformance-dev:
	// "local-lvm:8589934592" → lvcreate "Volume too large (8.00 EiB)" (the
	// bare number is read as GiB, not bytes); "local-lvm:1" allocated a
	// 1073741824-byte thin volume; "local-lvm:0.5" → 512 MiB. 8GiB → "8".
	// The sibling "scsi0.iothread=1" create param is rejected by PVE's schema.
	if got, want := p["scsi0"], "local-lvm:8,iothread=1"; got != want {
		t.Errorf("scsi0 = %v, want %q (<pool>:<GiB>, iothread inline)", got, want)
	}
	if _, ok := p["scsi0.iothread"]; ok {
		t.Errorf("scsi0.iothread sibling param emitted (PVE rejects it; iothread belongs inline in the drive string): %v", p["scsi0.iothread"])
	}
	// scsihw is VM-wide; virtio-scsi-single is the user's controller.
	if got, want := p["scsihw"], "virtio-scsi-single"; got != want {
		t.Errorf("scsihw = %v, want %q", got, want)
	}
	// net0: unpinned-MAC virtio on vmbr0. PVE 9.2 omits firewall=0 when not
	// enabled; proxops emits it only when spec.networks[].firewall is set.
	if got, want := p["net0"], "virtio,bridge=vmbr0"; got != want {
		t.Errorf("net0 = %v, want %q (no '=' on unpinned-MAC virtio; no firewall token when unset)", got, want)
	}
	// memory: PVE's create-time `memory` is an integer MIB count (qm.conf:
	// "in MiB"). 1GiB → 1024. (The former KiB form, 1048576, would have
	// asked PVE for ~1 TiB of RAM.)
	if got, want := p["memory"], int64(1024); got != want {
		t.Errorf("memory = %T %v, want int64(%d) = PVE MiB form", got, got, want)
	}
	// cpu / cores
	if got, want := p["cpu"], "kvm64"; got != want {
		t.Errorf("cpu = %v, want %q", got, want)
	}
	if got, want := p["cores"], 1; got != want {
		t.Errorf("cores = %T %v, want %v", got, got, want)
	}

	// Drift against PVE's own report must be a no-op once PVE has created
	// the VM. PVE reports scsi0 as "<pool>:<volname>,iothread=1,size=<binary>"
	// (volume name is PVE-assigned; iothread is inline in the drive string;
	// size in binary suffixes like "8G")," and memory as an integer MiB count.
	live := map[string]any{
		"vmid":   9100,
		"name":   "test-vm-01",
		"memory": 1024,
		"cpu":    "kvm64",
		"cores":  1,
		"scsihw": "virtio-scsi-single",
		// PVE's post-allocate report shape for our 8GiB + iothread disk:
		"scsi0": "local-lvm:local-lvm-vm-9100-disk-0,iothread=1,size=8G",
		"net0":  "virtio=52:54:00:aa:bb:cc,bridge=vmbr0,firewall=0",
		"tags":  []any{"proxops"},
	}
	if update, stop, changed := v.Drift(live); changed {
		t.Errorf("Drift on PVE's own create report is not a no-op: changed=%v stop=%v update=%v", changed, stop, update)
	}
}

// TestVMFractionalDiskGiBCreate — a sub-GiB disk spec must render as a
// fractional PVE GiB number ("0.5"), not a byte count. PVE accepts decimal
// GiB in LVM/LVM-thin volume specs (the PVE UI's "Disk size (GiB)" field is a
// numberfield with decimalPrecision 3).
func TestVMFractionalDiskGiBCreate(t *testing.T) {
	v := mustParseUserVM(t)         // iothread: true in the manifest
	v.Spec.Disks[0].Size = "512MiB" // 0.5 GiB
	p, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, want := p["scsi0"], "local-lvm:0.5,iothread=1"; got != want {
		t.Errorf("scsi0 = %v, want %q", got, want)
	}
}

// TestVMDiskSizeDriftDetection — a PVE-reported data disk NOT at the desired
// size must NOT auto-write (PVE 9.2 /config pool/size writes recreate the
// volume → data loss; probed 2026-09-08). It must instead surface as a
// non-destructive anomaly. An iothread-only mismatch remains a SAFE in-place
// write that preserves PVE's live volume id.
func TestVMDiskSizeDriftDetection(t *testing.T) {
	v := mustParseUserVM(t) // desired 8GiB, iothread
	bigger := map[string]any{
		"memory": 1024,
		"scsi0":  "local-lvm:vm-9100-disk-0,iothread=1,size=16G",
	}
	upd, _, _ := v.Drift(bigger)
	if got, ok := upd["scsi0"]; ok {
		t.Fatalf("drift must NOT auto-resize a live data disk, but emitted scsi0=%v", got)
	}
	anoms := v.DriftAnomalies(bigger)
	if len(anoms) == 0 {
		t.Fatalf("size mismatch on a live data disk must surface as an anomaly, got none")
	}
	found := false
	for _, m := range anoms {
		if strings.Contains(m, "scsi0") && strings.Contains(m, "NOT auto-resize") {
			found = true
		}
	}
	if !found {
		t.Errorf("anomaly must name scsi0 + the no-auto-resize guard; got: %v", anoms)
	}

	// iothread missing on the PVE side must still produce a SAFE in-place
	// write that preserves PVE's live volume id (vm-9100-disk-0) and exact
	// size spelling.
	noIOThread := map[string]any{
		"memory": 1024,
		"scsi0":  "local-lvm:vm-9100-disk-0,size=8G",
	}
	upd2, stop, changed := v.Drift(noIOThread)
	if !changed {
		t.Fatalf("iothread toggle on identical pool+size must produce a safe update")
	}
	if !stop {
		t.Errorf("iothread toggle is stop-required")
	}
	if got, ok := upd2["scsi0"].(string); !ok || !strings.Contains(got, "vm-9100-disk-0") || !strings.Contains(got, "iothread=1") {
		t.Errorf("safe iothread write must PRESERVE PVE's live volume id+size; got %v", upd2["scsi0"])
	}
}

// TestVMUnpinnedMacNICDrift — PVE assigns an unpinned MAC at create time.
// Drift MUST not compare MACs (we don't own them) so a converged VM does
// not trigger a needless stop+update on every cycle.
func TestVMUnpinnedMacNICDrift(t *testing.T) {
	v := mustParseUserVM(t)
	live := map[string]any{
		"vmid":   9100,
		"memory": 1024,
		"cpu":    "kvm64",
		"cores":  1,
		"scsihw": "virtio-scsi-single",
		"scsi0":  "local-lvm:vm-9100-disk-0,iothread=1,size=8G",
		// PVE natively reports net0 as "virtio=<auto-mac>,bridge=..." even
		// when we created it with an unpinned MAC.
		"net0": "virtio=aa:bb:cc:dd:ee:ff,bridge=vmbr0,firewall=0",
		"tags": []any{"proxops"},
	}
	if _, stop, changed := v.Drift(live); changed {
		t.Errorf("Drift treats a PVE-assigned MAC as owned: stop=%v — VM would churn on every cycle", stop)
	}
}

// TestVPNickedMacNICDrift — a PINNED MAC IS owned; PVE assigning a different
// one must produce a drift update.
func TestVMNICPinnedMacDrift(t *testing.T) {
	v := mustParseUserVM(t)
	v.Spec.NICs[0].MAC = "de:ad:be:ef:00:01"
	live := map[string]any{
		"vmid":           9100,
		"memory":         1048576,
		"cpu":            "kvm64",
		"cores":          1,
		"scsihw":         "virtio-scsi-single",
		"scsi0":          "local-lvm:vm-9100-disk-0,size=8589934592",
		"scsi0.iothread": "1",
		// PVE put a different MAC than we pinned → drift on net0.
		"net0": "virtio=aa:bb:cc:dd:ee:ff,bridge=vmbr0",
		"tags": []any{"proxops"},
	}
	upd, _, changed := v.Drift(live)
	if !changed {
		t.Fatal("Drift missed a pinned MAC that PVE overrode")
	}
	if _, ok := upd["net0"]; !ok {
		t.Errorf("drift update should set net0, got %v", upd)
	}
}
