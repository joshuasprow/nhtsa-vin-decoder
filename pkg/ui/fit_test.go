package ui

import (
	"bytes"
	"context"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// ram2010 is the synthetic catalog's VIN: a 2010 Dodge Ram, 4WD, as
// fitBackend decodes it.
const ram2010 = "1D7RV1GT5AS123456"

// fitBackend is backend answering translate, list and fit from
// testdata/vocab, the synthetic catalog (testdata/vocab/fitment) and its
// stock (testdata/vocab/stock), over fixtureMonth, as the server does
// through contract.Fitter. Its decoder knows ram2010 alone.
type fitBackend struct {
	backend
	f contract.Fitter
}

func (b fitBackend) Translate(req contract.TranslateRequest) (contract.Translation, error) {
	return b.f.Translate(req)
}
func (b fitBackend) List(req contract.ListRequest) (contract.List, error) { return b.f.List(req) }
func (b fitBackend) Fit(ctx context.Context, req contract.FitRequest) (contract.Fit, error) {
	return b.f.Fit(ctx, req)
}

// fitElements are the elements the synthetic catalog's qualifiers name.
var fitElements = contract.Elements{
	{ElementID: 15, Variable: "Drive Type", Code: "DriveType", DataType: "lookup"},
	{ElementID: 34, Variable: "Series", Code: "Series", DataType: "string"},
}

// fitMonth is fixtureMonth with the model year a value, as vPIC's is,
// so a decode gives its year.
type fitMonth struct{ fixtureMonth }

func (m fitMonth) Element(id int32) (bool, bool) {
	lookup, ok := m.fixtureMonth.Element(id)
	return lookup && id != 29, ok
}

// newFitBackend loads the fixtures dirs, under testdata/vocab, beside
// fixtureMonth.
func newFitBackend(t *testing.T, dirs ...string) fitBackend {
	t.Helper()
	var bundle bytes.Buffer
	if err := vocab.WriteBundle(&bundle, fitMonth{}); err != nil {
		t.Fatal(err)
	}
	month, err := vocab.ReadBundle(&bundle)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, d := range dirs {
		paths = append(paths, filepath.Join("..", "..", "testdata", "vocab", d))
	}
	files, err := vocab.Open(paths...)
	if err != nil {
		t.Fatal(err)
	}
	set, err := vocab.NewSet(month, files)
	if err != nil {
		t.Fatal(err)
	}
	tr := contract.Translator{Set: set, DataMonth: "2026_10", DecoderVersion: testInfo.DecoderVersion,
		Decode: func(vin string, _ contract.Null[int32], _ time.Time) (contract.VINDecode, error) {
			d := contract.VINDecode{Projection: contract.Projection{VIN: vin}, DataMonth: "2026_10", DecoderVersion: testInfo.DecoderVersion}
			if vin == ram2010 {
				for _, it := range [][2]string{{"26", "476"}, {"28", "1938"}, {"29", "2010"}, {"15", "2"}, {"34", "DS (1500)"}} {
					id := map[string]int32{"26": 26, "28": 28, "29": 29, "15": 15, "34": 34}[it[0]]
					d.Items = append(d.Items, contract.Item{ElementID: id, Value: contract.Value(it[1]), AttributeID: contract.Value(it[1])})
				}
			}
			return d, nil
		}}
	f := contract.Fitter{Translator: tr}
	if c := set.Catalog(); c != nil {
		f.Catalog = c
	}
	info := testInfo
	info.Elements = fitElements
	info.Vocabularies = nil
	for _, file := range files {
		v := contract.VocabularyFile{Name: file.Name, Kind: string(file.Kind), Version: file.Version}
		if file.Kind == vocab.KindStock {
			f.Stock = append(f.Stock, stock.NewFile(file, tr.StockDecode(time.Now())))
			v.StockCounts = &contract.StockCounts{Source: string(file.Holds)}
		}
		info.Vocabularies = append(info.Vocabularies, v)
	}
	return fitBackend{backend: backend{info: info}, f: f}
}

// fitHandler is the server's handler over the synthetic catalog and its
// stock.
func fitHandler(t *testing.T) *Handler {
	t.Helper()
	return New(newFitBackend(t, ".", "fitment", "stock"), log.New(io.Discard, "", 0))
}

// getFit is GET target from h as a page: its body, checked to be a 200
// page with its title.
func getFit(t *testing.T, h *Handler, target string, status int) string {
	t.Helper()
	got, hdr, body := response(t, h, "GET", target, nil)
	if got != status {
		t.Fatalf("GET %s: %d; want %d\n%s", target, got, status, body)
	}
	checkHTML(t, "GET "+target, hdr)
	return body
}

var (
	tagRE   = regexp.MustCompile(`<[^>]*>`)
	spaceRE = regexp.MustCompile(`\s+`)
)

// plain is markup's text, its whitespace collapsed.
func plain(markup string) string {
	return strings.TrimSpace(spaceRE.ReplaceAllString(html.UnescapeString(tagRE.ReplaceAllString(markup, " ")), " "))
}

// section is the markup between the first start and the end after it.
func section(t *testing.T, body, start, end string) string {
	t.Helper()
	_, after, ok := strings.Cut(body, start)
	if !ok {
		t.Fatalf("no %s in\n%s", start, body)
	}
	in, _, ok := strings.Cut(after, end)
	if !ok {
		t.Fatalf("no %s after %s in\n%s", end, start, body)
	}
	return in
}

// statusOf is the answer's status sentence.
func statusOf(t *testing.T, body string) string {
	t.Helper()
	_, sentence, _ := strings.Cut(section(t, body, `id="fit-status"`, "</p>"), ">")
	return plain(sentence)
}

// hrefs are the links in markup, unescaped, in order.
func hrefs(markup string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`href="([^"]*)"`).FindAllStringSubmatch(markup, -1) {
		out = append(out, html.UnescapeString(m[1]))
	}
	return out
}

// query is u's query, which the test fails on when u has none to parse.
func query(t *testing.T, u string) url.Values {
	t.Helper()
	parsed, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}

// TestFitPageEmpty: before anything is asked, the page has its tabs and
// form, each tab a link with its own form, and examples from the
// catalog's first fitment, each a fit with an answer; without a catalog
// it says so, as a sentence, and offers none.
func TestFitPageEmpty(t *testing.T) {
	h := fitHandler(t)
	body := getFit(t, h, "/ui/fit", 200)
	checkPage(t, "GET /ui/fit", body, "Fit parts")
	for _, want := range []string{
		`<a id="fit-tab-vin" href="/ui/fit" aria-current="page">VIN</a>`,
		`<a id="fit-tab-entry" href="/ui/fit?tab=entry">Year, make and model</a>`,
		`<a id="fit-tab-id" href="/ui/fit?tab=id">vPIC id</a>`,
		`<a id="fit-tab-fitment" href="/ui/fit?tab=fitment">Fitment</a>`,
		`<form method="get" action="/ui/fit" hx-get="/ui/fit" hx-target="#ui-main" hx-swap="innerHTML show:window:top" hx-push-url="true" aria-labelledby="fit-tab-vin">`,
		`<input type="text" id="fit-vin" name="from.vin" value=""`,
		`<label><input type="checkbox" name="to" value="catalog-models"> catalog-models</label>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /ui/fit: lacks %s\n%s", want, body)
		}
	}
	examples := hrefs(section(t, body, `<ul class="examples"`, "</ul>"))
	want := []string{
		"/ui/fit?from.entry.make=Dodge&from.entry.model=Ram+1500&from.vocabulary=catalog-models&model_year=2009",
		"/ui/fit?from.fitment.number=BR-2001&from.fitment.type=brake-rotor-front&stock=shelf&stock=yard",
	}
	if strings.Join(examples, "\n") != strings.Join(want, "\n") {
		t.Errorf("examples %q; want %q", examples, want)
	}
	for _, e := range examples {
		if b := getFit(t, h, e, 200); statusOf(t, b) == "" || !strings.Contains(b, `data-status="found"`) {
			t.Errorf("example %s: no answer found\n%s", e, b)
		}
	}

	// A tab's own form: the entry tab's fields are its vocabulary's, and
	// the fitment tab asks for every kind of stock loaded.
	body = getFit(t, h, "/ui/fit?tab=entry", 200)
	for _, want := range []string{`<select id="fit-vocabulary" name="from.vocabulary"`, `<option value="catalog-models" selected>`, `name="from.entry.make"`, `name="from.entry.model"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the entry tab lacks %s\n%s", want, body)
		}
	}
	body = getFit(t, h, "/ui/fit?tab=fitment", 200)
	for _, want := range []string{`name="from.fitment.type"`, `name="from.fitment.number"`, `<select id="fit-fitment-side" name="from.fitment.side"`, `<input type="hidden" name="stock" value="shelf">`, `<input type="hidden" name="stock" value="yard">`} {
		if !strings.Contains(body, want) {
			t.Errorf("the fitment tab lacks %s\n%s", want, body)
		}
	}

	// No catalog: said before anything is asked, and no example.
	none := New(backend{info: testInfo}, log.New(io.Discard, "", 0))
	body = getFit(t, none, "/ui/fit", 200)
	if got := statusOf(t, body); got != noCatalogText || strings.Contains(body, `class="examples"`) {
		t.Errorf("no catalog: status %q, examples %v\n%s", got, strings.Contains(body, `class="examples"`), body)
	}
}

// TestFitPageKinds: each kind of answer on the synthetic catalog's
// documented examples (docs/api/README.md's Fit): part types with counts,
// each a link to its candidates; candidates with via and unconfirmed,
// each number a link to its vehicles; a fitment's vehicles through a
// retired number; related vehicles; and stock lists when stock is
// loaded. Every link a page draws is /v1/fit's query, and answers.
func TestFitPageKinds(t *testing.T) {
	h := fitHandler(t)

	// The 2010 Ram's part types.
	ram := "/ui/fit?from.vocabulary=catalog-models&from.entry.make=Dodge&from.entry.model=Ram+1500&model_year=2010"
	body := getFit(t, h, ram, 200)
	checkPage(t, "part types", body, "Part types: Dodge Ram 1500, 2010")
	if got := statusOf(t, body); got != "3 part types have fitments for this vehicle." {
		t.Errorf("part types: status %q", got)
	}
	table := section(t, body, `<table class="fit">`, "</table>")
	if got := plain(section(t, table, "<tbody>", "</tbody>")); got != "Brake Rotor, Front brake-rotor-front 1 directly Engine engine 1 directly Door Mirror mirror 2 directly" {
		t.Errorf("part types: %q", got)
	}
	links := hrefs(table)
	if len(links) != 3 {
		t.Fatalf("part types' links: %q", links)
	}
	mirror := links[2]
	if q := query(t, mirror); q.Get("part.spine") != "part-types" || q.Get("part.id") != "mirror" || strings.Join(q["stock"], ",") != "shelf,yard" ||
		q.Get("from.vocabulary") != "catalog-models" || q.Get("model_year") != "2010" {
		t.Errorf("the mirror's link %s", mirror)
	}
	if !strings.Contains(body, `<a href="/ui/fit?from.entry.make=Dodge&amp;from.entry.model=Ram&#43;1500&amp;from.vocabulary=catalog-models&amp;model_year=2010&amp;related=true&amp;stock=yard">Related vehicles`) {
		t.Errorf("part types: no link to related vehicles\n%s", body)
	}

	// Picking the mirror: its candidates, each application's via, the
	// 4WD one's qualifier unconfirmed without a decode, and stock.
	body = getFit(t, h, mirror, 200)
	if got := statusOf(t, body); got != "2 fitments may be the part." {
		t.Errorf("mirrors: status %q", got)
	}
	first := section(t, body, `<article class="candidate"`, "</article>")
	for _, want := range []string{
		"mirror MR-1001, left : its vehicles",
		"Dodge Ram 1500 2009–2012 w/o towing package the vehicle itself everything checked",
		"Dodge Ram 1500 2009–2012 4WD, w/ towing package the vehicle itself Not checked: Drive Type",
		"On the shelf (1)", "S-1 shelf-stock north by its number, mirror MR-1001, left",
		"shelf-stock, a file, as of 2026-11-01T08:00:00Z: its numbers are under release 7.01, not the loaded catalog's.",
		"In the yard (2)",
	} {
		if !strings.Contains(plain(first), want) {
			t.Errorf("the left mirror lacks %q:\n%s", want, plain(first))
		}
	}
	vehicles := hrefs(section(t, first, "<h3", "</h3>"))[0]
	if want := "/ui/fit?from.fitment.number=MR-1001&from.fitment.side=left&from.fitment.type=mirror&stock=shelf&stock=yard"; vehicles != want {
		t.Errorf("MR-1001's link %s; want %s", vehicles, want)
	}
	if by := plain(section(t, body, `<h3>On the shelf with no number, for this vehicle`, "</ul>")); !strings.Contains(by, "S-4") || !strings.Contains(by, "A-2") {
		t.Errorf("shelf by vehicle: %s", by)
	}

	// The VIN's left mirror: the decode settles the 4WD qualifier.
	body = getFit(t, h, "/ui/fit?from.vin="+ram2010+"&part.spine=part-types&part.id=mirror&side=left", 200)
	if got := statusOf(t, body); got != "1 fitment may be the part, on the left side." {
		t.Errorf("the VIN's left mirror: status %q", got)
	}
	if p := plain(body); !strings.Contains(p, "VIN "+ram2010+" decodes to vPIC make 476, model 1938, model year 2010.") ||
		strings.Contains(p, "Not checked:") || strings.Count(p, "Matched through vPIC model 1938, …–2012 everything checked") != 2 {
		t.Errorf("the VIN's left mirror:\n%s", p)
	}

	// The 2010 Silverado's rear rotor, through a redirect, its Series
	// unconfirmed.
	body = getFit(t, h, "/ui/fit?from.vocabulary=catalog-models&from.entry.make=Chevrolet&from.entry.model=Silverado+1500&model_year=2010&part.spine=part-types&part.id=brake-rotor-rear", 200)
	if p := plain(section(t, body, `<article class="candidate"`, "</article>")); !strings.Contains(p, "BR-2001") ||
		!strings.Contains(p, "Reached through a redirect: the catalog lists brake-rotor-rear under brake-rotor-front for 2007–2013: rear rotors are listed with the front.") ||
		!strings.Contains(p, "Not checked: Series") {
		t.Errorf("the Silverado's rear rotor:\n%s", p)
	}

	// The retired BR-1999: BR-2001's vehicles, named in yard.
	body = getFit(t, h, "/ui/fit?from.fitment.type=brake-rotor-front&from.fitment.number=BR-1999&to=yard", 200)
	checkPage(t, "retired", body, "Vehicles of brake-rotor-front BR-1999")
	if got := statusOf(t, body); got != "The fitment fits 2 applications." {
		t.Errorf("BR-1999: status %q", got)
	}
	if p := plain(body); !strings.Contains(p, "The number asked for is retired: brake-rotor-front BR-2001 replaced it (7.01 → 7.02).") ||
		!strings.Contains(p, "Dodge Ram 1500 2009–2012 yard: Dodge Other; Dodge Ram 1500; Dodge Ram 1500 4x4") || !strings.Contains(p, "Chevrolet Silverado 1500 2007–2013 w/ heavy duty brakes yard: Chevy Silverado") {
		t.Errorf("BR-1999:\n%s", p)
	}

	// The Ram's related vehicles: the Silverado, sharing a front rotor.
	body = getFit(t, h, "/ui/fit?from.vin="+ram2010+"&related=true", 200)
	if got := statusOf(t, body); got != "1 vehicle shares fitments with this one." {
		t.Errorf("related: status %q", got)
	}
	if p := plain(section(t, body, "<tbody>", "</tbody>")); p != "Chevrolet Silverado 1500 2007–2013 1 Brake Rotor, Front 1 vPIC model 1850 Silverado; vPIC make 467 Chevrolet" {
		t.Errorf("related: %q", p)
	}

	// Every link the pages drew answers.
	for _, target := range []string{ram, mirror, vehicles} {
		for _, l := range hrefs(getFit(t, h, target, 200)) {
			if strings.HasPrefix(l, "/ui/fit?") {
				getFit(t, h, l, 200)
			}
		}
	}
}

// TestFitPagePaging: every list pages with the API's cursor: next leads
// on, page by page, to the last, which has no next, and every page but
// the first leads back to it.
func TestFitPagePaging(t *testing.T) {
	h := fitHandler(t)
	target := "/ui/fit?from.fitment.type=brake-rotor-front&from.fitment.number=BR-1999&stock=shelf&stock=yard&limit=1"
	var shelf, yard []string
	for page := 1; ; page++ {
		body := getFit(t, h, target, 200)
		stocks := strings.Split(body, `<div class="stock">`)[1:]
		if len(stocks) != 2 {
			t.Fatalf("page %d: %d stock lists", page, len(stocks))
		}
		for i, s := range stocks {
			for _, m := range regexp.MustCompile(`<td class="mono nowrap">([^<]*)</td>`).FindAllStringSubmatch(s, -1) {
				if i == 0 {
					shelf = append(shelf, m[1])
				} else {
					yard = append(yard, m[1])
				}
			}
		}
		pages := section(t, body, `<p class="pages">`, "</p>")
		if got, want := plain(pages), map[int]string{
			1: "Showing 1 of 2 applications. The stock lists page with the applications, so the next page may hold stock alone. Next page",
			2: "Showing 1 of 2 applications. The stock lists page with the applications, so the next page may hold stock alone. First page Next page",
			3: "Every one of the 2 applications is on an earlier page: this page goes on with the stock lists. First page",
		}[page]; got != want {
			t.Errorf("page %d says %q; want %q", page, got, want)
		}
		if (page > 1) != strings.Contains(pages, "First page") {
			t.Errorf("page %d: first page link: %s", page, pages)
		}
		m := regexp.MustCompile(`href="([^"]*)" rel="next"`).FindStringSubmatch(pages)
		if m == nil {
			if page != 3 {
				t.Errorf("the last page is %d; want 3", page)
			}
			break
		}
		if page == 5 {
			t.Fatal("no last page")
		}
		target = html.UnescapeString(m[1])
		if q := query(t, target); q.Get("after") == "" || q.Get("limit") != "1" || q.Get("from.fitment.number") != "BR-1999" {
			t.Errorf("page %d: next %s", page, target)
		}
	}
	if strings.Join(shelf, " ") != "A-1 S-2 S-3" || strings.Join(yard, " ") != "Y-1 Y-2 Y-3" {
		t.Errorf("paged: shelf %q, yard %q", shelf, yard)
	}

	// The part types page too: one at a time, three pages.
	body := getFit(t, h, "/ui/fit?from.vin="+ram2010+"&limit=1", 200)
	if p := plain(section(t, body, `<p class="pages">`, "</p>")); p != "Showing 1 of 3 part types. Next page" {
		t.Errorf("part types, a page of one: %q", p)
	}
}

// TestFitPageStatuses: every status reads as a sentence, no-catalog
// included, with the page's status 200: nothing to fit is never an error.
func TestFitPageStatuses(t *testing.T) {
	h := fitHandler(t)
	for target, want := range map[string]string{
		"/ui/fit?from.model=99999&model_year=2010":                                           "Nothing loaded here knows the vehicle asked for: a VIN that decodes to no vPIC make or model, or an entry or spine entry its vocabulary or spine lacks.",
		"/ui/fit?from.model=1938&model_year=2010&part.spine=part-types&part.id=no-such-type": "Nothing loaded here knows the vehicle or the part type asked for: a VIN that decodes to no vPIC make or model, or an entry or spine entry its vocabulary or spine lacks.",
		"/ui/fit?from.fitment.type=mirror&from.fitment.number=MR-0000":                       "parts-catalog has no fitment and no retired number mirror MR-0000.",
		"/ui/fit?from.model=13620&model_year=2010":                                           "vPIC model 13620, 2010 reaches none of parts-catalog's vehicles.",
		"/ui/fit?from.model=1850&model_year=2010&part.spine=part-types&part.id=mirror":       "parts-catalog has this vehicle, but no fitment of mirror fits it.",
		"/ui/fit?from.model=1850&model_year=2015&side=left":                                  "parts-catalog has this vehicle, but no fitment of any part type fits it, on the left side.",
		"/ui/fit?from.model=13620&model_year=2013&related=true":                              "No other vehicle shares a fitment with this one.",
		"/ui/fit?from.fitment.type=engine&from.fitment.number=EN-57&model_year=2015":         "None of the fitment's applications holds for model year 2015.",
	} {
		if got := statusOf(t, getFit(t, h, target, 200)); got != want {
			t.Errorf("%s: %q; want %q", target, got, want)
		}
	}
	none := New(backend{info: testInfo}, log.New(io.Discard, "", 0))
	for _, target := range []string{"/ui/fit?from.vin=" + ram2010, "/ui/fit?from.fitment.type=mirror&from.fitment.number=MR-1001"} {
		body := getFit(t, none, target, 200)
		if got := statusOf(t, body); got != noCatalogText || strings.Contains(body, `<p class="pages">`) {
			t.Errorf("%s without a catalog: %q\n%s", target, got, body)
		}
	}
}

// TestFitPageRefusals: a malformed fit is the page still, with its
// status, its refusal beside the field holding the value, or above the
// form; a link naming a file this server has not loaded says where it
// lives; and with htmx a refusal is sent to the page's main.
func TestFitPageRefusals(t *testing.T) {
	h := fitHandler(t)
	body := getFit(t, h, "/ui/fit?from.vin="+ram2010+"&side=up", 400)
	if !strings.Contains(body, `<select id="fit-side" name="side" aria-invalid="true" aria-describedby="fit-side-error">`) || !strings.Contains(body, `<p class="error" id="fit-side-error">`) {
		t.Errorf("a bad side: not beside its field\n%s", body)
	}
	body = getFit(t, h, "/ui/fit?from.vin=", 400)
	if !strings.Contains(body, `<input type="text" id="fit-vin" name="from.vin" value="" autocomplete="off" spellcheck="false" aria-invalid="true" aria-describedby="fit-vin-error">`) {
		t.Errorf("an empty VIN: not beside its field\n%s", body)
	}
	body = getFit(t, h, "/ui/fit?from.fitment.type=mirror&from.fitment.number=MR-1001&related=true", 400)
	if p := plain(section(t, body, `<p class="error" role="alert">`, "</p>")); !strings.Contains(p, "related") {
		t.Errorf("related with a fitment: %q", p)
	}
	body = getFit(t, h, "/ui/fit?from.vocabulary=mine&from.entry.make=Dodge", 404)
	if !strings.Contains(body, "Nothing named &#34;mine&#34; is loaded on this nitrovin. A file loaded into a browser stays in that browser: open this link in the browser that holds it.") {
		t.Errorf("a browser's vocabulary on the server:\n%s", body)
	}
	status, hdr, body := response(t, h, "GET", "/ui/fit?from.vin="+ram2010+"&side=up", htmx)
	if status != 400 || hdr.Get("HX-Retarget") != mainTarget || strings.Contains(body, "<header>") || !strings.Contains(body, `id="fit-side-error"`) {
		t.Errorf("a refusal's fragment: %d, HX-Retarget %q\n%s", status, hdr.Get("HX-Retarget"), body)
	}
}

// TestNavigation: one header leads to every page from every page, the
// server's and the Service Worker's alike: translate, fit, the
// Vocabularies dialog and the API's documentation, an error page's to
// all but the dialog; it marks the page shown, and a fragment never
// carries it.
func TestNavigation(t *testing.T) {
	onServer := fitHandler(t)
	browser := NewInBrowser(open(t, server(t, "yard"), newDisk()), BrowserConfig{}, log.New(io.Discard, "", 0))
	for _, tc := range []struct {
		h       *Handler
		target  string
		status  int
		current string
	}{
		{onServer, "/", 200, "/"},
		{onServer, "/ui/fit", 200, "/ui/fit"},
		{onServer, "/ui/fit?from.vin=" + ram2010, 200, "/ui/fit"},
		{onServer, "/ui/vocabularies/vpic", 200, ""},
		{onServer, "/ui/no-such-page", 404, ""},
		{browser, "/", 200, "/"},
		{browser, "/ui/fit?from.vin=" + ram2010, 200, "/ui/fit"},
		{browser, "/ui/vocabularies/vpic", 200, ""},
		{browser, "/ui/no-such-page", 404, ""},
	} {
		status, _, body := response(t, tc.h, "GET", tc.target, nil)
		if status != tc.status {
			t.Errorf("%s: %d; want %d", tc.target, status, tc.status)
		}
		nav := section(t, body, `<nav class="site" aria-label="Pages">`, "</nav>")
		if got, want := hrefs(nav), []string{"/", "/ui/fit", "https://github.com/joshuasprow/nitrovin/blob/main/docs/api/README.md"}; strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: the header's links %q; want %q", tc.target, got, want)
		}
		// Every page but an error's has the Vocabularies dialog behind the
		// header's button: an error page's would read the drift an
		// unknown path must never cost.
		button := strings.Contains(nav, `<button type="button" class="open-vocabularies" popovertarget="vocabularies">Vocabularies</button>`)
		if dialog := strings.Contains(body, `<dialog id="vocabularies" popover`); button != (tc.status == 200) || dialog != button {
			t.Errorf("%s: Vocabularies button %v, dialog %v\n%s", tc.target, button, dialog, body)
		}
		current := regexp.MustCompile(`<a href="([^"]*)" aria-current="page">`).FindAllStringSubmatch(nav, -1)
		if tc.current == "" && len(current) != 0 || tc.current != "" && (len(current) != 1 || current[0][1] != tc.current) {
			t.Errorf("%s: current %q; want %q", tc.target, current, tc.current)
		}
		if _, _, frag := response(t, tc.h, "GET", tc.target, htmx); strings.Contains(frag, `class="site"`) {
			t.Errorf("%s: a fragment carries the header", tc.target)
		}
	}
	// A release's tag leads to its own documentation.
	b := newFitBackend(t, ".")
	b.info.DecoderVersion = "v0.3.5"
	_, _, body := response(t, New(b, log.New(io.Discard, "", 0)), "GET", "/ui/fit", nil)
	if !strings.Contains(body, `<a href="https://github.com/joshuasprow/nitrovin/blob/v0.3.5/docs/api/README.md">API docs</a>`) {
		t.Errorf("a release's header: no docs at its tag\n%s", body)
	}
}

// TestBrowserFit: the Service Worker answers the fit page with the same
// handler, over the browser's Library, which holds no catalog: no-catalog,
// in words, with how to load the demo set, and no link to /v1, which is
// the server's.
func TestBrowserFit(t *testing.T) {
	l := open(t, server(t, "yard"), newDisk())
	h := NewInBrowser(l, BrowserConfig{}, log.New(io.Discard, "", 0))
	body := getFit(t, h, "/ui/fit?from.model=1938&model_year=2010", 200)
	if got := statusOf(t, body); got != noCatalogText+" "+demoLoadText {
		t.Errorf("the browser's fit: %q", got)
	}
	if strings.Contains(body, `href="/v1/fit`) {
		t.Errorf("the browser's fit links /v1\n%s", body)
	}
}

// TestFitPageCleanURL: a form's empty fields, which /v1/fit refuses, move
// to the same query without them (303), the page's tab along, so the
// page's URL is always /v1/fit's query; a query with none answers as it is.
func TestFitPageCleanURL(t *testing.T) {
	h := fitHandler(t)
	for target, want := range map[string]string{
		"/ui/fit?from.fitment.type=mirror&from.fitment.number=MR-1001&from.fitment.side=&model_year=&stock=shelf&stock=yard": "/ui/fit?from.fitment.number=MR-1001&from.fitment.type=mirror&stock=shelf&stock=yard",
		"/ui/fit?from.vin=" + ram2010 + "&model_year=&side=":                                                                 "/ui/fit?from.vin=" + ram2010,
		"/ui/fit?tab=entry&from.vocabulary=catalog-models&from.entry.make=&from.entry.model=":                                "/ui/fit?from.vocabulary=catalog-models&tab=entry",
	} {
		for _, header := range []http.Header{nil, htmx} {
			status, hdr, _ := response(t, h, "GET", target, header)
			if status != http.StatusSeeOther || hdr.Get("Location") != want {
				t.Errorf("%s (htmx %v): %d to %q; want 303 to %q", target, header != nil, status, hdr.Get("Location"), want)
			}
		}
		if _, err := contract.ParseFitQuery(query(t, want)); err != nil && !strings.Contains(want, "tab=") {
			t.Errorf("%s: not /v1/fit's query: %v", want, err)
		}
		getFit(t, h, want, 200)
	}
	getFit(t, h, "/ui/fit?from.vin=", 400) // an empty VIN is refused, not dropped
}

// TestFitPageUnknownStock: a stock list with an unavailable source says
// its stock is unknown, not empty, beside the rows the others gave.
func TestFitPageUnknownStock(t *testing.T) {
	down := stock.Down(stock.Info{Name: "shelf-live", Holds: stock.Shelf, Kind: stock.KindLive, Catalog: "parts-catalog", Connector: vocab.ConnectorPostgres, Relation: "nitrovin_shelf"}, "its database is unreachable")
	mirror := "/ui/fit?from.fitment.type=mirror&from.fitment.number=MR-1001&from.fitment.side=right&stock=shelf"

	// The files' shelf has no MR-1001 right; the live one is down.
	b := newFitBackend(t, ".", "fitment", "stock")
	b.f.Stock = append(b.f.Stock, down)
	body := getFit(t, New(b, log.New(io.Discard, "", 0)), mirror, 200)
	shelf := plain(section(t, body, `<div class="stock">`, "</ul>"))
	if want := "On the shelf (0 known) Unknown: a source is unavailable (below)."; !strings.HasPrefix(shelf, want) || strings.Contains(shelf, "None.") ||
		!strings.Contains(shelf, "shelf-live, read live from nitrovin_shelf: unavailable, so its stock is unknown, not empty (its database is unreachable).") {
		t.Errorf("a shelf with an unavailable source: %q", shelf)
	}

	// Beside a file's rows, the list may be longer than shown.
	body = getFit(t, New(b, log.New(io.Discard, "", 0)), "/ui/fit?from.fitment.type=mirror&from.fitment.number=MR-1001&from.fitment.side=left&stock=shelf", 200)
	if shelf := plain(section(t, body, `<div class="stock">`, "</ul>")); !strings.HasPrefix(shelf, "On the shelf (1 known) Not every source answered: the list may be longer than shown (below).") || !strings.Contains(shelf, "S-1") {
		t.Errorf("a shelf with rows and an unavailable source: %q", shelf)
	}

	// Every source answering: no word of unknown.
	body = getFit(t, fitHandler(t), mirror, 200)
	if shelf := plain(section(t, body, `<div class="stock">`, "</ul>")); !strings.HasPrefix(shelf, "On the shelf (0) None.") || strings.Contains(shelf, "nknown") {
		t.Errorf("a shelf every source answered: %q", shelf)
	}
}

// countedSource is a stock source that counts its lookups.
type countedSource struct {
	stock.Source
	lookups *atomic.Int32
}

func (c countedSource) ShelfByFitment(ctx context.Context, lists [][]vocab.FitmentKey, p stock.Page) ([]stock.Result, stock.Meta, error) {
	c.lookups.Add(1)
	return c.Source.ShelfByFitment(ctx, lists, p)
}
func (c countedSource) ShelfByVehicle(ctx context.Context, lists []stock.ShelfVehicle, p stock.Page) ([]stock.Result, stock.Meta, error) {
	c.lookups.Add(1)
	return c.Source.ShelfByVehicle(ctx, lists, p)
}
func (c countedSource) Yard(ctx context.Context, lists []stock.YardQuery, p stock.Page) ([]stock.Result, stock.Meta, error) {
	c.lookups.Add(1)
	return c.Source.Yard(ctx, lists, p)
}

// TestFitPageExamplesAskNoStock: drawing the empty page, its examples
// among it, asks no stock source, which may be slow or unreachable; the
// fitment example's link still asks for every kind loaded.
func TestFitPageExamplesAskNoStock(t *testing.T) {
	b := newFitBackend(t, ".", "fitment", "stock")
	var lookups atomic.Int32
	for i, src := range b.f.Stock {
		b.f.Stock[i] = countedSource{src, &lookups}
	}
	h := New(b, log.New(io.Discard, "", 0))
	body := getFit(t, h, "/ui/fit", 200)
	if n := lookups.Load(); n != 0 {
		t.Errorf("the empty page asked the stock sources %d times", n)
	}
	examples := hrefs(section(t, body, `<ul class="examples"`, "</ul>"))
	if len(examples) != 2 || strings.Join(query(t, examples[1])["stock"], ",") != "shelf,yard" {
		t.Fatalf("examples %q", examples)
	}
	getFit(t, h, examples[1], 200)
	if lookups.Load() == 0 {
		t.Errorf("the fitment example asked no stock")
	}
}
