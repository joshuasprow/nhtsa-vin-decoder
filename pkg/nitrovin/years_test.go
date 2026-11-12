package nitrovin

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// TestModelYearsCounts: the embedded month's model years are ADR 0022's
// measure of 2026_10.
func TestModelYearsCounts(t *testing.T) {
	d := embeddedDecoder(t)
	if embedded.Month != "2026_10" {
		t.Skipf("ADR 0022 measured 2026_10, not %s", embedded.Month)
	}
	known, unknown, gaps := 0, 0, 0
	for id := range d.month.ModelIDs() {
		switch ys := d.month.ModelYears(id); {
		case ys == nil:
			unknown++
		case len(ys) > 1:
			gaps++
			fallthrough
		default:
			known++
		}
	}
	open, bands := 0, 0
	for _, to := range d.artifact.Table("wmi_vinschema").Int32s("yearto") {
		bands++
		if to == -1<<31 {
			open++
		}
	}
	// ADR 0022 says 540 unknown: 533 models have no Model pattern, and 8
	// have one whose schemas have no year band.
	if known != 31529 || unknown != 541 || gaps != 920 || open != 12186 || bands != 41810 {
		t.Errorf("%d models with years, %d unknown, %d with gaps, %d of %d bands open; want 31529, 541, 920, 12186 of 41810", known, unknown, gaps, open, bands)
	}
}

// listed is the vPIC models a list of make with where gives, by id, as
// their JSON.
func listed(t *testing.T, tr *Translator, where string) map[int32]string {
	t.Helper()
	req, err := contract.ParseListRequest([]byte(`{"of":"vpic","limit":1000,"where":` + where + `}`))
	if err != nil {
		t.Fatal(err)
	}
	l, err := tr.List(req)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}
	if l.Next != "" {
		t.Fatalf("%s: more than a page", where)
	}
	out := map[int32]string{}
	for _, e := range l.Entries {
		se := e.(contract.SpineEntry)
		b, _ := json.Marshal(se.Years)
		out[se.Model] = string(b)
	}
	return out
}

// TestListModelYears: list vpic gives a model's years, gaps kept and an
// open band open, and where.year keeps the models built that year and
// those whose years are unknown.
func TestListModelYears(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	for _, tc := range []struct {
		name, where string
		model       int32
		years       string
	}{
		{"a gap gives two ranges", `{"make":4162}`, 9068, `[{"first":1981,"last":1981},{"first":1997,"last":1999}]`},
		{"an open band gives an open range", `{"make":12413}`, 32453, `[{"first":2019}]`},
		{"no Model pattern: unknown years", `{"make":567}`, 2924, `null`},
	} {
		if got := listed(t, tr, tc.where)[tc.model]; got != tc.years {
			t.Errorf("%s: model %d years %s, want %s", tc.name, tc.model, got, tc.years)
		}
	}

	for _, tc := range []struct {
		year       string
		keep, drop []int32
	}{
		{"1981", []int32{9068}, nil},
		{"1990", nil, []int32{9068}}, // the gap
		{"1998", []int32{9068}, nil},
		{"2000", nil, []int32{9068}},
	} {
		got := listed(t, tr, `{"make":4162,"year":`+tc.year+`}`)
		for _, id := range tc.keep {
			if _, ok := got[id]; !ok {
				t.Errorf("year %s: model %d is not kept", tc.year, id)
			}
		}
		for _, id := range tc.drop {
			if _, ok := got[id]; ok {
				t.Errorf("year %s: model %d is kept", tc.year, id)
			}
		}
	}
	if _, ok := listed(t, tr, `{"make":12413,"year":2040}`)[32453]; !ok {
		t.Error("year 2040: an open range does not include a later year")
	}
	if _, ok := listed(t, tr, `{"make":12413,"year":2018}`)[32453]; ok {
		t.Error("year 2018: a range starting 2019 includes it")
	}
	if _, ok := listed(t, tr, `{"make":567,"year":1950}`)[2924]; !ok {
		t.Error("year 1950: a model with unknown years is not kept")
	}
}

// TestModelYearsDrift: a mapping whose years fall outside its model's
// reports model-years drift, saying which and giving the ranges; one
// within, one to a model with unknown years, one with no years and one to
// a make never do. A single stated end beyond the model's years is wholly
// outside.
func TestModelYearsDrift(t *testing.T) {
	d := embeddedDecoder(t)
	f, err := vocab.Read("yard", strings.NewReader(strings.Join([]string{
		`{"format":1,"kind":"vocabulary","name":"yard","version":"1","subject":"vehicle","fields":["make","model"]}`,
		`{"entry":{"make":"Oldsmobile","model":"Cutlass gap"},"mappings":[{"model":9068,"first":1985,"last":1990}]}`,
		`{"entry":{"make":"Oldsmobile","model":"Cutlass late"},"mappings":[{"model":9068,"first":1998,"last":2002}]}`,
		`{"entry":{"make":"Oldsmobile","model":"Cutlass"},"mappings":[{"model":9068,"first":1997,"last":1999}]}`,
		`{"entry":{"make":"Oldsmobile","model":"Cutlass any"},"mappings":[{"model":9068}]}`,
		`{"entry":{"make":"Lonestar","model":"TX22"},"mappings":[{"model":32453,"first":2030}]}`,
		`{"entry":{"make":"Victory","model":"Ness Magnum"},"mappings":[{"model":2924,"first":1950,"last":1951}]}`,
		`{"entry":{"make":"Oldsmobile","model":"Other"},"mappings":[{"make":4162,"first":1950,"last":1951}]}`,
		`{"entry":{"make":"Oldsmobile","model":"Cutlass new"},"mappings":[{"model":9068,"first":2005}]}`,
		`{"entry":{"make":"Oldsmobile","model":"Cutlass old"},"mappings":[{"model":9068,"last":1970}]}`,
		`{"entry":{"make":"Oldsmobile","model":"Cutlass to 1990"},"mappings":[{"model":9068,"last":1990}]}`,
		`{"entry":{"make":"Lonestar","model":"TX22 old"},"mappings":[{"model":32453,"last":2010}]}`,
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, f)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range tr.Drift() {
		got = append(got, string(f.Drift)+" "+f.String())
	}
	want := []string{
		`model-years yard:2: /mappings/0: model years 1985-1990 fall wholly outside vPIC model 9068's years in data month 2026_10: 1981-1981, 1997-1999`,
		`model-years yard:3: /mappings/0: model years 1998-2002 fall partly outside vPIC model 9068's years in data month 2026_10: 1981-1981, 1997-1999`,
		`model-years yard:9: /mappings/0: model years 2005- fall wholly outside vPIC model 9068's years in data month 2026_10: 1981-1981, 1997-1999`,
		`model-years yard:10: /mappings/0: model years -1970 fall wholly outside vPIC model 9068's years in data month 2026_10: 1981-1981, 1997-1999`,
		`model-years yard:11: /mappings/0: model years -1990 fall partly outside vPIC model 9068's years in data month 2026_10: 1981-1981, 1997-1999`,
		`model-years yard:12: /mappings/0: model years -2010 fall wholly outside vPIC model 32453's years in data month 2026_10: 2019-`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("drift:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// BenchmarkModelYears builds every model's years (ADR 0022: 53 ms, 2.8
// MiB on 2026_10).
func BenchmarkModelYears(b *testing.B) {
	d, err := Embedded()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		d.month.modelYears()
	}
}
