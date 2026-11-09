package pki

import "testing"

// TestSameSerialSet: RunDenyWatch relies on this to tell whether the deny list changed. The key case is "same
// length, different contents" (one entry expires and drops out while a new revocation is added in the same
// tick); the old code compared only lengths, missed it, and the revocation was never broadcast.
func TestSameSerialSet(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		same bool
	}{
		{"完全相同", []string{"1", "2"}, []string{"1", "2"}, true},
		{"順序不同視為相同", []string{"1", "2"}, []string{"2", "1"}, true},
		{"長度不同", []string{"1"}, []string{"1", "2"}, false},
		{"長度相同內容不同(關鍵漏播案例)", []string{"1", "2"}, []string{"1", "3"}, false},
		{"皆空", nil, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sameSerialSet(c.a, c.b); got != c.same {
				t.Errorf("sameSerialSet(%v,%v) = %v, want %v", c.a, c.b, got, c.same)
			}
		})
	}
}
