package main

import (
	"bytes"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeebo/blake3"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/ui"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// uiService answers Info and faults on everything else.
type uiService struct{ faulty }

func (uiService) Info() contract.Info {
	return contract.Info{DecoderVersion: "v0.1.8", DataMonth: "2026_10", ArtifactHash: "blake3:00ff"}
}

// TestUI: serve hands /ui and everything under it to pkg/ui, which answers
// HTML with its CSP, never a problem and never CORS: an unknown path is the
// error page (a fragment for htmx), a fault the 500 page, and the
// release's assets are immutable.
func TestUI(t *testing.T) {
	newUI := func(d decodeService) *api {
		a, err := newAPI(d, log.New(io.Discard, "", 0), io.Discard, nil, "*")
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	good, bad := newUI(uiService{}), newUI(faulty{})
	for _, c := range []struct {
		a       *api
		target  string
		htmx    bool
		status  int
		want    string
		cache   string
		content string
	}{
		{good, "/ui/nothing", false, 404, "<!doctype html>", "", "text/html; charset=utf-8"},
		{good, "/ui/nothing", true, 404, "<p><strong>Not Found.</strong> There is no page at /ui/nothing.</p>", "", "text/html; charset=utf-8"},
		{good, "/ui/r/v0.1.8/2026_10/blake3:00ff/htmx.min.js", false, 200, "var htmx=function()", "public, max-age=31536000, immutable", "text/javascript; charset=utf-8"},
		{good, "/ui/r/v0.1.7/2026_09/blake3:00ff/htmx.min.js", false, 404, "<!doctype html>", "", "text/html; charset=utf-8"},
		{bad, "/ui/nothing", false, 500, "<h1>Internal Server Error</h1>", "", "text/html; charset=utf-8"},
	} {
		name := c.target
		if c.htmx {
			name += " (htmx)"
		}
		r := httptest.NewRequest("GET", c.target, nil)
		r.Header.Set("Origin", "https://example.com")
		if c.htmx {
			r.Header.Set("HX-Request", "true")
		}
		w := httptest.NewRecorder()
		c.a.ServeHTTP(w, r)
		h := w.Result().Header
		if w.Code != c.status || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d, body\n%s\nwant %d with %q", name, w.Code, w.Body.String(), c.status, c.want)
		}
		for k, v := range map[string]string{
			"Content-Type":                c.content,
			"Content-Security-Policy":     ui.CSP,
			"Cache-Control":               c.cache,
			"Access-Control-Allow-Origin": "",
		} {
			if got := h.Get(k); got != v {
				t.Errorf("%s: %s %q; want %q", name, k, got, v)
			}
		}
	}

	// HEAD of a page is its GET without the body.
	w := httptest.NewRecorder()
	good.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/ui/nothing", nil))
	if w.Code != 404 || w.Result().Header.Get("Content-Security-Policy") != ui.CSP {
		t.Errorf("HEAD /ui/nothing: %d, CSP %q", w.Code, w.Result().Header.Get("Content-Security-Policy"))
	}
}

// TestServePublished: serve publishes the data month's spine bundle and
// each loaded file, byte for byte, immutable at their release's URLs, and
// refuses a file whose bytes changed on disk since it was loaded.
func TestServePublished(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"classics.jsonl", "yard.jsonl"} {
		b, err := os.ReadFile(filepath.Join(fixture, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAPI(loaded{d, tr}, log.New(io.Discard, "", 0), io.Discard, nil, "*")
	if err != nil {
		t.Fatal(err)
	}
	get := func(target string) (int, http.Header, []byte) {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
		return w.Code, w.Result().Header, w.Body.Bytes()
	}
	immutable := func(target string, hdr http.Header, contentType string) {
		t.Helper()
		for k, v := range map[string]string{
			"Content-Type":                contentType,
			"Cache-Control":               "public, max-age=31536000, immutable",
			"Access-Control-Allow-Origin": "",
		} {
			if got := hdr.Get(k); got != v {
				t.Errorf("%s: %s %q; want %q", target, k, got, v)
			}
		}
	}

	info := tr.Info()
	var want bytes.Buffer
	if err := d.WriteSpineBundle(&want); err != nil {
		t.Fatal(err)
	}
	target := ui.SpinePath(info)
	status, hdr, body := get(target)
	if status != 200 || !bytes.Equal(body, want.Bytes()) {
		t.Fatalf("%s: %d, %d bytes; want 200, the spine bundle's %d", target, status, len(body), want.Len())
	}
	immutable(target, hdr, "application/json")
	if b, err := vocab.ReadBundle(bytes.NewReader(body)); err != nil || b.Name() != info.DataMonth {
		t.Errorf("%s reads back as %v (%v); want month %s", target, b, err, info.DataMonth)
	}

	for _, f := range info.Vocabularies {
		target := ui.FilePath(info, f)
		if want := strings.TrimSuffix(ui.SpinePath(info), "spine.json") + "vocab/" + f.Blake3 + "/" + f.Name + ".jsonl"; target != want {
			t.Errorf("FilePath %s; want %s", target, want)
		}
		status, hdr, body := get(target)
		disk, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		sum := blake3.Sum256(body)
		if status != 200 || !bytes.Equal(body, disk) || hex.EncodeToString(sum[:]) != f.Blake3 {
			t.Errorf("%s: %d, %d bytes, blake3 %x; want 200, %s's %d bytes, blake3 %s", target, status, len(body), sum, f.Path, len(disk), f.Blake3)
		}
		immutable(target, hdr, "application/jsonl")
	}

	// yard changes on disk: its URL names the bytes loaded, so it refuses.
	yard := info.Vocabularies[1]
	if err := os.WriteFile(yard.Path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, hdr, body = get(ui.FilePath(info, yard))
	if want := yard.Path + " changed since nitrovin loaded it as yard: restart nitrovin serve to serve it"; status != 500 || !strings.Contains(string(body), want) {
		t.Errorf("a changed yard: %d\n%s\nwant 500 with %q", status, body, want)
	}
	if hdr.Get("Cache-Control") != "" {
		t.Errorf("a changed yard: Cache-Control %q", hdr.Get("Cache-Control"))
	}
}
