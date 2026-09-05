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
	StatusOnly ActionKind = "verify" // read-back
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
}

// Plan is the ordered result of one planning pass.
type Plan struct {
	Actions  []Action
	Skipped  []Action // live objects with no pveconform tag (never touched)
	Deferred []Action // prunes beyond the per-cycle budget
	Anomaly  string   // probable-bad-push signal, if any
}

// LiveInventory is the PVE-side snapshot the planner reads from.
type LiveInventory struct {
	// Configs: keyed "node|KIND|id" → PVE config (all list-form-normalized).
	Configs map[string]map[string]any
	// Power: keyed the same → "running" | "stopped".
	Power map[string]string
	// Listing: every PVE resource entry (VMs, LXC, disks, etc.)
	Listing []pveclient.ClusterResource
}

// Budget caps deletions per cycle.
type Budget struct {
	Prune int
}

// DefaultBudget is 3 (plan §10).
func DefaultBudget() Budget { return Budget{Prune: 3} }

// PlanOptions carries planner inputs.
//
// The plan is PURE: it always reports the full desired-vs-actual delta
// (creates, updates, prunes, power). The executor is the only write gate —
// dry-run/apply semantics live there, not here. This is what makes
// `--diff` / `--dry-run` show the exact intent that `apply --once` would
// perform, including would-be deletes. (plan §2 / §10)
type PlanOptions struct {
	Budget Budget
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
		Actions:  make([]Action, 0, 32),
		Skipped:  make([]Action, 0, 4),
		Deferred: make([]Action, 0, 4),
	}

	// Pass A: desired objects → creates/updates/power.
	for _, r := range desired {
		key := liveKey(r.Node(), r.Ref().Kind, r.ID())
		cfg, present := live.Configs[key]

		if !present {
			params, err := r.ToCreateParams()
			if err != nil {
				return nil, fmt.Errorf("%s: create params: %w", r.Ref(), err)
			}
			p.Actions = append(p.Actions, Action{
				Tier: 0, Kind: r.Ref().Kind, Name: r.Ref().Name, Node: r.Node(), ID: r.ID(),
				What: Create, Params: params,
				Reason:    r.Ref().String() + ": not on PVE; will create",
				LivePower: "", DesiredPower: r.DesiredState(),
			})
			continue
		}

		updParams, stopFirst, changed := r.Drift(cfg)
		if changed {
			reason := r.Ref().String() + ": config drift"
			if stopFirst {
				reason += " (stop-required)"
			}
			p.Actions = append(p.Actions, Action{
				Tier: 0, Kind: r.Ref().Kind, Name: r.Ref().Name, Node: r.Node(), ID: r.ID(),
				What: Update, Params: updParams, StopFirst: stopFirst,
				Reason:    reason,
				LivePower: live.Power[key], DesiredPower: r.DesiredState(),
			})
		}

		// Power transition (independent of config drift).
		if verb := powerDesiredToLive(r.DesiredState(), live.Power[key]); verb != "" {
			what := Start
			if verb == "stop" {
				what = Stop
			}
			p.Actions = append(p.Actions, Action{
				Tier: 0, Kind: r.Ref().Kind, Name: r.Ref().Name, Node: r.Node(), ID: r.ID(),
				What:      what,
				Reason:    fmt.Sprintf("%s: power %q → %q", r.Ref(), live.Power[key], r.DesiredState()),
				LivePower: live.Power[key], DesiredPower: r.DesiredState(),
			})
		}
	}

	// Pass B: prunes (PVE-present, not in desired, pveconform-tagged).
	pruneCandidates := make([]Action, 0, 4)
	desiredByKind := map[schema.Kind]int{}
	for _, r := range desired {
		desiredByKind[r.Ref().Kind]++
	}

	// Build a set of desired identities for fast membership tests.
	desiredSet := map[string]bool{}
	for _, r := range desired {
		// identity by PVE id + node (the authoritative mapping PVE knows)
		desiredSet[liveKey(r.Node(), r.Ref().Kind, r.ID())] = true
		// also accept identity by ref (kind+name)
		desiredSet[r.Ref().String()] = true
	}

	for _, res := range live.Listing {
		if res.Node == "" || res.Vmid <= 0 || res.Type == "" {
			continue
		}
		kind := normalizeKind(res.Type)
		if kind != schema.KindVM && kind != schema.KindLXC {
			// Only VM/LXC are managed by pveconform MVP.
			continue
		}
		key := liveKey(res.Node, kind, res.Vmid)
		cfg := live.Configs[key]
		if cfg == nil {
			// Couldn't read config (transient / listing-only entry).
			// Safety: never prune on a nil config.
			continue
		}
		// membership check: either PVE id+node OR ref matches desired.
		if desiredSet[key] || desiredSet[kindRef(kind, cfg)] {
			continue // still in git → not a prune candidate
		}
		if !HasTag(cfg, schema.PveOwnershipTag) {
			name, _ := cfg["name"].(string)
			p.Skipped = append(p.Skipped, Action{
				Kind: kind, Name: name, Node: res.Node, ID: res.Vmid,
				What:   StatusOnly,
				Reason: "live object without pveconform tag; never touched",
			})
			continue
		}
		nameStr, _ := cfg["name"].(string)
		pruneCandidates = append(pruneCandidates, Action{
			Tier: 9, Kind: kind, Name: nameStr, Node: res.Node, ID: res.Vmid,
			What: Delete, Prune: true,
			Reason:    "present on PVE (pveconform-tagged) but absent in git",
			LivePower: live.Power[keyFor(res.Node, kind, res.Vmid)],
		})
	}

	// Anomaly guard (plan §10): when the desired set for a kind is EMPTY and
	// the count of PVE-live pveconform-tagged orphans of that kind EXCEEDS the
	// per-cycle prune budget, this is the "mass deletion" shape of a probable
	// bad push (someone deleted all manifests of a kind). Suppress prunes for
	// that kind and surface the anomaly. Orphans within the budget with empty
	// desired are treated as ordinary (safe) deletions.
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
				fmt.Sprintf("0 desired %s but %d pveconform-tagged live objects (more than the per-cycle prune budget of %d): probable bad push; suppresses prunes for %s this cycle",
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

	// Deterministic order within tier: node, then id ascending.
	sort.SliceStable(p.Actions, func(i, j int) bool {
		a, b := p.Actions[i], p.Actions[j]
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if a.What != b.What {
			// creates/updates before power, power before nothing else.
			order := map[ActionKind]int{Create: 0, Update: 1, Start: 2, Stop: 3, Delete: 9}
			oi, ob := order[a.What], order[b.What]
			return oi < ob
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		return a.ID < b.ID
	})
	return p, nil
}

// LoadLive reads inventory, then config + power for live VMs/CTs only.
func LoadLive(ctx context.Context, c *pveclient.Client) (*LiveInventory, error) {
	listing, err := c.ClusterResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("cluster resources: %w", err)
	}
	inv := &LiveInventory{
		Configs: map[string]map[string]any{},
		Power:   map[string]string{},
		Listing: listing,
	}
	for _, res := range listing {
		if res.Node == "" || res.Vmid <= 0 || res.Type == "" {
			continue
		}
		kind := normalizeKind(res.Type)
		switch kind {
		case schema.KindVM:
			cfg, err := c.VM().Get(ctx, res.Node, res.Vmid)
			if err != nil {
				if pveclient.IsNotFound(err) {
					continue
				}
				// Read failure: surface but keep going.
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = "read-error: " + err.Error()
				continue
			}
			inv.Configs[keyFor(res.Node, kind, res.Vmid)] = cfg
			if st, sErr := c.VM().Status(ctx, res.Node, res.Vmid); sErr == nil {
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = normalizePower(st.Status)
			}
		case schema.KindLXC:
			cfg, err := c.LXC().Get(ctx, res.Node, res.Vmid)
			if err != nil {
				if pveclient.IsNotFound(err) {
					continue
				}
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = "read-error: " + err.Error()
				continue
			}
			inv.Configs[keyFor(res.Node, kind, res.Vmid)] = cfg
			if st, sErr := c.LXC().Status(ctx, res.Node, res.Vmid); sErr == nil {
				inv.Power[keyFor(res.Node, kind, res.Vmid)] = normalizePower(st.Status)
			}
		}
	}
	return inv, nil
}

// keyFor builds the LiveInventory key.
func keyFor(node string, kind schema.Kind, id int) string {
	return node + "|" + string(kind) + "|" + strconv.Itoa(id)
}

func liveKey(node string, kind schema.Kind, id int) string { return keyFor(node, kind, id) }

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

// inDeferred reports whether action a is already in deferred list d.
func inDeferred(d []Action, a Action) bool {
	for _, x := range d {
		if x.Node == a.Node && x.ID == a.ID && x.Kind == a.Kind {
			return true
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
