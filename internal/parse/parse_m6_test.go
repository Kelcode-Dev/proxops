// M6 parse coverage: structured dependency inference, level ordering,
// cycle fail-closed, and unknown-reference fail-closed.
//
// These tests exercise the production seams — BuildIndex → Levels +
// EdgesFor — against a real manifest tree in a temp dir. No hand-built
// plan.Plan, no mock PVE: pure schema + parse.
package parse_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/parse"
	"github.com/Kelcode-Dev/proxops/internal/schema"
)

// cttManifestV1 returns a CTTemplate manifest (downloadable vztmpl artifact).
func cttManifestV1(name, storage, filename, url string) string {
	return "apiVersion: proxops/v1alpha1\n" +
		"kind: CTTemplate\n" +
		"metadata:\n  name: " + name + "\n" +
		"spec:\n  nodes: [pve01]\n  storage: " + storage + "\n" +
		"  filename: " + filename + "\n" +
		"  url: " + url + "\n"
}

// isoManifestV1 returns an ISO manifest.
func isoManifestV1(name, filename, url string) string {
	return "apiVersion: proxops/v1alpha1\n" +
		"kind: ISO\n" +
		"metadata:\n  name: " + name + "\n" +
		"spec:\n  nodes: [pve01]\n  storage: local\n" +
		"  filename: " + filename + "\n" +
		"  url: " + url + "\n"
}

// lxcManifestV1 returns an LXC manifest that references a CTTemplate.
func lxcManifestV1(name string, vmid int, tpl string) string {
	return "apiVersion: proxops/v1alpha1\n" +
		"kind: LXC\n" +
		"metadata:\n  name: " + name + "\n" +
		"spec:\n  node: pve01\n  vmid: " + itoaM(vmid) + "\n" +
		"  memory: 1GiB\n  cpu: {cores: 1}\n" +
		"  template: " + tpl + "\n" +
		"  root: {storage: local, size: 4GiB}\n"
}

// vmWithCDROMV1 returns a VM manifest with a cdrom that references an ISO.
func vmWithCDROMV1(name string, vmid int, iso string) string {
	return "apiVersion: proxops/v1alpha1\n" +
		"kind: VM\n" +
		"metadata:\n  name: " + name + "\n" +
		"spec:\n  node: pve01\n  vmid: " + itoaM(vmid) + "\n" +
		"  memory: 1GiB\n  cpu: {type: host, cores: 1}\n" +
		"  disks:\n    - {storage: local, size: 4GiB}\n" +
		"  hardware:\n    cdrom:\n      iso: " + iso + "\n"
}

func buildIndex(t *testing.T, files map[string]string) (*parse.Index, error) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return parse.BuildIndex(root)
}

// TestVMIsoDependencyInferred: a VM with spec.hardware.cdrom.iso creates a
// VM→ISO edge without any annotation.
func TestVMIsoDependencyInferred(t *testing.T) {
	idx, err := buildIndex(t, map[string]string{
		"iso.yaml": isoManifestV1("base-iso", "base.iso", "https://x"),
		"vm.yaml":  vmWithCDROMV1("vm-a", 100, "base-iso"),
	})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	vmRef := schema.Ref{Kind: schema.KindVM, Name: "vm-a"}
	isoRef := schema.Ref{Kind: schema.KindISO, Name: "base-iso"}
	edges := idx.EdgesFor(vmRef)
	if len(edges) != 1 || !edges[0].Equal(isoRef) {
		t.Errorf("VM->ISO edge missing; got %v", edges)
	}
	if lvl := idx.Levels()[vmRef]; lvl != 1 {
		t.Errorf("VM level = %d, want 1 (depends on ISO)", lvl)
	}
	if lvl := idx.Levels()[isoRef]; lvl != 0 {
		t.Errorf("ISO level = %d, want 0 (leaf)", lvl)
	}
}

// TestLXCCTTDepsInferred: an LXC with spec.template creates an LXC→CTTemplate edge.
func TestLXCCTTDepsInferred(t *testing.T) {
	idx, err := buildIndex(t, map[string]string{
		"ctt.yaml": cttManifestV1("debian-13", "local", "debian-13.tar.zst", "https://x"),
		"lxc.yaml": lxcManifestV1("lxc-a", 9000, "debian-13"),
	})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	lxcRef := schema.Ref{Kind: schema.KindLXC, Name: "lxc-a"}
	cttRef := schema.Ref{Kind: schema.KindCTTemplate, Name: "debian-13"}
	edges := idx.EdgesFor(lxcRef)
	if len(edges) != 1 || !edges[0].Equal(cttRef) {
		t.Errorf("LXC->CTTemplate edge missing; got %v", edges)
	}
	if lvl := idx.Levels()[lxcRef]; lvl != 1 {
		t.Errorf("LXC level = %d, want 1 (depends on CTT)", lvl)
	}
	if lvl := idx.Levels()[cttRef]; lvl != 0 {
		t.Errorf("CTT level = %d, want 0 (leaf)", lvl)
	}
}

// TestUnknownLXCTemplateRef: an LXC pointing at a non-existent CTTemplate
// must fail the BuildIndex hard.
func TestUnknownLXCTemplateRef(t *testing.T) {
	_, err := buildIndex(t, map[string]string{
		"lxc.yaml": lxcManifestV1("l1", 9000, "ghost"),
	})
	if err == nil {
		t.Fatal("expected BuildIndex to fail on unknown LXC template ref")
	}
	if !containsSubstr(err.Error(), "ghost") ||
		!containsSubstr(err.Error(), "unknown") {
		t.Errorf("error does not name the offending ref: %v", err)
	}
}

// TestUnknownVMISORef: a VM pointing at a non-existent ISO must fail.
func TestUnknownVMISORef(t *testing.T) {
	_, err := buildIndex(t, map[string]string{
		"vm.yaml": vmWithCDROMV1("v1", 100, "ghost"),
	})
	if err == nil {
		t.Fatal("expected BuildIndex to fail on unknown VM cdrom ISO ref")
	}
	if !containsSubstr(err.Error(), "ghost") ||
		!containsSubstr(err.Error(), "unknown") {
		t.Errorf("error does not name the offending ref: %v", err)
	}
}

// TestMultiLevelDAG: a 3-level DAG CTT → LXC → VM.
func TestMultiLevelDAG(t *testing.T) {
	idx, err := buildIndex(t, map[string]string{
		"ctt.yaml": cttManifestV1("gold", "local", "g.tar.zst", "https://x"),
		"lxc.yaml": lxcManifestV1("l1", 9000, "gold"),
		"vm.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: v1
  annotations:
    proxops/depends-on: "lxc:l1"
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 4GiB}
`,
	})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	want := map[schema.Ref]int{
		{Kind: schema.KindCTTemplate, Name: "gold"}: 0,
		{Kind: schema.KindLXC, Name: "l1"}:          1,
		{Kind: schema.KindVM, Name: "v1"}:           2,
	}
	for ref, wantLvl := range want {
		if got := idx.Levels()[ref]; got != wantLvl {
			t.Errorf("level(%s) = %d, want %d", ref, got, wantLvl)
		}
	}
}

// TestCycleFails: two resources annotating each other form a cycle.
func TestCycleFails(t *testing.T) {
	_, err := buildIndex(t, map[string]string{
		"a.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: a
  annotations:
    proxops/depends-on: "vm:b"
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 4GiB}
`,
		"b.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: b
  annotations:
    proxops/depends-on: "vm:a"
spec:
  node: pve01
  vmid: 200
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 4GiB}
`,
	})
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !containsSubstr(err.Error(), "cycle") {
		t.Errorf("error does not mention cycle: %v", err)
	}
}

// TestStructuredDepPlusAnnotation: a 3-node chain CTT (0) → LXC (1, via
// structured template) → VM (2, via annotation depends-on LXC).
func TestStructuredDepPlusAnnotation(t *testing.T) {
	idx, err := buildIndex(t, map[string]string{
		"ctt.yaml": cttManifestV1("g", "local", "g.tar.zst", "https://x"),
		"lxc.yaml": lxcManifestV1("l", 9000, "g"),
		"vm.yaml": `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: v
  annotations:
    proxops/depends-on: "lxc:l"
spec:
  node: pve01
  vmid: 100
  memory: 1GiB
  cpu: {type: host, cores: 1}
  disks:
    - {storage: local, size: 4GiB}
`,
	})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	lv := idx.Levels()
	if got := lv[schema.Ref{Kind: schema.KindLXC, Name: "l"}]; got != 1 {
		t.Errorf("LXC level = %d, want 1", got)
	}
	if got := lv[schema.Ref{Kind: schema.KindVM, Name: "v"}]; got != 2 {
		t.Errorf("VM level = %d, want 2", got)
	}
	edges := idx.EdgesFor(schema.Ref{Kind: schema.KindVM, Name: "v"})
	if len(edges) != 1 || !edges[0].Equal(schema.Ref{Kind: schema.KindLXC, Name: "l"}) {
		t.Errorf("VM edges = %v, want [LXC/l]", edges)
	}
}

func itoaM(i int) string {
	if i == 0 {
		return "0"
	}
	if i < 0 {
		return "-" + itoaM(-i)
	}
	var out strings.Builder
	for i > 0 {
		out.WriteByte('0' + byte(i%10))
		i /= 10
	}
	b := []byte(out.String())
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func containsSubstr(s, sub string) bool {
	if sub == "" {
		return true
	}
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
