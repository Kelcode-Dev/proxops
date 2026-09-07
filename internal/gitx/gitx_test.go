package gitx_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/GizzmoShifu/proxmox-operator/internal/gitx"
	"github.com/GizzmoShifu/proxmox-operator/internal/parse"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// vmManifest is a valid pveconform VM manifest.
const vmManifest = `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: test-vm
spec:
  node: pve01
  vmid: 100
  memory: 4GiB
  cpu:
    type: host
    cores: 2
  disks:
    - storage: local-lvm
      size: 10GiB
  networks:
    - model: virtio
      bridge: vmbr0
`

// lxcManifest is a valid pveconform LXC manifest.
const lxcManifest = `apiVersion: proxops/v1alpha1
kind: LXC
metadata:
  name: test-ct
spec:
  node: pve01
  vmid: 9000
  memory: 1GiB
  cpu:
    cores: 1
  template: test-ctt
  root:
    storage: local
    size: 8GiB
  networks:
    - bridge: vmbr0
`

// cttManifest is a valid pveconform CTTemplate (vztmpl artifact) manifest —
// a downloadable template archive with no PVE numeric id.
const cttManifest = `apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: test-ctt
spec:
  nodes: [pve01]
  storage: local
  filename: debian-13.tar.zst
  url: https://example.com/debian-13.tar.zst
`

// buildRepo creates a git repo at dir containing the given files, commits, and
// returns its absolute path. Uses go-git (no external binary) so the test is
// hermetic.
func buildRepo(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	// fresh repo
	if err := os.RemoveAll(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	rep, err := git.PlainInit(abs, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	wt, err := rep.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(abs, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(name); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	if _, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.com"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// set the default branch to "main" so New's fetch target exists.
	// PlainInit uses default HEAD; ensure main is checked out as current.
	wt.Checkout(&git.CheckoutOptions{Branch: "refs/heads/main", Create: true, Force: true})
	return abs
}

func TestNewURLModeInitialClone(t *testing.T) {
	remote := buildRepo(t, t.TempDir(), map[string]string{"vm.yaml": vmManifest})
	cache := filepath.Join(t.TempDir(), "cache")

	s, err := gitx.New(context.Background(), gitx.Options{
		URL:      remote,
		Branch:   "main",
		CacheDir: cache,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.Rev().IsZero() {
		t.Fatal("expected non-zero rev after initial clone")
	}
	// Working tree must contain the manifest.
	if _, err := os.Stat(filepath.Join(s.WorkDir(), "vm.yaml")); err != nil {
		t.Fatalf("manifest missing in worktree: %v", err)
	}

	// Full pipeline: parse the index off the worktree.
	idx, err := parse.BuildIndex(s.WorkDir())
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	if got := idx.Counts()[schema.KindVM]; got != 1 {
		t.Errorf("VM count = %d, want 1", got)
	}
	vmRef := schema.Ref{Kind: schema.KindVM, Name: "test-vm"}
	res, ok := idx.ByRef(vmRef)
	if !ok {
		t.Fatalf("VM not indexed: %+v", idx.Counts())
	}
	if res.ID() != 100 || res.Node() != "pve01" {
		t.Errorf("wrong identity: id=%d node=%s", res.ID(), res.Node())
	}
}

func TestFetchPicksUpCommit(t *testing.T) {
	remote := buildRepo(t, t.TempDir(), map[string]string{"vm.yaml": vmManifest})
	cache := filepath.Join(t.TempDir(), "cache")

	s, err := gitx.New(context.Background(), gitx.Options{URL: remote, CacheDir: cache})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	before := s.Rev()

	// No-op fetch.
	if changed, err := s.Fetch(context.Background()); err != nil || changed {
		t.Fatalf("no-op fetch: changed=%v err=%v", changed, err)
	}
	if s.Rev() != before {
		t.Fatal("no-op fetch moved rev")
	}

	// Add a new commit on the remote (CTTemplate + LXC; LXC references CTT,
	// so both must land in the same tree for BuildIndex to succeed).
	rep, _ := git.PlainOpen(remote)
	wt, _ := rep.Worktree()
	if err := os.WriteFile(filepath.Join(remote, "ctt.yaml"), []byte(cttManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "lxc.yaml"), []byte(lxcManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("ctt.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("lxc.yaml"); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("add ct", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	// Fetch should detect the move.
	changed, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !changed {
		t.Error("fetch not reported changed after new commit")
	}
	if s.Rev() == before {
		t.Error("rev did not move")
	}
	// Index now sees both objects.
	idx, err := parse.BuildIndex(s.WorkDir())
	if err != nil {
		t.Fatalf("BuildIndex after fetch: %v", err)
	}
	if got := idx.Counts()[schema.KindLXC]; got != 1 {
		t.Errorf("LXC count = %d, want 1", got)
	}
}

func TestLocalMode(t *testing.T) {
	remote := buildRepo(t, t.TempDir(), map[string]string{"vm.yaml": vmManifest})
	s, err := gitx.New(context.Background(), gitx.Options{Local: remote})
	if err != nil {
		t.Fatalf("New local: %v", err)
	}
	if !s.IsLocal() {
		t.Error("IsLocal must be true")
	}
	if s.WorkDir() != remote {
		t.Errorf("WorkDir = %s want %s", s.WorkDir(), remote)
	}
	// mutate in place; local fetch must see it (re-reads HEAD after commit —
	// we commit the change here to move HEAD).
	rep, _ := git.PlainOpen(remote)
	wt, _ := rep.Worktree()
	for f, c := range map[string]string{"ctt.yaml": cttManifest, "lxc.yaml": lxcManifest} {
		if err := os.WriteFile(filepath.Join(remote, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(f); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := wt.Commit("add ct", &git.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@example.com"},
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch local: %v", err)
	}
	_ = changed
	idx, err := parse.BuildIndex(s.WorkDir())
	if err != nil {
		t.Fatalf("BuildIndex local: %v", err)
	}
	if got := idx.Counts()[schema.KindLXC]; got != 1 {
		t.Errorf("LXC count = %d, want 1", got)
	}
}

func TestOptionsMutualExclusion(t *testing.T) {
	if _, err := gitx.New(context.Background(), gitx.Options{URL: "x", Local: "y"}); err == nil {
		t.Error("URL+Local must be rejected")
	}
	if _, err := gitx.New(context.Background(), gitx.Options{}); err == nil {
		t.Error("neither URL nor Local must be rejected")
	}
}
