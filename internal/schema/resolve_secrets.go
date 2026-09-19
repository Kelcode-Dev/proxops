// M13.2: resolve SOPS-referenced cloud-init secret material from the
// cluster's in-memory SOPS store into a VM / TemplateVM resource, so that
// its ToCreateParams / Drift / cloneClearKeys see the effective ssh-keys
// and cipassword wire values.
//
// Call site: reconcile.Reconciler.RunOneCycle, immediately after
//  parse.BuildClusterIndex returns. Fail closed:
//
//   - a manifest that declares `spec.cloud-init-data.ssh-key-refs` but the
//     cluster has no SOPS store (or the store is empty) → error
//   - a manifest that names a key the SOPS store does not have → error
//   - the error identifies the reference (no leak of the secret value)
//
// The SOPS store is per-cluster: `internal/config.Config.SopsResolved`
// carries one entry per cluster. reconcile.Reconciler is already
// cluster-scoped, so its Options hold the matching store.
package schema

import (
	"fmt"
	"strings"

	"github.com/GizzmoShifu/proxmox-operator/internal/config"
)

// CloudInitSecretStores carries the per-cluster SOPS-referenced
// cloud-init material for the current reconcile cycle.
//
// Empty stores are OK for a cycle: any manifest that has SOPS refs but no
// SOPS material is a fail-closed error (see ResolveCloudInitSecrets).
type CloudInitSecretStores struct {
	// SSHKeys is name → value, from SOPS doc's cloud-init.ssh-keys.<name>.
	SSHKeys map[string]string
	// Passwords is name → value, from SOPS doc's cloud-init.passwords.<name>.
	Passwords map[string]string
}

// StoresFromClusterSecrets builds cloud-init secret stores from one
// cluster's resolved SOPS material.
func StoresFromClusterSecrets(sc config.SopsClusterSecrets) CloudInitSecretStores {
	return CloudInitSecretStores{
		SSHKeys:     sc.CloudInitSSHKeys,
		Passwords:   sc.CloudInitPasswords,
	}
}

// ResolveCloudInitSecrets walks every resource in resources and:
//   - VM / TemplateVM: resolves CloudInitData.SSHKeyRefs to
//     CD.resolvedSshKeys and CloudInitData.CIPasswordRef to
//     CD.resolvedCIPassword.
//   - other kinds: no-op.
//
// Fail-closed rules:
//   - A manifest that declares SSHKeyRefs while the SOPS store is
//     empty (or does not carry ssh-keys) → error "cloud-init.ssh-keys.<name>:
//     no SOPS cloud-init material on the cluster; the cluster's
//     secrets-file must declare cloud-init.ssh-keys (or remove the ref)".
//   - A manifest that names a missing ssh-keys entry → error names the
//     reference.
//
// The resolved values stay in the unexported CD fields; they are
// NEVER serialised (YAML/JSON output omits them).
func ResolveCloudInitSecrets(resources []Resource, st CloudInitSecretStores) error {
	for _, r := range resources {
		switch v := r.(type) {
		case *VM:
			if err := resolveVMCloudInitSecrets(v, st); err != nil {
				return err
			}
		case *TemplateVM:
			// TemplateVM embeds VM. Resolve via the embedded
			// VM pointer so the SOPS material lands on the same
			// spec.cloud-init-data used for its Drift/ToCreateParams.
			if err := resolveVMCloudInitSecrets(&v.VM, st); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveVMCloudInitSecrets(v *VM, st CloudInitSecretStores) error {
	cd := &v.Spec.CloudInitData

	// SSH key refs (plural).
	if len(cd.SSHKeyRefs) > 0 {
		if len(st.SSHKeys) == 0 {
			return fmt.Errorf("%s: spec.cloud-init-data.ssh-key-refs declares SOPS references but cluster has no SOPS cloud-init.ssh-keys material; add cloud-init.ssh-keys.<name> entries to the cluster's secrets-file (or remove the refs from the manifest)", v.Ref())
		}
		out := make([]string, 0, len(cd.SSHKeyRefs))
		seen := map[string]bool{}
		for i, ref := range cd.SSHKeyRefs {
			name, ok := strings.CutPrefix(ref, CloudInitSSHRefPrefix)
			if !ok {
				return fmt.Errorf("%s: ssh-key-ref[%d] %q: malformed (want %q+name)", v.Ref(), i, ref, CloudInitSSHRefPrefix)
			}
			val, present := st.SSHKeys[name]
			if !present || trimSpaces(val) == "" {
				return fmt.Errorf("%s: ssh-key-ref %q references cloud-init.ssh-keys.%s which is missing or empty in the cluster's SOPS document; proxops fails closed rather than treating a missing ref as 'no keys'", v.Ref(), ref, name)
			}
			if seen[trimSpaces(val)] {
				// duplicate key material in refs → proxops treats as
				// no-op (dedup at wire render is already handled by
				// cloudInitSSHKeysWire's set semantics)
				continue
			}
			seen[trimSpaces(val)] = true
			out = append(out, trimSpaces(val))
		}
		if len(out) == 0 {
			// every ref was a duplicate / empty — fail closed: the
			// manifest intended to declare keys, got nothing.
			return fmt.Errorf("%s: ssh-key-refs resolve to zero keys (every named entry is missing or a duplicate); proxops fails closed", v.Ref())
		}
		cd.resolvedSshKeys = out
	}

	// Password ref (singular).
	if cd.CIPasswordRef != "" {
		if len(st.Passwords) == 0 {
			return fmt.Errorf("%s: spec.cloud-init-data.ci-password-ref %q declares a SOPS reference but cluster has no SOPS cloud-init.passwords material; add cloud-init.passwords.<name> entries (or remove the ref)", v.Ref(), cd.CIPasswordRef)
		}
		name, ok := strings.CutPrefix(cd.CIPasswordRef, CloudInitPasswordRefPrefix)
		if !ok {
			return fmt.Errorf("%s: ci-password-ref %q malformed (want %q+name)", v.Ref(), cd.CIPasswordRef, CloudInitPasswordRefPrefix)
		}
		val, present := st.Passwords[name]
		if !present || trimSpaces(val) == "" {
			return fmt.Errorf("%s: ci-password-ref %q references cloud-init.passwords.%s which is missing or empty in the cluster's SOPS document; proxops fails closed rather than sending no password", v.Ref(), cd.CIPasswordRef, name)
		}
		cd.resolvedCIPassword = trimSpaces(val)
	}
	return nil
}

func trimSpaces(s string) string {
	return strings.TrimSpace(s)
}
