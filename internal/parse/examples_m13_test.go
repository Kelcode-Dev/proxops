package parse_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/config"
	"github.com/Kelcode-Dev/proxops/internal/secrets"
)

// TestExamplesRepositoryFirstDiscovery (M13.1) pins that the shipped
// examples/ tree is a complete, valid ProxOps GitOps repository for the
// canonical, repository-first workflow: discovered from the current
// directory, its optional process-wide proxops.yaml + its cluster-local
// clusters/example/config.yaml load through config.LoadLocal, and the SOPS
// reference resolves against the example cluster directory.
//
// examples/ is a TEMPLATE subtree of the operator repository, not a git
// repository in itself, so the fixture is a copy into a fresh work tree
// with its own .git marker — exactly what a user gets after
// `git clone <their gitops repo> && cd <gitops repo>`.
func TestExamplesRepositoryFirstDiscovery(t *testing.T) {
	src, err := filepath.Abs("../../examples")
	if err != nil {
		t.Fatalf("abs examples: %v", err)
	}
	dst := t.TempDir()
	if err := copyTree(t, src, dst); err != nil {
		t.Fatalf("copy examples fixture: %v", err)
	}
	// Give the copy its own work-tree marker (the template has none).
	if err := os.MkdirAll(filepath.Join(dst, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The canonical user invocation: discover the repository from the tree
	// itself (CWD-equivalent); no --config, no explicit path.
	c, root, err := config.LoadLocal(dst)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if root != dst {
		t.Fatalf("discovered root = %q, want the fixture tree %q", root, dst)
	}
	if c.Git.Path != dst {
		t.Fatalf("git.path = %q, want the discovered repository root", c.Git.Path)
	}
	// The example repository declares exactly one cluster.
	if names := c.PVE.ClusterNames(); len(names) != 1 || names[0] != "example" {
		t.Fatalf("discovered clusters = %v, want [example]", c.PVE.ClusterNames())
	}
	ex, _ := c.PVE.Cluster("example")
	// Cluster-local endpoint + allowlist came from the example config.
	if ex.BaseURL == "" {
		t.Fatalf("example base-url is empty; cluster-local config not merged")
	}
	// The SOPS file resolves against the example cluster directory (not the
	// operator-repository root, not the CWD): the repo-relative path the
	// user wrote in config.yaml becomes an absolute path under the tree.
	wantSops := filepath.Join(dst, "clusters", "example", "secrets.sops.yaml")
	if ex.SecretsFile != wantSops {
		t.Fatalf("example secrets-file = %q, want %q", ex.SecretsFile, wantSops)
	}

	// SOPS path resolution after the configuration refactor: the stubbed
	// decrypt verifies the reference block names the example's keys.
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		return map[string]string{
			"proxops-user":    "example@pam",
			"proxops-token-id": "proxops",
			"proxops-token":   "00000000-0000-0000-0000-000000000000",
			"proxops-git-token": "ghp_0000000000000000000000000000000000000000",
		}, nil
	})
	t.Cleanup(restore)
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS against the example repository: %v", err)
	}
	got := c.SopsResolved["example"]
	if got.User != "example@pam" || got.TokenID != "proxops" || got.GitToken != "ghp_0000000000000000000000000000000000000000" {
		t.Fatalf("SopsResolved[example] = %+v, want the example's synthetic credentials", got)
	}
}

// copyTree copies src (and everything under it) to dst, preserving
// directory/file type and permission bits.
func copyTree(t *testing.T, src, dst string) error {
	t.Helper()
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
