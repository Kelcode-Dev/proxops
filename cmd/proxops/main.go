// Package main provides the proxops CLI.
//
// Repository-first execution model (M13.1): a ProxOps GitOps repository is
// the unit of operation. The normal workflow is to `cd` into the repository
// and run `proxops diff` / `status` / `apply` / `run` / `adopt` with no
// `--config` at all:
//
//  1. The repository root is discovered by walking up from the CWD to the
//     nearest `.git` marker (fail closed when none is found — proxops is not
//     invoked from inside a work tree).
//  2. Process-wide configuration is read from <root>/proxops.yaml when
//     present (log, reconcile, listen, data-dir, bootstrap credentials,
//     git.url URL-mode overrides, --git-path overrides). It is OPTIONAL:
//     defaults cover a repository that ships none.
//  3. Every <root>/clusters/<name>/config.yaml is loaded and merged into the
//     process config, declaring exactly one pve.clusters.<name> entry whose
//     key matches the directory (the M8 identity invariant). Per-cluster
//     endpoint, node allowlist, and SOPS reference come from these files.
//  4. The discovered root becomes git.path (local mode: no fetch, no token;
//     the operator's working tree IS the source of truth).
//
// The explicit repository/worktree path override is --git-path <path>
// (also honoured from PROXOPS_GIT_PATH), which is how development,
// automation and tests invoke proxops against a chosen tree. The --config
// flag remains for URL-mode deployments (proxops.yaml with git.url) and for
// pointing at a single cluster-local config file (advanced; pre-M13.1
// behaviour, still supported).
//
// Multi-cluster model (M8): the repository's clusters/ directories name the
// PVE clusters. `diff`, `apply`, `status`, and `run` process EVERY discovered
// cluster in deterministic (sorted name) order. `adopt` requires an explicit
// `--cluster=<name>` — the command refuses to run when that flag is not set,
// because an implicit cluster would silently adopt from the wrong endpoint.
//
// Flags only override their config-file values when explicitly set by the
// user (pflag.Changed).
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
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
	caFile      string
	gitPath     string // M13.1: explicit repository/worktree path override
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
		Use:   "proxops",
		Short: "Reconcile Proxmox with Git (Git -> proxops -> PVE API).",
		Long: "proxops is a GitOps reconciler for Proxmox VE. A ProxOps GitOps\n" +
			"repository is the unit of operation: it declares one or more PVE\n" +
			"clusters under clusters/<name>/ (endpoint + node allowlist + SOPS\n" +
			"credentials + resource composition). proxops converges each of them\n" +
			"to the declared state using the PVE API (no shell-out, no state\n" +
			"file, no Kubernetes).\n" +
			"\n" +
			"Normal use (repository-first, no --config needed):\n" +
			"\n" +
			"  cd ~/git/proxops-gitops\n" +
			"  proxops diff\n" +
			"  proxops status\n" +
			"  proxops apply\n" +
			"  proxops run\n" +
			"  proxops adopt --cluster <name>\n" +
			"\n" +
			"proxops discovers the repository from the current directory and\n" +
			"reads its optional proxops.yaml + per-cluster configuration.\n" +
			"Advanced overrides live under each command's --help: --git-path\n" +
			"(PROXOPS_GIT_PATH) for an explicit work tree, --config for URL mode\n" +
			"(a proxops.yaml with git.url) and single-cluster configs.\n" +
			"diff/apply/status/run process every discovered cluster in sorted\n" +
			"name order; adopt requires an explicit --cluster=<name>.\n",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().StringVar(&gf.configPath, "config", envOr("PROXOPS_CONFIG", ""), "path to proxops.yaml")
	root.PersistentFlags().StringVar(&gf.logLevel, "log-level", "", "override log level (debug|info|warn|error)")
	root.PersistentFlags().StringVar(&gf.listenAddr, "listen", "", "HTTP bind for /healthz /metrics /status")
	root.PersistentFlags().StringVar(&gf.gitURL, "git-url", "", "git repository URL (https)")
	root.PersistentFlags().StringVar(&gf.gitBranch, "git-branch", "", "git branch to reconcile")
	root.PersistentFlags().IntVar(&gf.pollSec, "git-poll-sec", 0, "git poll interval in seconds (0: use config)")
	root.PersistentFlags().IntVar(&gf.pruneBudget, "prune-budget", -1, "max deletions per reconcile cycle, per cluster (-1: use config)")
	root.PersistentFlags().StringVar(&gf.gitPath, "git-path", "", "path to a git work tree to reconcile (no fetch). Repository-first mode (the default) discovers this from the current directory; use this flag to override it explicitly for development, automation or tests (env: PROXOPS_GIT_PATH)")
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

// buildAgent loads config, validates it and returns an Agent.
//
// M13.1 repository-first flow:
//
//  1. --config set to a URL-mode config (a file with git.url) → the classic
//     Load path. Explicit advanced override (multi-host, remote-repo,
//     non-repo deployments).
//  2. Otherwise (no --config, or --config empty): discover the repository
//     root from CWD (or the explicit --git-path / PROXOPS_GIT_PATH
//     override), load <root>/proxops.yaml when present, load every
//     clusters/<name>/config.yaml, pin git.path to the root.
//
//  3. --config set to a cluster-local config file (clusters/<name>/config.yaml
//     or any file declaring pve.clusters.<name>) → load it as a single-cluster
//     config, merge the repository's proxops.yaml when present, pin git.path.
//     This is the pre-M13.1 behaviour preserved for backward compatibility.
//
//  4. --config set to an empty or file that has no pve.clusters → treat as
//     process-wide config; still discover the repository and its clusters.
//
// Flags are applied only when explicitly set (pflag.Changed).
func buildAgent(gf *globalFlags, log *slog.Logger, persistent, local *pflag.FlagSet) (*app.Agent, error) {
	var cfg *config.Config
	var repoRoot string
	var err error

	// Explicit --git-path override: the operator is pointing proxops at a
	// specific work tree (tests, automation, air-gapped, unusual invocations).
	explicitPath := gf.gitPath
	if explicitPath == "" {
		explicitPath = os.Getenv("PROXOPS_GIT_PATH")
	}

	if gf.configPath != "" {
		// Advanced path: point --config at a URL-mode proxops.yaml (or a
		// cluster-local config, or a bootstrap config).
		cfg, err = config.LoadCluster(gf.configPath)
		if err != nil {
			return nil, err
		}
		if explicitPath != "" {
			cfg.Git.Path = explicitPath
			cfg.Git.URL = ""
		}
		// Merge the repository's process-wide config under the
		// operator-selected file (local mode only). The operator-selected
		// file always wins on conflict; the process file only fills fields
		// the operator left at their default + adds clusters it declares.
		if cfg.Git.URL == "" && cfg.Git.Path != "" {
			if err := mergeProcessConfig(cfg, gf.configPath); err != nil {
				return nil, err
			}
		}
	} else {
		// Repository-first local mode.
		dirToDiscoverFrom := explicitPath
		if dirToDiscoverFrom == "" {
			wd, werr := os.Getwd()
			if werr != nil {
				return nil, fmt.Errorf("resolve current directory: %w", werr)
			}
			dirToDiscoverFrom = wd
		}
		cfg, repoRoot, err = config.LoadLocal(dirToDiscoverFrom)
		if err != nil {
			return nil, err
		}
	}

	// Apply flag overrides (flags only override their config-file values when
	// explicitly set).
	applyFlag := func(name string, fn func()) {
		if persistent != nil && persistent.Changed(name) {
			fn()
		}
	}
	applyFlag("log-level", func() { cfg.Log.Level = gf.logLevel })
	applyFlag("listen", func() { cfg.Listen = gf.listenAddr })
	applyFlag("git-url", func() {
		// URL-mode override: switch the source from the discovered local
		// tree to a remote URL. Drop the local path so Validate does not
		// reject the url+path combination; the branch default is kept.
		cfg.Git.URL = gf.gitURL
		cfg.Git.Path = ""
	})
	applyFlag("git-branch", func() { cfg.Git.Branch = gf.gitBranch })
	applyFlag("git-poll-sec", func() {
		if gf.pollSec > 0 {
			cfg.Rec.PollInterval = time.Duration(gf.pollSec) * time.Second
		}
	})
	applyFlag("prune-budget", func() { cfg.Rec.PruneBudget = gf.pruneBudget })
	applyFlag("pve-user", func() { cfg.PVE.User = gf.pveUser })
	applyFlag("pve-auth", func() { cfg.PVE.Auth = config.AuthMethod(gf.pveAuth) })
	applyFlag("git-path", func() {
		// Explicit worktree override: pin the root, drop URL mode.
		cfg.Git.Path = explicitPath
		cfg.Git.URL = ""
	})
	applyFlag("ca-file", func() { cfg.PVE.CAFile = gf.caFile })

	// Env overlays credentials (highest precedence for secrets), via the
	// single config.OverlayFromEnv path.
	config.OverlayFromEnv(cfg)

	// The repository root is informational for the log line; it may be ""
	// when --config was given (URL mode) — in that case the git source is
	// the URL clone.
	_ = repoRoot

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
			log.Info("proxops starting", "version", agent.Version(), "listen", agent.Config().Listen, "clusters", agent.Clusters())

			ctx, stop := signal.NotifyContext(c, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if err := agent.RunWatch(ctx); err != nil {
				return err
			}
			log.Info("proxops stopped")
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

// newAdoptCmd implements `proxops adopt` (M8). READ-ONLY: it inspects a
// single named PVE cluster and writes proxops YAML into a git working
// tree, suitable for committing. It never mutates PVE.
//
// Explicit cluster selection is required: adopt cannot "pick a cluster for
// you" — the cluster's endpoint and node allowlist come from pve.clusters,
// and the generated resources are placed under <kind>/<cluster>/.
func newAdoptCmd(gf *globalFlags, root *cobra.Command) *cobra.Command {
	var cf adoptFlags
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Generate proxops YAML from one PVE cluster's live objects (read-only).",
		Long: "Adopt inspects one configured PVE cluster (its endpoint + node " +
			"allowlist), reads live VM / LXC / ISO / CTTemplate objects, and " +
			"writes a proxops YAML manifest for each into the git work " +
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

// mergeProcessConfig applies the repository's process-wide proxops.yaml
// under an operator-selected local config (the advanced --config path).
// Semantics:
//
//   - the process config lives at <repo root>/proxops.yaml, where the repo
//     root is the .git-ancestor of the operator-selected file;
//   - it FILLS only fields the operator left at their built-in default —
//     an explicitly set operator value always wins;
//   - it ADDS pve.clusters entries the operator's file did not declare
//     (an operator-declared cluster entry always wins over a process one);
//   - it does NOT touch the git source (the operator explicitly chose
//     their file; the source of truth stays the local tree their file
//     anchors to), so a repository's git.url has no effect on this
//     invocation;
//   - a malformed process file fails closed (returning an error): a broken
//     proxops.yaml must not silently change the observed configuration.
//
// Returns nil when there is nothing to merge (no process file present).
func mergeProcessConfig(target *config.Config, anchorPath string) error {
	root := config.DiscoverGitRoot(filepath.Dir(anchorPath))
	if root == "" {
		return nil // the operator config does not anchor to a repository tree
	}
	procPath := filepath.Join(root, "proxops.yaml")
	if _, err := os.Stat(procPath); err != nil {
		return nil // no process config: operator file stands alone
	}
	proc, err := config.Load(procPath)
	if err != nil {
		return fmt.Errorf("repository process config %s: %w", procPath, err)
	}
	d := config.Defaults()

	// Fill defaulted fields (operator-explicit values are preserved).
	if target.Log.Level == d.Log.Level {
		target.Log.Level = proc.Log.Level
	}
	if target.Listen == d.Listen {
		target.Listen = proc.Listen
	}
	if target.DataDir == d.DataDir {
		target.DataDir = proc.DataDir
	}
	if target.Rec.PollInterval == d.Rec.PollInterval {
		target.Rec.PollInterval = proc.Rec.PollInterval
	}
	if target.Rec.TaskTimeout == d.Rec.TaskTimeout {
		target.Rec.TaskTimeout = proc.Rec.TaskTimeout
	}
	if target.Rec.PruneBudget == d.Rec.PruneBudget {
		target.Rec.PruneBudget = proc.Rec.PruneBudget
	}
	if target.PVE.Auth == d.PVE.Auth {
		target.PVE.Auth = proc.PVE.Auth
	}
	if target.PVE.CAFile == "" {
		target.PVE.CAFile = proc.PVE.CAFile
	}
	if target.PVE.User == "" {
		target.PVE.User = proc.PVE.User
	}
	if target.PVE.TokenID == "" {
		target.PVE.TokenID = proc.PVE.TokenID
	}
	if target.PVE.Token == "" {
		target.PVE.Token = proc.PVE.Token
	}
	if target.PVE.Password == "" {
		target.PVE.Password = proc.PVE.Password
	}
	if target.PVE.TokenValue == "" {
		target.PVE.TokenValue = proc.PVE.TokenValue
	}

	// Add process-declared clusters the operator did not declare.
	for _, name := range proc.PVE.ClusterNames() {
		if _, ok := target.PVE.Clusters[name]; ok {
			continue // operator entry wins
		}
		if target.PVE.Clusters == nil {
			target.PVE.Clusters = map[string]config.PVECluster{}
		}
		target.PVE.Clusters[name] = proc.PVE.Clusters[name]
	}
	return nil
}

// levelFor resolves the effective log level from flags, then config, then
// default. In repository-first mode (no --config) the level comes from the
// discovered <root>/proxops.yaml (if any) so the user's log verbosity is
// honoured before the agent even constructs.
func levelFor(gf *globalFlags) string {
	if gf.logLevel != "" {
		return gf.logLevel
	}
	if gf.configPath != "" {
		if cfg, err := config.Load(gf.configPath); err == nil && cfg.Log.Level != "" {
			return cfg.Log.Level
		}
		return "info"
	}
	wd, err := os.Getwd()
	if err != nil {
		return "info"
	}
	// Respect an explicit --git-path override, like buildAgent does.
	if p := gf.gitPath; p != "" {
		wd = p
	}
	cfg, _, err := config.LoadLocal(wd)
	if err != nil {
		return "info"
	}
	if cfg.Log.Level != "" {
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
