// Plan-level M12 coverage: the clone-backed VM create action shape.
//
// Exercises plan.PlanActions directly with hand-built resources + live
// inventory — no git, no mock PVE — to pin the planner's clone invariants:
// the Create action carries the resolved CloneSourceID + the disjoint
// delete-key list, and a clone-backed VM never plans a disk write.
package plan_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/plan"
	"github.com/Kelcode-Dev/proxops/internal/schema"
)

func m12PlanResources(t *testing.T) []schema.Resource {
	t.Helper()
	tv := schema.NewTemplateVM()
	if err := schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: TemplateVM
metadata:
  name: tpl
spec:
  node: n1
  vmid: 900
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 8GiB, interface: scsi0}
  networks:
    - {model: virtio, bridge: vmbr0}
  cloud-init-data:
    ci-user: tpluser
    ssh-keys: ["ssh-ed25519 AAAA tpl"]
`, tv); err != nil {
		t.Fatalf("YAMLTo template: %v", err)
	}
	v := schema.NewVM()
	if err := schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: vm
spec:
  node: n1
  vmid: 901
  clone: tpl
  memory: 2GiB
  cpu: {type: host, cores: 2}
  networks:
    - {model: virtio, bridge: vmbr0}
  cloud-init-data:
    ci-user: vmuser
`, v); err != nil {
		t.Fatalf("YAMLTo vm: %v", err)
	}
	if err := schema.ResolveArtifactRefs([]schema.Resource{tv, v}); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}
	return []schema.Resource{tv, v}
}

// TestPlanCloneCreateCarriesSourceAndDeleteKeys: the clone-backed VM's Create
// action names the resolved template vmid and the inherited keys to clear.
func TestPlanCloneCreateCarriesSourceAndDeleteKeys(t *testing.T) {
	desired := m12PlanResources(t)
	live := &plan.LiveInventory{
		Configs: map[string]map[string]any{},
		Power:   map[string]string{},
	}
	p, err := plan.PlanActions(context.Background(), desired, live, plan.PlanOptions{})
	if err != nil {
		t.Fatalf("PlanActions: %v", err)
	}
	var vmCreate *plan.Action
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind == schema.KindVM && a.What == plan.Create {
			vmCreate = a
		}
	}
	if vmCreate == nil {
		t.Fatalf("no VM create planned; actions=%+v", p.Actions)
	}
	if vmCreate.CloneSourceID != 900 {
		t.Errorf("CloneSourceID = %d, want 900", vmCreate.CloneSourceID)
	}
	if !strings.Contains(vmCreate.Reason, "full-clone") {
		t.Errorf("Reason = %q, want it to name the clone verb", vmCreate.Reason)
	}
	// The delete list must clear the template's sshkeys (the VM does not
	// declare them) but NOT ciuser (the VM declares its own).
	got := map[string]bool{}
	for _, k := range vmCreate.DeleteKeys {
		got[k] = true
	}
	if !got["sshkeys"] {
		t.Errorf("DeleteKeys missing sshkeys (inherited, undeclared): %v", vmCreate.DeleteKeys)
	}
	if got["ciuser"] {
		t.Errorf("DeleteKeys must NOT contain ciuser (the VM owns it): %v", vmCreate.DeleteKeys)
	}
	// set ∩ delete must be empty (PVE 400s on overlap).
	for _, k := range vmCreate.DeleteKeys {
		if _, ok := vmCreate.Params[k]; ok {
			t.Errorf("key %q appears in BOTH set params and delete list", k)
		}
	}
	// No disk slot may appear in the create params of a clone-backed VM.
	for k := range vmCreate.Params {
		if strings.HasPrefix(k, "scsi") {
			t.Errorf("clone create params carry disk slot %q", k)
		}
	}
}

// TestPlanCloneSourceEqualsTargetFailsClosed: a clone whose resolved source
// vmid equals the target vmid is a planner error (would clone onto itself).
func TestPlanCloneSourceEqualsTargetFailsClosed(t *testing.T) {
	tv := schema.NewTemplateVM()
	if err := schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: TemplateVM
metadata:
  name: tpl
spec:
  node: n1
  vmid: 900
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local-lvm, size: 8GiB}
  networks:
    - {model: virtio, bridge: vmbr0}
`, tv); err != nil {
		t.Fatal(err)
	}
	v := schema.NewVM()
	if err := schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: vm
spec:
  node: n1
  vmid: 900
  clone: tpl
  memory: 2GiB
  cpu: {type: host, cores: 2}
  networks:
    - {model: virtio, bridge: vmbr0}
`, v); err != nil {
		t.Fatal(err)
	}
	// The parse-time id-collision guard would normally reject two objects
	// claiming (n1, 900); here we drive the planner's own belt-and-braces
	// check directly by resolving then forcing the target id.
	if err := schema.ResolveArtifactRefs([]schema.Resource{tv, v}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	live := &plan.LiveInventory{Configs: map[string]map[string]any{}, Power: map[string]string{}}
	_, err := plan.PlanActions(context.Background(), []schema.Resource{tv, v}, live, plan.PlanOptions{})
	// The planner's belt-and-braces guard: a clone whose resolved source
	// vmid equals the target vmid would clone onto itself. Driving
	// PlanActions directly (bypassing parse's id-collision guard) reaches
	// it, and it MUST fail closed rather than emit a self-clone action.
	if err == nil || !strings.Contains(err.Error(), "clone source vmid") {
		t.Fatalf("PlanActions error = %v, want self-clone fail-closed", err)
	}
}
