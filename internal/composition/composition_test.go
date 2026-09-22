package composition_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/composition"
)

// writeRepo lays out files under a temp repo root.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const isoDoc = "apiVersion: proxops/v1alpha1\nkind: ISO\nmetadata:\n  name: base-iso\nspec:\n  nodes: [n1]\n  storage: local\n  filename: x.iso\n  url: https://example/x.iso\n"
const vmDoc = "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: base-vm\nspec:\n  node: n1\n  vmid: 100\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"

// TestCompositionValid — one cluster consuming a base + a cluster-specific resource.
func TestCompositionValid(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../iso/base/base-iso.yaml\n  - ../../vm/dev/dev-vm.yaml\n",
		"iso/base/base-iso.yaml":      isoDoc,
		"vm/dev/dev-vm.yaml":          vmDoc,
	})
	comps, err := composition.AllCompositions(root, []string{"dev"})
	if err != nil {
		t.Fatalf("AllCompositions: %v", err)
	}
	dev, ok := comps["dev"]
	if !ok {
		t.Fatal("dev composition missing")
	}
	if len(dev.Files) != 2 {
		t.Fatalf("dev files = %v, want 2", dev.Files)
	}
	// deterministic sorted order
	if dev.SortedFiles()[0] != "iso/base/base-iso.yaml" || dev.SortedFiles()[1] != "vm/dev/dev-vm.yaml" {
		t.Errorf("SortedFiles = %v", dev.SortedFiles())
	}
}

// TestCompositionReusableBase — two clusters referencing the SAME base file;
// the base is not owned by either cluster.
func TestCompositionReusableBase(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../iso/base/base-iso.yaml\n",
		"clusters/prod/resources.yaml": "resources:\n  - ../../iso/base/base-iso.yaml\n  - ../../iso/prod/prod-iso.yaml\n",
		"iso/base/base-iso.yaml":      isoDoc,
		"iso/prod/prod-iso.yaml":      strings.Replace(isoDoc, "base-iso", "prod-iso", 1),
	})
	comps, err := composition.AllCompositions(root, []string{"dev", "prod"})
	if err != nil {
		t.Fatalf("AllCompositions: %v", err)
	}
	if len(comps["dev"].Files) != 1 || comps["dev"].Files[0] != "iso/base/base-iso.yaml" {
		t.Errorf("dev files = %v", comps["dev"].Files)
	}
	if len(comps["prod"].Files) != 2 {
		t.Errorf("prod files = %v", comps["prod"].Files)
	}
}

// TestCompositionEmptyResources — a configured cluster with an empty
// resources list is valid (zero desired resources; prunes stay off via the
// empty-desired anomaly guard).
func TestCompositionEmptyResources(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources: []\n",
	})
	comps, err := composition.AllCompositions(root, []string{"dev"})
	if err != nil {
		t.Fatalf("AllCompositions: %v", err)
	}
	if len(comps["dev"].Files) != 0 {
		t.Errorf("dev files = %v, want empty", comps["dev"].Files)
	}
}

// TestCompositionUnknownClusterFailsClosed — a composition directory that has
// no pve.clusters entry is an error (never reconciled, never pruned).
func TestCompositionUnknownClusterFailsClosed(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/ghost/resources.yaml": "resources: []\n",
	})
	_, err := composition.AllCompositions(root, []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "no entry for") {
		t.Fatalf("want unknown-cluster error, got %v", err)
	}
}

// TestCompositionMissingConfiguredCluster — a pve.clusters entry with no
// composition cannot be reconciled: its desired set is unknown, and pruning
// against an unknown desired set is exactly the footgun that fails closed.
func TestCompositionMissingConfiguredCluster(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources: []\n",
	})
	_, err := composition.AllCompositions(root, []string{"dev", "prod"})
	if err == nil || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("want missing-composition error naming prod, got %v", err)
	}
}

// TestCompositionMissingResourceFile — a path that does not exist fails.
func TestCompositionMissingResourceFile(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../vm/gone.yaml\n",
	})
	_, err := composition.AllCompositions(root, []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "missing resource") {
		t.Fatalf("want missing-resource error, got %v", err)
	}
}

// TestCompositionOutsideRoot — a path escaping the repo root fails closed.
func TestCompositionOutsideRoot(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../../etc/passwd\n",
	})
	_, err := composition.AllCompositions(root, []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "escapes the repository root") {
		t.Fatalf("want outside-root error, got %v", err)
	}
}

// TestCompositionDuplicateResource — the same file listed twice fails.
func TestCompositionDuplicateResource(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../iso/base/base-iso.yaml\n  - ../../iso/base/base-iso.yaml\n",
		"iso/base/base-iso.yaml":      isoDoc,
	})
	_, err := composition.AllCompositions(root, []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("want duplicate error, got %v", err)
	}
}

// TestCompositionLegacyLayoutRejected — a resource directly under a kind root
// (the old vm/foo.yaml layout) is rejected, NOT auto-assigned to a cluster.
func TestCompositionLegacyLayoutRejected(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources: []\n",
		"vm/legacy.yaml":              vmDoc,
	})
	_, err := composition.AllCompositions(root, []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "legacy layout") {
		t.Fatalf("want legacy-layout error, got %v", err)
	}
}

// TestCompositionInvalidClusterName — clusters/<name>/ with a malformed name
// fails closed (a composition identity must be a valid identifier).
func TestCompositionInvalidClusterName(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/Bad Name/resources.yaml": "resources: []\n",
	})
	_, err := composition.AllCompositions(root, []string{"bad-name"})
	if err == nil || !strings.Contains(err.Error(), "not a valid cluster-name directory") {
		t.Fatalf("want invalid-name error, got %v", err)
	}
}

// TestCompositionMalformedYAML — a resources.yaml that is neither a mapping
// nor a list fails.
func TestCompositionMalformedYAML(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources: just-a-string\n",
	})
	_, err := composition.AllCompositions(root, []string{"dev"})
	if err == nil {
		t.Fatal("want parse error for scalar resources value, got nil")
	}
}
