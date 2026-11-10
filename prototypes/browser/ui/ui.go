// Package ui is the PROTOTYPE /ui handlers (#147): html/template pages and
// htmx fragments, one codebase run twice. `serve` (cmd/serve) runs them in
// Go against the artifact and --vocab files; the Service Worker runs the
// same Handler compiled to WebAssembly (wasm/sw) against the spine bundle and
// the vocabularies held in IndexedDB. A request with HX-Request gets a
// fragment, one without it a full page, so every link and form works with
// JavaScript off. Throwaway: rough markup, no tests.
package ui

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

//go:embed ui.html
var files embed.FS

var tpl = template.Must(template.New("").Funcs(template.FuncMap{
	"json": func(v any) string {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err.Error()
		}
		return string(b)
	},
	"asVocab": func(e contract.ListEntry) *contract.VocabularyEntry {
		if v, ok := e.(contract.VocabularyEntry); ok {
			return &v
		}
		return nil
	},
	"asSpine": func(e contract.ListEntry) *contract.SpineEntry {
		if v, ok := e.(contract.SpineEntry); ok {
			return &v
		}
		return nil
	},
	"unconf": func(u contract.Unconfirmed) string {
		var parts []string
		for _, e := range u.Elements {
			parts = append(parts, "element "+strconv.Itoa(int(e)))
		}
		if u.ModelYear {
			parts = append(parts, "model year")
		}
		return strings.Join(parts, ", ")
	},
	"has": func(vs []string, s string) bool { return slices.Contains(vs, s) },
	"join": func(fs []string) string { return strings.Join(fs, ", ") },
	"years": func(f, l contract.Null[int32]) string {
		switch {
		case !f.Valid && !l.Valid:
			return "any year"
		case f.Valid && l.Valid && f.V == l.V:
			return strconv.Itoa(int(f.V))
		}
		s := func(n contract.Null[int32]) string {
			if !n.Valid {
				return "…"
			}
			return strconv.Itoa(int(n.V))
		}
		return s(f) + "–" + s(l)
	},
	"ref":   refText,
	"short": func(s string) string { return s[:min(12, len(s))] },
	"kb":    func(n int) string { return strconv.FormatFloat(float64(n)/1024, 'f', 0, 64) + " KB" },
}).ParseFS(files, "ui.html"))

// Backend translates and lists: a *nitrovin.Translator on the server, the
// spine bundle's tr.Local in the browser.
type Backend interface {
	Translate(contract.TranslateRequest) (contract.Translation, error)
	List(contract.ListRequest) (contract.List, error)
}

// FileRow is one vocabulary held in this browser.
type FileRow struct {
	Key, Name, Kind, Version, Blake3 string
	Entries, Drift, Bytes            int
}

// Handler serves /ui/* (and /, /vocab on the server).
type Handler struct {
	// Browser is true in the Service Worker.
	Browser bool
	Month   string
	B       Backend
	// Decode is the server's decoder; nil in the browser, where /ui/decode
	// goes to the network.
	Decode func(contract.Request) (contract.Decode, error)
	// Files and Remove are the browser's IndexedDB store; nil on the server.
	Files  func() []FileRow
	Remove func(key string) error
	// Server is what the server has loaded besides vPIC (--vocab), for the
	// page's "rendered by" line.
	Server []string
}

// CSP is every /ui page's policy. WebAssembly runs in the Service Worker,
// whose own script response carries 'wasm-unsafe-eval'; no page needs it.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

type page struct {
	Title   string
	Page    string // decode | vocab
	H       *Handler
	Where   string
	Decode  *decodeView
	Vocab   *vocabView
	Problem string
}

type decodeView struct {
	VIN     string
	Year    string
	Dec     *contract.Decode
	Rows    [][2]string
	Problem string
}

type vocabView struct {
	Catalog   []contract.CatalogEntry
	Form      url.Values
	Result    *contract.Translation
	Problem   string
	Listing   *listView
	Files     []FileRow
	Browser   bool
	Month     string
	Server    []string
	Took      string
	FromVocab string
}

type listView struct {
	Of      string
	Entries []contract.ListEntry
	Next    string
	Problem string
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", CSP)
	w.Header().Set("Vary", "HX-Request")
	// htmx restores history with HX-Request too: that one wants the page.
	frag := r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-History-Restore-Request") != "true"
	q := r.URL.Query()
	switch r.URL.Path {
	case "/", "/ui/decode":
		if h.Decode == nil {
			http.NotFound(w, r)
			return
		}
		dv := h.decode(q)
		if frag {
			h.render(w, "decode-result", dv, http.StatusOK)
			return
		}
		h.render(w, "page", &page{Title: "Decode a VIN", Page: "decode", H: h, Decode: dv}, http.StatusOK)
	case "/vocab", "/ui/vocab":
		v := h.vocab(q, q.Has("from")) // the worker re-renders a /ui/translate page whole
		if frag {
			h.render(w, "vocab-app", v, http.StatusOK)
			return
		}
		h.render(w, "page", &page{Title: "Vocabularies", Page: "vocab", H: h, Vocab: v}, http.StatusOK)
	case "/ui/translate":
		v := h.vocab(q, true)
		if frag {
			h.render(w, "translate-result", v, http.StatusOK)
			return
		}
		h.render(w, "page", &page{Title: "Translate", Page: "vocab", H: h, Vocab: v}, http.StatusOK)
	case "/ui/vocabularies":
		v := h.vocab(url.Values{}, false)
		v.Listing = h.list(q)
		if frag {
			h.render(w, "listing", v.Listing, http.StatusOK)
			return
		}
		h.render(w, "page", &page{Title: "Vocabularies", Page: "vocab", H: h, Vocab: v}, http.StatusOK)
	case "/ui/files":
		if !h.Browser {
			h.needsJS(w, frag)
			return
		}
		v := h.vocab(url.Values{}, false)
		if frag {
			h.render(w, "files", v, http.StatusOK)
			return
		}
		h.render(w, "page", &page{Title: "Vocabularies", Page: "vocab", H: h, Vocab: v}, http.StatusOK)
	case "/ui/files/remove":
		if !h.Browser {
			h.needsJS(w, frag)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if err := h.Remove(form.Get("key")); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		v := h.vocab(url.Values{}, false)
		if frag {
			h.render(w, "vocab-app", v, http.StatusOK)
			return
		}
		w.Header().Set("Location", "/vocab")
		w.WriteHeader(http.StatusSeeOther)
	default:
		http.NotFound(w, r)
	}
}

// needsJS answers the server's /ui/files: the store is the browser's.
func (h *Handler) needsJS(w http.ResponseWriter, frag bool) {
	p := &page{Title: "Needs JavaScript", Page: "vocab", H: h, Vocab: h.vocab(url.Values{}, false),
		Problem: "Vocabularies held in your browser need JavaScript: they live in IndexedDB and are translated by WebAssembly in a Service Worker, so they are never uploaded. This server holds none of them."}
	if frag {
		h.render(w, "problem", p.Problem, http.StatusOK)
		return
	}
	h.render(w, "page", p, http.StatusNotFound)
}

func (h *Handler) render(w http.ResponseWriter, name string, data any, status int) {
	var b bytes.Buffer
	if err := tpl.ExecuteTemplate(&b, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

func (h *Handler) decode(q url.Values) *decodeView {
	dv := &decodeView{VIN: strings.TrimSpace(q.Get("vin")), Year: q.Get("model_year")}
	if dv.VIN == "" {
		return dv
	}
	query := url.Values{}
	if dv.Year != "" {
		query.Set("model_year", dv.Year)
	}
	req, err := contract.ParseQuery(dv.VIN, query)
	if err == nil {
		req.Now = time.Now()
		var dec contract.Decode
		dec, err = h.Decode(req)
		dv.Dec = &dec
	}
	if err != nil {
		dv.Problem = contract.AsProblem(err).Detail
		dv.Dec = nil
		return dv
	}
	if attrs, ok := dv.Dec.Attributes.(contract.VpicAttributes); ok {
		for _, k := range slices.Sorted(func(yield func(string) bool) {
			for k := range attrs {
				if !yield(k) {
					return
				}
			}
		}) {
			if v := attrs[k]; v != "" {
				dv.Rows = append(dv.Rows, [2]string{k, v})
			}
		}
	}
	return dv
}

// vocab is the /vocab app: the catalog, the translate form (and its result
// when translate), the browser's files.
func (h *Handler) vocab(q url.Values, translate bool) *vocabView {
	v := &vocabView{Form: q, Browser: h.Browser, Month: h.Month, Server: h.Server, FromVocab: q.Get("from")}
	if l, err := h.B.List(contract.ListRequest{}); err == nil {
		for _, e := range l.Entries {
			if c, ok := e.(contract.CatalogEntry); ok {
				v.Catalog = append(v.Catalog, c)
			}
		}
	}
	if h.Files != nil {
		v.Files = h.Files()
	}
	if !translate {
		return v
	}
	req, err := translateRequest(q)
	if err == nil {
		start := time.Now()
		var t contract.Translation
		t, err = h.B.Translate(req)
		v.Took = time.Since(start).String()
		v.Result = &t
	}
	if err != nil {
		v.Result = nil
		v.Problem = problemText(err)
	}
	return v
}

func problemText(err error) string {
	var p *contract.Problem
	if errors.As(err, &p) {
		if p.Pointer != "" {
			return p.Pointer + ": " + p.Detail
		}
		return p.Detail
	}
	return err.Error()
}

// translateRequest reads the translate form: from = "vin" or a vocabulary
// name; vin; make, model and fields ("field=value; …") for an entry;
// model_year; to (repeated).
func translateRequest(q url.Values) (contract.TranslateRequest, error) {
	var req contract.TranslateRequest
	vin := strings.TrimSpace(q.Get("vin"))
	switch from := q.Get("from"); from {
	case "", "vin":
		if vin == "" {
			return req, contract.NewProblem(contract.InvalidRequest, "/from", "give a VIN, or pick a vocabulary to translate an entry from")
		}
		req.From.VIN = vin
	default:
		req.From.Vocabulary = from
		req.From.Entry = map[string]string{}
		for _, k := range []string{"make", "model"} {
			if s := strings.TrimSpace(q.Get(k)); s != "" {
				req.From.Entry[k] = s
			}
		}
		for kv := range strings.SplitSeq(q.Get("fields"), ";") {
			if k, val, ok := strings.Cut(kv, "="); ok && strings.TrimSpace(k) != "" {
				req.From.Entry[strings.TrimSpace(k)] = strings.TrimSpace(val)
			}
		}
		req.VIN = vin
	}
	if y := strings.TrimSpace(q.Get("model_year")); y != "" {
		n, err := strconv.ParseInt(y, 10, 32)
		if err != nil {
			return req, contract.NewProblem(contract.InvalidRequest, "/model_year", "model year must be a number")
		}
		req.ModelYear = contract.Value(int32(n))
	}
	for _, to := range q["to"] {
		if to != "" {
			req.To = append(req.To, to)
		}
	}
	req.Now = time.Now()
	return req, req.Validate()
}

func (h *Handler) list(q url.Values) *listView {
	lv := &listView{Of: q.Get("of")}
	if lv.Of == "" {
		return lv
	}
	where := map[string]string{}
	if m := q.Get("make"); m != "" {
		where["make"] = m
	}
	l, err := h.B.List(contract.ListRequest{Of: lv.Of, Where: where, Limit: 25, After: q.Get("after")})
	if err != nil {
		lv.Problem = problemText(err)
		return lv
	}
	lv.Entries, lv.Next = l.Entries, l.Next
	return lv
}

func refText(r contract.SpineRef) string {
	switch {
	case r.Make != 0:
		return "vpic make " + strconv.Itoa(int(r.Make))
	case r.Model != 0:
		return "vpic model " + strconv.Itoa(int(r.Model))
	case r.Ext != "":
		return r.Ext
	default:
		return r.Spine + ":" + r.ID
	}
}
