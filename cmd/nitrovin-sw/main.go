//go:build js && wasm

// Command nitrovin-sw is the browser's Service Worker module (ADR 0013):
// package ui's vocabulary pages, the ones `nitrovin serve` runs, over a
// ui.Library of the files the browser holds and the server's. It is built
// with Go's toolchain for js/wasm by scripts/sw.sh, which `nitrovin serve`
// embeds and publishes at /ui/r/<release>/sw.wasm; pkg/ui's worker.js
// loads it. It gives the Library the browser: IndexedDB through
// self.nitrovinDisk and the server through self.nitrovinGet, both
// worker.js's, and sets self.nitrovin:
//
//	serve(method, url, hxRequest, hxHistoryRestore, body) → Promise<{status, headers, body}>
//	load([{name, data: Uint8Array}], progress(text)) → Promise<{ok, message}>
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall/js"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/ui"
)

var logger = log.New(os.Stderr, "nitrovin-sw: ", 0)

func main() {
	config := js.Global().Get("JSON").Call("stringify", js.Global().Get("nitrovinConfig")).String()
	info, offer, err := ui.ReadWorkerConfig([]byte(config))
	if err != nil {
		logger.Fatalf("reading the worker's config: %v", err)
	}
	w := &worker{info: info, offer: offer}
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
// again after a failure, and the vocabulary the server offers.
type worker struct {
	info  contract.Info
	offer *ui.Offer
	mu    sync.Mutex
	lib   *ui.Library
	h     *ui.Handler
}

func (w *worker) open() (*ui.Handler, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.h != nil {
		return w.h, nil
	}
	start := time.Now()
	lib, err := ui.OpenLibrary(w.info, remote{}, disk{})
	if err != nil {
		return nil, fmt.Errorf("opening the browser's files: %w", err)
	}
	logger.Printf("opened %d stored files in %v", len(lib.Files()), time.Since(start).Round(time.Millisecond))
	w.lib, w.h = lib, ui.NewInBrowser(lib, w.offer, logger)
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
		rec.h.Set("Server-Timing", w.lib.Opened()+fmt.Sprintf(", serve;dur=%.1f", float64(time.Since(start).Microseconds())/1000))
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

// disk is IndexedDB, through worker.js's self.nitrovinDisk.
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

// remote is the server, through worker.js's self.nitrovinGet.
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
