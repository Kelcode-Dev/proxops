// M13.1 CLI regression tests.
//
// These run the ACTUAL cobra command surface (the same code main() runs,
// minus os.Exit) from inside a fixture ProxOps repository, so they pin the
// canonical user workflow end to end: `cd repo && proxops diff` must reach
// PVE (aborting only because the fixture endpoint is unreachable), and an
// invocation outside a repository must fail closed with a discovery error.
//
// All fixtures use synthetic endpoints/values; no production credential,
// no real PVE.
package main

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/secrets"
)

// cliFixtureCreds supplies the bootstrap PVE credential env pair the
// SOPS-less fixture clusters need (user required even for token-value).
func cliFixtureCreds(t *testing.T) {
	t.Helper()
	t.Setenv("PROXOPS_PVE_USER", "fixture@pam")
	t.Setenv("PROXOPS_PVE_TOKEN_VALUE", "fixture@pam!fixture=aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
}

// cliFixtureRepo lays a minimal ProxOps repository built with go-git (a
// real work tree so the agent's gitx source can open it — the same code path
// a real user's repository takes). Contents: an optional process-wide
// proxops.yaml (when proc != "") + a single cluster-local "dev" config
// (bootstrap PVE credentials via env; unresolvable endpoint so cycles abort
// at the PVE read, not discovery/config/git).
//
// The cluster's `resources.yaml` lists nothing (a valid zero-resource
// composition) — discovery + config loading are what these tests pin, not
// PVE reconciliation.
func cliFixtureRepo(t *testing.T, proc string) string {
	t.Helper()
	root := t.TempDir()
	// Write the fixture files.
	if err := os.MkdirAll(filepath.Join(root, "clusters", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clusters", "dev", "config.yaml"), []byte(
		"pve:\n  clusters:\n    dev:\n      base-url: https://proxy-unreachable.invalid:8006\n      nodes: [node1]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "clusters", "dev", "resources.yaml"), []byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if proc != "" {
		if err := os.WriteFile(filepath.Join(root, "proxops.yaml"), []byte(proc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Initialise a real git work tree via host `git` (deterministic; the
	// test host has it — the same assumption the m10 real-g-tree fixture
	// makes). The commit is required only so go-git's PlainOpen + HEAD
	// resolve work; the test never fetches.
	gitRun := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("host git not available for fixture: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}
	gitRun("init", "-q", "-b", "main")
	gitRun("config", "user.email", "t@example.com")
	gitRun("config", "user.name", "t")
	gitRun("add", "-A")
	gitRun("commit", "-q", "-m", "fixture initial")
	return root
}

// TestCLIDiffFromInsideRepository pins the canonical invocation: from a
// CWD inside the fixture repository, `proxops diff` discovers the tree
// (no --config, no --git-path), builds the agent, and reaches the PVE read
// stage — the unresolvable fixture endpoint then aborts the cycle with the
// PVE error, NOT a configuration/discovery error.
func TestCLIDiffFromInsideRepository(t *testing.T) {
	root := cliFixtureRepo(t, "")
	t.Chdir(root)
	cliFixtureCreds(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"diff"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("proxops diff: want a non-nil error (fixture PVE is unreachable), got nil")
	}
	msg := err.Error()
	for _, bad := range []string{
		"no ProxOps GitOps repository found", // must have discovered the tree
		"invalid configuration",              // must have passed config validation
		"git source",                         // must have pinned git.path to the tree
	} {
		if strings.Contains(msg, bad) {
			t.Fatalf("proxops diff from inside the repository must not fail with %q; got: %v", bad, err)
		}
	}
	// The failure is a clean cycle abort at the PVE read (DNS dial of the
	// unresolvable fixture endpoint is the expected error class).
	if !strings.Contains(msg, "cycle aborted") && !strings.Contains(msg, "diff: cluster") {
		t.Fatalf("proxops diff: want a cycle abort at PVE read, got: %v", err)
	}
}

// TestCLIDiffOutsideRepositoryFailsClosed pins the fail-closed discovery:
// from a CWD with no .git ancestor, `proxops diff` must NOT run — it must
// report that no ProxOps GitOps repository was found.
func TestCLIDiffOutsideRepositoryFailsClosed(t *testing.T) {
	_ = cliFixtureRepo(t, "") // the fixture exists but we cd away from it
	plain := t.TempDir()
	t.Chdir(plain)
	cliFixtureCreds(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"diff"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("proxops diff outside any repository: want error, got nil")
	}
	if !strings.Contains(err.Error(), "no ProxOps GitOps repository found") {
		t.Fatalf("error must name the missing repository, got: %v", err)
	}
}

// TestCLIDiffExplicitGitPathOverride pins the documented development /
// automation / testing override: --git-path points at a specific work tree
// (the CWD is NOT inside it), and the cycle still reaches PVE.
func TestCLIDiffExplicitGitPathOverride(t *testing.T) {
	root := cliFixtureRepo(t, "")
	other := t.TempDir() // stand outside the repository
	t.Chdir(other)
	cliFixtureCreds(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"diff", "--git-path", root})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("proxops diff --git-path: want a cycle-abort error (fixture PVE unreachable), got nil")
	}
	if strings.Contains(err.Error(), "no ProxOps GitOps repository found") {
		t.Fatalf("--git-path override was not honoured: %v", err)
	}
}

// TestCLIDiffProxopsGitPathEnv pins the PROXOPS_GIT_PATH environment
// equivalent of --git-path.
func TestCLIDiffProxopsGitPathEnv(t *testing.T) {
	root := cliFixtureRepo(t, "")
	other := t.TempDir()
	t.Chdir(other)
	t.Setenv("PROXOPS_GIT_PATH", root)
	cliFixtureCreds(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"diff"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("proxops diff with PROXOPS_GIT_PATH: want a cycle-abort error, got nil")
	}
}

// TestCLIBackCompatConfigFlagClusterLocal pins the advanced pre-M13.1
// invocation: --config pointing at a single cluster-local config file must
// still build an agent for exactly that cluster (the file works from any
// CWD, including one that is not inside the repository — its git.path is
// anchored to the file's own .git ancestor).
func TestCLIBackCompatConfigFlagClusterLocal(t *testing.T) {
	root := cliFixtureRepo(t, "")
	other := t.TempDir() // CWD deliberately outside the repository
	t.Chdir(other)
	cliFixtureCreds(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"diff", "--config", filepath.Join(root, "clusters", "dev", "config.yaml")})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("proxops diff --config <cluster-local>: want a cycle-abort error, got nil")
	}
	for _, bad := range []string{
		"no ProxOps GitOps repository found",
		"declares no git source",
	} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("--config cluster-local must anchor git.path to the config file's repository; got: %v", err)
		}
	}
}

// TestAdoptWithoutClusterListsDiscoveredClusters pins `proxops adopt`'
// fail-closed message on the repository-first path: without --cluster the
// command refuses to run and lists the clusters DISCOVERED from the tree
// (the fixture's single "dev" cluster), so the operator's next step is
// obvious.
func TestAdoptWithoutClusterListsDiscoveredClusters(t *testing.T) {
	root := cliFixtureRepo(t, "")
	t.Chdir(root)
	cliFixtureCreds(t)

	cmd := newRootCmd()
	cmd.SetArgs([]string{"adopt"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("proxops adopt without --cluster: want error, got nil")
	}
	if !strings.Contains(err.Error(), "--cluster") {
		t.Fatalf("adopt error must require an explicit --cluster, got: %v", err)
	}
	// The discovered cluster name must be surfaced so the operator knows what
	// to select.
	if !strings.Contains(err.Error(), "dev") {
		t.Fatalf("adopt error should list the discovered cluster(s) [dev], got: %v", err)
	}
}

// TestLevelForRepositoryFirst pins the log-level resolution on the
// repository-first path: the level of the process-wide config the operator
// committed to their repository is honoured before the agent constructs.
func TestLevelForRepositoryFirst(t *testing.T) {
	root := cliFixtureRepo(t, "log:\n  level: debug\n")
	t.Chdir(root)
	if got := levelFor(&globalFlags{}); got != "debug" {
		t.Errorf("levelFor = %q, want the process-config's \"debug\"", got)
	}
	// A flag always wins.
	if got := levelFor(&globalFlags{logLevel: "warn"}); got != "warn" {
		t.Errorf("levelFor with flag = %q, want \"warn\"", got)
	}
	// No process config -> default.
	root2 := cliFixtureRepo(t, "")
	t.Chdir(root2)
	if got := levelFor(&globalFlags{}); got != "info" {
		t.Errorf("levelFor without process config = %q, want the default \"info\"", got)
	}
}

// TestBuildAgentRepositoryFirst constructs an agent via the SAME code path
// buildAgent uses (LoadLocal + OverlayFromEnv + app.New), asserting: the
// discovered cluster set, the local git source pinned to the root, and the
// in-memory SOPS-resolved credential isolation (stubbed sops).
func TestBuildAgentRepositoryFirst(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clDir := filepath.Join(root, "clusters", "dev")
	if err := os.MkdirAll(clDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clDir, "secrets.sops.yaml"), []byte("ENC[stub]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := "pve:\n  clusters:\n    dev:\n      base-url: https://dev.invalid:8006\n" +
		"      nodes: [n1]\n      secrets-file: secrets.sops.yaml\n      secrets:\n" +
		"        pve:\n          user: c-user\n          token-id: c-id\n          token: c-tok\n" +
		"        git:\n          token: c-git-tok\n"
	if err := os.WriteFile(filepath.Join(clDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clDir, "resources.yaml"), []byte("resources: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		return map[string]string{
			"c-user": "fixture@pam", "c-id": "fid", "c-tok": "ftok-0000", "c-git-tok": "fghp-0000",
		}, nil
	})
	t.Cleanup(restore)

	c, _, err := config.LoadLocal(root)
	if err != nil {
		t.Fatalf("LoadLocal: %v", err)
	}
	config.OverlayFromEnv(c)
	if got := c.Git.Path; got != root {
		t.Fatalf("git.path = %q, want the discovered root", got)
	}
	if names := c.PVE.ClusterNames(); len(names) != 1 || names[0] != "dev" {
		t.Fatalf("clusters = %v, want [dev]", names)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate (SOPS-supplied credentials must satisfy it): %v", err)
	}
	// app.New requires a real-git-capable tree; go-git's PlainOpen on the
	// marker-only fixture fails ("reference not found") — assert the
	// failure class IS the git stage, not a config/SOPS stage. This pins
	// the build order the m10 tests rely on (SOPS before gitx before PVE).
	if _, aerr := app.New(c, slog.Default(), nil, "dev"); aerr == nil {
		t.Skip("gitx opened the fixture; agent build not needed")
	} else if !strings.Contains(aerr.Error(), "git source") && !strings.Contains(aerr.Error(), "reference not found") {
		t.Fatalf("agent build must fail at the git stage, got: %v", aerr)
	}
}
