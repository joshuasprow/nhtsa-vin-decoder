package main

import (
	"context"
	_ "embed"
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
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
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
		synopsis: "[--addr ADDR]",
		summary:  "Serve the HTTP API (docs/api/openapi.yaml) and a decode form at / on --addr, then :$PORT, then 127.0.0.1:8080, once the data has loaded.",
		setup: func(fs *flag.FlagSet) func(*env, []string) error {
			addr := fs.String("addr", "", "listen on `address` (default :$PORT when PORT is set, else 127.0.0.1:8080)")
			return func(e *env, args []string) error {
				if len(args) > 0 {
					return usageError("serve takes no arguments")
				}
				return serve(e, listenAddr(*addr, e.getenv))
			}
		},
	})
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

// serve loads the data, then listens on addr and serves the API until
// SIGINT or SIGTERM, when it stops accepting and lets requests in flight
// finish.
func serve(e *env, addr string) error {
	d, err := e.decoder()
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	logger := log.New(e.stderr, "nitrovin: ", 0)
	srv := &http.Server{
		Handler:           &api{d: d, log: logger},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          logger,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	logger.Printf("serving data %s on http://%s", d.Info().DataMonth, ln.Addr())

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

// decodeService is what the API answers with: a *nitrovin.Decoder.
type decodeService interface {
	Info() contract.Info
	Decode(contract.Request) (contract.Decode, error)
	DecodeBatch([]contract.Request) ([]contract.Decode, error)
}

// api serves the routes of docs/api/openapi.yaml, and the decode form at /
// (form.go). Every refusal but the form's own is an RFC 9457 problem; it
// sets no CORS headers.
type api struct {
	d   decodeService
	log *log.Logger
}

func (a *api) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			if v == http.ErrAbortHandler {
				panic(v)
			}
			a.log.Printf("panic serving %s %s: %v\n%s", r.Method, r.URL.EscapedPath(), v, debug.Stack())
			a.problem(w, contract.NewProblem(contract.Internal, "", fmt.Sprint("panic: ", v)))
		}
	}()
	path := r.URL.EscapedPath()
	allow, handle := a.route(path)
	switch {
	case handle == nil:
		a.problem(w, contract.NewProblem(contract.NotFound, "", "no route "+path))
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
// lists them, and its handler; a nil handler is no route. A VIN is one path
// segment, so a VIN holding "/" is sent as %2F.
func (a *api) route(path string) (string, http.HandlerFunc) {
	switch path {
	case "/":
		return "GET, HEAD", a.form
	case "/v1/decode":
		return http.MethodPost, a.decodeBatch
	case "/v1/info":
		return http.MethodGet, a.info
	case "/v1/openapi.json":
		return http.MethodGet, a.openAPI
	case "/healthz":
		return http.MethodGet, a.health
	}
	if vin, ok := strings.CutPrefix(path, "/v1/decode/"); ok && !strings.Contains(vin, "/") {
		return http.MethodGet, func(w http.ResponseWriter, r *http.Request) { a.decodeOne(w, r, vin) }
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
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		a.problem(w, contract.NewProblem(contract.InvalidRequest, "", "the query string is malformed: "+err.Error()))
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

// decodeBatch is POST /v1/decode. Its request is the body alone, so any
// query parameter is refused.
func (a *api) decodeBatch(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		a.problem(w, contract.NewProblem(contract.InvalidRequest, "", "POST /v1/decode takes no query parameters"))
		return
	}
	if r.ContentLength > contract.MaxBody {
		a.problem(w, contract.NewProblem(contract.TooLarge, "", fmt.Sprintf("the body is over %d bytes", contract.MaxBody)))
		return
	}
	// One byte over the limit is enough for ParseBatch to refuse it.
	body, err := io.ReadAll(io.LimitReader(r.Body, contract.MaxBody+1))
	if err != nil {
		a.problem(w, contract.NewProblem(contract.MalformedJSON, "", "reading the body: "+err.Error()))
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
