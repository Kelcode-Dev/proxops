// Package adopt implements `proxops adopt`: the reverse-engineering
// path from live PVE back into proxops manifests.
//
// adopt is READ-ONLY with respect to PVE. It performs no create, update,
// delete, template, or tag mutations on PVE objects — only the GET
// endpoints the planner uses. The PVE client's write counter
// (Client.WritesPerformed) is asserted to be zero after an adopt run.
//
// The generated YAML is placed under <kind>/<cluster>/ in the git work
// tree so that it is cluster-specific: the live object was observed on a
// concrete cluster, and proxops's M8 model scopes reconciliation by
// cluster. A resource that can be shared across clusters (a "base") is a
// HUMAN refactoring decision made after review — adopt never promotes a
// live object to <kind>/base/ on its own.
//
// The gap report: a live PVE object may carry configuration proxops
// does not model (PVE replication, VM pools, firewalls, ...). adopt must
// not silently lose that configuration: every key that appears in the
// live /config that proxops does not understand is surfaced in a
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
	"github.com/GizzmoShifu/proxmox-operator/internal/secrets"
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
	// Gaps is the union of "live key proxops does not model" findings
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
	// clusters/<cluster>/resources.yaml: proxops treats a composition
	// that references a non-validating manifest as a parse error and aborts
	// the whole cluster cycle (fail-closed), so an incomplete manifest
	// silently listed would take the cluster down.
	Incomplete []string
	// Skipped is one census entry per live PVE object that adopt DELIBERATELY
	// did not generate a manifest for (template VMs / LXC templates).
	// Presence here means "we saw it, we are not covering it, and here is
	// why" — the audit stays complete.
	Skipped []SkippedObject

	// M13.2: cloud-init secret census. Populated when the adopter observed
	// PVE objects that configured cipassword= or a cloud-init ssh-keys
	// field. Empty when no such material was observed. The CLI surfaces
	// these counts without ever echoing secret material.
	CloudInit CloudInitSummary
	// M13.2: SSH key names resolved during adoption. In PLAIN mode this is
	// the set of manifest refs emitted via ssh-key-refs (only when live
	// material matched the existing SOPS store); in --adopt-secrets mode
	// this is the union of those PLUS any newly-adopted names. Sorted.
	AdoptedSSHRefs []string
	// M13.2: in --adopt-secrets mode: the SOPS names newly added by this
	// adopt run, mapped to the OpenSSH public key lines PVE held. The CLI
	// writes these into cloud-init.ssh-keys.<name> alongside the existing
	// material and merges atomically. Adopt NEVER writes the file itself.
	NewSSHKeys map[string]string
}

// CloudInitSummary is the M13.2 census of PVE-recoverable cloud-init
// material across all objects in an adopt run. Counts only — no key
// material, no password material.
//
// Mode distinction (M13.2 UX cleanup): the operator-facing output is
// "describe the final result, not internal phases". The census carries
// AdoptSecrets so Summary() can pick the matching report shape:
//   - plain adoption: "SSH public keys: N unique across M resources; SSH keys
//     were not imported; re-run with --adopt-secrets to import recoverable
//     keys."
//   - --adopt-secrets: "SSH public keys: N unique across M resources; imported:
//     I; reused existing: K; resources referencing: R."
type CloudInitSummary struct {
	// AdoptSecrets reflects the --adopt-secrets flag this run used. It does
	// NOT change matching behaviour (see Options.AdoptSecrets) — it tells
	// Summary() how to phrase the result.
	AdoptSecrets bool
	// SSHUniqueLiveKeys is the count of DISTINCT live PVE ssh-key lines
	// observed in this run, deduped by SSHKeyFingerprint (type + base64
	// body; the OpenSSH comment is NOT part of the identity — matching PVE's
	// storage semantics, so "the same key under two comments" still counts
	// once). Recorded independently of SOPS matching: a plain SOPS-less
	// adoption still reports the PVE material it saw. This is the "N unique
	// across M resources" operator-facing number.
	SSHUniqueLiveKeys int
	// SSHReused is how many distinct SOPS names already present in the store
	// had a live ssh-key match this run (0 in plain adoptions with no SOPS
	// store, or when no match succeeded).
	SSHReused int
	// SSHAdded is how many NEW SOPS names this run created
	// (--adopt-secrets only).
	SSHAdded int
	// SSHResources is the count of resources that had at least one SSH
	// public key configured on PVE.
	SSHResources int
	// SSHResourcesWithRefs is the count of resources whose ssh-keys were
	// fully resolved to SOPS refs this run (all-or-nothing rule: a partially
	// matched resource keeps the sentinel and does NOT count). In plain
	// adoptions without a SOPS store this is 0; in --adopt-secrets runs it
	// is the number of resources that now carry ssh-key-refs. This is the
	// final result the operator should see: "resources referencing: R".
	SSHResourcesWithRefs int
	// PwResources is the count of resources that had a cipassword value
	// configured on PVE (PVE 9.2 masks the value: presence is the only
	// signal; the plaintext is NOT recoverable — manual ci-password-ref
	// mapping is required).
	PwResources int
	// SOPSBacked is true when the cluster carried a DECRYPTED SOPS store this
	// run so matching/importing was possible. A valid SOPS document whose
	// blocks are empty at first sight is still SOPS-backed — operators should
	// re-run with --adopt-secrets to import unmatched live keys, not treat
	// the cluster as "not SOPS-backed".
	SOPSBacked bool
}

// Options is the M13.2 adopt-time configuration. The zero value (Options{})
// is valid: adopt then behaves exactly as it did pre-M13.2 (ssh-keys are
// emitted as the "PVE-owned" ["*"] sentinel whenever PVE reports
// ssh-keys material; cipassword stays in the gap report, never imported).
//
// When Options.SOPS is non-empty, adopt MATCHES live PVE ssh-keys against
// the in-memory SOPS cloud-init.ssh-keys material: a live key is referenced
// in the manifest via `spec.cloud-init-data.ssh-key-refs` ONLY when an
// exact material match exists in the SOPS store; otherwise it remains the
// sentinel. Matching is by SSHKeyIdentity (type + base64 material; the
// OpenSSH comment is intentionally part of the identity — two key-lines that
// differ only in comment are NOT the same key, matching PVE's own storage
// semantics where the comment rides on the wire).
//
// Options.AdoptSecrets is the `--adopt-secrets` flag: when true, live keys
// that do NOT match an existing SOPS entry are also imported into Result.
// NewSSHKeys (the name -> ssh-key-line map that the CLI persists to the
// SOPS document under cloud-init.ssh-keys.) and the corresponding
// ssh-key-refs in the manifest. The names are generated by the
// deterministic fingerprint algorithm in schema.SSHKeyFingerprint:
//   - if the PVE key-line has an OpenSSH comment that is already in use
//     by an existing SOPS key with the same material, that name is reused
//     (no new entry);
//   - if not, the name is "adopted-<fingerprint prefix>" (see schema
//     for the exact shape).
//
// Adopt NEVER writes the SOPS file itself (that would break the zero-write
// invariant). The CLI owns that write: after adopt returns, it inspects
// NewSSHKeys and (when --adopt-secrets) merges + re-encrypts atomically.
type Options struct {
	// SOPS is the in-memory, already-decrypted SOPS document for this
	// cluster. When SOPS.Loaded is false (the zero SecretsFile), adopt
	// matches nothing and emits only the ["*"] sentinel for PVE ssh-keys
	// (pre-M13.2 behaviour). When SOPS.Loaded is true, adopt treats the
	// cluster as SOPS-backed regardless of whether the cloud-init
	// entries are populated: a valid SOPS file whose ssh-keys/passwords
	// blocks are initially empty is still a SOPS store. Consumers must not
	// infer "SOPS-backed" from map population.
	SOPS secrets.SecretsFile
	// AdoptSecrets is the --adopt-secrets opt-in flag. When true, live
	// ssh-keys without an existing SOPS match are also imported (named
	// into NewSSHKeys + referenced in the manifest). Default false.
	AdoptSecrets bool
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
// into a manifest (template VMs / LXC templates: proxops has no
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

// Gap is one "supported live config proxops does not model" finding.
// It is the raw material for docs/GAPS.md. adopt emits these for review;
// it does not rewrite GAPS.md.
type Gap struct {
	// Kind is the proxops kind (VM / LXC / ISO / CTTemplate).
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
	// why proxops does not model it today. Kept stable across runs so
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
	ci := r.CloudInit
	// M13.2 UX cleanup: the census describes the FINAL RESULT of this
	// adoption, not internal phases. The two shapes:
	//
	//   plain adoption   -> "SSH keys were not imported; re-run with
	//                       --adopt-secrets to import recoverable keys"
	//                       (whether or not a SOPS store is configured:
	//                       a store with zero cloud-init entries behaves
	//                       identically to no store for matching, and the
	//                       operator's next step is the same in both cases).
	//
	//   --adopt-secrets  -> "imported: I; reused existing: K;
	//                       resources referencing: R"
	//
	// The Cloud-Init password line is identical in both shapes: PVE 9.2
	// masks cipassword on readback (a fixed "**********", plaintext
	// unrecoverable), so passwords are NEVER imported by --adopt-secrets and
	// always require manual ci-password-ref mapping.
	if ci.SSHResources > 0 || ci.PwResources > 0 {
		if ci.AdoptSecrets {
			b.WriteString("adopt: cloud-init secrets (PVE material; values NOT printed):\n")
		} else {
			b.WriteString("adopt: cloud-init secret census (PVE material; values NOT printed):\n")
		}
		if ci.SSHResources > 0 {
			fmt.Fprintf(&b, "  SSH public keys: %d unique across %d resources\n", ci.SSHUniqueLiveKeys, ci.SSHResources)
			if ci.AdoptSecrets {
				fmt.Fprintf(&b, "  imported: %d\n", ci.SSHAdded)
				fmt.Fprintf(&b, "  reused existing: %d\n", ci.SSHReused)
				fmt.Fprintf(&b, "  resources referencing: %d\n", ci.SSHResourcesWithRefs)
			} else {
				b.WriteString("  SSH keys were not imported; re-run with --adopt-secrets to import recoverable keys\n")
			}
			b.WriteString("\n")
		}
		if ci.PwResources > 0 {
			fmt.Fprintf(&b, "  Cloud-Init passwords: configured on %d resources\n", ci.PwResources)
			b.WriteString("  recoverable: 0 (PVE 9.2 returns only the masked value \"**********\" on readback; the plaintext is unrecoverable)\n")
			b.WriteString("  manual ci-password-ref mapping required: " + itoa(ci.PwResources) + "\n")
		}
	}
	if len(r.Gaps) > 0 {
		b.WriteString("adopt: WARNING: generated YAML does not fully represent the live object(s); see gaps above. Review before git committing.\n")
	}
	return b.String()
}

// nameForPVE derives a proxops metadata.name from a PVE-reported display /
// hostname + id fallback: PVE names are not guaranteed to be valid
// proxops identifiers ("prod web 01" / empty / i18n), so adopt falls back
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
