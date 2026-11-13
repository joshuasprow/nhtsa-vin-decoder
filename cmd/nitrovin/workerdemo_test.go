package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// demoExample is one request of the demo set's list
// (pkg/ui/demo/demo-examples.json).
type demoExample struct {
	Stories []int  `json:"stories"`
	Label   string `json:"label"`
	Href    string `json:"href"`
}

func demoExamples(t *testing.T) []demoExample {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(demoDir, "demo-examples.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []demoExample
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// demoPageShows is what the page of the demo's request that shows a user
// story of #278 says, in words, so a visitor sees what the story promises.
var demoPageShows = map[int][]string{
	5:  {"Alternator", "Transfer Case", "3"},
	6:  {"DEMO-1001", "DEMO-1002", "DEMO-1003"},
	7:  {"DEMO-4301", "Not checked: Drive Type"},
	8:  {"3.5L V6 EcoBoost (VIN G, 8th digit)"},
	9:  {"DEMO-1101"},
	10: {"DEMO-5301, both", "DEMO-5302, left"},
	11: {"DEMO-2101", "redirect"},
	12: {"DEMO-1001", "DEMO-0901", "retired"},
	13: {"Toyota Camry", "Toyota RAV4"},
	14: {"Honda CR-V"},
	15: {"DEMO-4103", "DEMO-4111"},
	16: {"DEMO-S-01", "demo-north"},
	17: {"DEMO-Y-07", "DEMO-Y-08"},
	18: {"DEMO-S-02", "DEMO-0901"},
	19: {"its numbers are under release DEMO-1, not the loaded catalog's"},
	20: {"demo-north"},
	21: {"Volkswagen Type 2", "Brake Rotor, Front"},
	22: {"Dodge Ram 1500", "13620"},
	23: {"Silverado C15 2WD"},
}

// TestWorkerDemo runs the demo set (#278) in a fresh profile against a
// server that loaded nothing, as nitrovin.app: without JavaScript the
// dialog says the demo needs it, and a link to one of its requests says
// how to load it. "Load the demo" loads it in one click, its catalog
// compiled in the browser; the fit page then offers its list of requests,
// and each, clicked, shows what its user stories promise, as the server
// draws it from the same files (`serve --vocab pkg/ui/demo`), at a
// phone's width too. It survives a reload, Remove the demo removes every
// file of it, and the only requests that ever carried a body to the
// server were the yard's VINs, in decode batches. It logs the module's
// retained memory with the demo loaded, against ADR 0023's 64 MiB.
func TestWorkerDemo(t *testing.T) {
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
	fixtureData(t)
	module := buildWorker(t)
	epaPath, epa := epaSample(t)
	module.EPA = epa
	wire := &wireLog{h: vocabServer(t, &module)}
	srv := httptest.NewServer(wire)
	t.Cleanup(srv.Close)
	examples := demoExamples(t)

	// Without JavaScript, the dialog says the demo needs it.
	off := newTab(t, path, srv.URL, &requestLog{}, false)
	off.do(chromedp.Navigate(srv.URL + "/"))
	if s := off.html("#browser-status"); !strings.Contains(s, "The demo set loads into this browser in one click, so it needs JavaScript too.") {
		t.Errorf("JS off, the dialog says: %s", s)
	}

	tb := newTab(t, path, srv.URL, &requestLog{}, true)
	tb.do(chromedp.Navigate(srv.URL + examples[0].Href))
	if s := text(tb.html("#fit-status")); !strings.Contains(s, "open Vocabularies and Load the demo") {
		t.Errorf("a demo link before the demo is loaded says %q", s)
	}
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()
	tb.do(chromedp.Click(chromedp.CSS("#load-demo")))
	tb.waitPrefix("#load-status", "Loaded the demo set in ")
	said := tb.html("#load-status")
	if !strings.Contains(said, "Compiled demo-catalog (") {
		t.Errorf("the demo's load said %q; want its catalog compiled here", said)
	}
	t.Logf("%s (%s ms)", text(said), eval[string](tb, `document.getElementById("browser").dataset.demoMs`))
	stored := func() []string {
		return eval[[]string](tb, `[...document.querySelectorAll("#browser-files tr[data-where=browser] th")].map(th => th.textContent.trim())`)
	}
	demoFiles := []string{"demo-catalog", "demo-classics", "demo-part-codes", "demo-part-types", "demo-shelf", "demo-vehicles", "demo-yard-stock"}
	if got := stored(); !slices.Equal(got, demoFiles) {
		t.Errorf("stored %q; want %q", got, demoFiles)
	}
	if n := tb.count("#remove-demo"); n != 1 {
		t.Error("the panel offers no Remove the demo once it is loaded")
	}
	// EPA's vocabulary beside it, for the translation into its names.
	tb.do(chromedp.Click(chromedp.CSS("#load-epa")))
	tb.waitPrefix("#load-status", "Loaded ")

	// The fit page offers the demo's list, and each request shows what
	// its stories promise, as the server draws it on the same files.
	same := vocabServer(t, nil, demoDir, epaPath)
	v1 := regexp.MustCompile(`<p class="hint"><a href="/v1/fit[^"]*"[^>]*>The same request on /v1</a></p>`)
	tb.closeVocabularies()
	tb.do(chromedp.Navigate(srv.URL + "/ui/fit"))
	tb.ticked()
	var hrefs, want []string
	for _, e := range examples {
		want = append(want, e.Href)
	}
	hrefs = eval[[]string](tb, `[...document.querySelectorAll("#fit-controls a.example")].map(a => a.getAttribute("href"))`)
	if !slices.Equal(hrefs, want) {
		t.Fatalf("the fit page's examples:\n%s\nwant the demo's list:\n%s", strings.Join(hrefs, "\n"), strings.Join(want, "\n"))
	}
	bare := eval[string](tb, `fetch("/ui/fit", {headers: {"HX-Request": "true"}}).then(r => r.text())`)
	if w := mainOf(t, same, srv.URL, "/ui/fit", true); bare != w {
		t.Errorf("the worker's fit page differs from the server's\nworker: %s\nserver: %s", bare, w)
	}
	covered := map[int]bool{}
	for i, e := range examples {
		tb.do(chromedp.Navigate(srv.URL + "/ui/fit"))
		tb.ticked()
		tb.do(chromedp.Click(chromedp.CSS(fmt.Sprintf("#fit-controls li:nth-child(%d) a.example", i+1))))
		tb.until(e.Label, `location.pathname + location.search === `+jsString(e.Href)+` && document.getElementById("result") !== null`)
		words := strings.Join(strings.Fields(html.UnescapeString(tag.ReplaceAllString(tb.html("#ui-main"), " "))), " ")
		if e.Href != examples[len(examples)-1].Href && !strings.Contains(words, "The demo set is loaded: every part number, location, stock row and field value in it is invented.") {
			t.Errorf("%s: the page does not say the demo's data is invented", e.Href)
		}
		for _, story := range e.Stories {
			covered[story] = true
			for _, w := range demoPageShows[story] {
				if !strings.Contains(words, w) {
					t.Errorf("story %d, %s: the page lacks %q: %s", story, e.Href, w, words)
				}
			}
		}
		target := e.Href + pinned
		got := eval[[]string](tb, `fetch(`+jsString(target)+`, {headers: {"HX-Request": "true"}}).then(async r => [String(r.status), await r.text()])`)
		if w := mainOf(t, same, srv.URL, target, true); got[0] != "200" || got[1] != v1.ReplaceAllString(w, "") {
			t.Errorf("%s: the worker's page differs from the server's\nworker: %s %s\nserver: %s", target, got[0], got[1], w)
		}
	}
	for story := range demoPageShows {
		if !covered[story] {
			t.Errorf("story %d: no example on the fit page shows it", story)
		}
	}

	// A phone's width: the candidates with their stock read there.
	tb.do(chromedp.Navigate(srv.URL + examples[10].Href))
	tb.wait(`#result[data-kind="candidates"]`)
	tb.do(chromedp.EmulateViewport(390, 844))
	if !eval[bool](tb, `document.documentElement.scrollWidth <= innerWidth`) {
		t.Error("at a phone's width the demo's candidates scroll sideways")
	}
	tb.audit("the demo's candidates at a phone's width")
	tb.do(chromedp.EmulateViewport(1280, 900))

	// What the module holds with the demo loaded.
	timing := tb.timeFetch(examples[8].Href + pinned).Timing
	memory, heap := timingDesc(timing, "memory"), timingDesc(timing, "heap")
	t.Logf("retained with the demo loaded: Go heap %.1f MiB, WebAssembly memory %.1f MiB", float64(heap)/(1<<20), float64(memory)/(1<<20))
	if memory == 0 || memory > 64<<20 {
		t.Errorf("WebAssembly memory %d bytes; ADR 0023's budget is 64 MiB (%s)", memory, timing)
	}

	// A demo file of another version (an older release's) makes the
	// offer a refresh, which removes the demo held and loads it again.
	shelf, err := os.ReadFile(filepath.Join(demoDir, "demo-shelf.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(t.TempDir(), "demo-shelf.jsonl")
	if err := os.WriteFile(older, []byte(strings.Replace(string(shelf), `"version":"2026.11-demo"`, `"version":"2026.10-demo"`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{older}))
	tb.waitPrefix("#load-status", "Loaded demo-shelf")
	if s := tb.html("#load-demo"); !strings.Contains(s, "Refresh the demo") {
		t.Errorf("with an older demo file held, the offer: %s", s)
	}
	tb.do(chromedp.Click(chromedp.CSS("#load-demo")))
	tb.waitPrefix("#load-status", "Loaded the demo set in ")
	if n := tb.count("#remove-demo"); n != 1 {
		t.Errorf("after the refresh the demo is not held: %s", tb.html("#browser-files"))
	}

	// A reload keeps it; Remove the demo removes every file of it, and
	// the offer is back.
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()
	if got := stored(); !slices.Equal(got, append(slices.Clone(demoFiles), "epa-vehicles")) {
		t.Errorf("after a reload: stored %q", got)
	}
	tb.do(chromedp.Click(chromedp.CSS("#remove-demo")))
	tb.wait("#load-demo")
	if got := stored(); !slices.Equal(got, []string{"epa-vehicles"}) {
		t.Errorf("after Remove the demo: stored %q", got)
	}

	// Nothing but the yard's VINs ever carried a body to the server.
	var vins []string
	for _, r := range wire.all() {
		if r.body == 0 {
			continue
		}
		var items []map[string]any
		if r.method != "POST" || r.target != "/v1/decode" || json.Unmarshal([]byte(r.data), &items) != nil {
			t.Errorf("the server got %s %s with a body: %s", r.method, r.target, r.data)
			continue
		}
		for _, it := range items {
			vin, _ := it["vin"].(string)
			if _, ok := demoVINs[vin]; !ok || len(it) != 2 || it["full"] != true {
				t.Errorf("POST /v1/decode carried %v; want only the yard's VINs", it)
			}
			vins = append(vins, vin)
		}
	}
	// Once for each load of the demo: the first, and the refresh.
	if len(vins) != 2*len(demoVINs) {
		t.Errorf("the server decoded %d of the yard's VINs; want each of its %d once a load, twice", len(vins), len(demoVINs))
	}
}
