package ui

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// The translation map draws a translation, below the page's controls
// (page.go): three columns, what it is from, the spine entries it crosses
// (each model under its make) and the entries reached in each target
// vocabulary, each a card, the mappings between them lines. Every card is
// a link: the From card to this answer's own page, a spine card to the
// page from that spine entry, a target's to the page translating it back,
// so the map is walked card to card. What the lines draw is in each
// card's text too (Rel): its make, what it was reached through and how,
// read as part of its name, and its own line when a narrow window stacks
// the columns and hides the lines. Each mapping's years, origin and note
// and what could not be checked is its title; the translation's JSON, a
// dialog below the map, has all of it.
//
// Every card is the same height, so the server knows where each sits and
// draws the lines in an SVG behind the cards, stretched across the
// columns' width (preserveAspectRatio none, strokes that do not scale):
// no script lays it out, and the page needs none, only CSP's own
// stylesheet. A name wraps to two lines; a longer one shows whole while
// its card is under the pointer or focused. map.js lights the route
// through the card under the pointer or focused, the cards and lines its
// data-lit names, which the server works out too.

// The map's geometry, which ui.css's map rules repeat: down, in CSS
// pixels, a column's heading, a target vocabulary's heading, a card, and
// the gap between any two; across, in percent of the map's width, a
// column, the gap between two, and a model card's indent under its make
// (10% of a column).
const (
	mapHeadH  = 32
	mapGroupH = 26
	mapCardH  = 72
	mapGapH   = 10
	mapColW   = 26.0
	mapColGap = 11.0
	mapIndent = mapColW / 10
)

// mapPage is a translation on the map. Heading names what was
// translated, the vehicle when the spine names one: "2015 Ram 1500".
// Height is the columns', in CSS pixels. Derived, Under and Decoded say
// which kinds of line it draws besides mappings, for its key.
type mapPage struct {
	Heading                 string
	Columns                 []mapColumn
	Edges                   []mapEdge
	Height                  int
	Derived, Under, Decoded bool
}

type mapColumn struct {
	Heading string
	Items   []mapItem
}

// mapItem is one row of a column: a target vocabulary's heading (Group),
// a line saying nothing is there (Empty), or a card. A card has its name,
// how it relates to the cards its lines reach (Rel), a line under it
// (Meta), how it was reached in detail (Details), a link (Href), and the
// ids of the cards and lines on the routes through it (Lit). Under
// indents a model under its make; Unchecked marks an entry whose
// qualifiers or model year could not all be checked.
type mapItem struct {
	Group, Empty                            string
	ID, Name, Rel, Meta, Details, Href, Lit string
	Mono, Under, Unchecked                  bool
}

func (i mapItem) height() int {
	if i.Group != "" {
		return mapGroupH
	}
	return mapCardH
}

// mapEdge is one line: a mapping (Derived when matched by rule), a
// model's tie to its make (Under), or a VIN's to the model it decodes to
// (Decoded). D is its path, x in percent of the map's width and y in CSS
// pixels.
type mapEdge struct {
	ID, D                   string
	Derived, Under, Decoded bool
}

// mapGraph is the map as it is built: its columns, and its cards and
// lines by number, each card's column and row.
type mapGraph struct {
	cols  []mapColumn
	cards []struct{ col, row int }
	links []mapLink
}

// mapLink is a line from card a to card b, a mapping, a model's tie to
// its make (under) or a VIN's decode (decoded).
type mapLink struct {
	a, b                    int
	derived, under, decoded bool
}

// add puts it at the foot of column col: its number when it is a card,
// else -1.
func (g *mapGraph) add(col int, it mapItem) int {
	g.cols[col].Items = append(g.cols[col].Items, it)
	if it.Group != "" || it.Empty != "" {
		return -1
	}
	n := len(g.cards)
	row := len(g.cols[col].Items) - 1
	g.cards = append(g.cards, struct{ col, row int }{col, row})
	g.cols[col].Items[row].ID = "map-n" + strconv.Itoa(n)
	return n
}

// link draws a line from card a to card b, once.
func (g *mapGraph) link(a, b int, derived, under bool) {
	if a < 0 || b < 0 || slices.ContainsFunc(g.links, func(l mapLink) bool { return l.a == a && l.b == b }) {
		return
	}
	g.links = append(g.links, mapLink{a: a, b: b, derived: derived, under: under})
}

// newMapPage is the map of tr over the catalog cat, asked for in model
// year year ("" for none), which a decoded VIN's own replaces, on the page
// at self, the From card's link.
func newMapPage(tr contract.Translation, cat []contract.CatalogEntry, elements contract.Elements, year, self string) mapPage {
	var p mapPage
	g := &mapGraph{cols: []mapColumn{{Heading: "From"}, {Heading: "Spine"}, {Heading: "To"}}}
	if x := tr.Vehicle; x != nil && x.ModelYear.Valid {
		year = strconv.Itoa(int(x.ModelYear.V))
	}
	names := map[contract.SpineRef]string{}
	for _, s := range tr.Spine {
		names[s.SpineRef] = s.Name
	}
	named := func(r contract.SpineRef) string {
		if n := names[r]; n != "" {
			return refText(r) + " (" + n + ")"
		}
		return refText(r)
	}

	// From.
	f := tr.From
	from := mapItem{Details: f.Note, Href: self, Rel: "what was translated, a link to this answer"}
	switch translatedFrom(f) {
	case fromEntry:
		from.Name, from.Meta = entryText(cat, f.Vocabulary, f.Entry), f.Vocabulary
	case fromVIN:
		from.Name, from.Mono, from.Meta = f.VIN, true, "VIN"
		if year != "" {
			from.Meta = "VIN, model year " + year
		}
	default:
		from.Name, from.Meta = names[f.SpineRef], refText(f.SpineRef)
		if from.Name == "" {
			from.Name = refText(f.SpineRef)
		}
	}
	switch f.Status {
	case contract.FromUnmapped:
		from.Meta += " · listed, but not mapped yet"
	case contract.FromUnknown:
		from.Meta += " · not found"
	}
	fromCard := g.add(0, from)

	// The heading names the vehicle: its model year, then the first model
	// reached under its make, "2015 Ram 1500"; else what it is from.
	for _, s := range tr.Spine {
		if p.Heading != "" || s.Name == "" {
			continue
		}
		switch {
		case s.Model != 0 || s.Ext != "":
			p.Heading = s.Name
			if s.Under != nil && names[*s.Under] != "" && !strings.HasPrefix(s.Name, names[*s.Under]) {
				p.Heading = names[*s.Under] + " " + s.Name
			}
		case s.Make != 0:
			p.Heading = s.Name
		}
	}
	if p.Heading == "" {
		switch translatedFrom(f) {
		case fromEntry:
			p.Heading = from.Name
		case fromVIN:
			p.Heading = "VIN " + f.VIN
		default:
			p.Heading = named(f.SpineRef)
		}
	}
	if year != "" && f.Spine == "" {
		p.Heading = year + " " + p.Heading
	}

	// The spine, each make followed by its models, which it groups as the
	// translation reached them.
	type group struct {
		head    *contract.SpineEntry
		members []contract.SpineEntry
	}
	var order []contract.SpineRef
	groups := map[contract.SpineRef]*group{}
	for _, s := range tr.Spine {
		key := s.SpineRef
		if s.Under != nil {
			key = *s.Under
		}
		gr := groups[key]
		if gr == nil {
			gr = &group{}
			groups[key] = gr
			order = append(order, key)
		}
		if s.Under == nil {
			gr.head = &s
		} else {
			gr.members = append(gr.members, s)
		}
	}
	// back is the targets a card's map goes to: the from's vocabulary,
	// then the translation's.
	var back []string
	if f.Vocabulary != "" {
		back = append(back, f.Vocabulary)
	}
	for _, t := range tr.To {
		if !slices.Contains(back, t.Vocabulary) {
			back = append(back, t.Vocabulary)
		}
	}
	spineCard := map[contract.SpineRef]int{}
	spineItem := func(s contract.SpineEntry, under bool) int {
		it := mapItem{Name: s.Name, Meta: refText(s.SpineRef), Under: under, Unchecked: !s.Unconfirmed.IsZero(), Href: spineHref(s.SpineRef, year, back)}
		if it.Name == "" {
			it.Name = it.Meta
		}
		if s.Drift {
			it.Meta += " · not in data month " + tr.DataMonth
		}
		var details []string
		for _, v := range s.Via {
			details = append(details, viaText(v, named))
		}
		if u := unconfirmedText(s.Unconfirmed, elements); u != "" {
			details = append(details, u)
		}
		it.Details = strings.Join(details, "\n")
		n := g.add(1, it)
		spineCard[s.SpineRef] = n
		return n
	}
	for _, key := range order {
		gr := groups[key]
		head := -1
		if gr.head != nil {
			head = spineItem(*gr.head, false)
		}
		for _, m := range gr.members {
			g.link(spineItem(m, head >= 0), head, false, true)
		}
	}
	if len(tr.Spine) == 0 {
		g.add(1, mapItem{Empty: "Nothing on the spine."})
	}

	// From to the spine: a vocabulary entry to what its mappings name, a
	// VIN to the model it decodes to (its make when no model), a spine
	// entry to itself.
	switch {
	case f.Vocabulary != "":
		for _, s := range tr.Spine {
			for _, v := range s.Via {
				if v.SpineRef == s.SpineRef {
					g.link(fromCard, spineCard[s.SpineRef], v.Origin == "derived", false)
				}
			}
		}
	case tr.Vehicle != nil:
		ref := contract.SpineRef{Model: tr.Vehicle.Model.V}
		if !tr.Vehicle.Model.Valid {
			ref = contract.SpineRef{Make: tr.Vehicle.Make.V}
		}
		if n, ok := spineCard[ref]; ok {
			g.link(fromCard, n, false, false)
		}
	default:
		if n, ok := spineCard[f.SpineRef]; ok {
			g.link(fromCard, n, false, false)
		}
	}
	// A VIN's line to the model it decodes to is no mapping.
	if translatedFrom(f) == fromVIN {
		for i := range g.links {
			g.links[i].decoded = g.links[i].a == fromCard
		}
	}

	// The targets, by vocabulary, each entry from the spine entries its
	// mappings name.
	for _, t := range tr.To {
		g.add(2, mapItem{Group: t.Vocabulary})
		if len(t.Entries) == 0 {
			g.add(2, mapItem{Empty: "No name here yet."})
		}
		for _, e := range t.Entries {
			// The entry's link translates it back: to the from's
			// vocabulary and the other targets, in the same model year.
			q := url.Values{"from.vocabulary": {t.Vocabulary}}
			for k, s := range e.Entry {
				q.Set("from.entry."+k, s)
			}
			if year != "" {
				q.Set("model_year", year)
			}
			for _, n := range back {
				if n != t.Vocabulary {
					q.Add("to", n)
				}
			}
			var details []string
			for _, via := range e.Via {
				details = append(details, viaText(via, named))
			}
			if e.Note != "" {
				details = append(details, "Note: "+e.Note)
			}
			if u := unconfirmedText(e.Unconfirmed, elements); u != "" {
				details = append(details, u)
			}
			it := mapItem{Name: entryText(cat, t.Vocabulary, e.Entry), Meta: viaShort(e.Via), Href: homePath + "?" + q.Encode(), Details: strings.Join(details, "\n"), Unchecked: !e.Unconfirmed.IsZero()}
			n := g.add(2, it)
			for _, v := range e.Via {
				if a, ok := spineCard[v.SpineRef]; ok {
					g.link(a, n, v.Origin == "derived", false)
				}
			}
		}
	}
	if len(tr.To) == 0 {
		g.add(2, mapItem{Empty: "No vocabulary to translate to."})
	}

	g.relate()
	p.Columns, p.Edges, p.Height = g.layout()
	for _, e := range p.Edges {
		p.Derived, p.Under, p.Decoded = p.Derived || e.Derived, p.Under || e.Under, p.Decoded || e.Decoded
	}
	return p
}

// relate words what each card's lines draw, after what it already says:
// a model's make, then what it was reached through and how, a model
// named with its make: "under GMC; reached through Envoy XL" on a model,
// "reached through GMC Envoy, matched by rule" on a target's entry.
func (g *mapGraph) relate() {
	item := func(n int) *mapItem {
		at := g.cards[n]
		return &g.cols[at.col].Items[at.row]
	}
	// named is card n's name with its make's before it, when it is a
	// model whose name does not already start with it.
	named := func(n int) string {
		name := item(n).Name
		for _, l := range g.links {
			if mk := item(l.b).Name; l.under && l.a == n && !strings.HasPrefix(name, mk) {
				return mk + " " + name
			}
		}
		return name
	}
	for n := range g.cards {
		var parts, through []string
		if r := item(n).Rel; r != "" {
			parts = append(parts, r)
		}
		decoded := false
		for _, l := range g.links {
			switch {
			case l.under && l.a == n:
				parts = append(parts, "under "+item(l.b).Name)
			case l.under || l.b != n:
			case l.decoded:
				decoded = true
			case l.derived:
				through = append(through, "through "+named(l.a)+", matched by rule")
			default:
				through = append(through, "through "+named(l.a))
			}
		}
		if decoded {
			parts = append(parts, "what the VIN decodes to")
		}
		if len(through) > 0 {
			parts = append(parts, "reached "+strings.Join(through, ", and "))
		}
		item(n).Rel = strings.Join(parts, "; ")
	}
}

// viaShort is an entry's mappings in its card's line: one's years and
// origin, "1994–2012 · matched by rule", or how many there are.
func viaShort(via []contract.Via) string {
	if len(via) != 1 {
		return strconv.Itoa(len(via)) + " mappings"
	}
	var parts []string
	if y := yearsText(via[0].First, via[0].Last); y != "" {
		parts = append(parts, y)
	}
	if o := originText(via[0].Origin, via[0].Note); o != "" {
		parts = append(parts, o)
	}
	if len(parts) == 0 {
		return "1 mapping"
	}
	return strings.Join(parts, " · ")
}

// spineHref is the page from spine entry r in year to the targets to.
func spineHref(r contract.SpineRef, year string, to []string) string {
	q := url.Values{}
	switch {
	case r.Make != 0:
		q.Set("from.make", strconv.Itoa(int(r.Make)))
	case r.Model != 0:
		q.Set("from.model", strconv.Itoa(int(r.Model)))
	case r.Ext != "":
		q.Set("from.ext", r.Ext)
	default:
		q.Set("from.spine", r.Spine)
		q.Set("from.id", r.ID)
	}
	if year != "" {
		q.Set("model_year", year)
	}
	for _, n := range to {
		q.Add("to", n)
	}
	return homePath + "?" + q.Encode()
}

// layout places the cards and draws the lines: each card's center, the
// paths between them, and on each card the routes through it, what is
// upstream of it and what downstream (a model's make is downstream of
// it).
func (g *mapGraph) layout() ([]mapColumn, []mapEdge, int) {
	// Down: a column is its heading, then its items, a gap between each.
	tops := make([][]int, len(g.cols))
	height := 0
	for c, col := range g.cols {
		y := mapHeadH
		for _, it := range col.Items {
			y += mapGapH
			tops[c] = append(tops[c], y)
			y += it.height()
		}
		height = max(height, y)
	}
	left := func(c int) float64 { return float64(c) * (mapColW + mapColGap) }
	card := func(n int) (c int, it mapItem, top int) {
		at := g.cards[n]
		return at.col, g.cols[at.col].Items[at.row], tops[at.col][at.row]
	}
	num := func(x float64) string { return strconv.FormatFloat(x, 'f', 2, 64) }

	edges := make([]mapEdge, len(g.links))
	for i, l := range g.links {
		edges[i] = mapEdge{ID: "map-e" + strconv.Itoa(i), Derived: l.derived, Under: l.under, Decoded: l.decoded}
		ca, _, ta := card(l.a)
		cb, b, tb := card(l.b)
		if l.under {
			// From the make's foot down its left, then into the model.
			x := left(cb) + mapIndent/2
			edges[i].D = fmt.Sprintf("M %s %d V %d H %s", num(x), tb+mapCardH, ta+mapCardH/2, num(left(ca)+mapIndent))
			continue
		}
		x1, y1 := left(ca)+mapColW, ta+mapCardH/2
		x2, y2 := left(cb), tb+mapCardH/2
		if b.Under {
			x2 += mapIndent
		}
		dx := (x2 - x1) / 2
		edges[i].D = fmt.Sprintf("M %s %d C %s %d, %s %d, %s %d", num(x1), y1, num(x1+dx), y1, num(x2-dx), y2, num(x2), y2)
	}

	// The routes through each card: the lines and cards reached walking
	// forward from it, then back, where each model reached brings its
	// make. The links make no cycle.
	walk := func(n int, forward bool, lit []string) []string {
		seen := map[int]bool{n: true}
		reach := func(i, to int, further bool, next []int) []int {
			_, it, _ := card(to)
			lit = append(lit, edges[i].ID, it.ID)
			if further && !seen[to] {
				seen[to] = true
				next = append(next, to)
			}
			return next
		}
		for next := []int{n}; len(next) > 0; next = next[1:] {
			for i, l := range g.links {
				switch m := next[0]; {
				case forward && l.a == m:
					next = reach(i, l.b, true, next)
				case !forward && l.b == m:
					next = reach(i, l.a, true, next)
				case !forward && l.under && l.a == m:
					next = reach(i, l.b, false, next)
				}
			}
		}
		return lit
	}
	for n, at := range g.cards {
		lit := walk(n, false, walk(n, true, []string{g.cols[at.col].Items[at.row].ID}))
		seen := map[string]bool{}
		lit = slices.DeleteFunc(lit, func(id string) bool {
			dup := seen[id]
			seen[id] = true
			return dup
		})
		g.cols[at.col].Items[at.row].Lit = strings.Join(lit, " ")
	}
	return g.cols, edges, height
}
