package decoder

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// The notes spvindecode_core appends to an error code's name in element 191
// (spvindecode_core.sql:48-49).
const (
	offRoadNote        = " NOTE: Disregard if this is an off-road vehicle PIN, as check digit calculation may not be accurate."
	checkDigitExcepted = " NOTE: Check Digit Exception - The check digit was given an exception based on data from the OEM indicating an error on production."
)

// errVarchar500 is Postgres's error storing spvindecode_errorcode's
// possible values in the core's varchar(500) ErrorBytes.
var errVarchar500 = errors.New("value too long for type character varying(500)")

// pass ports one spvindecode_core call: core's items, then the rest of the
// procedure, in its order:
//
//   - C12 (:381-389): code 8 when no item carries a PatternId, else
//     errorCode's codes, suggested VIN and possible values; code 7 (no
//     available WMI, core's nil items) runs neither.
//   - C13 (:396-409): code 9 for a Glider body class, 10 for an off-road
//     one, 11 for a NULL year; the Vehicle Type item's attribute.
//   - C14, C15 (:411-456): the invalid-character scan, skipping position 9
//     for an off-road VIN or a VinException check-digit exemption; each
//     invalid character stamps '!' into the suggested VIN (the input when
//     there is none yet) and is listed; then code 400, and code 12 for
//     error12.
//   - C16 (:458-476): the defaults.
//   - C17 (:478-486): code 6 under 17 characters, else code 1 when position
//     9 is not fVINCheckDigit2's, unless exempt.
//   - C18 (:488-502): code 0 when the codes other than 9, 10 and 12 are
//     none or only 14, then code 14 when no item is a Model.
//   - C19 (:504-523): AdditionalDecodingInfo (element 156).
//   - C20 (:525-559): the Corrections items: 142 suggested VIN, 143 codes,
//     191 their names, 144 possible values, 156, and 196 the descriptor.
//
// v is the wrapper's vin, modelYear 0 for NULL; modelYearSource,
// conclusive, error12 and now are the procedure's arguments (see core). It
// returns the pass's items and its return code, every code between spaces
// (' 0  14 '), which the wrapper reads with LIKE '% n %'. An error is
// core's, errorCode's, or a value the procedure's varchar(n) variables
// cannot hold.
func (e *engine) pass(v vinText, modelYear int, modelYearSource string, conclusive, error12 bool, now int64) ([]item, string, error) {
	vin := v.s
	items, err := e.core(vin, modelYear, modelYearSource, now)
	if err != nil {
		return nil, "", err
	}
	wlo, whi := e.wmi.wmi.EqualRange(0, len(e.wmi.id), wmi(vin))

	// C12.
	var rc, corrected, errorBytes string
	var unused sqlText
	switch {
	case items == nil:
		rc = " 7 "
	case !hasPatternID(items):
		rc = " 8 "
	default:
		res, err := e.errorCode(v, modelYear, items, wlo, whi)
		if err != nil {
			return nil, "", err
		}
		if utf8.RuneCountInString(res.errorBytes) > 500 {
			return nil, "", errVarchar500
		}
		rc, corrected, errorBytes, unused = res.returnCode, res.correctedVIN, res.errorBytes, res.unusedPositions
	}

	// C13.
	if hasAttribute(items, 5, "64") {
		rc += " 9 "
	}
	offRoad := hasAttribute(items, 5, "69", "84", "86", "88", "97", "105", "113", "124", "126", "127")
	if offRoad {
		rc += " 10 "
	}
	if modelYear == 0 {
		rc += " 11 "
	}
	var vehicleType sqlText
	for i := range items {
		if items[i].elementID == 39 {
			vehicleType = items[i].attributeID
			break
		}
	}

	// C14, C15. The wmi row is the first with the WMI, available or not.
	exempt := e.checkDigitExempt(string(v.echo))
	carMpvLT := e.carMpvLT(wlo, whi)
	startPos, checkDigitCarMpvLT := scanStart(vin, carMpvLT)
	suggested := []rune(corrected)
	var invalid strings.Builder
	for j := 1; j <= len(vin); j++ {
		if j == 9 && (offRoad || exempt) || !invalidAt(j, vin[j-1], startPos) {
			continue
		}
		if len(suggested) == 0 {
			suggested = append(suggested, v.echo...)
		}
		invalid.WriteString(", " + strconv.Itoa(j) + ":" + string(v.echo[j-1]))
		suggested = append(append(append([]rune(nil), suggested[:min(j-1, len(suggested))]...), '!'),
			substr(suggested, j+1, 100)...)
	}
	if invalid.Len() > 0 {
		rc += " 400 "
	}
	if error12 {
		rc += " 12 "
	}

	// C16.
	if items, err = e.defaultItems(items); err != nil {
		return nil, "", err
	}

	// C17.
	if len(vin) < 17 {
		rc += " 6 "
	} else if vin[8] != checkDigit2(vin, checkDigitCarMpvLT) && !exempt {
		rc += " 1 "
	}

	// C18.
	rest := rc
	for _, c := range []string{" 9 ", " 10 ", " 12 "} {
		rest = strings.ReplaceAll(rest, c, "")
	}
	if rest = trimSpaces(rest); rest == "" || rest == "14" {
		rc = " 0 " + rc
	}
	if hasCode(rc, 0) && !hasElement(items, 28) {
		rc += " 14 "
	}

	// C19.
	var info sqlText
	add := func(s sqlText) {
		if !s.valid {
			info = sqlText{}
			return
		}
		info = text(leftChars(trimSpaces(info.s+s.s), 500))
	}
	if hasCode(rc, 4) {
		info = e.additionalErrorText(4)
	}
	if hasCode(rc, 5) {
		info = e.additionalErrorText(5)
	}
	if hasCode(rc, 14) {
		add(sqlText{" Unused position(s): " + unused.s + ". ", unused.valid})
	}
	if hasCode(rc, 400) {
		add(text(" Invalid character(s): " + invalid.String()[2:] + ". "))
	}
	if vehicleType == text("10") || hasAttribute(items, 5,
		"65", "107", "70", "74", "63", "72", "112", "62", "64", "76", "78", "71", "77", "67", "116", "75") {
		add(text(" Incomplete Vehicle Warning - Please be advised that the vehicle may have been altered and may not be an accurate representation of the vehicle in its current condition. "))
	}
	if !conclusive {
		add(text(" The Model Year decoded for this VIN may be incorrect. If you know the Model year, please enter it and decode again to get more accurate information. "))
	}

	// C20.
	var names, ids []string
	ec := &e.errorCodes
	for r, id := range ec.id {
		if !hasCode(rc, id) {
			continue
		}
		name, _ := ec.name.At(r)
		switch {
		case offRoad && id == 1:
			name += offRoadNote
		case exempt && id == 0:
			name += checkDigitExcepted
		}
		names = append(names, trimSpaces(name))
		ids = append(ids, itoa(id))
	}
	var messages, codes sqlText
	if len(ids) > 0 {
		messages = text(leftChars(strings.Join(names, "; "), 500))
		codes = text(strings.Join(ids, ","))
	}
	for _, c := range []struct {
		element int32
		value   sqlText
	}{
		{142, text(string(suggested))},
		{143, codes},
		{191, messages},
		{144, text(errorBytes)},
		{156, info},
		{196, text(descriptor(v.echo))},
	} {
		items = append(items, item{
			createdOn: artifact.NullInt64, patternID: artifact.NullInt32, keys: text(""),
			vinSchemaID: artifact.NullInt32, wmiID: artifact.NullInt32, elementID: c.element,
			attributeID: c.value, value: c.value,
			source: "Corrections", priority: 999, toBeQCed: artifact.NullBool,
		})
	}
	return items, rc, nil
}

// carMpvLT is isCarMpvLT of the first wmi row in [wlo, whi), available or
// not, false when there is none: what the invalid-character scan and C17's
// check digit read.
func (e *engine) carMpvLT(wlo, whi int) bool {
	return wlo < whi && isCarMpvLT(int(e.wmi.vehicleTypeID[wlo]), int(e.wmi.truckTypeID[wlo]))
}

// errorValue ports fErrorValue (ferrorvalue.sql), the wrapper's score of a
// pass by its element-143 codes: the sum of ErrorCode.Weight over the ids
// in the comma-separated list, a NULL weight adding nothing; 0 for none or
// a NULL list.
func (e *engine) errorValue(codes sqlText) int {
	if !codes.valid {
		return 0
	}
	list := "," + codes.s + ","
	sum := 0
	for r, id := range e.errorCodes.id {
		if w := e.errorCodes.weight[r]; w != artifact.NullInt32 && strings.Contains(list, ","+itoa(id)+",") {
			sum += int(w)
		}
	}
	return sum
}

// additionalErrorText reads ErrorCode id's AdditionalErrorText as
// spvindecode_core selects it into a variable: coalesced to the empty
// string, and NULL when there is no such row.
func (e *engine) additionalErrorText(id int32) sqlText {
	i, j := artifact.EqualRange(e.errorCodes.id, 0, len(e.errorCodes.id), id)
	if i == j {
		return sqlText{}
	}
	s, _ := e.errorCodes.additionalText.At(i)
	return text(s)
}

// checkDigitExempt is `exists(select 1 from VinException where vin =
// var_vin and CheckDigit = true)`, on the VIN Postgres holds.
func (e *engine) checkDigitExempt(vin string) bool {
	x := &e.vinException
	lo, hi := x.vin.EqualRange(0, x.vin.Len(), vin)
	for r := lo; r < hi; r++ {
		if cd, ok := x.checkDigit.At(r); ok && cd {
			return true
		}
	}
	return false
}

// hasCode is `ReturnCode LIKE '% <id> %'`.
func hasCode(rc string, id int32) bool {
	return strings.Contains(rc, " "+itoa(id)+" ")
}

func hasPatternID(items []item) bool {
	for i := range items {
		if items[i].patternID != artifact.NullInt32 {
			return true
		}
	}
	return false
}

func hasElement(items []item, element int32) bool {
	for i := range items {
		if items[i].elementID == element {
			return true
		}
	}
	return false
}

// hasAttribute reports an item of element whose AttributeId is one of ids.
func hasAttribute(items []item, element int32, ids ...string) bool {
	for i := range items {
		if it := &items[i]; it.elementID == element && it.attributeID.valid {
			for _, id := range ids {
				if it.attributeID.s == id {
					return true
				}
			}
		}
	}
	return false
}
