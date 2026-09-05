package schema

import (
	"fmt"
	"regexp"
	"strings"
)

// isoNameRe validates ISO filenames (no path separators, no leading dots).
var isoNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]*$`)

// ISOSpec is the declarative ISO body.
//
// PVE "ISO" is not a persistent cluster resource the way a VM is; it is an
// entry in a storage backend's ISO pool. The agent's role for ISO is:
//   - ensure the file is present on PVE (download if absent)
//   - never delete ISOs (MVP: no ownership tag on ISOs; post-MVP add explicit
//     iso-delete support)
//
// PVE id: none. Identity = (node, storage, filename).
type ISOSpec struct {
	// Node is the PVE node running the storage backend.
	Node string `yaml:"node" json:"node"`
	// Storage is the PVE storage backend id.
	Storage string `yaml:"storage" json:"storage"`
	// Filename is the on-storage name (e.g. "ubuntu-24.04-server-amd64.iso").
	Filename string `yaml:"filename" json:"filename"`
	// URL is the public download URL PVE will use.
	URL string `yaml:"url" json:"url"`
}

// ISO is a schema.Resource for Kind=ISO.
type ISO struct {
	APIVersion string
	Kind       Kind
	Metadata   Metadata
	Spec       ISOSpec
}

// NewISO returns an empty ISO.
func NewISO() *ISO { return &ISO{Kind: KindISO} }

// Ref implements Resource.
func (i *ISO) Ref() Ref { return Ref{Kind: i.Kind, Name: i.Metadata.Name} }

// Node implements Resource.
func (i *ISO) Node() string { return i.Spec.Node }

// ID implements Resource — ISOs have no numeric PVE id.
func (i *ISO) ID() int { return 0 }

// DesiredState is always "" for ISOs.
func (i *ISO) DesiredState() string { return "" }

// Validate implements Resource.
func (i *ISO) Validate() error {
	if i.Spec.Node == "" {
		return fmt.Errorf("%s: spec.node is empty", i.Ref())
	}
	if !ValidNodeName(i.Spec.Node) {
		return fmt.Errorf("%s: spec.node %q invalid", i.Ref(), i.Spec.Node)
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
	if !isoNameRe.MatchString(i.Spec.Filename) {
		return fmt.Errorf("%s: spec.filename %q invalid (no path separators)", i.Ref(), i.Spec.Filename)
	}
	if i.Spec.URL == "" {
		return fmt.Errorf("%s: spec.url must be set", i.Ref())
	}
	if !strings.HasPrefix(i.Spec.URL, "http://") && !strings.HasPrefix(i.Spec.URL, "https://") {
		return fmt.Errorf("%s: spec.url must be http(s)", i.Ref())
	}
	return nil
}

var storageIDRe = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)

// ToCreateParams emits PVE wire form-values for POST /nodes/{n}/storage/{s}/download.
// The executor treats any ISO "create" as a download; the "exists" check
// goes through Drift (below), not a separate API call, so we keep it cheap.
func (i *ISO) ToCreateParams() (map[string]any, error) {
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return map[string]any{
		"url":      i.Spec.URL,
		"filename": i.Spec.Filename,
		"storage":  i.Spec.Storage, // executor hint: storage backend id (PVE-irrelevant)
	}, nil
}

// Drift implements Resource.
//
// current is a raw PVE storage iso-content listing, shaped by the executor to
// look like: {"<filename>": true} for present + empty for absent.
//
//   - absent → updateParams = {url, filename}, stopRequired=false, changed=true
//   - present → all-nil
func (i *ISO) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		// No current listing → we can't know whether the ISO is present.
		// Fail-closed: plan no download; executor will surface as Skipped.
		return nil, false, false
	}
	present, _ := current["present"].(bool)
	if present {
		return nil, false, false
	}
	return map[string]any{
		"url":      i.Spec.URL,
		"filename": i.Spec.Filename,
	}, false, true
}
