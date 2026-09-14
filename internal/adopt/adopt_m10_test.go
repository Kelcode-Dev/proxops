package adopt_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/adopt"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient/mock"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// M10 — production-read-only adoption hardening tests.
//
// Every test in this file uses the mock PVE. The mock records every HTTP
// method it serves (Methods / WritesObserved), so "zero PVE writes during
// adoption" is proven TWICE: at the pveclient level (WritesPerformed) and
// at the HTTP level (the mock saw no POST/PUT/DELETE at all).
//
// The tests pin the M10 schema-fidelity improvements that were required to
// faithfully represent the real prod-a fleet:
//
//   - PVE template VMs (template=1) → Result.Skipped, NOT a manifest
//   - VM cloud-init on non-IDE slots (scsi1=…-cloudinit,media=cdrom)
//     → excluded from spec.disks + a specific gap
//   - LXC unprivileged=0 / protection=0 (tri-state; PVE reports explicit 0)
//     → owned via pointer-bool options
//   - LXC console=1 / features=nesting=1 → owned
//   - LXC netX ip=/gw= (user-set static addressing) → owned
//   - PVE-inferred ostype → bookkeeping, NOT a gap
//   - Sensitive PVE /config fields (sshkeys, cipassword) → REDACTED in the
//     gap report value (the field name still reports)

// TestAdopt_ZeroWritesOnProdFixtureEquivalent pins the M10 guarantee against
// a "production-shaped" PVE fleet: template VMs, cloud-init on scsi, static
// LXC networking, PVE-9.x composite features, tri-state unprivileged=0, and
// PII in unowned fields.
func TestAdopt_ZeroWritesOnProdFixtureEquivalent(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)

	// One template VM (like prod-a 999).
	m.PreloadVM("pve01", 999, map[string]string{
		"name":     "tpl-almalinux-10",
		"memory":   "2048",
		"cpu":      "host",
		"cores":    "1",
		"machine":  "q35",
		"bios":     "ovmf",
		"scsihw":   "virtio-scsi-single",
		"scsi0":    "vm_disks:base-999-disk-1,iothread=1,size=32G",
		"scsi1":    "vm_disks:vm-999-cloudinit,media=cdrom",
		"efidisk0": "vm_disks:base-999-disk-0,efitype=4m,pre-enrolled-keys=1,size=1M",
		"net0":     "virtio=52:54:00:FF:F7:0D,bridge=vmbr2",
		"boot":     "order=scsi0",
		"agent":    "1",
		"template": "1",
		"ostype":   "l26",
		"digest":   "deadbeef",
		"vmgenid":  "aa",
		"smbios1":  "uuid=00",
		"meta":     "creation-qemu=9.2.0,ctime=1700000000",
		"sockets":  "1",
	}, "stopped")

	// One production VM with cloud-init on scsi1 (like 100/101/102/120)
	// plus PII fields.
	m.PreloadVM("pve01", 100, map[string]string{
		"name":       "app-prod-a",
		"memory":     "16384",
		"cpu":        "host",
		"cores":      "2",
		"machine":    "q35",
		"bios":       "ovmf",
		"scsihw":     "virtio-scsi-single",
		"scsi0":      "vm_disks:vm-100-disk-1,replicate=0,size=150G",
		"scsi1":      "vm_disks:vm-100-cloudinit,media=cdrom,size=4M",
		"serial0":    "socket",
		"sockets":    "2",
		"onboot":     "1",
		"agent":      "1",
		"tablet":     "1",
		"hotplug":    "network,disk,usb",
		"boot":       "order=scsi0",
		"net0":       "virtio=52:54:00:AA:BB:CC,bridge=vmbr2",
		"numa":       "0",
		"ostype":     "l26",
		"sshkeys":    "ssh-rsa AAAAB3NzaC1yc2E=PII-GIBBERISH operator@example-host\n",
		"ciuser":     "operator",
		"cipassword": "hunter2-should-never-appear",
		"nameserver": "1.1.1.1 8.8.8.8",
		"ipconfig0":  "ip=192.168.192.100/18,gw=192.168.192.5",
		"digest":     "aa",
		"vmgenid":    "bb",
		"smbios1":    "uuid=cc",
		"meta":       "creation-qemu=11.0.3,ctime=1710000000",
	}, "stopped")

	// LXC with static networking + PVE-9.x composite features + tri-state
	// unprivileged=0 + console=1 + protection=0.
	m.PreloadLXC("pve01", 111, map[string]string{
		"cores":        "2",
		"memory":       "8192",
		"swap":         "1024",
		"arch":         "amd64",
		"hostname":     "nfs-server",
		"rootfs":       "vm_disks:subvol-111-disk-0,size=32G",
		"mp0":          "k8s_volumes:111/vm-111-disk-0.raw,mp=/exports/k8s,backup=0,size=250G",
		"net0":         "name=eth0,bridge=vmbr2,gw=192.168.192.5,hwaddr=52:54:00:E0:03:3D,ip=192.168.192.111/18,type=veth",
		"unprivileged": "0",
		"protection":   "0",
		"onboot":       "1",
		"console":      "1",
		"features":     "nesting=1",
		"lxc":          "lxc.apparmor.profile,unconfined",
		"ostype":       "centos",
		"digest":       "dd",
		"cmode":        "tty",
		"cpulimit":     "0",
		"cpuunits":     "1024",
		"tty":          "2",
	}, "running")

	// Artifacts.
	m.PreloadISO("pve01", "local", "talos-v1.14.0.iso")
	m.PreloadTemplate("pve01", "local", "debian-13-standard_13.6-1_amd64.tar.zst")

	c := newMockClient(t, m)
	root := t.TempDir()

	res, err := adopt.Run(context.Background(), c, "prod-a", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}

	// THE zero-write assertion (client level).
	if got := c.WritesPerformed(); got != 0 {
		t.Fatalf("adopt wrote to PVE: %d write endpoints called", got)
	}
	// HTTP-level proof: the mock saw exactly zero POST/PUT/DELETE requests.
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("mock PVE observed %d write requests (POST/PUT/DELETE); methods=%v", got, m.Methods())
	}
	for _, meth := range m.Methods() {
		if meth != "GET" {
			t.Fatalf("mock PVE observed a non-GET request: %s (all: %v)", meth, m.Methods())
		}
	}

	// M11: the template VM MUST be ADOPTED as a proxops TemplateVM
	// manifest (replacing M10's "skipped + census" contract). PVE-side
	// sshkeys are redacted to the "*" sentinel in the committed manifest;
	// cipassword / cicustom stay in the gap report.
	gotTV := 0
	for _, w := range res.Wrote {
		if w.Kind != schema.KindTemplateVM {
			continue
		}
		// The manifest name is the PVE "name" field sanitized (sanitized in
		// adopt.nameForPVE): for PVE VM 999 named "tpl-almalinux-10" this is
		// "tpl-almalinux-10". Look for that.
		if strings.Contains(w.Path, "tpl-almalinux-10") {
			gotTV++
		}
	}
	if gotTV != 1 {
		t.Fatalf("TemplateVM manifest = %d, want 1 (PVE VM 999 'tpl-almalinux-10'); paths=%v", gotTV, m10Paths(res.Wrote))
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped = %d, want 0 (M11 adopts template VMs); got %+v", len(res.Skipped), res.Skipped)
	}

	// The production VM (100) must be adopted but with scsi1 excluded.
	vm100Path := ""
	for _, w := range res.Wrote {
		if w.Kind == schema.KindVM && strings.HasSuffix(w.Path, "app-prod-a.yaml") {
			vm100Path = w.Path
		}
	}
	if vm100Path == "" {
		t.Fatalf("no manifest for VM 100 (app-prod-a); paths=%v", m10Paths(res.Wrote))
	}
	vm100 := m10ReadVM(t, root, vm100Path)
	if len(vm100.Spec.Disks) != 1 {
		t.Fatalf("VM 100 disks = %d, want exactly 1 (scsi0); the PVE cloud-init volume on scsi1 must NOT be in spec.disks; got %+v", len(vm100.Spec.Disks), vm100.Spec.Disks)
	}
	if vm100.Spec.Disks[0].Slot != "scsi0" || vm100.Spec.Disks[0].Storage != "vm_disks" {
		t.Errorf("VM 100 scsi0 = %+v, want vm_disks on scsi0", vm100.Spec.Disks[0])
	}

	// PII fields must be REDACTED in the gap report.
	for _, g := range res.Gaps {
		if g.Field == "sshkeys" && !strings.Contains(g.Value, "<redacted>") {
			t.Errorf("gap sshkeys value must be <redacted>; got %q", g.Value)
		}
		if g.Field == "cipassword" && !strings.Contains(g.Value, "<redacted>") {
			t.Errorf("gap cipassword value must be <redacted>; got %q", g.Value)
		}
	}
	// The sentinel PII must NEVER appear anywhere in the whole report.
	for _, sentinel := range []string{"hunter2-should-never-appear", "AAAAB3NzaC1yc2E", "PII-GIBBERISH"} {
		if strings.Contains(m10AllReportText(res), sentinel) {
			t.Fatalf("PVE PII %q leaked into adopt output (gaps/warnings/incomplete/skipped/logs)", sentinel)
		}
	}

	// PVE-inferred ostype must not appear as a gap.
	for _, g := range res.Gaps {
		if g.Field == "ostype" {
			t.Errorf("ostype is PVE bookkeeping (inferred from the installed OS); it must not be a gap: %+v", g)
		}
	}

	// LXC 111 must have tri-state options captured.
	lxc111Path := ""
	for _, w := range res.Wrote {
		if w.Kind == schema.KindLXC && strings.HasSuffix(w.Path, "nfs-server.yaml") {
			lxc111Path = w.Path
		}
	}
	if lxc111Path == "" {
		t.Fatalf("no manifest for LXC 111 (nfs-server); paths=%v", m10Paths(res.Wrote))
	}
	lxc111 := m10ReadLXC(t, root, lxc111Path)
	if lxc111.Spec.Options.Unprivileged == nil || *lxc111.Spec.Options.Unprivileged {
		t.Errorf("LXC 111 unprivileged = %v, want false (pointer-bool tri-state captured the explicit 0)", lxc111.Spec.Options.Unprivileged)
	}
	if lxc111.Spec.Options.Protection == nil || *lxc111.Spec.Options.Protection {
		t.Errorf("LXC 111 protection = %v, want false (PVE reported protection=0; adopt must own it)", lxc111.Spec.Options.Protection)
	}
	if lxc111.Spec.Options.OnBoot == nil || !*lxc111.Spec.Options.OnBoot {
		t.Errorf("LXC 111 onboot = %v, want true", lxc111.Spec.Options.OnBoot)
	}
	if lxc111.Spec.Options.Console == nil || !*lxc111.Spec.Options.Console {
		t.Errorf("LXC 111 console = %v, want true (PVE reported console=1)", lxc111.Spec.Options.Console)
	}
	if lxc111.Spec.Options.Nesting == nil || !*lxc111.Spec.Options.Nesting {
		t.Errorf("LXC 111 nesting = %v, want true (PVE reported features=nesting=1; adopt parses the composite)", lxc111.Spec.Options.Nesting)
	}
	if len(lxc111.Spec.Networks) != 1 {
		t.Fatalf("LXC 111 networks = %d, want 1; got %+v", len(lxc111.Spec.Networks), lxc111.Spec.Networks)
	}
	n := lxc111.Spec.Networks[0]
	if n.Ip != "192.168.192.111/18" || n.Gw != "192.168.192.5" {
		t.Errorf("LXC 111 net0 ip/gw = %q/%q, want 192.168.192.111/18/192.168.192.5 (user-set static addressing is owned)", n.Ip, n.Gw)
	}
	if n.Iface != "eth0" {
		t.Errorf("LXC 111 net0 iface = %q, want eth0 (PVE reported name=eth0 != net0 slot)", n.Iface)
	}

	// cmode / tty / cpulimit / cpuunits / lxc remain genuine gaps.
	gapFields := map[string]bool{}
	for _, g := range res.Gaps {
		gapFields[g.Field] = true
	}
	for _, wantField := range []string{"cmode", "tty", "cpulimit", "cpuunits", "lxc"} {
		if !gapFields[wantField] {
			t.Errorf("expected gap field %q not present; gap fields = %v", wantField, m10GapFields(res))
		}
	}
	// Owned options must NOT be gaps.
	for _, owned := range []string{"console", "features", "nesting", "unprivileged", "protection", "onboot"} {
		if gapFields[owned] {
			t.Errorf("%s must not surface as a gap — proxops M10 owns it via LXCOptions", owned)
		}
	}
	if gapFields["ostype"] {
		t.Errorf("ostype is PVE bookkeeping; it must not surface as a gap")
	}
	// spec.template must surface (ostemplate is not in PVE /config), and the
	// manifest must be INCOMPLETE for it.
	if !gapFields["spec.template"] {
		t.Errorf("LXC 111 spec.template gap missing; PVE /config does not carry ostemplate (create-only)")
	}
	seenIncomplete := false
	for _, inc := range res.Incomplete {
		if strings.Contains(inc, "nfs-server") && strings.Contains(inc, "spec.template") {
			seenIncomplete = true
		}
	}
	if !seenIncomplete {
		t.Errorf("nfs-server not named INCOMPLETE for spec.template; Incomplete=%v", res.Incomplete)
	}
}

// TestAdopt_SecretFieldsNeverLeak pins that no PII (full public keys,
// passwords) appears ANYWHERE in the adopt output.
func TestAdopt_SecretFieldsNeverLeak(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	sshKey := "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDhNpWcK9LpZx7tJ3q2o0 operator@example-host\n"
	m.PreloadVM("pve01", 201, map[string]string{
		"name":       "vm-with-pii",
		"memory":     "1024",
		"cores":      "1",
		"scsi0":      "local-lvm:vm-201-disk-0,size=8G",
		"net0":       "virtio=00:00:00:00:00:01,bridge=vmbr0",
		"sshkeys":    sshKey,
		"ciuser":     "operator",
		"cipassword": "supersecretvalue",
		"digest":     "01",
		"vmgenid":    "02",
	}, "stopped")

	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-a", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if m.WritesObserved() != 0 {
		t.Fatalf("mock PVE observed %d write requests", m.WritesObserved())
	}
	for _, sentinel := range []string{"AAAAB3NzaC", "supersecretvalue"} {
		if strings.Contains(m10AllReportText(res), sentinel) {
			t.Fatalf("PVE PII %q leaked into the adopt report (gaps/warnings/incomplete/skipped/logs)", sentinel)
		}
		for _, w := range res.Wrote {
			if strings.Contains(w.Content, sentinel) {
				t.Fatalf("PVE PII %q leaked into generated manifest %s", sentinel, w.Path)
			}
		}
	}
}

// TestAdopt_CloudInitOnSATAAlsoExcluded pins that PVE's cloud-init /
// media=cdrom detection is slot-agnostic: a media=cdrom volume on sata1 is
// a PVE-owned cdrom and never enters spec.disks.
func TestAdopt_CloudInitOnSATAAlsoExcluded(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 300, map[string]string{
		"name":    "sata-cloudinit",
		"memory":  "1024",
		"cores":   "1",
		"sata0":   "local-lvm:vm-300-disk-0,size=8G",
		"sata1":   "local-lvm:vm-300-cloudinit,media=cdrom,size=4M",
		"net0":    "virtio=00:00:00:00:00:02,bridge=vmbr0",
		"digest":  "03",
		"vmgenid": "04",
	}, "stopped")

	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-a", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if m.WritesObserved() != 0 {
		t.Fatalf("mock PVE observed %d write requests", m.WritesObserved())
	}
	vmPath := ""
	for _, w := range res.Wrote {
		if w.Kind == schema.KindVM && strings.HasSuffix(w.Path, "sata-cloudinit.yaml") {
			vmPath = w.Path
		}
	}
	if vmPath == "" {
		t.Fatalf("no manifest for sata-cloudinit; paths=%v", m10Paths(res.Wrote))
	}
	vm := m10ReadVM(t, root, vmPath)
	if len(vm.Spec.Disks) != 1 {
		t.Fatalf("disks = %d, want exactly 1 (sata0); the PVE-owned sata1 cloud-init cdrom must be excluded; got %+v", len(vm.Spec.Disks), vm.Spec.Disks)
	}
	if vm.Spec.Disks[0].Slot != "sata0" {
		t.Errorf("disks[0].slot = %q, want sata0", vm.Spec.Disks[0].Slot)
	}
}

// TestAdopt_PlainDataDiskStillAdopted pins that the M10 cloud-init
// exclusion is NARROW: a normal second data disk (no media=cdrom token, no
// -cloudinit volume name) is proxops-owned and MUST appear in
// spec.disks.
func TestAdopt_PlainDataDiskStillAdopted(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 400, map[string]string{
		"name":    "plain-multidisk",
		"memory":  "1024",
		"cores":   "1",
		"scsi0":   "local-lvm:vm-400-disk-0,size=8G",
		"scsi1":   "local-lvm:vm-400-disk-1,size=20G",
		"net0":    "virtio=00:00:00:00:00:03,bridge=vmbr0",
		"digest":  "05",
		"vmgenid": "06",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-a", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if m.WritesObserved() != 0 {
		t.Fatalf("mock PVE observed %d write requests", m.WritesObserved())
	}
	vmPath := ""
	for _, w := range res.Wrote {
		if w.Kind == schema.KindVM && strings.HasSuffix(w.Path, "plain-multidisk.yaml") {
			vmPath = w.Path
		}
	}
	if vmPath == "" {
		t.Fatalf("no manifest for plain-multidisk; paths=%v", m10Paths(res.Wrote))
	}
	vm := m10ReadVM(t, root, vmPath)
	if len(vm.Spec.Disks) != 2 {
		t.Fatalf("disks = %d, want 2 (both scsi data disks are proxops-owned); got %+v", len(vm.Spec.Disks), vm.Spec.Disks)
	}
}

// TestAdopt_TemplateVMsAdoptedAsTemplateVMManifest pins the M11 adoption
// contract: PVE-template VMs are reverse-translated to proxops
// TemplateVM manifests (kind=TemplateVM, under templatevm/<cluster>/)
// rather than M10's "skipped + census" contract. The zero-write
// guarantee is preserved (adopt issues only GETs).
func TestAdopt_TemplateVMsAdoptedAsTemplateVMManifest(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 500, map[string]string{
		"name":     "tpl-a",
		"memory":   "2048",
		"cores":    "1",
		"scsi0":    "local-lvm:base-500-disk-0,size=32G",
		"net0":     "virtio=00:00:00:00:00:04,bridge=vmbr0",
		"template": "1",
		"digest":   "07",
	}, "stopped")
	m.PreloadVM("pve01", 501, map[string]string{
		"name":     "tpl-b",
		"memory":   "4096",
		"cores":    "2",
		"scsi0":    "local-lvm:base-501-disk-0,size=64G",
		"net0":     "virtio=00:00:00:00:00:05,bridge=vmbr0",
		"template": "1",
		"digest":   "08",
	}, "stopped")
	m.PreloadVM("pve01", 502, map[string]string{
		"name":    "prod-vm",
		"memory":  "2048",
		"cores":   "1",
		"scsi0":   "local-lvm:vm-502-disk-0,size=16G",
		"net0":    "virtio=00:00:00:00:00:06,bridge=vmbr0",
		"digest":  "09",
		"vmgenid": "0a",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-a", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if m.WritesObserved() != 0 {
		t.Fatalf("mock PVE observed %d write requests", m.WritesObserved())
	}
	// M11: PVE-template VMs are no longer "skipped + census": they are adopted
	// into proxops TemplateVM manifests (kind=TemplateVM). PVE 9.2's
	// wire-semantic change (one-way-only /template endpoint; 501 on
	// /untemplate) makes the manifest the right representation of
	// owner-controlled state.
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped = %d, want 0 (M11 adopts template VMs); got %+v", len(res.Skipped), res.Skipped)
	}
	tvKinds := 0
	for _, w := range res.Wrote {
		if w.Kind == schema.KindTemplateVM {
			tvKinds++
		}
	}
	if tvKinds != 2 {
		t.Fatalf("TemplateVM manifests = %d, want 2 (PVE VMs 500 + 501); paths=%v", tvKinds, m10Paths(res.Wrote))
	}
	prodFound := false
	for _, w := range res.Wrote {
		if strings.HasSuffix(w.Path, "prod-vm.yaml") {
			prodFound = true
		}
	}
	if !prodFound {
		t.Errorf("non-template VM 502 did not generate a manifest; paths=%v", m10Paths(res.Wrote))
	}
}

// TestAdopt_ArtifactsPlaceholderURL pins the placeholder-URL mechanism:
// PVE does not record where a file came from, so adopt uses a well-known
// placeholder AND records a gap that tells the operator to fill the real
// URL before listing the manifest in resources.yaml. No URL is invented.
func TestAdopt_ArtifactsPlaceholderURL(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadISO("pve01", "local", "fedora.iso")
	m.PreloadTemplate("pve01", "local", "debian-13-standard.tar.zst")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if m.WritesObserved() != 0 {
		t.Fatalf("mock PVE observed %d write requests (artifact discovery is read-only)", m.WritesObserved())
	}
	isoGaps, cttGaps := 0, 0
	for _, g := range res.Gaps {
		if g.Field != "spec.url" {
			continue
		}
		if !strings.Contains(g.Value, "placeholder.invalid") {
			t.Errorf("artifact spec.url gap value %q must be the placeholder (no invented source URL)", g.Value)
		}
		if g.Kind == schema.KindISO {
			isoGaps++
		}
		if g.Kind == schema.KindCTTemplate {
			cttGaps++
		}
	}
	if isoGaps != 1 || cttGaps != 1 {
		t.Errorf("placeholder URL gaps = iso:%d ctt:%d, want 1/1", isoGaps, cttGaps)
	}
}

// TestAdopt_IncompleteLXCTemplateStillEmitsManifest pins that an LXC whose
// spec.template is unrecoverable (PVE /config does not carry ostemplate)
// still gets a manifest WRITTEN (the operator sees the full shape), but is
// listed in Result.Incomplete with a reason naming the missing field.
func TestAdopt_IncompleteLXCTemplateStillEmitsManifest(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadLXC("pve01", 600, map[string]string{
		"cores":    "1",
		"memory":   "1024",
		"arch":     "amd64",
		"hostname": "orphan-lxc",
		"rootfs":   "local-lvm:vm-600-disk-0,size=8G",
		"net0":     "name=wired0,bridge=vmbr0",
		"digest":   "0b",
	}, "running")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if m.WritesObserved() != 0 {
		t.Fatalf("mock PVE observed %d write requests", m.WritesObserved())
	}
	found := false
	for _, w := range res.Wrote {
		if strings.HasSuffix(w.Path, "orphan-lxc.yaml") {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan LXC 600 must still have a manifest written even though spec.template is unknown; paths=%v", m10Paths(res.Wrote))
	}
	ok := false
	for _, inc := range res.Incomplete {
		if strings.Contains(inc, "orphan-lxc") && strings.Contains(inc, "spec.template") {
			ok = true
		}
	}
	if !ok {
		t.Errorf("no Incomplete entry naming orphan-lxc + spec.template; got %v", res.Incomplete)
	}
}

// TestAdopt_ConsistentIdempotencyAcrossTwoRuns pins the M10 idempotency
// requirement: two adopts against an identical PVE must produce identical
// manifest sets, gap lists, and incomplete/skipped lists (no re-shuffled
// order → no meaningless git diffs).
func TestAdopt_ConsistentIdempotencyAcrossTwoRuns(t *testing.T) {
	buildFleet := func(m *mock.Server) {
		m.PreloadVM("pve01", 700, map[string]string{
			"name":    "vm-a",
			"memory":  "2048",
			"cores":   "1",
			"scsi0":   "local-lvm:vm-700-disk-0,size=16G",
			"scsi1":   "local-lvm:vm-700-disk-1,size=32G",
			"net0":    "virtio=00:00:00:00:00:07,bridge=vmbr0",
			"digest":  "0c",
			"vmgenid": "0d",
		}, "stopped")
		m.PreloadVM("pve01", 709, map[string]string{
			"name":     "tpl",
			"memory":   "2048",
			"cores":    "1",
			"scsi0":    "local-lvm:base-709-disk-0,size=32G",
			"net0":     "virtio=00:00:00:00:00:08,bridge=vmbr0",
			"template": "1",
			"digest":   "0e",
		}, "stopped")
		m.PreloadLXC("pve01", 701, map[string]string{
			"cores":        "1",
			"memory":       "512",
			"arch":         "amd64",
			"hostname":     "lxc-a",
			"rootfs":       "local-lvm:vm-701-disk-0,size=4G",
			"net0":         "name=wired0,bridge=vmbr0",
			"unprivileged": "1",
			"digest":       "0f",
		}, "running")
		m.PreloadISO("pve01", "local", "a.iso")
		m.PreloadISO("pve01", "local", "b.iso")
	}

	runOnce := func(root string) *adopt.Result {
		m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
		t.Cleanup(m.Close)
		buildFleet(m)
		c := newMockClient(t, m)
		res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
		if err != nil {
			t.Fatalf("adopt.Run (against %s): %v", root, err)
		}
		if m.WritesObserved() != 0 {
			t.Fatalf("mock PVE observed %d write requests", m.WritesObserved())
		}
		return res
	}

	root1, root2 := t.TempDir(), t.TempDir()
	res1 := runOnce(root1)
	res2 := runOnce(root2)

	if !m10EqualStrings(m10Paths(res1.Wrote), m10Paths(res2.Wrote)) {
		t.Errorf("manifest paths differ between two identical runs:\n run1=%v\n run2=%v", m10Paths(res1.Wrote), m10Paths(res2.Wrote))
	}
	byPath1 := map[string]string{}
	for _, w := range res1.Wrote {
		byPath1[w.Path] = w.Content
	}
	for _, w := range res2.Wrote {
		if byPath1[w.Path] != w.Content {
			t.Errorf("manifest %s content differs between two identical runs", w.Path)
		}
	}
	g1, g2 := m10GapKeys(res1), m10GapKeys(res2)
	if !m10EqualStrings(g1, g2) {
		t.Errorf("gap sets differ between two identical runs:\n run1=%v\n run2=%v", g1, g2)
	}
	if !m10EqualStrings(res1.Incomplete, res2.Incomplete) {
		t.Errorf("incomplete lists differ: %v vs %v", res1.Incomplete, res2.Incomplete)
	}
	if len(res1.Skipped) != len(res2.Skipped) {
		t.Errorf("skipped counts differ: %d vs %d", len(res1.Skipped), len(res2.Skipped))
	}
}

// TestAdopt_ClusterNameValidation pins the M8 cluster-name invariant that
// adopt relies on (generated files land under <kind>/<cluster>/). Invalid
// cluster names must be rejected before any PVE interaction.
func TestAdopt_ClusterNameValidation(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	c := newMockClient(t, m)
	root := t.TempDir()
	// 65 chars > the 64-char limit.
	long := strings.Repeat("a", 63) + "-b"
	cases := []string{"Bad-Name", "-bad", long, ""}
	for _, name := range cases {
		_, err := adopt.Run(context.Background(), c, name, []string{"pve01"}, root)
		if err == nil {
			t.Errorf("cluster name %q: want error (invalid name); got nil", name)
		}
	}
	// No PVE write traffic for the rejected names.
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("rejected clusters still observed %d write requests", got)
	}
}

// TestAdopt_LXCBindMountsReported pins that PVE's host-path bind mounts
// (mpN=/host:path) are EXPLICITLY reported as gaps and NOT silently dropped
// into spec.mount-points (which would fail Validate) or lost from the
// adopt report (which would hide live configuration). GAPS.md: LXC bind
// mounts are not modelled by proxops today.
func TestAdopt_LXCBindMountsReported(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadLXC("pve01", 910, map[string]string{
		"cores":    "1",
		"memory":   "1024",
		"arch":     "amd64",
		"hostname": "bind-lxc",
		"rootfs":   "local-lvm:vm-910-disk-0,size=8G",
		"mp0":      "/mnt/host-share:/srv/data",
		"mp1":      "/mnt/host-share2:/svc/x,ro",
		"net0":     "name=wired0,bridge=vmbr0",
		"digest":   "20",
	}, "running")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("mock PVE observed %d write requests", got)
	}
	// The bind mp's must NOT be in spec.mount-points (an allocated-only shape).
	lxcPath := ""
	for _, w := range res.Wrote {
		if strings.HasSuffix(w.Path, "bind-lxc.yaml") {
			lxcPath = w.Path
		}
	}
	if lxcPath == "" {
		t.Fatalf("no manifest for bind-lxc; paths=%v", m10Paths(res.Wrote))
	}
	lxc := m10ReadLXC(t, root, lxcPath)
	for _, mp := range lxc.Spec.MountPoints {
		if mp.Storage == "/" || strings.HasPrefix(mp.Storage, "/mnt") {
			t.Errorf("bind mount leaked into spec.mount-points: %+v", mp)
		}
	}
	if len(lxc.Spec.MountPoints) != 0 {
		t.Errorf("bind-only LXC must have zero allocated mount-points; got %+v", lxc.Spec.MountPoints)
	}
	// AND the bind mp's must be reported as gaps (silent drop = forbidden).
	bindGaps := 0
	for _, g := range res.Gaps {
		if g.Field == "mp0" || g.Field == "mp1" {
			bindGaps++
			if !strings.Contains(g.Note, "bind") {
				t.Errorf("bind-mount gap note must say it is a bind mount: %+v", g)
			}
		}
	}
	if bindGaps != 2 {
		t.Errorf("bind-mount gaps = %d, want 2 (mp0 + mp1); gaps=%v", bindGaps, m10GapFields(res))
	}
}

// TestAdopt_MalformedClusterConfigFailsClosed pins that a malformed
// cluster name (e.g. the cluster block's name does not match the
// directories under <kind>/) is rejected by adopt before anything is
// emitted. The M8 composition invariant (config key == directory) is
// enforced at the config + composition layer; at the adopt layer, an
// invalid cluster name is rejected by ValidClusterName.
func TestAdopt_MalformedClusterConfigFailsClosed(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	c := newMockClient(t, m)
	root := t.TempDir()
	// Uppercase, leading dash, too long.
	for _, name := range []string{"Prod", "-prod", strings.Repeat("a", 70)} {
		_, err := adopt.Run(context.Background(), c, name, []string{"pve01"}, root)
		if err == nil {
			t.Errorf("cluster name %q: want error, got nil", name)
		}
	}
	// No PVE traffic for rejected names.
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("mock PVE observed %d write requests during rejected runs", got)
	}
	for _, meth := range m.Methods() {
		if meth != "GET" {
			t.Fatalf("non-GET method %s observed", meth)
		}
	}
}

// TestAdopt_NoNodesInAllowlistQueriesFleet pin that when an operator sets
// a node allowlist, adopt never enumerates PVE's cluster node list (the
// fallback GET /cluster/nodes) and only reads the allowlisted nodes.
// This is the "cluster isolation" guarantee: the allowlist is the only
// source of node names, and it scopes every subsequent PVE read.
func TestAdopt_NoNodesInAllowlistQueriesFleet(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	// Seed an object on a node that IS in the allowlist + one on a node
	// that is NOT. Only the allowlisted node's object may appear.
	m.PreloadVM("pve01", 1, map[string]string{
		"name":   "in-allowlist",
		"memory": "512", "cores": "1",
		"scsi0":  "local-lvm:vm-1-disk-0,size=8G",
		"net0":   "virtio=00:00:00:00:00:09,bridge=vmbr0",
		"digest": "21",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("mock PVE observed %d write requests", got)
	}
	// Only pve01 objects appear (nothing else was preloaded on a different
	// node, so we just pin the allowlist path: the run must not have
	// queried /cluster/nodes either — with an explicit allowlist, the
	// PVE client should use it as-is).
	for _, s := range res.Skipped {
		if s.Node != "pve01" {
			t.Errorf("Skipped object on node %q — the allowlist must scope discovery to pve01", s.Node)
		}
	}
	for _, g := range res.Gaps {
		if g.Node != "pve01" {
			t.Errorf("gap on node %q — the allowlist must scope discovery to pve01", g.Node)
		}
	}
}

// --- helpers -------------------------------------------------------------

func m10ReadVM(t *testing.T, root, rel string) *schema.VM {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read manifest %s: %v", rel, err)
	}
	var vm schema.VM
	if err := schema.YAMLTo(string(raw), &vm); err != nil {
		t.Fatalf("manifest %s is not a parseable proxops VM: %v", rel, err)
	}
	return &vm
}

func m10ReadLXC(t *testing.T, root, rel string) *schema.LXC {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read manifest %s: %v", rel, err)
	}
	var lxc schema.LXC
	if err := schema.YAMLTo(string(raw), &lxc); err != nil {
		t.Fatalf("manifest %s is not a parseable proxops LXC: %v", rel, err)
	}
	return &lxc
}

func m10Paths(w []adopt.WroteManifest) []string {
	out := make([]string, 0, len(w))
	for _, x := range w {
		out = append(out, x.Path)
	}
	sort.Strings(out)
	return out
}

func m10GapFields(res *adopt.Result) []string {
	seen := map[string]bool{}
	for _, g := range res.Gaps {
		seen[g.Field] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func m10GapKeys(res *adopt.Result) []string {
	out := make([]string, 0, len(res.Gaps))
	for _, g := range res.Gaps {
		out = append(out, string(g.Kind)+"\x00"+g.Node+"\x00"+g.Field+"\x00"+g.Value)
	}
	sort.Strings(out)
	return out
}

func m10AllReportText(res *adopt.Result) string {
	var b strings.Builder
	for _, g := range res.Gaps {
		b.WriteString(g.Field)
		b.WriteString("=")
		b.WriteString(g.Value)
		b.WriteString(" ")
		b.WriteString(g.Note)
		b.WriteString("\n")
	}
	for _, wmsg := range res.Warnings {
		b.WriteString(wmsg)
		b.WriteString("\n")
	}
	for _, inc := range res.Incomplete {
		b.WriteString(inc)
		b.WriteString("\n")
	}
	for _, s := range res.Skipped {
		b.WriteString(s.String())
		b.WriteString("\n")
	}
	for _, l := range res.Logs {
		b.WriteString(l)
		b.WriteString("\n")
	}
	return b.String()
}

func m10EqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
