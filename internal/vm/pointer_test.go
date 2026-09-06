package vm

import "testing"

func TestValidatePointer(t *testing.T) {
	for _, ok := range []string{"", "tablet", "mouse"} {
		if err := ValidatePointer(ok); err != nil {
			t.Errorf("ValidatePointer(%q) = %v, want nil", ok, err)
		}
	}
	if err := ValidatePointer("trackball"); err == nil {
		t.Error("ValidatePointer(trackball) = nil, want error")
	}
}
