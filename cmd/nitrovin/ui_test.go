package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/ui"
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
