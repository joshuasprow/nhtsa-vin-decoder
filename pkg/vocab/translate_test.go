package vocab

import (
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"
	"testing"
)

// fixtureMonth is a Month without an artifact holding the vPIC entries
// testdata/vocab maps to, named as 2026_10 names them.
type fixtureMonth struct{}

var (
	fixtureMakes  = map[int32]string{467: "Chevrolet", 476: "Dodge", 496: "Ram"}
	fixtureModels = map[int32]struct {
		make int32
		name string
	}{1850: {467, "Silverado"}, 1938: {476, "Ram"}, 13620: {496, "1500"}}
)

func (fixtureMonth) Name() string { return "2026_10" }
func (fixtureMonth) Make(id int32) (string, bool) {
	name, ok := fixtureMakes[id]
	return name, ok
}
func (fixtureMonth) Model(id int32) (int32, string, bool) {
	m, ok := fixtureModels[id]
	return m.make, m.name, ok
}
func (fixtureMonth) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range []int32{467, 476, 496} {
			if !yield(id, fixtureMakes[id]) {
				return
			}
		}
	}
}
func (fixtureMonth) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range []int32{1850, 1938, 13620} {
			if m := fixtureModels[id]; m.make == make && !yield(id, m.name) {
				return
			}
		}
	}
}
func (fixtureMonth) Element(id int32) (bool, bool)        { return id != 34, true }
func (fixtureMonth) Attribute(element, id int32) bool     { return true }
func (fixtureMonth) Text(element int32, text string) bool { return true }

func fixtureSet(t *testing.T) *Set {
	t.Helper()
	files, err := Open("../../testdata/vocab")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSet(fixtureMonth{}, files)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// routeLines writes a translation as lines: status, then each spine entry and
// each target entry with what stayed unconfirmed.
func routeLines(tr *Translation) []string {
	out := []string{string(tr.Status)}
	for _, s := range tr.Spine {
		out = append(out, "spine "+refText(s.Ref)+unconfirmedLine(s.Unconfirmed))
	}
	for _, to := range tr.To {
		for _, m := range to.Matches {
			out = append(out, to.Vocabulary.Name+": "+strings.Join(m.Entry.Fields, "/")+unconfirmedLine(m.Unconfirmed))
		}
	}
	return out
}

func refText(r Ref) string {
	switch {
	case r.Make != 0:
		return fmt.Sprint("make:", r.Make)
	case r.Model != 0:
		return fmt.Sprint("model:", r.Model)
	case r.Ext != "":
		return r.Ext
	}
	return r.Spine + "/" + r.ID
}

func unconfirmedLine(u Unconfirmed) string {
	if u.IsZero() {
		return ""
	}
	return fmt.Sprintf(" %v %t", u.Elements, u.ModelYear)
}

// TestTranslateDecoded checks qualifiers and years against decodes no VIN
// is needed for (ADR 0009): a decoded value outside a qualifier's set rules
// a mapping out; an element the decode left empty, or no model year (a
// partial VIN), keeps it and leaves it unconfirmed.
func TestTranslateDecoded(t *testing.T) {
	s := fixtureSet(t)
	ram := From{Vocabulary: "yard", Entry: map[string]string{"make": "Dodge", "model": "Ram 1500 4x4"}}
	series := func(v string) Value { return Value{IsText: true, Text: v} }
	for _, tc := range []struct {
		name string
		d    *Decoded
		want []string
	}{
		{
			name: "everything decoded and in the sets",
			d:    &Decoded{ModelYear: new(2010), Values: map[int32][]Value{15: {{Attribute: 2}}, 34: {series("DS (1500)")}}},
			want: []string{"mapped", "spine model:1938", "spine make:476"},
		},
		{
			name: "Series left empty: kept, unconfirmed",
			d:    &Decoded{ModelYear: new(2010), Values: map[int32][]Value{15: {{Attribute: 2}}}},
			want: []string{"mapped", "spine model:1938 [34] false", "spine make:476 [34] false"},
		},
		{
			name: "Series outside the set",
			d:    &Decoded{ModelYear: new(2010), Values: map[int32][]Value{15: {{Attribute: 2}}, 34: {series("DR (1500)")}}},
			want: []string{"mapped"},
		},
		{
			name: "a multi-valued element: any value satisfies",
			d:    &Decoded{ModelYear: new(2015), Values: map[int32][]Value{15: {{Attribute: 7}, {Attribute: 2}}}},
			want: []string{"mapped", "spine model:13620", "spine make:496"},
		},
		{
			name: "no model year: every year range a candidate",
			d:    &Decoded{Values: map[int32][]Value{15: {{Attribute: 2}}, 34: {series("1500")}}},
			want: []string{"mapped", "spine model:1938 [] true", "spine make:476 [] true", "spine model:13620 [] true", "spine make:496 [] true"},
		},
		{
			name: "no decode: qualifiers ignored, unconfirmed",
			want: []string{"mapped", "spine model:1938 [15 34] true", "spine make:476 [15 34] true", "spine model:13620 [15] true", "spine make:496 [15] true"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := s.Translate(Query{From: ram, Decoded: tc.d})
			if err != nil {
				t.Fatal(err)
			}
			if got := routeLines(tr); !slices.Equal(got, tc.want) {
				t.Errorf("got %q; want %q", got, tc.want)
			}
		})
	}

	// From a VIN's decode: its model and make, and a target entry reached
	// both ways is one entry with two vias, confirmed when one is.
	tr, err := s.Translate(Query{From: From{VIN: true}, To: []string{"yard"}, Decoded: &Decoded{Make: 476, Model: 1938, ModelYear: new(2010), Values: map[int32][]Value{}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mapped", "spine model:1938", "spine make:476", "yard: Dodge/Other", "yard: Dodge/Ram 1500 [34] false", "yard: Dodge/Ram 1500 4x4 [15 34] false"}
	if got := routeLines(tr); !slices.Equal(got, want) {
		t.Errorf("from a decode: got %q; want %q", got, want)
	}
}

// yearMonth is fixtureMonth with Model Year (29) and Series (34) text
// elements, and no element 999.
type yearMonth struct{ fixtureMonth }

func (yearMonth) Element(id int32) (bool, bool) { return id != 29 && id != 34, id != 999 }

// TestNewDecoded reads a decode's items as translation does, whatever
// decoded the VIN: lookup values by attribute id, text by text, nothing
// from an empty item, an element the month lacks or a code list.
func TestNewDecoded(t *testing.T) {
	d := NewDecoded(yearMonth{}, []Item{
		{ElementID: 26, Value: "DODGE", AttributeID: "476"},
		{ElementID: 28, Value: "Ram", AttributeID: "1938"},
		{ElementID: 29, Value: "2010"},
		{ElementID: 34, Value: "DS (1500)"},
		{ElementID: 15, Value: "4WD", AttributeID: "2"},
		{ElementID: 15, Value: "AWD", AttributeID: "7"},
		{ElementID: 143, Value: "0 - VIN decoded clean", AttributeID: "0,1"}, // Error Code's code list
		{ElementID: 38, Value: ""},
		{ElementID: 999, Value: "x", AttributeID: "1"},
	})
	if d.Make != 476 || d.Model != 1938 || d.ModelYear == nil || *d.ModelYear != 2010 {
		t.Errorf("make %d, model %d, year %v; want 476, 1938, 2010", d.Make, d.Model, d.ModelYear)
	}
	want := map[int32][]Value{
		26: {{Attribute: 476}},
		28: {{Attribute: 1938}},
		29: {{IsText: true, Text: "2010"}},
		34: {{IsText: true, Text: "DS (1500)"}},
		15: {{Attribute: 2}, {Attribute: 7}},
	}
	if fmt.Sprint(d.Values) != fmt.Sprint(want) {
		t.Errorf("values %v; want %v", d.Values, want)
	}
	if d := NewDecoded(yearMonth{}, nil); d.Make != 0 || d.Model != 0 || d.ModelYear != nil || len(d.Values) != 0 {
		t.Errorf("no items: %+v; want nothing decoded", d)
	}
}

// TestTranslateRefusals: what the loaded files refuse is a RequestError
// with a pointer into the request.
func TestTranslateRefusals(t *testing.T) {
	s := fixtureSet(t)
	for _, tc := range []struct {
		q   Query
		ptr string
	}{
		{Query{From: From{Vocabulary: "ebay", Entry: map[string]string{}}}, "/from/vocabulary"},
		{Query{From: From{Vocabulary: "vpic", Entry: map[string]string{}}}, "/from/vocabulary"},
		{Query{From: From{Vocabulary: "parts", Entry: map[string]string{}}}, "/from/vocabulary"},
		{Query{From: From{Vocabulary: "yard", Entry: map[string]string{"make": "Dodge", "model": "Ram", "a/b": ""}}}, "/from/entry/a~1b"},
		{Query{From: From{Vocabulary: "yard", Entry: map[string]string{"model": "Ram"}}}, "/from/entry"},
		{Query{From: From{Ref: Ref{Spine: "vpic", Target: Target{ID: "1"}}}}, "/from/spine"},
		{Query{From: From{Ref: Ref{Spine: "yard", Target: Target{ID: "1"}}}}, "/from/spine"},
		{Query{From: From{Ref: Ref{Target: Target{Ext: "ext:yard/1"}}}}, "/from/ext"},
		{Query{From: From{Ref: Ref{Target: Target{Make: 476}}}, To: []string{"listing"}}, "/to/0"},
		{Query{From: From{}}, "/from"},
	} {
		_, err := s.Translate(tc.q)
		if r, ok := errors.AsType[*RequestError](err); !ok || r.Pointer != tc.ptr {
			t.Errorf("%+v: error %v; want a RequestError at %s", tc.q, err, tc.ptr)
		}
	}
}

// TestListPagingEveryLimit pages the yard by every limit: the pages joined
// are the one-page order, nothing lost or repeated.
func TestListPagingEveryLimit(t *testing.T) {
	s := fixtureSet(t)
	all, err := s.List(ListQuery{Of: "yard", Limit: MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if all.Next != "" || len(all.Entries) != 8 {
		t.Fatalf("one page: %d entries, next %q", len(all.Entries), all.Next)
	}
	for limit := 1; limit <= len(all.Entries); limit++ {
		var got []*Entry
		q := ListQuery{Of: "yard", Limit: limit}
		for {
			l, err := s.List(q)
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, l.Entries...)
			if l.Next == "" {
				break
			}
			q.After = l.Next
		}
		if !slices.Equal(got, all.Entries) {
			t.Errorf("limit %d: pages differ from one page", limit)
		}
	}
	var names []string
	for _, e := range all.Entries {
		names = append(names, strings.Join(e.Fields, "/"))
	}
	want := []string{"Chevrolet/Corvair", "Chevy/Silverado", "Dodge/Other", "Dodge/Ram 1500", "Dodge/Ram 1500 4x4", "Packard/Clipper", "Ram/Other", "Studebaker/Lark"}
	if !slices.Equal(names, want) {
		t.Errorf("order %q; want %q", names, want)
	}
}
