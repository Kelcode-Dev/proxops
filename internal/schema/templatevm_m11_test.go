package schema

import (
	"strings"
	"testing"
)

// baseTemplateVM returns a minimal valid TemplateVM that will pass
// Validate() out of the box: pinned vmid, q35/ovmf hardware, scsi root disk on
// local-lvm with iothread, one virtio NIC on vmbr0, memory 2GiB, cores 1,
// agent+onboot on, boot-order scsi0.
func baseTemplateVM(name string, vmid int) *TemplateVM {
	t := NewTemplateVM()
	t.Metadata.Name = name
	t.Spec.Node = "pve01"
	t.Spec.VMID = vmid
	t.Spec.Memory = "2GiB"
	t.Spec.CPU = Cpu{Type: "host", Cores: 1}
	t.Spec.Disks = []Disk{{Storage: "local-lvm", Size: "32GiB", Slot: "scsi0", IOThread: true, Controller: "virtio-scsi-single"}}
	t.Spec.NICs = []NIC{{Model: "virtio", Bridge: "vmbr0", Slot: "net0"}}
	t.Spec.Hardware.Machine = "q35"
	t.Spec.Hardware.BIOS = "ovmf"
	t.Spec.Hardware.EFIDisk = &EFIDisk{Storage: "local-lvm", Size: "4MiB", Template: "4m"}
	t.Spec.Hardware.Serial0 = "socket"
	t.Spec.Options.Agent = true
	t.Spec.Options.OnBoot = true
	t.Spec.Options.BootOrder = []string{"scsi0"}
	return t
}

// TestTemplateVM_StateMustBeStopped pins the M11 rule: a TemplateVM spec
// cannot declare spec.state = "started" - PVE refuses to start a template,
// so Validate() fails closed.
func TestTemplateVM_StateMustBeStopped(t *testing.T) {
	for _, bad := range []string{"started", "booted"} {
		tv := baseTemplateVM("tpl-badstate", 900)
		tv.Spec.State = bad
		err := tv.Validate()
		if err == nil {
			t.Errorf("Validate(spec.state=%q) = nil, want error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "stopped") {
			t.Errorf("err = %q; want mention of 'stopped'", err.Error())
		}
	}
	tv := baseTemplateVM("tpl-stopped", 999)
	tv.Spec.State = "stopped"
	if err := tv.Validate(); err != nil {
		t.Errorf("Validate(spec.state=stopped) = %v, want nil", err)
	}
}

// TestTemplateVM_DesiredStateAlwaysStopped pins that DesiredState() returns
// "stopped" no matter what the manifest's spec.state field says (after
// Validate() has already passed). This is a hard invariant: PVE refuses to
// start a template, so the planner's powerDesiredToLive will never emit a
// Start/Stop for a TemplateVM whose live PVE power is already "stopped".
func TestTemplateVM_DesiredStateAlwaysStopped(t *testing.T) {
	tv := baseTemplateVM("tpl-pinv", 910)
	if got := tv.DesiredState(); got != "stopped" {
		t.Errorf("DesiredState() = %q, want 'stopped'", got)
	}
}

// TestTemplateVM_VerbatimWireShape pins that a TemplateVM manifest yields
// byte-identical PVE wire form-values as a pveconform VM with the same spec.
// This is the M11 invariant: TemplateVM embeds VM, so the create form is
// identical; the only PVE-side difference is the follow-up POST
// /qemu/{id}/template (executed by the executor, not the schema).
func TestTemplateVM_VerbatimWireShape(t *testing.T) {
	tv := baseTemplateVM("tpl-shape", 920)
	tvVM := NewVM()
	tvVM.Metadata = tv.Metadata
	tvVM.Spec = tv.Spec

	tvParams, tvErr := tv.ToCreateParams()
	vmParams, vmErr := tvVM.ToCreateParams()
	if tvErr != nil {
		t.Fatalf("TemplateVM ToCreateParams: %v", tvErr)
	}
	if vmErr != nil {
		t.Fatalf("VM (mirror) ToCreateParams: %v", vmErr)
	}
	if len(tvParams) != len(vmParams) {
		t.Fatalf("TemplateVM params=%d, mirror-VM params=%d; want identical length", len(tvParams), len(vmParams))
	}
	for k, v := range tvParams {
		if vmParams[k] != v {
			t.Errorf("TemplateVM params[%q] = %v, mirror VM params[%q] = %v; want identical", k, v, k, vmParams[k])
		}
	}
}

// TestTemplateVM_RefReportsTemplateVMPins pins that TemplateVM.Ref()
// returns KindTemplateVM, not KindVM (the embedded VM's Ref would
// otherwise lie about the kind).
func TestTemplateVM_RefReportsTemplateVMPins(t *testing.T) {
	tv := baseTemplateVM("tpl-ref", 930)
	ref := tv.Ref()
	if ref.Kind != KindTemplateVM {
		t.Errorf("Ref().Kind = %q, want TemplateVM", ref.Kind)
	}
	if ref.Name != "tpl-ref" {
		t.Errorf("Ref().Name = %q, want 'tpl-ref'", ref.Name)
	}
}

// TestTemplateVM_DriftSharesVmSemantics pins that a TemplateVM's Drift
// behaves identically to a pveconform VM's Drift on the same wire shape
// (no special-casing of the PVE template flag in Drift - PVE's
// /qemu/{id}/config endpoint ignores the template flag on reads/writes).
// The "template" PVE key must NOT appear in Drift's updateParams.
func TestTemplateVM_DriftSharesVmSemantics(t *testing.T) {
	tv := baseTemplateVM("tpl-drift", 940)
	live := map[string]any{
		"vmid":     "940",
		"name":     "tpl-drift",
		"memory":   "4096",
		"cpu":      "host",
		"cores":    "2",
		"machine":  "q35",
		"bios":     "ovmf",
		"agent":    "1",
		"onboot":   "1",
		"boot":     "order=scsi0",
		"scsihw":   "virtio-scsi-single",
		"scsi0":    "local-lvm:vm-940-disk-0,iothread=1,size=32G",
		"net0":     "virtio=52:54:00:FF:00:00,bridge=vmbr0",
		"serial0":  "socket",
		"efidisk0": "local-lvm:vm-940-efidisk,efitype=4m,size=4M",
		"tags":     "pveconform",
		"template": "1",
		"digest":   "aa",
		"vmgenid":  "bb",
	}
	upd, _, changed := tv.Drift(live)
	// Expect changed=true (memory 4096 vs desired 2048 + cores 2 vs 1).
	if !changed {
		t.Fatalf("Drift: changed=false, want true; live=%v", live)
	}
	// The "template" PVE key MUST NOT be in updateParams: pveconform never
	// writes the template flag from Drift - it is a PVE-side, mark-template-
	// only token.
	if _, got := upd["template"]; got {
		t.Errorf("Drift updated 'template' = %v, want absent (pveconform never writes this)", upd["template"])
	}
	// Check that memory/cores are in the update.
	for _, k := range []string{"memory", "cores"} {
		if _, got := upd[k]; !got {
			t.Errorf("Drift missing update[%q]", k)
		}
	}
}

// TestTemplateVM_ParseRoundTripYAML pins that a minimal TemplateVM YAML
// document parses cleanly, validates, and round-trips its identity. This
// catches accidental yaml tag breakage on the VMSpec fields when embedded
// on TemplateVM.
func TestTemplateVM_ParseRoundTripYAML(t *testing.T) {
	const doc = `apiVersion: proxops/v1alpha1
kind: TemplateVM
metadata:
  name: tpl-roundtrip
spec:
  node: pve01
  vmid: 950
  memory: 1GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      size: 4GiB
      interface: scsi0
  networks:
    - model: virtio
      bridge: vmbr0
  state: stopped
`
	var tv TemplateVM
	if err := YAMLTo(doc, &tv); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	if tv.Kind != KindTemplateVM {
		t.Errorf("Kind = %q, want TemplateVM", tv.Kind)
	}
	if tv.Metadata.Name != "tpl-roundtrip" {
		t.Errorf("metadata.name = %q", tv.Metadata.Name)
	}
	if tv.Spec.State != "stopped" {
		t.Errorf("spec.state = %q, want stopped (explicit)", tv.Spec.State)
	}
	if tv.Spec.VMID != 950 {
		t.Errorf("spec.vmid = %d, want 950", tv.Spec.VMID)
	}
	if err := tv.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestTemplateVM_ParseAcceptsTVMandTemplateAliases pins the M11 ParseKind
// spellings: "TemplateVM", "TVM", and "TEMPLATE" are all accepted case-
// insensitively.
func TestTemplateVM_ParseAcceptsTVMandTemplateAliases(t *testing.T) {
	for _, s := range []string{"TemplateVM", "templatevm", "TVM", "tvm", "TEMPLATE", "template"} {
		got, err := ParseKind(s)
		if err != nil {
			t.Errorf("ParseKind(%q) = %v, want nil", s, err)
			continue
		}
		if got != KindTemplateVM {
			t.Errorf("ParseKind(%q) = %q, want TemplateVM", s, got)
		}
	}
}
