package ui

import (
	"io"
	"log"
	"net/url"
	"slices"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestFromKind: a query's from and a translation's are one kind each.
func TestFromKind(t *testing.T) {
	for q, want := range map[string]fromKind{
		"":                                        fromNone,
		"tab=id&to=yard":                          fromNone,
		"from.vin=":                               fromVIN,
		"from.vocabulary=yard&from.entry.make=A":  fromEntry,
		"from.spine=parts&from.id=brake-pads":     fromSpine,
		"from.make=476":                           fromMake,
		"from.model=1861&model_year=2003":         fromModel,
		"from.ext=ext:classics/packard-clipper":   fromExt,
		"from.vin=1HGCM82633A004352&to=yard&to=x": fromVIN,
	} {
		v, err := url.ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		if got := queryFrom(v); got != want {
			t.Errorf("queryFrom(%q) = %q; want %q", q, got, want)
		}
	}
	for _, c := range []struct {
		from contract.TranslationFrom
		want fromKind
	}{
		{contract.TranslationFrom{VIN: "1HGCM82633A004352"}, fromVIN},
		{contract.TranslationFrom{Vocabulary: "yard", Entry: map[string]string{"make": "A"}}, fromEntry},
		{contract.TranslationFrom{SpineRef: contract.SpineRef{Spine: "parts", ID: "brake-pads"}}, fromSpine},
		{contract.TranslationFrom{SpineRef: contract.SpineRef{Make: 476}}, fromMake},
		{contract.TranslationFrom{SpineRef: contract.SpineRef{Model: 1861}}, fromModel},
		{contract.TranslationFrom{SpineRef: contract.SpineRef{Ext: "ext:classics/packard-clipper"}}, fromExt},
	} {
		if got := translatedFrom(c.from); got != c.want {
			t.Errorf("translatedFrom(%+v) = %q; want %q", c.from, got, c.want)
		}
	}
}

// TestDefaultTargets: a lookup naming no target goes, in the browser as
// on the server, to every vocabulary loaded on its spine but its own,
// whatever the visitor's selection; before a lookup, to none.
func TestDefaultTargets(t *testing.T) {
	s, d := server(t, "classics"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	if err := l.Select("vpic", []string{}); err != nil {
		t.Fatal(err)
	}
	h := NewInBrowser(l, BrowserConfig{}, log.New(io.Discard, "", 0))
	cat, err := h.catalog()
	if err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		"":                           nil,
		"tab=entry":                  nil,
		"from.vocabulary=yard":       nil,
		"from.vin=1D7RV1GT5AS123456": {"yard"},
		"from.model=1861":            {"yard"},
		"from.vocabulary=yard&from.entry.make=Dodge":                  nil,
		"from.vocabulary=nothing&from.entry.make=Dodge":               nil,
		"from.ext=ext:classics/packard-clipper&model_year=1955&now=x": {"yard"},
	} {
		v, err := url.ParseQuery(q)
		if err != nil {
			t.Fatal(err)
		}
		if got := h.defaultTargets(v, cat); !slices.Equal(got, want) {
			t.Errorf("defaultTargets(%q) = %q; want %q", q, got, want)
		}
	}
}
