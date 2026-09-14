package schema

import "testing"

func TestM11_TemplDrift_SimulateProd999(t *testing.T) {
	tv := NewTemplateVM()
	tv.Metadata.Name = "tpl-999"
	tv.Spec.Node = "pve01"
	tv.Spec.VMID = 999
	tv.Spec.Memory = "2GiB"
	tv.Spec.CPU = Cpu{Type: "host", Cores: 1}
	tv.Spec.Disks = []Disk{{Storage: "vm_disks", Size: "32GiB", Slot: "scsi0", Controller: "virtio-scsi-single", IOThread: true}}
	tv.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr2", Slot: "net0"}}
	tv.Spec.Hardware.Machine = "q35"
	tv.Spec.Hardware.BIOS = "ovmf"
	tv.Spec.Hardware.Sockets = 1
	tv.Spec.Options.Agent = true
	tv.Spec.Options.BootOrder = []string{"scsi0"}
	live := map[string]any{
		"vmid": "999", "name": "tpl-999", "memory": "2048", "cpu": "host", "cores": "1",
		"machine": "q35", "bios": "ovmf", "agent": "1", "boot": "order=scsi0",
		"scsihw":   "virtio-scsi-single",
		"scsi0":    "vm_disks:base-999-disk-1,iothread=1,size=32G",
		"scsi1":    "vm_disks:vm-999-cloudinit,media=cdrom",
		"net0":     "virtio=52:54:00:FF:F7:0D,bridge=vmbr2",
		"efidisk0": "vm_disks:base-999-disk-0,efitype=4m,pre-enrolled-keys=1,size=1M",
		"sockets":  1, "numa": 0, "ostype": "l26", "digest": "abc", "vmgenid": "def",
		"template": 1, "serial0": "socket",
	}
	upd, stop, changed := tv.Drift(live)
	t.Logf("changed=%v stop=%v", changed, stop)
	for k, v := range upd {
		t.Logf("  update %q -> %v", k, v)
	}
	if !changed {
		t.Errorf("Drift: changed=false; expected tags to claim proxops (PVE 999 has no tags key)")
	}
}
