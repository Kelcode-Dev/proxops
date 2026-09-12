package reconcile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// M11 e2e: TemplateVM lifecycle against the stateful mock PVE.
//
// Scenarios covered:
// 1. Create a TemplateVM on a fresh node (POST /qemu + POST /qemu/{id}/template).
//    PVE-side result: qm object with template=1, pveconform tag, stopped.
// 2. Idempotency: second cycle produces zero actions.
// 3. Desired pveconform VM against a live PVE-side template at the same
//    (node, vmid): planner surfaces a non-destructive anomaly (no config or
//    power write) — pinning PVE 9.2's one-way-only /template endpoint.
// 4. Desired pveconform VM against a live PVE-side VM at the same (node,
//    vmid) that is NOT a template: planner surfaces a MarkTemplate action
//    (POST /qemu/{id}/template).

func TestE2ETemplateVMCreateMarksAndIsIdempotent(t *testing.T) {
	const tplDoc = `
apiVersion: proxops/v1alpha1
kind: TemplateVM
metadata:
  name: tpl-m11
spec:
  node: pve01
  vmid: 555
  state: stopped
  memory: 2GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      size: 32GiB
      interface: scsi0
      iothread: true
      controller: virtio-scsi-single
  networks:
    - model: virtio
      bridge: vmbr0
  hardware:
    machine: q35
    bios: ovmf
  options:
    agent: true
`
	h := newHarness(t, map[string]string{"tpl.yaml": tplDoc}, 3)

	// First apply cycle: must plan a Create for the TemplateVM.
	p1 := h.apply(t)
	if p1 == nil {
		t.Fatal("apply returned nil plan")
	}
	foundCreate := false
	for _, a := range p1.Actions {
		if a.What == plan.Create && a.Kind == schema.KindTemplateVM && a.ID == 555 {
			foundCreate = true
		}
	}
	if !foundCreate {
		t.Fatalf("expected a plan.Create for TemplateVM#555; got %+v", p1.Actions)
	}

	// PVE-side result: object present, qm, template=1, pveconform tag.
	if !h.mock.VMExists(node, 555) {
		t.Fatalf("mock PVE: object 555 not present after template create")
	}
	if !h.mock.VMIsTemplate(node, 555) {
		t.Fatalf("mock PVE: object 555 not marked template after create + mark")
	}
	cfg := h.mock.VMConfig(node, 555)
	if !containsTagStr(cfg["tags"], schema.PveOwnershipTag) {
		t.Errorf("mock PVE: template 555 missing pveconform ownership tag; tags=%q", cfg["tags"])
	}
	// PVE-side power must be stopped even though the manifest is explicit.
	if st, _ := h.mock.VMStatus(node, 555); st != "stopped" {
		t.Errorf("mock PVE: template 555 power = %q, want stopped", st)
	}

	// Second cycle: idempotent convergence.
	p2 := h.apply(t)
	if p2 == nil {
		t.Fatal("second apply returned nil plan")
	}
	for _, a := range p2.Actions {
		// A TemplateVM's power verb must never be "start" (PVE would 500).
		if a.Kind == schema.KindTemplateVM && (a.What == plan.Start || a.What == plan.Stop) {
			t.Errorf("idempotent cycle planned a power verb %s on TemplateVM; got %+v", a.What, a)
		}
	}
	if len(p2.Actions) != 0 {
		t.Errorf("idempotent cycle: %d actions remaining, want 0; %v", len(p2.Actions), p2.Actions)
	}
}

func TestE2E_VMDesiredButPVEIsTemplateSurfacesAnomaly(t *testing.T) {
	// Mock PVE has VM#600 marked as a PVE-side template (untagged).
	harnessFiles := map[string]string{
		// A pveconform VM manifest at the same PVE-id.
		"vm.yaml": `
apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: vm-600
spec:
  node: pve01
  vmid: 600
  memory: 1GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      size: 4GiB
  networks:
    - model: virtio
      bridge: vmbr0
  state: stopped
`,
	}
	h := newHarness(t, harnessFiles, 3)
	// Preseed PVE-side template at node|VM|600 (with the pveconform tag
	// so the prune path recognises it as "owned" but the mismatch still
	// forces the anomaly over a normal Drift).
	h.mock.PreloadVMTemplate(node, 600, map[string]string{
		"name":     "existing-pve-tpl",
		"memory":   "1024",
		"cpu":      "host",
		"cores":    "1",
		"scsi0":    "local-lvm:vm-600-disk-0,size=4G",
		"net0":     "virtio=00:00:00:00:00:01,bridge=vmbr0",
		"tags":     schema.PveOwnershipTag,
		"template": "1",
	})

	p := h.diff(t)
	if p == nil {
		t.Fatal("diff returned nil plan")
	}
	if len(p.Anomalies) == 0 {
		t.Fatalf("expected a plan.Anomaly (VM desired against PVE-side template); got %+v", p)
	}
	found := false
	for _, an := range p.Anomalies {
		if an.ID == 600 && strings.Contains(an.Reason, "template") {
			found = true
		}
	}
	if !found {
		t.Errorf("anomaly for 600 mentioning 'template' not found: %v", p.Anomalies)
	}
	// No Update / Start / Stop actions on 600.
	for _, a := range p.Actions {
		if a.ID == 600 && (a.What == plan.Update || a.What == plan.Start || a.What == plan.Stop) {
			t.Errorf("did not expect write action %s on PVE-side-template; got %+v", a.What, a)
		}
	}
}

func TestE2E_VMDesiredAgainstLiveNonTemplateMarksIt(t *testing.T) {
	// Desired TemplateVM at a PVE-id that already exists as a plain qm on
	// PVE: pveconform should plan MarkTemplate, not Create.
	h := newHarness(t, map[string]string{
		"tpl.yaml": `
apiVersion: proxops/v1alpha1
kind: TemplateVM
metadata:
  name: tpl-mark-me
spec:
  node: pve01
  vmid: 610
  memory: 1GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      size: 4GiB
  networks:
    - model: virtio
      bridge: vmbr0
  state: stopped
`,
	}, 3)
	// Preload a pveconform-owned qm at 610 (no template flag yet).
	h.mock.PreloadVM(node, 610, map[string]string{
		"name":   "preexisting-vm",
		"memory": "1024",
		"cpu":    "host",
		"cores":  "1",
		"scsi0":  "local-lvm:vm-610-disk-0,size=4G",
		"net0":   "virtio=00:00:00:00:00:02,bridge=vmbr0",
		"tags":   schema.PveOwnershipTag,
	}, "stopped")

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil")
	}
	markSeen := false
	for _, a := range p.Actions {
		if a.ID == 610 && a.What == plan.MarkTemplate {
			markSeen = true
		}
		if a.ID == 610 && a.What == plan.Create {
			t.Errorf("did not expect a Create for a PVE-side object already present at 610; got %+v", a)
		}
	}
	if !markSeen {
		t.Fatalf("expected a plan.MarkTemplate for 610; got actions=%v; anomalies=%v", p.Actions, p.Anomalies)
	}
	// PVE-side: after the apply, 610 must be template=1.
	if !h.mock.VMIsTemplate(node, 610) {
		t.Errorf("mock PVE: 610 not template-flagged after MarkTemplate apply")
	}
}

// containsTagStr is a local mirror of the helper inside reconcile_test package
// for use in our M11 tests (the existing one is in TestE2ECreatesConverges but
// it's defined inline; we re-declare here to keep the test self-contained).
func containsTagStr(tags string, want string) bool {
	if tags == "" {
		return false
	}
	for _, tok := range strings.Split(tags, ",") {
		if strings.TrimSpace(tok) == want {
			return true
		}
	}
	return false
}

// (M11 e2e tests intentionally do NOT import _context/_time; the harness's
// apply/diff wrappers handle the context.)
var _ = context.Background
var _ interface{} = "m11"
