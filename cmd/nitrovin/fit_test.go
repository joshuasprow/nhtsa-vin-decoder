package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// fitter is the Go API the CLI's fit is compared with: the embedded data
// with testdata/vocab and the synthetic catalog loaded beside it.
func fitter(t *testing.T) *nitrovin.Translator {
	t.Helper()
	fixtureData(t)
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(fixture, fitmentFixture, stockFixture)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// fits are tr's answers to reqs.
func fits(t *testing.T, tr *nitrovin.Translator, reqs ...contract.FitRequest) []any {
	t.Helper()
	out := make([]any, len(reqs))
	for i, req := range reqs {
		a, err := tr.Fit(context.Background(), req)
		if err != nil {
			t.Fatalf("Fit(%+v): %v", req, err)
		}
		out[i] = a
	}
	return out
}

// TestFit: `fit <vin>…` and `fit -` print, byte for byte, what the Go API
// answers for the same requests, in input order.
func TestFit(t *testing.T) {
	tr := fitter(t)
	clock, _ := contract.ParseNow(now)
	vocabArgs := []string{"--vocab", fixture, "--vocab", fitmentFixture, "--vocab", stockFixture, "fit", "--now", now}
	mirror := &contract.FitPart{Spine: "part-types", ID: "mirror"}
	for _, tc := range []struct {
		args []string
		reqs []contract.FitRequest
	}{
		{[]string{ram2010}, []contract.FitRequest{{From: contract.FitFrom{VIN: ram2010}, Now: clock}}},
		{[]string{"--part", "part-types:mirror", "--side", "left", ram2010, ram2015}, []contract.FitRequest{
			{From: contract.FitFrom{VIN: ram2010}, Part: mirror, Side: "left", Now: clock},
			{From: contract.FitFrom{VIN: ram2015}, Part: mirror, Side: "left", Now: clock},
		}},
		{[]string{"--related", "--to", "yard", "--limit", "1", ram2010}, []contract.FitRequest{
			{From: contract.FitFrom{VIN: ram2010}, Related: true, To: []string{"yard"}, Limit: 1, Now: clock},
		}},
		{[]string{"--model-year", "2010", "--part", "part-types:brake-rotor-front", ram2015}, []contract.FitRequest{
			{From: contract.FitFrom{VIN: ram2015}, ModelYear: contract.Value[int32](2010), Part: &contract.FitPart{Spine: "part-types", ID: "brake-rotor-front"}, Now: clock},
		}},
		{[]string{"--part", "part-types:brake-rotor-front", "--stock", "shelf,yard", "--location", "north", "--location", "south", ram2010}, []contract.FitRequest{
			{From: contract.FitFrom{VIN: ram2010}, Part: &contract.FitPart{Spine: "part-types", ID: "brake-rotor-front"}, Stock: []string{"shelf", "yard"}, Location: []string{"north", "south"}, Now: clock},
		}},
	} {
		r := cli(t, nil, "", slices.Concat(vocabArgs, tc.args)...)
		if w := jsonLines(t, fits(t, tr, tc.reqs...)...); r.exit != 0 || r.stderr != "" || r.stdout != w {
			t.Errorf("fit %q: exit %d, stderr %q, stdout %q; want %q", tc.args, r.exit, r.stderr, r.stdout, w)
		}
	}

	// -: one request per line; a line's own to, side, model_year and now
	// beat the flags', a line without one takes the flag's.
	stdin := `{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}}}` + "\r\n\n" +
		`{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"part":{"spine":"part-types","id":"mirror"},"side":"right","model_year":null}` + "\n" +
		`{"from":{"model":1850},"related":true,"to":["yard"],"model_year":2010,"now":"2026-01-02"}` + "\n"
	jan2, _ := contract.ParseNow("2026-01-02")
	reqs := []contract.FitRequest{
		{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: "brake-rotor-front", Number: "BR-1999"}}, To: []string{"yard"}, Side: "left", ModelYear: contract.Value[int32](2011), Now: clock},
		{From: contract.FitFrom{Vocabulary: "catalog-models", Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}, Part: mirror, Side: "right", To: []string{"yard"}, ModelYear: contract.Value[int32](2011), Now: clock},
		{From: contract.FitFrom{SpineRef: contract.SpineRef{Model: 1850}}, Related: true, To: []string{"yard"}, Side: "left", ModelYear: contract.Value[int32](2010), Now: jan2},
	}
	r := cli(t, nil, stdin, slices.Concat(vocabArgs, []string{"--to", "yard", "--side", "left", "--model-year", "2011", "-"})...)
	if w := jsonLines(t, fits(t, tr, reqs...)...); r.exit != 0 || r.stderr != "" || r.stdout != w {
		t.Errorf("fit -: exit %d, stderr %q, stdout %q; want %q", r.exit, r.stderr, r.stdout, w)
	}

	// --part, --related and --limit fill a line that leaves them out; a
	// line's own beat them, and a fitment line takes no part or related.
	stdin = `{"from":"` + ram2010 + `"}` + "\n" +
		`{"from":"` + ram2010 + `","related":true,"limit":5}` + "\n" +
		`{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}}}` + "\n"
	reqs = []contract.FitRequest{
		{From: contract.FitFrom{VIN: ram2010}, Part: mirror, Limit: 1, Now: clock},
		{From: contract.FitFrom{VIN: ram2010}, Related: true, Limit: 5, Now: clock},
		{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: "brake-rotor-front", Number: "BR-1999"}}, Limit: 1, Now: clock},
	}
	r = cli(t, nil, stdin, slices.Concat(vocabArgs, []string{"--part", "part-types:mirror", "--limit", "1", "-"})...)
	if w := jsonLines(t, fits(t, tr, reqs...)...); r.exit != 0 || r.stderr != "" || r.stdout != w {
		t.Errorf("fit --part --limit -: exit %d, stderr %q, stdout %q; want %q", r.exit, r.stderr, r.stdout, w)
	}

	// No catalog loaded: the status no-catalog, exit 0.
	r = cli(t, nil, "", "--vocab", fixture, "fit", "--now", now, ram2010)
	if r.exit != 0 || r.stderr != "" || !strings.HasPrefix(r.stdout, `{"status":"no-catalog","models":[],"part_types":[],"total":0,`) {
		t.Errorf("fit without a catalog: exit %d, stderr %q, stdout %q", r.exit, r.stderr, r.stdout)
	}
}

// TestFitRefused: a refused argument exits 2 with nothing on stdout; a
// refused stdin line stops the run with exit 1; a mistake on the command
// line is a usage error.
func TestFitRefused(t *testing.T) {
	fixtureData(t)
	vocabArgs := []string{"--vocab", fixture, "--vocab", fitmentFixture, "--vocab", stockFixture, "fit"}
	for _, tc := range []struct {
		args            []string
		detail, pointer string
	}{
		{[]string{"fit", ram2010, "1D7RV1GT5AS1234567"}, "argument 2: ", "/from"},
		{slices.Concat(vocabArgs, []string{"--part", "parts:brake-rotor", ram2010}), "argument 1: ", "/part/spine"},
		{slices.Concat(vocabArgs, []string{"--related", "--to", "listing", ram2010}), "argument 1: ", "/to/0"},
	} {
		r := cli(t, nil, "", tc.args...)
		p := problem(t, r)
		if r.exit != 2 || r.stdout != "" || p.Type != contract.InvalidRequest || p.Pointer != tc.pointer || !strings.HasPrefix(p.Detail, tc.detail) {
			t.Errorf("nitrovin %q: exit %d, stdout %q, problem %+v; want exit 2, nothing on stdout, an invalid-request at %s naming %q", tc.args, r.exit, r.stdout, p, tc.pointer, tc.detail)
		}
	}

	tr := fitter(t)
	clock, _ := contract.ParseNow(now)
	first := contract.FitRequest{From: contract.FitFrom{VIN: ram2010}, Now: clock}
	stdin := `{"from":"` + ram2010 + `"}` + "\n" + `{"from":{"fitment":{"type":"mirror","number":"MR-1001"}},"related":true}` + "\n" + `{"from":"` + ram2015 + `"}`
	r := cli(t, nil, stdin, slices.Concat(vocabArgs, []string{"--now", now, "-"})...)
	p := problem(t, r)
	if w := jsonLines(t, fits(t, tr, first)...); r.exit != 1 || r.stdout != w || p.Type != contract.InvalidRequest || p.Pointer != "/related" || !strings.HasPrefix(p.Detail, "line 2: ") {
		t.Errorf("fit - : exit %d, stdout %q, problem %+v; want exit 1, the first answer, an invalid-request at /related naming line 2", r.exit, r.stdout, p)
	}

	for _, args := range [][]string{
		{"fit"},
		{"fit", ram2010, "-"},
		{"fit", "--part", "mirror", ram2010},
		{"fit", "--side", "front", ram2010},
		{"fit", "--limit", "0", ram2010},
		{"fit", "--to", "", ram2010},
		{"fit", "--now", "yesterday", ram2010},
		{"fit", "--after", "WyJjYW5kaWRhdGVzIiwxXQ", "-"},
		{"fit", "--stock", "bin", ram2010},
		{"fit", "--location", "", ram2010},
	} {
		usageMistake(t, args...)
	}
}
