// M13.1 SOPS + repository-first cross-check.
//
// These tests pin the regression the task names explicitly: after the
// configuration refactor, the SOPS security model still holds when proxops
// discovers its repository from the CWD (the canonical user workflow, not
// the advanced "point --config at a single cluster-local file" workflow).
//
// The fixtures are TEST-ONLY synthetic values (no production credential,
// no real PVE, no real git). The private age identity path is faked via
// secrets.SetTestDecrypter, so no sops binary is required.
//
// The "backward-compat" SOPS tests in sops_test.go (the config.Load /
// cluster-local `git.path: "."` shape) stay in that file — they pin an
// advanced path that LoadLocal does not use. This file pins the canonical
// local path specifically.
package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/app"
	"github.com/Kelcode-Dev/proxops/internal/config"
	"github.com/Kelcode-Dev/proxops/internal/secrets"
)

// m13RepositoryFixture lays a minimal ProxOps repository under root, with
// optional N cluster-local SOPS configs. Returns (root, abs paths to the
// cluster-local configs).
//
// The process-wide proxops.yaml (optional) is written if `proc` is non-empty.
// The cluster's `name` matches the directory name (the M8 key==dir invariant).
func m13RepositoryFixture(t *testing.T, proc string, clusters []string, withSOPS []string) (root string, cfgPaths map[string]string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if proc != "" {
		if err := os.WriteFile(filepath.Join(root, "proxops.yaml"), []byte(proc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfgPaths = map[string]string{}
	for _, name := range clusters {
		clDir := filepath.Join(root, "clusters", name)
		if err := os.MkdirAll(clDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfgPaths[name] = filepath.Join(clDir, "config.yaml")
		// The SOPS file bytes are irrelevant for the stub decrypter; only
		// the EXISTING PATH matters for Load's per-cluster-dir relative
		// resolution.
		isSOPS := false
		for _, s := range withSOPS {
			if s == name {
				isSOPS = true
			}
		}
		if isSOPS {
			if err := os.WriteFile(filepath.Join(clDir, "secrets.sops.yaml"), []byte("ENC[stub]\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var b strings.Builder
		b.WriteString("# ProxOps M13.1 repository fixture (test only)\n")
		b.WriteString("pve:\n")
		b.WriteString("  auth: token\n")
		b.WriteString("  clusters:\n")
		b.WriteString("    " + name + ":\n")
		b.WriteString("      base-url: https://" + name + "-pve.invalid:8006\n")
		b.WriteString("      nodes: [" + name + "-node]\n")
		if isSOPS {
			b.WriteString("      secrets-file: secrets.sops.yaml\n")
			b.WriteString("      secrets:\n")
			b.WriteString("        pve:\n")
			b.WriteString("          user: " + name + "-user\n")
			b.WriteString("          token-id: " + name + "-id\n")
			b.WriteString("          token: " + name + "-tok\n")
			b.WriteString("        git:\n")
			b.WriteString("          token: " + name + "-git-tok\n")
		}
		if err := os.WriteFile(cfgPaths[name], []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A resource composition for each cluster so BuildClusterIndex /
	// Validate do not fail the "composition with no endpoint" check.
	for _, name := range clusters {
		compDir := filepath.Join(root, "clusters", name)
		if err := os.WriteFile(filepath.Join(compDir, "resources.yaml"), []byte("resources: []\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, cfgPaths
}

// TestLoadLocalSOPSResolutionEndToEnd pins the canonical local-mode
// SOPS path (after the M13.1 refactor): discover repository from CWD,
// load every cluster-local config, decrypt every SOPS file, pin per-
// cluster effective credentials. All fixture values are distinct per
// cluster so a leak is unmistakable.
func TestLoadLocalSOPSResolutionEndToEnd(t *testing.T) {
	root, _ := m13RepositoryFixture(t, "", []string{"alpha", "beta"}, []string{"alpha", "beta"})
	// Stub decrypter that returns per-cluster values based on the
	// absolute SOPS path it is called with.
	restore := secrets.SetTestDecrypter(func(path string) (map[string]string, error) {
		if strings.Contains(path, "alpha") {
			return map[string]string{
				"alpha-user": "alpha@pam", "alpha-id": "alpha-id", "alpha-tok": "alpha-tok-0000",
				"alpha-git-tok": "alpha-ghp",
			}, nil
		}
		if strings.Contains(path, "beta") {
			return map[string]string{
				"beta-user": "beta@pam", "beta-id": "beta-id", "beta-tok": "beta-tok-1111",
				"beta-git-tok": "beta-ghp",
			}, nil
		}
		return nil, secrets.ErrMalformedDocument
	})
	t.Cleanup(restore)

	c, gotRoot, err := config.LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if gotRoot != root {
		t.Fatalf("root = %q, want %q", gotRoot, root)
	}
	if c.Git.Path != root {
		t.Fatalf("git.path = %q, want the discovered repository root %q (no config was written; it should still pin)", c.Git.Path, root)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	// Per-cluster secrets-file pinning must be cluster-dir-anchored, not root/
	// not CWD.
	alpha, _ := c.PVE.Cluster("alpha")
	beta, _ := c.PVE.Cluster("beta")
	wantAlpha := filepath.Join(root, "clusters", "alpha", "secrets.sops.yaml")
	wantBeta := filepath.Join(root, "clusters", "beta", "secrets.sops.yaml")
	if alpha.SecretsFile != wantAlpha {
		t.Errorf("alpha secrets-file = %q, want %q", alpha.SecretsFile, wantAlpha)
	}
	if beta.SecretsFile != wantBeta {
		t.Errorf("beta secrets-file = %q, want %q", beta.SecretsFile, wantBeta)
	}
	// Per-cluster EFFECTIVE credentials (via PVEParamsFrom).
	pa := app.PVEParamsFrom(c, "alpha")
	pb := app.PVEParamsFrom(c, "beta")
	if pa.User != "alpha@pam" || pa.TokenID != "alpha-id" || pa.Token != "alpha-tok-0000" {
		t.Errorf("alpha PVEParams = user=%q id=%q tok=%q; want alpha values", pa.User, pa.TokenID, pa.Token)
	}
	if pb.User != "beta@pam" || pb.TokenID != "beta-id" || pb.Token != "beta-tok-1111" {
		t.Errorf("beta PVEParams = user=%q id=%q tok=%q; want beta values", pb.User, pb.TokenID, pb.Token)
	}
	// No cross-cluster leak: alpha MUST NOT see beta's token and vice-versa.
	if strings.Contains(pa.Credential(), "beta") || strings.Contains(pb.Credential(), "alpha") {
		t.Errorf("cross-cluster SOPS leak: pa=%q pb=%q", pa.Credential(), pb.Credential())
	}
}

// TestLoadLocalBootstrapClusterUnaffected — a repository mixing SOPS and
// bootstrap (no SOPS) clusters: LoadLocal must still pin bootstrap
// credentials for the SOPS-less cluster and must NOT leak the SOPS cluster's
// credentials onto it.
func TestLoadLocalBootstrapClusterUnaffected(t *testing.T) {
	proc := "pve:\n  auth: token\n  user: bootstrap@pve\n  token-id: bootstrap-id\n  token: bootstrap-tok\n"
	root, _ := m13RepositoryFixture(t, proc, []string{"alpha", "beta"}, []string{"alpha"})
	restore := secrets.SetTestDecrypter(func(path string) (map[string]string, error) {
		return map[string]string{
			"alpha-user": "alpha@pam", "alpha-id": "alpha-id", "alpha-tok": "alpha-tok-0000",
			"alpha-git-tok": "alpha-ghp",
		}, nil
	})
	t.Cleanup(restore)

	c, _, err := config.LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	// alpha: SOPS credentials.
	pa := app.PVEParamsFrom(c, "alpha")
	if pa.User != "alpha@pam" || pa.TokenID != "alpha-id" || pa.Token != "alpha-tok-0000" {
		t.Errorf("alpha: user=%q id=%q tok=%q; want SOPS values", pa.User, pa.TokenID, pa.Token)
	}
	// beta: bootstrap credentials from the process-wide config.
	pb := app.PVEParamsFrom(c, "beta")
	if pb.User != "bootstrap@pve" || pb.TokenID != "bootstrap-id" || pb.Token != "bootstrap-tok" {
		t.Errorf("beta: user=%q id=%q tok=%q; want bootstrap values from the process-wide config", pb.User, pb.TokenID, pb.Token)
	}
	// beta MUST NOT see alpha's credentials.
	if strings.Contains(pb.Credential(), "alpha") {
		t.Errorf("beta PVEParams Credential = %q leaked alpha SOPS value", pb.Credential())
	}
}

// TestLoadLocalProcessConfigFillsGitBranch — a process-wide config that
// pins git.branch carries through to the cluster-local config so the
// reconciler tracks the operator's branch, not the default "main".
func TestLoadLocalProcessConfigFillsGitBranch(t *testing.T) {
	// Fixture does not use URL mode; git.branch is the only git field we
	// want to carry in. A repository with no git block at the process
	// level is valid.
	proc := "git:\n  branch: prod\n"
	root, _ := m13RepositoryFixture(t, proc, []string{"alpha"}, nil)
	c, _, err := config.LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if c.Git.Branch != "prod" {
		t.Errorf("git.branch = %q, want the process-config-carried \"prod\"", c.Git.Branch)
	}
	// git.path was still pinned to the discovered root (local mode).
	if c.Git.Path != root {
		t.Errorf("git.path = %q, want the discovered root %q", c.Git.Path, root)
	}
}

// TestLoadClusterBackCompatSOPS — pins the ADVANCED single-cluster workflow
// the pre-M13.1 user had: point --config at clusters/<name>/config.yaml,
// and git.path "." still resolves to the discovered .git ancestor
// (exactly the M9 SOPS anchor behaviour, unchanged).
func TestLoadClusterBackCompatSOPS(t *testing.T) {
	root, cfgPaths := m13RepositoryFixture(t, "", []string{"alpha"}, []string{"alpha"})
	restore := secrets.SetTestDecrypter(func(path string) (map[string]string, error) {
		return map[string]string{
			"alpha-user": "alpha@pam", "alpha-id": "alpha-id", "alpha-tok": "alpha-tok-0000",
			"alpha-git-tok": "alpha-ghp",
		}, nil
	})
	t.Cleanup(restore)
	c, err := config.LoadCluster(cfgPaths["alpha"])
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	if c.Git.Path != root {
		t.Errorf("git.path = %q, want the discovered root %q (cluster-local, M9 anchor behaviour preserved)", c.Git.Path, root)
	}
	p := app.PVEParamsFrom(c, "alpha")
	if p.Credential() != "alpha@pam!alpha-id=alpha-tok-0000" {
		t.Errorf("Credential = %q", p.Credential())
	}
}
