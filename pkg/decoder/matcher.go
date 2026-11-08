package decoder

import (
	"errors"
	"regexp"
	"strings"
)

// The core's pattern pass (spvindecode_core.sql:96-97) keeps a pattern when
//
//	(p.keys NOT LIKE '%[%' AND var_keys LIKE replace(p.keys, '*', '_') || '%')
//	OR (p.keys LIKE '%[%' AND var_keys ~ p.keys_regex)
//
// where keys_regex is the dump's generated column sqlwild_to_regex(keys). A
// keyMatcher is that clause compiled for one key. Both branches are anchored
// prefix matches with every token consuming exactly one character of
// var_keys, so a key compiles to a fixed-length prefix test; Go's regexp is
// only the fallback for a LIKE key holding '%' or '\', which no key in the
// 2026_10 dump does.
//
// var_keys is sanitized (see sanitize): ASCII, with '&' for each non-ASCII
// rune. That '&' is ambiguous: it is an input '&' or a rune no ASCII key can
// name, which Postgres matches with '_', '*' and '.' only. A key that accepts
// '&' anywhere else (a literal '&', or a class such as [!--]) would match
// the two differently, so it compiles to errKeyUnsupported, as does a key
// with a non-ASCII byte. The dump's alphabet is #*-[]_|0-9A-Z.
type keyMatcher struct {
	// like is a LIKE key without '%' or '\': position i of var_keys must
	// equal like[i] unless like[i] is '*' or '_'.
	like string
	// sets is a bracket key, one set of accepted bytes per regex token.
	sets []byteSet
	// re is a LIKE key with '%' or '\', as an equivalent RE2 regexp.
	re *regexp.Regexp
	// err is set when the oracle would raise an error evaluating the key
	// (an invalid regular expression), or the key is outside what this port
	// handles (errKeyUnsupported). Such a key matches nothing.
	err error
}

var (
	// errKeyRange is Postgres's "invalid regular expression: invalid
	// character range": a reversed range ([C-A]), a range starting where
	// one ended ([A-C-E]) or ending on a range operator ([A--B]).
	errKeyRange = errors.New("invalid regular expression: invalid character range")
	// errKeyBrackets is Postgres's "invalid regular expression: brackets []
	// not balanced": a '[' with no ']'.
	errKeyBrackets = errors.New("invalid regular expression: brackets [] not balanced")
	// errKeyUnsupported is a key this port declines rather than match
	// differently from Postgres: a non-ASCII byte, '&' accepted other than
	// by a wildcard, or a collating element, equivalence class or character
	// class ([. [= [:) inside a bracket expression. None is in the dump.
	errKeyUnsupported = errors.New("pattern key outside the matcher's grammar")
)

// compileKey compiles one pattern.keys for the core's pattern clause.
func compileKey(keys string) keyMatcher {
	if !strings.Contains(keys, "[") {
		return compileLike(keys)
	}
	if !isASCII(keys) {
		return keyMatcher{err: errKeyUnsupported}
	}
	sets, err := regexSets(sqlwildToRegex(keys))
	if err != nil {
		return keyMatcher{err: err}
	}
	for i := range sets {
		if sets[i] != anyByte && sets[i].has('&') {
			return keyMatcher{err: errKeyUnsupported}
		}
	}
	return keyMatcher{sets: sets}
}

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// compileLike compiles `LIKE replace(keys, '*', '_') || '%'`, the clause's
// first branch and the formula pass's whole clause
// (spvindecode_core.sql:174). '_' and '*' accept any one character; with
// the trailing '%', the key need only match a prefix. In Postgres LIKE, '%'
// inside the key is any run of characters and '\' escapes the next
// character, so such a key goes to an RE2 regexp, which then also matches
// newlines with '.' as LIKE does.
func compileLike(keys string) keyMatcher {
	if !isASCII(keys) || strings.IndexByte(keys, '&') >= 0 {
		return keyMatcher{err: errKeyUnsupported}
	}
	if !strings.ContainsAny(keys, `%\`) {
		return keyMatcher{like: keys}
	}
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	pat := strings.ReplaceAll(keys, "*", "_") + "%"
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; c {
		case '_':
			b.WriteByte('.')
		case '%':
			b.WriteString(".*")
		case '\\':
			// The appended '%' follows every key, so an escape always
			// has a character to escape.
			i++
			b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
		default:
			b.WriteString(regexp.QuoteMeta(pat[i : i+1]))
		}
	}
	b.WriteString(`\z`)
	return keyMatcher{re: regexp.MustCompile(b.String())}
}

// match reports whether the key accepts varKeys; err is the key's compile
// error, with matched false.
func (m *keyMatcher) match(varKeys string) (matched bool, err error) {
	switch {
	case m.err != nil:
		return false, m.err
	case m.re != nil:
		return m.re.MatchString(varKeys), nil
	case m.sets != nil:
		if len(varKeys) < len(m.sets) {
			return false, nil
		}
		for i := range m.sets {
			if !m.sets[i].has(varKeys[i]) {
				return false, nil
			}
		}
		return true, nil
	default:
		if len(varKeys) < len(m.like) {
			return false, nil
		}
		for i := range len(m.like) {
			if k := m.like[i]; k != varKeys[i] && k != '*' && k != '_' {
				return false, nil
			}
		}
		return true, nil
	}
}

// sqlwildToRegex ports vpic.sqlwild_to_regex, the function the dump's
// pattern.keys_regex column is generated by: '*' becomes '.', '|' and the
// ARE metacharacters \ . ^ $ + ? { } ( ) are escaped, '[' and ']' pass
// through, then every "1-A" becomes "1A" and the result is anchored as
// ^…​.*. The "1-A" rewrite applies to the whole regex, inside brackets and
// out, so [1-A-JT] is [1A-JT] and a literal 1-A outside brackets loses its
// '-'.
func sqlwildToRegex(keys string) string {
	var b strings.Builder
	b.Grow(len(keys) + 8)
	b.WriteByte('^')
	for i := range len(keys) {
		switch c := keys[i]; c {
		case '*':
			b.WriteByte('.')
		case '|', '\\', '.', '^', '$', '+', '?', '{', '}', '(', ')':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString(".*")
	return strings.ReplaceAll(b.String(), "1-A", "1A")
}

// regexSets reads a sqlwild_to_regex result, ^<body>.*, under Postgres's
// ARE rules into one byte set per body token. Every token consumes one
// character: a literal, an escaped literal \X, '.' (any character; ARE is
// not newline-sensitive by default, so it accepts '\n'), or a bracket
// expression.
func regexSets(re string) ([]byteSet, error) {
	body := strings.TrimSuffix(strings.TrimPrefix(re, "^"), ".*")
	sets := make([]byteSet, 0, len(body))
	for i := 0; i < len(body); {
		var s byteSet
		switch c := body[i]; c {
		case '\\':
			if i+1 == len(body) || isAlnum(body[i+1]) {
				return nil, errKeyUnsupported // sqlwild_to_regex escapes punctuation only
			}
			s.add(body[i+1])
			i += 2
		case '.':
			s = anyByte
			i++
		case '[':
			var err error
			if s, i, err = bracket(body, i+1); err != nil {
				return nil, err
			}
		default:
			// sqlwild_to_regex escapes every other metacharacter but '*',
			// which it rewrites, so the rest are literals, ']' included.
			s.add(c)
			i++
		}
		sets = append(sets, s)
	}
	return sets, nil
}

// bracket reads the bracket expression whose content starts at body[i], as
// Postgres's regc_lex/regcomp do, and returns its set and the index after
// its ']'. sqlwild_to_regex escapes '^', so it is never negated. A ']' first
// is a literal. '-' is a literal when first or just before the closing ']',
// and a range operator anywhere else; a range is start-end with start <= end,
// and an operator where an element should start or end is an error. '\X' is
// the literal X, and '[' is a literal unless it opens [. [= or [:.
func bracket(body string, i int) (byteSet, int, error) {
	var s byteSet
	first := true
	// elem reads one element at body[i]: its byte, whether it is the range
	// operator, and the index after it.
	elem := func(i int) (c byte, op bool, next int, err error) {
		c = body[i]
		switch {
		case c == '\\':
			if i+1 == len(body) {
				return 0, false, 0, errKeyBrackets
			}
			if isAlnum(body[i+1]) {
				return 0, false, 0, errKeyUnsupported
			}
			return body[i+1], false, i + 2, nil
		case c == '[' && i+1 < len(body) && strings.IndexByte(".=:", body[i+1]) >= 0:
			return 0, false, 0, errKeyUnsupported
		case c == '-':
			return c, !first && i+1 < len(body) && body[i+1] != ']', i + 1, nil
		}
		return c, false, i + 1, nil
	}
	for {
		if i == len(body) {
			return s, 0, errKeyBrackets
		}
		if body[i] == ']' && !first {
			return s, i + 1, nil
		}
		lo, op, next, err := elem(i)
		if err != nil {
			return s, 0, err
		}
		if op {
			return s, 0, errKeyRange
		}
		first = false
		i = next
		hi := lo
		if i+1 < len(body) && body[i] == '-' && body[i+1] != ']' {
			if hi, op, next, err = elem(i + 1); err != nil {
				return s, 0, err
			}
			if op || hi < lo {
				return s, 0, errKeyRange
			}
			i = next
		}
		for c := int(lo); c <= int(hi); c++ {
			s.add(byte(c))
		}
	}
}

func isAlnum(c byte) bool {
	return '0' <= c && c <= '9' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z'
}

// byteSet holds one bit per byte value.
type byteSet [4]uint64

var anyByte = byteSet{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)}

func (s *byteSet) add(c byte)      { s[c>>6] |= 1 << (c & 63) }
func (s *byteSet) remove(c byte)   { s[c>>6] &^= 1 << (c & 63) }
func (s *byteSet) has(c byte) bool { return s[c>>6]&(1<<(c&63)) != 0 }

func (s *byteSet) union(t *byteSet) {
	for i := range s {
		s[i] |= t[i]
	}
}

// formulaKeys is the formula pass's formulaKeys (spvindecode_core.sql:
// 151-161): var_keys with every digit replaced by '#'. A formula pattern
// matches when compileLike(keys) accepts it.
func formulaKeys(varKeys string) string {
	return strings.Map(func(r rune) rune {
		if '0' <= r && r <= '9' {
			return '#'
		}
		return r
	}, varKeys)
}
