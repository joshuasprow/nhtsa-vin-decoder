package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// The vocabulary pages (ADR 0013). /vocab is the entry page, the server's:
// its files, and the visitor's own, which need JavaScript. /ui/translate
// translates, its query GET /v1/translate's; /ui/vocabularies lists the
// catalog and /ui/vocabularies/{name} one file's entries, their queries
// GET /v1/vocabularies's. Each answers the same on the server and in the
// browser's Service Worker, which answers only the requests naming a file
// the browser holds and forwards the rest: so a link means one thing with
// JavaScript on or off, and a link naming a browser file, opened where the
// file is not, says where it lives. /ui/files lists the browser's files
// and /ui/files/remove drops some: the worker's alone, which the server
// answers by saying so. /sw.js is the worker itself, the only response
// whose CSP allows WebAssembly.

const (
	vocabPath        = "/vocab"
	translatePath    = "/ui/translate"
	vocabulariesPath = "/ui/vocabularies"
	filesPath        = "/ui/files"
	removePath       = "/ui/files/remove"
	swPath           = "/sw.js"
	// moduleName and execName are the worker's module and Go's
	// wasm_exec.js among a release's assets.
	moduleName = "sw.wasm"
	execName   = "wasm_exec.js"
	// filesTarget is the browser files' panel on /vocab.
	filesTarget = "#browser-files"
)

// workerCSP is /sw.js's Content-Security-Policy, the worker's: the
// server's own scripts and connections, and WebAssembly, which no page is
// allowed.
const workerCSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

// needsBrowser is what the server says of the browser's own files.
const needsBrowser = "Files you load are kept in your browser (IndexedDB) and read there by WebAssembly in a Service Worker, so they are never uploaded: " +
	"this page needs JavaScript, and a page the Service Worker controls. This server holds none of them. Open /vocab to load files."

func (h *Handler) vocabRoutes() {
	h.routes[translatePath] = h.translate
	h.routes[vocabulariesPath] = h.catalogPage
	h.routes[filesPath] = h.files
	h.posts[removePath] = h.remove
	if h.b != nil {
		h.routes[vocabPath] = h.vocab
	}
}

// catalog is what is loaded, vPIC first.
func (h *Handler) catalog() ([]contract.CatalogEntry, error) {
	l, err := h.p.List(contract.ListRequest{})
	if err != nil {
		return nil, err
	}
	var out []contract.CatalogEntry
	for _, e := range l.Entries {
		if c, ok := e.(contract.CatalogEntry); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// notHere refuses, on the server, a request naming what it has not loaded:
// a file loaded in a browser stays there.
func (h *Handler) notHere(names []string, catalog []contract.CatalogEntry) error {
	if h.b == nil {
		return nil
	}
	for _, n := range names {
		if !slices.ContainsFunc(catalog, func(c contract.CatalogEntry) bool { return c.Name == n }) {
			return &pageError{Status: http.StatusNotFound, Message: fmt.Sprintf(
				"Nothing named %q is loaded on this nitrovin. A file loaded into a browser stays in that browser: open this link in the browser that holds it.", n)}
		}
	}
	return nil
}

// named is what a translate query names: the from's vocabulary, spine or
// extension set, and the targets. worker.js reads the same parameters to
// tell the requests it answers.
func named(q url.Values) []string {
	var out []string
	for _, k := range []string{"from.vocabulary", "from.spine"} {
		if v := q.Get(k); v != "" {
			out = append(out, v)
		}
	}
	if ext, ok := strings.CutPrefix(q.Get("from.ext"), "ext:"); ok {
		set, _, _ := strings.Cut(ext, "/")
		out = append(out, set)
	}
	return append(out, q["to"]...)
}

// vocabPage is /vocab: the server's catalog, and whether the server has
// the worker to load the visitor's own files.
type vocabPage struct {
	Catalog []contract.CatalogEntry
	Worker  bool
}

func (h *Handler) vocab(*http.Request) (view, error) {
	cat, err := h.catalog()
	if err != nil {
		return view{}, err
	}
	return view{title: "Vocabularies", template: "vocab", vocab: true, data: vocabPage{Catalog: cat, Worker: h.worker != nil}}, nil
}

// translatePage is /ui/translate: its forms, and the translation asked for.
type translatePage struct {
	// Vocabularies are the catalog's vocabularies, the targets.
	Vocabularies []contract.CatalogEntry
	// From is the vocabulary an entry is translated from, nil before one
	// is chosen; Fields its entry's fields with the query's values.
	From   *contract.CatalogEntry
	Fields []fieldValue
	// FromVIN, VIN and ModelYear are the query's.
	FromVIN, VIN, ModelYear string
	To                      []string
	Result                  *translationView
}

type fieldValue struct{ Name, Value string }

// target is a vocabulary the form offers as a target, and whether the
// query names it.
type target struct {
	Name    string
	Checked bool
}

// Targets are the vocabularies a translation from From, or from a VIN,
// can reach: those on its spine.
func (p translatePage) Targets() []target {
	spine := "vpic"
	if p.From != nil {
		spine = p.From.Spine
	}
	var out []target
	for _, v := range p.Vocabularies {
		if v.Spine == spine {
			out = append(out, target{v.Name, slices.Contains(p.To, v.Name)})
		}
	}
	return out
}

func (h *Handler) translate(r *http.Request) (view, error) {
	q := url.Values{}
	for k, vs := range r.URL.Query() {
		for _, v := range vs {
			if strings.TrimSpace(v) != "" {
				q[k] = append(q[k], v)
			}
		}
	}
	cat, err := h.catalog()
	if err != nil {
		return view{}, err
	}
	if err := h.notHere(named(q), cat); err != nil {
		return view{}, err
	}
	p := translatePage{FromVIN: q.Get("from.vin"), VIN: q.Get("vin"), ModelYear: q.Get("model_year"), To: q["to"]}
	for _, c := range cat {
		if c.Kind == "vocabulary" {
			p.Vocabularies = append(p.Vocabularies, c)
		}
	}
	// By name: where a file lives (the server, the browser) changes the
	// catalog's load order, never the form.
	slices.SortFunc(p.Vocabularies, func(a, b contract.CatalogEntry) int { return strings.Compare(a.Name, b.Name) })
	entry := false
	if name := q.Get("from.vocabulary"); name != "" {
		if i := slices.IndexFunc(p.Vocabularies, func(c contract.CatalogEntry) bool { return c.Name == name }); i >= 0 {
			p.From = &p.Vocabularies[i]
			for _, f := range p.From.Fields {
				v := q.Get("from.entry." + f)
				p.Fields = append(p.Fields, fieldValue{f, v})
				entry = entry || v != ""
			}
		}
	}
	ask := entry || q.Has("from.vin") || q.Has("from.make") || q.Has("from.model") || q.Has("from.ext") || q.Has("from.spine")
	if !ask {
		return view{title: "Translate", template: "translate", data: p}, nil
	}
	req, err := contract.ParseTranslateQuery(q)
	if err != nil {
		return view{}, err
	}
	tr, err := h.p.Translate(req)
	if err != nil {
		return view{}, err
	}
	p.Result = newTranslationView(tr, cat)
	return view{title: "Translate", template: "translate", data: p}, nil
}

// translationView is a translation in words.
type translationView struct {
	From, Status, Note, Vehicle string
	Spine                       []string
	To                          []targetView
	Files                       []string
	DataMonth, DecoderVersion   string
}

type targetView struct {
	Vocabulary string
	Entries    []string
}

func newTranslationView(tr contract.Translation, cat []contract.CatalogEntry) *translationView {
	fields := func(name string, entry map[string]string) string {
		i := slices.IndexFunc(cat, func(c contract.CatalogEntry) bool { return c.Name == name })
		var order []string
		if i >= 0 {
			order = cat[i].Fields
		} else {
			order = slices.Sorted(maps.Keys(entry))
		}
		var vs []string
		for _, f := range order {
			vs = append(vs, entry[f])
		}
		return strings.Join(vs, " · ")
	}
	v := &translationView{Status: string(tr.From.Status), Note: tr.From.Note, DataMonth: tr.DataMonth, DecoderVersion: tr.DecoderVersion}
	switch f := tr.From; {
	case f.VIN != "":
		v.From = "VIN " + f.VIN
	case f.Vocabulary != "":
		v.From = f.Vocabulary + ": " + fields(f.Vocabulary, f.Entry)
	default:
		v.From = refText(f.SpineRef)
	}
	if x := tr.Vehicle; x != nil {
		v.Vehicle = "VIN " + x.VIN + " decodes to " + nullText("make", x.Make) + ", " + nullText("model", x.Model) + ", " + nullText("model year", x.ModelYear)
	}
	for _, s := range tr.Spine {
		line := refText(s.SpineRef)
		if s.Name != "" {
			line += " " + s.Name
		}
		if s.Drift {
			line += " (not in data month " + tr.DataMonth + ")"
		}
		if s.Under != nil {
			line += ", under " + refText(*s.Under)
		}
		line += viaText(s.Via) + unconfirmedText(s.Unconfirmed)
		v.Spine = append(v.Spine, line)
	}
	for _, t := range tr.To {
		tv := targetView{Vocabulary: t.Vocabulary}
		for _, e := range t.Entries {
			line := fields(t.Vocabulary, e.Entry) + viaText(e.Via) + unconfirmedText(e.Unconfirmed)
			if e.Note != "" {
				line += " (" + e.Note + ")"
			}
			tv.Entries = append(tv.Entries, line)
		}
		v.To = append(v.To, tv)
	}
	for _, f := range tr.Vocabularies {
		v.Files = append(v.Files, f.Name+" "+f.Version)
	}
	slices.Sort(v.Files)
	return v
}

func nullText(what string, n contract.Null[int32]) string {
	if !n.Valid {
		return "no " + what
	}
	return what + " " + strconv.Itoa(int(n.V))
}

// refText is a spine entry's reference in words: "vPIC model 1938".
func refText(r contract.SpineRef) string {
	switch {
	case r.Make != 0:
		return "vPIC make " + strconv.Itoa(int(r.Make))
	case r.Model != 0:
		return "vPIC model " + strconv.Itoa(int(r.Model))
	case r.Ext != "":
		return r.Ext
	}
	return r.Spine + " " + r.ID
}

func viaText(via []contract.Via) string {
	var out []string
	for _, v := range via {
		s := refText(v.SpineRef)
		var scope []string
		if y := yearsText(v.First, v.Last); y != "" {
			scope = append(scope, y)
		}
		if v.Origin != "" {
			scope = append(scope, v.Origin)
		}
		if len(scope) > 0 {
			s += " (" + strings.Join(scope, ", ") + ")"
		}
		out = append(out, s+unconfirmedText(v.Unconfirmed))
	}
	if len(out) == 0 {
		return ""
	}
	return ", via " + strings.Join(out, "; ")
}

func yearsText(first, last contract.Null[int32]) string {
	y := func(n contract.Null[int32]) string {
		if !n.Valid {
			return "…"
		}
		return strconv.Itoa(int(n.V))
	}
	switch {
	case !first.Valid && !last.Valid:
		return ""
	case first == last:
		return y(first)
	}
	return y(first) + "–" + y(last)
}

func unconfirmedText(u contract.Unconfirmed) string {
	if u.IsZero() {
		return ""
	}
	var parts []string
	for _, e := range u.Elements {
		parts = append(parts, "element "+strconv.Itoa(int(e)))
	}
	if u.ModelYear {
		parts = append(parts, "model year")
	}
	return " [unconfirmed: " + strings.Join(parts, ", ") + "]"
}

// catalogPage is /ui/vocabularies: everything loaded, vPIC first.
func (h *Handler) catalogPage(r *http.Request) (view, error) {
	if len(r.URL.Query()) > 0 {
		if _, err := contract.ParseListQuery("", r.URL.Query()); err != nil {
			return view{}, err
		}
	}
	cat, err := h.catalog()
	if err != nil {
		return view{}, err
	}
	return view{title: "Vocabularies", template: "catalog-page", data: cat}, nil
}

// entriesPage is one page of /ui/vocabularies/{name}.
type entriesPage struct {
	Name    string
	Columns []string
	Rows    []entryRow
	// Next is the next page's URL, "" on the last.
	Next string
}

// entryRow is one listed entry: its cells, and a link to what it lists
// (a vPIC make's models), "" for none.
type entryRow struct {
	Cells []string
	Link  string
}

func (h *Handler) entries(r *http.Request) (view, error) {
	name := strings.TrimPrefix(r.URL.Path, vocabulariesPath+"/")
	q := r.URL.Query()
	if !q.Has("limit") {
		q.Set("limit", "100")
	}
	req, err := contract.ParseListQuery(name, q)
	if err != nil {
		return view{}, err
	}
	cat, err := h.catalog()
	if err != nil {
		return view{}, err
	}
	if err := h.notHere([]string{name}, cat); err != nil {
		return view{}, err
	}
	l, err := h.p.List(req)
	if err != nil {
		return view{}, err
	}
	p := entriesPage{Name: name}
	var fields []string
	if i := slices.IndexFunc(cat, func(c contract.CatalogEntry) bool { return c.Name == name }); i >= 0 {
		fields = cat[i].Fields
	}
	if len(fields) > 0 {
		p.Columns = append(slices.Clone(fields), "maps to")
	} else {
		p.Columns = []string{"entry", "name", "under"}
	}
	for _, e := range l.Entries {
		switch e := e.(type) {
		case contract.VocabularyEntry:
			var row entryRow
			for _, f := range fields {
				row.Cells = append(row.Cells, e.Entry[f])
			}
			var maps []string
			for _, m := range e.Mappings {
				s := refText(contract.SpineRef{Make: m.Make, Model: m.Model, Ext: m.Ext, ID: m.ID})
				if y := yearsText(m.First, m.Last); y != "" {
					s += " (" + y + ")"
				}
				maps = append(maps, s)
			}
			if len(maps) == 0 {
				maps = []string{"unmapped"}
			}
			row.Cells = append(row.Cells, strings.Join(maps, "; "))
			p.Rows = append(p.Rows, row)
		case contract.SpineEntry:
			row := entryRow{Cells: []string{refText(e.SpineRef), e.Name, ""}}
			if e.Under != nil {
				row.Cells[2] = refText(*e.Under)
			}
			if name == "vpic" && e.Make != 0 && !q.Has("where.make") {
				row.Link = vocabulariesPath + "/vpic?where.make=" + strconv.Itoa(int(e.Make))
			}
			p.Rows = append(p.Rows, row)
		}
	}
	if l.Next != "" {
		next := url.Values{}
		for k, vs := range q {
			if k != "after" {
				next[k] = vs
			}
		}
		next.Set("after", l.Next)
		p.Next = vocabulariesPath + "/" + url.PathEscape(name) + "?" + next.Encode()
	}
	return view{title: name, template: "entries", data: p}, nil
}

// filesPanel is /ui/files: the browser's files, and a remove it refused.
type filesPanel struct {
	Files     []StoredFile
	Server    []StoredFile
	DataMonth string
	Release   string
	Refusal   *removeRefusal
}

func (h *Handler) panel() filesPanel {
	return filesPanel{Files: h.lib.Files(), Server: h.lib.Server(), DataMonth: h.lib.Month(), Release: Release(h.lib.Info())}
}

func (h *Handler) files(*http.Request) (view, error) {
	if h.lib == nil {
		return view{}, &pageError{Status: http.StatusNotFound, Message: needsBrowser}
	}
	return view{title: "Your files", template: "files", data: h.panel()}, nil
}

// remove drops the browser's files the form names (name, repeated),
// their dependants too with all=true. A refusal names the dependants and
// offers to remove all of them.
func (h *Handler) remove(r *http.Request) (view, error) {
	if h.lib == nil {
		return view{}, &pageError{Status: http.StatusNotFound, Message: needsBrowser}
	}
	if err := r.ParseForm(); err != nil {
		return view{}, &pageError{Status: http.StatusBadRequest, Message: "The form could not be read: " + err.Error()}
	}
	err := h.lib.Remove(r.PostForm["name"], r.PostForm.Get("all") == "true")
	if refusal, ok := errors.AsType[*removeRefusal](err); ok {
		p := h.panel()
		p.Refusal = refusal
		return view{title: "Your files", template: "files", data: p, status: http.StatusConflict, target: filesTarget}, nil
	}
	if err != nil {
		return view{}, err
	}
	if !fragment(r) {
		return view{location: vocabPath, status: http.StatusSeeOther}, nil
	}
	return view{title: "Your files", template: "files", data: h.panel()}, nil
}

// workerConfig is what /sw.js gives the worker: the release, its assets,
// and the server's files it links.
type workerConfig struct {
	Assets         string       `json:"assets"`
	Release        string       `json:"release"`
	DecoderVersion string       `json:"decoder_version"`
	DataMonth      string       `json:"data_month"`
	ArtifactHash   string       `json:"artifact_hash"`
	Files          []workerFile `json:"files"`
}

type workerFile struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Blake3  string `json:"blake3"`
	URL     string `json:"url"`
}

// WorkerInfo is the Info a worker's config describes: the release and the
// server's files, what a Library links.
func WorkerInfo(config []byte) (contract.Info, error) {
	var c workerConfig
	if err := json.Unmarshal(config, &c); err != nil {
		return contract.Info{}, err
	}
	info := contract.Info{DecoderVersion: c.DecoderVersion, DataMonth: c.DataMonth, ArtifactHash: c.ArtifactHash, Vocabularies: []contract.VocabularyFile{}}
	for _, f := range c.Files {
		info.Vocabularies = append(info.Vocabularies, contract.VocabularyFile{Name: f.Name, Kind: f.Kind, Version: f.Version, Blake3: f.Blake3})
	}
	return info, nil
}

// serviceWorker answers /sw.js: the release's worker. Its bytes name the
// release and the server's files, so a new release, or a server restarted
// with other files, is a new worker, which the browser installs beside
// the old one and starts once the old one's pages close. It is never
// cached without asking (no-cache), and it alone may run WebAssembly.
func (h *Handler) serviceWorker(w http.ResponseWriter, info contract.Info, base string) {
	c := workerConfig{Assets: base, Release: Release(info), DecoderVersion: info.DecoderVersion, DataMonth: info.DataMonth, ArtifactHash: info.ArtifactHash, Files: []workerFile{}}
	for _, f := range info.Vocabularies {
		c.Files = append(c.Files, workerFile{Name: f.Name, Kind: f.Kind, Version: f.Version, Blake3: f.Blake3, URL: FilePath(info, f)})
	}
	config, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	imports, err := json.Marshal([]string{base + "/" + execName, base + "/worker.js"})
	if err != nil {
		panic(err)
	}
	body := "// nitrovin's Service Worker, release " + c.Release + " (ADR 0013): the vocabulary pages run\n" +
		"// in the browser as WebAssembly, over the files it holds. worker.js says how.\n" +
		"self.nitrovinConfig = " + string(config) + ";\n" +
		"importScripts(..." + string(imports) + ");\n"
	w.Header().Set("Content-Security-Policy", workerCSP)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write([]byte(body))
}
