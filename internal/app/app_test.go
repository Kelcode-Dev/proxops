// Regression tests for the config -> pveclient auth/URL wiring.
//
// M8 multi-cluster model: credentials live at pve level; every cluster has
// its own base-url + node allowlist. Each cluster gets its own PVE client.
// The single-endpoint PVE routing property is preserved per cluster: the
// cluster's base-url host is used for every request, node names live in the
// request path only.
package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
)

// A valid base URL to satisfy config.Validate().
const testBaseURL = "https://pve-dev-01.example:8006"

// TestPVEParamsFromCopiesAllFields — the direct regression for dropped
// fields. Fills every config field, pins every PVEParams field for a named
// cluster.
func TestPVEParamsFromCopiesAllFields(t *testing.T) {
	c := config.Defaults()
	c.PVE.User = "root@pam"
	c.PVE.Auth = config.AuthToken
	c.PVE.TokenID = "pveconform"
	c.PVE.Token = "uuid-1234"
	c.PVE.CAFile = "/etc/pve/conformance-ca.pem"
	c.PVE.Clusters["conformance-dev"] = config.PVECluster{
		BaseURL: testBaseURL,
		Nodes:   []string{"pve-dev-01", "pve-dev-02"},
	}
	c.Git.URL = "https://git.example/repo"

	got := app.PVEParamsFrom(c, "conformance-dev")
	if got.User != c.PVE.User || got.Auth != string(c.PVE.Auth) ||
		got.TokenID != c.PVE.TokenID || got.Token != c.PVE.Token ||
		got.Password != c.PVE.Password || got.BaseURL != testBaseURL ||
		got.CAFile != c.PVE.CAFile || !nodesEqual(got.Nodes, []string{"pve-dev-01", "pve-dev-02"}) {
		t.Fatalf("PVEParamsFrom = %+v — the config->client wiring dropped a field.", got)
	}
	if got.Credential() != "root@pam!pveconform=uuid-1234" {
		t.Errorf("Credential() = %q, want root@pam!pveconform=uuid-1234", got.Credential())
	}

	// Unknown cluster names fail closed: empty params (no base-url) so no
	// PVE client can be constructed for them, and no action can address PVE.
	unknown := app.PVEParamsFrom(c, "ghost-cluster")
	if unknown.BaseURL != "" {
		t.Errorf("unknown cluster PVE params must be empty, got %+v", unknown)
	}
}

// TestPVEParamsFromCredSharing — credentials are shared across clusters:
// every cluster's derived PVEParams carries the same credential.
func TestPVEParamsFromCredSharing(t *testing.T) {
	c := config.Defaults()
	c.PVE.User = "root@pam"
	c.PVE.TokenID = "pveconform"
	c.PVE.Token = "uuid-1234"
	c.PVE.Clusters["dev"] = config.PVECluster{BaseURL: "https://dev.example:8006"}
	c.PVE.Clusters["prod"] = config.PVECluster{BaseURL: "https://prod.example:8006"}
	c.Git.URL = "https://git.example/repo"

	dev := app.PVEParamsFrom(c, "dev")
	prod := app.PVEParamsFrom(c, "prod")
	if dev.Credential() != prod.Credential() {
		t.Errorf("credentials must be shared: dev=%q prod=%q", dev.Credential(), prod.Credential())
	}
	if dev.BaseURL != "https://dev.example:8006" || prod.BaseURL != "https://prod.example:8006" {
		t.Errorf("per-cluster endpoints not preserved: dev=%q prod=%q", dev.BaseURL, prod.BaseURL)
	}
}

// TestPVEParamsFromYAML — token auth from a YAML config file in the M8
// multi-cluster shape.
func TestPVEParamsFromYAML(t *testing.T) {
	dir := t.TempDir()
	p := writeConfigFile(t, dir, `
log:
  level: info
pve:
  auth: token
  user: root@pve
  token-id: pveconform
  token: 5f4e2c1a-0000-0000-0000-00000000beef
  clusters:
    conformance-dev:
      base-url: https://pve-dev-01.example:8006
      nodes: [pve-dev-01, pve-dev-02]
git:
  url: https://git.example/repo
  branch: main
reconcile:
  poll-interval: 30s
  task-timeout: 10m
  prune-budget: 3
`)
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := app.PVEParamsFrom(c, "conformance-dev")
	wantCred := "root@pve!pveconform=5f4e2c1a-0000-0000-0000-00000000beef"
	if got.Credential() != wantCred {
		t.Fatalf("Credential() = %q, want %q", got.Credential(), wantCred)
	}
	// The single endpoint must be the cluster's base-url, not a node host.
	if got.BaseURL != "https://pve-dev-01.example:8006" {
		t.Errorf("BaseURL = %q, want https://pve-dev-01.example:8006", got.BaseURL)
	}
	if len(got.Nodes) != 2 || got.Nodes[0] != "pve-dev-01" || got.Nodes[1] != "pve-dev-02" {
		t.Errorf("Nodes = %+v, want the two configured node names", got.Nodes)
	}
}

// TestPVEClientRoutesNodeNameThroughSingleHost — the core integration
// property per cluster: a node that is only a PVE node name (NOT a DNS
// host) must still be reachable, because the client fixes the host from the
// cluster base-url and puts the node in the path only.
func TestPVEClientRoutesNodeNameThroughSingleHost(t *testing.T) {
	c := config.Defaults()
	c.PVE.Clusters["conformance-dev"] = config.PVECluster{
		BaseURL: "https://pve-dev-01.example:8006",
		Nodes:   []string{"pve-dev-01", "pve-dev-02"},
	}
	params := app.PVEParamsFrom(c, "conformance-dev")
	client, err := pveclient.New(pveclient.Options{PVE: params}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A per-node path for a second node must STILL hit the base-url host.
	u := client.URL("pve-dev-02", "nodes/pve-dev-02/qemu/100/config")
	want := "https://pve-dev-01.example:8006/api2/json/nodes/pve-dev-02/qemu/100/config"
	if u != want {
		t.Errorf("URL = %q, want %q (node name in path, host from config)", u, want)
	}
	// A cluster-wide call must hit the same host with no node segment.
	cu := client.URL("gateway", "cluster/resources")
	if cu != "https://pve-dev-01.example:8006/api2/json/cluster/resources" {
		t.Errorf("cluster URL = %q", cu)
	}
}

// TestPVEParamsFromEnv — token credential from environment only, shared by
// every cluster.
func TestPVEParamsFromEnv(t *testing.T) {
	t.Setenv("PVECONFORM_PVE_TOKEN", "env-token-abc")
	t.Setenv("PVECONFORM_PVE_USER", "root@pam")
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthToken
	c.PVE.TokenID = "pveconform"
	c.PVE.Clusters["dev"] = config.PVECluster{BaseURL: testBaseURL}

	config.OverlayFromEnv(c)

	if c.PVE.Token != "env-token-abc" {
		t.Errorf("Token = %q, want env-token-abc", c.PVE.Token)
	}
	if c.PVE.User != "root@pam" {
		t.Errorf("User = %q, want root@pam (PVECONFORM_PVE_USER not honored)", c.PVE.User)
	}
	got := app.PVEParamsFrom(c, "dev")
	if got.Credential() != "root@pam!pveconform=env-token-abc" {
		t.Errorf("Credential() = %q, want root@pam!pveconform=env-token-abc", got.Credential())
	}
	if got.BaseURL != testBaseURL {
		t.Errorf("BaseURL = %q, want %q", got.BaseURL, testBaseURL)
	}
}

// TestPVEParamsFromComposedTokenValue — PVECONFORM_PVE_TOKEN_VALUE wins
// over both the YAML pair and any previously set env token.
func TestPVEParamsFromComposedTokenValue(t *testing.T) {
	t.Setenv("PVECONFORM_PVE_TOKEN_VALUE", "composed@pam!ci=v1")
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.User = "yaml@pam"
	c.PVE.TokenID = "yaml-id"
	c.PVE.Token = "yaml-tok"
	c.PVE.Clusters["dev"] = config.PVECluster{BaseURL: testBaseURL}

	config.OverlayFromEnv(c)
	got := app.PVEParamsFrom(c, "dev")
	if got.Credential() != "composed@pam!ci=v1" {
		t.Fatalf("Credential() = %q, want composed@pam!ci=v1 (env should override yaml pair)", got.Credential())
	}
	if got.TokenValue != "composed@pam!ci=v1" {
		t.Errorf("TokenValue = %q, want the composed env value", got.TokenValue)
	}
}

// TestTicketAuthStillWorks — user + password reach pveclient, and
// PVEParams.Credential (token-mode-only) MUST remain empty so no
// PVEAPIToken header is accidentally sent alongside a PVEAuthCookie.
func TestTicketAuthStillWorks(t *testing.T) {
	t.Setenv("PVECONFORM_PVE_PASSWORD", "env-pass")
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthTicket
	c.PVE.User = "root@pam"
	c.PVE.Clusters["dev"] = config.PVECluster{BaseURL: testBaseURL}
	config.OverlayFromEnv(c)
	if c.PVE.Password != "env-pass" {
		t.Fatalf("password not overridden: %q", c.PVE.Password)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := app.PVEParamsFrom(c, "dev")
	if got.Credential() != "" {
		t.Errorf("ticket auth must not yield a PVEAPIToken credential; got %q", got.Credential())
	}
	if got.Password != "env-pass" {
		t.Errorf("Password not carried: %q", got.Password)
	}
}

// TestConfigPrecedence — the precedence chain the CLI documents:
// defaults < YAML < flags < env.
func TestConfigPrecedence(t *testing.T) {
	c := config.Defaults()
	if len(c.PVE.Clusters) != 0 {
		t.Fatalf("default clusters should be empty, got %v", c.PVE.ClusterNames())
	}

	// YAML wins over defaults: a cluster endpoint appears.
	p := writeConfigFile(t, t.TempDir(), "pve:\n  clusters:\n    dev:\n      base-url: https://a:8006\n      nodes: [n1]\ngit:\n  url: https://yaml/repo\n")
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load YAML: %v", err)
	}
	cluster, ok := c.PVE.Cluster("dev")
	if !ok || cluster.BaseURL != "https://a:8006" {
		t.Errorf("YAML cluster not applied: %+v", c.PVE.Clusters)
	}

	// Flags win over YAML (emulated — buildAgent is not exported).
	dev := c.PVE.Clusters["dev"]
	dev.BaseURL = "https://b:8006"
	c.PVE.Clusters["dev"] = dev
	if got, _ := c.PVE.Cluster("dev"); got.BaseURL != "https://b:8006" {
		t.Errorf("flag base-url not applied: %q", got.BaseURL)
	}

	// Env credentials win over flags.
	t.Setenv("PVECONFORM_PVE_USER", "env@pam")
	t.Setenv("PVECONFORM_PVE_TOKEN", "env-token-wins")
	c.PVE.User = "flag@pam"
	c.PVE.TokenID = "flag-id"
	c.PVE.Token = "flag-token"
	config.OverlayFromEnv(c)

	if c.PVE.User != "env@pam" {
		t.Errorf("User = %q, want env@pam", c.PVE.User)
	}
	if c.PVE.Token != "env-token-wins" {
		t.Errorf("Token = %q, want env-token-wins", c.PVE.Token)
	}
	pg := app.PVEParamsFrom(c, "dev")
	if pg.Credential() != "env@pam!flag-id=env-token-wins" {
		t.Errorf("Credential = %q, want env@pam!flag-id=env-token-wins", pg.Credential())
	}
	if pg.TokenValue != "" {
		t.Errorf("TokenValue = %q, want empty (no PVECONFORM_PVE_TOKEN_VALUE was set)", pg.TokenValue)
	}
}

// TestNoValidateErrorLeaksCredential — the error text must not echo the raw
// token.
func TestNoValidateErrorLeaksCredential(t *testing.T) {
	secret := "topsecret-uuid-9c13"
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthToken
	c.PVE.User = "u@pve"
	c.PVE.TokenID = "id"
	c.PVE.Token = secret
	c.PVE.Password = secret // password set under token auth → should error
	c.PVE.Clusters["dev"] = config.PVECluster{BaseURL: testBaseURL}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate should reject password under token auth")
	}
	s := err.Error()
	for i := 0; i+len(secret) <= len(s); i++ {
		if s[i:i+len(secret)] == secret {
			t.Fatalf("Validate() error leaked secret in: %s", s)
		}
	}
}

// TestValidateRejectsNoClusters — M8 multi-cluster model: a zero-cluster
// config is a hard error, as is a malformed cluster name.
func TestValidateRejectsNoClusters(t *testing.T) {
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthToken
	c.PVE.User = "u@pve"
	c.PVE.TokenID = "id"
	c.PVE.Token = "tok"
	if err := c.Validate(); err == nil {
		t.Fatal("Validate should reject a config with zero clusters")
	}

	badName := config.Defaults()
	badName.Git.URL = "https://x/y"
	badName.PVE.Auth = config.AuthToken
	badName.PVE.User = "u@pve"
	badName.PVE.TokenID = "id"
	badName.PVE.Token = "tok"
	badName.PVE.Clusters["-bad name-"] = config.PVECluster{BaseURL: "https://x:8006"}
	if err := badName.Validate(); err == nil {
		t.Fatal("Validate should reject a malformed cluster name")
	}
}

// TestAgentNewWithMultiCluster builds a real Agent off a 2-cluster config
// against a local git tree; it asserts the per-cluster PVE client routing:
// each cluster's client is pinned to its own base-url.
func TestAgentClustersExposure(t *testing.T) {
	c := config.Defaults()
	c.Git.Path = t.TempDir()
	c.PVE.Auth = config.AuthToken
	c.PVE.User = "root@pam"
	c.PVE.TokenID = "pveconform"
	c.PVE.Token = "deadbeef"
	c.PVE.Clusters["conformance-dev"] = config.PVECluster{BaseURL: "https://dev.example:8006", Nodes: []string{"dev-1"}}
	c.PVE.Clusters["prod-a"] = config.PVECluster{BaseURL: "https://prod.example:8006", Nodes: []string{"prod-1"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	names := c.PVE.ClusterNames()
	if len(names) != 2 || names[0] != "conformance-dev" || names[1] != "prod-a" {
		t.Fatalf("ClusterNames = %v (want deterministic sorted order)", names)
	}
	// The params mapping reaches both clusters.
	if got := app.PVEParamsFrom(c, "prod-a"); got.BaseURL != "https://prod.example:8006" {
		t.Errorf("prod-a BaseURL = %q", got.BaseURL)
	}
}

// --- helpers ---

func nodesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
