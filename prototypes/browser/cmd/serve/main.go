// Command serve is the PROTOTYPE server for #147, a stand-in for `nitrovin
// serve` with /ui: the decode form at /, /vocab, the /ui/* fragments
// (package ui), /v1/decode/{vin}, the spine bundle, the Service Worker and
// the plain-JS variant at /vocab-js. Run it through ../../run.sh.
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
	"github.com/joshuasprow/nitrovin/prototypes/browser/spine"
	"github.com/joshuasprow/nitrovin/prototypes/browser/ui"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8147", "listen address")
	dir := flag.String("dir", "prototypes/browser", "the prototype's directory (static/, build/)")
	core := flag.String("core", "go", "toolchain of the plain-JS variant's module: go or tinygo")
	vocabs := flag.String("vocab", "", "comma-separated vocabulary files the server loads (as --vocab)")
	flag.Parse()

	d, err := nitrovin.Embedded()
	if err != nil {
		log.Fatal(err)
	}
	var files []*vocab.File
	var names []string
	if *vocabs != "" {
		if files, err = vocab.Open(strings.Split(*vocabs, ",")...); err != nil {
			log.Fatal(err)
		}
		for _, f := range files {
			names = append(names, f.Name)
		}
	}
	t, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	bundle, err := spineBundle(d)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("spine bundle %s: %d bytes in %s", d.Info().DataMonth, len(bundle), time.Since(start))

	h := &ui.Handler{Month: d.Info().DataMonth, B: t, Decode: d.Decode, Server: names}
	static := filepath.Join(*dir, "static")
	build := filepath.Join(*dir, "build")
	mux := http.NewServeMux()
	mux.Handle("/", h)
	mux.Handle("/ui/", h)
	file := func(path, ctype, csp, cache string) http.Handler {
		return &asset{path: path, ctype: ctype, csp: csp, cache: cache}
	}
	const js, wasm = "text/javascript; charset=utf-8", "application/wasm"
	// The worker's own policy: it compiles WebAssembly, the pages don't.
	const swCSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self'"
	mux.Handle("GET /sw.js", file(filepath.Join(static, "sw.js"), js, swCSP, "no-cache"))
	for _, name := range []string{"htmx-2.0.11.min.js", "vocab.js", "vocab-plain.js", "idb.js"} {
		mux.Handle("GET /ui/"+name, file(filepath.Join(static, name), js, "", "no-cache"))
	}
	mux.Handle("GET /ui/proto.css", file(filepath.Join(static, "proto.css"), "text/css; charset=utf-8", "", "no-cache"))
	mux.Handle("GET /ui/wasm_exec.js", file(filepath.Join(build, "go", "wasm_exec.js"), js, "", "no-cache"))
	mux.Handle("GET /ui/sw.wasm", file(filepath.Join(build, "go", "sw.wasm"), wasm, "", "no-cache"))
	for _, tc := range []string{"go", "tinygo"} {
		mux.Handle("GET /ui/core/"+tc+"/wasm_exec.js", file(filepath.Join(build, tc, "wasm_exec.js"), js, "", "no-cache"))
		mux.Handle("GET /ui/core/"+tc+"/core.wasm", file(filepath.Join(build, tc, "core.wasm"), wasm, "", "no-cache"))
	}
	mux.Handle("GET /ui/spine.json", &asset{data: bundle, ctype: "application/json", cache: "no-cache", etag: `"` + d.Info().DataMonth + `"`})
	// The plain-JS variant compiles WebAssembly on the page: its policy
	// needs 'wasm-unsafe-eval'. ?core=go|tinygo picks the module.
	mux.HandleFunc("GET /vocab-js", func(w http.ResponseWriter, r *http.Request) {
		tc := r.URL.Query().Get("core")
		if tc != "go" && tc != "tinygo" {
			tc = *core
		}
		page, err := os.ReadFile(filepath.Join(static, "vocab-plain.html"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		_, _ = w.Write(bytes.ReplaceAll(page, []byte("CORE_TC"), []byte(tc)))
	})
	mux.HandleFunc("GET /v1/decode/{vin}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		req, err := contract.ParseQuery(r.PathValue("vin"), r.URL.Query())
		if err == nil {
			req.Now = time.Now()
			var dec contract.Decode
			if dec, err = d.Decode(req); err == nil {
				_ = json.NewEncoder(w).Encode(dec)
				return
			}
		}
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(contract.AsProblem(err))
	})
	log.Printf("serving on http://%s (plain-JS variant module: %s)", *addr, *core)
	log.Fatal(http.ListenAndServe(*addr, logged(mux)))
}

// logged logs each request, so the run shows what reached the server: a
// file loaded into the browser never does.
func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s hx=%q body=%d", r.Method, r.URL.RequestURI(), r.Header.Get("HX-Request"), r.ContentLength)
		h.ServeHTTP(w, r)
	})
}

// asset serves one file (read on each request, so a rebuild needs no
// restart) or fixed bytes, gzipped when the client takes it.
type asset struct {
	path, ctype, csp, cache, etag string
	data                          []byte

	mu   sync.Mutex
	gz   []byte
	from []byte
}

func (a *asset) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data := a.data
	if data == nil {
		var err error
		if data, err = os.ReadFile(a.path); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("Cache-Control", a.cache)
	h.Set("Vary", "Accept-Encoding")
	if a.csp != "" {
		h.Set("Content-Security-Policy", a.csp)
	}
	if a.etag != "" {
		h.Set("ETag", a.etag)
		if r.Header.Get("If-None-Match") == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		data = a.gzipped(data)
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (a *asset) gzipped(data []byte) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gz == nil || !bytes.Equal(a.from, data) {
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		_, _ = zw.Write(data)
		_ = zw.Close()
		a.gz, a.from = b.Bytes(), data
	}
	return a.gz
}

// spineBundle is the throwaway spine bundle of the embedded month: what
// pkg/nitrovin's month (month.go) reads, written out for the browser.
func spineBundle(d *nitrovin.Decoder) ([]byte, error) {
	a, err := artifact.Open(embedded.Artifact())
	if err != nil {
		return nil, err
	}
	m := d.Month()
	doc := spine.Doc{Month: m.Name(), Values: map[string][]string{}}
	public := map[int32]bool{}
	for _, e := range d.Info().Elements {
		lookup := int32(0)
		if e.DataType == "lookup" {
			lookup = 1
		}
		doc.Elements = append(doc.Elements, [2]int32{e.ElementID, lookup})
		public[e.ElementID] = true
	}
	for id, name := range m.Makes() {
		doc.Makes = append(doc.Makes, spine.MakeRow{ID: id, Name: name})
		for mid, mname := range m.Models(id) {
			doc.Models = append(doc.Models, spine.ModelRow{ID: mid, Make: id, Name: mname})
		}
	}
	values := map[int32]map[string]bool{}
	add := func(el int32, v string) {
		if !public[el] {
			return
		}
		if values[el] == nil {
			values[el] = map[string]bool{}
		}
		values[el][v] = true
	}
	for _, src := range []struct{ table, column string }{
		{"pattern", "attributeid"},
		{"enginemodelpattern", "attributeid"},
		{"vehiclespecpattern", "attributeid"},
		{"defaultvalue", "defaultvalue"},
	} {
		t := a.Table(src.table)
		vs := t.Strings(src.column)
		for r, el := range t.Int32s("elementid") {
			if v, ok := vs.At(r); ok {
				add(el, v)
			}
		}
	}
	ids := func(el int32, table, column string) {
		for _, id := range a.Table(table).Int32s(column) {
			if id != artifact.NullInt32 {
				add(el, strconv.Itoa(int(id)))
			}
		}
	}
	ids(26, "wmi", "makeid")
	ids(26, "wmi_make", "makeid")
	ids(26, "make_model", "makeid")
	ids(27, "wmi", "manufacturerid")
	ids(39, "wmi", "vehicletypeid")
	for el, vs := range values {
		list := make([]string, 0, len(vs))
		for v := range vs {
			list = append(list, v)
		}
		doc.Values[strconv.Itoa(int(el))] = list
	}
	doc.Sorted()
	return json.Marshal(doc)
}
