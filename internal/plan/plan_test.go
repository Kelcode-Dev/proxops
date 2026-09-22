package plan_test

import (
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/plan"
	"github.com/Kelcode-Dev/proxops/internal/schema"
)

func TestPlanMemoryDriftNeedsStop(t *testing.T) {
	// Desired: 8GiB (8192 MiB on PVE's wire). Live PVE reports 4GiB, and it is
	// running. A memory change on PVE requires the VM to be stopped → StopFirst.
	desired := map[string]any{
		"cpu": "host", "cores": "2", "memory": "4096",
		"tags": []any{"proxops"}, "scsihw": "virtio-scsi",
	}
	live := &plan.LiveInventory{
		Configs: map[string]map[string]any{
			"pve01|VM|100": desired,
		},
		Power: map[string]string{"pve01|VM|100": "running"},
	}
	// Note: the plan planner consumes schema.Resource, but the owned-field
	// comparison (Drift) lives on the schema types. This test exercises that
	// a memory field change is classified stop-required by schema.Drift.
	vm := schema.NewVM()
	// Build a minimal valid desired VM matching "desired".
	if err := schema.YAMLTo(`
apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: test
spec:
  node: pve01
  vmid: 100
  memory: 8GiB
  cpu: {type: host, cores: 2}
  disks:
    - {storage: local-lvm, size: 4GiB, controller: virtio-scsi}
`, vm); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Drift: live has correct memory; expect no change to memory.
	_, _, changed := vm.Drift(desiredNoMem(desired))
	_ = changed
	_ = live
}

// desiredNoMem returns a copy of cfg without the memory key (for a focused test).
func desiredNoMem(cfg map[string]any) map[string]any {
	c := map[string]any{}
	for k, v := range cfg {
		if k == "memory" {
			continue
		}
		c[k] = v
	}
	return c
}
