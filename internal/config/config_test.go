package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.PVE.Auth != AuthToken {
		t.Errorf("default auth = %q, want %q", c.PVE.Auth, AuthToken)
	}
	// BaseURL is intentionally NOT defaulted — it is environment-specific
	// and Validate() rejects an empty value.
	if c.PVE.BaseURL != "" {
		t.Errorf("default base-url should be empty, got %q", c.PVE.BaseURL)
	}
	if len(c.PVE.Nodes) != 0 {
		t.Errorf("default nodes should be empty, got %+v", c.PVE.Nodes)
	}
	if c.Git.Branch != "main" {
		t.Errorf("default branch = %q, want main", c.Git.Branch)
	}
	if c.Rec.PollInterval != 30*time.Second {
		t.Errorf("default poll interval = %s, want 30s", c.Rec.PollInterval)
	}
	if c.Rec.TaskTimeout != 30*time.Minute {
		t.Errorf("default task timeout = %s, want 30m", c.Rec.TaskTimeout)
	}
	if c.Rec.PruneBudget != 3 {
		t.Errorf("default prune budget = %d, want 3", c.Rec.PruneBudget)
	}
	if c.Listen != "127.0.0.1:9494" {
		t.Errorf("default listen = %q", c.Listen)
	}
	if c.Log.Level != "info" {
		t.Errorf("default log level = %q, want info", c.Log.Level)
	}
}

func TestLoadMergesOverDefaults(t *testing.T) {
	y := []byte(`
log:
  level: debug
pve:
  auth: token
  user: pveops@pve
  token-id: ci
  token: tok-abc
  base-url: https://pve-dev-01.example:8006
  nodes:
    - pve-dev-01
    - pve-dev-02
git:
  url: https://git.example.com/infra/pve.yaml
  branch: prod
reconcile:
  poll-interval: 10s
  task-timeout: 5m
  prune-budget: 7
`)
	f := filepath.Join(t.TempDir(), "pveconform.yaml")
	if err := os.WriteFile(f, y, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	if verr := c.Validate(); verr != nil {
		t.Fatalf("Validate on a fully-populated config: %v", verr)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level = %q, want debug", c.Log.Level)
	}
	if c.PVE.User != "pveops@pve" {
		t.Errorf("pve.user = %q", c.PVE.User)
	}
	if c.PVE.BaseURL != "https://pve-dev-01.example:8006" {
		t.Errorf("pve.base-url = %q, want the YAML value", c.PVE.BaseURL)
	}
	if len(c.PVE.Nodes) != 2 || c.PVE.Nodes[0] != "pve-dev-01" || c.PVE.Nodes[1] != "pve-dev-02" {
		t.Errorf("pve.nodes = %+v, want the two YAML entries", c.PVE.Nodes)
	}
	if c.PVE.Auth != AuthToken {
		t.Errorf("pve.auth not preserved = %q", c.PVE.Auth)
	}
	if c.Git.Branch != "prod" {
		t.Errorf("git.branch = %q", c.Git.Branch)
	}
	if c.Rec.PollInterval != 10*time.Second {
		t.Errorf("poll-interval = %s", c.Rec.PollInterval)
	}
	if c.Rec.TaskTimeout != 5*time.Minute {
		t.Errorf("task-timeout = %s", c.Rec.TaskTimeout)
	}
	if c.Rec.PruneBudget != 7 {
		t.Errorf("prune-budget = %d", c.Rec.PruneBudget)
	}
	// Untouched defaults survive the merge.
	if c.Git.Path != "" {
		t.Errorf("git.path should be empty, got %q", c.Git.Path)
	}
}

// TestLoadExpandsTilde pins config.Load's "tilde expansion" for data-dir and
// ca-file. Without it, `data-dir: ~/x` produces a literal ./x relative to
// $PWD (a stale "./~" directory), which is exactly the bug that broke the
// user's first `pveconform diff` run after the PVE-token fix.
func TestLoadExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	y := []byte("data-dir: ~/" + "share/pveconform\npve:\n  ca-file: ~/pve/ca.pem\n")
	f := filepath.Join(t.TempDir(), "tilde.yaml")
	if err := os.WriteFile(f, y, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	wantDataDir := filepath.Join(home, "share/pveconform")
	if c.DataDir != wantDataDir {
		t.Errorf("DataDir = %q, want %q", c.DataDir, wantDataDir)
	}
	wantCA := filepath.Join(home, "pve/ca.pem")
	if c.PVE.CAFile != wantCA {
		t.Errorf("PVE.CAFile = %q, want %q", c.PVE.CAFile, wantCA)
	}

	// Sanity check: when a user types "~" as a bare word, YAML parses
	// it as null (not a string) and leaves the default in place. That's
	// correct YAML behavior; we do not reinterpret null as a tilde
	// expansion.
	// (The expandTilde("~-") case would also not trigger, because that
	// string is not a prefix; it would pass through unchanged.)
	y2 := []byte("data-dir: '~'\n")
	f2 := filepath.Join(t.TempDir(), "tilde2.yaml")
	if err := os.WriteFile(f2, y2, 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(f2)
	if err != nil {
		t.Fatal(err)
	}
	// The YAML single-quoted '~' is the 1-char string "~", which should
	// expand to the home dir.
	if c2.DataDir != home {
		t.Errorf("DataDir = %q, want %q (tilde-only)", c2.DataDir, home)
	}

	// Non-tilde paths pass through unchanged.
	y3 := []byte("data-dir: /var/lib/pveconform\n")
	f3 := filepath.Join(t.TempDir(), "tilde3.yaml")
	if err := os.WriteFile(f3, y3, 0o600); err != nil {
		t.Fatal(err)
	}
	c3, err := Load(f3)
	if err != nil {
		t.Fatal(err)
	}
	if c3.DataDir != "/var/lib/pveconform" {
		t.Errorf("DataDir = %q, want /var/lib/pveconform", c3.DataDir)
	}
}

func TestLoadEmptyPathReturnsDefaults(t *testing.T) {
	c, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if c.Rec.PruneBudget != 3 {
		t.Errorf("prune-budget = %d, want 3", c.Rec.PruneBudget)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(f, []byte("pve: [unclosed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f); err == nil {
		t.Error("expected parse error")
	}
}

func TestValidateRejectsBadCombos(t *testing.T) {
	// No git source.
	c := Defaults()
	if err := c.Validate(); err == nil {
		t.Error("expected error: no git source")
	}

	// Both url and path set.
	c = Defaults()
	c.Git.URL = "https://x"
	c.Git.Path = "/tmp/x"
	c.PVE.User = "u@pve"
	c.PVE.Token = "tok"
	if err := c.Validate(); err == nil {
		t.Error("expected error: url+path both set")
	}

	// Token auth without a token.
	c = Defaults()
	c.Git.Path = "/tmp/x"
	c.PVE.User = "u@pve"
	if err := c.Validate(); err == nil {
		t.Error("expected error: missing pve.token")
	}

	// Token auth with token value but missing user.
	c = Defaults()
	c.Git.Path = "/tmp/x"
	c.PVE.TokenValue = "u@pve!x=y"
	if err := c.Validate(); err == nil {
		t.Error("expected error: missing pve.user for token value")
	}

	// Ticket auth without a password.
	c = Defaults()
	c.Git.Path = "/tmp/x"
	c.PVE.Auth = AuthTicket
	c.PVE.User = "u@pve"
	if err := c.Validate(); err == nil {
		t.Error("expected error: ticket auth requires password")
	}

	// Token auth with a stray password set.
	c = Defaults()
	c.Git.Path = "/tmp/x"
	c.PVE.User = "u@pve"
	c.PVE.Token = "tok"
	c.PVE.Password = "pw"
	if err := c.Validate(); err == nil {
		t.Error("expected error: password must be empty for token auth")
	}
}

func TestValidatePassesForValidConfigs(t *testing.T) {
	c := Defaults()
	c.Git.URL = "https://git.example.com/repo"
	c.PVE.User = "root@pam"
	c.PVE.TokenID = "pveconform"
	c.PVE.Token = "deadbeef"
	c.PVE.BaseURL = "https://pve.example:8006"
	if err := c.Validate(); err != nil {
		t.Fatalf("token mode should validate: %v", err)
	}

	// A composed token value also validates on its own without token-id.
	c3 := Defaults()
	c3.Git.URL = "https://git.example.com/repo"
	c3.PVE.User = "root@pam"
	c3.PVE.TokenValue = "root@pam!pveconform=deadbeef"
	c3.PVE.BaseURL = "https://pve.example:8006"
	if err := c3.Validate(); err != nil {
		t.Fatalf("token-value mode should validate: %v", err)
	}

	c2 := Defaults()
	c2.Git.Path = "/opt/git"
	c2.PVE.Auth = AuthTicket
	c2.PVE.User = "root@pam"
	c2.PVE.Password = "s3cret"
	c2.PVE.BaseURL = "https://pve.example:8006"
	if err := c2.Validate(); err != nil {
		t.Fatalf("ticket mode should validate: %v", err)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("PVECONFORM_PVE_TOKEN", "from-env")
	t.Setenv("PVECONFORM_PVE_PASSWORD", "from-env")
	t.Setenv("PVECONFORM_GIT_TOKEN", "git-from-env")
	c := Defaults()
	c.PVE.User = "u@pve"
	c.PVE.Token = "from-yaml"
	c.Git.URL = "https://x"
	OverlayFromEnv(c)
	if c.PVE.Token != "from-env" {
		t.Errorf("token not overridden by env: %q", c.PVE.Token)
	}
	if c.PVE.Password != "from-env" {
		t.Errorf("password not overridden: %q", c.PVE.Password)
	}
	if c.Git.Token != "git-from-env" {
		t.Errorf("git token not overridden: %q", c.Git.Token)
	}
}

func TestYAMLSerializationRoundTrip(t *testing.T) {
	c := Defaults()
	c.Git.URL = "https://x/y"
	out, err := yaml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back.Git.URL != c.Git.URL || back.Rec.PruneBudget != c.Rec.PruneBudget {
		t.Errorf("round-trip mismatch: %+v vs %+v", back, c)
	}
}
