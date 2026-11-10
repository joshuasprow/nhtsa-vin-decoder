// Package ui serves nitrovin's pages (ADR 0013): the entry page at /,
// /vocab, the Service Worker at /sw.js and everything under /ui,
// html/template pages and htmx fragments. Like the routes, which are
// outside the /v1 contract and openapi.yaml, its Go API is free to change.
//
// `nitrovin serve` runs the Handler against its decoder and loaded files
// (New); the browser's Service Worker runs the same vocabulary pages, built
// with Go's toolchain to WebAssembly (cmd/nitrovin-sw), against the files
// the browser holds and the server's (NewInBrowser, a Library). So the
// package never imports the artifact, the decoder or pkg/nitrovin: what a
// page answers from is an interface, and the package builds for js/wasm.
//
// Every route answers a fragment when the request carries HX-Request, and
// a full page in the one layout when it doesn't or when htmx restores
// history, so every page works without JavaScript and htmx only enhances
// links and forms that already work. A fragment carries its page's title,
// which htmx gives the document. Every response varies by HX-Request and
// carries CSP, script-src 'self' among it. Errors carry real status codes:
// a page request gets the one full-page error template, unknown paths
// included; an htmx request gets the error fragment, which the layout's
// htmx configuration (responseHandling) swaps into the page's error slot.
// A page that refuses with its own content instead, as the decode form
// shows a refusal beside its field, names the element it fills, and htmx
// is sent there (HX-Retarget). The pages' assets, htmx (vendored, one
// pinned file), the stylesheet and copy.js, are served under
// /ui/r/<release>/, immutable for a year: a new release is a new URL. So
// are what a Service Worker needs of the server to translate in the
// browser: the data month's spine bundle (SpinePath), each file the
// server loaded (FilePath), and EPA's vocabulary, which /vocab offers to
// load into the browser (Offer).
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
	"strconv"
	"strings"
	"sync"

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

//go:embed *.html static
var files embed.FS

var tpl = template.Must(template.ParseFS(files, "*.html"))

// asset is one file served under /ui/r/<release>/.
type asset struct {
	contentType string
	body        []byte
}

// static are the files under /ui/r/<release>/, by name. sw.wasm and
// wasm_exec.js join them when the server has the worker's module
// (WithWorker).
var static = func() map[string]asset {
	read := func(name string) []byte {
		b, err := files.ReadFile("static/" + name)
		if err != nil {
			panic(err)
		}
		return b
	}
	return map[string]asset{
		"copy.js":     {"text/javascript; charset=utf-8", read("copy.js")},
		"htmx.min.js": {"text/javascript; charset=utf-8", read("htmx.min.js")},
		"ui.css":      {"text/css; charset=utf-8", read("ui.css")},
		"vocab.js":    {"text/javascript; charset=utf-8", read("vocab.js")},
		"worker.js":   {"text/javascript; charset=utf-8", read("worker.js")},
	}
}()

// Pages is what the vocabulary pages answer from, on the server and in the
// browser's Service Worker alike. Info is GET /v1/info's answer, whose
// release names the assets' URLs; Translate and List answer as
// /v1/translate and /v1/vocabularies do.
type Pages interface {
	Info() contract.Info
	Translate(contract.TranslateRequest) (contract.Translation, error)
	List(contract.ListRequest) (contract.List, error)
}

// Backend is what the server's pages answer from: its decoder and loaded
// files. Decode answers as GET /v1/decode/{vin} does. ErrorText is an
// error code's text as NHTSA's ErrorText spells it ("1 - Check Digit (9th
// position) does not calculate properly"), "" when the data has none.
//
// SpineBundle and File are what the server publishes under /ui/r/: the
// Service Worker forwards those paths to it.
type Backend interface {
	Pages
	Decode(contract.Request) (contract.Decode, error)
	ErrorText(code int) string
	// SpineBundle is the data month's spine bundle (vocab.WriteBundle),
	// published at SpinePath. The Handler asks for it once.
	SpineBundle() ([]byte, error)
	// File is the bytes of the loaded file Info's vocabularies list as
	// name, published at FilePath: bytes whose blake3 is the one Info
	// lists, or an error.
	File(name string) ([]byte, error)
}

// Worker is the browser's Service Worker module, the same pages built
// with Go's toolchain for js/wasm (cmd/nitrovin-sw, scripts/sw.sh), and
// the wasm_exec.js of the Go that built it. EPA is EPA's vocabulary
// (epa-vehicles.jsonl, scripts/epa.sh), which /vocab offers as a
// one-click load into the browser; nil for none.
type Worker struct {
	Module, Exec, EPA []byte
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

// spineName is the spine bundle's name among a release's assets, and
// filesDir the directory of the loaded files'.
const (
	spineName = "spine.json"
	filesDir  = "vocab/"
)

// SpinePath is the path of the spine bundle of info's release, immutable:
// /ui/r/<release>/spine.json.
func SpinePath(info contract.Info) string {
	return assetsPath(Release(info)) + "/" + spineName
}

// FilePath is the path loaded file f of info is published at, immutable:
// /ui/r/<release>/vocab/<blake3>/<name>.jsonl. The release does not name
// the files a server loads, so the file's blake3 is in the path too: a
// server restarted with another file under the same name publishes it at
// another URL.
func FilePath(info contract.Info, f contract.VocabularyFile) string {
	return assetsPath(Release(info)) + "/" + filesDir + url.PathEscape(f.Blake3) + "/" + url.PathEscape(f.Name) + ".jsonl"
}

// Handler serves the pages. It is safe for concurrent use when what it
// answers from is.
type Handler struct {
	p Pages
	// b is the server's Backend, nil in the browser.
	b Backend
	// lib is the browser's files, nil on the server.
	lib *Library
	// worker is the Service Worker's module, nil on the server when it
	// has none and in the browser.
	worker *Worker
	// offer is the vocabulary /vocab offers to load into the browser,
	// EPA's; nil for none.
	offer *Offer
	log   *log.Logger
	// spine is the Backend's spine bundle, asked for once.
	spine func() ([]byte, error)
	// routes are the pages by path, and posts the forms that change
	// something, by path.
	routes, posts map[string]route
}

// route answers one page: the view, or the error the page shows instead.
type route func(r *http.Request) (view, error)

// view is what a route answers: its fragment, a template of the package's
// templates with its data, and the page's title and status (0 is 200).
// target is the element the fragment fills, where htmx is sent even with a
// 4xx status, "" for a fragment that only succeeds. A location other than
// "" redirects there with status instead.
type view struct {
	title    string
	template string
	data     any
	status   int
	target   string
	location string
	// vocab loads vocab.js, the browser files' script: /vocab's.
	vocab bool
}

// New is the server's handler of /, /vocab, /sw.js and /ui answering from
// b. A route's fault and a panic are logged to log.
func New(b Backend, log *log.Logger) *Handler {
	h := &Handler{p: b, b: b, log: log, spine: sync.OnceValues(b.SpineBundle), routes: map[string]route{}, posts: map[string]route{}}
	h.routes["/"] = h.home
	h.routes[decodePath] = h.decode
	h.vocabRoutes()
	return h
}

// NewInBrowser is the Service Worker's handler, answering from the files
// the browser holds and the server's it links (Library), and offering
// what the server offers (ReadWorkerConfig): the vocabulary pages alone,
// since the worker forwards every other request.
func NewInBrowser(lib *Library, offer *Offer, log *log.Logger) *Handler {
	h := &Handler{p: lib, lib: lib, offer: offer, log: log, routes: map[string]route{}, posts: map[string]route{}}
	h.vocabRoutes()
	return h
}

// WithWorker gives the server's handler the Service Worker's module: it
// then serves /sw.js and the module, and /vocab offers browser files,
// and EPA's vocabulary as a one-click load when w has it and it is of
// the server's data month.
func (h *Handler) WithWorker(w Worker) *Handler {
	h.worker = &w
	if len(w.EPA) > 0 {
		h.offer = h.epaOffer(w.EPA)
	}
	return h
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
	info := h.p.Info()
	base = assetsPath(Release(info))

	if rest, ok := strings.CutPrefix(r.URL.Path, assetsPrefix); ok && h.b != nil {
		h.asset(w, r, frag, base, info, rest)
		return
	}
	if r.URL.Path == swPath && h.b != nil && h.worker != nil {
		if !getOrHead(w, r) {
			h.fail(w, frag, base, &pageError{Status: http.StatusMethodNotAllowed, Message: swPath + " takes GET, not " + r.Method + "."})
			return
		}
		h.serviceWorker(w, info, base)
		return
	}
	answer, ok := h.routes[r.URL.Path]
	if strings.HasPrefix(r.URL.Path, vocabulariesPath+"/") {
		answer, ok = h.entries, true
	}
	post, posted := h.posts[r.URL.Path]
	switch {
	case !ok && !posted:
		h.fail(w, frag, base, &pageError{Status: http.StatusNotFound, Message: "There is no page at " + r.URL.Path + "."})
		return
	case posted && r.Method == http.MethodPost:
		answer = post
	case posted:
		w.Header().Set("Allow", http.MethodPost)
		h.fail(w, frag, base, &pageError{Status: http.StatusMethodNotAllowed, Message: r.URL.Path + " takes POST, not " + r.Method + "."})
		return
	case !getOrHead(w, r):
		h.fail(w, frag, base, &pageError{Status: http.StatusMethodNotAllowed, Message: r.URL.Path + " takes GET, not " + r.Method + "."})
		return
	}
	v, err := answer(r)
	if err != nil {
		h.fail(w, frag, base, h.pageError(err))
		return
	}
	if v.location != "" {
		http.Redirect(w, r, v.location, v.status)
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

// asset serves rest, the path after /ui/r/: a file of info's release,
// immutable. A file of another release is not found, so no cache ever
// holds one under the wrong release.
func (h *Handler) asset(w http.ResponseWriter, r *http.Request, frag bool, base string, info contract.Info, rest string) {
	release := Release(info)
	var contentType string
	var body func() ([]byte, error)
	e := &pageError{Status: http.StatusNotFound, Message: "This release of nitrovin (" + release + ") has no file " + r.URL.Path +
		". A page from an earlier release asks for its own release's files: reload it."}
	if name, ok := strings.CutPrefix(rest, release+"/"); ok {
		contentType, body, e = h.published(info, name, e)
	}
	if e != nil {
		h.fail(w, frag, base, e)
		return
	}
	if !getOrHead(w, r) {
		h.fail(w, frag, base, &pageError{Status: http.StatusMethodNotAllowed, Message: r.URL.Path + " takes GET, not " + r.Method + "."})
		return
	}
	b, err := body()
	if err != nil {
		h.fail(w, frag, base, h.pageError(err))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", immutable)
	_, _ = w.Write(b)
}

// published is the asset name of info's release: its content type and
// what gives its bytes, or the page error saying it is not there, missing
// when name is no asset's at all.
func (h *Handler) published(info contract.Info, name string, missing *pageError) (string, func() ([]byte, error), *pageError) {
	if a, ok := static[name]; ok {
		return a.contentType, func() ([]byte, error) { return a.body, nil }, nil
	}
	if name == spineName {
		return "application/json", h.spine, nil
	}
	if h.worker != nil {
		switch name {
		case moduleName:
			return "application/wasm", func() ([]byte, error) { return h.worker.Module, nil }, nil
		case execName:
			return "text/javascript; charset=utf-8", func() ([]byte, error) { return h.worker.Exec, nil }, nil
		}
	}
	rest, ok := strings.CutPrefix(name, filesDir)
	hash, file, ok2 := strings.Cut(rest, "/")
	fileName, ok3 := strings.CutSuffix(file, ".jsonl")
	if !ok || !ok2 || !ok3 {
		return "", nil, missing
	}
	if o := h.offer; o != nil && fileName == o.Name && hash == o.Blake3 {
		return "application/jsonl", func() ([]byte, error) { return h.worker.EPA, nil }, nil
	}
	for _, f := range info.Vocabularies {
		if f.Name != fileName {
			continue
		}
		if f.Blake3 != hash {
			return "", nil, &pageError{Status: http.StatusNotFound, Message: "This nitrovin's " + fileName +
				" is another file now (blake3 " + f.Blake3 + "): reload the page."}
		}
		return "application/jsonl", func() ([]byte, error) { return h.b.File(fileName) }, nil
	}
	return "", nil, &pageError{Status: http.StatusNotFound, Message: "This nitrovin has no file named " + fileName +
		" loaded now: reload the page."}
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
	// Vocab loads vocab.js.
	Vocab bool
}

// render writes v: its fragment alone, or the page around it. A fragment
// that is no error starts with its page's title and clears the page's
// error slot, and one with a target is sent there whatever its status.
// Rendering is whole before anything is written, so a template's fault is
// a clean 500.
func (h *Handler) render(w http.ResponseWriter, frag bool, base string, v view, failed bool) {
	var body bytes.Buffer
	if frag && !failed {
		if err := tpl.ExecuteTemplate(&body, "fragment-title", v.title); err != nil {
			panic(err)
		}
	}
	if err := tpl.ExecuteTemplate(&body, v.template, v.data); err != nil {
		panic(fmt.Errorf("template %s: %w", v.template, err))
	}
	out := &body
	if frag && !failed {
		if err := tpl.ExecuteTemplate(&body, "clear-error", nil); err != nil {
			panic(err)
		}
		if v.target != "" {
			w.Header().Set("HX-Retarget", v.target)
			w.Header().Set("HX-Reswap", "innerHTML")
		}
	}
	if !frag {
		out = new(bytes.Buffer)
		page := layout{Title: v.title, Assets: base, HTMXConfig: htmxConfig, Body: template.HTML(body.String()), Vocab: v.vocab}
		if err := tpl.ExecuteTemplate(out, "page", page); err != nil {
			panic(fmt.Errorf("template page: %w", err))
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(out.Len()))
	w.Header().Set("Vary", "HX-Request")
	status := v.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(out.Bytes())
}
