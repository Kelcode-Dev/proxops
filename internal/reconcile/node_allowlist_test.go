// Regression tests for pve.nodes allowlist enforcement.
//
// The single-endpoint PVE connection model (base-url + node-in-path) means
// an out-of-allowlist spec.node no longer fails with "node not a DNS
// host" — it would silently route to the base-url host and hit a 404 on
// the PVE side. To keep the operator's typo visible, the reconciler
// rejects manifests whose spec.node is not in NodeAllowlist BEFORE any PVE
// request (fail-closed).
package reconcile_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/Kelcode-Dev/proxops/internal/exec"
	"github.com/Kelcode-Dev/proxops/internal/gitx"
	"github.com/Kelcode-Dev/proxops/internal/plan"
	"github.com/Kelcode-Dev/proxops/internal/pveclient"
	"github.com/Kelcode-Dev/proxops/internal/pveclient/mock"
	"github.com/Kelcode-Dev/proxops/internal/reconcile"
	"github.com/Kelcode-Dev/proxops/internal/statusx"
)

// newAllowlistHarness wires a Reconciler with a NodeAllowlist and a git
// tree of one VM whose spec.node either matches or does not.
func newAllowlistHarness(t *testing.T, manifest string, allowlist []string) *reconcile.Reconciler {
	t.Helper()
	gitDir := t.TempDir()
	newGitRepo(t, gitDir, map[string]string{"vm.yaml": manifest})
	src, err := gitx.New(context.Background(), gitx.Options{Local: gitDir})
	if err != nil {
		t.Fatalf("gitx local: %v", err)
	}

	// The mock compares the PVEAPIToken header value (user!id=uuid).
	m := mock.New(mock.Config{Token: "root@pam!proxops=tok"})
	t.Cleanup(m.Close)
	log := slog.Default()
	pve, err := pveclient.New(pveclient.Options{
		PVE: pveclient.PVEParams{
			User:    "root@pam",
			Auth:    "token",
			TokenID: "proxops",
			Token:   "tok",
		},
		BaseURL:     m.URL(),
		HTTPTimeout: 5 * time.Second,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	store := statusx.New()
	executor := exec.New(pve, 5*time.Second, 2*time.Millisecond, store, log)
	executor.SetCluster(e2eCluster)
	// Apply-mode so the executor runs; we only care about the abort
	// reason, which happens before any action.
	r, err := reconcile.New(reconcile.Options{
		PVE:                pve,
		Fetcher:            src,
		Store:              store,
		Budget:             plan.Budget{Prune: 3},
		Executor:           executor,
		Cluster:            e2eCluster,
		NodeAllowlist:      allowlist,
		ConfiguredClusters: []string{e2eCluster},
		Log:                log,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAllowlistRejectsUnknownNodeBeforePVE(t *testing.T) {
	manifest := `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: bad-node
spec:
  node: not-in-list
  vmid: 500
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
	r := newAllowlistHarness(t, manifest, []string{"pve01"})
	res, p, err := r.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("cycle error: %v", err)
	}
	if p != nil {
		t.Fatalf("expected abort (no plan), got %+v", p.Actions)
	}
	if !res.Aborted {
		t.Fatalf("expected Aborted=true")
	}
	if !containsStr(res.AbortReason, "not-in-list") || !containsStr(res.AbortReason, "pve.clusters.") {
		t.Fatalf("AbortReason = %q; want it to name the unknown node and pve.clusters.<name>.nodes", res.AbortReason)
	}
}

func TestAllowlistAcceptsKnownNode(t *testing.T) {
	manifest := `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: good-node
spec:
  node: pve01
  vmid: 500
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
	r := newAllowlistHarness(t, manifest, []string{"pve01"})
	res, p, err := r.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.Aborted {
		t.Fatalf("should not abort: %s", res.AbortReason)
	}
	if p == nil || len(p.Actions) == 0 {
		t.Fatalf("expected a create action, got %+v", p)
	}
}

func TestAllowlistEmptyAcceptsAnyNode(t *testing.T) {
	manifest := `apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: no-allowlist
spec:
  node: anywhere
  vmid: 600
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
	r := newAllowlistHarness(t, manifest, nil) // no allowlist
	res, p, err := r.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if res.Aborted {
		t.Fatalf("empty allowlist should not abort: %s", res.AbortReason)
	}
	if p == nil || len(p.Actions) == 0 {
		t.Fatalf("expected a create, got %+v", p)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
