package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
)

// linesOff are the map's lines that miss their cards as the browser lays
// the page out, "map-e3 end" each: a mapping's line leaves the middle of
// a card's right edge and reaches the middle of a card's left edge, and a
// model's tie leaves its make's foot. The server draws the lines from the
// cards' sizes, which the stylesheet sets (pkg/ui, map.go and ui.css).
const linesOff = `(() => {
	const svg = document.querySelector(".map-edges"), m = svg.getScreenCTM();
	const at = (p, l) => p.getPointAtLength(l).matrixTransform(m);
	const cards = [...document.querySelectorAll(".map .node")].map(n => n.getBoundingClientRect());
	const near = (a, b) => Math.abs(a - b) < 1;
	const out = [];
	for (const p of svg.querySelectorAll("path")) {
		const a = at(p, 0), b = at(p, p.getTotalLength());
		const starts = cards.some(r => p.classList.contains("under")
			? near(a.y, r.bottom) && a.x > r.left && a.x < r.right
			: near(a.x, r.right) && near(a.y, r.top + r.height / 2));
		const ends = cards.some(r => near(b.x, r.left) && near(b.y, r.top + r.height / 2));
		if (!starts || !ends) out.push(p.id + (starts ? "" : " start") + (ends ? "" : " end"));
	}
	return out;
})()`

// stacked reports whether the map's columns stand one above the other,
// in order, its lines and their key hidden, and each card's relation
// shown as a line of its own; and that nothing is wider than the window.
const stacked = `(() => {
	const cols = [...document.querySelectorAll(".map-col")].map(c => c.getBoundingClientRect());
	const inOrder = cols.every((r, i) => i === 0 || r.top >= cols[i - 1].bottom);
	const hidden = sel => [...document.querySelectorAll(sel)].every(e => getComputedStyle(e).display === "none");
	const shown = [...document.querySelectorAll(".map .node .rel")].every(e => e.getBoundingClientRect().height > 10);
	return inOrder && hidden(".map-edges") && hidden(".map-key .key-lines") && shown &&
		document.documentElement.scrollWidth <= innerWidth;
})()`

// accName is the accessible name the browser gives the element sel
// matches.
func accName(tb *tab, sel string) string {
	tb.t.Helper()
	var name string
	tb.call(func(ctx context.Context) error {
		nodes, err := chromedp.Run(ctx, chromedp.Nodes(chromedp.CSS(sel)))
		if err != nil {
			return err
		}
		relatives := false
		r, err := chromedp.Call(ctx, accessibility.GetPartialAXTree, accessibility.GetPartialAXTreeParams{BackendNodeID: nodes[0].BackendNodeID, FetchRelatives: &relatives})
		if err != nil || len(r.Nodes) == 0 || r.Nodes[0].Name == nil {
			return err
		}
		return json.Unmarshal(r.Nodes[0].Name.Value, &name)
	})
	return name
}

// point moves the mouse to the middle of the element sel matches,
// scrolled into view.
func (tb *tab) point(sel string) {
	tb.t.Helper()
	xy := eval[[2]float64](tb, `(e => { e.scrollIntoView({block: "center"}); const r = e.getBoundingClientRect(); return [r.x + r.width / 2, r.y + r.height / 2]; })(document.querySelector(`+jsString(sel)+`))`)
	tb.do(chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, xy[0], xy[1]))
}

// translationMap: a lookup's answer is its translation's map, each line
// from its card to its card. Every card is a link, the From card to the
// answer itself, and its name says what its lines draw. A card focused
// lights the route through it, and one under the pointer its own until
// the pointer leaves, the focused card's then; a long name shows whole
// under the pointer. A narrow window stacks the columns, hiding the lines
// and showing each card's relation. A card's link is the page from it,
// which htmx follows, and back and forward ask the server, so nothing
// left on the page by a script, here an empty style attribute, comes back
// for CSP to refuse. Without JavaScript the map is drawn the same. axe
// audits each state.
func translationMap(t *testing.T, path, base string) {
	const translation = "/?from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500&to=yard"
	drawn := func(tb *tab, step string, lines int) {
		t.Helper()
		if got := tb.count(".map-edges path"); got != lines {
			t.Errorf("%s: %d lines; want %d", step, got, lines)
		}
		if off := eval[[]string](tb, linesOff); len(off) > 0 {
			t.Errorf("%s: lines miss their cards: %q", step, off)
		}
	}
	lit := func(tb *tab, step string, cards, lines int) {
		t.Helper()
		if c, l := tb.count(".map .node.lit"), tb.count(".map-edges path.lit"); c != cards || l != lines {
			t.Errorf("%s lights %d cards and %d lines; want %d and %d", step, c, l, cards, lines)
		}
	}

	tb := newTab(t, path, base, &requestLog{}, true)
	tb.do(chromedp.EmulateViewport(1440, 900))
	tb.open(translation)
	tb.wait(".map")
	drawn(tb, "the map", 10)
	if links, cards := tb.count(".map a.node[href]"), tb.count(".map .node"); links != cards || cards != 9 {
		t.Errorf("%d of %d cards are links; want all 9", links, cards)
	}
	if href := attributes(tb, ".map-col:nth-child(1) a.node"); len(href) != 1 || href[0]["href"] != translation {
		t.Errorf("the From card: %v; want a link to %s", href, translation)
	}
	// Its relation is part of its name; the browser spaces out each part.
	const otherName = "Dodge Other , reached through Dodge ; 1 mapping"
	if got := accName(tb, ".map-col:nth-child(3) a.node"); got != otherName {
		t.Errorf("Dodge Other's name %q; want %q", got, otherName)
	}
	tb.audit("the map")

	// Dodge Other, reached through the make, which the model led to. A
	// click first: a page never clicked has no focus to move.
	tb.do(chromedp.Click(chromedp.CSS("h1")), chromedp.Focus(chromedp.CSS(".map-col:nth-child(3) a.node")))
	lit(tb, "Dodge Other focused", 4, 3)
	// The pointer over the make Ram lights its route, which Dodge Other is
	// not on; leaving it gives the light back to Dodge Other.
	const other, ram = ".map-col:nth-child(3) a.node:nth-of-type(1)", ".map-col:nth-child(2) a.node:nth-of-type(3)"
	tb.point(ram)
	if tb.count(ram+".lit") != 1 || tb.count(other+".lit") != 0 {
		t.Errorf("the make Ram under the pointer: its route not lit, or Dodge Other's")
	}
	tb.point("h1")
	lit(tb, "the pointer gone, Dodge Other focused", 4, 3)
	tb.audit("the map, a route lit")

	// A name too long for two lines shows whole under the pointer, over
	// the cards below, which stay put.
	tb.do(chromedp.Click(chromedp.CSS("h1")))
	const long = `document.querySelector(".map-col:nth-child(3) a.node:nth-of-type(2) .name")`
	const below = `document.querySelector(".map-col:nth-child(3) a.node:nth-of-type(3)").offsetTop`
	eval[bool](tb, long+`.textContent = "Dodge Ram 1500 Quad Cab Long Bed Heavy Duty Cummins Turbo Diesel Laramie Longhorn Limited Edition", true`)
	if h := eval[float64](tb, long+`.getBoundingClientRect().height`); h > 41 {
		t.Errorf("a long name: %v px; want two lines at most", h)
	}
	before := eval[float64](tb, below)
	tb.point(".map-col:nth-child(3) a.node:nth-of-type(2)")
	if whole := eval[bool](tb, `(n => n.scrollHeight <= n.clientHeight && n.getBoundingClientRect().height > 41)(`+long+`)`); !whole {
		t.Errorf("a long name under the pointer is not whole")
	}
	if after := eval[float64](tb, below); after != before {
		t.Errorf("a long name under the pointer moved the card below from %v to %v", before, after)
	}
	tb.point("h1")

	tb.do(chromedp.EmulateViewport(390, 844))
	if !eval[bool](tb, stacked) {
		t.Errorf("the map at 390: not stacked")
	}
	tb.audit("the map, narrow")
	tb.do(chromedp.EmulateViewport(1440, 900))

	// A spine card's link is the page from it, to the same vocabularies.
	// A script's empty style attribute left on the page first.
	eval[bool](tb, `window.kept = true`)
	eval[bool](tb, `(n => { n.style.color = "red"; n.style.color = ""; return n.hasAttribute("style"); })(document.querySelector(".map .node"))`)
	tb.do(chromedp.Click(chromedp.CSS(".map-col:nth-child(2) a.node.under")))
	const from = "/?from.model=1938&to=yard"
	tb.until("the model's card", `location.pathname + location.search === "`+from+`" && document.querySelector(".map .node .meta").textContent === "vPIC model 1938"`)
	if !eval[bool](tb, `window.kept === true`) {
		t.Errorf("the model's card loaded as a page; want htmx's swap")
	}
	if tabs := attributes(tb, `#tab-id[aria-current="page"]`); len(tabs) != 1 {
		t.Errorf("the model's card: not on the vPIC id tab")
	}
	// The model to its make, itself to the model, and the model's two
	// entries and the make's one.
	drawn(tb, "the map from a model", 5)

	// Back is the translation again, forward the model's, each from the
	// server: CSP refuses nothing.
	eval[bool](tb, `history.back(), true`)
	tb.until("back", `location.search === "`+translation[1:]+`" && document.querySelectorAll(".map-edges path").length === 10`)
	drawn(tb, "back", 10)
	eval[bool](tb, `history.forward(), true`)
	tb.until("forward", `location.pathname + location.search === "`+from+`" && document.querySelectorAll(".map-edges path").length === 5`)
	if !eval[bool](tb, `window.kept === true`) {
		t.Errorf("back and forward reloaded the page")
	}
	if refused := eval[[]string](tb, `refused`); len(refused) > 0 {
		t.Errorf("back and forward: CSP refused %q", refused)
	}

	off := newTab(t, path, base, &requestLog{}, false)
	off.do(chromedp.EmulateViewport(1440, 900))
	off.open(translation)
	off.wait(".map")
	if got := off.count(".map-edges path"); got != 10 {
		t.Errorf("JS off, the map: %d lines; want 10", got)
	}
	off.audit("the map, JS off")
	off.do(chromedp.EmulateViewport(390, 844))
	off.scripts(true)
	if !eval[bool](off, stacked) {
		t.Errorf("JS off, the map at 390: not stacked")
	}
	off.scripts(false)
	off.audit("the map, JS off, narrow")
}
