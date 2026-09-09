package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// The M8 config model: pve.clusters is a map of named clusters, each with
// its own base-url + node allowlist. Credentials stay at the pve level
// (shared). The old pve.base-url/pve.nodes shape is gone.

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.PVE.Auth != AuthToken {
		t.Errorf("default auth = %q, want %q", c.PVE.Auth, AuthToken)
	}
	// No cluster endpoints are defaulted: they are environment-specific and
	// Validate() rejects a config with zero clusters.
	if len(c.PVE.Clusters) != 0 {
		t.Errorf("default clusters should be empty, got %+v", c.PVE.Clusters)
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

func TestLoadMergesMultiClusterOverDefaults(t *testing.T) {
	y := []byte(`
log:
  level: debug
pve:
  auth: token
  user: pveops@pve
  token-id: ci
  token: tok-abc
  clusters:
    conformance-dev:
      base-url: https://pve-dev-01.example:8006
      nodes: [pve-dev-01, pve-dev-02]
    prod:
      base-url: https://pve-prod-01.example:8006
      nodes: [pve-prod-01]
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
		t.Fatalf("Validate on a fully-populated multi-cluster config: %v", verr)
	}
	if c.Log.Level != "debug" {
		t.Errorf("log.level = %q, want debug", c.Log.Level)
	}
	if c.PVE.User != "pveops@pve" {
		t.Errorf("pve.user = %q", c.PVE.User)
	}
	if len(c.PVE.Clusters) != 2 {
		t.Fatalf("clusters = %+v, want 2 entries", c.PVE.Clusters)
	}
	cdev, ok := c.PVE.Cluster("conformance-dev")
	if !ok {
		t.Fatalf("conformance-dev cluster missing")
	}
	if cdev.BaseURL != "https://pve-dev-01.example:8006" {
		t.Errorf("conformance-dev base-url = %q", cdev.BaseURL)
	}
	if len(cdev.Nodes) != 2 || cdev.Nodes[0] != "pve-dev-01" || cdev.Nodes[1] != "pve-dev-02" {
		t.Errorf("conformance-dev nodes = %+v", cdev.Nodes)
	}
	prod, ok := c.PVE.Cluster("prod")
	if !ok {
		t.Fatalf("prod cluster missing")
	}
	if prod.BaseURL != "https://pve-prod-01.example:8006" {
		t.Errorf("prod base-url = %q", prod.BaseURL)
	}
	// Deterministic cluster ordering.
	if got := c.PVE.ClusterNames(); len(got) != 2 || got[0] != "conformance-dev" || got[1] != "prod" {
		t.Errorf("ClusterNames = %v, want sorted [conformance-dev prod]", got)
	}
	// Credentials stay at the pve level, not per-cluster.
	if c.PVE.TokenID != "ci" || c.PVE.Token != "tok-abc" {
		t.Errorf("shared credentials not preserved: %+v", c.PVE)
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
}

func TestLoadExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	y := []byte("data-dir: ~/" + "share/pveconform\npve:\n  ca-file: ~/pve/ca.pem\n")
	f := filepath.Join(t.TempDir(), "tilde.yaml")
	if err := os.WriteFile(f, y, 0o644); err != nil {
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
		t.Errorf("PVE.CAFile = %q, want %q", c.PVE.CAFile, c.PVE.CAFile)
	}

	// Single-quoted bare "~" is a 1-char string, not a YAML null.
	y2 := []byte("data-dir: '~'\n")
	f2 := filepath.Join(t.TempDir(), "tilde2.yaml")
	if err := os.WriteFile(f2, y2, 0o600); err != nil {
		t.Fatal(err)
	}
	c2, err := Load(f2)
	if err != nil {
		t.Fatal(err)
	}
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
		t.Errorf("DataDir = %q", c3.DataDir)
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
	// No git source + no clusters.
	c := Defaults()
	if verr := c.Validate(); verr == nil {
		t.Error("expected error: no git source + no clusters")
	} else {
		if !strings.Contains(verr.Error(), "git source") {
			t.Errorf("error should mention the git source: %v", verr)
		}
		if !strings.Contains(verr.Error(), "pve.clusters") {
			t.Errorf("error should mention missing clusters: %v", verr)
		}
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
	setOneCluster(c)
	if err := c.Validate(); err == nil {
		t.Error("expected error: missing pve.token")
	}

	// Token auth with token value but missing user.
	c = Defaults()
	c.Git.Path = "/tmp/x"
	c.PVE.TokenValue = "u@pve!x=y"
	setOneCluster(c)
	if err := c.Validate(); err == nil {
		t.Error("expected error: missing pve.user for token value")
	}

	// Ticket auth without a password.
	c = Defaults()
	c.Git.Path = "/tmp/x"
	c.PVE.Auth = AuthTicket
	c.PVE.User = "u@pve"
	setOneCluster(c)
	if err := c.Validate(); err == nil {
		t.Error("expected error: ticket auth requires password")
	}

	// Token auth with a stray password set.
	c = Defaults()
	c.Git.Path = "/tmp/x"
	c.PVE.User = "u@pve"
	c.PVE.Token = "tok"
	c.PVE.Password = "pw"
	setOneCluster(c)
	if err := c.Validate(); err == nil {
		t.Error("expected error: password must be empty for token auth")
	}
}

// setOneCluster gives a config a single valid cluster so that the PVE
// connection checks don't drown the credential-shape checks under test.
func setOneCluster(c *Config) {
	c.PVE.Clusters["dev"] = PVECluster{
		BaseURL: "https://pve-dev-01.example:8006",
		Nodes:   []string{"pve-dev-01"},
	}
}

func TestValidatePassesForValidConfigs(t *testing.T) {
	c := Defaults()
	c.Git.URL = "https://git.example.com/repo"
	c.PVE.User = "root@pam"
	c.PVE.TokenID = "pveconform"
	c.PVE.Token = "deadbeef"
	c.PVE.Clusters["dev"] = PVECluster{BaseURL: "https://pve.example:8006"}
	if err := c.Validate(); err != nil {
		t.Fatalf("token mode + one cluster should validate: %v", err)
	}

	// A composed token value also validates on its own without token-id.
	c3 := Defaults()
	c3.Git.URL = "https://git.example.com/repo"
	c3.PVE.User = "root@pam"
	c3.PVE.TokenValue = "root@pam!pveconform=deadbeef"
	c3.PVE.Clusters["dev"] = PVECluster{BaseURL: "https://pve.example:8006"}
	if err := c3.Validate(); err != nil {
		t.Fatalf("token-value mode should validate: %v", err)
	}

	// Ticket auth + multi-cluster.
	c2 := Defaults()
	c2.Git.Path = "/opt/git"
	c2.PVE.Auth = AuthTicket
	c2.PVE.User = "root@pam"
	c2.PVE.Password = "s3cret"
	c2.PVE.Clusters["dev"] = PVECluster{BaseURL: "https://pve-a.example:8006"}
	c2.PVE.Clusters["prod"] = PVECluster{BaseURL: "https://pve-b.example:8006"}
	if err := c2.Validate(); err != nil {
		t.Fatalf("ticket mode + two clusters should validate: %v", err)
	}
}

func TestValidateRejectsBadPVEClusters(t *testing.T) {
	// Cluster with an unparseable base-url.
	c := Defaults()
	setCluster(c, "dev", PVECluster{BaseURL: "not-a-url"})
	c.PVE.User = "u@pve"
	c.PVE.Token = "tok"
	c.PVE.TokenID = "id"
	c.Git.URL = "https://x/y"
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "base-url must be a parseable") {
		t.Errorf("want parseable-URL error, got %v", err)
	}

	// Cluster with an empty base-url.
	c = Defaults()
	setCluster(c, "dev", PVECluster{BaseURL: ""})
	c.PVE.User = "u@pve"
	c.PVE.Token = "tok"
	c.PVE.TokenID = "id"
	c.Git.URL = "https://x/y"
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "base-url is required") {
		t.Errorf("want required-base-url error, got %v", err)
	}

	// Bad node name shape + duplicate node entries.
	c = Defaults()
	setCluster(c, "dev", PVECluster{BaseURL: "https://x:8006", Nodes: []string{"bad name", "n1", "n1"}})
	c.PVE.User = "u@pve"
	c.PVE.Token = "tok"
	c.PVE.TokenID = "id"
	c.Git.URL = "https://x/y"
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "not a valid PVE node name") {
		t.Errorf("want node-name error, got %v", err)
	}
	if !strings.Contains(err.Error(), "duplicate entry") {
		t.Errorf("want duplicate-node error, got %v", err)
	}

	// Two clusters sharing one endpoint must fail closed (prune scoping is
	// endpoint-based).
	c = Defaults()
	setCluster(c, "dev", PVECluster{BaseURL: "https://x:8006"})
	setCluster(c, "prod", PVECluster{BaseURL: "https://x:8006"})
	c.PVE.User = "u@pve"
	c.PVE.Token = "tok"
	c.PVE.TokenID = "id"
	c.Git.URL = "https://x/y"
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "share base-url") {
		t.Errorf("want shared-endpoint error, got %v", err)
	}
}

func setCluster(c *Config, name string, cl PVECluster) {
	if c.PVE.Clusters == nil {
		c.PVE.Clusters = map[string]PVECluster{}
	}
	c.PVE.Clusters[name] = cl
}

func TestValidClusterName(t *testing.T) {
	cases := map[string]bool{
		"dev":            true,
		"conformance-dev": true,
		"prod-a":    true,
		"a1":             true,
		"":               false,
		"-leading":       false,
		"trailing-":      false,
		"UPPER":          false,
		"with space":     false,
		"sl/ash":         false,
		"dot.name":       false,
	}
	for in, want := range cases {
		if got := ValidClusterName(in); got != want {
			t.Errorf("ValidClusterName(%q) = %v, want %v", in, got, want)
		}
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
		t.Errorf("password not overridden by env: %q", c.PVE.Password)
	}
	if c.Git.Token != "git-from-env" {
		t.Errorf("git token not overridden by env: %q", c.Git.Token)
	}
}

func TestYAMLSerializationRoundTrip(t *testing.T) {
	c := Defaults()
	c.Git.URL = "https://x/y"
	setCluster(c, "dev", PVECluster{BaseURL: "https://pve-dev-01.example:8006", Nodes: []string{"pve-dev-01"}})
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
	if got := back.PVE.Clusters["dev"]; got.BaseURL != "https://pve-dev-01.example:8006" || len(got.Nodes) != 1 {
		t.Errorf("clusters not round-tripped: %+v", back.PVE.Clusters)
	}
}
