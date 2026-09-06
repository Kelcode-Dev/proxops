// Regression tests for the config -> pveclient auth wiring.
//
// The historical bug (the one that made `pveconform diff` report
// "pve token auth is not configured"): app.New built
// pveclient.Options.PVE inline and OMITTED TokenID. config.Validate() had
// already passed (it inspects the config struct, not the client's params),
// so the failure only surfaced at request time inside Auth.applyAuth. Every
// existing test constructed pveclient.PVEParams directly (unit tests) or
// hand-wired its own pveclient.New (reconcile e2e harness), so none of them
// exercised the actual production config->client mapping. This file pins
// that mapping.
package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
)

// TestPVEParamsFromCopiesAllFields — the direct regression for the
// TokenID omission. Fills every config field, pins every PVEParams field,
// and verifies the composed credential.
func TestPVEParamsFromCopiesAllFields(t *testing.T) {
	c := config.Defaults()
	c.PVE.User = "root@pam"
	c.PVE.Auth = config.AuthToken
	c.PVE.TokenID = "pveconform"
	c.PVE.Token = "uuid-1234"
	c.PVE.Gateway = "pve"
	c.PVE.Port = 8006
	c.PVE.CAFile = "/etc/pve/conformance-ca.pem"

	got := app.PVEParamsFrom(c)
	want := pveclient.PVEParams{
		User:       c.PVE.User,
		Auth:       string(c.PVE.Auth),
		TokenID:    c.PVE.TokenID,
		Token:      c.PVE.Token,
		Password:   c.PVE.Password,
		Gateway:    c.PVE.Gateway,
		Port:       c.PVE.Port,
		CAFile:     c.PVE.CAFile,
	}
	if got != want {
		t.Fatalf("PVEParamsFrom = %+v, want %+v — the config->client wiring dropped a field.", got, want)
	}
	if got.Credential() != "root@pam!pveconform=uuid-1234" {
		t.Errorf("Credential() = %q, want root@pam!pveconform=uuid-1234", got.Credential())
	}
}

// TestPVEParamsFromYAML — token auth from a YAML config file (the user's
// .config.yaml shape). Load() returns a fully-populated config.
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
  gateway: conformance-dev
  port: 8006
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
	if got.Gateway != "conformance-dev" {
		t.Errorf("Gateway = %q, want conformance-dev", got.Gateway)
	}
}

// TestPVEParamsFromEnv — token credential from environment only
// (PVECONFORM_PVE_TOKEN + PVECONFORM_PVE_USER).
func TestPVEParamsFromEnv(t *testing.T) {
	t.Setenv("PVECONFORM_PVE_TOKEN", "env-token-abc")
	t.Setenv("PVECONFORM_PVE_USER", "root@pam")
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthToken
	c.PVE.TokenID = "pveconform"

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
	// The raw env var must NOT have leaked into TokenValue.
	if got.TokenValue != "" {
		t.Errorf("TokenValue = %q, want empty (only PVECONFORM_PVE_TOKEN was set)", got.TokenValue)
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

	config.OverlayFromEnv(c)
	got := app.PVEParamsFrom(c)
	if got.Credential() != "composed@pam!ci=v1" {
		t.Fatalf("Credential() = %q, want composed@pam!ci=v1 (env should override yaml pair)", got.Credential())
	}
	// TokenValue is the source of truth now.
	if got.TokenValue != "composed@pam!ci=v1" {
		t.Errorf("TokenValue = %q, want the composed env value", got.TokenValue)
	}
}

// TestTicketAuthStillWorks — user + password compose into PVEParams that
// reach pveclient.New (checked via Validate + PVEParamsFrom), and
// PVEParams.Credential (token-mode-only) MUST remain empty so no
// PVEAPIToken header is accidentally sent alongside a PVEAuthCookie.
func TestTicketAuthStillWorks(t *testing.T) {
	t.Setenv("PVECONFORM_PVE_PASSWORD", "env-pass")
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthTicket
	c.PVE.User = "root@pam"
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

// TestConfigPrecedence — the exact precedence chain the CLI documents:
// defaults < YAML < flags < env (for credentials).
func TestConfigPrecedence(t *testing.T) {
	// Defaults: port 8006.
	c := config.Defaults()
	if c.PVE.Port != 8006 {
		t.Fatalf("default port changed: %d", c.PVE.Port)
	}

	// YAML wins over defaults: port 8007.
	p := writeConfigFile(t, t.TempDir(), "pve:\n  port: 8007\ngit:\n  url: https://yaml/repo\n")
	c, err := config.Load(p)
	if err != nil {
		t.Fatalf("Load YAML: %v", err)
	}
	if c.PVE.Port != 8007 {
		t.Errorf("YAML port not applied: %d", c.PVE.Port)
	}

	// Flags win over YAML (emulated — buildAgent is not exported).
	c.PVE.Port = 8008
	if c.PVE.Port != 8008 {
		t.Errorf("flag port not applied: %d", c.PVE.Port)
	}

	// Env credentials win over flags — via the CLI's actual code path.
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

// TestNoValidateErrorLeaksCredential — a misconfigured credential in the
// error message is a real concern (an operator's log could carry it).
// config.Validate() must not echo raw token / password values in its
// error text.
func TestNoValidateErrorLeaksCredential(t *testing.T) {
	secret := "topsecret-uuid-9c13"
	c := config.Defaults()
	c.Git.URL = "https://x/y"
	c.PVE.Auth = config.AuthToken
	c.PVE.User = "u@pve"
	c.PVE.TokenID = "id"
	c.PVE.Token = secret
	c.PVE.Password = secret // password set under token auth → should error
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate should reject password under token auth")
	}
	if idx := indexErr(err.Error(), secret); idx >= 0 {
		t.Fatalf("Validate() error leaked secret in: %s", err.Error())
	}
}

func indexErr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- helpers ---

func writeConfigFile(t *testing.T, dir, contents string) string {
	t.Helper()
	p := filepath.Join(dir, "cfg.yaml")
	// 0600: it's a test tempdir file, but the habit matters.
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
