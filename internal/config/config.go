// Package config holds the pveconform runtime configuration.
//
// Precedence (lowest to highest): built-in defaults, then the YAML config
// file (--config), then explicit command-line flags, then selected
// environment variables (credentials only; never in YAML or flags).
package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// PVEConfig holds Proxmox connection settings.
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
	// Port is the PVE API (pveproxy) port. Default 8006.
	Port int `yaml:"port"`
	// CAFile optionally points at the PVE cluster self-signed CA certificate.
	// When empty the system trust store is used.
	CAFile string `yaml:"ca-file"`
	// Gateway is the node used to bootstrap ticket auth and reach
	// cluster-wide endpoints. Default "pve".
	Gateway string `yaml:"gateway"`
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

// Defaults returns a Config with sensible default values.
func Defaults() *Config {
	return &Config{
		Log:     LogConfig{Level: "info"},
		PVE:     PVEConfig{Auth: AuthToken, Port: 8006, Gateway: "pve"},
		Git:     GitConfig{Branch: "main"},
		Rec:     ReconcileConfig{PollInterval: 30 * time.Second, TaskTimeout: 30 * time.Minute, PruneBudget: 3},
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
	return c, nil
}

// applyEnv overlays selected environment variables.
// Credentials come ONLY from the environment.
func applyEnv(c *Config) {
	if v := os.Getenv("PVECONFORM_PVE_TOKEN"); v != "" {
		c.PVE.Token = v
	}
	if v := os.Getenv("PVECONFORM_PVE_TOKEN_VALUE"); v != "" {
		c.PVE.TokenValue = v
	}
	if v := os.Getenv("PVECONFORM_PVE_TOKEN_VALUE"); v != "" {
		c.PVE.TokenValue = v
	}
	if v := os.Getenv("PVECONFORM_PVE_PASSWORD"); v != "" {
		c.PVE.Password = v
	}
	if v := os.Getenv("PVECONFORM_GIT_TOKEN"); v != "" {
		c.Git.Token = v
	}
}

// resolveUserEnv applies PVECONFORM_PVE_USER when set (convenience for
// ticket-auth deployments that cannot store the user in config at all).
func resolveUserEnv(c *Config) {
	if v, ok := os.LookupEnv("PVECONFORM_PVE_USER"); ok {
		c.PVE.User = v
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

	if c.PVE.Port < 1 || c.PVE.Port > 65535 {
		errs = append(errs, "pve.port out of range")
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

func defaultDataDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "pveconform")
	}
	return "/tmp/pveconform-data"
}

func joinErrs(errs []string) string {
	return strings.Join(errs, "; ")
}
