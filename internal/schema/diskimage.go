package schema

import (
	"fmt"
	"strings"
)

// DiskImage is a downloadable disk-image storage artifact (PVE 9 `import`
// content pool: qcow2 / vmdk / raw). It is the third ArtifactKind alongside
// ISO and CTTemplate, and it is what makes a pveconform `kind: VM` able to
// boot a real OS: PVE's create-time `import-from` seeds a VM disk from an
// image volume instead of allocating an empty one.
//
// PVE 9.2 wire facts (probed on conformance-dev 2026-09-13):
//
//   - Download endpoint: the SAME `POST /nodes/{n}/storage/{s}/download-url`
//     used by ISO/CTTemplate, with `content=import`. The file lands in the
//     storage's `import/` sub-pool: volid `<storage>:import/<filename>`.
//   - Filename extension is validated by PVE: `.qcow2`, `.vmdk` and `.raw`
//     are accepted; `.qcow`, `.img` and `.iso` are REJECTED with
//     `400 "invalid filename or wrong extension"` (note that `.img` is an
//     ISO-pool extension only — the import pool does not accept it).
//   - A VM consumes it at create time as
//     `scsiN=<pool>:0,import-from=<storage>:import/<filename>` — the size
//     token MUST be `0` (PVE: "'import-from' requires special syntax - use
//     <storage ID>:0,import-from=<source>"). PVE derives the volume size
//     from the image's virtual size and reports the plain imported form
//     afterwards (`local-lvm:vm-N-disk-0,size=3G`), i.e. the import-from
//     option is create-time bookkeeping and is NOT re-reported.
//
// Identity on PVE: (node, storage, filename). Identity on pveconform:
// metadata.name. Multi-node placement via spec.nodes, exactly like ISO/CTT.
// Never pruned (artifact rule).
//
// A VM references a DiskImage by metadata.name on its disk entry
// (`spec.disks[].image`), which yields a structured VM → DiskImage
// dependency edge: the image must be present on the VM's node before the
// VM's create can succeed.
type DiskImageSpec struct {
	// Node is the legacy single-node form. Honor alongside Nodes.
	Node string `yaml:"node,omitempty" json:"node,omitempty"`
	// Nodes is the list of PVE nodes this image must exist on.
	Nodes []string `yaml:"nodes,omitempty" json:"nodes,omitempty"`
	// Storage is the PVE storage backend id with `import` content (e.g.
	// "local").
	Storage string `yaml:"storage" json:"storage"`
	// Filename is the on-storage name (e.g. "debian-13-genericcloud-amd64.qcow2").
	Filename string `yaml:"filename" json:"filename"`
	// URL is the public download URL PVE will use.
	URL string `yaml:"url" json:"url"`
	// Checksum is the optional artifact digest (see schema.Checksum).
	Checksum Checksum `yaml:"checksum,omitempty" json:"checksum,omitempty"`
}

// DiskImage is a schema.Resource for Kind=DiskImage.
type DiskImage struct {
	APIVersion string        `yaml:"apiVersion" json:"apiVersion"`
	Kind       Kind          `yaml:"kind" json:"kind"`
	Metadata   Metadata      `yaml:"metadata" json:"metadata"`
	Spec       DiskImageSpec `yaml:"spec" json:"spec"`
}

// NewDiskImage returns an empty DiskImage (storage artifact, no PVE id).
func NewDiskImage() *DiskImage { return &DiskImage{Kind: KindDiskImage, APIVersion: APIVersion} }

// Ref implements Resource.
func (d *DiskImage) Ref() Ref { return Ref{Kind: d.Kind, Name: d.Metadata.Name} }

// Node returns the primary PVE node (first entry of the normalized Nodes).
func (d *DiskImage) Node() string {
	ns := d.Nodes()
	if len(ns) == 0 {
		return ""
	}
	return ns[0]
}

// Nodes implements Resource — every PVE node this image must be present on.
func (d *DiskImage) Nodes() []string { return artifactNodes(d.Spec.Node, d.Spec.Nodes) }

// ID implements Resource — DiskImage has no PVE numeric id (artifact).
func (d *DiskImage) ID() int { return 0 }

// DesiredState is always "" for artifacts.
func (d *DiskImage) DesiredState() string { return "" }

// Deps implements Resource — DiskImages have no pveconform-side references.
func (d *DiskImage) Deps() []Ref { return nil }

// DriftAnomalies always returns nil: a DiskImage is a storage artifact.
func (d *DiskImage) DriftAnomalies(current map[string]any) []string { return nil }

// Validate implements Resource.
func (d *DiskImage) Validate() error {
	nodes := d.Nodes()
	if len(nodes) == 0 {
		return fmt.Errorf("%s: spec.node or spec.nodes must be set", d.Ref())
	}
	for _, n := range nodes {
		if !ValidNodeName(n) {
			return fmt.Errorf("%s: spec.node %q invalid", d.Ref(), n)
		}
	}
	if d.Spec.Storage == "" {
		return fmt.Errorf("%s: spec.storage must be set (the PVE storage id that carries `import` content, e.g. local)", d.Ref())
	}
	if !storageIDRe.MatchString(d.Spec.Storage) {
		return fmt.Errorf("%s: spec.storage %q invalid", d.Ref(), d.Spec.Storage)
	}
	if d.Spec.Filename == "" {
		return fmt.Errorf("%s: spec.filename must be set", d.Ref())
	}
	if !artifactNameRe.MatchString(d.Spec.Filename) {
		return fmt.Errorf("%s: spec.filename %q invalid (no path separators)", d.Ref(), d.Spec.Filename)
	}
	// PVE 9.2 dir-storage import pool accepts .qcow2 | .vmdk | .raw (probed
	// on conformance-dev 2026-09-13); anything else is 400 "wrong file
	// extension" at download time. Fail closed at parse.
	if ok, why := artifactExtAccept(d.Spec.Filename, "import"); !ok {
		return fmt.Errorf("%s: spec.filename %q: %s (PVE 9.2 import dir-storage accepts .qcow2 | .vmdk | .raw)", d.Ref(), d.Spec.Filename, why)
	}
	if d.Spec.URL == "" {
		return fmt.Errorf("%s: spec.url must be set", d.Ref())
	}
	if !strings.HasPrefix(d.Spec.URL, "http://") && !strings.HasPrefix(d.Spec.URL, "https://") {
		return fmt.Errorf("%s: spec.url must be http(s)", d.Ref())
	}
	if d.Spec.Checksum.Algorithm != "" || d.Spec.Checksum.Value != "" {
		if !d.Spec.Checksum.Valid() {
			return fmt.Errorf("%s: spec.checksum must specify both an algorithm (sha256|sha1|sha512|md5) and value", d.Ref())
		}
	}
	return nil
}

// ToCreateParams emits PVE wire form-values for
// POST /nodes/{n}/storage/{s}/download-url with content=import.
func (d *DiskImage) ToCreateParams() (map[string]any, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	p := map[string]any{
		"url":      d.Spec.URL,
		"filename": d.Spec.Filename,
		"storage":  d.Spec.Storage,
		"content":  "import",
	}
	if d.Spec.Checksum.Valid() {
		p["checksum"] = d.Spec.Checksum.Value
		p["checksum_algorithm"] = strings.ToLower(d.Spec.Checksum.Algorithm)
	}
	return p, nil
}

// Drift implements Resource. current is the planner's {"present": bool} probe.
func (d *DiskImage) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		return nil, false, false
	}
	present, _ := current["present"].(bool)
	if present {
		return nil, false, false
	}
	p, err := d.ToCreateParams()
	if err != nil {
		return nil, false, false
	}
	return p, false, true
}

// Volid returns the PVE volume id for this image's storage+filename.
func (d *DiskImage) Volid() string {
	return d.Spec.Storage + ":import/" + d.Spec.Filename
}
