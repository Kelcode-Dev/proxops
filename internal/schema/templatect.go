package schema

import (
	"fmt"
)

// TemplateCT is a PVE LXC container that has been promoted to a template
// (PVE `template=1` on the /lxc/{id}/config report). It is a first-class
// proxops kind as of M13, the LXC analogue of TemplateVM (M11).
//
// It is DISTINCT from KindCTTemplate, which is a storage ARTIFACT (a
// .tar.zst vztmpl file downloaded into a storage's vztmpl content). A
// TemplateCT is a live container object with a numeric CTID that was
// customized and then promoted with `pct template` (POST /lxc/{id}/
// template).
//
// Wire shape: identical to a proxops LXC. A TemplateCT embeds an LXC and
// inherits its spec surface, owned fields, Drift, ToCreateParams,
// DriftAnomalies and Deps. The differences are deliberately narrow
// (all probe-verified on conformance-dev, PVE 9.2.2):
//
//   - PVE's per-node id space is shared with LXC, so a TemplateCT and an
//     LXC cannot claim the same (node, ctid). proxops enforces this at
//     parse time as any other in-cluster id collision.
//   - PVE's /cluster/resources lists both with type="lxc"; the
//     distinguishing token is the per-object /config "template" = 1.
//   - Promotion is POST /lxc/{id}/template: HTTP 200 with a NULL data
//     response (synchronous — no task UPID, unlike the qm mark endpoint).
//     The executor treats an empty UPID as success.
//   - Promotion RENAMES the container's rootfs volume from
//     "vm-<ctid>-disk-0" to "base-<ctid>-disk-0" (PVE-owned volume naming;
//     proxops never compares volume names, so drift is stable across the
//     rename).
//   - A PVE template CT is reported "stopped" and cannot be usefully
//     started; proxops's TemplateCT DesiredState() is always "stopped".
//   - PVE 9.2 has NO /lxc/{id}/untemplate endpoint (probe-verified HTTP
//     501 "not implemented"). proxops therefore:
//   - creates a TemplateCT by POST /lxc (start=0) + POST /lxc/{id}/
//     template (the executor wraps the create),
//   - updates it via the standard PUT /lxc/{id}/config (the template
//     flag persists across config writes — probe-verified 200),
//   - and — when a proxops LXC (kind=LXC) is desired but PVE's live
//     object reports template=1 — surfaces a non-destructive anomaly
//     instead of attempting an untemplate.
//   - deletes it via DELETE /lxc/{id} (PVE accepts destroy on a
//     template CT; the executor pre-stops as usual, already stopped).
//
// Adoption (reverse translation): proxops adopt emits a TemplateCT
// manifest for any PVE CT that reports template=1, under
// templatect/<cluster>/. The manifest carries the same owned-field surface
// as a regular LXC, plus an explicit gap: PVE does not persist the
// ostemplate a container was created from, so spec.template must be
// filled by a human before the manifest is listed (same contract as the
// LXC ostemplate gap).
type TemplateCT struct {
	LXC `yaml:",inline" json:",inline"`
}

// NewTemplateCT returns an empty TemplateCT.
func NewTemplateCT() *TemplateCT {
	t := &TemplateCT{}
	t.Kind = KindTemplateCT
	t.APIVersion = APIVersion
	return t
}

// Ref overrides the embedded LXC.Ref to report Kind=TemplateCT.
func (t *TemplateCT) Ref() Ref { return Ref{Kind: t.Kind, Name: t.Metadata.Name} }

// DesiredState always returns "stopped": PVE templates are not started.
func (t *TemplateCT) DesiredState() string { return "stopped" }

// Validate delegates to LXC.Validate for the shared wire surface, then
// enforces the TemplateCT-only rule: state must be absent or "stopped".
func (t *TemplateCT) Validate() error {
	if err := t.LXC.Validate(); err != nil {
		return err
	}
	switch t.Spec.State {
	case "", "stopped":
		// ok
	default:
		return fmt.Errorf("%s: spec.state %q is not allowed for a TemplateCT (PVE templates are not started; use 'stopped')", t.Ref(), t.Spec.State)
	}
	return nil
}

// Deps delegates to the embedded LXC.Deps: a TemplateCT is bootstrapped
// from a CTTemplate artifact (spec.template → the vztmpl), exactly like a
// regular LXC. The promotion step itself adds no edges.
func (t *TemplateCT) Deps() []Ref { return t.LXC.Deps() }
