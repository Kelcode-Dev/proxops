// M4 e2e: CTTemplate + ISO reconcile against the stateful mock PVE.
//
// Scenarios covered:
//   - CTT create: desired CTT (source ct exists + templated) → clone + mark
//     template; second cycle idempotent (0 actions)
//   - CTT drift: a live pveconform-tagged LXC at the CTT cid that lost its
//     template flag (running) → stop + re-template; converges
//   - CTT membership: a templated live LXC that is a desired CTT is NOT
//     pruned even though PVE lists it with type "lxc"
//   - ISO download: desired ISO absent on storage → PVE /storage/{s}/download
//     issued; second cycle idempotent (present → 0 actions)
package reconcile_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// cttManifest returns a CTTemplate manifest: clone source → pinned cid.
func cttManifest(name string, dst, src int) string {
	return `apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: ` + name + `
spec:
  node: pve01
  vmid: ` + itoaManifest(dst) + `
  source: ` + itoaManifest(src) + `
  pve-description: baseline golden ct
`
}

// isoManifest returns an ISO manifest.
func isoManifest(name, storage, filename, url string) string {
	return `apiVersion: proxops/v1alpha1
kind: ISO
metadata:
  name: ` + name + `
spec:
  node: pve01
  storage: ` + storage + `
  filename: ` + filename + `
  url: ` + url + `
`
}

// TestE2ECTTCreatesConverges: a source CT (2000, already templated) + desired
// CTT cloning 2000 → 1000. CTT appears on PVE flagged as a template; the
// second cycle is a no-op.
func TestE2ECTTCreatesConverges(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ctt.yaml": cttManifest("golden", 1000, 2000),
	}, 3)
	// Seed the clone source: a templated LXC at 2000 WITHOUT the pveconform tag
	// (sources are not owned; the agent must never prune it).
	h.mock.PreloadCTTemplate(node, 2000, map[string]string{"name": "source-baseline", "memory": "4096"})

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	var creates int
	for _, a := range p.Actions {
		if a.Kind == schema.KindCTTemplate && a.What == plan.Create {
			creates++
		}
	}
	if creates != 1 {
		t.Fatalf("expected exactly 1 CTT create, got %d: %+v", creates, p.Actions)
	}
	// PVE: 1000 exists and is a template.
	if !h.mock.VMExists(node, 1000) {
		t.Fatalf("expected CTT 1000 on %s; plan=%+v", node, p.Actions)
	}
	if !h.mock.CTIsTemplate(node, 1000) {
		t.Fatalf("expected 1000 flagged as template; cfg=%+v", h.mock.CTConfig(node, 1000))
	}
	// The source must have been left untouched.
	if !h.mock.VMExists(node, 2000) || !h.mock.CTIsTemplate(node, 2000) {
		t.Fatal("clone source 2000 should be untouched")
	}

	// Idempotency: second cycle 0 actions.
	p2 := h.apply(t)
	if p2 == nil {
		t.Fatal("second apply returned nil plan")
	}
	if len(p2.Actions) != 0 {
		t.Errorf("expected 0 actions once CTT converged, got %+v", p2.Actions)
	}
}

// TestE2ECTTDriftRetemplates: a live tagged LXC at the CTT cid lost its
// template flag and is RUNNING. The planner must stop + re-template; the
// next cycle converges.
func TestE2ECTTDriftRetemplates(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ctt.yaml": cttManifest("golden", 1000, 2000),
	}, 3)
	h.mock.PreloadCTTemplate(node, 2000, map[string]string{"name": "source-baseline", "memory": "4096"})
	// 1000 exists as a running, un-templated, pveconform-tagged LXC.
	h.mock.PreloadLXC(node, 1000, map[string]string{"name": "golden", "memory": "4096", "tags": "pveconform"}, "running")

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	var stopFirstUpdates int
	for _, a := range p.Actions {
		if a.Kind != schema.KindCTTemplate {
			continue
		}
		if a.What == plan.Update && a.StopFirst {
			stopFirstUpdates++
		}
	}
	if stopFirstUpdates != 1 {
		t.Fatalf("expected 1 StopFirst Update for CTT drift, got %d: %+v", stopFirstUpdates, p.Actions)
	}

	if !h.mock.CTIsTemplate(node, 1000) {
		t.Fatalf("1000 not templated after drift update; cfg=%+v", h.mock.CTConfig(node, 1000))
	}
	if st, _ := h.mock.LXCStatus(node, 1000); st != "stopped" {
		t.Errorf("templated CTT should end stopped (no power state), got %q", st)
	}

	// Converged second cycle.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Errorf("expected 0 actions after re-template, got %+v", p2.Actions)
	}
}

// TestE2EISOAbsentDownloads: a desired ISO absent from storage triggers a PVE
// download; the second cycle finds it present and does nothing.
func TestE2EISOAbsentDownloads(t *testing.T) {
	isoURL := "https://releases.example.com/talos/1.8.0/v1.8.0/x86_64"
	isoName := "talos-1.8.0.iso"
	h := newHarness(t, map[string]string{
		"iso.yaml": isoManifest("talos-180", "local", isoName, isoURL),
	}, 3)

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	var isoCreates int
	for _, a := range p.Actions {
		if a.Kind == schema.KindISO && a.What == plan.Create {
			isoCreates++
		}
	}
	if isoCreates != 1 {
		t.Fatalf("expected 1 ISO download action, got %d: %+v", isoCreates, p.Actions)
	}
	if !h.mock.ISOExists(node, "local", isoName) {
		t.Fatalf("ISO %s should be present on %s/%s after apply", isoName, node, "local")
	}

	// Idempotency.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Errorf("expected 0 actions for a present ISO, got %+v", p2.Actions)
	}
}

// TestE2EISOAlreadyPresentNoop: an ISO that is already on storage → no
// download; the plan reports zero active actions for it.
func TestE2EISOAlreadyPresentNoop(t *testing.T) {
	isoName := "talos-1.8.0.iso"
	h := newHarness(t, map[string]string{
		"iso.yaml": isoManifest("talos-180", "local", isoName, "https://x/"+isoName),
	}, 3)
	h.mock.PreloadISO(node, "local", isoName)

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	if len(p.Actions) != 0 {
		t.Fatalf("present ISO must not produce actions, got %+v", p.Actions)
	}
}

// TestE2ECTTAlreadyPresentNoop: a live pveconform-tagged, templated LXC at
// the desired CTT cid → no clone, no re-template; converges on first cycle.
func TestE2ECTTAlreadyPresentNoop(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ctt.yaml": cttManifest("golden", 1000, 2000),
	}, 3)
	// 2000 templated LXC (the source)
	h.mock.PreloadCTTemplate(node, 2000, map[string]string{"name": "src", "memory": "4096"})
	// 1000 already exists as a tagged, templated LXC that matches the desired
	// CTT exactly.
	h.mock.PreloadLXC(node, 1000,
		map[string]string{"name": "golden", "memory": "4096", "template": "1", "tags": "pveconform"},
		"stopped")

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	if len(p.Actions) != 0 {
		t.Fatalf("expected 0 actions for already-templated CTT, got %+v", p.Actions)
	}
	if !h.mock.CTIsTemplate(node, 1000) {
		t.Fatalf("1000 should remain templated after cycle")
	}
}

// TestE2ECTTNotPrunedWhenLiveInGit: a templated live LXC at the CTT cid
// (which PVE lists as type "lxc") must not be pruned just because PVE's
// listing has type "lxc" rather than "cttemplate". The planner treats it as
// membership in the desired set (via isCTTOfDesired).
func TestE2ECTTNotPrunedWhenLiveInGit(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ctt.yaml": cttManifest("golden", 1000, 2000),
	}, 3)
	h.mock.PreloadCTTemplate(node, 2000, map[string]string{"name": "src", "memory": "4096"})
	// 1000 is templated + tagged and matches the CTT ref.
	h.mock.PreloadLXC(node, 1000,
		map[string]string{"name": "golden", "memory": "4096", "template": "1", "tags": "pveconform"},
		"stopped")

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	// No action of any shape should fire: not a prune, not a clone, not a
	// re-template.
	for _, a := range p.Actions {
		if a.Kind == schema.KindCTTemplate {
			t.Fatalf("unexpected CTT action %+v", a)
		}
	}
	// 1000 must still be present after the cycle.
	if !h.mock.VMExists(node, 1000) {
		t.Fatalf("1000 pruned despite being a desired CTT")
	}
}
