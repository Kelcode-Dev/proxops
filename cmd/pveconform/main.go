// Package main provides the pveconform CLI. Configuration is loaded from
// defaults + an optional YAML file + flags + env, validated, then handed to the
// appropriate subcommand. Flags only override their config-file values when
// explicitly set by the user (pflag.Changed).
//
// Multi-cluster model (M8): the config names one or more PVE clusters.
// `diff`, `apply`, `status`, and `run` process EVERY configured cluster in
// deterministic (sorted name) order. `adopt` requires an explicit
// `--cluster=<name>` — the command refuses to run when that flag is not set,
// because an implicit cluster would silently adopt from the wrong endpoint.
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
	configPath string
	logLevel   string
	listenAddr string
	gitURL     string
	gitBranch  string
	pollSec    int
	pruneBudget int
	pveUser    string
	pveAuth    string
	caFile     string
	gitPath    string // air-gapped / local-mode source of truth
}

// commandFlags carries per-command flags.
type commandFlags struct {
	dryRun   bool
	showDiff bool
}

// adoptFlags carries the adopt command's per-command surface.
type adoptFlags struct {
	cluster string
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
			"repository as the source of truth and continuously converges every\n" +
			"configured Proxmox cluster to match it, using the PVE API (no shell-\n" +
			"out, no state file, no Kubernetes).\n" +
			"\n" +
			"Configuration precedence: defaults < --config YAML < flags < env (secrets).\n" +
			"The config carries one or more named PVE clusters (pve.clusters), each\n" +
			"with its own base-url + node allowlist. diff/apply/status/run process\n" +
			"all configured clusters; adopt requires an explicit --cluster=<name>.\n",
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
	root.PersistentFlags().IntVar(&gf.pruneBudget, "prune-budget", -1, "max deletions per reconcile cycle, per cluster (-1: use config)")
	root.PersistentFlags().StringVar(&gf.gitPath, "git-path", "", "air-gapped / local mode: path to a git work tree (no fetch)")
	root.PersistentFlags().StringVar(&gf.pveUser, "pve-user", "", "PVE user ID, e.g. root@pam (shared across clusters)")
	root.PersistentFlags().StringVar(&gf.pveAuth, "pve-auth", "", "PVE auth method: token or ticket (shared across clusters)")
	root.PersistentFlags().StringVar(&gf.caFile, "ca-file", "", "PVE cluster CA certificate path (shared across clusters)")

	root.AddCommand(
		newRunCmd(&gf, root),
		newDiffCmd(&gf, root),
		newApplyCmd(&gf, root),
		newStatusCmd(&gf, root),
		newAdoptCmd(&gf, root),
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
	applyFlag("git-path", func() { cfg.Git.Path = gf.gitPath })
	applyFlag("ca-file", func() { cfg.PVE.CAFile = gf.caFile })

	// Env overlays credentials (highest precedence for secrets), via the
	// single config.OverlayFromEnv path.
	config.OverlayFromEnv(cfg)

	// M9: do NOT call cfg.Validate() here. app.New is responsible for
	// calling ResolveSOPS() (per-cluster SOPS decryption) BEFORE its
	// internal Validate() pass. Validating in buildAgent would run
	// Validate() with empty SopsResolved, causing SOPS configs that have
	// no global pve.user/token triple (which is correct — SOPS supplies
	// them) to fail validation prematurely.
	return app.New(cfg, log, metrics.Register(), version)
}

// newRunCmd is the daemon mode: poll git and reconcile PVE continuously,
// for every configured cluster.
func newRunCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the continuous reconcile loop (daemon mode), for every configured cluster.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := cmd.Context()
			log := logger.New(levelFor(gf))
			agent, err := buildAgent(gf, log, root.PersistentFlags(), nil)
			if err != nil {
				return err
			}
			log.Info("pveconform starting", "version", agent.Version(), "listen", agent.Config().Listen, "clusters", agent.Clusters())

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

// newDiffCmd prints a read-only desired-vs-actual report for every
// configured cluster.
func newDiffCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Show the drift between git and Proxmox for every configured cluster, without applying it.",
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
				if _, werr := fmt.Fprintln(cmd.OutOrStdout(), "no drift"); werr != nil {
					return werr
				}
			} else {
				if _, werr := fmt.Fprintln(cmd.OutOrStdout(), out); werr != nil {
					return werr
				}
			}
			return nil
		},
	}
}

// newApplyCmd performs a single converge cycle for every configured cluster
// (optionally dry-run).
func newApplyCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	var cf commandFlags
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Converge every configured cluster to git in a single cycle.",
		RunE: func(c *cobra.Command, _ []string) error {
			log := logger.New(levelFor(gf))
			agent, err := buildAgent(gf, log, c.Root().PersistentFlags(), c.Flags())
			if err != nil {
				return err
			}
			out, err := agent.ApplyOnce(c.Context(), cf.dryRun)
			if err != nil {
				return err
			}
			if out != "" {
				if _, werr := fmt.Fprintln(c.OutOrStdout(), out); werr != nil {
					return werr
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&cf.dryRun, "dry-run", "n", false, "compute planned actions but apply nothing")
	cmd.Flags().BoolVar(&cf.showDiff, "diff", false, "render per-field diffs for the planned actions")
	return cmd
}

// newStatusCmd prints a per-cluster snapshot of convergence for managed
// objects.
func newStatusCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print convergence status for managed objects on every configured cluster.",
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

// newAdoptCmd implements `pveconform adopt` (M8). READ-ONLY: it inspects a
// single named PVE cluster and writes pveconform YAML into a git working
// tree, suitable for committing. It never mutates PVE.
//
// Explicit cluster selection is required: adopt cannot "pick a cluster for
// you" — the cluster's endpoint and node allowlist come from pve.clusters,
// and the generated resources are placed under <kind>/<cluster>/.
func newAdoptCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	var cf adoptFlags
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Generate pveconform YAML from one PVE cluster's live objects (read-only).",
		Long: "Adopt inspects one configured PVE cluster (its endpoint + node " +
			"allowlist), reads live VM / LXC / ISO / CTTemplate objects, and " +
			"writes a pveconform YAML manifest for each into the git work " +
			"tree under <kind>/<cluster>/. The command is READ-ONLY with " +
			"respect to PVE: it does not create, modify, delete, or tag any " +
			"PVE object.\n" +
			"\n" +
			"A cluster MUST be named with --cluster=<name>; unknown cluster " +
			"names fail closed. A node outside the cluster's allowlist is " +
			"skipped and reported.",
		RunE: func(c *cobra.Command, _ []string) error {
			log := logger.New(levelFor(gf))
			agent, err := buildAgent(gf, log, c.Root().PersistentFlags(), c.Flags())
			if err != nil {
				return err
			}
			if cf.cluster == "" {
				return errors.New("adopt requires an explicit --cluster=<name>; configured clusters: " + fmt.Sprint(agent.Clusters()))
			}
			if _, ok := agent.Config().PVE.Cluster(cf.cluster); !ok {
				return fmt.Errorf("--cluster %q is not in pve.clusters (known: %v)", cf.cluster, agent.Clusters())
			}
			// The git source's WorkDir is the place adopt writes.
			adoptRoot := agent.GitSource().WorkDir()
			out, err := runAdopt(c.Context(), agent, cf.cluster, adoptRoot, log)
			if err != nil {
				return err
			}
			if out == "" {
				out = "no adoptable objects found"
			}
			if _, werr := fmt.Fprintln(c.OutOrStdout(), out); werr != nil {
				return werr
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cf.cluster, "cluster", "", "named PVE cluster to adopt from (required; must be in pve.clusters)")
	return cmd
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
