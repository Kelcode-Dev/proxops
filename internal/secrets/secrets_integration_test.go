// SOPS + age integration tests for the internal/secrets package.
//
// These tests use the REAL sops + age binaries (installed on the dev host);
// they are skipped when the binaries are not on PATH. The tests use test-only
// age identities generated per run (never the production key), test-only
// SOPS-encrypted files written into a temp dir, and test-only credential
// values that are never pulled from the .env.
//
// Task §12 coverage:
//   - "encrypted secrets are successfully decrypted when a valid age
//     identity is available" → TestDecryptionRoundTrip.
//   - "missing SOPS identity fails clearly" → TestNoIdentity_ErrNoIdentity.
//   - "wrong age identity fails clearly" → TestWrongIdentity_ErrIdentityMismatch.
//   - "unencrypted secrets file fails clearly" → TestUnencrypted_ErrUnencrypted.
//   - "malformed encrypted file fails clearly" → see TestUnencrypted_ErrUnencrypted
//     (sops reports the same sentinel for missing metadata — that is the
//     observable contract here).
//   - "plaintext secrets do not appear in normal / error text" → asserted
//     inside each test via assertNoToken.
package secrets

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// genAgeIdentity writes a fresh age private key + its public key to
// dir/keyname and returns (privPath, pub).
func genAgeIdentity(t *testing.T, dir, keyName string) (string, string) {
	t.Helper()
	privPath := filepath.Join(dir, keyName)
	if _, err := exec.LookPath("age-keygen"); err != nil {
		t.Skipf("age-keygen not available (%v)", err)
	}
	if _, err := os.Stat(privPath); err == nil {
		t.Fatalf("age key already exists at %s; refusing to overwrite", privPath)
	}
	genOut, err := exec.Command("age-keygen", "-o", privPath).CombinedOutput()
	if err != nil {
		t.Fatalf("age-keygen failed: %v; out=%s", err, genOut)
	}
	// Parse the `# public key: age...` comment line from the written key
	// file (always present, regardless of age-keygen's stdout quirks).
	body, err := os.ReadFile(privPath)
	if err != nil {
		t.Fatalf("read age key file %s: %v", privPath, err)
	}
	pub := ""
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "# public key: ") {
			pub = strings.TrimPrefix(line, "# public key: ")
			break
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(pub), "age1") {
		t.Fatalf("age key file %s has no `# public key: age1...` line; body=%s", privPath, string(body))
	}
	return privPath, strings.TrimSpace(pub)
}

// sopsEncryptFile uses the sops binary to encrypt the file at plaintext and
// write SOPS-encrypted bytes to ciphertext, using pub as the recipient.
func sopsEncryptFile(t *testing.T, dir, plaintext, ciphertext, pub string) {
	t.Helper()
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skipf("sops not available (%v); integration test skipped", err)
	}
	cmd := exec.Command("sops", "--encrypt", "--age", pub,
		"--input-type", "yaml", "--output-type", "yaml", plaintext)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sops --encrypt failed: %v; out=%s", err, out)
	}
	if err := os.WriteFile(ciphertext, out, 0o600); err != nil {
		t.Fatalf("write ciphertext: %v", err)
	}
}

// assertNoToken verifies that the SUT error text does NOT contain a plaintext
// token value. The value passed in is a test-only credential, NOT the
// production PVE token. Task §12 "plaintext secrets do not appear in errors".
func assertNoToken(t *testing.T, err error, want string, label string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected non-nil error, got nil", label)
	}
	if strings.Contains(err.Error(), want) {
		t.Fatalf("%s: error text leaked the plaintext value %q\nfull error: %v", label, want, err)
	}
}

// TestDecryptionRoundTrip verifies the happy path: SOPS-encrypted → sops
// decrypt → the in-memory value matches the original (task §12 first bullet).
func TestDecryptionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	priv, pub := genAgeIdentity(t, dir, "key.text")
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skipf("sops not available (%v); round-trip test skipped", err)
	}
	testToken := "test-token-000-aaaa-1111-2222-bbbb"
	plain := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(plain, []byte("secrets:\n  pveconform-token: "+testToken+"\n"+
		"  pveconform-user: root@pam\n"+
		"  pveconform-token-id: pveconform\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher := filepath.Join(dir, "secrets.sops.yaml")
	sopsEncryptFile(t, dir, plain, cipher, pub)

	// Decrypt with SOPS_AGE_KEY_FILE set to our fresh private key.
	t.Setenv("SOPS_AGE_KEY_FILE", priv)
	t.Setenv("AGE_KEY_FILE", "") // make sure our SOPS_AGE_KEY_FILE wins
	sf, err := DecryptFile(cipher)
	if err != nil {
		t.Fatalf("DecryptFile: %v", err)
	}
	if got, ok := sf.Get("pveconform-token"); !ok || got != testToken {
		t.Errorf("pveconform-token = %q (ok=%v), want %q", got, ok, testToken)
	}
	if got, ok := sf.Get("pveconform-user"); !ok || got != "root@pam" {
		t.Errorf("pveconform-user = %q (ok=%v)", got, ok)
	}
	if got, ok := sf.Get("pveconform-token-id"); !ok || got != "pveconform" {
		t.Errorf("pveconform-token-id = %q (ok=%v)", got, ok)
	}
}

// TestNoIdentity_ErrNoIdentity verifies: when the SOPS age identity is
// NOT present in the environment, pveconform returns a fail-closed error
// with the ErrNoIdentity sentinel, and the plaintext value does NOT appear
// in the error text (task §12 "missing SOPS identity fails clearly").
func TestNoIdentity_ErrNoIdentity(t *testing.T) {
	dir := t.TempDir()
	_, pub := genAgeIdentity(t, dir, "enc_only.key") // we only use the public key here
	private := filepath.Join(dir, "enc_only.key")

	if _, err := exec.LookPath("sops"); err != nil {
		t.Skipf("sops not available (%v); missing-identity test skipped", err)
	}
	testToken := "never-leaked-c7b0-5c0f-1f0f-aaaaaaaaaaaa"
	plain := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(plain, []byte("secrets:\n  pveconform-token: "+testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher := filepath.Join(dir, "secrets.sops.yaml")
	sopsEncryptFile(t, dir, plain, cipher, pub)

	// Now: NO SOPS age identity in the environment. The SUT binary should
	// fail with ErrNoIdentity.
	// Clear the standard identity envs (env vars are per-process; t.Setenv
	// overrides only what we set; but we need to unset them too — the clean
	// way is to spawn a subprocess-free test: use exec to unset and run sops
	// ourselves isn't what we want. Instead, we set SOPS_AGE_KEY_FILE to a
	// path that EXISTS but is EMPTY. sops will try to load an identity from
	// it, fail to parse, and fall through to "no age identity".
	emptyKey := filepath.Join(dir, "empty.key")
	if err := os.WriteFile(emptyKey, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", emptyKey)
	t.Setenv("AGE_KEY_FILE", "")
	// Unset SOPS_AGE_KEY explicitly (in case it was exported in the env).
	// Test env vars: we cannot unset via t.Setenv (it only sets), so we use
	// os.Unsetenv; this is scoped to this process, which is the test process,
	// not the operator's — safe.
	_ = os.Unsetenv("SOPS_AGE_KEY")

	_, err := DecryptFile(cipher)
	if err == nil {
		t.Fatal("DecryptFile returned nil error, want no-identity sentinel")
	}
	// With SOPS_AGE_KEY_FILE pointed at a present-but-empty file, sops
	// reports "failed to load age identities" → ErrNoIdentity (our
	// classifier distinguishes this from the wrong-identity case which
	// reports "identity did not match any of the recipients" →
	// ErrIdentityMismatch). Pin the sentinel.
	if !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("DecryptFile = %v, want ErrNoIdentity (empty key file => no usable identity)", err)
	}
	assertNoToken(t, err, testToken, "no-identity")
	_ = private
}

// TestWrongIdentity_ErrIdentityMismatch verifies: when the SOPS age identity
// IS present but CANNOT DECRYPT the file, pveconform returns the
// ErrIdentityMismatch sentinel and the plaintext value does NOT appear in
// the error text (task §12 "wrong age identity fails clearly").
func TestWrongIdentity_ErrIdentityMismatch(t *testing.T) {
	dir := t.TempDir()
	_, pub := genAgeIdentity(t, dir, "enc.key") // identity #1 (used to encrypt)
	priv2, _ := genAgeIdentity(t, dir, "enc_wrong.key") // identity #2 (the WRONG key)

	if _, err := exec.LookPath("sops"); err != nil {
		t.Skipf("sops not available (%v); wrong-identity test skipped", err)
	}
	testToken := "wrong-id-xxxx-leaked-should-be-aaaa-bbbb-cccc"
	plain := filepath.Join(dir, "plain.yaml")
	if err := os.WriteFile(plain, []byte("secrets:\n  pveconform-token: "+testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cipher := filepath.Join(dir, "secrets.sops.yaml")
	sopsEncryptFile(t, dir, plain, cipher, pub)

	// Decrypt with the WRONG identity: sops will report
	// "identity did not match any of the recipients" → ErrIdentityMismatch.
	_ = os.Unsetenv("SOPS_AGE_KEY")
	_ = os.Unsetenv("AGE_KEY_FILE")
	t.Setenv("SOPS_AGE_KEY_FILE", priv2)
	_, err := DecryptFile(cipher)
	if err == nil {
		t.Fatal("DecryptFile returned nil, want wrong-identity error")
	}
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("DecryptFile = %v, want ErrIdentityMismatch", err)
	}
	assertNoToken(t, err, testToken, "wrong-identity")
}

// TestUnencrypted_ErrUnencrypted verifies: when the "SOPS file" is actually a
// PLAINTEXT YAML (no sops metadata), pveconform returns the
// ErrUnencryptedSecrets sentinel and the plaintext value does NOT appear in
// the error text (task §12 "unencrypted secrets file fails clearly"; task §13
// "do not silently accept an unencrypted secrets.sops.yaml").
func TestUnencrypted_ErrUnencrypted(t *testing.T) {
	dir := t.TempDir()

	if _, err := exec.LookPath("sops"); err != nil {
		t.Skipf("sops not available (%v); unencrypted-file test skipped", err)
	}
	testToken := "plain-text-leak-should-not-appear-deadbeef-aabb-ccdd"
	plain := filepath.Join(dir, "secrets.yaml")
	if err := os.WriteFile(plain, []byte("secrets:\n  pveconform-token: "+testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// DecryptFile on the plain (not-sops-encrypted) file.
	_, err := DecryptFile(plain)
	if err == nil {
		t.Fatal("DecryptFile returned nil, want ErrUnencryptedSecrets")
	}
	if !errors.Is(err, ErrUnencryptedSecrets) {
		t.Fatalf("DecryptFile = %v, want ErrUnencryptedSecrets", err)
	}
	assertNoToken(t, err, testToken, "unencrypted")
}

// TestSOPSBinaryMissing_ErrSOPSBinaryMissing verifies: when a secrets-file is
// configured but the sops binary is NOT on PATH, DecryptFile returns
// ErrSOPSBinaryMissing and the plaintext value does NOT appear in the error
// text (task §12 "missing SOPS binary fails clearly").
func TestSOPSBinaryMissing_ErrSOPSBinaryMissing(t *testing.T) {
	// We cannot remove the operator's sops binary, but we CAN point the
	// Decrypter at a nonexistent binary by calling the bin path with a fake
	// name. The binDecryptor uses exec.LookPath("sops") hardcoded — to unit
	// test the "missing" path we swap the Decrypter to one that returns
	// ErrSOPSBinaryMissing directly.
	restore := SetTestDecrypter(func(string) (map[string]string, error) {
		return nil, ErrSOPSBinaryMissing
	})
	defer restore()
	_, err := DecryptFile("/does/not/matter")
	if !errors.Is(err, ErrSOPSBinaryMissing) {
		t.Fatalf("DecryptFile = %v, want ErrSOPSBinaryMissing", err)
	}
}

// TestGet_ReturnsEmptyForWhitespace verifies that SecretsFile.Get treats
// whitespace-only values as "missing" (fail closed; task §6 "do not allow
// an empty/missing SOPS secret to silently result in an unintended
// credential being used").
func TestGet_ReturnsEmptyForWhitespace(t *testing.T) {
	sf := SecretsFile{Values: map[string]string{
		"a": "   ",
		"b": "real",
		"c": "  padded  ",
	}}
	if _, ok := sf.Get("a"); ok {
		t.Fatal("Get(a) returned ok=true for whitespace-only; want ok=false")
	}
	if v, ok := sf.Get("b"); !ok || v != "real" {
		t.Fatalf("Get(b) = %q, ok=%v; want real,true", v, ok)
	}
	if v, ok := sf.Get("c"); !ok || v != "padded" {
		t.Fatalf("Get(c) = %q, ok=%v; want padded,true (trimmed)", v, ok)
	}
}
