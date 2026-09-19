// M13.2 CLI-layer tests for the `adopt --adopt-secrets` SOPS merge.
//
// mergeSOPSSSHKeys is the ONE on-disk SOPS writer in proxops: adopt itself
// is PVE-READ-ONLY and never touches the secrets file; the CLI owns the
// SOPS re-encrypt step, gated on (--adopt-secrets AND new keys found).
//
// These tests use the REAL sops + age binaries (same contract as
// internal/secrets's suite): a per-run age identity written under
// t.TempDir() (never the production key), a SOPS-encrypted fixture file,
// and a stub config.Config. They SKIP when the binaries are absent.
//
// Pinned behaviour (task §15):
//   - zero new keys → the merge is a clean no-op; the on-disk SOPS file is
//     byte-identical (plain adopt never writes the SOPS file).
//   - new keys → imported under cloud-init.ssh-keys; pre-existing entries
//     (ssh-keys, passwords) + unrelated blocks survive verbatim; age
//     recipients are preserved; no plaintext reaches disk; no transient
//     sidecar survives.
//   - SOPS merge failure → clear "SOPS merge failed" error; file untouched.
//   - schema.SSHKeyFingerprint: comment-insensitive, stable, 16 lowercase
//     hex (basis of the deterministic adopted-<fp> names).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
	"github.com/GizzmoShifu/proxmox-operator/internal/secrets"
)

const (
	m132OrigKey   = "ssh-ed25519 ORIGINAL9000IG9wZXJhdG9yQHRlc3QgY2x1c3Rlci1tYWlu"
	m132NewKeyA   = "ssh-ed25519 NEWKEYA0000bmV3LWtleS1BIE5ldyBLZXkgQQ=="
	m132NewKeyB   = "ssh-ed25519 NEWKEYB0000bmV3LWtleS1CIE9wcyBFLW1haWw="
	m132OrigPw    = "original-ci-default-password"
)

// requireSopsToolchain skips the test unless sops + age-keygen are present.
func requireSopsToolchain(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"sops", "age-keygen"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not on PATH; SOPS-merge integration skipped", bin)
		}
	}
}

// m132Fixture lays a temp dir with:
//   - a fresh age identity (private key + public key)
//   - a SOPS-encrypted secrets.sops.yaml carrying M9 credential values +
//     M13.2 cloud-init material ("main" key + "default" password)
//   - a config.Config pointing at the cluster "m132-dev"
//   - a baseline copy for the byte-identity assertions
//
// SOPS_AGE_KEY_FILE is set for the whole test so the on-disk file can be
// decrypted by the real sops binary.
type m132Fixture struct {
	root, priv, pub, cipher, baseline string
	cfg                               *config.Config
}

func newM132Fixture(t *testing.T) *m132Fixture {
	t.Helper()
	dir := t.TempDir()
	priv := filepath.Join(dir, "age.key")
	out, err := exec.Command("age-keygen", "-o", priv).CombinedOutput()
	if err != nil {
		t.Fatalf("age-keygen: %v; out=%s", err, out)
	}
	pub := ""
	body, _ := os.ReadFile(priv)
	for _, ln := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(ln, "# public key: ") {
			pub = strings.TrimSpace(strings.TrimPrefix(ln, "# public key: "))
			break
		}
	}
	if !strings.HasPrefix(pub, "age1") {
		t.Fatalf("no public key emitted: %s", body)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", priv)

	cipher := filepath.Join(dir, "secrets.sops.yaml")
	plain := secrets.SecretsFile{
		Values: map[string]string{
			"proxops-user":   "fixture@pam",
			"proxops-token":  "ftok-0000-m132",
			"proxops-gittok": "fghp-0000-m132",
		},
		SSHKeys: map[string]string{"main": m132OrigKey},
		Passwords: map[string]string{
			"default": m132OrigPw,
		},
	}
	encoded := secrets.EncodeSopsYAML(&plain)
	plainFile := cipher + ".plain.yaml"
	if err := os.WriteFile(plainFile, []byte(encoded), 0o600); err != nil {
		t.Fatalf("write plaintext: %v", err)
	}
	out, err = exec.Command("sops", "--encrypt", "--input-type", "yaml",
		"--output-type", "yaml", "--age", pub, plainFile).CombinedOutput()
	if err != nil {
		t.Fatalf("sops --encrypt fixture: %v; out=%s", err, out)
	}
	if err := os.Remove(plainFile); err != nil {
		t.Fatalf("remove transient plaintext: %v", err)
	}
	if err := os.WriteFile(cipher, out, 0o600); err != nil {
		t.Fatalf("write cipher: %v", err)
	}
	baseline, err := os.ReadFile(cipher)
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}

	cfg := &config.Config{PVE: config.PVEConfig{
		Clusters: map[string]config.PVECluster{
			"m132-dev": {
				BaseURL:     "https://m132.invalid:8006",
				Nodes:       []string{"node1"},
				SecretsFile: cipher,
			},
		},
	}}
	return &m132Fixture{
		root: dir, priv: priv, pub: pub,
		cipher: cipher, baseline: string(baseline),
		cfg: cfg,
	}
}

// TestM132_SOPS_plainAdoptNeverWrites pins the §8 boundary: with zero new
// keys the merge is a clean no-op and the SOPS file is byte-identical.
// This is the plain-adopt guarantee (proxops adopt without --adopt-secrets
// ends up here with an empty map and must touch nothing).
func TestM132_SOPS_plainAdoptNeverWrites(t *testing.T) {
	requireSopsToolchain(t)
	f := newM132Fixture(t)
	after, err := os.ReadFile(f.cipher)
	if err != nil {
		t.Fatalf("read before merge: %v", err)
	}
	_ = after
	msg, err := mergeSOPSSSHKeys(f.cfg, "m132-dev", map[string]string{})
	if err != nil {
		t.Fatalf("zero-key merge must be a clean no-op, got: %v (msg=%q)", err, msg)
	}
	if !strings.Contains(msg, "no new ssh-keys to import") {
		t.Errorf("summary = %q, want the no-op marker", msg)
	}
	again, err := os.ReadFile(f.cipher)
	if err != nil {
		t.Fatalf("read after no-op merge: %v", err)
	}
	if string(again) != f.baseline {
		t.Fatalf("SOPS file modified by a zero-key merge (%d -> %d bytes)",
			len(f.baseline), len(again))
	}
}

// TestM132_SOPS_mergeImportsPreservesSurvives pins the full import: new
// keys land under cloud-init.ssh-keys; existing ssh-keys + passwords +
// M9 credential values survive verbatim; recipients are preserved; no
// plaintext reaches disk; no sidecar survives.
func TestM132_SOPS_mergeImportsPreservesSurvives(t *testing.T) {
	requireSopsToolchain(t)
	f := newM132Fixture(t)

	_, err := mergeSOPSSSHKeys(f.cfg, "m132-dev", map[string]string{
		"adopted-a": m132NewKeyA,
		"ops-b":     m132NewKeyB,
	})
	if err != nil {
		t.Fatalf("mergeSOPSSSHKeys: %v", err)
	}

	merged, err := secrets.DecryptFile(f.cipher)
	if err != nil {
		t.Fatalf("DecryptFile(post-merge): %v", err)
	}
	// New keys imported verbatim.
	if merged.SSHKeys["adopted-a"] != m132NewKeyA {
		t.Errorf("adopted-a = %q, want the import payload", merged.SSHKeys["adopted-a"])
	}
	if merged.SSHKeys["ops-b"] != m132NewKeyB {
		t.Errorf("ops-b = %q, want the import payload", merged.SSHKeys["ops-b"])
	}
	// Pre-existing entries survive verbatim.
	if merged.SSHKeys["main"] != m132OrigKey {
		t.Errorf("pre-existing 'main' key changed: %q", merged.SSHKeys["main"])
	}
	if merged.Passwords["default"] != m132OrigPw {
		t.Errorf("pre-existing ci password changed: %q", merged.Passwords["default"])
	}
	if merged.Values["proxops-token"] != "ftok-0000-m132" || merged.Values["proxops-gittok"] != "fghp-0000-m132" {
		t.Errorf("M9 credential values did not survive the re-encrypt: %v", merged.Values)
	}
	if len(merged.SSHKeys) != 3 {
		t.Errorf("merged ssh-keys = %d entries, want 3 (main + adopted-a + ops-b)", len(merged.SSHKeys))
	}
	// Recipients preserved: the merged file still names the original age
	// public key (no operator lockout).
	rcpts, err := secrets.ReadAgeRecipients(f.cipher)
	if err != nil {
		t.Fatalf("ReadAgeRecipients(post-merge): %v", err)
	}
	if len(rcpts) != 1 || rcpts[0] != f.pub {
		t.Errorf("age recipients after merge = %v, want [%s] (original recipient preserved)", rcpts, f.pub)
	}
	// No PII anywhere in the on-disk ciphertext.
	cbytes, _ := os.ReadFile(f.cipher)
	for _, pii := range []string{m132OrigKey, m132NewKeyA, m132NewKeyB, m132OrigPw, "ftok-0000-m132"} {
		if strings.Contains(string(cbytes), pii) {
			t.Errorf("plaintext %q found on disk after merge — the SOPS write is not atomic/encrypted", pii)
		}
	}
	// No transient sidecar survives a successful merge.
	if _, err := os.Stat(f.cipher + ".proxops-tmp"); err == nil {
		t.Errorf("atomic sidecar still on disk after a successful merge")
	}
}

// TestM132_SOPS_mergeDoesNotClobberExistingNames pins that re-importing a
// key ALREADY in the SOPS store under the same name does not double it or
// report it as new.
func TestM132_SOPS_mergeDoesNotClobberExistingNames(t *testing.T) {
	requireSopsToolchain(t)
	f := newM132Fixture(t)

	msg, err := mergeSOPSSSHKeys(f.cfg, "m132-dev", map[string]string{
		"main": m132NewKeyA, // same NAME as the store already carries
	})
	if err != nil {
		t.Fatalf("mergeSOPSSSHKeys: %v", err)
	}
	if strings.Contains(msg, "imported 1") {
		t.Errorf("merge claims a new import for a pre-existing name; msg=%q", msg)
	}
	if !strings.Contains(msg, "no new ssh-keys to import") {
		t.Errorf("expected the no-op marker for an already-present name; msg=%q", msg)
	}
	merged, err := secrets.DecryptFile(f.cipher)
	if err != nil {
		t.Fatalf("DecryptFile: %v", err)
	}
	if merged.SSHKeys["main"] != m132OrigKey {
		t.Errorf("existing 'main' entry clobbered by re-import: %q (want the ORIGINAL value — a name collision must NOT overwrite operator data)",
			merged.SSHKeys["main"])
	}
}

// TestM132_SOPS_failureLeavesFileUntouched pins atomicity: when the SOPS
// document on disk is unreadable/undecryptable, the merge fails closed with
// a clear error class and the file bytes are UNCHANGED.
func TestM132_SOPS_failureLeavesFileUntouched(t *testing.T) {
	requireSopsToolchain(t)
	f := newM132Fixture(t)

	broken := "total not a sops document at all\n"
	if err := os.WriteFile(f.cipher, []byte(broken), 0o600); err != nil {
		t.Fatalf("overwrite cipher with garbage: %v", err)
	}
	msg, err := mergeSOPSSSHKeys(f.cfg, "m132-dev", map[string]string{
		"adopted-z": "ssh-ed25519 Z0000bmV3LWtleS1a",
	})
	if err == nil {
		t.Fatalf("merge against a broken SOPS file returned nil; want a clear failure")
	}
	// The stage marker lands in the RETURNED MESSAGE (the runAdout summary
	// prints it); the error carries the decrypt-class detail.
	if !strings.Contains(msg, "SOPS merge failed") {
		t.Errorf("failure message does not name the SOPS merge stage: %q", msg)
	}
	after, _ := os.ReadFile(f.cipher)
	if string(after) != broken {
		t.Fatalf("failed merge modified the on-disk file:\n got  %q\n want  %q (unchanged)", after, broken)
	}
}

// TestM132_SSHKeyFingerprint is comment-insensitive, length-16 lowercase
// hex, and differs across distinct key bodies. This is what makes
// `adopted-<fingerprint>` names deterministic across runs and across VMs.
func TestM132_SSHKeyFingerprint(t *testing.T) {
	fpA := schema.SSHKeyFingerprint("ssh-ed25519 FPTEST0000ZmstZXktaG9zdA== host-a")
	fpA2 := schema.SSHKeyFingerprint("ssh-ed25519 FPTEST0000ZmstZXktaG9zdA== host-b")
	fpNoComment := schema.SSHKeyFingerprint("ssh-ed25519 FPTEST0000ZmstZXktaG9zdA==")
	fpB := schema.SSHKeyFingerprint("ssh-ed25519 OTHERBODY0000b3RoZXIga2V5 host")
	if fpA == "" || fpA != fpA2 || fpA != fpNoComment {
		t.Errorf("fingerprint unstable under comment drift: %q / %q / %q", fpA, fpA2, fpNoComment)
	}
	if len(fpA) != 16 {
		t.Fatalf("fingerprint length = %d (%q), want 16 lowercase hex", len(fpA), fpA)
	}
	for _, c := range fpA {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("fingerprint %q is not lowercase hex", fpA)
		}
	}
	if fpA == fpB {
		t.Errorf("distinct key bodies produced the same fingerprint %q; collision breaks dedup safety", fpA)
	}
	_ = fmt.Sprintf
}
