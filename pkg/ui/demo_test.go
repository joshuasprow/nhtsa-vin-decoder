package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// epaSample is cmd/nitrovin-epa's sample of EPA's vocabulary, which the
// demo's translation into EPA's names (story 23) is asked beside.
var epaSample = filepath.Join("..", "..", "cmd", "nitrovin-epa", "testdata", "vehicles.jsonl")

// demoPicked are the demo set's files of the kinds given, as vocab.js
// picks them from the release's assets.
func demoPicked(kinds ...string) []Picked {
	var out []Picked
	for _, f := range theDemo().files {
		if slices.Contains(kinds, f.Kind) {
			out = append(out, Picked{FileName: f.FileName, Data: f.data})
		}
	}
	return out
}

// loadDemo loads the demo set into l as vocab.js's Load the demo does:
// its vocabularies as one batch, its catalog compiled, then its shelf and
// yard as one batch.
func loadDemo(t *testing.T, l *Library) {
	t.Helper()
	if _, err := l.Load(demoPicked("spine", "vocabulary", "extension-set"), quiet); err != nil {
		t.Fatal(err)
	}
	c := theDemo().catalog
	if _, err := l.Compile(bytes.NewReader(c.data), c.FileName, int64(len(c.data)), vocab.DirScratch(t.TempDir()), quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Load(demoPicked("stock"), quiet); err != nil {
		t.Fatal(err)
	}
}

// demoServer is `nitrovin serve --vocab pkg/ui/demo --vocab <EPA's>`: the
// answers the browser's must give.
func demoServer(t *testing.T, d *nitrovin.Decoder) *nitrovin.Translator {
	t.Helper()
	files, err := vocab.Open(demoDir, epaSample)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// demoPromises is what each user story of #278 that a request shows
// promises, in the answer's JSON: text it holds, and text it must not.
var demoPromises = map[int]struct{ has, lacks []string }{
	5:  {has: []string{`"status":"found"`, `"part_types":[{"type":"alternator","name":"Alternator","count":3`}},
	6:  {has: []string{`"number":"DEMO-1001"`, `"number":"DEMO-1002"`, `"number":"DEMO-1003"`}},
	7:  {has: []string{`"number":"DEMO-4301"`, `"unconfirmed":[15]`}},
	8:  {has: []string{`"options":"3.5L V6 EcoBoost (VIN G, 8th digit)"`}},
	9:  {has: []string{`"number":"DEMO-1101"`}, lacks: []string{`DEMO-1102`}},
	10: {has: []string{`"number":"DEMO-5301","side":"both"`, `"number":"DEMO-5302","side":"left"`}, lacks: []string{`"side":"right"`}},
	11: {has: []string{`"reached":"redirect"`, `"number":"DEMO-2101"`}},
	12: {has: []string{`"reached":"retired"`, `"number":"DEMO-1001"`}},
	13: {has: []string{`"model":"Camry"`, `"model":"RAV4"`}},
	14: {has: []string{`"model":"CR-V"`, `"shared":2`}},
	15: {has: []string{`"type":"brake-rotor-front","number":"DEMO-4103"`, `"type":"brake-rotor-rear","number":"DEMO-4111"`}},
	16: {has: []string{`"id":"DEMO-S-01","location":"demo-north"`}},
	17: {has: []string{`"id":"DEMO-Y-07"`, `"id":"DEMO-Y-08"`, `"matched":"vin"`}},
	18: {has: []string{`"id":"DEMO-S-02","location":"demo-south","matched":"fitment","fitment":{"type":"alternator","number":"DEMO-0901"},"reached":"retired"`}},
	19: {has: []string{`"catalog_mismatch":true`}},
	20: {has: []string{`"location":"demo-north"`}, lacks: []string{`"location":"demo-south"`}},
	21: {has: []string{`"status":"found"`, `"type":"brake-rotor-front"`, `"type":"mirror"`}},
	22: {has: []string{`"vehicle":{"make":"Dodge","model":"Ram 1500"}`, `"model":13620`}},
	23: {has: []string{`"vocabulary":"epa-vehicles"`, `"entry":{"make":"Chevrolet","model":"Silverado C15 2WD"}`}},
}

// TestLibraryDemoFit: the demo set, loaded into the browser as Load the
// demo loads it (its catalog compiled there), answers byte for byte as
// `nitrovin serve --vocab` does with the same files, every request of its
// list and a sweep over its vehicles, VINs, part types, part codes,
// fitments, sides and stock; each request of its list shows what the user
// stories it names promise; and only the yard's VINs went to the server.
func TestLibraryDemoFit(t *testing.T) {
	d := embedded(t)
	srv := demoServer(t, d)
	s, disk := newDecoderServer(t, d), newDisk()
	l := openWith(t, s, disk)
	if _, err := l.Load([]Picked{{FileName: "epa-vehicles.jsonl", Data: mustRead(t, epaSample)}}, quiet); err != nil {
		t.Fatal(err)
	}
	loadDemo(t, l)
	var names []string
	for _, f := range l.Files() {
		if f.Fault != "" {
			t.Errorf("%s left out: %s", f.Name, f.Fault)
		}
		names = append(names, f.Name)
	}
	if want := []string{"demo-catalog", "demo-classics", "demo-part-codes", "demo-part-types", "demo-shelf", "demo-vehicles", "demo-yard-stock", "epa-vehicles"}; !slices.Equal(names, want) {
		t.Fatalf("stored %q; want %q", names, want)
	}

	// The yard's VINs went to the server, each once, in one batch, and
	// nothing else of the demo did.
	var vins []string
	for _, p := range s.posts {
		var items []map[string]any
		if err := json.Unmarshal(p, &items); err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			if len(it) != 2 || it["full"] != true {
				t.Errorf("POSTed %v; want a VIN to decode", it)
			}
			vins = append(vins, it["vin"].(string))
		}
	}
	if len(s.posts) != 1 || len(vins) != 9 {
		t.Errorf("%d POSTs of %d VINs; want the yard's 9 VINs in one", len(s.posts), len(vins))
	}
	for _, field := range []string{"DEMO-Y-", "DEMO-S-", "stock_number", "demo-north"} {
		if strings.Contains(string(bytes.Join(s.posts, nil))+strings.Join(s.gets, " "), field) {
			t.Errorf("%q went to the server", field)
		}
	}

	// Every request of the list answers as the server does, and shows
	// what its stories promise.
	covered := map[int]bool{}
	for _, e := range theDemo().examples {
		u, err := url.Parse(e.Href)
		if err != nil {
			t.Fatal(err)
		}
		var got []byte
		if u.Path == fitPath {
			req, err := contract.ParseFitQuery(u.Query())
			if err != nil {
				t.Fatalf("%s: %v", e.Href, err)
			}
			if sameFit(t, srv, l, req) == 0 {
				t.Errorf("%s: not found", e.Href)
			}
			req.Now = fitNow
			fit, err := l.Fit(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			got, _ = json.Marshal(fit)
		} else {
			req, err := contract.ParseTranslateQuery(u.Query())
			if err != nil {
				t.Fatalf("%s: %v", e.Href, err)
			}
			want, werr := srv.Translate(req)
			tr, gerr := l.Translate(req)
			wb, _ := json.Marshal(want)
			got, _ = json.Marshal(tr)
			if werr != nil || gerr != nil || !bytes.Equal(wb, got) {
				t.Errorf("%s:\nbrowser %s (%v)\nserver %s (%v)", e.Href, got, gerr, wb, werr)
			}
		}
		for _, story := range e.Stories {
			covered[story] = true
			p, ok := demoPromises[story]
			if !ok {
				t.Errorf("%s names story %d, which no promise checks", e.Href, story)
			}
			for _, s := range p.has {
				if !bytes.Contains(got, []byte(s)) {
					t.Errorf("story %d, %s: the answer lacks %s:\n%s", story, e.Href, s, got)
				}
			}
			for _, s := range p.lacks {
				if bytes.Contains(got, []byte(s)) {
					t.Errorf("story %d, %s: the answer has %s:\n%s", story, e.Href, s, got)
				}
			}
		}
	}
	for story := range demoPromises {
		if !covered[story] {
			t.Errorf("story %d: no request of the demo's list shows it", story)
		}
	}

	found := sameDemoFits(t, srv, l)
	t.Logf("%d answers found", found)
	if found < 500 {
		t.Fatalf("%d answers found; want many", found)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sameDemoFits asks the sweep of the demo set of both: every vehicle of
// its vehicle vocabulary, extension and yard VIN, in and across years,
// with every part type and part code on every side, and related; and
// every fitment and retired number; each with and without stock. It
// returns how many answers were found.
func sameDemoFits(t *testing.T, srv *nitrovin.Translator, l *Library) int {
	t.Helper()
	d := theDemo()
	var froms []contract.FitFrom
	var parts []*contract.FitPart
	var keys []contract.FitmentKey
	for _, f := range d.files {
		file, err := vocab.Read(f.FileName, bytes.NewReader(f.data))
		if err != nil {
			t.Fatal(err)
		}
		switch f.Name {
		case "demo-vehicles":
			for _, e := range file.Entries {
				froms = append(froms, contract.FitFrom{Vocabulary: f.Name, Entry: entryOf(file, e)})
			}
		case "demo-classics":
			for _, x := range file.Extensions {
				froms = append(froms, contract.FitFrom{SpineRef: contract.SpineRef{Ext: "ext:" + f.Name + "/" + x.ID}})
			}
		case "demo-part-types":
			for _, e := range file.SpineEntries {
				parts = append(parts, &contract.FitPart{Spine: f.Name, ID: e.ID})
			}
		case "demo-part-codes":
			for _, e := range file.Entries {
				parts = append(parts, &contract.FitPart{Vocabulary: f.Name, Entry: entryOf(file, e)})
			}
		case "demo-yard-stock":
			for _, r := range file.Stock {
				if r.VIN != "" {
					froms = append(froms, contract.FitFrom{VIN: r.VIN})
				}
			}
		}
	}
	lines, err := l.List(contract.ListRequest{Of: d.catalog.Name, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range lines.Entries {
		keys = append(keys, e.(contract.FitmentLine).Fitment)
	}
	keys = append(keys, contract.FitmentKey{Type: "alternator", Number: "DEMO-0901"}, contract.FitmentKey{Type: "alternator", Number: "DEMO-0000"})
	years := []contract.Null[int32]{{}, contract.Value[int32](1950), contract.Value[int32](1974), contract.Value[int32](2012), contract.Value[int32](2015), contract.Value[int32](2018), contract.Value[int32](2019)}
	asks := []stockAsk{{stock: []string{"shelf", "yard"}}, {stock: []string{"shelf", "yard"}, location: []string{"demo-north"}}}
	found := 0
	for _, from := range froms {
		ys := years
		if from.VIN != "" {
			ys = years[:1]
		}
		for _, year := range ys {
			for _, to := range [][]string{nil, {"demo-vehicles", "epa-vehicles"}} {
				found += sameFitStock(t, srv, l, nil, contract.FitRequest{From: from, ModelYear: year, To: to})
				found += sameFitStock(t, srv, l, asks, contract.FitRequest{From: from, ModelYear: year, To: to, Related: true, Limit: 1})
				for _, part := range parts {
					for _, side := range []string{"", "left", "right", "both"} {
						found += sameFitStock(t, srv, l, asks, contract.FitRequest{From: from, ModelYear: year, Part: part, Side: side, To: to, Limit: 1})
					}
				}
			}
		}
	}
	for _, k := range keys {
		for _, year := range []contract.Null[int32]{{}, contract.Value[int32](2018)} {
			found += sameFitStock(t, srv, l, asks, contract.FitRequest{From: contract.FitFrom{Fitment: &k}, ModelYear: year, To: []string{"demo-vehicles"}, Limit: 1})
		}
	}
	return found
}

// entryOf is e, an entry of f, as a request names it.
func entryOf(f *vocab.File, e vocab.Entry) map[string]string {
	out := map[string]string{}
	for i, name := range f.Fields {
		out[name] = e.Fields[i]
	}
	return out
}

// demoPanelOf is the worker's panel over l, offering the demo.
func demoPanelOf(t *testing.T, l *Library) string {
	t.Helper()
	h := NewInBrowser(l, BrowserConfig{Info: l.Info()}, log.New(io.Discard, "", 0))
	r := httptest.NewRequest("GET", filesPath, nil)
	r.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/ui/files: HTTP %d: %s", w.Code, w.Body)
	}
	return w.Body.String()
}

// TestDemoPanel: the worker's panel offers the demo set as one click, its
// files at the release's URLs; holds it once loaded, with Remove the demo
// naming every file; offers a refresh, replacing what it holds, once one
// file is another; and refuses it in words while the server or the
// browser holds another catalog, or the server a file of a demo name.
func TestDemoPanel(t *testing.T) {
	d := embedded(t)
	s, disk := newDecoderServer(t, d), newDisk()
	l := openWith(t, s, disk)
	panel := demoPanelOf(t, l)
	m := regexp.MustCompile(`<button type="button" id="load-demo" data-demo="([^"]*)"[^>]*>Load the demo</button>`).FindStringSubmatch(panel)
	if m == nil {
		t.Fatalf("no Load the demo in the panel:\n%s", panel)
	}
	var load demoLoad
	if err := json.Unmarshal([]byte(htmlUnescape(m[1])), &load); err != nil {
		t.Fatal(err)
	}
	if len(load.Files) != 7 || len(load.Remove) != 0 {
		t.Fatalf("Load the demo loads %+v", load)
	}
	for _, f := range load.Files {
		if !strings.HasPrefix(f.Name, "demo-") || !strings.HasPrefix(f.URL, assetsPath(Release(l.Info()))+"/vocab/") || !strings.HasSuffix(f.URL, "/"+f.File) {
			t.Errorf("the demo's %+v", f)
		}
	}

	loadDemo(t, l)
	panel = demoPanelOf(t, l)
	if !strings.Contains(panel, "is kept in this browser") || !strings.Contains(panel, `id="remove-demo"`) || strings.Contains(panel, `id="load-demo"`) {
		t.Errorf("with the demo held, the panel:\n%s", panel)
	}
	for _, f := range theDemo().files {
		if !strings.Contains(panel, `<input type="hidden" name="name" value="`+f.Name+`">`) {
			t.Errorf("Remove the demo does not name %s", f.Name)
		}
	}

	// Another catalog in the browser refuses it, naming it.
	other := newDisk()
	lo := openWith(t, s, other)
	if _, err := lo.Load([]Picked{{FileName: "catalog-models.jsonl", Data: fitmentFile(t, "catalog-models")}, {FileName: "part-types.jsonl", Data: fitmentFile(t, "part-types")}}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := compileFixture(t, lo, fitmentFile(t, "catalog")); err != nil {
		t.Fatal(err)
	}
	if p := demoPanelOf(t, lo); !strings.Contains(p, "The demo brings its own catalog, and this browser holds one, parts-catalog: one catalog per nitrovin. Remove parts-catalog to load the demo.") || strings.Contains(p, `id="load-demo"`) {
		t.Errorf("with another catalog in the browser, the panel:\n%s", p)
	}

	// A demo file changed (a newer release's) is offered as a refresh,
	// which removes what the browser holds first.
	shelf := theDemo().file("demo-shelf")
	changed := bytes.Replace(shelf.data, []byte(`"version":"2026.11-demo"`), []byte(`"version":"2026.10-demo"`), 1)
	if _, err := l.Load([]Picked{{FileName: shelf.FileName, Data: changed}}, quiet); err != nil {
		t.Fatal(err)
	}
	panel = demoPanelOf(t, l)
	m = regexp.MustCompile(`<button type="button" id="load-demo" data-demo="([^"]*)"[^>]*>Refresh the demo</button>`).FindStringSubmatch(panel)
	if m == nil {
		t.Fatalf("no Refresh the demo in the panel:\n%s", panel)
	}
	if err := json.Unmarshal([]byte(htmlUnescape(m[1])), &load); err != nil {
		t.Fatal(err)
	}
	if len(load.Remove) != 7 {
		t.Errorf("a refresh removes %q first; want every demo file held", load.Remove)
	}
	if err := l.Remove(load.Remove, false); err != nil {
		t.Fatalf("removing the demo: %v", err)
	}
	if got := stored(l); len(got) != 0 {
		t.Errorf("after removing the demo: %q", got)
	}

	// A server with a catalog refuses it, and so does one with a file of
	// a demo name; neither page tells the visitor to load it.
	withCatalog := newDecoderServer(t, d, "part-types", "catalog-models")
	withCatalog.serve(t, "catalog", fitmentFile(t, "catalog"))
	lc := openWith(t, withCatalog, newDisk())
	if p := demoPanelOf(t, lc); !strings.Contains(p, "The demo brings its own catalog, and this server has one, catalog: one catalog per nitrovin, so the demo cannot load here.") || strings.Contains(p, `id="load-demo"`) {
		t.Errorf("with a catalog on the server, the panel:\n%s", p)
	}
	named := newDecoderServer(t, d)
	named.serve(t, "demo-part-types", theDemo().file("demo-part-types").data)
	ln := openWith(t, named, newDisk())
	if p := demoPanelOf(t, ln); !strings.Contains(p, "This server loaded demo-part-types, named like a file of the demo&#39;s: one name is one file (ADR 0011), so the demo cannot load here.") || strings.Contains(p, `id="load-demo"`) {
		t.Errorf("with a file of a demo name on the server, the panel:\n%s", p)
	}
	for _, refused := range []*Library{lc, ln} {
		h := NewInBrowser(refused, BrowserConfig{Info: refused.Info()}, log.New(io.Discard, "", 0))
		if h.demoLoadable() {
			t.Errorf("a refused demo is loadable beside %v", refused.info.Vocabularies)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/ui/fit?from.model=1938&model_year=2010", nil))
		if body := w.Body.String(); w.Code != http.StatusOK || strings.Contains(body, demoLoadText) {
			t.Errorf("beside %v, a fit page says how to load the demo:\n%s", refused.info.Vocabularies, body)
		}
	}
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&#34;", `"`, "&quot;", `"`, "&amp;", "&", "&lt;", "<", "&gt;", ">", "&#39;", "'").Replace(s)
}

// TestDemoWalkthrough: docs/vocabularies.md walks through the demo set's
// list of requests, every one in the list's order, and no other.
func TestDemoWalkthrough(t *testing.T) {
	doc := string(mustRead(t, filepath.Join("..", "..", "docs", "vocabularies.md")))
	start := strings.Index(doc, "\n## The demo set\n")
	if start < 0 {
		t.Fatal("docs/vocabularies.md has no section The demo set")
	}
	section := doc[start+1:]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	var got []string
	for _, m := range regexp.MustCompile("`(/(?:ui/fit)?\\?[^`\\s]+)`").FindAllStringSubmatch(section, -1) {
		got = append(got, m[1])
	}
	var want []string
	for _, e := range theDemo().examples {
		want = append(want, e.Href)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the walkthrough's requests:\n%s\nthe demo's list:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
