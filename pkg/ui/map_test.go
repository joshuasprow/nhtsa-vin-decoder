package ui

import (
	"io"
	"log"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// mapOf is the map of the translation q asks l for.
func mapOf(t *testing.T, l *Library, q string) mapPage {
	t.Helper()
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	req, err := contract.ParseTranslateQuery(v)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := l.Translate(req)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	cat, err := NewInBrowser(l, BrowserConfig{}, log.New(io.Discard, "", 0)).catalog()
	if err != nil {
		t.Fatal(err)
	}
	return newMapPage(tr, cat, l.Info().Elements, v.Get("model_year"), homePath+"?"+q)
}

// cards are p's cards, column by column: "1 Ram", a model under its make
// "1 > Ram".
func cards(p mapPage) []string {
	var out []string
	for c, col := range p.Columns {
		for _, it := range col.Items {
			if it.ID == "" {
				continue
			}
			s := string(rune('0'+c)) + " "
			if it.Under {
				s += "> "
			}
			out = append(out, s+it.Name)
		}
	}
	return out
}

// lights is what the card named name in column col lights: the other
// cards' names, sorted, and how many lines.
func lights(t *testing.T, p mapPage, col int, name string) ([]string, int) {
	t.Helper()
	names := map[string]string{}
	var lit string
	for c, column := range p.Columns {
		for _, it := range column.Items {
			names[it.ID] = it.Name
			if c == col && it.Name == name {
				lit = it.Lit
			}
		}
	}
	ids := strings.Fields(lit)
	if len(ids) == 0 || names[ids[0]] != name {
		t.Fatalf("no card %q in column %d, or it does not light itself: %q", name, col, lit)
	}
	var out []string
	lines := 0
	for _, id := range ids[1:] {
		if n, ok := names[id]; ok {
			out = append(out, n)
		} else if slices.ContainsFunc(p.Edges, func(e mapEdge) bool { return e.ID == id }) {
			lines++
		} else {
			t.Errorf("%q lights %s, which the map lacks", name, id)
		}
	}
	slices.Sort(out)
	return out, lines
}

// TestMapRoutes: the map puts each model under its make, draws each
// mapping once, and a card lights the routes through it alone: an entry
// reached through a make lights the model that led to the make, and one
// reached through a model lights that model's make but not the make's
// other entries.
func TestMapRoutes(t *testing.T) {
	s, d := server(t, "classics"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	p := mapOf(t, l, "from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500&to=yard")
	if got, want := cards(p), []string{
		"0 Dodge Ram 1500",
		"1 Dodge", "1 > Ram", "1 Ram", "1 > 1500",
		"2 Dodge Other", "2 Dodge Ram 1500", "2 Dodge Ram 1500 4x4", "2 Ram Other",
	}; !slices.Equal(got, want) {
		t.Fatalf("cards\n%q\nwant\n%q", got, want)
	}
	// From to its two models, each model to its make, and each target to
	// what its mappings name: both Ram 1500s to both models, each Other to
	// its make.
	if len(p.Edges) != 2+2+4+2 {
		t.Errorf("%d lines; want 10: %+v", len(p.Edges), p.Edges)
	}
	for _, c := range []struct {
		col   int
		name  string
		want  []string
		lines int
	}{
		{2, "Ram Other", []string{"1500", "Dodge Ram 1500", "Ram"}, 3},
		{2, "Dodge Ram 1500 4x4", []string{"1500", "Dodge", "Dodge Ram 1500", "Ram", "Ram"}, 6},
		{1, "1500", []string{"Dodge Ram 1500", "Dodge Ram 1500", "Dodge Ram 1500 4x4", "Ram", "Ram Other"}, 5},
	} {
		got, lines := lights(t, p, c.col, c.name)
		if !slices.Equal(got, c.want) || lines != c.lines {
			t.Errorf("%s lights %q and %d lines; want %q and %d", c.name, got, lines, c.want, c.lines)
		}
	}
	if p.Heading != "Dodge Ram" {
		t.Errorf("heading %q; want Dodge Ram, the first model reached under its make", p.Heading)
	}
}

// rels are p's cards' relations, keyed as cards names them: "1 > Ram"
// to what the card's text says its lines draw.
func rels(p mapPage) map[string]string {
	out := map[string]string{}
	for c, col := range p.Columns {
		for _, it := range col.Items {
			if it.ID == "" {
				continue
			}
			k := string(rune('0'+c)) + " "
			if it.Under {
				k += "> "
			}
			out[k+it.Name] = it.Rel
		}
	}
	return out
}

// TestMapRelations: every card is a link, the From card to the answer's
// own page, and each says in words what its lines draw, its make and
// what it was reached through and how, a model named with its make, so
// the map reads without them. The key names the kinds of line the map
// draws.
func TestMapRelations(t *testing.T) {
	s, d := server(t, "classics"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	const q = "from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500&to=yard"
	p := mapOf(t, l, q)
	for _, col := range p.Columns {
		for _, it := range col.Items {
			if it.ID != "" && it.Href == "" {
				t.Errorf("%s is no link", it.Name)
			}
		}
	}
	if got := p.Columns[0].Items[0].Href; got != homePath+"?"+q {
		t.Errorf("the From card links to %s; want the answer's own page", got)
	}
	if got, want := rels(p), map[string]string{
		"0 Dodge Ram 1500":     "what was translated, a link to this answer",
		"1 Dodge":              "",
		"1 > Ram":              "under Dodge; reached through Dodge Ram 1500",
		"1 Ram":                "",
		"1 > 1500":             "under Ram; reached through Dodge Ram 1500",
		"2 Dodge Other":        "reached through Dodge",
		"2 Dodge Ram 1500":     "reached through Dodge Ram, and through Ram 1500",
		"2 Dodge Ram 1500 4x4": "reached through Dodge Ram, and through Ram 1500",
		"2 Ram Other":          "reached through Ram",
	}; !maps.Equal(got, want) {
		t.Errorf("relations\n%q\nwant\n%q", got, want)
	}
	if p.Derived || p.Decoded || !p.Under {
		t.Errorf("key: derived %v, decoded %v, under %v; want under alone", p.Derived, p.Decoded, p.Under)
	}

	// A VIN's decode, drawn from the VIN to the model it decodes to, and
	// an entry matched by rule.
	mk, model := contract.SpineRef{Make: 474}, contract.SpineRef{Model: 1861}
	year := contract.Null[int32]{V: 2003, Valid: true}
	tr := contract.Translation{
		From:    contract.TranslationFrom{VIN: "1HGCM82633A004352", Status: contract.FromMapped},
		Vehicle: &contract.Vehicle{VIN: "1HGCM82633A004352", Make: contract.Null[int32]{V: 474, Valid: true}, Model: contract.Null[int32]{V: 1861, Valid: true}, ModelYear: year},
		Spine:   []contract.SpineEntry{{SpineRef: mk, Name: "Honda"}, {SpineRef: model, Name: "Accord", Under: &mk}},
		To: []contract.Translated{{Vocabulary: "yard", Entries: []contract.TranslatedEntry{
			{Entry: map[string]string{"model": "ACCORD"}, Via: []contract.Via{{SpineRef: model, Origin: "derived"}}},
		}}},
	}
	p = newMapPage(tr, nil, contract.Elements{}, "", "/?from.vin=1HGCM82633A004352")
	if !p.Decoded || !p.Under || !p.Derived {
		t.Errorf("a VIN's key: decoded %v, under %v, derived %v; want all three", p.Decoded, p.Under, p.Derived)
	}
	if got, want := rels(p), map[string]string{
		"0 1HGCM82633A004352": "what was translated, a link to this answer",
		"1 Honda":             "",
		"1 > Accord":          "under Honda; what the VIN decodes to",
		"2 ACCORD":            "reached through Honda Accord, matched by rule",
	}; !maps.Equal(got, want) {
		t.Errorf("a VIN's relations\n%q\nwant\n%q", got, want)
	}

	// An entry translated with a VIN for context: its lines are mappings,
	// no decode.
	tr.From = contract.TranslationFrom{Vocabulary: "yard", Entry: map[string]string{"model": "ACCORD"}, Status: contract.FromMapped}
	tr.Spine[1].Via = []contract.Via{{SpineRef: model}}
	p = newMapPage(tr, nil, contract.Elements{}, "", "/?from.vocabulary=yard&from.entry.model=ACCORD&vin=1HGCM82633A004352")
	if p.Decoded {
		t.Errorf("an entry with a VIN for context: a line drawn as the VIN's decode")
	}
	if got := rels(p)["1 > Accord"]; got != "under Honda; reached through ACCORD" {
		t.Errorf("an entry with a VIN for context: Accord %q", got)
	}
}
