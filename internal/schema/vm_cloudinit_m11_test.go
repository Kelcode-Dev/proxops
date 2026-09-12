package schema

import (
	"strings"
	"testing"
)

func baseCloudInitVM(name string) *VM {
	vm := NewVM()
	vm.Metadata.Name = name
	vm.Spec.Node = "pve01"
	vm.Spec.VMID = 100
	vm.Spec.Memory = "1GiB"
	vm.Spec.CPU = Cpu{Type: "host", Cores: 1}
	vm.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "4GiB"}}
	vm.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0"}}
	return vm
}

func baseLiveVM(id, name string) map[string]any {
	return map[string]any{
		// PVE stores tags as a comma-joined string on /config; pveconform
		// appends its ownership tag "pveconform" at write time. Include
		// it here so Drift() does not emit a spurious tags-update.
		"memory": "1024",
		"cpu":    "host",
		"cores":  "1",
		"vmid":   id,
		"name":   name,
		"tags":   "pveconform",
		"scsi0":  "local-lvm:vm-" + id + "-disk-0,size=4G",
		"net0":   "virtio=52:54:00:FF:00:01,bridge=vmbr0",
	}
}

func TestVMSpecCloudInitData_ToCreateParams(t *testing.T) {
	vm := baseCloudInitVM("vm-ci-full")
	vm.Spec.CloudInitData = CloudInitData{
		CIUser:        "operator",
		SSHKeys:       []string{"operator=AAA", "bob=BBB"},
		Nameservers:   []string{"1.1.1.1", "8.8.8.8"},
		SearchDomains: []string{"example.search"},
		IPConfigs:     []CloudInitIPConfig{{NIC: 0, IP: "192.168.192.199/18", Gateway: "192.168.192.5"}},
	}
	params, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if params["ciuser"] != "operator" {
		t.Errorf("ciuser = %v", params["ciuser"])
	}
	if params["sshkeys"] != "operator=AAA,bob=BBB" {
		t.Errorf("sshkeys = %v", params["sshkeys"])
	}
	if params["nameserver"] != "1.1.1.1 8.8.8.8" {
		t.Errorf("nameserver = %v", params["nameserver"])
	}
	if params["searchdomain"] != "example.search" {
		t.Errorf("searchdomain = %v", params["searchdomain"])
	}
	if params["ipconfig0"] != "ip=192.168.192.199/18,gw=192.168.192.5" {
		t.Errorf("ipconfig0 = %v", params["ipconfig0"])
	}
	if _, got := params["ipconfig1"]; got {
		t.Errorf("ipconfig1 present, want absent")
	}
}

func TestVMSpecCloudInitData_EmptyNotOwned(t *testing.T) {
	vm := baseCloudInitVM("vm-ci-none")
	params, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	for _, k := range []string{"ciuser", "sshkeys", "nameserver", "searchdomain", "ipconfig0"} {
		if _, got := params[k]; got {
			t.Errorf("%s present, want absent (empty desired) = %v", k, params[k])
		}
	}
}

func TestVMSpecCloudInitData_SentinelSSHKeysNotOwned(t *testing.T) {
	vm := baseCloudInitVM("vm-ci-sentinel")
	vm.Spec.CloudInitData.SSHKeys = []string{CloudInitRedactedSentinel}
	params, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if _, got := params["sshkeys"]; got {
		t.Errorf("sshkeys present for sentinel-only = %v", params["sshkeys"])
	}
}

func TestVMSpecCloudInitData_MixedSentinelFailsClosed(t *testing.T) {
	vm := baseCloudInitVM("vm-ci-mixed")
	vm.Spec.CloudInitData.SSHKeys = []string{"operator=AAA", CloudInitRedactedSentinel}
	err := vm.Validate()
	if err == nil {
		t.Fatalf("Validate ok, want error for mixed sentinel + real keys")
	}
	if !strings.Contains(err.Error(), "ssh-keys") {
		t.Errorf("err = %q; want ssh-keys mention", err.Error())
	}
}

func TestVMSpecCloudInitData_BadCIDRFailsClosed(t *testing.T) {
	vm := baseCloudInitVM("vm-ci-badcidr")
	vm.Spec.CloudInitData.IPConfigs = []CloudInitIPConfig{{NIC: 0, IP: "10.0.0.0"}}
	if err := vm.Validate(); err == nil {
		t.Fatalf("Validate ok, want error for non-CIDR ip")
	}
}

func TestVMSpecCloudInitData_GatewayWithoutIPFailsClosed(t *testing.T) {
	vm := baseCloudInitVM("vm-ci-gwnoip")
	vm.Spec.CloudInitData.IPConfigs = []CloudInitIPConfig{{NIC: 0, Gateway: "10.0.0.1"}}
	if err := vm.Validate(); err == nil {
		t.Fatalf("Validate ok, want error for gateway without ip")
	}
}

func TestVMExtraCloudInitDataCollision(t *testing.T) {
	for _, bad := range []string{"ciuser", "sshkeys", "nameserver", "searchdomain", "ipconfig0"} {
		vm := baseCloudInitVM("vm-extra-" + bad)
		vm.Spec.Extra = map[string]string{bad: "1"}
		if err := vm.Validate(); err == nil {
			t.Errorf("spec.extra[%q] accepted, want conflict", bad)
		}
	}
}

func TestVMSpecCloudInitData_Drift(t *testing.T) {
	t.Run("changed", func(t *testing.T) {
		vm := baseCloudInitVM("vm-drift-1")
		vm.Spec.CloudInitData.CIUser = "operator"
		vm.Spec.CloudInitData.Nameservers = []string{"1.1.1.1", "8.8.8.8"}
		live := baseLiveVM("9100", "vm-drift-1")
		live["ciuser"] = "olduser"
		live["nameserver"] = "8.8.8.8"
		upd, stop, changed := vm.Drift(live)
		if !changed {
			t.Fatalf("changed=false, want true")
		}
		if stop {
			t.Errorf("stopRequired=true, want false")
		}
		if upd["ciuser"] != "operator" {
			t.Errorf("upd[ciuser]=%v", upd["ciuser"])
		}
		if upd["nameserver"] != "1.1.1.1 8.8.8.8" {
			t.Errorf("upd[nameserver]=%v", upd["nameserver"])
		}
	})
	t.Run("empty-desired-no-write", func(t *testing.T) {
		vm := baseCloudInitVM("vm-drift-2")
		live := baseLiveVM("9101", "vm-drift-2")
		live["ciuser"] = "pve-side-user"
		live["nameserver"] = "8.8.8.8"
		upd, _, changed := vm.Drift(live)
		if changed {
			t.Fatalf("changed=true with empty desired, want false; upd=%v", upd)
		}
	})
	t.Run("nameserver-set-equal", func(t *testing.T) {
		vm := baseCloudInitVM("vm-drift-3")
		vm.Spec.CloudInitData.Nameservers = []string{"8.8.8.8", "1.1.1.1"}
		live := baseLiveVM("9102", "vm-drift-3")
		live["nameserver"] = "1.1.1.1 8.8.8.8"
		upd, _, changed := vm.Drift(live)
		if changed {
			t.Errorf("changed=true, want false (set-equal); upd=%v", upd)
		}
	})
}
