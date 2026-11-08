package decoder

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// errorCodeResult is spvindecode_errorcode's four OUT parameters.
type errorCodeResult struct {
	returnCode      string  // ' 2 ', ' 5  14 ', …: each code between spaces
	correctedVIN    string  // the suggested VIN, '' unless code 2-5
	errorBytes      string  // the possible values, '' unless code 2-5
	unusedPositions sqlText // NULL unless code 14
}

// errVarchar50 is Postgres's error assigning more than 50 characters to
// the procedure's varchar(50) lastReplacements: more than 50 valid
// characters at one position, which the dump's key alphabet cannot reach.
var errVarchar50 = errors.New("value too long for type character varying(50)")

// errorCode ports spvindecode_errorcode (spvindecode_errorcode.sql), the
// correction search spvindecode_core runs when some item has a PatternId.
// items are the pass's items at that point: core's, before the defaults.
// wlo and whi are the wmi rows with the WMI.
//
//   - E0 (:37-40): a WMI shorter than 3 is code 6 and nothing else.
//   - E1 (:47-67): the (WMI, year) charset, always charset's recompute
//     (ADR 0006).
//   - E2 (:73-105): positions 4 to 14 (11 for a 6-character WMI) and the
//     VIN's length, but 9 and 10, are checked against the charset when it
//     has a row there: an absent character becomes '!' and appends
//     (position:valid characters) to the replacements.
//   - E3 (:107-115): the corrected VIN is the WMI around those positions,
//     then up to 3 more input characters.
//   - E4, E5 (:117-152): one error with one candidate is code 2 with it
//     substituted; with several, code 3 when exactly one makes position 9
//     equal fVINCheckDigit, else code 4; more errors are code 5.
//   - E6 (:164-200): positions 4-8 and 11, up to the VIN's length, whose
//     character no pattern-sourced item's key allows there are code 14.
//
// Characters keep the procedure's char(1) types: a space read into a
// varchar is dropped, so a space at a position E2 keeps shortens the
// corrected VIN, and none is ever listed as a valid character.
func (e *engine) errorCode(v vinText, modelYear int, items []item, wlo, whi int) (errorCodeResult, error) {
	var res errorCodeResult
	vin := v.s
	varWMI := wmi(vin)
	if len(varWMI) < 3 {
		res.returnCode = " 6 "
		return res, nil
	}
	cell, err := e.charset(wlo, whi, modelYear)
	if err != nil {
		return res, err
	}

	n := 14
	if len(varWMI) == 6 {
		n = 11
	}
	var corrected []rune
	var replacements strings.Builder
	errorCount, lastErrorPos, lastReplacements := 0, 0, ""
	for i := 4; i <= min(n, len(vin)); i++ {
		c := vin[i-1]
		var set *byteSet
		if i != 9 && i != 10 && i < len(cell) && cell[i] != (byteSet{}) {
			set = &cell[i]
		}
		if set == nil || set.has(c) {
			corrected = appendChar(corrected, v.echo[i-1])
			continue
		}
		x := renderChars(set)
		if len(x) > 50 {
			return res, errVarchar50
		}
		corrected = append(corrected, '!')
		replacements.WriteString("(" + strconv.Itoa(i) + ":" + x + ")")
		errorCount++
		lastErrorPos, lastReplacements = i, x
	}

	// E3.
	head := append([]rune(nil), v.echo[:3]...)
	if len(varWMI) == 6 {
		corrected = append(append(head, corrected...), v.echo[11:14]...)
	} else {
		corrected = append(head, corrected...)
	}
	if len(v.echo) > len(corrected) {
		corrected = append(corrected, substr(v.echo, len(corrected)+1, 3)...)
	}

	// E4, E5.
	spliced := func(s []rune, c rune) []rune {
		out := append([]rune(nil), substr(s, 1, lastErrorPos-1)...)
		out = append(out, c)
		return append(out, substr(s, lastErrorPos+1, 17-lastErrorPos)...)
	}
	switch {
	case errorCount == 1 && len(lastReplacements) == 1:
		res.returnCode += " 2 "
		res.correctedVIN = string(spliced(v.echo, rune(lastReplacements[0])))
		res.errorBytes = replacements.String()
	case errorCount == 1:
		var good []byte
		var goodVIN []rune
		sanitized := []rune(vin)
		for j := range len(lastReplacements) {
			c := rune(lastReplacements[j])
			// SUBSTRING(tmpVin, 9, 1) = fVINCheckDigit(tmpVin). Under 9
			// characters both sides are '' and every candidate passes,
			// which is code 4 as surely as none passing: there are several.
			if tmp := string(spliced(sanitized, c)); len(tmp) == 17 && tmp[8] == checkDigit1(tmp) {
				good = append(good, byte(c))
				goodVIN = spliced(v.echo, c)
			}
		}
		if len(good) == 1 {
			res.returnCode += " 3 "
			res.correctedVIN = string(goodVIN)
			res.errorBytes = "(" + strconv.Itoa(lastErrorPos) + ":" + string(good) + ")"
		} else {
			res.returnCode += " 4 "
			res.correctedVIN = string(corrected)
			res.errorBytes = "(" + strconv.Itoa(lastErrorPos) + ":" + lastReplacements + ")"
		}
	case errorCount > 1:
		res.returnCode += " 5 "
		res.correctedVIN = string(corrected)
		res.errorBytes = replacements.String()
	}

	// E6.
	var used [12]byteSet // by VIN position, 4 to 11
	for i := range items {
		it := &items[i]
		if !it.keys.valid || it.keys.s == "" || !strings.Contains(strings.ToLower(it.source), "pattern") {
			continue
		}
		sets, err := validCharsInKey(it.keys.s)
		if err != nil {
			return res, err
		}
		for j := range sets {
			if p := j + 4; p < len(used) {
				used[p].union(&sets[j])
			}
		}
	}
	var unused []string
	for i := 4; i <= min(11, len(vin)); i++ {
		if (i <= 8 || i == 11) && !(used[i].has(vin[i-1]) && vin[i-1] != '|') {
			unused = append(unused, strconv.Itoa(i))
		}
	}
	if len(unused) > 0 {
		res.returnCode += " 14 "
		res.unusedPositions = text(strings.Join(unused, ","))
	}
	return res, nil
}

// appendChar is `corrected || r` for r a char(1) read into a varchar,
// which drops a space.
func appendChar(s []rune, c rune) []rune {
	if c == ' ' {
		return s
	}
	return append(s, c)
}

// renderChars is the procedure's list of one position's valid characters:
// '_' first, then by byte, the C collation (spvindecode_errorcode.sql:
// 88-94); a space is dropped as appendChar drops it.
func renderChars(set *byteSet) string {
	var b []byte
	if set.has('_') {
		b = append(b, '_')
	}
	for c := range byte(utf8.RuneSelf) {
		if c != '_' && c != ' ' && set.has(c) {
			b = append(b, c)
		}
	}
	return string(b)
}

// substr is SUBSTRING(s, from, count) on characters, from 1-based; it
// never reads past either end.
func substr(s []rune, from, count int) []rune {
	lo, hi := max(from, 1), from+count
	if hi > len(s)+1 {
		hi = len(s) + 1
	}
	if lo >= hi {
		return nil
	}
	return s[lo-1 : hi-1]
}
