package blockdev

import "testing"

func TestParseSerial(t *testing.T) {
	cases := map[string]string{
		"/dev/disk/by-id/ata-ST22000NM000C-3WC103_ZXA0S3H6":     "ZXA0S3H6",
		"/dev/disk/by-id/nvme-Samsung_SSD_990_PRO_2TB_S7DNNU0Y": "S7DNNU0Y",
		"/dev/disk/by-id/wwn-0x5000c5007445319d":                "",
		"/dev/sda":                                              "",
	}
	for in, want := range cases {
		if got := parseSerial(in); got != want {
			t.Errorf("parseSerial(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{0, ""},
		{500, "500B"},
		{1_000, "1KB"},
		{1_500, "1.5KB"},
		{2_000_000_000, "2GB"},
		{1_800_000_000_000, "1.8TB"},
		{22_000_000_000_000, "22TB"},
	}
	for _, c := range cases {
		if got := humanSize(c.in); got != c.want {
			t.Errorf("humanSize(%d) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestDescribeShortSkipsEmpty(t *testing.T) {
	d := Device{Model: "Foo SSD"}
	if got := d.DescribeShort(); got != "Foo SSD" {
		t.Errorf("DescribeShort = %q; want %q", got, "Foo SSD")
	}
	d = Device{SizeBytes: 2_000_000_000_000, Serial: "ABC"}
	if got := d.DescribeShort(); got != "2TB [ABC]" {
		t.Errorf("DescribeShort = %q; want %q", got, "2TB [ABC]")
	}
}
