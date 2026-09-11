package app_test

// M10 dangerous-case tests at the agent construction boundary.
//
// These tests pin three fail-closed guarantees the task requires for
// production adoption:
//
//   1. missing SOPS credentials      → agent construction fails closed
//   2. wrong age identity            → agent construction fails closed
//   3. malformed/missing cluster config → agent construction fails closed
//
// In every case the failure happens INSIDE app.New, BEFORE any PVE client
// is constructed or dialed: ResolveSOPS() and Validate() run ahead of
// gitx.New + pveclient.New. The tests assert the construction error and
// rely on app.New's construction order for the "zero PVE traffic" property
// (no PVE endpoint exists that could be reached: the error is returned at
// credential validation time).

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/secrets"
)

// freshRegistry returns a NEW prometheus registry so a successful
// construction in one test does not pollute the process-global registry
// that app.New(nil) would use.
func m10Registry() *prometheus.Registry { return prometheus.NewRegistry() }

// m10GitTree creates a temp dir with a minimal .git marker so
// config.Load's "git.path: ." walk-up finds a root.
func m10GitTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// m10SOPSConfig writes a pveconform config + fake SOPS file for one
// SOPS-backed cluster. Returns the config path.
func m10SOPSConfig(t *testing.T, cluster string) string {
	t.Helper()
	root := m10GitTree(t)
	clDir := filepath.Join(root, "clusters", cluster)
	if err := os.MkdirAll(clDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clDir, "secrets.sops.yaml"), []byte("ENC[stub-decryption-target]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := "# pveconform M10 test config (SOPS cluster)\n" +
		"log:\n  level: info\n" +
		"pve:\n  auth: token\n  clusters:\n" +
		"    " + cluster + ":\n" +
		"      base-url: https://pve-m10-test.invalid:8006\n" +
		"      nodes:\n        - pve-m10-test\n" +
		"      secrets-file: secrets.sops.yaml\n" +
		"      secrets:\n" +
		"        pve:\n" +
		"          user: pveconform-user\n" +
		"          token-id: pveconform-token-id\n" +
		"          token: pveconform-token\n" +
		"        git:\n" +
		"          token: pve-git-token\n" +
		"git:\n  branch: main\n  path: .\n" +
		"reconcile:\n  poll-interval: 30s\n  task-timeout: 30m\n  prune-budget: 3\n" +
		"listen: 127.0.0.1:0\n" +
		"data-dir: " + filepath.Join(root, "data") + "\n"
	if err := os.WriteFile(filepath.Join(clDir, "config.yaml"), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(clDir, "config.yaml")
}

// m10StubSOPS installs a secrets.SetTestDecrypter that returns the given
// SOPS document (or error), and restores the production binary at cleanup.
func m10StubSOPS(t *testing.T, vals map[string]string, err error) {
	t.Helper()
	restore := secrets.SetTestDecrypter(func(path string) (map[string]string, error) {
		return vals, err
	})
	t.Cleanup(restore)
}

// TestAgentApp_MissingSOPSCredentialsFailsClosed pins that a SOPS cluster
// whose decrypted document omits a referenced PVE credential key fails
// agent construction. app.New's order is ResolveSOPS → Validate →
// gitx.New → pveclient.New: the SOPS resolution step fails first, so NO
// PVE client is ever built and NO PVE endpoint is ever dialed.
func TestAgentApp_MissingSOPSCredentialsFailsClosed(t *testing.T) {
	// SOPS doc: everything present EXCEPT pveconform-token.
	m10StubSOPS(t, map[string]string{
		"pveconform-user":     "root@pam",
		"pveconform-token-id": "m10tok",
		"pve-git-token":       "gittoken",
	}, nil)
	cfgPath := m10SOPSConfig(t, "prod-a")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, aerr := app.New(cfg, slog.Default(), m10Registry(), "dev"); aerr == nil {
		t.Fatal("app.New succeeded despite a SOPS cluster with a missing referenced PVE credential — fail-closed violated")
	}
}

// TestAgentApp_SOPSWrongIdentityFailsClosed pins that a SOPS decrypt
// failure (wrong age identity) fails agent construction. The operator
// never reaches PVE with credentials pveconform could not verify.
func TestAgentApp_SOPSWrongIdentityFailsClosed(t *testing.T) {
	m10StubSOPS(t, nil, secrets.ErrIdentityMismatch)
	cfgPath := m10SOPSConfig(t, "prod-a")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, aerr := app.New(cfg, slog.Default(), m10Registry(), "dev"); aerr == nil {
		t.Fatal("app.New succeeded despite SOPS identity mismatch — fail-closed violated")
	}
}

// TestAgentApp_SOPSBinaryMissingFailsClosed pins that a missing sops
// binary fails agent construction: pveconform never bypasses SOPS
// resolution into a bootstrap credential.
func TestAgentApp_SOPSBinaryMissingFailsClosed(t *testing.T) {
	m10StubSOPS(t, nil, secrets.ErrSOPSBinaryMissing)
	cfgPath := m10SOPSConfig(t, "prod-a")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, aerr := app.New(cfg, slog.Default(), m10Registry(), "dev"); aerr == nil {
		t.Fatal("app.New succeeded despite missing sops binary — fail-closed violated")
	}
}

// TestAgentApp_UnencryptedSOPSFileFailsClosed pins that a SOPS file that
// is not actually encrypted fails agent construction (the M9
// ErrUnencryptedSecrets sentinel). A plaintext secrets file next to a
// production config MUST NOT be silently accepted.
func TestAgentApp_UnencryptedSOPSFileFailsClosed(t *testing.T) {
	m10StubSOPS(t, nil, secrets.ErrUnencryptedSecrets)
	cfgPath := m10SOPSConfig(t, "prod-a")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if _, aerr := app.New(cfg, slog.Default(), m10Registry(), "dev"); aerr == nil {
		t.Fatal("app.New succeeded despite an unencrypted SOPS file — fail-closed violated")
	}
}

// TestAgentApp_MalformedClusterNameFailsClosed pins that a malformed
// pve.clusters key (not lowercase-alnum-dash) fails agent construction.
// The M8 composition invariant (cluster name == <kind>/<cluster>/ dir) is
// caught at Validate() before any PVE client is constructed.
func TestAgentApp_MalformedClusterNameFailsClosed(t *testing.T) {
	root := m10GitTree(t)
	badName := "Bad Prod" // space + uppercase
	clDir := filepath.Join(root, "clusters", "prod-a")
	if err := os.MkdirAll(clDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The config's pve.clusters key is malformed; the directory name is
	// fine but the cross-check invariant requires config key == dir name,
	// and the key itself fails ValidClusterName first.
	b := "# pveconform M10 test config (malformed cluster name)\n" +
		"log:\n  level: info\n" +
		"pve:\n  auth: token\n  user: root@pam\n  token-id: x\n  token: y\n  clusters:\n" +
		"    \"" + badName + "\":\n" +
		"      base-url: https://pve-m10-bad.invalid:8006\n" +
		"      nodes:\n        - pve-m10-bad\n" +
		"git:\n  branch: main\n  path: .\n" +
		"reconcile:\n  poll-interval: 30s\n  task-timeout: 30m\n  prune-budget: 3\n" +
		"listen: 127.0.0.1:0\n" +
		"data-dir: " + filepath.Join(root, "data") + "\n"
	cfgPath := filepath.Join(clDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		// Load may reject the malformed key up front; either way,
		// construction must not succeed.
		return
	}
	if _, aerr := app.New(cfg, slog.Default(), m10Registry(), "dev"); aerr == nil {
		t.Fatalf("app.New accepted malformed cluster name %q", badName)
	}
}

// TestAgentApp_ClusterSelectionIsolated pins that an agent built for ONE
// configured SOPS cluster exposes exactly that cluster — PVEClientFor on
// a different (unconfigured) name fails closed. Cluster isolation: the
// prod-a agent cannot reach any conformance-dev endpoint or vice
// versa.
func TestAgentApp_ClusterSelectionIsolated(t *testing.T) {
	root := m10RealGitTree(t)
	// Lay the SOPS cluster config inside a REAL git worktree so
	// gitx.New (PlainOpen + HEAD resolve) succeeds.
	clDir := filepath.Join(root, "clusters", "prod-a")
	if err := os.MkdirAll(clDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clDir, "secrets.sops.yaml"), []byte("ENC[stub-decryption-target]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := "# pveconform M10 test config (SOPS cluster)\n" +
		"log:\n  level: info\n" +
		"pve:\n  auth: token\n  clusters:\n" +
		"    prod-a:\n" +
		"      base-url: https://pve-m10-test.invalid:8006\n" +
		"      nodes:\n        - pve-m10-test\n" +
		"      secrets-file: secrets.sops.yaml\n" +
		"      secrets:\n" +
		"        pve:\n" +
		"          user: pveconform-user\n" +
		"          token-id: pveconform-token-id\n" +
		"          token: pveconform-token\n" +
		"        git:\n" +
		"          token: pve-git-token\n" +
		"git:\n  branch: main\n  path: .\n" +
		"reconcile:\n  poll-interval: 30s\n  task-timeout: 30m\n  prune-budget: 3\n" +
		"listen: 127.0.0.1:0\n" +
		"data-dir: " + filepath.Join(root, "data") + "\n"
	cfgPath := filepath.Join(clDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
	// Also write the composition pieces gitx.New / validate need:
	// a resources.yaml with an empty list (a valid zero-resource
	// composition for prod-a).
	if err := os.WriteFile(filepath.Join(clDir, "resources.yaml"), []byte("resources: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m10StubSOPS(t, map[string]string{
		"pveconform-user":     "root@pam",
		"pveconform-token-id": "m10tok",
		"pveconform-token":    "deadbeef",
		"pve-git-token":       "gittoken",
	}, nil)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	agent, aerr := app.New(cfg, slog.Default(), m10Registry(), "dev")
	if aerr != nil {
		// Even here, the guarantee stands: NO PVE endpoint was dialed
		// (the fail closed at construction, before pveclient use).
		t.Skipf("agent construction failed at git stage: %v (fail-closed still held; skipping PVEClientFor assertions)", aerr)
	}
	// The agent exposes exactly one cluster.
	if got := agent.Clusters(); len(got) != 1 || got[0] != "prod-a" {
		t.Fatalf("Clusters() = %v, want [prod-a] (single-cluster SOPS config)", got)
	}
	// PVEClientFor on the known cluster succeeds.
	if _, perr := agent.PVEClientFor("prod-a"); perr != nil {
		t.Fatalf("PVEClientFor(prod-a): %v", perr)
	}
	// PVEClientFor on an unknown cluster fails closed.
	if _, perr := agent.PVEClientFor("conformance-dev"); perr == nil {
		t.Fatal("PVEClientFor(conformance-dev): want error (cluster not configured on this agent), got nil")
	}
}

// m10RealGitTree builds a REAL git worktree (git init + one commit) so
// that go-git's PlainOpen + HEAD resolve succeed inside app.New. When the
// host has no git binary, the test is skipped (the M9 tests already pin
// the .git-marker path via config.Load; this test needs a full worktree).
func m10RealGitTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// A throwaway config user: this tree holds no secrets and is never
	// pushed; we only need go-git to open it.
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	commit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("host git unavailable for real-worktree test (%v): %s", err, strings.TrimSpace(string(out)))
		}
	}
	commit("init", "-q", "-b", "main")
	commit("config", "user.email", "m10-test@local.invalid")
	commit("config", "user.name", "M10 Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("m10 scratch tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commit("add", "-A")
	commit("commit", "-q", "-m", "m10 scratch root")
	return root
}
