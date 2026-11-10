package vocab

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// The scanner reads a line in place, without a map or reflection, after
// json.Valid has passed it: every function here trusts its input to be
// valid JSON, trimmed of white space, and reads values as encoding/json
// would, falling back to it for any string with an escape or invalid
// UTF-8.

// skipSpace is the index of the first byte of b at or after i that is not
// JSON white space.
func skipSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// trimSpace is b without leading and trailing JSON white space.
func trimSpace(b []byte) []byte {
	b = b[skipSpace(b, 0):]
	for len(b) > 0 {
		switch b[len(b)-1] {
		case ' ', '\t', '\n', '\r':
			b = b[:len(b)-1]
		default:
			return b
		}
	}
	return b
}

// stringEnd is the index just past the string that starts at b[i], a '"'.
func stringEnd(b []byte, i int) int {
	for i++; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '"':
			return i + 1
		}
	}
	return i
}

// valueEnd is the index just past the value that starts at b[i].
func valueEnd(b []byte, i int) int {
	switch b[i] {
	case '"':
		return stringEnd(b, i)
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '"':
				i = stringEnd(b, i)
				continue
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1
				}
			}
			i++
		}
		return i
	}
	for i < len(b) { // a number, true, false or null
		switch b[i] {
		case ',', '}', ']', ' ', '\t', '\n', '\r':
			return i
		}
		i++
	}
	return i
}

// eachMember calls fn with each member of raw in order: its key, a JSON
// string with its quotes, and its value. It reports whether raw is an
// object.
func eachMember(raw []byte, fn func(key, value []byte)) bool {
	if len(raw) == 0 || raw[0] != '{' {
		return false
	}
	i := skipSpace(raw, 1)
	if raw[i] == '}' {
		return true
	}
	for {
		end := stringEnd(raw, i)
		key := raw[i:end]
		i = skipSpace(raw, skipSpace(raw, end)+1) // past the ':'
		end = valueEnd(raw, i)
		fn(key, raw[i:end])
		if i = skipSpace(raw, end); raw[i] == '}' {
			return true
		}
		i = skipSpace(raw, i+1) // past the ','
	}
}

// members reads raw as an object whose members are named among names, as
// encoding/json reads one into a map: vals[i] is the value of names[i],
// nil when absent and the last one when the key repeats; unknown is the
// least of the other keys, and has whether there is one. ok reports
// whether raw is an object.
func members(raw []byte, names []string, vals [][]byte) (unknown string, has, ok bool) {
	clear(vals)
	ok = eachMember(raw, func(key, value []byte) {
		k, plain := plainString(key)
		if !plain {
			s, _ := str(key)
			k = []byte(s)
		}
		for i, name := range names {
			if string(k) == name {
				vals[i] = value
				return
			}
		}
		if !has || string(k) < unknown {
			unknown, has = string(k), true
		}
	})
	return unknown, has, ok
}

// items appends raw's elements to buf. ok reports whether raw is an array.
func items(raw []byte, buf [][]byte) (_ [][]byte, ok bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return buf, false
	}
	i := skipSpace(raw, 1)
	if raw[i] == ']' {
		return buf, true
	}
	for {
		end := valueEnd(raw, i)
		buf = append(buf, raw[i:end])
		if i = skipSpace(raw, end); raw[i] == ']' {
			return buf, true
		}
		i = skipSpace(raw, i+1)
	}
}

// plainString is the text of raw, a JSON string, when it reads as it is
// written: no escape, valid UTF-8. ok is false otherwise, and for any
// other value.
func plainString(raw []byte) (_ []byte, ok bool) {
	if len(raw) < 2 || raw[0] != '"' {
		return nil, false
	}
	s := raw[1 : len(raw)-1]
	if bytes.IndexByte(s, '\\') >= 0 || !utf8.Valid(s) {
		return nil, false
	}
	return s, true
}

// str reads raw as a JSON string.
func str(raw []byte) (string, bool) {
	if s, ok := plainString(raw); ok {
		return string(s), true
	}
	var s string
	if isNull(raw) || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// texts hands out strings copied into shared blocks, one allocation per
// block rather than one per string. A block lives as long as any string
// in it.
type texts struct {
	b       strings.Builder
	scratch []byte
}

// textBlock is the size of a block of texts.
const textBlock = 64 << 10

// of is p as a string.
func (t *texts) of(p []byte) string {
	if t.b.Cap()-t.b.Len() < len(p) {
		t.b = strings.Builder{}
		t.b.Grow(max(len(p), textBlock))
	}
	start := t.b.Len()
	t.b.Write(p)
	return t.b.String()[start:]
}

// normalized is Normalize(s), s itself when Normalize leaves it as it is.
func (t *texts) normalized(s string) string {
	if normalASCII(s) {
		return s
	}
	t.scratch = appendNormalized(t.scratch[:0], s)
	return t.of(t.scratch)
}
