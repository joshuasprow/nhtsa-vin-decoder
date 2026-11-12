package nitrovin

import (
	"bufio"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Models the model values cases ask about (ADR 0015).
const (
	silveradoModel = "1850" // Chevrolet Silverado, a VIN schema shared with the van line
	f150Model      = "1801" // Ford F-150, a VIN schema shared with the Econoline
)

// listValues lists every model value of model and element, one page of
// 1,000.
func listValues(t *testing.T, tr *Translator, model, element string) []contract.ModelValueEntry {
	t.Helper()
	l, err := tr.List(contract.ListRequest{Of: "vpic", Where: map[string]string{"model": model, "element": element}, Limit: contract.MaxListLimit})
	if err != nil {
		t.Fatal(err)
	}
	if l.Next != "" {
		t.Fatalf("model %s element %s: more than one page", model, element)
	}
	conforms(t, "List", l)
	out := make([]contract.ModelValueEntry, len(l.Entries))
	for i, e := range l.Entries {
		v, ok := e.(contract.ModelValueEntry)
		if !ok {
			t.Fatalf("model %s element %s: entry %T", model, element, e)
		}
		out[i] = v
	}
	return out
}

func texts(vs []contract.ModelValueEntry) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.Value.Text
	}
	return out
}

// TestModelValuesAnotherModelsSeries: a model's Series values are its own,
// not those of another model whose VIN schema it shares, which a schema-wide
// reading would give it. The Silverado lists its pickups' series and no van
// series; the F-150 lists no Econoline.
func TestModelValuesAnotherModelsSeries(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")
	for _, tc := range []struct {
		model      string
		want, none []string
	}{
		{silveradoModel, []string{"1500", "2500 HD", "3500"}, []string{"1500 Cargo", "2500 Cargo", "3500 Cargo"}},
		{f150Model, []string{"XL", "XLT", "Lariat", "Raptor"}, []string{"Econoline"}},
	} {
		got := texts(listValues(t, tr, tc.model, "34"))
		for _, v := range tc.want {
			if !slices.Contains(got, v) {
				t.Errorf("model %s's Series %q lack %q", tc.model, got, v)
			}
		}
		for _, v := range got {
			for _, no := range tc.none {
				if strings.Contains(v, no) {
					t.Errorf("model %s's Series list %q, another model's", tc.model, v)
				}
			}
		}
	}
}

// TestModelValuesCoverDecodes: every value a decode of a parity-corpus VIN
// gives an element is among its model's model values for that element, or
// the element is one list refuses, written from the VIN itself: the index
// leaves nothing out. Only a formula pattern's values, which ADR 0015
// leaves out, are not checked, and the two kinds of decode its amendment
// names: one with no model year, whose pass reads every year band of the
// WMI code, and one of a VIN holding '#', which is no VIN character and
// which only a formula pattern's key holds.
func TestModelValuesCoverDecodes(t *testing.T) {
	d := embeddedDecoder(t)
	f, err := os.Open("../../testdata/parity/2026_10/corpus.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	vins, checked := 0, 0
	sources, refused := map[string]bool{}, map[int32]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		vin, year, hinted := strings.Cut(line, "\t")
		if strings.Contains(vin, "#") {
			continue
		}
		var hint contract.Null[int32]
		if hinted {
			y, err := strconv.Atoi(year)
			if err != nil {
				t.Fatalf("corpus line %q: %v", line, err)
			}
			hint = contract.Value(int32(y))
		}
		p, err := d.engine.Project(vin, hint, now)
		if err != nil {
			continue // a VIN the procedure raises on decodes nothing
		}
		model := -1
		dated := false
		for _, it := range p.Items {
			switch it.ElementID {
			case elementModel:
				if model, err = strconv.Atoi(it.AttributeID.V); err != nil {
					t.Fatalf("%s: model %q", vin, it.AttributeID.V)
				}
			case 29: // Model Year
				dated = true
			}
		}
		if model < 0 || !dated {
			continue
		}
		vins++
		for _, it := range p.Items {
			if it.Source.V == "Formula Pattern" || !it.AttributeID.Valid {
				continue
			}
			values, ok := d.month.ModelValues(int32(model), it.ElementID)
			if !ok {
				refused[it.ElementID] = true
				continue
			}
			checked++
			sources[strings.SplitN(it.Source.V, " ", 2)[0]] = true
			if !slices.Contains(values, it.AttributeID.V) {
				t.Errorf("%s (model %d): element %d's %q (%s, VIN schema %v) is not among the model's values", vin, model, it.ElementID, it.AttributeID.V, it.Source.V, it.VinSchemaID)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if vins < 300 {
		t.Errorf("checked %d VINs; the corpus has over 300 with a model and a year", vins)
	}
	t.Logf("%d values of %d VINs, each among its model's; sources %v; refused elements %v", checked, vins, slices.Sorted(maps.Keys(sources)), slices.Sorted(maps.Keys(refused)))
	if !sources["Conversion"] {
		t.Error("no conversion value was checked")
	}
}

// TestModelValuesInMonth: a model's values are values the month's decode
// can give the element: the index adds nothing the month lacks.
func TestModelValuesInMonth(t *testing.T) {
	d := embeddedDecoder(t)
	m := d.month
	for _, el := range []int32{15, 34, 26, 27, 39, 28} {
		lookup, _ := m.Element(el)
		for _, model := range []int32{1850, 1801, 1938, 13620} {
			values, _ := m.ModelValues(model, el)
			for _, v := range values {
				ok := m.Text(el, v)
				if lookup {
					id, err := strconv.Atoi(v)
					ok = err == nil && m.Attribute(el, int32(id))
				}
				if !ok {
					t.Errorf("model %d element %d: %q, a value the month never gives", model, el, v)
				}
			}
		}
	}
}

// TestListModelValues: list's model values, as rows, sorted, paged and
// refused (ADR 0015).
func TestListModelValues(t *testing.T) {
	tr := fixtureTranslator(t, "../../testdata/vocab")

	// A text element: exact text, no name, by normalized then exact text,
	// so case variants both appear.
	series := listValues(t, tr, silveradoModel, "34")
	got := texts(series)
	for i, v := range series {
		if !v.Value.IsText || v.Name != "" {
			t.Errorf("Series row %d is %+v; want text without a name", i, v)
		}
	}
	if !slices.Contains(got, "3/4 Ton") || !slices.Contains(got, "3/4 ton") {
		t.Errorf("Series %q: want both case variants of 3/4 Ton", got)
	}
	if !slices.IsSortedFunc(got, func(a, b string) int {
		if c := strings.Compare(vocab.Normalize(a), vocab.Normalize(b)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	}) {
		t.Errorf("Series %q are not by normalized then exact value", got)
	}

	// A lookup element: attribute ids, ascending, each with its name.
	drive := listValues(t, tr, silveradoModel, "15")
	b, err := json.Marshal(drive)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`{"value":2,"name":"4WD/4-Wheel Drive/4x4"}`, `{"value":7,"name":"4x2"}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("Drive Type %s lacks %s", b, want)
		}
	}
	if !slices.IsSortedFunc(drive, func(a, b contract.ModelValueEntry) int { return int(a.Value.Attribute - b.Value.Attribute) }) {
		t.Errorf("Drive Type %s is not by id", b)
	}

	// The model's own make and model.
	for _, tc := range []struct{ element, want string }{
		{"26", `[{"value":467,"name":"Chevrolet"}]`},
		{"28", `[{"value":1850,"name":"Silverado"}]`},
	} {
		if b, _ := json.Marshal(listValues(t, tr, silveradoModel, tc.element)); string(b) != tc.want {
			t.Errorf("element %s: %s; want %s", tc.element, b, tc.want)
		}
	}

	// Pages of two are the one page.
	var paged []string
	req := contract.ListRequest{Of: "vpic", Where: map[string]string{"model": silveradoModel, "element": "34"}, Limit: 2}
	for {
		l, err := tr.List(req)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range l.Entries {
			paged = append(paged, e.(contract.ModelValueEntry).Value.Text)
		}
		if l.Next == "" {
			break
		}
		req.After = l.Next
	}
	if !slices.Equal(paged, got) {
		t.Errorf("paged by 2: %q; one page: %q", paged, got)
	}

	// An unknown model has no values.
	if vs := listValues(t, tr, "2147483647", "34"); len(vs) != 0 {
		t.Errorf("an unknown model lists %v", vs)
	}

	for _, tc := range []struct{ name, req, ptr string }{
		{"model without element", `{"of":"vpic","where":{"model":1850}}`, "/where/model"},
		{"element without model", `{"of":"vpic","where":{"element":34}}`, "/where/element"},
		{"model with make", `{"of":"vpic","where":{"make":467,"model":1850,"element":34}}`, "/where/make"},
		{"element with make", `{"of":"vpic","where":{"make":467,"element":34}}`, "/where/make"},
		{"model alone with make", `{"of":"vpic","where":{"make":467,"model":1850}}`, "/where/make"},
		{"a model that is no id", `{"of":"vpic","where":{"model":"Silverado","element":34}}`, "/where/model"},
		{"an element that is no id", `{"of":"vpic","where":{"model":1850,"element":"Series"}}`, "/where/element"},
		{"an element the month lacks", `{"of":"vpic","where":{"model":1850,"element":999999}}`, "/where/element"},
		{"Model Year, decoded from the VIN", `{"of":"vpic","where":{"model":1850,"element":29}}`, "/where/element"},
		{"Error Code, decoded from the VIN", `{"of":"vpic","where":{"model":1850,"element":143}}`, "/where/element"},
		{"an unknown model and Model Year", `{"of":"vpic","where":{"model":2147483647,"element":29}}`, "/where/element"},
		{"a private element", `{"of":"vpic","where":{"model":1850,"element":` + privateElement(t, tr) + `}}`, "/where/element"},
		{"a year with model and element", `{"of":"vpic","where":{"model":1850,"element":34,"year":2014}}`, "/where/year"},
		{"a year with element", `{"of":"vpic","where":{"element":34,"year":2014}}`, "/where/year"},
		{"a year with model", `{"of":"vpic","where":{"model":1850,"year":2014}}`, "/where/year"},
		{"another where field", `{"of":"vpic","where":{"model":1850,"element":34,"trim":"LT"}}`, "/where/trim"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := contract.ParseListRequest([]byte(tc.req))
			if err != nil {
				t.Fatal(err)
			}
			l, err := tr.List(req)
			p, ok := err.(*contract.Problem)
			if !ok || p.Type != contract.InvalidRequest || p.Pointer != tc.ptr {
				t.Fatalf("answered %+v, %v; want #invalid-request at %s", l, err, tc.ptr)
			}
		})
	}
}

// privateElement is an element the artifact has that is not public.
func privateElement(t *testing.T, tr *Translator) string {
	t.Helper()
	el := tr.d.artifact.Table("element")
	private := el.Bools("isprivate")
	for r, id := range el.Int32s("id") {
		if p, _ := private.At(r); p {
			if _, public := tr.d.info.Elements.Find(id); public {
				t.Fatalf("element %d is private and public", id)
			}
			return strconv.Itoa(int(id))
		}
	}
	t.Skip("the month has no private element")
	return ""
}
