// The decode form's copy buttons (cmd/nitrovin/form.go): the page's one
// script, served from /copy.js under script-src 'self'. Every element with
// a data-copy attribute gets a Copy button after it, named by the
// attribute ("Copy the decode's JSON"); a click copies the element's text
// and announces it. Without this script there are no buttons and the text
// stays on the page to select.
"use strict";

const status = document.createElement("p");
status.className = "visually-hidden";
status.setAttribute("role", "status");
document.body.append(status);

for (const el of document.querySelectorAll("[data-copy]")) {
  const what = el.dataset.copy;
  const button = document.createElement("button");
  button.type = "button";
  button.className = "copy";
  button.textContent = "Copy";
  button.setAttribute("aria-label", "Copy " + what);
  let reset;
  button.addEventListener("click", async () => {
    let copied = true;
    try {
      await copy(el);
    } catch {
      copied = false;
    }
    button.textContent = copied ? "Copied" : "Select to copy";
    status.textContent = copied ? "Copied " + what : "Could not copy " + what + "; select the text instead";
    clearTimeout(reset);
    reset = setTimeout(() => { button.textContent = "Copy"; }, 2000);
  });
  el.after(button);
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
