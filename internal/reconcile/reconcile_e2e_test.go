// E2E: the full apply pipeline against the stateful mock PVE, driven by a
// git work-tree (local source mode). It exercises the whole chain:
//
//	gitx.Source(local) → reconcile.RunOneCycle → parse.BuildIndex →
//	plan.LoadLive + plan.PlanActions → exec.Executor.Run → mock PVE
//
// Invariants verified:
//   - create → PVE object appears with proxops ownership tag + desired power
//   - idempotency: a second cycle on converged state produces zero actions
//   - prune: a proxops-tagged live VM absent from git is deleted
//   - budget: 4 tagged orphans with budget=3 → only 3 pruned, 1 deferred
//   - ownership gate: an untagged live VM is NEVER deleted
//   - anomaly: desired empty but tagged live present → prunes suppressed
//   - diff (dry-run): plan reported, PVE untouched
package reconcile_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"gopkg.in/yaml.v3"

	"github.com/GizzmoShifu/proxmox-operator/internal/exec"
	"github.com/GizzmoShifu/proxmox-operator/internal/gitx"
	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient/mock"
	"github.com/GizzmoShifu/proxmox-operator/internal/reconcile"
	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
	"github.com/GizzmoShifu/proxmox-operator/internal/statusx"
)

const e2eCluster = "default"

// newGitRepo initialises a git work-tree at dir with the supplied files
// committed on branch main, and returns the dir path.
//
// M8: fixtures written in the legacy "vm.yaml"-style top level are
// relocated to the multi-cluster layout (<kind>/default/... plus
// clusters/default/resources.yaml) so the reconciler's BuildClusterIndex
// path is exercised. Files already under a directory (clusters/...,
// vm/..., ...) are written through verbatim.
func newGitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	// If the caller already built an M8 multi-cluster tree (any key under
	// "clusters/"), write it verbatim: no relocation.
	hasClusters := false
	for k := range files {
		if strings.HasPrefix(k, "clusters/") {
			hasClusters = true
			break
		}
	}
	if !hasClusters {
		files = relocateForM8(t, files, e2eCluster)
	}
	rep, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("git init: %v", err)
	}
	// Seed a .keep so go-git can create an initial commit even when the
	// desired manifest set is empty (go-git refuses empty commits). Parse
	// only reads *.yaml / *.yml, so .keep is inert.
	keepMap := map[string]string{}
	for k, v := range files {
		keepMap[k] = v
	}
	if len(keepMap) == 0 {
		keepMap[".keep"] = "proxops e2e placeholder\n"
	}
	w, werr := rep.Worktree()
	if werr != nil {
		t.Fatalf("worktree: %v", werr)
	}
	for name, content := range keepMap {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Add(name); err != nil {
			t.Fatalf("git add %s: %v", name, err)
		}
	}
	if _, err := w.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{Name: "proxops-test", Email: "t@example.com"},
	}); err != nil {
		t.Fatalf("git commit: %v", err)
	}
	// Move HEAD to a "main" branch so local-mode reads are anchored.
	if err := w.Checkout(&git.CheckoutOptions{Branch: "refs/heads/main", Create: true, Force: true}); err != nil {
		t.Fatalf("git checkout main: %v", err)
	}
}

// relocateForM8 rewrites legacy top-level manifest fixtures into the M8
// multi-cluster layout for the harness cluster. Top-level .yaml files that
// hold one or more proxops manifests become <kind>/<cluster>/<name>.yaml
// files; a clusters/<cluster>/resources.yaml listing the result is emitted.
// Non-manifest files (.keep, README) and already-structured paths pass
// through unchanged.
func relocateForM8(t *testing.T, files map[string]string, cluster string) map[string]string {
	t.Helper()
	out := map[string]string{
		fmt.Sprintf("clusters/%s/resources.yaml", cluster): "", // filled below
	}
	var rels []string
	kindDir := map[schema.Kind]string{
		schema.KindVM:         "vm",
		schema.KindLXC:        "lxc",
		schema.KindISO:        "iso",
		schema.KindCTTemplate: "ctt",
		// M11: TemplateVM manifests live under the templatevm/ kind root
		// (mirrors composition.rootDirs and adopt.kindPath).
		schema.KindTemplateVM: "templatevm",
		// M11+: DiskImage artifacts live under the diskimage/ kind root.
		schema.KindDiskImage: "diskimage",
	}
	used := map[string]bool{}

	for key, content := range files {
		if strings.Contains(key, "/") || !strings.HasSuffix(key, ".yaml") && !strings.HasSuffix(key, ".yml") {
			out[key] = content
			continue
		}
		docs, derr := splitDocs(content)
		if derr != nil {
			out[key] = content
			continue
		}
		for i, doc := range docs {
			apiVer := yamlString(doc, "apiVersion")
			if apiVer != schema.APIVersion {
				continue
			}
			kindStr := yamlString(doc, "kind")
			kind, kerr := schema.ParseKind(kindStr)
			if kerr != nil {
				out[key] = content
				continue
			}
			name := yamlString(doc["metadata"].(map[string]any), "name")
			if strings.TrimSpace(name) == "" {
				name = strings.TrimSuffix(key, filepath.Ext(key))
			}
			name = m8Sanitize(name)
			path := kindDir[kind] + "/" + cluster + "/" + name + ".yaml"
			for used[path] {
				name = name + "-x"
				path = kindDir[kind] + "/" + cluster + "/" + name + ".yaml"
			}
			used[path] = true
			_ = i
			yb, merr := yaml.Marshal(doc)
			if merr != nil {
				t.Fatalf("marshal relocated doc: %v", merr)
			}
			out[path] = string(yb)
			rels = append(rels, "../../"+path)
		}
	}
	sort.Strings(rels)
	var b bytes.Buffer
	b.WriteString("resources:\n")
	if len(rels) == 0 {
		b.WriteString("  []\n")
	} else {
		for _, r := range rels {
			fmt.Fprintf(&b, "  - %s\n", r)
		}
	}
	out[fmt.Sprintf("clusters/%s/resources.yaml", cluster)] = b.String()
	return out
}

func yamlString(v any, key string) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// splitDocs decodes a multi-document YAML string into raw maps.
func splitDocs(content string) ([]map[string]any, error) {
	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(content))
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

// m8Sanitize collapses a name to a lowercase alnum + '-' dns label.
func m8Sanitize(s string) string {
	var b strings.Builder
	prevDash := true
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteByte(byte(c))
			prevDash = false
			continue
		}
		if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
		}
		prevDash = true
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "unnamed"
	}
	return out
}

// node is the PVE node name used throughout (mock is a single-node cluster in
// these tests).
const node = "pve01"

// apiToken is the accepted PVEAPIToken value.
const apiToken = "root@pam!proxops=deadbeef"

// harness wires gitx(local) + mock PVE into a Reconciler.
type harness struct {
	gitDir string
	src    *gitx.Source
	pve    *pveclient.Client
	mock   *mock.Server
	store  *statusx.Store
	rec    *reconcile.Reconciler // apply-mode (executor wired)
	dry    *reconcile.Reconciler // dry-mode (no writes)
	log    *slog.Logger
}

func (h *harness) apply(t *testing.T) *plan.Plan {
	t.Helper()
	res, p, err := h.rec.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("apply cycle: %v", err)
	}
	if res.Aborted && p == nil {
		t.Fatalf("apply cycle aborted: %s", res.AbortReason)
	}
	return p
}

func (h *harness) diff(t *testing.T) *plan.Plan {
	t.Helper()
	res, p, err := h.dry.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("diff cycle: %v", err)
	}
	if res.Aborted && p == nil {
		t.Fatalf("diff cycle aborted: %s", res.AbortReason)
	}
	return p
}

func newHarness(t *testing.T, gitFiles map[string]string, budget int) *harness {
	t.Helper()
	log := slog.Default()

	gitDir := t.TempDir()
	newGitRepo(t, gitDir, gitFiles)

	m := mock.New(mock.Config{
		Token:          apiToken,
		TicketUser:     "root@pam",
		TicketPassword: "secret",
		TaskTicks:      1,
	})
	t.Cleanup(m.Close)

	src, err := gitx.New(context.Background(), gitx.Options{Local: gitDir})
	if err != nil {
		t.Fatalf("gitx.New(local): %v", err)
	}
	pve, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:    "root@pam",
			Auth:    "token",
			TokenID: "proxops",
			Token:   "deadbeef",
		},
		BaseURL:     m.URL(),
		HTTPTimeout: 5 * time.Second,
	}, log)
	if err != nil {
		t.Fatalf("pveclient.New: %v", err)
	}

	store := statusx.New()

	executor := exec.New(pve, 30*time.Second, 2*time.Millisecond, store, log)
	executor.SetCluster(e2eCluster)
	rec, err := reconcile.New(reconcile.Options{
		PVE:                pve,
		Fetcher:            src,
		Store:              store,
		Budget:             plan.Budget{Prune: budget},
		Executor:           executor,
		Cluster:            e2eCluster,
		ConfiguredClusters: []string{e2eCluster},
		Log:                log,
	})
	if err != nil {
		t.Fatalf("reconcile.New(apply): %v", err)
	}
	dry, err := reconcile.New(reconcile.Options{
		PVE:                pve,
		Fetcher:            src,
		Store:              store,
		Budget:             plan.Budget{Prune: budget},
		Executor:           nil, // read-only
		Cluster:            e2eCluster,
		ConfiguredClusters: []string{e2eCluster},
		Log:                log,
	})
	if err != nil {
		t.Fatalf("reconcile.New(dry): %v", err)
	}
	return &harness{
		gitDir: gitDir, src: src, pve: pve, mock: m,
		store: store, rec: rec, dry: dry, log: log,
	}
}

// --- test scenarios ---

// TestE2ECreatesConverges: the full create-then-idempotent loop.
func TestE2ECreatesConverges(t *testing.T) {
	h := newHarness(t, map[string]string{
		"vm.yaml": `
apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: talos-worker-01
spec:
  node: pve01
  vmid: 100
  state: started
  memory: 8GiB
  cpu:
    type: host
    cores: 4
  disks:
    - storage: local-lvm
      size: 50GiB
      controller: virtio-scsi
  networks:
    - model: virtio
      bridge: vmbr0
`,
	}, 3)

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	// We must have planned a create followed by a start (state: started).
	var actions []string
	for _, a := range p.Actions {
		actions = append(actions, string(a.What))
	}
	containsPlan := func(kind plan.ActionKind) bool {
		for _, a := range actions {
			if a == string(kind) {
				return true
			}
		}
		return false
	}
	if !containsPlan(plan.Create) {
		t.Fatalf("expected a Create action, got %v", actions)
	}

	// PVE should have the object now.
	if !h.mock.VMExists(node, 100) {
		t.Fatalf("expected VM 100 on %s after apply cycle; mock=%+v", node, h.mock.VMConfig(node, 100))
	}
	cfg := h.mock.VMConfig(node, 100)
	if cfg == nil {
		t.Fatal("no config")
	}
	// proxops ownership tag must be present on the PVE object.
	if !containsTag(cfg["tags"], schema.PveOwnershipTag) {
		t.Errorf("managed VM missing ownership tag; tags=%q", cfg["tags"])
	}

	// The second apply cycle must be clean (idempotent convergence).
	p2 := h.apply(t)
	if p2 == nil {
		t.Fatal("second apply returned nil plan")
	}
	if len(p2.Actions) > 0 {
		t.Errorf("expected 0 actions on second cycle (idempotent), got %+v", p2.Actions)
	}
}

// TestE2EPruneTaggedOrphan: a proxops-tagged VM absent from git is deleted.
func TestE2EPruneTaggedOrphan(t *testing.T) {
	h := newHarness(t, map[string]string{}, 3)
	// Simulate a live tagged VM that is not in git.
	h.mock.PreloadVM(node, 100, map[string]string{
		"name":   "stale-vm",
		"memory": "8192",
		"tags":   "proxops",
	}, "stopped")

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	if !h.mock.VMExists(node, 100) {
		// It should have been pruned; check the plan recorded it too.
		var plannedAnyDelete bool
		for _, a := range p.Actions {
			if a.What == plan.Delete {
				plannedAnyDelete = true
			}
		}
		if !plannedAnyDelete {
			t.Errorf("expected a Delete action in plan; got %+v", p.Actions)
		}
	} else {
		// If it's still here, it was NOT pruned (budget or anomaly suppressed it).
		t.Errorf("expected VM 100 to be pruned; plan=%+v", p.Actions)
	}
}

// TestE2EPruneRespectsBudget: with a non-empty desired set the anomaly guard is
// off, so the per-cycle budget is the binding cap. 3 tagged orphans + budget=2
// → exactly 2 pruned, 1 deferred.
func TestE2EPruneRespectsBudget(t *testing.T) {
	h := newHarness(t, map[string]string{
		"vm.yaml": vmManifest("kept", 1000),
	}, 2) // budget = 2
	// 1000 matches git; 100/101/102 are tagged orphans.
	h.mock.PreloadVM(node, 1000, map[string]string{"name": "kept", "memory": "8192", "tags": "proxops"}, "stopped")
	for _, id := range []int{100, 101, 102} {
		h.mock.PreloadVM(node, id, map[string]string{
			"name": "stale", "memory": "8192", "tags": "proxops",
		}, "stopped")
	}
	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	// Budget = 2 deletions; 3 candidates → 2 applied, 1 deferred.
	deletes := 0
	for _, a := range p.Actions {
		if a.What == plan.Delete {
			deletes++
		}
	}
	if gets := deletes; gets != 2 {
		t.Errorf("expected exactly 2 Delete actions for budget=2, got %d: %+v", deletes, p.Actions)
	}
	if got := len(p.Deferred); got != 1 {
		t.Errorf("expected 1 deferred (budget-exhausted), got %d: %+v", got, p.Deferred)
	}
	// desired VM 1000 must remain.
	if !h.mock.VMExists(node, 1000) {
		t.Error("desired VM 1000 was removed — must survive")
	}
}

// TestE2EPruneUntaggedIgnored: an untagged live VM must NEVER be pruned.
// This is the plan §10 "never touch what we didn't create" guarantee.
func TestE2EPruneUntaggedIgnored(t *testing.T) {
	h := newHarness(t, map[string]string{
		"vm.yaml": vmManifest("kept", 1000),
	}, 3)
	h.mock.PreloadVM(node, 1000, map[string]string{"name": "kept", "memory": "8192", "tags": "proxops"}, "stopped")
	h.mock.PreloadVM(node, 100, map[string]string{
		"name": "manual-vm", "memory": "8192",
		// no proxops tag
	}, "stopped")

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	if !h.mock.VMExists(node, 100) {
		t.Fatalf("untagged VM 100 was pruned — ownership gate violated")
	}
	if len(p.Skipped) != 1 {
		t.Errorf("expected 1 skipped (untagged) record, got %d: %+v", len(p.Skipped), p.Skipped)
	}
}

// TestE2EAnomalyGuards: 0 desired VMs (kind empty) but MORE tagged live
// VMs than the per-cycle budget → probable-bad-push anomaly; suppress all
// prunes for that kind this cycle (none are deleted).
func TestE2EAnomalySuppressesPrune(t *testing.T) {
	h := newHarness(t, map[string]string{}, 3) // empty desired, budget = 3
	// 4 tagged orphans > budget 3 → anomaly shape.
	for _, id := range []int{100, 101, 102, 103} {
		h.mock.PreloadVM(node, id, map[string]string{
			"name": "last-vm", "memory": "4096", "tags": "proxops",
		}, "stopped")
	}

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	if p.Anomaly == "" {
		t.Errorf("expected an anomaly when 0 desired VMs but more tagged live VMs than the budget; got %+v", p)
	}
	for _, id := range []int{100, 101, 102, 103} {
		if !h.mock.VMExists(node, id) {
			t.Errorf("VM %d should NOT have been deleted under the anomaly guard", id)
		}
	}
	if len(p.Actions) != 0 {
		t.Errorf("no prunes should be applied under anomaly; got %+v", p.Actions)
	}
}

// TestE2EDiffIsDryRun: diff shows the would-be plan but mutates nothing.
func TestE2EDiffIsDryRun(t *testing.T) {
	h := newHarness(t, map[string]string{}, 3)
	h.mock.PreloadVM(node, 100, map[string]string{"name": "x", "memory": "4096", "tags": "proxops"}, "stopped")

	p := h.diff(t)
	if p == nil {
		t.Fatal("diff returned nil plan")
	}
	// Diff should plan a delete but not apply it.
	// (The diff plan is the same shape as apply.)
	if len(p.Actions) == 0 {
		t.Errorf("expected a diff plan to report an action; got %+v", p.Actions)
	}
	// PVE should be untouched.
	if !h.mock.VMExists(node, 100) {
		t.Error("dry-run diff must not mutate PVE")
	}
}

// cttManifest returns a minimal CTTemplate (vztmpl artifact) manifest —
// no PVE numeric id, no clone; just a downloadable template archive.
func cttManifest(name, filename string) string {
	return `apiVersion: proxops/v1alpha1
kind: CTTemplate
metadata:
  name: ` + name + `
spec:
  nodes: [pve01]
  storage: local
  filename: ` + filename + `
  url: https://example.com/` + filename + `
`
}

// lxcManifest returns a fully-valid minimal LXC manifest for e2e tests.
//
// PVE 9.x /lxc create REQUIRES `ostemplate`; proxops encodes this as
// spec.template = <CTTemplate name>. To keep each fixture self-contained,
// the test caller must include a ctt.yaml that defines "base-ctt".
func lxcManifest(name string, cid int) string {
	return `apiVersion: proxops/v1alpha1
kind: LXC
metadata:
  name: ` + name + `
spec:
  node: pve01
  vmid: ` + itoaManifest(cid) + `
  memory: 1GiB
  cpu:
    cores: 1
  template: base-ctt
  root:
    storage: local
    size: 4GiB
  networks:
    - bridge: vmbr0
`
}

// TestE2ELXCCreatesConverges: an LXC in git, absent on PVE → created +
// idempotent. The companion CTTemplate (downloaded once by PVE, not
// simulated here) is required for parsing.
func TestE2ELXCCreatesConverges(t *testing.T) {
	h := newHarness(t, map[string]string{
		"lxc.yaml": lxcManifest("cache-01", 9000),
		"ctt.yaml": cttManifest("base-ctt", "debian-13.tar.zst"),
	}, 3)

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	if !h.mock.VMExists(node, 9000) {
		t.Fatalf("expected LXC 9000 on %s after apply; plan=%+v", node, p.Actions)
	}
	// Second cycle must be idempotent.
	p2 := h.apply(t)
	if p2 == nil {
		t.Fatal("second apply returned nil plan")
	}
	if len(p2.Actions) > 0 {
		t.Errorf("expected 0 actions on converged LXC, got %+v", p2.Actions)
	}
}

// TestE2EDeferralWhenPrerequisiteFails: a LXC that depends on a CTTemplate
// (spec.template). The planner orders the CTTemplate download (level 0)
// before the LXC create (level 1); if the CTTemplate download FAILS in-cycle,
// the LXC create is deferred (the executor never attempts a dependant whose
// prerequisite just failed). A second cycle retries and converges.
func TestE2EDeferralWhenPrerequisiteFails(t *testing.T) {
	h := newHarness(t, map[string]string{
		"lxc.yaml": lxcManifest("cache-01", 9000),
		"ctt.yaml": cttManifest("base-ctt", "debian-13.tar.zst"),
	}, 3)

	// Force the CTTemplate download task to fail in the mock.
	h.mock.SetDownloadFail(true)

	p := h.apply(t)
	if p == nil {
		t.Fatal("apply returned nil plan")
	}
	// The CTTemplate should be the level-0 create; LXC the level-1 create.
	var cttCreate, lxcCreate bool
	for _, a := range p.Actions {
		if a.Kind == schema.KindCTTemplate && a.What == plan.Create {
			cttCreate = true
		}
		if a.Kind == schema.KindLXC && a.What == plan.Create {
			lxcCreate = true
		}
	}
	if !cttCreate || !lxcCreate {
		t.Fatalf("expected both CTTemplate and LXC creates planned, got ctt=%v lxc=%v: %+v",
			cttCreate, lxcCreate, p.Actions)
	}
	// LXC must not have been created (it was deferred because the
	// CTTemplate download failed in-cycle).
	if h.mock.VMExists(node, 9000) {
		t.Fatal("LXC 9000 should NOT be created when the CTTemplate prerequisite failed in-cycle")
	}

	// Re-enable the download; a second cycle should now both succeed.
	h.mock.SetDownloadFail(false)
	p2 := h.apply(t)
	if p2 == nil {
		t.Fatal("second apply returned nil plan")
	}
	if !h.mock.VMExists(node, 9000) {
		t.Fatalf("LXC 9000 should be created after the CTTemplate succeeded; plan=%+v", p2.Actions)
	}

	// Converged.
	p3 := h.apply(t)
	if len(p3.Actions) != 0 {
		t.Errorf("expected 0 actions on converged state, got %+v", p3.Actions)
	}
}

// vmManifest returns a fully-valid minimal VM manifest for e2e tests:
// single disk, single virtio NIC on vmbr0, pinned vmid, host CPU, 1GiB memory.
func vmManifest(name string, vmid int) string {
	return `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: ` + name + `
spec:
  node: pve01
  vmid: ` + itoaManifest(vmid) + `
  memory: 1GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      size: 4GiB
  networks:
    - model: virtio
      bridge: vmbr0
`
}

func itoaManifest(n int) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	s := ""
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(digits[n%10]) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// --- helpers ---

// containsTag checks a PVE tags value (string, potentially comma-separated)
// for a specific tag.
func containsTag(s, tag string) bool {
	if s == "" {
		return false
	}
	for _, part := range splitTags(s) {
		if part == tag {
			return true
		}
	}
	return false
}

func splitTags(s string) []string {
	// split on comma or space
	seen := []string{}
	cur := ""
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ',' || c == ' ' {
			if cur != "" {
				seen = append(seen, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		seen = append(seen, cur)
	}
	return seen
}
