package schema

import (
	"fmt"
	"regexp"
	"strings"
)

// artifactNameRe validates ISO/CTTemplate on-storage filenames (no path
// separators, no leading dots). Both kinds use the same PVE dir-storage
// volid suffix, so the shape is the same.
var artifactNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]*$`)

// PVE storage ids follow this shape (alnum + . _ -).
var storageIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

// Checksum is an optional declared artifact digest. PVE's
// `POST /storage/{s}/download` API does not accept a `verify` form
// parameter on PVE 9.2 (probe-verified), so when declared the reconciler
// records the expected digest and logs a warn if it cannot read it back. The
// main role today is to give operators a first-class place to pin a known-
// good SHA-256 so typos / swapped URLs are easier to spot in review.
type Checksum struct {
	Algorithm string `yaml:"algorithm,omitempty" json:"algorithm,omitempty"`
	Value     string `yaml:"value,omitempty" json:"value,omitempty"`
}

// Valid reports whether the checksum is populated (algorithm + value) and
// the algorithm is one of the accepted hash names.
func (c Checksum) Valid() bool {
	if c.Algorithm == "" || c.Value == "" {
		return false
	}
	switch strings.ToLower(c.Algorithm) {
	case "sha256", "sha1", "sha512", "md5":
		return true
	}
	return false
}

// ISOSpec is the declarative ISO body.
//
// PVE "ISO" is not a persistent cluster object the way a VM is; it is an
// entry in a storage backend's ISO pool. The agent's role for an ISO is:
//   - ensure the file is present on PVE at EVERY (node, storage) declared;
//     when absent, issue a `POST /nodes/{n}/storage/{s}/download` using the
//     declared URL.
//   - never delete ISOs (MVP)
//
// PVE id: none. pveconform identity = metadata.name; per-node placement =
// `(node, storage, filename)` on PVE.
//
// Multi-node placement:
//
//	ISO/debian-13-netinst           (metadata.name)
//	  spec.nodes: [pve-dev-01, pve-dev-02]
//	  → "the file must exist on pve-dev-01/local AND pve-dev-02/local".
//
// The legacy single-node form is also honored: `spec.node` promotes to a
// one-entry Nodes list when spec.nodes is empty.
type ISOSpec struct {
	// Node is the legacy single-node form. Honor alongside Nodes.
	Node string `yaml:"node,omitempty" json:"node,omitempty"`
	// Nodes is the list of PVE nodes this ISO must exist on.
	Nodes []string `yaml:"nodes,omitempty" json:"nodes,omitempty"`
	// Storage is the PVE storage backend id (e.g. "local", "isos").
	Storage string `yaml:"storage" json:"storage"`
	// Filename is the on-storage name (e.g. "ubuntu-24.04-server-amd64.iso").
	Filename string `yaml:"filename" json:"filename"`
	// URL is the public download URL PVE will use.
	URL string `yaml:"url" json:"url"`
	// Checksum is the optional declared digest (see Checksum.Valid).
	Checksum Checksum `yaml:"checksum,omitempty" json:"checksum,omitempty"`
}

// artifactNodes returns the validated, deduplicated node list for an ISO or
// CTTemplate. When the legacy spec.node is set and nodes is empty, it is
// promoted to a one-element slice.
func artifactNodes(legacy string, declared []string) []string {
	n := declared
	if len(n) == 0 && legacy != "" {
		n = []string{legacy}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(n))
	for _, x := range n {
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

// ISO is a schema.Resource for Kind=ISO.
type ISO struct {
	APIVersion string
	Kind       Kind
	Metadata   Metadata
	Spec       ISOSpec
}

// NewISO returns an empty ISO.
func NewISO() *ISO { return &ISO{Kind: KindISO, APIVersion: APIVersion} }

// Ref implements Resource.
func (i *ISO) Ref() Ref { return Ref{Kind: i.Kind, Name: i.Metadata.Name} }

// Node returns the primary PVE node: the first entry of the normalized
// Nodes, so spec.node remains a drop-in for spec.nodes=[spec.node].
func (i *ISO) Node() string {
	ns := i.Nodes()
	if len(ns) == 0 {
		return ""
	}
	return ns[0]
}

// Nodes implements Resource — every PVE node this ISO must be present on.
func (i *ISO) Nodes() []string { return artifactNodes(i.Spec.Node, i.Spec.Nodes) }

// ID implements Resource — ISOs have no numeric PVE id.
func (i *ISO) ID() int { return 0 }

// DesiredState is always "" for artifacts.
func (i *ISO) DesiredState() string { return "" }

// Deps implements Resource — ISOs have no pveconform-side references.
func (i *ISO) Deps() []Ref { return nil }

// DriftAnomalies always returns nil: an ISO is a storage artifact, not a
// PVE object with per-slot live devices. There is no "live-only ISO" shape
// pveconform would have to surface.
func (i *ISO) DriftAnomalies(current map[string]any) []string { return nil }

// Validate implements Resource.
func (i *ISO) Validate() error {
	nodes := i.Nodes()
	if len(nodes) == 0 {
		return fmt.Errorf("%s: spec.node or spec.nodes must be set", i.Ref())
	}
	for _, n := range nodes {
		if !ValidNodeName(n) {
			return fmt.Errorf("%s: spec.node %q invalid", i.Ref(), n)
		}
	}
	if i.Spec.Storage == "" {
		return fmt.Errorf("%s: spec.storage must be set (e.g. local, isos)", i.Ref())
	}
	if !storageIDRe.MatchString(i.Spec.Storage) {
		return fmt.Errorf("%s: spec.storage %q invalid", i.Ref(), i.Spec.Storage)
	}
	if i.Spec.Filename == "" {
		return fmt.Errorf("%s: spec.filename must be set", i.Ref())
	}
	if !artifactNameRe.MatchString(i.Spec.Filename) {
		return fmt.Errorf("%s: spec.filename %q invalid (no path separators)", i.Ref(), i.Spec.Filename)
	}
	if i.Spec.URL == "" {
		return fmt.Errorf("%s: spec.url must be set", i.Ref())
	}
	if !strings.HasPrefix(i.Spec.URL, "http://") && !strings.HasPrefix(i.Spec.URL, "https://") {
		return fmt.Errorf("%s: spec.url must be http(s)", i.Ref())
	}
	if i.Spec.Checksum.Algorithm != "" || i.Spec.Checksum.Value != "" {
		if !i.Spec.Checksum.Valid() {
			return fmt.Errorf("%s: spec.checksum must specify both algorithm (sha256|sha1|sha512|md5) and value", i.Ref())
		}
	}
	return nil
}

// ToCreateParams emits PVE wire form-values for
// POST /nodes/{n}/storage/{s}/download. The executor uses these to drive
// the PVE-side download for every node in spec.nodes.
//
// The "storage" and "content" keys are executor-side hints (not PVE API
// form params) so the executor can route the download without re-reading
// the ISO struct.
func (i *ISO) ToCreateParams() (map[string]any, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	p := map[string]any{
		"url":      i.Spec.URL,
		"filename": i.Spec.Filename,
		"storage":  i.Spec.Storage,
		"content":  "iso",
	}
	if i.Spec.Checksum.Valid() {
		p["checksum"] = i.Spec.Checksum.Value
		p["checksum_algorithm"] = strings.ToLower(i.Spec.Checksum.Algorithm)
	}
	return p, nil
}

// Drift implements Resource.
//
// current is a raw PVE storage iso content entry, shaped by the planner to
// look like: {"present": bool}.
//
//   - absent → updateParams = {url, filename, storage, content}, changed=true
//   - present → all-nil
//   - nil    → fail-closed (no download on unreadable inventory).
func (i *ISO) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		return nil, false, false
	}
	present, _ := current["present"].(bool)
	if present {
		return nil, false, false
	}
	p, err := i.ToCreateParams()
	if err != nil {
		// Unreachable after Validate; keep Drift safe.
		return nil, false, false
	}
	return p, false, true
}
