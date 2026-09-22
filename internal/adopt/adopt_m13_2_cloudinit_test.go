// M13.2: cloud-init secret adoption tests.
//
// Pins:
//   - Plain adopt.Run (zero Options): unchanged M10 behaviour — PVE
//     sshkeys → ["*"] sentinel; cipassword → redacted gap.
//   - adopt.RunWithOptions with SOPS material: full-key match emits
//     ssh-key-refs on the generated manifest (no sentinel, no PII).
//   - Partial match (one key in SOPS, one not) with --adopt-secrets off:
//     all-or-nothing rule keeps the resource on the sentinel (manifest
//     stays schema-valid; no PII, no partial refs).
//   - --adopt-secrets on: unmatched live keys receive deterministic
//     "adopted-<fingerprint>" names in Result.NewSSHKeys (the CLI writes
//     these into the SOPS file; adopt itself performs ZERO PVE writes).
//   - Deterministic dedup: two live lines with the same key body but
//     different comments map to ONE SOPS entry (fingerprint excludes the
//     comment column).
//   - Cloud-init census counts land on Result.CloudInit (counts only).
//   - PII no-leak: the live PVE ssh-key line and the PVE cipassword value
//     NEVER appear in any manifest, gap, warning, skipped, or log.
//   - Passwords are NOT auto-imported under any circumstances.
//
// PVE-shape note: PVE 9.2's /config percent-encodes the whole `sshkeys`
// value (spaces → %20, newlines → %0A). The fixtures below mimic that
// shape so the decode path (schema.PveCISshLinesFromPVE) is exercised
// honestly.
package adopt_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/Kelcode-Dev/proxops/internal/adopt"
	"github.com/Kelcode-Dev/proxops/internal/pveclient/mock"
	"github.com/Kelcode-Dev/proxops/internal/schema"
	"github.com/Kelcode-Dev/proxops/internal/secrets"
)

// M13.2 test keys. Fixtures, not real PII; chosen so the exact bytes are
// assertable (and must never leak).
const (
	m132Key1 = "ssh-ed25519 AAAAC3NzMyMDEyMzIzMDFhYmNkZWYga2V5MQ"
	// m132Key2 and m132Key2b share the public-key body and differ ONLY in
	// the comment column — the fingerprint (type+blob, comment excluded)
	// must collapse them to one SOPS entry.
	m132Key2  = "ssh-ed25519 BBBB5Z01bGJqay1tbnUvcGhpcy1pcy1rZXktaD10ZWFtcC1ob3N0 team-host"
	m132Key2b = "ssh-ed25519 BBBB5Z01bGJqay1tbnUvcGhpcy1pcy1rZXktaD10ZWFtcC1ob3N0 other-comment"
	m132Pw    = "m13-2-test-password-value"
)

// pveSshKeysWire percent-encodes multi-line ssh-keys the way PVE 9.2
// reports them: every byte outside the RFC 3986 unreserved set is
// %-encoded, with UPPERCASE hex digits (PVE encodes spaces as %20, NOT
// + — Go's url.QueryEscape is form-encoding and is WRONG here). Lines are
// joined by %0A.
func pveSshKeysWire(lines ...string) string {
	encs := make([]string, 0, len(lines))
	for _, l := range lines {
		encs = append(encs, pve3986Encode(l))
	}
	return strings.Join(encs, "%0A")
}

func pve3986Encode(s string) string {
	const upperhex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) * 3)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upperhex[c>>4])
		b.WriteByte(upperhex[c&0x0f])
	}
	return b.String()
}

// preloadM132VM seeds a PVE VM with M13.2-shaped cloud-init fields.
func preloadM132VM(m *mock.Server, vmid int, sshKeyLines ...string) {
	cfg := map[string]string{
		"name":       "m132-vm",
		"memory":     "1024",
		"cores":      "1",
		"scsi0":      fmt.Sprintf("local-lvm:vm-%d-disk-0,size=8G", vmid),
		"net0":       "virtio=00:00:00:00:00:99,bridge=vmbr0",
		"ciuser":     "root",
		"cipassword": m132Pw,
		"digest":     "aa",
		"vmgenid":    "bb",
	}
	if len(sshKeyLines) > 0 {
		cfg["sshkeys"] = pveSshKeysWire(sshKeyLines...)
	}
	m.PreloadVM("pve01", vmid, cfg, "stopped")
}

func m132Opts(sops secrets.SecretsFile) adopt.Options {
	// Test stores are always a "configured SOPS store" (SOPS-backed); Loaded
	// is true when a SOPS file is set up and successfully decrypted, even if
	// its cloud-init blocks are empty. If a test wants "no SOPS store at all"
	// it MUST use Options{} (SOPS.Loaded=false).
	sops.Loaded = true
	return adopt.Options{SOPS: sops}
}

// m132AllText concatenates every non-PII-bearing adopt surface
// (manifests, gaps, logs, incomplete, skipped, warnings) for leak checks.
// Result.NewSSHKeys is deliberately EXCLUDED: that is the SOPS-import
// payload where the PII is ALLOWED to live (the CLI writes it to the
// encrypted file).
func m132AllText(res *adopt.Result) string {
	var b strings.Builder
	for _, w := range res.Wrote {
		b.WriteString(w.Content)
		b.WriteString("\n")
	}
	for _, g := range res.Gaps {
		fmt.Fprintf(&b, "gap %s#%d %s=%s note=%s\n", g.Kind, g.ID, g.Field, g.Value, g.Note)
	}
	for _, l := range res.Logs {
		b.WriteString(l)
		b.WriteString("\n")
	}
	for _, s := range res.Incomplete {
		b.WriteString(s)
		b.WriteString("\n")
	}
	for _, s := range res.Skipped {
		b.WriteString(s.String())
		b.WriteString("\n")
	}
	for _, w := range res.Warnings {
		b.WriteString(w)
		b.WriteString("\n")
	}
	return b.String()
}

func m132VMManifest(res *adopt.Result) string {
	for _, w := range res.Wrote {
		if w.Kind == schema.KindVM {
			return w.Content
		}
	}
	return ""
}

// TestAdopt_M132_PlainRunUnchanged pins that adopt.Run without Options
// (the plain CLI path) preserves M10's redaction behaviour: PVE sshkeys →
// ["*"] sentinel; cipassword → "<redacted>" gap; census carries counts
// only; PII leaks nowhere.
func TestAdopt_M132_PlainRunUnchanged(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	preloadM132VM(m, 9100, m132Key1)
	c := newMockClient(t, m)
	root := t.TempDir()

	res, err := adopt.Run(context.Background(), c, "m132-dev", []string{"pve01"}, root)
	if err != nil {
		t.Fatalf("adopt.Run: %v", err)
	}
	if got := c.WritesPerformed(); got != 0 {
		t.Fatalf("adopt wrote to PVE: %d write endpoints (M13.2 plain path MUST be read-only)", got)
	}
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("mock PVE observed %d write requests", got)
	}
	for _, meth := range m.Methods() {
		if meth != "GET" {
			t.Fatalf("mock PVE observed a non-GET request: %s", meth)
		}
	}
	vmManifest := m132VMManifest(res)
	if vmManifest == "" {
		t.Fatalf("no VM manifest written; paths=%v", m10Paths(res.Wrote))
	}
	// yaml.v3 renders SSHKeys: ["*"] as a block list: ssh-keys:\n  - '*'
	if !strings.Contains(vmManifest, "ssh-keys:") || !strings.Contains(vmManifest, "- '*'") {
		t.Errorf("plain adopt: manifest does not carry the M10 ssh-keys sentinel:\n%s", vmManifest)
	}
	if strings.Contains(vmManifest, "ssh-key-refs") {
		t.Errorf("plain adopt: manifest unexpectedly carries M13.2 ssh-key-refs; the SOPS store was empty")
	}
	// cipassword MUST be a redacted gap (PVE 9.2 masks it; unrecoverable).
	gapPw := false
	for _, g := range res.Gaps {
		if g.Field == "cipassword" {
			gapPw = true
			if g.Value != "<redacted>" {
				t.Errorf("cipassword gap value = %q, want <redacted> (never the PVE value)", g.Value)
			}
		}
	}
	if !gapPw {
		t.Errorf("plain adopt: expected a redacted cipassword gap; got %+v", res.Gaps)
	}
	// sshkeys is OWNED (sentinel) → no gap for it in plain mode.
	for _, g := range res.Gaps {
		if g.Field == "sshkeys" {
			t.Errorf("plain adopt: sshkeys gap emitted although the sentinel owns the field: %+v", g)
		}
	}
	// Census counts.
	if res.CloudInit.SSHResources != 1 {
		t.Errorf("census SSHResources = %d, want 1", res.CloudInit.SSHResources)
	}
	if res.CloudInit.PwResources != 1 {
		t.Errorf("census PwResources = %d, want 1", res.CloudInit.PwResources)
	}
	// M13.2 UX cleanup: SSHUniqueLiveKeys describes PVE material, not SOPS
	// matching — a plain SOPS-less run still reports the live key it saw.
	if res.CloudInit.SSHUniqueLiveKeys != 1 {
		t.Errorf("census SSHUniqueLiveKeys = %d, want 1 (one live key on PVE, regardless of SOPS)", res.CloudInit.SSHUniqueLiveKeys)
	}
	if res.CloudInit.SSHReused != 0 || res.CloudInit.SSHAdded != 0 {
		t.Errorf("census SOPS reused/added = %d/%d, want 0/0 (no SOPS matching in plain mode)", res.CloudInit.SSHReused, res.CloudInit.SSHAdded)
	}
	if res.CloudInit.SSHResourcesWithRefs != 0 {
		t.Errorf("census SSHResourcesWithRefs = %d, want 0 (plain mode keeps the sentinel)", res.CloudInit.SSHResourcesWithRefs)
	}
	if res.CloudInit.SOPSBacked {
		t.Errorf("census SOPSBacked = true; no SOPS store was configured")
	}
	if s := m132AllText(res); strings.Contains(s, m132Pw) || strings.Contains(s, m132Key1) {
		t.Fatalf("PVE PII leaked into the adopt report:\n%s", s)
	}
}

// TestAdopt_M132_EXACTMatchEmitsRefs pins that when the cluster SOPS
// carries the live key material verbatim, adopt emits ssh-key-refs (NOT
// the sentinel) on the generated manifest, and the PII leaks nowhere.
func TestAdopt_M132_EXACTMatchEmitsRefs(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	preloadM132VM(m, 9101, m132Key1)
	c := newMockClient(t, m)

	sops := secrets.SecretsFile{
		SSHKeys: map[string]string{
			"ops-a":  m132Key1,
			"unused": "ssh-ed25519 N05U9z83Y2FtcGxlIG9wcy1pbi10aGUtZG9j",
		},
		Passwords: map[string]string{"default": "unrelated-existing-pw"},
	}
	res, err := adopt.RunWithOptions(context.Background(), c, "m132-dev", []string{"pve01"}, t.TempDir(), m132Opts(sops))
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("mock PVE observed %d write requests", got)
	}
	vmManifest := m132VMManifest(res)
	if !strings.Contains(vmManifest, "ssh-key-refs") {
		t.Fatalf("SOPS-matched adopt: manifest does not carry ssh-key-refs:\n%s", vmManifest)
	}
	if !strings.Contains(vmManifest, "cloud-init.ssh-keys.ops-a") {
		t.Fatalf("SOPS-matched adopt: ssh-key-refs does not carry the matching SOPS name:\n%s", vmManifest)
	}
	if strings.Contains(vmManifest, m132Key1) {
		t.Fatalf("SOPS-matched adopt: live PVE ssh-key line leaked into the manifest (ref-only expected):\n%s", vmManifest)
	}
	if strings.Contains(vmManifest, "unused") {
		t.Errorf("SOPS-matched adopt: the non-matching SOPS name 'unused' must not be referenced:\n%s", vmManifest)
	}
	// The sentinel must be GONE (all-or-nothing rule replaced it).
	if strings.Contains(vmManifest, "- '*'") {
		t.Errorf("SOPS-matched adopt: manifest still carries the ssh-keys sentinel alongside refs:\n%s", vmManifest)
	}
	// No PII anywhere.
	if s := m132AllText(res); strings.Contains(s, m132Pw) {
		t.Fatalf("PVE PII leaked into the adopt report:\n%s", s)
	}
	// New names: NONE (matching, not importing).
	if len(res.NewSSHKeys) != 0 {
		t.Errorf("NewSSHKeys = %v; exact match must not create new names", res.NewSSHKeys)
	}
	// Census.
	if res.CloudInit.SSHUniqueLiveKeys != 1 || res.CloudInit.SSHReused != 1 || res.CloudInit.SSHAdded != 0 {
		t.Errorf("census live-unique/reused/added = %d/%d/%d, want 1/1/0",
			res.CloudInit.SSHUniqueLiveKeys, res.CloudInit.SSHReused, res.CloudInit.SSHAdded)
	}
	if res.CloudInit.SSHResourcesWithRefs != 1 {
		t.Errorf("census resources with refs = %d, want 1", res.CloudInit.SSHResourcesWithRefs)
	}
	if res.CloudInit.SOPSBacked != true {
		t.Errorf("census SOPSBacked = false; the store was populated")
	}
	if len(res.AdoptedSSHRefs) != 1 || res.AdoptedSSHRefs[0] != "ops-a" {
		t.Errorf("AdoptedSSHRefs = %v, want [ops-a]", res.AdoptedSSHRefs)
	}
}

// TestAdopt_M132_PartialMatchAllOrNothing pins that ONE unmatched live key
// in the resource's PVE report causes ALL of that resource's ssh-keys to
// remain on the sentinel (mixed sentinel+refs is a schema-parse error):
// proxops fails closed rather than clobber the unadopted key.
func TestAdopt_M132_PartialMatchAllOrNothing(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	// Two live keys on one VM: the first in SOPS, the second NOT.
	preloadM132VM(m, 9102, m132Key1, m132Key2)
	c := newMockClient(t, m)

	sops := secrets.SecretsFile{
		SSHKeys: map[string]string{"ops-a": m132Key1},
	}
	res, err := adopt.RunWithOptions(context.Background(), c, "m132-dev", []string{"pve01"}, t.TempDir(), m132Opts(sops))
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	vmManifest := m132VMManifest(res)
	if !strings.Contains(vmManifest, "ssh-keys:") || !strings.Contains(vmManifest, "- '*'") {
		t.Errorf("partial-match resource: manifest did not fall back to the ssh-keys sentinel:\n%s", vmManifest)
	}
	if strings.Contains(vmManifest, "ssh-key-refs") {
		t.Errorf("partial-match resource: manifest carries refs; the all-or-nothing rule MUST keep the whole resource on the sentinel:\n%s", vmManifest)
	}
	if strings.Contains(vmManifest, m132Key2) {
		t.Errorf("partial-match resource: unmatched live key leaked into the manifest:\n%s", vmManifest)
	}
	// Census: the resource configured ssh-keys (counted), but NO ref was
	// adopted (all-or-nothing fell back to the sentinel on the WHOLE resource).
	// M13.2 UX cleanup: 2 live lines were OBSERVED and deduped to 2 distinct
	// fingerprints (SSHUniqueLiveKeys=2) — independent of match outcome.
	if res.CloudInit.SSHResources != 1 {
		t.Errorf("census SSHResources = %d, want 1", res.CloudInit.SSHResources)
	}
	if res.CloudInit.SSHUniqueLiveKeys != 2 {
		t.Errorf("census SSHUniqueLiveKeys = %d, want 2 (two live lines observed)", res.CloudInit.SSHUniqueLiveKeys)
	}
	if res.CloudInit.SSHReused != 0 || res.CloudInit.SSHResourcesWithRefs != 0 {
		t.Errorf("census reused/resourcesWithRefs = %d/%d, want 0/0 (all-or-nothing fell back to sentinel)",
			res.CloudInit.SSHReused, res.CloudInit.SSHResourcesWithRefs)
	}
	// No PII anywhere.
	if s := m132AllText(res); strings.Contains(s, m132Pw) || strings.Contains(s, m132Key2) {
		t.Fatalf("PVE PII leaked into the adopt report:\n%s", s)
	}
}

// TestAdopt_M132_AdoptSecretsNewNames pins --adopt-secrets mode with
// UNMATCHED live keys: Result.NewSSHKeys carries deterministic
// "adopted-<fingerprint>" names, VM 9104 and 9105 (same key body,
// different comment) dedupe to the SAME SOPS entry, and the run is
// deterministic across invocations.
func TestAdopt_M132_AdoptSecretsNewNames(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	preloadM132VM(m, 9103, m132Key1)
	preloadM132VM(m, 9104, m132Key2)
	preloadM132VM(m, 9105, m132Key2b) // same body as 9104, different comment
	c := newMockClient(t, m)

	sops := secrets.SecretsFile{SSHKeys: map[string]string{"ops-a": m132Key1}}
	opts := m132Opts(sops)
	opts.AdoptSecrets = true
	res, err := adopt.RunWithOptions(context.Background(), c, "m132-dev", []string{"pve01"}, t.TempDir(), opts)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	// One EXISTING entry (key1) reused; ONE NEW entry (key2 == key2b) added;
	// three resources configured ssh-keys.
	if res.CloudInit.SSHReused != 1 {
		t.Errorf("census SSHReused = %d, want 1 (only key1)", res.CloudInit.SSHReused)
	}
	if res.CloudInit.SSHAdded != 1 {
		t.Errorf("census SSHAdded = %d, want 1 (key2+key2b collapse by fingerprint)", res.CloudInit.SSHAdded)
	}
	if len(res.NewSSHKeys) != 1 {
		t.Fatalf("NewSSHKeys = %d entries, want 1", len(res.NewSSHKeys))
	}
	for name := range res.NewSSHKeys {
		if !strings.HasPrefix(name, "adopted-") {
			t.Errorf("NewSSHKeys name %q does not start with adopted-", name)
		}
	}
	// Every VM manifest carries ssh-key-refs (NOT the sentinel): 9103 →
	// ops-a; 9104 and 9105 → the single adopted name.
	adoptedNames := extractRefNamesAcross(res.Wrote)
	refNames := map[string]bool{}
	for _, w := range res.Wrote {
		if w.Kind != schema.KindVM {
			continue
		}
		for _, n := range extractCloudInitRefNames(w.Content) {
			refNames[n] = true
		}
	}
	if !refNames["ops-a"] || !refNames[adoptedNames[0]] {
		t.Errorf("manifest refs = %v; want ops-a + the adopted name", refNames)
	}
	if len(adoptedNames) != 1 {
		t.Errorf("adopted ref names across manifests = %v, want 1 (dedup)", adoptedNames)
	}

	// Determinism: re-run the same SOPS store + same PVE fleet; the NEW
	// name must be byte-identical.
	m2 := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m2.Close)
	preloadM132VM(m2, 9103, m132Key1)
	preloadM132VM(m2, 9104, m132Key2)
	preloadM132VM(m2, 9105, m132Key2b)
	c2 := newMockClient(t, m2)
	res2, err := adopt.RunWithOptions(context.Background(), c2, "m132-dev", []string{"pve01"}, t.TempDir(), opts)
	if err != nil {
		t.Fatalf("re-run RunWithOptions: %v", err)
	}
	if len(res2.NewSSHKeys) != len(res.NewSSHKeys) {
		t.Fatalf("re-run NewSSHKeys count changed: %d -> %d", len(res.NewSSHKeys), len(res2.NewSSHKeys))
	}
	for k, v := range res.NewSSHKeys {
		if v2, ok := res2.NewSSHKeys[k]; !ok || v2 != v {
			t.Errorf("NewSSHKeys[%s] unstable across runs: %q vs %q", k, v, v2)
		}
	}
}

// TestAdopt_M132_PII_noLeak_inNewNames pins that the live PVE ssh-key line
// DOES end up in Result.NewSSHKeys (the SOPS-import payload the CLI writes
// to the encrypted file) and that NOTHING else in the adopt surface carries
// it. Passwords are NEVER auto-imported (manual ci-password-ref mapping
// only), even with --adopt-secrets.
func TestAdopt_M132_PII_noLeak_inNewNames(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	preloadM132VM(m, 9106, m132Key2)
	c := newMockClient(t, m)

	// The store already holds a password (proves passwords are never
	// touched by --adopt-secrets) and no SSH keys.
	sops := secrets.SecretsFile{Passwords: map[string]string{"default": m132Pw}}
	opts := m132Opts(sops)
	opts.AdoptSecrets = true
	res, err := adopt.RunWithOptions(context.Background(), c, "m132-dev", []string{"pve01"}, t.TempDir(), opts)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if len(res.NewSSHKeys) != 1 || res.NewSSHKeys["adopted-"+schema.SSHKeyFingerprint(m132Key2)] != m132Key2 {
		t.Fatalf("NewSSHKeys = %v; want exactly one adopted key line", res.NewSSHKeys)
	}
	// No PII anywhere else.
	if s := m132AllText(res); strings.Contains(s, m132Key2) {
		t.Fatalf("live ssh-key leaked into manifests/gaps/warnings/logs:\n%s", s)
	}
	if s := m132AllText(res); strings.Contains(s, m132Pw) {
		t.Fatalf("cipassword leaked into the adopt report:\n%s", s)
	}
	// Result.NewSSHKeys must not carry password material.
	for _, line := range res.NewSSHKeys {
		if strings.Contains(line, m132Pw) {
			t.Fatalf("password material in NewSSHKeys: %q", line)
		}
	}
}

// TestAdopt_M132_DeterministicName pins the adopted name shape:
// adopted-<16 lowercase hex> where the hex = first 8 bytes of
// sha256("sshpki\x00<type>\x00<blob>"); cross-checked against
// schema.SSHKeyFingerprint (the shared single source of truth).
func TestAdopt_M132_DeterministicName(t *testing.T) {
	fp := schema.SSHKeyFingerprint(m132Key2)
	if len(fp) != 16 {
		t.Fatalf("SSHKeyFingerprint length = %d (%q), want 16 hex chars", len(fp), fp)
	}
	for _, cch := range fp {
		if (cch < '0' || cch > '9') && (cch < 'a' || cch > 'f') {
			t.Fatalf("fingerprint %q is not lowercase hex", fp)
		}
	}
	// Comment must not affect the fingerprint.
	if fp2 := schema.SSHKeyFingerprint(m132Key2b); fp2 != fp {
		t.Errorf("fingerprint differs under comment drift: %q vs %q", fp, fp2)
	}
	if fp3 := schema.SSHKeyFingerprint(m132Key1); fp3 == fp {
		t.Errorf("fingerprint collision between distinct keys")
	}
}

// extractCloudInitRefNames pulls the <name> component(s) off
// cloud-init.ssh-keys.<name> refs in one manifest.
func extractCloudInitRefNames(content string) []string {
	const tok = "cloud-init.ssh-keys."
	out := []string{}
	for _, ln := range strings.Split(content, "\n") {
		v := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "-"))
		if strings.HasPrefix(v, tok) && strings.TrimPrefix(v, tok) != "" {
			out = append(out, strings.TrimPrefix(v, tok))
		}
	}
	sort.Strings(out)
	return out
}

// extractRefNamesAcross returns the union of adopted-<...> ref names seen
// in the written manifests (deduped).
func extractRefNamesAcross(wrote []adopt.WroteManifest) []string {
	seen := map[string]bool{}
	for _, w := range wrote {
		for _, n := range extractCloudInitRefNames(w.Content) {
			if strings.HasPrefix(n, "adopted-") {
				seen[n] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// --- M13.2 UX cleanup (public release) regression tests ---
//
// The bug: "SOPS-backed" was inferred from whether the cloud-init maps
// already carried entries. A valid but initially-empty Cloud-Init secret
// store is still SOPS-backed. The census now distinguishes:
//   - SOPSBacked: a decrypted SOPS store was configured & usable (Loaded=true)
//   - live keys:  PVE material observed (SSHUniqueLiveKeys; independent of SOPS)
//   - SOPS outcome: imported/reused/resources-referencing (the final result)

// TestAdopt_M132_EmptySOPS_StoreStillSOPSBacked pins the exact release-blocker
// scenario: a cluster with a SOPS file configured (empty cloud-init blocks)
// runs PLAIN adopt against PVE material carrying ssh-keys + cipassword.
// The output must NOT claim the cluster "is not SOPS-backed" and the
// census counts must still report PVE material honestly.
func TestAdopt_M132_EmptySOPS_StoreStillSOPSBacked(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	// Multiple VMs, all carrying the same live PVE key + a cipassword.
	for _, vmid := range []int{9190, 9191, 9192} {
		preloadM132VM(m, vmid, m132Key1)
	}
	c := newMockClient(t, m)

	// A configured SOPS store whose cloud-init blocks are empty — the
	// "SOPS-backed but empty" state.
	emptyStore := secrets.SecretsFile{
		SSHKeys:    map[string]string{},
		Passwords:  map[string]string{},
		SourcePath: "/etc/proxops/secrets.sops.yaml",
		Loaded:     true,
	}
	res, err := adopt.RunWithOptions(context.Background(), c, "m132-dev", []string{"pve01"}, t.TempDir(), adopt.Options{SOPS: emptyStore})
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if got := m.WritesObserved(); got != 0 {
		t.Fatalf("mock PVE observed %d write requests (plain path is read-only)", got)
	}
	// SOPS-backed is TRUE: a usable store was configured, even though it is empty.
	if !res.CloudInit.SOPSBacked {
		t.Errorf("census SOPSBacked = false; want true (a SOPS store was configured and decrypted)")
	}
	// Live PVE material is reported honestly: 1 unique key across 3 resources.
	if res.CloudInit.SSHUniqueLiveKeys != 1 {
		t.Errorf("census SSHUniqueLiveKeys = %d, want 1 (1 live key on PVE)", res.CloudInit.SSHUniqueLiveKeys)
	}
	if res.CloudInit.SSHResources != 3 {
		t.Errorf("census SSHResources = %d, want 3", res.CloudInit.SSHResources)
	}
	if res.CloudInit.PwResources != 3 {
		t.Errorf("census PwResources = %d, want 3", res.CloudInit.PwResources)
	}
	// No SOPS outcome: nothing imported, nothing reused, nothing referenced.
	if res.CloudInit.SSHReused != 0 || res.CloudInit.SSHAdded != 0 || res.CloudInit.SSHResourcesWithRefs != 0 {
		t.Errorf("plain adoptions must not produce SOPS refs: reused/added/withRefs = %d/%d/%d",
			res.CloudInit.SSHReused, res.CloudInit.SSHAdded, res.CloudInit.SSHResourcesWithRefs)
	}
	// Summary: plain shape, no "not SOPS-backed" claim, no PII.
	sum := res.Summary()
	if strings.Contains(sum, "not SOPS-backed") || strings.Contains(sum, "NOT SOPS-backed") || strings.Contains(sum, "NOT matched against") {
		t.Errorf("summary incorrectly claims the cluster is not SOPS-backed:\n%s", sum)
	}
	if !strings.Contains(sum, "SSH public keys: 1 unique across 3 resources") {
		t.Errorf("summary missing the live-key census line:\n%s", sum)
	}
	if !strings.Contains(sum, "re-run with --adopt-secrets to import recoverable keys") {
		t.Errorf("summary must tell the operator their next step:\n%s", sum)
	}
	if !strings.Contains(sum, "recoverable: 0") {
		t.Errorf("summary must report password recoverability:\n%s", sum)
	}
	// PII leak check.
	if s := m132AllText(res) + "\n" + sum; strings.Contains(s, m132Pw) || strings.Contains(s, m132Key1) {
		t.Fatalf("PVE PII leaked into the adopt report:\n%s", s)
	}
}

// TestAdopt_M132_AdoptSecrets_EmptyStore_Imports pins the --adopt-secrets path
// against an empty SOPS store: the operator-facing output must describe the
// FINAL RESULT (imported / reused / resources referencing) and must NOT
// contradict itself with a "not SOPS-backed" line.
func TestAdopt_M132_AdoptSecrets_EmptyStore_Imports(t *testing.T) {
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	for _, vmid := range []int{9193, 9194} {
		preloadM132VM(m, vmid, m132Key1)
	}
	c := newMockClient(t, m)

	emptyStore := secrets.SecretsFile{
		SSHKeys:    map[string]string{},
		Passwords:  map[string]string{},
		SourcePath: "/etc/proxops/secrets.sops.yaml",
		Loaded:     true,
	}
	opts := adopt.Options{SOPS: emptyStore, AdoptSecrets: true}
	res, err := adopt.RunWithOptions(context.Background(), c, "m132-dev", []string{"pve01"}, t.TempDir(), opts)
	if err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if len(res.NewSSHKeys) != 1 {
		t.Fatalf("NewSSHKeys = %d entries, want 1 (one distinct live key imported)", len(res.NewSSHKeys))
	}
	// Census: 1 live key across 2 resources; SOPS outcome = imported 1, reused 0,
	// resources referencing 2.
	if res.CloudInit.SSHUniqueLiveKeys != 1 || res.CloudInit.SSHResources != 2 {
		t.Errorf("census live/resources = %d/%d, want 1/2", res.CloudInit.SSHUniqueLiveKeys, res.CloudInit.SSHResources)
	}
	if res.CloudInit.SSHReused != 0 || res.CloudInit.SSHAdded != 1 || res.CloudInit.SSHResourcesWithRefs != 2 {
		t.Errorf("census reused/added/withRefs = %d/%d/%d, want 0/1/2",
			res.CloudInit.SSHReused, res.CloudInit.SSHAdded, res.CloudInit.SSHResourcesWithRefs)
	}
	if !res.CloudInit.SOPSBacked {
		t.Errorf("census SOPSBacked = false; a store was configured")
	}
	// Summary: secret-adoption shape.
	sum := res.Summary()
	if !strings.Contains(sum, "imported: 1") {
		t.Errorf("summary missing 'imported: 1':\n%s", sum)
	}
	if !strings.Contains(sum, "reused existing: 0") {
		t.Errorf("summary missing 'reused existing: 0':\n%s", sum)
	}
	if !strings.Contains(sum, "resources referencing: 2") {
		t.Errorf("summary missing 'resources referencing: 2':\n%s", sum)
	}
	if strings.Contains(sum, "not SOPS-backed") || strings.Contains(sum, "NOT matched against") {
		t.Errorf("summary contradicts the SOPS store:\n%s", sum)
	}
}
