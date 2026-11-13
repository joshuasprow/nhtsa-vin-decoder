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

// TestVocabPartTargets runs the page's forms from a part vocabulary's
// entry in headless Chrome or Chromium, each run in a fresh profile
// (https://github.com/joshuasprow/nitrovin/issues/198). Each declared
// spine a vocabulary is on gets a tab of its own, whose form's targets
// are the vocabularies on that spine alone (ADR 0010: a translation
// crosses one spine). Beside the fixture, shelf is a second part
// vocabulary on parts and bin-labels one on another spine, bins
// (partFiles). Without JavaScript the ticks travel in to=; with it they
// are the visitor's selection (#194): a file loaded is ticked, and the
// selection survives a tick, a reload, a cold start of the worker, a
// translation and a removal. axe audits every state.
func TestVocabPartTargets(t *testing.T) {
	path := chromePath()
	if path == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no Chrome or Chromium to run the page in: set NITROVIN_CHROME")
		}
		t.Skip("no Chrome or Chromium: set NITROVIN_CHROME to run the page in a browser")
	}
	if testing.Short() {
		t.Skip("builds the worker's module")
	}
	fixtureData(t)
	shelf, bins, binLabels := partFiles(t)

	off := httptest.NewServer(vocabServer(t, nil, fixture, shelf, bins, binLabels))
	t.Cleanup(off.Close)
	partTargetsJSOff(t, path, off.URL)

	module := buildWorker(t)
	wire := &wireLog{h: vocabServer(t, &module, filepath.Join(fixture, "classics.jsonl"))}
	on := httptest.NewServer(wire)
	t.Cleanup(on.Close)
	partTargetsJSOn(t, path, on.URL, wire, []string{
		filepath.Join(fixture, "parts.jsonl"), filepath.Join(fixture, "listing.jsonl"), shelf, bins, binLabels,
	})
}

// partFiles writes the fixture's part files under other names: shelf,
// listing's entries as a second part vocabulary on parts, and bins, the
// parts spine's entries as a spine of its own, with bin-labels, listing's
// entries mapped onto bins.
func partFiles(t *testing.T) (shelf, bins, binLabels string) {
	t.Helper()
	dir := t.TempDir()
	derive := func(from, to string, header ...string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(fixture, from+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		head, rest, _ := strings.Cut(string(b), "\n")
		for i := 0; i < len(header); i += 2 {
			if !strings.Contains(head, header[i]) {
				t.Fatalf("%s's header %s has no %s", from, head, header[i])
			}
			head = strings.Replace(head, header[i], header[i+1], 1)
		}
		p := filepath.Join(dir, to+".jsonl")
		if err := os.WriteFile(p, []byte(head+"\n"+rest), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	return derive("listing", "shelf", `"name":"listing"`, `"name":"shelf"`),
		derive("parts", "bins", `"name":"parts"`, `"name":"bins"`),
		derive("listing", "bin-labels", `"name":"listing"`, `"name":"bin-labels"`, `"spine":"parts"`, `"spine":"bins"`)
}

// The tabs of the entries on the bins spine and the form of a part
// vocabulary's entry.
const (
	binsTab   = "/?tab=entry-bins"
	partsForm = `#controls form[aria-labelledby="tab-entry-parts"] `
)

// partTargetsJSOff: without JavaScript, on a server that loaded the
// fixture, shelf, bins and bin-labels, each spine has its tab, whose form
// offers that spine's targets, and a part entry translates into another
// part vocabulary on its spine, the target ticked travelling in to=.
func partTargetsJSOff(t *testing.T, path, base string) {
	tb := newTab(t, path, base, &requestLog{}, false)
	tb.open(vinTab)
	if got, want := tabHrefs(tb), []string{vinTab, entryTab, binsTab, partsTab, idTab}; !slices.Equal(got, want) {
		t.Fatalf("JS off: tabs %q; want %q", got, want)
	}
	vehicles := []string{"yard"}
	for href, want := range map[string]offered{
		vinTab:   {"tab-vin", "vpic", vehicles, nil},
		entryTab: {"tab-entry", "vpic", vehicles, nil},
		binsTab:  {"tab-entry-bins", "bins", []string{"bin-labels"}, nil},
		partsTab: {"tab-entry-parts", "parts", []string{"listing", "shelf"}, nil},
		idTab:    {"tab-id", "vpic", vehicles, nil},
	} {
		tb.open(href)
		if got := renderedTargets(tb, "#controls"); fmt.Sprint(got) != fmt.Sprint([]offered{want}) {
			t.Errorf("JS off, %s: targets %v; want %v", href, got, want)
		}
	}
	tb.open(partsTab)
	tb.audit("the parts tab, part vocabularies on two spines, JS off")

	// A link's to= ticks its targets on their spine alone.
	for href, want := range map[string][]string{partsTab + "&to=listing&to=shelf": {"listing", "shelf"}, vinTab + "?to=listing&to=shelf": nil} {
		tb.open(href)
		for _, f := range renderedTargets(tb, "#controls") {
			if !slices.Equal(f.Ticked, want) {
				t.Errorf("JS off, %s: the form %s ticks %q; want %q", href, f.Form, f.Ticked, want)
			}
		}
	}

	// The tab opens on its first vocabulary, listing. shelf chosen, to
	// listing: sent, the form is shelf's; then its entry translates.
	tb.open(partsTab)
	tb.do(chromedp.SetValue(chromedp.CSS(partsForm+"select"), "shelf"),
		chromedp.Click(chromedp.CSS(partsForm+`input[value="listing"]`)),
		chromedp.Click(chromedp.CSS(partsForm+"button[type=submit]")))
	tb.wait(partsForm + `option[value="shelf"][selected]`)
	const chosen = "/?from.vocabulary=shelf&from.entry.category=&to=listing"
	if got := tb.at(); got != chosen {
		t.Errorf("JS off, shelf chosen: at %s; want %s", got, chosen)
	}
	if n := tb.count("#result"); n != 0 {
		t.Errorf("JS off, shelf chosen: an answer before an entry")
	}
	tb.do(chromedp.SetValue(chromedp.CSS("#entry-category"), "Brake Pads"),
		chromedp.Click(chromedp.CSS(partsForm+`button[type=submit]`)))
	tb.wait("#result")
	const translated = "/?from.vocabulary=shelf&from.entry.category=Brake+Pads&to=listing"
	if got := tb.at(); got != translated {
		t.Errorf("JS off, a part translation: at %s; want %s", got, translated)
	}
	checkFilled(tb, "JS off, a part translation", translated)
	if s := tb.html("#result"); !strings.Contains(s, `<h4 class="map-group">listing</h4>`) || !strings.Contains(s, `<span class="name">Brake Pads</span>`) {
		t.Errorf("JS off, a part translation: %s", s)
	}
	// The page's examples keep its target.
	for _, a := range attributes(tb, `#controls a.example`) {
		if u, err := url.Parse(a["href"]); err != nil || !slices.Equal(u.Query()["to"], []string{"listing"}) {
			t.Errorf("JS off, a part translation: the example %s does not keep to=listing", a["href"])
		}
	}
	tb.audit("a part translation, JS off")
}

// partTargetsJSOn: with JavaScript, in a fresh profile, against a server
// that loaded classics alone, the browser loads paths (the parts spine,
// listing, shelf, bins and bin-labels) as one batch.
func partTargetsJSOn(t *testing.T, path, base string, wire *wireLog, paths []string) {
	tb := newTab(t, path, base, &requestLog{}, true)
	tb.open(vinTab)
	eval[bool](tb, `window.stayed = true`)
	tb.vocabularies()
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), paths))
	tb.waitPrefix("#load-status", "Loaded ")
	// The page's tabs follow the files, without a reload.
	tb.until("the tabs of the files loaded", `document.getElementById("tab-entry-parts") !== null`)
	if got, want := tabHrefs(tb), []string{vinTab, binsTab, partsTab, idTab}; !slices.Equal(got, want) || !eval[bool](tb, `window.stayed === true`) {
		t.Errorf("JS on: tabs %q, or the page reloaded; want %q", got, want)
	}
	loaded := selection{"bins": {"bin-labels"}, "parts": {"listing", "shelf"}}
	checkTicked(tb, "a batch of part files", loaded)
	tb.open(partsTab)
	tb.audit("the parts tab, part vocabularies on two spines loaded and ticked")

	// A target cleared stays cleared through a reload and a cold start.
	tb.waitState("ready")
	tb.ticked()
	tb.do(chromedp.Click(chromedp.CSS(partsForm + `input[value="shelf"]`)))
	cleared := selection{"bins": {"bin-labels"}, "parts": {"listing"}}
	waitSelection(tb, "clearing shelf", cleared)
	checkTicked(tb, "a reload", cleared)
	tb.stopWorkers()
	checkTicked(tb, "a cold start", cleared)

	// shelf's example goes to listing, ticked on its spine, by htmx.
	const shelfExample = "/?from.entry.category=Brake+Pads&from.vocabulary=shelf&to=listing"
	tb.open(binsTab)
	tb.ticked()
	if got, want := exampleHrefs(tb, "#controls"), []string{"/?from.entry.category=Brake+Pads&from.vocabulary=bin-labels&to=bin-labels"}; !slices.Equal(got, want) {
		t.Errorf("JS on, the bins tab: examples %q; want %q", got, want)
	}
	tb.open(partsTab)
	tb.ticked()
	eval[bool](tb, `window.stayed = true`)
	if got, want := exampleHrefs(tb, "#controls"), []string{listingExample, shelfExample}; !slices.Equal(got, want) {
		t.Errorf("JS on, the parts tab: examples\n%q\nwant\n%q", got, want)
	}
	tb.do(chromedp.Click(chromedp.CSS(`#controls a.example[href="` + shelfExample + `"]`)))
	tb.wait("#result")
	if got := tb.at(); got != shelfExample {
		t.Errorf("JS on, shelf's example: at %s; want %s", got, shelfExample)
	}
	checkFilled(tb, "JS on, shelf's example", shelfExample)
	if !eval[bool](tb, `window.stayed === true`) {
		t.Error("JS on, shelf's example went as a page")
	}
	tb.audit("a part translation in the browser")
	waitSelection(tb, "a part translation", cleared)

	// A removed file drops out, and its spine's tab with it.
	tb.open(partsTab)
	tb.waitState("ready")
	tb.ticked()
	tb.do(chromedp.Click(chromedp.CSS(partsForm + `input[value="shelf"]`)))
	waitSelection(tb, "ticking shelf", loaded)
	tb.vocabularies()
	tb.do(chromedp.Click(chromedp.CSS(`form.inline:has(input[value="bin-labels"]) button`)))
	tb.until("removing bin-labels", `!document.getElementById("tab-entry-bins")`)
	tb.audit("the dialog after removing bin-labels")
	checkTicked(tb, "a removal", selection{"parts": {"listing", "shelf"}})

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
		for _, name := range []string{"parts", "listing", "shelf", "bins", "bin-labels"} {
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

// attributes are the attributes of each element sel matches, as the page
// has them, none when it matches nothing: no script needed.
func attributes(tb *tab, sel string) []map[string]string {
	tb.t.Helper()
	if tb.count(sel) == 0 {
		return nil
	}
	var out []map[string]string
	tb.call(func(ctx context.Context) (err error) {
		out, err = chromedp.Run(ctx, chromedp.AttributesAll(chromedp.CSSAll(sel)))
		return err
	})
	return out
}

// renderedTargets are the targets each form under the element root
// offers, as the page has them, ticked as drawn: no script needed.
func renderedTargets(tb *tab, root string) []offered {
	tb.t.Helper()
	var out []offered
	for _, fs := range attributes(tb, root+" form:has(fieldset)") {
		form := fs["aria-labelledby"]
		sel := root + ` form[aria-labelledby="` + form + `"] fieldset`
		f := offered{Form: form, Spine: attributes(tb, sel)[0]["data-spine"]}
		for _, box := range attributes(tb, sel+" input[name=to]") {
			f.Targets = append(f.Targets, box["value"])
			if _, ok := box["checked"]; ok {
				f.Ticked = append(f.Ticked, box["value"])
			}
		}
		slices.Sort(f.Targets)
		slices.Sort(f.Ticked)
		out = append(out, f)
	}
	return out
}
