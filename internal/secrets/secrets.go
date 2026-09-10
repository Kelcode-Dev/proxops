// Package secrets decrypts SOPS/age-backed cluster credential material for
// pveconform. It is the ONLY place that touches encrypted secret bytes. The
// plaintext it returns is held in memory only: pveconform never writes a
// decrypted secrets file to disk, and nothing in this package or in the
// config / app / CLI layers logs, diffs, statuses, or exports the values
// (task §2: no plaintext secret in any output channel).
//
// Decryption drives the external `sops` binary (Mozilla SOPS, age backend)
// rather than linking github.com/getsops/sops/v3. Rationale (task §16):
//
//   - the sops Go module transitively pulls ~160 extra dependencies (every
//     cloud KMS backend, gRPC, Azure/GCP/Alibaba/Huawei SDKs) into what is a
//     standalone static-binary tool; shelling out adds ZERO Go dependencies;
//   - operators already have sops + age installed (they generate and encrypt
//     the secrets with them), so the toolchain assumption is documented, not
//     hidden;
//   - the standard SOPS age identity mechanism (SOPS_AGE_KEY_FILE /
//     SOPS_AGE_KEY / AGE_KEY_FILE) is honoured by inheriting the process env —
//     pveconform itself never sets or reads key material (task §7, §8);
//   - "decrypt into memory only" is structurally true: the sops child process
//     prints the plaintext; pveconform reads it once into a map. If a
//     secrets-file is NOT configured, pveconform never invokes sops at all.
//
// Bootstrap / chicken-and-egg (task §8, documented explicitly): pveconform
// obtains the age IDENTITY (private key) ONLY from outside the encrypted
// repository — the operator's process environment (SOPS_AGE_KEY_FILE or
// SOPS_AGE_KEY). The GitOps repository holds the encrypted file (whose
// `sops:` metadata names the PUBLIC age recipient) and all non-secret config;
// the private key lives on the operator's workstation or a key service, never
// in git. pveconform adds no alternative (no self-hosted vault, no in-repo
// key, no key-file flag that would accept a repo path).
package secrets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Sentinel errors. Callers use errors.Is to distinguish failure classes
// without parsing text. None of these — nor any error this package returns —
// ever carries a decrypted secret value.
var (
	// ErrSOPSBinaryMissing: a secrets-file is configured but the sops
	// executable is not on PATH.
	ErrSOPSBinaryMissing = errors.New("sops binary not found on PATH; install Mozilla SOPS + age, or run pveconform without a configured secrets-file")

	// ErrUnencryptedSecrets: the configured file parses but carries no sops:
	// metadata — i.e. somebody committed a PLAINTEXT credential file.
	// pveconform refuses to use it (task §13: do not silently accept an
	// unencrypted secrets file as valid configuration).
	ErrUnencryptedSecrets = errors.New("configured secrets file is not SOPS-encrypted (no sops metadata); a cluster secrets file must be encrypted with sops — pveconform refuses plaintext credential files")

	// ErrNoIdentity: sops reported that no usable age identity is present in
	// the environment (SOPS_AGE_KEY_FILE / SOPS_AGE_KEY unset or empty).
	ErrNoIdentity = errors.New("SOPS decryption failed: no usable age identity in the environment; set SOPS_AGE_KEY_FILE to a private age key that can decrypt this cluster's secrets file")

	// ErrIdentityMismatch: an age identity is present but cannot decrypt the
	// file (wrong key / key not a named recipient in the sops metadata).
	ErrIdentityMismatch = errors.New("SOPS decryption failed: the provided age identity cannot decrypt this file; verify SOPS_AGE_KEY_FILE belongs to a key named in the file's sops age recipients")

	// ErrMalformedDocument: decryption succeeded but the plaintext is not the
	// documented top-level `secrets:` mapping of scalar values.
	ErrMalformedDocument = errors.New("decrypted secrets document is malformed: expected a top-level `secrets:` mapping of `name: value` scalar entries")
)

// sopsBinaryName is the executable the production path invokes.
const sopsBinaryName = "sops"

var (
	decryptMu sync.RWMutex
	// testDecrypter, when non-nil, replaces the real sops-binary call.
	// It is intended for pure unit tests that must not shell out. The
	// hook is installed via SetTestDecrypter and must be reset at the
	// end of each test (e.g. t.Cleanup) to avoid leaking across tests.
	testDecrypter func(path string) (map[string]string, error)
)

// sopsTimeout bounds one decryption invocation. A well-provisioned sops age
// decrypt is milliseconds; the generous bound catches a wedged key agent
// without making pveconform hang indefinitely.
const sopsTimeout = 30 * time.Second

// SecretsFile is the decrypted result of one SOPS file: the flat top-level
// `secrets:` mapping, name → value.
type SecretsFile struct {
	Values map[string]string
	// SourcePath is the on-disk path that was decrypted (for error text).
	// It is not secret material.
	SourcePath string
}

// Get returns the value for a SOPS top-level name and whether it is present
// AND non-empty after trimming. A SOPS reference that resolves to an empty
// value must fail exactly like an absent one: fail closed, no silent
// substitution (task §6).
func (s SecretsFile) Get(name string) (string, bool) {
	v, ok := s.Values[name]
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}

// Len reports how many secret names were decrypted.
func (s SecretsFile) Len() int { return len(s.Values) }

// DecryptFile decrypts the SOPS-encrypted file at path and returns its
// top-level `secrets:` mapping, in memory only, via the external sops binary.
//
// Security properties:
//   - Plaintext is never written to disk by pveconform.
//   - Key material (the private age key) enters only through the inherited
//     process environment (SOPS_AGE_KEY_FILE / SOPS_AGE_KEY / AGE_KEY_FILE);
//     pveconform never sets, reads, or copies it.
//   - Errors are classified into the sentinel set above plus, for unknown
//     failures, a short redacted hint; a decrypted value is never embedded.
func DecryptFile(path string) (SecretsFile, error) {
	if err := validatePath(path); err != nil {
		return SecretsFile{}, err
	}
	m, err := decryptInner(path)
	if err != nil {
		return SecretsFile{}, err
	}
	return SecretsFile{Values: m, SourcePath: path}, nil
}

// decryptInner is the internal decryption entry point that honors the
// test hook. Production callers use DecryptFile; tests that install a
// SetTestDecrypter callback see the callback invoked here.
func decryptInner(path string) (map[string]string, error) {
	decryptMu.RLock()
	fn := testDecrypter
	decryptMu.RUnlock()
	if fn != nil {
		return fn(path)
	}
	return decryptWithSops(path)
}

// validatePath checks the path is absolute or safely non-empty.
func validatePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("secrets: empty file path")
	}
	return nil
}

// SetTestDecrypter replaces the sops-binary decryption with the given
// callback. It is a test-only hook; production callers never invoke it.
// Tests MUST pair SetTestDecrypter with t.Cleanup to restore the
// production binary path.
func SetTestDecrypter(fn func(path string) (map[string]string, error)) (restore func()) {
	decryptMu.Lock()
	prev := testDecrypter
	testDecrypter = fn
	decryptMu.Unlock()
	return func() {
		decryptMu.Lock()
		testDecrypter = prev
		decryptMu.Unlock()
	}
}

// decryptWithSops runs the sops binary and parses the JSON result.
func decryptWithSops(path string) (map[string]string, error) {
	if _, err := exec.LookPath(sopsBinaryName); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w", ErrSOPSBinaryMissing)
		}
		return nil, fmt.Errorf("locate %s: %v", sopsBinaryName, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), sopsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sopsBinaryName,
		"--decrypt",
		"--input-type", "yaml",
		"--output-type", "json",
		path)
	// Inherit the operator's process environment: this is the standard
	// SOPS age identity mechanism. Nothing sensitive set by pveconform (no
	// PVE tokens, no git tokens) is passed to sops, because the env is the
	// operator's own — but we deliberately do not ADD anything.
	cmd.Env = os.Environ()

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		return nil, classifySOPSError(ctx, runErr, errBuf.String())
	}
	var doc struct {
		Secrets map[string]json.RawMessage `json:"secrets"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedDocument, err)
	}
	if doc.Secrets == nil {
		return nil, fmt.Errorf("%w: %s did not contain a top-level `secrets:` mapping (point secrets-file at the SOPS file, not the pveconform config)",
			ErrMalformedDocument, shorten(path))
	}
	vals := make(map[string]string, len(doc.Secrets))
	for name, raw := range doc.Secrets {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			// Non-string leaf: accept numbers (coerce); reject maps/lists.
			var f float64
			if ferr := json.Unmarshal(raw, &f); ferr != nil {
				return nil, fmt.Errorf("%w: secret %q is not a scalar string or number", ErrMalformedDocument, name)
			}
			v = formatFloat(f)
		}
		vals[name] = v
	}
	return vals, nil
}

// classifySOPSError maps a failed sops invocation to a stable, secret-free
// sentinel. sops stderr is used ONLY to select the sentinel; it is never
// included verbatim (defense: sops version changes or a hostile file could
// put anything in stderr). Empirically-observed sops stderr markers:
//
//   - unencrypted plain YAML   → "sops metadata not found"
//   - no age identity present  → "failed to load age identities"
//   - wrong/mismatched identity → "identity did not match any of the recipients"
//   - general decrypt failure  → "failed to get the data key"
func classifySOPSError(ctx context.Context, err error, stderr string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w: decryption exceeded the %s budget", ErrNoIdentity, sopsTimeout)
	}
	s := strings.ToLower(stderr)
	// NOTE: the order of these cases is deliberate. A WRONG age identity and
	// a MISSING age identity both produce "did not find keys in locations"
	// in sops stderr (sops lists every location it searched). The
	// distinguishing markers are:
	//   - "failed to load age identities"  → NO identity was loaded at all.
	//   - "identity did not match" / "failed to create reader" → an identity
	//     WAS loaded but cannot decrypt (wrong key).
	// The "failed to load age identities" case must be checked first.
	switch {
	case strings.Contains(s, "sops metadata not found"),
		strings.Contains(s, "no sops metadata"),
		strings.Contains(s, "not encrypted"):
		return fmt.Errorf("%w", ErrUnencryptedSecrets)
	case strings.Contains(s, "failed to load age identities"),
		strings.Contains(s, "no age identity"):
		return fmt.Errorf("%w", ErrNoIdentity)
	case strings.Contains(s, "identity did not match"),
		strings.Contains(s, "incorrect identity"),
		strings.Contains(s, "failed to create reader"),
		strings.Contains(s, "did not find keys in locations"),
		strings.Contains(s, "failed to get the data key"),
		strings.Contains(s, "decryption failed"),
		strings.Contains(s, "unable to decrypt"):
		return fmt.Errorf("%w", ErrIdentityMismatch)
	default:
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		// Unknown sops failure: exit code + a short redacted hint. The
		// operator can reproduce with `sops --decrypt <file>` by hand.
		return fmt.Errorf("sops decrypt failed (exit %d): %s", code, redactLine(stderr))
	}
}

// redactLine produces a <= 160-char, single-line, token-redacted hint from a
// sops stderr body. Any run of 24+ alnum chars (the shape of a UUID, base64
// blob, or token) is replaced before it reaches error text.
func redactLine(stderr string) string {
	s := singleLine(stderr)
	s = longTokenRe.ReplaceAllString(s, "[redacted]")
	if len(s) > 160 {
		s = s[:159] + "…"
	}
	if s == "" {
		return "run `sops --decrypt <file>` manually for details"
	}
	return s
}

// longTokenRe matches runs of 24+ token-like chars — the shape of a UUID,
// base64 blob, or API token.
var longTokenRe = regexp.MustCompile(`[A-Za-z0-9+/=_-]{24,}`)

func singleLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}

// shorten bounds s to 80 chars. Used only for paths and short fragments,
// never for sops stderr.
func shorten(s string) string {
	if len(s) > 80 {
		return s[:79] + "…"
	}
	return s
}

// formatFloat renders a JSON number without a trailing ".0" for whole values.
func formatFloat(f float64) string {
	if f == float64(int64(f)) {
		return fmt.Sprintf("%d", int64(f))
	}
	return fmt.Sprintf("%v", f)
}

// SOPSAvailable reports whether the sops binary can be found on PATH.
// Used by config/CLI to give an actionable "sops not installed" error
// instead of a raw LookPath failure. PATH check only; it does not validate
// the binary's version.
func SOPSAvailable() bool {
	_, err := exec.LookPath(sopsBinaryName)
	return err == nil
}
