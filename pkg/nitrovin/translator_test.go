package nitrovin

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// fixtureMonth is the data month testdata/vocab's ids and values are
// taken from.
const fixtureMonth = "2026_10"

// embeddedDecoder is the embedded data, skipping when it is not the
// fixture's month.
func embeddedDecoder(t *testing.T) *Decoder {
	t.Helper()
	if embedded.Month != fixtureMonth {
		t.Skipf("testdata/vocab is %s's; the embedded data is %s", fixtureMonth, embedded.Month)
	}
	d, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestFixtureLoadsClean: the synthetic fixture loads against 2026_10 with
// no drift, and Info lists its four files.
func TestFixtureLoadsClean(t *testing.T) {
	d := embeddedDecoder(t)
	files, err := vocab.Open("../../testdata/vocab")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	if drift := tr.Drift(); len(drift) > 0 {
		t.Errorf("the fixture drifts from %s: %v", fixtureMonth, drift)
	}
	info := tr.Info()
	var got []string
	for i, v := range info.Vocabularies {
		got = append(got, v.Name+" "+v.Kind+" "+v.Version)
		if v.Path != files[i].Path || v.Blake3 != hex.EncodeToString(files[i].Hash[:]) || v.Drift == nil || len(v.Drift) > 0 {
			t.Errorf("vocabulary %d: %+v", i, v)
		}
	}
	want := []string{"classics extension-set 2026.11", "listing vocabulary 2026.11", "parts spine 2026.11", "yard vocabulary 2026.11"}
	if !slices.Equal(got, want) {
		t.Errorf("info vocabularies %v; want %v", got, want)
	}
	if info.DataMonth != d.Info().DataMonth || len(d.Info().Vocabularies) != 0 || tr.Decoder() != d {
		t.Error("the translator's info is not the decoder's plus the files, or the decoder changed")
	}
}

// TestFixtureDrift: testdata/vocab/drift has one drift of each kind, and
// loads with exactly those findings, counted by kind in Info.
func TestFixtureDrift(t *testing.T) {
	d := embeddedDecoder(t)
	files, err := vocab.Open("../../testdata/vocab/drift")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range tr.Drift() {
		got = append(got, string(f.Drift)+" "+filepath.Base(f.File)+":"+strconv.Itoa(f.Line)+": "+f.Detail)
	}
	want := []string{
		`extension classics.jsonl:2: extension "silverado" is named like vPIC model 1850 (Silverado) under make 467 (Chevrolet) in data month 2026_10`,
		`qualifier yard.jsonl:2: /mappings/0/qualifiers/34/1: data month 2026_10 never decodes element 34 to "1500 Megacab"`,
		`qualifier yard.jsonl:3: /mappings/0/qualifiers/15/1: data month 2026_10 never decodes element 15 to attribute id 99`,
		`make yard.jsonl:4: /mappings/0/make: vPIC make 99999 is not in data month 2026_10`,
		`model yard.jsonl:5: /mappings/0/model: vPIC model 99999 is not in data month 2026_10`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("drift:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	counts := map[string][]contract.DriftCount{}
	for _, v := range tr.Info().Vocabularies {
		counts[v.Name] = v.Drift
	}
	if c := counts["classics"]; len(c) != 1 || c[0].Kind != "extension" || c[0].Count != 1 || len(c[0].Examples) != 1 || c[0].Examples[0].Line != 2 {
		t.Errorf("classics drift counts: %+v", c)
	}
	var kinds []string
	for _, c := range counts["yard"] {
		kinds = append(kinds, c.Kind+"="+strconv.Itoa(c.Count))
	}
	if want := []string{"make=1", "model=1", "qualifier=2"}; !slices.Equal(kinds, want) {
		t.Errorf("yard drift counts %v; want %v", kinds, want)
	}
}

// TestTranslatorRefusesUnlinked: NewTranslator links what it is given, as
// vocab.Open does.
func TestTranslatorRefusesUnlinked(t *testing.T) {
	d := embeddedDecoder(t)
	f, err := vocab.Read("listing", strings.NewReader(`{"format":1,"kind":"vocabulary","name":"listing","version":"1","subject":"part","fields":["category"],"spine":"parts"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewTranslator(d, f)
	if faults, ok := errors.AsType[vocab.Faults](err); !ok || faults[0].String() != `listing:1: /spine: spine "parts" is not loaded` {
		t.Errorf("NewTranslator without the spine: %v", err)
	}
}
