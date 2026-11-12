package nitrovin

import (
	"bytes"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// spineBudget is the spine bundle's budget, brotli-compressed (ADR 0013).
const spineBudget = 1_000_000

// TestSpineBundle: the embedded month's spine bundle is under budget, the
// same bytes each time, and read back it answers every question
// vocab.Month asks exactly as the artifact does. Any month: the refresh
// holds a new one to the budget.
func TestSpineBundle(t *testing.T) {
	d, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	b := spineBundle(t, d, true)
	sameMonth(t, d.month, b)
}

// spineBundle is d's spine bundle read back; checked, it is under budget
// and the same bytes when written again.
func spineBundle(t *testing.T, d *Decoder, checked bool) *vocab.Bundle {
	t.Helper()
	var raw bytes.Buffer
	if err := d.WriteSpineBundle(&raw); err != nil {
		t.Fatal(err)
	}
	if checked {
		var br bytes.Buffer
		w := brotli.NewWriterLevel(&br, brotli.BestCompression)
		if _, err := w.Write(raw.Bytes()); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("spine bundle %s: %d bytes, %d brotli", d.Info().DataMonth, raw.Len(), br.Len())
		if br.Len() > spineBudget {
			t.Errorf("the spine bundle is %d bytes brotli; the budget is %d", br.Len(), spineBudget)
		}
		again := new(bytes.Buffer)
		if err := d.WriteSpineBundle(again); err != nil || !bytes.Equal(again.Bytes(), raw.Bytes()) {
			t.Errorf("a second spine bundle differs from the first (%v)", err)
		}
	}
	b, err := vocab.ReadBundle(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSpineBundleTranslates: drift, translations and lists through
// the embedded month's spine bundle are the artifact's, on testdata/vocab.
func TestSpineBundleTranslates(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	b := spineBundle(t, tr.d, false)
	for _, dir := range []string{"../../testdata/vocab", "../../testdata/vocab/drift"} {
		files, err := vocab.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		// A spine bundle carries no model years (ADR 0022), so it reports
		// no model-years drift.
		want := slices.DeleteFunc(vocab.Drift(tr.d.month, files), func(f vocab.Finding) bool { return f.Drift == vocab.DriftModelYears })
		if got := vocab.Drift(b, files); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: drift through the bundle\n%v\nwant the artifact's\n%v", dir, got, want)
		}
	}
	set, err := vocab.NewSet(b, tr.core.Set.Files())
	if err != nil {
		t.Fatal(err)
	}
	core := tr.core
	core.Set = set
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for _, req := range []contract.TranslateRequest{
		{From: contract.TranslateFrom{VIN: ram2010}, To: []string{"yard"}},
		{From: contract.TranslateFrom{VIN: ram2010x2}, To: []string{"yard"}},
		{From: contract.TranslateFrom{VIN: ram2015}, To: []string{"yard"}},
		{From: contract.TranslateFrom{VIN: motorcycle}, To: []string{"listing", "yard"}},
		{From: contract.TranslateFrom{Vocabulary: "yard", Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}, To: []string{"yard"}, ModelYear: contract.Value[int32](2012)},
		{From: contract.TranslateFrom{Vocabulary: "yard", Entry: map[string]string{"make": "Packard", "model": "Clipper"}}, To: []string{"yard"}},
		{From: contract.TranslateFrom{SpineRef: contract.SpineRef{Make: 476}}, To: []string{"yard"}},
		{From: contract.TranslateFrom{SpineRef: contract.SpineRef{Model: 1938}}, To: []string{"yard"}, VIN: ram2010},
		{From: contract.TranslateFrom{SpineRef: contract.SpineRef{Spine: "parts", ID: "brake-pads"}}, To: []string{"listing"}, VIN: motorcycle},
	} {
		req.Now = now
		got, gotErr := core.Translate(req)
		want, wantErr := tr.Translate(req)
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Errorf("translate %+v through the bundle:\n%+v (%v)\nwant the artifact's\n%+v (%v)", req.From, got, gotErr, want, wantErr)
		}
	}
	for _, req := range []contract.ListRequest{
		{},
		{Of: "vpic", Limit: contract.MaxListLimit},
		{Of: "vpic", Where: map[string]string{"make": "476"}},
		{Of: "vpic", Where: map[string]string{"make": "dodge"}},
		{Of: "yard"},
	} {
		got, gotErr := core.List(req)
		want, wantErr := tr.List(req)
		// A spine bundle carries no model years (ADR 0022): its models'
		// are unknown.
		for i, e := range want.Entries {
			if se, ok := e.(contract.SpineEntry); ok {
				se.Years = nil
				want.Entries[i] = se
			}
		}
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Errorf("list %+v through the bundle:\n%+v (%v)\nwant the artifact's\n%+v (%v)", req, got, gotErr, want, wantErr)
		}
	}
	// A spine bundle carries no model values (ADR 0015): where the artifact
	// lists them, the bundle refuses.
	values := contract.ListRequest{Of: "vpic", Where: map[string]string{"model": "1850", "element": "34"}}
	if _, err := tr.List(values); err != nil {
		t.Fatalf("list %+v: %v", values, err)
	}
	if l, err := core.List(values); err == nil || contract.AsProblem(err).Type != contract.InvalidRequest || contract.AsProblem(err).Pointer != "/where/model" {
		t.Errorf("list %+v through the bundle: %+v, %v; want #invalid-request at /where/model", values, l, err)
	}
}

// sameMonth checks that got answers every question of vocab.Month as want
// does: each over everything want knows, and ids and values it does not.
func sameMonth(t *testing.T, want, got vocab.BundleSource) {
	t.Helper()
	if want.Name() != got.Name() {
		t.Errorf("name %q; want %q", got.Name(), want.Name())
	}
	type pair struct {
		id   int32
		name string
	}
	pairs := func(seq func(func(int32, string) bool)) []pair {
		var out []pair
		for id, name := range seq {
			out = append(out, pair{id, name})
		}
		return out
	}
	if g, w := pairs(got.Makes()), pairs(want.Makes()); !slices.Equal(g, w) {
		t.Errorf("Makes: %d makes; want %d, or not in the same order", len(g), len(w))
	}
	modelIDs := slices.Compact(slices.Sorted(want.ModelIDs()))
	if g := slices.Compact(slices.Sorted(got.ModelIDs())); !slices.Equal(g, modelIDs) {
		t.Errorf("ModelIDs: %d models; want %d", len(g), len(modelIDs))
	}

	// Ids: every make and model id, each one off, and ids nothing has.
	ids := map[int32]bool{0: true, -1: true, 1<<31 - 1: true, -1 << 31: true}
	add := func(id int32) {
		ids[id], ids[id-1], ids[id+1] = true, true, true
	}
	for id := range want.Makes() {
		add(id)
	}
	for _, id := range modelIDs {
		add(id)
	}
	known := 0
	for id := range ids {
		if g, w := pairs(got.Models(id)), pairs(want.Models(id)); !slices.Equal(g, w) {
			t.Errorf("Models(%d): %v; want %v", id, g, w)
		}
		gName, gOK := got.Make(id)
		wName, wOK := want.Make(id)
		if gName != wName || gOK != wOK {
			t.Errorf("Make(%d): %q %t; want %q %t", id, gName, gOK, wName, wOK)
		}
		gMake, gName, gOK := got.Model(id)
		wMake, wName, wOK := want.Model(id)
		if gMake != wMake || gName != wName || gOK != wOK {
			t.Errorf("Model(%d): %d %q %t; want %d %q %t", id, gMake, gName, gOK, wMake, wName, wOK)
		}
		if wOK {
			known++
		}
	}
	if known != len(modelIDs) {
		t.Errorf("Model knows %d of %d model ids", known, len(modelIDs))
	}

	// Elements: every public one, each element any value names, and ids
	// around them.
	elements := map[int32]bool{0: true, -1: true}
	for id := range want.Elements() {
		add := []int32{id - 1, id, id + 1}
		for _, e := range add {
			elements[e] = true
		}
	}
	values := map[string]bool{"": true, "0": true, "-1": true, "x": true}
	for el, v := range want.Values() {
		elements[el] = true
		values[v] = true
	}
	for el := range got.Values() {
		elements[el] = true
	}
	for el := range elements {
		gLookup, gOK := got.Element(el)
		wLookup, wOK := want.Element(el)
		if gLookup != wLookup || gOK != wOK {
			t.Errorf("Element(%d): %t %t; want %t %t", el, gLookup, gOK, wLookup, wOK)
		}
	}
	// Every value against every element: Text, and Attribute for each
	// that is an attribute id.
	asked := 0
	for el := range elements {
		for v := range values {
			if g, w := got.Text(el, v), want.Text(el, v); g != w {
				t.Errorf("Text(%d, %q): %t; want %t", el, v, g, w)
			}
			if id, err := strconv.ParseInt(v, 10, 32); err == nil {
				if g, w := got.Attribute(el, int32(id)), want.Attribute(el, int32(id)); g != w {
					t.Errorf("Attribute(%d, %d): %t; want %t", el, id, g, w)
				}
			}
			asked++
		}
	}
	t.Logf("asked %d makes and models, %d elements, %d element values", len(ids), len(elements), asked)
}
