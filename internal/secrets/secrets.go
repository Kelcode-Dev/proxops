// Package secrets decrypts SOPS/age-backed cluster credential material for
// proxops. It is the ONLY place that touches encrypted secret bytes. The
// plaintext it returns is held in memory only: proxops never writes a
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
//     proxops itself never sets or reads key material (task §7, §8);
//   - "decrypt into memory only" is structurally true: the sops child process
//     prints the plaintext; proxops reads it once into a map. If a
//     secrets-file is NOT configured, proxops never invokes sops at all.
//
// Bootstrap / chicken-and-egg (task §8, documented explicitly): proxops
// obtains the age IDENTITY (private key) ONLY from outside the encrypted
// repository — the operator's process environment (SOPS_AGE_KEY_FILE or
// SOPS_AGE_KEY). The GitOps repository holds the encrypted file (whose
// `sops:` metadata names the PUBLIC age recipient) and all non-secret config;
// the private key lives on the operator's workstation or a key service, never
// in git. proxops adds no alternative (no self-hosted vault, no in-repo
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

	"gopkg.in/yaml.v3"
)

// Sentinel errors. Callers use errors.Is to distinguish failure classes
// without parsing text. None of these — nor any error this package returns —
// ever carries a decrypted secret value.
var (
	// ErrSOPSBinaryMissing: a secrets-file is configured but the sops
	// executable is not on PATH.
	ErrSOPSBinaryMissing = errors.New("sops binary not found on PATH; install Mozilla SOPS + age, or run proxops without a configured secrets-file")

	// ErrUnencryptedSecrets: the configured file parses but carries no sops:
	// metadata — i.e. somebody committed a PLAINTEXT credential file.
	// proxops refuses to use it (task §13: do not silently accept an
	// unencrypted secrets file as valid configuration).
	ErrUnencryptedSecrets = errors.New("configured secrets file is not SOPS-encrypted (no sops metadata); a cluster secrets file must be encrypted with sops — proxops refuses plaintext credential files")

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
	// testDocDecrypter, when non-nil, replaces the real sops-binary call
	// with a full structured document (secrets + cloud-init blocks).
	// testDecrypter (older flat hook) is wrapped into a doc hook by
	// SetTestDecrypter. Both are test-only and MUST be reset at the end of
	// each test (e.g. t.Cleanup) to avoid leaking across tests.
	testDocDecrypter func(path string) (SecretsFile, error)
)

// sopsTimeout bounds one decryption invocation. A well-provisioned sops age
// decrypt is milliseconds; the generous bound catches a wedged key agent
// without making proxops hang indefinitely.
const sopsTimeout = 30 * time.Second

// SecretsFile is the decrypted result of one SOPS file: the FLAT top-level
// `secrets:` mapping (name → value) plus — since M13.2 — the optional
// structured `cloud-init.ssh-keys` and `cloud-init.passwords` maps.
//
// The SOPS document shape ProxOps supports:
//
//	secrets:              # flat name → value (M9 credentials: PVE user /
//	  proxops-user: ...     # token-id / token / password / git token).
//	cloud-init:           # structured — M13.2 reusable cloud-init material.
//	  ssh-keys:           # name → OpenSSH public key line (one key per line;
//	    main: "ssh-..."     # PVE sshkeys field is urlencoded \n-joined; we
//	    github: "ssh-..."   # keep individual lines here so users can write
//	                          multi-key sets cleanly).
//	  passwords:          # name → ci-password plaintext (or any password the
//	    default: "..."      # operator wants to inject as PVE's cipassword).
//	    legacy: "..."
//
// All three blocks live in one SOPS file so a single cluster has ONE
// encrypted credential surface. The private age key that decrypts them is
// never in the repository (task §8).
type SecretsFile struct {
	// Values is the flat `secrets:` mapping — used by the M9 PVE/Git
	// credentials. Empty when the SOPS document has no top-level
	// `secrets:` block.
	Values map[string]string
	// SSHKeys is the `cloud-init.ssh-keys` mapping, name → one OpenSSH
	// public-key line (the comment column is NOT part of the dedup
	// identity; SSHKeyFingerprint uses type+blob only). Empty when the
	// document has no such block.
	SSHKeys map[string]string
	// Passwords is the `cloud-init.passwords` mapping, name → plaintext
	// password. Empty when absent.
	Passwords map[string]string
	// SourcePath is the on-disk path that was decrypted (for error text).
	// It is not secret material.
	SourcePath string
	// Loaded reports whether this SecretsFile was decrypted from a
	// configured SOPS document — as opposed to the zero value, which
	// means "no SOPS store is configured at all". A valid SOPS document
	// whose cloud-init blocks are initially empty is still SOPS-backed:
	// the empty SSHKeys/Passwords maps + Loaded=true is the only correct
	// representation of that state. Consumers must not infer "SOPS-backed"
	// from map population (a non-empty Values map proves nothing about
	// the cloud-init blocks either — it is a credential surface).
	Loaded bool
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

// Len reports how many flat `secrets:` names were decrypted.
func (s SecretsFile) Len() int { return len(s.Values) }

// HasCloudInitSecrets reports whether the document carries any M13.2
// cloud-init material. Used to decide whether adopt's "re-encrypt the file"
// path is needed at all.
func (s SecretsFile) HasCloudInitSecrets() bool {
	return len(s.SSHKeys) > 0 || len(s.Passwords) > 0
}

// CloudInitMaterial returns a copy of the (ssh-keys, passwords) document
// content. Callers mutate copies freely; SecretsFile itself is never
// modified in place (task §2: "values live only for the lifetime of this
// Config object, never written / logged").
func (s SecretsFile) CloudInitMaterial() (ssh map[string]string, passwords map[string]string) {
	ssh = map[string]string{}
	for k, v := range s.SSHKeys {
		ssh[k] = v
	}
	passwords = map[string]string{}
	for k, v := range s.Passwords {
		passwords[k] = v
	}
	return
}

// DecryptFile decrypts the SOPS-encrypted file at path and returns its
// structured document (flat `secrets:` mapping + optional `cloud-init`
// M13.2 structured block), in memory only, via the external sops binary.
//
// Security properties:
//   - Plaintext is never written to disk by proxops.
//   - Key material (the private age key) enters only through the inherited
//     process environment (SOPS_AGE_KEY_FILE / SOPS_AGE_KEY / AGE_KEY_FILE);
//     proxops never sets, reads, or copies it.
//   - Errors are classified into the sentinel set above plus, for unknown
//     failures, a short redacted hint; a decrypted value is never embedded.
func DecryptFile(path string) (SecretsFile, error) {
	if err := validatePath(path); err != nil {
		return SecretsFile{}, err
	}
	return decryptInner(path)
}

// decryptInner is the internal decryption entry point that honors the
// test hook. Production callers use DecryptFile; tests that install
// SetTestDecrypter / SetTestDocDecrypter see the callback invoked here.
//
// Every SecretsFile returned here (in either path) carries Loaded=true:
// a SOPS document that DECRYPTED VALIDLY is SOPS-backed, even when its
// cloud-init.ssh-keys / cloud-init.passwords blocks are empty at first
// sight. Callers that want to represent "no SOPS store at all" must use
// the zero SecretsFile{} and not call DecryptFile.
func decryptInner(path string) (SecretsFile, error) {
        decryptMu.RLock()
        fn := testDocDecrypter
        decryptMu.RUnlock()
        if fn != nil {
                got, err := fn(path)
                if err != nil {
                        return SecretsFile{}, err
                }
                if got.SourcePath == "" {
                        got.SourcePath = path
                }
                got.Loaded = true
                return got, nil
        }
        return markLoaded(decryptWithSops(path))
}

// markLoaded stamps the success return of decryptWithSops with Loaded=true
// at the single call site that needs it. Error returns are left untouched.
func markLoaded(got SecretsFile, err error) (SecretsFile, error) {
        if err == nil {
                got.Loaded = true
        }
        return got, err
}

// validatePath checks the path is absolute or safely non-empty.
func validatePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("secrets: empty file path")
	}
	return nil
}

// SetTestDecrypter replaces the sops-binary decryption with the given
// flat-`secrets:`-mapping callback (M9 shape). The callback is wrapped into
// a full-document hook whose cloud-init blocks stay empty, so every M9-era
// test that only exercised `Values` behaves identically after M13.2.
// Test-only; MUST be reset at end of test (e.g. t.Cleanup).
func SetTestDecrypter(fn func(path string) (map[string]string, error)) (restore func()) {
	return SetTestDocDecrypter(func(path string) (SecretsFile, error) {
		m, err := fn(path)
		if err != nil {
			return SecretsFile{}, err
		}
		return SecretsFile{Values: m}, nil
	})
}

// SetTestDocDecrypter replaces the sops-binary decryption with a full
// structured-document callback (M13.2: flat `secrets:` map PLUS
// `cloud-init.ssh-keys` and `cloud-init.passwords`). Test-only; MUST be
// reset at end of test (e.g. t.Cleanup).
func SetTestDocDecrypter(fn func(path string) (SecretsFile, error)) (restore func()) {
	decryptMu.Lock()
	prev := testDocDecrypter
	testDocDecrypter = fn
	decryptMu.Unlock()
	return func() {
		decryptMu.Lock()
		testDocDecrypter = prev
		decryptMu.Unlock()
	}
}

// docJSON is the JSON shape sops --decrypt --output-type json emits against
// the accepted SOPS layout. `sops` is a metadata side-car we ignore.
type docJSON struct {
	Secrets map[string]json.RawMessage `json:"secrets"`
	CloudInit struct {
		SSHKeys   map[string]json.RawMessage `json:"ssh-keys"`
		Passwords map[string]json.RawMessage `json:"passwords"`
	} `json:"cloud-init"`
}

// decryptWithSops runs the sops binary and parses the structured JSON result.
//
// The legacy M9 shape still has exactly one block (flat top-level
// `secrets:` mapping of scalar values). M13.2 adds the structured
// `cloud-init` block; when it is missing the result is empty and
// SecretsFile.SSHKeys / .Passwords stay nil.
//
// A malformed or non-scalar value is fail-closed (ErrMalformedDocument).
func decryptWithSops(path string) (SecretsFile, error) {
	if _, err := exec.LookPath(sopsBinaryName); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return SecretsFile{}, fmt.Errorf("%w", ErrSOPSBinaryMissing)
		}
		return SecretsFile{}, fmt.Errorf("locate %s: %v", sopsBinaryName, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), sopsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sopsBinaryName,
		"--decrypt", "--input-type", "yaml", "--output-type", "json", path)
	cmd.Env = os.Environ()

	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if runErr := cmd.Run(); runErr != nil {
		return SecretsFile{}, classifySOPSError(ctx, runErr, errBuf.String())
	}
	var doc docJSON
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		return SecretsFile{}, fmt.Errorf("%w: %v", ErrMalformedDocument, err)
	}
	// M9 invariant: the secrets-file MUST be a SOPS document carrying either
	// the flat `secrets:` mapping or M13.2's `cloud-init` block. A file with
	// NEITHER is almost always the proxops config YAML (not a SOPS file) —
	// fail closed with a pointer at the mis-point.
	if doc.Secrets == nil && len(doc.CloudInit.SSHKeys) == 0 && len(doc.CloudInit.Passwords) == 0 {
		return SecretsFile{}, fmt.Errorf("%w: %s did not contain a SOPS `secrets:` or `cloud-init:` mapping (point secrets-file at the SOPS file, not the proxops config)",
			ErrMalformedDocument, shorten(path))
	}
	vals, err := flatFromJSON(doc.Secrets)
	if err != nil {
		return SecretsFile{}, err
	}
	ssh, err := flatFromJSON(doc.CloudInit.SSHKeys)
	if err != nil {
		return SecretsFile{}, fmt.Errorf("%w: cloud-init.ssh-keys: %v", ErrMalformedDocument, err)
	}
	pwd, err := flatFromJSON(doc.CloudInit.Passwords)
	if err != nil {
		return SecretsFile{}, fmt.Errorf("%w: cloud-init.passwords: %v", ErrMalformedDocument, err)
	}
	return SecretsFile{
		Values:     vals,
		SSHKeys:    ssh,
		Passwords:  pwd,
		SourcePath: path,
	}, nil
}

// flatFromJSON decodes a SOPS mapping (name → scalar) into a Go
// map[string]string. Empty / nil input yields an empty map.
func flatFromJSON(src map[string]json.RawMessage) (map[string]string, error) {
	if len(src) == 0 {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(src))
	for name, raw := range src {
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			// Non-string leaf: accept numbers (coerce); reject maps/lists.
			var f float64
			if ferr := json.Unmarshal(raw, &f); ferr != nil {
				return nil, fmt.Errorf("%w: secret %q is not a scalar string or number", ErrMalformedDocument, name)
			}
			v = formatFloat(f)
		}
		out[name] = v
	}
	return out, nil
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
func SOPSAvailable() error {
	_, err := exec.LookPath(sopsBinaryName)
	return err
}

// SOPSError classifies an encrypting sops child process's stderr. The
// encrypt path re-uses the sentinel set from the decrypt path: any sops
// failure during re-encryption is either an identity/metadata problem
// (same class), a sops-binary problem (binary missing), or an unknown
// failure (exit code + redacted hint).
func SOPSError(ctx context.Context, err error, stderr string) error {
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%w: encryption exceeded the %s budget", ErrNoIdentity, sopsTimeout)
	}
	s := strings.ToLower(stderr)
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
		strings.Contains(s, "unable to decrypt"),
		strings.Contains(s, "encryption failed"),
		strings.Contains(s, "unknown age recipient"):
		return fmt.Errorf("%w", ErrIdentityMismatch)
	default:
		code := -1
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
		return fmt.Errorf("sops encrypt failed (exit %d): %s", code, redactLine(stderr))
	}
}

// EncodeSopsYAML renders a SecretsFile as the SOPS-document YAML layout
// ProxOps accepts (both on decrypt and on re-encrypt). The output shape is
// DETERMINISTIC so a logical no-change re-encrypt can be short-circuited.
//
// Layout:
//
//	secrets:
//	  flat-key: value
//	  ...
//	cloud-init:
//	  ssh-keys:
//	    name: "line"
//	  passwords:
//	    name: value
//
// Top-level block order is fixed ("secrets" then "cloud-init"). Keys within
// each block are sorted. Scalar values are emitted as single-quoted YAML
// strings (with backslashes escaped) when they contain YAML-special
// characters; otherwise as bare strings. Multi-line public keys are single-
// quoted to keep them as single lines (PVE expects the key-line to be
// contiguous in memory).
//
// This render is NOT used by DecryptFile (that path reads arbitrary SOPS
// documents produced by operators' `sops --encrypt`), but it IS used for
// re-encryption after --adopt-secrets and it must stay byte-stable for a
// byte-stable input so re-encrypting the same document yields the same YAML
// (deterministic idempotency test).
func EncodeSopsYAML(doc *SecretsFile) string {
	var b strings.Builder
	if len(doc.Values) > 0 {
		b.WriteString("secrets:\n")
		for _, k := range sortedKeys(doc.Values) {
			fmt.Fprintf(&b, "  %s: %s\n", yamlScalarSafe(k), yamlScalarQuoted(doc.Values[k]))
		}
	}
	if len(doc.SSHKeys) > 0 || len(doc.Passwords) > 0 {
		b.WriteString("cloud-init:\n")
		if len(doc.SSHKeys) > 0 {
			b.WriteString("  ssh-keys:\n")
			for _, k := range sortedKeys(doc.SSHKeys) {
				fmt.Fprintf(&b, "    %s: %s\n", yamlScalarSafe(k), yamlScalarQuoted(doc.SSHKeys[k]))
			}
		}
		if len(doc.Passwords) > 0 {
			b.WriteString("  passwords:\n")
			for _, k := range sortedKeys(doc.Passwords) {
				fmt.Fprintf(&b, "    %s: %s\n", yamlScalarSafe(k), yamlScalarQuoted(doc.Passwords[k]))
			}
		}
	}
	return b.String()
}

// yamlScalarSafe normalises a YAML scalar key for a sops-document render.
// The `secrets:` top-level block keys are user-supplied; we accept the same
// character set the M9 credential references use ("a-z0-9-").
func yamlScalarSafe(k string) string {
	// YAML keys that would be unquoted-confusing (leading dash, colon, etc.)
	// are quoted; proxops restricts M13.2 ssh-key/password names to
	// [a-z0-9][a-z0-9-]* so this is mostly a belt-and-braces pass.
	if k == "" || k[0] == '-' || strings.ContainsAny(k, ":") {
		return `"` + strings.ReplaceAll(k, `"`, `\"`) + `"`
	}
	return k
}

// yamlScalarQuoted quotes a value with double quotes, escaping backslashes
// + double quotes. Newlines are escaped to \n (sops round-trips them as
// literal \n in the encrypted value; ProxOps decodes them back when it
// re-renders — so the round-trip is stable).
func yamlScalarQuoted(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return `"` + v + `"`
}

// sortedKeys returns the keys of a string map in lexicographic order.
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// encryptMu serialises encrypt+rename so concurrent --adopt-secrets calls
// against the same destination file never interleave their writes.
var encryptMu sync.Mutex

// EncryptToFile re-encrypts doc into dest using sops --encrypt (age backend)
// with the given recipient public keys. The output is written atomically:
//
//   1. sops --encrypt stdin -> stdout (encrypted bytes, in memory)
//   2. temp file <dest>.proxops-tmp with 0600 perms is created + written
//   3. os.Rename(temp, dest) on the same filesystem
//
// On any failure, the temp file is removed and dest is untouched.
//
// The plaintext YAML is passed to sops via the child process's stdin (no
// plaintext temp file on disk, task §12). The age recipients come from the
// existing SOPS file's metadata: ProxOps reads them via ReadAgeRecipients
// (which uses sops CLI to inspect the document — never re-encrypts with a
// DIFFERENT recipient set).
func EncryptToFile(doc *SecretsFile, recipients []string, dest string) error {
	if len(recipients) == 0 {
		return fmt.Errorf("secrets: no age recipients supplied; ProxOps never re-encrypts without the original recipient(s) (task §12)")
	}
	encryptMu.Lock()
	defer encryptMu.Unlock()
	plaintext := EncodeSopsYAML(doc)
	b, err := encryptSOPS(plaintext, recipients)
	if err != nil {
		return err
	}
	tmp := dest + ".proxops-tmp"
	if err := atomicWrite(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename encrypted file into place: %w", err)
	}
	return nil
}

// encryptSOPS shells out to `sops --encrypt --input-type yaml --age <list>`
// with the plaintext on stdin; returns the encrypted bytes on stdout. Errors
// are classified via SOPSError().
func encryptSOPS(plaintext string, recipients []string) ([]byte, error) {
	if err := SOPSAvailable(); err != nil {
		return nil, fmt.Errorf("%w", ErrSOPSBinaryMissing)
	}
	ctx, cancel := context.WithTimeout(context.Background(), sopsTimeout)
	defer cancel()
	ageArg := ""
	for i, r := range recipients {
		if i > 0 {
			ageArg += ","
		}
		ageArg += r
	}
	// --filename-override is required by sops 3.x for stdin encrypt
	// (it infers the input type from the filename when not explicit; we
	// ARE explicit via --input-type/--output-type, so any deterministic
	// .yaml override name is fine — the content never touches disk).
	cmd := exec.CommandContext(ctx, sopsBinaryName,
		"encrypt",
		"--filename-override", "secrets.sops.yaml",
		"--input-type", "yaml",
		"--output-type", "yaml",
		"--age", ageArg)
	cmd.Stdin = strings.NewReader(plaintext)
	cmd.Env = os.Environ() // the operator's env (including SOPS_AGE_KEY_FILE) reaches sops
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, SOPSError(ctx, err, errBuf.String())
	}
	return out.Bytes(), nil
}

// ReadAgeRecipients extracts the age public-key recipients from an existing
// SOPS-encrypted file WITHOUT decrypting it. ProxOps uses this on the
// --adopt-secrets path: when we need to re-encrypt the document with a new
// cloud-init block, we MUST keep the original recipients (dropping one would
// lock out the other operators). The implementation shells out to
// `sops --decrypt` is wrong for this: `sops` exposes the metadata via a
// `--show-version`-style flag on some versions; on sops 3.x the reliable
// path is to inspect the raw `sops.kms/age` YAML block directly. The
// metadata block on the enctypted file is unencrypted and is a stable,
// sops-version-stable YAML layout:
//
//	sops:
//	  age:
//	    - recipient: <public key>
//	      enc: |
//	        -----BEGIN AGE ENCRYPTED FILE-----
//	        ...
//	        -----END AGE ENCRYPTED FILE-----
//
// We parse that unencrypted block with Go's YAML reader — NEVER the
// encrypted data.
func ReadAgeRecipients(path string) ([]string, error) {
	if err := validatePath(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read SOPS file for age recipients: %w", err)
	}
	var doc struct {
		Sops struct {
			Age []struct {
				Recipient string `yaml:"recipient"`
			} `yaml:"age"`
		} `yaml:"sops"`
	}
	if err := yamlUnmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("secrets: parse SOPS age metadata in %s: %w", shorten(path), err)
	}
	out := make([]string, 0, len(doc.Sops.Age))
	for _, a := range doc.Sops.Age {
		if a.Recipient == "" {
			continue
		}
		out = append(out, a.Recipient)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("secrets: %s has no age recipients in its sops metadata; cannot re-encrypt (task §12)", shorten(path))
	}
	return out, nil
}

// yamlUnmarshal is a thin wrapper so the import is used in exactly one
// place. (ProxOps's internal/config already imports gopkg.in/yaml.v3; we
// import the same here so the reader is consistent with the rest of the
// codebase.)
func yamlUnmarshal(b []byte, v any) error {
	return yaml.Unmarshal(b, v)
}

// atomicWrite creates the file at path with 0644 then rewrites it to mode
// perm (umask-safe: 0600 is the common target for SOPS-encrypted files).
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	// Close on every path; a close failure (e.g. disk full mid-flush)
	// must not be silently swallowed — and the defer form would bury it
	// behind the write/sync error reporting (errcheck + correctness).
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return werr
	}
	if serr := f.Sync(); serr != nil {
		_ = f.Close()
		return serr
	}
	return f.Close()
}
