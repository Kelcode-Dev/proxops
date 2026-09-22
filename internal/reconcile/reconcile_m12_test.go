// M12 e2e: TemplateVM → clone → VM provisioning lifecycle against the
// stateful mock PVE (whose clone handler mirrors the conformance-dev PVE
// 9.2.2 clone semantics probed 2026-09-15).
//
// Scenarios:
// 1. Full happy path: a TemplateVM and a clone-backed VM in one tree. The
//    first cycle creates the template (create+mark, level 0) and clones the
//    VM from it (level 1), applies the VM's own config, and starts it per
//    spec.state. The clone must NOT carry the template's cloud-init
//    identity: declared values win, undeclared inherited keys are cleared.
// 2. Idempotency: a second cycle plans zero actions.
// 3. Drift: out-of-band change to the clone's memory → one update action,
//    corrected next cycle. The clone is never re-cloned.
// 4. Fail closed: a VM whose spec.clone names a TemplateVM that is absent
//    from the tree aborts the cycle at parse (no PVE writes at all).
// 5. Ownership: the clone carries the proxops tag (prune-safe), and
//    deleting the manifest prunes the clone like any other VM.
package reconcile_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/Kelcode-Dev/proxops/internal/plan"
	"github.com/Kelcode-Dev/proxops/internal/schema"
)

// commitFiles writes (or removes, for empty content) files in the harness
// git work tree and commits them, so the next cycle's advisory fetch sees a
// new HEAD. Local-mode gitx reads HEAD only — the tree is mutated in place.
func commitFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	rep, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("git open: %v", err)
	}
	w, err := rep.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if content == "" {
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove %s: %v", name, err)
			}
			if _, err := w.Remove(name); err != nil {
				t.Fatalf("git rm %s: %v", name, err)
			}
			continue
		}
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
	if _, err := w.Commit("m12 update", &git.CommitOptions{
		Author: &object.Signature{Name: "proxops-test", Email: "t@example.com"},
	}); err != nil {
		t.Fatalf("git commit: %v", err)
	}
}

const m12TplDoc = `
apiVersion: proxops/v1alpha1
kind: TemplateVM
metadata:
  name: tpl-m12
spec:
  node: pve01
  vmid: 950
  state: stopped
  memory: 1GiB
  cpu:
    type: host
    cores: 1
  disks:
    - storage: local-lvm
      size: 8GiB
      interface: scsi0
  networks:
    - model: virtio
      bridge: vmbr0
  hardware:
    cloud-init:
      enabled: true
      storage: local-lvm
  cloud-init-data:
    ci-user: tpluser
    ssh-keys: ["ssh-ed25519 AAAAcloneprobe tpl"]
    nameservers: ["1.1.1.1"]
    search-domains: ["tpl.example"]
    ipconfigs:
      - nic: 0
        ip: 192.168.192.240/18
        gateway: 192.168.192.5
`

const m12VMDoc = `
apiVersion: proxops/v1alpha1
kind: VM
metadata:
  name: vm-m12
spec:
  node: pve01
  vmid: 951
  clone: tpl-m12
  state: started
  memory: 2GiB
  cpu:
    type: host
    cores: 2
  networks:
    - model: virtio
      bridge: vmbr0
  hardware:
    cloud-init:
      enabled: true
      storage: local-lvm
  cloud-init-data:
    ci-user: vmuser
    ipconfigs:
      - nic: 0
        ip: 192.168.192.250/18
        gateway: 192.168.192.5
`

func TestE2ECloneProvisionLifecycle(t *testing.T) {
	h := newHarness(t, map[string]string{
		"tpl.yaml": m12TplDoc,
		"vm.yaml":  m12VMDoc,
	}, 3)

	p1 := h.apply(t)
	if p1 == nil {
		t.Fatal("apply returned nil plan")
	}

	// The clone create must be planned at a level ABOVE the template's
	// create (dependency ordering: the source must exist first).
	var tplCreate, vmCreate *plan.Action
	for i := range p1.Actions {
		a := &p1.Actions[i]
		if a.What == plan.Create && a.Kind == schema.KindTemplateVM {
			tplCreate = a
		}
		if a.What == plan.Create && a.Kind == schema.KindVM {
			vmCreate = a
		}
	}
	if tplCreate == nil || vmCreate == nil {
		t.Fatalf("expected creates for both TemplateVM and clone VM; got %+v", p1.Actions)
	}
	if vmCreate.CloneSourceID != 950 {
		t.Errorf("clone action CloneSourceID = %d, want 950 (the template's pinned vmid)", vmCreate.CloneSourceID)
	}
	if vmCreate.Level <= tplCreate.Level {
		t.Errorf("clone level %d must exceed template level %d", vmCreate.Level, tplCreate.Level)
	}

	// PVE-side result: the clone exists as a normal (non-template) qemu
	// object with the VM's OWN identity, not the template's.
	if !h.mock.VMExists(node, 951) {
		t.Fatal("clone VM 951 absent after apply")
	}
	if h.mock.VMIsTemplate(node, 951) {
		t.Error("clone VM 951 must NOT carry the template flag")
	}
	cfg := h.mock.VMConfig(node, 951)
	if cfg["template"] == "1" {
		t.Error("clone config carries template=1")
	}
	// Declared values win.
	if cfg["ciuser"] != "vmuser" {
		t.Errorf("clone ciuser = %q, want vmuser (declared value must overwrite the template's)", cfg["ciuser"])
	}
	if cfg["ipconfig0"] != "ip=192.168.192.250/18,gw=192.168.192.5" {
		t.Errorf("clone ipconfig0 = %q, want the VM's own static IP", cfg["ipconfig0"])
	}
	if cfg["memory"] != "2048" {
		t.Errorf("clone memory = %q, want 2048", cfg["memory"])
	}
	// Undeclared inherited identity keys are CLEARED, not leaked.
	for _, k := range []string{"sshkeys", "nameserver", "searchdomain"} {
		if v, ok := cfg[k]; ok && v != "" {
			t.Errorf("clone %s = %q, want cleared (inherited from template, not declared by the VM)", k, v)
		}
	}
	// Ownership tag present (prune-safe).
	if !containsTagStr(cfg["tags"], schema.PveOwnershipTag) {
		t.Errorf("clone missing proxops ownership tag; tags=%q", cfg["tags"])
	}
	// Power honoured: spec.state started → running. PVE's clone endpoint
	// rejects start=1 (probe-verified), so the executor clones stopped and
	// the planner's normal power step starts the VM on the NEXT cycle —
	// same convergence guarantee, one cycle later than a plain create.
	h.apply(t)
	if st, _ := h.mock.VMStatus(node, 951); st != "running" {
		t.Errorf("clone power = %q, want running", st)
	}
	// The template itself stays stopped + flagged.
	if !h.mock.VMIsTemplate(node, 950) {
		t.Error("template 950 lost its flag")
	}
	if st, _ := h.mock.VMStatus(node, 950); st != "stopped" {
		t.Errorf("template power = %q, want stopped", st)
	}

	// Second cycle: fully converged, zero actions.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Fatalf("idempotent cycle: %d actions, want 0: %+v", len(p2.Actions), p2.Actions)
	}
}

func TestE2ECloneDriftCorrectsWithoutReclone(t *testing.T) {
	h := newHarness(t, map[string]string{
		"tpl.yaml": m12TplDoc,
		"vm.yaml":  m12VMDoc,
	}, 3)
	h.apply(t) // converge

	// Out-of-band memory change on the clone.
	h.mock.SetVMConfigField(node, 951, "memory", "1024")

	p := h.apply(t)
	var upd *plan.Action
	for i := range p.Actions {
		a := &p.Actions[i]
		if a.Kind == schema.KindVM && a.ID == 951 && a.What == plan.Update {
			upd = a
		}
	}
	if upd == nil {
		t.Fatalf("expected a config Update on the drifted clone; got %+v", p.Actions)
	}
	for _, a := range p.Actions {
		if a.What == plan.Create && a.ID == 951 {
			t.Fatal("drift must NEVER re-clone an existing VM")
		}
	}
	if h.mock.VMConfig(node, 951)["memory"] != "2048" {
		t.Errorf("clone memory = %q after correction, want 2048", h.mock.VMConfig(node, 951)["memory"])
	}
	// Converged again.
	p2 := h.apply(t)
	if len(p2.Actions) != 0 {
		t.Fatalf("post-drift cycle: %d actions, want 0", len(p2.Actions))
	}
}

func TestE2ECloneMissingTemplateFailsClosed(t *testing.T) {
	// A clone-backed VM whose TemplateVM is NOT in the composition must
	// abort the cycle at parse — before any PVE call. Zero writes.
	h := newHarness(t, map[string]string{
		"vm.yaml": m12VMDoc,
	}, 3)
	res, p, err := h.rec.RunOneCycle(context.Background())
	if err != nil {
		t.Fatalf("cycle returned fatal error: %v", err)
	}
	if !res.Aborted {
		t.Fatalf("cycle must abort on an unresolvable clone ref; plan=%v", p)
	}
	if p != nil {
		t.Errorf("aborted cycle must not produce a plan; got %d actions", len(p.Actions))
	}
	if got := h.mock.WritesObserved(); got != 0 {
		t.Errorf("failed-closed cycle performed %d PVE writes, want 0", got)
	}
}

func TestE2EClonePruneRespectsOwnership(t *testing.T) {
	h := newHarness(t, map[string]string{
		"tpl.yaml": m12TplDoc,
		"vm.yaml":  m12VMDoc,
	}, 3)
	h.apply(t)

	// Remove the VM manifest (and drop it from the composition) → the
	// proxops-tagged clone becomes a prune candidate; the template stays
	// desired and must never be pruned. The harness relocates top-level
	// fixtures to <kind>/<cluster>/<name>.yaml.
	commitFiles(t, h.gitDir, map[string]string{
		"vm/default/vm-m12.yaml":            "",
		"clusters/default/resources.yaml":   "resources:\n  - ../../templatevm/default/tpl-m12.yaml\n",
	})
	p := h.apply(t)
	var delVM, delTpl bool
	for _, a := range p.Actions {
		if a.What == plan.Delete && a.ID == 951 {
			delVM = true
		}
		if a.What == plan.Delete && a.ID == 950 {
			delTpl = true
		}
	}
	if !delVM {
		t.Fatalf("expected prune of the removed clone 951; actions=%+v", p.Actions)
	}
	if delTpl {
		t.Error("template 950 is still desired and must never be pruned")
	}
	if h.mock.VMExists(node, 951) {
		t.Error("clone 951 still present after prune")
	}
	if !h.mock.VMExists(node, 950) {
		t.Error("template 950 vanished")
	}
}
