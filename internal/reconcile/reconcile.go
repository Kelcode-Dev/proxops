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
	log     *slog.Logger

	exec         *exec.Executor // nil ⇒ dry-run
	lastGood     *parse.Index
	lastCommit   string
	desiredStale bool
}

// Options configure a Reconciler.
type Options struct {
	PVE      *pveclient.Client
	Fetcher  Fetcher
	Store    *statusx.Store
	Budget   plan.Budget
	Executor *exec.Executor // nil = dry-run
	Log      *slog.Logger
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
	return &Reconciler{
		PVE:     o.PVE,
		Fetcher: o.Fetcher,
		Budget:  o.Budget,
		Store:   o.Store,
		exec:    o.Executor,
		log:     o.Log,
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
	Anomaly       string
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

	// (2) parse + validate. Parse errors are fail-closed for the whole cycle
	// (a half-valid desired set could prune against missing objects).
	idx, err := parse.BuildIndex(r.Fetcher.WorkDir())
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

	// (3) load live PVE inventory + ISO presence.
	live, err := plan.LoadLive(ctx, r.PVE, idx.List())
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
	pl, err := plan.PlanActions(ctx, idx.List(), live, plan.PlanOptions{Budget: r.Budget})
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

	// Record desired-state objects so /status shows intended-but-yet-created.
	for _, a := range pl.Actions {
		if a.What == plan.Create || a.What == plan.Update {
			r.Store.SetObject(&statusx.Object{
				Kind: a.Kind, Name: a.Name, Node: a.Node, ID: a.ID,
				State: statusx.Drift, LastAction: string(a.What),
			})
		}
	}

	// (5) execute or dry-run.
	if r.exec == nil {
		// dry-run: report the plan; executor will not write.
		res.Actions = len(pl.Actions)
		res.Skipped = len(pl.Skipped)
		res.PruneDeferred = len(pl.Deferred)
		r.Store.FinishCycle(false, "")
		metrics.CyclesTotal.WithLabelValues("dry_run").Inc()
		r.log.Info("dry-run cycle complete",
			slog.Int("objects", len(idx.List())),
			slog.Int("actions", res.Actions),
			slog.Int("skipped", res.Skipped),
			slog.Int("prune_deferred", res.PruneDeferred),
			slog.String("anomaly", res.Anomaly))
		return res, pl, nil
	}

	results := r.exec.Run(ctx, pl)
	res.Actions = len(pl.Actions)
	res.Skipped = len(pl.Skipped)
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
		slog.String("anomaly", res.Anomaly),
		slog.String("commit", res.Commit),
		slog.Bool("stale", res.DesiredStale))
	return res, pl, nil
}

// LastGood is the last successfully parsed desired index (may be nil before
// the first successful parse).
func (r *Reconciler) LastGood() *parse.Index { return r.lastGood }

// LastCommit is the last successfully synced git commit.
func (r *Reconciler) LastCommit() string { return r.lastCommit }

// DesiredStale reports whether the reconciler is operating on a cached tree
// because git sync has been failing.
func (r *Reconciler) DesiredStale() bool { return r.desiredStale }
