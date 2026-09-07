// Package parse turns a directory of PVE manifests into a validated
// desired-state Index.
//
// Responsibilities:
//   - file walk: *.yaml / *.yml, skipping hidden dirs and .git
//   - multi-document YAML splitting
//   - apiVersion/kind routing to typed schema resources
//   - per-resource validation
//   - cross-resource checks:
//     duplicate (kind,name) → error
//     duplicate PVE id on a node (VM/LXC share PVE's id space) → error
//     depends-on targets exist → error otherwise
//     dependency cycle → error (plan §9: never partially apply)
//
// parse does NOT speak PVE; it only knows schema. The differ/planner (M3)
// consume the Index.
package parse

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// Index is the parsed, validated desired state.
type Index struct {
	byRef map[schema.Ref]schema.Resource
	order []schema.Ref
	files []string
	root  string
}

// BuildIndex reads and validates every manifest under rootDir.
//
// An empty or manifest-free tree yields an (empty) Index with ErrEmpty set to
// nil — pruning safety is the planner's job, not parse's.
func BuildIndex(rootDir string) (*Index, error) {
	files, err := walkManifests(rootDir)
	if err != nil {
		return nil, err
	}
	byRef := map[schema.Ref]schema.Resource{}
	seenID := map[string]schema.Ref{} // "node:vmid" for PVE id-space collisions
	var order []schema.Ref

	for _, rel := range files {
		docs, err := readDocs(filepath.Join(rootDir, rel))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		for i, doc := range docs {
			res, err := newResource(doc)
			if err != nil {
				return nil, fmt.Errorf("%s (document %d): %w", rel, i, err)
			}
			if err := res.Validate(); err != nil {
				return nil, fmt.Errorf("%s (document %d): %w", rel, i, err)
			}
			ref := res.Ref()
			if dup, ok := byRef[ref]; ok {
				return nil, fmt.Errorf("%s (document %d): duplicate resource %s (%s also defines it)", rel, i, ref, kindName(dup))
			}
			// PVE id space: VMs, LXC and CTs share one numeric id per node.
			if id := res.ID(); id > 0 {
				key := res.Node() + ":" + itoa(id)
				if owner, dup := seenID[key]; dup {
					return nil, fmt.Errorf("%s (document %d): PVE id %d on node %q is claimed by both %s and %s",
						rel, i, id, res.Node(), owner, ref)
				}
				seenID[key] = ref
			}
			byRef[ref] = res
			order = append(order, ref)
		}
	}

	idx := &Index{byRef: byRef, order: order, files: files, root: rootDir}
	if err := idx.checkDeps(); err != nil {
		return nil, err
	}
	return idx, nil
}

// checkDeps validates the dependency DAG: structured schema refs resolve,
// every edge target exists, and no cycle. It also runs artifact reference
// resolution (schema.ResolveArtifactRefs) so the planner reads resolved
// PVE volumes straight off the resources.
func (idx *Index) checkDeps() error {
	if err := schema.ResolveArtifactRefs(idx.List()); err != nil {
		return err
	}
	for _, ref := range idx.order {
		res := idx.byRef[ref]
		if annErr := metadataOf(res).DependsOnError(); annErr != nil {
			return fmt.Errorf("%s: %w", ref, annErr)
		}
		for _, target := range idx.EdgesFor(ref) {
			if _, ok := idx.byRef[target]; !ok {
				return fmt.Errorf("%s: dependency target %s is not defined in the repo", ref, target)
			}
		}
	}
	if cyc := indexOfCycle(idx.order, idx.EdgesFor); cyc != nil {
		return fmt.Errorf("dependency cycle detected: %s", refToCycleString(cyc))
	}
	return nil
}

// EdgesFor returns the merged dependency edge set for ref:
//   - structured schema edges (res.Deps() — VM→ISO, LXC→CTTemplate)
//   - annotation edges (metadata.depends-on — escape hatch)
func (idx *Index) EdgesFor(ref schema.Ref) []schema.Ref {
	res, ok := idx.byRef[ref]
	if !ok {
		return nil
	}
	seen := map[schema.Ref]bool{}
	var out []schema.Ref
	for _, d := range res.Deps() {
		if !seen[d] {
			out = append(out, d)
			seen[d] = true
		}
	}
	ann, _ := metadataOf(res).DependsOn()
	for _, d := range ann {
		target := d.Ref()
		if !seen[target] {
			out = append(out, target)
			seen[target] = true
		}
	}
	return out
}

// Levels returns the topological create-level of every resource: level 0 =
// no dependencies; level n = depends on resources at level < n (max parent
// level + 1). The planner uses this to order creates so prerequisites finish
// before dependants. Deterministic across re-cycles for identical input.
func (idx *Index) Levels() map[schema.Ref]int {
	levels := map[schema.Ref]int{}
	done := map[schema.Ref]bool{}
	var levelOf func(ref schema.Ref) int
	levelOf = func(ref schema.Ref) int {
		if done[ref] {
			if l, ok := levels[ref]; ok {
				return l
			}
		}
		lvl := 0
		for _, dep := range idx.EdgesFor(ref) {
			dl := levelOf(dep)
			if dl+1 > lvl {
				lvl = dl + 1
			}
		}
		levels[ref] = lvl
		done[ref] = true
		return lvl
	}
	for _, r := range idx.order {
		_ = levelOf(r)
	}
	return levels
}

// indexOfCycle walks the merged edge graph and returns one concrete cycle
// when a back-edge is found (or nil if acyclic).
func indexOfCycle(order []schema.Ref, edgesFor func(schema.Ref) []schema.Ref) []schema.Ref {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[schema.Ref]int{}
	var path []schema.Ref
	var visit func(ref schema.Ref) []schema.Ref
	visit = func(ref schema.Ref) []schema.Ref {
		color[ref] = gray
		path = append(path, ref)
		for _, dep := range edgesFor(ref) {
			switch color[dep] {
			case gray:
				for i, p := range path {
					if p == dep {
						return append(append([]schema.Ref{}, path[i:]...), dep)
					}
				}
			case white:
				if c := visit(dep); c != nil {
					return c
				}
			}
		}
		path = path[:len(path)-1]
		color[ref] = black
		return nil
	}
	for _, r := range order {
		if color[r] == white {
			if c := visit(r); c != nil {
				return c
			}
		}
	}
	return nil
}

func refToCycleString(cyc []schema.Ref) string {
	parts := make([]string, len(cyc))
	for i, r := range cyc {
		parts[i] = r.String()
	}
	return strings.Join(parts, " → ")
}

// detectCycle is iterative DFS (white→gray→black) over the dependency DAG.
func detectCycle(order []schema.Ref, edges map[schema.Ref][]schema.Ref) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[schema.Ref]int{}
	for _, start := range order {
		if color[start] != white {
			continue
		}
		var stack []schema.Ref
		color[start] = gray
		stack = append(stack, start)
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			advanced := false
			for _, dep := range edges[cur] {
				switch color[dep] {
				case gray:
					return fmt.Errorf("dependency cycle detected involving %s and %s", cur, dep)
				case white:
					color[dep] = gray
					stack = append(stack, dep)
					advanced = true
				}
			}
			if !advanced {
				color[cur] = black
			}
		}
	}
	return nil
}

// ByRef returns the resource for a Ref.
func (idx *Index) ByRef(ref schema.Ref) (schema.Resource, bool) {
	r, ok := idx.byRef[ref]
	return r, ok
}

// List returns all resources in stable input order.
func (idx *Index) List() []schema.Resource {
	out := make([]schema.Resource, len(idx.order))
	for i, ref := range idx.order {
		out[i] = idx.byRef[ref]
	}
	return out
}

// Counts is a per-kind tally (for status display and the prune empty-state guard).
func (idx *Index) Counts() map[schema.Kind]int {
	out := map[schema.Kind]int{}
	for _, ref := range idx.order {
		out[ref.Kind]++
	}
	return out
}

// Files returns the manifest paths (relative to root) the index came from.
func (idx *Index) Files() []string {
	out := make([]string, len(idx.files))
	copy(out, idx.files)
	return out
}

// Root returns the index root directory.
func (idx *Index) Root() string { return idx.root }

// --- walk + decode ---

func walkManifests(root string) ([]string, error) {
	var out []string
	walkFn := func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".yaml" || ext == ".yml" {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	}
	err := filepath.WalkDir(root, walkFn)
	if err != nil {
		return nil, err
	}
	// Deterministic order for stable diffs.
	// (strings.Sort)
	sortStrings(out)
	return out, nil
}

// readDocs decodes a multi-document YAML file into raw maps.
func readDocs(path string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var docs []map[string]any
	dec := yaml.NewDecoder(f)
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if doc == nil {
			continue
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// newResource routes a raw document to a typed resource.
func newResource(doc map[string]any) (schema.Resource, error) {
	apiVersion, _ := doc["apiVersion"].(string)
	if apiVersion != schema.APIVersion {
		return nil, fmt.Errorf("unsupported apiVersion %q (want %q)", apiVersion, schema.APIVersion)
	}
	kindRaw, _ := doc["kind"].(string)
	kind, err := schema.ParseKind(kindRaw)
	if err != nil {
		return nil, err
	}
	switch kind {
	case schema.KindVM:
		v := schema.NewVM()
		if err := docTo(v, doc); err != nil {
			return nil, err
		}
		return v, nil
	case schema.KindLXC:
		l := schema.NewLXC()
		if err := docTo(l, doc); err != nil {
			return nil, err
		}
		return l, nil
	case schema.KindCTTemplate:
		c := schema.NewCTTemplate()
		if err := docTo(c, doc); err != nil {
			return nil, err
		}
		return c, nil
	case schema.KindISO:
		iso := schema.NewISO()
		if err := docTo(iso, doc); err != nil {
			return nil, err
		}
		return iso, nil
	default:
		return nil, fmt.Errorf("unknown kind %q", kindRaw)
	}
}

// docTo round-trips a map to a struct via YAML.
func docTo(target any, doc map[string]any) error {
	b, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, target)
}

// metadataOf reaches the Metadata field without an exported accessor on the
// Resource interface (keeps the interface narrow).
func metadataOf(r schema.Resource) *schema.Metadata {
	switch v := r.(type) {
	case *schema.VM:
		return &v.Metadata
	case *schema.LXC:
		return &v.Metadata
	case *schema.CTTemplate:
		return &v.Metadata
	case *schema.ISO:
		return &v.Metadata
	default:
		return &schema.Metadata{}
	}
}

func kindName(r schema.Resource) string {
	ref := r.Ref()
	return fmt.Sprintf("%s: %s", ref.Kind, ref.Name)
}

// sortStrings is a tiny stable sort kept local to avoid an extra import block.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
