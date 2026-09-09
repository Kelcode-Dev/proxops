// Package reconcile is the pveconform pipeline glue: one reconcile cycle.
//
// A cycle is:
//  1. git sync (ADVISORY): on failure keep the last-good tree, flag
//     desiredStale. Zero last-good tree → not-ready, no writes.
//  2. parse + validate the work tree → desired index. HARD error → abort
//     cycle, keep last-good index in memory (fail-closed).
//  3. load PVE live inventory (listing + per-object config/power). HARD
//     error → abort cycle.
//  4. plan: pure desired-vs-actual delta (tags gate, budget, anomaly guard
//     are all planner-owned; plan.go).
//  5. execute: via the injected Executor (a nil executor = dry-run; the
//     plan itself is returned to the caller for --diff rendering).
//  6. report: statusx + metrics + structured log.
//
// The reconciler is stateless: restart ⇒ full re-diff from git + PVE.
package reconcile

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/GizzmoShifu/proxmox-operator/internal/exec"
	"github.com/GizzmoShifu/proxmox-operator/internal/metrics"
	"github.com/GizzmoShifu/proxmox-operator/internal/parse"
	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
	"github.com/GizzmoShifu/proxmox-operator/internal/statusx"
)

// Fetcher is the git-source seam. gitx.Source implements it directly
// (Fetch / RevString / WorkDir).
type Fetcher interface {
	// Fetch fetches/resets the work tree to the branch head. Advisory:
	// errors are returned but the reconciler continues with the existing
	// work tree.
	Fetch(ctx context.Context) (changed bool, err error)
	RevString() string
	WorkDir() string
}

// Reconciler runs reconcile cycles.
type Reconciler struct {
	PVE     *pveclient.Client
	Fetcher Fetcher
	Budget  plan.Budget
	Store   *statusx.Store
	// Cluster is the pveconform cluster name this reconciler owns. It is
	// part of every statusx.Object written so a multi-cluster agent keeps
	// per-cluster records in one store.
	Cluster string
	// NodeAllowlist, when non-empty, restricts which PVE node names the
	// agent will reconcile against. A manifest whose spec.node is not in
	// this list aborts the cycle (fail-closed) with a clear message.
	// This is the runtime enforcement of the per-cluster node boundary —
	// config.Validate() checks the shape only. Every planner decision
	// (read, write, prune candidate) is scoped to this allowlist.
	NodeAllowlist []string
	// ConfiguredClusters is the full set of cluster names present in
	// pve.clusters of the validated config. Needed by BuildClusterIndex
	// to cross-check the composition against the endpoint list (a
	// composition with no configured endpoint fails closed).
	ConfiguredClusters []string
	log                *slog.Logger

	exec         *exec.Executor // nil ⇒ dry-run
	lastGood     *parse.Index
	lastCommit   string
	desiredStale bool
}

// Options configure a Reconciler.
type Options struct {
	PVE           *pveclient.Client
	Fetcher       Fetcher
	Store         *statusx.Store
	Budget        plan.Budget
	Executor      *exec.Executor // nil = dry-run
	Cluster       string
	NodeAllowlist []string
	// ConfiguredClusters is the full pve.clusters key set (for the
	// BuildClusterIndex cross-check).
	ConfiguredClusters []string
	Log                *slog.Logger
}

// New builds a Reconciler.
func New(o Options) (*Reconciler, error) {
	if o.PVE == nil || o.Fetcher == nil || o.Store == nil {
		return nil, fmt.Errorf("reconcile.New: PVE, Fetcher and Store are required")
	}
	if o.Budget.Prune <= 0 {
		o.Budget = plan.DefaultBudget()
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Cluster == "" {
		// Legacy single-cluster test harnesses construct a reconciler
		// without a cluster name; pin a valid identity so
		// BuildClusterIndex (and the composition cross-check) work.
		o.Cluster = "default"
		if len(o.ConfiguredClusters) == 0 {
			o.ConfiguredClusters = []string{"default"}
		}
	}
	return &Reconciler{
		PVE:                o.PVE,
		Fetcher:            o.Fetcher,
		Budget:             o.Budget,
		Store:              o.Store,
		Cluster:            o.Cluster,
		NodeAllowlist:      o.NodeAllowlist,
		ConfiguredClusters: o.ConfiguredClusters,
		exec:               o.Executor,
		log:                o.Log,
	}, nil
}

// Result describes one cycle's outcome for reporting.
type Result struct {
	Aborted       bool
	AbortReason   string
	Commit        string
	DesiredStale  bool
	Objects       int
	Actions       int
	ActionsOK     int
	ActionsErr    int
	Pruned        int
	PruneDeferred int
	Skipped       int
	Anomalies     int      // live-only-slot observations recorded this cycle
	AnomaliesList []string // human reasons (one per anomaly; empty when none)
	Anomaly       string   // full-cycle anomaly-guard text (empty-desired)
}

// RunOneCycle performs a single reconcile cycle.
//
// Returns:
//   - the completed/aborted Result
//   - a non-nil plan (for --diff / --dry-run rendering) when we reached the
//     planning stage; nil when aborted earlier
//   - a non-nil error only for *fatal* conditions (caller should log and
//     stop the watch loop); in-cycle parse/read failures are reported via
//     Result.Aborted, not via error (the reconcile loop itself is healthy).
func (r *Reconciler) RunOneCycle(ctx context.Context) (Result, *plan.Plan, error) {
	res := Result{Commit: r.lastCommit}

	// (1) advisory git sync.
	_, syncErr := r.Fetcher.Fetch(ctx)
	if syncErr != nil {
		if r.lastGood == nil {
			res.Aborted = true
			res.AbortReason = "git sync failed and no previous good tree available"
			r.Store.BeginCycle(res.Commit, res.DesiredStale, r.exec == nil)
			r.Store.FinishCycle(true, res.AbortReason)
			metrics.CyclesTotal.WithLabelValues("not_ready").Inc()
			r.log.Error("git not ready; refusing to reconcile",
				slog.String("err", syncErr.Error()),
				slog.String("commit", res.Commit))
			return res, nil, nil // not fatal: retry next cycle
		}
		res.DesiredStale = true
		r.desiredStale = true
		metrics.DesiredStale.Set(1)
		r.log.Warn("git sync failed; reconciling against last-good tree",
			slog.String("err", syncErr.Error()))
	} else {
		r.desiredStale = false
		res.DesiredStale = false
		metrics.DesiredStale.Set(0)
		metrics.GitLastFetchAgeSeconds.Set(0)
	}
	res.Commit = r.Fetcher.RevString()
	r.Store.BeginCycle(res.Commit, res.DesiredStale, r.exec == nil)

	// (2) parse + validate the cluster's composition. Parse errors are
	// fail-closed for this cluster's cycle (a half-valid desired set could
	// prune against missing objects on this cluster). Other clusters' cycles
	// are independent: they parse their own compositions.
	idx, err := parse.BuildClusterIndex(r.Fetcher.WorkDir(), r.Cluster, r.ConfiguredClusters)
	if err != nil {
		res.Aborted = true
		res.AbortReason = "manifest parse error: " + err.Error()
		r.Store.FinishCycle(true, res.AbortReason)
		metrics.CyclesTotal.WithLabelValues("parse_error").Inc()
		r.log.Error("manifest parse error; aborting cycle (keeping last-good tree)",
			slog.String("err", err.Error()),
			slog.String("commit", res.Commit))
		return res, nil, nil // cycle aborted by design, not a fatal error
	}
	r.lastGood = idx
	r.lastCommit = res.Commit

	// (2b) Node allowlist: a manifest referencing a spec.node that is not in
	// pve.nodes (when set) is a mis-route risk — abort before any PVE call
	// instead of failing later with confusing per-request errors.
	if err := r.checkNodeAllowlist(idx.List()); err != nil {
		res.Aborted = true
		res.AbortReason = "unknown node: " + err.Error()
		r.Store.FinishCycle(true, res.AbortReason)
		metrics.CyclesTotal.WithLabelValues("unknown_node").Inc()
		r.log.Error("manifest references a node outside pve.nodes; aborting cycle",
			slog.String("err", err.Error()),
			slog.String("commit", res.Commit))
		return res, nil, nil
	}

	// (3) load live PVE inventory + ISO presence. scoped to this cluster's
	// node allowlist: objects on other nodes are not read, not counted,
	// and never become prune candidates for THIS cluster.
	live, err := plan.LoadLive(ctx, r.PVE, idx.List(), r.NodeAllowlist)
	if err != nil {
		res.Aborted = true
		res.AbortReason = "pve inventory read failed: " + err.Error()
		r.Store.FinishCycle(true, res.AbortReason)
		metrics.CyclesTotal.WithLabelValues("pve_read_error").Inc()
		r.log.Error("pve inventory read failed; aborting cycle", slog.String("err", err.Error()))
		return res, nil, nil
	}
	metrics.PVELastAuthAgeSeconds.Set(0)

	// (4) plan (pure).
	levels := idx.Levels()
	pl, err := plan.PlanActions(ctx, idx.List(), live, plan.PlanOptions{
		Budget:        r.Budget,
		Levels:        func(ref schema.Ref) int {
			if lvl, ok := levels[ref]; ok {
				return lvl
			}
			return 0
		},
		Edges:         idx.EdgesFor,
		NodeAllowlist: r.NodeAllowlist,
	})
	if err != nil {
		res.Aborted = true
		res.AbortReason = "plan error: " + err.Error()
		r.Store.FinishCycle(true, res.AbortReason)
		metrics.CyclesTotal.WithLabelValues("plan_error").Inc()
		return res, nil, fmt.Errorf("plan: %w", err) // fatal, unexpected
	}
	if pl.Anomaly != "" {
		res.Anomaly = pl.Anomaly
		metrics.Anomalies.WithLabelValues("empty_desired").Inc()
		r.log.Error("planner anomaly detected; prunes suppressed for affected kinds",
			slog.String("anomaly", pl.Anomaly))
	}

	// Anomalies: non-destructive live-only-slot observations. NEVER PVE
	// writes — pveconform deliberately does not auto-delete a disk or
	// mount point the manifest does not declare (PVE's scsiN=none only
	// detaches; the underlying LVM volume remains). They are surfaced on
	// /status (state=anomalous), /metrics (pveconform_anomalies_total{
	// type="live_only_slot"}), and this log — the operator removes them
	// manually if that is the intended move.
	// Record EVERY desired object in /status so the operator sees the full
	// fleet, not just what's churning. Objects with a planned write are
	// "drift"; with a prune are "orphan"; with an anomaly are "anomalous"
	// (set above); everything else that is present+converged or absent+not-
	// planned is "converged" (the steady state). The next apply/execute pass
	// refines these to their true end-state; this gives /status a complete
	// object list every cycle.
	kindNodeID := map[string]bool{}
	for _, a := range pl.Actions {
		kindNodeID[string(a.Kind)+"/"+a.Name] = true
	}
	// desired set
	byName := map[schema.Kind]map[string]schema.Resource{}
	for _, d := range idx.List() {
		ref := d.Ref()
		if byName[ref.Kind] == nil {
			byName[ref.Kind] = map[string]schema.Resource{}
		}
		byName[ref.Kind][ref.Name] = d
	}
	for kind, names := range byName {
		for name, d := range names {
			key := string(kind) + "/" + name
			if !kindNodeID[key] {
				// No planned write -> already converged (or will confirm soon).
				r.Store.SetObject(&statusx.Object{
					Cluster: r.Cluster, Kind: d.Ref().Kind, Name: name, Node: d.Node(), ID: d.ID(),
					State: statusx.Converged,
				})
			} else {
				r.Store.SetObject(&statusx.Object{
					Cluster: r.Cluster, Kind: d.Ref().Kind, Name: name, Node: d.Node(), ID: d.ID(),
					State: statusx.Drift,
				})
			}
		}
	}

	for _, an := range pl.Anomalies {
		res.Anomalies++
		res.AnomaliesList = append(res.AnomaliesList, an.Reason)
		metrics.AnomaliesTotal.WithLabelValues("live_only_slot").Inc()
		r.Store.BumpAnomaly()
		r.Store.SetObject(&statusx.Object{
			Cluster: r.Cluster, Kind: an.Kind, Name: an.Name, Node: an.Node, ID: an.ID,
			State: statusx.Anomalous, LastAction: string(an.What),
			LastError: an.Reason,
		})
		r.log.Warn("pveconform.anomaly",
			slog.String("kind", string(an.Kind)),
			slog.String("name", an.Name),
			slog.String("node", an.Node),
			slog.Int("id", an.ID),
			slog.String("reason", an.Reason))
	}

	// (5) execute or dry-run.
	if r.exec == nil {
		// dry-run: report the plan; executor will not write.
		res.Actions = len(pl.Actions)
		res.Skipped = len(pl.Skipped)
		res.PruneDeferred = len(pl.Deferred)
		for i := 0; i < len(pl.Skipped); i++ {
			r.Store.BumpSkipped()
		}
		// Prune-deferred bumps are recorded by the executor's
		// recordDeferred (one BumpPruneDeferred per Deferred action);
		// don't double-count here.
		r.Store.FinishCycle(false, "")
		metrics.CyclesTotal.WithLabelValues("dry_run").Inc()
		r.log.Info("dry-run cycle complete",
			slog.Int("objects", len(idx.List())),
			slog.Int("actions", res.Actions),
			slog.Int("skipped", res.Skipped),
			slog.Int("prune_deferred", res.PruneDeferred),
			slog.Int("anomalies", res.Anomalies),
			slog.String("anomaly", res.Anomaly))
		return res, pl, nil
	}

	results := r.exec.Run(ctx, pl)
	res.Actions = len(pl.Actions)
	res.Skipped = len(pl.Skipped)
	for i := 0; i < res.Skipped; i++ {
		r.Store.BumpSkipped()
	}
	res.PruneDeferred = len(pl.Deferred)
	for _, rr := range results {
		if rr.OK {
			res.ActionsOK++
		} else {
			res.ActionsErr++
		}
	}
	// count prunes executed successfully
	for _, rr := range results {
		if rr.OK && rr.Action.Prune {
			res.Pruned++
		}
	}

	// (6) report.
	r.Store.FinishCycle(false, "")
	metrics.CyclesTotal.WithLabelValues("ok").Inc()
	r.log.Info("reconcile cycle complete",
		slog.Int("objects", len(idx.List())),
		slog.Int("actions", res.Actions),
		slog.Int("ok", res.ActionsOK),
		slog.Int("errors", res.ActionsErr),
		slog.Int("pruned", res.Pruned),
		slog.Int("prune_deferred", res.PruneDeferred),
		slog.Int("skipped", res.Skipped),
		slog.Int("anomalies", res.Anomalies),
		slog.String("anomaly", res.Anomaly),
		slog.String("commit", res.Commit),
		slog.Bool("stale", res.DesiredStale))
	return res, pl, nil
}

// LastGood returns the last successfully parsed desired index (nil before
// the first successful parse).
func (r *Reconciler) LastGood() *parse.Index { return r.lastGood }

// LastCommit is the last successfully synced git commit.
func (r *Reconciler) LastCommit() string { return r.lastCommit }

// DesiredStale reports whether the reconciler is operating on a cached tree
// because git sync has been failing.
func (r *Reconciler) DesiredStale() bool { return r.desiredStale }

// checkNodeAllowlist rejects any manifest whose declared PVE nodes are not
// all in NodeAllowlist (when that slice is non-empty). Multi-node artifacts
// (ISO / CTTemplate with spec.nodes) check every declared node, not just the
// primary. VM / LXC have a single spec.node. Empty allowlist = all nodes
// accepted (MVP default until operators pin a list).
//
// Returns an error naming the first-offending (kind, name, node) triple.
func (r *Reconciler) checkNodeAllowlist(resources []schema.Resource) error {
	if len(r.NodeAllowlist) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(r.NodeAllowlist))
	for _, n := range r.NodeAllowlist {
		allowed[n] = true
	}
	for _, res := range resources {
		for _, node := range res.Nodes() {
			if node != "" && !allowed[node] {
				return fmt.Errorf("%s references node %q which is not in pve.clusters.%s.nodes (allowed: %v)",
					res.Ref().String(), node, r.Cluster, r.NodeAllowlist)
			}
		}
	}
	return nil
}
