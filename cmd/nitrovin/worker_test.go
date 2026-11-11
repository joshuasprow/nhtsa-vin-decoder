package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/serviceworker"
	"github.com/chromedp/chromedp"

	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/ui"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The browser's Service Worker (ADR 0013), in headless Chrome or Chromium
// with a fresh profile: it loads files the server never sees, answers
// what the server answers for the same files, recovers when the browser
// stops it, starts cold from stored indexes, and a hard reload says it is
// paused.

// wireLog serves h and records every request the server gets: method,
// path and query, and how many bytes of body it carried.
type wireLog struct {
	h    http.Handler
	mu   sync.Mutex
	seen []wireRequest
	// swTail, when not "", is added to /sw.js: another worker, as a new
	// release or a restart with other files makes.
	swTail string
	// cutDecodes drops the connection of every request for a decode, the
	// API's and the page's (/ui/decoded), as an unreachable server does.
	cutDecodes bool
}

type wireRequest struct {
	method, target string
	body           int
}

func (l *wireLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	l.mu.Lock()
	l.seen = append(l.seen, wireRequest{r.Method, r.URL.RequestURI(), len(body)})
	tail, cut := l.swTail, l.cutDecodes
	l.mu.Unlock()
	if cut && (strings.HasPrefix(r.URL.Path, "/v1/decode/") || r.URL.Path == "/ui/decoded") {
		if conn, _, err := http.NewResponseController(w).Hijack(); err == nil {
			_ = conn.Close()
		}
		return
	}
	if r.URL.Path != "/sw.js" || tail == "" {
		l.h.ServeHTTP(w, r)
		return
	}
	rec := httptest.NewRecorder()
	l.h.ServeHTTP(rec, r)
	for k, vs := range rec.Result().Header {
		w.Header()[k] = vs
	}
	w.Header().Del("Content-Length")
	w.WriteHeader(rec.Code)
	_, _ = w.Write(append(rec.Body.Bytes(), tail...))
}

func (l *wireLog) all() []wireRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.seen)
}

// buildWorker is the module as a release builds it (scripts/sw.sh), built
// twice, once per test binary, the two builds held to the same bytes.
func buildWorker(t *testing.T) ui.Worker {
	t.Helper()
	w, err := builtWorker()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

var builtWorker = sync.OnceValues(func() (ui.Worker, error) {
	var builds [2]ui.Worker
	for i := range builds {
		dir, err := os.MkdirTemp("", "nitrovin-sw")
		if err != nil {
			return ui.Worker{}, err
		}
		defer os.RemoveAll(dir)
		cmd := exec.Command("bash", "../../scripts/sw.sh", dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return ui.Worker{}, fmt.Errorf("scripts/sw.sh: %v\n%s", err, out)
		}
		if builds[i].Module, err = os.ReadFile(filepath.Join(dir, "sw.wasm")); err != nil {
			return ui.Worker{}, err
		}
		if builds[i].Exec, err = os.ReadFile(filepath.Join(dir, "wasm_exec.js")); err != nil {
			return ui.Worker{}, err
		}
	}
	if !bytes.Equal(builds[0].Module, builds[1].Module) {
		return ui.Worker{}, errors.New("two builds of the module differ: it is not reproducible")
	}
	return builds[0], nil
})

// vocabServer serves the API with the embedded data and the files at
// paths loaded, with the worker's module when w is not nil.
func vocabServer(t *testing.T, w *ui.Worker, paths ...string) http.Handler {
	t.Helper()
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(paths...)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAPI(loaded{d, tr}, log.New(io.Discard, "", 0), io.Discard, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if w != nil {
		a.ui.WithWorker(*w)
	}
	return a
}

// mainOf is the outer HTML of a page's main, from a server asked as the
// browser at base asks: the page's own URL is in it.
func mainOf(t *testing.T, h http.Handler, base, target string, htmx bool) string {
	t.Helper()
	r := httptest.NewRequest("GET", base+target, nil)
	if htmx {
		r.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d from the server", target, w.Code)
	}
	body := w.Body.String()
	if htmx {
		return body
	}
	start, end := strings.Index(body, `<main id="ui-main" hx-history-elt>`), strings.Index(body, "</main>")
	if start < 0 || end < 0 {
		t.Fatalf("%s: no main in %s", target, body)
	}
	return body[start : end+len("</main>")]
}

// mainHTML is the outer HTML of the page's main as the server drew it:
// without the copy buttons copy.js adds.
func mainHTML(tb *tab) string {
	tb.t.Helper()
	return eval[string](tb, `(() => {
		const main = document.getElementById("ui-main").cloneNode(true);
		main.querySelectorAll("button.copy").forEach(b => b.remove());
		return main.outerHTML;
	})()`)
}

// vocabularies opens the page's Vocabularies dialog, where the browser's
// files are loaded and removed, and waits for the worker's panel in it:
// the dialog's first opening starts the worker.
func (tb *tab) vocabularies() {
	tb.t.Helper()
	if tb.count("#vocabularies:popover-open") == 0 {
		tb.do(chromedp.Click(chromedp.CSS("button.fab")))
		tb.wait("#vocabularies:popover-open")
	}
	tb.waitState("ready")
}

// closeVocabularies closes the dialog, by its own button.
func (tb *tab) closeVocabularies() {
	tb.t.Helper()
	tb.do(chromedp.Click(chromedp.CSS("#vocabularies .dialog-head button")))
	tb.wait("#vocabularies:not(:popover-open)")
}

// pinned ends a page's query with a clock: its answer's JSON says when it
// was asked, so two answers to compare are asked at one time.
const pinned = "&now=2026-11-01T00:00:00Z"

// TestWorker runs the worker in a fresh profile against a server that has
// loaded classics alone: the browser loads yard (which maps into the
// server's classics), the parts spine and the part vocabulary listing in
// one batch, listing first, and a synthetic vocabulary of 150,000 entries.
// What it answers is held to what a server that loaded the same files
// answers. Set NITROVIN_COLD_START_BUDGET (say 1s) to hold the cold start
// to a budget; it is reported either way.
func TestWorker(t *testing.T) {
	path := chromePath()
	if path == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no Chrome or Chromium to run the worker in: set NITROVIN_CHROME")
		}
		t.Skip("no Chrome or Chromium: set NITROVIN_CHROME to run the worker in a browser")
	}
	if testing.Short() {
		t.Skip("builds the module and loads 150,000 entries in a browser")
	}
	fixtureData(t)
	module := buildWorker(t)
	big := filepath.Join(t.TempDir(), "synthetic.jsonl")
	const entries = 150_000
	probe := "/?" + writeSynthetic(t, big, entries).Encode() + pinned

	wire := &wireLog{h: vocabServer(t, &module, filepath.Join(fixture, "classics.jsonl"))}
	srv := httptest.NewServer(wire)
	t.Cleanup(srv.Close)
	// The references: a server that loaded the same files, before the
	// synthetic one is loaded and after.
	same := vocabServer(t, nil, fixture)

	tb := newTab(t, path, srv.URL, &requestLog{}, true)
	// The page starts no worker until its Vocabularies dialog opens.
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.wait("#browser")
	if state := eval[string](tb, `new Promise(resolve => setTimeout(() => resolve(document.getElementById("browser").dataset.state || ""), 500))`); state != "" ||
		!eval[bool](tb, `navigator.serviceWorker.getRegistration("/").then(r => r === undefined)`) {
		t.Errorf("the page alone: worker state %q, or a worker registered; want none before the dialog opens", state)
	}
	tb.vocabularies()
	if eval[bool](tb, `navigator.serviceWorker.controller === null`) {
		t.Fatal("ready, yet no worker controls the page")
	}

	// One batch, in an order that only links as a whole: listing maps onto
	// the parts spine, and yard into the server's classics.
	start := time.Now()
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{
		filepath.Join(fixture, "listing.jsonl"), filepath.Join(fixture, "yard.jsonl"), filepath.Join(fixture, "parts.jsonl"),
	}))
	tb.waitPrefix("#load-status", "Loaded ")
	t.Logf("loaded the fixture batch in %v", time.Since(start).Round(time.Millisecond))
	if got := eval[[]string](tb, `[...document.querySelectorAll("table.catalog tr[data-where=browser] th")].map(th => th.firstChild.textContent)`); !slices.Equal(got, []string{"listing", "parts", "yard"}) {
		t.Errorf("stored files %q; want listing, parts, yard", got)
	}

	queries := []string{
		"from.vocabulary=yard&from.entry.make=Packard&from.entry.model=Clipper&to=yard",
		"from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500&model_year=2010&to=yard",
		"from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500+4x4&vin=1C6RR7LT5FS123456&to=yard",
		"from.vin=1D7RV1GT5AS123456&to=yard",
		"from.vin=1GCVKREC6EZ123456&to=yard",
		"from.ext=ext:classics/packard-clipper&to=yard",
		"from.vocabulary=listing&from.entry.category=Brake+Pads&vin=JH2PC40066M200001&to=listing",
		"from.spine=parts&from.id=brake-pads&to=listing",
		"from.vocabulary=yard&from.entry.make=Nobody&from.entry.model=Nothing&to=yard",
	}
	same1 := func(step, target string) {
		t.Helper()
		// As a page the browser navigates to.
		tb.do(chromedp.Navigate(srv.URL + target))
		tb.wait("#ui-main")
		// The server's main as this browser parses it, as the worker's is.
		want := eval[string](tb, `new DOMParser().parseFromString(`+jsString(mainOf(t, same, srv.URL, target, false))+`, "text/html").getElementById("ui-main").outerHTML`)
		if got := mainHTML(tb); got != want {
			t.Errorf("%s %s: the worker's page differs from the server's\nworker: %s\nserver: %s", step, target, got, want)
		}
		// As the fragment htmx asks for.
		got := eval[[]string](tb, `fetch(`+jsString(target)+`, {headers: {"HX-Request": "true"}}).then(async r => [String(r.status), r.headers.get("Content-Security-Policy"), r.headers.get("Vary"), await r.text()])`)
		if got[0] != "200" || got[1] != ui.CSP || got[2] != "HX-Request" {
			t.Errorf("%s %s fragment: status %s, CSP %q, Vary %q", step, target, got[0], got[1], got[2])
		}
		if want := mainOf(t, same, srv.URL, target, true); got[3] != want {
			t.Errorf("%s %s: the worker's fragment differs from the server's\nworker: %s\nserver: %s", step, target, got[3], want)
		}
	}
	// Each of the page's tabs, its targets named (unnamed, the browser
	// ticks the visitor's selection, which the server has none of), and
	// each lookup: a VIN's with its decode, which is the server's.
	for _, target := range []string{"/?to=yard", "/?tab=entry&to=yard", "/?tab=entry-parts&to=listing", "/?tab=id&to=yard"} {
		same1("the page", target)
	}
	for _, q := range queries {
		same1("lookup", "/?"+q+pinned)
	}
	same1("list", "/ui/vocabularies/yard")
	same1("list", "/ui/vocabularies/listing?limit=1")

	// A stopped worker starts again on the next request and answers the same.
	tb.stopWorkers()
	same1("after a stop", "/?"+queries[3]+pinned)

	// A hard reload bypasses the worker: the page says the files are paused.
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.waitState("ready")
	tb.call(func(ctx context.Context) error {
		_, err := chromedp.Call(ctx, page.Reload, page.ReloadParams{IgnoreCache: new(true)})
		return err
	})
	tb.waitState("paused")
	if s := tb.html("#browser-status"); !strings.Contains(s, "paused for this load") {
		t.Errorf("hard reload: %s", s)
	}
	if n := tb.count("#vocab-files"); n != 0 {
		t.Errorf("hard reload: a file input with no worker to take the files")
	}
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()

	// A load the worker never finishes (stopped mid-load) gives up after
	// the stall time, here 3 s, and stores nothing.
	eval[bool](tb, `(document.getElementById("browser").dataset.stall = "3000", true)`)
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{big}))
	time.Sleep(300 * time.Millisecond)
	tb.stopWorkers()
	tb.waitPrefix("#load-status", "Nothing was loaded: The load stalled")
	if got := eval[[]string](tb, `[...document.querySelectorAll("table.catalog tr[data-where=browser] th")].map(th => th.firstChild.textContent)`); !slices.Equal(got, []string{"listing", "parts", "yard"}) {
		t.Errorf("after a stalled load: stored files %q; want listing, parts, yard", got)
	}

	// 150,000 entries, then cold starts: the worker stopped, a translation
	// naming the file. The worker reads every file's stored index.
	start = time.Now()
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{big}))
	tb.waitPrefix("#load-status", "Loaded ")
	t.Logf("loaded %d synthetic entries (%d bytes) in %v", entries, fileSize(t, big), time.Since(start).Round(time.Millisecond))
	same = vocabServer(t, nil, fixture, big)
	warm := tb.timeFetch(probe)
	var cold []float64
	for range 5 {
		tb.stopWorkers()
		f := tb.timeFetch(probe)
		cold = append(cold, f.MS)
		t.Logf("cold start: %.0f ms; worker: %s", f.MS, f.Timing)
	}
	sort.Float64s(cold)
	t.Logf("translation naming the 150,000-entry file, worker stopped first: cold start %.0f ms median (each %v); warm %.1f ms", cold[len(cold)/2], round(cold), warm.MS)
	if b := os.Getenv("NITROVIN_COLD_START_BUDGET"); b != "" {
		budget, err := time.ParseDuration(b)
		if err != nil {
			t.Fatal(err)
		}
		if med := time.Duration(cold[len(cold)/2] * float64(time.Millisecond)); med > budget {
			t.Errorf("cold start %v; budget %v", med, budget)
		}
	}
	same1("cold, 150,000 entries", probe)

	// A new worker (here, /sw.js with other bytes) installs beside the one
	// in control and waits for this page to close; the page says so, and
	// the old one keeps answering.
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.waitState("ready")
	wire.mu.Lock()
	wire.swTail = "// another release\n"
	wire.mu.Unlock()
	eval[bool](tb, `navigator.serviceWorker.getRegistration().then(r => r.update()).then(() => true)`)
	if got := eval[string](tb, `new Promise(resolve => {
		const start = Date.now();
		(function poll() {
			const s = document.getElementById("browser").dataset.update || "";
			if (s === "waiting" || Date.now() - start > 30000) resolve(s); else setTimeout(poll, 50);
		})();
	})`); got != "waiting" {
		t.Errorf("an update: data-update %q; want waiting", got)
	}
	if s := tb.html("#browser-status"); !strings.Contains(s, "starts once every nitrovin tab is closed") {
		t.Errorf("an update: %s", s)
	}
	if eval[bool](tb, `navigator.serviceWorker.getRegistration().then(r => r.waiting === null || r.active === null)`) {
		t.Error("an update: no worker waiting beside the active one")
	}
	same1("with an update waiting", "/?"+queries[0]+pinned)

	// The files never reached the server: every request it got was a GET
	// or HEAD with no body, and none but for a release's asset named a
	// file the browser holds.
	var lines []string
	for _, r := range wire.all() {
		lines = append(lines, fmt.Sprintf("%s %s (%d bytes)", r.method, r.target, r.body))
		if (r.method != http.MethodGet && r.method != http.MethodHead) || r.body != 0 {
			t.Errorf("the server got %s %s with %d bytes of body", r.method, r.target, r.body)
		}
		if u, err := url.Parse(r.target); err == nil && !strings.HasPrefix(u.Path, "/ui/r/") {
			for _, name := range []string{"listing", "parts", "yard", "synthetic"} {
				if strings.Contains(r.target, name) {
					t.Errorf("the server got %s, which names %s, a file the browser holds", r.target, name)
				}
			}
		}
	}
	t.Logf("every request the server got:\n%s", strings.Join(lines, "\n"))
	tb.mu.Lock()
	defer tb.mu.Unlock()
	if len(tb.thrown) > 0 {
		t.Errorf("thrown: %q", tb.thrown)
	}
	if refused := eval[[]string](tb, `window.refused || []`); len(refused) > 0 {
		t.Errorf("CSP refused %q", refused)
	}
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func round(ms []float64) []string {
	var out []string
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%.0fms", m))
	}
	return out
}

func fileSize(t *testing.T, path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// waitState waits, up to a minute, for #browser's data-state to be state.
func (tb *tab) waitState(state string) {
	tb.t.Helper()
	got := eval[string](tb, `new Promise(resolve => {
		const want = `+jsString(state)+`, start = Date.now();
		(function poll() {
			const el = document.getElementById("browser"), s = el ? el.dataset.state || "" : "(no #browser)";
			if (s === want || Date.now() - start > 55000) resolve(s + ": " + (document.getElementById("browser-status") || {}).textContent); else setTimeout(poll, 50);
		})();
	})`)
	if !strings.HasPrefix(got, state+": ") {
		tb.t.Fatalf("browser state %q; want %s", got, state)
	}
}

// waitPrefix waits, up to a minute, for the text of sel to start with
// prefix, and fails with what it says instead.
func (tb *tab) waitPrefix(sel, prefix string) {
	tb.t.Helper()
	args, _ := json.Marshal([2]string{sel, prefix})
	got := eval[string](tb, `new Promise(resolve => {
		const [sel, want] = `+string(args)+`, start = Date.now();
		(function poll() {
			const el = document.querySelector(sel), text = el ? el.textContent : "";
			if (text.startsWith(want) || text.startsWith("Nothing was loaded") || Date.now() - start > 55000) resolve(text); else setTimeout(poll, 50);
		})();
	})`)
	if !strings.HasPrefix(got, prefix) {
		tb.t.Fatalf("%s: %q; want it to start %q", sel, got, prefix)
	}
}

// stopWorkers stops the browser's Service Workers, as a browser does with
// an idle one: the next request starts it cold.
func (tb *tab) stopWorkers() {
	tb.t.Helper()
	tb.call(func(ctx context.Context) error {
		if _, err := chromedp.Call(ctx, serviceworker.Enable, cdp.Empty{}); err != nil {
			return err
		}
		_, err := chromedp.Call(ctx, serviceworker.StopAllWorkers, cdp.Empty{})
		return err
	})
}

// fetched is one timed request: milliseconds from fetch to the whole
// body, and the worker's Server-Timing (its start and the Library's
// steps on a cold start).
type fetched struct {
	MS     float64 `json:"ms"`
	Timing string  `json:"timing"`
}

// timeFetch times target as htmx asks for it.
func (tb *tab) timeFetch(target string) fetched {
	tb.t.Helper()
	return eval[fetched](tb, `(async () => {
		const t0 = performance.now();
		const r = await fetch(`+jsString(target)+`, {headers: {"HX-Request": "true"}});
		await r.text();
		if (r.status !== 200) throw new Error("HTTP " + r.status);
		return {ms: performance.now() - t0, timing: r.headers.get("Server-Timing") || ""};
	})()`)
}

// writeSynthetic writes a vehicle vocabulary of n entries at the scale of
// eBay's (make, model, trim, year: one entry per year), over the vPIC
// models of the embedded month, each mapped to its model for its year and
// every third with a Drive Type qualifier. The trims are made up. It
// returns the query naming one entry from the middle of the file.
func writeSynthetic(t *testing.T, path string, n int) url.Values {
	t.Helper()
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	m := d.Month()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	fmt.Fprintf(w, `{"format":1,"kind":"vocabulary","name":"synthetic","version":"1","data_month":%q,"subject":"vehicle","fields":["make","model","trim","year"]}`+"\n", m.Name())
	type model struct {
		make, name string
		id         int32
	}
	var all []model
	seen := map[string]bool{}
	for mk, mkName := range m.Makes() {
		for id, name := range m.Models(mk) {
			// One entry per name: two vPIC models of one name under one
			// make would be one entry twice.
			if k := vocab.Normalize(mkName) + "\x00" + vocab.Normalize(name); !seen[k] {
				seen[k] = true
				all = append(all, model{mkName, name, id})
			}
		}
	}
	slices.SortFunc(all, func(a, b model) int { return int(a.id) - int(b.id) })
	per := n/len(all) + 1
	written := 0
	var probe url.Values
	for i, x := range all {
		for j := 0; j < per && written < n; j++ {
			trim, year := fmt.Sprintf("Trim %d", j%8), fmt.Sprint(1981+j/8)
			entry, _ := json.Marshal(map[string]string{"make": x.make, "model": x.name, "trim": trim, "year": year})
			qualifiers := ""
			if written%3 == 0 {
				qualifiers = `,"qualifiers":{"15":[2]}`
			}
			fmt.Fprintf(w, `{"entry":%s,"mappings":[{"model":%d,"first":%s,"last":%s%s,"origin":"derived"}]}`+"\n", entry, x.id, year, year, qualifiers)
			written++
			if i == len(all)/2 && j == 0 {
				probe = url.Values{"from.vocabulary": {"synthetic"}, "from.entry.make": {x.make}, "from.entry.model": {x.name},
					"from.entry.trim": {trim}, "from.entry.year": {year}, "to": {"synthetic", "yard"}}
			}
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if written != n || probe == nil {
		t.Fatalf("wrote %d entries, probe %v", written, probe)
	}
	return probe
}
