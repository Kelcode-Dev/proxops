// M9 SOPS security tests (task §12, §6, §13, §14).
//
// These run WITHOUT the sops binary: a stub decrypter (via
// secrets.SetTestDecrypter) stands in for the real `sops --decrypt`, so the
// tests exercise the CONFIG layer (Load path resolution, ResolveSOPS, the
// SOPS-aware Validate) + the APP layer (PVEParamsFrom precedence +
// EffectiveGitToken) deterministically. The real sops+age binary is covered
// by internal/secrets integration tests + the live conformance-dev run.
//
// All credential values here are TEST-ONLY fixtures. No production or .env
// credential appears in this file.
package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/secrets"
)

// test-only SOPS fixture values (never real credentials).
const (
	sopsUser     = "sopsuser@pam"
	sopsTokenID  = "sopsid"
	sopsToken    = "sops-token-value-aaaaaaaa-0000-0000000000"
	sopsPassword = "sops-password-value-9999"
	sopsGitToken = "ghp_sops_gittoken_value_aaaaaaaa0000"
)

// stubSOPS installs a decrypter returning m for any SOPS file.
func stubSOPS(t *testing.T, m map[string]string) {
	t.Helper()
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		return m, nil
	})
	t.Cleanup(restore)
}

// gitRepoPath returns a temp dir containing a minimal .git marker so that
// Load()'s git.path "." walk-up finds a root.
func gitRepoPath(t *testing.T) string {
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

// writePVEconformConfig lays out a single-cluster pveconform config + a fake
// SOPS-encrypted file next to it, under a git worktree marker. The
// `secretsKeysYAML` is the `secrets:` block inside the cluster.
// extraPVEYAML is optional extra top-level pve.* body lines (for bootstrap
// credential testing).
func writePVEconformConfig(t *testing.T, cluster, secretsFile, secretsKeysYAML, extraPVEYAML string) (root, cfgPath string) {
	t.Helper()
	root = gitRepoPath(t)
	clDir := filepath.Join(root, "clusters", cluster)
	if err := os.MkdirAll(clDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The SOPS file bytes don't matter for the stub, but a real file must
	// exist for Load's path resolution.
	sopsPath := filepath.Join(clDir, "secrets.sops.yaml")
	if err := os.WriteFile(sopsPath, []byte("ENC[stub-decryption-target]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("# pveconform test config (M9 SOPS)\n")
	b.WriteString("log:\n  level: info\n")
	b.WriteString("pve:\n  auth: token\n  clusters:\n")
	b.WriteString("    " + cluster + ":\n")
	b.WriteString("      base-url: https://pve-test.example:8006\n")
	b.WriteString("      nodes:\n        - pve-test-01\n")
	if secretsFile != "" {
		b.WriteString("      secrets-file: " + secretsFile + "\n")
	}
	if secretsKeysYAML != "" {
		b.WriteString("      secrets:\n" + secretsKeysYAML)
	}
	if extraPVEYAML != "" {
		b.WriteString(extraPVEYAML)
	}
	b.WriteString("git:\n  branch: main\n  path: .\n")
	b.WriteString("reconcile:\n  poll-interval: 30s\n  task-timeout: 30m\n  prune-budget: 3\n")
	b.WriteString("listen: 127.0.0.1:0\n")
	b.WriteString("data-dir: " + filepath.Join(root, "data") + "\n")
	cfgPath = filepath.Join(clDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, cfgPath
}

// fullSopsRefs returns the YAML `secrets:` block referencing every SOPS key
// the M9 fixture document carries.
func fullSopsKeys() string {
	return "        pve:\n" +
		"          user: pveconform-user\n" +
		"          token-id: pveconform-token-id\n" +
		"          token: pveconform-token\n" +
		"        git:\n" +
		"          token: pve-git-token\n"
}

// TestResolveSOPSSuccess: encrypted secrets are successfully decrypted when
// a valid age identity (stub) is available (task §12 first bullet).
func TestResolveSOPSSuccess(t *testing.T) {
	const cluster = "alpha"
	stubSOPS(t, map[string]string{
		"pveconform-user":     sopsUser,
		"pveconform-token-id": sopsTokenID,
		"pveconform-token":    sopsToken,
		"pve-git-token":       sopsGitToken,
	})
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	rs, ok := c.SopsResolved[cluster]
	if !ok {
		t.Fatalf("SopsResolved missing %s (got %v)", cluster, c.SopsResolved)
	}
	if rs.User != sopsUser || rs.TokenID != sopsTokenID || rs.Token != sopsToken {
		t.Fatalf("resolved PVE = %+v, want stubs", rs)
	}
	if rs.GitToken != sopsGitToken {
		t.Fatalf("resolved.GitToken = %q, want %q", rs.GitToken, sopsGitToken)
	}
	// SOPS file path must be resolved to an absolute path under the
	// cluster's config dir (relative → absolute determinism, task §5):
	// `secrets-file: secrets.sops.yaml` in the config next to
	// clusters/<name>/config.yaml → <configdir>/secrets.sops.yaml.
	absSops := filepath.Join(filepath.Dir(cfgPath), "secrets.sops.yaml")
	if got := c.PVE.Clusters[cluster].SecretsFile; got != absSops {
		t.Fatalf("secret file = %q, want %q (relative secrets-file must resolve against the config dir)", got, absSops)
	}
	// git.path "." must be resolved to the git root (where the .git marker
	// is) — this is the worktree root, not the cluster dir.
	wantGitRoot := filepath.Dir(filepath.Dir(filepath.Dir(cfgPath)))
	if c.Git.Path != wantGitRoot {
		t.Fatalf("git.path = %q, want repo root %q", c.Git.Path, wantGitRoot)
	}
}

// TestPVEParamsFromDecryptedCredential: PVEParamsFrom merges the SOPS-
// decrypted credentials into the effective pveclient params (task §12
// "PVE client receives the decrypted credential correctly").
func TestPVEParamsFromDecryptedCredential(t *testing.T) {
	const cluster = "alpha"
	stubSOPS(t, map[string]string{
		"pveconform-user":     sopsUser,
		"pveconform-token-id": sopsTokenID,
		"pveconform-token":    sopsToken,
		"pve-git-token":       sopsGitToken,
	})
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	p := app.PVEParamsFrom(c, cluster)
	if p.User != sopsUser || p.TokenID != sopsTokenID || p.Token != sopsToken {
		t.Fatalf("PVEParams = user=%q id=%q tok=%q; want SOPS creds", p.User, p.TokenID, p.Token)
	}
	if want := sopsUser + "!" + sopsTokenID + "=" + sopsToken; p.Credential() != want {
		t.Fatalf("Credential() = %q, want composed %q", p.Credential(), want)
	}
}

// TestResolveSOPSNoSecretsFile: a config that declares no secrets-file
// resolves nothing; the stub decrypter must NOT be invoked.
func TestResolveSOPSNoSecretsFile(t *testing.T) {
	const cluster = "alpha"
	invoked := 0
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		invoked++
		return map[string]string{"pveconform-token": sopsToken}, nil
	})
	t.Cleanup(restore)
	_, cfgPath := writePVEconformConfig(t, cluster, "", "", "  user: bootstrap@pve\n  token-id: bootstrap-id\n  token: bootstrap-tok\n")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	if invoked != 0 {
		t.Fatalf("stub decrypter invoked %d times; no secrets-file → zero sops calls (task §16)", invoked)
	}
	if len(c.SopsResolved) > 0 {
		t.Fatalf("SopsResolved = %v, want empty", c.SopsResolved)
	}
}

// TestResolveSOPSMissingKeyFails: a secrets block referencing a key not
// present in the SOPS file fails CLOSED. No silent fallback to a bootstrap
// credential (task §6 "fail closed where credentials are required", task
// §12 "missing secret reference fails clearly").
func TestResolveSOPSMissingKeyFails(t *testing.T) {
	const cluster = "alpha"
	// Stub omits `pveconform-token` but the config references it.
	stubSOPS(t, map[string]string{
		"pveconform-user":     sopsUser,
		"pveconform-token-id": sopsTokenID,
	})
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(),
		// Deliberate bootstrap creds that MUST NOT be silently used.
		"  user: bootstrap@pve\n  token-id: bootstrap-id\n  token: bootstrap-tok\n")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = c.ResolveSOPS()
	if err == nil {
		t.Fatal("ResolveSOPS: want error for missing pveconform-token key, got nil (silent fallback would violate §6)")
	}
	if !strings.Contains(err.Error(), "pveconform-token") {
		t.Fatalf("error must name the failing SOPS key: %v", err)
	}
	// The SOPS reference that DID resolve must NOT be populated: a failed
	// resolution must not leave a partial credential set behind.
	if _, ok := c.SopsResolved[cluster]; ok {
		t.Fatalf("SopsResolved has a partial entry for %s after a failed resolve; want no entry", cluster)
	}
}

// TestResolveSOPSEmptyValueFails: a secrets block referencing a key that
// is EMPTY in the SOPS file also fails closed (task §6).
func TestResolveSOPSEmptyValueFails(t *testing.T) {
	const cluster = "alpha"
	stubSOPS(t, map[string]string{
		"pveconform-user":     sopsUser,
		"pveconform-token-id": sopsTokenID,
		"pveconform-token":    "   ", // whitespace-only = empty
	})
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.ResolveSOPS(); err == nil {
		t.Fatal("ResolveSOPS: want fail-closed error for an empty SOPS token value")
	} else if !strings.Contains(err.Error(), "pveconform-token") {
		t.Fatalf("error must name the empty SOPS key: %v", err)
	}
}

// TestSOPSUnencryptedFileFails: the sops layer reports an unencrypted file
// and ResolveSOPS propagates the error (task §13: never silently accept an
// unencrypted secrets file).
func TestSOPSUnencryptedFileFails(t *testing.T) {
	const cluster = "alpha"
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		return nil, secrets.ErrUnencryptedSecrets
	})
	t.Cleanup(restore)
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = c.ResolveSOPS()
	if err == nil {
		t.Fatal("ResolveSOPS: want error for unencrypted SOPS file, got nil")
	}
	if !strings.Contains(err.Error(), "not SOPS-encrypted") {
		t.Fatalf("error must identify the unencrypted-file problem: %v", err)
	}
}

// TestSOPSWrongIdentityFails: a wrong/absent age identity fails with a clear
// error; the config remains constructible but the PVE client must not be
// built with the missing credential (task §12 "missing SOPS identity fails
// clearly", "wrong age identity fails clearly").
func TestSOPSWrongIdentityFails(t *testing.T) {
	const cluster = "alpha"
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		return nil, secrets.ErrIdentityMismatch
	})
	t.Cleanup(restore)
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = c.ResolveSOPS()
	if err == nil {
		t.Fatal("ResolveSOPS: want error for wrong age identity, got nil")
	}
	if !strings.Contains(err.Error(), "age identity") {
		t.Fatalf("error must identify the identity problem: %v", err)
	}
}

// TestSOPSNoIdentityFails: a missing age identity (SOPS_AGE_KEY_FILE unset
// / empty) fails with the no-identity error.
func TestSOPSNoIdentityFails(t *testing.T) {
	const cluster = "alpha"
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		return nil, secrets.ErrNoIdentity
	})
	t.Cleanup(restore)
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = c.ResolveSOPS()
	if err == nil {
		t.Fatal("ResolveSOPS: want error for missing age identity, got nil")
	}
	if !strings.Contains(err.Error(), "no usable age identity") {
		t.Fatalf("error must identify the missing-identity problem: %v", err)
	}
}

// TestSOPSNoPlaintextInErrorText: error text from SOPS resolution must NOT
// contain the decrypted secret values (task §12 "plaintext secrets do not
// appear in errors").
func TestSOPSNoPlaintextInErrorText(t *testing.T) {
	const cluster = "alpha"
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		// The stub's failure does NOT echo the secret; but it CAN, in a
		// buggy implementation. The test asserts the propagated error text
		// does not contain the test-only secret values.
		return nil, secrets.ErrMalformedDocument
	})
	t.Cleanup(restore)
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = c.ResolveSOPS()
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{sopsUser, sopsTokenID, sopsToken, sopsGitToken, sopsPassword} {
		if strings.Contains(err.Error(), want) {
			t.Fatalf("error text leaked SOPS value %q: %v", want, err)
		}
	}
}

// TestSOPSNoPlaintextInConfigJSON: the SOPS-resolved values must NOT appear
// when the Config value is serialised to JSON (task §12 "plaintext secrets
// do not appear in status / metrics / normal CLI output"). SopsResolved
// carries a json:"-" tag for exactly this reason: a future /config-dump or
// error-shape serialisation of the Config object must not leak the
// decrypted credentials. (The pveclient.PVEParams struct IS the intended
// carrier of the credential into the client — it is not a status/metrics
// surface, so it is out of scope for this check.)
func TestSOPSNoPlaintextInConfigJSON(t *testing.T) {
	const cluster = "alpha"
	stubSOPS(t, map[string]string{
		"pveconform-user":     sopsUser,
		"pveconform-token-id": sopsTokenID,
		"pveconform-token":    sopsToken,
		"pve-git-token":       sopsGitToken,
	})
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	var cj []byte
	if cj, err = json.Marshal(c); err != nil {
		t.Fatalf("json.Marshal(Config): %v", err)
	}
	for _, want := range []string{sopsUser, sopsTokenID, sopsToken, sopsGitToken, sopsPassword} {
		if strings.Contains(string(cj), want) {
			t.Fatalf("Config JSON serialisation leaked the SOPS value %q — secret is bleeding into a status/metrics/CLI channel", want)
		}
	}
}

// TestSOPSPrecedenceSopsBeatsEnvBeatsYAML: the effective credential chain
// for a SOPS-using cluster is SOPS > env > global YAML (task §6). The
// env vars here are test-only values, distinct from the YAML and SOPS
// values so any mis-ordering is visible.
func TestSOPSPrecedenceSopsBeatsEnvBeatsYAML(t *testing.T) {
	const cluster = "alpha"
	stubSOPS(t, map[string]string{
		"pveconform-user":     sopsUser,
		"pveconform-token-id": sopsTokenID,
		"pveconform-token":    sopsToken,
		"pve-git-token":       sopsGitToken,
	})
	// Env-var values distinct from the YAML values.
	t.Setenv("PVECONFORM_PVE_USER", "envuser@pam")
	t.Setenv("PVECONFORM_PVE_TOKEN", "envtok-0000")
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(),
		"  user: yamluser@pam\n  token-id: yamlid\n  token: yamltok-1111\n")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	config.OverlayFromEnv(c)
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	p := app.PVEParamsFrom(c, cluster)
	if p.User != sopsUser {
		t.Fatalf("User = %q, want SOPS value %q (SOPS > env > YAML)", p.User, sopsUser)
	}
	if p.Token != sopsToken {
		t.Fatalf("Token = %q, want SOPS value %q", p.Token, sopsToken)
	}
	if p.TokenID != sopsTokenID {
		t.Fatalf("TokenID = %q, want SOPS value %q (SOPS beats env + YAML)", p.TokenID, sopsTokenID)
	}
}

// TestSOPSPrecedenceEnvBeatsYAML_Bootstrap: a bootstrap (no SOPS) cluster
// keeps the M8 env-over-YAML behaviour (task §6 "existing environment
// credential compatibility works").
func TestSOPSPrecedenceEnvBeatsYAML_Bootstrap(t *testing.T) {
	const cluster = "alpha"
	// No stub: the config has no secrets-file, so the decrypter is never
	// called. We still install one to prove it stays unused.
	invoked := 0
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		invoked++
		return map[string]string{"pveconform-token": sopsToken}, nil
	})
	t.Cleanup(restore)
	t.Setenv("PVECONFORM_PVE_USER", "envuser@pam")
	t.Setenv("PVECONFORM_PVE_TOKEN", "envtok-0000")
	_, cfgPath := writePVEconformConfig(t, cluster, "", "",
		"  user: yamluser@pam\n  token-id: yamlid\n  token: yamltok-1111\n")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	config.OverlayFromEnv(c)
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	if invoked != 0 {
		t.Fatalf("bootstrap cluster invoked the SOPS decrypter %d times; want 0 (task §16)", invoked)
	}
	p := app.PVEParamsFrom(c, cluster)
	if p.User != "envuser@pam" || p.Token != "envtok-0000" {
		t.Fatalf("bootstrap PVEParams = user=%q tok=%q, want env values (env > YAML)", p.User, p.Token)
	}
}

// TestSOPSNoCrossClusterLeak: two clusters in one config, each resolved from
// a DIFFERENT SOPS file. Cluster A's decrypted credentials must NOT end up
// on B's PVEParams and vice-versa (task §12 "cluster A cannot accidentally
// consume cluster B's secret configuration").
//
// The no-leak property sits in PVEParamsFrom: it reads SopsResolved[cluster]
// — only that cluster's entry — over the shared pve-level bootstrap values.
// This test populates both entries via ResolveSOPS (each cluster points at
// its own file via an absolute path so the per-config-dir relative
// resolution cannot merge them) and asserts per-cluster separation.
func TestSOPSNoCrossClusterLeak(t *testing.T) {
	root := gitRepoPath(t)
	aSops := filepath.Join(root, "alpha.secrets.sops.yaml")
	bSops := filepath.Join(root, "beta.secrets.sops.yaml")
	_ = os.WriteFile(aSops, []byte("ENC[a]\n"), 0o600)
	_ = os.WriteFile(bSops, []byte("ENC[b]\n"), 0o600)
	restore := secrets.SetTestDecrypter(func(path string) (map[string]string, error) {
		switch path {
		case aSops:
			return map[string]string{
				"alpha-user": "alpha-user@pam", "alpha-id": "alpha-id", "alpha-tok": "alpha-secret-tok-0000",
			}, nil
		case bSops:
			return map[string]string{
				"beta-user": "beta-user@pam", "beta-id": "beta-id", "beta-tok": "beta-secret-tok-1111",
			}, nil
		default:
			return nil, secrets.ErrMalformedDocument
		}
	})
	t.Cleanup(restore)
	// The config lives at the worktree root; both clusters' SOPS files are
	// referenced by ABSOLUTE path, so per-config-dir relative resolution
	// does not matter here and each cluster points at its own file.
	cfg := "log:\n  level: info\n" +
		"pve:\n  auth: token\n  clusters:\n" +
		"    alpha:\n      base-url: https://a.example:8006\n      nodes: [a1]\n      secrets-file: " + aSops + "\n      secrets:\n" +
		"        pve:\n          user: alpha-user\n          token-id: alpha-id\n          token: alpha-tok\n" +
		"    beta:\n      base-url: https://b.example:8006\n      nodes: [b1]\n      secrets-file: " + bSops + "\n      secrets:\n" +
		"        pve:\n          user: beta-user\n          token-id: beta-id\n          token: beta-tok\n" +
		"git:\n  branch: main\n  path: .\nlisten: 127.0.0.1:0\n"
	rootCfg := filepath.Join(root, "pveconform.yaml")
	_ = os.WriteFile(rootCfg, []byte(cfg), 0o600)
	c, err := config.Load(rootCfg)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	pa := app.PVEParamsFrom(c, "alpha")
	pb := app.PVEParamsFrom(c, "beta")
	if pa.User != "alpha-user@pam" || pa.TokenID != "alpha-id" || pa.Token != "alpha-secret-tok-0000" {
		t.Fatalf("alpha PVEParams = user=%q id=%q tok=%q; want alpha's SOPS values", pa.User, pa.TokenID, pa.Token)
	}
	if pb.User != "beta-user@pam" || pb.TokenID != "beta-id" || pb.Token != "beta-secret-tok-1111" {
		t.Fatalf("beta PVEParams = user=%q id=%q tok=%q; want beta's SOPS values", pb.User, pb.TokenID, pb.Token)
	}
	// And the composed credentials each use ONLY their own cluster's values:
	if pa.Credential() != "alpha-user@pam!alpha-id=alpha-secret-tok-0000" {
		t.Fatalf("alpha Credential() = %q", pa.Credential())
	}
	if pb.Credential() != "beta-user@pam!beta-id=beta-secret-tok-1111" {
		t.Fatalf("beta Credential() = %q", pb.Credential())
	}
}

// TestValidate_SOPSOnlyConfigFailsWithoutResolve: with SopsResolved empty
// (a SOPS-declaring config that has NOT had ResolveSOPS run yet) and no
// bootstrap credentials, Validate MUST report the missing effective
// credential. This pins the fail-loud contract: an operator who points
// --config at the cluster-local SOPS config but forgets the age identity
// gets a clear "no effective PVE token" error, not a silent empty
// Authorization header later.
func TestValidate_SOPSOnlyConfigFailsWithoutResolve(t *testing.T) {
	const cluster = "alpha"
	// Stub so that ResolveSOPS, if called, would succeed — but we do NOT
	// call it here.
	stubSOPS(t, map[string]string{
		"pveconform-user":     sopsUser,
		"pveconform-token-id": sopsTokenID,
		"pveconform-token":    sopsToken,
		"pve-git-token":       sopsGitToken,
	})
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// No ResolveSOPS yet: SopsResolved empty + no bootstrap pve creds.
	if err := c.Validate(); err == nil {
		t.Fatal("Validate: want fail-closed (SOPS configured but not resolved, and no bootstrap credentials), got nil")
	}
	// Now resolve and validate again: must pass.
	if err := c.ResolveSOPS(); err != nil {
		t.Fatalf("ResolveSOPS: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate after ResolveSOPS: %v (SOPS-supplied credentials should satisfy the pve check)", err)
	}
}

// TestValidate_SOPSResolveFails_StaysFailing: if SOPS resolution fails, a
// subsequent Validate must still report a missing effective credential.
func TestValidate_SOPSResolveFails_StaysFailing(t *testing.T) {
	const cluster = "alpha"
	restore := secrets.SetTestDecrypter(func(string) (map[string]string, error) {
		return nil, secrets.ErrNoIdentity
	})
	t.Cleanup(restore)
	_, cfgPath := writePVEconformConfig(t, cluster, "secrets.sops.yaml", fullSopsKeys(), "")
	c, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	_ = c.ResolveSOPS() // fails
	if err := c.Validate(); err == nil {
		t.Fatal("Validate: want fail-closed after SOPS resolution failed (no credentials available)")
	}
}

// TestGitPathDotNoGitRoot: a config with git.path "." where NO .git
// ancestor exists fails Load with a clear error (task §5 "no implicit
// magic"; task §18 "verify the expected fresh-checkout workflow").
func TestGitPathDotNoGitRoot(t *testing.T) {
	const cluster = "alpha"
	// A plain temp dir with NO .git marker anywhere in its ancestors up to
	// / — t.TempDir() returns /tmp/... and /tmp has no .git. To be safe,
	// build under a fresh subdir that is not inside any repo.
	root := t.TempDir()
	clDir := filepath.Join(root, "clusters", cluster)
	_ = os.MkdirAll(clDir, 0o755)
	_ = os.WriteFile(filepath.Join(clDir, "secrets.sops.yaml"), []byte("ENC\n"), 0o600)
	cfg := "log:\n  level: info\npve:\n  auth: token\n  clusters:\n    " + cluster + ":\n      base-url: https://a.example:8006\n      secrets-file: secrets.sops.yaml\n      secrets:\n" + fullSopsKeys() +
		"git:\n  branch: main\n  path: .\nlisten: 127.0.0.1:0\n"
	cfgPath := filepath.Join(clDir, "config.yaml")
	_ = os.WriteFile(cfgPath, []byte(cfg), 0o600)
	_, err := config.Load(cfgPath)
	if err == nil {
		t.Fatal("Load: want error when git.path '.' has no .git ancestor")
	}
	if !strings.Contains(err.Error(), "no .git worktree") {
		t.Fatalf("error must explain the missing git root: %v", err)
	}
}

// TestGitPathAbsolutePassesThrough: an absolute git.path is resolved as-is
// (no walk-up), preserving the M8 path.
func TestGitPathAbsolutePassesThrough(t *testing.T) {
	const cluster = "alpha"
	root := gitRepoPath(t)
	// An explicit worktree path different from root.
	otherTree := t.TempDir()
	clDir := filepath.Join(root, "clusters", cluster)
	_ = os.MkdirAll(clDir, 0o755)
	_ = os.WriteFile(filepath.Join(clDir, "secrets.sops.yaml"), []byte("ENC\n"), 0o600)
	cfg := "log:\n  level: info\npve:\n  auth: token\n  user: bootstrap@pam\n  token-id: bootstrap-id\n  token: bootstrap-tok\n  clusters:\n    " + cluster + ":\n      base-url: https://a.example:8006\n      secrets-file: secrets.sops.yaml\n      secrets:\n" + fullSopsKeys() +
		"git:\n  branch: main\n  path: " + otherTree + "\nlisten: 127.0.0.1:0\n"
	_ = os.WriteFile(filepath.Join(clDir, "config.yaml"), []byte(cfg), 0o600)
	c, err := config.Load(filepath.Join(clDir, "config.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Git.Path != otherTree {
		t.Fatalf("git.path = %q, want absolute passthrough %q", c.Git.Path, otherTree)
	}
}
