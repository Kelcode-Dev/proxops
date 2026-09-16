package schema

import (
	"fmt"
	"strings"
)

// TemplateVM is a PVE qemu object that has been promoted to a template
// (PVE `template=1`). It is a first-class proxops kind as of M11.
//
// Wire shape: identical to a proxops VM. A TemplateVM embeds a VM and
// inherits its spec surface, owned fields, Drift, ToCreateParams,
// DriftAnomalies and Deps. The differences are deliberately narrow:
//
//   - PVE's per-node "qm" id space is shared with VM, so a TemplateVM and a
//     VM cannot claim the same (node, vmid). proxops enforces this at
//     parse time as any other in-cluster id collision.
//   - PVE's /cluster/resources lists both with type="qm"; the distinguishing
//     token is the per-object /config "template" = "1".
//   - A PVE template CANNOT be started. proxops's TemplateVM DesiredState()
//     is always "stopped"; spec.state "started" fails at parse time.
//   - PVE 9.2 has no /qemu/{id}/untemplate endpoint (probe-verified
//     HTTP 501 "not implemented"). proxops therefore:
//       * creates a TemplateVM by POST /qemu (start=0) + POST /qemu/{id}/
//         template (the executor wraps the create),
//       * updates it via the standard POST /qemu/{id}/config (the PVE
//         template flag persists across config writes),
//       * and — when a proxops VM (kind=VM) is desired but PVE's live
//         object reports template=1 — surfaces a non-destructive anomaly
//         instead of attempting an untemplate.
//       * deletes it via REST DELETE /qemu/{id} (PVE accepts delete on a
//         template; the executor pre-stops as usual, already stopped).
//
// Adoption (reverse translation):
//
//   - proxops adopt emits a TemplateVM manifest for any PVE object that
//     reports template=1. M10's "skip + census" behaviour is REPLACED in
//     M11: the manifest is written under templatevm/<cluster>/, the
//     proxops ownership tag + every owned VM field is captured via the
//     same owned-field surface as a regular VM.
//   - PVE-side sshkeys are redacted to "ssh-keys: ["*"]" in the emitted
//     manifest (M10 PII-redaction contract generalized to any kind carrying
//     cloud-init data). cipassword / cicustom are never adopted (secret +
//     PVE-owned respectively; they stay in the gap report).
//   - The manifest's cloud-init-data.ssh-keys sentinel ("*") must be
//     replaced by the operator with their real public key(s) before first
//     apply. proxops's Drift treats the sentinel as "PVE owns live
//     sshkeys — do not write", so round-trips are stable.
//
// Dependency semantics: a TemplateVM's own outgoing edge is the inherited
// VM cdrom.iso edge (to an ISO). The reverse edge — a VM cloning this
// TemplateVM — is modelled on the VM side (VM.spec.clone → TemplateVM, M12):
// the clone is a full clone, so the VM inherits the template's disk layout
// and a clone-backed VM declares no spec.disks (the data-loss guarantee is
// preserved by never re-stating a disk over a cloned live volume). See
// docs/SCHEMA.md § "Provisioning a VM from a TemplateVM".

// TemplateVM is a schema.Resource for Kind=TemplateVM, embedding a VM to
// reuse the PVE wire surface. The ",inline" yaml tag is required so
// YAML-to-struct unmarshaling flattens the embedded VM's fields into the
// TemplateVM document (without it, yaml.v3 looks for a top-level "vm"
// key, which is not what M11 manifests carry — the proxops manifest
// for a TemplateVM is syntactically identical to a proxops VM
// manifest, just with kind: TemplateVM).
type TemplateVM struct {
	VM `yaml:",inline" json:",inline"`
}

// NewTemplateVM returns an empty TemplateVM.
func NewTemplateVM() *TemplateVM {
	t := &TemplateVM{}
	t.Kind = KindTemplateVM
	t.APIVersion = APIVersion
	return t
}

// Ref overrides the embedded VM.Ref to report Kind=TemplateVM.
func (t *TemplateVM) Ref() Ref { return Ref{Kind: t.Kind, Name: t.Metadata.Name} }

// DesiredState always returns "stopped": PVE templates cannot be started.
// The planner's powerDesiredToLive will therefore never emit a Start/Stop
// for a TemplateVM (a live template is always "stopped" and desired is
// "stopped").
func (t *TemplateVM) DesiredState() string { return "stopped" }

// Validate delegates to VM.Validate for the shared wire surface, then
// enforces the TemplateVM-only rules: state must be absent or "stopped",
// and spec.clone is not allowed (a template is a clone SOURCE, never a
// target). The clone check runs FIRST because the embedded VM.Validate's
// clone rules (disks must be empty when cloning) would otherwise mask the
// clearer TemplateVM-specific message.
func (t *TemplateVM) Validate() error {
	if strings.TrimSpace(t.Spec.Clone) != "" {
		return fmt.Errorf("%s: spec.clone is not allowed on a TemplateVM (templates are clone sources, not clone targets)", t.Ref())
	}
	if err := t.VM.Validate(); err != nil {
		return err
	}
	switch t.Spec.State {
	case "", "stopped":
		// ok
	default:
		return fmt.Errorf("%s: spec.state %q is not allowed for a TemplateVM (PVE templates cannot be started; use 'stopped')", t.Ref(), t.Spec.State)
	}
	return nil
}

// Deps overrides the embedded VM.Deps: a TemplateVM is a clone SOURCE, never
// a clone target, so the VM→TemplateVM edge (M12) must not appear here even
// though the embedded struct promotes Deps() unchanged. (Validate rejects
// spec.clone on a TemplateVM, so the edge could only come from a manifest
// that never reaches the planner — this override is the belt-and-braces
// version of that guarantee.)
func (t *TemplateVM) Deps() []Ref {
	if strings.TrimSpace(t.Spec.Clone) == "" {
		return t.VM.Deps()
	}
	var refs []Ref
	if iso := strings.TrimSpace(t.Spec.Hardware.Cdrom.Iso); iso != "" && iso != CDROMNone {
		refs = append(refs, Ref{Kind: KindISO, Name: iso})
	}
	for _, d := range t.Spec.Disks {
		if img := strings.TrimSpace(d.Image); img != "" {
			refs = append(refs, Ref{Kind: KindDiskImage, Name: img})
		}
	}
	return refs
}
