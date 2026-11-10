// The pages' copy buttons (pkg/ui): served from /ui/r/<release>/copy.js
// under script-src 'self'. Every element with a data-copy attribute gets a
// Copy button after it, named by the attribute ("Copy the decode's JSON");
// a click copies the element's text and announces it. Without this script
// there are no buttons and the text stays on the page to select.
//
// htmx brings in new content (a decode swapped into the page, a page put
// back on back or forward), and each gets its buttons. A page htmx put
// back from its history snapshot holds the buttons it had, without their
// listeners: those are replaced.
"use strict";

const made = new WeakSet();

// buttons adds a Copy button after each element under root that wants one.
function buttons(root) {
  const els = [...root.querySelectorAll("[data-copy]")];
  if (root.matches && root.matches("[data-copy]")) {
    els.unshift(root);
  }
  for (const el of els) {
    const next = el.nextElementSibling;
    if (next && next.matches("button.copy")) {
      if (made.has(next)) {
        continue;
      }
      next.remove();
    }
    el.after(button(el));
  }
}

function button(el) {
  const what = el.dataset.copy;
  const b = document.createElement("button");
  b.type = "button";
  b.className = "copy";
  b.textContent = "Copy";
  b.setAttribute("aria-label", "Copy " + what);
  let reset;
  b.addEventListener("click", async () => {
    let copied = true;
    try {
      await copy(el);
    } catch {
      copied = false;
    }
    b.textContent = copied ? "Copied" : "Select to copy";
    announce(copied ? "Copied " + what : "Could not copy " + what + "; select the text instead");
    clearTimeout(reset);
    reset = setTimeout(() => { b.textContent = "Copy"; }, 2000);
  });
  made.add(b);
  return b;
}

// announce says text through the page's status line, made on first use.
function announce(text) {
  let status = document.getElementById("copy-status");
  if (!status) {
    status = document.createElement("p");
    status.id = "copy-status";
    status.className = "visually-hidden";
    status.setAttribute("role", "status");
    document.body.append(status);
  }
  status.textContent = text;
}

// copy puts el's text on the clipboard: through the Clipboard API in a
// secure context (https, localhost), else by selecting it, which plain http
// on another host still allows.
async function copy(el) {
  if (window.isSecureContext && navigator.clipboard) {
    return navigator.clipboard.writeText(el.textContent);
  }
  const range = document.createRange();
  range.selectNodeContents(el);
  const selection = getSelection();
  selection.removeAllRanges();
  selection.addRange(range);
  if (!document.execCommand("copy")) {
    throw new Error("copy refused");
  }
}

buttons(document);
document.addEventListener("htmx:load", (e) => buttons(e.target));
document.addEventListener("htmx:historyRestore", () => buttons(document));
