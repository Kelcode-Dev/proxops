package parse_test

import (
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/parse"
)

// TestExamplesDirParses verifies that the shipped examples/ directory — in
// the M8 multi-cluster layout (clusters/<cluster>/resources.yaml +
// <kind>/{base,<cluster>}/<manifest>.yaml) — round-trips through the
// cluster-scoped index builder. Guard against docs/examples drift.
func TestExamplesDirParses(t *testing.T) {
	idx, err := parse.BuildClusterIndex("../../examples", "example", []string{"example"})
	if err != nil {
		t.Fatalf("BuildClusterIndex: %v", err)
	}
	got := len(idx.List())
	// M13.2 added two VM examples (gpu-passthrough-01 + sops-cloudinit-01),
	// 12 total: base CTTemplate, ISO, DiskImage, LXC, 6 VMs, TemplateVM,
	// TemplateCT. Guard against docs/examples drift.
	if want := 12; got != want {
		t.Fatalf("parsed %d resources for cluster example/, want %d", got, want)
	}
}
