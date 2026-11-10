//go:build js && wasm

// Command core is the PROTOTYPE plain-JS variant's module (#147): no
// html/template, no net/http, JSON in and out. The page's script
// (static/vocab-plain.js) holds IndexedDB, fetches /v1/decode itself and
// renders the answer. Builds with Go and with TinyGo. globalThis.nitrovin:
// setSpine, index, load, remove, translate, list, files.
package main

import (
	"encoding/json"
	"syscall/js"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/prototypes/browser/state"
	"github.com/joshuasprow/nitrovin/prototypes/browser/tr"
)

var st state.State

func out(v any, err error) any {
	if err != nil {
		if p, ok := err.(*contract.Problem); ok {
			return tr.JSON(map[string]any{"problem": p})
		}
		return tr.JSON(map[string]string{"error": err.Error()})
	}
	return tr.JSON(v)
}

func timing(t state.Timing, err error) any {
	if err != nil {
		t.Error = err.Error()
	}
	return tr.JSON(t)
}

func main() {
	js.Global().Set("nitrovin", js.ValueOf(map[string]any{
		"setSpine": js.FuncOf(func(_ js.Value, a []js.Value) any {
			return timing(st.SetSpine([]byte(a[0].String())))
		}),
		"index": js.FuncOf(func(_ js.Value, a []js.Value) any {
			n := a[0].Length()
			recs := make([]state.Record, n)
			for i := range n {
				r := a[0].Index(i)
				recs[i] = state.Record{Key: r.Get("key").String(), Data: []byte(r.Get("text").String())}
			}
			return timing(st.Index(recs))
		}),
		"load": js.FuncOf(func(_ js.Value, a []js.Value) any {
			return timing(st.Load(a[0].String(), []byte(a[1].String())))
		}),
		"remove": js.FuncOf(func(_ js.Value, a []js.Value) any {
			return out(map[string]bool{"ok": true}, st.Remove(a[0].String()))
		}),
		"files": js.FuncOf(func(_ js.Value, a []js.Value) any {
			return tr.JSON(st.Rows())
		}),
		// translate(requestJSON, decodeJSON or "") → Translation JSON
		"translate": js.FuncOf(func(_ js.Value, a []js.Value) any {
			req, err := contract.ParseTranslateRequest([]byte(a[0].String()))
			if err != nil {
				return out(nil, err)
			}
			var dec *tr.Decoded
			if s := a[1].String(); s != "" {
				dec = &tr.Decoded{}
				if err := json.Unmarshal([]byte(s), dec); err != nil {
					return out(nil, err)
				}
			}
			return out(st.Local().Translate(req, dec))
		}),
		// list(requestJSON) → List JSON
		"list": js.FuncOf(func(_ js.Value, a []js.Value) any {
			req, err := contract.ParseListRequest([]byte(a[0].String()))
			if err != nil {
				return out(nil, err)
			}
			return out(st.Local().List(req))
		}),
	}))
	select {}
}
