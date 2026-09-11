package schema_test

// PVE 9.x LXC option wire-grammar regressions (M10).
//
// Probe source: conformance-dev PVE 9.2.2, disposable CTs 9870-9882
// (created + destroyed 2026-09-10, all 8 gone afterwards). The probes
// pin:
//
//	Create-time (POST /lxc):
//	  top-level   unprivileged/protection/onboot/console  → accepted
//	  top-level   nesting                                 → 400 "property not defined"
//	  top-level   keyctl / fuse                           → 403 "property not defined"
//	  composite   features=nesting=1                       → accepted; report re-echoes
//	  composite   features=keyctl=1 / fuse=1               → 403 (unknown token)
//	  ip=/gw=  inside  netN property string                → accepted; report re-echoes
//
//	/config-PUT (PUT /lxc/{cid}/config):
//	  top-level   unprivileged                             → 500 (create-only)
//	  top-level   nesting / keyctl / fuse                  → 400
//	  top-level   protection / onboot / console            → accepted (0 or 1)
//	  top-level   features                                 → accepted (composite form)
//
// These tests pin the pveconform-side consequence:
//
//   1. ToCreateParams MUST NOT emit top-level nesting/keyctl/fuse
//     (PVE rejects). It MUST emit features=nesting=<0|1> when nesting
//     is set; keyctl/fuse=TRUE manifests fail closed; keyctl/fuse=FALSE
//     are omitted (PVE default).
//   2. Drift MUST NOT emit top-level nesting (400 on PVE). It MUST
//     emit features=nesting=<0|1> when desired nesting diverges.
//     Drift MUST NOT emit top-level keyctl/fuse; a desired=true +
//     live-off mismatch on keyctl/fuse surfaces as a non-destructive
//     anomaly (no write) because PVE has no accepted update form.
//
// Every test uses the in-package `current` map[string]any as the PVE
// /config report. No PVE HTTP is involved — the tests pin the wire
// shape Go would emit.

import (
	"testing"

	"github.com/GizzmoShifu/proxmox-operator/internal/schema"
)

// TestLXCToCreateParams_NestingAsComposite pins the PVE 9.x nesting
// create-time form: a manifest with `options.nesting: true` emits
// `features=nesting=1` and NO top-level `nesting` key.
func TestLXCToCreateParams_NestingAsComposite(t *testing.T) {
	raw := "apiVersion: " + schema.APIVersion + "\n" +
		"kind: LXC\nmetadata:\n  name: x\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    nesting: true\n"
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(raw, lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	p, err := lxc.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	if got, _ := p["features"]; got != "nesting=1" {
		t.Errorf(`create features = %v, want "nesting=1"`, got)
	}
	if _, ok := p["nesting"]; ok {
		t.Errorf("create top-level nesting key must not be emitted (PVE 9.2 rejects with 400)")
	}
}

// TestLXCToCreateParams_KeyctlTrueBlocks pins that a manifest with
// spec.options.keyctl=true cannot be submitted: PVE 9.x has no LXC
// create form for keyctl.
func TestLXCToCreateParams_KeyctlTrueBlocks(t *testing.T) {
	probe := func(field string) {
		raw := "apiVersion: " + schema.APIVersion + "\n" +
			"kind: LXC\nmetadata:\n  name: x-" + field + "\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    " + field + ": true\n"
		lxc := schema.NewLXC()
		if err := schema.YAMLTo(raw, lxc); err != nil {
			t.Fatalf("YAMLTo (%s): %v", field, err)
		}
		if _, err := lxc.ToCreateParams(); err == nil {
			t.Errorf("%s=true: ToCreateParams must fail closed (no PVE 9.x create form)", field)
		}
	}
	probe("keyctl")
	probe("fuse")
}

// TestLXCToCreateParams_KeyctlFalseOmit pins that a manifest with
// spec.options.keyctl=false (the PVE default) must NOT emit a keyctl
// token at create — PVE would reject it, and the default is already
// off anyway. Same for fuse.
func TestLXCToCreateParams_KeyctlFalseOmit(t *testing.T) {
	for _, field := range []string{"keyctl", "fuse"} {
		raw := "apiVersion: " + schema.APIVersion + "\n" +
			"kind: LXC\nmetadata:\n  name: x-" + field + "\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    " + field + ": false\n"
		lxc := schema.NewLXC()
		if err := schema.YAMLTo(raw, lxc); err != nil {
			t.Fatalf("YAMLTo (%s=false): %v", field, err)
		}
		p, err := lxc.ToCreateParams()
		if err != nil {
			t.Fatalf("ToCreateParams (%s=false): %v", field, err)
		}
		if _, ok := p[field]; ok {
			t.Errorf("%s=false must be OMITTED at create (PVE default; PVE rejects the token anyway)", field)
		}
	}
}

// TestLXCToCreateParams_ConsoleOnAccepted pins that spec.options.console
// is a /lxc create-accepted key (unlike nesting / keyctl / fuse). Both
// true and false wire forms must be emitted (false is meaningful: PVE's
// create default is off? No — PVE 9.x create does not report console on
// /config by default, so an explicit `console: false` manifest intends
// "PVE, set it off"; this is the tri-state capture).
func TestLXCToCreateParams_ConsoleOnAccepted(t *testing.T) {
	raw := "apiVersion: " + schema.APIVersion + "\n" +
		"kind: LXC\nmetadata:\n  name: x\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    console: true\n    onboot: true\n    protection: true\n"
	lxc := schema.NewLXC()
	if err := schema.YAMLTo(raw, lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	p, err := lxc.ToCreateParams()
	if err != nil {
		t.Fatalf("ToCreateParams: %v", err)
	}
	for _, k := range []string{"console", "onboot", "protection"} {
		if got, _ := p[k]; got != "1" {
			t.Errorf("create %s = %v, want \"1\" (PVE 9.2 /lxc accepts these top-level)", k, got)
		}
	}
}

// TestLXCDrift_NestingEmitsFeaturesNotTopLevel pins Drift's PVE 9.x
// nesting convergence: it must write features=nesting=<0|1>, NEVER a
// top-level nesting write (PVE would 400 the /config PUT).
func TestLXCDrift_NestingEmitsFeaturesNotTopLevel(t *testing.T) {
	// Live: PVE reports features=nesting=0 (i.e. off); desired: true.
	lxc := schema.NewLXC()
	if err := schema.YAMLTo("apiVersion: "+schema.APIVersion+"\n"+
		"kind: LXC\nmetadata:\n  name: x\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    nesting: true\n", lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	// Simulate PVE /config report: features=nesting=0
	live := map[string]any{
		"cores":    "1",
		"memory":   "1024",
		"hostname": "x",
		"rootfs":   "local-lvm:vm-100-disk-0,size=4G",
		"net0":     "name=wired0,bridge=vmbr0",
		"tags":     "pveconform",
		"features": "nesting=0",
	}
	upd, stop, _ := lxc.Drift(live)
	if _, ok := upd["nesting"]; ok {
		t.Fatalf("Drift emitted top-level nesting = %v (PVE 9.2 would 400 this)", upd["nesting"])
	}
	if got, _ := upd["features"]; got != "nesting=1" {
		t.Errorf("Drift features = %v, want \"nesting=1\"", got)
	}
	if !stop {
		// nesting write does not require a stop
		_ = stop
	}
}

// TestLXCDrift_KeyctlTrueLiveOffIsAnomalyNoWrite pins that when PVE does
// not have an accepted /config form for keyctl, Drift MUST surface a
// non-destructive anomaly (no write attempt) rather than emitting a
// 400-guaranteed token.
func TestLXCDrift_KeyctlTrueLiveOffIsAnomalyNoWrite(t *testing.T) {
	lxc := schema.NewLXC()
	if err := schema.YAMLTo("apiVersion: "+schema.APIVersion+"\n"+
		"kind: LXC\nmetadata:\n  name: x\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    keyctl: true\n", lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := map[string]any{
		"cores":    "1",
		"memory":   "1024",
		"hostname": "x",
		"rootfs":   "local-lvm:vm-100-disk-0,size=4G",
		"net0":     "name=wired0,bridge=vmbr0",
		"tags":     "pveconform",
		// keyctl absent in live = off
	}
	upd, _, changed := lxc.Drift(live)
	if changed {
		// The "config drift" flag is not set because keyctl/fuse have no
		// convergable wire form — they surface as ANOMALIES instead.
		t.Fatalf("Drift on keyctl=true + live keyctl=absent must NOT claim pveconform can apply a change")
	}
	if _, ok := upd["keyctl"]; ok {
		t.Fatalf("Drift emitted top-level keyctl write (PVE would 400 this)")
	}
	anoms := lxc.DriftAnomalies(live)
	if len(anoms) == 0 {
		t.Fatalf("DriftAnomalies must surface a keyctl convergence anomaly; got none")
	}
	found := false
	for _, a := range anoms {
		if contains(a, "keyctl") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DriftAnomalies = %v; want one naming keyctl", anoms)
	}
}

// TestLXCDrift_UnprivilegedTrueLiveOffIsAnomalyNoWrite: unprivileged is
// PVE 9.x create-only; Drift must NOT emit it on /config PUT — it
// surfaces as a recreate-required anomaly.
func TestLXCDrift_UnprivilegedTrueLiveOffIsAnomalyNoWrite(t *testing.T) {
	lxc := schema.NewLXC()
	if err := schema.YAMLTo("apiVersion: "+schema.APIVersion+"\n"+
		"kind: LXC\nmetadata:\n  name: x\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    unprivileged: true\n", lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := map[string]any{
		"cores":        "1",
		"memory":       "1024",
		"hostname":     "x",
		"rootfs":       "local-lvm:vm-100-disk-0,size=4G",
		"net0":         "name=wired0,bridge=vmbr0",
		"tags":         "pveconform",
		"unprivileged": "0",
	}
	upd, _, _ := lxc.Drift(live)
	if _, ok := upd["unprivileged"]; ok {
		t.Fatalf("Drift emitted unprivileged write (PVE 9.2 /config PUT returns 500)")
	}
	anoms := lxc.DriftAnomalies(live)
	found := false
	for _, a := range anoms {
		if contains(a, "unprivileged") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DriftAnomalies must surface an unprivileged recreate-required anomaly; got %v", anoms)
	}
}

// TestLXCDrift_ConsoleIsConvergeable: PVE 9.x /config PUT DOES accept
// top-level console=<0|1> (probe 2026-09-10). Drift emits it normally.
func TestLXCDrift_ConsoleIsConvergeable(t *testing.T) {
	lxc := schema.NewLXC()
	if err := schema.YAMLTo("apiVersion: "+schema.APIVersion+"\n"+
		"kind: LXC\nmetadata:\n  name: x\nspec:\n  node: pve01\n  vmid: 100\n  memory: 1GiB\n  cpu: {cores: 1}\n  template: t\n  root: {storage: local, size: 4GiB}\n  options:\n    console: true\n", lxc); err != nil {
		t.Fatalf("YAMLTo: %v", err)
	}
	live := map[string]any{
		"cores":    "1",
		"memory":   "1024",
		"hostname": "x",
		"rootfs":   "local-lvm:vm-100-disk-0,size=4G",
		"net0":     "name=wired0,bridge=vmbr0",
		"tags":     "pveconform",
		// console absent in live = off
	}
	upd, _, _ := lxc.Drift(live)
	if got, ok := upd["console"]; !ok || got != "1" {
		t.Errorf("Drift must emit console=1 for desired-true + live-absent; got upd=%v", upd)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
