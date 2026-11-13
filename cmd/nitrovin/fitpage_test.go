package main

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// pinnedNow freezes a fit's clock, so the page's JSON and the API's agree.
const pinnedNow = "&now=2026-11-10T00:00:00Z"

// fitServer is serve on the embedded data with testdata/vocab, the
// synthetic catalog and its stock loaded beside it.
func fitServer(t *testing.T) http.Handler {
	t.Helper()
	fixtureData(t)
	return vocabServer(t, nil, fixture, fitmentFixture, stockFixture)
}

// pageFitJSON is the fit a page's JSON dialog holds, and the /v1 request it
// names as the same.
func pageFitJSON(t *testing.T, body string) (any, string) {
	t.Helper()
	m := regexp.MustCompile(`(?s)<a href="([^"]*)" hx-boost="false">The same request on /v1</a>.*?<pre tabindex="0" id="fit-json" data-copy="the fit&#39;s JSON">(.*?)</pre>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no fit JSON on the page\n%s", body)
	}
	var v any
	if err := json.Unmarshal([]byte(html.UnescapeString(m[2])), &v); err != nil {
		t.Fatal(err)
	}
	return v, html.UnescapeString(m[1])
}

// pageText is a page's main in words, its whitespace collapsed.
func pageText(body string) string {
	_, main, _ := strings.Cut(body, `<main id="ui-main"`)
	main, _, _ = strings.Cut(main, "</main>")
	return strings.Join(strings.Fields(html.UnescapeString(tag.ReplaceAllString(main, " "))), " ")
}

// apiBody is GET url's body, which must answer 200.
func apiBody(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d\n%s", url, resp.StatusCode, b)
	}
	return b
}

// TestFitPageServer: /ui/fit answers every one of the synthetic catalog's
// documented fit examples (docs/api/README.md's Fit and Stock) on the
// server, as a page whose JSON is /v1/fit's answer to the same query, which
// it links, and which says in words what the documentation says.
func TestFitPageServer(t *testing.T) {
	h := fitServer(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	says := map[string][]string{
		fitExamples[0].query: {"3 part types have fitments for this vehicle.", "Brake Rotor, Front brake-rotor-front 1 directly Engine engine 1 directly Door Mirror mirror 2 directly"},
		fitExamples[1].query: {"1 fitment may be the part, on the left side.", "mirror MR-1001, left",
			"2009–2012 w/o towing package Matched through vPIC model 1938, …–2012 everything checked",
			"2009–2012 4WD, w/ towing package Matched through vPIC model 1938, …–2012 everything checked"},
		fitExamples[2].query: {"BR-2001", "Reached through a redirect: the catalog lists brake-rotor-rear under brake-rotor-front for 2007–2013: rear rotors are listed with the front.", "Not checked: Series"},
		fitExamples[4].query: {"The number asked for is retired: brake-rotor-front BR-2001 replaced it (7.01 → 7.02).",
			"Dodge Ram 1500 2009–2012 yard:", "Chevrolet Silverado 1500 2007–2013 w/ heavy duty brakes yard:"},
		fitExamples[5].query: {"1 vehicle shares fitments with this one.", "Chevrolet Silverado 1500 2007–2013 1 Brake Rotor, Front 1"},
		fitExamples[8].query: {"BR-2001", "A-1 shelf-annex", "S-2 shelf-stock", "S-3 shelf-stock", "filed under the retired number brake-rotor-front BR-1999",
			"its numbers are under release 7.01, not the loaded catalog's", "Y-1 yard-stock", "Y-2 yard-stock", "Y-3 yard-stock", "by the catalog's vehicle, Chevrolet Silverado 1500, 2010"},
		fitExamples[9].query:  {"S-1 shelf-stock north by its number, mirror MR-1001, left", "On the shelf with no number, for this vehicle (1)", "S-4 shelf-stock north by part type mirror for Dodge Ram 1500, 2010"},
		fitExamples[10].query: {"A-1 shelf-annex", "Y-1 yard-stock", "Showing 1 of 2 applications. The stock lists page with the applications, so the next page may hold stock alone.", "Next page"},
		fitExamples[11].query: {"1 vehicle shares fitments with this one.", "Dodge Ram 1500 2009–2012", "2 vehicles in the yard"},
	}
	for _, ex := range fitExamples {
		page := get(t, srv.URL+"/ui/fit?"+ex.query+pinnedNow, http.StatusOK)
		got, v1 := pageFitJSON(t, page)
		if want := "/v1/fit?" + ex.query + pinnedNow; !sameQuery(t, v1, want) {
			t.Errorf("%s: links %s; want %s", ex.query, v1, want)
		}
		var want any
		if err := json.Unmarshal(apiBody(t, srv.URL+v1), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the page's JSON is not /v1/fit's\npage: %v\napi:  %v", ex.query, got, want)
		}
		words := pageText(page)
		if !strings.Contains(page, `id="fit-status"`) {
			t.Errorf("%s: no status sentence", ex.query)
		}
		for _, s := range says[ex.query] {
			if !strings.Contains(words, s) {
				t.Errorf("%s: lacks %q:\n%s", ex.query, s, words)
			}
		}
	}
	// From vPIC model 1938 and a year, the 4WD application is unconfirmed.
	if w := pageText(get(t, srv.URL+"/ui/fit?from.model=1938&model_year=2010&part.spine=part-types&part.id=mirror&side=left", http.StatusOK)); !strings.Contains(w, "4WD, w/ towing package Matched through vPIC model 1938, …–2012 Not checked: Drive Type") {
		t.Errorf("model 1938's left mirror: %s", w)
	}

	// Without a catalog, a fit is no-catalog, in words.
	bare := httptest.NewServer(vocabServer(t, nil, fixture))
	t.Cleanup(bare.Close)
	if w := pageText(get(t, bare.URL+"/ui/fit?"+fitExamples[5].query, http.StatusOK)); !strings.Contains(w, "No catalog is loaded here, so there is nothing to fit against.") {
		t.Errorf("no catalog: %s", w)
	}
}

// sameQuery reports whether a and b are one path with one query, in any
// order.
func sameQuery(t *testing.T, a, b string) bool {
	t.Helper()
	ua, err := url.Parse(a)
	if err != nil {
		t.Fatal(err)
	}
	ub, err := url.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return ua.Path == ub.Path && reflect.DeepEqual(ua.Query(), ub.Query())
}

// TestFitPageBrowser runs the fit page in headless Chrome or Chromium,
// each run in a fresh profile, without JavaScript and with it, on the
// synthetic catalog and its stock: from the empty page through an example
// to a vehicle's part types, a part type's candidates and their stock, a
// fitment's vehicles, the pages of a list, a refusal, and the header's
// way to every page. axe audits every state, in both runs, at a desktop
// width and a phone's.
func TestFitPageBrowser(t *testing.T) {
	path := chromePath()
	if path == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no Chrome or Chromium to run /ui/fit in: set NITROVIN_CHROME")
		}
		t.Skip("no Chrome or Chromium: set NITROVIN_CHROME to run /ui/fit in a browser")
	}
	log := &requestLog{h: fitServer(t)}
	srv := httptest.NewServer(log)
	t.Cleanup(srv.Close)
	t.Run("js-off", func(t *testing.T) { fitInBrowser(t, path, srv.URL, log, false) })
	t.Run("js-on", func(t *testing.T) { fitInBrowser(t, path, srv.URL, log, true) })
}

// fitInBrowser is one run of the fit page: every step a page load without
// JavaScript, and with it a fragment htmx swaps into the main, the page
// never reloading, nothing refused by CSP and nothing thrown.
func fitInBrowser(t *testing.T, path, base string, log *requestLog, js bool) {
	tb := newTab(t, path, base, log, js)
	tb.do(chromedp.EmulateViewport(1280, 900))
	tb.do(chromedp.Navigate(base + "/ui/fit"))
	tb.wait("#fit-controls")
	log.take()
	tb.audit("the empty fit page")
	step := func(name, click, ready, at string) {
		t.Helper()
		tb.do(chromedp.Click(chromedp.CSS(click)))
		tb.wait(ready)
		if got := tb.at(); !sameQuery(t, got, at) {
			t.Errorf("%s: at %s; want %s", name, got, at)
		}
		tb.requests(name, js, at)
		tb.audit(name)
	}

	// An example, on the entry tab, which offers the entries alone: the
	// 2009 Ram's part types.
	step("the entry tab", `#fit-tab-entry`, `a.example`, "/ui/fit?tab=entry")
	ram := "/ui/fit?from.entry.make=Dodge&from.entry.model=Ram+1500&from.vocabulary=catalog-models&model_year=2009"
	step("an example", `a.example[href="`+ram+`"]`, `#result[data-kind="part-types"]`, ram)
	// Picking the mirror: its candidates, with stock.
	mirror := "/ui/fit?from.entry.make=Dodge&from.entry.model=Ram+1500&from.vocabulary=catalog-models&model_year=2009&part.id=mirror&part.spine=part-types&stock=shelf&stock=yard"
	step("the mirror's candidates", `#result tbody a[href*="part.id=mirror"]`, `#result[data-kind="candidates"]`, mirror)
	if n := tb.count("article.candidate"); n != 2 {
		t.Errorf("the mirror's candidates: %d; want 2", n)
	}
	// The JSON, a popover, opens and closes without a script.
	tb.do(chromedp.Click(chromedp.CSS("button.open-dialog")))
	tb.wait("#result-json:popover-open")
	tb.audit("the candidates' JSON")
	tb.do(chromedp.Click(chromedp.CSS("#result-json .dialog-head button")))
	tb.wait("#result-json:not(:popover-open)")
	// Its link to the same request on /v1 is the API's answer, a document
	// of its own, never swapped into the page by htmx.
	v1 := attributes(tb, "#result-json a")
	if len(v1) != 1 || !sameQuery(t, v1[0]["href"], strings.Replace(mirror, "/ui/fit?", "/v1/fit?", 1)) {
		t.Fatalf("the JSON's /v1 link: %v", v1)
	}
	tb.do(chromedp.Click(chromedp.CSS("button.open-dialog")))
	tb.wait("#result-json:popover-open")
	// The click navigates: wait for the new document's load as one step
	// with it (RunResponse). WaitReady("body") after the click matches the
	// old document's body before navigation commits, so the next query can
	// read the old URL or land while the page detaches ("Not attached to an
	// active page").
	tb.call(func(ctx context.Context) error {
		_, err := chromedp.RunResponse(ctx, chromedp.Click(chromedp.CSS("#result-json a")))
		return err
	})
	if at := tb.at(); !sameQuery(t, at, v1[0]["href"]) || eval[string](tb, `document.contentType`) != "application/json" {
		t.Errorf("the /v1 link: at %s, %s; want %s, application/json", at, eval[string](tb, `document.contentType`), v1[0]["href"])
	}
	tb.do(chromedp.Navigate(base + mirror))
	tb.wait(`#result[data-kind="candidates"]`)
	log.take()
	// A phone's width.
	tb.do(chromedp.EmulateViewport(390, 844))
	tb.audit("the candidates at a phone's width")
	tb.do(chromedp.EmulateViewport(1280, 900))
	// A number: its vehicles, and its stock.
	vehicles := "/ui/fit?from.fitment.number=MR-1001&from.fitment.side=left&from.fitment.type=mirror&stock=shelf&stock=yard"
	step("MR-1001's vehicles", `article.candidate h3 a`, `#result[data-kind="vehicles"]`, vehicles)
	// A list a page at a time.
	tb.do(chromedp.Navigate(base + "/ui/fit?from.fitment.type=brake-rotor-front&from.fitment.number=BR-1999&stock=shelf&stock=yard&limit=1"))
	tb.wait(`#result[data-kind="vehicles"]`)
	log.take()
	next := attributes(tb, `a[rel="next"]`)
	if len(next) != 1 {
		t.Fatalf("BR-1999 a page at a time: no next page")
	}
	step("the next page", `a[rel="next"]`, `p.pages a:not([rel])`, next[0]["href"])
	// A refusal: the form, its refusal beside the side.
	tb.do(chromedp.Navigate(base + "/ui/fit?tab=vin"))
	tb.wait("#fit-vin")
	log.take()
	tb.do(chromedp.SetValue(chromedp.CSS("#fit-vin"), "1D7RV1GT5AS123456"), chromedp.SetValue(chromedp.CSS("#fit-model-year"), "2010.5"),
		chromedp.Click(chromedp.CSS(`#fit-controls form button[type=submit]`)))
	tb.wait("#fit-model-year-error")
	// The form sent side empty: the page moved to the query without it,
	// /v1/fit's.
	if at, want := tb.at(), "/ui/fit?from.vin=1D7RV1GT5AS123456&model_year=2010.5"; !sameQuery(t, at, want) {
		t.Errorf("a form's empty field: at %s; want %s", at, want)
	}
	tb.audit("a refusal")
	log.take()

	// The header leads to every page: translate, fit, the Vocabularies
	// dialog, and the API's documentation; its links are page loads.
	tb.do(chromedp.Click(chromedp.CSS(`nav.site a[href="/"]`)))
	tb.wait("#controls")
	if got := tb.at(); got != "/" {
		t.Errorf("the header's Translate: at %s", got)
	}
	tb.do(chromedp.Click(chromedp.CSS(`nav.site a[href="/ui/fit"]`)))
	tb.wait("#fit-controls")
	if got := tb.at(); got != "/ui/fit" {
		t.Errorf("the header's Fit: at %s", got)
	}
	tb.requests("the header", false, "/", "/ui/fit")
	if current := attributes(tb, `nav.site [aria-current="page"]`); len(current) != 1 || current[0]["href"] != "/ui/fit" {
		t.Errorf("the header marks %v; want /ui/fit", current)
	}
	tb.do(chromedp.Click(chromedp.CSS("nav.site button.open-vocabularies")))
	tb.wait("#vocabularies:popover-open")
	tb.audit("the Vocabularies dialog on the fit page")
	if docs := attributes(tb, `nav.site a[href$="/docs/api/README.md"]`); len(docs) != 1 {
		t.Errorf("the header: no API docs")
	}

	if js {
		if refused := eval[[]string](tb, `refused`); len(refused) > 0 {
			t.Errorf("CSP refused %q", refused)
		}
		tb.mu.Lock()
		if len(tb.thrown) > 0 {
			t.Errorf("thrown: %q", tb.thrown)
		}
		tb.mu.Unlock()
	}
}

// TestFitPageUnavailableStock: serve with the connectors' headers and no
// DSN: each stock list's sources are unavailable, and the page says its
// stock is unknown, with why, never that it is empty.
func TestFitPageUnavailableStock(t *testing.T) {
	fixtureData(t)
	t.Setenv("NITROVIN_STOCK_DSN", "")
	os.Unsetenv("NITROVIN_STOCK_DSN")
	s := startServe(t, nil, "--vocab", fixture, "--vocab", fitmentFixture, "--vocab", liveFixture, "--addr", "127.0.0.1:0")
	defer s.stop(t)
	words := pageText(get(t, "http://127.0.0.1:"+s.port+"/ui/fit?from.fitment.type=brake-rotor-front&from.fitment.number=BR-2001&stock=shelf&stock=yard", http.StatusOK))
	for _, want := range []string{
		"On the shelf (0 known) Unknown: a source is unavailable (below).",
		"In the yard (0 known) Unknown: a source is unavailable (below).",
		"shelf-stock, read live from stock.shelf_stock: unavailable, so its stock is unknown, not empty (environment variable NITROVIN_STOCK_DSN, which holds the DSN, is not set).",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("lacks %q:\n%s", want, words)
		}
	}
	if strings.Contains(words, "None.") {
		t.Errorf("an unknown list says None:\n%s", words)
	}
}
