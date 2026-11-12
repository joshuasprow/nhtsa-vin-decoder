package nitrovin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// VINs of the embedded data the translation cases decode, chosen against
// 2026_10 for decodes a later month keeps.
const (
	// ram2010 decodes to Dodge (476) Ram (1938), 2010, Series "DS (1500)",
	// Drive Type 4WD (2).
	ram2010 = "1D7RV1GT5AS123456"
	// ram2010x2 is ram2010 with Drive Type 4x2 (7).
	ram2010x2 = "1D7RB1CT0AS123456"
	// ram2005 decodes to Dodge Ram, 2005, Series "2500".
	ram2005 = "3D7KS28C35G123456"
	// ram2015 decodes to Ram (496) 1500 (13620), 2015, Series empty, 4WD.
	ram2015 = "1C6RR7LT5FS123456"
	// motorcycle decodes to Honda (474), 2006, Vehicle Type Motorcycle (1),
	// no model.
	motorcycle = "JH2PC40066M200001"
	// silverado decodes to Chevrolet (467) Silverado (1850), 2014, Vehicle
	// Type Truck (3).
	silverado = "1GCVKREC6EZ123456"
)

// fixtureTranslator is the embedded data with testdata/vocab loaded.
func fixtureTranslator(t *testing.T, dir string) *Translator {
	t.Helper()
	d := embeddedDecoder(t)
	files, err := vocab.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// apiSchema compiles $defs/name of docs/api/decode.schema.json.
func apiSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c, err := compileAPISchema()
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("https://github.com/joshuasprow/nitrovin/blob/main/docs/api/decode.schema.json#/$defs/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var compileAPISchema = sync.OnceValues(func() (*jsonschema.Compiler, error) {
	f, err := os.Open("../../docs/api/decode.schema.json")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("https://github.com/joshuasprow/nitrovin/blob/main/docs/api/decode.schema.json", doc); err != nil {
		return nil, err
	}
	return c, nil
})

// conforms checks that v marshals to what $defs/name of the schema allows.
func conforms(t *testing.T, name string, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if err := apiSchema(t, name).Validate(doc); err != nil {
		t.Errorf("%s does not validate: %v\n%s", name, err, b)
	}
	return b
}

// ref writes a spine entry as make:476, model:1938, ext:classics/corvair
// or parts/brake-pads.
func ref(r contract.SpineRef) string {
	switch {
	case r.Make != 0:
		return fmt.Sprintf("make:%d", r.Make)
	case r.Model != 0:
		return fmt.Sprintf("model:%d", r.Model)
	case r.Ext != "":
		return r.Ext
	}
	return r.Spine + "/" + r.ID
}

// unconfirmedText writes an Unconfirmed as " [34 model_year]", "" when
// empty.
func unconfirmedText(u contract.Unconfirmed) string {
	if u.IsZero() {
		return ""
	}
	var parts []string
	for _, e := range u.Elements {
		parts = append(parts, fmt.Sprint(e))
	}
	if u.ModelYear {
		parts = append(parts, "model_year")
	}
	return " [" + strings.Join(parts, " ") + "]"
}

// summary writes a translation as lines: the from's status, each spine
// entry (ref, name or drift, unconfirmed), each target entry (vocabulary,
// fields joined by "/", the spine entries its vias name, unconfirmed).
func summary(tr contract.Translation) []string {
	out := []string{"from " + string(tr.From.Status)}
	for _, s := range tr.Spine {
		name := s.Name
		if s.Drift {
			name = "drift"
		}
		out = append(out, "spine "+ref(s.SpineRef)+" "+name+unconfirmedText(s.Unconfirmed))
	}
	for _, to := range tr.To {
		if len(to.Entries) == 0 {
			out = append(out, to.Vocabulary+": none")
		}
		for _, e := range to.Entries {
			var vias []string
			for _, v := range e.Via {
				vias = append(vias, ref(v.SpineRef))
			}
			out = append(out, to.Vocabulary+": "+fields(e.Entry)+" via "+strings.Join(vias, ",")+unconfirmedText(e.Unconfirmed))
		}
	}
	return out
}

// fields writes an entry's field values in name order, "/"-joined.
func fields(entry map[string]string) string {
	var vs []string
	for _, k := range slices.Sorted(maps.Keys(entry)) {
		vs = append(vs, entry[k])
	}
	return strings.Join(vs, "/")
}

// TestTranslate is ticket #136's table over the synthetic fixture: each
// request, parsed as every surface parses it, and the route it answers
// with, or the pointer of its refusal. Every answer validates against
// decode.schema.json's Translation.
func TestTranslate(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	for _, tc := range []struct {
		name string
		req  string
		want []string // summary lines; nil with ptr
		ptr  string   // the #invalid-request pointer
	}{
		// A yard entry split by year across two vPIC models: Dodge Ram up to
		// 2012, Ram 1500 from 2013; each model reaches its make.
		{
			name: "split by year, no year",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":" dodge","model":"RAM  1500"}}}`,
			want: []string{
				"from mapped",
				"spine model:1938 Ram [34 model_year]",
				"spine make:476 Dodge [34 model_year]",
				"spine model:13620 1500 [model_year]",
				"spine make:496 Ram [model_year]",
			},
		},
		{
			name: "split by year, 2015",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2015}`,
			want: []string{"from mapped", "spine model:13620 1500", "spine make:496 Ram"},
		},
		{
			name: "split by year, 2010, to yard",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"to":["yard"]}`,
			want: []string{
				"from mapped",
				"spine model:1938 Ram [34]",
				"spine make:476 Dodge [34]",
				"yard: Dodge/Other via make:476 [34]",
				"yard: Dodge/Ram 1500 via model:1938 [34]",
				"yard: Dodge/Ram 1500 4x4 via model:1938 [15 34]",
			},
		},
		{
			name: "deduplicated, a via per mapping",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"to":["yard","yard"]}`,
			want: []string{
				"from mapped",
				"spine model:1938 Ram [34 model_year]",
				"spine make:476 Dodge [34 model_year]",
				"spine model:13620 1500 [model_year]",
				"spine make:496 Ram [model_year]",
				"yard: Dodge/Other via make:476 [34 model_year]",
				"yard: Dodge/Ram 1500 via model:1938,model:13620 [34 model_year]",
				"yard: Dodge/Ram 1500 4x4 via model:1938,model:13620 [15 34 model_year]",
				"yard: Ram/Other via make:496 [model_year]",
			},
		},

		// A Series qualifier confirmed by a decode, refuted by one, and
		// unconfirmed without one.
		{
			name: "Series confirmed by a context decode",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"vin":"` + ram2010 + `"}`,
			want: []string{"from mapped", "spine model:1938 Ram", "spine make:476 Dodge"},
		},
		{
			name: "Series refuted by a context decode",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"vin":"` + ram2005 + `","to":["yard"]}`,
			want: []string{"from mapped", "yard: none"},
		},
		{
			name: "Series unconfirmed without a decode",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010}`,
			want: []string{"from mapped", "spine model:1938 Ram [34]", "spine make:476 Dodge [34]"},
		},
		{
			name: "a VIN to yard: Series confirmed, Drive Type 4WD confirmed",
			req:  `{"from":"` + ram2010 + `","to":["yard"]}`,
			want: []string{
				"from mapped",
				"spine model:1938 Ram",
				"spine make:476 Dodge",
				"yard: Dodge/Other via make:476",
				"yard: Dodge/Ram 1500 via model:1938",
				"yard: Dodge/Ram 1500 4x4 via model:1938",
			},
		},
		{
			name: "a VIN to yard: Drive Type 4x2 refutes the 4x4",
			req:  `{"from":"` + ram2010x2 + `","to":["yard"]}`,
			want: []string{
				"from mapped",
				"spine model:1938 Ram",
				"spine make:476 Dodge",
				"yard: Dodge/Other via make:476",
				"yard: Dodge/Ram 1500 via model:1938",
			},
		},
		{
			name: "a VIN's decoded year wins over model_year",
			req:  `{"from":"` + ram2015 + `","model_year":2010,"to":["yard"]}`,
			want: []string{
				"from mapped",
				"spine model:13620 1500",
				"spine make:496 Ram",
				"yard: Dodge/Ram 1500 via model:13620",
				"yard: Dodge/Ram 1500 4x4 via model:13620",
				"yard: Ram/Other via make:496",
			},
		},

		// An extension mapping.
		{
			name: "an extension model, under a vPIC make",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Chevrolet","model":"Corvair"}},"to":["yard"]}`,
			want: []string{
				"from mapped",
				"spine ext:classics/corvair Corvair",
				"spine make:467 Chevrolet",
				"yard: Chevrolet/Corvair via ext:classics/corvair",
			},
		},
		{
			name: "an extension model, under an extension make, year scoped",
			req:  `{"from":{"ext":"ext:classics/packard-clipper"},"to":["yard"]}`,
			want: []string{
				"from mapped",
				"spine ext:classics/packard-clipper Clipper",
				"spine ext:classics/packard Packard",
				"yard: Packard/Clipper via ext:classics/packard-clipper [model_year]",
			},
		},
		{
			name: "an extension mapping outside its years",
			req:  `{"from":{"ext":"ext:classics/packard-clipper"},"model_year":1960,"to":["yard"]}`,
			want: []string{"from mapped", "spine ext:classics/packard-clipper Clipper", "spine ext:classics/packard Packard", "yard: none"},
		},

		// A part vocabulary routed through its spine with a Vehicle Type
		// qualifier.
		{
			name: "parts without a vehicle: both, unconfirmed",
			req:  `{"from":{"vocabulary":"listing","entry":{"category":"brake pads"}},"to":["listing"]}`,
			want: []string{
				"from mapped",
				"spine parts/brake-pads Brake Pads [39]",
				"spine parts/brake-pads-motorcycle Brake Pads, Motorcycle [39]",
				"listing: Brake Pads via parts/brake-pads,parts/brake-pads-motorcycle [39]",
				"listing: Brake Parts via parts/brake-pads,parts/brake-pads-motorcycle [39]",
			},
		},
		{
			name: "parts for a motorcycle",
			req:  `{"from":{"vocabulary":"listing","entry":{"category":"Brake Pads"}},"vin":"` + motorcycle + `","to":["listing"]}`,
			want: []string{
				"from mapped",
				"spine parts/brake-pads-motorcycle Brake Pads, Motorcycle",
				"listing: Brake Pads via parts/brake-pads-motorcycle",
				"listing: Brake Parts via parts/brake-pads-motorcycle",
			},
		},
		{
			name: "parts for a truck",
			req:  `{"from":{"vocabulary":"listing","entry":{"category":"Brake Pads"}},"vin":"` + silverado + `"}`,
			want: []string{"from mapped", "spine parts/brake-pads Brake Pads"},
		},
		{
			name: "a spine entry to its vocabulary, one-to-many",
			req:  `{"from":{"spine":"parts","id":"fender-lf"},"to":["listing"]}`,
			want: []string{"from mapped", "spine parts/fender-lf Fender, Left Front", "listing: Fenders via parts/fender-lf"},
		},
		{
			name: "an unnamed spine entry",
			req:  `{"from":{"vocabulary":"listing","entry":{"category":"Engine"}}}`,
			want: []string{"from mapped", "spine parts/300 "},
		},

		// Unknown and unmapped entries.
		{
			name: "unmapped",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Studebaker","model":"Lark"}},"to":["yard"]}`,
			want: []string{"from unmapped", "yard: none"},
		},
		{
			name: "unknown entry",
			req:  `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Caravan"}},"to":["yard"]}`,
			want: []string{"from unknown", "yard: none"},
		},
		{
			name: "unknown vPIC id",
			req:  `{"from":{"model":2147483647}}`,
			want: []string{"from unknown"},
		},
		{
			name: "unknown extension",
			req:  `{"from":{"ext":"ext:classics/tucker"}}`,
			want: []string{"from unknown"},
		},
		{
			name: "unknown spine id",
			req:  `{"from":{"spine":"parts","id":"muffler"},"to":["listing"]}`,
			want: []string{"from unknown", "listing: none"},
		},
		{
			name: "a VIN that decodes to nothing",
			req:  `{"from":"ZZZZZZZZZZZZZZZZZ","to":["yard"]}`,
			want: []string{"from unknown", "yard: none"},
		},
		{
			name: "a vPIC model, to its make's entries too",
			req:  `{"from":{"model":1850},"to":["yard"]}`,
			want: []string{"from mapped", "spine model:1850 Silverado", "spine make:467 Chevrolet", "yard: Chevy/Silverado via model:1850"},
		},

		// Refusals: an unknown vocabulary, entry fields other than the
		// vocabulary's, vin given both as from and as context, and routes
		// that cross a second spine.
		{name: "unknown vocabulary", req: `{"from":{"vocabulary":"ebay","entry":{"make":"Dodge"}}}`, ptr: "/from/vocabulary"},
		{name: "unknown target", req: `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"to":["yard","ebay"]}`, ptr: "/to/1"},
		{name: "a spine is no target", req: `{"from":{"spine":"parts","id":"300"},"to":["parts"]}`, ptr: "/to/0"},
		{name: "vpic is no target", req: `{"from":{"make":476},"to":["vpic"]}`, ptr: "/to/0"},
		{name: "an extension set is no vocabulary", req: `{"from":{"vocabulary":"classics","entry":{"id":"corvair"}}}`, ptr: "/from/vocabulary"},
		{name: "an unknown entry field", req: `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram","trim":"SLT"}}}`, ptr: "/from/entry/trim"},
		{name: "a missing entry field", req: `{"from":{"vocabulary":"yard","entry":{"make":"Dodge"}}}`, ptr: "/from/entry"},
		{name: "vin both as from and as context", req: `{"from":"` + ram2010 + `","vin":"` + ram2015 + `"}`, ptr: "/vin"},
		{name: "a VIN to a part vocabulary", req: `{"from":"` + ram2010 + `","to":["listing"]}`, ptr: "/to/0"},
		{name: "a part entry to a vehicle vocabulary", req: `{"from":{"vocabulary":"listing","entry":{"category":"Fenders"}},"to":["yard"]}`, ptr: "/to/0"},
		{name: "unknown spine", req: `{"from":{"spine":"catalog","id":"300"}}`, ptr: "/from/spine"},
		{name: "unknown extension set", req: `{"from":{"ext":"ext:snyder/42"}}`, ptr: "/from/ext"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := contract.ParseTranslateRequest([]byte(tc.req))
			if err == nil {
				req.Now, _ = contract.ParseNow("2026-11-10")
				var ans contract.Translation
				if ans, err = tr.Translate(req); err == nil {
					if tc.ptr != "" {
						t.Fatalf("answered %v; want a refusal at %s", summary(ans), tc.ptr)
					}
					conforms(t, "Translation", ans)
					if got := summary(ans); !slices.Equal(got, tc.want) {
						t.Errorf("got\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(tc.want, "\n\t"))
					}
					return
				}
			}
			p, ok := err.(*contract.Problem)
			if !ok || p.Type != contract.InvalidRequest || p.Pointer != tc.ptr || tc.ptr == "" {
				t.Fatalf("error %v; want #invalid-request at %q", err, tc.ptr)
			}
			conforms(t, "Problem", p)
		})
	}
}

// TestTranslateAnswer pins the whole answer's header fields: from as the
// vocabulary writes it with its note, vehicle, the files used in load
// order, now, data month and decoder version, and each via's years,
// origin and note.
func TestTranslateAnswer(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	now, _ := contract.ParseNow("2026-11-10T12:34:56.789Z")
	ans, err := tr.Translate(contract.TranslateRequest{
		From: contract.TranslateFrom{Vocabulary: "listing", Entry: map[string]string{"category": "brake  PARTS"}},
		To:   []string{"listing"},
		VIN:  " " + strings.ToLower(silverado) + " ",
		Now:  now,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := conforms(t, "Translation", ans)
	f := ans.From
	if f.Status != contract.FromMapped || f.Entry["category"] != "Brake Parts" || f.Note != "a coarse category maps to every entry it covers" || f.VIN != "" {
		t.Errorf("from %+v", f)
	}
	if v := ans.Vehicle; v == nil || v.VIN != silverado || v.Make != contract.Value[int32](467) || v.Model != contract.Value[int32](1850) || v.ModelYear != contract.Value[int32](2014) {
		t.Errorf("vehicle %+v", ans.Vehicle)
	}
	var files []string
	for _, v := range ans.Vocabularies {
		files = append(files, v.Name+" "+v.Version)
	}
	if want := []string{"listing 2026.11", "parts 2026.11"}; !slices.Equal(files, want) {
		t.Errorf("vocabularies %v; want %v", files, want)
	}
	if got := ans.Now.Format("2006-01-02T15:04:05Z07:00"); got != "2026-11-10T12:34:56Z" || ans.DataMonth != embedded.Month || ans.DecoderVersion != Version() {
		t.Errorf("now %s, data month %s, decoder version %s", got, ans.DataMonth, ans.DecoderVersion)
	}
	// Brake Parts' own mappings carry no qualifiers, so it reaches all
	// three spine entries; back from them, listing's Brake Pads holds only
	// through brake-pads, its car mapping (a truck), not its motorcycle one.
	if got := summary(ans); !slices.Equal(got, []string{
		"from mapped",
		"spine parts/brake-pads Brake Pads",
		"spine parts/brake-pads-motorcycle Brake Pads, Motorcycle",
		"spine parts/brake-rotor Brake Rotor",
		"listing: Brake Pads via parts/brake-pads",
		"listing: Brake Parts via parts/brake-pads,parts/brake-pads-motorcycle,parts/brake-rotor",
	}) {
		t.Errorf("summary %v", got)
	}
	if v := ans.To[0].Entries[0].Via[0]; v.Origin != "curated" || v.First.Valid || v.Last.Valid {
		t.Errorf("Brake Pads' via %+v", v)
	}
	if !bytes.Contains(b, []byte(`"note":"a coarse category maps to every entry it covers","via":[`)) {
		t.Errorf("the target entry's own note is not in %s", b)
	}

	// A via carries its mapping's years, origin and note.
	ans, err = tr.Translate(contract.TranslateRequest{From: contract.TranslateFrom{VIN: ram2015}, To: []string{"yard"}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	conforms(t, "Translation", ans)
	if f := ans.From; f.VIN != ram2015 || f.Status != contract.FromMapped {
		t.Errorf("from %+v", f)
	}
	v := ans.To[0].Entries[0].Via[0]
	if v.Model != 13620 || v.First != contract.Value[int32](2013) || v.Last.Valid || v.Origin != "curated" || v.Note != "Ram is its own make from 2013" {
		t.Errorf("Dodge Ram 1500's via %+v", v)
	}
}

// TestTranslateDrift: a spine entry the data month lacks is served with
// drift and no name, and translates on.
func TestTranslateDrift(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab/drift")
	ans, err := tr.Translate(contract.TranslateRequest{From: contract.TranslateFrom{Vocabulary: "yard", Entry: map[string]string{"make": "Imperial", "model": "Other"}}, To: []string{"yard"}})
	if err != nil {
		t.Fatal(err)
	}
	conforms(t, "Translation", ans)
	if got, want := summary(ans), []string{"from mapped", "spine make:99999 drift", "yard: Imperial/Other via make:99999"}; !slices.Equal(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
	if s := ans.Spine[0]; s.Name != "" || !s.Drift || s.Under != nil {
		t.Errorf("the drifted spine entry %+v", s)
	}
}

// TestTranslateWithoutFiles: with nothing loaded, as nitrovin.app runs, a
// VIN or a vPIC id still translates to the spine, and any vocabulary is
// unknown.
func TestTranslateWithoutFiles(t *testing.T) {
	tr, err := NewTranslator(embeddedDecoder(t))
	if err != nil {
		t.Fatal(err)
	}
	ans, err := tr.Translate(contract.TranslateRequest{From: contract.TranslateFrom{VIN: ram2010}})
	if err != nil {
		t.Fatal(err)
	}
	conforms(t, "Translation", ans)
	if got, want := summary(ans), []string{"from mapped", "spine model:1938 Ram", "spine make:476 Dodge"}; !slices.Equal(got, want) {
		t.Errorf("got %v; want %v", got, want)
	}
	if len(ans.To) != 0 || len(ans.Vocabularies) != 0 || ans.Now.IsZero() {
		t.Errorf("to %v, vocabularies %v, now %v", ans.To, ans.Vocabularies, ans.Now)
	}
	_, err = tr.Translate(contract.TranslateRequest{From: contract.TranslateFrom{VIN: ram2010}, To: []string{"yard"}})
	if p, ok := err.(*contract.Problem); !ok || p.Pointer != "/to/0" {
		t.Errorf("a target with nothing loaded: %v", err)
	}
	l, err := tr.List(contract.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	conforms(t, "List", l)
	if len(l.Entries) != 1 || l.Entries[0].(contract.CatalogEntry).Name != "vpic" {
		t.Errorf("the catalog with nothing loaded: %+v", l.Entries)
	}
}

// TestTranslateBatch: answers in order, as Translate gives them, one clock
// reading for the items without now, and a refusal naming the item.
func TestTranslateBatch(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	frozen := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	reqs := []contract.TranslateRequest{
		{From: contract.TranslateFrom{VIN: ram2010}, To: []string{"yard"}},
		{From: contract.TranslateFrom{SpineRef: contract.SpineRef{Spine: "parts", ID: "brake-pads"}}, To: []string{"listing"}, Now: frozen},
		{From: contract.TranslateFrom{SpineRef: contract.SpineRef{Make: 476}}},
	}
	got, err := tr.TranslateBatch(reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(reqs) || got[0].Now != got[2].Now || !got[1].Now.Equal(frozen) {
		t.Fatalf("%d answers, now %v, %v, %v; want %d, one clock for 0 and 2, %v for 1", len(got), got[0].Now, got[1].Now, got[2].Now, len(reqs), frozen)
	}
	for i, req := range reqs {
		req.Now = got[i].Now
		want, err := tr.Translate(req)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[i], want) {
			t.Errorf("answer %d: %+v; want Translate's %+v", i, got[i], want)
		}
	}
	reqs[2].To = []string{"listing"}
	_, err = tr.TranslateBatch(reqs)
	if p, ok := err.(*contract.Problem); !ok || p.Type != contract.InvalidRequest || p.Pointer != "/2/to/0" {
		t.Errorf("a refused item: %v; want an invalid request at /2/to/0", err)
	}
}

// TestList: the catalog, each kind's entries in list order, where, and
// every refusal. Every answer validates against decode.schema.json's List.
func TestList(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	for _, tc := range []struct {
		name string
		req  string
		want []string // each entry's compact JSON, then "next" when there is one
		ptr  string
	}{
		{
			name: "the catalog: vpic, then the files in load order",
			req:  `{}`,
			want: []string{
				`{"name":"vpic","kind":"spine","version":"` + embedded.Month + `","subject":"vehicle"}`,
				`{"name":"classics","kind":"extension-set","version":"2026.11","subject":"vehicle","spine":"vpic"}`,
				`{"name":"listing","kind":"vocabulary","version":"2026.11","subject":"part","fields":["category"],"spine":"parts"}`,
				`{"name":"parts","kind":"spine","version":"2026.11","subject":"part"}`,
				`{"name":"yard","kind":"vocabulary","version":"2026.11","subject":"vehicle","fields":["make","model"],"spine":"vpic"}`,
			},
		},
		{
			name: "a vocabulary's entries with their mappings, by normalized fields",
			req:  `{"of":"yard","where":{"make":"DODGE"}}`,
			want: []string{
				`{"entry":{"make":"Dodge","model":"Other"},"mappings":[{"make":476}],"note":"any Dodge"}`,
				`{"entry":{"make":"Dodge","model":"Ram 1500"},"mappings":[{"model":1938,"last":2012,"qualifiers":{"34":["1500","1500 (1/2 Ton)","DS (1500)"]},"origin":"curated"},{"model":13620,"first":2013,"origin":"curated","note":"Ram is its own make from 2013"}]}`,
				`{"entry":{"make":"Dodge","model":"Ram 1500 4x4"},"mappings":[{"model":1938,"last":2012,"qualifiers":{"15":[2],"34":["1500","DS (1500)"]}},{"model":13620,"first":2013,"qualifiers":{"15":[2]}}]}`,
			},
		},
		{
			name: "an unmapped entry, by every field",
			req:  `{"of":"yard","where":{"make":"studebaker","model":" Lark "}}`,
			want: []string{`{"entry":{"make":"Studebaker","model":"Lark"},"mappings":[],"note":"listed, not yet placed"}`},
		},
		{
			name: "a make's models",
			req:  `{"of":"vpic","where":{"make":496},"limit":2}`,
			want: []string{`{"model":13620,"name":"1500","under":{"make":496},"years":[{"first":2013}]}`, `{"model":13621,"name":"2500","under":{"make":496},"years":[{"first":2013}]}`, "next"},
		},
		{name: "an unknown make has no models", req: `{"of":"vpic","where":{"make":"2147483647"}}`},
		{
			name: "a declared spine's entries, by id",
			req:  `{"of":"parts","limit":3}`,
			want: []string{`{"spine":"parts","id":"300"}`, `{"spine":"parts","id":"brake-pads","name":"Brake Pads"}`, `{"spine":"parts","id":"brake-pads-motorcycle","name":"Brake Pads, Motorcycle"}`, "next"},
		},
		{
			name: "an extension set's extensions, by id",
			req:  `{"of":"classics"}`,
			want: []string{
				`{"ext":"ext:classics/corvair","name":"Corvair","under":{"make":467}}`,
				`{"ext":"ext:classics/packard","name":"Packard"}`,
				`{"ext":"ext:classics/packard-clipper","name":"Clipper","under":{"ext":"ext:classics/packard"}}`,
			},
		},

		{name: "of nothing loaded", req: `{"of":"ebay"}`, ptr: "/of"},
		{name: "a field the vocabulary lacks", req: `{"of":"yard","where":{"trim":"SLT"}}`, ptr: "/where/trim"},
		{name: "vpic takes only make", req: `{"of":"vpic","where":{"name":"Dodge"}}`, ptr: "/where/name"},
		{name: "vpic's year needs make", req: `{"of":"vpic","where":{"year":2004}}`, ptr: "/where/year"},
		{name: "vpic's year is a year", req: `{"of":"vpic","where":{"make":496,"year":"new"}}`, ptr: "/where/year"},
		{name: "vpic's make is an id", req: `{"of":"vpic","where":{"make":"Dodge"}}`, ptr: "/where/make"},
		{name: "a spine takes no where", req: `{"of":"parts","where":{"id":"300"}}`, ptr: "/where/id"},
		{name: "the catalog takes no where", req: `{"where":{"make":"Dodge"}}`, ptr: "/where"},
		{name: "the catalog is not paged", req: `{"limit":10}`, ptr: "/limit"},
		{name: "limit over 1,000", req: `{"of":"vpic","limit":1001}`, ptr: "/limit"},
		{name: "limit under 1", req: `{"of":"vpic","limit":0}`, ptr: "/limit"},
		{name: "an after no list gave", req: `{"of":"yard","after":"not a cursor"}`, ptr: "/after"},
		{name: "another list's after", req: `{"of":"yard","after":"WyJicmFrZSBwYWRzIl0"}`, ptr: "/after"},
		{name: "an unknown field", req: `{"of":"yard","page":2}`, ptr: "/page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := contract.ParseListRequest([]byte(tc.req))
			if err == nil {
				var l contract.List
				if l, err = tr.List(req); err == nil {
					if tc.ptr != "" {
						t.Fatalf("answered %+v; want a refusal at %s", l, tc.ptr)
					}
					conforms(t, "List", l)
					var got []string
					for _, e := range l.Entries {
						b, err := json.Marshal(e)
						if err != nil {
							t.Fatal(err)
						}
						got = append(got, string(b))
					}
					if l.Next != "" {
						got = append(got, "next")
					}
					if !slices.Equal(got, tc.want) {
						t.Errorf("got\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(tc.want, "\n\t"))
					}
					return
				}
			}
			p, ok := err.(*contract.Problem)
			if !ok || p.Type != contract.InvalidRequest || p.Pointer != tc.ptr || tc.ptr == "" {
				t.Fatalf("error %v; want #invalid-request at %q", err, tc.ptr)
			}
		})
	}
}

// TestListPaging pages lists end to end, each page's next the following
// page's after: no entry is lost or repeated, the order is the one-page
// order, and the last page has no next.
func TestListPaging(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	if contract.MaxListLimit != vocab.MaxLimit || contract.DefaultListLimit != vocab.DefaultLimit {
		t.Fatal("contract's list limits are not vocab's")
	}
	page := func(req contract.ListRequest) []string {
		t.Helper()
		var out []string
		for pages := 0; ; pages++ {
			l, err := tr.List(req)
			if err != nil {
				t.Fatal(err)
			}
			if len(l.Entries) == 0 && l.Next == "" && pages > 0 {
				t.Fatal("an empty page after a next")
			}
			for _, e := range l.Entries {
				b, _ := json.Marshal(e)
				out = append(out, string(b))
			}
			if l.Next == "" {
				return out
			}
			req.After = l.Next
		}
	}
	for _, tc := range []struct {
		name  string
		req   contract.ListRequest
		limit int
	}{
		{"yard", contract.ListRequest{Of: "yard"}, 3},
		{"yard's Dodges", contract.ListRequest{Of: "yard", Where: map[string]string{"make": "dodge"}}, 1},
		{"parts", contract.ListRequest{Of: "parts"}, 4},
		{"vPIC's makes", contract.ListRequest{Of: "vpic"}, 1000},
		{"Dodge's models", contract.ListRequest{Of: "vpic", Where: map[string]string{"make": "476"}}, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			whole := tc.req
			whole.Limit = contract.MaxListLimit
			want := page(whole)
			paged := tc.req
			paged.Limit = tc.limit
			got := page(paged)
			if len(got) <= tc.limit {
				t.Fatalf("%d entries fit one page of %d", len(got), tc.limit)
			}
			if !slices.Equal(got, want) {
				t.Errorf("paged by %d: %d entries; one page: %d", tc.limit, len(got), len(want))
			}
			seen := map[string]bool{}
			for _, e := range got {
				if seen[e] {
					t.Errorf("%s listed twice", e)
				}
				seen[e] = true
			}
		})
	}
	// Every vPIC make is listed, and the default page is 100.
	l, err := tr.List(contract.ListRequest{Of: "vpic"})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != contract.DefaultListLimit || l.Next == "" {
		t.Errorf("the default page has %d entries, next %q", len(l.Entries), l.Next)
	}
	makes := 0
	for range newMonth(tr.d.artifact, tr.d.info.Elements).Makes() {
		makes++
	}
	if got := len(page(contract.ListRequest{Of: "vpic", Limit: contract.MaxListLimit})); got != makes {
		t.Errorf("listed %d makes; the month has %d", got, makes)
	}
}
