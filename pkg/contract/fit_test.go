package contract

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseFitRequest(t *testing.T) {
	ram := map[string]string{"make": "Dodge", "model": "Ram 1500"}
	accepted := []struct {
		in   string
		want FitRequest
	}{
		{`{"from":"1D7RV1GT5AS123456"}`, FitRequest{From: FitFrom{VIN: "1D7RV1GT5AS123456"}}},
		{
			`{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"mirror"},"side":"left","to":["yard"],"limit":1000,"after":"WyJhIiwxXQ","now":"2026-11-10"}`,
			FitRequest{
				From:      FitFrom{Vocabulary: "catalog-models", Entry: ram},
				ModelYear: Value[int32](2010),
				Part:      &FitPart{Spine: "part-types", ID: "mirror"},
				Side:      "left",
				To:        []string{"yard"},
				Limit:     1000,
				After:     "WyJhIiwxXQ",
				Now:       time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
			},
		},
		{`{"from":{"model":1938},"part":{"vocabulary":"listing","entry":{"category":"Brake Parts"}},"related":false,"model_year":null}`,
			FitRequest{From: FitFrom{SpineRef: SpineRef{Model: 1938}}, Part: &FitPart{Vocabulary: "listing", Entry: map[string]string{"category": "Brake Parts"}}}},
		{`{"from":{"make":476},"related":true}`, FitRequest{From: FitFrom{SpineRef: SpineRef{Make: 476}}, Related: true}},
		{`{"from":{"ext":"ext:classics/corvair"}}`, FitRequest{From: FitFrom{SpineRef: SpineRef{Ext: "ext:classics/corvair"}}}},
		{`{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}},"to":["yard"],"side":"both"}`,
			FitRequest{From: FitFrom{Fitment: &FitmentKey{Type: "brake-rotor-front", Number: "BR-1999"}}, To: []string{"yard"}, Side: "both"}},
		{`{"from":{"fitment":{"type":"mirror","number":"MR-1001","side":"right"}},"model_year":2010}`,
			FitRequest{From: FitFrom{Fitment: &FitmentKey{Type: "mirror", Number: "MR-1001", Side: "right"}}, ModelYear: Value[int32](2010)}},
	}
	fitRequest := schemaDef(t, "FitRequest")
	for _, c := range accepted {
		got, err := ParseFitRequest([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.in, got, c.want)
		}
		if err := validate(fitRequest, []byte(c.in)); err != nil {
			t.Errorf("%s: accepted, but the schema refuses it: %v", c.in, err)
		}
	}

	refused := []struct {
		in           string
		typ          ProblemType
		pointer      string
		beyondSchema bool
	}{
		{`{"from":`, MalformedJSON, "", false},
		{`[]`, InvalidRequest, "", false},
		{`{}`, InvalidRequest, "/from", false},
		{`{"from":"x","vin":"x"}`, InvalidRequest, "/vin", false},
		{`{"from":""}`, InvalidRequest, "/from", false},
		{`{"from":"1HGCM82633A004352A"}`, InvalidRequest, "/from", true},
		{`{"from":{}}`, InvalidRequest, "/from", false},
		{`{"from":{"spine":"part-types","id":"mirror"}}`, InvalidRequest, "/from/id", false},
		{`{"from":{"vocabulary":"catalog-models"}}`, InvalidRequest, "/from/entry", false},
		// ADR 0018's refusals a request makes on its own.
		{`{"from":{"fitment":{"type":"mirror"}}}`, InvalidRequest, "/from/fitment/number", false},
		{`{"from":{"fitment":{"number":"MR-1001"}}}`, InvalidRequest, "/from/fitment/type", false},
		{`{"from":{"fitment":{"type":"","number":"MR-1001"}}}`, InvalidRequest, "/from/fitment/type", false},
		{`{"from":{"fitment":{"type":"mirror","number":"MR-1001","side":"front"}}}`, InvalidRequest, "/from/fitment/side", false},
		{`{"from":{"fitment":{"type":"mirror","number":"MR-1001","position":"upper"}}}`, InvalidRequest, "/from/fitment/position", false},
		{`{"from":{"fitment":{"type":"mirror","number":"MR-1001"},"make":476}}`, InvalidRequest, "/from/make", false},
		{`{"from":{"fitment":{"type":"mirror","number":"MR-1001"}},"part":{"spine":"part-types","id":"mirror"}}`, InvalidRequest, "/part", false},
		{`{"from":{"fitment":{"type":"mirror","number":"MR-1001"}},"related":true}`, InvalidRequest, "/related", false},
		{`{"from":{"make":476},"side":"front"}`, InvalidRequest, "/side", false},
		{`{"from":{"make":476},"side":""}`, InvalidRequest, "/side", false},
		// The request's shape.
		{`{"from":{"make":476},"part":{}}`, InvalidRequest, "/part", false},
		{`{"from":{"make":476},"part":{"spine":"part-types"}}`, InvalidRequest, "/part/id", false},
		{`{"from":{"make":476},"part":{"vocabulary":"listing"}}`, InvalidRequest, "/part/entry", false},
		{`{"from":{"make":476},"part":{"spine":"part-types","id":"mirror","vocabulary":"listing"}}`, InvalidRequest, "/part/spine", false},
		{`{"from":{"make":476},"part":{"make":476}}`, InvalidRequest, "/part/make", false},
		{`{"from":{"make":476},"related":"yes"}`, InvalidRequest, "/related", false},
		{`{"from":{"make":476},"to":["yard",""]}`, InvalidRequest, "/to/1", false},
		{`{"from":{"make":476},"limit":0}`, InvalidRequest, "/limit", false},
		{`{"from":{"make":476},"limit":1001}`, InvalidRequest, "/limit", false},
		{`{"from":{"make":476},"after":""}`, InvalidRequest, "/after", false},
		{`{"from":{"make":476},"now":"tomorrow"}`, InvalidRequest, "/now", false},
		{`{"from":{"make":476},"model_year":"2010"}`, InvalidRequest, "/model_year", false},
	}
	for _, c := range refused {
		_, err := ParseFitRequest([]byte(c.in))
		if err == nil {
			t.Errorf("%s: accepted", c.in)
			continue
		}
		problemOf(t, err, c.typ, c.pointer)
		if c.typ == InvalidRequest && !c.beyondSchema {
			if validate(fitRequest, []byte(c.in)) == nil {
				t.Errorf("%s: refused, but the schema accepts it", c.in)
			}
		}
	}
}

// TestFitRequestValidate: a Go caller's request is checked as a parsed one
// is.
func TestFitRequestValidate(t *testing.T) {
	key := &FitmentKey{Type: "mirror", Number: "MR-1001"}
	for _, c := range []struct {
		r       FitRequest
		pointer string
	}{
		{FitRequest{}, "/from"},
		{FitRequest{From: FitFrom{VIN: "x", Fitment: key}}, "/from"},
		{FitRequest{From: FitFrom{SpineRef: SpineRef{Spine: "part-types", ID: "mirror"}}}, "/from"},
		{FitRequest{From: FitFrom{Fitment: &FitmentKey{Type: "mirror"}}}, "/from/fitment/number"},
		{FitRequest{From: FitFrom{Fitment: key}, Part: &FitPart{Spine: "part-types", ID: "mirror"}}, "/part"},
		{FitRequest{From: FitFrom{Fitment: key}, Related: true}, "/related"},
		{FitRequest{From: FitFrom{VIN: "x"}, Part: &FitPart{}}, "/part"},
		{FitRequest{From: FitFrom{VIN: "x"}, Side: "front"}, "/side"},
		{FitRequest{From: FitFrom{VIN: "x"}, Limit: 1001}, "/limit"},
		{FitRequest{From: FitFrom{VIN: "x"}, To: []string{""}}, "/to/0"},
	} {
		problemOf(t, c.r.Validate(), InvalidRequest, c.pointer)
	}
	if err := (FitRequest{From: FitFrom{Fitment: key}, Side: "left"}).Validate(); err != nil {
		t.Errorf("a fitment and a side: %v", err)
	}
}

func TestParseFitBatch(t *testing.T) {
	reqs, err := ParseFitBatch([]byte(`[{"from":"a"},{"from":{"fitment":{"type":"t","number":"1"}}}]`))
	want := []FitRequest{{From: FitFrom{VIN: "a"}}, {From: FitFrom{Fitment: &FitmentKey{Type: "t", Number: "1"}}}}
	if err != nil || !reflect.DeepEqual(reqs, want) {
		t.Errorf("got %+v, %v", reqs, err)
	}
	item := `{"from":{"make":476}},`
	for _, c := range []struct {
		in      string
		typ     ProblemType
		pointer string
	}{
		{`[`, MalformedJSON, ""},
		{`{"from":"a"}`, InvalidRequest, ""},
		{`[]`, InvalidRequest, ""},
		{"[" + strings.Repeat(item, MaxBatch) + `{"from":"a"}]`, TooLarge, ""},
		{`[{"from":"a"},{"from":{"fitment":{"type":"t","number":"1"}},"related":true}]`, InvalidRequest, "/1/related"},
		{`["` + strings.Repeat("a", MaxBody) + `"]`, TooLarge, ""},
	} {
		_, err := ParseFitBatch([]byte(c.in))
		problemOf(t, err, c.typ, c.pointer)
	}
}

// TestParseFitQuery: a query asks what the JSON request it spells asks,
// and is refused where that request is, at the same pointer.
func TestParseFitQuery(t *testing.T) {
	for _, c := range []struct{ query, json string }{
		{"from.vin=1D7RV1GT5AS123456&part.spine=part-types&part.id=mirror&side=left&now=2026-11-10",
			`{"from":"1D7RV1GT5AS123456","part":{"spine":"part-types","id":"mirror"},"side":"left","now":"2026-11-10"}`},
		{"from.vocabulary=catalog-models&from.entry.make=Dodge&from.entry.model=Ram+1500&model_year=2010&related=true&to=yard&to=listing&limit=5&after=x",
			`{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"related":true,"to":["yard","listing"],"limit":5,"after":"x"}`},
		{"from.model=1938&part.vocabulary=listing&part.entry.category=Brake+Parts",
			`{"from":{"model":1938},"part":{"vocabulary":"listing","entry":{"category":"Brake Parts"}}}`},
		{"from.fitment.type=brake-rotor-front&from.fitment.number=BR-1999&to=yard",
			`{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}},"to":["yard"]}`},
		{"from.fitment.type=mirror&from.fitment.number=MR-1001&from.fitment.side=left",
			`{"from":{"fitment":{"type":"mirror","number":"MR-1001","side":"left"}}}`},
		// Refused alike.
		{"", `{}`},
		{"from.fitment.type=mirror", `{"from":{"fitment":{"type":"mirror"}}}`},
		{"from.fitment.type=mirror&from.fitment.number=1&part.spine=part-types&part.id=mirror",
			`{"from":{"fitment":{"type":"mirror","number":"1"}},"part":{"spine":"part-types","id":"mirror"}}`},
		{"from.fitment.type=mirror&from.fitment.number=1&related=true", `{"from":{"fitment":{"type":"mirror","number":"1"}},"related":true}`},
		{"from.fitment.type=mirror&from.fitment.number=1&from.make=476", `{"from":{"fitment":{"type":"mirror","number":"1"},"make":476}}`},
		{"from.make=476&related=yes", `{"from":{"make":476},"related":"yes"}`},
		{"from.make=476&side=front", `{"from":{"make":476},"side":"front"}`},
		{"from.make=476&limit=0", `{"from":{"make":476},"limit":0}`},
		{"from.make=476&part.spine=part-types", `{"from":{"make":476},"part":{"spine":"part-types"}}`},
	} {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		got, gotErr := ParseFitQuery(q)
		want, wantErr := ParseFitRequest([]byte(c.json))
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Errorf("?%s: %+v, %v; want %s's %+v, %v", c.query, got, gotErr, c.json, want, wantErr)
		}
	}
	// What only a query can get wrong.
	for _, c := range []struct{ query, pointer string }{
		{"from.vin=a&from.make=476", "/from"},
		{"from.make=476&from.make=477", "/from/make"},
		{"from.make=476&side=left&side=right", "/side"},
		{"from.make=476&part.entry.code=1&part.entry.code=2", "/part/entry/code"},
		{"from.make=476&part.kind=x", "/part/kind"},
		{"from.make=476&vin=1D7RV1GT5AS123456", "/vin"},
		{"from.spine=part-types&from.id=mirror", "/from/id"},
	} {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseFitQuery(q)
		problemOf(t, err, InvalidRequest, c.pointer)
	}
}

// TestFitJSON pins the fit answer types' JSON where Go's defaults would
// differ from the schema: a catalog's vehicle as its file writes it, and
// an answer's list present, empty, when nothing fits.
func TestFitJSON(t *testing.T) {
	for _, c := range []struct {
		v    any
		def  string
		want string
	}{
		{CatalogVehicle{Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}, "CatalogVehicle", `{"make":"Dodge","model":"Ram 1500"}`},
		{CatalogVehicle{Model: 1938}, "CatalogVehicle", `{"model":1938}`},
		{CatalogVehicle{Ext: "ext:classics/corvair"}, "CatalogVehicle", `{"ext":"ext:classics/corvair"}`},
		{
			FitApplication{Vehicle: CatalogVehicle{Model: 1938}, First: Value[int32](2009), Options: "", Unconfirmed: Unconfirmed{Elements: []int32{15}}},
			"FitApplication", `{"vehicle":{"model":1938},"first":2009,"options":"","unconfirmed":[15]}`,
		},
		{
			Fit{Status: FitNoCatalog, Models: []FitModel{}, Candidates: []FitCandidate{}, Now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), DataMonth: "2026_10", DecoderVersion: "v1"},
			"Fit", `{"status":"no-catalog","models":[],"candidates":[],"total":0,"now":"2026-11-01T00:00:00Z","data_month":"2026_10","decoder_version":"v1"}`,
		},
		{
			FitmentLine{Fitment: FitmentKey{Type: "mirror", Number: "MR-1001", Side: "left"}, Applications: []LineApplication{{
				Vehicle: CatalogVehicle{Entry: map[string]string{"make": "Dodge"}}, Options: "4WD", Qualifiers: map[string][]QualifierValue{"15": {{Attribute: 2}}},
			}}},
			"FitmentLine", `{"fitment":{"type":"mirror","number":"MR-1001","side":"left"},"applications":[{"vehicle":{"make":"Dodge"},"options":"4WD","qualifiers":{"15":[2]}}]}`,
		},
	} {
		b, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != c.want {
			t.Errorf("got %s, want %s", b, c.want)
		}
		if err := validate(schemaDef(t, c.def), b); err != nil {
			t.Errorf("%s does not validate as %s: %v", b, c.def, err)
		}
	}
}

// TestFitCursor: a cursor is the answer kind's own; another kind's, or
// anything else, is refused at /after.
func TestFitCursor(t *testing.T) {
	c := encodeFitCursor(fitCandidates, 100)
	if n, p := decodeFitCursor(c, fitCandidates); p != nil || n != 100 {
		t.Errorf("decode %q: %d, %v", c, n, p)
	}
	for _, after := range []string{c + "=", "WyJhIl0", encodeFitCursor(fitCandidates, 0)} {
		if _, p := decodeFitCursor(after, fitCandidates); p == nil || p.Pointer != "/after" {
			t.Errorf("%q: %v; want a refusal at /after", after, p)
		}
	}
	if _, p := decodeFitCursor(c, fitRelated); p == nil {
		t.Errorf("a candidates cursor is taken for related vehicles")
	}
}
