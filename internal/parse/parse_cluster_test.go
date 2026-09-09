package parse_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/parse"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// writeM8Repo lays out files under a temp repo root.
func writeM8Repo(t *testing.T, files map[string]string) string {
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

const m8IsoBase = "apiVersion: proxops/v1alpha1\nkind: ISO\nmetadata:\n  name: base-iso\nspec:\n  nodes: [pve01]\n  storage: local\n  filename: debian.iso\n  url: https://example/debian.iso\n"

const m8CTTBase = "apiVersion: proxops/v1alpha1\nkind: CTTemplate\nmetadata:\n  name: base-ctt\nspec:\n  nodes: [pve01]\n  storage: local\n  filename: debian.tar.zst\n  url: https://example/debian.tar.zst\n"

const m8VMDev = "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: dev-vm\nspec:\n  node: pve01\n  vmid: 111\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"

const m8LXCDev = "apiVersion: proxops/v1alpha1\nkind: LXC\nmetadata:\n  name: dev-lxc\nspec:\n  node: pve01\n  vmid: 222\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: base-ctt\n  root: {storage: local-lvm, size: 4GiB}\n  networks: [{bridge: vmbr0}]\n"

// TestBuildClusterIndexUnknownCluster — an unknown cluster name cannot build
// an index (fail closed: no PVE actions can ever target it).
func TestBuildClusterIndexUnknownCluster(t *testing.T) {
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../vm/dev/dev-vm.yaml\n",
		"vm/dev/dev-vm.yaml":          m8VMDev,
	})
	_, err := parse.BuildClusterIndex(root, "ghost", []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "pve.clusters") {
		t.Fatalf("want unknown-cluster error naming pve.clusters, got %v", err)
	}
}

// TestBuildClusterIndexInvalidName — a malformed cluster name fails before
// any filesystem work.
func TestBuildClusterIndexInvalidName(t *testing.T) {
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources: []\n",
	})
	_, err := parse.BuildClusterIndex(root, "Bad Name", []string{"dev"})
	if err == nil {
		t.Fatal("want invalid-name error")
	}
}

// TestBuildClusterIndexBaseReusable — the SAME base ISO is composed by two
// clusters; each cluster's index sees the base resource, and no cross-cluster
// leakage happens: prod's index does NOT see dev's cluster-specific VM.
func TestBuildClusterIndexBaseReusable(t *testing.T) {
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml":  "resources:\n  - ../../iso/base/base-iso.yaml\n  - ../../vm/dev/dev-vm.yaml\n",
		"clusters/prod/resources.yaml": "resources:\n  - ../../iso/base/base-iso.yaml\n",
		"iso/base/base-iso.yaml":       m8IsoBase,
		"vm/dev/dev-vm.yaml":           m8VMDev,
	})
	devIdx, err := parse.BuildClusterIndex(root, "dev", []string{"dev", "prod"})
	if err != nil {
		t.Fatalf("dev index: %v", err)
	}
	prodIdx, err := parse.BuildClusterIndex(root, "prod", []string{"dev", "prod"})
	if err != nil {
		t.Fatalf("prod index: %v", err)
	}
	if _, ok := devIdx.ByRef(schema.Ref{Kind: schema.KindISO, Name: "base-iso"}); !ok {
		t.Error("dev index must contain the base ISO")
	}
	if _, ok := prodIdx.ByRef(schema.Ref{Kind: schema.KindISO, Name: "base-iso"}); !ok {
		t.Error("prod index must contain the base ISO (reusable by reference)")
	}
	if len(prodIdx.List()) != 1 {
		t.Errorf("prod index = %d resources, want exactly the base ISO", len(prodIdx.List()))
	}
	if _, ok := prodIdx.ByRef(schema.Ref{Kind: schema.KindVM, Name: "dev-vm"}); ok {
		t.Error("prod index must not see dev's cluster-specific VM")
	}
}

// TestBuildClusterIndexSameVMIDAcrossClusters — the same PVE numeric id can
// live in two different clusters: id spaces are scoped to
// (cluster composition, node, id).
func TestBuildClusterIndexSameVMIDAcrossClusters(t *testing.T) {
	shared := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: shared-vm\nspec:\n  node: pveX\n  vmid: 333\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml":  "resources:\n  - ../../vm/dev/shared-vm.yaml\n",
		"clusters/prod/resources.yaml": "resources:\n  - ../../vm/prod/shared-vm.yaml\n",
		"vm/dev/shared-vm.yaml":        shared,
		"vm/prod/shared-vm.yaml":       shared,
	})
	if _, err := parse.BuildClusterIndex(root, "dev", []string{"dev", "prod"}); err != nil {
		t.Fatalf("dev index should be fine (id not shared within a cluster): %v", err)
	}
	if _, err := parse.BuildClusterIndex(root, "prod", []string{"dev", "prod"}); err != nil {
		t.Fatalf("prod index should be fine: %v", err)
	}
}

// TestBuildClusterIndexSameVMIDWithinClusterRejected — the same PVE numeric
// id IS rejected when a VM and an LXC in the SAME cluster's composition
// claim it on one node (PVE's per-node integer id pool is shared).
func TestBuildClusterIndexSameVMIDWithinClusterRejected(t *testing.T) {
	sharedA := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: colliding-a\nspec:\n  node: pve01\n  vmid: 999\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"
	sharedB := "apiVersion: proxops/v1alpha1\nkind: LXC\nmetadata:\n  name: colliding-lxc\nspec:\n  node: pve01\n  vmid: 999\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: base-ctt\n  root: {storage: local-lvm, size: 4GiB}\n  networks: [{bridge: vmbr0}]\n"
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../vm/dev/a.yaml\n  - ../../lxc/dev/b.yaml\n  - ../../ctt/base/base-ctt.yaml\n",
		"vm/dev/a.yaml":               sharedA,
		"lxc/dev/b.yaml":              sharedB,
		"ctt/base/base-ctt.yaml":      m8CTTBase,
	})
	_, err := parse.BuildClusterIndex(root, "dev", []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "PVE id 999") {
		t.Fatalf("want PVE id collision error, got %v", err)
	}
}

// TestBuildClusterIndexSameNameAcrossClustersAllowed — two clusters can both
// carry a resource with the same metadata.name: names are scoped to a
// cluster's Index.
func TestBuildClusterIndexSameNameAcrossClustersAllowed(t *testing.T) {
	shared := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: worker-01\nspec:\n  node: pve01\n  vmid: 555\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"
	shared2 := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: worker-01\nspec:\n  node: pve02\n  vmid: 556\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml":  "resources:\n  - ../../vm/dev/worker-01.yaml\n",
		"clusters/prod/resources.yaml": "resources:\n  - ../../vm/prod/worker-01.yaml\n",
		"vm/dev/worker-01.yaml":        shared,
		"vm/prod/worker-01.yaml":       shared2,
	})
	if _, err := parse.BuildClusterIndex(root, "dev", []string{"dev", "prod"}); err != nil {
		t.Fatalf("dev index: %v", err)
	}
	if _, err := parse.BuildClusterIndex(root, "prod", []string{"dev", "prod"}); err != nil {
		t.Fatalf("prod index: same name across clusters must be allowed: %v", err)
	}
}

// TestBuildClusterIndexSameNameWithinClusterRejected — two files in the same
// cluster's composition with the same metadata.name + kind is a duplicate.
func TestBuildClusterIndexSameNameWithinClusterRejected(t *testing.T) {
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../vm/dev/a.yaml\n  - ../../vm/dev/b.yaml\n",
		"vm/dev/a.yaml":               m8VMDev,
		"vm/dev/b.yaml":               m8VMDev,
	})
	_, err := parse.BuildClusterIndex(root, "dev", []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "duplicate resource") {
		t.Fatalf("want duplicate-resource error, got %v", err)
	}
}

// TestBuildClusterIndexEmptyResources — a cluster with zero declared
// resources builds a valid, empty Index.
func TestBuildClusterIndexEmptyResources(t *testing.T) {
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources: []\n",
	})
	idx, err := parse.BuildClusterIndex(root, "dev", []string{"dev"})
	if err != nil {
		t.Fatalf("empty-resources index: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Errorf("want empty index, got %d resources", len(idx.List()))
	}
	if got := idx.Counts()[schema.KindVM]; got != 0 {
		t.Errorf("VM count = %d, want 0", got)
	}
}

// TestBuildClusterIndexDependencyWithinCluster — a VM→ISO structured edge
// resolves inside the cluster composition.
func TestBuildClusterIndexDependencyWithinCluster(t *testing.T) {
	vmRefISO := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: dev-vm\nspec:\n  node: pve01\n  vmid: 111\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n  hardware:\n    cdrom:\n      iso: base-iso\n"
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../iso/base/base-iso.yaml\n  - ../../vm/dev/dev-vm.yaml\n",
		"iso/base/base-iso.yaml":       m8IsoBase,
		"vm/dev/dev-vm.yaml":           vmRefISO,
	})
	idx, err := parse.BuildClusterIndex(root, "dev", []string{"dev"})
	if err != nil {
		t.Fatalf("dep index: %v", err)
	}
	lv := idx.Levels()
	if lv[schema.Ref{Kind: schema.KindISO, Name: "base-iso"}] != 0 {
		t.Errorf("ISO level = %v, want 0 (base dep)", lv[schema.Ref{Kind: schema.KindISO, Name: "base-iso"}])
	}
	if lv[schema.Ref{Kind: schema.KindVM, Name: "dev-vm"}] != 1 {
		t.Errorf("VM level = %v, want 1", lv[schema.Ref{Kind: schema.KindVM, Name: "dev-vm"}])
	}
}

// TestBuildClusterIndexLXCDependencyWithinCluster — LXC→CTTemplate edge.
func TestBuildClusterIndexLXCDependencyWithinCluster(t *testing.T) {
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../ctt/base/base-ctt.yaml\n  - ../../lxc/dev/dev-lxc.yaml\n",
		"ctt/base/base-ctt.yaml":       m8CTTBase,
		"lxc/dev/dev-lxc.yaml":         m8LXCDev,
	})
	idx, err := parse.BuildClusterIndex(root, "dev", []string{"dev"})
	if err != nil {
		t.Fatalf("LXC dep index: %v", err)
	}
	lv := idx.Levels()
	if lv[schema.Ref{Kind: schema.KindLXC, Name: "dev-lxc"}] != 1 {
		t.Errorf("LXC level = %v (want 1, depends on base-ctt)", lv[schema.Ref{Kind: schema.KindLXC, Name: "dev-lxc"}])
	}
}

// TestBuildClusterIndexCrossClusterRejected — cluster dev's VM references an
// ISO that ONLY cluster prod lists. By construction the ISO is not in dev's
// index → the structured edge is unresolved → fail closed.
func TestBuildClusterIndexCrossClusterRejected(t *testing.T) {
	vmRefISO := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: dev-vm\nspec:\n  node: pve01\n  vmid: 111\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n  hardware:\n    cdrom:\n      iso: base-iso\n"
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml":  "resources:\n  - ../../vm/dev/dev-vm.yaml\n",
		"clusters/prod/resources.yaml": "resources:\n  - ../../iso/base/base-iso.yaml\n",
		"iso/base/base-iso.yaml":       m8IsoBase,
		"vm/dev/dev-vm.yaml":           vmRefISO,
	})
	_, err := parse.BuildClusterIndex(root, "dev", []string{"dev", "prod"})
	if err == nil {
		t.Fatal("want cross-cluster-unresolved-dep error (dev's VM references an ISO that only prod lists)")
	}
	if !strings.Contains(err.Error(), "base-iso") {
		t.Errorf("error should name base-iso, got %v", err)
	}
}

// TestBuildClusterIndexDependencyCycle — two VMs in the same cluster
// referencing each other via depends-on annotations fail closed.
func TestBuildClusterIndexDependencyCycle(t *testing.T) {
	vmA := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: vm-a\n  annotations:\n    proxops/depends-on: \"VM:vm-b\"\nspec:\n  node: pve01\n  vmid: 701\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"
	vmB := "apiVersion: proxops/v1alpha1\nkind: VM\nmetadata:\n  name: vm-b\n  annotations:\n    proxops/depends-on: \"VM:vm-a\"\nspec:\n  node: pve01\n  vmid: 702\n  memory: 1GiB\n  cpu: {type: host, cores: 1}\n  disks: [{storage: local-lvm, size: 4GiB}]\n  networks: [{bridge: vmbr0}]\n"
	root := writeM8Repo(t, map[string]string{
		"clusters/dev/resources.yaml": "resources:\n  - ../../vm/dev/a.yaml\n  - ../../vm/dev/b.yaml\n",
		"vm/dev/a.yaml":               vmA,
		"vm/dev/b.yaml":               vmB,
	})
	_, err := parse.BuildClusterIndex(root, "dev", []string{"dev"})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want dependency cycle error, got %v", err)
	}
}

// TestBuildIndexFromFilesMalformedShape — a file list outside the kind roots
// (e.g. a top-level "notes.yaml" directly under the repo root) is malformed;
// a correct single file still builds.
func TestBuildIndexFromFilesMalformedShape(t *testing.T) {
	root := writeM8Repo(t, map[string]string{
		"notes.yaml":    m8VMDev,
		"vm/dev/x.yaml": m8VMDev,
	})
	_, err := parse.BuildIndexFromFiles(root, []string{"notes.yaml"})
	if err == nil || !strings.Contains(err.Error(), "not under a recognised kind root") {
		t.Fatalf("want kind-root error, got %v", err)
	}
	idx, err2 := parse.BuildIndexFromFiles(root, []string{"vm/dev/x.yaml"})
	if err2 != nil {
		t.Fatalf("valid file list should build: %v", err2)
	}
	if len(idx.List()) != 1 {
		t.Errorf("idx = %d resources, want 1", len(idx.List()))
	}
}
