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

func TestMemoryKiB(t *testing.T) {
	cases := map[string]int64{
		"8GiB":   8 * 1024 * 1024, // 8 GiB in KiB
		"1MiB":   1024,
		"512KiB": 512,
	}
	for in, want := range cases {
		got, err := MemoryKiB(in)
		if err != nil {
			t.Errorf("MemoryKiB(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("MemoryKiB(%q) = %d KiB, want %d", in, got, want)
		}
	}
	if _, err := MemoryKiB("1K"); err == nil {
		t.Error("MemoryKiB(1K) should fail (1000 not a multiple of 1024)")
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
