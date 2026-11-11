// The translation map's highlighting (pkg/ui, map.go): served from
// /ui/r/<release>/map.js under script-src 'self'. The card under the
// pointer, or holding focus, lights the route through it: the cards and
// lines its data-lit names, which the server worked out. The rest dim.
// Without this script the map is whole and still; its cards are links
// either way.
//
// The listeners are the document's, so a map htmx swaps in, or puts back
// on back or forward, needs nothing more.
"use strict";

// light lights the route through node, or nothing when node is null.
function light(map, node) {
  for (const el of map.querySelectorAll(".lit")) {
    el.classList.remove("lit");
  }
  map.classList.toggle("lighting", !!node);
  if (!node) {
    return;
  }
  for (const id of node.dataset.lit.split(" ")) {
    const el = document.getElementById(id);
    if (el) {
      el.classList.add("lit");
    }
  }
}

// node is the map card e is on, null for none.
function node(e) {
  return e.target instanceof Element ? e.target.closest(".map .node[data-lit]") : null;
}

for (const [on, off] of [["pointerover", "pointerout"], ["focusin", "focusout"]]) {
  document.addEventListener(on, (e) => {
    const n = node(e);
    if (n) {
      light(n.closest(".map"), n);
    }
  });
  document.addEventListener(off, (e) => {
    const n = node(e);
    if (n && !n.contains(e.relatedTarget)) {
      light(n.closest(".map"), null);
    }
  });
}
