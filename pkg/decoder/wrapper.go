package decoder

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// errVarchar17 is Postgres's error storing upper(trim(v)) in spvindecode's
// vin varchar(17) (:8, :37) when it is longer than 17 characters.
var errVarchar17 = errors.New("value too long for type character varying(17)")

// yearHint is spvindecode's year argument, the caller's model year: an
// int4, NULL when valid is false. Any integer is a hint: one outside 1980
// to v_limit runs no pass of its own but still sets code 12 on the passes
// whose year it is not.
type yearHint struct {
	year  int
	valid bool
}

// yearPlan is the procedure's model-year candidates (W5, :55-90): rmy, the
// year position 10 gives, after the altMY swap; omy, 30 years earlier when
// the year is inconclusive. 0 is NULL.
type yearPlan struct {
	rmy, omy   int
	conclusive bool
}

// passScore is one row of the procedure's table x (:146-187), compared
// under ORDER BY ErrorValue DESC, ElementsWeight DESC, Patterns DESC,
// ModelYear DESC, where Postgres puts NULL first. ErrorValue is never NULL:
// every pass has its element-143 item and fErrorValue coalesces to 0. With
// 2026_10's weights no NULL decides between passes: Patterns is NULL only
// for a code-8 pass, which ErrorValue's −1000 already ranks, and
// ElementsWeight and ModelYear only when every pass is code 7 or there is
// one pass. The order is kept for a month where that changes.
type passScore struct {
	errorValue                          int
	elementsWeight, patterns, modelYear sqlInt
}

// sqlInt is an integer that can be NULL; the zero value is NULL.
type sqlInt struct {
	n     int
	valid bool
}

// decode ports vpic.spvindecode (spvindecode.sql) up to value resolution:
// the winning pass's items after the QC filter, values still 'XXX' where
// the procedure resolves them. pass is the winner's DecodingId, 2, 3 or 4.
//
//   - W0 (:37): vin = upper(trim(v)) into a varchar(17); more than 17
//     characters after trimming raises there, so decode returns
//     errVarchar17 and runs nothing (ADR 0006: where Postgres raises, the
//     decoder returns a decode error).
//   - W4 (:36-53): the descriptor pass never runs. fVinDescriptor reads vin
//     before :37 assigns it, so the VinDescriptor lookup is for
//     '***********', which no row has; the artifact does not carry the
//     table.
//   - W5 (:55-90): planYears.
//   - W6 (:92-111): a hint from 1980 to v_limit that is neither rmy nor omy
//     runs pass 2 at the hint, conclusive and with code 12, and becomes
//     every later pass's modelYearSource; passes 3 and 4 then run only if
//     pass 2 returned code 8 and rmy is not NULL.
//   - W7 (:113-141): pass 3 at rmy, pass 4 at omy when it is not NULL, each
//     with code 12 when there is a hint and it is not the pass's year.
//   - W8 (:144-191): the pass scored first by passScore wins. Postgres
//     leaves a full tie to the executor; the lowest pass wins here, as in
//     ultravin. A full tie needs equal model years, so two passes with no
//     element-29 item: both code 7, which runs no year-dependent stage,
//     and with equal codes, so their items are the same.
//   - W9 (:193-202): the QC filter. Its UPDATE joins DecItem to a second
//     DecItem with no condition between them, so one item of a QC'd
//     VinSchema with a pattern, formula, engine model or conversion source
//     marks every item, and the DELETE leaves none.
//
// v_limit, the last year a hint or an altMY may be, is two past the year of
// now in UTC (CURRENT_DATE under the oracle's TimeZone); now is as core's.
// An error is a pass's, or a model-year value that is not an integer.
func (e *engine) decode(input string, hint yearHint, now int64) (items []item, pass int, err error) {
	if utf8.RuneCountInString(trimSpaces(input)) > 17 {
		return nil, 0, errVarchar17
	}
	v := newVINText(input)
	nowYear := time.UnixMicro(now).UTC().Year()
	vLimit := nowYear + 2
	plan := e.planYears(v.s, nowYear)

	var best []item
	var bestScore passScore
	run := func(id, modelYear int, source string, conclusive, error12 bool) (string, error) {
		items, rc, err := e.pass(v, modelYear, source, conclusive, error12, now)
		if err != nil {
			return "", err
		}
		s, err := e.score(items, hint)
		if err != nil {
			return "", err
		}
		if pass == 0 || s.before(bestScore) {
			best, bestScore, pass = items, s, id
		}
		return rc, nil
	}

	source, do3and4 := "***X*|Y", true
	if hint.valid && 1980 <= hint.year && hint.year <= vLimit && hint.year != plan.rmy && hint.year != plan.omy {
		source = strconv.Itoa(hint.year)
		rc, err := run(2, hint.year, source, true, true)
		if err != nil {
			return nil, 0, err
		}
		do3and4 = hasCode(rc, 8) && plan.rmy != 0
	}
	if do3and4 {
		if _, err := run(3, plan.rmy, source, plan.conclusive, hint.valid && plan.rmy != 0 && hint.year != plan.rmy); err != nil {
			return nil, 0, err
		}
		if plan.omy != 0 {
			if _, err := run(4, plan.omy, source, plan.conclusive, hint.valid && hint.year != plan.omy); err != nil {
				return nil, 0, err
			}
		}
	}
	return e.qcFilter(best), pass, nil
}

// planYears is W5 (:55-90) on the sanitized vin: rmy is fVinModelYear2's
// year, its absolute value when inconclusive, with omy 30 years earlier. A
// conclusive rmy from 1980 to v_limit-30 has the alternative rmy+30, one
// from 2010 to v_limit rmy-30; the alternative replaces rmy when no year
// band of any wmi row with the WMI covers rmy and one covers it. A NULL rmy
// is conclusive.
func (e *engine) planYears(vin string, nowYear int) yearPlan {
	wlo, whi := e.wmi.wmi.EqualRange(0, len(e.wmi.id), wmi(vin))
	// fVinModelYear2 reads the first wmi row with the WMI, available or not.
	carLT := wlo < whi && isCarMpvLT(int(e.wmi.vehicleTypeID[wlo]), int(e.wmi.truckTypeID[wlo]))
	y, ok := modelYearRaw(vin, carLT, nowYear)
	plan := yearPlan{rmy: y, conclusive: true}
	switch {
	case !ok:
		plan.rmy = 0
	case y < 0:
		plan = yearPlan{rmy: -y, omy: -y - 30}
	}
	if !plan.conclusive || plan.rmy == 0 {
		return plan
	}
	vLimit := nowYear + 2
	alt := 0
	switch rmy := plan.rmy; {
	case 1980 <= rmy && rmy <= vLimit-30:
		alt = rmy + 30
	case 2010 <= rmy && rmy <= vLimit:
		alt = rmy - 30
	}
	if alt != 0 && !e.wmiHasBand(wlo, whi, plan.rmy) && e.wmiHasBand(wlo, whi, alt) {
		plan.rmy = alt
	}
	return plan
}

// wmiHasBand is the altMY count (:74-84) above zero: some Wmi_VinSchema row
// of a wmi row in [wlo, whi), joined to its VinSchema, covers year.
func (e *engine) wmiHasBand(wlo, whi, year int) bool {
	wvs := &e.wmiVinSchema
	for r := wlo; r < whi; r++ {
		lo, hi := artifact.EqualRange(wvs.wmiID, 0, len(wvs.wmiID), e.wmi.id[r])
		for b := lo; b < hi; b++ {
			if !e.inBand(b, year) {
				continue
			}
			if i, j := artifact.EqualRange(e.vinSchema.id, 0, len(e.vinSchema.id), wvs.vinSchemaID[b]); i < j {
				return true
			}
		}
	}
	return false
}

// score is a pass's row of x (:152-187):
//
//   - ErrorValue: fErrorValue of the element-143 value;
//   - ElementsWeight: the sum of Element.Weight over the distinct elements
//     with a non-empty value and a weight, NULL for none;
//   - Patterns: the items from Pattern, EngineModelPattern or Formula
//     Pattern whose value is neither empty nor 'Not Applicable', NULL for
//     none;
//   - ModelYear: the element-29 value cast to integer, plus 10000 when it
//     equals the hint; NULL with no element-29 item. Each element-29 item
//     is a row of x, so a pass's best one competes.
//
// A model-year value that is not an integer raises, as the cast does.
func (e *engine) score(items []item, hint yearHint) (passScore, error) {
	var s passScore
	seen := make([]bool, len(e.element.row))
	years := 0
	for i := range items {
		it := &items[i]
		switch it.elementID {
		case 143:
			s.errorValue = e.errorValue(it.value)
		case 29:
			y, err := sqlInt4(it.value)
			if err != nil {
				return s, err
			}
			my := sqlInt{}
			if y != artifact.NullInt32 {
				my = sqlInt{int(y), true}
				if hint.valid && hint.year == int(y) {
					my.n += 10000
				}
			}
			if years++; years == 1 || descNullsFirst(my, s.modelYear) < 0 {
				s.modelYear = my
			}
		}
		if it.value.s == "" {
			continue // NULL or empty
		}
		if e.elementExists(it.elementID) && !seen[it.elementID] {
			seen[it.elementID] = true
			if w := e.element.weight[e.element.row[it.elementID]]; w != artifact.NullInt32 {
				s.elementsWeight = sqlInt{s.elementsWeight.n + int(w), true}
			}
		}
		switch it.source {
		case "Pattern", "EngineModelPattern", "Formula Pattern":
			if it.value.s != "Not Applicable" {
				s.patterns = sqlInt{s.patterns.n + 1, true}
			}
		}
	}
	return s, nil
}

// before reports whether s sorts before t in x's ORDER BY.
func (s passScore) before(t passScore) bool {
	if s.errorValue != t.errorValue {
		return s.errorValue > t.errorValue
	}
	for _, c := range [...]int{
		descNullsFirst(s.elementsWeight, t.elementsWeight),
		descNullsFirst(s.patterns, t.patterns),
		descNullsFirst(s.modelYear, t.modelYear),
	} {
		if c != 0 {
			return c < 0
		}
	}
	return false
}

// descNullsFirst orders a and b as ORDER BY … DESC does in Postgres: NULL
// first, then larger values.
func descNullsFirst(a, b sqlInt) int {
	switch {
	case a.valid != b.valid:
		if !a.valid {
			return -1
		}
		return 1
	case a.n > b.n:
		return -1
	case a.n < b.n:
		return 1
	}
	return 0
}

// qcFilter is W9 (:193-202) on the winning pass's items: none survive when
// one whose source starts, lower-cased, with 'pattern', 'formula',
// 'enginem' or 'convers' has a VinSchema with TobeQCed true; otherwise
// those already marked TobeQCed are dropped.
func (e *engine) qcFilter(items []item) []item {
	for i := range items {
		switch strings.ToLower(leftChars(items[i].source, 7)) {
		case "pattern", "formula", "enginem", "convers":
		default:
			continue
		}
		// A NULL VinSchemaId (artifact.NullInt32) joins no VinSchema row.
		vs, vsEnd := artifact.EqualRange(e.vinSchema.id, 0, len(e.vinSchema.id), items[i].vinSchemaID)
		if vs == vsEnd {
			continue
		}
		if qc, _ := e.vinSchema.toBeQCed.At(vs); qc {
			return items[:0]
		}
	}
	kept := items[:0]
	for _, it := range items {
		if it.toBeQCed != 1 {
			kept = append(kept, it)
		}
	}
	return kept
}
