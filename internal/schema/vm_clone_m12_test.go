// M12: clone-backed VM provisioning — schema surface.
//
// Pins the confirmed PVE 9.2.2 clone behaviour (probed on conformance-dev
// 2026-09-15, template VM 9160 → full clone 9165):
//   - a full clone copies the template's cloud-init DATA block verbatim
//     (ciuser / sshkeys / ipconfig<N> / nameserver / searchdomain);
//   - proxops must therefore OVERWRITE the keys the VM declares and CLEAR
//     the ones it does not, via PVE's `delete=` form-value;
//   - one /config request cannot both set and delete the SAME key (400),
//     so the set map and the delete list are disjoint by construction;
//   - deleting an ABSENT key is tolerated (so the delete list may be
//     derived from the manifest alone — deterministic).
package schema

import (
	"strings"
	"testing"
)

func cloneTestTemplate() *TemplateVM {
	t := NewTemplateVM()
	t.Metadata.Name = "tpl-m12"
	t.Spec.Node = "pve01"
	t.Spec.VMID = 900
	t.Spec.Memory = "1GiB"
	t.Spec.CPU = Cpu{Type: "host", Cores: 1}
	t.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB", Slot: "scsi0"}}
	t.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	t.Spec.Hardware.CloudInit = CloudInit{Enabled: true, Storage: "local-lvm", Size: "4M"}
	t.Spec.CloudInitData = CloudInitData{
		CIUser:        "tpluser",
		SSHKeys:       []string{"ssh-ed25519 AAAAcloneprobe tpl"},
		Nameservers:   []string{"1.1.1.1"},
		SearchDomains: []string{"tpl.example"},
		IPConfigs:     []CloudInitIPConfig{{NIC: 0, IP: "192.168.192.240/18", Gateway: "192.168.192.5"}},
	}
	return t
}

func cloneTestVM(name string, vmid int) *VM {
	v := NewVM()
	v.Metadata.Name = name
	v.Spec.Node = "pve01"
	v.Spec.VMID = vmid
	v.Spec.Clone = "tpl-m12"
	v.Spec.Memory = "2GiB"
	v.Spec.CPU = Cpu{Type: "host", Cores: 2}
	v.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	v.Spec.Hardware.CloudInit = CloudInit{Enabled: true, Storage: "local-lvm", Size: "4M"}
	return v
}

// TestClone_ValidateRejectsDisks: a clone-backed VM must not declare disks.
func TestClone_ValidateRejectsDisks(t *testing.T) {
	v := cloneTestVM("vm-disks", 901)
	v.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB"}}
	err := v.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.disks must be empty when spec.clone is set") {
		t.Fatalf("Validate error = %v, want clone-disks rejection", err)
	}
}

// TestClone_ValidateAllowsNoDisks: the clone-backed shape validates with an
// empty disk list (the template's layout is inherited).
func TestClone_ValidateAllowsNoDisks(t *testing.T) {
	v := cloneTestVM("vm-ok", 902)
	if err := v.Validate(); err != nil {
		t.Fatalf("clone-backed VM Validate: %v", err)
	}
}

// TestClone_ValidateRejectsSelfReference pins the self-clone guard.
func TestClone_ValidateRejectsSelfReference(t *testing.T) {
	v := cloneTestVM("tpl-m12", 903) // clone name == own name
	err := v.Validate()
	if err == nil || !strings.Contains(err.Error(), "references itself") {
		t.Fatalf("Validate error = %v, want self-reference rejection", err)
	}
}

// TestClone_TemplateVMRejectsClone: a TemplateVM is a clone source, never a
// target.
func TestClone_TemplateVMRejectsClone(t *testing.T) {
	tv := cloneTestTemplate()
	tv.Spec.Clone = "tpl-m12"
	err := tv.Validate()
	if err == nil || !strings.Contains(err.Error(), "spec.clone is not allowed on a TemplateVM") {
		t.Fatalf("TemplateVM Validate error = %v, want clone rejection", err)
	}
}

// TestClone_DepsEdge: a clone-backed VM creates the structured VM→TemplateVM
// edge (planner ordering + executor deferral depend on it).
func TestClone_DepsEdge(t *testing.T) {
	v := cloneTestVM("vm-dep", 904)
	deps := v.Deps()
	want := Ref{Kind: KindTemplateVM, Name: "tpl-m12"}
	found := false
	for _, d := range deps {
		if d.Equal(want) {
			found = true
		}
	}
	if !found {
		t.Fatalf("VM.Deps() = %v, want the TemplateVM/tpl-m12 edge", deps)
	}
}

// TestClone_ResolveFailsClosed: unknown / wrong-kind / off-node clone refs
// must fail resolution (the cycle aborts before any PVE call).
func TestClone_ResolveFailsClosed(t *testing.T) {
	v := cloneTestVM("vm-ghost", 905)
	if err := ResolveArtifactRefs([]Resource{v}); err == nil ||
		!strings.Contains(err.Error(), "unknown TemplateVM") {
		t.Fatalf("ResolveArtifactRefs unknown-ref error = %v, want fail-closed", err)
	}

	// Wrong kind: a VM named like the clone target does not satisfy a
	// TemplateVM reference (the lookup is kind-qualified, so it fails closed
	// as an unknown TemplateVM).
	other := cloneTestVM("vm-other", 906)
	other.Spec.Clone = "vm-target"
	target := cloneTestVM("vm-target", 907)
	if err := ResolveArtifactRefs([]Resource{other, target}); err == nil ||
		!strings.Contains(err.Error(), "unknown TemplateVM") {
		t.Fatalf("ResolveArtifactRefs wrong-kind error = %v, want fail-closed", err)
	}

	// Off-node template.
	tv := cloneTestTemplate()
	tv.Spec.Node = "pve02"
	v2 := cloneTestVM("vm-offnode", 908)
	if err := ResolveArtifactRefs([]Resource{tv, v2}); err == nil ||
		!strings.Contains(err.Error(), "not placed on node") {
		t.Fatalf("ResolveArtifactRefs off-node error = %v, want fail-closed", err)
	}

	// Happy path resolves the template's PINNED vmid (never a manifest value).
	tv2 := cloneTestTemplate()
	v3 := cloneTestVM("vm-good", 909)
	if err := ResolveArtifactRefs([]Resource{tv2, v3}); err != nil {
		t.Fatalf("ResolveArtifactRefs happy path: %v", err)
	}
	if v3.CloneSourceID() != 900 {
		t.Fatalf("CloneSourceID = %d, want 900 (the TemplateVM's spec.vmid)", v3.CloneSourceID())
	}
}

// TestClone_ConfigParams: the post-clone write applies the VM's own values
// and clears the inherited identity keys it does not own. The set map and
// the delete list MUST be disjoint (PVE 400s on overlap — probe-verified).
func TestClone_ConfigParams(t *testing.T) {
	tv := cloneTestTemplate()
	v := cloneTestVM("vm-params", 910)
	// The VM declares its OWN cloud-init user + static IP, and nothing else.
	v.Spec.CloudInitData = CloudInitData{
		CIUser:    "vmuser",
		IPConfigs: []CloudInitIPConfig{{NIC: 0, IP: "192.168.192.250/18", Gateway: "192.168.192.5"}},
	}
	if err := ResolveArtifactRefs([]Resource{tv, v}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	params, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	set := CloneConfigSet(params)
	del := v.CloneDeleteKeys(params)

	// The set map carries the VM's own identity + hardware values.
	if set["ciuser"] != "vmuser" {
		t.Errorf("set[ciuser] = %v, want vmuser", set["ciuser"])
	}
	if set["ipconfig0"] != "ip=192.168.192.250/18,gw=192.168.192.5" {
		t.Errorf("set[ipconfig0] = %v", set["ipconfig0"])
	}
	if set["memory"] != int64(2048) {
		t.Errorf("set[memory] = %v, want 2048", set["memory"])
	}
	// vmid/start/disk slots never ride the clone config write.
	if _, ok := set["vmid"]; ok {
		t.Errorf("set must not carry vmid")
	}
	if _, ok := set["start"]; ok {
		t.Errorf("set must not carry start (PVE rejects it on clone; power is a separate step)")
	}
	for k := range set {
		if isDiskSlot(k) {
			t.Errorf("set must not carry disk slot %q", k)
		}
	}

	// The delete list clears exactly the inherited identity keys the VM does
	// not own — and never overlaps the set map.
	wantDel := map[string]bool{"sshkeys": true, "nameserver": true, "searchdomain": true, "cipassword": true, "cicustom": true}
	got := map[string]bool{}
	for _, k := range del {
		got[k] = true
	}
	for k := range wantDel {
		if !got[k] {
			t.Errorf("delete list missing %q; got %v", k, del)
		}
	}
	for _, k := range []string{"ciuser", "ipconfig0"} {
		if got[k] {
			t.Errorf("delete list must NOT contain owned key %q (PVE 400s on set+delete overlap)", k)
		}
	}
	// Determinism: sorted.
	for i := 1; i < len(del); i++ {
		if del[i-1] > del[i] {
			t.Fatalf("delete list not sorted: %v", del)
		}
	}
}

// TestClone_ConfigParamsNoCloudInit: a clone-backed VM that declares NO
// cloud-init data must clear the whole inherited identity block — the
// "never boot with the template's hostname/IP/keys" guarantee.
func TestClone_ConfigParamsNoCloudInit(t *testing.T) {
	tv := cloneTestTemplate()
	v := cloneTestVM("vm-bare", 911)
	v.Spec.Hardware.CloudInit = CloudInit{}
	if err := ResolveArtifactRefs([]Resource{tv, v}); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	params, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	del := v.CloneDeleteKeys(params)
	for _, k := range []string{"ciuser", "sshkeys", "nameserver", "searchdomain", "cipassword", "cicustom", "ipconfig0"} {
		found := false
		for _, d := range del {
			if d == k {
				found = true
			}
		}
		if !found {
			t.Errorf("bare clone: delete list missing %q; got %v", k, del)
		}
	}
}

// TestClone_DriveNotRewritten: the cloud-init DRIVE is a storage-backed
// volume the clone inherits from the template. Re-sending the create form
// over the clone's live volume makes PVE lvcreate a volume that already
// exists and the TASK FAILS (probe-verified on conformance-dev 2026-09-15:
// "lvcreate 'pve/vm-9161-cloudinit' error: Logical Volume ... already
// exists"). The post-clone write must therefore omit ide2 when the clone
// already carries a live cloud-init volume — and must still write it when
// the slot is empty (a template without a drive).
func TestClone_DriveNotRewritten(t *testing.T) {
	v := cloneTestVM("vm-drive", 915)
	v.Spec.CloudInitData.CIUser = "vmuser"
	params, err := v.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if _, ok := params["ide2"]; !ok {
		t.Fatal("precondition: create params must carry the cloud-init drive")
	}

	// Clone inherited a live cloud-init volume → the write must NOT restate it.
	live := map[string]any{"ide2": "local-lvm:vm-915-cloudinit,media=cdrom,size=4M"}
	w := CloneConfigWrite(params, live)
	if _, ok := w["ide2"]; ok {
		t.Errorf("post-clone write restates ide2 over a live cloud-init volume — lvcreate-already-exists task failure")
	}
	// The cloud-init DATA still rides the write.
	if _, ok := w["ciuser"]; !ok {
		t.Errorf("post-clone write must still carry ciuser")
	}

	// Template carried NO cloud-init drive → the slot is empty and writing
	// it is safe (and keeps the first cycle convergent).
	w2 := CloneConfigWrite(params, map[string]any{"ide2": "none"})
	if got := w2["ide2"]; got != "local-lvm:cloudinit,size=4M" {
		t.Errorf("empty-slot clone write ide2 = %v, want the create-form drive", got)
	}

	// Drift on a clone with an inherited drive must not emit an ide2 write.
	v2 := cloneTestVM("vm-drive-drift", 916)
	upd, _, _ := v2.Drift(map[string]any{
		"ide2":   "local-lvm:vm-916-cloudinit,media=cdrom,size=4M",
		"memory": "2048", "cpu": "host", "cores": float64(2),
		"net0": "virtio=52:54:00:00:00:0F,bridge=vmbr0",
		"tags": []any{PveOwnershipTag},
	})
	if _, ok := upd["ide2"]; ok {
		t.Errorf("clone Drift emitted an ide2 write: %v", upd)
	}
}

// TestClone_DrivePoolMismatchIsAnomaly: a clone whose inherited cloud-init
// drive lives on a different pool than the manifest asks for must surface a
// non-destructive anomaly (moving a live volume is a storage migration, not
// a config write).
func TestClone_DrivePoolMismatchIsAnomaly(t *testing.T) {
	v := cloneTestVM("vm-pool", 917)
	v.Spec.Hardware.CloudInit.Storage = "vm_disks"
	an := v.DriftAnomalies(map[string]any{
		"ide2":   "local-lvm:vm-917-cloudinit,media=cdrom,size=4M",
		"memory": "2048", "cpu": "host", "cores": float64(2),
	})
	found := false
	for _, s := range an {
		if strings.Contains(s, "cloud-init drive lives on pool") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cloud-init drive pool anomaly; got %v", an)
	}
}

// TestClone_DriftAnomaliesSuppressesDiskNoise: a converged clone's inherited
// disks are EXPECTED live state — they must not surface as live-only
// anomalies on every cycle.
func TestClone_DriftAnomaliesSuppressesDiskNoise(t *testing.T) {
	v := cloneTestVM("vm-anom", 912)
	live := map[string]any{
		"scsi0": "local-lvm:vm-912-disk-0,size=8G",
		"net0":  "virtio=52:54:00:00:00:0C,bridge=vmbr0",
		"memory": "2048",
		"cpu":    "host",
		"cores":  float64(2),
	}
	if an := v.DriftAnomalies(live); len(an) != 0 {
		t.Fatalf("clone DriftAnomalies = %v, want none (inherited disks are expected)", an)
	}
	// A NON-clone VM keeps the live-only-disk anomaly behaviour.
	plain := NewVM()
	plain.Metadata.Name = "vm-plain"
	plain.Spec.Node = "pve01"
	plain.Spec.VMID = 913
	plain.Spec.Memory = "2GiB"
	plain.Spec.CPU = Cpu{Type: "host", Cores: 2}
	plain.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "8GiB", Slot: "scsi1"}}
	plain.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	an := plain.DriftAnomalies(live)
	if len(an) == 0 || !strings.Contains(an[0], "live-only disk slot scsi0") {
		t.Fatalf("non-clone DriftAnomalies = %v, want the scsi0 live-only anomaly", an)
	}
}

// TestClone_DriftNoDiskWrites: a clone-backed VM's Drift must never emit a
// disk-slot write (the clone owns its volumes; re-stating a create-form disk
// over a live volume is the data-loss shape).
func TestClone_DriftNoDiskWrites(t *testing.T) {
	v := cloneTestVM("vm-drift", 914)
	live := map[string]any{
		"scsi0":  "local-lvm:vm-914-disk-0,size=8G",
		"ide2":   "local-lvm:vm-914-cloudinit,media=cdrom,size=4M",
		"net0":   "virtio=52:54:00:00:00:0E,bridge=vmbr0",
		"memory": "2048",
		"cpu":    "host",
		"cores":  float64(2),
		"tags":   []any{PveOwnershipTag},
		"name":   "vm-drift",
		// A CONVERGED clone: the create-time write already cleared the
		// template's undeclared identity keys, so they are absent here.
		// (A live ciuser/sshkeys on an undeclared clone is NOT converged —
		// Drift must emit delete= for them; pinned by
		// TestClone_DriftClearsLeakedIdentity.)
	}
	upd, stop, changed := v.Drift(live)
	if changed {
		t.Fatalf("converged clone Drift changed = %v (upd=%v); want no-op", changed, upd)
	}
	if stop {
		t.Errorf("converged clone Drift stop = true, want false")
	}
	for k := range upd {
		if isDiskSlot(k) {
			t.Fatalf("clone Drift emitted disk write %q — data-loss shape", k)
		}
	}
}

// TestClone_DriftClearsLeakedIdentity: a clone whose live config still
// carries the template's undeclared identity (the half-configured shape a
// failed clone-create leaves behind) must be REPAIRED through the normal
// update path — the "never keep the template's identity" guarantee is
// convergent, not create-time-only.
func TestClone_DriftClearsLeakedIdentity(t *testing.T) {
	v := cloneTestVM("vm-leak", 918)
	v.Spec.CloudInitData.CIUser = "vmuser" // declared → overwritten, not cleared
	live := map[string]any{
		"ide2":      "local-lvm:vm-918-cloudinit,media=cdrom,size=4M",
		"net0":      "virtio=52:54:00:00:00:10,bridge=vmbr0",
		"memory":    "2048",
		"cpu":       "host",
		"cores":     float64(2),
		"tags":      []any{PveOwnershipTag},
		"name":      "vm-leak",
		"ciuser":    "vmuser",
		"sshkeys":   "ssh-ed25519%20AAAA%20tpl",
		"ipconfig0": "ip=192.168.192.240/18,gw=192.168.192.5",
	}
	upd, _, changed := v.Drift(live)
	if !changed {
		t.Fatal("leaked identity must register as drift")
	}
	dl, _ := upd["delete"].(string)
	if !strings.Contains(dl, "sshkeys") || !strings.Contains(dl, "ipconfig0") {
		t.Errorf("delete list = %q, want sshkeys + ipconfig0 cleared", dl)
	}
	if strings.Contains(dl, "ciuser") {
		t.Errorf("delete list must not clear ciuser (the VM owns it): %q", dl)
	}
	// Converged after the clear: no delete, no change.
	clean := map[string]any{}
	for k, val := range live {
		clean[k] = val
	}
	delete(clean, "sshkeys")
	delete(clean, "ipconfig0")
	if _, _, changed := v.Drift(clean); changed {
		t.Fatal("converged clone must not drift")
	}
}
