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

	"github.com/GizzmoShifu/proxmox-operator/internal/secrets"
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
//
// M9 (SOPS-backed cluster config): a cluster may declare a secrets-file
// reference — a pointer at a SOPS/age-encrypted file that supplies THIS
// cluster's PVE credentials. The decrypted values live in memory only
// (see Config.SopsResolved), are never serialized, and never reach logs /
// status / diff / metrics / errors (task §2). See PVECredentialReferences
// for the task §3 "small, explicit" reference block.
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
	// SecretsFile is the path to this cluster's SOPS-encrypted secret file.
	// A relative path resolves against the DIRECTORY OF THE PVECONFORM
	// CONFIG FILE, not the CWD (task §5 — same config works from any CWD).
	// Empty means "this cluster does not use SOPS": the shared pve.-level
	// bootstrap credentials + env overlay apply, as they did in M8.
	//
	// The SOPS file shape is flat under a top-level `secrets:` mapping:
	//
	//	secrets:
	//	  pveconform-user: root@pam
	//	  pveconform-token-id: pveconform
	//	  pveconform-token: <the PVE API token value>
	//	  pveconform-password: <only when the cluster's pve.auth=ticket>
	//	  pveconform-git-token: <the git fetch token, if git.mode=url>
	//
	// The age recipient (public key) is recorded in the SOPS file's `sops:`
	// metadata and may be committed (task §7 "the repository may contain the
	// public age recipient configuration required to encrypt secrets");
	// the age identity (PRIVATE key) MUST come from OUTSIDE the encrypted
	// repository — the standard SOPS age identity mechanism on the
	// pveconform host's process environment
	// (SOPS_AGE_KEY_FILE / SOPS_AGE_KEY / AGE_KEY_FILE — task §7, §8).
	// pveconform decrypts into memory only and never writes the plaintext
	// to disk (task §2, §13).
	SecretsFile string `yaml:"secrets-file"`
	// Secrets references which decrypted SOPS keys carry which pveconform
	// credential fields for this cluster. The reference shape is the
	// "small, explicit" block (task §3 "a configuration concept such as...
	// may be appropriate"):
	//
	//	secrets-file: secrets.sops.yaml
	//	secrets:
	//	  pve:
	//	    user: pveconform-user
	//	    token-id: pveconform-token-id
	//	    token: pveconform-token
	//	  git:
	//	    token: pveconform-git-token
	//
	// When SecretsFile is set but the reference is missing for a field,
	// pveconform falls back to the shared pve.-level bootstrap YAML/env
	// value for that field. A field that IS referenced but has a
	// missing/empty value in the SOPS file FAILS CLOSED — pveconform never
	// silently substitutes a lower-precedence source for a field the
	// operator asked SOPS to carry (task §6 "do not allow an empty/missing
	// SOPS secret to silently result in an unintended credential being
	// used").
	Secrets PVECredRefs `yaml:"secrets"`
}

// PVECredRefs is the per-cluster reference block: which SOPS file keys
// supply which pveconform credential fields. The shape is closed and
// explicit (task §3, §14 "small, explicit secret format" — no template
// language, no glob, no interpolation).
type PVECredRefs struct {
	// PVE names which decrypted SOPS keys carry which PVE credential
	PVE PVESecretRefs `yaml:"pve"`
	// Git names which decrypted SOPS key carries the git fetch token
	Git GitSecretRefs `yaml:"git"`
}

// PVESecretRefs names the SOPS key for each PVE credential field. All
// fields are optional; an empty string = "no SOPS reference for this
// field, use bootstrap pve-level YAML/env value".
type PVESecretRefs struct {
	// User names the SOPS key for the PVE user id (token owner).
	User string `yaml:"user"`
	// TokenID names the SOPS key for the PVE API token id.
	TokenID string `yaml:"token-id"`
	// Token names the SOPS key for the PVE API token value (uuid).
	Token string `yaml:"token"`
	// Password names the SOPS key for the PVE user password
	// (ticket auth only).
	Password string `yaml:"password"`
}

// GitSecretRefs names the SOPS key for the git fetch token. Empty means
// "no SOPS reference; use the shared git-level YAML/env value" (M8).
type GitSecretRefs struct {
	Token string `yaml:"token"`
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

	// SopsResolved holds the IN-MEMORY, decrypted SOPS secret values for
	// every cluster that declared a SOPS reference. It is populated by
	// Config.ResolveSOPS and consumed by PVEParamsFrom + the git source
	// (in internal/app). It is intentionally tagged `json:"-"`: the
	// values must never round-trip through JSON/YAML status surfaces (task
	// §12 "plaintext secrets do not appear in status / metrics / normal
	// CLI output"). Cluster isolation: cluster A's entry never affects
	// cluster B's effective credentials (task §12). The values live only
	// for the lifetime of this Config object: never written to disk, never
	// logged (task §2).
	SopsResolved map[string]SopsClusterSecrets `json:"-" yaml:"-"`
}

// SopsClusterSecrets holds the decrypted, in-memory PVE credentials for one
// cluster, keyed by the PVE credential fields pveconform needs. The git
// fetch token is shared (not per-cluster) because pveconform has a single
// git source (URL mode) that serves every cluster.
type SopsClusterSecrets struct {
	User     string
	TokenID  string
	Token    string
	Password string
	GitToken string
	// SourceFile records the SOPS file that produced these values. It is
	// an on-disk path, not secret material. Used in error text only.
	SourceFile string
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
//
// Path resolution (M9):
//   - relative `pve.clusters.<name>.secrets-file` paths resolve against
//     the DIRECTORY OF THE PVECONFORM CONFIG FILE, not the CWD. Same
//     cluster-local config → same SOPS file, regardless of where
//     pveconform is invoked from (task §5, §18).
//   - `git.path: "."` is a sentinel that resolves to the nearest `.git`
//     ancestor of the config file's directory; the config path itself is
//     canonicalised to absolute first so that CWD-relative `--config
//     clusters/<name>/config.yaml` works. If no `.git` ancestor is found,
//     Load fails closed: pveconform does not guess the git tree
//     (task §5 "no implicit magic").
//
// Load does NOT decrypt anything. SOPS happens in Config.ResolveSOPS
// (called by app.New after Load + OverlayFromEnv), so that hosts running
// pveconform without SOPS credentials never invoke the sops binary
// (task §16).
func Load(path string) (*Config, error) {
	c := Defaults()
	if path == "" {
		return c, nil
	}

	// M9: canonicalise the config path to absolute. This matters when
	// `--config` is a CWD-relative path like
	// `clusters/conformance-dev/config.yaml` from the repo root: otherwise
	// the git.path "." walk-up + SOPS-file resolution operate on relative
	// dirs and composition path checks (which compare absolute against
	// absolute) misfire. The operator's CWD is the correct reference for a
	// relative config path, so Abs() it once up front and keep going.
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve config path %s: %w", path, err)
		}
		path = abs
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

	// M9: resolve per-cluster secrets-file paths. Relative paths are
	// resolved against the DIRECTORY OF THE PVECONFORM CONFIG FILE. This
	// keeps the GitOps layout self-contained: clusters/<name>/config.yaml
	// + clusters/<name>/secrets.sops.yaml live side by side, and the
	// agent's SOPS call is independent of process CWD.
	if base := filepath.Dir(path); base != "" && base != "." {
		for name, cl := range c.PVE.Clusters {
			if cl.SecretsFile == "" {
				continue
			}
			if !filepath.IsAbs(cl.SecretsFile) {
				cl.SecretsFile = filepath.Join(base, filepath.Clean("./"+cl.SecretsFile))
			}
			c.PVE.Clusters[name] = cl
		}
	}

	// M9: the git.path sentinel. "git.path: ." means "this pveconform config
	// lives inside the GitOps repo; use the repo that contains this config
	// file as the git work tree". Load walks upward from the config file's
	// directory to the nearest .git marker. If no such ancestor exists, Load
	// fails closed with a clear error: pveconform never silently picks a git
	// tree for the operator (task §5: no implicit magic, no silent
	// selection).
	if c.Git.Path == "." {
		if root := walkUpForGitRoot(filepath.Dir(path)); root != "" {
			c.Git.Path = root
		} else {
			return nil, fmt.Errorf("git.path: \".\" was set in %s but no .git worktree was found in any ancestor directory; pveconform will not guess the git tree — cd into the GitOps repository and retry, or set an explicit git.path", path)
		}
	}
	return c, nil
}

// walkUpForGitRoot finds the root of the git worktree that contains dir, by
// walking upward until a `.git` entry (file or dir) appears. Returns "" when
// no such root exists in any ancestor up to the filesystem root. The `.git`
// marker is the standard git checkout indicator; a `.git` FILE is what git
// submodules and clones-in-monorepos use, so both shapes are accepted.
func walkUpForGitRoot(dir string) string {
	if dir == "" || dir == "." {
		return ""
	}
	cur := dir
	for {
		if _, err := os.Stat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

// ResolveSOPS decrypts every cluster's SOPS file (in memory) and stores the
// values in c.SopsResolved. It is called by the CLI (buildAgent) and by
// app.New AFTER config.Load, and BEFORE config.Validate (which needs to see
// the resolved SOPS fields) — see the precedence documentation. The values
// are never written to disk and never logged (task §2, §13).
//
// Behaviour (fail closed, task §6):
//   - A cluster with no SecretsFile is skipped (bootstrap credentials
//     apply; M8 behaviour).
//   - A cluster whose named SOPS key is missing/empty in the decrypted
//     file errors (pveconform never silently substitutes a lower-
//     precedence source for a field the operator asked SOPS to carry).
//   - An unencrypted/invalid SOPS file errors (secrets.ErrUnencryptedSecrets /
//     secrets.ErrMalformedDocument).
//   - A wrong/absent age identity errors (secrets.ErrNoIdentity / secrets.
//     ErrIdentityMismatch).
//
// The SOPS age identity (private key) comes from OUTSIDE the encrypted
// repository — the operator's process environment (SOPS_AGE_KEY_FILE / SOPS_
// AGE_KEY / AGE_KEY_FILE). pveconform never sets or reads it itself
// (task §8).
func (c *Config) ResolveSOPS() error {
	c.SopsResolved = map[string]SopsClusterSecrets{}
	// Deterministic order: clusters sorted by name.
	for _, name := range c.PVE.ClusterNames() {
		cl := c.PVE.Clusters[name]
		if cl.SecretsFile == "" {
			continue
		}
		val, err := secrets.DecryptFile(cl.SecretsFile)
		if err != nil {
			return fmt.Errorf("cluster %s: SOPS decrypt %s: %w", name, cl.SecretsFile, err)
		}
		sc := SopsClusterSecrets{SourceFile: cl.SecretsFile}
		if ref := cl.Secrets.PVE.User; ref != "" {
			v, ok := val.Get(ref)
			if !ok {
				return sopsKeyMissingErr(name, cl.SecretsFile, ref, "pve.user")
			}
			sc.User = v
		}
		if ref := cl.Secrets.PVE.TokenID; ref != "" {
			v, ok := val.Get(ref)
			if !ok {
				return sopsKeyMissingErr(name, cl.SecretsFile, ref, "pve.token-id")
			}
			sc.TokenID = v
		}
		if ref := cl.Secrets.PVE.Token; ref != "" {
			v, ok := val.Get(ref)
			if !ok {
				return sopsKeyMissingErr(name, cl.SecretsFile, ref, "pve.token")
			}
			sc.Token = v
		}
		if ref := cl.Secrets.PVE.Password; ref != "" {
			v, ok := val.Get(ref)
			if !ok {
				return sopsKeyMissingErr(name, cl.SecretsFile, ref, "pve.password")
			}
			sc.Password = v
		}
		if ref := cl.Secrets.Git.Token; ref != "" {
			v, ok := val.Get(ref)
			if !ok {
				return sopsKeyMissingErr(name, cl.SecretsFile, ref, "git token")
			}
			sc.GitToken = v
		}
		c.SopsResolved[name] = sc
	}
	return nil
}

func sopsKeyMissingErr(cluster, file, key, field string) error {
	return fmt.Errorf("cluster %s: SOPS file %s declares key %q for %s, but that key is missing or empty in the decrypted SOPS document; pveconform fails closed rather than falling back to a bootstrap credential for that field — add the key to the encrypted file or drop the reference from config.yaml",
		cluster, shortenPath(file), key, field)
}

// shortenPath bounds a filesystem path to 80 chars for use in error text.
// (config.go does not need full path truncation elsewhere; this helper
// is deliberately local to keep error messages stable and scannable.)
func shortenPath(p string) string {
	if len(p) > 80 {
		return p[:79] + "…"
	}
	return p
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
//
// M9 (SOPS-aware): when a cluster supplies its PVE credentials via
// SopsResolved (see Config.ResolveSOPS), the global pve.user + pve.token-id +
// pve.token triple is NOT required in the YAML/env for that cluster. The
// per-cluster effective-credential check happens at PVEParamsFrom time:
// every constructed pveclient params MUST carry a composed credential,
// which ValidateCreds (below) enforces for each cluster after resolution.
// This is what lets the cluster-local GitOps config carry credentials that
// have been encrypted with SOPS without ever appearing in the config YAML.
func (c *Config) Validate() error {
	var errs []string

	switch c.PVE.Auth {
	case AuthToken:
		// Global pve.user/token-id/token are the BOOTSTRAP credentials.
		// If at least one cluster resolves SOPS credentials, the bootstrap
		// triple may be empty, but at least some cluster-level SOPS override
		// must cover each of user/token-id/token — checked below.
		// If NO cluster has SOPS, the M8 behaviour applies: bootstrap creds
		// MUST be present.
		if !c.sopsCoversCreds() {
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
		}
		if c.PVE.Password != "" {
			errs = append(errs, "pve.password must be empty when pve.auth=token")
		}
		c.PVE.Password = "" // never carry the unused secret
	case AuthTicket:
		if !c.sopsCoversCreds() {
			if c.PVE.User == "" {
				errs = append(errs, "pve.user is required for ticket auth")
			}
			if c.PVE.Password == "" {
				errs = append(errs, "pve.password missing (set PVECONFORM_PVE_PASSWORD)")
			}
		}
	default:
		errs = append(errs, fmt.Sprintf("pve.auth must be %q or %q, got %q", AuthToken, AuthTicket, c.PVE.Auth))
	}

	// M9: per-cluster credential coverage, mirroring PVEParamsFrom exactly
	// (so Validate rejects every shape that would build a PVE client without
	// a usable credential). The "effective" credential for a cluster is:
	//   - if ANY SOPS field resolved for it (hasSops): the global
	//     TokenValue is DROPPED (never let a global composed credential
	//     shadow a SOPS cluster), and user/token-id/token/password are
	//     each the SOPS value when SOPS named one, else the global value;
	//   - else (bootstrap cluster): the global user/token-id/token/password
	//     + global TokenValue, exactly as M8.
	for _, name := range c.PVE.ClusterNames() {
		sc := c.SopsResolved[name]
		hasSops := sc.User != "" || sc.TokenID != "" || sc.Token != "" || sc.Password != ""
		eUser := c.PVE.User
		if sc.User != "" {
			eUser = sc.User
		}
		eTokID := c.PVE.TokenID
		if sc.TokenID != "" {
			eTokID = sc.TokenID
		}
		eTok := c.PVE.Token
		if sc.Token != "" {
			eTok = sc.Token
		}
		ePsw := c.PVE.Password
		if sc.Password != "" {
			ePsw = sc.Password
		}
		eVal := c.PVE.TokenValue
		if hasSops {
			eVal = ""
		}
		switch c.PVE.Auth {
		case AuthToken:
			if eUser == "" {
				errs = append(errs, fmt.Sprintf("pve.clusters.%s: no effective PVE user after credential resolution (SOPS user reference + pve.user + PVECONFORM_PVE_USER all empty); pveconform fails closed", name))
			}
			if eVal == "" && (eTokID == "" || eTok == "") {
				errs = append(errs, fmt.Sprintf("pve.clusters.%s: no effective PVE token after credential resolution (need a SOPS pve token reference, or pve.token-id+pve.token, or PVECONFORM_PVE_TOKEN_VALUE); pveconform fails closed", name))
			}
		case AuthTicket:
			if eUser == "" {
				errs = append(errs, fmt.Sprintf("pve.clusters.%s: no effective PVE user for ticket auth; pveconform fails closed", name))
			}
			if ePsw == "" {
				errs = append(errs, fmt.Sprintf("pve.clusters.%s: no effective PVE password for ticket auth (SOPS pve.password reference or PVECONFORM_PVE_PASSWORD required); pveconform fails closed", name))
			}
		}
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

// sopsCoversCreds reports whether at least one cluster has SOPS-resolved
// PVE credentials available. Used by Validate to allow SOPS-using
// deployments to run without the global pve-level bootstrap credentials
// (the SOPS file in the GitOps worktree supplies them per-cluster).
func (c *Config) sopsCoversCreds() bool {
	for name, sc := range c.SopsResolved {
		_ = name
		if sc.User != "" || sc.TokenID != "" || sc.Token != "" || sc.Password != "" {
			return true
		}
	}
	return false
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
