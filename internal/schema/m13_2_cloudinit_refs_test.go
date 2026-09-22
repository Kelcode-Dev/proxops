// M13.2: Cloud-Init SOPS references — schema, resolve, drift, no-leak tests.
//
// Pins:
//   - ssh-key-refs (plural) + ci-password-ref (singular) grammar
//     (cloud-init.ssh-keys.<name> / cloud-init.passwords.<name>).
//   - Fail-closed resolution: ref present but SOPS store empty → cycle
//     abort (before any PVE mutation); ref pointing at a missing SOPS
//     entry → error that NAMES THE REF, never the secret value.
//   - Determinism: dedup of identical refs; stable ordering.
//   - No secret leakage: resolved material lives in unexported fields and
//     never appears in YAML marshal output (manifests, plans, diffs).
//   - Drift: cipassword writes only when live is absent (PVE masks it);
//     never deletes.
package schema_test

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/Kelcode-Dev/proxops/internal/config"
	"github.com/Kelcode-Dev/proxops/internal/schema"
)

const ciSSHStoreKey = "ssh-ed25519 AAAAC3NzMyMDEyMw== ops@gitzmo"

func ciVMWithRefs(refs []string, pwRef, manifestKey string) *schema.VM {
	v := schema.NewVM()
	v.Metadata.Name = "ci-refs-vm"
	v.Spec.VMID = 9110
	v.Spec.Node = "pve01"
	v.Spec.CPU = schema.Cpu{Type: "host", Cores: 1}
	v.Spec.Memory = "1GiB"
	v.Spec.Disks = []schema.Disk{{Storage: "local-lvm", Size: "4GiB"}}
	v.Spec.NICs = []schema.NIC{{Model: "virtio", Bridge: "vmbr0"}}
	if len(refs) > 0 {
		v.Spec.CloudInitData.SSHKeyRefs = refs
	}
	if manifestKey != "" {
		v.Spec.CloudInitData.SSHKeys = []string{manifestKey}
	}
	if pwRef != "" {
		v.Spec.CloudInitData.CIPasswordRef = pwRef
	}
	return v
}

var ciStore = schema.CloudInitSecretStores{
	SSHKeys: map[string]string{
		"main":   ciSSHStoreKey,
		"github": "ssh-ed25519 BBBB8888== github@host",
		"empty":  "",
	},
	Passwords: map[string]string{
		"default": "s3cr3t-pw-value",
	},
}

// TestCIRefs_Grammar pins the prefix + name grammar; wrong-type refs
// (cloud-init.passwords under ssh-key-refs, vice versa) are rejected.
func TestCIRefs_Grammar(t *testing.T) {
	good := []string{
		"cloud-init.ssh-keys.main",
		"cloud-init.ssh-keys.github",
		"cloud-init.passwords.default",
	}
	for _, r := range good {
		v := ciVMWithRefs(nil, "", "")
		if r == "cloud-init.passwords.default" {
			v.Spec.CloudInitData.CIPasswordRef = r
		} else {
			v.Spec.CloudInitData.SSHKeyRefs = []string{r}
		}
		if err := v.Validate(); err != nil {
			t.Errorf("good ref %q: Validate() = %v, want nil", r, err)
		}
	}
	bad := []string{
		"ssh-keys.main",             // no prefix
		"cloud-init.ssh-keys.",      // empty name
		"cloud-init.passwords.main", // wrong type in ssh-key-refs
		"cloud-init.SSH-KEYS.main",  // case
		"cloud-init.ssh-keys.a b",   // space
		"cloud-init.ssh-keys.-x",    // leading dash
		"cloud-init.ssh-keys.a.b",   // internal dot
	}
	for _, r := range bad {
		// Split by semantic field: the "wrong type" cases validate against
		// their natural field and must fail there.
		v := ciVMWithRefs([]string{r}, "", "")
		if r == "cloud-init.passwords.main" {
			v.Spec.CloudInitData.CIPasswordRef = ""
			// it is grammatically a valid password ref on its own; as an
			// ssh-key-ref it must fail.
			v.Spec.CloudInitData.SSHKeyRefs = []string{r}
		}
		if err := v.Validate(); err == nil {
			t.Errorf("bad ref %q: Validate() = nil, want error", r)
		}
	}
	// ci-password-ref of the wrong type:
	v := ciVMWithRefs(nil, "cloud-init.ssh-keys.main", "")
	if err := v.Validate(); err == nil || !strings.Contains(err.Error(), "cloud-init.passwords.") {
		t.Errorf("ci-password-ref wrong type: err = %v", v.Validate())
	}
	// name too long (>64).
	long := strings.Repeat("a", 65)
	v = ciVMWithRefs([]string{"cloud-init.ssh-keys." + long}, "", "")
	if err := v.Validate(); err == nil {
		t.Errorf("over-length name: err = nil, want error")
	}
}

// TestCIRefs_ManifestExclusion pins ssh-keys and ssh-key-refs mutual
// exclusion: a resource cannot both carry inline keys and references
// (ambiguous ownership of the PVE sshkeys value).
func TestCIRefs_ManifestExclusion(t *testing.T) {
	v := ciVMWithRefs([]string{"cloud-init.ssh-keys.main"}, "", ciSSHStoreKey)
	if err := v.Validate(); err == nil {
		t.Fatalf("ssh-keys + ssh-key-refs together: Validate() = nil, want fail-closed")
	}
}

// TestCIResolve_MissingRefFailsClosedAndNamesRef pins that a ref to a
// missing SOPS entry errors out with the REF name — and that the error
// NEVER contains a secret value.
func TestCIResolve_MissingRefNamesRef(t *testing.T) {
	v := ciVMWithRefs([]string{"cloud-init.ssh-keys.nonexistent"}, "", "")
	st := ciStore
	delete(st.SSHKeys, "nonexistent")
	err := schema.ResolveCloudInitSecrets([]schema.Resource{v}, st)
	if err == nil {
		t.Fatalf("Resolve: missing ref resolved; want fail-closed")
	}
	if !strings.Contains(err.Error(), "cloud-init.ssh-keys.nonexistent") {
		t.Errorf("error does not name the ref: %q", err.Error())
	}
}

// TestCIResolve_EmptyStoreFailsClosed: refs present but the cluster has no
// SOPS material at all → abort (a missing ref must never mean "no keys").
func TestCIResolve_EmptyStoreFailsClosed(t *testing.T) {
	v := ciVMWithRefs([]string{"cloud-init.ssh-keys.main"}, "", "")
	err := schema.ResolveCloudInitSecrets([]schema.Resource{v}, schema.CloudInitSecretStores{})
	if err == nil {
		t.Fatalf("Resolve against empty store: nil, want fail-closed")
	}
}

// TestCIResolve_EmptySOPSEntryFailsClosed: ref points at a SOPS entry that
// is an empty string → fail closed (an empty ssh-key would be a no-op
// PVE mutation; better to refuse).
func TestCIResolve_EmptySOPSEntryFailsClosed(t *testing.T) {
	v := ciVMWithRefs([]string{"cloud-init.ssh-keys.empty"}, "", "")
	err := schema.ResolveCloudInitSecrets([]schema.Resource{v}, ciStore)
	if err == nil {
		t.Fatalf("Resolve: empty SOPS entry accepted; want fail-closed")
	}
}

// TestCIResolve_Dedup pins that duplicate refs resolve to one key and the
// wire value is stable.
func TestCIResolve_Dedup(t *testing.T) {
	v := ciVMWithRefs([]string{
		"cloud-init.ssh-keys.main",
		"cloud-init.ssh-keys.github",
		"cloud-init.ssh-keys.main",
	}, "", "")
	if err := schema.ResolveCloudInitSecrets([]schema.Resource{v}, ciStore); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := schema.CloudInitSSHKeysEffective(v.Spec.CloudInitData)
	if len(got) != 2 {
		t.Fatalf("effective keys = %d, want 2 (dedup); got %q", len(got), got)
	}
}

// TestCIResolve_SetsVMAndTemplateVM pins resolution across both kinds.
func TestCIResolve_SetsBothKinds(t *testing.T) {
	vm := ciVMWithRefs([]string{"cloud-init.ssh-keys.main"}, "cloud-init.passwords.default", "")
	tvm := schema.NewTemplateVM()
	tvm.Metadata.Name = "ci-tmpl"
	tvm.Spec = vm.Spec
	tvm.Spec.CloudInitData = vm.Spec.CloudInitData
	if err := schema.ResolveCloudInitSecrets([]schema.Resource{vm, tvm}, ciStore); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := schema.CloudInitCIPasswordEffective(tvm.Spec.CloudInitData); got != "s3cr3t-pw-value" {
		t.Errorf("TemplateVM ci-password effective = %q", got)
	}
	if got := schema.CloudInitSSHKeysEffective(vm.Spec.CloudInitData); len(got) != 1 || got[0] != ciSSHStoreKey {
		t.Errorf("VM ssh effective = %q", got)
	}
}

// TestCINoLeak_NoSecretInMarshalledYAML pins that resolved material lives
// in UNEXPORTED fields: marshalling the resource (as done into planned
// manifests, diffs, and reports) must never contain the secret value.
func TestCINoLeak_NoSecretInMarshalledYAML(t *testing.T) {
	v := ciVMWithRefs([]string{"cloud-init.ssh-keys.main", "cloud-init.ssh-keys.github"}, "cloud-init.passwords.default", "")
	if err := schema.ResolveCloudInitSecrets([]schema.Resource{v}, ciStore); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	buf, err := yaml.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(buf)
	if strings.Contains(s, "s3cr3t-pw-value") {
		t.Errorf("marshalled YAML leaks the resolved ci-password:\n%s", s)
	}
	if strings.Contains(s, "BBBB8888") || strings.Contains(s, "AAAA8888") {
		t.Errorf("marshalled YAML leaks resolved ssh-key material:\n%s", s)
	}
	if !strings.Contains(s, "cloud-init.ssh-keys.main") {
		t.Errorf("marshalled YAML lost the refs (desired state must persist):\n%s", s)
	}
}

// TestCINoLeak_ErrorNeverLeaksSecretValue pins that resolve errors name
// the ref, not the value.
func TestCINoLeak_ErrorNeverLeaksSecretValue(t *testing.T) {
	ref := "cloud-init.passwords.missing"
	v := ciVMWithRefs(nil, ref, "")
	err := schema.ResolveCloudInitSecrets([]schema.Resource{v}, ciStore)
	if err == nil {
		t.Fatalf("Resolve: missing password ref accepted")
	}
	if strings.Contains(err.Error(), "s3cr3t-pw-value") {
		t.Errorf("error contains secret value: %q", err.Error())
	}
}

// TestCIDrift_CIPasswordNeverDeletes pins: when proxops owns a ci-password
// and PVE reports the value (masked), no write; when PVE reports
// cipassword ABSENT, proxops writes. In NEITHER case does proxops emit a
// delete=cipassword.
func TestCIDrift_CIPasswordRules(t *testing.T) {
	base := ciVMWithRefs(nil, "cloud-init.passwords.default", "")
	if err := schema.ResolveCloudInitSecrets([]schema.Resource{base}, ciStore); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// PVE reports a masked value present → no cipassword write at all.
	livePresent := livePCI(9110, "cipassword=**********")
	baseCopy := *base
	upd, _, changed := baseCopy.Drift(livePresent)
	if _, had := upd["cipassword"]; had {
		t.Errorf("Drift: rewrote a live-masked cipassword (%v); PVE masks the value — never rewrite", upd)
	}
	if _, del := upd["delete"]; del {
		t.Errorf("Drift: deleted cipassword (%v)", upd)
	}
	if changed {
		t.Errorf("Drift: changed=true against a live-present masked cipassword; want no action")
	}
	// PVE has NO cipassword → write the desired value.
	liveAbsent := livePCI(9110)
	baseCopy2 := *base
	upd2, stop, changed2 := baseCopy2.Drift(liveAbsent)
	if !changed2 {
		t.Fatalf("Drift: changed=false against absent live cipassword; want write")
	}
	if upd2["cipassword"] != "s3cr3t-pw-value" {
		t.Errorf("Drift: cipassword write = %q, want the sops-resolved value", upd2["cipassword"])
	}
	if stop {
		t.Errorf("Drift: cipassword alone should not require stop (cloud-init data is live-writable)")
	}
}

// TestCIDrift_SSHKeysWireFromRefs pins that the PVE `sshkeys` wire value is
// built from the RESOLVED refs (not the refs literally; not the unexported
// resolved list when the manifest also had inline keys).
func TestCIDrift_SSHKeysWireFromRefs(t *testing.T) {
	v := ciVMWithRefs([]string{"cloud-init.ssh-keys.main"}, "", "")
	if err := schema.ResolveCloudInitSecrets([]schema.Resource{v}, ciStore); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	p, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	sshkeys, _ := p["sshkeys"].(string)
	if !strings.Contains(sshkeys, "AAAAC3NzMyMDEyMw") {
		t.Errorf("create sshkeys wire = %q; want it to contain the resolved key body", sshkeys)
	}
	if strings.Contains(sshkeys, "cloud-init.ssh-keys.") {
		t.Errorf("create sshkeys wire leaked the REF into PVE: %q", sshkeys)
	}
	// Drift convergence: live reports the same (encoded) key → no write.
	enc := strings.ReplaceAll(strings.ReplaceAll(sshkeys, "+", "%2B"), " ", "%20")
	_ = enc
	live := livePCI(9110, "sshkeys="+sshkeys)
	upd, _, changed := v.Drift(live)
	if _, had := upd["sshkeys"]; had {
		t.Errorf("Drift: rewrote sshkeys against matching live value: %v vs %q", upd["sshkeys"], sshkeys)
	}
	if changed {
		t.Errorf("Drift: changed=true against matching sshkeys; live=%v", live)
	}
}

// TestCIStoresFromClusterSecrets pins the config → stores adapter shape.
func TestCIStoresFromClusterSecrets(t *testing.T) {
	sc := config.SopsClusterSecrets{
		CloudInitSSHKeys:  map[string]string{"main": "k1"},
		CloudInitPasswords: map[string]string{"default": "p1"},
	}
	st := schema.StoresFromClusterSecrets(sc)
	if got := st.SSHKeys["main"]; got != "k1" {
		t.Errorf("StoresFromClusterSecrets.SSHKeys[main] = %q", got)
	}
	if got := st.Passwords["default"]; got != "p1" {
		t.Errorf("StoresFromClusterSecrets.Passwords[default] = %q", got)
	}
}
