package schema

import (
	"strings"
	"testing"
)

// M11+ (cloud-init E2E hardening): DiskImage artifact kind + VM disk
// import-from seeding + the PVE 9.2 sshkeys wire grammar.
//
// All wire expectations here are pinned by live probes on conformance-dev
// (PVE 9.2.2, 2026-09-13); see docs/GAPS.md "M11 cloud-init E2E".

func TestDiskImage_Validate(t *testing.T) {
	base := func() *DiskImage {
		d := NewDiskImage()
		d.Metadata.Name = "debian-13-cloud"
		d.Spec.Node = "pve01"
		d.Spec.Storage = "local"
		d.Spec.Filename = "debian-13-genericcloud-amd64.qcow2"
		d.Spec.URL = "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
		return d
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("valid DiskImage rejected: %v", err)
	}
	t.Run("ext", func(t *testing.T) {
		for _, fn := range []string{"a.qcow2", "a.vmdk", "a.raw", "a.QCOW2"} {
			d := base()
			d.Spec.Filename = fn
			if err := d.Validate(); err != nil {
				t.Errorf("filename %q rejected: %v", fn, err)
			}
		}
		// PVE 9.2 import pool rejects these (probed: .qcow/.img/.iso → 400
		// "invalid filename or wrong extension").
		for _, fn := range []string{"a.qcow", "a.img", "a.iso", "a.tar.zst", "a"} {
			d := base()
			d.Spec.Filename = fn
			if err := d.Validate(); err == nil {
				t.Errorf("filename %q accepted, want reject", fn)
			}
		}
	})
	t.Run("missing-fields", func(t *testing.T) {
		for _, f := range []func(d *DiskImage){
			func(d *DiskImage) { d.Spec.Node = ""; d.Spec.Nodes = nil },
			func(d *DiskImage) { d.Spec.Storage = "" },
			func(d *DiskImage) { d.Spec.Filename = "" },
			func(d *DiskImage) { d.Spec.URL = "" },
		} {
			d := base()
			f(d)
			if err := d.Validate(); err == nil {
				t.Error("missing field accepted, want reject")
			}
		}
	})
}

func TestDiskImage_ToCreateParams(t *testing.T) {
	d := NewDiskImage()
	d.Metadata.Name = "img"
	d.Spec.Node = "pve01"
	d.Spec.Storage = "local"
	d.Spec.Filename = "x.qcow2"
	d.Spec.URL = "https://example.invalid/x.qcow2"
	p, err := d.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if p["content"] != "import" {
		t.Errorf("content = %v, want import", p["content"])
	}
	if p["storage"] != "local" || p["filename"] != "x.qcow2" {
		t.Errorf("storage/filename = %v/%v", p["storage"], p["filename"])
	}
	if d.Volid() != "local:import/x.qcow2" {
		t.Errorf("Volid = %q", d.Volid())
	}
}

// A VM disk seeded from a DiskImage renders PVE's import-from create form:
// "<pool>:0,import-from=<volid>". The size token MUST be 0 (probed: any other
// size → 400 "'import-from' requires special syntax").
func TestVM_DiskImage_CreateParams(t *testing.T) {
	vm := baseCloudInitVM("vm-img")
	vm.Spec.Disks = []Disk{{Storage: "local-lvm", Slot: "scsi0", Image: "debian-13-cloud"}}
	img := NewDiskImage()
	img.Metadata.Name = "debian-13-cloud"
	img.Spec.Node = "pve01"
	img.Spec.Storage = "local"
	img.Spec.Filename = "debian-13-genericcloud-amd64.qcow2"
	img.Spec.URL = "https://example.invalid/x.qcow2"
	if err := ResolveArtifactRefs([]Resource{vm, img}); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}
	p, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	want := "local-lvm:0,import-from=local:import/debian-13-genericcloud-amd64.qcow2"
	if p["scsi0"] != want {
		t.Errorf("scsi0 = %v, want %v", p["scsi0"], want)
	}
}

func TestVM_DiskImage_Validation(t *testing.T) {
	vm := baseCloudInitVM("vm-img-bad")
	vm.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "10GiB", Image: "img"}}
	if err := vm.Validate(); err == nil {
		t.Fatal("size+image accepted, want reject (size is derived from the image)")
	}
	vm2 := baseCloudInitVM("vm-img-ok")
	vm2.Spec.Disks = []Disk{{Storage: "local-lvm", Image: "img"}}
	if err := vm2.Validate(); err != nil {
		t.Fatalf("image-only disk rejected: %v", err)
	}
}

func TestVM_DiskImage_Deps(t *testing.T) {
	vm := baseCloudInitVM("vm-img-deps")
	vm.Spec.Disks = []Disk{{Storage: "local-lvm", Image: "img-a"}, {Storage: "local-lvm", Slot: "scsi1", Image: "img-b"}}
	deps := vm.Deps()
	if len(deps) != 2 {
		t.Fatalf("Deps = %v, want 2", deps)
	}
	if deps[0] != (Ref{Kind: KindDiskImage, Name: "img-a"}) || deps[1] != (Ref{Kind: KindDiskImage, Name: "img-b"}) {
		t.Errorf("Deps = %v", deps)
	}
}

func TestVM_DiskImage_ResolveFailsClosed(t *testing.T) {
	vm := baseCloudInitVM("vm-img-missing")
	vm.Spec.Disks = []Disk{{Storage: "local-lvm", Image: "nope"}}
	if err := ResolveArtifactRefs([]Resource{vm}); err == nil {
		t.Fatal("unknown DiskImage reference accepted, want fail-closed")
	} else if !strings.Contains(err.Error(), "unknown DiskImage") {
		t.Errorf("err = %v", err)
	}
	// Wrong-node placement fails closed (same rule as ISO/CTTemplate).
	vm2 := baseCloudInitVM("vm-img-wrongnode")
	vm2.Spec.Disks = []Disk{{Storage: "local-lvm", Image: "img"}}
	img := NewDiskImage()
	img.Metadata.Name = "img"
	img.Spec.Node = "pve02"
	img.Spec.Storage = "local"
	img.Spec.Filename = "x.qcow2"
	img.Spec.URL = "https://example.invalid/x.qcow2"
	if err := ResolveArtifactRefs([]Resource{vm2, img}); err == nil {
		t.Fatal("cross-node DiskImage reference accepted, want fail-closed")
	}
}

// Drift semantics for image-seeded disks:
//   - empty live slot → safe create-form write (re-import)
//   - live volume at the slot → pool-only compare; the import-from option and
//     the image-derived size are NEVER re-reported by PVE, so a live
//     "local-lvm:vm-N-disk-0,size=3G" against desired image "img" with pool
//     local-lvm is CONVERGED (no flap, no data-destroying re-import).
func TestVM_DiskImage_Drift(t *testing.T) {
	mkVM := func() *VM {
		vm := baseCloudInitVM("vm-img-drift")
		vm.Spec.Disks = []Disk{{Storage: "local-lvm", Slot: "scsi0", Image: "img"}}
		img := NewDiskImage()
		img.Metadata.Name = "img"
		img.Spec.Node = "pve01"
		img.Spec.Storage = "local"
		img.Spec.Filename = "x.qcow2"
		img.Spec.URL = "https://example.invalid/x.qcow2"
		if err := ResolveArtifactRefs([]Resource{vm, img}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		return vm
	}
	t.Run("live-imported-converged", func(t *testing.T) {
		vm := mkVM()
		live := baseLiveVM("9100", "vm-img-drift")
		// PVE's report after an import: plain volume, NO import-from token.
		live["scsi0"] = "local-lvm:vm-9100-disk-0,size=3G"
		if _, _, changed := vm.Drift(live); changed {
			t.Errorf("changed=true on converged imported disk, want false")
		}
	})
	t.Run("empty-slot-reimport", func(t *testing.T) {
		vm := mkVM()
		live := baseLiveVM("9100", "vm-img-drift")
		live["scsi0"] = "none"
		upd, _, changed := vm.Drift(live)
		if !changed {
			t.Fatal("changed=false on empty slot, want re-import")
		}
		want := "local-lvm:0,import-from=local:import/x.qcow2"
		if upd["scsi0"] != want {
			t.Errorf("upd[scsi0] = %v, want %v", upd["scsi0"], want)
		}
	})
	t.Run("live-pool-mismatch-anomaly", func(t *testing.T) {
		vm := mkVM()
		live := baseLiveVM("9100", "vm-img-drift")
		live["scsi0"] = "other-pool:vm-9100-disk-0,size=3G"
		upd, _, changed := vm.Drift(live)
		if changed {
			t.Errorf("changed=true on live pool mismatch, want anomaly-only; upd=%v", upd)
		}
		anoms := vm.DriftAnomalies(live)
		found := false
		for _, a := range anoms {
			if strings.Contains(a, "storage/size drift") {
				found = true
			}
		}
		if !found {
			t.Errorf("no disk drift anomaly surfaced: %v", anoms)
		}
	})
}

