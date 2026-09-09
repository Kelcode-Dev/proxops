package schema

import (
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

// pveDiskSizeBytes parses a PVE disk size string into bytes. PVE reports
// owned disk sizes with a binary suffix ("8G", "512M", "16T", "1K") — cf. the
// qm.conf "pve-size" format, which renders whole KiB/MiB/GiB/TiB values that
// way. This normalizes both a suffixed and a bare-byte string to bytes, so
// the differ can compare owned disk sizes regardless of spelling.
//
// Suffix scale (PVE convention, binary): K=2^10, M=2^20, G=2^30, T=2^40.
// A fractional mantissa ("0.5G") is supported: PVE stores half-GiB LVM
// volumes and reports them as "512M", but its config grammar tolerates the
// decimal form too.
func pveDiskSizeBytes(s string) (int64, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, false
	}
	// Bare integer → bytes.
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v, true
	}
	var mult int64
	var mantissa string
	switch {
	case strings.HasSuffix(s, "T"):
		mantissa = strings.TrimSuffix(s, "T")
		mult = 1 << 40
	case strings.HasSuffix(s, "G"):
		mantissa = strings.TrimSuffix(s, "G")
		mult = 1 << 30
	case strings.HasSuffix(s, "M"):
		mantissa = strings.TrimSuffix(s, "M")
		mult = 1 << 20
	case strings.HasSuffix(s, "K"):
		mantissa = strings.TrimSuffix(s, "K")
		mult = 1 << 10
	default:
		return 0, false
	}
	mantissa = strings.TrimSpace(mantissa)
	if f, err := strconv.ParseFloat(mantissa, 64); err == nil {
		// Mantissa is a float ("0.5", "8", "1.5").
		return int64(f * float64(mult)), true
	}
	if iv, err := strconv.Atoi(mantissa); err == nil {
		return int64(iv) * mult, true
	}
	return 0, false
}
