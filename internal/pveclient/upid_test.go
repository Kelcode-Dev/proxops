package pveclient

import "testing"

func TestParseUPID(t *testing.T) {
	cases := []struct {
		in    string
		node  string
		after string
	}{
		{"pve01@25012!VM:142:1640528320:root@pam", "pve01", "VM:142:1640528320:root@pam"},
		{"pve@mock!task-5", "pve", "task-5"},
		{"not-a-upid", "", "not-a-upid"},
		{"", "", ""},
	}
	for _, tc := range cases {
		upid := ParseUPID(tc.in)
		if upid.Raw != tc.in {
			t.Errorf("ParseUPID(%q).Raw = %q, want %q", tc.in, upid.Raw, tc.in)
		}
		if upid.Node != tc.node {
			t.Errorf("ParseUPID(%q).Node = %q, want %q", tc.in, upid.Node, tc.node)
		}
		if upid.After != tc.after {
			t.Errorf("ParseUPID(%q).After = %q, want %q", tc.in, upid.After, tc.after)
		}
		if upid.String() != tc.in {
			t.Errorf("ParseUPID(%q).String() = %q", tc.in, upid.String())
		}
	}

	// PID must be parsed for the realistic UPID.
	u := ParseUPID("pve01@25012!VM:142")
	if u.PID != 25012 {
		t.Errorf("PID = %d, want 25012", u.PID)
	}
}
