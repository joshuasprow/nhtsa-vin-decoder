package decoder

import "testing"

// TestKeyOverlaps: two keys overlap when one var_keys matches both, as the
// pattern pass matches them. For each overlap claimed, a var_keys built
// position by position from characters both keys accept matches each key;
// for each refused, no character at the position that refutes it is
// accepted by both.
func TestKeyOverlaps(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"1G1*K", "1G1Z", true},
		{"1G1*K", "1G2", false},
		{"AB", "ABCD", true}, // a key is a prefix test
		{"", "ABC", true},
		{"_X", "YX", true},
		{"**[A-C]", "**B", true},
		{"**[A-C]", "**D", false},
		{"[1-3]A", "[3-5]", true},
		{"[1-3]A", "[4-5]", false},
		{"[A-C]1", "[C-E]2", false},
		{"[A-C]*", "[C-E]2", true},
		{"[C-A]", "*", false},  // Postgres rejects the key: it matches nothing
		{"A&", "A*", false},    // outside the matcher's grammar: matches nothing
		{"A%B", "ZZZ", true},   // a LIKE key with '%' overlaps every key
		{"A\\*", "ZZZ", true},  // and one with '\'
		{"[C-A]", "A%", false}, // but not a key that matches nothing
	} {
		a, b := CompileKey(tc.a), CompileKey(tc.b)
		if got := a.Overlaps(b); got != tc.want {
			t.Errorf("%q overlaps %q: %v; want %v", tc.a, tc.b, got, tc.want)
		}
		if got := b.Overlaps(a); got != tc.want {
			t.Errorf("%q overlaps %q: %v; want %v", tc.b, tc.a, got, tc.want)
		}
		if a.m.err != nil || b.m.err != nil || a.m.re != nil || b.m.re != nil {
			continue
		}
		n := max(a.m.positions(), b.m.positions())
		witness := make([]byte, n)
		refuted := false
		for i := range n {
			found := false
			for c := byte(0x21); c < 0x7f; c++ {
				if accepts(&a.m, i, c) && accepts(&b.m, i, c) {
					witness[i], found = c, true
					break
				}
			}
			refuted = refuted || !found
		}
		if refuted == tc.want {
			t.Errorf("%q and %q: overlap %v, but a position refutes it: %v", tc.a, tc.b, tc.want, refuted)
			continue
		}
		if tc.want {
			for key, k := range map[string]Key{tc.a: a, tc.b: b} {
				if ok, _ := k.m.match(string(witness)); !ok {
					t.Errorf("%q and %q overlap, but %q does not match %q", tc.a, tc.b, key, witness)
				}
			}
		}
	}
}

// accepts reports whether m accepts c at var_keys position i: any
// character past its last position.
func accepts(m *keyMatcher, i int, c byte) bool {
	if i >= m.positions() {
		return true
	}
	s := m.at(i)
	return s.has(c)
}
