package reconcile_test

import (
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// M11+ cloud-init E2E (mock parity for the live conformance-dev validation):
// a DiskImage artifact + a VM whose scsi0 is seeded from it via PVE 9's
// create-time import-from, carrying the full cloud-init DATA surface
// (ciuser/sshkeys/nameserver/searchdomain/ipconfig0) + the cloud-init drive.
//
// Proven against real PVE 9.2.2 on conformance-dev 2026-09-13 (the guest
// booted, cloud-init reported DataSourceNoCloud seed=/dev/sr0, and the
// hostname/static-IP/DNS/user/SSH-key all matched the manifest). This test
// pins the same flow against the stateful mock so regressions in the wire
// grammar fail in CI.

const cloudInitVMYAML = `
apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: ci-vm
spec:
  node: pve01
  vmid: 9150
  state: started
  memory: 2GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      interface: scsi0
      image: debian-cloud
      controller: virtio-scsi-single
  networks:
    - model: virtio
      bridge: vmbr0
  hardware:
    cloud-init:
      enabled: true
      storage: local-lvm
  cloud-init-data:
    ci-user: cloudadmin
    ssh-keys:
      - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA m11-e2e@test"
    nameservers: [1.1.1.1, 8.8.8.8]
    search-domains: [dev.example.com]
    ipconfigs:
      - nic: 0
        ip: 192.168.0.199/24
        gateway: 192.168.0.1
  options:
    agent: true
`

const diskImageYAML = `
apiVersion: proxops/v1alpha1
kind: DiskImage
metadata:
  name: debian-cloud
spec:
  node: pve01
  storage: local
  filename: debian-13-genericcloud-amd64.qcow2
  url: https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2
`

func TestE2E_CloudInitVMFromDiskImage(t *testing.T) {
	h := newHarness(t, map[string]string{
		"vm.yaml":  cloudInitVMYAML,
		"img.yaml": diskImageYAML,
	}, 3)

	// Cycle 1: the DiskImage must download BEFORE the VM creates (structured
	// dep edge VM→DiskImage). Both in one cycle: the image create is level 0,
	// the VM create is level 1. The VM create carries start=true (state:
	// started), so no separate Start action is planned for a missing object.
	p1 := h.apply(t)
	var order []string
	for _, a := range p1.Actions {
		order = append(order, string(a.Kind)+"."+string(a.What))
	}
	wantOrder := []string{"DiskImage.create", "VM.create"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("cycle 1 actions = %v, want %v", order, wantOrder)
	}

	// PVE-side state: the VM exists with the import-from create form
	// normalized by PVE into a plain volume, the cloud-init drive on ide2,
	// and every cloud-init DATA field verbatim.
	if !h.mock.VMExists(node, 9150) {
		t.Fatal("VM 9150 missing after apply")
	}
	cfg := h.mock.VMConfig(node, 9150)
	if cfg["scsi0"] != "local-lvm:vm-9150-disk-0,size=3G" {
		t.Errorf("scsi0 = %q, want PVE-normalized imported volume", cfg["scsi0"])
	}
	if cfg["ciuser"] != "cloudadmin" {
		t.Errorf("ciuser = %q", cfg["ciuser"])
	}
	if cfg["nameserver"] != "1.1.1.1 8.8.8.8" {
		t.Errorf("nameserver = %q", cfg["nameserver"])
	}
	if cfg["searchdomain"] != "dev.example.com" {
		t.Errorf("searchdomain = %q", cfg["searchdomain"])
	}
	if cfg["ipconfig0"] != "ip=192.168.0.199/24,gw=192.168.0.1" {
		t.Errorf("ipconfig0 = %q", cfg["ipconfig0"])
	}
	// sshkeys must be the PERCENT-ENCODED form (PVE 9.2's own storage shape —
	// a raw value is rejected by PVE at create with "invalid urlencoded
	// string").
	if !strings.HasPrefix(cfg["sshkeys"], "ssh-ed25519%20") || !strings.Contains(cfg["sshkeys"], "%40") {
		t.Errorf("sshkeys = %q, want percent-encoded wire form", cfg["sshkeys"])
	}
	if !strings.Contains(cfg["ide2"], "cloudinit") {
		t.Errorf("ide2 = %q, want cloud-init drive", cfg["ide2"])
	}
	if st, _ := h.mock.VMStatus(node, 9150); st != "running" {
		t.Errorf("power = %q, want running (state: started must boot on the create cycle)", st)
	}

	// Cycle 2: idempotent — zero actions, especially no sshkeys re-write
	// (decoded-set comparison) and no cloud-init drive rewrite.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Fatalf("cycle 2 actions = %v, want 0", p2.Actions)
	}
}

func TestE2E_CloudInitVMUpdateDrift(t *testing.T) {
	h := newHarness(t, map[string]string{
		"vm.yaml":  cloudInitVMYAML,
		"img.yaml": diskImageYAML,
	}, 3)
	h.apply(t)

	// Simulate PVE-side drift on the cloud-init DATA fields: ciuser changed,
	// nameserver reordered-but-equal (must NOT drift), sshkeys replaced.
	h.mock.SetVMConfigField(node, 9150, "ciuser", "someone-else")
	h.mock.SetVMConfigField(node, 9150, "nameserver", "8.8.8.8 1.1.1.1")
	h.mock.SetVMConfigField(node, 9150, "sshkeys", "ssh-rsa%20AAAA%20intruder%40evil")

	p := h.apply(t)
	var upd map[string]any
	for _, a := range p.Actions {
		if a.What == plan.Update && a.Kind == schema.KindVM {
			upd = a.Params
		}
	}
	if upd == nil {
		t.Fatalf("no Update action planned; actions=%v", p.Actions)
	}
	if upd["ciuser"] != "cloudadmin" {
		t.Errorf("upd[ciuser] = %v, want cloudadmin", upd["ciuser"])
	}
	if _, bad := upd["nameserver"]; bad {
		t.Errorf("nameserver re-written despite set-equality: %v", upd["nameserver"])
	}
	if _, bad := upd["sshkeys"]; !bad {
		t.Error("sshkeys drift not detected")
	} else if !strings.HasPrefix(upd["sshkeys"].(string), "ssh-ed25519%20") {
		t.Errorf("sshkeys update = %v, want percent-encoded", upd["sshkeys"])
	}

	// After the update cycle the object must be converged again.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Fatalf("post-drift cycle actions = %v, want 0", p2.Actions)
	}
}
