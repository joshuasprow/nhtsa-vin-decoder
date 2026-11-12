package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
)

// TestVocabPage runs the page's Vocabularies dialog in headless Chrome or
// Chromium, each run in a fresh profile, against a server that loaded the
// drift fixture's classics and offers the EPA sample
// (cmd/nitrovin-epa/testdata) as EPA's vocabulary. Without JavaScript the
// dialog lists the server's catalog and says that your own files need
// JavaScript; with it, the worker's panel lists the same rows and the
// browser's beside them. Each finding of the browser prototype's first
// review
// (https://github.com/joshuasprow/nitrovin/issues/147#issuecomment-6779155104)
// has its step, marked "finding N"; TestWorker also covers 1 (hard
// reload), 4 (batch order) and 5 (a stalled load). axe audits every state,
// with JavaScript and without.
func TestVocabPage(t *testing.T) {
	path := chromePath()
	if path == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no Chrome or Chromium to run /vocab in: set NITROVIN_CHROME")
		}
		t.Skip("no Chrome or Chromium: set NITROVIN_CHROME to run /vocab in a browser")
	}
	if testing.Short() {
		t.Skip("builds the worker's module")
	}
	fixtureData(t)
	module := buildWorker(t)
	epaPath, epa := epaSample(t)
	module.EPA = epa
	driftDir := filepath.Join(fixture, "drift")
	wire := &wireLog{h: vocabServer(t, &module, filepath.Join(driftDir, "classics.jsonl"))}
	srv := httptest.NewServer(wire)
	t.Cleanup(srv.Close)

	off := vocabJSOff(t, path, srv.URL)
	vocabJSOn(t, path, srv.URL, wire, off, epaPath, driftDir)
}

// offPages are what the JavaScript-off run saw, for the JavaScript-on
// run to compare: the server's catalog row, and a translation through the
// server's file.
type offPages struct {
	serverRow, translation string
}

// throughServer names the server's file alone, and when it was asked: it
// answers the same with JavaScript on and off.
const throughServer = "/?from.ext=ext:classics/silverado" + pinned

// vocabJSOff: the dialog without JavaScript, in a fresh profile, opens,
// says your own files need JavaScript, offers no file input or EPA load,
// and lists the server's catalog, its drift behind a disclosure;
// translation and listing through the server's file work as pages, and
// /ui/files says what it is for.
func vocabJSOff(t *testing.T, path, base string) offPages {
	tb := newTab(t, path, base, &requestLog{}, false)
	tb.do(chromedp.Navigate(base + "/"))
	tb.wait("#browser")
	tb.do(chromedp.Click(chromedp.CSS("button.open-vocabularies")))
	tb.wait("#vocabularies:popover-open")
	if s := tb.html("#browser-status"); !strings.Contains(s, "Loading your own files needs JavaScript. Your files never leave this browser.") || !strings.Contains(s, "works without it") {
		t.Errorf("JS off: %s", s)
	}
	if n := tb.count("#vocab-files, #load-epa"); n != 0 {
		t.Errorf("JS off: %d file inputs or EPA buttons", n)
	}
	if got := catalogRows(tb); !slices.Equal(got, []string{"vpic vpic", "server classics"}) {
		t.Errorf("JS off: catalog %q", got)
	}
	var p offPages
	p.serverRow = tb.html(`tr[data-where="server"]`)
	if !strings.Contains(p.serverRow, "<summary>Drift: 1 extension</summary>") {
		t.Errorf("JS off: the server's file without its drift: %s", p.serverRow)
	}
	tb.audit("the dialog, JS off")

	tb.do(chromedp.Navigate(base + throughServer))
	tb.wait("#result")
	p.translation = tb.html("#ui-main")
	tb.audit("translation through the server's file, JS off")

	tb.do(chromedp.Navigate(base + "/ui/vocabularies/classics"))
	tb.wait("#ui-main table")
	tb.audit("/ui/vocabularies/classics, JS off")

	// Finding 8: /ui/files without the worker is the one error page.
	tb.do(chromedp.Navigate(base + "/ui/files"))
	tb.wait("#ui-main h1")
	if s := tb.html("#ui-main"); !strings.Contains(s, "<h1>Not Found</h1>") || !strings.Contains(s, "never uploaded") {
		t.Errorf("/ui/files, JS off: %s", s)
	}
	tb.audit("/ui/files, JS off")
	return p
}

// vocabJSOn: the dialog with JavaScript, in a fresh profile.
func vocabJSOn(t *testing.T, path, base string, wire *wireLog, off offPages, epaPath, driftDir string) {
	tb := newTab(t, path, base, &requestLog{}, true)
	tb.do(chromedp.Navigate(base + "/"))
	tb.vocabularies()

	// Finding 6: the catalog does not flip. The worker's panel lists the
	// server's rows as the server does, and the browser's beside them; a
	// link through the server's file answers as it does without
	// JavaScript.
	if got := catalogRows(tb); !slices.Equal(got, []string{"vpic vpic", "server classics"}) {
		t.Errorf("ready: catalog %q", got)
	}
	if got := tb.html(`tr[data-where="server"]`); got != off.serverRow {
		t.Errorf("ready: the server's row differs from JS off's\nJS on:  %s\nJS off: %s", got, off.serverRow)
	}
	if !strings.Contains(tb.html("#browser-files"), "No files are kept in this browser yet.") {
		t.Error("ready: no word that the browser keeps no file")
	}
	tb.audit("the dialog ready, no file kept")
	tb.do(chromedp.Navigate(base + throughServer))
	tb.wait("#result")
	if got := mainHTML(tb); got != off.translation {
		t.Errorf("%s with JS on differs from JS off\nJS on:  %s\nJS off: %s", throughServer, got, off.translation)
	}

	// Finding 8: real status codes, the one error page and the fragment.
	for _, c := range []struct {
		target string
		htmx   bool
		status string
		start  string
	}{
		{"/ui/nope", false, "404", "<!doctype html>"},
		{"/ui/nope", true, "404", "<p><strong>Not Found.</strong> There is no page at /ui/nope."},
		{"/ui/files", true, "200", "<title>Your files · nitrovin</title>"},
	} {
		got := eval[[]string](tb, `fetch(`+jsString(c.target)+`, {headers: `+map[bool]string{true: `{"HX-Request": "true"}`, false: `{}`}[c.htmx]+`}).then(async r => [String(r.status), r.headers.get("Content-Type"), await r.text()])`)
		if got[0] != c.status || got[1] != "text/html; charset=utf-8" || !strings.HasPrefix(got[2], c.start) {
			t.Errorf("%s (htmx %v): %s %s %.120q; want %s, starting %q", c.target, c.htmx, got[0], got[1], got[2], c.status, c.start)
		}
	}

	tb.do(chromedp.Navigate(base + "/"))
	tb.vocabularies()
	eval[bool](tb, `window.stayed = true`)

	// Finding 4: one batch, in an order that links only as a whole:
	// listing maps onto the parts spine.
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{filepath.Join(fixture, "listing.jsonl"), filepath.Join(fixture, "parts.jsonl")}))
	tb.waitPrefix("#load-status", "Loaded listing (")
	if got := catalogRows(tb); !slices.Equal(got, []string{"vpic vpic", "server classics", "browser listing", "browser parts"}) {
		t.Errorf("a batch: catalog %q", got)
	}
	if got := storedNames(tb); !slices.Equal(got, []string{"listing", "parts"}) {
		t.Errorf("a batch: IndexedDB holds %q", got)
	}

	// Files dropped on the page load as if picked: yard, with drift, which
	// maps into the server's classics.
	tb.dropFiles(filepath.Join(driftDir, "yard.jsonl"))
	tb.waitPrefix("#load-status", "Loaded yard (")
	if s := tb.html(`tr[data-where="browser"]:has(a[href="/ui/vocabularies/yard"])`); !strings.Contains(s, "<summary>Drift: 1 make, 1 model, 2 qualifier</summary>") ||
		!strings.Contains(s, "<li>line 4: "+driftDetail("/mappings/0/make: vPIC make 99999 is not in data month %[1]s")+"</li>") {
		t.Errorf("a dropped file with drift: %s", s)
	}

	// EPA's vocabulary in one click, from the release's assets.
	tb.do(chromedp.Click(chromedp.CSS("#load-epa")))
	tb.waitPrefix("#load-status", "Loaded epa-vehicles (34 lines).")
	if s := tb.html("#browser-files"); !strings.Contains(s, "EPA's vocabulary, epa-vehicles 2026-06-09+"+embedded.Month+", is kept in this browser.") || tb.count("#load-epa") != 0 {
		t.Errorf("EPA loaded: %s", s)
	}
	tb.until("the storage usage", `(document.getElementById("storage-usage") || {}).textContent.startsWith("This site keeps ")`)
	if got := catalogRows(tb); !slices.Equal(got, []string{"vpic vpic", "server classics", "browser epa-vehicles", "browser listing", "browser parts", "browser yard"}) {
		t.Errorf("EPA loaded: catalog %q", got)
	}
	// It translates in the browser as on a server that loaded the same files.
	same := vocabServer(t, nil, filepath.Join(driftDir, "classics.jsonl"), filepath.Join(fixture, "listing.jsonl"), filepath.Join(fixture, "parts.jsonl"),
		filepath.Join(driftDir, "yard.jsonl"), epaPath)
	for _, target := range []string{
		"/?from.model=3876&model_year=2003&to=epa-vehicles" + pinned,
		"/?from.vocabulary=epa-vehicles&from.entry.make=GMC&from.entry.model=Sierra+LTD+K15+4WD&to=yard" + pinned,
	} {
		tb.do(chromedp.Navigate(base + target))
		tb.wait("#result")
		want := eval[string](tb, `new DOMParser().parseFromString(`+jsString(mainOf(t, same, base, target, false))+`, "text/html").getElementById("ui-main").outerHTML`)
		if got := mainHTML(tb); got != want {
			t.Errorf("%s: the worker's page differs from the server's\nworker: %s\nserver: %s", target, got, want)
		}
	}
	tb.audit("a translation in the browser")

	tb.do(chromedp.Navigate(base + "/"))
	tb.vocabularies()
	eval[bool](tb, `window.stayed = true`)
	eval[bool](tb, `(document.querySelectorAll("table.catalog details").forEach(d => d.open = true), true)`)
	tb.audit("the dialog with files, drift shown, EPA's held")

	// A file named like a stored one replaces it, and what links to it
	// still links.
	replaced := filepath.Join(t.TempDir(), "parts.jsonl")
	b, err := os.ReadFile(filepath.Join(fixture, "parts.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replaced, []byte(strings.Replace(string(b), `"version":"2026.11"`, `"version":"2026.12"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{replaced}))
	tb.waitPrefix("#load-status", "Loaded parts (")
	if s := tb.html(`tr[data-where="browser"]:has(a[href="/ui/vocabularies/parts"])`); !strings.Contains(s, `<dd class="version">2026.12</dd>`) {
		t.Errorf("a replaced file: %s", s)
	}
	if s := tb.html(`tr[data-where="browser"]:has(a[href="/ui/vocabularies/listing"])`); !strings.Contains(s, "linked, no drift") {
		t.Errorf("a replaced file's dependant: %s", s)
	}

	// Finding 8: a load's error names the file, never a stored key.
	bad := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(bad, []byte(`{"format":1,"kind":"vocabulary","name":"bad","version":"1","subject":"vehicle","fields":["make"]}`+"\n"+
		`{"entry":{"make":"A"},"mappings":[],"colour":"red"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{bad}))
	tb.waitPrefix("#load-status", "Nothing was loaded: bad.jsonl:2: ")
	if s := tb.html("#load-status"); regexp.MustCompile(`[0-9a-f]{64}`).MatchString(s) {
		t.Errorf("a refused load names a key: %s", s)
	}
	if got := storedNames(tb); !slices.Equal(got, []string{"epa-vehicles", "listing", "parts", "yard"}) {
		t.Errorf("a refused load: IndexedDB holds %q", got)
	}

	// Finding 3: a refused remove says so, names what depends, and
	// changes nothing; "remove all" removes them all.
	tb.do(chromedp.Click(chromedp.CSS(`form.inline:has(input[value="parts"]) button`)))
	tb.wait(`.refusal[role="alert"]`)
	if s := tb.html(".refusal"); !strings.Contains(s, "parts cannot go alone: listing link to it.") {
		t.Errorf("a refused remove: %s", s)
	}
	if got := storedNames(tb); !slices.Equal(got, []string{"epa-vehicles", "listing", "parts", "yard"}) {
		t.Errorf("a refused remove: IndexedDB holds %q", got)
	}
	if got := catalogRows(tb); !slices.Equal(got, []string{"vpic vpic", "server classics", "browser epa-vehicles", "browser listing", "browser parts", "browser yard"}) {
		t.Errorf("a refused remove: catalog %q", got)
	}
	tb.audit("a refused remove")
	tb.do(chromedp.Click(chromedp.CSS(`.refusal button`)))
	tb.until("remove all", `!document.querySelector(".refusal") && !document.querySelector('a[href="/ui/vocabularies/parts"]')`)
	if got := storedNames(tb); !slices.Equal(got, []string{"epa-vehicles", "yard"}) {
		t.Errorf("remove all: IndexedDB holds %q", got)
	}
	if got := catalogRows(tb); !slices.Equal(got, []string{"vpic vpic", "server classics", "browser epa-vehicles", "browser yard"}) {
		t.Errorf("remove all: catalog %q", got)
	}
	if !eval[bool](tb, `window.stayed === true`) {
		t.Error("loads and removes reloaded the page")
	}

	// Finding 8: Remove clicked the moment the panel is ready goes by htmx,
	// never as a page.
	tb.do(chromedp.Navigate(base + "/"))
	eval[bool](tb, `new Promise(resolve => {
		(function poll() {
			const button = document.querySelector('form.inline:has(input[value="yard"]) button');
			if (document.getElementById("browser").dataset.state !== "ready" || !button) return setTimeout(poll, 0);
			window.stayed = true;
			button.click();
			resolve(true);
		})();
	})`)
	tb.until("a remove at once", `!document.querySelector('a[href="/ui/vocabularies/yard"]')`)
	if !eval[bool](tb, `window.stayed === true`) {
		t.Error("a remove at once went as a page")
	}

	// Finding 2: the list is the store. A stored record this release
	// cannot read is listed with its fault, and removable.
	eval[bool](tb, `new Promise((resolve, reject) => {
		const r = indexedDB.open("nitrovin");
		r.onerror = () => reject(r.error);
		r.onsuccess = () => {
			const db = r.result, t = db.transaction(["files", "texts"], "readwrite"), key = "00".repeat(32);
			t.objectStore("files").put({name: "broken", file_name: "broken.jsonl", blake3: key, size: 3, stored: new Date().toISOString()});
			t.objectStore("texts").put(new TextEncoder().encode("{}\n").buffer, key);
			t.oncomplete = () => { db.close(); resolve(true); };
			t.onerror = () => reject(t.error);
		};
	})`)
	tb.stopWorkers()
	tb.do(chromedp.Navigate(base + "/"))
	tb.vocabularies()
	if s := tb.html(`tr[data-where="browser"]:has(input[value="broken"])`); !strings.Contains(s, `<span class="error">Left out: broken.jsonl:1: `) {
		t.Errorf("a stored file that faults: %s", s)
	}
	tb.do(chromedp.Click(chromedp.CSS(`form.inline:has(input[value="broken"]) button`)))
	tb.until("removing a stored file that faults", `!document.querySelector('input[value="broken"]')`)
	if got := storedNames(tb); !slices.Equal(got, []string{"epa-vehicles"}) {
		t.Errorf("a stored file that faults, removed: IndexedDB holds %q", got)
	}

	// Finding 8: a VIN with the server out of reach fails in words, in the
	// page's controls. The worker answers it for naming a browser file,
	// epa-vehicles, among its targets: a VIN naming none is the server's.
	wire.mu.Lock()
	wire.cutDecodes = true
	wire.mu.Unlock()
	tb.closeVocabularies()
	eval[bool](tb, `(document.querySelector('#controls input[name=to][value="epa-vehicles"]').checked = true)`)
	tb.do(chromedp.SetValue(chromedp.CSS("#from-vin"), vin), chromedp.Click(chromedp.CSS(`#controls form button[type=submit]`)))
	tb.until("an unreachable server", `document.querySelector("#controls .error") !== null`)
	if s := tb.html("#controls .error"); !strings.Contains(s, "could not be reached") || strings.Contains(s, "Failed to fetch") || strings.Contains(s, "TypeError") {
		t.Errorf("an unreachable server: %s", s)
	}
	tb.audit("an unreachable server")
	wire.mu.Lock()
	wire.cutDecodes = false
	wire.mu.Unlock()

	// Finding 1: a hard reload says the files are paused, offers no
	// loading, and still lists the server's catalog.
	tb.do(chromedp.Navigate(base + "/"))
	tb.waitState("ready")
	tb.call(func(ctx context.Context) error {
		_, err := chromedp.Call(ctx, page.Reload, page.ReloadParams{IgnoreCache: new(true)})
		return err
	})
	tb.waitState("paused")
	if n := tb.count("#vocab-files, #load-epa"); n != 0 {
		t.Errorf("hard reload: %d file inputs or EPA buttons", n)
	}
	if got := catalogRows(tb); !slices.Equal(got, []string{"vpic vpic", "server classics"}) {
		t.Errorf("hard reload: catalog %q", got)
	}
	tb.audit("paused by a hard reload")

	// Finding 7: cold starts download nothing again. The worker fetched
	// the module and the spine bundle once, at install, from immutable
	// URLs, and starts from its cache after every stop.
	tb.do(chromedp.Navigate(base + "/"))
	tb.waitState("ready")
	tb.stopWorkers()
	if f := tb.timeFetch("/ui/vocabularies/epa-vehicles"); !strings.Contains(f.Timing, "module;dur=") {
		t.Errorf("a cold start without the module's timing: %q", f.Timing)
	}
	counts := map[string]int{}
	for _, r := range wire.all() {
		if u, err := url.Parse(r.target); err == nil {
			for _, name := range []string{"sw.wasm", "spine.json"} {
				if strings.HasSuffix(u.Path, "/"+name) {
					counts[name]++
				}
			}
		}
	}
	if counts["sw.wasm"] != 1 || counts["spine.json"] != 1 {
		t.Errorf("the server got %d requests for sw.wasm and %d for spine.json over three cold starts; want one each", counts["sw.wasm"], counts["spine.json"])
	}
	if got := eval[string](tb, `fetch(document.querySelector('script[src$="/htmx.min.js"]').src.replace("htmx.min.js", "sw.wasm"), {cache: "no-store"}).then(r => r.headers.get("Cache-Control"))`); got != "public, max-age=31536000, immutable" {
		t.Errorf("sw.wasm Cache-Control %q", got)
	}

	// No file reached the server: every request it got was a GET or HEAD
	// with no body, and none but the release's EPA asset named a file the
	// browser holds.
	held := []string{"listing", "parts", "yard", "epa-vehicles", "bad", "broken"}
	for _, r := range wire.all() {
		if (r.method != http.MethodGet && r.method != http.MethodHead) || r.body != 0 {
			t.Errorf("the server got %s %s with %d bytes of body", r.method, r.target, r.body)
		}
		u, err := url.Parse(r.target)
		if err != nil || strings.HasPrefix(u.Path, "/ui/r/") {
			continue
		}
		q := u.Query()
		for _, name := range held {
			if u.Path == "/ui/vocabularies/"+name || q.Get("from.vocabulary") == name || slices.Contains(q["to"], name) {
				t.Errorf("the server got %s, which names %s, a file the browser holds", r.target, name)
			}
		}
	}
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if len(tb.thrown) > 0 {
		t.Errorf("thrown: %q", tb.thrown)
	}
	if refused := eval[[]string](tb, `window.refused || []`); len(refused) > 0 {
		t.Errorf("CSP refused %q", refused)
	}
}

// catalogRows are the catalog's rows on the page, "where name" each.
func catalogRows(tb *tab) []string {
	tb.t.Helper()
	return eval[[]string](tb, `[...document.querySelectorAll("table.catalog tbody tr")].map(tr => tr.dataset.where + " " + tr.querySelector("th").textContent)`)
}

// storedNames are the names of the files IndexedDB holds, by name.
func storedNames(tb *tab) []string {
	tb.t.Helper()
	return eval[[]string](tb, `new Promise((resolve, reject) => {
		const r = indexedDB.open("nitrovin");
		r.onerror = () => reject(r.error);
		r.onsuccess = () => {
			const db = r.result, q = db.transaction("files").objectStore("files").getAllKeys();
			q.onsuccess = () => { db.close(); resolve(q.result.sort()); };
			q.onerror = () => reject(q.error);
		};
	})`)
}

// until waits, up to 20 s, for the expression to be true.
func (tb *tab) until(step, expression string) {
	tb.t.Helper()
	if !eval[bool](tb, `new Promise(resolve => {
		const start = Date.now();
		(function poll() {
			if (`+expression+`) resolve(true); else if (Date.now() - start > 20000) resolve(false); else setTimeout(poll, 50);
		})();
	})`) {
		tb.t.Fatalf("%s: %s never held", step, expression)
	}
}

// dropFiles drags the files at paths over the page and drops them, as a
// visitor drops files from their desktop.
func (tb *tab) dropFiles(paths ...string) {
	tb.t.Helper()
	data := &input.DragData{Items: []*input.DragDataItem{}, Files: paths, DragOperationsMask: 1}
	tb.call(func(ctx context.Context) error {
		for _, typ := range []input.DispatchDragEventType{input.DispatchDragEventTypeDragEnter, input.DispatchDragEventTypeDragOver, input.DispatchDragEventTypeDrop} {
			if _, err := chromedp.Call(ctx, input.DispatchDragEvent, input.DispatchDragEventParams{Type: typ, X: 200, Y: 200, Data: data}); err != nil {
				return err
			}
		}
		return nil
	})
}
