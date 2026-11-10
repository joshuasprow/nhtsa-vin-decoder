package vocab

import (
	"bytes"
	"iter"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// sourceModel is one of edgeSource's models: its make, as Model gives it,
// and the makes Models lists it under.
type sourceModel struct {
	make   int32
	name   string
	listed []int32
}

// edgeSource is a BundleSource with what an artifact can hold but
// 2026_10 does not: a make twice in Makes, a model under no make, one
// listed under 0 and under its make, one listed under a make Makes lacks,
// a model under make 0 alone, and values of an element that is not
// public.
type edgeSource struct{}

var edgeModels = map[int32]sourceModel{
	10: {1, "Ten", []int32{1}},
	11: {1, "Eleven", []int32{1}},
	12: {0, "Twelve", nil},             // no make_model row
	13: {2, "Thirteen", []int32{0, 2}}, // make_model rows 0 and 2
	14: {9, "Fourteen", []int32{9}},    // make 9 is not a lookup make
	15: {0, "Fifteen", []int32{0}},     // make_model row 0 only
}

func (edgeSource) Name() string { return "2031_02" }

func (edgeSource) Make(id int32) (string, bool) {
	switch id {
	case 1:
		return "One", true
	case 2:
		return "Two", true
	}
	return "", false
}

func (edgeSource) Model(id int32) (int32, string, bool) {
	m, ok := edgeModels[id]
	return m.make, m.name, ok
}

func (edgeSource) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range []int32{1, 2, 2} {
			name, _ := edgeSource{}.Make(id)
			if !yield(id, name) {
				return
			}
		}
	}
}

func (edgeSource) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range slices.Sorted(maps.Keys(edgeModels)) {
			if m := edgeModels[id]; slices.Contains(m.listed, make) && !yield(id, m.name) {
				return
			}
		}
	}
}

func (edgeSource) ModelIDs() iter.Seq[int32] { return maps.Keys(edgeModels) }

func (edgeSource) Elements() iter.Seq[int32] { return slices.Values([]int32{26, 34}) }

func (edgeSource) Element(id int32) (lookup, ok bool) {
	switch id {
	case 26:
		return true, true
	case 34:
		return false, true
	}
	return false, false
}

var edgeValues = map[int32][]string{26: {"1", "2", "9"}, 34: {"DS (1500)", "", "2500"}, 140: {"x"}}

func (edgeSource) Attribute(element, id int32) bool {
	return slices.Contains(edgeValues[element], strconv.Itoa(int(id)))
}

func (edgeSource) Text(element int32, text string) bool {
	return slices.Contains(edgeValues[element], text)
}

func (edgeSource) Values() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for el, vs := range edgeValues {
			for _, v := range append(vs, vs[0]) { // a value twice
				if !yield(el, v) {
					return
				}
			}
		}
	}
}

// TestBundle: a bundle read back answers every question as its source
// does, edge cases included, and writes back to the same bytes.
func TestBundle(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteBundle(&buf, edgeSource{}); err != nil {
		t.Fatal(err)
	}
	written := slices.Clone(buf.Bytes())
	b, err := ReadBundle(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var src edgeSource
	if b.Name() != src.Name() {
		t.Errorf("Name %q", b.Name())
	}
	collect := func(seq iter.Seq2[int32, string]) []string {
		var out []string
		for id, name := range seq {
			out = append(out, strconv.Itoa(int(id))+" "+name)
		}
		return out
	}
	if got, want := collect(b.Makes()), collect(src.Makes()); !slices.Equal(got, want) {
		t.Errorf("Makes %v; want %v", got, want)
	}
	for id := int32(-1); id <= 16; id++ {
		if got, want := collect(b.Models(id)), collect(src.Models(id)); !slices.Equal(got, want) {
			t.Errorf("Models(%d) %v; want %v", id, got, want)
		}
		gName, gOK := b.Make(id)
		wName, wOK := src.Make(id)
		if gName != wName || gOK != wOK {
			t.Errorf("Make(%d) %q %t; want %q %t", id, gName, gOK, wName, wOK)
		}
		gMake, gName, gOK := b.Model(id)
		wMake, wName, wOK := src.Model(id)
		if gMake != wMake || gName != wName || gOK != wOK {
			t.Errorf("Model(%d) %d %q %t; want %d %q %t", id, gMake, gName, gOK, wMake, wName, wOK)
		}
	}
	if got, want := slices.Sorted(b.ModelIDs()), slices.Sorted(src.ModelIDs()); !slices.Equal(got, want) {
		t.Errorf("ModelIDs %v; want %v", got, want)
	}
	for _, el := range []int32{0, 25, 26, 27, 34, 140} {
		gLookup, gOK := b.Element(el)
		wLookup, wOK := src.Element(el)
		if gLookup != wLookup || gOK != wOK {
			t.Errorf("Element(%d) %t %t; want %t %t", el, gLookup, gOK, wLookup, wOK)
		}
		for _, v := range []string{"", "1", "2", "3", "9", "x", "DS (1500)", "ds (1500)", "2500"} {
			if g, w := b.Text(el, v), src.Text(el, v); g != w {
				t.Errorf("Text(%d, %q) %t; want %t", el, v, g, w)
			}
			if id, err := strconv.Atoi(v); err == nil {
				if g, w := b.Attribute(el, int32(id)), src.Attribute(el, int32(id)); g != w {
					t.Errorf("Attribute(%d, %d) %t; want %t", el, id, g, w)
				}
			}
		}
	}
	var again bytes.Buffer
	if err := WriteBundle(&again, b); err != nil || !bytes.Equal(again.Bytes(), written) {
		t.Errorf("the bundle written back (%v):\n%s\nwant\n%s", err, again.Bytes(), written)
	}
}

// TestReadBundleRefuses: another format, columns of different lengths,
// and what is not a bundle.
func TestReadBundleRefuses(t *testing.T) {
	for _, tc := range []struct{ doc, want string }{
		{`{"format":2,"month":"2026_10"}`, "spine bundle: format 2; this release reads format 1"},
		{`{"format":1,"makes":{"ids":[1,2],"names":["One"]}}`, "spine bundle: the makes columns hold [2 1] values"},
		{`{"format":1,"values":{"x":["1"]}}`, `spine bundle: values of "x": not an element id`},
		{`<!doctype html>`, "spine bundle: invalid character '<' looking for beginning of value"},
	} {
		_, err := ReadBundle(strings.NewReader(tc.doc))
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s: %v; want %s", tc.doc, err, tc.want)
		}
	}
}
