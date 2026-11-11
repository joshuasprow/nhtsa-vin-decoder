package main

import (
	"testing"

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

// translationMap: a lookup's answer is its translation's map, each line
// from its card to its card at a wide window and a narrow one, where the
// map scrolls sideways; a card focused lights the route through it, and
// its link is the page from it, which htmx follows. Without JavaScript
// the map is drawn the same. axe audits each state.
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

	tb := newTab(t, path, base, &requestLog{}, true)
	tb.open(translation)
	tb.wait(".map")
	drawn(tb, "the map", 10)
	tb.audit("the map")

	// Dodge Other, reached through the make, which the model led to. A
	// click first: a page never clicked has no focus to move.
	tb.do(chromedp.Click(chromedp.CSS("h1")), chromedp.Focus(chromedp.CSS(".map-col:nth-child(3) a.node")))
	if cards, lines := tb.count(".map .node.lit"), tb.count(".map-edges path.lit"); cards != 4 || lines != 3 {
		t.Errorf("Dodge Other focused lights %d cards and %d lines; want 4 and 3", cards, lines)
	}
	tb.audit("the map, a route lit")

	tb.do(chromedp.EmulateViewport(400, 800))
	drawn(tb, "the map, narrow", 10)
	tb.audit("the map, narrow")
	tb.do(chromedp.EmulateViewport(1200, 900))

	// A spine card's link is the page from it, to the same vocabularies.
	eval[bool](tb, `window.kept = true`)
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

	off := newTab(t, path, base, &requestLog{}, false)
	off.open(translation)
	off.wait(".map")
	if got := off.count(".map-edges path"); got != 10 {
		t.Errorf("JS off, the map: %d lines; want 10", got)
	}
	off.audit("the map, JS off")
}
