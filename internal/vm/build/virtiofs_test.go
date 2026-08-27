package build

import "testing"

func TestValidateVirtiofsRequest(t *testing.T) {
	tests := []struct {
		name      string
		dir       string
		supported bool
		wantErr   bool
	}{
		{name: "no share requested", dir: "", supported: false},
		{name: "supported host", dir: "/srv/share", supported: true},
		{name: "unsupported host with share", dir: "/srv/share", supported: false, wantErr: true},
		{name: "unsupported host without share", dir: "", supported: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVirtiofsRequest(tt.dir, tt.supported)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVirtiofsRequest(%q, %v) error = %v, wantErr %v", tt.dir, tt.supported, err, tt.wantErr)
			}
		})
	}
}
