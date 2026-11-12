package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zeebo/blake3"

	// Aliased: the package's tests name a helper artifact.
	dataartifact "github.com/joshuasprow/nitrovin-data/pkg/artifact"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/ui"
)

// openAPI is GET /v1/openapi.json: docs/api/openapi.yaml as JSON, with
// decode.schema.json's definitions bundled in as components.schemas.
// TestOpenAPIBundle rebuilds it from those two files and fails when this
// copy is stale; -update-openapi rewrites it:
//
//	go test ./cmd/nitrovin -run TestOpenAPIBundle -update-openapi
//
//go:embed openapi.json
var openAPI []byte

func init() {
	register(command{
		name:     "serve",
		synopsis: "[--addr ADDR] [--cors ORIGIN]",
		summary:  "Serve the HTTP API (docs/api/openapi.yaml), MCP over Streamable HTTP at /mcp and a page for browsers at / on --addr, then :$PORT, then 127.0.0.1:8080, once the data has loaded.",
		setup: func(fs *flag.FlagSet) func(*env, []string) error {
			addr := fs.String("addr", "", "listen on `address` (default :$PORT when PORT is set, else 127.0.0.1:8080)")
			cors := fs.String("cors", "", "let browsers on `origin` (scheme://host[:port], or * for any) call /v1 and /mcp (default none)")
			return func(e *env, args []string) error {
				if len(args) > 0 {
					return usageError("serve takes no arguments")
				}
				if err := checkOrigin(*cors); err != nil {
					return usageError(err.Error())
				}
				return serve(e, listenAddr(*addr, e.getenv), *cors)
			}
		},
	})
}

// checkOrigin accepts --cors's value: empty (no CORS), *, or one origin as
// a browser sends it in Origin, scheme://host[:port] and nothing more.
func checkOrigin(origin string) error {
	if origin == "" || origin == "*" {
		return nil
	}
	// An origin is exactly scheme://host[:port]: anything else (a path, a
	// trailing slash, a query, user info) fails the round trip.
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Scheme+"://"+u.Host != origin {
		return fmt.Errorf("--cors %q: want * or an origin, scheme://host[:port]", origin)
	}
	return nil
}

// listenAddr is the address serve listens on: --addr, then :$PORT, then
// 127.0.0.1:8080.
func listenAddr(addr string, getenv func(string) string) string {
	if addr != "" {
		return addr
	}
	if port := getenv("PORT"); port != "" {
		return ":" + port
	}
	return "127.0.0.1:8080"
}

// serve loads the data and the vocabularies beside it, then listens on
// addr and serves the API until SIGINT or SIGTERM, when it stops accepting
// and lets requests in flight finish. A cors origin other than "" lets
// browsers there call /v1 and /mcp.
func serve(e *env, addr, cors string) error {
	t, data, err := e.load()
	if err != nil {
		return err
	}
	d := loaded{t.Decoder(), t}
	texts, err := errorTexts(data) // the bytes d opened: no error
	if err != nil {
		return err
	}
	logger := log.New(e.stderr, "nitrovin: ", 0)
	handler, err := newAPI(d, logger, e.stderr, texts, cors)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          logger,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	logger.Printf("serving data %s on http://%s", d.Decoder.Info().DataMonth, ln.Addr())

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	stop()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// loaded is the decoder with the files loaded beside it: /v1/info lists
// them, /v1/translate, /v1/fit and /v1/vocabularies answer through them,
// and /ui/r publishes them with the data month's spine bundle.
type loaded struct {
	*nitrovin.Decoder
	*nitrovin.Translator
}

func (l loaded) Info() contract.Info { return l.Translator.Info() }

// SpineBundle is the data month's spine bundle.
func (l loaded) SpineBundle() ([]byte, error) {
	var b bytes.Buffer
	err := l.Decoder.WriteSpineBundle(&b)
	return b.Bytes(), err
}

// File is loaded file name's bytes, read again from its path: they are
// not kept, and a browser asks for them once a release. Bytes that are
// not the ones loaded are refused, so the URL naming their blake3 never
// serves others.
func (l loaded) File(name string) ([]byte, error) {
	for _, f := range l.LoadInfo().Vocabularies {
		if f.Name != name {
			continue
		}
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, err
		}
		if sum := blake3.Sum256(data); hex.EncodeToString(sum[:]) != f.Blake3 {
			return nil, fmt.Errorf("%s changed since nitrovin loaded it as %s: restart nitrovin serve to serve it", f.Path, name)
		}
		return data, nil
	}
	return nil, fmt.Errorf("no file named %s is loaded", name)
}

// pages is what the pages (pkg/ui) answer from on the server: the API's
// service and its data's error-code table.
type pages struct {
	decodeService
	errorTexts map[int]string
}

func (p pages) ErrorTexts() map[int]string { return p.errorTexts }

// LoadInfo is Info without the drift a first Info measures (ADR 0015):
// what the pages read for the release and the files' names, so only the
// page that shows drift pays for it.
func (p pages) LoadInfo() contract.Info {
	if l, ok := p.decodeService.(interface{ LoadInfo() contract.Info }); ok {
		return l.LoadInfo()
	}
	return p.Info()
}

// errorTexts reads the error-code table of the artifact data: each code's
// text as NHTSA's ErrorText spells it ("1 - Check Digit (9th position)
// does not calculate properly"), by id.
func errorTexts(data []byte) (map[int]string, error) {
	a, err := dataartifact.Open(data)
	if err != nil {
		return nil, err
	}
	t := a.Table("errorcode")
	ids, names := t.Int32s("id"), t.Strings("name")
	texts := make(map[int]string, len(ids))
	for i, id := range ids {
		if s, ok := names.At(i); ok {
			texts[int(id)] = strings.TrimSpace(s)
		}
	}
	return texts, nil
}

// decodeService is what the API answers with: loaded, or a fault injected
// in a test. With the error-code table (pages) it is the pages'
// ui.Backend.
type decodeService interface {
	Info() contract.Info
	Decode(contract.Request) (contract.Decode, error)
	DecodeBatch([]contract.Request) ([]contract.Decode, error)
	Translate(contract.TranslateRequest) (contract.Translation, error)
	TranslateBatch([]contract.TranslateRequest) ([]contract.Translation, error)
	List(contract.ListRequest) (contract.List, error)
	Fit(context.Context, contract.FitRequest) (contract.Fit, error)
	FitBatch(context.Context, []contract.FitRequest) ([]contract.Fit, error)
	SpineBundle() ([]byte, error)
	File(name string) ([]byte, error)
}

// api serves the routes of docs/api/openapi.yaml, /mcp among them, and the
// pages for browsers (pkg/ui): the page at /, the Service Worker at
// /sw.js, everything under /ui, and /vocab, which moved to the page. Every refusal but the pages'
// and the MCP transport's is an RFC 9457 problem. CORS headers are set
// only with a cors origin, and only under /v1/ and on /mcp.
type api struct {
	d   decodeService
	log *log.Logger
	// mcp is `nitrovin mcp`'s server over Streamable HTTP, stateless.
	mcp http.Handler
	// ui serves the pages, their errors included.
	ui *ui.Handler
	// cors is the origin browsers may call /v1 and /mcp from, * for any;
	// "" sets no CORS header.
	cors string
}

// newAPI is the API answering with d. The MCP server's diagnostics go to
// stderr; errorTexts is the data's error-code table, each code's text by
// id, for the decode form (nil shows the codes alone); cors is
// checkOrigin's.
func newAPI(d decodeService, log *log.Logger, stderr io.Writer, errorTexts map[int]string, cors string) (*api, error) {
	server, err := newMCPServer(d, stderr)
	if err != nil {
		return nil, err
	}
	// Stateless: every POST is a session of its own, so any instance
	// answers any request and no session outlives its request. JSON
	// responses: a decode is one message, never worth an event stream.
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless:           true,
		JSONResponse:        true,
		MaxRequestBodyBytes: contract.MaxBody,
	})
	h := ui.New(pages{d, errorTexts}, log)
	if w := embeddedWorker(); w != nil {
		h.WithWorker(*w)
	}
	return &api{d: d, log: log, mcp: handler, ui: h, cors: cors}, nil
}

// corsAllowHeaders are the request headers a preflight allows: a POST's
// Content-Type, and the ones the MCP transport reads.
const corsAllowHeaders = "Accept, Content-Type, Last-Event-ID, Mcp-Protocol-Version, Mcp-Session-Id"

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			a.log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.EscapedPath(), v, panicStack(v))
			a.problem(w, contract.NewProblem(contract.Internal, "", fmt.Sprint("panic: ", v)))
		}
	}()
	path := r.URL.EscapedPath()
	if path == "/" || path == "/vocab" || path == "/sw.js" || path == "/ui" || strings.HasPrefix(path, "/ui/") {
		a.ui.ServeHTTP(w, r)
		return
	}
	cors := a.cors != "" && (path == "/mcp" || strings.HasPrefix(path, "/v1/"))
	if cors {
		w.Header().Set("Access-Control-Allow-Origin", a.cors)
	}
	allow, handle := a.route(path)
	switch {
	case handle == nil:
		a.problem(w, contract.NewProblem(contract.NotFound, "", "no route "+path))
	case cors && r.Method == http.MethodOptions:
		// A preflight: what a cross-origin request may carry here.
		h := w.Header()
		h.Set("Access-Control-Allow-Methods", allow)
		h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
		h.Set("Access-Control-Max-Age", "86400")
		w.WriteHeader(http.StatusNoContent)
	case !allows(allow, r.Method):
		w.Header().Set("Allow", allow)
		a.problem(w, contract.NewProblem(contract.MethodNotAllowed, "", fmt.Sprintf("%s takes %s, not %s", path, allow, r.Method)))
	default:
		handle(w, r)
	}
}

// allows reports whether allow, a list as the Allow header gives it, holds
// method.
func allows(allow, method string) bool {
	for m := range strings.SplitSeq(allow, ", ") {
		if m == method {
			return true
		}
	}
	return false
}

// route maps an escaped path to the methods it takes, as the Allow header
// lists them, and its handler; a nil handler is no route. A VIN and a
// vocabulary name are one path segment each, so a VIN holding "/" is sent
// as %2F.
func (a *api) route(path string) (string, http.HandlerFunc) {
	switch path {
	case "/v1/decode":
		return http.MethodPost, a.decodeBatch
	case "/v1/translate":
		return "GET, POST", a.translate
	case "/v1/fit":
		return "GET, POST", a.fit
	case "/v1/vocabularies":
		return http.MethodGet, func(w http.ResponseWriter, r *http.Request) { a.list(w, r, "") }
	case "/v1/info":
		return http.MethodGet, a.info
	case "/v1/openapi.json":
		return http.MethodGet, a.openAPI
	case "/healthz":
		return http.MethodGet, a.health
	case "/mcp":
		return http.MethodPost, a.mcp.ServeHTTP
	}
	if vin, ok := strings.CutPrefix(path, "/v1/decode/"); ok && !strings.Contains(vin, "/") {
		return http.MethodGet, func(w http.ResponseWriter, r *http.Request) { a.decodeOne(w, r, vin) }
	}
	if rest, ok := strings.CutPrefix(path, "/v1/vocabularies/"); ok {
		if name, ok := strings.CutSuffix(rest, "/entries"); ok && name != "" && !strings.Contains(name, "/") {
			return http.MethodGet, func(w http.ResponseWriter, r *http.Request) { a.list(w, r, name) }
		}
	}
	return "", nil
}

// decodeOne is GET /v1/decode/{vin}; escapedVIN is the path segment.
func (a *api) decodeOne(w http.ResponseWriter, r *http.Request, escapedVIN string) {
	vin, err := url.PathUnescape(escapedVIN)
	if err != nil {
		a.problem(w, contract.NewProblem(contract.InvalidRequest, "/vin", err.Error()))
		return
	}
	query, ok := a.query(w, r)
	if !ok {
		return
	}
	req, err := contract.ParseQuery(vin, query)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	dec, err := a.d.Decode(req)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	a.ok(w, dec)
}

// decodeBatch is POST /v1/decode.
func (a *api) decodeBatch(w http.ResponseWriter, r *http.Request) {
	body, ok := a.body(w, r)
	if !ok {
		return
	}
	reqs, err := contract.ParseBatch(body)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	decs, err := a.d.DecodeBatch(reqs)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	a.ok(w, decs)
}

// translate is GET /v1/translate, one request as dotted query parameters,
// and POST /v1/translate, a batch.
func (a *api) translate(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		a.translateBatch(w, r)
		return
	}
	query, ok := a.query(w, r)
	if !ok {
		return
	}
	req, err := contract.ParseTranslateQuery(query)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	tr, err := a.d.Translate(req)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	a.ok(w, tr)
}

func (a *api) translateBatch(w http.ResponseWriter, r *http.Request) {
	body, ok := a.body(w, r)
	if !ok {
		return
	}
	reqs, err := contract.ParseTranslateBatch(body)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	trs, err := a.d.TranslateBatch(reqs)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	a.ok(w, trs)
}

// fit is GET /v1/fit, one request as dotted query parameters, and POST
// /v1/fit, a batch (ADR 0018).
func (a *api) fit(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		body, ok := a.body(w, r)
		if !ok {
			return
		}
		reqs, err := contract.ParseFitBatch(body)
		if err != nil {
			a.problem(w, contract.AsProblem(err))
			return
		}
		fits, err := a.d.FitBatch(r.Context(), reqs)
		if err != nil {
			a.problem(w, contract.AsProblem(err))
			return
		}
		a.ok(w, fits)
		return
	}
	query, ok := a.query(w, r)
	if !ok {
		return
	}
	req, err := contract.ParseFitQuery(query)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	fit, err := a.d.Fit(r.Context(), req)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	a.ok(w, fit)
}

// list is GET /v1/vocabularies, the catalog, with escapedName "", and GET
// /v1/vocabularies/{name}/entries; escapedName is the path segment.
func (a *api) list(w http.ResponseWriter, r *http.Request, escapedName string) {
	of, err := url.PathUnescape(escapedName)
	if err != nil {
		a.problem(w, contract.NewProblem(contract.InvalidRequest, "/of", err.Error()))
		return
	}
	query, ok := a.query(w, r)
	if !ok {
		return
	}
	req, err := contract.ParseListQuery(of, query)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	l, err := a.d.List(req)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	a.ok(w, l)
}

// query is a GET's query; a malformed one is refused.
func (a *api) query(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		a.problem(w, contract.NewProblem(contract.InvalidRequest, "", "the query string is malformed: "+err.Error()))
		return nil, false
	}
	return query, true
}

// body is a POST's body, up to one byte over contract.MaxBody, enough for
// the batch parsers to refuse it. The request is the body alone, so any
// query parameter is refused.
func (a *api) body(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if r.URL.RawQuery != "" {
		a.problem(w, contract.NewProblem(contract.InvalidRequest, "", fmt.Sprintf("POST %s takes no query parameters", r.URL.EscapedPath())))
		return nil, false
	}
	if r.ContentLength > contract.MaxBody {
		a.problem(w, contract.NewProblem(contract.TooLarge, "", fmt.Sprintf("the body is over %d bytes", contract.MaxBody)))
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, contract.MaxBody+1))
	if err != nil {
		a.problem(w, contract.NewProblem(contract.MalformedJSON, "", "reading the body: "+err.Error()))
		return nil, false
	}
	return body, true
}

func (a *api) info(w http.ResponseWriter, _ *http.Request) { a.ok(w, a.d.Info()) }

func (a *api) openAPI(w http.ResponseWriter, _ *http.Request) {
	a.write(w, http.StatusOK, "application/json", openAPI)
}

func (a *api) health(w http.ResponseWriter, _ *http.Request) {
	a.write(w, http.StatusOK, "application/json", []byte(`{"status":"ok"}`+"\n"))
}

// ok writes v as a 200, compact, as the Go module marshals it.
func (a *api) ok(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	a.write(w, http.StatusOK, "application/json", append(b, '\n'))
}

// problem writes p with its status. An internal problem is a fault in
// nitrovin, so it is logged too.
func (a *api) problem(w http.ResponseWriter, p *contract.Problem) {
	if p.Status == http.StatusInternalServerError {
		a.log.Print(p)
	}
	b, err := json.Marshal(p)
	if err != nil { // a Problem is strings and an int: unreachable
		panic(err)
	}
	a.write(w, p.Status, "application/problem+json", append(b, '\n'))
}

func (a *api) write(w http.ResponseWriter, status int, contentType string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body) // an error means the client is gone: no one is left to tell
}
