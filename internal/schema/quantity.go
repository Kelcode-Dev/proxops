package schema

import (
	"fmt"
	"strconv"
	"strings"
)

// Human size parsing for manifest fields.
//
// PVE 9.2 wire units (empirically verified on conformance-dev and against
// PVE's own qm.conf(5)/pct.conf(5)): memory is an integer MiB count; disk /
// container volume sizes are expressed in GiB. Manifests use friendly units
// ("8GiB", "50GiB"); this package converts to PVE wire units at the schema
// boundary.

// MemoryMiB converts a human quantity ("8GiB") to PVE memory in MiB.
//
// PVE's create-time `memory` (QEMU) and `memory`/`swap` (LXC) fields are
// integer megabyte counts: `memory=100` is stored and reported as `100`.
// qm.conf documents QEMU memory "in MiB"; pct.conf documents LXC memory/swap
// "in MB". Sending KiB would be off by 1024× (1GiB would become ~1TiB).
func MemoryMiB(q string) (int64, error) {
	b, err := ParseBytes(q)
	if err != nil {
		return 0, err
	}
	const mib = int64(1) << 20
	if b%mib != 0 {
		return 0, fmt.Errorf("memory %q is not a whole number of MiB", q)
	}
	return b / mib, nil
}

// GiBString renders a byte count as the PVE volume-size number in GiB:
//
//	8589934592 bytes (8 GiB)  → "8"
//	536870912  bytes (512 MiB) → "0.5"
//	9126817280 bytes (8.5 GiB) → "8.5"
//
// PVE's LVM/LVM-thin VM disk spec (`scsi0=local-lvm:8`) and LXC container
// rootfs/mount-point spec (`rootfs=local-lvm:8`, documented as
// "STORAGE_ID:SIZE_IN_GiB") both read the number after the storage id as GiB,
// and accept fractional values (the web UI uses a 3-decimal GiB field). A bare
// byte count is misread as GiB — the original defect that produced
// "Volume too large (8.00 EiB)" for `local-lvm:8589934592`.
func GiBString(b int64) string {
	if b < 0 {
		b = 0
	}
	const gib = int64(1) << 30
	whole := b / gib
	if b%gib == 0 {
		return strconv.FormatInt(whole, 10)
	}
	// Fractional GiB: use the shortest round-tripping decimal. b is always
	// well below 2^53 bytes for any realistic disk, so float64 is exact.
	return strconv.FormatFloat(float64(b)/float64(gib), 'f', -1, 64)
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
