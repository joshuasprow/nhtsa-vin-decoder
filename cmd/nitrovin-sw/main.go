//go:build js && wasm

// Command nitrovin-sw is the browser's Service Worker module (ADR 0013):
// package ui's vocabulary pages, the ones `nitrovin serve` runs, over a
// ui.Library of the files the browser holds and the server's. It is built
// with Go's toolchain for js/wasm by scripts/sw.sh, which `nitrovin serve`
// embeds and publishes at /ui/r/<release>/sw.wasm; pkg/ui's worker.js
// loads it. It gives the Library the browser: IndexedDB through
// self.nitrovinDisk (disk.js) and the server through self.nitrovinGet,
// and sets self.nitrovin:
//
//	serve(method, url, hxRequest, hxHistoryRestore, body) → Promise<{status, headers, body}>
//	load([{name, data: Uint8Array}], progress(text)) → Promise<{ok, message}>
//	compile(file: File, progress(text)) → Promise<{ok, message}>
//	reset() → the Library is opened again on its next request
//
// The same module runs in the catalog compiler, compile.js, a dedicated
// worker the page starts (ADR 0023), which calls compile: it reads the
// picked File in place, sorts in IndexedDB and streams the compiled file
// there. The Service Worker reads it back by range (Disk.Catalog).
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/ui"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

var logger = log.New(os.Stderr, "nitrovin-sw: ", 0)

func main() {
	config := js.Global().Get("JSON").Call("stringify", js.Global().Get("nitrovinConfig")).String()
	c, err := ui.ReadWorkerConfig([]byte(config))
	if err != nil {
		logger.Fatalf("reading the worker's config: %v", err)
	}
	w := &worker{config: c}
	js.Global().Set("nitrovin", js.ValueOf(map[string]any{
		"serve": js.FuncOf(func(_ js.Value, a []js.Value) any {
			method, url, hx, restore, body := a[0].String(), a[1].String(), a[2].String(), a[3].String(), a[4].String()
			return promise(func() (any, error) { return w.serve(method, url, hx, restore, body), nil })
		}),
		"load": js.FuncOf(func(_ js.Value, a []js.Value) any {
			files, progress := a[0], a[1]
			picked := make([]ui.Picked, files.Length())
			for i := range picked {
				f := files.Index(i)
				picked[i] = ui.Picked{FileName: f.Get("name").String(), Data: goBytes(f.Get("data"))}
			}
			return promise(func() (any, error) {
				msg, err := w.load(picked, func(s string) { progress.Invoke(s) })
				if err != nil {
					return map[string]any{"ok": false, "message": err.Error()}, nil
				}
				return map[string]any{"ok": true, "message": msg}, nil
			})
		}),
		"compile": js.FuncOf(func(_ js.Value, a []js.Value) any {
			file, progress := a[0], a[1]
			return promise(func() (any, error) {
				msg, err := w.compile(file, func(s string) { progress.Invoke(s) })
				if err != nil {
					return map[string]any{"ok": false, "message": err.Error()}, nil
				}
				return map[string]any{"ok": true, "message": msg}, nil
			})
		}),
		"reset": js.FuncOf(func(js.Value, []js.Value) any {
			w.reset()
			return nil
		}),
	}))
	js.Global().Call("nitrovinReady")
	go func() {
		if _, err := w.open(); err != nil {
			logger.Print(err)
		}
	}()
	select {}
}

// worker is the module's state: the Library, opened on first need and
// again after a failure, and what the server gave it for the pages.
type worker struct {
	config ui.BrowserConfig
	mu     sync.Mutex
	lib    *ui.Library
	h      *ui.Handler
}

func (w *worker) open() (*ui.Handler, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.h != nil {
		return w.h, nil
	}
	start := time.Now()
	lib, err := ui.OpenLibrary(w.config.Info, remote{}, disk{})
	if err != nil {
		return nil, fmt.Errorf("opening the browser's files: %w", err)
	}
	logger.Printf("opened %d stored files in %v", len(lib.Files()), time.Since(start).Round(time.Millisecond))
	w.lib, w.h = lib, ui.NewInBrowser(lib, w.config, logger)
	return w.h, nil
}

// serve answers one request through the pages.
func (w *worker) serve(method, url, hx, restore, body string) map[string]any {
	start := time.Now()
	rec := &recorder{h: http.Header{}}
	h, err := w.open()
	if err != nil {
		rec.h.Set("Content-Type", "text/plain; charset=utf-8")
		rec.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(rec, "The translator in this browser could not start: %v. Reload the page to try again.", err)
	} else {
		req, err := http.NewRequest(method, url, io.NopCloser(strings.NewReader(body)))
		if err != nil {
			rec.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(rec, err)
		} else {
			if method == http.MethodPost {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
			if hx != "" {
				req.Header.Set("HX-Request", hx)
			}
			if restore != "" {
				req.Header.Set("HX-History-Restore-Request", restore)
			}
			h.ServeHTTP(rec, req)
		}
	}
	if w.lib != nil {
		// What the module holds after answering, for the browser's half of
		// ADR 0023's budget: the Go heap in use, and the WebAssembly
		// memory it has taken from the browser, which never shrinks.
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		timing := w.lib.Opened() + fmt.Sprintf(", serve;dur=%.1f, heap;desc=%d, memory;desc=%d",
			float64(time.Since(start).Microseconds())/1000, m.HeapInuse, m.Sys)
		if d := w.lib.FitTook(); d > 0 {
			timing += fmt.Sprintf(", fit;dur=%.2f", float64(d.Microseconds())/1000)
		}
		rec.h.Set("Server-Timing", timing)
	}
	headers := map[string]any{}
	for k := range rec.h {
		headers[k] = rec.h.Get(k)
	}
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return map[string]any{"status": rec.status, "headers": headers, "body": rec.body.String()}
}

// load loads picked as one batch, and says what it loaded.
func (w *worker) load(picked []ui.Picked, progress func(string)) (string, error) {
	if _, err := w.open(); err != nil {
		return "", err
	}
	files, err := w.lib.Load(picked, progress)
	if err != nil {
		return "", err
	}
	var said []string
	for _, p := range picked {
		for _, f := range files {
			if f.FileName == p.FileName {
				said = append(said, f.Name+" ("+strconv.Itoa(f.Lines)+" lines)")
			}
		}
	}
	return "Loaded " + strings.Join(said, ", ") + ".", nil
}

// reset drops the Library, which the next request opens again: the
// browser's catalog changed under it (compiled by compile.js).
func (w *worker) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lib, w.h = nil, nil
}

// compile compiles file, a picked File, as the browser's catalog, and says
// what it compiled.
func (w *worker) compile(file js.Value, progress func(string)) (string, error) {
	if _, err := w.open(); err != nil {
		return "", err
	}
	// A compile that stopped half way (its tab closed) left its parts.
	if _, err := call("clearParts"); err != nil {
		return "", fmt.Errorf("clearing what an earlier compile left: %w", err)
	}
	start := time.Now()
	size := int64(file.Get("size").Float())
	name := file.Get("name").String()
	f, err := w.lib.Compile(bufio.NewReaderSize(&blob{v: file, size: size}, readBytes), name, size, &scratch{}, progress)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Compiled %s in %s.", ui.CatalogText(f), time.Since(start).Round(10*time.Millisecond))
	if len(f.Skipped) > 0 {
		msg += " Not checked here: " + f.SkippedText() + " drift; nitrovin vocab check on a server runs every check."
	}
	return msg, nil
}

// readBytes is how much of a picked catalog is read at a time, and
// partBytes the parts IndexedDB keeps a file being written in.
const (
	readBytes = 4 << 20
	partBytes = 1 << 20
)

// blob is a Blob of size bytes (a picked File, or a file IndexedDB keeps
// in parts), read by range: each read awaits a slice().arrayBuffer(). Read
// reads it front to back.
type blob struct {
	v        js.Value
	size, at int64
}

func (b *blob) ReadAt(p []byte, off int64) (int, error) {
	if off >= b.size {
		return 0, io.EOF
	}
	n := min(int64(len(p)), b.size-off)
	v, err := call("read", b.v, off, off+n)
	if err != nil {
		return 0, err
	}
	js.CopyBytesToGo(p[:n], v)
	if n < int64(len(p)) {
		return int(n), io.EOF
	}
	return int(n), nil
}

func (b *blob) Read(p []byte) (int, error) {
	n, err := b.ReadAt(p, b.at)
	b.at += int64(n)
	return n, err
}

// parts is a file IndexedDB keeps in parts of partBytes, written front to
// back (disk.js's putPart), then read in place as one Blob (parts).
type parts struct {
	key  string
	buf  []byte
	n    int
	size int64
	read *blob
}

func (f *parts) Write(p []byte) (int, error) {
	if f.read != nil {
		return 0, errors.New("written after it was read")
	}
	f.buf = append(f.buf, p...)
	for len(f.buf) >= partBytes {
		if err := f.put(f.buf[:partBytes]); err != nil {
			return 0, err
		}
		f.buf = append(f.buf[:0], f.buf[partBytes:]...)
	}
	return len(p), nil
}

func (f *parts) put(b []byte) error {
	if _, err := call("putPart", f.key, f.n, jsBytes(b)); err != nil {
		return err
	}
	f.n++
	f.size += int64(len(b))
	return nil
}

// written ends writing, and reads what was written in place.
func (f *parts) written() (*blob, error) {
	if f.read == nil {
		if len(f.buf) > 0 {
			if err := f.put(f.buf); err != nil {
				return nil, err
			}
		}
		f.buf = nil
		v, err := call("parts", f.key)
		if err != nil {
			return nil, err
		}
		f.read = &blob{v: v, size: f.size}
	}
	return f.read, nil
}

func (f *parts) drop() error {
	_, err := call("dropParts", f.key)
	f.buf, f.read = nil, nil
	return err
}

// scratch is the compile's vocab.Scratch: each file kept in parts.
type scratch struct{ n int }

func (s *scratch) Create() (vocab.ScratchFile, error) {
	s.n++
	return &scratchFile{parts{key: "scratch " + strconv.Itoa(s.n)}}, nil
}

type scratchFile struct{ parts }

func (f *scratchFile) ReadAt(p []byte, off int64) (int, error) {
	b, err := f.written()
	if err != nil {
		return 0, err
	}
	return b.ReadAt(p, off)
}

func (f *scratchFile) Close() error { return f.drop() }

// catalogWriter is a compiled catalog being stored (ui.CatalogWriter):
// its parts, committed as the browser's catalog in one transaction.
type catalogWriter struct{ parts }

func (w *catalogWriter) Written() (io.ReaderAt, error) { return w.written() }

func (w *catalogWriter) Commit(rec ui.Record) error {
	if _, err := w.written(); err != nil {
		return err
	}
	r, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	_, err = call("commitCatalog", w.key, js.Global().Get("JSON").Call("parse", string(r)))
	return err
}

func (w *catalogWriter) Abort() error { return w.drop() }

// recorder is the ResponseWriter the pages answer into.
type recorder struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.h }

func (r *recorder) WriteHeader(s int) {
	if r.status == 0 {
		r.status = s
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(b)
}

// disk is IndexedDB, through disk.js's self.nitrovinDisk.
type disk struct{}

func call(method string, args ...any) (js.Value, error) {
	return await(js.Global().Get("nitrovinDisk").Call(method, args...))
}

func (disk) Records() ([]ui.Record, error) {
	v, err := call("records")
	if err != nil {
		return nil, err
	}
	var out []ui.Record
	err = json.Unmarshal([]byte(js.Global().Get("JSON").Call("stringify", v).String()), &out)
	return out, err
}

func (disk) Index(blake3 string) ([]byte, error) {
	v, err := call("index", blake3)
	if err != nil || v.IsNull() {
		return nil, err
	}
	return goBytes(v), nil
}

func (disk) PutIndex(blake3 string, index []byte) error {
	_, err := call("putIndex", blake3, jsBytes(index))
	return err
}

func (disk) Text(blake3 string) ([]byte, error) {
	v, err := call("text", blake3)
	if err != nil {
		return nil, err
	}
	return goBytes(v), nil
}

func (disk) Selection() (ui.Selection, error) {
	v, err := call("selection")
	if err != nil {
		return nil, err
	}
	var out ui.Selection
	err = json.Unmarshal([]byte(js.Global().Get("JSON").Call("stringify", v).String()), &out)
	return out, err
}

func (disk) Change(put []ui.Stored, remove []string, selection ui.Selection) error {
	puts := make([]any, len(put))
	for i, p := range put {
		rec, err := json.Marshal(p.Record)
		if err != nil {
			return err
		}
		puts[i] = map[string]any{
			"record": js.Global().Get("JSON").Call("parse", string(rec)),
			"text":   jsBytes(p.Text),
			"index":  jsBytes(p.Index),
		}
	}
	names := make([]any, len(remove))
	for i, n := range remove {
		names[i] = n
	}
	if selection == nil {
		selection = ui.Selection{}
	}
	sel, err := json.Marshal(selection)
	if err != nil {
		return err
	}
	_, err = call("change", puts, names, js.Global().Get("JSON").Call("parse", string(sel)))
	return err
}

func (disk) Catalog() (ui.Record, io.ReaderAt, bool, error) {
	v, err := call("catalog")
	if err != nil || v.IsNull() || v.IsUndefined() {
		return ui.Record{}, nil, false, err
	}
	var rec ui.Record
	if err := json.Unmarshal([]byte(js.Global().Get("JSON").Call("stringify", v.Get("record")).String()), &rec); err != nil {
		return ui.Record{}, nil, false, err
	}
	b := v.Get("blob")
	return rec, &blob{v: b, size: int64(b.Get("size").Float())}, true, nil
}

func (disk) NewCatalog() (ui.CatalogWriter, error) {
	return &catalogWriter{parts{key: "catalog " + strconv.FormatInt(time.Now().UnixNano(), 36)}}, nil
}

func (disk) RemoveCatalog() error {
	_, err := call("removeCatalog")
	return err
}

// remote is the server, through self.nitrovinGet (worker.js's, or
// compile.js's).
type remote struct{}

func (remote) Get(path string) (int, []byte, error) {
	v, err := await(js.Global().Call("nitrovinGet", path))
	if err != nil {
		return 0, nil, err
	}
	return v.Get("status").Int(), goBytes(v.Get("body")), nil
}

func goBytes(v js.Value) []byte {
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}

func jsBytes(b []byte) js.Value {
	v := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(v, b)
	return v
}

// await blocks the calling goroutine on a JavaScript promise.
func await(p js.Value) (js.Value, error) {
	ok, fail := make(chan js.Value, 1), make(chan js.Value, 1)
	then := js.FuncOf(func(_ js.Value, a []js.Value) any { ok <- arg(a); return nil })
	catch := js.FuncOf(func(_ js.Value, a []js.Value) any { fail <- arg(a); return nil })
	defer then.Release()
	defer catch.Release()
	p.Call("then", then, catch)
	select {
	case v := <-ok:
		return v, nil
	case e := <-fail:
		return js.Undefined(), errors.New(e.Call("toString").String())
	}
}

func arg(a []js.Value) js.Value {
	if len(a) == 0 {
		return js.Undefined()
	}
	return a[0]
}

// promise runs f in a goroutine, resolving with its result.
func promise(f func() (any, error)) js.Value {
	var executor js.Func
	executor = js.FuncOf(func(_ js.Value, a []js.Value) any {
		resolve, reject := a[0], a[1]
		go func() {
			defer executor.Release()
			v, err := f()
			if err != nil {
				reject.Invoke(err.Error())
				return
			}
			resolve.Invoke(js.ValueOf(v))
		}()
		return nil
	})
	return js.Global().Get("Promise").New(executor)
}
