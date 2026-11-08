package contract

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

// problemOf asserts err is a *Problem of type typ at pointer, valid against
// the schema's Problem.
func problemOf(t *testing.T, err error, typ ProblemType, pointer string) {
	t.Helper()
	p, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("error %v is not a *Problem", err)
	}
	if p.Type != typ || p.Pointer != pointer {
		t.Errorf("problem %s at %q, want %s at %q (%v)", p.Type, p.Pointer, typ, pointer, p)
	}
	if p.Status != typ.Status() || p.Title == "" {
		t.Errorf("problem %+v lacks its type's status or title", p)
	}
	b, _ := json.Marshal(p)
	if err := validate(schemaDef(t, "Problem"), b); err != nil {
		t.Errorf("problem %s does not validate: %v", b, err)
	}
}

func TestParseRequest(t *testing.T) {
	vin17 := strings.Repeat("É", 17) // 34 bytes
	accepted := []struct {
		in   string
		want Request
	}{
		{`{"vin":"1HGCM82633A004352"}`, Request{VIN: "1HGCM82633A004352"}},
		{`{"vin":" *1 ","model_year":null,"full":false,"shape":"vpic"}`, Request{VIN: " *1 ", Shape: ShapeVPIC}},
		{`{"vin":"` + vin17 + `"}`, Request{VIN: vin17}},
		{`{"vin":"  1HGCM82633A004352  "}`, Request{VIN: "  1HGCM82633A004352  "}}, // 17 after trimming
		{`{"vin":"x","model_year":-2147483648}`, Request{VIN: "x", ModelYear: Value[int32](-2147483648)}},
		{`{"vin":"x","model_year":2147483647}`, Request{VIN: "x", ModelYear: Value[int32](2147483647)}},
		{`{"vin":"x","model_year":2003.0}`, Request{VIN: "x", ModelYear: Value[int32](2003)}},
		{`{"vin":"x","model_year":2.003e3}`, Request{VIN: "x", ModelYear: Value[int32](2003)}},
		{`{"vin":"x","full":true,"shape":"nitrovin"}`, Request{VIN: "x", Full: true, Shape: ShapeNitrovin}},
		{`{"vin":"x","now":"2026-03-01"}`, Request{VIN: "x", Now: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)}},
		{`{"vin":"x","now":"2026-03-01T23:59:59.999999999-05:00"}`, Request{VIN: "x", Now: time.Date(2026, 3, 2, 4, 59, 59, 0, time.UTC)}},
		{`{"vin":"x","now":"2026-03-01T10:00:00z"}`, Request{VIN: "x", Now: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}},
	}
	decodeRequest := schemaDef(t, "DecodeRequest")
	for _, c := range accepted {
		got, err := ParseRequest([]byte(c.in))
		if err != nil {
			t.Errorf("%s: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.in, got, c.want)
		}
		if err := validate(decodeRequest, []byte(c.in)); err != nil {
			t.Errorf("%s: accepted, but the schema refuses it: %v", c.in, err)
		}
	}

	refused := []struct {
		in      string
		typ     ProblemType
		pointer string
		// beyondSchema marks refusals the schema cannot express; every
		// other refusal must be one the schema makes too.
		beyondSchema bool
	}{
		{`{"vin":`, MalformedJSON, "", false},
		{``, MalformedJSON, "", false},
		{`["x"]`, InvalidRequest, "", false},
		{`"x"`, InvalidRequest, "", false},
		{`{}`, InvalidRequest, "/vin", false},
		{`{"vin":null}`, InvalidRequest, "/vin", false},
		{`{"vin":17}`, InvalidRequest, "/vin", false},
		{`{"vin":""}`, InvalidRequest, "/vin", false},
		{`{"vin":"   "}`, InvalidRequest, "/vin", true},
		{`{"vin":"1HGCM82633A004352A"}`, InvalidRequest, "/vin", true},
		{`{"vin":"` + vin17 + `A"}`, InvalidRequest, "/vin", true},
		{`{"vin":"x","VIN":"y"}`, InvalidRequest, "/VIN", false},
		{`{"vin":"x","a/b":1,"z":2}`, InvalidRequest, "/a~1b", false},
		{`{"vin":"x","id":1}`, InvalidRequest, "/id", false},
		{`{"vin":"x","model_year":2147483648}`, InvalidRequest, "/model_year", false},
		{`{"vin":"x","model_year":2003.5}`, InvalidRequest, "/model_year", false},
		{`{"vin":"x","model_year":"2003"}`, InvalidRequest, "/model_year", false},
		{`{"vin":"x","full":"true"}`, InvalidRequest, "/full", false},
		{`{"vin":"x","full":null}`, InvalidRequest, "/full", false},
		{`{"vin":"x","shape":"VPIC"}`, InvalidRequest, "/shape", false},
		{`{"vin":"x","shape":null}`, InvalidRequest, "/shape", false},
		{`{"vin":"x","now":"2026-03-01T10:00Z"}`, InvalidRequest, "/now", false},
		{`{"vin":"x","now":"2026-03-01 10:00:00Z"}`, InvalidRequest, "/now", false},
		{`{"vin":"x","now":1700000000}`, InvalidRequest, "/now", false},
		{`{"vin":"x","now":"2026-02-30"}`, InvalidRequest, "/now", true},
		{`{"vin":"x","now":"2026-03-01T24:00:00Z"}`, InvalidRequest, "/now", true},
		{`{"vin":"x","now":"0000-01-01T00:00:00+01:00"}`, InvalidRequest, "/now", true},
	}
	for _, c := range refused {
		_, err := ParseRequest([]byte(c.in))
		if err == nil {
			t.Errorf("%s: accepted", c.in)
			continue
		}
		problemOf(t, err, c.typ, c.pointer)
		if c.typ == InvalidRequest && !c.beyondSchema && validate(decodeRequest, []byte(c.in)) == nil {
			t.Errorf("%s: refused, but the schema accepts it", c.in)
		}
	}
}

func TestParseBatch(t *testing.T) {
	reqs, err := ParseBatch([]byte(`[{"vin":"a"},{"vin":"b","full":true}]`))
	if err != nil || len(reqs) != 2 || reqs[1] != (Request{VIN: "b", Full: true}) {
		t.Errorf("got %+v, %v", reqs, err)
	}
	many := "[" + strings.Repeat(`{"vin":"a"},`, MaxBatch) + `{"vin":"a"}]`
	for _, c := range []struct {
		in      string
		typ     ProblemType
		pointer string
	}{
		{`[`, MalformedJSON, ""},
		{`{"vin":"a"}`, InvalidRequest, ""},
		{`[]`, InvalidRequest, ""},
		{many, TooLarge, ""},
		{`[{"vin":"a"},{"vin":"b"},{"vin":""}]`, InvalidRequest, "/2/vin"},
		{`[{"vin":"a"},"b"]`, InvalidRequest, "/1"},
		{`[{"vin":"a","now":"soon"}]`, InvalidRequest, "/0/now"},
		{`["` + strings.Repeat("a", MaxBody) + `"]`, TooLarge, ""},
	} {
		_, err := ParseBatch([]byte(c.in))
		problemOf(t, err, c.typ, c.pointer)
	}
	if _, err := ParseBatch([]byte("[" + strings.Repeat(`{"vin":"a"},`, MaxBatch-1) + `{"vin":"a"}]`)); err != nil {
		t.Errorf("%d requests: %v", MaxBatch, err)
	}
}

func TestParseStdio(t *testing.T) {
	id, r, err := ParseStdio([]byte(`{"id":{"n":[1]},"vin":"a"}`))
	if err != nil || string(id) != `{"n":[1]}` || r != (Request{VIN: "a"}) {
		t.Errorf("got %s, %+v, %v", id, r, err)
	}
	// A refused line still yields its id, so the error line can echo it.
	id, _, err = ParseStdio([]byte(`{"id":"q7","vin":"a","colour":"red"}`))
	problemOf(t, err, InvalidRequest, "/colour")
	if string(id) != `"q7"` {
		t.Errorf("refused line's id %s, want \"q7\"", id)
	}
	id, _, err = ParseStdio([]byte(`{"id":"q8","vin":`))
	problemOf(t, err, MalformedJSON, "")
	if id != nil {
		t.Errorf("malformed line's id %s, want none", id)
	}
	if _, _, err := ParseStdio([]byte(`{"vin":"a"}`)); err != nil {
		t.Errorf("no id: %v", err)
	}
	stdioRequest := schemaDef(t, "StdioRequest")
	for _, line := range []string{`{"id":{"n":[1]},"vin":"a"}`, `{"vin":"a"}`} {
		if err := validate(stdioRequest, []byte(line)); err != nil {
			t.Errorf("%s: the schema refuses it: %v", line, err)
		}
	}
}

func TestParseQuery(t *testing.T) {
	r, err := ParseQuery("1HGCM82633A004352", url.Values{"model_year": {"2003"}, "full": {"true"}, "shape": {"nitrovin"}, "now": {"2026-11-08"}})
	want := Request{VIN: "1HGCM82633A004352", ModelYear: Value[int32](2003), Full: true, Shape: ShapeNitrovin, Now: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)}
	if err != nil || r != want {
		t.Errorf("got %+v, %v", r, err)
	}
	for _, c := range []struct {
		vin     string
		q       url.Values
		pointer string
	}{
		{"x", url.Values{"fll": {"true"}}, "/fll"},
		{"x", url.Values{"full": {"1"}}, "/full"},
		{"x", url.Values{"full": {"true", "false"}}, "/full"},
		{"x", url.Values{"model_year": {"2147483648"}}, "/model_year"},
		{"x", url.Values{"model_year": {""}}, "/model_year"},
		{"x", url.Values{"shape": {"flat"}}, "/shape"},
		{"x", url.Values{"now": {"yesterday"}}, "/now"},
		{" ", nil, "/vin"},
	} {
		_, err := ParseQuery(c.vin, c.q)
		problemOf(t, err, InvalidRequest, c.pointer)
	}
}

func TestAsProblem(t *testing.T) {
	inner := NewProblem(InvalidRequest, "/vin", "empty")
	if got := AsProblem(errors.Join(errors.New("context"), inner)); got != inner {
		t.Errorf("wrapped problem: got %+v", got)
	}
	problemOf(t, AsProblem(errors.New("index out of range")), Internal, "")
}
