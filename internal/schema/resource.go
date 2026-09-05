package schema

// Kind identifies a resource type within a given apiVersion.
type Kind string

const (
	KindVM         Kind = "VM"
	KindLXC        Kind = "LXC"
	KindCTTemplate Kind = "CTTemplate"
	KindISO        Kind = "ISO"
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

	// Node is the PVE node the resource lives on.
	Node() string

	// ID is the PVE object id (vmid/cid); 0 when the kind has no numeric id
	// (ISO).
	ID() int

	// DesiredState returns the requested power state; "" for kinds with none.
	DesiredState() string

	// ToCreateParams returns the PVE wire form-values for the create call.
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
}
