package decoder

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzPrimitives holds sanitize to what every later stage relies on: one
// uppercase ASCII byte per character Postgres would count, so byte offsets
// are the SQL's 1-based positions minus one, and no primitive can index out
// of range on any input, invalid UTF-8 included (Postgres never sees that).
func FuzzPrimitives(f *testing.F) {
	for _, s := range oracleSeeds {
		f.Add(s)
	}
	f.Add("")
	f.Add(" ı1hgcm82633a00435ſ\xff ")
	f.Add("1M9AC2BC0RB026001X")
	f.Fuzz(func(t *testing.T, in string) {
		vin := sanitize(in)
		if want := utf8.RuneCountInString(strings.Trim(in, " ")); len(vin) != want {
			t.Fatalf("sanitize(%q) = %q: %d bytes for %d characters", in, vin, len(vin), want)
		}
		for i := range len(vin) {
			if c := vin[i]; c >= utf8.RuneSelf || 'a' <= c && c <= 'z' {
				t.Fatalf("sanitize(%q) = %q: byte %d is %q", in, vin, i, c)
			}
		}
		if vin != "" && (vin[0] == ' ' || vin[len(vin)-1] == ' ') {
			t.Fatalf("sanitize(%q) = %q: not trimmed", in, vin)
		}
		wmi(vin)
		descriptor(vin)
		varKeys(vin)
		checkDigit1(vin)
		checkDigit2(vin, true)
		modelYearRaw(vin, true, 2026)
		start, _ := scanStart(vin, true)
		for j := range len(vin) {
			invalidAt(j+1, vin[j], start)
		}
	})
}
