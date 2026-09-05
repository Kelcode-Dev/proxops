// Package app wires the configuration, logger, and metrics together into the
// long-running Agent. Higher-level reconcile logic (git, PVE, diff, plan,
// exec) is attached incrementally in subsequent milestones; at this point the
// Agent is the place where those collaborators meet.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrNotImplemented is the stable sentinel for CLI paths reserved for later milestones.
var ErrNotImplemented = errors.New("not implemented in this build; see milestone plan (docs/ARCHITECTURE.md)")

// Agent orchestrates one pveconform process.
//
// It is intentionally small at the scaffold stage: it owns the validated
// config, the logger, and the prometheus registry. The reconcile collaborators
// (git source, PVE client, planner, executor) are added as the milestones land
// and are injected here.
type Agent struct {
	log      *slog.Logger
	cfg      *config.Config
	registry *prometheus.Registry
	version  string

	// notReady is a stable sentinel used by the --once/diff paths before the
	// reconcile pipeline exists, so the CLI can report a clear "not yet"
	// without pretending work happened.
	notReady error
}

// New constructs an Agent from validated config.
func New(cfg *config.Config, log *slog.Logger, registry *prometheus.Registry, version string) *Agent {
	return &Agent{
		log:      log,
		cfg:      cfg,
		registry: registry,
		version:  version,
		notReady: fmt.Errorf("reconcile pipeline not yet implemented (milestone M3+); config and CLI are active"),
	}
}

// Config returns the validated configuration.
func (a *Agent) Config() *config.Config { return a.cfg }

// Registry returns the prometheus registry (for the /metrics handler).
func (a *Agent) Registry() *prometheus.Registry { return a.registry }

// Version returns the build version.
func (a *Agent) Version() string { return a.version }

// Log returns the agent logger.
func (a *Agent) Log() *slog.Logger { return a.log }

// Diff performs a read-only comparison of desired (git) vs actual (PVE) state
// and returns a human-readable report without applying anything.
//
// M0: returns notReady. Populated in M3 for VMs and extended in M4.
func (a *Agent) Diff(ctx context.Context) (string, error) {
	_ = ctx
	a.log.Info("diff requested", "version", a.version)
	return "", a.notReady
}

// ApplyOnce performs a single reconcile cycle that converges PVE to git,
// respecting the dry-run flag.
//
// M0: returns notReady. Populated in M3.
func (a *Agent) ApplyOnce(ctx context.Context, dryRun bool) (string, error) {
	_ = ctx
	_ = dryRun
	a.log.Info("apply --once requested", "dry_run", dryRun)
	return "", a.notReady
}

// ShowStatus prints a snapshot of the last reconcile state.
//
// M0: returns notReady; the status store lands with M3.
func (a *Agent) ShowStatus(ctx context.Context) error {
	_ = ctx
	a.log.Info("status requested")
	return a.notReady
}

// RunWatch starts the continuous poll-and-reconcile loop, blocking until ctx is
// cancelled.
//
// M0: not implemented; the watch loop lands with M3.
func (a *Agent) RunWatch(ctx context.Context) error {
	a.log.Info("watch mode requested", "version", a.version)
	select {
	case <-ctx.Done():
		return nil
	}
}
