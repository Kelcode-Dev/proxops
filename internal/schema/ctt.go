package schema

import (
	"fmt"
	"regexp"
	"strings"
)

// cttNameRe validates a vztmpl filename on PVE storage. PVE's template
// files are usually "<dist>-<ver>_<build>_<arch>.tar.zst" or similar; the
// same grammar as ISO filename (alnum, . _ -; no path separators).
var cttNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]*$`)

// CTTSpec is the declarative body for a downloadable PVE "CT template"
// (vztmpl) storage artifact.
//
// PVE's CT template pool is a storage-content type (a `vztmpl` on a
// dir-storage backend). pveconform's job for this kind is to ensure the
// file is present at every (node, storage) the manifest declares —
// downloading from spec.url via PVE's POST /storage/{s}/download.
//
// This is NOT the same thing as an "LXC marked as template": that concept
// (clone a CT, then /ct/{cid}/template) can be modeled later as a
// different kind (see M5 notes). The CTT model here is a storage artifact
// and has no PVE numeric id.
//
// Identity on PVE: (node, storage, filename). Identity on pveconform:
// metadata.name. Multi-node placement via spec.nodes:
//
//	CTTemplate/debian-13
//	  spec.nodes: [pve-dev-01, pve-dev-02]
//	  spec.storage: local
//	  spec.filename: debian-13-standard_13.6.1-1_amd64.tar.zst
//	  spec.url: https://ftp-master.debian.org/template-files/...
//	  → the file must exist on both nodes' `local` storage.
//
// LXC manifests reference a CTTemplate via spec.template: metadata.name —
// that edge is a typed dependency in the planner (see LXC.Deps).
type CTTSpec struct {
	// Node is the legacy single-node form. Honor alongside Nodes.
	Node string `yaml:"node,omitempty" json:"node,omitempty"`
	// Nodes is the list of PVE nodes this template must exist on.
	Nodes []string `yaml:"nodes,omitempty" json:"nodes,omitempty"`
	// Storage is the PVE storage backend id with `vztmpl` content (e.g.
	// "local").
	Storage string `yaml:"storage" json:"storage"`
	// Filename is the on-storage name (e.g. "debian-13-standard_13.6.1-1_amd64.tar.zst").
	Filename string `yaml:"filename" json:"filename"`
	// URL is the public download URL PVE will use.
	URL string `yaml:"url" json:"url"`
	// Checksum is the optional artifact digest (see schema.Checksum).
	Checksum Checksum `yaml:"checksum,omitempty" json:"checksum,omitempty"`
}

// CTTemplate is a schema.Resource for Kind=CTTemplate.
type CTTemplate struct {
	APIVersion string
	Kind       Kind
	Metadata   Metadata
	Spec       CTTSpec
}

// NewCTTemplate returns an empty CTTemplate (storage artifact, no PVE cid).
func NewCTTemplate() *CTTemplate { return &CTTemplate{Kind: KindCTTemplate, APIVersion: APIVersion} }

// Ref implements Resource.
func (c *CTTemplate) Ref() Ref { return Ref{Kind: c.Kind, Name: c.Metadata.Name} }

// Node returns the primary PVE node (first entry of the normalized Nodes).
func (c *CTTemplate) Node() string {
	ns := c.Nodes()
	if len(ns) == 0 {
		return ""
	}
	return ns[0]
}

// Nodes implements Resource — every PVE node this template must be
// present on.
func (c *CTTemplate) Nodes() []string { return artifactNodes(c.Spec.Node, c.Spec.Nodes) }

// ID implements Resource — CTTemplate has no PVE numeric id (artifact).
func (c *CTTemplate) ID() int { return 0 }

// DesiredState is always "" for artifacts.
func (c *CTTemplate) DesiredState() string { return "" }

// Deps implements Resource — CTTs have no pveconform-side references.
func (c *CTTemplate) Deps() []Ref { return nil }

// Validate implements Resource.
func (c *CTTemplate) Validate() error {
	nodes := c.Nodes()
	if len(nodes) == 0 {
		return fmt.Errorf("%s: spec.node or spec.nodes must be set", c.Ref())
	}
	for _, n := range nodes {
		if !ValidNodeName(n) {
			return fmt.Errorf("%s: spec.node %q invalid", c.Ref(), n)
		}
	}
	if c.Spec.Storage == "" {
		return fmt.Errorf("%s: spec.storage must be set (the PVE storage id that carries `vztmpl` content, e.g. local)", c.Ref())
	}
	if !storageIDRe.MatchString(c.Spec.Storage) {
		return fmt.Errorf("%s: spec.storage %q invalid", c.Ref(), c.Spec.Storage)
	}
	if c.Spec.Filename == "" {
		return fmt.Errorf("%s: spec.filename must be set", c.Ref())
	}
	if !cttNameRe.MatchString(c.Spec.Filename) {
		return fmt.Errorf("%s: spec.filename %q invalid (no path separators)", c.Ref(), c.Spec.Filename)
	}
	if c.Spec.URL == "" {
		return fmt.Errorf("%s: spec.url must be set", c.Ref())
	}
	if !strings.HasPrefix(c.Spec.URL, "http://") && !strings.HasPrefix(c.Spec.URL, "https://") {
		return fmt.Errorf("%s: spec.url must be http(s)", c.Ref())
	}
	if c.Spec.Checksum.Algorithm != "" || c.Spec.Checksum.Value != "" {
		if !c.Spec.Checksum.Valid() {
			return fmt.Errorf("%s: spec.checksum must specify both algorithm (sha256|sha1|sha512|md5) and value", c.Ref())
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
// the CTT struct. PVE's "content" form parameter is set to "vztmpl" so the
// download lands in the template pool.
func (c *CTTemplate) ToCreateParams() (map[string]any, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	p := map[string]any{
		"url":      c.Spec.URL,
		"filename": c.Spec.Filename,
		"storage":  c.Spec.Storage,
		"content":  "vztmpl",
	}
	if c.Spec.Checksum.Valid() {
		p["checksum"] = c.Spec.Checksum.Value
		p["checksum_algorithm"] = strings.ToLower(c.Spec.Checksum.Algorithm)
	}
	return p, nil
}

// Drift implements Resource.
//
// current is a raw PVE storage vztmpl content entry, shaped by the planner to
// look like: {"present": bool}.
//
//   - absent → updateParams = download payload, changed=true
//   - present → all-nil
//   - nil    → fail-closed (no download on unreadable inventory)
func (c *CTTemplate) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		return nil, false, false
	}
	present, _ := current["present"].(bool)
	if present {
		return nil, false, false
	}
	p, err := c.ToCreateParams()
	if err != nil {
		return nil, false, false
	}
	return p, false, true
}
