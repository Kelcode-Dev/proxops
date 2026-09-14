package schema

import "fmt"

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
// Dependency semantics: a TemplateVM's only proxops-side edge is the
// inherited VM cdrom.iso edge (to an ISO). proxops does NOT model a
// "VM clones TemplateVM" edge in M11 — VMs are never created by PVE clone
// from a proxops TemplateVM (a VM manifest describes a fresh VM, disks
// allocated by PVE at create, which is the proxops guarantee against
// data loss: a clone would re-create the template's own disk on first
// apply → the exact data-loss shape M10's adopt guards against).

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
// enforces the TemplateVM-only rule: state must be absent or "stopped".
func (t *TemplateVM) Validate() error {
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

// Deps is inherited from the embedded VM (inherited cdrom.iso → ISO edge).
