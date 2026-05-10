package gpu

import (
	"testing"
)

func TestParseMemoryBytes(t *testing.T) {
	cases := []struct {
		input string
		want  uint64
		isErr bool
	}{
		{"16G", 16 * 1024 * 1024 * 1024, false},
		{"4096M", 4096 * 1024 * 1024, false},
		{"1024K", 1024 * 1024, false},
		{"1073741824", 1073741824, false},
		{"1g", 1024 * 1024 * 1024, false},
		{"512m", 512 * 1024 * 1024, false},
		{"", 0, true},
		{"notanumber", 0, true},
	}

	for _, tc := range cases {
		got, err := parseMemoryBytes(tc.input)
		if tc.isErr {
			if err == nil {
				t.Errorf("parseMemoryBytes(%q): expected error, got %d", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseMemoryBytes(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseMemoryBytes(%q): got %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		input uint64
		want  string
	}{
		{^uint64(0), "unlimited"},
		{16 * 1024 * 1024 * 1024, "16G"},
		{512 * 1024 * 1024, "512M"},
		{1024, "1K"},
		{512, "512B"},
	}

	for _, tc := range cases {
		got := FormatBytes(tc.input)
		if got != tc.want {
			t.Errorf("FormatBytes(%d): got %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestMemlockOK(t *testing.T) {
	unlimited := ^uint64(0)

	cases := []struct {
		name     string
		soft     uint64
		required uint64
		want     bool
	}{
		{"unlimited soft", unlimited, 16 * 1024 * 1024 * 1024, true},
		{"soft >= required", 32 * 1024 * 1024 * 1024, 16 * 1024 * 1024 * 1024, true},
		{"soft == required", 16 * 1024 * 1024 * 1024, 16 * 1024 * 1024 * 1024, true},
		{"soft < required", 8 * 1024 * 1024 * 1024, 16 * 1024 * 1024 * 1024, false},
		{"required zero", 4096, 0, false},
	}

	for _, tc := range cases {
		r := &PreflightResult{
			MemlockSoftBytes:     tc.soft,
			MemlockRequiredBytes: tc.required,
		}
		got := r.MemlockOK()
		if got != tc.want {
			t.Errorf("%s: MemlockOK() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
