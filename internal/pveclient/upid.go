package pveclient

import (
	"strconv"
	"strings"
)

// UPID is a PVE task identifier. Real PVE UPIDs look like
//
//	pve01@25012!VM:106:1640528320:root@pam
//
// i.e. "<node>@<pid>!<task-part>". The exact task-part grammar is not a stable
// contract, so only the node is parsed out with meaning; Raw stays
// authoritative for URL construction.
type UPID struct {
	Raw  string
	Node string
	PID  int
	// After is the part after "node@pid!" (the task part).
	After string
}

// ParseUPID parses a PVE task identifier. It never fails; unrecognized input
// degrades to a Raw-only UPID.
func ParseUPID(s string) UPID {
	u := UPID{Raw: s}
	rest := s
	if i := strings.IndexByte(s, '@'); i > 0 {
		u.Node = s[:i]
		rest = s[i+1:]
	}
	if j := strings.IndexByte(rest, '!'); j >= 0 {
		if id, err := strconv.Atoi(rest[:j]); err == nil {
			u.PID = id
		}
		u.After = rest[j+1:]
	} else {
		u.After = rest
	}
	return u
}

// String returns the raw UPID.
func (u UPID) String() string { return u.Raw }
