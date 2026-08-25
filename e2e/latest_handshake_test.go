//go:build e2e

package e2e_test

import "testing"

func TestLatestHandshakeParsing(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"peerkey\t1787684683", 1787684683},
		{"1787684683", 1787684683},
		{"peerkey\t0", 0},
		{"", 0},
		{"dial error: connect: connection refused", 0},
		{"sudo: unable to resolve host x\npeerkey\t1787684683", 1787684683},
		{"a\t100\nb\t900", 900},
	} {
		if got := latestHandshake(tc.in); got != tc.want {
			t.Errorf("latestHandshake(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
