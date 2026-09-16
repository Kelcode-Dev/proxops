// Package plan turns the diff between desired state (git Index) and actual
// state (live PVE reads) into an ordered list of Actions for the executor.
//
// Ordering (deterministic, plan §5/§9):
//  1. Tier 0 creates
//  2. Tier 0 config updates (StopFirst flagged when PVE requires a stop)
//  3. Tier 0 power transitions (start/stop)
//  4. Tier 9 prunes LAST (never interleaved with creates/updates)
//
// The planner is pure: no PVE writes, no task waits. It owns the safety model
// (plan §10): ownership-tag gate, prune budget, empty-desired anomaly guard,
// and a never-touch-untagged rule.
package plan

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// ActionKind is one unit of work the executor may perform.
type ActionKind string

const (
	Create     ActionKind = "create"
	Update     ActionKind = "update"
	Start      ActionKind = "start"
	Stop       ActionKind = "stop"
	Delete     ActionKind = "delete"
	StatusOnly ActionKind = "verify"  // read-back
	Anomaly    ActionKind = "anomaly" // non-destructive live-only slot surfacing
	// M11: mark an existing PVE qemu object as a template (POST /qemu/{id}/template).
	// The planner emits this when a desired TemplateVM matches a live PVE object
	// at the same (node, vmid) that does NOT report template=1.
	MarkTemplate ActionKind = "mark-template"
)

// Action is one planned operation on one PVE object.
type Action struct {
	Tier   int // 0 = creates/updates/power, 9 = prunes
	Kind   schema.Kind
	Name   string
	Node   string
	ID     int
	What   ActionKind
	Params map[string]any
	// StopFirst flags that this update may require stopping the object first.
	StopFirst bool
	// Prune marks this as a delete of a live object that is no longer desired.
	Prune bool
	// Reason is a human-readable explanation (for logs / --diff / status).
	Reason string
	// LivePower is the PVE power state at planning time ("" when unknown).
	LivePower string
	// DesiredPower is the manifest power state ("started"/"stopped").
	DesiredPower string
	// Level is the topological create-level of this action's resource:
	// level 0 = resource has no proxops dependencies; level n = depends
	// on resources at level < n. The planner uses this to order creates
	// so prerequisites are planned before dependants; prunes are ordered
	// by REVERSE level so dependants delete first (ISOs / templates must
	// outlive the VMs / LXC that reference them).
	Level int
	// Deps are this action's prerequisite refs (structured + annotation
	// edges). The executor uses them to defer this action when a
	// prerequisite failed earlier in the same cycle (a dependant is not
	// attempted before its prerequisite is ready).
	Deps []schema.Ref
	// Ref is the proxops manifest Ref this action targets (nil for
	// prunes of PVE-side objects not in git).
	Ref schema.Ref
	// Anomaly marks this action as a no-write observation. The executor
	// skips it; the planner records it on Plan.Anomalies so /status +
	// /metrics surface the live-only slot to the operator.
	Anomaly bool
	// CloneSourceID (M12) is the PVE vmid of the TemplateVM this Create
	// clones from. 0 = ordinary create. The executor refuses to run a
	// clone when the source id equals the target id (wrong-resource guard).
	CloneSourceID int
	// DeleteKeys (M12) are PVE /config keys the executor clears via the
	// `delete=` form-value in the post-clone config write, so a clone never
	// silently keeps the template's identity (hostname/cloud-init user/keys/
	// static IP/onboot/...) for fields the VM manifest does not own.
	DeleteKeys []string
}

// Plan is the ordered result of one planning pass.
type Plan struct {
	Actions  []Action
	Skipped  []Action // live objects with no proxops tag (never touched)
	Deferred []Action // prunes beyond the per-cycle budget
	Anomaly  string   // probable-bad-push signal (empty-desired anomaly guard)
	// Anomalies: live-only-slot observations (proxops will NOT delete
	// them; the operator does that manually). Surfaced on /status +
	// /metrics; never executed.
	Anomalies []Action
}

// LiveInventory is the PVE-side snapshot the planner reads from.
type LiveInventory struct {
	// Configs: keyed "node|KIND|id" → PVE config (all list-form-normalized).
	Configs map[string]map[string]any
	// Power: keyed the same → "running" | "stopped".
	Power map[string]string
	// Listing: every PVE resource entry (VMs, LXC, disks, etc.)
	Listing []pveclient.ClusterResource
	// M11: qm ids PVE reports as template=1, keyed "node|qmid" -> PVE name.
	// Used for VM-vs-TemplateVM mismatch anomaly and prune-safety.
	TemplateKeys map[string]string
}

// Budget caps deletions per cycle.
type Budget struct {
	Prune int
}

// DefaultBudget is 3 (plan §10).
func DefaultBudget() Budget { return Budget{Prune: 3} }

// LevelsFunc returns the topological create-level for a Ref. 0 when the
// resource has no dependencies. The planner uses this to sort creates
// (level ascending) and prunes (level descending).
type LevelsFunc func(ref schema.Ref) int

// EdgesFunc returns the dependency refs (prerequisites) of a given Ref. The
// planner uses this to populate Action.Deps so the executor can defer a
// dependant when its prerequisite failed earlier in the same cycle.
type EdgesFunc func(ref schema.Ref) []schema.Ref

// PlanOptions carries planner inputs.
//
// The plan is PURE: it always reports the full desired-vs-actual delta
// (creates, updates, prunes, power). The executor is the only write gate —
// dry-run/apply semantics live there, not here. This is what makes
// `--diff` / `--dry-run` show the exact intent that `apply --once` would
// perform, including would-be deletes. (plan §2 / §10)
type PlanOptions struct {
	Budget Budget
	// Levels, when non-nil, orders creates by dependency level:
	// prerequisites (level 0, e.g. an ISO) are planned before dependent
	// resources (level 1, e.g. a VM that mounts that ISO). Prunes use
	// the reverse: a resource that depends on another is pruned first so
	// the dependency outlives the dependent. When Levels is nil the
	// planner falls back to the historical deterministic sort.
	Levels LevelsFunc
	// Edges, when non-nil, populates Action.Deps for VM/LXC creates so that
	// the executor can defer a dependant whose prerequisite failed in-cycle.
	// Returns nil → no deferral (all actions attempted in plan order).
	Edges EdgesFunc
	// NodeAllowlist restricts live-inventory consideration (configs, power,
	// AND prune candidates) to these PVE node names. Empty = accept all
	// nodes. This is the M8 cluster boundary: two clusters that share a PVE
	// endpoint (forbidden by config) would otherwise be able to prune each
	// other's objects; within one cluster the allowlist scopes every
	// planner decision to that cluster's configured nodes.
	NodeAllowlist []string
}

// Plan walks desired vs. live and emits the ordered action list.
func PlanActions(ctx context.Context, desired []schema.Resource, live *LiveInventory, opts PlanOptions) (*Plan, error) {
	if live == nil {
		live = &LiveInventory{
			Configs: map[string]map[string]any{},
			Power:   map[string]string{},
		}
	}
	_ = ctx

	p := &Plan{
		Actions:   make([]Action, 0, 32),
		Skipped:   make([]Action, 0, 4),
		Anomalies: make([]Action, 0, 4),
		Deferred:  make([]Action, 0, 4),
	}
	levels := opts.Levels
	if levels == nil {
		levels = func(_ schema.Ref) int { return 0 }
	}
	edges := opts.Edges
	if edges == nil {
		edges = func(_ schema.Ref) []schema.Ref { return nil }
	}
	depsFor := func(ref schema.Ref) []schema.Ref { return edges(ref) }

	// Pass A: desired objects → creates/updates/power.
	for _, r := range desired {
		ref := r.Ref()
		// Artifacts (ISO, CTTemplate) are per-node per-storage storage
		// entries and do not use the standard (node, kind, id) live shape.
		if schema.ArtifactKind(ref.Kind) {
			planArtifact(p, r, live, levels)
			continue
		}
		kt := ref.Kind
		key := liveKey(r.Node(), kt, r.ID())
		cfg, present := live.Configs[key]

		// M11: desired TemplateVM whose live PVE object exists as a non-template
		// VM -> emit MarkTemplate (one write; re-derivable next cycle).
		if kt == schema.KindTemplateVM && !present {
			vmKey := liveKey(r.Node(), schema.KindVM, r.ID())
			if live.Configs[vmKey] != nil {
				p.Actions = append(p.Actions, Action{
					Tier: 0, Kind: kt, Name: ref.Name, Node: r.Node(), ID: r.ID(),
					What: MarkTemplate, Level: levels(ref), Ref: ref,
					Reason:    ref.String() + ": PVE object present but not a template; will mark as template",
					Deps:      depsFor(ref),
					LivePower: live.Power[vmKey], DesiredPower: r.DesiredState(),
				})
				continue
			}
		}

		// M13: desired TemplateCT whose live PVE object exists as a
		// non-template CT -> emit MarkTemplate (POST /lxc/{id}/template;
		// synchronous null response, handled by the executor).
		if kt == schema.KindTemplateCT && !present {
			ctKey := liveKey(r.Node(), schema.KindLXC, r.ID())
			if live.Configs[ctKey] != nil {
				p.Actions = append(p.Actions, Action{
					Tier: 0, Kind: kt, Name: ref.Name, Node: r.Node(), ID: r.ID(),
					What: MarkTemplate, Level: levels(ref), Ref: ref,
					Reason:    ref.String() + ": PVE object present but not a template; will mark as template",
					Deps:      depsFor(ref),
					LivePower: live.Power[ctKey], DesiredPower: r.DesiredState(),
				})
				continue
			}
		}

		if !present {
			params, err := r.ToCreateParams()
			if err != nil {
				return nil, fmt.Errorf("%s: create params: %w", ref, err)
			}
			// M12: clone-backed VM. The create is a PVE full clone from the
			// resolved TemplateVM's pinned vmid (never a vmid the manifest
			// could invent), followed by a config write that applies the
			// VM's own values and clears the inherited identity keys the
			// manifest does not own.
			var cloneSrc int
			var delKeys []string
			verb := createVerb(kt)
			if v, ok := r.(*schema.VM); ok && v.IsCloneBacked() {
				cloneSrc = v.CloneSourceID()
				if cloneSrc == 0 {
					return nil, fmt.Errorf("%s: spec.clone references %q but no clone source vmid resolved (resolver did not run?)", ref, v.Spec.Clone)
				}
				if cloneSrc == v.Spec.VMID {
					return nil, fmt.Errorf("%s: clone source vmid %d equals the target vmid (would clone onto itself)", ref, cloneSrc)
				}
				delKeys = v.CloneDeleteKeys(params)
				verb = fmt.Sprintf("full-clone from TemplateVM %s (vmid %d)", strings.TrimSpace(v.Spec.Clone), cloneSrc)
			}
			p.Actions = append(p.Actions, Action{
				Tier: 0, Kind: kt, Name: ref.Name, Node: r.Node(), ID: r.ID(),
				What: Create, Params: params, Level: levels(ref), Ref: ref,
				Deps:          depsFor(ref),
				Reason:        ref.String() + ": not on PVE; will " + verb,
				LivePower:     "", DesiredPower: r.DesiredState(),
				CloneSourceID: cloneSrc,
				DeleteKeys:    delKeys,
			})
			continue
		}

		// M11: desired proxops VM whose live PVE object is a PVE-side
		// template (template=1): proxops cannot untemplate on PVE 9.2 (501
		// "not implemented") and must not claim ownership of a clone source.
		// Surface a non-destructive anomaly; no config/power write.
		if kt == schema.KindVM && isPVETemplate(cfg) {
			p.Anomalies = append(p.Anomalies, Action{
				Tier: 0, Kind: kt, Name: ref.Name, Node: r.Node(), ID: r.ID(),
				What: Anomaly, Level: levels(ref), Ref: ref,
				Anomaly: true,
				// Niche note: proxops refuses to write a kind-flip on PVE; the
				// operator either switches the manifest to kind: TemplateVM
				// (M11), or demotes the PVE object manually.
				Reason: ref.String() + ": live PVE object is a template (template=1); a proxops VM manifest cannot be applied to a PVE template and PVE 9.2 has no /qemu/{id}/untemplate - change the manifest to kind: TemplateVM (or demote the PVE object by hand)",
			})
			continue
		}

		// M13: desired proxops LXC whose live PVE object is a PVE-side
		// template CT: same rule as the VM case — no untemplate endpoint
		// exists (probe-verified 501), so surface a non-destructive anomaly
		// instead of writing config onto a clone source.
		if kt == schema.KindLXC && isPVETemplate(cfg) {
			p.Anomalies = append(p.Anomalies, Action{
				Tier: 0, Kind: kt, Name: ref.Name, Node: r.Node(), ID: r.ID(),
				What: Anomaly, Level: levels(ref), Ref: ref,
				Anomaly: true,
				Reason:  ref.String() + ": live PVE object is a template (template=1); a proxops LXC manifest cannot be applied to a PVE template and PVE 9.2 has no /lxc/{id}/untemplate - change the manifest to kind: TemplateCT (or demote the PVE object by hand)",
			})
			continue
		}

		updParams, stopFirst, changed := r.Drift(cfg)

		// Non-destructive anomalies: live-only slots (VM disks / NICs / LXC
		// mount-points) that the manifest does not declare.
		// proxops will NOT auto-delete these (the executor skips Anomaly
		// actions); they are surfaced on /status + /metrics so the operator
		// can remove them by hand.
		if anomalies := r.DriftAnomalies(cfg); anomalies != nil {
			for _, msg := range anomalies {
				an := Action{
					Tier:    0,
					Kind:    kt,
					Name:    ref.Name,
					Node:    r.Node(),
					ID:      r.ID(),
					What:    Anomaly,
					Level:   levels(ref),
					Ref:     ref,
					Reason:  ref.String() + ": " + msg,
					Anomaly: true,
				}
				p.Anomalies = append(p.Anomalies, an)
			}
		}

		if changed {
			reason := ref.String() + ": config drift"
			if stopFirst {
				reason += " (stop-required)"
			}
			p.Actions = append(p.Actions, Action{
				Tier: 0, Kind: kt, Name: ref.Name, Node: r.Node(), ID: r.ID(),
				What: Update, Params: updParams, StopFirst: stopFirst,
				Level: levels(ref), Ref: ref,
				Reason:    reason,
				LivePower: live.Power[key], DesiredPower: r.DesiredState(),
			})
		}

		// Power transition (independent of config drift). Templates and other
		// kind without state contribute no power verb.
		if verb := powerDesiredToLive(r.DesiredState(), live.Power[key]); verb != "" {
			what := Start
			if verb == "stop" {
				what = Stop
			}
			p.Actions = append(p.Actions, Action{
				Tier: 0, Kind: kt, Name: ref.Name, Node: r.Node(), ID: r.ID(),
				What:  what,
				Level: levels(ref), Ref: ref,
				Reason:    fmt.Sprintf("%s: power %q → %q", ref, live.Power[key], r.DesiredState()),
				LivePower: live.Power[key], DesiredPower: r.DesiredState(),
			})
		}
	}

	// Pass B: prunes (PVE-present, not in desired, proxops-tagged).
	//
	// Artifacts (ISO, CTTemplate) are NEVER pruned in the MVP — PV has no
	// ownership tag on storage content; deleting a vztmpl / iso can silently
	// break other tooling that references it. The operator removes them by
	// hand or via a future explicit artifact-delete resource. This is the
	// "conservative" artifact deletion guarantee.
	pruneCandidates := make([]Action, 0, 4)
	desiredByKind := map[schema.Kind]int{}
	for _, r := range desired {
		desiredByKind[r.Ref().Kind]++
	}

	// Build a set of desired identities for fast membership tests.
	desiredSet := map[string]bool{}
	for _, r := range desired {
		kind := r.Ref().Kind
		if schema.ArtifactKind(kind) {
			// Artifacts are per-(node, storage, filename); not part of PVE
			// cluster/resources listing, so no PVE-side membership test.
			continue
		}
		// identity by PVE id + node (the authoritative mapping PVE knows).
		desiredSet[liveKey(r.Node(), kind, r.ID())] = true
		// also accept identity by ref (kind+name)
		desiredSet[r.Ref().String()] = true
		// M11: a TemplateVM and a VM share PVE's per-qm-id space. Register BOTH
		// so the prune pass (normalizes qm->KindVM) never prunes a desired
		// TemplateVM; a desired VM is recognised against a live template.
		// M13: same rule for TemplateCT/LXC on the per-lxc-id space.
		if kind == schema.KindTemplateVM {
			desiredSet[liveKey(r.Node(), schema.KindVM, r.ID())] = true
		}
		if kind == schema.KindTemplateCT {
			desiredSet[liveKey(r.Node(), schema.KindLXC, r.ID())] = true
		}
	}

	for _, res := range live.Listing {
		if res.Node == "" || res.Vmid <= 0 || res.Type == "" {
			continue
		}
		// Cluster boundary: prune candidates are limited to the cluster's
		// configured node allowlist. A tagged live object on a node outside
		// this cluster's allowlist is never a candidate, no matter what the
		// desired composition says.
		if !nodeAllowed(opts.NodeAllowlist, res.Node) {
			continue
		}
		// PVE's cluster/resources "type" is "qm" or "lxc". A live CTT (an LXC
		// with template flag) shows up as "lxc" here; the planner must NOT
		// prune it when the desired side tracks it as CTT, so we check both
		// LXC and CTT live keys against membership. If PVE lists the object
		// as "lxc" and the desired side has it as CTT at the same cid, the
		// CTT desiredSet key will match (we insert CTT's liveKey too).
		kind := normalizeKind(res.Type)
		if kind != schema.KindVM && kind != schema.KindLXC {
			// Only VM/LXC are managed by proxops MVP (CTT is LXC-flavored;
			// ISO is not a listing entry).
			continue
		}
		key := liveKey(res.Node, kind, res.Vmid)
		cfg := live.Configs[key]
		if cfg == nil {
			// Couldn't read config (transient / listing-only entry).
			// Safety: never prune on a nil config.
			continue
		}
		// membership check: PVE id+node OR ref matches desired.
		if desiredSet[key] || desiredSet[kindRef(kind, cfg)] {
			continue // still in git → not a prune candidate
		}
		if !HasTag(cfg, schema.PveOwnershipTag) {
			name, _ := cfg["name"].(string)
			p.Skipped = append(p.Skipped, Action{
				Kind: kind, Name: name, Node: res.Node, ID: res.Vmid,
				What:   StatusOnly,
				Reason: "live object without proxops tag; never touched",
			})
			continue
		}
		nameStr, _ := cfg["name"].(string)
		// M11: a live PVE-side template whose PVE id is claimed by a desired
		// TemplateVM manifest is NOT a prune candidate.
		if isPVETemplate(cfg) && desiredTemplateVM(desired, res.Node, res.Vmid) {
			continue
		}
		// M13: same rule for a desired TemplateCT claiming a live template CT.
		if isPVETemplate(cfg) && desiredTemplateCT(desired, res.Node, res.Vmid) {
			continue
		}
		pruneCandidates = append(pruneCandidates, Action{
			Tier: 9, Kind: kind, Name: nameStr, Node: res.Node, ID: res.Vmid,
			What: Delete, Prune: true, Level: 0,
			Reason:    "present on PVE (proxops-tagged) but absent in git",
			LivePower: live.Power[keyFor(res.Node, kind, res.Vmid)],
		})
	}

	// Reverse-topological prune order: a live CTT/ISO is never pruned in MVP
	// (conservative), so prunes are all VM/LXC; the order is:
	// dependent first (higher level), prerequisite later (lower level). With
	// no dependency info (legacy planner, no Levels func), level is 0 and
	// sort is stable-deterministic.
	for i := range pruneCandidates {
		pruneCandidates[i].Level = 0 // prunes are tier-9 anyway
	}

	// Anomaly guard (plan §10): when the desired set for a kind is EMPTY and
	// the count of PVE-live proxops-tagged orphans of that kind EXCEEDS the
	// per-cycle prune budget, this is the "mass deletion" shape of a probable
	// bad push (someone deleted all manifests of a kind). Suppress prunes for
	// that kind and surface the anomaly. Orphans within the budget with empty
	// desired are treated as ordinary (safe) deletions. CTT orphans share the
	// LXC pool in PVE's listing; the guard covers VM + LXC only.
	var anomalyParts []string
	suppressed := map[schema.Kind]bool{}
	for _, k := range []schema.Kind{schema.KindVM, schema.KindLXC} {
		if desiredByKind[k] > 0 {
			continue // desired set of this kind is not empty
		}
		candidates := 0
		for _, a := range pruneCandidates {
			if a.Kind == k {
				candidates++
			}
		}
		if candidates > opts.Budget.Prune {
			suppressed[k] = true
			anomalyParts = append(anomalyParts,
				fmt.Sprintf("0 desired %s but %d proxops-tagged live objects (more than the per-cycle prune budget of %d): probable bad push; suppresses prunes for %s this cycle",
					k, candidates, opts.Budget.Prune, k))
		}
	}
	if len(anomalyParts) > 0 {
		p.Anomaly = strings.Join(anomalyParts, "; ")
	}
	// Drop suppressed-kind prunes from the active candidate list.
	if len(suppressed) > 0 {
		filtered := make([]Action, 0, len(pruneCandidates))
		for _, a := range pruneCandidates {
			if suppressed[a.Kind] {
				p.Deferred = append(p.Deferred, a)
				continue
			}
			filtered = append(filtered, a)
		}
		pruneCandidates = filtered
	}

	// Apply budget.
	if opts.Budget.Prune > 0 && len(pruneCandidates) > opts.Budget.Prune {
		// Deterministic ordering for prunes: node, then id ascending.
		sort.Slice(pruneCandidates, func(i, j int) bool {
			if pruneCandidates[i].Node != pruneCandidates[j].Node {
				return pruneCandidates[i].Node < pruneCandidates[j].Node
			}
			return pruneCandidates[i].ID < pruneCandidates[j].ID
		})
		p.Deferred = append(p.Deferred, pruneCandidates[opts.Budget.Prune:]...)
		pruneCandidates = pruneCandidates[:opts.Budget.Prune]
	}

	// Append prunes LAST.
	p.Actions = append(p.Actions, pruneCandidates...)

	// Deterministic, dependency-aware ordering:
	//   1. Tier (0 = creates/updates/power, 9 = prunes).
	//   2. Within tier 0: topological level ascending (prerequisites
	//      first), then What (Create < Update < power), Node, ID, Name.
	//   3. Within tier 9: prunes are all VM/LXC — no dependencies in
	//      the artifact layer; ordering is stable-deterministic.
	whatRank := map[ActionKind]int{Create: 0, Update: 1, StatusOnly: 2, Start: 3, Stop: 4, Delete: 9, Anomaly: 10}
	sort.SliceStable(p.Actions, func(i, j int) bool {
		a, b := p.Actions[i], p.Actions[j]
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if a.Tier == 0 && a.Level != b.Level {
			return a.Level < b.Level
		}
		if a.What != b.What {
			return whatRank[a.What] < whatRank[b.What]
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Name < b.Name
	})
	return p, nil
}

// LoadLive reads the cluster listing, then per-object config (VM + LXC; CTT
// is now a storage artifact and not a PVE listing entry), then power; and
// finally — for every desired ARTIFACT (ISO, CTTemplate, DiskImage), probes the PVE
// storage backend's content listing to emit {"present": bool} at the
// artifactKey(node, storage, filename, kind) for each declared node.
//
// allowedNodes, when non-empty, restricts the live inventory to those PVE
// node names: listing entries on other nodes are not read, not counted,
// and — critically — not considered as prune candidates. This is the
// cluster-scope gate that keeps one cluster's planner from acting on
// objects that belong to another cluster's composition.
func LoadLive(ctx context.Context, c *pveclient.Client, desired []schema.Resource, allowedNodes []string) (*LiveInventory, error) {
	listing, err := c.ClusterResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("cluster resources: %w", err)
	}
	inv := &LiveInventory{
		Configs:      map[string]map[string]any{},
		Power:        map[string]string{},
		Listing:      listing,
		TemplateKeys: map[string]string{},
	}
	for _, res := range listing {
		if res.Node == "" || res.Vmid <= 0 || res.Type == "" {
			continue
		}
		if !nodeAllowed(allowedNodes, res.Node) {
			continue
		}
		kind := normalizeKind(res.Type)
		switch kind {
		case schema.KindVM:
			cfg, gErr := c.VM().Get(ctx, res.Node, res.Vmid)
			if gErr != nil {
				if pveclient.IsNotFound(gErr) {
					continue
				}
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = "read-error: " + gErr.Error()
				continue
			}
			inv.Configs[keyFor(res.Node, kind, res.Vmid)] = cfg
			if st, sErr := c.VM().Status(ctx, res.Node, res.Vmid); sErr == nil {
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = normalizePower(st.Status)
			}
			// M11: PVE qm object reporting template=1 -> also key under
			// TemplateVM + record the "node|qmid" template record.
			if isPVETemplate(cfg) {
				tvKey := keyFor(res.Node, schema.KindTemplateVM, res.Vmid)
				inv.Configs[tvKey] = cfg
				name, _ := cfg["name"].(string)
				inv.TemplateKeys[res.Node+"|q"+strconv.Itoa(res.Vmid)] = name
			}
		case schema.KindLXC:
			cfg, gErr := c.LXC().Get(ctx, res.Node, res.Vmid)
			if gErr != nil {
				if pveclient.IsNotFound(gErr) {
					continue
				}
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = "read-error: " + gErr.Error()
				continue
			}
			inv.Configs[keyFor(res.Node, kind, res.Vmid)] = cfg
			if st, sErr := c.LXC().Status(ctx, res.Node, res.Vmid); sErr == nil {
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = normalizePower(st.Status)
			}
			// M13: PVE lxc object reporting template=1 -> also key under
			// TemplateCT so a desired TemplateCT finds its live config.
			if isPVETemplate(cfg) {
				inv.Configs[keyFor(res.Node, schema.KindTemplateCT, res.Vmid)] = cfg
			}
		}
	}

	// For each desired ARTIFACT (ISO, CTTemplate, DiskImage), probe every
	// (node, storage, content) pair it wants present and record
	// {"present": bool} at the artifact-key for that node.
	for _, r := range desired {
		if !schema.ArtifactKind(r.Ref().Kind) {
			continue
		}
		kind := r.Ref().Kind
		content, storage, filename := artifactStorageFields(r, kind)
		if content == "" {
			// malformed (or legacy spec not yet validated) — planner will
			// surface via resource.Validate in parse; skip here.
			continue
		}
		for _, node := range r.Nodes() {
			key := artifactKey(node, storage, filename, kind)
			if _, present := inv.Configs[key]; present {
				// dedup
				continue
			}
			has, err := c.Storage().HasContent(ctx, node, storage, content, filename)
			if err != nil {
				// Fail-closed: unreadable inventory. Plan no download this
				// cycle; planArtifact will skip-and-report.
				inv.Power[key] = "read-error: " + err.Error()
				continue
			}
			inv.Configs[key] = map[string]any{"present": has}
		}
	}
	return inv, nil
}

// artifactStorageFields returns (content, storage, filename) for an artifact
// resource. content is "iso" for ISO and "vztmpl" for CTTemplate.
// storage / filename are taken from the spec.
func artifactStorageFields(r schema.Resource, kind schema.Kind) (content, storage, filename string) {
	switch v := r.(type) {
	case *schema.ISO:
		content = "iso"
		storage = v.Spec.Storage
		filename = v.Spec.Filename
	case *schema.CTTemplate:
		content = "vztmpl"
		storage = v.Spec.Storage
		filename = v.Spec.Filename
	case *schema.DiskImage:
		content = "import"
		storage = v.Spec.Storage
		filename = v.Spec.Filename
	}
	_ = kind
	return
}

// artifactKey builds the LiveInventory key for an (artifact, node, storage,
// filename) tuple. Used by LoadLive to cache presence; consumed by
// planArtifact.
func artifactKey(node, storage, filename string, kind schema.Kind) string {
	return node + "|" + string(kind) + "|" + storage + ":" + filename
}

// keyFor builds the LiveInventory key.
func keyFor(node string, kind schema.Kind, id int) string {
	return node + "|" + string(kind) + "|" + strconv.Itoa(id)
}

func liveKey(node string, kind schema.Kind, id int) string { return keyFor(node, kind, id) }

// createVerb is a human noun-verb used in the "not on PVE; will ..." reason.
func createVerb(k schema.Kind) string {
	switch k {
	case schema.KindCTTemplate:
		return "download ct-template (vztmpl)"
	case schema.KindISO:
		return "download"
	case schema.KindDiskImage:
		return "download disk image (import)"
	// M11: TemplateVM create is two PVE writes (POST /qemu + POST /qemu/{id}/template).
	case schema.KindTemplateVM:
		return "create + mark as PVE template"
	default:
		return "create"
	}
}

// planArtifact plans one artifact (ISO, CTTemplate or DiskImage) at every node
// it is declared to exist on. For each (node, storage, filename) it is missing,
// emit one CREATE with the artifact's ToCreateParams.
//
// Artifacts are never pruned: deleting an ISO / vztmpl / import image can break
// other tooling (LXC clones, other VMs that reference it); this is the
// conservative artifact-deletion guarantee.
// planArtifact plans one artifact (ISO, CTTemplate or DiskImage) at every node
// it is declared to exist on. Artifacts are LEAF nodes in the dep graph: they
// never depend on each other or on VM/LXC, so no `Deps` are populated.
func planArtifact(p *Plan, r schema.Resource, live *LiveInventory, levels LevelsFunc) {
	kind := r.Ref().Kind
	_, storage, filename := artifactStorageFields(r, kind)
	lvl := levels(r.Ref())
	params, err := r.ToCreateParams()
	if err != nil {
		p.Skipped = append(p.Skipped, Action{
			Kind: kind, Name: r.Ref().Name, Node: r.Node(),
			What: StatusOnly, Level: lvl,
			Reason: fmt.Sprintf("%s: create params: %v", r.Ref(), err),
		})
		return
	}
	for _, node := range r.Nodes() {
		key := artifactKey(node, storage, filename, kind)
		cur, present := live.Configs[key]
		if !present || cur == nil {
			// Fail-closed: without a live listing the agent cannot verify
			// the artifact state. Skip (never blindly download on a broken
			// read); the next cycle retries after a successful read.
			p.Skipped = append(p.Skipped, Action{
				Kind: kind,
				Name: r.Ref().Name,
				Node: node,
				What: StatusOnly, Level: lvl,
				Reason: fmt.Sprintf("%s live inventory unreadable this cycle; skipping download of %s/%s",
					kind, storage, filename),
			})
			continue
		}
		if _, _, changed := r.Drift(cur); !changed {
			continue
		}
		p.Actions = append(p.Actions, Action{
			Tier: 0, Kind: kind, Name: r.Ref().Name, Node: node, ID: 0,
			Level: lvl, Ref: r.Ref(),
			What: Create, Params: params,
			Reason: fmt.Sprintf("%s %s not present on storage %s on %s; will download",
				kind, filename, storage, node),
		})
	}
}

// nodeAllowed reports whether node passes the allowlist. An empty allowlist
// accepts every node (single-node test mocks, MVP-era config without
// pve.nodes).
func nodeAllowed(allow []string, node string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, a := range allow {
		if a == node {
			return true
		}
	}
	return false
}

// normalizeKind maps PVE's /cluster/resources type field to schema.Kind.
// PVE uses "qemu" (VM) and "lxc" (ct). Unknown → "".
func normalizeKind(t string) schema.Kind {
	switch strings.ToLower(t) {
	case "vm", "qemu", "qm":
		return schema.KindVM
	case "ct", "lxc", "container":
		return schema.KindLXC
	default:
		return schema.Kind(strings.ToUpper(t))
	}
}

func kindRef(kind schema.Kind, cfg map[string]any) string {
	name, _ := cfg["name"].(string)
	return string(kind) + "/" + name
}

// powerDesiredToLive returns "start" / "stop" / "" to reach desired from live.
// desired: "started" | "stopped"; live: "running" | "stopped" (normalized).
func powerDesiredToLive(desired, live string) string {
	if live == "" {
		return "" // no live power info
	}
	switch desired {
	case "started":
		if live != "running" {
			return "start"
		}
	case "stopped":
		if live == "running" {
			return "stop"
		}
	}
	return ""
}

// HasTag reports whether PVE's tags value (JSON array or string) contains t.
func HasTag(cfg map[string]any, t string) bool {
	raw, ok := cfg["tags"]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && s == t {
				return true
			}
		}
	case string:
		for _, part := range strings.Split(v, ",") {
			if strings.TrimSpace(part) == t {
				return true
			}
		}
	}
	return false
}

// normalizePower maps PVE's raw status to "running"/"stopped".
func normalizePower(s string) string {
	switch strings.ToLower(s) {
	case "running", "started":
		return "running"
	case "stopped", "paused", "":
		return "stopped"
	default:
		return strings.ToLower(s)
	}
}

// M11: isPVETemplate reports whether PVE's /config indicates the object is a
// template (template=1). PVE encodes this as an integer JSON number
// ("template": 1); Go's encoding/json decodes bare integers as float64, so the
// float64 case is the real-world wire form. The mock PVE stores it as the
// string "1" (mock form-values are map[string]string), so both cases are
// supported.
func isPVETemplate(cfg map[string]any) bool {
	v, ok := cfg["template"]
	if !ok {
		return false
	}
	switch t := v.(type) {
	case string:
		return t == "1" || t == "true"
	case int:
		return t == 1
	case int64:
		return t == 1
	case float64:
		return t == 1.0
	}
	return false
}

// M11: desiredTemplateVM reports whether a desired TemplateVM manifest claims
// the PVE id on the given node (prune-safe guard).
func desiredTemplateVM(desired []schema.Resource, node string, id int) bool {
	for _, r := range desired {
		if r.Ref().Kind != schema.KindTemplateVM {
			continue
		}
		if r.Node() == node && r.ID() == id {
			return true
		}
	}
	return false
}

// M13: desiredTemplateCT reports whether a desired TemplateCT manifest claims
// the PVE CT id on the given node (prune-safe guard).
func desiredTemplateCT(desired []schema.Resource, node string, id int) bool {
	for _, r := range desired {
		if r.Ref().Kind != schema.KindTemplateCT {
			continue
		}
		if r.Node() == node && r.ID() == id {
			return true
		}
	}
	return false
}
