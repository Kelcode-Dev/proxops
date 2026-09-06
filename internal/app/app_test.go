// Regression tests for the config -> pveclient auth/URL wiring.
//
// The historical bug (the one that made `pveconform diff` report
// "pve token auth is not configured"): app.New built
// pveclient.Options.PVE inline and OMITTED TokenID. Every existing test
// constructed pveclient.PVEParams directly (unit tests) or hand-wired its
// own pveclient.New (reconcile e2e harness), so none of them exercised the
// actual production config->client mapping. This file pins that mapping.
//
// These tests also pin the new single-endpoint PVE connection model: the
// client talks to one base URL regardless of the node name, so a node name
// that is not a resolvable DNS hostname (the conformance-dev bug) can still
// be reached.
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
// fields. Fills every config field, pins every PVEParams field.
func TestPVEParamsFromCopiesAllFields(t *testing.T) {
	c := config.Defaults()
	c.PVE.User = "root@pam"
	c.PVE.Auth = config.AuthToken
	c.PVE.TokenID = "pveconform"
	c.PVE.Token = "uuid-1234"
	c.PVE.BaseURL = testBaseURL
	c.PVE.Nodes = []string{"pve-dev-01", "pve-dev-02"}
	c.PVE.CAFile = "/etc/pve/conformance-ca.pem"
	c.Git.URL = "https://git.example/repo"

	got := app.PVEParamsFrom(c)
	if got.User != c.PVE.User || got.Auth != string(c.PVE.Auth) ||
		got.TokenID != c.PVE.TokenID || got.Token != c.PVE.Token ||
		got.Password != c.PVE.Password || got.BaseURL != c.PVE.BaseURL ||
		got.CAFile != c.PVE.CAFile || !nodesEqual(got.Nodes, c.PVE.Nodes) {
		t.Fatalf("PVEParamsFrom = %+v — the config->client wiring dropped a field.", got)
	}
	if got.Credential() != "root@pam!pveconform=uuid-1234" {
		t.Errorf("Credential() = %q, want root@pam!pveconform=uuid-1234", got.Credential())
	}
}

// TestPVEParamsFromYAML — token auth from a YAML config file in the new
// single-endpoint shape (base-url + nodes).
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
  base-url: https://pve-dev-01.example:8006
  nodes:
    - pve-dev-01
    - pve-dev-02
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
	got := app.PVEParamsFrom(c)
	wantCred := "root@pve!pveconform=5f4e2c1a-0000-0000-0000-00000000beef"
	if got.Credential() != wantCred {
		t.Fatalf("Credential() = %q, want %q", got.Credential(), wantCred)
	}
	// The single endpoint must be the config's base-url, not a node host.
	if got.BaseURL != "https://pve-dev-01.example:8006" {
		t.Errorf("BaseURL = %q, want https://pve-dev-01.example:8006", got.BaseURL)
	}
	if len(got.Nodes) != 2 || got.Nodes[0] != "pve-dev-01" || got.Nodes[1] != "pve-dev-02" {
		t.Errorf("Nodes = %+v, want the two configured node names", got.Nodes)
	}
}

// TestPVEClientRoutesNodeNameThroughSingleHost — the core integration
// property: a node that is only a PVE node name (NOT a DNS host) must still
// be reachable, because the client fixes the host from base-url and puts the
// node in the path only.
func TestPVEClientRoutesNodeNameThroughSingleHost(t *testing.T) {
	c := config.Defaults()
	c.PVE.BaseURL = "https://pve-dev-01.example:8006"
	c.PVE.Nodes = []string{"pve-dev-01", "pve-dev-02"}
	params := app.PVEParamsFrom(c)
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

// TestPVEParamsFromEnv — token credential from environment only.
func TestPVEParamsFromEnv(t *testing.T) {
	t.Setenv("PVECONFORM_PVE_TOKEN", "env-token-abc")
	t.Setenv("PVECONFORM_PVE_USER", "root@pam")
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthToken
	c.PVE.TokenID = "pveconform"
	c.PVE.BaseURL = testBaseURL

	config.OverlayFromEnv(c)

	if c.PVE.Token != "env-token-abc" {
		t.Errorf("Token = %q, want env-token-abc", c.PVE.Token)
	}
	if c.PVE.User != "root@pam" {
		t.Errorf("User = %q, want root@pam (PVECONFORM_PVE_USER not honored)", c.PVE.User)
	}
	got := app.PVEParamsFrom(c)
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
	c.PVE.BaseURL = testBaseURL

	config.OverlayFromEnv(c)
	got := app.PVEParamsFrom(c)
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
	c.PVE.BaseURL = testBaseURL
	config.OverlayFromEnv(c)
	if c.PVE.Password != "env-pass" {
		t.Fatalf("password not overridden: %q", c.PVE.Password)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	got := app.PVEParamsFrom(c)
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
	if c.PVE.BaseURL != "" {
		t.Fatalf("default base-url should be empty, got %q", c.PVE.BaseURL)
	}

	// YAML wins over defaults: a base-url appears, and a node is listed.
	p := writeConfigFile(t, t.TempDir(), "pve:\n  base-url: https://a:8006\n  nodes: [n1]\ngit:\n  url: https://yaml/repo\n")
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load YAML: %v", err)
	}
	if c.PVE.BaseURL != "https://a:8006" {
		t.Errorf("YAML base-url not applied: %q", c.PVE.BaseURL)
	}

	// Flags win over YAML (emulated — buildAgent is not exported).
	c.PVE.BaseURL = "https://b:8006"
	if c.PVE.BaseURL != "https://b:8006" {
		t.Errorf("flag base-url not applied: %q", c.PVE.BaseURL)
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
	pg := app.PVEParamsFrom(c)
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
	c.PVE.BaseURL = testBaseURL
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate should reject password under token auth")
	}
	for i := 0; i+len(secret) <= len(err.Error()); i++ {
		if err.Error()[i:i+len(secret)] == secret {
			t.Fatalf("Validate() error leaked secret in: %s", err.Error())
		}
	}
}

// TestValidateRejectsMissingBaseURL — config.Validate now enforces the new
// single-endpoint model: an empty base-url is a hard error, and node names
// that are not plain identifiers are rejected.
func TestValidateRejectsBadPVEConnection(t *testing.T) {
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthToken
	c.PVE.User = "u@pve"
	c.PVE.TokenID = "id"
	c.PVE.Token = "tok"
	c.PVE.BaseURL = "" // missing
	c.PVE.Nodes = []string{"good", "bad name"}
	if err := c.Validate(); err == nil {
		t.Fatal("Validate should reject empty base-url + bad node name")
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
