package adopt_test

import (
	"context"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/adopt"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient/mock"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// M13 adoption tests: TemplateCT reverse-translation, Secure Boot adoption,
// VM drive-option adoption, and LXC allocated-mount option adoption.

// TestAdopt_M13_TemplateCTManifest pins that a PVE CT reporting template=1
// is adopted as a kind=TemplateCT manifest under templatect/, NOT as a plain
// LXC manifest. The "template" key is owned by the kind (not a gap), and the
// ostemplate gap still applies (PVE does not persist it).
func TestAdopt_M13_TemplateCTManifest(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadCTTemplate("pve01", 700, map[string]string{
		"hostname":     "ct-tpl",
		"memory":       "1024",
		"cores":        "1",
		"rootfs":       "local-lvm:base-700-disk-0,size=4G",
		"net0":         "name=net0,bridge=vmbr0",
		"unprivileged": "1",
		"template":     "1",
		"digest":       "0a",
	})
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if m.WritesObserved() != 0 {
		t.Fatalf("adopt must be read-only; %d writes", m.WritesObserved())
	}
	var ctPath string
	for _, w := range res.Wrote {
		if w.Kind == schema.KindTemplateCT {
			ctPath = w.Path
		}
		for _, bad := range []schema.Kind{schema.KindLXC} {
			if w.Kind == bad && strings.Contains(w.Content, "kind: LXC") {
				t.Errorf("template CT adopted as plain LXC: %s", w.Path)
			}
		}
	}
	if ctPath == "" {
		t.Fatalf("no TemplateCT manifest; wrote=%v", m10Paths(res.Wrote))
	}
	if !strings.HasPrefix(ctPath, "templatect/") {
		t.Errorf("TemplateCT path = %q, want templatect/ root", ctPath)
	}
	// The template key must NOT be a gap for a TemplateCT (the kind owns it).
	for _, g := range res.Gaps {
		if g.Field == "template" {
			t.Errorf("template key must not be a gap on a TemplateCT: %+v", g)
		}
	}
	// The ostemplate gap still applies (unrecoverable).
	ostGap := false
	for _, g := range res.Gaps {
		if g.Field == "spec.template" {
			ostGap = true
		}
	}
	if !ostGap {
		t.Errorf("expected the spec.template ostemplate gap on the TemplateCT")
	}
}

// TestAdopt_M13_SecureBoot pins that a live efidisk0 with
// pre-enrolled-keys=1 adopts as secure-boot: enabled (and efitype → template).
func TestAdopt_M13_SecureBoot(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 710, map[string]string{
		"name":     "sb-vm",
		"memory":   "2048",
		"cores":    "1",
		"machine":  "q35",
		"bios":     "ovmf",
		"scsi0":    "local-lvm:vm-710-disk-0,size=8G",
		"efidisk0": "local-lvm:vm-710-disk-1,efitype=4m,ms-cert=2023k,pre-enrolled-keys=1,size=4M",
		"net0":     "virtio=52:54:00:00:00:01,bridge=vmbr0",
		"digest":   "0b",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	var content string
	for _, w := range res.Wrote {
		if strings.HasSuffix(w.Path, "sb-vm.yaml") {
			content = w.Content
		}
	}
	if content == "" {
		t.Fatalf("no manifest for sb-vm; wrote=%v", m10Paths(res.Wrote))
	}
	if !strings.Contains(content, "secure-boot: enabled") {
		t.Errorf("adopted manifest missing secure-boot: enabled:\n%s", content)
	}
	// ms-cert must NOT leak into the manifest (PVE-owned).
	if strings.Contains(content, "ms-cert") {
		t.Errorf("ms-cert must not be adopted (PVE-owned):\n%s", content)
	}
}

// TestAdopt_M13_DriveOptions pins that live drive options (discard/ssd/aio)
// are adopted into spec.disks entries.
func TestAdopt_M13_DriveOptions(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 720, map[string]string{
		"name":   "do-vm",
		"memory": "2048",
		"cores":  "1",
		"scsi0":  "local-lvm:vm-720-disk-0,aio=io_uring,discard=on,iothread=1,size=8G,ssd=1",
		"net0":   "virtio=52:54:00:00:00:02,bridge=vmbr0",
		"digest": "0c",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	var content string
	for _, w := range res.Wrote {
		if strings.HasSuffix(w.Path, "do-vm.yaml") {
			content = w.Content
		}
	}
	if content == "" {
		t.Fatalf("no manifest for do-vm; wrote=%v", m10Paths(res.Wrote))
	}
	for _, want := range []string{`discard: "on"`, "ssd: true", "aio: io_uring"} {
		if !strings.Contains(content, want) {
			t.Errorf("adopted manifest missing %q:\n%s", want, content)
		}
	}
}

// TestAdopt_M13_LXCMountOptions pins that allocated mpN option tokens are
// adopted into spec.mount-points[].options.
func TestAdopt_M13_LXCMountOptions(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadLXC("pve01", 730, map[string]string{
		"hostname": "mp-ct",
		"memory":   "1024",
		"cores":    "1",
		"rootfs":   "local-lvm:vm-730-disk-0,size=8G",
		"mp0":      "local-lvm:vm-730-disk-1,mp=/mnt/data,ro=1,size=1G",
		"net0":     "name=net0,bridge=vmbr0",
		"digest":   "0d",
	}, "stopped")
	c := newMockClient(t, m)
	root := t.TempDir()
	res, err := adopt.Run(context.Background(), c, "prod-test", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	var content string
	for _, w := range res.Wrote {
		if strings.HasSuffix(w.Path, "mp-ct.yaml") {
			content = w.Content
		}
	}
	if content == "" {
		t.Fatalf("no manifest for mp-ct; wrote=%v", m10Paths(res.Wrote))
	}
	if !strings.Contains(content, "read-only: true") {
		t.Errorf("adopted manifest missing mount options read-only: true:\n%s", content)
	}
}
