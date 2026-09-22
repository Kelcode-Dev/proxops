// M4 e2e: CTTemplate + ISO reconcile against the stateful mock PVE.
//
// CTTemplate is a downloadable PVE vztmpl artifact (no PVE cid, no clone):
// ensure the file is present on storage at every declared node, downloading
// from spec.url when absent. ISO is the same artifact shape with
// content="iso".
//
// Scenarios covered:
//   - CTT create: absent on storage → PVE /storage/{s}/download
//     (content=vztmpl); second cycle idempotent (0 actions)
//   - CTT multi-node: one manifest with two nodes → one download each
//   - CTT present: pre-seeded vztmpl → zero actions
//   - ISO: same shape with content=iso
//   - conservative delete: removing artifact manifests never prunes the
//     PVE-side file
package reconcile_test

import (
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/plan"
	"github.com/Kelcode-Dev/proxops/internal/schema"
)

// cttArtifactManifest returns a CTTemplate (vztmpl artifact) manifest.
func cttArtifactManifest(name, storage, filename, url string) string {
	return `apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: ` + name + `
spec:
  nodes: [pve01]
  storage: ` + storage + `
  filename: ` + filename + `
  url: ` + url + `
`
}

// cttMultiArtifactManifest returns a two-node CTTemplate artifact (multi-node placement).
func cttMultiArtifactManifest(name, storage, filename, url string) string {
	return `apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: ` + name + `
spec:
  nodes: [pve01, pve02]
  storage: ` + storage + `
  filename: ` + filename + `
  url: ` + url + `
`
}

// isoManifest returns an ISO manifest.
func isoManifest(name, storage, filename, url string) string {
	return `apiVersion: proxops/v1alpha1
kind: ISO
metadata:
  name: ` + name + `
spec:
  nodes: [pve01]
  storage: ` + storage + `
  filename: ` + filename + `
  url: ` + url + `
`
}

// TestE2ECTTCreatesConverges: a desired CTT absent from local storage
// triggers a PVE /storage/local/download for a vztmpl; second cycle 0 actions.
func TestE2ECTTCreatesConverges(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ctt.yaml": cttArtifactManifest("golden", "local", "debian-13.tar.zst", "https://example.com/debian-13.tar.zst"),
	}, 3)

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
	if !h.mock.TemplateExists(node, "local", "debian-13.tar.zst") {
		t.Fatalf("expected vztmpl on %s/local after apply; plan=%+v", node, p.Actions)
	}

	// Idempotency.
	p2 := h.apply(t)
	if p2 == nil {
		t.Fatal("second apply returned nil plan")
	}
	if len(p2.Actions) != 0 {
		t.Errorf("expected 0 actions once CTT converged, got %+v", p2.Actions)
	}
}

// TestE2ECTTMultiNodePlacesPerNode: a single manifest declaring two nodes
// emits one download per missing node.
func TestE2ECTTMultiNodePlacesPerNode(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ctt.yaml": cttMultiArtifactManifest("golden", "local", "debian-13.tar.zst", "https://example.com/debian-13.tar.zst"),
	}, 3)

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
	if creates != 2 {
		t.Fatalf("expected 2 CTT creates (one per node), got %d: %+v", creates, p.Actions)
	}
	if !h.mock.TemplateExists("pve01", "local", "debian-13.tar.zst") ||
		!h.mock.TemplateExists("pve02", "local", "debian-13.tar.zst") {
		t.Fatalf("expected vztmpl on both nodes; plan=%+v", p.Actions)
	}

	// Idempotency.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Errorf("expected 0 actions once both nodes converged, got %+v", p2.Actions)
	}
}

// TestE2EISOAbsentDownloads: a desired ISO absent from storage triggers a PVE
// download; second cycle finds it present and does nothing.
func TestE2EISOAbsentDownloads(t *testing.T) {
	isoURL := "https://releases.example.com/talos/1.8.0"
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
		t.Fatalf("ISO %s should be present on %s/local after apply", isoName, node)
	}

	// Idempotency.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Errorf("expected 0 actions for a present ISO, got %+v", p2.Actions)
	}
}

// TestE2EISOAlreadyPresentNoop: a pre-seeded ISO on storage → zero actions.
func TestE2EISOAlreadyPresentNoop(t *testing.T) {
	isoName := "talos-1.8.0.iso"
	h := newHarness(t, map[string]string{
		"iso.yaml": isoManifest("talos-180", "local", isoName, "https://x/"+isoName),
	}, 3)
	h.mock.PreloadISO(node, "local", isoName)

	p := h.apply(t)
	if len(p.Actions) != 0 {
		t.Fatalf("present ISO must not produce actions, got %+v", p.Actions)
	}
}

// TestE2ECTTAlreadyPresentNoop: a pre-seeded vztmpl → zero actions.
func TestE2ECTTAlreadyPresentNoop(t *testing.T) {
	h := newHarness(t, map[string]string{
		"ctt.yaml": cttArtifactManifest("golden", "local", "debian-13.tar.zst", "https://x/y"),
	}, 3)
	h.mock.PreloadTemplate(node, "local", "debian-13.tar.zst")

	p := h.apply(t)
	if len(p.Actions) != 0 {
		t.Fatalf("expected 0 actions for a present CTT, got %+v", p.Actions)
	}
}

// TestE2EAbsentPruneNeverTouchesArtifacts: removing every artifact manifest
// while the PVE-side files remain present must produce zero prunes —
// proxops's conservative artifact-deletion guarantee.
func TestE2EAbsentPruneNeverTouchesArtifacts(t *testing.T) {
	// Seed the PVE side with both an ISO and a vztmpl.
	h := newHarness(t, map[string]string{}, 3)
	h.mock.PreloadISO(node, "local", "old-talos.iso")
	h.mock.PreloadTemplate(node, "local", "old-debian.tar.zst")

	p := h.apply(t)
	if len(p.Actions) != 0 {
		t.Fatalf("empty desired must not prune storage artifacts; got %+v", p.Actions)
	}
	if !h.mock.ISOExists(node, "local", "old-talos.iso") {
		t.Fatal("iso should remain even with no manifest")
	}
	if !h.mock.TemplateExists(node, "local", "old-debian.tar.zst") {
		t.Fatal("vztmpl should remain even with no manifest")
	}
}
