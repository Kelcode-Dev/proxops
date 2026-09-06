package schema

import "testing"

func TestParseBytes(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"123", 123},
		{"0", 0},
		{"1B", 1},
		{"10K", 10000},
		{"10KB", 10000},
		{"10Ki", 10240},
		{"10KiB", 10240},
		{"1M", 1000000},
		{"1Mi", 1048576},
		{"50G", 50 * 1000 * 1000 * 1000},
		{"50GiB", 50 * 1024 * 1024 * 1024},
		{"1T", 1000 * 1000 * 1000 * 1000},
	}
	for _, tc := range cases {
		got, err := ParseBytes(tc.in)
		if err != nil {
			t.Errorf("ParseBytes(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseBytesInvalid(t *testing.T) {
	for _, in := range []string{"", "abc", "10X", "10GiBb", "10GiB extra", "  1 0GiB"} {
		if _, err := ParseBytes(in); err == nil {
			t.Errorf("ParseBytes(%q) should fail", in)
		}
	}
}

// TestMemoryMiB — PVE's memory wire unit is MiB (qm.conf(5): "in MiB"),
// verified live on PVE 9.2: memory=100 → stored/reported as 100.
func TestMemoryMiB(t *testing.T) {
	// Note: PVE's PVM memory is an integer MiB count; manifests are expected
	// to use binary ("32MiB"), which is also what PVE's own UI fields imply.
	cases := map[string]int64{
		"8GiB":   8192, // 8 GiB in MiB
		"1GiB":   1024,
		"1MiB":   1,
		"512MiB": 512,
		"32MiB":  32,
	}
	for in, want := range cases {
		got, err := MemoryMiB(in)
		if err != nil {
			t.Errorf("MemoryMiB(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("MemoryMiB(%q) = %d MiB, want %d", in, got, want)
		}
	}
	if _, err := MemoryMiB("1"); err == nil {
		t.Error("MemoryMiB(1) should fail (1 byte is not a whole MiB)")
	}
}

// TestGiBString — PVE's disk volume size number is GiB (verified live:
// local-lvm:1 → 1073741824-byte LV; local-lvm:0.5 → 512 MiB LV;
// local-lvm:8589934592 → "Volume too large (8.00 EiB)").
func TestGiBString(t *testing.T) {
	cases := []struct{ in, want string }{
		{in: "8GiB", want: "8"},
		{in: "50GiB", want: "50"},
		{in: "1GiB", want: "1"},
		{in: "512MiB", want: "0.5"},
		{in: "1536MiB", want: "1.5"},
	}
	for _, c := range cases {
		b, err := ParseBytes(c.in)
		if err != nil {
			t.Fatalf("ParseBytes(%q): %v", c.in, err)
		}
		if got := GiBString(b); got != c.want {
			t.Errorf("GiBString(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatDiskBytes(t *testing.T) {
	if got := FormatDiskBytes(50 * 1024 * 1024 * 1024); got != "50G" {
		t.Errorf("FormatDiskBytes(50GiB) = %q, want 50G", got)
	}
	if got := FormatDiskBytes(512 * 1024 * 1024); got != "512M" {
		t.Errorf("FormatDiskBytes(512MiB) = %q, want 512M", got)
	}
	// Non-exact multiples are rendered in bytes (never silently rounded down).
	if got := FormatDiskBytes(1<<30 + 1); got != "1073741825" {
		t.Errorf("FormatDiskBytes(1GiB+1) = %q, want 1073741825", got)
	}
}
