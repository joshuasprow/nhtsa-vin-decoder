package main

import (
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chromedp/chromedp"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// TestWorkerCatalog runs a catalog in the browser (ADR 0023) in a fresh
// profile: against a server that has a catalog, the dialog refuses one;
// against one with the fixture's spine, the browser loads the catalog's
// vehicle vocabulary, then compiles catalog.jsonl picked in the dialog
// (in a dedicated worker), which is listed with the drift kinds it could
// not measure, survives a reload, and downloads as a compiled catalog that
// opens. The fit page is the server's until the browser holds the catalog,
// and the worker's after.
func TestWorkerCatalog(t *testing.T) {
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
	catalog := filepath.Join(fitment, "catalog.jsonl")

	// One catalog per nitrovin: a server's refuses a browser's.
	withCatalog := httptest.NewServer(vocabServer(t, &module, fitment))
	t.Cleanup(withCatalog.Close)
	tb := newTab(t, path, withCatalog.URL, &requestLog{}, true)
	tb.do(chromedp.Navigate(withCatalog.URL + "/"))
	tb.vocabularies()
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{catalog}))
	tb.waitPrefix("#load-status", "Nothing was compiled: This server has a catalog, parts-catalog; one catalog per nitrovin")

	wire := &wireLog{h: vocabServer(t, &module, filepath.Join(fitment, "part-types.jsonl"), filepath.Join(fitment, "part-codes.jsonl"))}
	srv := httptest.NewServer(wire)
	t.Cleanup(srv.Close)
	tb = newTab(t, path, srv.URL, &requestLog{}, true)
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()
	fit := "/ui/fit?from.model=1938&model_year=2010" + pinned
	served := func(target string) bool {
		return slices.ContainsFunc(wire.all(), func(r wireRequest) bool { return r.target == target })
	}
	eval[int](tb, `fetch(`+jsString(fit)+`).then(r => r.status)`)
	if !served(fit) {
		t.Errorf("%s without a catalog in the browser: the worker answered it; want the server", fit)
	}

	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{filepath.Join(fitment, "catalog-models.jsonl")}))
	tb.waitPrefix("#load-status", "Loaded ")
	tb.do(chromedp.SetUploadFiles(chromedp.CSS("#vocab-files"), []string{catalog}))
	tb.waitPrefix("#load-status", "Compiled parts-catalog (5 fitments")
	if s := tb.html("#load-status"); !strings.Contains(s, "Not checked here: model-years, model-qualifier drift") {
		t.Errorf("the compile said %q", s)
	}
	if bodies := slices.ContainsFunc(wire.all(), func(r wireRequest) bool { return r.body > 0 }); bodies {
		t.Error("the server got a request with a body: a file left the browser")
	}

	// It survives a reload, and the fit page is then the worker's.
	tb.do(chromedp.Navigate(srv.URL + "/"))
	tb.vocabularies()
	if got := eval[[]string](tb, `[...document.querySelectorAll("#browser-files tr[data-where=browser] th")].map(th => th.textContent.trim())`); !slices.Equal(got, []string{"catalog-models", "parts-catalog"}) {
		t.Errorf("after a reload: stored %q", got)
	}
	if s := tb.html("#browser-files"); !strings.Contains(s, "5 fitments, compiled in this browser") || !strings.Contains(s, "model-years, model-qualifier drift") {
		t.Errorf("the panel: %s", s)
	}
	since := len(wire.all())
	again := fit + "&side=left"
	eval[int](tb, `fetch(`+jsString(again)+`).then(r => r.status)`)
	if slices.ContainsFunc(wire.all()[since:], func(r wireRequest) bool { return r.target == again }) {
		t.Errorf("%s with the browser's catalog: the server answered it; want the worker", again)
	}

	// The compiled file downloads, and opens.
	b64 := eval[string](tb, `fetch("/ui/files/catalog").then(r => r.blob()).then(b => new Promise(resolve => {
		const f = new FileReader(); f.onload = () => resolve(f.result.split(",")[1]); f.readAsDataURL(b);
	}))`)
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	f, err := vocab.OpenCompiled("parts-catalog.compiled", bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the download does not open: %v", err)
	}
	if cc := f.Compiled(); f.Name != "parts-catalog" || !slices.Equal(cc.Skipped, []vocab.DriftKind{vocab.DriftModelYears, vocab.DriftModelQualifier}) {
		t.Errorf("the download: %s, skipped %v", f.Name, cc.Skipped)
	}
}
