package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// VINs the fixture translates: Dodge (476) Ram (1938) 2010; Ram (496)
// 1500 (13620) 2015.
const (
	ram2010 = "1D7RV1GT5AS123456"
	ram2015 = "1C6RR7LT9FS123456"
)

// translator is the Go API the CLI's translate and list are compared with:
// the embedded data with the fixture loaded beside it, as --vocab loads it.
func translator(t *testing.T) *nitrovin.Translator {
	t.Helper()
	fixtureData(t)
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// jsonLines is vs as the Go module marshals them, one compact line each.
func jsonLines(t *testing.T, vs ...any) string {
	t.Helper()
	var b strings.Builder
	for _, v := range vs {
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

// translations are tr's answers to reqs.
func translations(t *testing.T, tr *nitrovin.Translator, reqs ...contract.TranslateRequest) []any {
	t.Helper()
	out := make([]any, len(reqs))
	for i, req := range reqs {
		a, err := tr.Translate(req)
		if err != nil {
			t.Fatalf("Translate(%+v): %v", req, err)
		}
		out[i] = a
	}
	return out
}

// usageMistake checks that args are a mistake on the command line: exit 2,
// the message and the usage on stderr, nothing on stdout.
func usageMistake(t *testing.T, args ...string) {
	t.Helper()
	r := cli(t, nil, "", args...)
	if r.exit != 2 || r.stdout != "" || !strings.HasPrefix(r.stderr, "nitrovin: ") || !strings.Contains(r.stderr, "\nusage: nitrovin "+args[0]) {
		t.Errorf("nitrovin %q: exit %d, stdout %q, stderr %q; want exit 2, the mistake and the usage on stderr only", args, r.exit, r.stdout, r.stderr)
	}
}

// TestTranslate: `translate <vin>…` and `translate -` print, byte for
// byte, what the Go API answers for the same requests, in input order.
func TestTranslate(t *testing.T) {
	tr := translator(t)
	clock, _ := contract.ParseNow(now)
	vocabArgs := []string{"--vocab", fixture, "translate", "--now", now}

	yard := contract.TranslateRequest{To: []string{"yard"}, Now: clock}
	from := func(base contract.TranslateRequest, f contract.TranslateFrom) contract.TranslateRequest {
		base.From = f
		return base
	}
	for _, tc := range []struct {
		args []string
		reqs []contract.TranslateRequest
	}{
		{[]string{"--to", "yard", ram2010, ram2015}, []contract.TranslateRequest{
			from(yard, contract.TranslateFrom{VIN: ram2010}),
			from(yard, contract.TranslateFrom{VIN: ram2015}),
		}},
		// One VIN off a terminal: one compact line.
		{[]string{ram2010, "--model-year", "2009"}, []contract.TranslateRequest{
			{From: contract.TranslateFrom{VIN: ram2010}, ModelYear: contract.Value[int32](2009), Now: clock},
		}},
		{[]string{"--to", "yard", "--to", "yard", ram2015}, []contract.TranslateRequest{
			{From: contract.TranslateFrom{VIN: ram2015}, To: []string{"yard", "yard"}, Now: clock},
		}},
	} {
		r := cli(t, nil, "", slices.Concat(vocabArgs, tc.args)...)
		if w := jsonLines(t, translations(t, tr, tc.reqs...)...); r.exit != 0 || r.stderr != "" || r.stdout != w {
			t.Errorf("translate %q: exit %d, stderr %q, stdout %q; want %q", tc.args, r.exit, r.stderr, r.stdout, w)
		}
	}

	// -: one request per line; a line's own to, model_year and now beat
	// the flags', and a line without one takes the flag's. A trailing \r
	// is dropped, blank lines are skipped.
	stdin := `{"from":"` + ram2010 + `"}` + "\r\n" +
		"\n   \n" +
		`{"from":{"vocabulary":"yard","entry":{"make":"dodge","model":"RAM 1500"}},"vin":"` + ram2015 + `","model_year":null}` + "\n" +
		`{"from":{"ext":"ext:classics/corvair"},"to":["yard"],"model_year":1965,"now":"2026-01-02"}` + "\n" +
		`{"from":{"spine":"parts","id":"brake-pads"},"to":["listing"]}`
	jan2, _ := contract.ParseNow("2026-01-02")
	flagged := contract.TranslateRequest{To: []string{"yard"}, ModelYear: contract.Value[int32](2011), Now: clock}
	reqs := []contract.TranslateRequest{
		from(flagged, contract.TranslateFrom{VIN: ram2010}),
		{From: contract.TranslateFrom{Vocabulary: "yard", Entry: map[string]string{"make": "dodge", "model": "RAM 1500"}}, VIN: ram2015, To: flagged.To, ModelYear: flagged.ModelYear, Now: clock},
		{From: contract.TranslateFrom{SpineRef: contract.SpineRef{Ext: "ext:classics/corvair"}}, To: []string{"yard"}, ModelYear: contract.Value[int32](1965), Now: jan2},
		{From: contract.TranslateFrom{SpineRef: contract.SpineRef{Spine: "parts", ID: "brake-pads"}}, To: []string{"listing"}, ModelYear: flagged.ModelYear, Now: clock},
	}
	r := cli(t, nil, stdin, slices.Concat(vocabArgs, []string{"--to", "yard", "--model-year", "2011", "-"})...)
	if w := jsonLines(t, translations(t, tr, reqs...)...); r.exit != 0 || r.stderr != "" || r.stdout != w {
		t.Errorf("translate -: exit %d, stderr %q, stdout %q; want %q", r.exit, r.stderr, r.stdout, w)
	}
}

// TestTranslateRefused: a refused argument exits 2 with nothing on stdout,
// a VIN before any data is loaded and a --to the loaded files refuse after;
// a refused stdin line stops the run with exit 1, the answers before it
// complete; a mistake on the command line is a usage error.
func TestTranslateRefused(t *testing.T) {
	fixtureData(t)
	for _, tc := range []struct {
		args            []string
		detail, pointer string
	}{
		{[]string{"translate", ram2010, "1D7RV1GT5AS1234567"}, "argument 2: ", "/from"},
		{[]string{"translate", "--to", "yard", "  "}, "argument 1: ", "/from"},
		{[]string{"--vocab", fixture, "translate", "--to", "yard,listing", ram2010}, "argument 1: ", "/to/1"},
		{[]string{"--vocab", fixture, "translate", "--to", "nope", ram2010, ram2015}, "argument 1: ", "/to/0"},
		{[]string{"translate", "--to", "yard", ram2010}, "argument 1: ", "/to/0"}, // nothing loaded
	} {
		r := cli(t, nil, "", tc.args...)
		p := problem(t, r)
		if r.exit != 2 || r.stdout != "" || p.Type != contract.InvalidRequest || p.Pointer != tc.pointer || !strings.HasPrefix(p.Detail, tc.detail) {
			t.Errorf("nitrovin %q: exit %d, stdout %q, problem %+v; want exit 2, nothing on stdout, an invalid-request at %s naming %q", tc.args, r.exit, r.stdout, p, tc.pointer, tc.detail)
		}
	}

	tr := translator(t)
	clock, _ := contract.ParseNow(now)
	first := contract.TranslateRequest{From: contract.TranslateFrom{VIN: ram2010}, To: []string{"yard"}, Now: clock}
	for _, tc := range []struct {
		stdin   string
		printed []contract.TranslateRequest
		typ     contract.ProblemType
		detail  string
		pointer string
	}{
		{`{"from":"` + ram2010 + `"}` + "\n\n" + `{"from":` + "\n" + `{"from":"` + ram2015 + `"}`, []contract.TranslateRequest{first}, contract.MalformedJSON, "line 3: ", ""},
		{`{"from":"` + ram2010 + `"}` + "\n" + `{"from":"` + ram2015 + `","to":["listing"]}` + "\n", []contract.TranslateRequest{first}, contract.InvalidRequest, "line 2: ", "/to/0"},
		{`{"from":{"vocabulary":"yard","entry":{"make":"Dodge"}}}`, nil, contract.InvalidRequest, "line 1: ", "/from/entry"},
		{`{"from":"` + ram2010 + `","colour":"red"}`, nil, contract.InvalidRequest, "line 1: ", "/colour"},
		{"\n" + `"` + ram2010 + `"`, nil, contract.InvalidRequest, "line 2: ", ""},
	} {
		r := cli(t, nil, tc.stdin, "--vocab", fixture, "translate", "--now", now, "--to", "yard", "-")
		p := problem(t, r)
		if w := jsonLines(t, translations(t, tr, tc.printed...)...); r.exit != 1 || r.stdout != w || p.Type != tc.typ || p.Pointer != tc.pointer || !strings.HasPrefix(p.Detail, tc.detail) {
			t.Errorf("translate - <<< %q: exit %d, stdout %q, problem %+v; want exit 1, stdout %q, a %s at %q naming %q", tc.stdin, r.exit, r.stdout, p, w, tc.typ, tc.pointer, tc.detail)
		}
	}

	for _, args := range [][]string{
		{"translate"},
		{"translate", ram2010, "-"},
		{"translate", "--to", "yard,,classics", ram2010},
		{"translate", "--to", "", ram2010},
		{"translate", "--model-year", "2010.5", ram2010},
		{"translate", "--now", "yesterday", ram2010},
		{"translate", ram2010, "--bogus"},
	} {
		usageMistake(t, args...)
	}
}

// TestList: `list [of]` prints, byte for byte, what the Go API answers for
// the same request; its pages chain through --after.
func TestList(t *testing.T) {
	tr := translator(t)
	page := func(req contract.ListRequest) contract.List {
		t.Helper()
		l, err := tr.List(req)
		if err != nil {
			t.Fatalf("List(%+v): %v", req, err)
		}
		return l
	}
	first := page(contract.ListRequest{Of: "yard", Limit: 2})
	if first.Next == "" {
		t.Fatal("yard fits one page of 2: the fixture no longer pages")
	}
	for _, tc := range []struct {
		args []string
		req  contract.ListRequest
	}{
		{nil, contract.ListRequest{}},
		{[]string{"yard", "--limit", "2"}, contract.ListRequest{Of: "yard", Limit: 2}},
		{[]string{"--after", first.Next, "yard", "--limit", "2"}, contract.ListRequest{Of: "yard", Limit: 2, After: first.Next}},
		{[]string{"yard", "--where", "make=  DODGE "}, contract.ListRequest{Of: "yard", Where: map[string]string{"make": "  DODGE "}}},
		{[]string{"vpic", "--where", "make=476", "--limit", "3"}, contract.ListRequest{Of: "vpic", Where: map[string]string{"make": "476"}, Limit: 3}},
		{[]string{"vpic"}, contract.ListRequest{Of: "vpic"}},
		{[]string{"parts"}, contract.ListRequest{Of: "parts"}},
		{[]string{"classics"}, contract.ListRequest{Of: "classics"}},
	} {
		r := cli(t, nil, "", slices.Concat([]string{"--vocab", fixture, "list"}, tc.args)...)
		if w := jsonLines(t, page(tc.req)); r.exit != 0 || r.stderr != "" || r.stdout != w {
			t.Errorf("list %q: exit %d, stderr %q, stdout %q; want %q", tc.args, r.exit, r.stderr, r.stdout, w)
		}
	}
}

// TestListRefused: a list request the loaded files refuse exits 2 with
// nothing on stdout; a mistake on the command line is a usage error.
func TestListRefused(t *testing.T) {
	fixtureData(t)
	for _, tc := range []struct {
		args    []string
		pointer string
	}{
		{[]string{"nope"}, "/of"},
		{[]string{"yard", "--where", "colour=red"}, "/where/colour"},
		{[]string{"parts", "--where", "id=brake-pads"}, "/where/id"},
		{[]string{"vpic", "--where", "make=dodge"}, "/where/make"},
		{[]string{"--limit", "5"}, "/limit"},
		{[]string{"yard", "--after", "bogus"}, "/after"},
	} {
		r := cli(t, nil, "", slices.Concat([]string{"--vocab", fixture, "list"}, tc.args)...)
		p := problem(t, r)
		if r.exit != 2 || r.stdout != "" || p.Type != contract.InvalidRequest || p.Pointer != tc.pointer {
			t.Errorf("list %q: exit %d, stdout %q, problem %+v; want exit 2, nothing on stdout, an invalid-request at %s", tc.args, r.exit, r.stdout, p, tc.pointer)
		}
	}

	for _, args := range [][]string{
		{"list", "yard", "parts"},
		{"list", ""},
		{"list", "yard", "--where", "make"},
		{"list", "yard", "--where", "=dodge"},
		{"list", "yard", "--where", "make=a", "--where", "make=b"},
		{"list", "yard", "--limit", "0"},
		{"list", "yard", "--limit", "1001"},
		{"list", "yard", "--limit", "ten"},
		{"list", "yard", "--after", ""},
	} {
		usageMistake(t, args...)
	}
}
