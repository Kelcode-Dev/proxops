package reconcile_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// M13 e2e: TemplateCT lifecycle against the stateful mock PVE, mirroring the
// M11 TemplateVM scenarios for the LXC side.
//
// Scenarios:
// 1. Create a TemplateCT on a fresh node (POST /lxc + POST /lxc/{id}/template).
//    PVE-side result: lxc object with template=1, proxops tag, stopped.
// 2. Idempotency: second cycle produces zero actions.
// 3. Desired proxops LXC against a live PVE-side template CT at the same
//    (node, cid): planner surfaces a non-destructive anomaly (no config or
//    power write) — pinning PVE 9.2's one-way-only /lxc/{id}/template.
// 4. Desired proxops LXC against a live PVE-side CT at the same (node, cid)
//    that is NOT a template: planner surfaces a MarkTemplate action.

func templateCTManifest(name string, cid int) string {
	return `apiVersion: proxops/v1alpha1
kind: TemplateCT
metadata:
  name: ` + name + `
spec:
  node: pve01
  vmid: ` + itoaManifest(cid) + `
  state: stopped
  memory: 1GiB
  cpu:
    cores: 1
  template: base-ctt
  root:
    storage: local
    size: 4GiB
  networks:
    - bridge: vmbr0
`
}

func TestE2ETemplateCTCreateMarksAndIsIdempotent(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ct.yaml":  templateCTManifest("ct-tpl-m13", 5555),
		"ctt.yaml": cttManifest("base-ctt", "debian-13.tar.zst"),
	}, 3)

	p1 := h.apply(t)
	if p1 == nil {
		t.Fatal("apply returned nil plan")
	}
	foundCreate := false
	for _, a := range p1.Actions {
		if a.What == plan.Create && a.Kind == schema.KindTemplateCT && a.ID == 5555 {
			foundCreate = true
		}
	}
	if !foundCreate {
		t.Fatalf("expected a plan.Create for TemplateCT#5555; got %+v", p1.Actions)
	}
	if !h.mock.CTExists(node, 5555) {
		t.Fatalf("mock PVE: CT 5555 not present after template create")
	}
	if !h.mock.CTIsTemplate(node, 5555) {
		t.Fatalf("mock PVE: CT 5555 not marked template after create + mark")
	}
	cfg := h.mock.CTConfig(node, 5555)
	if !containsTagStr(cfg["tags"], schema.PveOwnershipTag) {
		t.Errorf("mock PVE: template CT 5555 missing proxops tag; tags=%q", cfg["tags"])
	}

	// Second cycle: idempotent convergence, no power verb on the template.
	p2 := h.apply(t)
	if p2 == nil {
		t.Fatal("second apply returned nil plan")
	}
	for _, a := range p2.Actions {
		if a.Kind == schema.KindTemplateCT && (a.What == plan.Start || a.What == plan.Stop) {
			t.Errorf("idempotent cycle planned a power verb %s on TemplateCT; got %+v", a.What, a)
		}
	}
	if len(p2.Actions) != 0 {
		t.Errorf("idempotent cycle: %d actions remaining, want 0; %v", len(p2.Actions), p2.Actions)
	}
}

func TestE2E_LXCDesiredButPVEIsTemplateCTSurfacesAnomaly(t *testing.T) {
	// Mock PVE has CT#6000 marked as a PVE-side template (untagged).
	h := newHarness(t, map[string]string{
		"lxc.yaml": lxcManifest("ct-6000", 6000),
		"ctt.yaml": cttManifest("base-ctt", "debian-13.tar.zst"),
	}, 3)
	h.mock.PreloadCTTemplate(node, 6000, map[string]string{
		"cores":    "1",
		"memory":   "1024",
		"hostname": "ct-6000",
		"rootfs":   "local:4",
		"net0":     "name=net0,bridge=vmbr0",
	})

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	foundAnomaly := false
	for _, a := range p.Anomalies {
		if a.Kind == schema.KindLXC && a.ID == 6000 && a.What == plan.Anomaly {
			foundAnomaly = true
		}
	}
	if !foundAnomaly {
		t.Fatalf("expected an LXC↔TemplateCT anomaly for #6000; anomalies=%+v actions=%+v", p.Anomalies, p.Actions)
	}
	// No config write must have been planned against the template.
	for _, a := range p.Actions {
		if a.ID == 6000 && (a.What == plan.Update || a.What == plan.Create) {
			t.Errorf("planner must not write config onto a PVE template CT; got %+v", a)
		}
	}
}

func TestE2E_LXCDesiredButPVEIsNonTemplateMarksTemplate(t *testing.T) {
	// A desired TemplateCT whose live CT exists but is NOT yet a template
	// → planner emits MarkTemplate (one write).
	h := newHarness(t, map[string]string{
		"ct.yaml":  templateCTManifest("ct-6100", 6100),
		"ctt.yaml": cttManifest("base-ctt", "debian-13.tar.zst"),
	}, 3)
	h.mock.PreloadCT(node, 6100, map[string]string{
		"cores":    "1",
		"memory":   "1024",
		"hostname": "ct-6100",
		"rootfs":   "local:4",
		"net0":     "name=net0,bridge=vmbr0",
		"tags":     schema.PveOwnershipTag,
	}, "stopped")

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	foundMark := false
	for _, a := range p.Actions {
		if a.What == plan.MarkTemplate && a.Kind == schema.KindTemplateCT && a.ID == 6100 {
			foundMark = true
		}
	}
	if !foundMark {
		t.Fatalf("expected a MarkTemplate action for TemplateCT#6100; got %+v", p.Actions)
	}
	if !h.mock.CTIsTemplate(node, 6100) {
		t.Errorf("mock PVE: CT 6100 not marked template after apply")
	}
}
