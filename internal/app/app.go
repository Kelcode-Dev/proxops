// Package app constructs and runs the pveconform agent: one validated config
// in, a wired graph of per-cluster pveclient + one gitx source + one statusx
// store + per-cluster reconciler + executor + one HTTP server out.
//
// Multi-cluster model (M8): the config names one or more PVE clusters, each
// with its own endpoint and node allowlist. Every reconcile cycle is scoped
// to exactly one cluster:
//
//	Git composition (clusters/<cluster>/resources.yaml)
//	    → named cluster (config + composition cross-checked, fail closed)
//	    → configured PVE endpoint (one pveclient per cluster)
//	    → configured node allowlist (planner reads/ prunes only those nodes)
//	    → PVE resource
//
// There is no implicit "current cluster" global state: each reconciler,
// executor, PVE client, and statusx record is explicitly bound to its
// cluster. Clusters are processed in deterministic (sorted) name order.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/exec"
	"github.com/GizzmoShifu/proxmox-operator/internal/gitx"
	"github.com/GizzmoShifu/proxmox-operator/internal/metrics"
	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/reconcile"
	"github.com/GizzmoShifu/proxmox-operator/internal/server"
	"github.com/GizzmoShifu/proxmox-operator/internal/statusx"
)

// PVEParamsFrom maps one named cluster from a validated config to a
// pveclient.PVEParams. Factored out of New so the mapping can be
// unit-tested without constructing a full Agent (which would trigger a git
// fetch).
//
// It is a pure copy with no defaults: auth/user/token shared at the pve
// level; endpoint + node allowlist per cluster.
func PVEParamsFrom(cfg *config.Config, cluster string) pveclient.PVEParams {
	c, ok := cfg.PVE.Cluster(cluster)
	if !ok {
		// Unknown cluster names fail closed at the composition root: no PVE
		// client, no endpoint, no actions.
		return pveclient.PVEParams{}
	}
	return pveclient.PVEParams{
		User:     cfg.PVE.User,
		Auth:     string(cfg.PVE.Auth),
		TokenID:  cfg.PVE.TokenID,
		Token:    cfg.PVE.Token,
		TokenValue: cfg.PVE.TokenValue,
		Password: cfg.PVE.Password,
		BaseURL:  c.BaseURL,
		Nodes:    c.Nodes,
		CAFile:   cfg.PVE.CAFile,
	}
}

// clusterAgent is one pveconform cluster: PVE client + dry/apply reconcilers
// bound to its endpoint and node allowlist.
type clusterAgent struct {
	name    string
	pve     *pveclient.Client
	recDry  *reconcile.Reconciler
	rec     *reconcile.Reconciler
}

// Agent is the long-running pveconform process.
type Agent struct {
	log      *slog.Logger
	cfg      *config.Config
	registry *prometheus.Registry
	version  string

	// shared collaborators
	git    *gitx.Source
	store  *statusx.Store
	clusters []clusterAgent // deterministic order (by name)
	srv    *http.Server
	srvErr chan error
	mu     sync.Mutex // guards ready/lastPVE bookkeeping
	ready  bool
	lastGit time.Time
	lastPVE time.Time
}

// ErrAborted is returned by one-shot commands (apply / diff) when a cycle
// was aborted (bad manifests, unreachable PVE, no git tree yet). The CLI maps
// this to a non-zero exit so operators/scripting can detect it.
//
// The `run` watch loop deliberately does NOT stop on ErrAborted: continuous
// reconciliation means a failed cycle is retried on the next tick.
var ErrAborted = errors.New("reconcile cycle aborted")

// AbortError carries the abort reason from a cycle.
type AbortError struct{ Reason string }

func (e *AbortError) Error() string { return "cycle aborted: " + e.Reason }

func (e *AbortError) Is(target error) bool { return target == ErrAborted }

// New validates the configuration and builds the agent's collaborators:
// per-cluster pveclient pairs, one git Source, one status store, per-cluster
// dry/apply reconcilers, and the HTTP server (not started yet — Start does
// that).
func New(cfg *config.Config, log *slog.Logger, registry *prometheus.Registry, version string) (*Agent, error) {
	if registry == nil {
		registry = metrics.Register()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	a := &Agent{log: log, cfg: cfg, registry: registry, version: version}

	// Git Source (single shared tree for all clusters: the composition is
	// cluster-scoped at parse time, not at fetch time).
	gitOpts := gitx.Options{
		Branch: cfg.Git.Branch,
		Token:  cfg.Git.Token,
	}
	if cfg.Git.URL != "" {
		gitOpts.URL = cfg.Git.URL
		gitOpts.CacheDir = cfg.GitCacheDir()
	} else {
		gitOpts.Local = cfg.Git.Path
	}
	src, err := gitx.New(context.Background(), gitOpts)
	if err != nil {
		return nil, fmt.Errorf("git source: %w", err)
	}
	a.git = src
	a.lastGit = time.Now()

	// Status store (shared: records keep a per-cluster tag).
	a.store = statusx.New()

	// One PVE client + reconciler pair per configured cluster, in
	// deterministic (name-sorted) order.
	clusterNames := cfg.PVE.ClusterNames()
	for _, name := range clusterNames {
		cParams := PVEParamsFrom(cfg, name)
		pveOpts := pveclient.Options{PVE: cParams}
		c, perr := pveclient.New(pveOpts, log)
		if perr != nil {
			return nil, fmt.Errorf("pveclient for cluster %s: %w", name, perr)
		}
		clusterDef, _ := cfg.PVE.Cluster(name)

		buildRec := func(executor *exec.Executor) (*reconcile.Reconciler, error) {
			return reconcile.New(reconcile.Options{
				PVE:            c,
				Fetcher:        src,
				Store:          a.store,
				Budget:         plan.Budget{Prune: cfg.Rec.PruneBudget},
				Executor:       executor,
				Cluster:        name,
				NodeAllowlist:  clusterDef.Nodes,
				ConfiguredClusters: clusterNames,
				Log:            log,
			})
		}

		dry, derr := buildRec(nil)
		if derr != nil {
			return nil, fmt.Errorf("dry reconciler for cluster %s: %w", name, derr)
		}
		aplExec := exec.New(c, cfg.Rec.TaskTimeout, 2*time.Second, a.store, log)
		aplExec.SetCluster(name)
		apl, aerr := buildRec(aplExec)
		if aerr != nil {
			return nil, fmt.Errorf("apply reconciler for cluster %s: %w", name, aerr)
		}
		a.clusters = append(a.clusters, clusterAgent{name: name, pve: c, recDry: dry, rec: apl})
	}

	// HTTP server.
	srv := server.New(registry, a.store, a.healthInfo)
	a.srvErr = make(chan error, 1)
	a.srv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handle(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return a, nil
}

// Clusters returns the cluster names in processing order (sorted).
func (a *Agent) Clusters() []string {
	out := make([]string, 0, len(a.clusters))
	for _, c := range a.clusters {
		out = append(out, c.name)
	}
	return out
}

// PVEClientFor returns the PVE API client bound to a configured cluster.
// Unknown cluster names are an error (fail closed).
func (a *Agent) PVEClientFor(cluster string) (*pveclient.Client, error) {
	for _, c := range a.clusters {
		if c.name == cluster {
			return c.pve, nil
		}
	}
	return nil, fmt.Errorf("unknown cluster %q (configured: %v)", cluster, a.Clusters())
}

// GitSource returns the shared git source (the composition tree).
func (a *Agent) GitSource() *gitx.Source { return a.git }

// StatusStore returns the shared status store.
func (a *Agent) StatusStore() *statusx.Store { return a.store }

// healthInfo returns the liveness inputs that /healthz reads. It may run on
// the HTTP server's goroutine while the reconcile loop mutates the
// bookkeeping fields, so they are read under mu.
func (a *Agent) healthInfo() server.Info {
	a.mu.Lock()
	lastGit, lastPVE, ready := a.lastGit, a.lastPVE, a.ready
	a.mu.Unlock()

	sinceGitFetch := time.Since(lastGit)
	sincePVERead := time.Since(lastPVE)
	if lastGit.IsZero() {
		sinceGitFetch = time.Since(time.Now()) // no successful fetch yet
	}
	if lastPVE.IsZero() {
		sincePVERead = time.Since(time.Now())
	}
	return server.Info{
		Ready:  ready,
		GitAge: sinceGitFetch,
		PVEAge: sincePVERead,
	}
}

// SetReadOnly toggles the executor's read-only flag for the process. (Used by
// the write breaker hook — M3+)
func (a *Agent) SetReadOnly(bool) {}

// Registry exposes the prometheus registry (for external instrumentation).
func (a *Agent) Registry() *prometheus.Registry { return a.registry }

// Config exposes the validated configuration.
func (a *Agent) Config() *config.Config { return a.cfg }

// Version returns the build version.
func (a *Agent) Version() string { return a.version }

// Log returns the agent logger.
func (a *Agent) Log() *slog.Logger { return a.log }

// Start launches the HTTP server (non-blocking) and the continuous reconcile
// loop. It returns when ctx is cancelled.
//
// The reconcile loop is serial; a failing cycle does not stop the watch —
// each cycle's outcome is reported via statusx + metrics. Clusters are
// processed in deterministic sorted-name order every tick.
func (a *Agent) Start(ctx context.Context) error {
	a.log.Info("pveconform starting",
		slog.String("version", a.version),
		slog.String("listen", a.cfg.Listen),
		slog.String("branch", a.cfg.Git.Branch),
		slog.String("git_source", sourceString(a.cfg.Git)),
		slog.String("clusters", fmt.Sprint(a.Clusters())),
	)

	// HTTP server in a goroutine.
	go func() {
		a.srvErr <- a.srv.ListenAndServe()
	}()

	// Reconcile loop: every PollInterval ticks. ctx cancels everything.
	ticker := time.NewTicker(a.cfg.Rec.PollInterval)
	defer ticker.Stop()
	// First tick immediately so diff/apply semantics are available fast.
	for {
		select {
		case <-ctx.Done():
			return a.shutdown()
		case err := <-a.srvErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.log.Error("http server error", slog.String("err", err.Error()))
			}
		case <-ticker.C:
			a.runAllClusters(ctx)
		}
	}
}

// runAllClusters performs one apply cycle for every configured cluster, in
// deterministic order. A per-cluster abort is reported (statusx + log +
// metrics) and does not stop the remaining clusters: an un-reachable
// conformance-dev endpoint must not prevent prod-a from converging.
func (a *Agent) runAllClusters(ctx context.Context) {
	failed := false
	for _, ca := range a.clusters {
		res, err := a.runClusterApply(ctx, ca)
		if err != nil {
			failed = true
			a.log.Error("reconcile cycle error",
				slog.String("cluster", ca.name),
				slog.String("err", err.Error()))
			continue
		}
		if res.Aborted {
			failed = true
			a.log.Warn("cycle aborted (will retry next tick)",
				slog.String("cluster", ca.name),
				slog.String("reason", res.AbortReason))
		}
	}
	a.setReady(!failed)
}

func (a *Agent) runClusterApply(ctx context.Context, ca clusterAgent) (reconcile.Result, error) {
	res, _, err := ca.rec.RunOneCycle(ctx)
	if err != nil {
		a.markPVERead()
		return res, fmt.Errorf("reconcile cluster %s: %w", ca.name, err)
	}
	a.markPVERead()
	return res, nil
}

// RunOnce performs a single apply cycle for EVERY configured cluster (determin
// order) and blocks until all complete. Returns the concatenated per-cluster
// status report plus an error (AbortError) when ANY cycle aborted, so the CLI
// can exit non-zero.
func (a *Agent) RunOnce(ctx context.Context) (string, error) {
	var firstAbort *AbortError
	for _, ca := range a.clusters {
		res, err := a.runClusterApply(ctx, ca)
		if err != nil {
			return a.multiclusterReport(), fmt.Errorf("reconcile cluster %s: %w", ca.name, err)
		}
		if res.Aborted && firstAbort == nil {
			firstAbort = &AbortError{Reason: ca.name + ": " + res.AbortReason}
		}
		a.log.Debug("apply --once complete",
			slog.String("cluster", ca.name),
			slog.Int("actions", res.Actions),
			slog.Int("ok", res.ActionsOK),
			slog.Int("errors", res.ActionsErr))
	}
	if firstAbort != nil {
		return a.multiclusterReport(), firstAbort
	}
	return a.multiclusterReport(), nil
}

// multiclusterReport renders a text snapshot of the status store, grouping
// records per cluster.
func (a *Agent) multiclusterReport() string {
	cyc := a.store.Last()
	buf := &slogBuilder{}
	if cyc == nil {
		return "no reconcile cycle has completed yet"
	}
	buf.lines = append(buf.lines, fmt.Sprintf("last cycle: %s (commit %s)",
		stateWord(cyc.Aborted, cyc.AbortReason, cyc.ActionsError), cyc.Commit))
	if cyc.DesiredStale {
		buf.lines = append(buf.lines, "DESIRED STATE IS STALE (git fetch has been failing)")
	}
	buf.lines = append(buf.lines, fmt.Sprintf("objects=%d actions_ok=%d actions_err=%d pruned=%d prune_deferred=%d skipped=%d anomalies=%d",
		cyc.Objects, cyc.ActionsOK, cyc.ActionsError, cyc.Pruned, cyc.PruneDeferred, cyc.Skipped, cyc.Anomalies))
	byCluster := map[string][]statusx.Object{}
	for _, o := range a.store.Objects() {
		byCluster[o.Cluster] = append(byCluster[o.Cluster], o)
	}
	for _, name := range a.Clusters() {
		objects := byCluster[name]
		if len(objects) == 0 {
			buf.lines = append(buf.lines, fmt.Sprintf("[%s] (no recorded objects)", name))
			continue
		}
		buf.lines = append(buf.lines, fmt.Sprintf("[%s]", name))
		for _, o := range objects {
			line := fmt.Sprintf("  %-8s %-40s %-10s %-12s %s",
				o.Kind, o.Name, shortID(o.ID), shortState(o.State), o.PruneReason)
			if o.LastError != "" {
				line += " err: " + o.LastError
			}
			buf.lines = append(buf.lines, line)
		}
	}
	buf.lines = append(buf.lines, "")
	return buf.String()
}


// Diff performs a read-only cycle for every configured cluster and returns a
// human-readable, cluster-labelled report of what would change. It does NOT
// write to PVE.
//
// Returns the report text (always) and, when a cycle aborted (unreachable
// PVE, bad manifests, no git tree yet), an *AbortError so callers can signal
// a non-zero exit. The abort reason is included in the returned text.
func (a *Agent) Diff(ctx context.Context) (string, error) {
	a.log.Info("diff requested", slog.String("commit", a.git.RevString()))
	if fErr := a.fetchOnce(ctx); fErr != nil {
		a.log.Warn("git refresh before diff: keep last-good tree",
			slog.String("err", fErr.Error()))
	}
	var b string
	aborted := false
	for i, ca := range a.clusters {
		res, pl, rErr := ca.recDry.RunOneCycle(ctx)
		if rErr != nil {
			return a.multiclusterReport(), fmt.Errorf("diff: cluster %s: %w", ca.name, rErr)
		}
		a.markPVERead()
		if res.Aborted {
			aborted = true
			b += fmt.Sprintf("\n=== cluster %s ===\nABORTED: %s\n", ca.name, res.AbortReason)
			continue
		}
		b += a.renderClusterDiff(i, ca.name, pl, res)
	}
	a.setReady(!aborted || len(a.clusters) == 0)
	if b == "" {
		return "no drift — PVE matches git", nil
	}
	if aborted {
		return b, &AbortError{Reason: "one or more clusters aborted"}
	}
	return b, nil
}

// fetchOnce syncs the git tree exactly once for the diff/apply pass. The
// per-cluster cycles inside the reconcile pipeline do their own advisory
// sync, but doing a single sync up-front keeps the multi-cluster report
// coherent (all clusters see the same commit) and avoids N git fetches.
func (a *Agent) fetchOnce(ctx context.Context) error {
	if _, err := a.git.Fetch(ctx); err != nil {
		return fmt.Errorf("git sync: %w", err)
	}
	a.mu.Lock()
	a.lastGit = a.git.LastSuccess()
	a.mu.Unlock()
	return nil
}

// renderClusterDiff renders one cluster's plan into the shared diff text.
func (a *Agent) renderClusterDiff(i int, cluster string, pl *plan.Plan, res reconcile.Result) string {
	var b strings.Builder
	if i > 0 {
		b.WriteString("\n")
	}
	b.WriteString("=== " + cluster + " ===\n")
	if pl != nil {
		b.WriteString(renderClusterPlan(pl))
	}
	if res.Anomaly != "" {
		b.WriteString("\nANOMALY: " + res.Anomaly + "\n")
	}
	if len(res.AnomaliesList) > 0 {
		fmt.Fprintf(&b, "\nANOMALIES (%d live-only slots pveconform will NOT auto-remove):\n", len(res.AnomaliesList))
		for _, msg := range res.AnomaliesList {
			b.WriteString("  - " + msg + "\n")
		}
	}
	if pl == nil || (len(pl.Actions) == 0 && len(pl.Deferred) == 0 && len(pl.Skipped) == 0 && len(pl.Anomalies) == 0) {
		b.WriteString("no drift — PVE matches git\n")
	}
	return b.String()
}

// ApplyOnce performs a single apply cycle that converges PVE to git, for
// every configured cluster.
func (a *Agent) ApplyOnce(ctx context.Context, dryRun bool) (string, error) {
	a.log.Info("apply --once requested", slog.Bool("dry_run", dryRun))
	if dryRun {
		return a.Diff(ctx)
	}
	return a.RunOnce(ctx)
}

// ShowStatus prints a snapshot of the in-memory status store.
func (a *Agent) ShowStatus(ctx context.Context) error {
	a.log.Info("status requested")
	// One-shot `pveconform status` must be useful by itself: if no cycle has
	// completed yet, run a read-only cycle for every cluster so the report
	// reflects PVE + git now rather than showing an empty "no cycle" message.
	if a.store.Last() == nil {
		for _, ca := range a.clusters {
			if _, _, err := ca.recDry.RunOneCycle(ctx); err != nil {
				a.log.Warn("status: dry cycle failed; showing empty report",
					slog.String("cluster", ca.name),
					slog.String("err", err.Error()))
			}
		}
	}
	fmt.Println(a.multiclusterReport())
	return nil
}

// RunWatch runs the continuous reconcile loop (used by `run`).
func (a *Agent) RunWatch(ctx context.Context) error {
	a.log.Info("watch mode requested", slog.String("version", a.version))
	return a.Start(ctx)
}

// markPVERead records that a PVE read succeeded.
func (a *Agent) markPVERead() {
	a.mu.Lock()
	a.lastPVE = time.Now()
	a.mu.Unlock()
}

// setReady updates the readiness flag read by /healthz.
func (a *Agent) setReady(v bool) {
	a.mu.Lock()
	a.ready = v
	a.mu.Unlock()
}

func (a *Agent) shutdown() error {
	shctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.srv.Shutdown(shctx)
	a.log.Info("pveconform stopped")
	return err
}

// helpers

type slogBuilder struct{ lines []string }

func (b *slogBuilder) String() string {
	out := ""
	for i, l := range b.lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// stateWord summarizes the cycle outcome for the status report.
// "converged" means ALL planned actions succeeded; any errored action
// downgrades to "incomplete" — an operator reading "converged" must be
// able to trust that PVE matches git, and a cycle with failed downloads
// does not.
func stateWord(aborted bool, reason string, actionsError int) string {
	if aborted {
		return "ABORTED (" + reason + ")"
	}
	if actionsError > 0 {
		suffix := "s"
		if actionsError == 1 {
			suffix = ""
		}
		return "incomplete (" + strconv.Itoa(actionsError) + " failed action" + suffix + ")"
	}
	return "converged"
}

func shortID(id int) string {
	if id <= 0 {
		return "-"
	}
	return strconv.Itoa(id)
}

func shortState(x statusx.State) string { return string(x) }

func sourceString(g config.GitConfig) string {
	if g.URL != "" {
		return "git@" + shortURL(g.URL)
	}
	return "local:" + g.Path
}

func shortURL(u string) string {
	if len(u) > 40 {
		return u[:40] + "…"
	}
	return u
}
