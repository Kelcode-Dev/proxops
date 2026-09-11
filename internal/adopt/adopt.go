// Package adopt implements `pveconform adopt`: the reverse-engineering
// path from live PVE back into pveconform manifests.
//
// adopt is READ-ONLY with respect to PVE. It performs no create, update,
// delete, template, or tag mutations on PVE objects — only the GET
// endpoints the planner uses. The PVE client's write counter
// (Client.WritesPerformed) is asserted to be zero after an adopt run.
//
// The generated YAML is placed under <kind>/<cluster>/ in the git work
// tree so that it is cluster-specific: the live object was observed on a
// concrete cluster, and pveconform's M8 model scopes reconciliation by
// cluster. A resource that can be shared across clusters (a "base") is a
// HUMAN refactoring decision made after review — adopt never promotes a
// live object to <kind>/base/ on its own.
//
// The gap report: a live PVE object may carry configuration pveconform
// does not model (PVE replication, VM pools, firewalls, ...). adopt must
// not silently lose that configuration: every key that appears in the
// live /config that pveconform does not understand is surfaced in a
// GAPS section of the output and (in an operator-visible message) named
// in the summary. docs/GAPS.md is the project-level compatibility
// backlog seeded from these discoveries — adopt does NOT edit GAPS.md
// (it is source-controlled project documentation, not runtime state).
package adopt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// Result is one adopt run outcome.
type Result struct {
	// Cluster is the named PVE cluster adopted from.
	Cluster string
	// PVE is the PVE client the run used (exposed so callers/tests can
	// assert PVE.WritesPerformed() == 0 — the read-only invariant).
	PVE *pveclient.Client
	// Wrote is the per-kind list of manifest files written under
	// <kind>/<cluster>/ in the git root, in deterministic order.
	Wrote []WroteManifest
	// Gaps is the union of "live key pveconform does not model" findings
	// across all adopted objects, deduplicated and ordered.
	Gaps []Gap
	// Warnings are human-readable per-object notes (unsupported config,
	// round-trip caveats, PVE-assigned fields).
	Warnings []string
	// Logs holds per-stage progress lines (node enumeration, artifacts
	// discovered, ...), for the CLI / future log integrations.
	Logs []string
	// Incomplete lists generated manifests that DO NOT satisfy Validate()
	// because a live value could not be recovered (typically LXC
	// spec.root.size, unreported by PVE's /config). The operator must
	// review + complete these before listing anything in a
	// clusters/<cluster>/resources.yaml: pveconform treats a composition
	// that references a non-validating manifest as a parse error and aborts
	// the whole cluster cycle (fail-closed), so an incomplete manifest
	// silently listed would take the cluster down.
	Incomplete []string
	// Skipped is one census entry per live PVE object that adopt DELIBERATELY
	// did not generate a manifest for (template VMs / LXC templates).
	// Presence here means "we saw it, we are not covering it, and here is
	// why" — the audit stays complete.
	Skipped []SkippedObject
}

// WroteManifest is one generated file.
type WroteManifest struct {
	Cluster  string
	Kind     schema.Kind
	Path     string // relative to git root, e.g. "vm/conformance-dev/existing-vm.yaml"
	Content  string
	LiveOnly []string // PVE keys not modeled in the generated manifest
}

// SkippedObject is one live PVE object that adopt DELIBERATELY did not turn
// into a manifest (template VMs / LXC templates: pveconform has no
// template-VM resource kind and must not claim ownership of a clone
// source). The census stays complete: a skipped object is reported, not
// silently dropped.
type SkippedObject struct {
	Kind   schema.Kind
	Node   string
	ID     int
	Name   string
	Reason string
}

// String renders one SkippedObject for summary / audit lines.
func (s SkippedObject) String() string {
	return fmt.Sprintf("%s %s#%d (%q): %s", kindLabel(s.Kind), s.Node, s.ID, s.Name, s.Reason)
}

// Gap is one "supported live config pveconform does not model" finding.
// It is the raw material for docs/GAPS.md. adopt emits these for review;
// it does not rewrite GAPS.md.
type Gap struct {
	// Kind is the pveconform kind (VM / LXC / ISO / CTTemplate).
	Kind schema.Kind
	// Node is the PVE node the gap was observed on.
	Node string
	// ID is the PVE numeric id (0 for artifacts).
	ID int
	// Field is the PVE /config key (e.g. "repl1", "numa", "boot").
	Field string
	// Value is the live value as a PVE string.
	Value string
	// Note explains, as compactly as adopt can, what the key means and
	// why pveconform does not model it today. Kept stable across runs so
	// operators can diff the gap set.
	Note string
}

// GapKey is the dedup key for a Gap.
func (g Gap) key() string {
	return string(g.Kind) + "\x00" + g.Node + "\x00" + itoa(g.ID) + "\x00" + g.Field + "\x00" + g.Value
}

// Summary renders a Result compactly for CLI output.
func (r Result) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "adopt: cluster %s (read-only)\n", r.Cluster)
	if len(r.Wrote) == 0 {
		b.WriteString("adopt: no objects to adopt\n")
		return b.String()
	}
	for _, w := range r.Wrote {
		fmt.Fprintf(&b, "adopt: wrote %s (%s)\n", w.Path, kindLabel(w.Kind))
	}
	for _, g := range r.Gaps {
		fmt.Fprintf(&b, "adopt: gap %s on %s#%d: %s=%s — %s\n",
			kindLabel(g.Kind), g.Node, g.ID, g.Field, g.Value, g.Note)
	}
	for _, wmsg := range r.Warnings {
		fmt.Fprintf(&b, "adopt: warning: %s\n", wmsg)
	}
	if len(r.Incomplete) > 0 {
		b.WriteString("adopt: INCOMPLETE MESSAGES (do NOT list in resources.yaml until reviewed):\n")
		for _, line := range r.Incomplete {
			b.WriteString("  - " + line + "\n")
		}
		b.WriteString("\n")
	}
	if len(r.Skipped) > 0 {
		b.WriteString("adopt: SKIPPED LIVE OBJECTS (deliberately not adopted — no manifest generated):\n")
		for _, s := range r.Skipped {
			b.WriteString("  - " + s.String() + "\n")
		}
		b.WriteString("\n")
	}
	// The reviewable next step: the paths that belong in
	// clusters/<cluster>/resources.yaml, excluding anything incomplete.
	// adopt does NOT write resources.yaml for the operator — it only names
	// the entries so the commit is explicit and reviewable.
	incompletePaths := map[string]bool{}
	for _, line := range r.Incomplete {
		// Incomplete entries are keyed by path: "kind/cluster/name: reason".
		if idx := strings.Index(line, ":"); idx > 0 {
			incompletePaths[strings.TrimSpace(line[:idx])] = true
		}
	}
	listable := []string{}
	for _, w := range r.Wrote {
		if incompletePaths[w.Path] {
			continue
		}
		// Path "vm/conformance-dev/name.yaml" → relative to
		// clusters/conformance-dev/ = "../../vm/conformance-dev/name.yaml".
		listable = append(listable, `  - ../../`+w.Path)
	}
	if len(listable) > 0 {
		b.WriteString("adopt: add these lines to clusters/" + r.Cluster + "/resources.yaml:\n")
		b.WriteString("  resources:\n")
		for _, l := range listable {
			b.WriteString(l + "\n")
		}
	}
	if len(r.Gaps) > 0 {
		b.WriteString("adopt: WARNING: generated YAML does not fully represent the live object(s); see gaps above. Review before git committing.\n")
	}
	return b.String()
}

// nameForPVE derives a pveconform metadata.name from a PVE-reported display /
// hostname + id fallback: PVE names are not guaranteed to be valid
// pveconform identifiers ("prod web 01" / empty / i18n), so adopt falls back
// to "vm-<id>" / "ct-<id>" when nothing usable remains (id >= 0 only;
// artifacts have no PVE numeric id and keep the sanitized label).
func nameForPVE(pveName string, id int, prefix string) string {
	n := sanitizeName(strings.TrimSpace(pveName))
	if (n == "" || n == "unnamed") && id >= 0 {
		return fmt.Sprintf("%s-%d", prefix, id)
	}
	return n
}

func kindLabel(k schema.Kind) string {
	switch k {
	case schema.KindVM:
		return "VM"
	case schema.KindLXC:
		return "LXC"
	case schema.KindISO:
		return "ISO"
	case schema.KindCTTemplate:
		return "CTTemplate"
	default:
		return string(k)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// dedupGaps orders + dedupes gaps by key. Ordering is a TOTAL order
// (kind, node, id, field, value) so two adopt runs against an unchanged
// PVE always produce byte-identical gap reports — critical for
// idempotency (a human diffing two consecutive adopt logs must not see
// any re-shuffling for reasons other than PVE actually changing).
func dedupGaps(in []Gap) []Gap {
	seen := map[string]Gap{}
	for _, g := range in {
		seen[g.key()] = g
	}
	out := make([]Gap, 0, len(seen))
	for _, g := range seen {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := &out[i], &out[j]
		if a.kindKey() != b.kindKey() {
			return a.kindKey() < b.kindKey()
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.Field != b.Field {
			return a.Field < b.Field
		}
		return a.Value < b.Value
	})
	return out
}

func (g Gap) kindKey() string { return string(g.Kind) + "\x00" + itoa(g.ID) }
