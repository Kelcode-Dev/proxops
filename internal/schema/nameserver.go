package schema

import "strings"

// nameserverEquals reports whether two `nameserver` wire forms match
// PVE-fully. PVE accepts comma-separated on input (e.g. "1.1.1.1,8.8.8.8")
// but reports space-separated on /config (e.g. "1.1.1.1 8.8.8.8").
// pveconform normalizes both sides so PVE's whitespace reformat does not
// trip drift.
func nameserverEquals(current, want string) bool {
	if current == "" && want == "" {
		return true
	}
	if current == "" || want == "" {
		return false
	}
	// Split both sides on any comma-whitespace mix, compare as a set.
	cur := splitNameserver(current)
	wn := splitNameserver(want)
	if len(cur) != len(wn) {
		return false
	}
	// Order is significant: PVE stores in input order.
	for i := range cur {
		if cur[i] != wn[i] {
			return false
		}
	}
	return true
}

// splitNameserver returns the list of IP/hostname entries. PVE's own
// normalization splits on either comma or whitespace (after replacing
// commas with spaces); we follow that.
func splitNameserver(s string) []string {
	spacey := strings.ReplaceAll(s, ",", " ")
	return strings.Fields(spacey)
}
