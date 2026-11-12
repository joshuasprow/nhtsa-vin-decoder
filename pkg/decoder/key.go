package decoder

// Key is one pattern.keys compiled as the pattern pass matches it
// (compileKey): a test on a prefix of a VIN's var_keys, the string every
// pattern key is matched against, which is the VIN's positions 4-8, then
// '|' and positions 10-17. The key accepts one set of characters per
// position. It lets a reader of the artifact ask which patterns could
// match one VIN together (ADR 0015) with the decoder's own reading of a
// key.
type Key struct{ m keyMatcher }

// CompileKey compiles pattern key keys as the pattern pass does.
func CompileKey(keys string) Key { return Key{compileKey(keys)} }

// Overlaps reports false only when no var_keys matches both k and o; it
// may report true when none does. It compares the keys position by
// position, over the shorter key's length: they overlap when the
// characters each accepts intersect at every position. A key the pattern
// pass rejects matches nothing, so it overlaps nothing. A LIKE key holding
// '%' or '\', which no 2026_10 key does, is not read position by position
// and overlaps every key that matches anything.
func (k Key) Overlaps(o Key) bool {
	a, b := &k.m, &o.m
	switch {
	case a.err != nil || b.err != nil:
		return false
	case a.re != nil || b.re != nil:
		return true
	}
	if a.sets == nil && b.sets == nil { // two LIKE keys, the dump's usual
		for i := range min(len(a.like), len(b.like)) {
			if x, y := a.like[i], b.like[i]; x != y && x != '*' && x != '_' && y != '*' && y != '_' {
				return false
			}
		}
		return true
	}
	for i := range min(a.positions(), b.positions()) {
		sa, sb := a.at(i), b.at(i)
		if sa[0]&sb[0] == 0 && sa[1]&sb[1] == 0 && sa[2]&sb[2] == 0 && sa[3]&sb[3] == 0 {
			return false
		}
	}
	return true
}

// positions is how many characters of var_keys a LIKE or bracket key tests.
func (m *keyMatcher) positions() int {
	if m.sets != nil {
		return len(m.sets)
	}
	return len(m.like)
}

// at is the set of characters a LIKE or bracket key accepts at position i.
func (m *keyMatcher) at(i int) byteSet {
	if m.sets != nil {
		return m.sets[i]
	}
	var s byteSet
	switch c := m.like[i]; c {
	case '*', '_':
		return anyByte
	default:
		s.add(c)
	}
	return s
}
