package parse_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/parse"
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
	if want := 9; got != want {
		t.Fatalf("parsed %d resources for cluster example/, want %d", got, want)
	}
}
