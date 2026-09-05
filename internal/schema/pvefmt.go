package schema

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLTo decodes a YAML document into target (a pointer to a typed schema
// struct, e.g. *VM). It is the canonical entry point for callers that have a
// raw manifest string (tests, future `adopt`, docs generators).
func YAMLTo(s string, target any) error {
	return yaml.Unmarshal([]byte(s), target)
}

// pveDiskSizeBytes parses a PVE disk size string into bytes. PVE accepts
// "50G" (binary) or "53687091200" (bare bytes); this normalizes both to bytes
// so the differ can compare owned disk sizes regardless of spelling.
//
// Suffix scale (PVE convention, binary): K=2^10, M=2^20, G=2^30, T=2^40.
func pveDiskSizeBytes(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, false
	}
	// Bare integer → bytes.
	if v, err := strconv.ParseInt(strings.ToLower(s), 10, 64); err == nil {
		return v, true
	}
	var mult int64
	switch {
	case strings.HasSuffix(s, "T"):
		s = strings.TrimSuffix(s, "T")
		mult = 1 << 40
	case strings.HasSuffix(s, "G"):
		s = strings.TrimSuffix(s, "G")
		mult = 1 << 30
	case strings.HasSuffix(s, "M"):
		s = strings.TrimSuffix(s, "M")
		mult = 1 << 20
	case strings.HasSuffix(s, "K"):
		s = strings.TrimSuffix(s, "K")
		mult = 1 << 10
	default:
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		// allow floats like "50.5G"? PVE does not. Treat as failure.
		return 0, false
	}
	return int64(n) * mult, true
}

// diskSizeEqual reports whether two PVE disk size strings are the same bytes.
// Either side may be empty (then equal only if the other is empty).
func diskSizeEqual(a, b string) bool {
	ab, oa := pveDiskSizeBytes(a)
	bb, ob := pveDiskSizeBytes(b)
	if !oa && !ob {
		return true // both unparseable → treat as equal to avoid false drift
	}
	if oa != ob {
		return false
	}
	return ab == bb
}

var _ = fmt.Sprintf
