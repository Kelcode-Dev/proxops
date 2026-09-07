package pveclient

import "testing"

// TestTaskStatusIsOK pins the PVE 9.2 task exit-status vocabulary. PVE exits
// tasks with "OK" or "WARNINGS: N" on success (the warning is cosmetic; e.g.
// LXC vzcreate emits a warning about hostname vs. template mismatch) and with
// "ERROR: <reason>" on failure. The old `ExitStatus == "OK"` test falsely
// flagged WARNINGS as failures.
func TestTaskStatusIsOK(t *testing.T) {
	cases := []struct {
		name    string
		status  TaskStatus
		wantOK  bool
	}{
		{"ok", TaskStatus{Status: "stopped", ExitStatus: "OK"}, true},
		{"warnings1", TaskStatus{Status: "stopped", ExitStatus: "WARNINGS: 1"}, true},
		{"warnings3", TaskStatus{Status: "stopped", ExitStatus: "WARNINGS: 3"}, true},
		{"lowercase ok", TaskStatus{Status: "stopped", ExitStatus: "ok"}, true},
		{"error", TaskStatus{Status: "stopped", ExitStatus: "ERROR: unable to create"}, false},
		{"failure reason in exitstatus", TaskStatus{Status: "stopped", ExitStatus: "unable to create VM 9100 - lvcreate error"}, false},
		{"running is not stopped", TaskStatus{Status: "running", ExitStatus: "OK"}, false},
	}
	for _, tc := range cases {
		if got := tc.status.IsOK(); got != tc.wantOK {
			t.Errorf("TaskStatus{Status:%q, ExitStatus:%q}.IsOK = %v, want %v",
				tc.status.Status, tc.status.ExitStatus, got, tc.wantOK)
		}
	}
}
