//go:build js && wasm

// Command sw is the PROTOTYPE Service Worker module (#147): package ui's
// handlers, the ones `serve` runs, over the spine bundle and the files the
// worker keeps in IndexedDB. static/sw.js loads it and calls
// globalThis.nitrovin: setSpine, index, load, remove, serve.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/prototypes/browser/state"
	"github.com/joshuasprow/nitrovin/prototypes/browser/tr"
	"github.com/joshuasprow/nitrovin/prototypes/browser/ui"
)

var st state.State

// backend is the browser's Backend: a VIN is decoded by the server's
// /v1/decode, fetched from the worker, then translated here.
type backend struct{}

func (backend) Translate(req contract.TranslateRequest) (contract.Translation, error) {
	l := st.Local()
	if l == nil {
		return contract.Translation{}, errors.New("the spine bundle is not loaded")
	}
	var dec *tr.Decoded
	if vin := req.From.VIN + req.VIN; vin != "" {
		var err error
		if dec, err = decode(vin, req.ModelYear); err != nil {
			return contract.Translation{}, err
		}
	}
	return l.Translate(req, dec)
}

func (backend) List(req contract.ListRequest) (contract.List, error) {
	l := st.Local()
	if l == nil {
		return contract.List{}, errors.New("the spine bundle is not loaded")
	}
	return l.List(req)
}

// decode GETs /v1/decode/{vin}?full=true through the worker's fetch.
func decode(vin string, year contract.Null[int32]) (*tr.Decoded, error) {
	u := "/v1/decode/" + url.PathEscape(vin) + "?full=true"
	if year.Valid {
		u += "&model_year=" + strconv.Itoa(int(year.V))
	}
	resp, err := await(js.Global().Call("fetch", u))
	if err != nil {
		return nil, err
	}
	text, err := await(resp.Call("text"))
	if err != nil {
		return nil, err
	}
	body := []byte(text.String())
	if !resp.Get("ok").Bool() {
		var p struct{ Detail string }
		_ = json.Unmarshal(body, &p)
		return nil, contract.NewProblem(contract.InvalidRequest, "/vin", p.Detail)
	}
	var d tr.Decoded
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// await blocks the calling goroutine on a JS promise.
func await(p js.Value) (js.Value, error) {
	ok, fail := make(chan js.Value, 1), make(chan js.Value, 1)
	then := js.FuncOf(func(_ js.Value, a []js.Value) any { ok <- a[0]; return nil })
	catch := js.FuncOf(func(_ js.Value, a []js.Value) any { fail <- a[0]; return nil })
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

// promise runs f in a goroutine and resolves with its result.
func promise(f func() (any, error)) js.Value {
	return js.Global().Get("Promise").New(js.FuncOf(func(_ js.Value, a []js.Value) any {
		resolve, reject := a[0], a[1]
		go func() {
			v, err := f()
			if err != nil {
				reject.Invoke(err.Error())
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	}))
}

func jsonValue(v any) any {
	b, _ := json.Marshal(v)
	return string(b)
}

// recorder is the ResponseWriter the worker hands the handler.
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

func main() {
	h := &ui.Handler{
		Browser: true,
		B:       backend{},
		Files: func() []ui.FileRow {
			rows := st.Rows()
			out := make([]ui.FileRow, len(rows))
			for i, r := range rows {
				out[i] = ui.FileRow(r)
			}
			return out
		},
		Remove: func(key string) error {
			if err := st.Remove(key); err != nil {
				return err
			}
			_, err := await(js.Global().Call("idbDelete", key))
			return err
		},
	}
	js.Global().Set("nitrovin", js.ValueOf(map[string]any{
		// setSpine(text) → timing JSON
		"setSpine": js.FuncOf(func(_ js.Value, a []js.Value) any {
			t, err := st.SetSpine([]byte(a[0].String()))
			if err != nil {
				t.Error = err.Error()
			}
			h.Month = st.Month()
			return jsonValue(t)
		}),
		// index([{key, text}]) → timing JSON
		"index": js.FuncOf(func(_ js.Value, a []js.Value) any {
			n := a[0].Length()
			recs := make([]state.Record, n)
			for i := range n {
				r := a[0].Index(i)
				recs[i] = state.Record{Key: r.Get("key").String(), Data: []byte(r.Get("text").String())}
			}
			t, err := st.Index(recs)
			if err != nil {
				t.Error = err.Error()
			}
			return jsonValue(t)
		}),
		// load(name, text) → timing JSON with key, or error
		"load": js.FuncOf(func(_ js.Value, a []js.Value) any {
			t, err := st.Load(a[0].String(), []byte(a[1].String()))
			if err != nil {
				t.Error = err.Error()
			}
			return jsonValue(t)
		}),
		// serve(method, url, hxRequest, hxHistoryRestore, body) → Promise<{status, headers, body}>
		"serve": js.FuncOf(func(_ js.Value, a []js.Value) any {
			method, u, hx, restore, body := a[0].String(), a[1].String(), a[2].String(), a[3].String(), a[4].String()
			return promise(func() (any, error) {
				req, err := http.NewRequest(method, u, io.NopCloser(strings.NewReader(body)))
				if err != nil {
					return nil, err
				}
				if hx != "" {
					req.Header.Set("HX-Request", hx)
				}
				if restore != "" {
					req.Header.Set("HX-History-Restore-Request", restore)
				}
				rec := &recorder{h: http.Header{}}
				h.ServeHTTP(rec, req)
				headers := map[string]any{}
				for k := range rec.h {
					headers[k] = rec.h.Get(k)
				}
				return map[string]any{"status": rec.status, "headers": headers, "body": rec.body.String()}, nil
			})
		}),
	}))
	select {}
}
