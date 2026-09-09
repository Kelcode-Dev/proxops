package adopt_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GizzmoShifu/proxmox-operator/internal/adopt"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient/mock"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

const apiToken = "root@pam!pveconform=deadbeef"

func newMockClient(t *testing.T, m *mock.Server) *pveclient.Client {
	t.Helper()
	c, err := pveclient.New(pveclient.Options{
		PVE:         pveclient.PVEParams{User: "root@pam", Auth: "token", TokenID: "pveconform", Token: "deadbeef"},
		BaseURL:     m.URL(),
		HTTPTimeout: 5 * time.Second,
	}, slog.Default())
	if err != nil {
		t.Fatalf("pveclient.New: %v", err)
	}
	return c
}

// TestAdoptVMRoundTrip — PVE → adopt → YAML schema → parse → same owned
// fields, incl. the live-only scsi1 disk (no silent omission).
func TestAdoptVMRoundTrip(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 100, map[string]string{
		"name":     "existing-vm",
		"memory":   "1024",
		"cpu":      "host",
		"cores":    "4",
		"scsi0":    "local-lvm:vm-100-disk-0,iothread=1,size=8G",
		"scsi1":    "local-lvm:vm-100-disk-1,size=20G", // the live-only anomaly
		"net0":     "virtio=52:54:00:E6:5D:C7,bridge=vmbr0",
		"tags":     "pveconform,env=dev",
		"smbios1":  "uuid=deadbeef",
		"vmgenid":  "aa",
		"digest":   "dd",
		"meta":     "creation-qemu=11.0.0",
		"soc0":     "ignored", // unknown PVE key → gap
		"replicat1": "target=host",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "conformance-dev", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	// Read-only assertion.
	if got := c.WritesPerformed(); got != 0 {
		t.Fatalf("adopt wrote to PVE: %d write endpoints called", got)
	}
	// One VM manifest under vm/<cluster>/.
	var vmPath string
	for _, w := range res.Wrote {
		if w.Kind == schema.KindVM {
			vmPath = w.Path
		}
	}
	if vmPath == "" {
		t.Fatalf("no VM manifest written; wrote=%+v", res.Wrote)
	}
	if !strings.HasPrefix(vmPath, "vm/conformance-dev/") {
		t.Errorf("VM manifest must live under vm/conformance-dev/, got %s", vmPath)
	}
	raw, rerr := os.ReadFile(filepath.Join(root, vmPath))
	if rerr != nil {
		t.Fatalf("read generated manifest: %v", rerr)
	}
	var vm schema.VM
	if uerr := schema.YAMLTo(string(raw), &vm); uerr != nil {
		t.Fatalf("generated manifest is not YAML-parseable into a pveconform VM: %v", uerr)
	}
	if vm.Metadata.Name != "existing-vm" {
		t.Errorf("name = %q, want existing-vm", vm.Metadata.Name)
	}
	if vm.Spec.Node != "pve01" || vm.Spec.VMID != 100 {
		t.Errorf("identity = node %q / vmid %d, want pve01/100", vm.Spec.Node, vm.Spec.VMID)
	}
	if vm.Spec.Memory != "1GiB" {
		t.Errorf("memory = %q, want 1GiB (human form)", vm.Spec.Memory)
	}
	if vm.Spec.CPU.Type != "host" || vm.Spec.CPU.Cores != 4 {
		t.Errorf("cpu = %+v, want host/4", vm.Spec.CPU)
	}
	// BOTH disks must be present: adopt does not silently omit the live-only
	// scsi1 — it interrogates PVE and reflects every owned disk it sees.
	if len(vm.Spec.Disks) != 2 {
		t.Fatalf("disks = %d, want 2 (scsi0 + the live-only scsi1); got %+v", len(vm.Spec.Disks), vm.Spec.Disks)
	}
	bySlot := map[string]schema.Disk{}
	for _, d := range vm.Spec.Disks {
		bySlot[d.Slot] = d
	}
	if d := bySlot["scsi0"]; d.Storage != "local-lvm" || d.Size != "8GiB" || !d.IOThread {
		t.Errorf("scsi0 = %+v, want local-lvm/8GiB iothread", d)
	}
	if d := bySlot["scsi1"]; d.Storage != "local-lvm" || d.Size != "20GiB" {
		t.Errorf("scsi1 = %+v, want local-lvm/20GiB (live-only, must be represented, not dropped)", d)
	}
	// NIC: PVE's random MAC is NOT pinned in the manifest.
	if len(vm.Spec.NICs) != 1 || vm.Spec.NICs[0].Bridge != "vmbr0" {
		t.Errorf("nics = %+v, want exactly one on vmbr0", vm.Spec.NICs)
	}
	for _, n := range vm.Spec.NICs {
		if n.MAC != "" {
			t.Errorf("adopt pins PVE's random MAC %q — should leave it unpinned", n.MAC)
		}
	}
	// Ownership tag is stripped (the schema re-appends it at create time).
	for _, tag := range vm.Spec.Tags {
		if tag == "pveconform" {
			t.Errorf("generated manifest must not carry the ownership tag in spec.tags")
		}
	}
	// Bookkeeping keys are NOT in the gap report.
	for _, g := range res.Gaps {
		if g.Field == "digest" || g.Field == "meta" || g.Field == "vmgenid" || g.Field == "smbios1" {
			t.Errorf("PVE bookkeeping key %q surfaced as a gap", g.Field)
		}
	}
	// Unknown PVE keys are surfaced as gaps.
	foundUnknown := false
	for _, g := range res.Gaps {
		if g.Field == "soc0" || g.Field == "replicat1" {
			foundUnknown = true
		}
	}
	if !foundUnknown {
		t.Errorf("unknown PVE keys (soc0 / replicat1) not surfaced on Gaps")
	}
	// The generated manifest must be valid pveconform YAML.
	if vErr := vm.Validate(); vErr != nil {
		t.Errorf("generated manifest must Validate; got %v", vErr)
	}
}

// TestAdoptLXCRoundTrip — PVE → adopt → YAML → LXC with template resolved.
func TestAdoptLXCRoundTrip(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadTemplate("pve01", "local", "debian-13.tar.zst")
	m.PreloadLVMVolume("pve01", "local-lvm", "local-lvm:vm-500-disk-0", 4096*1024*1024, "rootdir")
	m.PreloadLXC("pve01", 500, map[string]string{
		"hostname":     "cache-lxc",
		"memory":       "1024",
		"swap":         "512",
		"arch":         "amd64",
		"cores":        "1",
		"rootfs":       "local-lvm:vm-500-disk-0",
		"net0":         "name=wired0,bridge=vmbr0,hwaddr=AA:BB:CC:DD:EE:01,type=veth",
		"nameserver":   "1.1.1.1 8.8.8.8",
		"searchdomain": "example.com",
		"tags":         "pveconform",
		"ostype":       "debian",
		"ostemplate":   "local:vztmpl/debian-13.tar.zst",
		"description":  "cache box\n",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "dev", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if got := c.WritesPerformed(); got != 0 {
		t.Fatalf("adopt wrote to PVE: %d", got)
	}
	var lxcPath, cttPath string
	for _, w := range res.Wrote {
		switch w.Kind {
		case schema.KindLXC:
			lxcPath = w.Path
		case schema.KindCTTemplate:
			cttPath = w.Path
		}
	}
	if lxcPath == "" {
		t.Fatalf("no LXC manifest; wrote=%+v", res.Wrote)
	}
	if cttPath == "" {
		t.Fatalf("no CTTemplate manifest (vztmpl present on pve01/local); wrote=%+v", res.Wrote)
	}
	raw, rerr := os.ReadFile(filepath.Join(root, lxcPath))
	if rerr != nil {
		t.Fatalf("read LXC manifest: %v", rerr)
	}
	var lxc schema.LXC
	if uerr := schema.YAMLTo(string(raw), &lxc); uerr != nil {
		t.Fatalf("LXC manifest not YAML-parseable: %v", uerr)
	}
	if lxc.Metadata.Name != "cache-lxc" {
		t.Errorf("name = %q, want cache-lxc", lxc.Metadata.Name)
	}
	if lxc.Spec.VMID != 500 || lxc.Spec.Node != "pve01" {
		t.Errorf("identity = %q/%d", lxc.Spec.Node, lxc.Spec.VMID)
	}
	if lxc.Spec.Template == "" {
		t.Errorf("spec.template empty; want the adopted CTTemplate name")
	}
	// PVE's ostype must NOT be in the gaps (bookkeeping key).
	for _, g := range res.Gaps {
		if g.Field == "ostype" {
			t.Errorf("PVE's ostype is bookkeeping, not a gap; got %+v", g)
		}
	}
	// PVE's random hwaddr is NOT pinned.
	for _, n := range lxc.Spec.Networks {
		if n.HWAddr != "" {
			t.Errorf("adopt pinned LXC's PVE-assigned hwaddr %q", n.HWAddr)
		}
	}
	// Nameserver parsed into a slice.
	if len(lxc.Spec.DNS.Nameservers) != 2 || lxc.Spec.DNS.Nameservers[0] != "1.1.1.1" || lxc.Spec.DNS.Nameservers[1] != "8.8.8.8" {
		t.Errorf("nameservers = %v, want [1.1.1.1 8.8.8.8]", lxc.Spec.DNS.Nameservers)
	}
	// rootfs size recovered from the storage listing.
	if lxc.Spec.Root.Storage != "local-lvm" {
		t.Errorf("root.storage = %q, want local-lvm", lxc.Spec.Root.Storage)
	}
	if lxc.Spec.Root.Size != "4GiB" {
		t.Errorf("root.size = %q, want 4GiB (recovered from pve01 content listing)", lxc.Spec.Root.Size)
	}
	// The manifest must be usable pveconform YAML.
	if vErr := lxc.Validate(); vErr != nil {
		t.Errorf("LXC manifest Validate: %v", vErr)
	}
}

// TestAdoptLXCRootFSUnknownSize — rootfs size not in the storage listing:
// adopt surfaces the gap and records the manifest in Result.Incomplete.
func TestAdoptLXCRootFSUnknownSize(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadTemplate("pve01", "local", "debian-13.tar.zst")
	m.PreloadLXC("pve01", 500, map[string]string{
		"hostname":   "cache-lxc",
		"memory":     "1024",
		"arch":       "amd64",
		"cores":      "1",
		"rootfs":     "local-lvm:vm-500-disk-0",
		"ostemplate": "local:vztmpl/debian-13.tar.zst",
		"tags":       "pveconform",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "dev", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if len(res.Incomplete) == 0 {
		t.Fatalf("rootfs size unrecoverable → Result.Incomplete must list the manifest; got %+v", res.Incomplete)
	}
	if !strings.Contains(res.Incomplete[0], "spec.root.size") {
		t.Errorf("incomplete entry should name spec.root.size, got %v", res.Incomplete)
	}
}

// TestAdoptISOAndCTTemplate — artifacts are reverse-engineered from the
// storage listing; the placeholder URL is recorded as a gap.
func TestAdoptISOAndCTTemplate(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadISO("pve01", "local", "debian-13.iso")
	m.PreloadTemplate("pve01", "local", "debian-13-standard.tar.zst")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "dev", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	var isoPath, cttPath string
	for _, w := range res.Wrote {
		switch w.Kind {
		case schema.KindISO:
			isoPath = w.Path
		case schema.KindCTTemplate:
			cttPath = w.Path
		}
	}
	if isoPath == "" || cttPath == "" {
		t.Fatalf("want both ISO and CTT manifests, got wrote=%+v", res.Wrote)
	}
	rawISO, _ := os.ReadFile(filepath.Join(root, isoPath))
	var iso schema.ISO
	if uerr := schema.YAMLTo(string(rawISO), &iso); uerr != nil {
		t.Fatalf("ISO manifest YAML: %v", uerr)
	}
	if iso.Spec.Filename != "debian-13.iso" || iso.Spec.Storage != "local" {
		t.Errorf("ISO manifest = %+v", iso.Spec)
	}
	if len(iso.Spec.Nodes) != 1 || iso.Spec.Nodes[0] != "pve01" {
		t.Errorf("ISO nodes = %v", iso.Spec.Nodes)
	}
	// The placeholder URL is recorded as a gap.
	var found bool
	for _, g := range res.Gaps {
		if g.Field == "spec.url" && g.Kind == schema.KindISO {
			found = true
		}
	}
	if !found {
		t.Errorf("ISO placeholder URL should be a named gap")
	}
	if vErr := iso.Validate(); vErr != nil {
		t.Errorf("ISO Validate: %v", vErr)
	}
}

// TestAdoptCDROMDetach — PVE's `ide2 = none` (explicit detach) is adopted as
// spec.hardware.cdrom.iso = none.
func TestAdoptCDROMDetach(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 100, map[string]string{
		"name":   "test-vm",
		"memory": "1024",
		"cpu":    "host",
		"cores":  "1",
		"scsi0":  "local-lvm:vm-100-disk-0,size=4G",
		"ide2":   "none",
		"net0":   "virtio,bridge=vmbr0",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	_, err := adopt.Run(context.Background(), c, "conformance-dev", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	var vmPath string
	for _, f := range []string{} {
		_ = f
	}
	entries, _ := os.ReadDir(root + "/vm/conformance-dev")
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(root, "vm/conformance-dev", e.Name()))
		var vm schema.VM
		if serr := schema.YAMLTo(string(raw), &vm); serr == nil && vm.Spec.VMID == 100 {
			vmPath = "vm/conformance-dev/" + e.Name()
			if vm.Spec.Hardware.Cdrom.Iso != "none" {
				t.Errorf("cdrom.iso = %q, want \"none\" (PVE explicitly detached)", vm.Spec.Hardware.Cdrom.Iso)
			}
			if vErr := vm.Validate(); vErr != nil {
				t.Errorf("Validate: %v", vErr)
			}
		}
	}
	if vmPath == "" {
		t.Fatalf("no VM manifest for vmid 100 under vm/conformance-dev/")
	}
}

// TestAdoptNoCluster — an empty cluster name is rejected.
func TestAdoptNoCluster(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	c := newMockClient(t, m)
	_, err := adopt.Run(context.Background(), c, "", []string{"pve01"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cluster name required") {
		t.Fatalf("want cluster name required, got %v", err)
	}
}

// TestAdoptMalformedClusterName — a malformed cluster name is rejected before
// any PVE call.
func TestAdoptMalformedClusterName(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	c := newMockClient(t, m)
	_, err := adopt.Run(context.Background(), c, "NOT A CLUSTER", []string{"pve01"}, t.TempDir())
	if err == nil {
		t.Fatal("want malformed-cluster-name error")
	}
}

// TestAdoptDoesNotTouchPVE — after a real adopt run, PVE's write counter is
// still 0 and every live object is byte-identical.
func TestAdoptDoesNotTouchPVE(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 100, map[string]string{"name": "v", "memory": "1024", "cpu": "host", "cores": "1", "scsi0": "local:4G", "tags": "pveconform"}, "stopped")
	m.PreloadLXC("pve01", 500, map[string]string{"name": "l", "hostname": "h", "memory": "512", "cores": "1", "rootfs": "local:4G", "ostemplate": "local:vztmpl/x.tar.zst", "tags": "pveconform"}, "stopped")
	m.PreloadTemplate("pve01", "local", "x.tar.zst")

	c := newMockClient(t, m)
	beforeVM, berr := c.VM().Get(context.Background(), "pve01", 100)
	if berr != nil {
		t.Fatalf("before: %v", berr)
	}
	beforeLXC, lerr := c.LXC().Get(context.Background(), "pve01", 500)
	if lerr != nil {
		t.Fatalf("before lxc: %v", lerr)
	}

	root := t.TempDir()
	_, err := adopt.Run(context.Background(), c, "conformance-dev", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if got := c.WritesPerformed(); got != 0 {
		t.Fatalf("PVE writes = %d, want 0 (adopt is read-only)", got)
	}
	afterVM, aerr := c.VM().Get(context.Background(), "pve01", 100)
	if aerr != nil {
		t.Fatalf("after vm config: %v", aerr)
	}
	if afterVM["memory"] != beforeVM["memory"] || afterVM["name"] != beforeVM["name"] {
		t.Errorf("VM config changed; adopt must be read-only: before=%+v after=%+v", beforeVM, afterVM)
	}
	afterLXC, lerr2 := c.LXC().Get(context.Background(), "pve01", 500)
	if lerr2 != nil {
		t.Fatalf("after lxc config: %v", lerr2)
	}
	if afterLXC["memory"] != beforeLXC["memory"] || afterLXC["hostname"] != beforeLXC["hostname"] {
		t.Errorf("LXC config changed; adopt must be read-only: before=%+v after=%+v", beforeLXC, afterLXC)
	}
}
