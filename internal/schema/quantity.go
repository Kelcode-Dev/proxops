package schema

import (
	"fmt"
	"strconv"
	"strings"
)

// Human size parsing for manifest fields.
//
// PVE wire units: memory is KiB (int); disk size is bytes.
// Manifests use human-friendly units ("8GiB", "50GiB"); this package
// converts to PVE wire units at the schema boundary.

// MemoryKiB converts a human quantity ("8GiB") to PVE memory KiB.
func MemoryKiB(q string) (int64, error) {
	b, err := ParseBytes(q)
	if err != nil {
		return 0, err
	}
	if b%1024 != 0 {
		return 0, fmt.Errorf("memory %q is not a whole number of KiB", q)
	}
	return b / 1024, nil
}

// ParseBytes parses a quantity string to bytes.
//
//	"123"          → 123 bytes
//	"512KiB"       → 524288 bytes
//	"512K"         → 512000 bytes (decimal)
//	"8GiB"         → 8589934592 bytes
//	"8G"           → 8000000000 bytes (decimal)
//	"2T"           → 2e12 bytes
//
// Unit suffixes: KiB/Ki/K = 1024; MiB/Mi/M = 1MiB; GiB/Gi/G = 1GiB; TiB/Ti/T = 1TiB; PiB/Pi/P = 1PiB.
// Decimal SI ("500M") = 5e8 is preserved: PVE uses binary for sizes with
// K/M/G/T/P (case-insensitive) and KiB is also accepted. Both forms are
// commonly seen in the wild (PVE's own `qm` output uses the binary letters).
func ParseBytes(q string) (int64, error) {
	trimmed := strings.TrimSpace(q)
	if trimmed == "" {
		return 0, fmt.Errorf("empty quantity")
	}
	if v, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return v, nil
	}
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("quantity %q must start with a digit", q)
	}
	num, err := strconv.ParseInt(trimmed[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bad quantity number in %q: %w", q, err)
	}
	unit := strings.ToUpper(trimmed[i:])
	switch unit {
	case "B", "":
		return num, nil
	case "K", "KB":
		return num * 1000, nil
	case "KI", "KIB":
		return num * 1024, nil
	case "M", "MB":
		return num * 1000 * 1000, nil
	case "MI", "MIB":
		return num * 1024 * 1024, nil
	case "G", "GB":
		return num * 1000 * 1000 * 1000, nil
	case "GI", "GIB":
		return num * 1024 * 1024 * 1024, nil
	case "T", "TB":
		return num * 1000 * 1000 * 1000 * 1000, nil
	case "TI", "TIB":
		return num * 1024 * 1024 * 1024 * 1024, nil
	case "P", "PB":
		return num * 1000 * 1000 * 1000 * 1000 * 1000, nil
	case "PI", "PIB":
		return num * 1024 * 1024 * 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unknown quantity unit %q (use K/M/G/T/P or KiB/MiB/GiB/TiB/PiB)", unit)
	}
}

// FormatDiskBytes renders a byte count in PVE disk size style ("50G", "512M").
// Rounds down to the chosen suffix.
func FormatDiskBytes(b int64) string {
	for _, mult := range []struct {
		m   int64
		suf string
	}{
		{1 << 30, "G"},
		{1 << 20, "M"},
		{1 << 10, "K"},
	} {
		if b >= mult.m && b%(mult.m) == 0 {
			return strconv.FormatInt(b/mult.m, 10) + mult.suf
		}
	}
	return strconv.FormatInt(b, 10)
}
