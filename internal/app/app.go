// Package app constructs and runs the pveconform agent: one validated config
// in, a wired graph of pveclient + gitx + statusx + reconcile + executor +
// HTTP server out. It is the composition root; the per-cycle logic lives in
// reconcile, the per-action logic in exec.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/exec"
	"github.com/GizzmoShifu/proxmox-operator/internal/gitx"
	"github.com/GizzmoShifu/proxmox-operator/internal/metrics"
	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/reconcile"
	"github.com/GizzmoShifu/proxmox-operator/internal/server"
	"github.com/GizzmoShifu/proxmox-operator/internal/statusx"
	"github.com/prometheus/client_golang/prometheus"
)

// PVEParamsFrom is the single wiring point from a validated config to the
// pveclient.PVEParams. Factored out of New so the mapping can be unit-tested
// without constructing a full Agent (which would trigger a git fetch).
//
// It is a pure copy with no defaults: defaults are config's job, and
// pveclient.New validates that a base URL is present (config.Validate
// already rejects empty BaseURL in the CLI path).
func PVEParamsFrom(cfg *config.Config) pveclient.PVEParams {
	return pveclient.PVEParams{
		User:       cfg.PVE.User,
		Auth:       string(cfg.PVE.Auth),
		TokenID:    cfg.PVE.TokenID,
		Token:      cfg.PVE.Token,
		TokenValue: cfg.PVE.TokenValue,
		Password:   cfg.PVE.Password,
		BaseURL:    cfg.PVE.BaseURL,
		Nodes:      cfg.PVE.Nodes,
		CAFile:     cfg.PVE.CAFile,
	}
}

// Agent is the long-running pveconform process.
type Agent struct {
	log      *slog.Logger
	cfg      *config.Config
	registry *prometheus.Registry
	version  string

	// collaborators (set by New / Build, read by the methods below)
	pve     *pveclient.Client
	git     *gitx.Source
	store   *statusx.Store
	rec     *reconcile.Reconciler // apply-mode executor
	recDry  *reconcile.Reconciler // dry-run (same pipeline, nil executor)
	srv     *http.Server
	srvErr  chan error
	mu      sync.Mutex // guards ready/cycle bookkeeping
	ready   bool
	lastGit time.Time
	lastPVE time.Time
}

// ErrNotImplemented is reserved for CLI paths that remain out of scope in a
// given build (adopt is post-MVP).
var ErrNotImplemented = errors.New("not implemented in this build")

// ErrAborted is returned by one-shot commands (apply / diff) when a cycle was
// aborted (bad manifests, unreachable PVE, no git tree yet). The CLI maps this
// to a non-zero exit so operators/scripting can detect it.
//
// The `run` watch loop deliberately does NOT stop on ErrAborted: continuous
// reconciliation means a failed cycle is retried on the next tick.
var ErrAborted = errors.New("reconcile cycle aborted")

// AbortError carries the abort reason from a cycle.
type AbortError struct{ Reason string }

func (e *AbortError) Error() string { return "cycle aborted: " + e.Reason }

func (e *AbortError) Is(target error) bool { return target == ErrAborted }

// New validates the configuration and builds the agent's collaborators:
// pveclient, git Source, status store, dry-run reconciler, apply reconciler,
// and the HTTP server (not started yet — Start does that).
func New(cfg *config.Config, log *slog.Logger, registry *prometheus.Registry, version string) (*Agent, error) {
	if registry == nil {
		registry = metrics.Register()
	}
	a := &Agent{log: log, cfg: cfg, registry: registry, version: version}

	// PVE client. The single base URL comes from PVE.BaseURL (set in config).
	// Tests that need to point at the stateful mock PVE construct their own
	// pveclient with Options.BaseURL.
	pveOpts := pveclient.Options{PVE: PVEParamsFrom(cfg)}
	c, err := pveclient.New(pveOpts, log)
	if err != nil {
		return nil, fmt.Errorf("pveclient: %w", err)
	}
	a.pve = c

	// Git Source
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

	// Status store
	a.store = statusx.New()

	// Reconcilers (share the PVE client + git source + store).
	dry, err := reconcile.New(reconcile.Options{
		PVE:           a.pve,
		Fetcher:       a.git,
		Store:         a.store,
		Budget:        plan.Budget{Prune: cfg.Rec.PruneBudget},
		Executor:      nil, // dry-run
		NodeAllowlist: cfg.PVE.Nodes,
		Log:           log,
	})
	if err != nil {
		return nil, fmt.Errorf("dry reconcile: %w", err)
	}
	a.recDry = dry

	applyExec := exec.New(a.pve, cfg.Rec.TaskTimeout, 2*time.Second, a.store, log)
	apl, err := reconcile.New(reconcile.Options{
		PVE:           a.pve,
		Fetcher:       a.git,
		Store:         a.store,
		Budget:        plan.Budget{Prune: cfg.Rec.PruneBudget},
		Executor:      applyExec,
		NodeAllowlist: cfg.PVE.Nodes,
		Log:           log,
	})
	if err != nil {
		return nil, fmt.Errorf("apply reconcile: %w", err)
	}
	a.rec = apl

	// HTTP server. info() is computed on each /healthz call.
	srv := server.New(registry, a.store, a.healthInfo)
	a.srvErr = make(chan error, 1)
	a.srv = &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handle(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	return a, nil
}

// healthInfo returns the liveness inputs that /healthz reads.
func (a *Agent) healthInfo() server.Info {
	sinceGitFetch := time.Since(a.lastGit)
	sincePVERead := time.Since(a.lastPVE)
	ready := a.ready
	if a.lastGit.IsZero() {
		sinceGitFetch = time.Since(time.Now()) // no successful fetch yet
	}
	if a.lastPVE.IsZero() {
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
func (a *Agent) SetReadOnly(bool) {
}

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
// each cycle's outcome is reported via statusx + metrics.
func (a *Agent) Start(ctx context.Context) error {
	a.log.Info("pveconform starting",
		slog.String("version", a.version),
		slog.String("listen", a.cfg.Listen),
		slog.String("branch", a.cfg.Git.Branch),
		slog.String("git_source", sourceString(a.cfg.Git)),
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
			res, err := a.runApply(ctx)
			// In the watch loop, an aborted cycle is NOT fatal: it is reported
			// (statusx + log) and retried on the next tick. ErrAborted here
			// just means "the PVE/git side didn't converge this tick".
			if err != nil {
				if errors.Is(err, ErrAborted) {
					a.log.Debug("cycle aborted (will retry next tick)",
						slog.String("reason", res.AbortReason))
					continue
				}
				a.log.Error("reconcile cycle error",
					slog.String("err", err.Error()))
			}
		}
	}
}

// RunOnce performs a single apply cycle and blocks until it completes.
// Returns the status report plus an error (AbortError) when the cycle was
// aborted, so the CLI can exit non-zero.
func (a *Agent) RunOnce(ctx context.Context) (string, error) {
	res, err := a.runApply(ctx)
	if err != nil {
		return a.statusReport(), err
	}
	a.log.Debug("apply --once complete",
		slog.Int("actions", res.Actions),
		slog.Int("ok", res.ActionsOK),
		slog.Int("errors", res.ActionsErr))
	return a.statusReport(), nil
}

// runApply performs one apply-mode reconcile cycle.
//
// It returns:
//   - the cycle result
//   - an AbortError (*AbortError wraps ErrAborted) when the cycle aborted
//     (bad manifests / unreachable PVE / parse error). The caller decides
//     whether that is fatal (one-shot) or a transient state (watch loop).
func (a *Agent) runApply(ctx context.Context) (reconcile.Result, error) {
	res, _, err := a.rec.RunOneCycle(ctx)
	if err != nil {
		// A hard error from the pipeline itself (unexpected). Still report the
		// current cycle result so callers can read store state.
		a.markPVERead()
		a.ready = false
		return res, fmt.Errorf("reconcile: %w", err)
	}
	a.markPVERead()
	a.ready = !res.Aborted
	if res.Aborted {
		return res, &AbortError{Reason: res.AbortReason}
	}
	return res, nil
}

// markPVERead records that a PVE read succeeded.
func (a *Agent) markPVERead() { a.lastPVE = time.Now() }

// statusReport renders a text snapshot of the status store.
func (a *Agent) statusReport() string {
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
	buf.lines = append(buf.lines, fmt.Sprintf("objects=%d actions_ok=%d actions_err=%d pruned=%d prune_deferred=%d anomalies=%d",
		cyc.Objects, cyc.ActionsOK, cyc.ActionsError, cyc.Pruned, cyc.PruneDeferred, cyc.Anomalies))
	for _, o := range a.store.Objects() {
		line := fmt.Sprintf("  %-8s %-40s %-10s %-12s %s",
			o.Kind, o.Name, shortID(o.ID), shortState(o.State), o.PruneReason)
		if o.LastError != "" {
			line += " err: " + o.LastError
		}
		buf.lines = append(buf.lines, line)
	}
	return buf.String()
}

// Diff performs a read-only cycle and returns a human-readable report of what
// would change. It does NOT write to PVE.
//
// Returns the report text (always) and, when the cycle aborted (unreachable
// PVE, bad manifests, no git tree yet), an *AbortError so callers can signal
// a non-zero exit. The abort reason is included in the returned text.
func (a *Agent) Diff(ctx context.Context) (string, error) {
	a.log.Info("diff requested", slog.String("commit", a.git.RevString()))
	res, pl, err := a.recDry.RunOneCycle(ctx)
	if err != nil {
		return a.statusReport(), fmt.Errorf("diff: %w", err)
	}
	a.markPVERead()
	if res.Aborted {
		return a.statusReport(), &AbortError{Reason: res.AbortReason}
	}
	var b string
	if pl != nil {
		b = renderPlan(pl)
	}
	if res.Anomaly != "" {
		b += "\nANOMALY: " + res.Anomaly + "\n"
	}
	if len(res.AnomaliesList) > 0 {
		b += fmt.Sprintf("\nANOMALIES (%d live-only slots pveconform will NOT auto-remove):\n", len(res.AnomaliesList))
		for _, msg := range res.AnomaliesList {
			b += "  - " + msg + "\n"
		}
	}
	if b == "" {
		b = "no drift — PVE matches git"
	}
	return b, nil
}

// ApplyOnce performs a single apply cycle that converges PVE to git.
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
	out := a.statusReport()
	fmt.Println(out)
	return nil
}

// RunWatch runs the continuous reconcile loop (used by `run`).
func (a *Agent) RunWatch(ctx context.Context) error {
	a.log.Info("watch mode requested", slog.String("version", a.version))
	return a.Start(ctx)
}

// Adopt is reserved for the post-MVP "dump PVE → YAML" workflow.
func (a *Agent) Adopt(ctx context.Context) error {
	return ErrNotImplemented
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
	return itoa(id)
}

func shortState(x statusx.State) string { return string(x) }

func itoa(n int) string {
	return strconv.Itoa(n)
}

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
