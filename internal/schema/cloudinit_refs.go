// M13.2: Cloud-Init SOPS reference validation.
//
// A VM/TemplateVM manifest can name (reference) Cloud-Init secret material
// that lives in the cluster's SOPS document:
//
//   - spec.cloud-init-data.ssh-key-refs: a PLURAL list of "cloud-init.ssh-keys.<name>"
//     dot-paths that resolve to OpenSSH public keys.
//   - spec.cloud-init-data.ci-password-ref: a SINGULAR "cloud-init.passwords.<name>"
//     dot-path that resolves to the PVE cipassword value.
//
// The reference name must be a valid YAML mapping key (non-empty, and only a
// small conservative character set so the dot-path round-trips cleanly).
//
// The actual SOPS lookup (does the named key exist and is it non-empty in
// the cluster's decrypted SOPS document) happens at Reconcile time (schema.
// ResolveCloudInitSecrets): the config layer has already resolved SOPS and
// exposed the cloud-init material, so this call fails closed when a manifest
// names a key the document does not carry.
package schema

import (
	"fmt"
	"strings"
)

// CloudInitSSHRefPrefix is the required prefix for an ssh-key-ref.
const CloudInitSSHRefPrefix = "cloud-init.ssh-keys."

// CloudInitPasswordRefPrefix is the required prefix for a ci-password-ref.
const CloudInitPasswordRefPrefix = "cloud-init.passwords."

// validCloudInitRefName reports whether s is a valid SOPS reference name:
// non-empty, no dot or whitespace, characters [a-z0-9-], leading alnum.
func validCloudInitRefName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i, c := range s {
		if c == '.' || c == ' ' || c == '\t' {
			return false
		}
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
		// The per-char check above already permits [a-z0-9-] and rejects
		// dots/whitespace; the only position-specific rule is "no leading
		// dash" (keeps YAML keys unambiguous and mirrors SOPS key style).
		if i == 0 && c == '-' {
			return false
		}
	}
	return true
}

// ValidateCloudInitRefs checks the cloud-init SOPS reference fields on
// CloudInitData + enforces the ssh-keys / ssh-key-refs mutual-exclusion rule.
//
// Returns an error on:
//   - empty / malformed ssh-key-refs entries (must start with the prefix +
//     a valid ref name; no trailing dot / no whitespace / no "..").
//   - empty / malformed ci-password-ref (must start with the password prefix
//     + a valid ref name).
//   - both SSHKeys and SSHKeyRefs being non-empty: ambiguous intent — the
//     operator must pick one.
//
// The sentinel "*" entry in SSHKeys is already enforced earlier in VM.Validate.
func ValidateCloudInitRefs(ref string, cd CloudInitData) error {
	// Mutual-exclusion.
	if len(cd.SSHKeys) > 0 && len(cd.SSHKeyRefs) > 0 {
		return fmt.Errorf("%s: spec.cloud-init-data defines BOTH ssh-keys (plaintext, pre-M13.2) and ssh-key-refs (SOPS-referenced, M13.2); pick one — keep ssh-keys to hold keys in-repo verbatim, or switch to ssh-key-refs to hold keys in SOPS", ref)
	}
	for i, r := range cd.SSHKeyRefs {
		r = strings.TrimSpace(r)
		if r == "" {
			return fmt.Errorf("%s: spec.cloud-init-data.ssh-key-refs[%d] is empty (drop the entry or provide a cloud-init.ssh-keys.<name> dot-path)", ref, i)
		}
		name, ok := strings.CutPrefix(r, CloudInitSSHRefPrefix)
		if !ok {
			return fmt.Errorf("%s: spec.cloud-init-data.ssh-key-refs[%d] %q does not start with %q", ref, i, r, CloudInitSSHRefPrefix)
		}
		if !validCloudInitRefName(name) {
			return fmt.Errorf("%s: ssh-key-ref %q has an invalid reference name (want [a-z0-9][a-z0-9-]* up to 64 chars, no dots or spaces)", ref, r)
		}
	}
	if cd.CIPasswordRef != "" {
		p := strings.TrimSpace(cd.CIPasswordRef)
		name, ok := strings.CutPrefix(p, CloudInitPasswordRefPrefix)
		if !ok {
			return fmt.Errorf("%s: spec.cloud-init-data.ci-password-ref %q does not start with %q", ref, p, CloudInitPasswordRefPrefix)
		}
		if !validCloudInitRefName(name) {
			return fmt.Errorf("%s: ci-password-ref %q has an invalid reference name (want [a-z0-9][a-z0-9-]* up to 64 chars, no dots or spaces)", ref, p)
		}
	}
	return nil
}

// CloudInitSSHKeysEffective returns the effective ssh-keys list for a
// CloudInitData after resolution. Rules:
//   - resolvedSshKeys non-empty (SOPS-resolved): returns that, in
//     manifest-refs order. This is what ProxOps writes to PVE.
//   - else SSHKeys non-empty: returns the manifest's plaintext keys.
//   - else: nil.
//
// The sentinel "*" remains a special case: SSHKeys == [ "*"] returns the
// sentinel unchanged (PVE-owned semantics preserved).
func CloudInitSSHKeysEffective(cd CloudInitData) []string {
	if len(cd.resolvedSshKeys) > 0 {
		return cd.resolvedSshKeys
	}
	if len(cd.SSHKeys) > 0 {
		return cd.SSHKeys
	}
	return nil
}

// CloudInitCIPasswordEffective returns the effective PVE cipassword value
// for a CloudInitData after resolution:
//   - resolvedCIPassword non-empty: returns that.
//   - else: "" (ProxOps does not own the field).
func CloudInitCIPasswordEffective(cd CloudInitData) string {
	return cd.resolvedCIPassword
}
