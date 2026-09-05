// Package main provides the pveconform CLI. Configuration is loaded from
// defaults + an optional YAML file + flags + env, validated, then handed to the
// appropriate subcommand. Flags only override their config-file values when
// explicitly set by the user (pflag.Changed).
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/GizzmoShifu/proxmox-operator/internal/app"
	"github.com/GizzmoShifu/proxmox-operator/internal/config"
	"github.com/GizzmoShifu/proxmox-operator/internal/logger"
	"github.com/GizzmoShifu/proxmox-operator/internal/metrics"
)

// version is set at build time via -ldflags.
var version = "dev"

type globalFlags struct {
	configPath  string
	logLevel    string
	listenAddr  string
	gitURL      string
	gitBranch   string
	pollSec     int
	pruneBudget int
	pveUser     string
	pveAuth     string
	pvePort     int
	caFile      string
}

// commandFlags carries per-command flags.
type commandFlags struct {
	dryRun   bool
	showDiff bool
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

// errSilent is returned by commands that have already reported their outcome.
var errSilent = errors.New("silent")

func newRootCmd() *cobra.Command {
	var gf globalFlags

	root := &cobra.Command{
		Use:   "pveconform",
		Short: "Reconcile Proxmox with Git (Git -> pveconform -> PVE API).",
		Long: "pveconform is a GitOps reconciler for Proxmox VE. It watches a git\n" +
			"repository as the source of truth and continuously converges Proxmox to\n" +
			"match it, using the PVE API (no shell-out, no state file, no Kubernetes).\n" +
			"\nConfiguration precedence: defaults < --config YAML < flags < env (secrets).\n",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&gf.configPath, "config", envOr("PVECONFORM_CONFIG", ""), "path to pveconform.yaml")
	root.PersistentFlags().StringVar(&gf.logLevel, "log-level", "", "override log level (debug|info|warn|error)")
	root.PersistentFlags().StringVar(&gf.listenAddr, "listen", "", "HTTP bind for /healthz /metrics /status")
	root.PersistentFlags().StringVar(&gf.gitURL, "git-url", "", "git repository URL (https)")
	root.PersistentFlags().StringVar(&gf.gitBranch, "git-branch", "", "git branch to reconcile")
	root.PersistentFlags().IntVar(&gf.pollSec, "git-poll-sec", 0, "git poll interval in seconds (0: use config)")
	root.PersistentFlags().IntVar(&gf.pruneBudget, "prune-budget", -1, "max deletions per reconcile cycle (-1: use config)")
	root.PersistentFlags().StringVar(&gf.pveUser, "pve-user", "", "PVE user ID, e.g. root@pam")
	root.PersistentFlags().StringVar(&gf.pveAuth, "pve-auth", "", "PVE auth method: token or ticket")
	root.PersistentFlags().IntVar(&gf.pvePort, "pve-port", 0, "PVE API port (0: use config)")
	root.PersistentFlags().StringVar(&gf.caFile, "ca-file", "", "path to the PVE cluster CA certificate")

	root.AddCommand(
		newRunCmd(&gf, root),
		newDiffCmd(&gf, root),
		newApplyCmd(&gf, root),
		newStatusCmd(&gf, root),
		newAdoptCmd(),
	)
	return root
}

// buildAgent loads config (defaults <- file <- flags <- env), validates it and
// returns an Agent. Flags are applied only when explicitly set.
func buildAgent(gf *globalFlags, log *slog.Logger, persistent, local *pflag.FlagSet) (*app.Agent, error) {
	cfg, err := config.Load(gf.configPath)
	if err != nil {
		return nil, err
	}

	applyFlag := func(name string, fn func()) {
		if persistent != nil && persistent.Changed(name) {
			fn()
		}
	}
	applyFlag("log-level", func() { cfg.Log.Level = gf.logLevel })
	applyFlag("listen", func() { cfg.Listen = gf.listenAddr })
	applyFlag("git-url", func() { cfg.Git.URL = gf.gitURL })
	applyFlag("git-branch", func() { cfg.Git.Branch = gf.gitBranch })
	applyFlag("git-poll-sec", func() {
		if gf.pollSec > 0 {
			cfg.Rec.PollInterval = time.Duration(gf.pollSec) * time.Second
		}
	})
	applyFlag("prune-budget", func() { cfg.Rec.PruneBudget = gf.pruneBudget })
	applyFlag("pve-user", func() { cfg.PVE.User = gf.pveUser })
	applyFlag("pve-auth", func() { cfg.PVE.Auth = config.AuthMethod(gf.pveAuth) })
	applyFlag("pve-port", func() {
		if gf.pvePort > 0 {
			cfg.PVE.Port = gf.pvePort
		}
	})
	applyFlag("ca-file", func() { cfg.PVE.CAFile = gf.caFile })

	// Env overlays credentials (highest precedence for secrets).
	overlayEnv(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return app.New(cfg, log, metrics.Register(), version), nil
}

// overlayEnv mirrors the config package's env overlay for testability.
func overlayEnv(cfg *config.Config) {
	if v := os.Getenv("PVECONFORM_PVE_TOKEN"); v != "" {
		cfg.PVE.Token = v
	}
	if v := os.Getenv("PVECONFORM_PVE_PASSWORD"); v != "" {
		cfg.PVE.Password = v
	}
	if v := os.Getenv("PVECONFORM_GIT_TOKEN"); v != "" {
		cfg.Git.Token = v
	}
}

// newRunCmd is the daemon mode: poll git and reconcile PVE continuously.
func newRunCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the continuous reconcile loop (daemon mode).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := cmd.Context()
			log := logger.New(levelFor(gf))
			agent, err := buildAgent(gf, log, root.PersistentFlags(), nil)
			if err != nil {
				return err
			}
			log.Info("pveconform starting", "version", agent.Version(), "listen", agent.Config().Listen)

			ctx, stop := signal.NotifyContext(c, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if err := agent.RunWatch(ctx); err != nil {
				return err
			}
			log.Info("pveconform stopped")
			return nil
		},
	}
}

// newDiffCmd prints a read-only desired-vs-actual report.
func newDiffCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Show the drift between git and Proxmox without applying it.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := logger.New(levelFor(gf))
			agent, err := buildAgent(gf, log, root.PersistentFlags(), nil)
			if err != nil {
				return err
			}
			out, err := agent.Diff(cmd.Context())
			if err != nil {
				return err
			}
			if out == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "no drift")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), out)
			}
			return nil
		},
	}
}

// newApplyCmd performs a single converge cycle (optionally dry-run).
func newApplyCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	var cf commandFlags
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Converge Proxmox to git in a single cycle.",
		RunE: func(c *cobra.Command, _ []string) error {
			log := logger.New(levelFor(gf))
			agent, err := buildAgent(gf, log, root.PersistentFlags(), c.Flags())
			if err != nil {
				return err
			}
			out, err := agent.ApplyOnce(c.Context(), cf.dryRun)
			if err != nil {
				return err
			}
			if out != "" {
				fmt.Fprintln(c.OutOrStdout(), out)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&cf.dryRun, "dry-run", "n", false, "compute planned actions but apply nothing")
	cmd.Flags().BoolVar(&cf.showDiff, "diff", false, "render per-field diffs for the planned actions")
	return cmd
}

// newStatusCmd prints a snapshot of convergence for managed objects.
func newStatusCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print convergence status for managed objects.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := logger.New(levelFor(gf))
			agent, err := buildAgent(gf, log, root.PersistentFlags(), nil)
			if err != nil {
				return err
			}
			return agent.ShowStatus(cmd.Context())
		},
	}
}

// newAdoptCmd is reserved for the post-MVP "dump live PVE -> YAML" workflow.
func newAdoptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "adopt",
		Short: "Generate YAML for existing PVE objects so they can be imported.",
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("adopt is reserved for a later milestone (PVE -> YAML); not available in this build")
		},
	}
}

// levelFor resolves the effective log level from flags, then config, then default.
func levelFor(gf *globalFlags) string {
	if gf.logLevel != "" {
		return gf.logLevel
	}
	if cfg, err := config.Load(gf.configPath); err == nil && cfg.Log.Level != "" {
		return cfg.Log.Level
	}
	return "info"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
