package gpu

import (
	"testing"
)

func TestParseRebarSize(t *testing.T) {
	cases := []struct {
		in      string
		wantExp int
		wantErr bool
	}{
		{"1M", 0, false},
		{"256M", 8, false},
		{"512M", 9, false},
		{"1G", 10, false},
		{"16G", 14, false},
		{"32G", 15, false},
		{"", 0, true},
		{"0", 0, true},
		{"3G", 0, true},    // not a power of two
		{"1500M", 0, true}, // not a power of two
		{"512K", 0, true},  // below 1M
		{"junk", 0, true},
	}
	for _, c := range cases {
		exp, err := ParseRebarSize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRebarSize(%q): want error, got exp=%d", c.in, exp)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRebarSize(%q): %v", c.in, err)
			continue
		}
		if exp != c.wantExp {
			t.Errorf("ParseRebarSize(%q) = %d, want %d", c.in, exp, c.wantExp)
		}
	}
}

func TestRebarEntrySizeBytes(t *testing.T) {
	e := RebarEntry{Exp: 14}
	if got := e.SizeBytes(); got != 16<<30 {
		t.Errorf("SizeBytes(exp=14) = %d, want %d", got, 16<<30)
	}
}

func TestFormatRebarSizes(t *testing.T) {
	// 0x7f00: bits 8..14 -> 256M..16G, as reported by an RX 7900 GRE.
	got := FormatRebarSizes(0x7f00)
	want := "256M, 512M, 1G, 2G, 4G, 8G, 16G"
	if got != want {
		t.Errorf("FormatRebarSizes(0x7f00) = %q, want %q", got, want)
	}
}

func TestRebarConfRoundTrip(t *testing.T) {
	entries := []RebarEntry{
		{Addr: "0000:08:00.0", Bar: 0, Exp: 14},
		{Addr: "0000:03:00.0", Bar: 2, Exp: 8},
	}
	parsed, err := ParseRebarConf(RenderRebarConf(entries))
	if err != nil {
		t.Fatalf("ParseRebarConf: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("got %d entries, want 2", len(parsed))
	}
	// RenderRebarConf sorts by address.
	if parsed[0].Addr != "0000:03:00.0" || parsed[0].Bar != 2 || parsed[0].Exp != 8 {
		t.Errorf("entry 0 = %+v", parsed[0])
	}
	if parsed[1].Addr != "0000:08:00.0" || parsed[1].Bar != 0 || parsed[1].Exp != 14 {
		t.Errorf("entry 1 = %+v", parsed[1])
	}
}

func TestParseRebarConfNormalizesAndRejects(t *testing.T) {
	entries, err := ParseRebarConf("# comment\n\n08:00.0 0 14\n")
	if err != nil {
		t.Fatalf("ParseRebarConf: %v", err)
	}
	if len(entries) != 1 || entries[0].Addr != "0000:08:00.0" {
		t.Errorf("entries = %+v", entries)
	}

	if _, err := ParseRebarConf("08:00.0 14\n"); err == nil {
		t.Error("want error for 2-field line")
	}
	if _, err := ParseRebarConf("08:00.0 x 14\n"); err == nil {
		t.Error("want error for non-numeric bar")
	}
}

func TestMergeAndRemoveRebarEntry(t *testing.T) {
	var entries []RebarEntry
	entries = MergeRebarEntry(entries, RebarEntry{Addr: "08:00.0", Bar: 0, Exp: 10})
	if len(entries) != 1 || entries[0].Addr != "0000:08:00.0" {
		t.Fatalf("after insert: %+v", entries)
	}

	// Same device+bar replaces rather than duplicates.
	entries = MergeRebarEntry(entries, RebarEntry{Addr: "0000:08:00.0", Bar: 0, Exp: 14})
	if len(entries) != 1 || entries[0].Exp != 14 {
		t.Fatalf("after replace: %+v", entries)
	}

	entries = MergeRebarEntry(entries, RebarEntry{Addr: "03:00.0", Bar: 0, Exp: 14})
	if len(entries) != 2 {
		t.Fatalf("after second insert: %+v", entries)
	}

	entries, found := RemoveRebarEntry(entries, "08:00.0", 0)
	if !found || len(entries) != 1 || entries[0].Addr != "0000:03:00.0" {
		t.Fatalf("after remove: found=%v entries=%+v", found, entries)
	}

	if _, found := RemoveRebarEntry(entries, "08:00.0", 0); found {
		t.Error("remove of absent entry reported found")
	}
}
