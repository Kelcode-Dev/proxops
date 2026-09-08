// Plan-level M6 coverage: artifact handling in the planner.
//
// These tests exercise plan.PlanActions directly with hand-built Live
// Inventory — no git, no mock PVE — to verify planner invariants.
package plan_test

import (
	"context"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// TestPlanArtifactNeverPruned: even with zero desired artifacts,
// storage content in PVE never becomes a prune candidate (planner has no
// PVE-side artifact listing — conservative artifact-deletion guarantee).
func TestPlanArtifactNeverPruned(t *testing.T) {
	var desired []schema.Resource
	live := &plan.LiveInventory{
		Configs: map[string]map[string]any{},
		Power:   map[string]string{},
		Listing: nil,
	}
	p, err := plan.PlanActions(context.Background(), desired, live, plan.PlanOptions{})
	if err != nil {
		t.Fatalf("PlanActions: %v", err)
	}
	if len(p.Actions) != 0 {
		t.Errorf("empty desired + no listing must yield no actions, got %+v", p.Actions)
	}
	if len(p.Deferred) != 0 {
		t.Errorf("no deferred expected, got %+v", p.Deferred)
	}
}

// TestPlanArtifactPresentNoop: an artifact already present at every
// declared node must not produce actions.
func TestPlanArtifactPresentNoop(t *testing.T) {
	iso := schema.NewISO()
	if err := schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: ISO
metadata:
  name: i
spec:
  nodes: [n1, n2]
  storage: local
  filename: x.iso
  url: https://example.com/x.iso
`, iso); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if err := iso.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	live := &plan.LiveInventory{
		Configs: map[string]map[string]any{
			// planner artifact-key is "node|ISO|storage:filename"
			"n1|ISO|local:x.iso": {"present": true},
			"n2|ISO|local:x.iso": {"present": true},
		},
		Power: map[string]string{},
	}
	p, err := plan.PlanActions(context.Background(), []schema.Resource{iso}, live, plan.PlanOptions{})
	if err != nil {
		t.Fatalf("PlanActions: %v", err)
	}
	if len(p.Actions) != 0 {
		t.Errorf("present ISO must yield 0 actions, got %+v", p.Actions)
	}
}

// TestPlanArtifactMissingOnSomeNodes: an artifact missing on one of two
// nodes plans exactly one download (for that node).
func TestPlanArtifactMissingOnSomeNodes(t *testing.T) {
	ctt := schema.NewCTTemplate()
	if err := schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: t
spec:
  nodes: [n1, n2]
  storage: local
  filename: t.tar.gz
  url: https://example.com/t.tar.gz
`, ctt); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := &plan.LiveInventory{
		Configs: map[string]map[string]any{
			"n1|CTTemplate|local:t.tar.gz": {"present": true},
			"n2|CTTemplate|local:t.tar.gz": {"present": false},
		},
		Power: map[string]string{},
	}
	p, err := plan.PlanActions(context.Background(), []schema.Resource{ctt}, live, plan.PlanOptions{})
	if err != nil {
		t.Fatalf("PlanActions: %v", err)
	}
	var downloads int
	for _, a := range p.Actions {
		if a.Kind == schema.KindCTTemplate && a.What == plan.Create {
			downloads++
			if a.Node != "n2" {
				t.Errorf("download planned for node %q, want n2", a.Node)
			}
		}
	}
	if downloads != 1 {
		t.Errorf("expected 1 CTT download (n2 missing), got %d: %v", downloads, p.Actions)
	}
}

// TestPlanArtifactUnreadableFailClosed: when live.Configs has no entry
// for an artifact on a node (read failed), planner must NOT plan a download
// (conservative) but must add a Skipped record.
func TestPlanArtifactUnreadableFailClosed(t *testing.T) {
	iso := schema.NewISO()
	if err := schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: ISO
metadata:
  name: i
spec:
  nodes: [n1]
  storage: local
  filename: x.iso
  url: https://example.com/x.iso
`, iso); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := &plan.LiveInventory{
		Configs: map[string]map[string]any{},
		Power: map[string]string{
			"n1|ISO|local:x.iso": "read-error: pve unreachable",
		},
	}
	p, err := plan.PlanActions(context.Background(), []schema.Resource{iso}, live, plan.PlanOptions{})
	if err != nil {
		t.Fatalf("PlanActions: %v", err)
	}
	var actions, skipped int
	for range p.Actions {
		actions++
	}
	for range p.Skipped {
		skipped++
	}
	if actions != 0 {
		t.Errorf("unreadable ISO must not plan a download; got %d actions", actions)
	}
	if skipped != 1 {
		t.Errorf("unreadable ISO must emit one Skipped record; got %d", skipped)
	}
}

// TestVMRequiresDepsPlannerOrdersISOFirst: a VM referencing an ISO must be
// planned AFTER the ISO download so PVE's create doesn't fail on missing
// storage.
func TestVMRequiresDepsPlannerOrdersISOFirst(t *testing.T) {
	iso := schema.NewISO()
	_ = schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: ISO
metadata:
  name: i
spec:
  nodes: [n1]
  storage: local
  filename: x.iso
  url: https://example.com/x.iso
`, iso)

	vm := schema.NewVM()
	_ = schema.YAMLTo(`apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: v
spec:
  node: n1
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 4GiB}
  hardware:
    cdrom:
      iso: i
`, vm)

	resources := []schema.Resource{iso, vm}
	if err := schema.ResolveArtifactRefs(resources); err != nil {
		t.Fatalf("ResolveArtifactRefs: %v", err)
	}

	live := &plan.LiveInventory{
		Configs: map[string]map[string]any{
			"n1|ISO|local:x.iso": {"present": false},
			// n1|VM|100 intentionally absent (create path for the VM).
		},
		Power: map[string]string{},
	}

	levels := func(ref schema.Ref) int {
		if ref.Kind == schema.KindISO {
			return 0
		}
		if ref.Kind == schema.KindVM {
			return 1
		}
		return 0
	}
	edges := func(ref schema.Ref) []schema.Ref {
		if ref.Kind == schema.KindVM {
			return []schema.Ref{{Kind: schema.KindISO, Name: "i"}}
		}
		return nil
	}

	p, err := plan.PlanActions(context.Background(), resources, live, plan.PlanOptions{
		Levels: levels,
		Edges:  edges,
	})
	if err != nil {
		t.Fatalf("PlanActions: %v", err)
	}
	// The ISO create must come before the VM create in plan order.
	pos := map[pair]int{}
	for i, a := range p.Actions {
		pos[pair{a.Kind, a.Name}] = i
	}
	isoPos, okI := pos[pair{schema.KindISO, "i"}]
	vmPos, okV := pos[pair{schema.KindVM, "v"}]
	if !okI || !okV {
		t.Fatalf("expected both ISO and VM creates in plan; got %+v", p.Actions)
	}
	if isoPos >= vmPos {
		t.Errorf("ISO create must be planned BEFORE VM create; got iso@%d vm@%d", isoPos, vmPos)
	}
	// VM-level 1 should carry a Deps entry pointing at ISO/i.
	foundDeps := false
	for _, a := range p.Actions {
		if a.Kind == schema.KindVM {
			foundDeps = len(a.Deps) == 1 && a.Deps[0].Equal(schema.Ref{Kind: schema.KindISO, Name: "i"})
			break
		}
	}
	if !foundDeps {
		t.Errorf("VM Action.Deps should contain ISO/i; got %+v", p.Actions)
	}
}

type pair struct {
	Kind schema.Kind
	Name string
}
