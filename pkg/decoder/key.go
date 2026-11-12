package decoder

// Key is one pattern.keys compiled as the pattern pass matches it
// (compileKey): a test on a prefix of var_keys, one set of accepted
// characters per position. It lets a reader of the artifact ask which
// patterns could match one VIN together (ADR 0015) with the decoder's own
// reading of a key.
type Key struct{ m keyMatcher }

// CompileKey compiles pattern key keys as the pattern pass does.
func CompileKey(keys string) Key { return Key{compileKey(keys)} }

// Overlaps reports whether some var_keys matches both k and o: position by
// position, over the shorter key's length, the characters each accepts
// intersect. A key the pattern pass rejects matches nothing and so
// overlaps nothing. A LIKE key holding '%' or '\', which no 2026_10 key
// does, is not read position by position: it overlaps every key that
// matches anything, so a reader of overlaps never leaves out a pair.
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
