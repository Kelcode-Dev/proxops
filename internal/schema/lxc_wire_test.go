package schema_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// lxcWireManifest mirrors the user's LXP shape: root on local-lvm, one
// unpinned veth on vmbr0, 1GiB / 1 core.
const lxcWireManifest = `apiVersion: proxops/v1alpha1
kind: LXC
metadata:
  name: cache-01
spec:
  node: pve-dev-01
  vmid: 9000
  state: stopped
  memory: 1GiB
  cpu:
    cores: 1
  root:
    storage: local-lvm
    size: 8GiB
  networks:
    - model: veth
      bridge: vmbr0
`

func mustParseLXC(t *testing.T) *schema.LXC {
	t.Helper()
	l := schema.NewLXC()
	if err := schema.YAMLTo(lxcWireManifest, l); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if err := l.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return l
}

// TestLXCRootfsWireFormat — rootfs must be a PVE volume-spec
// "<pool>:<size>" (8GiB → "8G"); NOT "pool,size=<bytes>" and not a bare
// "pool". Regression for the same class of parameter-verification error
// that hit the VM scsi0 path.
func TestLXCRootfsWireFormat(t *testing.T) {
	l := mustParseLXC(t)
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, want := p["rootfs"], "local-lvm:8G"; got != want {
		t.Errorf("rootfs = %v, want %q (PVE volume-spec)", got, want)
	}
}

// TestLXCVethUnpinnedWireFormat — an UNPINNED veth must serialize as the
// bare "veth,bridge=vmbr0" form. PVE's comma-separated property parser
// rejects "veth=,bridge=vmbr0" ("missing key in comma-separated list
// property") — the exact class of bug that broke the VM virtio NIC.
func TestLXCVethUnpinnedWireFormat(t *testing.T) {
	l := mustParseLXC(t)
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got, ok := p["net0"].(string)
	if !ok {
		t.Fatalf("net0 is %T", p["net0"])
	}
	if want := "veth,bridge=vmbr0"; got != want {
		t.Errorf("net0 = %q, want %q", got, want)
	}
}

// TestLXCVethPinnedWireFormat — a pinned hwaddr must keep the "veth=MAC,..."
// form, lower-cased.
func TestLXCVethPinnedWireFormat(t *testing.T) {
	l := mustParseLXC(t)
	l.Spec.Networks[0].HWAddr = "AA:BB:CC:DD:EE:90"
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, want := p["net0"].(string), "veth=aa:bb:cc:dd:ee:90,bridge=vmbr0"; got != want {
		t.Errorf("net0 = %q, want %q", got, want)
	}
	// Drift against PVE's report of the same device must be a no-op.
	live := map[string]any{
		"cores":  1,
		"memory": int64(1 << 20),
		"tags":   []any{"pveconform"},
		"rootfs": "local-lvm:local-lvm-ct-9000",
		"net0":   "veth=aa:bb:cc:dd:ee:90,bridge=vmbr0",
	}
	if _, stop, changed := l.Drift(live); changed {
		t.Errorf("Drift on a pinned-matched LXC reported change (stop=%v): PVE report shape mishandled", stop)
	}
}

// TestLXCMemoryNormalization — 1GiB memory must be emitted as PVE's wire
// form: KiB as an integer (1GiB = 1048576 KiB).
func TestLXCMemoryNormalization(t *testing.T) {
	l := mustParseLXC(t)
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, want := p["memory"], int64(1048576); got != want {
		t.Errorf("memory = %T %v, want int64(%d)", got, got, want)
	}
}
