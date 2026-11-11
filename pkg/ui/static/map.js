// The translation map's highlighting (pkg/ui, map.go): served from
// /ui/r/<release>/map.js under script-src 'self'. The card under the
// pointer lights the route through it, else the card holding focus: the
// cards and lines its data-lit names, which the server worked out. The
// rest dim. The two are kept apart, so the pointer leaving a card gives
// the light back to the focused one. Without this script the map is whole
// and still; its cards are links either way.
//
// The listeners are the document's, so a map htmx swaps in, or puts back
// on back or forward, needs nothing more.
"use strict";

// pointed and focused are the cards under the pointer and holding focus,
// null for none.
let pointed = null;
let focused = null;

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

// relight lights the map card n is on for the card under the pointer,
// else the focused one, each only while it is on that map.
function relight(n) {
  const map = n.closest(".map");
  const on = [pointed, focused].find((c) => c && map.contains(c));
  light(map, on || null);
}

// node is the map card e is on, null for none.
function node(e) {
  return e.target instanceof Element ? e.target.closest(".map .node[data-lit]") : null;
}

document.addEventListener("pointerover", (e) => {
  const n = node(e);
  if (n && n !== pointed) {
    pointed = n;
    relight(n);
  }
});
document.addEventListener("pointerout", (e) => {
  const n = node(e);
  if (n && !n.contains(e.relatedTarget)) {
    pointed = null;
    relight(n);
  }
});
document.addEventListener("focusin", (e) => {
  const n = node(e);
  if (n) {
    focused = n;
    relight(n);
  }
});
document.addEventListener("focusout", (e) => {
  const n = node(e);
  if (n && !n.contains(e.relatedTarget)) {
    focused = null;
    relight(n);
  }
});
