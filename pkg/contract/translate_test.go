package contract

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseTranslateRequest(t *testing.T) {
	accepted := []struct {
		in   string
		want TranslateRequest
	}{
		{`{"from":"1HGCM82633A004352"}`, TranslateRequest{From: TranslateFrom{VIN: "1HGCM82633A004352"}}},
		{
			`{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"to":["yard","ebay"],"model_year":2010,"vin":"1D7RV1GT5AS123456","now":"2026-11-10"}`,
			TranslateRequest{
				From:      TranslateFrom{Vocabulary: "yard", Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}},
				To:        []string{"yard", "ebay"},
				ModelYear: Value[int32](2010),
				VIN:       "1D7RV1GT5AS123456",
				Now:       time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC),
			},
		},
		{`{"from":{"vocabulary":"codes","entry":{}},"to":[],"model_year":null}`, TranslateRequest{From: TranslateFrom{Vocabulary: "codes", Entry: map[string]string{}}, To: []string{}}},
		{`{"from":{"make":476}}`, TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{Make: 476}}}},
		{`{"from":{"model":1938.0}}`, TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{Model: 1938}}}},
		{`{"from":{"ext":"ext:classics/corvair"}}`, TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{Ext: "ext:classics/corvair"}}}},
		{`{"from":{"spine":"parts","id":"300"}}`, TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{Spine: "parts", ID: "300"}}}},
	}
	translateRequest := schemaDef(t, "TranslateRequest")
	for _, c := range accepted {
		got, err := ParseTranslateRequest([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.in, got, c.want)
		}
		if err := validate(translateRequest, []byte(c.in)); err != nil {
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
		{`{"from":"x","shape":"vpic"}`, InvalidRequest, "/shape", false},
		{`{"from":null}`, InvalidRequest, "/from", false},
		{`{"from":""}`, InvalidRequest, "/from", false},
		{`{"from":"  "}`, InvalidRequest, "/from", true},
		{`{"from":"1HGCM82633A004352A"}`, InvalidRequest, "/from", true},
		{`{"from":"1HGCM82633A004352","vin":"1HGCM82633A004352"}`, InvalidRequest, "/vin", false},
		{`{"from":{"make":476},"vin":""}`, InvalidRequest, "/vin", false},
		{`{"from":{}}`, InvalidRequest, "/from", false},
		{`{"from":{"trim":"SLT"}}`, InvalidRequest, "/from/trim", false},
		{`{"from":{"vocabulary":"yard"}}`, InvalidRequest, "/from/entry", false},
		{`{"from":{"entry":{}}}`, InvalidRequest, "/from/vocabulary", false},
		{`{"from":{"vocabulary":"","entry":{}}}`, InvalidRequest, "/from/vocabulary", false},
		{`{"from":{"vocabulary":"yard","entry":[]}}`, InvalidRequest, "/from/entry", false},
		{`{"from":{"vocabulary":"yard","entry":{"make":476}}}`, InvalidRequest, "/from/entry/make", false},
		{`{"from":{"vocabulary":"yard","entry":{},"make":476}}`, InvalidRequest, "/from/make", false},
		{`{"from":{"make":476,"model":1938}}`, InvalidRequest, "/from", false},
		{`{"from":{"make":0}}`, InvalidRequest, "/from/make", false},
		{`{"from":{"model":"1938"}}`, InvalidRequest, "/from/model", false},
		{`{"from":{"ext":"classics/corvair"}}`, InvalidRequest, "/from/ext", false},
		{`{"from":{"spine":"parts"}}`, InvalidRequest, "/from/id", false},
		{`{"from":{"id":"300"}}`, InvalidRequest, "/from/spine", false},
		{`{"from":{"make":476},"to":"yard"}`, InvalidRequest, "/to", false},
		{`{"from":{"make":476},"to":["yard",""]}`, InvalidRequest, "/to/1", false},
		{`{"from":{"make":476},"model_year":"2010"}`, InvalidRequest, "/model_year", false},
		{`{"from":{"make":476},"now":"tomorrow"}`, InvalidRequest, "/now", false},
	}
	for _, c := range refused {
		_, err := ParseTranslateRequest([]byte(c.in))
		if err == nil {
			t.Errorf("%s: accepted", c.in)
			continue
		}
		problemOf(t, err, c.typ, c.pointer)
		if c.typ == InvalidRequest && !c.beyondSchema {
			if validate(translateRequest, []byte(c.in)) == nil {
				t.Errorf("%s: refused, but the schema accepts it", c.in)
			}
		}
	}
}

// TestTranslateRequestValidate: a Go caller's request is checked as a
// parsed one is.
func TestTranslateRequestValidate(t *testing.T) {
	for _, c := range []struct {
		r       TranslateRequest
		pointer string
	}{
		{TranslateRequest{}, "/from"},
		{TranslateRequest{From: TranslateFrom{VIN: "x", SpineRef: SpineRef{Make: 476}}}, "/from"},
		{TranslateRequest{From: TranslateFrom{Vocabulary: "yard"}}, "/from/entry"},
		{TranslateRequest{From: TranslateFrom{VIN: "x"}, VIN: "y"}, "/vin"},
		{TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{ID: "300"}}}, "/from/spine"},
		{TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{Make: 476}}, To: []string{""}}, "/to/0"},
		{TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{Make: 476}}, Now: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}, "/now"},
	} {
		problemOf(t, c.r.Validate(), InvalidRequest, c.pointer)
	}
	if err := (TranslateRequest{From: TranslateFrom{SpineRef: SpineRef{Spine: "parts", ID: "300"}}}).Validate(); err != nil {
		t.Errorf("a spine entry: %v", err)
	}
}

func TestParseTranslateBatch(t *testing.T) {
	reqs, err := ParseTranslateBatch([]byte(`[{"from":"a"},{"from":{"make":476},"to":["yard"]}]`))
	want := []TranslateRequest{{From: TranslateFrom{VIN: "a"}}, {From: TranslateFrom{SpineRef: SpineRef{Make: 476}}, To: []string{"yard"}}}
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
		{`[{"from":"a"},{"from":"a","vin":"b"}]`, InvalidRequest, "/1/vin"},
		{`[{"from":"a"},"a"]`, InvalidRequest, "/1"},
		{`["` + strings.Repeat("a", MaxBody) + `"]`, TooLarge, ""},
	} {
		_, err := ParseTranslateBatch([]byte(c.in))
		problemOf(t, err, c.typ, c.pointer)
	}
}

// TestParseTranslateQuery: a query asks what the JSON request it spells
// asks, and is refused where that request is, at the same pointer.
func TestParseTranslateQuery(t *testing.T) {
	for _, c := range []struct{ query, json string }{
		{"from.vin=1HGCM82633A004352", `{"from":"1HGCM82633A004352"}`},
		{"from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500&to=yard&to=ebay&model_year=2010&vin=1D7RV1GT5AS123456&now=2026-11-10",
			`{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"to":["yard","ebay"],"model_year":2010,"vin":"1D7RV1GT5AS123456","now":"2026-11-10"}`},
		{"from.vocabulary=codes&from.entry.a%2Fb=1", `{"from":{"vocabulary":"codes","entry":{"a/b":"1"}}}`},
		{"from.make=476", `{"from":{"make":476}}`},
		{"from.model=1938.0", `{"from":{"model":1938.0}}`},
		{"from.ext=ext:classics/corvair", `{"from":{"ext":"ext:classics/corvair"}}`},
		{"from.spine=parts&from.id=300", `{"from":{"spine":"parts","id":"300"}}`},
		// Refused alike.
		{"", `{}`},
		{"from.vin=", `{"from":""}`},
		{"from.vin=1HGCM82633A004352A", `{"from":"1HGCM82633A004352A"}`},
		{"from.vin=a&vin=a", `{"from":"a","vin":"a"}`},
		{"from.make=476&vin=", `{"from":{"make":476},"vin":""}`},
		{"from.vocabulary=yard", `{"from":{"vocabulary":"yard"}}`},
		{"from.entry.make=Dodge", `{"from":{"entry":{"make":"Dodge"}}}`},
		{"from.vocabulary=&from.entry.make=Dodge", `{"from":{"vocabulary":"","entry":{"make":"Dodge"}}}`},
		{"from.vocabulary=yard&from.entry.make=Dodge&from.make=476", `{"from":{"vocabulary":"yard","entry":{"make":"Dodge"},"make":476}}`},
		{"from.make=476&from.model=1938", `{"from":{"make":476,"model":1938}}`},
		{"from.make=0", `{"from":{"make":0}}`},
		{"from.make=Dodge", `{"from":{"make":"Dodge"}}`},
		{"from.make=+476", `{"from":{"make":" 476"}}`},
		{"from.ext=classics/corvair", `{"from":{"ext":"classics/corvair"}}`},
		{"from.spine=parts", `{"from":{"spine":"parts"}}`},
		{"from.id=300", `{"from":{"id":"300"}}`},
		{"from.make=476&to=yard&to=", `{"from":{"make":476},"to":["yard",""]}`},
		{"from.make=476&model_year=2010.5", `{"from":{"make":476},"model_year":2010.5}`},
		{"from.make=476&model_year=null", `{"from":{"make":476},"model_year":"null"}`},
		{"from.make=476&now=tomorrow", `{"from":{"make":476},"now":"tomorrow"}`},
	} {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		got, gotErr := ParseTranslateQuery(q)
		want, wantErr := ParseTranslateRequest([]byte(c.json))
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Errorf("?%s: %+v, %v; want %s's %+v, %v", c.query, got, gotErr, c.json, want, wantErr)
		}
	}
	// What only a query can get wrong.
	for _, c := range []struct{ query, pointer string }{
		{"from.vin=a&from.make=476", "/from"},
		{"from.vin=a&from.entry.make=Dodge", "/from"},
		{"from.make=476&from.make=477", "/from/make"},
		{"from.make=476&now=2026-11-01&now=2026-11-02", "/now"},
		{"from.vocabulary=yard&from.entry.make=Dodge&from.entry.make=Ram", "/from/entry/make"},
		{"from.make=476&from.mke=1", "/from/mke"},
		{"from.make=476&model.year=2010", "/model/year"},
		{"from.make=476&shape=vpic", "/shape"},
		{"from.make=476&from.entry~x=1", "/from/entry~0x"},
	} {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseTranslateQuery(q)
		problemOf(t, err, InvalidRequest, c.pointer)
	}
}

func TestParseListRequest(t *testing.T) {
	accepted := []struct {
		in   string
		want ListRequest
	}{
		{`{}`, ListRequest{}},
		{`{"of":"vpic","where":{"make":476},"limit":1000,"after":"WyJhIl0"}`, ListRequest{Of: "vpic", Where: map[string]string{"make": "476"}, Limit: 1000, After: "WyJhIl0"}},
		{`{"of":"yard","where":{"make":"Dodge"},"limit":1}`, ListRequest{Of: "yard", Where: map[string]string{"make": "Dodge"}, Limit: 1}},
	}
	listRequest := schemaDef(t, "ListRequest")
	for _, c := range accepted {
		got, err := ParseListRequest([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.in, got, c.want)
		}
		if err := validate(listRequest, []byte(c.in)); err != nil {
			t.Errorf("%s: accepted, but the schema refuses it: %v", c.in, err)
		}
	}
	for _, c := range []struct {
		in, pointer string
	}{
		{`{"of":""}`, "/of"},
		{`{"of":7}`, "/of"},
		{`{"of":"yard","page":2}`, "/page"},
		{`{"of":"yard","where":[]}`, "/where"},
		{`{"of":"yard","where":{"make":true}}`, "/where/make"},
		{`{"of":"yard","limit":0}`, "/limit"},
		{`{"of":"yard","limit":1001}`, "/limit"},
		{`{"of":"yard","limit":1.5}`, "/limit"},
		{`{"of":"yard","after":""}`, "/after"},
	} {
		_, err := ParseListRequest([]byte(c.in))
		if err == nil {
			t.Errorf("%s: accepted", c.in)
			continue
		}
		problemOf(t, err, InvalidRequest, c.pointer)
		if validate(listRequest, []byte(c.in)) == nil {
			t.Errorf("%s: refused, but the schema accepts it", c.in)
		}
	}
}

// TestParseListQuery: as TestParseTranslateQuery, for list.
func TestParseListQuery(t *testing.T) {
	for _, c := range []struct{ of, query, json string }{
		{"", "", `{}`},
		{"vpic", "where.make=476&limit=1000&after=WyJhIl0", `{"of":"vpic","where":{"make":476},"limit":1000,"after":"WyJhIl0"}`},
		{"yard", "where.make=Dodge&where.model=Ram+1500&limit=1", `{"of":"yard","where":{"make":"Dodge","model":"Ram 1500"},"limit":1}`},
		{"yard", "limit=0", `{"of":"yard","limit":0}`},
		{"yard", "limit=1.5", `{"of":"yard","limit":1.5}`},
		{"yard", "limit=ten", `{"of":"yard","limit":"ten"}`},
		{"yard", "after=", `{"of":"yard","after":""}`},
	} {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		got, gotErr := ParseListQuery(c.of, q)
		want, wantErr := ParseListRequest([]byte(c.json))
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Errorf("%s ?%s: %+v, %v; want %s's %+v, %v", c.of, c.query, got, gotErr, c.json, want, wantErr)
		}
	}
	for _, c := range []struct{ query, pointer string }{
		{"of=yard", "/of"},
		{"page=2", "/page"},
		{"limit=1&limit=2", "/limit"},
		{"where.make=Dodge&where.make=Ram", "/where/make"},
		{"where.a.b=1&where.a.b=2", "/where/a.b"},
	} {
		q, err := url.ParseQuery(c.query)
		if err != nil {
			t.Fatal(err)
		}
		_, err = ParseListQuery("yard", q)
		problemOf(t, err, InvalidRequest, c.pointer)
	}
}

// TestAnswerJSON pins the answer types' JSON where Go's defaults would
// differ from the schema: unconfirmed's mixed array, qualifier values,
// omitted years and refs.
func TestAnswerJSON(t *testing.T) {
	for _, c := range []struct {
		v    any
		def  string
		want string
	}{
		{Via{SpineRef: SpineRef{Model: 1938}, Last: Value[int32](2012), Unconfirmed: Unconfirmed{Elements: []int32{15, 34}, ModelYear: true}}, "Via", `{"model":1938,"last":2012,"unconfirmed":[15,34,"model_year"]}`},
		{Via{SpineRef: SpineRef{Spine: "parts", ID: "300"}, First: Value[int32](0)}, "Via", `{"spine":"parts","id":"300","first":0}`},
		{SpineEntry{SpineRef: SpineRef{Make: 99999}, Drift: true}, "SpineEntry", `{"make":99999,"drift":true}`},
		{SpineEntry{SpineRef: SpineRef{Ext: "ext:classics/corvair"}, Name: "Corvair", Under: &SpineRef{Make: 467}}, "SpineEntry", `{"ext":"ext:classics/corvair","name":"Corvair","under":{"make":467}}`},
		{TranslatedEntry{Entry: map[string]string{"code": "A"}, Via: []Via{{SpineRef: SpineRef{Make: 476}}}, Unconfirmed: Unconfirmed{ModelYear: true}}, "TranslatedEntry", `{"entry":{"code":"A"},"via":[{"make":476}],"unconfirmed":["model_year"]}`},
		{
			VocabularyEntry{Entry: map[string]string{"make": "Dodge"}, Mappings: []VocabularyMapping{{Model: 1938, Qualifiers: map[string][]QualifierValue{"15": {{Attribute: 2}}, "34": {{IsText: true, Text: "DS (1500)"}}}}}},
			"VocabularyEntry",
			`{"entry":{"make":"Dodge"},"mappings":[{"model":1938,"qualifiers":{"15":[2],"34":["DS (1500)"]}}]}`,
		},
		{VocabularyEntry{Entry: map[string]string{"make": "Lark"}, Mappings: []VocabularyMapping{}}, "VocabularyEntry", `{"entry":{"make":"Lark"},"mappings":[]}`},
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
