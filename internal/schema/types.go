package schema

import (
	"fmt"
	"regexp"
	"strings"
)

// APIVersion is the only accepted manifest apiVersion for now.
const APIVersion = "proxops/v1alpha1"

// AllKinds returns the known kinds (used for routing/validation).
func AllKinds() []Kind {
	return []Kind{KindVM, KindLXC, KindCTTemplate, KindISO, KindTemplateVM, KindDiskImage}
}

// ParseKind maps a string to a Kind, case-insensitively. It also accepts the
// legacy "CTT" spelling for CTTemplate and the M11 aliases "TVM" / "TEMPLATE"
// for TemplateVM.
//
// The input is normalized to the canonical kind so that callers (manifest
// routing, depends-on edge resolution) can compare kinds with ==.
func ParseKind(s string) (Kind, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "VM":
		return KindVM, nil
	case "LXC":
		return KindLXC, nil
	case "CTTEMPLATE", "CTT":
		return KindCTTemplate, nil
	case "ISO":
		return KindISO, nil
	case "DISKIMAGE", "DISK":
		return KindDiskImage, nil
	case "TEMPLATEVM", "TVM", "TEMPLATE":
		return KindTemplateVM, nil
	default:
		return "", fmt.Errorf("unknown kind %q (valid: VM, LXC, CTTemplate, ISO, TemplateVM, DiskImage)", s)
	}
}

// NodeSpec-like name validation.
var nameRe = regexp.MustCompile(`^[a-z0-9]((-?[a-z0-9])*){0,63}$`)

// ValidName reports whether n is a usable metadata.name (lowercase dns-1035-ish).
func ValidName(n string) bool {
	return nameRe.MatchString(n)
}

// ValidNodeName reports whether a string is a usable PVE node name.
func ValidNodeName(n string) bool {
	return len(n) > 0 && len(n) <= 64 && !strings.ContainsAny(n, "/ ")
}

// ValidTag reports a PVE tag value (no whitespace, <= 256 chars).
func ValidTag(t string) bool {
	return t != "" && !strings.ContainsAny(t, " \t") && len(t) <= 256
}

// Metadata is the shared manifest header.
type Metadata struct {
	// Name is the git-side identity ("talos-worker-01").
	Name string `yaml:"name" json:"name"`
	// Namespace groups resources (optional; not a PVE concept).
	Namespace string `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	// Labels are opaque user annotations (informational).
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	// Annotations carry proxops/* hints.
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

// DependsOnKey is the annotation that declares an explicit dependency.
const DependsOnKey = "proxops/depends-on"

// dependsRe matches "ISO:ubuntu", "CTTemplate:base", "Pool:default".
var dependsRe = regexp.MustCompile(`^([A-Za-z]+):([A-Za-z0-9.\-_]+)$`)

// DependsOnError returns the parse-error from the DependsOnKey annotation, if
// any, WITHOUT materialising the Deps. parse uses this to surface annotation
// typos without needing to re-run the annotation parse.
func (m Metadata) DependsOnError() error {
	raw, ok := m.Annotations[DependsOnKey]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if mm := dependsRe.FindStringSubmatch(part); mm == nil {
			return fmt.Errorf("bad depends-on %q: expected KindName", part)
		}
	}
	return nil
}

// DependsOn parses the DependsOnKey annotation value into (kind, name) edges.
// Multiple values are comma-separated.
func (m Metadata) DependsOn() ([]Dep, error) {
	raw, ok := m.Annotations[DependsOnKey]
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []Dep
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mm := dependsRe.FindStringSubmatch(part)
		if mm == nil {
			return nil, fmt.Errorf("bad depends-on %q: expected KindName", part)
		}
		depKind, _ := ParseKind(mm[1])
		_ = depKind // kinds for edges are textual; kind routing happens at index time
		out = append(out, Dep{Kind: mm[1], Name: mm[2]})
	}
	return out, nil
}

// Dep is a typed dependency edge.
type Dep struct {
	Kind string `yaml:"kind" json:"kind"`
	Name string `yaml:"name" json:"name"`
}

// Ref is a convenience Ref built from a Dep.
func (d Dep) Ref() Ref {
	k, err := ParseKind(d.Kind)
	if err != nil {
		k = Kind(d.Kind)
	}
	return Ref{Kind: k, Name: d.Name}
}
