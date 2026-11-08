package decoder

import (
	"strings"
	"unicode/utf8"
)

// sanitize is the wrapper's first step, vin = upper(trim(v))
// (spvindecode.sql:36), made byte-indexable.
//
// Postgres trim removes spaces only, so a tab or newline stays and is later
// an invalid character; upper under the dump's en_US.UTF-8 locale maps
// exactly two non-ASCII runes into ASCII, ı to I and ſ to S. Every other
// non-ASCII rune becomes one '&', which no VIN character class, LIKE literal
// or year letter accepts, so positions stay character positions (as in
// Postgres) and every later stage can index bytes. This is ultravin's
// substitution (uv:crates/ultravin/src/lib.rs:989); the text the oracle
// would echo back (descriptor, suggested VIN) shows '&' where it shows the
// original rune.
func sanitize(input string) string {
	s := strings.Trim(input, " ")
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return sanitizeRunes(s)
		}
	}
	return strings.ToUpper(s)
}

func sanitizeRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case 'a' <= r && r <= 'z':
			b.WriteByte(byte(r - 'a' + 'A'))
		case r < utf8.RuneSelf:
			b.WriteByte(byte(r))
		case r == 'ı':
			b.WriteByte('I')
		case r == 'ſ':
			b.WriteByte('S')
		default:
			b.WriteByte('&')
		}
	}
	return b.String()
}

// wmi ports fVinWMI (fvinwmi.sql): positions 1-3, followed by positions
// 12-14 when position 3 is '9' and the VIN has at least 14 characters.
func wmi(vin string) string {
	if len(vin) < 14 || vin[2] != '9' {
		return vin[:min(len(vin), 3)]
	}
	return vin[:3] + vin[11:14]
}

// descriptor ports fVinDescriptor (fvindescriptor.sql) on a sanitized VIN:
// pad to 17 with '*', mask position 9, keep 11 characters, or 14 when
// position 3 is '9'.
func descriptor(vin string) string {
	var d [14]byte
	n := 11
	if len(vin) >= 3 && vin[2] == '9' {
		n = 14
	}
	for i := range n {
		switch {
		case i == 8, i >= len(vin):
			d[i] = '*'
		default:
			d[i] = vin[i]
		}
	}
	return string(d[:n])
}

// varKeys builds the string every pattern key is matched against
// (spvindecode_core.sql:56-61): positions 4-8, then '|' and positions 10-17
// when the VIN is longer than 9; empty for 3 characters or fewer.
func varKeys(vin string) string {
	if len(vin) <= 3 {
		return ""
	}
	keys := vin[3:min(len(vin), 8)]
	if len(vin) <= 9 {
		return keys
	}
	return keys + "|" + vin[9:min(len(vin), 17)]
}
