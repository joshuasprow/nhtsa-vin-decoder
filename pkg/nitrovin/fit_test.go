package nitrovin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// fitNow is the clock of every fit a test asks for.
var fitNow = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// catalogTranslator loads testdata/vocab and the synthetic catalog in
// testdata/vocab/fitment beside the embedded data month.
func catalogTranslator(t *testing.T) *Translator {
	t.Helper()
	files, err := vocab.Open("../../testdata/vocab", "../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(embeddedDecoder(t), files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// fit parses request, a FitRequest's JSON, fits it at fitNow, and checks
// the answer against the schema.
func fit(t *testing.T, tr *Translator, request string) (contract.Fit, error) {
	t.Helper()
	req, err := contract.ParseFitRequest([]byte(request))
	if err != nil {
		t.Fatalf("%s: %v", request, err)
	}
	req.Now = fitNow
	f, err := tr.Fit(context.Background(), req)
	if err == nil {
		conforms(t, "Fit", f)
	}
	return f, err
}

// vehicleText writes a catalog vehicle as its fields joined by "/", or its
// spine entry.
func vehicleText(v contract.CatalogVehicle) string {
	if v.Entry != nil {
		return fields(v.Entry)
	}
	return ref(contract.SpineRef{Make: v.Make, Model: v.Model, Ext: v.Ext})
}

// yearsText writes a year range as 2009-2012, 2013- or -.
func yearsText(first, last contract.Null[int32]) string {
	s := ""
	if first.Valid {
		s = fmt.Sprint(first.V)
	}
	s += "-"
	if last.Valid {
		s += fmt.Sprint(last.V)
	}
	return s
}

func viasText(vs []contract.Via) string {
	var out []string
	for _, v := range vs {
		out = append(out, ref(v.SpineRef))
	}
	if len(out) == 0 {
		return ""
	}
	return " via " + strings.Join(out, ",")
}

func namesText(n contract.FitNames) string {
	var out []string
	for _, s := range n.Spine {
		out = append(out, ref(s.SpineRef))
	}
	for _, to := range n.To {
		for _, e := range to.Entries {
			out = append(out, to.Vocabulary+":"+fields(e.Entry)+unconfirmedText(e.Unconfirmed))
		}
	}
	return " → " + strings.Join(out, " ")
}

// fitSummary writes a fit as lines: its status; each model; then the
// answer's list, an item a line (an application, a redirect, indented
// under its candidate); then the total, and "next" when there is a page
// after.
func fitSummary(f contract.Fit) []string {
	out := []string{"status " + string(f.Status)}
	for _, m := range f.Models {
		out = append(out, "model "+vehicleText(m.Vehicle)+viasText(m.Via)+unconfirmedText(m.Unconfirmed))
	}
	if f.Fitment != nil {
		s := fmt.Sprintf("fitment %s %s %s %s", f.Fitment.Type, f.Fitment.Number, f.Fitment.Side, f.Reached)
		if f.Retired != nil {
			s += fmt.Sprintf(" from %s %q", f.Retired.Number, f.Retired.Note)
		}
		out = append(out, s)
	}
	for _, p := range f.PartTypes {
		out = append(out, fmt.Sprintf("part-type %s %q %d %s", p.Type, p.Name, p.Count, p.Reached))
	}
	for _, c := range f.Candidates {
		out = append(out, fmt.Sprintf("candidate %s %s %s %s", c.Fitment.Type, c.Fitment.Number, c.Fitment.Side, c.Reached))
		for _, a := range c.Applications {
			out = append(out, fmt.Sprintf("  %s %s %q%s%s", vehicleText(a.Vehicle), yearsText(a.First, a.Last), a.Options, viasText(a.Via), unconfirmedText(a.Unconfirmed)))
		}
		for _, r := range c.Redirects {
			out = append(out, fmt.Sprintf("  redirect %s %s %s→%s %q", vehicleText(r.Vehicle), yearsText(r.First, r.Last), r.From, r.To, r.Options))
		}
	}
	for _, v := range f.Vehicles {
		out = append(out, fmt.Sprintf("vehicle %s %s %q%s", vehicleText(v.Vehicle), yearsText(v.First, v.Last), v.Options, namesText(v.FitNames)))
	}
	for _, r := range f.Related {
		var shared []string
		for _, p := range r.PartTypes {
			shared = append(shared, fmt.Sprintf("%s:%d", p.Type, p.Count))
		}
		out = append(out, fmt.Sprintf("related %s %s %d %s%s", vehicleText(r.Vehicle), yearsText(r.First, r.Last), r.Shared, strings.Join(shared, ","), namesText(r.FitNames)))
	}
	out = append(out, fmt.Sprintf("total %d", f.Total))
	if f.Next != "" {
		out = append(out, "next")
	}
	return out
}

// TestFit: the synthetic catalog's documented examples (docs/api/README.md,
// docs/vocabularies.md), each kind of answer, as the Go module gives them.
func TestFit(t *testing.T) {
	tr := catalogTranslator(t)
	const (
		ram       = `{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}}`
		silverado = `{"vocabulary":"catalog-models","entry":{"make":"Chevrolet","model":"Silverado 1500"}}`
		ramVIN    = `"1D7RV1GT5AS123456"` // a 2010 Ram 1500 4WD, Series DS (1500)
		// A 2007 Silverado whose Series, 1/2 Ton, is not the 1500 that
		// BR-2001's application to it asks for.
		silveradoVIN = `"1GCEK19J07Z123456"`
	)
	for _, c := range []struct {
		name, request string
		want          []string
	}{
		{"part types", `{"from":` + ram + `,"model_year":2010}`, []string{
			"status found",
			"model Dodge/Ram 1500",
			`part-type brake-rotor-front "Brake Rotor, Front" 1 direct`,
			`part-type engine "Engine" 1 direct`,
			`part-type mirror "Door Mirror" 2 direct`,
			"total 3",
		}},
		{"part types, every year", `{"from":` + ram + `}`, []string{
			"status found",
			"model Dodge/Ram 1500",
			`part-type brake-rotor-front "Brake Rotor, Front" 2 direct`,
			`part-type engine "Engine" 1 direct`,
			`part-type mirror "Door Mirror" 2 direct`,
			"total 3",
		}},
		{"part types through a redirect", `{"from":` + silverado + `,"model_year":2010}`, []string{
			"status found",
			"model Chevrolet/Silverado 1500",
			`part-type brake-rotor-front "Brake Rotor, Front" 1 direct`,
			`part-type brake-rotor-rear "Brake Rotor, Rear" 1 redirect`,
			"total 2",
		}},
		// A VIN's decode settles the 4WD qualifier, so nothing is
		// unconfirmed; the side keeps the left mirror.
		{"candidates from a VIN", `{"from":` + ramVIN + `,"part":{"spine":"part-types","id":"mirror"},"side":"left"}`, []string{
			"status found",
			"model Dodge/Ram 1500 via model:1938",
			"candidate mirror MR-1001 left direct",
			`  Dodge/Ram 1500 2009-2012 "w/o towing package" via model:1938`,
			`  Dodge/Ram 1500 2009-2012 "4WD, w/ towing package" via model:1938`,
			"total 1",
		}},
		// From a vPIC model and a year, Drive Type (15) is not checked.
		{"candidates from a model", `{"from":{"model":1938},"model_year":2010,"part":{"spine":"part-types","id":"mirror"}}`, []string{
			"status found",
			"model Dodge/Ram 1500 via model:1938",
			"candidate mirror MR-1001 left direct",
			`  Dodge/Ram 1500 2009-2012 "w/o towing package" via model:1938`,
			`  Dodge/Ram 1500 2009-2012 "4WD, w/ towing package" via model:1938 [15]`,
			"candidate mirror MR-1001 right direct",
			`  Dodge/Ram 1500 2009-2012 "w/o towing package" via model:1938`,
			"total 2",
		}},
		// No year: every year's applications, the model year unconfirmed.
		{"candidates in every year", `{"from":` + ram + `,"part":{"spine":"part-types","id":"brake-rotor-front"}}`, []string{
			"status found",
			"model Dodge/Ram 1500",
			"candidate brake-rotor-front BR-2001  direct",
			`  Dodge/Ram 1500 2009-2012 "" [model_year]`,
			"candidate brake-rotor-front BR-2002  direct",
			`  Dodge/Ram 1500 2013- "VIN T, 8th digit" [model_year]`,
			"total 2",
		}},
		{"candidates through a redirect", `{"from":` + silverado + `,"model_year":2010,"part":{"spine":"part-types","id":"brake-rotor-rear"}}`, []string{
			"status found",
			"model Chevrolet/Silverado 1500",
			"candidate brake-rotor-front BR-2001  redirect",
			`  Chevrolet/Silverado 1500 2007-2013 "w/ heavy duty brakes" [34]`,
			`  redirect Chevrolet/Silverado 1500 2007-2013 brake-rotor-rear→brake-rotor-front "rear rotors are listed with the front"`,
			"total 1",
		}},
		// A part vocabulary's code that covers both rotors: the front's
		// fitment, reached directly as well as through the redirect.
		{"candidates of a part vocabulary's entry", `{"from":` + silverado + `,"model_year":2010,"part":{"vocabulary":"part-codes","entry":{"code":"ROT"}}}`, []string{
			"status found",
			"model Chevrolet/Silverado 1500",
			"candidate brake-rotor-front BR-2001  direct",
			`  Chevrolet/Silverado 1500 2007-2013 "w/ heavy duty brakes" [34]`,
			"total 1",
		}},
		// The decode's Series rules BR-2001's application out.
		{"a qualifier the decode fails", `{"from":` + silveradoVIN + `,"part":{"spine":"part-types","id":"brake-rotor-rear"}}`, []string{
			"status no-fitment",
			"model Chevrolet/Silverado 1500 via model:1850",
			"total 0",
		}},
		{"vehicles of a retired number", `{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}}}`, []string{
			"status found",
			`fitment brake-rotor-front BR-2001  retired from BR-1999 "7.01 → 7.02"`,
			`vehicle Dodge/Ram 1500 2009-2012 "" → model:1938 make:476`,
			`vehicle Chevrolet/Silverado 1500 2007-2013 "w/ heavy duty brakes" → model:1850 make:467`,
			"total 2",
		}},
		{"vehicles of a fitment, to a vocabulary", `{"from":{"fitment":{"type":"mirror","number":"MR-1001","side":"right"}},"to":["yard"]}`, []string{
			"status found",
			"fitment mirror MR-1001 right direct",
			`vehicle Dodge/Ram 1500 2009-2012 "w/o towing package" → yard:Dodge/Other yard:Dodge/Ram 1500 [34] yard:Dodge/Ram 1500 4x4 [15 34]`,
			"total 1",
		}},
		{"related vehicles", `{"from":` + ramVIN + `,"related":true}`, []string{
			"status found",
			"model Dodge/Ram 1500 via model:1938",
			"related Chevrolet/Silverado 1500 2007-2013 1 brake-rotor-front:1 → model:1850 make:467",
			"total 1",
		}},
		{"related vehicles of one part type", `{"from":` + ram + `,"model_year":2010,"related":true,"part":{"spine":"part-types","id":"mirror"}}`, []string{
			"status no-fitment",
			"model Dodge/Ram 1500",
			"total 0",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := fit(t, tr, c.request)
			if err != nil {
				t.Fatal(err)
			}
			if got := fitSummary(f); !slices.Equal(got, c.want) {
				t.Errorf("%s:\n%s\nwant\n%s", c.request, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
			if f.Catalog == nil || *f.Catalog != (contract.FitCatalog{Name: "parts-catalog", Version: "2026.11", Release: "7.02", Spine: "part-types", Vehicles: "catalog-models"}) {
				t.Errorf("catalog %+v", f.Catalog)
			}
		})
	}
}

// TestFitStatus: nothing to fit is a status with an empty list, never a
// refusal, a missing catalog included.
func TestFitStatus(t *testing.T) {
	tr := catalogTranslator(t)
	files, err := vocab.Open("../../testdata/vocab")
	if err != nil {
		t.Fatal(err)
	}
	none, err := NewTranslator(embeddedDecoder(t), files...)
	if err != nil {
		t.Fatal(err)
	}
	ram := `{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}}`
	for _, c := range []struct {
		t       *Translator
		request string
		want    contract.FitStatus
	}{
		{tr, `{"from":{"vocabulary":"yard","entry":{"make":"Tucker","model":"48"}}}`, contract.FitUnknown},
		{tr, `{"from":{"model":99999999},"part":{"spine":"part-types","id":"mirror"}}`, contract.FitUnknown},
		{tr, `{"from":` + ram + `,"part":{"spine":"part-types","id":"wheel"}}`, contract.FitUnknown},
		{tr, `{"from":{"fitment":{"type":"mirror","number":"MR-1001"}}}`, contract.FitUnknown}, // MR-1001 has sides
		{tr, `{"from":{"make":476}}`, contract.FitNoVehicle},                                   // the catalog names models, not makes
		{tr, `{"from":{"vocabulary":"yard","entry":{"make":"Studebaker","model":"Lark"}},"related":true}`, contract.FitNoVehicle},
		{tr, `{"from":` + ram + `,"model_year":2005}`, contract.FitNoFitment},
		{tr, `{"from":` + ram + `,"model_year":2010,"part":{"spine":"part-types","id":"brake-rotor-rear"}}`, contract.FitNoFitment},
		{tr, `{"from":` + ram + `,"part":{"vocabulary":"part-codes","entry":{"code":"SEAT"}}}`, contract.FitNoFitment}, // unmapped
		{tr, `{"from":{"fitment":{"type":"engine","number":"EN-57"}},"model_year":2014}`, contract.FitNoFitment},
		{tr, `{"from":{"fitment":{"type":"engine","number":"EN-57"}},"side":"left"}`, contract.FitFound}, // no side, never ruled out
		{none, `{"from":"1D7RV1GT5AS123456"}`, contract.FitNoCatalog},
		{none, `{"from":` + ram + `,"part":{"vocabulary":"nope","entry":{"code":"MIR"}}}`, contract.FitNoCatalog},
		{none, `{"from":{"model":1938},"related":true,"to":["nope"]}`, contract.FitNoCatalog},
		{none, `{"from":{"fitment":{"type":"mirror","number":"MR-1001","side":"left"}}}`, contract.FitNoCatalog},
	} {
		f, err := fit(t, c.t, c.request)
		if err != nil {
			t.Errorf("%s: %v; want status %s", c.request, err, c.want)
			continue
		}
		if f.Status != c.want {
			t.Errorf("%s: status %s; want %s", c.request, f.Status, c.want)
		}
		lists := 0
		for _, n := range []int{len(f.PartTypes), len(f.Candidates), len(f.Vehicles), len(f.Related)} {
			lists += n
		}
		if empty := lists == 0 && f.Total == 0 && f.Next == ""; empty != (c.want != contract.FitFound) {
			t.Errorf("%s: %d items, total %d; want them only with status found", c.request, lists, f.Total)
		}
		if (f.Status == contract.FitNoCatalog) != (f.Catalog == nil) {
			t.Errorf("%s: catalog %+v with status %s", c.request, f.Catalog, f.Status)
		}
	}
	// The list asked for is present, empty: a client reads it whatever the
	// status.
	for request, list := range map[string]string{
		`{"from":"1D7RV1GT5AS123456"}`:                                             `"part_types":[]`,
		`{"from":"1D7RV1GT5AS123456","part":{"spine":"part-types","id":"mirror"}}`: `"candidates":[]`,
		`{"from":"1D7RV1GT5AS123456","related":true}`:                              `"related":[]`,
		`{"from":{"fitment":{"type":"mirror","number":"MR-1001","side":"left"}}}`:  `"vehicles":[]`,
	} {
		f, err := fit(t, none, request)
		if err != nil {
			t.Fatal(err)
		}
		if b := conforms(t, "Fit", f); !strings.Contains(string(b), `{"status":"no-catalog",`) || !strings.Contains(string(b), list) {
			t.Errorf("%s with no catalog: %s; want status no-catalog and %s", request, b, list)
		}
	}
}

// TestFitSide: a side keeps the fitments that can go there: its own, a
// part for both sides, and one that states no side.
func TestFitSide(t *testing.T) {
	tr := catalogTranslator(t)
	ram := `{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}}`
	for _, c := range []struct {
		part, side string
		want       []string
	}{
		{"mirror", "", []string{"MR-1001 left", "MR-1001 right"}},
		{"mirror", "left", []string{"MR-1001 left"}},
		{"mirror", "right", []string{"MR-1001 right"}},
		{"mirror", "both", nil},
		{"brake-rotor-front", "left", []string{"BR-2001 "}},
		{"brake-rotor-front", "both", []string{"BR-2001 "}},
	} {
		request := `{"from":` + ram + `,"model_year":2010,"part":{"spine":"part-types","id":"` + c.part + `"}`
		if c.side != "" {
			request += `,"side":"` + c.side + `"`
		}
		f, err := fit(t, tr, request+"}")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, cand := range f.Candidates {
			got = append(got, cand.Fitment.Number+" "+cand.Fitment.Side)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s on side %q: %v; want %v", c.part, c.side, got, c.want)
		}
	}
	// A part for both sides goes on the left: a catalog that has one.
	dir := t.TempDir()
	write(t, dir, "catalog.jsonl",
		`{"format":1,"kind":"catalog","name":"sides","version":"1","spine":"part-types","vehicles":"catalog-models"}`,
		`{"fitment":{"type":"mirror","number":"M-1","side":"both"},"applications":[{"vehicle":{"make":"Dodge","model":"Ram 1500"},"options":""}]}`,
		`{"fitment":{"type":"mirror","number":"M-2","side":"right"},"applications":[{"vehicle":{"make":"Dodge","model":"Ram 1500"},"options":""}]}`)
	files, err := vocab.Open("../../testdata/vocab/fitment/catalog-models.jsonl", "../../testdata/vocab/fitment/part-types.jsonl", dir)
	if err != nil {
		t.Fatal(err)
	}
	sides, err := NewTranslator(embeddedDecoder(t), files...)
	if err != nil {
		t.Fatal(err)
	}
	for side, want := range map[string][]string{"left": {"M-1 both"}, "right": {"M-2 right", "M-1 both"}, "both": {"M-1 both"}} {
		f, err := fit(t, sides, `{"from":`+ram+`,"part":{"spine":"part-types","id":"mirror"},"side":"`+side+`"}`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, cand := range f.Candidates {
			got = append(got, cand.Fitment.Number+" "+cand.Fitment.Side)
		}
		if !slices.Equal(got, want) {
			t.Errorf("side %s: %v; want %v", side, got, want)
		}
	}
}

// write writes lines to dir/name.
func write(t *testing.T, dir, name string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFitRefusals: only a malformed request is refused, with ADR 0018's
// refusals for what the loaded files decide, each at its pointer.
func TestFitRefusals(t *testing.T) {
	tr := catalogTranslator(t)
	ram := `{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}}`
	for _, c := range []struct{ request, pointer string }{
		// A part vocabulary, or a spine, that isn't the catalog's.
		{`{"from":` + ram + `,"part":{"vocabulary":"listing","entry":{"category":"Brake Pads"}}}`, "/part/vocabulary"},
		{`{"from":` + ram + `,"part":{"spine":"parts","id":"brake-rotor"}}`, "/part/spine"},
		{`{"from":` + ram + `,"part":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}}}`, "/part/vocabulary"},
		// An unknown vocabulary or spine.
		{`{"from":{"vocabulary":"nope","entry":{"make":"Dodge"}}}`, "/from/vocabulary"},
		{`{"from":` + ram + `,"part":{"vocabulary":"nope","entry":{"code":"MIR"}}}`, "/part/vocabulary"},
		{`{"from":` + ram + `,"part":{"spine":"nope","id":"mirror"}}`, "/part/spine"},
		{`{"from":` + ram + `,"related":true,"to":["yard","nope"]}`, "/to/1"},
		{`{"from":{"fitment":{"type":"engine","number":"EN-57"}},"to":["part-codes"]}`, "/to/0"},
		{`{"from":{"fitment":{"type":"engine","number":"EN-57"}},"to":["vpic"]}`, "/to/0"},
		{`{"from":{"vocabulary":"part-codes","entry":{"code":"MIR"}}}`, "/from/vocabulary"},
		// Entry fields other than the vocabulary's.
		{`{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge"}}}`, "/from/entry"},
		{`{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500","trim":"SLT"}}}`, "/from/entry/trim"},
		{`{"from":` + ram + `,"part":{"vocabulary":"part-codes","entry":{"kode":"MIR"}}}`, "/part/entry/kode"},
		// A cursor of another kind of answer.
		{`{"from":` + ram + `,"after":"WyJjYW5kaWRhdGVzIiwxXQ"}`, "/after"},
	} {
		_, err := fit(t, tr, c.request)
		p := contract.AsProblem(err)
		if err == nil || p.Type != contract.InvalidRequest || p.Pointer != c.pointer {
			t.Errorf("%s: %v; want #invalid-request at %s", c.request, err, c.pointer)
		}
	}
	// What a request refuses on its own (side, a part or related beside a
	// fitment key, a fitment key without type and number) is refused
	// before any file is read: contract.TestParseFitRequest.
	for _, r := range []contract.FitRequest{
		{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: "engine"}}},
		{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: "engine", Number: "EN-57"}}, Related: true},
		{From: contract.FitFrom{VIN: "1D7RV1GT5AS123456"}, Side: "front"},
	} {
		if _, err := tr.Fit(context.Background(), r); contract.AsProblem(err).Type != contract.InvalidRequest {
			t.Errorf("%+v: %v; want #invalid-request", r, err)
		}
	}
}

// TestFitPaging: every list pages, total counts every page, and paging
// end to end loses and repeats nothing.
func TestFitPaging(t *testing.T) {
	tr := catalogTranslator(t)
	request := `{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"limit":1`
	whole, err := fit(t, tr, request+"}")
	if err != nil {
		t.Fatal(err)
	}
	all, _ := fit(t, tr, `{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}}}`)
	var got []string
	f := whole
	for {
		if f.Total != 3 {
			t.Fatalf("total %d; want 3 on every page", f.Total)
		}
		for _, p := range f.PartTypes {
			got = append(got, p.Type)
		}
		if f.Next == "" {
			break
		}
		if f, err = fit(t, tr, request+`,"after":"`+f.Next+`"}`); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for _, p := range all.PartTypes {
		want = append(want, p.Type)
	}
	if !slices.Equal(got, want) || len(got) != 3 {
		t.Errorf("paged %v; whole %v", got, want)
	}
}

// TestFitBatch: a batch answers in order, a refusal naming its item.
func TestFitBatch(t *testing.T) {
	tr := catalogTranslator(t)
	reqs, err := contract.ParseFitBatch([]byte(`[{"from":"1D7RV1GT5AS123456"},{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}}}]`))
	if err != nil {
		t.Fatal(err)
	}
	fits, err := tr.FitBatch(context.Background(), reqs)
	if err != nil || len(fits) != 2 || fits[0].PartTypes == nil || fits[1].Vehicles == nil || fits[0].Now != fits[1].Now {
		t.Fatalf("%+v, %v", fits, err)
	}
	reqs = append(reqs, contract.FitRequest{From: contract.FitFrom{VIN: "1D7RV1GT5AS123456"}, To: []string{"nope"}})
	if _, err := tr.FitBatch(context.Background(), reqs); contract.AsProblem(err).Pointer != "/2/to/0" {
		t.Errorf("a batch's refusal: %v; want at /2/to/0", err)
	}
}

// TestListCatalog: list with of the catalog pages its fitment lines in key
// order, filtered by where.type, and refuses another field or cursor.
func TestListCatalog(t *testing.T) {
	tr := catalogTranslator(t)
	var got []string
	req := contract.ListRequest{Of: "parts-catalog", Limit: 2}
	for {
		l, err := tr.List(req)
		if err != nil {
			t.Fatal(err)
		}
		conforms(t, "List", l)
		if len(l.Vocabularies) != 1 || l.Vocabularies[0] != (contract.FileVersion{Name: "parts-catalog", Version: "2026.11"}) {
			t.Errorf("vocabularies %+v", l.Vocabularies)
		}
		for _, e := range l.Entries {
			line := e.(contract.FitmentLine)
			got = append(got, fmt.Sprintf("%s %s %s %d", line.Fitment.Type, line.Fitment.Number, line.Fitment.Side, len(line.Applications)))
		}
		if l.Next == "" {
			break
		}
		req.After = l.Next
	}
	want := []string{"brake-rotor-front BR-2001  2", "brake-rotor-front BR-2002  1", "engine EN-57  1", "mirror MR-1001 left 2", "mirror MR-1001 right 1"}
	if !slices.Equal(got, want) {
		t.Errorf("paged %v; want %v", got, want)
	}
	l, err := tr.List(contract.ListRequest{Of: "parts-catalog", Where: map[string]string{"type": "mirror"}})
	if err != nil {
		t.Fatal(err)
	}
	b := conforms(t, "List", l)
	if want := `{"of":"parts-catalog","entries":[{"fitment":{"type":"mirror","number":"MR-1001","side":"left"},"note":"power, heated","applications":[{"vehicle":{"make":"Dodge","model":"Ram 1500"},"first":2009,"last":2012,"options":"w/o towing package"},{"vehicle":{"make":"Dodge","model":"Ram 1500"},"first":2009,"last":2012,"options":"4WD, w/ towing package","qualifiers":{"15":[2]}}]},{"fitment":{"type":"mirror","number":"MR-1001","side":"right"},"note":"power, heated","applications":[{"vehicle":{"make":"Dodge","model":"Ram 1500"},"first":2009,"last":2012,"options":"w/o towing package"}]}],"vocabularies":[{"name":"parts-catalog","version":"2026.11"}],`; !strings.HasPrefix(string(b), want) {
		t.Errorf("where.type mirror:\n%s\nwant it to start\n%s", b, want)
	}
	for _, c := range []struct {
		req     contract.ListRequest
		pointer string
	}{
		{contract.ListRequest{Of: "parts-catalog", Where: map[string]string{"number": "MR-1001"}}, "/where/number"},
		{contract.ListRequest{Of: "parts-catalog", After: "WyJhIl0"}, "/after"},
		{contract.ListRequest{Of: "parts-catalog", Limit: 1001}, "/limit"},
	} {
		if _, err := tr.List(c.req); contract.AsProblem(err).Pointer != c.pointer {
			t.Errorf("%+v: %v; want a refusal at %s", c.req, err, c.pointer)
		}
	}
}

// modelsTranslator loads a catalog whose vehicle vocabulary maps two
// entries, Ram A and Ram B, to one vPIC model (1938), and Other to 1850:
// a vehicle reaching two catalog models; a rear rotor listed directly for
// one and through a redirect for the other, and for Ram A through a
// redirect in later years; and fitments Ram A shares with Other over
// differing years.
func modelsTranslator(t *testing.T) *Translator {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "cm.jsonl",
		`{"format":1,"kind":"vocabulary","name":"cm","version":"1","subject":"vehicle","fields":["model"]}`,
		`{"entry":{"model":"Ram A"},"mappings":[{"model":1938}]}`,
		`{"entry":{"model":"Ram B"},"mappings":[{"model":1938}]}`,
		`{"entry":{"model":"Other"},"mappings":[{"model":1850}]}`)
	write(t, dir, "catalog.jsonl",
		`{"format":1,"kind":"catalog","name":"models","version":"1","spine":"part-types","vehicles":"cm"}`,
		`{"fitment":{"type":"brake-rotor-rear","number":"R-1"},"applications":[{"vehicle":{"model":"Ram A"},"first":2009,"last":2012,"options":""}]}`,
		`{"fitment":{"type":"brake-rotor-front","number":"F-1"},"applications":[{"vehicle":{"model":"Ram B"},"first":2009,"last":2012,"options":""},{"vehicle":{"model":"Ram A"},"first":2013,"last":2015,"options":""}]}`,
		`{"fitment":{"type":"mirror","number":"M-1"},"applications":[{"vehicle":{"model":"Ram A"},"first":2010,"last":2010,"options":""},{"vehicle":{"model":"Other"},"first":2007,"last":2013,"options":""}]}`,
		`{"fitment":{"type":"mirror","number":"M-2"},"applications":[{"vehicle":{"model":"Ram A"},"first":2010,"last":2010,"options":""},{"vehicle":{"model":"Other"},"first":2008,"last":2013,"options":""}]}`,
		`{"fitment":{"type":"engine","number":"E-3"},"applications":[{"vehicle":{"model":"Ram A"},"first":2010,"last":2010,"options":""},{"vehicle":{"model":"Other"},"first":2009,"last":2011,"options":""}]}`,
		`{"fitment":{"type":"mirror","number":"M-4"},"applications":[{"vehicle":{"model":"Ram A"},"first":2010,"last":2010,"options":""},{"vehicle":{"model":"Other"},"first":2009,"last":2010,"options":"early"},{"vehicle":{"model":"Other"},"first":2011,"last":2013,"options":"late"}]}`,
		`{"redirect":{"vehicle":{"model":"Ram B"},"first":2009,"last":2012,"from":"brake-rotor-rear","to":"brake-rotor-front"}}`,
		`{"redirect":{"vehicle":{"model":"Ram A"},"first":2013,"last":2015,"from":"brake-rotor-rear","to":"brake-rotor-front"}}`)
	files, err := vocab.Open("../../testdata/vocab/fitment/part-types.jsonl", dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(embeddedDecoder(t), files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// TestFitModels: a redirect is for a catalog vehicle and years, followed
// wherever no direct candidate covers them, so one of several models, or
// one range of years, is reached through it while the rest are direct;
// and a related vehicle is a catalog vehicle over the years it shares the
// same fitments, counting every one.
func TestFitModels(t *testing.T) {
	tr := modelsTranslator(t)
	for _, c := range []struct {
		name, request string
		want          []string
	}{
		{"two models, one through a redirect", `{"from":{"model":1938},"model_year":2010,"part":{"spine":"part-types","id":"brake-rotor-rear"}}`, []string{
			"status found",
			"model Ram A via model:1938",
			"model Ram B via model:1938",
			"candidate brake-rotor-front F-1  redirect",
			`  Ram B 2009-2012 "" via model:1938`,
			`  redirect Ram B 2009-2012 brake-rotor-rear→brake-rotor-front ""`,
			"candidate brake-rotor-rear R-1  direct",
			`  Ram A 2009-2012 "" via model:1938`,
			"total 2",
		}},
		{"no year, later years through a redirect", `{"from":{"vocabulary":"cm","entry":{"model":"Ram A"}},"part":{"spine":"part-types","id":"brake-rotor-rear"}}`, []string{
			"status found",
			"model Ram A",
			"candidate brake-rotor-front F-1  redirect",
			`  Ram A 2013-2015 "" [model_year]`,
			`  redirect Ram A 2013-2015 brake-rotor-rear→brake-rotor-front ""`,
			"candidate brake-rotor-rear R-1  direct",
			`  Ram A 2009-2012 "" [model_year]`,
			"total 2",
		}},
		{"part types of two models", `{"from":{"model":1938},"model_year":2010}`, []string{
			"status found",
			"model Ram A via model:1938",
			"model Ram B via model:1938",
			`part-type brake-rotor-front "Brake Rotor, Front" 1 direct`,
			`part-type brake-rotor-rear "Brake Rotor, Rear" 2 direct`,
			`part-type engine "Engine" 1 direct`,
			`part-type mirror "Door Mirror" 3 direct`,
			"total 4",
		}},
		{"related over differing years", `{"from":{"vocabulary":"cm","entry":{"model":"Ram A"}},"model_year":2010,"related":true}`, []string{
			"status found",
			"model Ram A",
			"related Other 2009-2011 4 engine:1,mirror:3 → model:1850 make:467",
			"related Other 2012-2013 3 mirror:3 → model:1850 make:467",
			"related Other 2008-2008 2 mirror:2 → model:1850 make:467",
			"related Other 2007-2007 1 mirror:1 → model:1850 make:467",
			"total 4",
		}},
		{"related in every year", `{"from":{"vocabulary":"cm","entry":{"model":"Ram A"}},"related":true,"part":{"spine":"part-types","id":"brake-rotor-front"}}`, []string{
			"status found",
			"model Ram A",
			"related Ram B 2009-2012 1 brake-rotor-front:1 → model:1938 make:476",
			"total 1",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, err := fit(t, tr, c.request)
			if err != nil {
				t.Fatal(err)
			}
			if got := fitSummary(f); !slices.Equal(got, c.want) {
				t.Errorf("%s:\n%s\nwant\n%s", c.request, strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

// TestFitPagingEveryKind: candidates, related vehicles and a fitment's
// vehicles page end to end one at a time, each page's total the whole's,
// and each kind refuses another kind's cursor.
func TestFitPagingEveryKind(t *testing.T) {
	tr := modelsTranslator(t)
	requests := map[string]string{
		"candidates": `{"from":{"model":1938},"model_year":2010,"part":{"spine":"part-types","id":"brake-rotor-rear"}`,
		"related":    `{"from":{"vocabulary":"cm","entry":{"model":"Ram A"}},"model_year":2010,"related":true`,
		"vehicles":   `{"from":{"fitment":{"type":"brake-rotor-front","number":"F-1"}}`,
		"part_types": `{"from":{"model":1938},"model_year":2010`,
	}
	cursors := map[string]string{}
	for kind, request := range requests {
		whole, err := fit(t, tr, request+"}")
		if err != nil {
			t.Fatal(err)
		}
		want := fitSummary(whole)
		var got []string
		f, err := fit(t, tr, request+`,"limit":1}`)
		for page := 0; ; page++ {
			if err != nil {
				t.Fatalf("%s page %d: %v", kind, page, err)
			}
			if f.Total != whole.Total {
				t.Fatalf("%s page %d: total %d; want %d", kind, page, f.Total, whole.Total)
			}
			lines := fitSummary(f)
			if page == 0 {
				got = append(got, lines[:len(lines)-2]...) // status and models, then the item; not total, next
				cursors[kind] = f.Next
			} else {
				got = append(got, itemLines(lines)...)
			}
			if f.Next == "" {
				break
			}
			f, err = fit(t, tr, request+`,"limit":1,"after":"`+f.Next+`"}`)
		}
		got = append(got, fmt.Sprintf("total %d", whole.Total))
		if !slices.Equal(got, want) {
			t.Errorf("%s paged one at a time:\n%s\nwant\n%s", kind, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if whole.Total < 2 {
			t.Errorf("%s: total %d; the case needs two pages", kind, whole.Total)
		}
	}
	for kind, request := range requests {
		for other, cursor := range cursors {
			if other == kind {
				continue
			}
			_, err := fit(t, tr, request+`,"after":"`+cursor+`"}`)
			if p := contract.AsProblem(err); err == nil || p.Pointer != "/after" {
				t.Errorf("%s with %s's cursor: %v; want a refusal at /after", kind, other, err)
			}
		}
	}
}

// itemLines are a later page's summary lines that are its items: without
// the status, models and fitment it repeats, and the total.
func itemLines(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, "status ") || strings.HasPrefix(l, "model ") || strings.HasPrefix(l, "fitment ") || strings.HasPrefix(l, "total ") || l == "next" {
			continue
		}
		out = append(out, l)
	}
	return out
}
