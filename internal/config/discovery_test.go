// M13.1 repository-first discovery tests.
//
// These pin the canonical user workflow: a ProxOps GitOps repository is the
// unit of operation. ProxOps discovers the repository from a directory,
// loads its process-wide config (optional) plus every clusters/<name>/
// config.yaml (cluster-local), and pins git.path to the discovered root.
//
// All PVE credentials here are TEST-ONLY fixtures (synthetic domains,
// fake tokens). No production or .env credential appears in this file.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRepo lays a minimal ProxOps repository under root:
//
//	.git/                      (marker only — LoadLocal does not need go-git)
//	proxops.yaml               (process-wide config; optional, content=proc)
//	clusters/<c1>/config.yaml  (cluster-local SOPS config; c1)
//	clusters/<c2>/config.yaml  (cluster-local config; c2)
//
// Returns the root path and a map of the absolute cluster config paths.
func fixtureRepo(t *testing.T, proc string, c1, c1cfg, c2, c2cfg string) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
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
	clDir := filepath.Join(root, "clusters")
	c1Dir := filepath.Join(clDir, c1)
	c2Dir := filepath.Join(clDir, c2)
	if err := os.MkdirAll(c1Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c2Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if c1 != "" {
		if err := os.WriteFile(filepath.Join(c1Dir, "secrets.sops.yaml"), []byte("ENC[stub]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(c1Dir, "config.yaml"), []byte(c1cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c2Dir, "config.yaml"), []byte(c2cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{
		c1: filepath.Join(c1Dir, "config.yaml"),
		c2: filepath.Join(c2Dir, "config.yaml"),
	}
	return root, paths
}

// clusterLocalConfig builds a canonical single-cluster config file.
func clusterLocalConfig(cluster, baseURL string, withSops bool) string {
	var b strings.Builder
	b.WriteString("# ProxOps cluster-local config for " + cluster + "\n")
	b.WriteString("pve:\n")
	b.WriteString("  auth: token\n")
	b.WriteString("  clusters:\n")
	b.WriteString("    " + cluster + ":\n")
	b.WriteString("      base-url: " + baseURL + "\n")
	b.WriteString("      nodes:\n")
	b.WriteString("        - " + cluster + "-node\n")
	if withSops {
		b.WriteString("      secrets-file: secrets.sops.yaml\n")
		b.WriteString("      secrets:\n")
		b.WriteString("        pve:\n")
		b.WriteString("          user: proxops-user\n")
		b.WriteString("          token-id: proxops-token-id\n")
		b.WriteString("          token: proxops-token\n")
		b.WriteString("        git:\n")
		b.WriteString("          token: proxops-git-token\n")
	}
	return b.String()
}

// TestLoadLocalDiscoversRepositoryPinsGitPath — the core M13.1 property:
// LoadLocal from any directory inside the repository resolves to the
// repository root, and c.Git.Path is pinned to it (local mode).
func TestLoadLocalDiscoversRepositoryPinsGitPath(t *testing.T) {
	root, _ := fixtureRepo(t, "", "alpha",
		clusterLocalConfig("alpha", "https://alpha.example:8006", false),
		"beta",
		clusterLocalConfig("beta", "https://beta.example:8006", false))

	c, gotRoot, err := LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if gotRoot != root {
		t.Errorf("root = %q, want %q", gotRoot, root)
	}
	if c.Git.Path != root {
		t.Errorf("git.path = %q, want the discovered repository root %q", c.Git.Path, root)
	}
	if c.Git.URL != "" {
		t.Errorf("git.url = %q, want empty (local mode)", c.Git.URL)
	}
	// Both clusters must be in the discovered config, deterministic order.
	names := c.PVE.ClusterNames()
	if len(names) != 2 || names[0] != "alpha" || names[1] != "beta" {
		t.Errorf("ClusterNames = %v, want [alpha beta] (sorted)", names)
	}
	alpha, _ := c.PVE.Cluster("alpha")
	if alpha.BaseURL != "https://alpha.example:8006" {
		t.Errorf("alpha base-url = %q", alpha.BaseURL)
	}
}

// TestLoadLocalFromSubdirectory — repository-first execution does not care
// which subdirectory the user cd'd into: discovery walks up to the .git
// marker, so `cd clusters/alpha` still finds the repository root.
func TestLoadLocalFromSubdirectory(t *testing.T) {
	root, paths := fixtureRepo(t, "", "alpha",
		clusterLocalConfig("alpha", "https://alpha.example:8006", false),
		"beta",
		clusterLocalConfig("beta", "https://beta.example:8006", false))

	// Start from inside the cluster directory.
	c, gotRoot, err := LoadLocal(paths["alpha"])
	if err != nil {
		t.Fatalf("LoadLocal(subdir): %v", err)
	}
	if gotRoot != root {
		t.Errorf("root = %q, want %q (walked up from the cluster dir)", gotRoot, root)
	}
	if c.Git.Path != root {
		t.Errorf("git.path = %q, want %q", c.Git.Path, root)
	}
}

// TestLoadLocalFailsClosedOutsideARepository — invoking proxops outside a
// git work tree must fail closed with a clear error naming the directory.
func TestLoadLocalFailsClosedOutsideARepository(t *testing.T) {
	// t.TempDir() is /tmp/... and /tmp has no .git ancestor.
	plain := t.TempDir()
	if _, _, err := LoadLocal(plain); err == nil {
		t.Fatal("LoadLocal outside a repository: want error, got nil")
	} else if !strings.Contains(err.Error(), "no ProxOps GitOps repository found") {
		t.Errorf("error must explain the missing repository: %v", err)
	}
}

// TestLoadLocalProcessConfigMerged — a repository that ships a
// process-wide proxops.yaml gets its values (log level, reconcile, listen,
// data-dir, bootstrap credentials, git branch) merged IN alongside the
// cluster-local configs. The cluster entries always win for pve.clusters.
func TestLoadLocalProcessConfigMerged(t *testing.T) {
	root, _ := fixtureRepo(t, `log:
  level: debug
pve:
  auth: ticket
  user: bootstrap@pve
  password: bootstrap-pw
git:
  branch: main
reconcile:
  poll-interval: 10s
  task-timeout: 5m
  prune-budget: 7
listen: 1.2.3.4:9
data-dir: /tmp/fixture-data
`, "alpha",
		clusterLocalConfig("alpha", "https://alpha.example:8006", false),
		"beta",
		clusterLocalConfig("beta", "https://beta.example:8006", false))

	c, _, err := LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level = %q, want debug (from process config)", c.Log.Level)
	}
	if c.PVE.Auth != AuthTicket || c.PVE.User != "bootstrap@pve" || c.PVE.Password != "bootstrap-pw" {
		t.Errorf("bootstrap pve creds = auth=%q user=%q psw=%q; want the process-config values", c.PVE.Auth, c.PVE.User, c.PVE.Password)
	}
	if c.Rec.PollInterval != 10*time.Second {
		t.Errorf("poll-interval = %s, want 10s", c.Rec.PollInterval)
	}
	if c.Rec.TaskTimeout != 5*time.Minute {
		t.Errorf("task-timeout = %s, want 5m", c.Rec.TaskTimeout)
	}
	if c.Rec.PruneBudget != 7 {
		t.Errorf("prune-budget = %d, want 7", c.Rec.PruneBudget)
	}
	if c.Listen != "1.2.3.4:9" {
		t.Errorf("listen = %q, want 1.2.3.4:9", c.Listen)
	}
	if c.DataDir != "/tmp/fixture-data" {
		t.Errorf("data-dir = %q", c.DataDir)
	}
	// The cluster-local pve.clusters entries must be present and correct.
	if _, ok := c.PVE.Cluster("alpha"); !ok {
		t.Error("alpha missing from the discovered config")
	}
	if _, ok := c.PVE.Cluster("beta"); !ok {
		t.Error("beta missing from the discovered config")
	}
}

// TestLoadLocalNoProcessConfigStillWorks — a repository without a
// proxops.yaml is valid: defaults + the cluster-local configs are enough
// for the repository-first workflow.
func TestLoadLocalNoProcessConfigStillWorks(t *testing.T) {
	root, _ := fixtureRepo(t, "", "alpha",
		clusterLocalConfig("alpha", "https://alpha.example:8006", false),
		"beta",
		clusterLocalConfig("beta", "https://beta.example:8006", false))
	c, _, err := LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal without proxops.yaml: %v", err)
	}
	if c.Log.Level != "info" {
		t.Errorf("log.level = %q, want the default info", c.Log.Level)
	}
	if c.Rec.PollInterval != 30*time.Second {
		t.Errorf("poll-interval = %s, want the default 30s", c.Rec.PollInterval)
	}
	if c.PVE.Auth != AuthToken {
		t.Errorf("pve.auth = %q, want the default token", c.PVE.Auth)
	}
	if len(c.PVE.ClusterNames()) != 2 {
		t.Errorf("clusters = %v, want both discovered", c.PVE.ClusterNames())
	}
}

// TestLoadLocalKeyDirInvariantFailsClosed — a cluster-local config whose
// pve.clusters key does not match its directory name is a malformed
// repository: fail closed.
func TestLoadLocalKeyDirInvariantFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	clDir := filepath.Join(root, "clusters", "alpha")
	if err := os.MkdirAll(clDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The key says "beta" but the directory says "alpha".
	bad := strings.Replace(clusterLocalConfig("alpha", "https://alpha.example:8006", false),
		"    alpha:", "    beta:", 1)
	if err := os.WriteFile(filepath.Join(clDir, "config.yaml"), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadLocal(root); err == nil {
		t.Fatal("LoadLocal: want error for key != dir, got nil")
	} else if !strings.Contains(err.Error(), "does not match its directory name") {
		t.Errorf("error must name the invariant: %v", err)
	}
}

// TestLoadLocalNoClustersFailsAtValidate — a repository with no
// clusters/<name>/config.yaml yet: LoadLocal succeeds (discovery), but the
// discovered config has zero pve.clusters entries, so Validate fails closed
// with the M8 "at least one named PVE cluster" error.
func TestLoadLocalNoClustersFailsAtValidate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, _, err := LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	if len(c.PVE.Clusters) != 0 {
		t.Fatalf("expected zero clusters, got %+v", c.PVE.Clusters)
	}
	if verr := c.Validate(); verr == nil {
		t.Error("Validate: a discovered repository with no clusters must fail closed")
	} else if !strings.Contains(verr.Error(), "at least one named PVE cluster") {
		t.Errorf("validate error: %v", verr)
	}
}

// TestLoadLocalIgnoresNonClusterDirs — a clusters/ entry that is NOT a
// directory (or a directory without a config.yaml) is not a cluster; it is
// silently ignored by discovery (a user may put notes under clusters/).
func TestLoadLocalIgnoresNonClusterDirs(t *testing.T) {
	root, _ := fixtureRepo(t, "", "alpha",
		clusterLocalConfig("alpha", "https://alpha.example:8006", false), "",
		"")
	// Add a directory with no config.yaml: silently ignored.
	ghostDir := filepath.Join(root, "clusters", "ghost")
	if err := os.MkdirAll(ghostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghostDir, "README.md"), []byte("not a cluster\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Add a stray file under clusters/: silently ignored.
	if err := os.WriteFile(filepath.Join(root, "clusters", "notes.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _, err := LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	names := c.PVE.ClusterNames()
	if len(names) != 1 || names[0] != "alpha" {
		t.Errorf("ClusterNames = %v, want [alpha] only", names)
	}
}

// TestLoadLocalSOPSPathsResolveAfterDiscovery — the SOPS security model is
// preserved after the refactor: cluster-local secrets-file + secrets refs
// are resolved against the CLUSTER directory (not the repo root, not the
// CWD), and the git.path pin is the repository root. This is the "SOPS path
// resolution after the configuration refactor" regression the task
// requires.
func TestLoadLocalSOPSPathsResolveAfterDiscovery(t *testing.T) {
	root, paths := fixtureRepo(t, "", "alpha",
		clusterLocalConfig("alpha", "https://alpha.example:8006", true),
		"beta",
		clusterLocalConfig("beta", "https://beta.example:8006", false))
	c, _, err := LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	// The SOPS file must be pinned to the cluster directory.
	alpha, _ := c.PVE.Cluster("alpha")
	wantSops := filepath.Join(filepath.Dir(paths["alpha"]), "secrets.sops.yaml")
	if alpha.SecretsFile != wantSops {
		t.Errorf("alpha secrets-file = %q, want %q (absolute, cluster-dir-anchored)", alpha.SecretsFile, wantSops)
	}
	// The SOPS refs block must be carried through.
	if alpha.Secrets.PVE.User != "proxops-user" || alpha.Secrets.PVE.Token != "proxops-token" {
		t.Errorf("alpha secrets refs = %+v, want the fixture refs", alpha.Secrets)
	}
	if alpha.Secrets.Git.Token != "proxops-git-token" {
		t.Errorf("alpha git token ref = %q", alpha.Secrets.Git.Token)
	}
	// git.path is the discovered repository root.
	if c.Git.Path != root {
		t.Errorf("git.path = %q, want %q", c.Git.Path, root)
	}
	_ = c
	_ = alpha
}

// TestLoadClusterBackCompatSingleCluster — advanced workflow preserved:
// pointing --config at a single clusters/<name>/config.yaml must still work
// exactly as before M13.1 (the M9 SOPS anchor), and the git.path pin is the
// discovered root of that file's repository.
func TestLoadClusterBackCompatSingleCluster(t *testing.T) {
	root, paths := fixtureRepo(t, "", "alpha",
		clusterLocalConfig("alpha", "https://alpha.example:8006", false),
		"beta",
		clusterLocalConfig("beta", "https://beta.example:8006", false))

	c, err := LoadCluster(paths["alpha"])
	if err != nil {
		t.Fatalf("LoadCluster: %v", err)
	}
	// The single cluster is loaded and the git path is anchored to the root.
	names := c.PVE.ClusterNames()
	if len(names) != 1 || names[0] != "alpha" {
		t.Errorf("ClusterNames = %v, want [alpha] (single-cluster advanced path)", names)
	}
	if c.Git.Path != root {
		t.Errorf("git.path = %q, want the discovered repository root %q", c.Git.Path, root)
	}
	if c.Git.URL != "" {
		t.Errorf("git.url = %q, want empty", c.Git.URL)
	}
}

// TestDiscoverGitRoot — the low-level discovery helper.
func TestDiscoverGitRoot(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverGitRoot(sub); got != "" {
		t.Errorf("DiscoverGitRoot(plain temp dir) = %q, want empty (no .git)", got)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DiscoverGitRoot(sub); got != root {
		t.Errorf("DiscoverGitRoot(nearest .git) = %q, want %q", got, root)
	}
	// A directory that already IS the .git root walks one step up and stops
	// there only when its parent has a .git marker; the root itself reports.
	if got := DiscoverGitRoot(root); got != root {
		t.Errorf("DiscoverGitRoot(root) = %q, want %q", got, root)
	}
}

// TestLoadLocalNestedReposSelectNearest pins the "commands not
// accidentally resolving resources from outside the selected repository"
// guarantee: when a work tree is itself INSIDE another git repository
// (a nested checkout, or a scratch tree living in an operator's home),
// discovery stops at the NEAREST .git marker. The inner repository's
// clusters are the ones reconciled; the outer repository's clusters are
// never visible to the inner invocation.
func TestLoadLocalNestedReposSelectNearest(t *testing.T) {
	// Outer repository: its own cluster "outer", its own process config.
	outer := t.TempDir()
	mkGit := func(dir string) {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mkGit(outer)
	if err := os.WriteFile(filepath.Join(outer, "proxops.yaml"), []byte("log:\n  level: debug\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outerCl := filepath.Join(outer, "clusters", "outer")
	if err := os.MkdirAll(outerCl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outerCl, "config.yaml"), []byte(
		"pve:\n  clusters:\n    outer:\n      base-url: https://outer.example:8006\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Inner repository: nested inside the outer one, CWD-equivalent is a
	// directory inside the inner.
	innerRoot := filepath.Join(outer, "scratch", "inner")
	if err := os.MkdirAll(filepath.Join(innerRoot, "clusters", "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkGit(innerRoot)
	if err := os.WriteFile(filepath.Join(innerRoot, "clusters", "inner", "config.yaml"), []byte(
		"pve:\n  clusters:\n    inner:\n      base-url: https://inner.example:8006\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Discover from deep inside the inner repository.
	deep := filepath.Join(innerRoot, "clusters", "inner")
	c, root, err := LoadLocal(deep)
	if err != nil {
		t.Fatalf("LoadLocal(nested): %v", err)
	}
	if root != innerRoot {
		t.Fatalf("discovered root = %q, want the NEAREST .git (%q), not the outer repo", root, innerRoot)
	}
	if c.Log.Level == "debug" {
		t.Fatalf("process config was loaded from the OUTER repository (log.level=debug); the inner repository has no proxops.yaml and must not inherit it")
	}
	names := c.PVE.ClusterNames()
	if len(names) != 1 || names[0] != "inner" {
		t.Fatalf("clusters = %v, want [inner] only — the outer repository's clusters must not leak in", names)
	}
	inner, _ := c.PVE.Cluster("inner")
	if inner.BaseURL != "https://inner.example:8006" {
		t.Fatalf("inner base-url = %q", inner.BaseURL)
	}
	if _, ok := c.PVE.Cluster("outer"); ok {
		t.Fatal("outer repository's cluster leaked into the inner invocation")
	}
}

// TestLoadLocalExplicitDirectoryCWDIndependent pins that the explicit-tree
// override (what --git-path / PROXOPS_GIT_PATH pass through to LoadLocal)
// is CWD-independent: the selected tree is read from its own root, no
// matter where the process was launched. This is the "explicit
// repository-path override" regression for development/automation
// contexts. (The directory passed to LoadLocal is what --git-path gives
// the CLI; discovery walks up from THAT, never from the real CWD.)
func TestLoadLocalExplicitDirectoryCWDIndependent(t *testing.T) {
	// Stand-in CWD, unrelated to the selected tree.
	otherCWD := t.TempDir()
	_ = otherCWD
	// The explicitly selected tree.
	sel := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sel, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sel, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	selCl := filepath.Join(sel, "clusters", "sel")
	if err := os.MkdirAll(selCl, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selCl, "config.yaml"), []byte(
		"pve:\n  clusters:\n    sel:\n      base-url: https://sel.example:8006\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, root, err := LoadLocal(sel)
	if err != nil {
		t.Fatalf("LoadLocal(explicit dir): %v", err)
	}
	if root != sel {
		t.Fatalf("root = %q, want the explicitly selected tree %q", root, sel)
	}
	if c.Git.Path != sel {
		t.Fatalf("git.path = %q, want the selected tree (the unrelated CWD must not influence it)", c.Git.Path)
	}
	if names := c.PVE.ClusterNames(); len(names) != 1 || names[0] != "sel" {
		t.Fatalf("clusters = %v, want [sel]", c.PVE.ClusterNames())
	}
}

// TestFileHasGitURL — the URL/cluster disambiguator used by the CLI and
// LoadCluster.
func TestFileHasGitURL(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "url.yaml")
	if err := os.WriteFile(p1, []byte("git:\n  url: https://x.example/repo\n  branch: main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !FileHasGitURL(p1) {
		t.Error("FileHasGitURL: a git.url file must report true")
	}
	p2 := filepath.Join(dir, "local.yaml")
	if err := os.WriteFile(p2, []byte("pve:\n  clusters:\n    a:\n      base-url: h\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if FileHasGitURL(p2) {
		t.Error("FileHasGitURL: a cluster-local file must report false")
	}
	p3 := filepath.Join(dir, "missing.yaml")
	if FileHasGitURL(p3) {
		t.Error("FileHasGitURL: a missing file must report false")
	}
	p4 := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(p4, []byte("git: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A malformed file is treated as "not URL-mode" (it will fail Load later).
	if FileHasGitURL(p4) {
		t.Error("FileHasGitURL: a malformed file must report false")
	}
}
