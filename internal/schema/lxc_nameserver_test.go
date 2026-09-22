package schema_test

import (
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/schema"
)

// TestLXCNameserverSpaceFormNoDrift: PVE accepts comma-separated CSV on
// create/update ("1.1.1.1,8.8.8.8") but reports space-separated on /config
// ("1.1.1.1 8.8.8.8"). proxops must treat these as equal so re-reads do
// not trip drift.
func TestLXCNameserverSpaceFormNoDrift(t *testing.T) {
	lxcRaw := "apiVersion: proxops/v1alpha1\n" +
		"kind: LXC\n" +
		"metadata:\n  name: lxc\n" +
		"spec:\n  node: pve01\n  vmid: 9200\n  memory: 1GiB\n" +
		"  cpu: {cores: 1}\n" +
		"  template: base-ctt\n" +
		"  root: {storage: local, size: 4GiB}\n" +
		"  networks:\n    - {bridge: vmbr0}\n" +
		"  dns:\n    hostname: lxc\n    nameservers: [1.1.1.1, 8.8.8.8]\n    domain: example.com\n"
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := map[string]any{
		"memory":       int64(1024),
		"cores":        1,
		"name":         "lxc", // PVE's display-name field (auto-set from hostname)
		"tags":         []any{"proxops"},
		"hostname":     "lxc",
		"nameserver":   "1.1.1.1 8.8.8.8", // PVE normalizes CSV → spaces
		"searchdomain": "example.com",
		"rootfs":       "local:vm-9200-disk-0,size=4G",
		"net0":         "name=net0,bridge=vmbr0,hwaddr=00:11:22:33:44:55,type=veth",
		"onboot":       "0",
		"unprivileged": "0",
	}
	if _, _, changed := lxc.Drift(live); changed {
		t.Errorf("Drift PVE-live report with space-form nameserver should NOT report change")
	}
}

// TestLXCNameserverDriftsOnRealChange: if PVE really has a different set,
// drift must be reported.
func TestLXCNameserverDriftsOnRealChange(t *testing.T) {
	lxcRaw := "apiVersion: proxops/v1alpha1\n" +
		"kind: LXC\n" +
		"metadata:\n  name: lxc\n" +
		"spec:\n  node: pve01\n  vmid: 9200\n  memory: 1GiB\n" +
		"  cpu: {cores: 1}\n" +
		"  template: base-ctt\n" +
		"  root: {storage: local, size: 4GiB}\n" +
		"  networks:\n    - {bridge: vmbr0}\n" +
		"  dns:\n    nameservers: [9.9.9.9]\n"
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(lxcRaw, lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := map[string]any{
		"memory": int64(1024), "cores": 1,
		"tags": []any{"proxops"}, "hostname": "lxc",
		"nameserver": "1.1.1.1 8.8.8.8", // PVE's current state: does not match
		"rootfs":     "local:vm-9200-disk-0,size=4G",
		"net0":       "name=net0,bridge=vmbr0",
	}
	upd, _, changed := lxc.Drift(live)
	if !changed {
		t.Error("expected a change")
	}
	if got, ok := upd["nameserver"]; !ok {
		t.Error("expected nameserver in update params")
	} else if got != "9.9.9.9" {
		t.Errorf("nameserver update = %v, want 9.9.9.9", got)
	}
}
