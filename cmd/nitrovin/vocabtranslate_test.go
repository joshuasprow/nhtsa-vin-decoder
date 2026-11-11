package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"
)

// TestVocabTranslate runs /vocab's translate forms in headless Chrome or
// Chromium, each run in a fresh profile
// (https://github.com/joshuasprow/nitrovin/issues/194). Every example is a
// link to the translate page with its form filled in, and fills it:
// without JavaScript as a page, on a server that loaded the fixture and
// the EPA sample (cmd/nitrovin-epa/testdata), so each of its vocabularies
// has an example; with JavaScript by htmx, the worker drawing the forms
// over the files the browser holds. With JavaScript the targets ticked on
// /vocab are the visitor's selection, kept in IndexedDB beside the files,
// by spine: a file loaded is ticked, and the selection survives a second
// load, a removal (the removed file drops out), a reload and a cold start
// of the worker, while a translation's or a link's own to= replaces it on
// its spine alone, whether the worker answers it or the server. axe
// audits every state.
func TestVocabTranslate(t *testing.T) {
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
	epaPath := abs("../nitrovin-epa/testdata/vehicles.jsonl")

	off := httptest.NewServer(vocabServer(t, nil, fixture, epaPath))
	t.Cleanup(off.Close)
	examplesJSOff(t, path, off.URL)

	module := buildWorker(t)
	epa, err := os.ReadFile(epaPath)
	if err != nil {
		t.Fatal(err)
	}
	module.EPA = epa
	wire := &wireLog{h: vocabServer(t, &module, filepath.Join(fixture, "classics.jsonl"), filepath.Join(fixture, "parts.jsonl"), filepath.Join(fixture, "listing.jsonl"))}
	on := httptest.NewServer(wire)
	t.Cleanup(on.Close)
	selectionJSOn(t, path, on.URL, wire)
}

// The examples that need no file.
const (
	vinExample   = "/ui/translate?from.vin=" + vin
	modelExample = "/ui/translate?from.model=1861&model_year=2003"
)

// examplesJSOff: without JavaScript /vocab has an example for each of the
// server's vocabularies, its first mapped entry to another vocabulary on
// its spine, and every example is a page with its form filled in. On the
// translate page the examples go to the targets its URL names.
func examplesJSOff(t *testing.T, path, base string) {
	tb := newTab(t, path, base, &requestLog{}, false)
	tb.do(chromedp.Navigate(base + "/vocab"))
	tb.wait("#vocab-translate")
	hrefs := exampleHrefs(tb, "#vocab-translate")
	if want := []string{
		vinExample,
		modelExample,
		"/ui/translate?from.entry.make=Acura&from.entry.model=2.2CL%2F3.0CL&from.vocabulary=epa-vehicles&model_year=1997&to=yard",
		"/ui/translate?from.entry.category=Brake+Pads&from.vocabulary=listing&to=listing",
		"/ui/translate?from.entry.make=Chevrolet&from.entry.model=Corvair&from.vocabulary=yard&to=epa-vehicles",
	}; !slices.Equal(hrefs, want) {
		t.Errorf("JS off: examples\n%q\nwant\n%q", hrefs, want)
	}
	tb.audit("/vocab's translate forms, JS off")
	for i, href := range hrefs {
		tb.do(chromedp.Navigate(base + "/vocab"))
		tb.wait("#vocab-translate")
		tb.do(chromedp.Click(chromedp.CSS(fmt.Sprintf("#vocab-translate ul.examples li:nth-child(%d) a", i+1))))
		tb.wait("#translation")
		if got := tb.at(); got != href {
			t.Errorf("JS off, example %d: at %s; want %s", i+1, got, href)
		}
		checkFilled(tb, "JS off, example "+href, href)
		if i == len(hrefs)-1 {
			tb.audit("an example, JS off")
		}
	}

	const asked = vinExample + "&to=epa-vehicles&to=yard"
	tb.do(chromedp.Navigate(base + asked))
	tb.wait("#translation")
	checkFilled(tb, "JS off, a translation", asked)
	if got := exampleHrefs(tb, "#ui-main"); len(got) != 5 || got[0] != asked || got[1] != modelExample+"&to=epa-vehicles&to=yard" ||
		!strings.HasSuffix(got[3], "&to=listing") {
		t.Errorf("JS off: the translate page's examples do not keep its targets: %q", got)
	}
}

// selectionJSOn: with JavaScript, in a fresh profile, against a server
// that loaded classics, parts and listing, a vocabulary on the parts
// spine, and offers the EPA sample.
func selectionJSOn(t *testing.T, path, base string, wire *wireLog) {
	tb := newTab(t, path, base, &requestLog{}, true)
	ready := func() {
		tb.t.Helper()
		tb.do(chromedp.Navigate(base + "/vocab"))
		tb.waitState("ready")
		eval[bool](tb, `window.stayed = true`)
	}
	const listingExample = "/ui/translate?from.entry.category=Brake+Pads&from.vocabulary=listing&to=listing"
	ready()
	if got := exampleHrefs(tb, "#vocab-translate"); !slices.Equal(got, []string{vinExample, modelExample, listingExample}) {
		t.Errorf("no file in the browser: examples %q", got)
	}
	checkTicked(tb, "no file in the browser", nil)

	// A file loaded is ticked, and a second load keeps the first.
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{filepath.Join(fixture, "yard.jsonl")}))
	tb.waitPrefix("#load-status", "Loaded yard (")
	checkTicked(tb, "a load", selection{"vpic": {"yard"}})
	tb.do(chromedp.Click(chromedp.CSS("#load-epa")))
	tb.waitPrefix("#load-status", "Loaded epa-vehicles (")
	checkTicked(tb, "a second load", selection{"vpic": {"epa-vehicles", "yard"}})
	tb.audit("/vocab, two files loaded and ticked")

	// Each example fills its form by htmx, to the targets ticked on its
	// spine, else to its own. The server's listing goes to the server,
	// and its target becomes the selection on the parts spine, beside
	// vPIC's.
	const both = "&to=epa-vehicles&to=yard"
	hrefs := exampleHrefs(tb, "#vocab-translate")
	if want := []string{
		vinExample + both,
		modelExample + both,
		"/ui/translate?from.entry.make=Acura&from.entry.model=2.2CL%2F3.0CL&from.vocabulary=epa-vehicles&model_year=1997" + both,
		listingExample,
		"/ui/translate?from.entry.make=Chevrolet&from.entry.model=Corvair&from.vocabulary=yard" + both,
	}; !slices.Equal(hrefs, want) {
		t.Errorf("JS on: examples\n%q\nwant\n%q", hrefs, want)
	}
	for i, href := range hrefs {
		if i > 0 {
			ready()
		}
		tb.do(chromedp.Click(chromedp.CSS(fmt.Sprintf("#vocab-translate ul.examples li:nth-child(%d) a", i+1))))
		tb.wait("#translation")
		if got := tb.at(); got != href {
			t.Errorf("JS on, example %d: at %s; want %s", i+1, got, href)
		}
		checkFilled(tb, "JS on, example "+href, href)
		if !eval[bool](tb, `window.stayed === true`) {
			t.Errorf("JS on, example %d went as a page", i+1)
		}
		if i == 0 {
			tb.audit("an example in the browser")
		}
	}
	all := selection{"parts": {"listing"}, "vpic": {"epa-vehicles", "yard"}}
	waitSelection(tb, "every example", all)

	// A target cleared is dropped at once, in every form, and an example
	// clicked then goes to what is ticked, the parts spine's untouched.
	ready()
	tb.do(chromedp.Click(chromedp.CSS(`#vocab-translate form[aria-labelledby="from-vin-h"] input[value="yard"]`)))
	tb.until("clearing yard", `!document.querySelector('#vocab-translate form[aria-labelledby="from-vpic-h"] input[value="yard"]').checked`)
	waitSelection(tb, "clearing yard", selection{"parts": {"listing"}, "vpic": {"epa-vehicles"}})
	tb.do(chromedp.Click(chromedp.CSS("#vocab-translate ul.examples li:nth-child(1) a")))
	tb.wait("#translation")
	if got, want := tb.at(), vinExample+"&to=epa-vehicles"; got != want {
		t.Errorf("an example after clearing yard: at %s; want %s", got, want)
	}
	ready()
	checkTicked(tb, "a translation", selection{"parts": {"listing"}, "vpic": {"epa-vehicles"}})
	tb.do(chromedp.Click(chromedp.CSS(`#vocab-translate form[aria-labelledby="from-vpic-h"] input[value="yard"]`)))
	waitSelection(tb, "ticking yard", all)

	// A removed file drops out; the rest stay ticked.
	tb.do(chromedp.Click(chromedp.CSS(`form.inline:has(input[value="yard"]) button`)))
	tb.until("removing yard", `!document.querySelector('a[href="/ui/vocabularies/yard"]')`)
	afterRemove := selection{"parts": {"listing"}, "vpic": {"epa-vehicles"}}
	checkTicked(tb, "a removal", afterRemove)
	tb.audit("/vocab after a removal")

	// A reload, and a cold start of the worker, keep the selection.
	ready()
	checkTicked(tb, "a reload", afterRemove)
	tb.stopWorkers()
	ready()
	checkTicked(tb, "a cold start", afterRemove)

	// A link's own to= replaces the selection on its spine.
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{filepath.Join(fixture, "yard.jsonl")}))
	tb.waitPrefix("#load-status", "Loaded yard (")
	checkTicked(tb, "yard loaded again", all)
	const link = vinExample + "&to=yard"
	tb.do(chromedp.Navigate(base + link))
	tb.wait("#translation")
	checkFilled(tb, "a link with to=", link)
	waitSelection(tb, "a link with to=", selection{"parts": {"listing"}, "vpic": {"yard"}})
	ready()
	checkTicked(tb, "after a link with to=", selection{"parts": {"listing"}, "vpic": {"yard"}})

	// No file reached the server, nothing threw and CSP refused nothing.
	for _, r := range wire.all() {
		if (r.method != http.MethodGet && r.method != http.MethodHead) || r.body != 0 {
			t.Errorf("the server got %s %s with %d bytes of body", r.method, r.target, r.body)
		}
		u, err := url.Parse(r.target)
		if err != nil || strings.HasPrefix(u.Path, "/ui/r/") {
			continue
		}
		q := u.Query()
		for _, name := range []string{"yard", "epa-vehicles"} {
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

// exampleHrefs are the hrefs of the examples under the element sel
// matches, as the page has them: no script needed.
func exampleHrefs(tb *tab, sel string) []string {
	tb.t.Helper()
	var as []map[string]string
	tb.call(func(ctx context.Context) (err error) {
		as, err = chromedp.Run(ctx, chromedp.AttributesAll(chromedp.CSSAll(sel+" a.example"), chromedp.AtLeast(0)))
		return err
	})
	var out []string
	for _, a := range as {
		out = append(out, a["href"])
	}
	return out
}

// checkFilled checks that the form target's query came from, on the
// translate page, holds each of its values as the page has them, and
// ticks its targets alone: no script needed.
func checkFilled(tb *tab, step, target string) {
	tb.t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		tb.t.Fatal(err)
	}
	q := u.Query()
	inForm := `form:has(select[name="from.vocabulary"] option[selected][value=` + cssString(q.Get("from.vocabulary")) + `]) `
	form := "entry"
	switch {
	case q.Has("from.vin"):
		form = "vin"
	case q.Has("from.make") || q.Has("from.model"):
		form = "vpic"
	}
	if form != "entry" {
		inForm = `form[aria-labelledby="from-` + form + `-h"] `
	}
	var want []string
	for k, vs := range q {
		v := cssString(vs[0])
		switch field, entry := strings.CutPrefix(k, "from.entry."); {
		case entry:
			want = append(want, inForm+"#entry-"+field+"[value="+v+"]")
		case k == "from.vin" || k == "from.make" || k == "from.model":
			want = append(want, inForm+"#"+strings.ReplaceAll(k, ".", "-")+"[value="+v+"]")
		case k == "model_year":
			want = append(want, inForm+"#"+form+"-model-year[value="+v+"]")
		case k == "from.vocabulary":
			want = append(want, inForm+`select[name="from.vocabulary"] option[value=`+v+"][selected]")
		case k == "to":
			for _, name := range vs {
				want = append(want, inForm+"input[name=to][value="+cssString(name)+"][checked]")
			}
		default:
			tb.t.Fatalf("%s: no field for %s", step, k)
		}
	}
	for _, sel := range want {
		if n := tb.count(sel); n != 1 {
			tb.t.Errorf("%s: %d of %s", step, n, sel)
		}
	}
	if n := tb.count(inForm + "input[name=to][checked]"); n != len(q["to"]) {
		tb.t.Errorf("%s: %d targets ticked; want %d", step, n, len(q["to"]))
	}
}

// cssString is s as a CSS string.
func cssString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// selection is a selection as IndexedDB keeps it: names by spine, each
// list sorted, no empty one.
type selection map[string][]string

// checkTicked checks that each of /vocab's forms ticks want's targets on
// its spine alone, and that IndexedDB keeps want.
func checkTicked(tb *tab, step string, want selection) {
	tb.t.Helper()
	for _, f := range targetsOffered(tb, "#vocab-translate") {
		if !slices.Equal(f.Ticked, want[f.Spine]) {
			tb.t.Errorf("%s: the form %s ticks %q; want %q", step, f.Form, f.Ticked, want[f.Spine])
		}
	}
	if got := storedSelection(tb); fmt.Sprint(got) != fmt.Sprint(want) {
		tb.t.Errorf("%s: IndexedDB keeps the selection %v; want %v", step, got, want)
	}
}

// offered is the targets one of the translate forms offers, by name: the
// form's heading id, the spine its targets are on, all of them and those
// ticked.
type offered struct {
	Form, Spine     string
	Targets, Ticked []string
}

// targetsOffered are the targets each translate form under the element
// root matches offers, in page order, as they are ticked now.
func targetsOffered(tb *tab, root string) []offered {
	tb.t.Helper()
	return eval[[]offered](tb, `[...document.querySelectorAll(`+jsString(root+" fieldset")+`)].map(f => {
		const boxes = [...f.querySelectorAll("input[name=to]")];
		return {
			form: f.closest("form").getAttribute("aria-labelledby"),
			spine: f.dataset.spine,
			targets: boxes.map(b => b.value).sort(),
			ticked: boxes.filter(b => b.checked).map(b => b.value).sort(),
		};
	})`)
}

// waitSelection waits, up to 10 s, for IndexedDB to keep want.
func waitSelection(tb *tab, step string, want selection) {
	tb.t.Helper()
	got := storedSelection(tb)
	for i := 0; i < 100 && fmt.Sprint(got) != fmt.Sprint(want); i++ {
		eval[bool](tb, `new Promise(resolve => setTimeout(() => resolve(true), 100))`)
		got = storedSelection(tb)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		tb.t.Errorf("%s: IndexedDB keeps the selection %v; want %v", step, got, want)
	}
}

// storedSelection is the selection IndexedDB keeps.
func storedSelection(tb *tab) selection {
	tb.t.Helper()
	return eval[selection](tb, `new Promise((resolve, reject) => {
		const r = indexedDB.open("nitrovin-state");
		r.onerror = () => reject(r.error);
		r.onsuccess = () => {
			const db = r.result;
			if (!db.objectStoreNames.contains("state")) {
				db.close();
				resolve({});
				return;
			}
			const q = db.transaction("state").objectStore("state").get("selection");
			q.onsuccess = () => {
				db.close();
				const out = {};
				for (const [spine, names] of Object.entries(q.result || {})) if (names && names.length) out[spine] = [...names].sort();
				resolve(out);
			};
			q.onerror = () => reject(q.error);
		};
	})`)
}
