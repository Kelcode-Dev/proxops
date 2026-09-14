package schema

import (
	"testing"
)

// TestVMCdromNoneDetach: "cdrom.iso: none" → detach. proxops owns the
// IDE slot and renders `ide2=none`. A VM with no cdrom block must NOT
// manage the slot at all.
func TestVMCdromNoneDetach(t *testing.T) {
	vm := NewVM()
	src := "apiVersion: " + APIVersion + "\nkind: VM\nmetadata:\n  name: detacher\nspec:\n  node: pve01\n  vmid: 700\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks:\n    - {storage: local, size: 4GiB}\n  hardware:\n    cdrom:\n      iso: none\n"
	if err := YAMLTo(src, vm); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if err := vm.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	p, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	slot := vm.CdromSlot()
	if slot != "ide2" {
		t.Errorf("CdromSlot = %q, want ide2 (no cloud-init)", slot)
	}
	if got, _ := p[slot].(string); got != "none" {
		t.Errorf("%s = %q, want \"none\" (detaching: proxops owns the slot)", slot, got)
	}
	if deps := vm.Deps(); deps != nil {
		t.Errorf("a detaching VM must own no ISO dep, got %v", deps)
	}
}

func TestVMCdromAbsentUnmanaged(t *testing.T) {
	vm := NewVM()
	src := "apiVersion: " + APIVersion + "\nkind: VM\nmetadata:\n  name: no-cdrom\nspec:\n  node: pve01\n  vmid: 701\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks:\n    - {storage: local, size: 4GiB}\n"
	if err := YAMLTo(src, vm); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if err := vm.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	p, err := vm.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, ok := p[vm.CdromSlot()]; ok {
		t.Errorf("%s should be unset when no cdrom block is declared, got %v", vm.CdromSlot(), got)
	}
	if deps := vm.Deps(); deps != nil {
		t.Errorf("a VM without cdrom must own no deps, got %v", deps)
	}
}
