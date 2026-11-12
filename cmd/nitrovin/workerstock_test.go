package main

import (
	"fmt"
	"html"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// TestWorkerStock runs stock in the browser (ADR 0023, ADR 0019) in a
// fresh profile: beside the browser's compiled catalog, the dialog loads
// the synthetic shelves and yard, picked together, lists each as a shelf
// or a yard with its drift, and repeats ADR 0019's warning about personal
// data; it refuses a connector's file in words. The yard's VINs alone go
// to the server, once; no row's fields ever do. The worker's fit pages,
// with every kind of stock, at locations and a page at a time, are the
// server's on the same files loaded in the same order, and stay so after
// a reload. It logs the module's retained memory with the stock beside
// the catalog, against ADR 0023's 64 MiB.
func TestWorkerStock(t *testing.T) {
	path := chromePath()
	if path == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no Chrome or Chromium to run the worker in: set NITROVIN_CHROME")
		}
		t.Skip("no Chrome or Chromium: set NITROVIN_CHROME to run the worker in a browser")
	}
	if testing.Short() {
		t.Skip("builds the module and compiles a catalog in a browser")
	}
	module := buildWorker(t)
	fitment := filepath.Join(fixture, "fitment")
	stock := []string{
		filepath.Join(stockFixture, "shelf-annex.jsonl"), filepath.Join(stockFixture, "drift", "shelf-orphans.jsonl"),
		filepath.Join(stockFixture, "shelf-stock.jsonl"), filepath.Join(stockFixture, "yard-stock.jsonl"),
	}

	wire := &wireLog{h: vocabServer(t, &module, filepath.Join(fitment, "part-types.jsonl"), filepath.Join(fitment, "part-codes.jsonl"))}
	srv := httptest.NewServer(wire)
	t.Cleanup(srv.Close)
	tb := newTab(t, path, srv.URL, &requestLog{}, true)
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()

	// A shelf before the browser holds its catalog is refused, and so is a
	// connector's file.
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), stock[2:3]))
	tb.waitPrefix("#load-status", `Nothing was loaded: shelf-stock.jsonl is a shelf of catalog "parts-catalog", which this browser does not hold`)
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{filepath.Join(stockFixture, "live", "yard-stock.jsonl")}))
	tb.waitPrefix("#load-status", "Nothing was loaded: yard-stock.jsonl declares a connector, reading stock.yard_stock from a database: connectors run on a server")

	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{filepath.Join(fitment, "catalog-models.jsonl")}))
	tb.waitPrefix("#load-status", "Loaded ")
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{filepath.Join(fitment, "catalog.jsonl")}))
	tb.waitPrefix("#load-status", "Compiled parts-catalog (5 fitments")
	alone := tb.timeFetch("/ui/fit?from.model=1938&model_year=2010" + pinned).Timing
	before := len(wire.all())
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), stock))
	tb.waitPrefix("#load-status", "Loaded shelf-annex (2 lines), shelf-orphans (2 lines), shelf-stock (5 lines), yard-stock (4 lines).")
	panel := tb.html("#browser-files")
	for _, want := range []string{
		"stock, a shelf of parts-catalog, 5 rows", "stock, a yard, 4 rows", "Drift: 1 orphan",
		"Connectors run on a server.", "a stock file supplies no personal data: no names, contact details or identity numbers of owners, sellers, customers or staff",
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("the panel lacks %q:\n%s", want, panel)
		}
	}

	// The yard's one VIN to decode went to the server, once, alone.
	var posts []wireRequest
	for _, r := range wire.all()[before:] {
		if r.body > 0 {
			posts = append(posts, r)
		}
	}
	if len(posts) != 1 || posts[0].method != "POST" || posts[0].target != "/v1/decode" || posts[0].data != `[{"vin":"1D7RV1GT5AS123456","full":true}]` {
		t.Errorf("the load sent %+v; want the yard's one VIN to decode", posts)
	}

	same := vocabServer(t, nil, append([]string{filepath.Join(fitment, "part-types.jsonl"), filepath.Join(fitment, "part-codes.jsonl"),
		filepath.Join(fitment, "catalog-models.jsonl"), filepath.Join(fitment, "catalog.jsonl")}, stock...)...)
	v1 := regexp.MustCompile(`<p class="hint"><a href="/v1/fit[^"]*"[^>]*>The same request on /v1</a></p>`)
	ram := "from.vocabulary=catalog-models&from.entry.make=Dodge&from.entry.model=Ram+1500&model_year=2010"
	pages := []string{
		ram + "&part.vocabulary=part-codes&part.entry.code=ROT&stock=shelf&stock=yard",
		ram + "&part.spine=part-types&part.id=mirror&stock=shelf&stock=yard",
		ram + "&part.spine=part-types&part.id=mirror&side=left&stock=shelf&location=north",
		ram + "&related=true&stock=yard",
		"from.model=1938&related=true&stock=yard&location=south",
		"from.vin=1D7RV1GT5AS123456&part.spine=part-types&part.id=brake-rotor-front&stock=shelf&stock=yard",
		"from.fitment.type=brake-rotor-front&from.fitment.number=BR-2001&stock=shelf&stock=yard",
		"from.fitment.type=brake-rotor-front&from.fitment.number=BR-1999&stock=shelf&stock=yard&limit=1",
		"from.fitment.type=mirror&from.fitment.number=MR-1001&from.fitment.side=left&stock=shelf&stock=yard",
		"from.fitment.type=mirror&from.fitment.number=MR-0000&from.fitment.side=right&stock=shelf",
		"from.vocabulary=catalog-models&from.entry.make=Chevrolet&from.entry.model=Silverado+1500&model_year=2010&part.spine=part-types&part.id=engine&stock=shelf&stock=yard",
	}
	samePages := func(when string) {
		t.Helper()
		since := len(wire.all())
		for _, q := range pages {
			target := "/ui/fit?" + q + pinned
			got := eval[[]string](tb, `fetch(`+jsString(target)+`, {headers: {"HX-Request": "true"}}).then(async r => [String(r.status), await r.text()])`)
			want := mainOf(t, same, srv.URL, target, true)
			if got[0] != "200" || got[1] != v1.ReplaceAllString(want, "") {
				t.Errorf("%s, %s: the worker's fit page differs from the server's\nworker: %s %s\nserver: %s", when, target, got[0], got[1], want)
			}
		}
		for _, r := range wire.all()[since:] {
			if strings.HasPrefix(r.target, "/ui/fit") || r.body > 0 {
				t.Errorf("%s: the server got %s %s (%d bytes): the fit page is the browser's", when, r.method, r.target, r.body)
			}
		}
	}
	samePages("loaded")
	// The stock lists are on those pages, every route a row takes.
	fragment := eval[string](tb, `fetch(`+jsString("/ui/fit?"+pages[0]+pinned)+`, {headers: {"HX-Request": "true"}}).then(r => r.text())`)
	words := strings.Join(strings.Fields(html.UnescapeString(tag.ReplaceAllString(fragment, " "))), " ")
	for _, want := range []string{"S-2 shelf-stock", "A-1 shelf-annex", "Y-1 yard-stock", "Y-2 yard-stock", "Y-3 yard-stock"} {
		if !strings.Contains(words, want) {
			t.Errorf("the Ram's rotors lack %q: %s", want, words)
		}
	}

	// What the module holds with the stock beside the catalog, after
	// answering every page.
	timing := tb.timeFetch("/ui/fit?" + pages[3] + pinned).Timing
	memory, heap := timingDesc(timing, "memory"), timingDesc(timing, "heap")
	t.Logf("retained with the catalog alone: Go heap %.1f MiB, WebAssembly memory %.1f MiB", float64(timingDesc(alone, "heap"))/(1<<20), float64(timingDesc(alone, "memory"))/(1<<20))
	t.Logf("retained with the stock beside the catalog: Go heap %.1f MiB, WebAssembly memory %.1f MiB", float64(heap)/(1<<20), float64(memory)/(1<<20))
	if memory == 0 || memory > 64<<20 {
		t.Errorf("WebAssembly memory %d bytes; ADR 0023's budget is 64 MiB (%s)", memory, timing)
	}

	// A reload reads the stock, and the yard's decodes, from IndexedDB.
	// A compile that stopped half way left parts, which the dialog's
	// first opening drops and counts.
	eval[bool](tb, `new Promise((resolve, reject) => {
		const r = indexedDB.open("nitrovin-catalog", 1);
		r.onerror = () => reject(r.error);
		r.onsuccess = () => {
			const t = r.result.transaction(["parts"], "readwrite");
			t.objectStore("parts").put(new Blob([new Uint8Array(1000)]), ["scratch 9", 0]);
			t.oncomplete = () => { r.result.close(); resolve(true); };
			t.onerror = () => reject(t.error);
		};
	})`)
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()
	left := eval[string](tb, `new Promise(resolve => {
		const start = Date.now();
		(function poll() {
			const left = document.getElementById("browser").dataset.leftBytes;
			if (left === "0" || Date.now() - start > 10000) resolve(left || ""); else setTimeout(poll, 50);
		})();
	})`)
	if left != "0" {
		t.Errorf("after the dialog's first opening a compile's leftovers are %q bytes; want them dropped, 0", left)
	}
	stored := eval[[]string](tb, `[...document.querySelectorAll("#browser-files tr[data-where=browser] th")].map(th => th.textContent.trim())`)
	if !slices.Equal(stored, []string{"catalog-models", "parts-catalog", "shelf-annex", "shelf-orphans", "shelf-stock", "yard-stock"}) {
		t.Errorf("after a reload: stored %q", stored)
	}
	samePages("after a reload")
	if n := slices.IndexFunc(wire.all(), func(r wireRequest) bool { return r.method == "POST" }); n >= 0 && slices.ContainsFunc(wire.all()[n+1:], func(r wireRequest) bool { return r.method == "POST" }) {
		t.Error("the yard's VINs were decoded again after a reload")
	}

	// A shelf of many numbered rows is a read of the catalog a row to
	// count its orphans: done once, as it loads. A start of the module
	// (a browser stops an idle worker) and the fit page and the panel
	// after it read the catalog only as a fit does. A yard of many VINs
	// to decode keeps the module within its memory.
	dir := t.TempDir()
	many, yard := filepath.Join(dir, "shelf-many.jsonl"), filepath.Join(dir, "yard-many.jsonl")
	writeStock(t, many, `{"format":1,"kind":"stock","name":"shelf-many","version":"1","source":"shelf","as_of":"2026-11-01","catalog":"parts-catalog"}`, 2000,
		func(i int) string {
			return fmt.Sprintf(`{"id":"M-%d","location":"north","type":"brake-rotor-front","number":"BR-%d","fields":{}}`, i, 2001+i%2*1000)
		})
	writeStock(t, yard, `{"format":1,"kind":"stock","name":"yard-many","version":"1","source":"yard","as_of":"2026-11-01"}`, 3000,
		func(i int) string {
			return fmt.Sprintf(`{"id":"V-%d","location":"north","vin":"1D7RV1GT5AS%06d","fields":{}}`, i, i)
		})
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{many, yard}))
	tb.waitPrefix("#load-status", "Loaded shelf-many (2000 lines), yard-many (3000 lines).")
	if s := tb.html("#browser-files"); !strings.Contains(s, "Drift: 1000 orphan") {
		t.Errorf("shelf-many's orphans are not counted: %s", s)
	}
	loaded := tb.timeFetch("/ui/files").Timing
	t.Logf("after loading a 2,000-row shelf and a 3,000-VIN yard: Go heap %.1f MiB, WebAssembly memory %.1f MiB",
		float64(timingDesc(loaded, "heap"))/(1<<20), float64(timingDesc(loaded, "memory"))/(1<<20))
	if memory := timingDesc(loaded, "memory"); memory == 0 || memory > 64<<20 {
		t.Errorf("after loading them, WebAssembly memory %d bytes; ADR 0023's budget is 64 MiB (%s)", memory, loaded)
	}
	for _, target := range []string{"/ui/fit?from.fitment.type=brake-rotor-front&from.fitment.number=BR-2001&stock=shelf&stock=yard" + pinned, "/ui/files"} {
		tb.stopWorkers()
		timing := tb.timeFetch(target).Timing
		if reads := timingDesc(timing, "reads"); reads > 20 {
			t.Errorf("%s after a start of the module read the catalog %d times; want only what a fit reads (%s)", target, reads, timing)
		}
	}
}

// writeStock writes a stock file to path: header, then n rows, row(i).
func writeStock(t *testing.T, path, header string, n int, row func(int) string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(header + "\n")
	for i := range n {
		b.WriteString(row(i) + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// timingDesc is metric name's desc in a Server-Timing header, 0 when it
// has none.
func timingDesc(timing, name string) int {
	m := regexp.MustCompile(`(?:^|, )` + regexp.QuoteMeta(name) + `;desc=(\d+)`).FindStringSubmatch(timing)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
