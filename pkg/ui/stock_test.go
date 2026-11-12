package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// stockNames are the synthetic stock fixtures, testdata/vocab/stock's
// shelves and yard and its orphans' shelf, by name: the order a browser
// lists them in, and so the order its fit asks their sources in.
var stockNames = []string{"shelf-annex", "shelf-orphans", "shelf-stock", "yard-stock"}

// stockPath is a stock fixture's path, by name.
func stockPath(name string) string {
	dir := filepath.Join("..", "..", "testdata", "vocab", "stock")
	if name == "shelf-orphans" {
		dir = filepath.Join(dir, "drift")
	}
	return filepath.Join(dir, name+".jsonl")
}

// stockPicked are the stock fixtures names as the visitor picks them.
func stockPicked(t *testing.T, names ...string) []Picked {
	t.Helper()
	var out []Picked
	for _, n := range names {
		b, err := os.ReadFile(stockPath(n))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, Picked{FileName: n + ".jsonl", Data: b})
	}
	return out
}

// stockServer is the server with the fitment fixture loaded, its catalog
// as JSON Lines, and the stock fixtures in the browser's order.
func stockServer(t *testing.T, d *nitrovin.Decoder) *nitrovin.Translator {
	t.Helper()
	var paths []string
	for _, n := range []string{"part-types", "part-codes", "catalog-models", "catalog"} {
		paths = append(paths, fitmentPath(n))
	}
	for _, n := range stockNames {
		paths = append(paths, stockPath(n))
	}
	files, err := vocab.Open(paths...)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// browserStock is a browser beside s, a server with the fixture's spine
// and part vocabulary, holding the fixture's vehicle vocabulary and
// catalog, compiled, and the stock fixtures, loaded as one batch.
func browserStock(t *testing.T, s *decoderServer, disk *memDisk) *Library {
	t.Helper()
	l := openWith(t, s, disk)
	if _, err := l.Load([]Picked{{FileName: "catalog-models.jsonl", Data: fitmentFile(t, "catalog-models")}}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := compileFixture(t, l, fitmentFile(t, "catalog")); err != nil {
		t.Fatal(err)
	}
	var said []string
	if _, err := l.Load(stockPicked(t, stockNames...), func(s string) { said = append(said, s) }); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(said, func(s string) bool { return strings.HasPrefix(s, "Decoding the VINs of yard-stock") }) {
		t.Errorf("the load said %q", said)
	}
	return l
}

// stockAsks are the stock a request is asked again with: every kind,
// each kind alone, and at locations.
var stockAsks = []stockAsk{
	{stock: []string{"shelf", "yard"}},
	{stock: []string{"shelf"}, location: []string{"north", "east"}},
	{stock: []string{"yard"}, location: []string{"south"}},
	{stock: []string{"yard"}},
}

// TestLibraryStockFit: the synthetic shelves and yard, loaded in the
// browser beside its compiled catalog, answer fit exactly as the server
// does with the same files loaded in the same order, its catalog as JSON
// Lines: every request of TestLibraryCatalogFit asked with every kind of
// stock and at locations, a candidate's shelf (by number and through a
// retired number, under another release) and yard (by a precomputed
// decode, a VIN the server decoded and a catalog vehicle), rows with no
// number, related vehicles' yard counts, and a fitment's stock a page at a
// time. The yard's VINs are decoded once, when it loads, its VINs alone
// sent; a reload reads the decodes kept with it, and a release that kept
// none decodes them again when it opens it.
func TestLibraryStockFit(t *testing.T) {
	d := embedded(t)
	srv := stockServer(t, d)
	s, disk := newDecoderServer(t, d, "part-types", "part-codes"), newDisk()
	l := browserStock(t, s, disk)

	// The yard's VINs alone went to the server, once each, in one batch:
	// a row's fields never leave the browser.
	if len(s.posts) != 1 {
		t.Fatalf("%d POSTs; want 1, the yard's VINs", len(s.posts))
	}
	var items []map[string]any
	if err := json.Unmarshal(s.posts[0], &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0]) != 2 || items[0]["vin"] != "1D7RV1GT5AS123456" || items[0]["full"] != true {
		t.Errorf("POSTed %s; want the one VIN no row decoded already", s.posts[0])
	}
	sent := strings.Join(s.gets, " ") + string(bytes.Join(s.posts, nil))
	for _, field := range []string{"stock_number", "Y101", "A100", "C300", "photos", "north"} {
		if strings.Contains(sent, field) {
			t.Errorf("%q went to the server", field)
		}
	}

	files := map[string]StoredFile{}
	for _, f := range l.Files() {
		files[f.Name] = f
	}
	for _, n := range stockNames {
		if f := files[n]; f.Fault != "" || f.Kind != "stock" || f.Lines == 0 || f.Holds == "" {
			t.Errorf("%s: %+v", n, f)
		}
	}
	if f := files["shelf-orphans"]; !slices.ContainsFunc(f.Drift, func(c contract.DriftCount) bool { return c.Kind == "orphan" && c.Count == 1 }) {
		t.Errorf("shelf-orphans' drift: %+v; want its orphan row", f.Drift)
	}
	info := l.Info()
	for _, n := range stockNames {
		i := slices.IndexFunc(info.Vocabularies, func(v contract.VocabularyFile) bool { return v.Name == n })
		if i < 0 || info.Vocabularies[i].StockCounts == nil || info.Vocabularies[i].StockCounts.Rows == nil {
			t.Errorf("info lists %s as %+v", n, info.Vocabularies)
		}
	}
	if load := l.LoadInfo(); slices.ContainsFunc(load.Vocabularies, func(v contract.VocabularyFile) bool { return v.Drift != nil }) {
		t.Error("LoadInfo measured drift")
	}

	found := sameFits(t, srv, l, stockAsks...)
	t.Logf("%d answers found", found)
	if found < 200 {
		t.Fatalf("%d answers found; want many", found)
	}
	// The fixture's stock is in those answers: every row form.
	stocked, err := l.Fit(context.Background(), contract.FitRequest{
		From:      contract.FitFrom{Vocabulary: "catalog-models", Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}},
		ModelYear: contract.Value[int32](2010), Part: &contract.FitPart{Vocabulary: "part-codes", Entry: map[string]string{"code": "ROT"}},
		Stock: []string{"shelf", "yard"}, Now: fitNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	var matched []string
	for _, c := range stocked.Candidates {
		for _, list := range []*contract.StockList{c.Shelf, c.Yard} {
			for _, r := range list.Rows {
				matched = append(matched, r.ID+"/"+r.Matched)
			}
		}
	}
	for _, want := range []string{"S-2/fitment", "A-1/fitment", "Y-1/vin", "Y-2/vin", "Y-3/catalog-vehicle"} {
		if !slices.Contains(matched, want) {
			t.Errorf("the Ram's rotors' stock: %q; want %s among it", matched, want)
		}
	}

	// A reload reads the decodes kept with the yard, asking nothing.
	again := openWith(t, s, disk)
	if n := sameFits(t, srv, again, stockAsks...); n != found || len(s.posts) != 1 {
		t.Errorf("after a reload %d answers found, %d POSTs; before, %d and 1", n, len(s.posts), found)
	}
	// Another release keeps no index of this one's: it decodes again.
	clear(disk.indexes)
	fresh := openWith(t, s, disk)
	if n := sameFits(t, srv, fresh, stockAsks...); n != found || len(s.posts) != 2 {
		t.Errorf("with no index %d answers found, %d POSTs; before, %d and 2", n, len(s.posts), found)
	}
	// And a server out of reach then leaves the yard out, saying why,
	// until it can decode them.
	clear(disk.indexes)
	s.decodesDown = true
	got := stored(openWith(t, s, disk))
	s.decodesDown = false
	if i := slices.IndexFunc(got, func(n string) bool { return strings.HasPrefix(n, "yard-stock") }); i < 0 || !strings.Contains(got[i], "needs this nitrovin's server, which could not be reached") {
		t.Errorf("offline: %q", got)
	}
	if stored(openWith(t, s, disk))[len(stockNames)+1] != "yard-stock" {
		t.Errorf("online again: %q", stored(openWith(t, s, disk)))
	}
}

// TestLibraryStockRefusals: a shelf whose catalog the browser does not
// hold, a connector's file, a yard whose VINs the server cannot decode,
// and a shelf beside the server's catalog are refused in words, storing
// nothing; a field that looks personal is drift; and the catalog goes
// only with the shelves that name it.
func TestLibraryStockRefusals(t *testing.T) {
	d := embedded(t)
	s, disk := newDecoderServer(t, d, "part-types", "part-codes"), newDisk()
	l := openWith(t, s, disk)
	if _, err := l.Load([]Picked{{FileName: "catalog-models.jsonl", Data: fitmentFile(t, "catalog-models")}}, quiet); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, picked []Picked, want string) {
		t.Helper()
		before := len(disk.records)
		if _, err := l.Load(picked, quiet); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v; want %q", what, err, want)
		}
		if len(disk.records) != before {
			t.Errorf("%s stored something", what)
		}
	}
	refused("a shelf with no catalog", stockPicked(t, "shelf-stock"),
		`shelf-stock.jsonl is a shelf of catalog "parts-catalog", which this browser does not hold: a shelf loads beside the catalog its numbers belong to. Compile the catalog here first`)
	live, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vocab", "stock", "live", "yard-stock.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	refused("a connector", []Picked{{FileName: "live.jsonl", Data: live}}, "connectors run on a server (ADR 0020)")

	if _, err := compileFixture(t, l, fitmentFile(t, "catalog")); err != nil {
		t.Fatal(err)
	}
	s.decodesDown = true
	refused("a yard offline", stockPicked(t, "yard-stock"), "Decoding the VINs of yard-stock needs this nitrovin's server, which could not be reached")
	s.decodesDown = false
	posted := len(s.posts)
	personal := []byte(`{"format":1,"kind":"stock","name":"yard-people","version":"1","source":"yard","as_of":"2026-11-01"}
{"id":"P-1","location":"north","decoded":{"make":476,"model":1938,"year":2010},"fields":{"seller":{"email":"x"},"row":"A"}}
`)
	if _, err := l.Load(append(stockPicked(t, "shelf-stock"), Picked{FileName: "people.jsonl", Data: personal}), quiet); err != nil {
		t.Fatal(err)
	}
	for _, f := range l.Files() {
		if f.Name == "yard-people" && !slices.ContainsFunc(f.Drift, func(c contract.DriftCount) bool { return c.Kind == "personal-field" }) {
			t.Errorf("a personal-looking field: drift %+v", f.Drift)
		}
	}
	if len(s.posts) != posted {
		t.Errorf("a yard of precomputed decodes asked the server %d times", len(s.posts)-posted)
	}
	if err := l.Remove([]string{"parts-catalog"}, false); err == nil || !strings.Contains(err.Error(), "shelf-stock link to it") {
		t.Errorf("removing the catalog under a shelf: %v", err)
	}

	// The server has a catalog: a shelf of it loads there, not here.
	s = newDecoderServer(t, d, "part-types", "catalog-models")
	s.info.Vocabularies = append(s.info.Vocabularies, contract.VocabularyFile{Name: "parts-catalog", Kind: "catalog", Blake3: "ab12"})
	l = openWith(t, s, newDisk())
	if _, err := l.Load(stockPicked(t, "shelf-annex"), quiet); err == nil || !strings.Contains(err.Error(), "shelf-annex.jsonl is a shelf of parts-catalog, this server's catalog, which stays on the server: stock beside it loads there too") {
		t.Errorf("a shelf of the server's catalog: %v", err)
	}
}

// TestFilesPanelStock: the dialog's panel lists a stock file as a shelf
// or yard with its rows, and repeats ADR 0019's warning about personal
// data.
func TestFilesPanelStock(t *testing.T) {
	d := embedded(t)
	s, disk := newDecoderServer(t, d, "part-types", "part-codes"), newDisk()
	l := browserStock(t, s, disk)
	h := NewInBrowser(l, BrowserConfig{}, log.New(io.Discard, "", 0))
	status, _, body := response(t, h, "GET", "/ui/files", htmx)
	if status != http.StatusOK {
		t.Fatalf("/ui/files: %d\n%s", status, body)
	}
	text := plain(body)
	for _, want := range []string{
		"shelf-stock stock, a shelf of parts-catalog, 5 rows", "yard-stock stock, a yard, 4 rows", "Drift: 1 orphan",
		"Connectors run on a server.",
		"a stock file supplies no personal data: no names, contact details or identity numbers of owners, sellers, customers or staff, and nothing it would not show a customer, such as cost.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the panel lacks %q:\n%s", want, text)
		}
	}
}
