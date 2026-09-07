package parse_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/parse"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// writeRepo lays out manifest files at root.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const vmA = `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: talos-worker-01
spec:
  node: pve01
  vmid: 142
  memory: 8GiB
  cpu: {type: host, cores: 4}
  disks:
    - {storage: vm_disks, size: 50GiB}
  networks:
    - {model: virtio, bridge: vmbr2}
`

const cttA = `apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: base-ctt
spec:
  nodes: [pve01]
  storage: local
  filename: debian-13.tar.zst
  url: https://example.com/debian-13.tar.zst
`

const isoA = `apiVersion: proxops/v1alpha1
kind: ISO
metadata:
  name: talos-iso
spec:
  nodes: [pve01]
  storage: local
  filename: talos.iso
  url: https://example.com/talos.iso
`

const lxcA = `apiVersion: proxops/v1alpha1
kind: LXC
metadata:
  name: cache-01
spec:
  node: pve01
  vmid: 901
  memory: 2GiB
  cpu: {cores: 1}
  template: base-ctt
  root: {storage: local, size: 10GiB}
  networks:
    - {bridge: vmbr0}
`

func TestBuildIndexValid(t *testing.T) {
	root := writeRepo(t, map[string]string{"vm.yaml": vmA, "lxc.yaml": lxcA, "ctt.yaml": cttA})
	idx, err := parse.BuildIndex(root)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	counts := idx.Counts()
	if counts[schema.KindVM] != 1 {
		t.Errorf("VMs = %d, want 1", counts[schema.KindVM])
	}
	if counts[schema.KindLXC] != 1 {
		t.Errorf("LXC = %d, want 1", counts[schema.KindLXC])
	}
	if counts[schema.KindCTTemplate] != 1 {
		t.Errorf("CTTemplate = %d, want 1", counts[schema.KindCTTemplate])
	}
	vm, ok := idx.ByRef(schema.Ref{Kind: schema.KindVM, Name: "talos-worker-01"})
	if !ok {
		t.Fatalf("VM not indexed")
	}
	if vm.ID() != 142 || vm.Node() != "pve01" || vm.DesiredState() != "started" {
		t.Errorf("VM identity: id=%d node=%s state=%s", vm.ID(), vm.Node(), vm.DesiredState())
	}
	if len(idx.List()) != 3 {
		t.Errorf("List len = %d, want 3", len(idx.List()))
	}
	if len(idx.Files()) != 3 {
		t.Errorf("Files len = %d, want 3", len(idx.Files()))
	}
	// The LXC→CTTemplate dependency must be inferred: level(cache-01)==1,
	// level(base-ctt)==0.
	lv := idx.Levels()
	if got := lv[schema.Ref{Kind: schema.KindLXC, Name: "cache-01"}]; got != 1 {
		t.Errorf("LXC level = %d, want 1 (depends on CTTemplate)", got)
	}
	if got := lv[schema.Ref{Kind: schema.KindCTTemplate, Name: "base-ctt"}]; got != 0 {
		t.Errorf("CTTemplate level = %d, want 0", got)
	}
}

func TestDuplicateNameRejected(t *testing.T) {
	root := writeRepo(t, map[string]string{"a.yaml": vmA, "b.yaml": vmA})
	_, err := parse.BuildIndex(root)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-name error, got: %v", err)
	}
}

func TestPVEIDCollisionSameNodeRejected(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"vm.yaml": vmA,
		"lxc.yaml": `apiVersion: proxops/v1alpha1
kind: LXC
metadata:
  name: collides
spec:
  node: pve01
  vmid: 142
  memory: 2GiB
  cpu: {cores: 1}
  template: base-ctt
  root: {storage: local, size: 4GiB}
`,
		"ctt.yaml": cttA,
	})
	_, err := parse.BuildIndex(root)
	if err == nil || !strings.Contains(err.Error(), "PVE id 142") {
		t.Errorf("expected PVE-id-collision error, got: %v", err)
	}
}

func TestSamePVEIDDifferentNodesAllowed(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"vm1.yaml": vmA,
		"vm2.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: other
spec:
  node: pve02
  vmid: 142
  memory: 4GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 10GiB}
`,
	})
	if _, err := parse.BuildIndex(root); err != nil {
		t.Errorf("same id on different nodes must be allowed: %v", err)
	}
}

func TestUnsupportedAPIVersionRejected(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"vm.yaml": `apiVersion: proxops/v1beta1
kind: VM
metadata:
  name: x
spec:
  node: pve01
  vmid: 1
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 4GiB}
`,
	})
	_, err := parse.BuildIndex(root)
	if err == nil || !strings.Contains(err.Error(), "apiVersion") {
		t.Errorf("expected apiVersion error, got: %v", err)
	}
}

func TestUnknownKindRejected(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"x.yaml": `apiVersion: proxops/v1alpha1
kind: Widget
metadata:
  name: x
`,
	})
	_, err := parse.BuildIndex(root)
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Errorf("expected unknown-kind error, got: %v", err)
	}
}

func TestMalformedManifestRejected(t *testing.T) {
	// Missing spec.memory.
	root := writeRepo(t, map[string]string{
		"vm.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: incomplete
spec:
  node: pve01
  vmid: 142
  cpu: {type: host, cores: 2}
`,
	})
	_, err := parse.BuildIndex(root)
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Errorf("expected memory-required error, got: %v", err)
	}
}

func TestNonYAMLIgnored(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"vm.yaml":   vmA,
		"README.md": "# not a manifest",
		"notes.txt": "plain text",
		".github/x.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: hidden
spec:
  node: pve01
  vmid: 1
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 4GiB}
`,
	})
	idx, err := parse.BuildIndex(root)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	// The hidden dir must be skipped (parse skips dot-dirs? it only skips .git
	// today — assert what we actually do: .github is a dot-dir).
	counts := idx.Counts()
	if counts[schema.KindVM] != 1 {
		t.Errorf("VM count = %d, want 1 (dot-dirs skipped)", counts[schema.KindVM])
	}
}

func TestEmptyDirProducesEmptyIndex(t *testing.T) {
	root := t.TempDir()
	idx, err := parse.BuildIndex(root)
	if err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if len(idx.List()) != 0 {
		t.Errorf("expected empty index, got %d", len(idx.List()))
	}
}

func TestDependsOnEdgeAccepted(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"vm.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: talos-worker-01
  annotations:
    proxops/depends-on: "lxc:cache-01"
spec:
  node: pve01
  vmid: 142
  memory: 8GiB
  cpu: {type: host, cores: 4}
  disks:
    - {storage: vm_disks, size: 50GiB}
  networks:
    - {model: virtio, bridge: vmbr2}
`,
		"lxc.yaml": lxcA,
		"ctt.yaml": cttA,
	})
	idx, err := parse.BuildIndex(root)
	if err != nil {
		t.Errorf("valid depends-on must not error: %v", err)
	}
	// Annotation-driven VM→LXC edge: level of talos-worker-01 must be 2 (LXC
	// is 1 via template, VM is 2 via the annotation).
	if idx == nil {
		return
	}
	lv := idx.Levels()
	if got := lv[schema.Ref{Kind: schema.KindVM, Name: "talos-worker-01"}]; got != 2 {
		t.Errorf("VM level = %d, want 2 (annotation edge LXC→CTTemplate)", got)
	}
}

func TestDependsOnUnknownTargetRejected(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"vm.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: talos-worker-01
  annotations:
    proxops/depends-on: "lxc:ghost-ct"
spec:
  node: pve01
  vmid: 142
  memory: 8GiB
  cpu: {type: host, cores: 4}
  disks:
    - {storage: vm_disks, size: 50GiB}
`,
	})
	// lxc:ghost-ct is not defined anywhere → must be rejected.
	_, err := parse.BuildIndex(root)
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Errorf("expected unknown-dep error, got: %v", err)
	}
}
