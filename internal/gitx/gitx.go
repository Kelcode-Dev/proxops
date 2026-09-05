// Package gitx is pveconform's git source of truth. It wraps go-git to keep
// a local clone of the configured repository and read out the desired
// manifest tree.
//
// Scope (MVP): HTTPS remotes with basic auth (username + token/PAT) and a
// local working-tree mode for air-gapped setups (no fetching, no network).
// SSH remotes land post-MVP.
//
// Design invariants (plan §8):
//   - git-as-snapshot: the branch head IS the desired state; history is
//     never mined for operations ("undo last push" is not a feature).
//   - advisory: fetch failures must not kill the loop — the source keeps
//     serving the last-good revision and the caller flags desiredStale.
package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// Options configure a Source.
type Options struct {
	// URL is the HTTPS repository. Mutually exclusive with Local.
	URL string
	// Token is the basic-auth secret (env PVECONFORM_GIT_TOKEN preferred).
	Token string
	// User is the basic-auth username; default "git".
	User string
	// Branch to track; default "main".
	Branch string
	// CacheDir is where the local clone cache lives; default
	// <DataDir>/git-cache. Ignored in Local mode.
	CacheDir string
	// Local, when set, points at an existing git working tree to read in
	// place (air-gapped). No fetching, no auth. Mutually exclusive with URL.
	Local string
}

type options struct {
	url      string
	token    string
	user     string
	branch   string
	cacheDir string
	local    string
}

func normalize(o Options) (options, error) {
	if o.URL != "" && o.Local != "" {
		return options{}, errors.New("git: URL and Local are mutually exclusive")
	}
	if o.URL == "" && o.Local == "" {
		return options{}, errors.New("git: set either URL or Local")
	}
	if o.User == "" {
		o.User = "git"
	}
	if o.Branch == "" {
		o.Branch = "main"
	}
	if o.CacheDir == "" {
		o.CacheDir = filepath.Join(homeShareDir(), "git-cache")
	}
	return options{url: o.URL, token: o.Token, user: o.User, branch: o.Branch, cacheDir: o.CacheDir, local: o.Local}, nil
}

// Source is the git-backed desired-state source.
//
// Invariant: after every successful operation the working tree at WorkDir()
// matches Rev(). Fetch failures preserve the last-good pair.
type Source struct {
	opts      options
	rep       *git.Repository
	remoteRef plumbing.ReferenceName // refs/remotes/origin/<branch> (URL mode)
	rev       plumbing.Hash
	lastOK    time.Time
}

// New opens (or clones) the source and positions the worktree at the branch
// head.
func New(ctx context.Context, o Options) (*Source, error) {
	_ = ctx
	opts, err := normalize(o)
	if err != nil {
		return nil, err
	}

	s := &Source{opts: opts}
	if opts.local != "" {
		rep, err := git.PlainOpen(opts.local)
		if err != nil {
			return nil, fmt.Errorf("git: open local tree %s: %w", opts.local, err)
		}
		s.rep = rep
		rev, err := s.worktreeHead()
		if err != nil {
			return nil, err
		}
		s.rev = rev
	} else {
		rep, err := openOrClone(opts)
		if err != nil {
			return nil, err
		}
		s.rep = rep
		s.remoteRef = plumbing.NewRemoteReferenceName(git.DefaultRemoteName, opts.branch)
		remote, err := rep.Remote(git.DefaultRemoteName)
		if err != nil {
			return nil, fmt.Errorf("git: remote: %w", err)
		}
		if err := remote.Fetch(&git.FetchOptions{
			RemoteName: git.DefaultRemoteName,
			Force:      true,
			Prune:      true,
			Auth:       basicAuth(opts),
		}); err != nil {
			return nil, fmt.Errorf("git: initial fetch: %w", err)
		}
		ref, err := rep.Reference(s.remoteRef, true)
		if err != nil {
			return nil, fmt.Errorf("git: resolve %s: %w", s.remoteRef, err)
		}
		if err := s.reset(ref.Hash()); err != nil {
			return nil, err
		}
		s.rev = ref.Hash()
	}
	s.lastOK = time.Now()
	return s, nil
}

// openOrClone returns an existing clone cache or creates a new one.
func openOrClone(o options) (*git.Repository, error) {
	if isRepoDir(o.cacheDir) {
		rep, err := git.PlainOpen(o.cacheDir)
		if err != nil {
			return nil, fmt.Errorf("git: reopen cache %s: %w", o.cacheDir, err)
		}
		return rep, nil
	}
	if err := os.MkdirAll(o.cacheDir, 0o755); err != nil {
		return nil, err
	}
	rep, err := git.PlainClone(o.cacheDir, false, &git.CloneOptions{
		URL:               o.url,
		ReferenceName:     plumbing.NewBranchReferenceName(o.branch),
		RemoteName:        git.DefaultRemoteName,
		SingleBranch:      false,
		Tags:              git.NoTags,
		RecurseSubmodules: git.NoRecurseSubmodules,
		Auth:              basicAuth(o),
	})
	if err != nil {
		return nil, fmt.Errorf("git: clone %s: %w", o.url, err)
	}
	return rep, nil
}

// Fetch advances the source to the branch head when it moved, and reports
// whether the revision changed.
//
// Failure is advisory: the source keeps serving its last-good Rev() and the
// error is returned for the caller to log + flag (desiredStale), not to
// abort on.
func (s *Source) Fetch(ctx context.Context) (changed bool, err error) {
	if s.opts.local != "" {
		// Local tree: re-read HEAD only (the other side mutates it in place).
		rev, herr := s.worktreeHead()
		if herr != nil {
			return false, herr
		}
		changed = rev != s.rev
		if changed {
			if rerr := s.reset(rev); rerr != nil {
				return false, rerr
			}
			s.rev = rev
			s.lastOK = time.Now()
		}
		return changed, nil
	}

	remote, err := s.rep.Remote(git.DefaultRemoteName)
	if err != nil {
		return false, fmt.Errorf("git: remote: %w", err)
	}
	if err := remote.FetchContext(ctx, &git.FetchOptions{
		RemoteName: git.DefaultRemoteName,
		Force:      true,
		Prune:      true,
		Auth:       basicAuth(s.opts),
	}); err != nil {
		if errors.Is(err, transport.ErrRepositoryNotFound) {
			return false, fmt.Errorf("git: repository vanished (%w)", err)
		}
		return false, fmt.Errorf("git: fetch: %w", err)
	}
	ref, err := s.rep.Reference(s.remoteRef, true)
	if err != nil {
		return false, fmt.Errorf("git: resolve %s: %w", s.remoteRef, err)
	}
	changed = ref.Hash() != s.rev
	if changed {
		if err := s.reset(ref.Hash()); err != nil {
			return false, err
		}
		s.rev = ref.Hash()
		s.lastOK = time.Now()
	}
	return changed, nil
}

func basicAuth(o options) *githttp.BasicAuth {
	if o.token == "" {
		return nil
	}
	return &githttp.BasicAuth{Username: o.user, Password: o.token}
}

// worktreeHead returns the current HEAD commit of the local tree.
func (s *Source) worktreeHead() (plumbing.Hash, error) {
	head, err := s.rep.Head()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return head.Hash(), nil
}

// reset hard-resets the working tree to commit.
func (s *Source) reset(commit plumbing.Hash) error {
	wt, err := s.rep.Worktree()
	if err != nil {
		return err
	}
	return wt.Reset(&git.ResetOptions{Commit: commit, Mode: git.HardReset})
}

// --- accessors ---

// Rev is the last successfully resolved branch head.
func (s *Source) Rev() plumbing.Hash { return s.rev }

// RevString is the Rev in hex.
func (s *Source) RevString() string { return s.rev.String() }

// WorkDir is the on-disk root of the manifest tree.
func (s *Source) WorkDir() string {
	if s.opts.local != "" {
		return s.opts.local
	}
	return s.opts.cacheDir
}

// BranchName is the tracked branch.
func (s *Source) BranchName() string { return s.opts.branch }

// RemoteURL is the configured remote ("URL mode"), else "".
func (s *Source) RemoteURL() string { return s.opts.url }

// IsLocal reports air-gapped local-tree mode.
func (s *Source) IsLocal() bool { return s.opts.local != "" }

// LastSuccess is the wall-clock time of the last successful positioning.
func (s *Source) LastSuccess() time.Time { return s.lastOK }

// AgeSinceSuccess is how long ago the last success was.
func (s *Source) AgeSinceSuccess() time.Duration { return time.Since(s.lastOK) }

// isRepoDir reports whether path contains a usable git repository.
func isRepoDir(p string) bool {
	if st, err := os.Stat(p); err != nil || !st.IsDir() {
		return false
	}
	for _, marker := range []string{"HEAD", "objects", "refs"} {
		if _, err := os.Stat(filepath.Join(p, ".git", marker)); err != nil {
			return false
		}
	}
	return true
}

func homeShareDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/pveconform"
	}
	return filepath.Join(home, ".local", "share", "pveconform")
}
