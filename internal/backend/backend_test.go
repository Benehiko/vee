package backend

import "testing"

func TestValid(t *testing.T) {
	tests := []struct {
		name Name
		want bool
	}{
		{"", true},
		{QEMU, true},
		{VZ, true},
		{"kvm", false},
		{"QEMU", false}, // names are case-sensitive
		{"vmapple", false},
	}
	for _, tt := range tests {
		if got := Valid(tt.name); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
