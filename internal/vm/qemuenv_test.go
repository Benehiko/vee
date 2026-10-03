package vm

import "testing"

func TestValidateQemuEnv(t *testing.T) {
	if err := ValidateQemuEnv([]string{"A=1", "LD_LIBRARY_PATH=/x:/y", "EMPTY="}); err != nil {
		t.Fatalf("valid env rejected: %v", err)
	}
	for _, bad := range []string{"NOEQUALS", "=value", "A=b\x00c"} {
		if err := ValidateQemuEnv([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
