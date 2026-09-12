package schema

// Kind identifies a resource type within a given apiVersion.
type Kind string

const (
	KindVM         Kind = "VM"
	KindLXC        Kind = "LXC"
	KindCTTemplate Kind = "CTTemplate"
	KindISO        Kind = "ISO"
	// KindTemplateVM is a PVE qemu VM that has been marked as a
	// template (PVE `template=1`). pveconform manages its lifecycle as
	// a first-class object (M11): create + mark-template, config drift,
	// ownership-tag prunes, and — because PVE 9.2 does not implement
	// /qemu/{id}/untemplate — a non-destructive anomaly when a pveconform
	// VM desired matches a PVE-side template.
	//
	// Wire shape: identical to VM. pveconform shares PVE's per-node qm
	// id space with VM, so a TemplateVM and a VM cannot co-exist on the
	// same PVE id on the same node. PVE's /cluster/resources reports
	// both with type="qm"; the template flag in the per-object /config
	// report is what distinguishes them.
	KindTemplateVM Kind = "TemplateVM"
)

// Ref is the cross-kind identifier used by the Index and prune guards.
type Ref struct {
	Kind Kind
	Name string
}

// String renders Ref for display and logging: "VM/talos-worker-01".
func (r Ref) String() string { return string(r.Kind) + "/" + r.Name }

// Equal reports whether two Refs are the same identity.
func (r Ref) Equal(o Ref) bool { return r.Kind == o.Kind && r.Name == o.Name }

// ArtifactKind reports whether k is a PVE storage-artifact kind: the PVE
// object has no numeric vmid/cid. Artifacts are multi-node by design
// (spec.nodes), and their desired state is "present on each declared
// node/storage pair", verified via the storage content listing.
//
// Both ISO and the new CTTemplate resource shape are artifacts:
//   - ISO:        a .iso file on ISO pool storage.
//   - CTTemplate: a .vztmpl / tarball on vztmpl pool storage.
func ArtifactKind(k Kind) bool {
	return k == KindISO || k == KindCTTemplate
}

// Resource is the contract every parsed manifest satisfies. The reconciler
// programs against this interface; adding a new kind is additive (implement
// the methods, register it in NewResourceFor, and give it a differ/executor
// strategy in M3+).
//
// The interface keeps the schema layer independent of any PVE representation
// type: ToCreateParams and Drift emit "PVE wire" keys (snake_case PVE API
// form-values) that pveclient POSTs directly. This avoids a schema↔pve cycle
// and is what makes the per-kind owned-field table live in one place.
type Resource interface {
	// Validate checks cross-field invariants. After it succeeds the resource
	// is safe to index.
	Validate() error

	// Ref returns the cross-kind identity.
	Ref() Ref

	// Node is the PVE node the resource lives on (primary). For VM / LXC
	// this is spec.node. For artifacts (ISO / CTTemplate) it is the first
	// entry of spec.nodes (or spec.node when legacy form is used).
	Node() string

	// Nodes returns the list of PVE nodes the resource must be present on.
	// For VM / LXC it is always a singleton slice wrapping spec.node; for
	// ISO / CTTemplate it is spec.nodes (in manifest order, deduped,
	// validated), so multi-node artifact placement is first-class.
	Nodes() []string

	// ID is the PVE object id (vmid/cid); 0 when the kind has no numeric id
	// (ISO and CTTemplate are storage artifacts).
	ID() int

	// DesiredState returns the requested power state; "" for kinds with none.
	DesiredState() string

	// ToCreateParams returns the PVE wire form-values for the create call.
	// For VMs and LXCs these are PVE's /qemu or /lxc create parameters.
	// For ISO / CTTemplate the schema carries a storage + URL payload the
	// executor turns into a POST /nodes/{n}/storage/{s}/download call
	// (per node, when spec.nodes is multi-node).
	ToCreateParams() (map[string]any, error)

	// Drift compares the resource's desired state to a current PVE config
	// (as PVE returns it: a raw map[string]any with PVE's native spellings,
	// e.g. memory="8192") and returns (updateParams, stopRequired, changed):
	//   - updateParams: PVE wire form-values to POST to /config; empty when
	//     not changed.
	//   - stopRequired: true when the object must be stopped to apply.
	//   - changed:      true when any owned field diverges.
	//
	// Drift is the single owned-field surface: it owns both the desired and
	// actual encodings, which is how "diff(apply(X)) == 0" is testable.
	Drift(current map[string]any) (updateParams map[string]any, stopRequired, changed bool)

	// DriftAnomalies reports live-only devices that pveconform owns the
	// manifest but does not want to auto-delete:
	//   - VM: PVE disk slots (scsi*, virtio*, sata*) present on the live
	//     object with no matching entry in spec.disks. A live-only disk
	//     means somebody hand-added a drive; PVE's `scsiN=none` detach
	//     does NOT delete the underlying LVM volume (probe-verified on
	//     PVE 9.2), and deleting an arbitrary manifest-author-unaware
	//     volume would be a data-loss feature. So pveconform surfaces
	//     the anomaly on /status + /metrics without taking action.
	//   - LXC: PVE mp* slots present on the live object with no matching
	//     entry in spec.mount-points. Same semantics.
	//   - ISO / CTTemplate: nil (artifacts have no per-slot live devices).
	DriftAnomalies(current map[string]any) []string

	// Deps returns the structured, typed dependency edges the schema layer
	// can infer. For VM: one edge per cdrom.iso reference. For LXC: one
	// edge per spec.template reference. For ISO / CTTemplate: nil.
	//
	// The parse layer merges Deps() with the depends-on annotation edges
	// from Metadata and enforces the DAG (fail-closed on unknown targets or
	// cycles). The planner uses the merged edge set to order creates
	// topologically and to prune reverse-dependencies last.
	Deps() []Ref
}
