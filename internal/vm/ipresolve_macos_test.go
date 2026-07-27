package vm

import "testing"

// leasesFixture mirrors real bootpd output: hardware-type prefixes, octets
// without leading zeros, hex lease expiries, and a stale duplicate for the
// same MAC (recreated VM) that must lose to the fresher entry.
const leasesFixture = `{
	name=macvm
	ip_address=192.168.64.9
	hw_address=1,f6:38:41:1:e9:e6
	identifier=1,f6:38:41:1:e9:e6
	lease=0x64b5c000
}
{
	name=macvm
	ip_address=192.168.64.5
	hw_address=1,f6:38:41:1:e9:e6
	identifier=1,f6:38:41:1:e9:e6
	lease=0x64b5c92a
}
{
	name=other
	ip_address=192.168.64.7
	hw_address=1,52:54:0:12:34:56
	identifier=1,52:54:0:12:34:56
	lease=0x64b5c92b
}
`

func TestLookupDHCPDLease(t *testing.T) {
	tests := []struct {
		name   string
		mac    string
		wantIP string
		wantOK bool
	}{
		// Config MACs carry leading zeros; the lease file does not.
		{"leading-zero octets normalize", "f6:38:41:01:e9:e6", "192.168.64.5", true},
		{"uppercase normalizes", "F6:38:41:01:E9:E6", "192.168.64.5", true},
		{"other guest", "52:54:00:12:34:56", "192.168.64.7", true},
		{"unknown mac", "aa:bb:cc:dd:ee:ff", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want, err := normalizeMAC(tt.mac)
			if err != nil {
				t.Fatalf("normalizeMAC(%q): %v", tt.mac, err)
			}
			ip, ok := lookupDHCPDLease(leasesFixture, want)
			if ok != tt.wantOK || ip != tt.wantIP {
				t.Errorf("lookupDHCPDLease(%q) = (%q, %v), want (%q, %v)", tt.mac, ip, ok, tt.wantIP, tt.wantOK)
			}
		})
	}
}

func TestLookupDHCPDLeaseFreshest(t *testing.T) {
	// Both fixture blocks for f6:38:41:1:e9:e6 match; the higher lease
	// expiry (0x64b5c92a → .5) must win over the stale one (.9).
	want, _ := normalizeMAC("f6:38:41:01:e9:e6")
	ip, ok := lookupDHCPDLease(leasesFixture, want)
	if !ok || ip != "192.168.64.5" {
		t.Errorf("freshest lease: got (%q, %v), want (192.168.64.5, true)", ip, ok)
	}
}

func TestLookupArpAn(t *testing.T) {
	const out = `? (192.168.64.1) at 3e:76:previously:bad on bridge100 ifscope permanent [ethernet]
? (192.168.64.5) at f6:38:41:1:e9:e6 on bridge100 ifscope [ethernet]
? (192.168.64.255) at ff:ff:ff:ff:ff:ff on bridge100 ifscope [ethernet]
? (192.168.1.20) at (incomplete) on en0 ifscope [ethernet]
`
	want, _ := normalizeMAC("f6:38:41:01:e9:e6")
	ip, ok := lookupArpAn(out, want)
	if !ok || ip != "192.168.64.5" {
		t.Errorf("lookupArpAn = (%q, %v), want (192.168.64.5, true)", ip, ok)
	}
	missing, _ := normalizeMAC("aa:bb:cc:dd:ee:ff")
	if _, ok := lookupArpAn(out, missing); ok {
		t.Errorf("lookupArpAn found an IP for an absent MAC")
	}
}

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"F6:38:41:01:E9:E6", "f6:38:41:1:e9:e6", false},
		{"1:2:3:4:5:6", "1:2:3:4:5:6", false},
		{"52:54:00:12:34:56", "52:54:0:12:34:56", false},
		{"not-a-mac", "", true},
		{"f6:38:41:01:e9", "", true},     // 5 octets
		{"f6:38:41:001:e9:e6", "", true}, // 3-digit octet
	}
	for _, tt := range tests {
		got, err := normalizeMAC(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("normalizeMAC(%q): expected error, got %q", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("normalizeMAC(%q) = (%q, %v), want %q", tt.in, got, err, tt.want)
		}
	}
}
