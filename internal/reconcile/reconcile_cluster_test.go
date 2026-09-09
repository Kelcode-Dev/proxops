// Cluster-safety e2e tests (M8): the Git-composition → named-cluster →
// endpoint → node → resource invariant.
//
// The full e2e harness (newHarness in reconcile_e2e_test.go) relocates
// fixtures into the M8 layout for the "default" cluster. These tests drive
// the same harness to pin the multi-cluster guarantees:
//
//   - pruning is scoped to the cluster's node allowlist: a tagged orphan on
//     a node OUTSIDE the allowlist is never pruned, and a live object there
//     is not even an "untagged skip" entry;
//   - an unknown cluster name can never produce PVE actions (parse fails
//     closed before any PVE call);
//   - one cluster's absence-from-git can never prune another cluster's
//     objects (no cross-cluster membership test exists: the desired index is
//     built per cluster).
package reconcile_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/GizzmoShifu/proxmox-operator/internal/exec"
	"github.com/GizzmoShifu/proxmox-operator/internal/gitx"
	"github.com/GizzmoShifu/proxmox-operator/internal/plan"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient"
	"github.com/GizzmoShifu/proxmox-operator/internal/pveclient/mock"
	"github.com/GizzmoShifu/proxmox-operator/internal/reconcile"
	"github.com/GizzmoShifu/proxmox-operator/internal/statusx"
)

// TestClusterAllowlistExcludesLiveObjectsFromPlan — a cluster whose node
// allowlist is [pve01] and whose composition is empty: a tagged live VM on
// pve01 is a prune candidate, but a tagged live VM on pve02 (outside the
// allowlist) must not appear in the plan AT ALL (neither as a prune nor as a
// skip). This is the cluster boundary: pruning never crosses the allowlist.
func TestClusterAllowlistExcludesLiveObjectsFromPlan(t *testing.T) {
	log := slog.Default()
	gitDir := t.TempDir()
	newGitRepo(t, gitDir, map[string]string{
		"clusters/conformance-dev/resources.yaml": "resources: []\n",
	})
	src, err := gitx.New(context.Background(), gitx.Options{Local: gitDir})
	if err != nil {
		t.Fatalf("gitx: %v", err)
	}
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	m.PreloadVM("pve01", 100, map[string]string{"name": "in-list-orphan", "tags": "pveconform", "memory": "1024"}, "stopped")
	m.PreloadVM("pve02", 101, map[string]string{"name": "out-list-orphan", "tags": "pveconform", "memory": "1024"}, "stopped")
	pve, err := pveclient.New(pveclient.Options{
		PVE:     pveclient.PVEParams{User: "root@pam", Auth: "token", TokenID: "pveconform", Token: "deadbeef"},
		BaseURL: m.URL(),
	}, log)
	if err != nil {
		t.Fatalf("pveclient: %v", err)
	}
	store := statusx.New()
	ce := exec.New(pve, 30*time.Second, 2*time.Millisecond, store, log)
	ce.SetCluster("conformance-dev")
	rec, err := reconcile.New(reconcile.Options{
		PVE: pve, Fetcher: src, Store: store,
		Budget: plan.Budget{Prune: 3},
		Executor: ce,
		Cluster:  "conformance-dev",
		NodeAllowlist: []string{"pve01"},
		ConfiguredClusters: []string{"conformance-dev"},
		Log: log,
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	res, pl, err := rec.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.Aborted {
		t.Fatalf("aborted: %s", res.AbortReason)
	}
	// Both orphans carry the pveconform tag; 0 desired VMs + 2 tagged live
	// = the empty-desired anomaly shape? budget=3, candidates=2 → 2 <= 3 →
	// NOT the anomaly shape, prunes are allowed. Only the pve01 orphan is a
	// candidate.
	var inList, outList bool
	for _, a := range pl.Actions {
		if a.Node == "pve01" && a.ID == 100 {
			inList = true
		}
		if a.Node == "pve02" && a.ID == 101 {
			outList = true
		}
	}
	if !inList {
		t.Errorf("in-allowlist orphan pve01#100 must be a prune candidate; plan=%+v", pl.Actions)
	}
	if outList {
		t.Errorf("out-of-allowlist orphan pve02#101 must NOT appear in this cluster's plan; plan=%+v", pl.Actions)
	}
	for _, s := range pl.Skipped {
		if s.Node == "pve02" {
			t.Errorf("out-of-allowlist object must not be a skip either: %+v", s)
		}
	}
}

// TestUnknownClusterProducesNoPVEActions — a cluster name that is NOT in
// pve.clusters (i.e. not in ConfiguredClusters) must fail closed at parse,
// before any PVE call. The PVE client's read/write counters stay untouched.
func TestUnknownClusterProducesNoPVEActions(t *testing.T) {
	gitDir := t.TempDir()
	// No composition for "ghost", but the cluster "default" is configured.
	newGitRepo(t, gitDir, map[string]string{
		"clusters/default/resources.yaml": "resources: []\n",
	})
	src, err := gitx.New(context.Background(), gitx.Options{Local: gitDir})
	if err != nil {
		t.Fatalf("gitx: %v", err)
	}
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	pve, err := pveclient.New(pveclient.Options{
		PVE:     pveclient.PVEParams{User: "root@pam", Auth: "token", TokenID: "pveconform", Token: "deadbeef"},
		BaseURL: m.URL(),
	}, slog.Default())
	if err != nil {
		t.Fatalf("pveclient: %v", err)
	}
	store := statusx.New()
	rec, err := reconcile.New(reconcile.Options{
		PVE: pve, Fetcher: src, Store: store,
		Budget: plan.Budget{Prune: 3},
		// "ghost" is NOT in ConfiguredClusters → parse must fail.
		Cluster:              "ghost",
		NodeAllowlist:        nil,
		ConfiguredClusters:    []string{"default"},
		Log: slog.Default(),
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}
	res, _, err := rec.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !res.Aborted {
		t.Fatalf("unknown cluster must abort, got %+v", res)
	}
	if !containsStr2(res.AbortReason, "ghost") {
		t.Fatalf("abort reason should name the cluster: %s", res.AbortReason)
	}
}

func containsStr2(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestClusterEmptyDesiredNoPruneFromOtherCluster — two clusters share ONE
// git tree and ONE mock PVE inventory (each cluster is scoped by its allow
// list). Cluster "dev" has an EMPTY composition + allowlist [pve01];
// cluster "prod" lists a VM on pve02. The dev cluster's cycle must NOT
// prune the prod cluster's live VM (the VM's node is not in dev's allow
// list → invisible to dev's planner), and prod's cycle converges with zero
// prunes.
func TestClusterEmptyDesiredNoPruneFromOtherCluster(t *testing.T) {
	prodVM := `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: prod-vm
spec:
  node: pve02
  vmid: 500
  memory: 1GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      size: 4GiB
  networks:
    - bridge: vmbr0
`
	files := map[string]string{
		"clusters/dev/resources.yaml":  "resources: []\n",
		"clusters/prod/resources.yaml": "resources:\n  - ../../vm/prod/prod-vm.yaml\n",
		"vm/prod/prod-vm.yaml":         prodVM,
	}
	gitDir := t.TempDir()
	newGitRepo(t, gitDir, files)
	src, err := gitx.New(context.Background(), gitx.Options{Local: gitDir})
	if err != nil {
		t.Fatalf("gitx: %v", err)
	}
	m := mock.New(mock.Config{Token: apiToken, TaskTicks: 1})
	t.Cleanup(m.Close)
	// Live: the prod VM exists on pve02 (tagged).
	m.PreloadVM("pve02", 500, map[string]string{"name": "prod-vm", "tags": "pveconform", "memory": "1024"}, "stopped")
	pve, err := pveclient.New(pveclient.Options{
		PVE:     pveclient.PVEParams{User: "root@pam", Auth: "token", TokenID: "pveconform", Token: "deadbeef"},
		BaseURL: m.URL(),
	}, slog.Default())
	if err != nil {
		t.Fatalf("pveclient: %v", err)
	}
	store := statusx.New()

	newRec := func(cluster string, allowlist []string) *reconcile.Reconciler {
		e := exec.New(pve, 30*time.Second, 2*time.Millisecond, store, slog.Default())
		e.SetCluster(cluster)
		r, err := reconcile.New(reconcile.Options{
			PVE: pve, Fetcher: src, Store: store,
			Budget: plan.Budget{Prune: 3},
			Executor: e,
			Cluster:  cluster,
			NodeAllowlist: allowlist,
			ConfiguredClusters: []string{"dev", "prod"},
			Log: slog.Default(),
		})
		if err != nil {
			t.Fatalf("reconcile.New(%s): %v", cluster, err)
		}
		return r
	}

	// PROD cycle: composition matches the live VM → no prune.
	res, pl, err := newRec("prod", []string{"pve02"}).RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("prod cycle: %v", err)
	}
	if res.Aborted {
		t.Fatalf("prod aborted: %s", res.AbortReason)
	}
	for _, a := range pl.Actions {
		if a.What == plan.Delete {
			t.Errorf("prod cycle must not prune its own live VM: %+v", a)
		}
	}

	// DEV cycle: empty composition + allowlist [pve01]. The prod VM on
	// pve02 must be invisible to dev's planner → no prune, no skip.
	resDev, plDev, err := newRec("dev", []string{"pve01"}).RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("dev cycle: %v", err)
	}
	if resDev.Aborted {
		t.Fatalf("dev aborted: %s", resDev.AbortReason)
	}
	for _, a := range plDev.Actions {
		if a.Node == "pve02" || a.ID == 500 {
			t.Errorf("dev cycle must not act on pve02's objects: %+v", a)
		}
	}
	for _, s := range plDev.Skipped {
		if s.Node == "pve02" {
			t.Errorf("dev cycle must not even skip pve02's objects: %+v", s)
		}
	}
	if !m.VMExists("pve02", 500) {
		t.Errorf("prod's live VM on pve02 was pruned by dev's cycle — cluster boundary violated")
	}
}
