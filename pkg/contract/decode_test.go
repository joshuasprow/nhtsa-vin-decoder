package contract

import (
	"bytes"
	"cmp"
	"encoding/json"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// answerKey is the freshened Postgres oracle's answer key for
// testdata/oracle/sample-vins.tsv: every row spvindecode emitted for 28
// VINs, in the month manifest.json beside it names. The monthly refresh cuts
// it from the frozen parity key (scripts/refresh.sh contract-key), whose
// corpus opens with those VINs. TestAnswerKey runs the contract layer on its
// rows, with no artifact; TestEngineAnswerKey runs it on the engine's
// decodes of the same VINs.
const answerKey = "testdata/oracle/answer-key.csv"

type keyedVIN struct {
	VIN   string
	Hint  Null[int32]
	Items []Item
}

// loadAnswerKey reads the answer key, one entry per input row in order.
func loadAnswerKey(t *testing.T) []keyedVIN {
	t.Helper()
	b, err := os.ReadFile(answerKey)
	if err != nil {
		t.Fatal(err)
	}
	rows := parseCSV(string(b))
	header := "input_row,vin,year_hint,groupname,variable,value,itempatternid,itemvinschemaid,itemkeys,itemelementid,itemattributeid,itemcreatedon,itemwmiid,code,datatype,decode,itemsource,itemtobeqced"
	var got []string
	for _, f := range rows[0] {
		got = append(got, f.s)
	}
	if strings.Join(got, ",") != header {
		t.Fatalf("answer key header %q", strings.Join(got, ","))
	}
	var vins []keyedVIN
	for n, r := range rows[1:] {
		if len(r) != 18 {
			t.Fatalf("answer key row %d has %d fields", n+1, len(r))
		}
		row, _ := strconv.Atoi(r[0].s)
		if row == len(vins)+1 {
			vins = append(vins, keyedVIN{VIN: r[1].s, Hint: r[2].int(t)})
		}
		v := &vins[row-1]
		v.Items = append(v.Items, Item{
			GroupName:   r[3].text(),
			Variable:    r[4].s,
			Value:       r[5].text(),
			PatternID:   r[6].int(t),
			VinSchemaID: r[7].int(t),
			Keys:        r[8].text(),
			ElementID:   r[9].int(t).V,
			AttributeID: r[10].text(),
			CreatedOn:   r[11].text(),
			WmiID:       r[12].int(t),
			Code:        r[13].text(),
			DataType:    r[14].text(),
			Decode:      r[15].text(),
			Source:      r[16].text(),
			ToBeQCed:    r[17].bool(t),
		})
	}
	return vins
}

// csvField is one field of Postgres's CSV, which tells NULL (unquoted and
// empty) from the empty string (quoted).
type csvField struct {
	s      string
	quoted bool
}

func (f csvField) text() Null[string] {
	if f.s == "" && !f.quoted {
		return Null[string]{}
	}
	return Value(f.s)
}

func (f csvField) int(t *testing.T) Null[int32] {
	if f.s == "" {
		return Null[int32]{}
	}
	n, err := strconv.ParseInt(f.s, 10, 32)
	if err != nil {
		t.Fatal(err)
	}
	return Value(int32(n))
}

func (f csvField) bool(t *testing.T) Null[bool] {
	switch f.s {
	case "":
		return Null[bool]{}
	case "t":
		return Value(true)
	case "f":
		return Value(false)
	}
	t.Fatalf("boolean %q", f.s)
	return Null[bool]{}
}

// parseCSV parses RFC 4180 CSV with \n line ends.
func parseCSV(data string) [][]csvField {
	var rows [][]csvField
	var row []csvField
	for i := 0; i < len(data); i++ {
		var f csvField
		if data[i] == '"' {
			f.quoted = true
			var b strings.Builder
			for i++; ; {
				j := strings.IndexByte(data[i:], '"')
				b.WriteString(data[i : i+j])
				i += j + 1
				if i < len(data) && data[i] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				break
			}
			f.s = b.String()
		} else {
			j := strings.IndexAny(data[i:], ",\n")
			if j < 0 {
				j = len(data) - i
			}
			f.s = data[i : i+j]
			i += j
		}
		row = append(row, f)
		if i >= len(data) || data[i] == '\n' {
			rows = append(rows, row)
			row = nil
		}
	}
	return rows
}

// projectionOf stands in for the engine's header fields: the oracle's rows
// do not carry them, and the contract only passes them through.
func projectionOf(v keyedVIN) Projection {
	vin := strings.ToUpper(strings.Trim(v.VIN, " "))
	wmi := vin[:min(3, len(vin))]
	if len(vin) >= 14 && vin[2] == '9' {
		wmi += vin[11:14]
	}
	return Projection{VIN: vin, WMI: wmi, Items: slices.Clone(v.Items)}
}

var testNow = time.Date(2026, 11, 8, 12, 30, 45, 0, time.UTC)

// TestAnswerKey decodes every VIN of the oracle's answer key, from the
// oracle's own rows, in both layouts with and without items, and checks
// every decode against the contract's schema; then that the bytes do not
// depend on the items' order, which the procedure does not keep stable.
func TestAnswerKey(t *testing.T) {
	info := &Info{DecoderVersion: "(devel)", DataMonth: "2026_10", Elements: loadElements(t)}
	vins := loadAnswerKey(t)
	if len(vins) != 28 {
		t.Fatalf("%d VINs in the answer key, want 28", len(vins))
	}
	schema := schemaDef(t, "Decode")
	rng := rand.New(rand.NewPCG(1, 2))
	for i, v := range vins {
		for _, shape := range []Shape{ShapeVPIC, ShapeNitrovin} {
			for _, full := range []bool{false, true} {
				req := Request{VIN: v.VIN, ModelYear: v.Hint, Full: full, Shape: shape, Now: testNow}
				d, err := NewDecode(projectionOf(v), req, info)
				if err != nil {
					t.Fatalf("row %d %s %v: %v", i+1, shape, full, err)
				}
				b, err := json.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
				if err := validate(schema, b); err != nil {
					t.Errorf("row %d (%s) shape %s full %v does not validate: %v", i+1, v.VIN, shape, full, err)
				}
				if full && len(d.Items) != len(v.Items) {
					t.Errorf("row %d: %d items, the oracle has %d", i+1, len(d.Items), len(v.Items))
				}

				p := projectionOf(v)
				rng.Shuffle(len(p.Items), func(i, j int) { p.Items[i], p.Items[j] = p.Items[j], p.Items[i] })
				d2, err := NewDecode(p, req, info)
				if err != nil {
					t.Fatal(err)
				}
				b2, _ := json.Marshal(d2)
				if !bytes.Equal(b, b2) {
					t.Errorf("row %d shape %s full %v: shuffled items give different bytes", i+1, shape, full)
				}
			}
		}
	}
}

// TestLayouts pins the layouts' rules on answer-key VINs that exercise them.
func TestLayouts(t *testing.T) {
	info := &Info{DecoderVersion: "(devel)", DataMonth: "2026_10", Elements: loadElements(t)}
	vins := loadAnswerKey(t)
	decode := func(row int, shape Shape) Decode {
		t.Helper()
		d, err := NewDecode(projectionOf(vins[row-1]), Request{VIN: vins[row-1].VIN, Shape: shape, Now: testNow}, info)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Row 1, 1HGCM82633A004352: a clean decode.
	vp := decode(1, ShapeVPIC).Attributes.(VpicAttributes)
	if len(vp) != 141+3 {
		t.Errorf("vpic has %d keys, want 141 public elements + 3 ids", len(vp))
	}
	for k, want := range map[string]string{
		"Make": "HONDA", "MakeID": "474", "ManufacturerId": "988", "ModelID": "1861",
		"ErrorCode": "0", "SuggestedVIN": "", "AdditionalErrorText": "", "BedType": "Not Applicable",
		"Note": "", // multi-valued and undecoded
	} {
		if vp[k] != want {
			t.Errorf("vpic %s = %q, want %q", k, vp[k], want)
		}
	}
	nv := decode(1, ShapeNitrovin).Attributes.(NitrovinAttributes)
	if a := nv["Make"]; a.ElementID != 26 || a.Value != "HONDA" || a.AttributeID != Value[int32](474) || a.Values != nil {
		t.Errorf("nitrovin Make = %+v", a)
	}
	if a, ok := nv["Model Year"]; !ok || a.AttributeID.Valid {
		t.Errorf("nitrovin Model Year = %+v, want an int element without attribute_id", a)
	}
	if a := nv["Error Code"]; a.Value != "0" || a.AttributeID.Valid {
		t.Errorf("nitrovin Error Code = %+v, want no attribute_id", a)
	}
	if a, ok := nv["Suggested VIN"]; !ok || a.Value != "" {
		t.Errorf("nitrovin Suggested VIN (\"\") = %+v, %v: want present and empty", a, ok)
	}
	if _, ok := nv["Additional Error Text"]; ok {
		t.Error("nitrovin Additional Error Text (NULL) is present")
	}
	d := decode(1, ShapeNitrovin)
	if !slices.Equal(d.ErrorCodes, []int{0}) || d.CorrectedVIN.Valid || d.ModelYear != Value[int32](2003) {
		t.Errorf("row 1 header: codes %v, corrected %+v, model year %+v", d.ErrorCodes, d.CorrectedVIN, d.ModelYear)
	}

	// Row 22, 1HGCM8263IA004352: an 'I' at position 10, and element 121
	// four times the same from four patterns plus one other.
	d = decode(22, ShapeNitrovin)
	if !slices.Equal(d.ErrorCodes, []int{1, 11, 400}) || d.CorrectedVIN != Value("1HGCM8263!A004352") {
		t.Errorf("row 22 header: codes %v, corrected %+v", d.ErrorCodes, d.CorrectedVIN)
	}
	same := "Seat Belt (Rr center position)"
	other := "Seat Belt - Driver, Passenger and Rear Center positions  Rear Side Curtain Air bag - Rear Outer positions"
	if a := d.Attributes.(NitrovinAttributes)["Other Restraint System Info"]; !slices.Equal(a.Values, []string{same, same, same, same, other}) {
		t.Errorf("nitrovin Other Restraint System Info = %q, want duplicates kept in byte order", a.Values)
	}
	if got := decode(22, ShapeVPIC).Attributes.(VpicAttributes)["OtherRestraintSystemInfo"]; got != same+", "+other {
		t.Errorf("vpic OtherRestraintSystemInfo = %q, want distinct values joined", got)
	}

	// Row 18, JH2PC40066M200001: two codes.
	if d := decode(18, ShapeVPIC); !slices.Equal(d.ErrorCodes, []int{0, 14}) {
		t.Errorf("row 18 error codes %v, want [0 14]", d.ErrorCodes)
	}
}

// TestLayoutRefusals: items the layouts cannot represent are a fault, not a
// guess.
func TestLayoutRefusals(t *testing.T) {
	es := loadElements(t)
	make1 := Item{ElementID: 26, Variable: "Make", Value: Value("HONDA")}
	for name, items := range map[string][]Item{
		"single-valued element twice": {make1, make1},
		"private element":             {{ElementID: 186, Value: Value("x")}},
	} {
		for _, shape := range []Shape{ShapeVPIC, ShapeNitrovin} {
			if _, err := Layout(shape, items, es); err == nil {
				t.Errorf("%s, %s: no error", name, shape)
			}
		}
	}
	notes := []Item{{ElementID: 114, Value: Value("b")}, {ElementID: 114, Value: Value("a")}, {ElementID: 114, Value: Value("b")}}
	if _, err := Layout(ShapeVPIC, notes, es); err != nil {
		t.Errorf("multi-valued element three times: %v", err)
	}
}

func TestCompareItems(t *testing.T) {
	// Each item sorts strictly before the next.
	ordered := []Item{
		{ElementID: 2, Value: Value("z")},
		{ElementID: 10}, // numeric, not "10" < "2"
		{ElementID: 10, Value: Value("")},
		{ElementID: 10, Value: Value("A")},
		{ElementID: 10, Value: Value("a")}, // bytewise: 'A' < 'a'
		{ElementID: 10, Value: Value("a"), GroupName: Value("")},
		{ElementID: 10, Value: Value("a"), GroupName: Value(""), PatternID: Value[int32](-1)},
		{ElementID: 10, Value: Value("a"), GroupName: Value(""), PatternID: Value[int32](9)},
		{ElementID: 10, Value: Value("a"), GroupName: Value(""), PatternID: Value[int32](10)},
		{ElementID: 10, Value: Value("a"), GroupName: Value(""), PatternID: Value[int32](10), ToBeQCed: Value(false)},
		{ElementID: 10, Value: Value("a"), GroupName: Value(""), PatternID: Value[int32](10), ToBeQCed: Value(true)},
	}
	for i := range ordered {
		for j := range ordered {
			if got, want := CompareItems(ordered[i], ordered[j]), cmp.Compare(i, j); got != want {
				t.Errorf("CompareItems(%d, %d) = %d, want %d", i, j, got, want)
			}
		}
	}
}

// TestItemStrings: an item's strings are escaped exactly as encoding/json
// escapes them, so every surface writes the same bytes.
func TestItemStrings(t *testing.T) {
	for _, s := range []string{
		"", "plain ASCII", `quote " and \ backslash`, "<b>&amp;</b>", "tab\tnew\nline\x00",
		"Citroën", "\u2028\u2029", "bad \xff utf-8", "\x7f",
	} {
		b, err := json.Marshal(Item{Variable: s})
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(s)
		if !bytes.Contains(b, append([]byte(`"variable":`), want...)) {
			t.Errorf("%q: item has %s, want %s", s, b, want)
		}
	}
}
