package contract

import (
	"encoding/json"
	"reflect"
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
