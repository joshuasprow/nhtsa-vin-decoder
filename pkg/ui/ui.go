// Package ui serves nitrovin's pages under /ui (ADR 0013): html/template
// pages and htmx fragments. Like the routes, which are outside the /v1
// contract and openapi.yaml, its Go API is free to change.
//
// `nitrovin serve` runs the Handler against its decoder and loaded files;
// the browser's Service Worker runs the same Handler, built with Go's
// toolchain to WebAssembly, against the files the browser holds. So the
// package never imports the artifact, the decoder or pkg/nitrovin: what a
// page answers from is a Backend, and the package builds for js/wasm.
//
// Every route answers a fragment when the request carries HX-Request, and
// a full page in the one layout when it doesn't or when htmx restores
// history, so every page works without JavaScript and htmx only enhances
// links and forms that already work. Every response varies by HX-Request
// and carries CSP, script-src 'self' among it. Errors carry real status
// codes: a page request gets the one full-page error template, unknown
// paths included; an htmx request gets the error fragment, which the
// layout's htmx configuration (responseHandling) swaps into the page's
// error slot. The pages' assets, htmx (vendored, one pinned file) and the
// stylesheet, are served under /ui/r/<release>/, immutable for a year: a
// new release is a new URL.
package ui

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// CSP is every /ui response's Content-Security-Policy: nothing but the
// server's own scripts, styles and connections, no inline script or style.
// WebAssembly runs in the Service Worker, whose own script alone carries
// 'wasm-unsafe-eval' (ADR 0013).
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// htmxVersion is the vendored htmx: static/htmx.min.js is dist/htmx.min.js
// of the npm package htmx.org at this version, byte for byte, and
// TestHTMXPin holds its sha256. scripts/tools.sh outdated reports a newer
// release in the weekly tools issue. To bump, replace the file from the
// package, then this version and the test's hash.
const htmxVersion = "2.0.11"

// htmxConfig is the layout's htmx configuration (the htmx-config meta):
// a 4xx or 5xx fragment is an error, swapped into the error slot; htmx
// adds no inline style, which CSP refuses, and evaluates nothing.
const htmxConfig = `{"allowEval":false,"includeIndicatorStyles":false,"responseHandling":[` +
	`{"code":"204","swap":false},` +
	`{"code":"[23]..","swap":true},` +
	`{"code":"[45]..","swap":true,"error":true,"target":"#ui-error","swapOverride":"innerHTML"}]}`

// immutable is an asset's Cache-Control: its URL names its release, so it
// never changes.
const immutable = "public, max-age=31536000, immutable"

//go:embed ui.html static
var files embed.FS

var tpl = template.Must(template.ParseFS(files, "ui.html"))

// asset is one file served under /ui/r/<release>/.
type asset struct {
	contentType string
	body        []byte
}

// static are the files under /ui/r/<release>/, by name.
var static = func() map[string]asset {
	read := func(name string) []byte {
		b, err := files.ReadFile("static/" + name)
		if err != nil {
			panic(err)
		}
		return b
	}
	return map[string]asset{
		"htmx.min.js": {"text/javascript; charset=utf-8", read("htmx.min.js")},
		"ui.css":      {"text/css; charset=utf-8", read("ui.css")},
	}
}()

// Backend is what the pages answer from: on the server its decoder and
// loaded files, in the Service Worker the files the browser holds. Info is
// GET /v1/info's answer; its release names the assets' URLs.
type Backend interface {
	Info() contract.Info
}

// Release is info's release as the Worker caches it
// (deploy/cloudflare/worker): the decoder version, data month and artifact
// hash, joined by "/".
func Release(info contract.Info) string {
	return info.DecoderVersion + "/" + info.DataMonth + "/" + info.ArtifactHash
}

// assetsPrefix starts every asset's path; the release and the file's name
// follow.
const assetsPrefix = "/ui/r/"

// assetsPath is the path release's assets are under, each part of the
// release escaped as one path segment.
func assetsPath(release string) string {
	parts := strings.Split(release, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return assetsPrefix + strings.Join(parts, "/")
}

// Handler serves /ui/*. It is safe for concurrent use when its Backend is.
type Handler struct {
	b   Backend
	log *log.Logger
	// routes are the pages by path.
	routes map[string]route
}

// route answers one page: the view, or the error the page shows instead.
type route func(r *http.Request) (view, error)

// view is what a route answers: its fragment, a template of ui.html with
// its data, and the page's title and status (0 is 200).
type view struct {
	title    string
	template string
	data     any
	status   int
}

// New is the /ui handler answering from b. A route's fault and a panic are
// logged to log.
func New(b Backend, log *log.Logger) *Handler {
	return &Handler{b: b, log: log, routes: map[string]route{}}
}

// fragment reports whether r asks for a fragment: htmx sent it, and not to
// restore history, which wants the page.
func fragment(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", CSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	frag := fragment(r)
	var base string // the release's assets, "" until it is known: the page then links none
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			h.log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.EscapedPath(), v, debug.Stack())
			h.fail(w, frag, base, &pageError{Status: http.StatusInternalServerError, Message: fmt.Sprint("nitrovin failed: ", v)})
		}
	}()
	release := Release(h.b.Info())
	base = assetsPath(release)

	if rest, ok := strings.CutPrefix(r.URL.Path, assetsPrefix); ok {
		h.asset(w, r, frag, base, release, rest)
		return
	}
	answer, ok := h.routes[r.URL.Path]
	if !ok {
		h.fail(w, frag, base, &pageError{Status: http.StatusNotFound, Message: "There is no page at " + r.URL.Path + "."})
		return
	}
	if !getOrHead(w, r) {
		h.fail(w, frag, base, &pageError{Status: http.StatusMethodNotAllowed, Message: r.URL.Path + " takes GET, not " + r.Method + "."})
		return
	}
	v, err := answer(r)
	if err != nil {
		h.fail(w, frag, base, h.pageError(err))
		return
	}
	h.render(w, frag, base, v, false)
}

// getOrHead reports whether r is a GET or HEAD, else sets the Allow header
// its refusal carries.
func getOrHead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	return false
}

// asset serves rest, the path after /ui/r/: release's file, immutable. A
// file of another release is not found, so no cache ever holds one under
// the wrong release.
func (h *Handler) asset(w http.ResponseWriter, r *http.Request, frag bool, base, release, rest string) {
	name, ok := strings.CutPrefix(rest, release+"/")
	a, found := static[name]
	if !ok || !found {
		h.fail(w, frag, base, &pageError{Status: http.StatusNotFound, Message: "This release of nitrovin (" + release + ") has no file " + r.URL.Path +
			". A page from an earlier release asks for its own release's files: reload it."})
		return
	}
	if !getOrHead(w, r) {
		h.fail(w, frag, base, &pageError{Status: http.StatusMethodNotAllowed, Message: r.URL.Path + " takes GET, not " + r.Method + "."})
		return
	}
	w.Header().Set("Content-Type", a.contentType)
	w.Header().Set("Cache-Control", immutable)
	_, _ = w.Write(a.body)
}

// pageError is what a page shows instead of its answer: a status, and
// what went wrong in words.
type pageError struct {
	Status  int
	Message string
}

func (e *pageError) Error() string { return e.Message }

// Title is the status's text: "Not Found".
func (e *pageError) Title() string { return http.StatusText(e.Status) }

// pageError is err as a page shows it: a *contract.Problem's status and
// detail, and anything else a fault in nitrovin, logged.
func (h *Handler) pageError(err error) *pageError {
	if e, ok := errors.AsType[*pageError](err); ok {
		return e
	}
	if p, ok := errors.AsType[*contract.Problem](err); ok && p.Type != contract.Internal {
		return &pageError{Status: p.Status, Message: p.Detail}
	}
	h.log.Printf("ui: %v", err)
	return &pageError{Status: http.StatusInternalServerError, Message: "nitrovin failed: " + err.Error()}
}

// fail answers e: the error fragment, or the error page.
func (h *Handler) fail(w http.ResponseWriter, frag bool, base string, e *pageError) {
	name := "error-page"
	if frag {
		name = "error"
	}
	h.render(w, frag, base, view{title: e.Title(), template: name, data: e, status: e.Status}, true)
}

// layout is what the page template renders: the view's fragment in the
// layout.
type layout struct {
	Title string
	// Assets is the path of the release's assets, "" for a page that
	// links none.
	Assets     string
	HTMXConfig string
	Body       template.HTML
}

// render writes v: its fragment alone, or the page around it. A fragment
// that is no error clears the page's error slot. Rendering is whole before
// anything is written, so a template's fault is a clean 500.
func (h *Handler) render(w http.ResponseWriter, frag bool, base string, v view, failed bool) {
	var body bytes.Buffer
	if err := tpl.ExecuteTemplate(&body, v.template, v.data); err != nil {
		panic(fmt.Errorf("template %s: %w", v.template, err))
	}
	out := &body
	if frag && !failed {
		if err := tpl.ExecuteTemplate(&body, "clear-error", nil); err != nil {
			panic(err)
		}
	}
	if !frag {
		out = new(bytes.Buffer)
		page := layout{Title: v.title, Assets: base, HTMXConfig: htmxConfig, Body: template.HTML(body.String())}
		if err := tpl.ExecuteTemplate(out, "page", page); err != nil {
			panic(fmt.Errorf("template page: %w", err))
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Vary", "HX-Request")
	status := v.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}
