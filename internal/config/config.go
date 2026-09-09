// Package config holds the pveconform runtime configuration.
//
// Precedence (lowest to highest): built-in defaults, then the YAML config
// file (--config), then explicit command-line flags, then selected
// environment variables (credentials only; never in YAML or flags).
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AuthMethod selects how the agent authenticates against the PVE API.
type AuthMethod string

const (
	// AuthToken uses PVE API token authentication:
	// Authorization: PVEAPIToken=<user>@<realm>!<tokenid>=<uuid>
	AuthToken AuthMethod = "token"
	// AuthTicket uses username+password, exchanged for a session ticket.
	AuthTicket AuthMethod = "ticket"
)

// LogConfig holds logging settings.
type LogConfig struct {
	Level string `yaml:"level"` // debug | info | warn | error
}

// PVECluster is one named PVE cluster: a single API endpoint plus the
// allowlist of PVE node names that belong to it.
//
// The cluster NAME is the stable identifier shared by GitOps composition
// (the clusters/<name>/ directory) and this configuration. It is NOT a PVE
// node name — a cluster can span many PVE nodes (its Nodes allowlist).
type PVECluster struct {
	// BaseURL is the PVE API endpoint (scheme://host[:port]) the agent talks
	// to for this cluster. PVE exposes the entire API on every node, so one
	// endpoint reaches every node/object in the cluster; the PVE node name
	// lives only in the request path, never the host.
	// Example: "https://pve-dev-01.example.invalid:8006".
	BaseURL string `yaml:"base-url"`
	// Nodes is the allowlist of PVE node names in this cluster. A manifest
	// whose spec.node is not in this list (when the list is non-empty) fails
	// closed before any PVE call — this is the per-cluster node boundary.
	// When empty, no allowlist check is performed for that cluster.
	Nodes []string `yaml:"nodes"`
}

// PVEConfig holds Proxmox connection settings.
//
// Credentials (auth/user/token/password/ca-file) are SHARED across all
// clusters; endpoints differ PER CLUSTER. Each cluster reconciles against a
// single PVE endpoint and node allowlist — there is no implicit "current
// cluster" global state.
type PVEConfig struct {
	Auth AuthMethod `yaml:"auth"` // token (default) | ticket
	// User is the PVE user ID; for tokens it is the token owner,
	// e.g. "root@pam" or "pveops@pve".
	User string `yaml:"user"`
	// TokenID is the PVE API token name (the part before '=' in the
	// user@realm!tokenid=uuid credential). Preferred over TokenValue.
	TokenID string `yaml:"token-id"`
	// Token is the API token UUID (token auth). Prefer env PVECONFORM_PVE_TOKEN.
	Token string `yaml:"token"`
	// TokenValue, when set, overrides TokenID+Token with a fully-composed
	// "user@realm!tokenid=uuid" credential. Prefer env PVECONFORM_PVE_TOKEN_VALUE.
	TokenValue string `yaml:"token-value"`
	// Password is the user's password (ticket auth only).
	// Prefer env PVECONFORM_PVE_PASSWORD.
	Password string `yaml:"password"`
	// CAFile optionally points at a PVE cluster self-signed CA. It applies
	// to every cluster endpoint in this configuration; per-cluster CAs come
	// with SOPS (post-M8). When empty the system trust store is used.
	CAFile string `yaml:"ca-file"`
	// Clusters is the set of named PVE clusters. At least one entry is
	// required. Each cluster is reconciled against its own base-url and
	// node allowlist.
	Clusters map[string]PVECluster `yaml:"clusters"`
}

// clusterNameRe validates a named cluster: lowercase alnum + '-', no
// leading '-'. This matches the GitOps composition directory grammar
// (clusters/<cluster>/), so a cluster's config key and its composition
// directory are always the same string.
var clusterNameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// ValidClusterName reports whether s is a usable named cluster.
func ValidClusterName(s string) bool {
	return len(s) >= 1 && len(s) <= 64 && clusterNameRe.MatchString(s)
}

// ClusterNames returns the cluster names in deterministic (lexicographic)
// order, so multi-cluster processing is stable across runs.
func (c *PVEConfig) ClusterNames() []string {
	names := make([]string, 0, len(c.Clusters))
	for n := range c.Clusters {
		names = append(names, n)
	}
	sortStrings(names)
	return names
}

// Cluster returns the named cluster and whether it exists.
func (c *PVEConfig) Cluster(name string) (PVECluster, bool) {
	cluster, ok := c.Clusters[name]
	return cluster, ok
}

// GitConfig holds git source-of-truth settings.
type GitConfig struct {
	// URL is the git repository (https preferred). Mutually exclusive with Path.
	URL string `yaml:"url"`
	// Token authenticates git fetches. Prefer env PVECONFORM_GIT_TOKEN.
	Token string `yaml:"token"`
	// Branch is the ref to reconcile from. Default "main".
	Branch string `yaml:"branch"`
	// Path is a local working tree checkout (air-gapped mode).
	// Mutually exclusive with URL.
	Path string `yaml:"path"`
}

// ReconcileConfig holds reconciliation loop behavior.
type ReconcileConfig struct {
	// PollInterval is how often the agent polls git and re-diffs PVE state.
	PollInterval time.Duration `yaml:"poll-interval"`
	// TaskTimeout is the max time to wait for a PVE async task to finish.
	TaskTimeout time.Duration `yaml:"task-timeout"`
	// PruneBudget caps deletions per reconcile cycle.
	PruneBudget int `yaml:"prune-budget"`
}

// Config is the complete pveconform configuration.
type Config struct {
	Log     LogConfig       `yaml:"log"`
	PVE     PVEConfig       `yaml:"pve"`
	Git     GitConfig       `yaml:"git"`
	Rec     ReconcileConfig `yaml:"reconcile"`
	Listen  string          `yaml:"listen"`   // HTTP endpoint for /healthz /metrics /status
	DataDir string          `yaml:"data-dir"` // git cache, CA pinning; default ~/.local/share/pveconform
}

// Defaults returns a Config with sensible default values. PVE cluster
// endpoints are intentionally NOT defaulted: they are environment-specific
// and Validate() refuses to run the agent when no named cluster is present.
func Defaults() *Config {
	return &Config{
		Log:  LogConfig{Level: "info"},
		PVE:  PVEConfig{Auth: AuthToken, Clusters: map[string]PVECluster{}},
		Git:  GitConfig{Branch: "main"},
		Rec:  ReconcileConfig{PollInterval: 30 * time.Second, TaskTimeout: 30 * time.Minute, PruneBudget: 3},
		Listen:  "127.0.0.1:9494",
		DataDir: defaultDataDir(),
	}
}

// Load reads a YAML config file (optional) and merges it over defaults.
// An empty path returns Defaults().
func Load(path string) (*Config, error) {
	c := Defaults()
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	// YAML is a plain string format; "~" at the start of a filesystem path
	// must be expanded manually (go's os.UserHomeDir doesn't do this for us).
	// Without this, `data-dir: ~/.local/share/pveconform` produces a stale
	// `./~` directory under $PWD.
	c.DataDir = expandTilde(c.DataDir)
	c.PVE.CAFile = expandTilde(c.PVE.CAFile)
	return c, nil
}

// OverlayFromEnv applies pveconform's credential-override env vars onto a
// config. Called by the CLI AFTER flag overlays, so the precedence chain is:
// defaults < YAML < flags < env.
//
// Semantics (highest precedence first, only when the env var is set
// non-empty):
//
//	PVECONFORM_PVE_TOKEN_VALUE  -> PVE.TokenValue (overrides user+id+token)
//	PVECONFORM_PVE_TOKEN        -> PVE.Token
//	PVECONFORM_PVE_PASSWORD     -> PVE.Password
//	PVECONFORM_PVE_USER         -> PVE.User
//	PVECONFORM_GIT_TOKEN        -> Git.Token
//
// PVECONFORM_PVE_USER is deliberately honored as an override (not just a
// fallback): the documented model is "the environment carries credentials,
// the YAML carries non-secrets". An operator who set it in the env expects
// it to reach the PVE client.
func OverlayFromEnv(c *Config) {
	if v := os.Getenv("PVECONFORM_PVE_TOKEN_VALUE"); v != "" {
		c.PVE.TokenValue = v
	}
	if v := os.Getenv("PVECONFORM_PVE_TOKEN"); v != "" {
		c.PVE.Token = v
	}
	if v := os.Getenv("PVECONFORM_PVE_PASSWORD"); v != "" {
		c.PVE.Password = v
	}
	if v := os.Getenv("PVECONFORM_PVE_USER"); v != "" {
		c.PVE.User = v
	}
	if v := os.Getenv("PVECONFORM_GIT_TOKEN"); v != "" {
		c.Git.Token = v
	}
}

// Validate checks the configuration for internal consistency and required
// fields, reporting all problems at once.
func (c *Config) Validate() error {
	var errs []string

	switch c.PVE.Auth {
	case AuthToken:
		if c.PVE.User == "" {
			errs = append(errs, "pve.user is required for token auth")
		}
		if c.PVE.TokenValue == "" {
			if c.PVE.TokenID == "" {
				errs = append(errs, "pve.token-id missing")
			}
			if c.PVE.Token == "" {
				errs = append(errs, "pve.token missing (set PVECONFORM_PVE_TOKEN)")
			}
		}
		if c.PVE.Password != "" {
			errs = append(errs, "pve.password must be empty when pve.auth=token")
		}
		c.PVE.Password = "" // never carry the unused secret
	case AuthTicket:
		if c.PVE.User == "" {
			errs = append(errs, "pve.user is required for ticket auth")
		}
		if c.PVE.Password == "" {
			errs = append(errs, "pve.password missing (set PVECONFORM_PVE_PASSWORD)")
		}
	default:
		errs = append(errs, fmt.Sprintf("pve.auth must be %q or %q, got %q", AuthToken, AuthTicket, c.PVE.Auth))
	}

	// Named PVE clusters (M8 multi-cluster model). At least one cluster is
	// required. Each cluster needs a parseable base-url endpoint and a
	// well-formed node allowlist. This is the multi-cluster "fail closed"
	// boundary: a resource may only be reconciled against the cluster whose
	// composition explicitly includes it, and the endpoint + node allowlist
	// of that cluster are the only ones that apply.
	if len(c.PVE.Clusters) == 0 {
		errs = append(errs, "pve.clusters: at least one named PVE cluster is required (each with its own base-url)")
	}
	// Endpoints must be unique across clusters. Prune scoping is endpoint-
	// based (the PVE /cluster/resources listing for that endpoint is the
	// live inventory), so two shared clusters would share a live inventory
	// and one could prune the other — a footgun the model must not allow.
	endpointSeen := map[string]string{}
	for _, name := range c.PVE.ClusterNames() {
		base := c.PVE.Clusters[name].BaseURL
		if base == "" {
			continue
		}
		if other, dup := endpointSeen[base]; dup {
			errs = append(errs, fmt.Sprintf("pve.clusters.%s and pve.clusters.%s share base-url %q; each cluster must have its own PVE endpoint", other, name, base))
			continue
		}
		endpointSeen[base] = name
	}
	for _, name := range c.PVE.ClusterNames() {
		cluster, _ := c.PVE.Cluster(name)
		if !ValidClusterName(name) {
			errs = append(errs, fmt.Sprintf("pve.clusters: %q is not a valid cluster name (lowercase alnum + '-', no leading '-')", name))
			continue
		}
		if cluster.BaseURL == "" {
			errs = append(errs, fmt.Sprintf("pve.clusters.%s.base-url is required (e.g. https://<host>[:port])", name))
			continue
		}
		if u, err := url.Parse(cluster.BaseURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			errs = append(errs, fmt.Sprintf("pve.clusters.%s.base-url must be a parseable http(s)://host[:port] URL with a host component", name))
		}
		seen := map[string]bool{}
		for i, n := range cluster.Nodes {
			if strings.TrimSpace(n) == "" || strings.ContainsAny(n, " /") {
				errs = append(errs, fmt.Sprintf("pve.clusters.%s.nodes[%d]: %q is not a valid PVE node name", name, i, n))
				continue
			}
			if seen[n] {
				errs = append(errs, fmt.Sprintf("pve.clusters.%s.nodes: duplicate entry %q", name, n))
			}
			seen[n] = true
		}
	}

	if c.Git.URL != "" && c.Git.Path != "" {
		errs = append(errs, "git.url and git.path are mutually exclusive; pick one")
	}
	if c.Git.URL == "" && c.Git.Path == "" {
		errs = append(errs, "a git source is required: set git.url or git.path")
	}
	if c.Git.Branch == "" {
		errs = append(errs, "git.branch must not be empty")
	}

	if c.Rec.PollInterval <= 0 {
		errs = append(errs, "reconcile.poll-interval must be > 0")
	}
	if c.Rec.TaskTimeout <= 0 {
		errs = append(errs, "reconcile.task-timeout must be > 0")
	}
	if c.Rec.PruneBudget < 0 {
		errs = append(errs, "reconcile.prune-budget must be >= 0")
	}

	if c.Log.Level == "" {
		errs = append(errs, "log.level must not be empty")
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration: %s", joinErrs(errs))
	}
	return nil
}

// GitCacheDir returns the directory the go-git clone cache lives in.
func (c *Config) GitCacheDir() string {
	return filepath.Join(c.DataDir, "git-cache")
}

// CAFileResolved returns the effective CA file path, or "" when none is set.
func (c *Config) CAFileResolved() string { return c.PVE.CAFile }

// expandTilde expands a leading "~" or "~/" to the user home directory.
// This is the semantics POSIX shell tools (e.g. bash tilde expansion)
// provide; YAML configs are plain strings, so we do it explicitly. Other
// occurrences of "~" in the string are left alone.
func expandTilde(p string) string {
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "pveconform")
	}
	return "/tmp/pveconform-data"
}

func joinErrs(errs []string) string {
	return strings.Join(errs, "; ")
}

// sortStrings is a tiny stable lexicographic sort for cluster names.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
