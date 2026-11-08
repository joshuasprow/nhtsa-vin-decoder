package decoder

import (
	"fmt"
	"strings"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// validChars is the VIN characters fValidCharsInRegEx tests a bracket
// against, the 33 a VIN may hold.
const validChars = "ABCDEFGHJKLMNPRSTUVWXYZ0123456789"

// validCharsInKey ports fValidCharsInKey (fvalidcharsinkey.sql) in its
// strict mode, the only one it runs: one set per key position, sets[i] for
// the key's (i+1)th token. '#' is the ten digits, '*' is nothing, a bracket
// expression is validCharsInRegex of it, and any other character is itself,
// '|' included; the callers drop or skip what they do not read. A '[' with
// no ']' ends the key: its characters are dropped and it takes no position.
//
// The error is the one Postgres raises evaluating a bracket, which aborts the
// decode, or errKeyUnsupported for a key the port declines: a non-ASCII
// byte, or a set holding '&', which sanitize's '&' for a non-ASCII rune
// would satisfy where Postgres's rune does not (see keyMatcher). None is in
// the 2026_10 dump, whose alphabet is #*-[]_|0-9A-Z.
func validCharsInKey(keys string) ([]byteSet, error) {
	if !isASCII(keys) {
		return nil, errKeyUnsupported
	}
	sets := make([]byteSet, 0, len(keys))
	open := -1 // the index of an unclosed '['
	for i := range len(keys) {
		c := keys[i]
		switch {
		case open < 0 && c == '[':
			open = i
		case open < 0:
			var s byteSet
			switch c {
			case '#':
				for d := byte('0'); d <= '9'; d++ {
					s.add(d)
				}
			case '*':
			default:
				s.add(c)
			}
			sets = append(sets, s)
		case c == ']':
			s, err := validCharsInRegex(keys[open : i+1])
			if err != nil {
				return nil, err
			}
			s.remove('*')
			s.remove('|')
			sets = append(sets, s)
			open = -1
		}
	}
	for i := range sets {
		if sets[i].has('&') {
			return nil, errKeyUnsupported
		}
	}
	return sets, nil
}

// validCharsInRegex ports fValidCharsInRegEx (fvalidcharsinregex.sql) on
// one bracket expression of a key, '[' to the first ']'. Upper-cased, one
// without '-' or '^' is the list of its characters; any other is the
// regular expression '^' || str || '$', and the result is the validChars it
// matches. That expression is the key's own bracket, with none of
// sqlwild_to_regex's escaping or its "1-A" rewrite, so [1-A-JT], which the
// pattern pass reads as [1A-JT], is an invalid range here, and a leading '^'
// negates.
func validCharsInRegex(str string) (byteSet, error) {
	str = strings.ToUpper(str)
	var out byteSet
	if !strings.ContainsAny(str, "-^") {
		for i := range len(str) {
			if c := str[i]; c != '[' && c != ']' {
				out.add(c)
			}
		}
		return out, nil
	}
	i, negate := 1, false
	if len(str) > 1 && str[1] == '^' {
		i, negate = 2, true
	}
	set, _, err := bracket(str, i)
	if err != nil {
		return out, err
	}
	for j := range len(validChars) {
		if c := validChars[j]; set.has(c) != negate {
			out.add(c)
		}
	}
	return out, nil
}

// schemaCharset is the union of validCharsInKey over one VinSchema's
// distinct pattern keys, by VIN position (a key's position plus 3): what
// the VinSchema adds to fExtractValidCharsPerWmiYear. err is the first key
// validCharsInKey rejects; Postgres raises on it.
type schemaCharset struct {
	sets []byteSet // sets[p] for VIN position p
	err  error
}

var emptySchemaCharset = &schemaCharset{}

// schemaCharset returns VinSchema id's charset, building it on first use.
func (e *engine) schemaCharset(id int32) *schemaCharset {
	if id < 0 || int(id) >= len(e.charsets) {
		return emptySchemaCharset
	}
	slot := &e.charsets[id]
	if c := slot.Load(); c != nil {
		return c
	}
	slot.CompareAndSwap(nil, e.newSchemaCharset(id))
	return slot.Load()
}

func (e *engine) newSchemaCharset(id int32) *schemaCharset {
	c := &schemaCharset{}
	lo, hi := artifact.EqualRange(e.pattern.vinSchemaID, 0, len(e.pattern.vinSchemaID), id)
	seen := make(map[uint32]bool, hi-lo)
	for r := lo; r < hi; r++ {
		ref := e.pattern.keys.Ref(r)
		if seen[ref] {
			continue
		}
		seen[ref] = true
		keys, ok := e.pattern.keys.At(r)
		if !ok {
			continue // a NULL key yields no rows
		}
		sets, err := validCharsInKey(keys)
		if err != nil {
			return &schemaCharset{err: fmt.Errorf("pattern key %q: %w", keys, err)}
		}
		for j := range sets {
			p := j + 4
			for len(c.sets) <= p {
				c.sets = append(c.sets, byteSet{})
			}
			c.sets[p].union(&sets[j])
		}
	}
	return c
}

// charset ports fExtractValidCharsPerWmiYear(var_wmi, year)
// (fextractvalidcharsperwmiyear.sql), the dump's fallback, which
// spvindecode_errorcode reads only for a (WMI, year) cell its
// wmiyearvalidchars cache lacks. nitrovin always takes it and never ships
// the cache (ADR 0006): the cache is stale in thousands of cells, see
// docs/deviations/stale-cache.md.
//
// The cell is the union of every schemaCharset in a band covering year of
// any wmi row with the WMI (rows [wlo, whi)), with no availability or
// TobeQCed filter; the result is indexed by VIN position. year 0 is NULL,
// which no band covers.
func (e *engine) charset(wlo, whi, year int) ([]byteSet, error) {
	if year == 0 {
		return nil, nil
	}
	var cell []byteSet
	wvs := &e.wmiVinSchema
	for r := wlo; r < whi; r++ {
		lo, hi := artifact.EqualRange(wvs.wmiID, 0, len(wvs.wmiID), e.wmi.id[r])
		for b := lo; b < hi; b++ {
			if !e.inBand(b, year) {
				continue
			}
			sid := wvs.vinSchemaID[b]
			if vs, vsEnd := artifact.EqualRange(e.vinSchema.id, 0, len(e.vinSchema.id), sid); vs == vsEnd {
				continue // inner join VinSchema
			}
			c := e.schemaCharset(sid)
			if c.err != nil {
				return nil, fmt.Errorf("decoder: VinSchema %d: %w", sid, c.err)
			}
			for len(cell) < len(c.sets) {
				cell = append(cell, byteSet{})
			}
			for p := range c.sets {
				cell[p].union(&c.sets[p])
			}
		}
	}
	return cell, nil
}
