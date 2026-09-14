package schema_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// lxcWireManifest mirrors PVE 9.2 LXC shapes: root on local-lvm, one
// unnamed veth on vmbr0, 1GiB / 1 core, and a template reference.
//
// Note: `ResolveArtifactRefs` fills in the ostemplate wire value; this
// test focuses on schema-side fields, so the CTTemplate is not declared
// in the manifest and the `ostemplate` form-key is asserted separately
// when the planner / resolver are involved.
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
  template: debian-13
  root:
    storage: local-lvm
    size: 8GiB
  networks:
    - bridge: vmbr0
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

// TestLXCRootfsWireFormat — rootfs must be "pool:<size-GiB>" (8GiB → "8").
// PVE's documented LXC container-creation form is "STORAGE_ID:SIZE_IN_GiB"
// (pct.conf(5)), and the bare number after the storage id is read in GiB —
// the same unit PVE uses for QEMU scsiN create-time volume specs (empirically
// verified on PVE 9.2: local-lvm:8589934592 → "Volume too large (8.00 EiB)").
func TestLXCRootfsWireFormat(t *testing.T) {
	l := mustParseLXC(t)
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	// 8 GiB → "8" (GiB, not bytes).
	if got, want := p["rootfs"], "local-lvm:8"; got != want {
		t.Errorf("rootfs = %v, want %q (pool:<GiB>)", got, want)
	}
}

// TestLXCNetUnpinnedWireFormat — an LXC NIC with no iface / hwaddr pinned
// must serialize in PVE 9.2's `name=<iface>,bridge=<br>` form. PVE's /lxc
// create rejects a bare model name ("invalid format - value without key, but
// schema does not define a default key"), so `name=` is REQUIRED; the
// default is the slot name (net0).
func TestLXCNetUnpinnedWireFormat(t *testing.T) {
	l := mustParseLXC(t)
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got, ok := p["net0"].(string)
	if !ok {
		t.Fatalf("net0 is %T", p["net0"])
	}
	if want := "name=net0,bridge=vmbr0"; got != want {
		t.Errorf("net0 = %q, want %q", got, want)
	}
}

// TestLXCNetPinnedWireFormat — a pinned iface + hwaddr keeps the
// `name=wired0,bridge=vmbr0,hwaddr=...` form, lower-cased MAC.
func TestLXCNetPinnedWireFormat(t *testing.T) {
	l := mustParseLXC(t)
	l.Spec.Networks[0].Iface = "wired0"
	l.Spec.Networks[0].HWAddr = "AA:BB:CC:DD:EE:90"
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, want := p["net0"].(string), "name=wired0,bridge=vmbr0,hwaddr=aa:bb:cc:dd:ee:90"; got != want {
		t.Errorf("net0 = %q, want %q", got, want)
	}
	// Drift against PVE's normalized report must be a no-op: PVE attaches
	// a PVE-assigned hwaddr and `type=veth` that proxops does not own.
	live := map[string]any{
		"cores":    1,
		"memory":   int64(1024), // PVE MiB count for 1GiB
		"tags":     []any{"proxops"},
		"hostname": "cache-01",
		// PVE-assigned container volume id; no size token PVE reports for
		// the LVM container rootfs. pveDiskInfo treats a missing size as
		// "compatible" so adoption does not churn on unknown sizes.
		"rootfs": "local-lvm:vm-9000-disk-0,size=8G",
		"net0":   "name=wired0,bridge=vmbr0,hwaddr=AA:BB:CC:DD:EE:90,type=veth",
	}
	if _, stop, changed := l.Drift(live); changed {
		t.Errorf("Drift on a pinned-matched LXC reported change (stop=%v): PVE report shape mishandled", stop)
	}
}

// TestLXCMemoryNormalization — 1GiB memory must be emitted as PVE's wire
// form: MiB as an integer (1GiB = 1024 MiB). PVE's LXC /config "memory" is
// documented in MiB (pct.conf(5)).
func TestLXCMemoryNormalization(t *testing.T) {
	l := mustParseLXC(t)
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, want := p["memory"], int64(1024); got != want {
		t.Errorf("memory = %T %v, want int64(%d)", got, got, want)
	}
}

// TestLXCRequiresTemplate — PVE 9.x /lxc create rejects an LXC without
// `ostemplate` ("ostemplate: property is missing and it is not optional").
// The schema encodes this as `spec.template` — a CTTemplate manifest
// reference the planner resolves to the PVE wire value at create time.
//
// The test here verifies LXC.Validate rejects an LXC without spec.template.
func TestLXCRequiresTemplate(t *testing.T) {
	const m = `apiVersion: proxops/v1alpha1
kind: LXC
metadata:
  name: no-template
spec:
  node: pve-dev-01
  vmid: 9500
  memory: 1GiB
  cpu:
    cores: 1
  root:
    storage: local-lvm
    size: 4GiB
`
	l := schema.NewLXC()
	if err := schema.YAMLTo(m, l); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if err := l.Validate(); err == nil {
		t.Fatal("Validate must reject an LXC without spec.template")
	}
}

// TestLXCOSTemplateWireInjection — once the planner resolves spec.template,
// LXC.ToCreateParams emits `ostemplate=<storage>:vztmpl/<filename>`.
//
// The test calls LXC.ToCreateParams AFTER injecting the resolved value via
// the resolveTemplate helper (a direct internal call is not exposed, so the
// test re-parses and resolves via the public schema.ResolveArtifactRefs).
func TestLXCOSTemplateWireInjection(t *testing.T) {
	const ctt = `apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: debian-13
spec:
  nodes: [pve-dev-01]
  storage: local
  filename: debian-13-standard_13.6.1-1_amd64.tar.zst
  url: https://example.com/debian-13-standard_13.6.1-1_amd64.tar.zst
`
	const lxc = `apiVersion: proxops/v1alpha1
kind: LXC
metadata:
  name: cache-01
spec:
  node: pve-dev-01
  vmid: 9501
  memory: 1GiB
  cpu:
    cores: 1
  template: debian-13
  root:
    storage: local-lvm
    size: 4GiB
`
	c := schema.NewCTTemplate()
	if err := schema.YAMLTo(ctt, c); err != nil {
		t.Fatalf("YAMLTo ctt: %v", err)
	}
	l := schema.NewLXC()
	if err := schema.YAMLTo(lxc, l); err != nil {
		t.Fatalf("YAMLTo lxc: %v", err)
	}
	resources := []schema.Resource{c, l}
	if err := schema.ResolveArtifactRefs(resources); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got, ok := p["ostemplate"].(string)
	if !ok {
		t.Fatalf("ostemplate missing from create params: %v", p)
	}
	if want := "local:vztmpl/debian-13-standard_13.6.1-1_amd64.tar.zst"; got != want {
		t.Errorf("ostemplate = %q, want %q", got, want)
	}
}
