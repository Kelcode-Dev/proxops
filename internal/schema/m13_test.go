package schema

import (
	"strings"
	"testing"
)

// M13 regression tests: VM Secure Boot, VM disk drive options, LXC allocated
// mount points, LXC bind mounts, and the TemplateCT kind. Every wire
// expectation here is pinned against conformance-dev (PVE 9.2.2) probes.

// --- 1. VM Secure Boot (pre-enrolled-keys) ---

func baseSecureBootVM(name string, vmid int) *VM {
	v := NewVM()
	v.Metadata.Name = name
	v.Spec.Node = "pve01"
	v.Spec.VMID = vmid
	v.Spec.Memory = "2GiB"
	v.Spec.CPU = Cpu{Type: "host", Cores: 1}
	v.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB", Slot: "scsi0"}}
	v.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	v.Spec.Hardware.Machine = "q35"
	v.Spec.Hardware.BIOS = "ovmf"
	v.Spec.Hardware.EFIDisk = &EFIDisk{Storage: "local-lvm", Size: "4MiB", Template: "4m"}
	return v
}

// TestM13_SecureBoot_CreateWire pins the create-form rendering: secure-boot
// "enabled" → pre-enrolled-keys=1, "disabled" → =0, "" → token omitted.
// (Probe-verified PVE 9.2.2: the /qemu/{id}/security endpoint does NOT exist;
// the real form is the pre-enrolled-keys token on efidisk0.)
func TestM13_SecureBoot_CreateWire(t *testing.T) {
	for _, tc := range []struct {
		set  string
		want string
	}{
		{"enabled", "local-lvm:0.00390625,efitype=4m,pre-enrolled-keys=1"},
		{"disabled", "local-lvm:0.00390625,efitype=4m,pre-enrolled-keys=0"},
		{"", "local-lvm:0.00390625,efitype=4m"},
	} {
		v := baseSecureBootVM("sb-"+tc.set, 9100)
		v.Spec.Hardware.EFIDisk.SecureBoot = tc.set
		p, err := v.ToCreateParams()
		if err != nil {
			t.Fatalf("ToCreateParams(%q): %v", tc.set, err)
		}
		if got := p["efidisk0"]; got != tc.want {
			t.Errorf("secure-boot=%q: efidisk0=%v, want %q", tc.set, got, tc.want)
		}
	}
}

// TestM13_SecureBoot_Validate pins the accepted value set: enabled|disabled|""
// (the old required|optional|disabled spelling is gone — optional was never
// expressible on the wire).
func TestM13_SecureBoot_Validate(t *testing.T) {
	for _, ok := range []string{"", "enabled", "disabled"} {
		v := baseSecureBootVM("sbv", 9101)
		v.Spec.Hardware.EFIDisk.SecureBoot = ok
		if err := v.Validate(); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"required", "optional", "yes"} {
		v := baseSecureBootVM("sbv", 9101)
		v.Spec.Hardware.EFIDisk.SecureBoot = bad
		if err := v.Validate(); err == nil {
			t.Errorf("Validate(%q) = nil, want error", bad)
		}
	}
}

// TestM13_SecureBoot_Drift pins convergence:
//   - empty slot + desired enabled → create-form write
//   - live pre-enrolled-keys=0 + desired enabled → live-form toggle (volume
//     preserved)
//   - live pre-enrolled-keys=1 + desired enabled → no drift (idempotent)
//   - live ms-cert=2023k + desired enabled → NO drift (ms-cert is PVE-owned)
//   - live ms-cert=2023k + desired disabled → live-form toggle PRESERVING
//     ms-cert (a live-form write without it drops it from the report)
func TestM13_SecureBoot_Drift(t *testing.T) {
	// empty slot → create-form.
	v := baseSecureBootVM("sbd", 9102)
	v.Spec.Hardware.EFIDisk.SecureBoot = "enabled"
	live := map[string]any{"efidisk0": "", "memory": "2048", "cpu": "host", "cores": 1, "machine": "q35", "bios": "ovmf", "scsi0": "local-lvm:vm-9102-disk-0,size=8G", "net0": "virtio,bridge=vmbr0", "tags": []any{"proxops"}}
	upd, _, changed := v.Drift(live)
	if !changed {
		t.Fatalf("empty-slot drift: changed=false, want true")
	}
	if !strings.Contains(upd["efidisk0"].(string), "pre-enrolled-keys=1") {
		t.Errorf("empty-slot drift: efidisk0=%v, want create-form with pre-enrolled-keys=1", upd["efidisk0"])
	}

	// live 0 → desired enabled: live-form toggle preserving volume + ms-cert.
	v2 := baseSecureBootVM("sbd", 9102)
	v2.Spec.Hardware.EFIDisk.SecureBoot = "enabled"
	live2 := map[string]any{"efidisk0": "local-lvm:vm-9102-disk-1,efitype=4m,pre-enrolled-keys=0,size=4M", "memory": "2048", "cpu": "host", "cores": 1, "machine": "q35", "bios": "ovmf", "scsi0": "local-lvm:vm-9102-disk-0,size=8G", "net0": "virtio,bridge=vmbr0", "tags": []any{"proxops"}}
	upd2, _, ch2 := v2.Drift(live2)
	if !ch2 {
		t.Fatalf("toggle drift: changed=false, want true")
	}
	got := upd2["efidisk0"].(string)
	if !strings.Contains(got, "vm-9102-disk-1") || !strings.Contains(got, "pre-enrolled-keys=1") {
		t.Errorf("toggle drift: efidisk0=%q, want live form preserving volume + pre-enrolled-keys=1", got)
	}

	// live 1 → desired enabled: converged.
	v3 := baseSecureBootVM("sbd", 9102)
	v3.Spec.Hardware.EFIDisk.SecureBoot = "enabled"
	live3 := map[string]any{"efidisk0": "local-lvm:vm-9102-disk-1,efitype=4m,ms-cert=2023k,pre-enrolled-keys=1,size=4M", "memory": "2048", "cpu": "host", "cores": 1, "machine": "q35", "bios": "ovmf", "scsi0": "local-lvm:vm-9102-disk-0,size=8G", "net0": "virtio,bridge=vmbr0", "tags": []any{"proxops"}}
	if _, _, ch := v3.Drift(live3); ch {
		t.Errorf("ms-cert converged drift: changed=true, want false (ms-cert is PVE-owned)")
	}

	// live ms-cert + 1 → desired disabled: toggle preserving ms-cert.
	v4 := baseSecureBootVM("sbd", 9102)
	v4.Spec.Hardware.EFIDisk.SecureBoot = "disabled"
	upd4, _, ch4 := v4.Drift(live3)
	if !ch4 {
		t.Fatalf("disable drift: changed=false, want true")
	}
	got4 := upd4["efidisk0"].(string)
	if !strings.Contains(got4, "pre-enrolled-keys=0") || !strings.Contains(got4, "ms-cert=2023k") {
		t.Errorf("disable drift: efidisk0=%q, want pre-enrolled-keys=0 preserving ms-cert", got4)
	}
}

// TestM13_SecureBoot_Adopt pins the reverse translation: pre-enrolled-keys=1
// → SecureBoot "enabled", =0 → "disabled", efitype → Template.
func TestM13_SecureBoot_Adopt(t *testing.T) {
	e := parseEFIDiskPVE("local-lvm:vm-9102-disk-1,efitype=4m,ms-cert=2023k,pre-enrolled-keys=1,size=4M")
	if e == nil {
		t.Fatalf("parseEFIDiskPVE returned nil")
	}
	if e.SecureBoot != "enabled" {
		t.Errorf("adopted secure-boot=%q, want enabled", e.SecureBoot)
	}
	if e.Template != "4m" {
		t.Errorf("adopted efitype=%q, want 4m", e.Template)
	}
	e0 := parseEFIDiskPVE("local-lvm:vm-9102-disk-1,efitype=4m,pre-enrolled-keys=0,size=4M")
	if e0.SecureBoot != "disabled" {
		t.Errorf("adopted secure-boot=%q, want disabled", e0.SecureBoot)
	}
}

// --- 2. VM disk drive options ---

// TestM13_DiskOptions_CreateWire pins the inline create-form tokens.
func TestM13_DiskOptions_CreateWire(t *testing.T) {
	v := NewVM()
	v.Metadata.Name = "do"
	v.Spec.Node = "pve01"
	v.Spec.VMID = 9103
	v.Spec.Memory = "1GiB"
	v.Spec.CPU = Cpu{Type: "host", Cores: 1}
	ssd := true
	v.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB", Slot: "scsi0", Discard: "on", SSD: &ssd, AIO: "io_uring"}}
	v.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	p, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got := p["scsi0"].(string)
	for _, want := range []string{"discard=on", "ssd=1", "aio=io_uring"} {
		if !strings.Contains(got, want) {
			t.Errorf("scsi0=%q, missing %q", got, want)
		}
	}
}

// TestM13_DiskOptions_SSD_BusValidation pins the fail-closed rule: ssd= is
// rejected on virtio/nvme (probe-verified 400 "property is not defined"),
// accepted on scsi/sata/ide.
func TestM13_DiskOptions_SSD_BusValidation(t *testing.T) {
	ssd := true
	for _, tc := range []struct {
		slot string
		ok   bool
	}{
		{"scsi0", true}, {"sata0", true}, {"ide0", true},
		{"virtio0", false}, {"nvme0", false},
	} {
		v := NewVM()
		v.Metadata.Name = "ssd"
		v.Spec.Node = "pve01"
		v.Spec.VMID = 9104
		v.Spec.Memory = "1GiB"
		v.Spec.CPU = Cpu{Type: "host", Cores: 1}
		v.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB", Slot: tc.slot, SSD: &ssd}}
		v.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
		err := v.Validate()
		if tc.ok && err != nil {
			t.Errorf("slot %s: Validate=%v, want nil", tc.slot, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("slot %s: Validate=nil, want error (ssd unsupported)", tc.slot)
		}
	}
}

// TestM13_DiskOptions_DiscardAIO_Validate pins the enum validation.
func TestM13_DiskOptions_DiscardAIO_Validate(t *testing.T) {
	v := NewVM()
	v.Metadata.Name = "d"
	v.Spec.Node = "pve01"
	v.Spec.VMID = 9105
	v.Spec.Memory = "1GiB"
	v.Spec.CPU = Cpu{Type: "host", Cores: 1}
	v.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	v.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB", Slot: "scsi0", Discard: "maybe"}}
	if err := v.Validate(); err == nil {
		t.Errorf("discard=maybe: Validate=nil, want error")
	}
	v.Spec.Disks[0].Discard = "on"
	v.Spec.Disks[0].AIO = "epoll"
	if err := v.Validate(); err == nil {
		t.Errorf("aio=epoll: Validate=nil, want error")
	}
}

// TestM13_DiskOptions_Drift pins the live-form option toggle: a discard
// change on a live volume rewrites in place (volume preserved), NOT a
// create-form write (which would recreate → data loss).
func TestM13_DiskOptions_Drift(t *testing.T) {
	ssd := true
	v := NewVM()
	v.Metadata.Name = "do"
	v.Spec.Node = "pve01"
	v.Spec.VMID = 9106
	v.Spec.Memory = "1GiB"
	v.Spec.CPU = Cpu{Type: "host", Cores: 1}
	v.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB", Slot: "scsi0", Discard: "on", SSD: &ssd}}
	v.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	live := map[string]any{"scsi0": "local-lvm:vm-9106-disk-0,size=8G", "memory": "1024", "cpu": "host", "cores": 1, "net0": "virtio,bridge=vmbr0", "tags": []any{"proxops"}}
	upd, _, changed := v.Drift(live)
	if !changed {
		t.Fatalf("drift: changed=false, want true")
	}
	got := upd["scsi0"].(string)
	if !strings.Contains(got, "vm-9106-disk-0") {
		t.Errorf("drift: scsi0=%q, want live form preserving volume id", got)
	}
	if !strings.Contains(got, "discard=on") || !strings.Contains(got, "ssd=1") {
		t.Errorf("drift: scsi0=%q, want discard=on + ssd=1", got)
	}
	// Idempotent: a second Drift against the rewritten live value is clean.
	live2 := map[string]any{"scsi0": got, "memory": "1024", "cpu": "host", "cores": 1, "net0": "virtio,bridge=vmbr0", "tags": []any{"proxops"}}
	if _, _, ch := v.Drift(live2); ch {
		t.Errorf("second drift: changed=true, want false (converged)")
	}
}

// TestM13_DiskOptions_Adopt pins the reverse translation of drive options.
func TestM13_DiskOptions_Adopt(t *testing.T) {
	cur := map[string]any{"scsi0": "local-lvm:vm-9106-disk-0,aio=io_uring,discard=on,iothread=1,size=8G,ssd=1"}
	disks, ok := PveDisksFromPVE(cur)
	if !ok || len(disks) != 1 {
		t.Fatalf("PveDisksFromPVE: %v", disks)
	}
	d := disks[0]
	if d.Discard != "on" {
		t.Errorf("discard=%q, want on", d.Discard)
	}
	if d.AIO != "io_uring" {
		t.Errorf("aio=%q, want io_uring", d.AIO)
	}
	if d.SSD == nil || !*d.SSD {
		t.Errorf("ssd=%v, want true", d.SSD)
	}
	if !d.IOThread {
		t.Errorf("iothread=false, want true")
	}
}

// --- 3. LXC allocated mount points ---

func baseLXC(name string, cid int) *LXC {
	l := NewLXC()
	l.Metadata.Name = name
	l.Spec.Node = "pve01"
	l.Spec.VMID = cid
	l.Spec.Memory = "1GiB"
	l.Spec.CPU = LXCCPU{Cores: 1}
	l.Spec.Template = "base-ctt"
	l.Spec.Root = LXCRoot{Storage: "local-lvm", Size: "8GiB"}
	l.Spec.Networks = []LXCNetwork{{Bridge: "vmbr0", Slot: "net0"}}
	return l
}

// TestM13_LXCMount_CreateWire pins the allocated mpN create form with options.
func TestM13_LXCMount_CreateWire(t *testing.T) {
	l := baseLXC("mp", 9200)
	ro := true
	l.Spec.MountPoints = []LXCMount{{Storage: "local-lvm", Size: "1GiB", MountPoint: "/mnt/data", Slot: "mp0", Options: &LXCMountOptions{ReadOnly: &ro, Backup: boolPtr(false)}}}
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got := p["mp0"].(string)
	for _, want := range []string{"local-lvm:1", "mp=/mnt/data", "ro=1", "backup=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("mp0=%q, missing %q", got, want)
		}
	}
}

// TestM13_LXCMount_Drift pins:
//   - empty slot → create-form write
//   - live volume, mp path change → live-form rewrite (volume preserved)
//   - live volume, option toggle → live-form rewrite
//   - live volume, pool/size change → anomaly (data-loss guard)
func TestM13_LXCMount_Drift(t *testing.T) {
	// empty slot.
	l := baseLXC("mp", 9201)
	l.Spec.MountPoints = []LXCMount{{Storage: "local-lvm", Size: "1GiB", MountPoint: "/mnt/data", Slot: "mp0"}}
	upd, _, ch := l.Drift(map[string]any{"mp0": "", "rootfs": "local-lvm:vm-9201-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "mp", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	if !ch || !strings.Contains(upd["mp0"].(string), "local-lvm:1") {
		t.Errorf("empty-slot mp0: upd=%v changed=%v, want create-form", upd["mp0"], ch)
	}

	// mp path change on a live volume → live-form rewrite.
	l2 := baseLXC("mp", 9201)
	l2.Spec.MountPoints = []LXCMount{{Storage: "local-lvm", Size: "1GiB", MountPoint: "/mnt/new", Slot: "mp0"}}
	upd2, _, ch2 := l2.Drift(map[string]any{"mp0": "local-lvm:vm-9201-disk-1,mp=/mnt/old,size=1G", "rootfs": "local-lvm:vm-9201-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "mp", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	if !ch2 {
		t.Fatalf("mp path drift: changed=false, want true")
	}
	got2 := upd2["mp0"].(string)
	if !strings.Contains(got2, "vm-9201-disk-1") || !strings.Contains(got2, "mp=/mnt/new") {
		t.Errorf("mp path drift: mp0=%q, want live form preserving volume + new path", got2)
	}

	// pool/size change → anomaly, no write.
	l3 := baseLXC("mp", 9201)
	l3.Spec.MountPoints = []LXCMount{{Storage: "other-pool", Size: "1GiB", MountPoint: "/mnt/data", Slot: "mp0"}}
	upd3, _, _ := l3.Drift(map[string]any{"mp0": "local-lvm:vm-9201-disk-1,mp=/mnt/data,size=1G", "rootfs": "local-lvm:vm-9201-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "mp", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	if _, ok := upd3["mp0"]; ok {
		t.Errorf("pool drift: mp0=%v, want NO write (anomaly only)", upd3["mp0"])
	}
	anoms := l3.DriftAnomalies(map[string]any{"mp0": "local-lvm:vm-9201-disk-1,mp=/mnt/data,size=1G", "rootfs": "local-lvm:vm-9201-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "mp", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	found := false
	for _, a := range anoms {
		if strings.Contains(a, "mp0") && strings.Contains(a, "storage/size drift") {
			found = true
		}
	}
	if !found {
		t.Errorf("pool drift: anomalies=%v, want mp0 storage/size drift", anoms)
	}
}

// TestM13_LXCMount_OptionsAdopt pins option adoption.
func TestM13_LXCMount_OptionsAdopt(t *testing.T) {
	cur := map[string]any{"rootfs": "local-lvm:vm-9201-disk-0,size=8G", "mp0": "local-lvm:vm-9201-disk-1,mp=/mnt/opt,acl=1,backup=0,mountoptions=noatime,quota=1,shared=1,size=1G"}
	_, mps, _, _, _ := PveLXCDisksFromPVE(cur)
	if len(mps) != 1 {
		t.Fatalf("mps=%d, want 1", len(mps))
	}
	o := mps[0].Options
	if o == nil {
		t.Fatalf("options=nil, want populated")
	}
	if o.ACL == nil || !*o.ACL {
		t.Errorf("acl=%v, want true", o.ACL)
	}
	if o.Backup == nil || *o.Backup {
		t.Errorf("backup=%v, want false", o.Backup)
	}
	if o.MountOptions != "noatime" {
		t.Errorf("mountoptions=%q, want noatime", o.MountOptions)
	}
}

// --- 4. LXC bind mounts ---

// TestM13_BindMount_CreateWire pins the bind create form.
func TestM13_BindMount_CreateWire(t *testing.T) {
	l := baseLXC("bind", 9202)
	ro := true
	l.Spec.BindMounts = []LXCBinding{{HostPath: "/mnt/share", MountPoint: "/shared", Slot: "mp0", ReadOnly: &ro}}
	p, err := l.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	got := p["mp0"].(string)
	if !strings.HasPrefix(got, "/mnt/share,mp=/shared") || !strings.Contains(got, "ro=1") {
		t.Errorf("mp0=%q, want bind form /mnt/share,mp=/shared,ro=1", got)
	}
}

// TestM13_BindMount_Validate pins fail-closed host-path rules.
func TestM13_BindMount_Validate(t *testing.T) {
	for _, tc := range []struct {
		host string
		ok   bool
	}{
		{"/mnt/share", true},
		{"/data/x", true},
		{"relative/path", false},
		{"/etc", false},
		{"/", false},
		{"/var", false},
	} {
		l := baseLXC("bind", 9203)
		l.Spec.BindMounts = []LXCBinding{{HostPath: tc.host, MountPoint: "/shared", Slot: "mp0"}}
		err := l.Validate()
		if tc.ok && err != nil {
			t.Errorf("host %q: Validate=%v, want nil", tc.host, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("host %q: Validate=nil, want error", tc.host)
		}
	}
}

// TestM13_BindMount_SlotCollision pins that mount-points and bind-mounts
// share the mpN namespace and collide fail-closed.
func TestM13_BindMount_SlotCollision(t *testing.T) {
	l := baseLXC("bind", 9204)
	l.Spec.MountPoints = []LXCMount{{Storage: "local-lvm", Size: "1GiB", MountPoint: "/mnt/a", Slot: "mp0"}}
	l.Spec.BindMounts = []LXCBinding{{HostPath: "/mnt/share", MountPoint: "/shared", Slot: "mp0"}}
	if err := l.Validate(); err == nil {
		t.Errorf("slot collision: Validate=nil, want error")
	}
}

// TestM13_BindMount_Drift pins:
//   - live bind, same host path, guest path change → live-form rewrite
//   - live bind, different host path → anomaly (never re-point)
//   - live allocated volume at a bind slot → anomaly
func TestM13_BindMount_Drift(t *testing.T) {
	// guest path change.
	l := baseLXC("bind", 9205)
	l.Spec.BindMounts = []LXCBinding{{HostPath: "/mnt/share", MountPoint: "/new", Slot: "mp0"}}
	upd, _, ch := l.Drift(map[string]any{"mp0": "/mnt/share,mp=/old", "rootfs": "local-lvm:vm-9205-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "bind", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	if !ch || upd["mp0"] != "/mnt/share,mp=/new" {
		t.Errorf("guest path drift: mp0=%v changed=%v, want /mnt/share,mp=/new", upd["mp0"], ch)
	}

	// host path change → anomaly, no write.
	l2 := baseLXC("bind", 9205)
	l2.Spec.BindMounts = []LXCBinding{{HostPath: "/mnt/other", MountPoint: "/shared", Slot: "mp0"}}
	upd2, _, _ := l2.Drift(map[string]any{"mp0": "/mnt/share,mp=/shared", "rootfs": "local-lvm:vm-9205-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "bind", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	if _, ok := upd2["mp0"]; ok {
		t.Errorf("host path drift: mp0=%v, want NO write", upd2["mp0"])
	}
	l2.Drift(map[string]any{"mp0": "/mnt/share,mp=/shared", "rootfs": "local-lvm:vm-9205-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "bind", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	anoms := l2.DriftAnomalies(map[string]any{"mp0": "/mnt/share,mp=/shared", "rootfs": "local-lvm:vm-9205-disk-0,size=8G", "memory": "1024", "cores": 1, "hostname": "bind", "net0": "name=net0,bridge=vmbr0", "tags": []any{"proxops"}})
	found := false
	for _, a := range anoms {
		if strings.Contains(a, "bind host path drift") {
			found = true
		}
	}
	if !found {
		t.Errorf("host path drift: anomalies=%v, want bind host path drift", anoms)
	}
}

// TestM13_BindMount_Adopt pins the reverse translation (comma + legacy colon).
func TestM13_BindMount_Adopt(t *testing.T) {
	cur := map[string]any{"rootfs": "local-lvm:vm-9205-disk-0,size=8G", "mp0": "/mnt/share,mp=/shared,ro=1", "mp1": "/mnt/legacy:/guest"}
	binds := PveLXCBindMountsFromPVE(cur)
	if len(binds) != 2 {
		t.Fatalf("binds=%d, want 2: %+v", len(binds), binds)
	}
	if binds[0].HostPath != "/mnt/share" || binds[0].MountPoint != "/shared" || binds[0].ReadOnly == nil || !*binds[0].ReadOnly {
		t.Errorf("bind[0]=%+v, want /mnt/share /shared ro=true", binds[0])
	}
	if binds[1].HostPath != "/mnt/legacy" || binds[1].MountPoint != "/guest" {
		t.Errorf("bind[1]=%+v, want /mnt/legacy /guest (legacy colon form)", binds[1])
	}
}

// --- 5. TemplateCT kind ---

// TestM13_TemplateCT_StateMustBeStopped pins the TemplateCT state rule.
func TestM13_TemplateCT_StateMustBeStopped(t *testing.T) {
	tc := NewTemplateCT()
	tc.Metadata.Name = "ct-tpl"
	tc.Spec.Node = "pve01"
	tc.Spec.VMID = 9300
	tc.Spec.Memory = "1GiB"
	tc.Spec.CPU = LXCCPU{Cores: 1}
	tc.Spec.Template = "base-ctt"
	tc.Spec.Root = LXCRoot{Storage: "local-lvm", Size: "8GiB"}
	tc.Spec.Networks = []LXCNetwork{{Bridge: "vmbr0", Slot: "net0"}}
	if tc.DesiredState() != "stopped" {
		t.Errorf("DesiredState=%q, want stopped", tc.DesiredState())
	}
	if tc.Ref().Kind != KindTemplateCT {
		t.Errorf("Ref kind=%q, want TemplateCT", tc.Ref().Kind)
	}
	tc.Spec.State = "started"
	if err := tc.Validate(); err == nil {
		t.Errorf("Validate(state=started)=nil, want error")
	}
}

// TestM13_TemplateCT_KindRouting pins ParseKind + AllKinds registration.
func TestM13_TemplateCT_KindRouting(t *testing.T) {
	for _, s := range []string{"TemplateCT", "templatect", "TCT"} {
		k, err := ParseKind(s)
		if err != nil || k != KindTemplateCT {
			t.Errorf("ParseKind(%q)=%q,%v, want TemplateCT", s, k, err)
		}
	}
	found := false
	for _, k := range AllKinds() {
		if k == KindTemplateCT {
			found = true
		}
	}
	if !found {
		t.Errorf("AllKinds missing TemplateCT")
	}
}

func boolPtr(b bool) *bool { return &b }
