package schema

import (
	"testing"
)

// M11+ (cloud-init E2E validation on conformance-dev, 2026-09-13): the PVE 9.2
// sshkeys wire grammar and the cloud-init drive size-token tolerance. Both
// were real bugs found by the end-to-end validation; see docs/GAPS.md.

// PVE 9.2 sshkeys wire grammar (probed 2026-09-13): the field VALUE must be
// percent-encoded, keys newline-joined; PVE reports the encoded form verbatim.
// Drift must compare DECODED key sets so a re-encode never reads as drift.
func TestCloudInitSSHKeys_WireGrammar(t *testing.T) {
	key1 := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA TESTKEY1 a@b"
	key2 := "ssh-rsa AAAAB3NzaC1yc2E TESTKEY2 c@d"
	got := cloudInitSSHKeysWire([]string{key1, key2})
	want := "ssh-ed25519%20AAAAC3NzaC1lZDI1NTE5AAAA%20TESTKEY1%20a%40b%0Assh-rsa%20AAAAB3NzaC1yc2E%20TESTKEY2%20c%40d"
	if got != want {
		t.Errorf("wire = %q\nwant %q", got, want)
	}
	// The encoded form round-trips through the set comparison.
	if !sshKeysWireMatch(got, cloudInitSSHKeysWire([]string{key2, key1})) {
		t.Error("order-insensitive match failed")
	}
	if sshKeysWireMatch(got, cloudInitSSHKeysWire([]string{key1})) {
		t.Error("subset must not match (missing key)")
	}
	// A raw (unencoded) live value with the same keys matches too.
	if !sshKeysWireMatch(key1+"\n"+key2, got) {
		t.Error("raw-vs-encoded match failed")
	}
}

func TestCloudInitSSHKeys_DriftNoFlap(t *testing.T) {
	vm := baseCloudInitVM("vm-sshkeys")
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAA TESTKEY a@b"
	vm.Spec.CloudInitData.SSHKeys = []string{key}
	live := baseLiveVM("9100", "vm-sshkeys")
	// PVE reports the percent-encoded form verbatim.
	live["sshkeys"] = cloudInitSSHKeysWire([]string{key})
	if _, _, changed := vm.Drift(live); changed {
		t.Error("changed=true on converged sshkeys, want no flap")
	}
	// A different live key set IS drift.
	live["sshkeys"] = cloudInitSSHKeysWire([]string{"ssh-rsa AAAA OTHER z@z"})
	upd, _, changed := vm.Drift(live)
	if !changed {
		t.Fatal("changed=false on different sshkeys, want drift")
	}
	if upd["sshkeys"] != cloudInitSSHKeysWire([]string{key}) {
		t.Errorf("upd[sshkeys] = %v", upd["sshkeys"])
	}
}

// The cloud-init drive report may omit the size token when PVE created the
// drive without one (probed: ide2=local-lvm:cloudinit reports
// "local-lvm:vm-N-cloudinit,media=cdrom" with NO size). That is compatible —
// rewriting the drive to "fix" a size PVE never reported is a pointless
// stop/start cycle.
func TestCloudInitDrive_SizeTokenTolerance(t *testing.T) {
	if !cloudInitMatches("local-lvm:vm-9100-cloudinit,media=cdrom", "local-lvm:cloudinit,size=4M") {
		t.Error("live without size token must be compatible")
	}
	if !cloudInitMatches("local-lvm:vm-9100-cloudinit,media=cdrom,size=4M", "local-lvm:cloudinit,size=4M") {
		t.Error("same size must match")
	}
	if cloudInitMatches("local-lvm:vm-9100-cloudinit,media=cdrom,size=8M", "local-lvm:cloudinit,size=4M") {
		t.Error("different size must drift")
	}
	if cloudInitMatches("other:vm-9100-cloudinit,media=cdrom", "local-lvm:cloudinit,size=4M") {
		t.Error("different pool must drift")
	}
}
