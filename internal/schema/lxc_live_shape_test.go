package schema_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// TestLXCDriftOnLiveShape: reproduce the real PVE 9.2 LXC 9200 live report +
// proxops manifest, and assert that Drift returns "no change". This
// pins the PVE 9.2 LXC wire quirk (PVE adds net0.name/type/hwaddr, reports
// rootfs with PVE-assigned volume id, and normalizes nameserver to space
// form).
func TestLXCDriftOnLiveShape(t *testing.T) {
	lxcRaw := "apiVersion: proxops/v1alpha1\n" +
		"kind: LXC\n" +
		"metadata:\n  name: test-lxc-01\n" +
		"spec:\n  node: pve-dev-01\n  vmid: 9200\n  state: stopped\n" +
		"  memory: 1GiB\n  swap: 512MiB\n" +
		"  cpu: {cores: 1}\n" +
		"  template: debian-13-standard\n" +
		"  root:\n    storage: local-lvm\n    size: 4GiB\n" +
		"  networks:\n    - {bridge: vmbr0}\n" +
		"  dns:\n    hostname: test-lxc-01\n    nameservers: [127.0.0.1, 1.1.1.1]\n    domain: example.invalid\n" +
		"  arch: amd64\n" +
		"  options:\n    unprivileged: true\n    onboot: true\n" +
		"  pve-description: proxops M6 LXC with inferred template reference\n"

	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}

	// PVE's actual on-disk shape after vzcreate:
	//   - nameserver is space-separated (PVE normalizes CSV input)
	//   - rootfs is PVE-assigned LVM volume id "local-lvm:vm-9200-disk-0,size=4G"
	//   - net0 has PVE-assigned hwaddr + type=veth + name=net0
	//   - PVE stores description with a trailing newline
	live := map[string]any{
		"arch":         "amd64",
		"cores":        int64(1),
		"description":  "proxops M6 LXC with inferred template reference\n",
		"hostname":     "test-lxc-01",
		"memory":       int64(1024),
		"nameserver":   "127.0.0.1 1.1.1.1",
		"net0":         "name=net0,bridge=vmbr0,hwaddr=52:54:00:C0:11:48,type=veth",
		"onboot":       "1",
		"ostype":       "debian",
		"rootfs":       "local-lvm:vm-9200-disk-0,size=4G",
		"searchdomain": "example.invalid",
		"swap":         int64(512),
		"tags":         []any{"proxops"},
		"unprivileged": "1",
	}

	upd, _, changed := lxc.Drift(live)
	if changed {
		t.Errorf("Drift reported change on a PVE-live LXC; update params: %#v", upd)
	}
}
