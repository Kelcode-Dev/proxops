package parse_test

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/parse"
)

// TestExamplesDirParses verifies that the shipped examples/ directory
// round-trips through BuildIndex. Guard against docs/examples drift.
func TestExamplesDirParses(t *testing.T) {
	idx, err := parse.BuildIndex("../../examples")
	if err != nil {
		t.Fatalf("parse examples/: %v", err)
	}
	got := len(idx.List())
	if want := 5; got != want {
		t.Fatalf("parsed %d resources from examples/, want %d", got, want)
	}
}
