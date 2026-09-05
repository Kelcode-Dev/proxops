package schema

import (
	"fmt"
	"strings"
)

// CTTSpec is the declarative CT clone-template body.
//
// PVE models a "template" as a CT (LXC container) with the `template` config
// flag set to 1. Cloning a template is what bootstraps a new CT; the
// agent's role for CTT is only "ensure a templated CT exists on PVE at this
// cid and it was cloned-from this source".
//
// MVP behavior:
//   - create: clone from Source into VMID, then mark as template
//   - drift:  the target cid exists on PVE AND is a template (i.e. config
//     "template" == "1"); if not, plan an action to re-issue the clone (or
//     mark-template when the cid exists as a plain CT)
//   - prune:  CTTs are LXC objects so they share the LXC prune model
type CTTSpec struct {
	// Node is the PVE node hosting this template.
	Node string `yaml:"node" json:"node"`
	// VMID is the PVE cid of the template (required; pinned).
	VMID int `yaml:"vmid" json:"vmid"`
	// Source is the cid of the existing CT/CTTemplate to clone from
	// (required for create; ignored for drift).
	Source int `yaml:"source" json:"source"`
	// PveName is PVE's "name" (defaults to metadata.name).
	PveName string `yaml:"pve-name,omitempty" json:"pve-name,omitempty"`
	// PveDescription.
	PveDescription string `yaml:"pve-description,omitempty" json:"pve-description,omitempty"`
	// Tags are PVE user tags (pveconform tag is auto-appended).
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	// FullClone uses PVE's full clone (default PVE clones via copy-on-write).
	Full bool `yaml:"full,omitempty" json:"full,omitempty"`
}

// CTTemplate is a schema.Resource for Kind=CTTemplate.
type CTTemplate struct {
	APIVersion string
	Kind       Kind
	Metadata   Metadata
	Spec       CTTSpec
}

// NewCTTemplate returns an empty CTTemplate.
func NewCTTemplate() *CTTemplate { return &CTTemplate{Kind: KindCTTemplate} }

// Ref implements Resource.
func (c *CTTemplate) Ref() Ref { return Ref{Kind: c.Kind, Name: c.Metadata.Name} }

// Node implements Resource.
func (c *CTTemplate) Node() string { return c.Spec.Node }

// ID implements Resource — PVE cid.
func (c *CTTemplate) ID() int { return c.Spec.VMID }

// DesiredState is always "" for CTT (no power state for templates).
func (c *CTTemplate) DesiredState() string { return "" }

// Validate implements Resource.
func (c *CTTemplate) Validate() error {
	if c.Spec.Node == "" {
		return fmt.Errorf("%s: spec.node is empty", c.Ref())
	}
	if !ValidNodeName(c.Spec.Node) {
		return fmt.Errorf("%s: spec.node %q invalid", c.Ref(), c.Spec.Node)
	}
	if c.Spec.VMID <= 0 {
		return fmt.Errorf("%s: spec.vmid must be > 0", c.Ref())
	}
	if c.Spec.Source <= 0 {
		return fmt.Errorf("%s: spec.source must be > 0", c.Ref())
	}
	if c.Spec.Source == c.Spec.VMID {
		return fmt.Errorf("%s: spec.source must differ from spec.vmid", c.Ref())
	}
	return nil
}

// toCreateParams returns PVE wire form-values for a clone+template
// (POST /lxc/{src}/clone + POST /lxc/{dst}/template). The executor reads
// "source" (PVE form-irrelevant) to build the clone URL.
func (c *CTTemplate) toCreateParams() (map[string]any, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	p := map[string]any{
		"newid":  c.Spec.VMID,
		"source": c.Spec.Source, // executor hint: clone source cid
	}
	if c.Spec.Full {
		p["full"] = "1"
	}
	if c.Spec.PveName != "" {
		p["name"] = c.Spec.PveName
	} else if c.Metadata.Name != "" {
		p["name"] = c.Metadata.Name
	}
	if c.Spec.PveDescription != "" {
		p["description"] = c.Spec.PveDescription
	}
	tags := c.allTags()
	if len(tags) > 0 {
		p["tags"] = strings.Join(tags, ",")
	}
	return p, nil
}

// ToCreateParams emits PVE clone params; the executor will then issue the
// "template" flag against the new cid.
func (c *CTTemplate) ToCreateParams() (map[string]any, error) {
	return c.toCreateParams()
}

// Drift implements Resource.
//
// A CTT is "drifted" when:
//   - the target cid does not exist yet (absent), OR
//   - the target cid exists but is not flagged as a template
//
// updateParams in that case carries the "recreate" flag; the executor checks
// LiveInventory for the "template" config value before issuing.
func (c *CTTemplate) Drift(current map[string]any) (map[string]any, bool, bool) {
	if current == nil {
		// Not present → create path; updateParams is empty, stopRequired is
		// false, changed is true so the planner emits an action.
		return map[string]any{"recreate": c.Spec.Source}, false, true
	}
	tplRaw, ok := current["template"]
	if !ok {
		// Target exists but is not templated → drift.
		return map[string]any{"template": "1"}, false, true
	}
	switch t := tplRaw.(type) {
	case string:
		if t == "1" {
			return nil, false, false
		}
	case int:
		if t == 1 {
			return nil, false, false
		}
	}
	return map[string]any{"template": "1"}, false, true
}

func (c *CTTemplate) allTags() []string {
	out := make([]string, 0, len(c.Spec.Tags)+1)
	seen := map[string]bool{}
	for _, t := range c.Spec.Tags {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	if !seen[PveOwnershipTag] {
		out = append(out, PveOwnershipTag)
	}
	return out
}

// pveName returns the PVE "name" for the template CT.
func (c *CTTemplate) pveName() string {
	if c.Spec.PveName != "" {
		return c.Spec.PveName
	}
	return c.Metadata.Name
}
