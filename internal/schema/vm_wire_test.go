package schema_test

import (
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
	// scsi0: PVE 9 create-time volume spec on LVM storage.
	if got, want := p["scsi0"], "local-lvm:8G"; got != want {
		t.Errorf("scsi0 = %v, want %q (PVE volume-spec — LVM gets pool:size)", got, want)
	}
	// scsihw is VM-wide; virtio-scsi-single is the user's controller.
	if got, want := p["scsihw"], "virtio-scsi-single"; got != want {
		t.Errorf("scsihw = %v, want %q", got, want)
	}
	// scsi0.iothread must be a sibling key with value "1".
	if got := p["scsi0.iothread"]; got != "1" {
		t.Errorf("scsi0.iothread = %T %v, want \"1\" (iothread=true)", got, got)
	}
	// net0: unpinned-MAC virtio on vmbr0.
	if got, want := p["net0"], "virtio,bridge=vmbr0,firewall=0"; got != want {
		t.Errorf("net0 = %v, want %q (no '=' on unpinned-MAC virtio)", got, want)
	}
	// memory: 1GiB → 1048576 KiB as an integer in the wire form.
	if got, want := p["memory"], int64(1<<20); got != want {
		t.Errorf("memory = %T %v, want int64(%d) = KiB form", got, got, want)
	}
	// cpu / cores
	if got, want := p["cpu"], "kvm64"; got != want {
		t.Errorf("cpu = %v, want %q", got, want)
	}
	if got, want := p["cores"], 1; got != want {
		t.Errorf("cores = %T %v, want %v", got, got, want)
	}

	// Drift against PVE's own report must be a no-op once PVE has created
	// the VM. PVE reports scsi0 as "<pool>:<vmid>-disk-<n>,size=<bytes>" —
	// diskMatches normalizes that to (pool, size-bytes) and compares against
	// our desired (pool, size-bytes).
	live := map[string]any{
		"vmid":           9100,
		"name":           "test-vm-01",
		"memory":         1048576,
		"cpu":            "kvm64",
		"cores":          1,
		"scsihw":         "virtio-scsi-single",
		"scsi0":          "local-lvm:vm-9100-disk-0,size=8589934592",
		"scsi0.iothread": "1",
		"net0":           "virtio=52:54:00:aa:bb:cc,bridge=vmbr0,firewall=0",
		"tags":           []any{"pveconform"},
	}
	if update, stop, changed := v.Drift(live); changed {
		t.Errorf("Drift on PVE's own create report is not a no-op: changed=%v stop=%v update=%v", changed, stop, update)
	}
}

// TestVMUnpinnedMacNICDrift — PVE assigns an unpinned MAC at create time.
// Drift MUST not compare MACs (we don't own them) so a converged VM does
// not trigger a needless stop+update on every cycle.
func TestVMUnpinnedMacNICDrift(t *testing.T) {
	v := mustParseUserVM(t)
	live := map[string]any{
		"vmid":           9100,
		"memory":         1048576,
		"cpu":            "kvm64",
		"cores":          1,
		"scsihw":         "virtio-scsi-single",
		"scsi0":          "local-lvm:vm-9100-disk-0,size=8589934592",
		"scsi0.iothread": "1",
		// PVE-assigned MAC on net0 — not in our manifest.
		"net0": "virtio=aa:bb:cc:dd:ee:ff,bridge=vmbr0,firewall=0",
		"tags": []any{"pveconform"},
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
		"tags": []any{"pveconform"},
	}
	upd, _, changed := v.Drift(live)
	if !changed {
		t.Fatal("Drift missed a pinned MAC that PVE overrode")
	}
	if _, ok := upd["net0"]; !ok {
		t.Errorf("drift update should set net0, got %v", upd)
	}
}
