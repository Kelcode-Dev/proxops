// Package composition is the multi-cluster GitOps boundary for proxops (M8).
//
// The repository is the source of truth. Its shape:
//
//	clusters/<cluster>/config.yaml     # cluster-specific config (future; not SOPS)
//	clusters/<cluster>/resources.yaml  # declares which resource files this
//	                                   # cluster consumes (the composition)
//	<kind>/base/*.yaml                 # reusable resource definitions
//	<kind>/<cluster>/*.yaml            # cluster-specific definitions
//
// where <kind> is one of vm, lxc, iso, ctt.
//
// A cluster's composition is an explicit list of resource file paths (relative
// to clusters/<cluster>/, e.g. ../../vm/base/foo.yaml). There is NO implicit
// overlay, inheritance, or merge: a cluster consumes exactly the files it
// lists. The same file may be referenced by several clusters (a reusable base
// resource is shared by reference, never by ownership).
//
// composition is read-only: it never writes to the git tree. It resolves and
// validates composition paths, and enforces the repo-shape invariant that
// fails closed on the legacy layout — a resource file placed directly under a
// kind root (vm/foo.yaml) is NOT auto-discovered; it must live under
// <kind>/base/ or <kind>/<cluster>/ and be listed by the consuming cluster.
package composition

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/GizzmoShifu/proxmox-operator/internal/config"
)

// rootDirs are the resource kind roots. Any *.yaml placed DIRECTLY under one of
// these directories is legacy shape and is rejected by checkRepoShape.
var rootDirs = []string{"vm", "lxc", "iso", "ctt", "templatevm", "diskimage"}

// Composition is one cluster's discovered resource list.
type Composition struct {
	// Cluster is the cluster name (the directory under clusters/).
	Cluster string
	// Files are resource file paths relative to the repo root, in declaration
	// order.
	Files []string
}

// ValidClusterName re-exports config.ValidClusterName so parse layers do not
// import config directly.
func ValidClusterName(s string) bool { return config.ValidClusterName(s) }

// KindForPath maps a resource path (relative to the repository root,
// slash-separated) to its top-level kind directory, or "" when the path is
// not rooted at a recognised kind. parse uses this to fail closed on
// manifests placed outside the M8 kind roots.
func KindForPath(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return ""
	}
	// M11: "templatevm" is the 5th kind root for the new TemplateVM manifest
	// (a PVE qemu object that has been marked template). "diskimage" is the
	// 6th (a downloadable qcow2/vmdk/raw image seeding a VM disk via
	// import-from).
	switch parts[0] {
	case "vm", "lxc", "iso", "ctt", "templatevm", "diskimage":
		return parts[0]
	default:
		return ""
	}
}

// SortedFiles returns the composition's files in lexicographic order
// (deterministic; used by the parse layer).
func (c *Composition) SortedFiles() []string {
	out := make([]string, len(c.Files))
	copy(out, c.Files)
	sort.Strings(out)
	return out
}

// AllCompositions returns the composition for every cluster directory found
// under <root>/clusters/, plus a repository-wide error when the shape is
// invalid (legacy top-level resources, missing/unsatisfiable paths, a
// configured cluster with no composition, or a composition for an unknown
// cluster).
//
// configuredClusters is the set of cluster names present in pve.clusters of
// the proxops config. Both directions are checked so nothing is silently
// dropped:
//   - a composition without a configured PVE endpoint fails closed (unknown
//     cluster names must fail closed);
//   - a configured PVE endpoint without a composition fails closed (there is
//     nothing proxops can reconcile on it — and pruning off an unknown
//     desired set is exactly the footgun the composition exists to prevent).
//
// An empty resources: list for a configured cluster is a valid, safe
// composition: zero desired resources on that cluster, no prunes.
func AllCompositions(root string, configuredClusters []string) (map[string]*Composition, error) {
	compositions := map[string]*Composition{}
	var errs []string

	clustersDir := filepath.Join(root, "clusters")
	entries, err := os.ReadDir(clustersDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			comp, cErr := loadComposition(root, clustersDir, name)
			if cErr != nil {
				errs = append(errs, cErr.Error())
				continue
			}
			compositions[name] = comp
		}
	} else if !os.IsNotExist(err) {
		errs = append(errs, fmt.Sprintf("read clusters/: %v", err))
	}

	// Repo-shape: reject legacy top-level resources.
	if shapeErr := checkRepoShape(root); shapeErr != nil {
		errs = append(errs, shapeErr.Error())
	}

	// config <-> git cross-check (fail both directions closed).
	configured := map[string]bool{}
	for _, c := range configuredClusters {
		configured[c] = true
	}
	for name := range compositions {
		if !configured[name] {
			errs = append(errs, fmt.Sprintf("composition clusters/%s declares resources but pve.clusters has no entry for %q; add the cluster to the proxops config (unknown cluster names fail closed)", name, name))
		}
	}
	for name := range configured {
		if _, ok := compositions[name]; !ok {
			errs = append(errs, fmt.Sprintf("pve.clusters.%s is configured but has no composition at clusters/%s/resources.yaml; every configured cluster needs an explicit resource list (or an empty resources: list)", name, name))
		}
	}

	if len(errs) > 0 {
		return compositions, fmt.Errorf("composition: %s", strings.Join(errs, "; "))
	}
	return compositions, nil
}

// loadComposition reads clusters/<name>/resources.yaml and resolves each
// resource path relative to that directory. It verifies that every referenced
// file exists, stays inside the repository root, and is a manifest. Duplicate
// or out-of-root references fail closed.
func loadComposition(root, clustersDir, name string) (*Composition, error) {
	if !config.ValidClusterName(name) {
		return nil, fmt.Errorf("clusters/%s is not a valid cluster-name directory (lowercase alnum + '-', no leading '-')", name)
	}
	resPath := filepath.Join(clustersDir, name, "resources.yaml")
	relRes := relSlash(root, resPath)
	b, err := os.ReadFile(resPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("clusters/%s/ exists but has no resources.yaml (a cluster directory must contain resources.yaml)", name)
		}
		return nil, fmt.Errorf("read %s: %v", relRes, err)
	}
	var doc struct {
		Resources []string `yaml:"resources"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		// Accept a bare top-level list of paths as well.
		var bare []string
		if berr := yaml.Unmarshal(b, &bare); berr == nil {
			doc.Resources = bare
		} else {
			return nil, fmt.Errorf("parse %s: %v", relRes, err)
		}
	}
	compDir := filepath.Join(clustersDir, name)
	comp := &Composition{Cluster: name}
	seen := map[string]bool{}
	for _, p := range doc.Resources {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, ok := resolveWithinRoot(root, compDir, p)
		if !ok {
			return nil, fmt.Errorf("%s: resource path %q escapes the repository root or is malformed", relRes, p)
		}
		rel := relSlash(root, abs)
		st, sErr := os.Stat(abs)
		if sErr != nil {
			return nil, fmt.Errorf("%s: references missing resource %s", relRes, rel)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%s: references a directory %s (resource paths must be .yaml files)", relRes, rel)
		}
		ext := strings.ToLower(filepath.Ext(rel))
		if ext != ".yaml" && ext != ".yml" {
			return nil, fmt.Errorf("%s: references non-manifest path %s (want .yaml)", relRes, rel)
		}
		if seen[rel] {
			return nil, fmt.Errorf("%s: references the same resource %s more than once", relRes, rel)
		}
		seen[rel] = true
		comp.Files = append(comp.Files, rel)
	}
	return comp, nil
}

// checkRepoShape walks the four kind roots and fails closed on any legacy
// top-level manifest (<kind>/<file>.yaml placed directly under the kind
// root). The M8 model places resources under <kind>/base/ or
// <kind>/<cluster>/; a bare file directly under the kind root is the old
// layout and is rejected with a remediation hint.
func checkRepoShape(root string) error {
	for _, kind := range rootDirs {
		kindDir := filepath.Join(root, kind)
		wd, err := os.ReadDir(kindDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue // kind dir absent is fine
			}
			return fmt.Errorf("read %s/: %v", kind, err)
		}
		for _, e := range wd {
			if e.IsDir() {
				continue // base/ or <cluster>/: the new model
			}
			ext := strings.ToLower(e.Name())
			if !strings.HasSuffix(ext, ".yaml") && !strings.HasSuffix(ext, ".yml") {
				continue // not a manifest (README.md, .keep, ...)
			}
			return fmt.Errorf("legacy layout: %s sits directly under the %s/ kind root, which M8 no longer auto-discovers; move it to %s/base/ or %s/<cluster>/ and list it in the consuming cluster's clusters/<cluster>/resources.yaml",
				relSlash(root, filepath.Join(kindDir, e.Name())), kind, kind, kind)
		}
	}
	return nil
}

// resolveWithinRoot resolves rel (a composition path that may contain ..)
// against baseDir and reports whether the result stays within root.
func resolveWithinRoot(root, baseDir, rel string) (string, bool) {
	root = strings.TrimRight(filepath.Clean(root), string(filepath.Separator))
	clean := filepath.Clean(filepath.Join(baseDir, rel))
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return clean, false
	}
	return clean, true
}

// relSlash returns p relative to root, slash-separated.
func relSlash(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}
