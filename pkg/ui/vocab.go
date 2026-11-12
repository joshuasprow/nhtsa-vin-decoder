package ui

import (
	"bytes"
	"encoding/hex"
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
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The vocabularies (ADR 0013). The page (page.go) keeps them behind its
// Vocabularies button, in a dialog: the catalog of what is loaded, each
// file marked by where it lives, and the visitor's own files, which need
// JavaScript. /ui/vocabularies/{name} lists one file's entries, its query
// GET /v1/vocabularies's. Each answers the same on the server and in the
// browser's Service Worker, which answers the page once the browser holds
// a file, and the entry lists of the files it holds, and forwards the
// rest: so a link means one thing with JavaScript on or off, and a link
// naming a browser file, opened where the file is not, says where it
// lives. /ui/files is the worker's panel, which fills the dialog: the
// merged catalog (vPIC, the server's files and the browser's) with the
// browser's files' storage, drift and removal, and the one-click load of
// EPA's vocabulary; /ui/files/remove drops browser files. Both are the
// worker's alone, which the server answers by saying so. /sw.js is the
// worker itself, the only response whose CSP allows WebAssembly.

const (
	vocabulariesPath = "/ui/vocabularies"
	filesPath        = "/ui/files"
	removePath       = "/ui/files/remove"
	swPath           = "/sw.js"
	// vocabPath and translatePath were pages before the one page held
	// them: old links to them move to it (moved).
	vocabPath     = "/vocab"
	translatePath = "/ui/translate"
	// moduleName and execName are the worker's module and Go's
	// wasm_exec.js among a release's assets.
	moduleName = "sw.wasm"
	execName   = "wasm_exec.js"
	// filesTarget is the browser files' panel in the page's dialog.
	filesTarget = "#browser-files"
)

// workerCSP is /sw.js's Content-Security-Policy, the worker's: the
// server's own scripts and connections, and WebAssembly, which no page is
// allowed.
const workerCSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'"

// needsBrowser is what the server says of the browser's own files.
const needsBrowser = "Files you load are kept in your browser (IndexedDB) and read there by WebAssembly in a Service Worker, so they are never uploaded: " +
	"this page needs JavaScript, and a page the Service Worker controls. This server holds none of them. Open / and its Vocabularies to load files."

// pageRoutes are the routes: the page, the worker's panel and its remove,
// and the pages that moved to the page. The entry lists are ServeHTTP's.
func (h *Handler) pageRoutes() {
	h.routes[homePath] = h.home
	h.routes[filesPath] = h.files
	h.posts[removePath] = h.remove
	for _, old := range []string{decodePath, translatePath, vocabPath, vocabulariesPath} {
		h.routes[old] = h.moved
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

// named is what a lookup on the page names: the from's vocabulary, spine
// or extension set, and the targets. worker.js reads the same parameters
// (its named): it answers a lookup naming a file the browser holds and
// sends every other to the server, a lookup naming no target included.
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

// catalogTable is the catalog as the dialog shows it; Manage adds the
// browser's files' removal, which only the worker's panel offers.
type catalogTable struct {
	Rows   []catalogRow
	Manage bool
}

// catalogRow is one row of the catalog: vPIC or a file, where it lives,
// its drift from the data month, and why it is left out ("" when it
// links). File is the browser's record of a file it holds, nil for the
// others.
type catalogRow struct {
	contract.CatalogEntry
	// Where is "vpic" (nitrovin's own data), "server" or "browser".
	Where string
	Drift []contract.DriftCount
	Fault string
	File  *StoredFile
}

// WhereText is where the row's file lives, in words.
func (r catalogRow) WhereText() string {
	switch r.Where {
	case "vpic":
		return "built in"
	case "server":
		return "this server"
	}
	return "this browser"
}

// What says what the row's file is, in words: "vocabulary of vehicles:
// make, model → vpic", "spine of parts"; "" for a file left out unread.
func (r catalogRow) What() string {
	if r.Kind == "" {
		return ""
	}
	s := strings.ReplaceAll(r.Kind, "-", " ")
	if r.Subject != "" {
		s += " of " + r.Subject + "s"
	}
	switch {
	case len(r.Fields) > 0:
		s += ": " + strings.Join(r.Fields, ", ") + " → " + r.Spine
	case r.Spine != "":
		s += " → " + r.Spine
	}
	return s
}

// DriftText is the row's drift counted by kind: "1 make, 2 qualifier".
func (r catalogRow) DriftText() string {
	var out []string
	for _, d := range r.Drift {
		out = append(out, strconv.Itoa(d.Count)+" "+d.Kind)
	}
	return strings.Join(out, ", ")
}

// DriftExamples are the row's drift examples, each with its line, and
// how many more of each kind there are.
func (r catalogRow) DriftExamples() []string {
	var out []string
	for _, d := range r.Drift {
		for _, e := range d.Examples {
			out = append(out, "line "+strconv.Itoa(e.Line)+": "+e.Detail)
		}
		if more := d.Count - len(d.Examples); more > 0 {
			out = append(out, fmt.Sprintf("and %d more of kind %s", more, d.Kind))
		}
	}
	return out
}

// rows is the catalog cat as the dialog shows it, vPIC first. On the server,
// each file is the server's, its drift Info's. In the browser, the
// Library says which files are the server's and which the browser's, the
// drift of each, and the files left out, which cat lacks.
func (h *Handler) rows(cat []contract.CatalogEntry) []catalogRow {
	if h.lib == nil {
		info := h.p.Info()
		out := make([]catalogRow, len(cat))
		for i, c := range cat {
			out[i] = catalogRow{CatalogEntry: c, Where: "server"}
			if c.Name == "vpic" {
				out[i].Where = "vpic"
			}
			if j := slices.IndexFunc(info.Vocabularies, func(v contract.VocabularyFile) bool { return v.Name == c.Name }); j >= 0 {
				out[i].Drift = info.Vocabularies[j].Drift
			}
		}
		return out
	}
	var out []catalogRow
	byName := map[string]contract.CatalogEntry{}
	for _, c := range cat {
		byName[c.Name] = c
		if c.Name == "vpic" {
			out = append(out, catalogRow{CatalogEntry: c, Where: "vpic"})
		}
	}
	add := func(where string, files []StoredFile) {
		for _, f := range files {
			r := catalogRow{Where: where, Drift: f.Drift, Fault: f.Fault}
			if c, ok := byName[f.Name]; ok && f.Fault == "" {
				r.CatalogEntry = c
			} else {
				r.CatalogEntry = contract.CatalogEntry{Name: f.Name, Kind: f.Kind, Version: f.Version}
			}
			if where == "browser" {
				r.File = &f
			}
			out = append(out, r)
		}
	}
	add("server", h.lib.Server())
	add("browser", h.lib.Files())
	return out
}

// remember makes the targets q names, in the browser, the visitor's
// selection on their spine: a link's own to= wins over the selection,
// which vocab.js ticks in the forms. History going back to an older page
// changes nothing. worker.js does the same for a lookup it sends to the
// server, by the answer's data-from and data-spine.
func (h *Handler) remember(r *http.Request, q url.Values, cat []contract.CatalogEntry) {
	if h.lib == nil || !q.Has("to") || r.Header.Get("HX-History-Restore-Request") == "true" {
		return
	}
	spine, ok := spineOf(queryFrom(q), q, cat)
	if !ok {
		return // nothing loaded by its name: the translation is refused
	}
	if err := h.lib.Select(spine, q["to"]); err != nil {
		h.log.Printf("ui: %v", err)
	}
}

// defaultTargets are a lookup's targets when q names none, the same on
// the server and in the browser: every vocabulary loaded where it is
// answered on the from's spine but the from's own. Before a lookup is
// asked there are none: the forms tick none, and vocab.js ticks the
// visitor's selection. A link naming no target is always the server's
// (worker.js), so it means the same in every browser.
func (h *Handler) defaultTargets(q url.Values, cat []contract.CatalogEntry) []string {
	if !slices.ContainsFunc(slices.Collect(maps.Keys(q)), func(k string) bool { return strings.HasPrefix(k, "from.") && k != "from.vocabulary" }) {
		return nil
	}
	spine, ok := spineOf(queryFrom(q), q, cat)
	if !ok {
		return nil
	}
	from := q.Get("from.vocabulary")
	var out []string
	for _, c := range cat {
		if c.Kind == "vocabulary" && c.Spine == spine && c.Name != from {
			out = append(out, c.Name)
		}
	}
	return out
}

// entryText is an entry of vocabulary name in words, its fields' values in
// the vocabulary's order: "Dodge Ram 1500".
func entryText(cat []contract.CatalogEntry, name string, entry map[string]string) string {
	i := slices.IndexFunc(cat, func(c contract.CatalogEntry) bool { return c.Name == name })
	var order []string
	if i >= 0 {
		order = cat[i].Fields
	} else {
		order = slices.Sorted(maps.Keys(entry))
	}
	var vs []string
	for _, f := range order {
		if entry[f] != "" {
			vs = append(vs, entry[f])
		}
	}
	return strings.Join(vs, " ")
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

// viaText is one mapping that held, in words: "Matched through vPIC model
// 13620 (1500), 2013 on, curated: Ram is its own make from 2013".
func viaText(v contract.Via, named func(contract.SpineRef) string) string {
	s := "Matched through " + named(v.SpineRef)
	if y := yearsText(v.First, v.Last); y != "" {
		s += ", " + y
	}
	if o := originText(v.Origin, v.Note); o != "" {
		s += ", " + o
	}
	if v.Note != "" {
		s += ": " + v.Note
	}
	return s
}

// originText is where a mapping came from, in words.
func originText(origin, note string) string {
	switch {
	case strings.Contains(strings.ToLower(note), "barrett"):
		return "from Barrett's review"
	case origin == "derived":
		return "matched by rule"
	case origin == "seed":
		return "seeded"
	}
	return origin
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

// unconfirmedText is what applied but could not be checked, by element
// name: "Not checked: Drive Type, model year".
func unconfirmedText(u contract.Unconfirmed, elements contract.Elements) string {
	if u.IsZero() {
		return ""
	}
	var parts []string
	for _, id := range u.Elements {
		if e, ok := elements.Find(id); ok && e.Variable != "" {
			parts = append(parts, e.Variable)
		} else {
			parts = append(parts, "element "+strconv.Itoa(int(id)))
		}
	}
	if u.ModelYear {
		parts = append(parts, "model year")
	}
	return "Not checked: " + strings.Join(parts, ", ")
}

// entriesPage is one page of /ui/vocabularies/{name}.
type entriesPage struct {
	Name    string
	Columns []string
	Rows    []entryRow
	// Next is the next page's URL, "" on the last.
	Next string
	// Filter is the box filtering on the first field, nil for a spine.
	Filter *entryFilter
}

// entryFilter is the entry list's filter: GET where.{Field}={Value}.
type entryFilter struct {
	Field, Value string
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
	// The filter box submitted empty filters nothing.
	for k, vs := range q {
		if strings.HasPrefix(k, "where.") && !slices.ContainsFunc(vs, func(v string) bool { return v != "" }) {
			q.Del(k)
		}
	}
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
		p.Filter = &entryFilter{Field: fields[0], Value: q.Get("where." + fields[0])}
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
		case contract.ModelValueEntry:
			v := e.Value.Text
			if !e.Value.IsText {
				v = strconv.Itoa(int(e.Value.Attribute))
			}
			p.Rows = append(p.Rows, entryRow{Cells: []string{v, e.Name, ""}})
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
	return view{title: name, template: "entries", data: p, vocabDialog: true}, nil
}

// filesPanel is /ui/files, which fills the page's dialog: the merged
// catalog with the browser's files' removal, a remove it refused, and
// EPA's vocabulary to load.
type filesPanel struct {
	Catalog   catalogTable
	Offer     *offerView
	DataMonth string
	Release   string
	Refusal   *removeRefusal
}

// NoneStored reports whether the browser keeps no file.
func (p filesPanel) NoneStored() bool {
	return !slices.ContainsFunc(p.Catalog.Rows, func(r catalogRow) bool { return r.Where == "browser" })
}

// offerView is the Offer as the panel shows it: whether the browser holds
// it already, and the version of it the browser holds otherwise, "" for
// none.
type offerView struct {
	*Offer
	Held     bool
	Replaces string
}

func (h *Handler) panel() (filesPanel, error) {
	cat, err := h.catalog()
	if err != nil {
		return filesPanel{}, err
	}
	p := filesPanel{Catalog: catalogTable{Rows: h.rows(cat), Manage: true}, DataMonth: h.lib.Month(), Release: Release(h.lib.Info())}
	if o := h.offered(); o != nil {
		p.Offer = &offerView{Offer: o}
		for _, f := range h.lib.Files() {
			if f.Name == o.Name {
				p.Offer.Held = f.Blake3 == o.Blake3
				p.Offer.Replaces = f.Version
			}
		}
	}
	return p, nil
}

func (h *Handler) files(*http.Request) (view, error) {
	if h.lib == nil {
		return view{}, &pageError{Status: http.StatusNotFound, Message: needsBrowser}
	}
	p, err := h.panel()
	if err != nil {
		return view{}, err
	}
	return view{title: "Your files", template: "files", data: p}, nil
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
		p, err := h.panel()
		if err != nil {
			return view{}, err
		}
		p.Refusal = refusal
		return view{title: "Your files", template: "files", data: p, status: http.StatusConflict, target: filesTarget}, nil
	}
	if err != nil {
		return view{}, err
	}
	if !fragment(r) {
		return view{location: homePath, status: http.StatusSeeOther}, nil
	}
	return h.files(r)
}

// Offer is the one public vocabulary a server offers the browser as a
// one-click load: EPA's (epa-vehicles, nitrovin-data's release asset for
// the data month, which release builds embed), published among the
// release's assets at URL. Loaded, the browser keeps it as a file of its
// own.
type Offer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Blake3  string `json:"blake3"`
	Size    int    `json:"size"`
	URL     string `json:"url"`
}

// SizeText is the offer's size in words.
func (o *Offer) SizeText() string { return sizeText(o.Size) }

// epaFileName is the file name the EPA vocabulary is read and offered as.
const epaFileName = "epa-vehicles.jsonl"

// epaOffer is the EPA vocabulary b as the server offers it, nil when it is
// not offered: it does not read (logged), it was built for another data
// month than the server decodes with, or the server loaded a file of its
// name, which makes the browser refuse it.
func (h *Handler) epaOffer(b []byte) *Offer {
	info := h.info()
	f, err := vocab.Read(epaFileName, bytes.NewReader(b))
	if err != nil {
		h.log.Printf("ui: the EPA vocabulary built in is not offered: %v", err)
		return nil
	}
	if f.DataMonth != info.DataMonth || slices.ContainsFunc(info.Vocabularies, func(v contract.VocabularyFile) bool { return v.Name == f.Name }) {
		return nil
	}
	o := &Offer{Name: f.Name, Version: f.Version, Blake3: hex.EncodeToString(f.Hash[:]), Size: len(b)}
	o.URL = FilePath(info, contract.VocabularyFile{Name: o.Name, Blake3: o.Blake3})
	return o
}

// sizeText is n bytes in words: "512 bytes", "2.1 kB", "956 kB", "25.5 MB".
func sizeText(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n) + " bytes"
	case n < 10_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + " kB"
	case n < 1_000_000:
		return strconv.Itoa((n+500)/1000) + " kB"
	}
	return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + " MB"
}

// SizeText is the stored file's size in words.
func (f StoredFile) SizeText() string { return sizeText(f.Size) }

// workerConfig is what /sw.js gives the worker: the release, its assets,
// the server's files it links and the vocabulary it offers.
type workerConfig struct {
	Assets         string       `json:"assets"`
	Release        string       `json:"release"`
	DecoderVersion string       `json:"decoder_version"`
	DataMonth      string       `json:"data_month"`
	ArtifactHash   string       `json:"artifact_hash"`
	Files          []workerFile `json:"files"`
	EPA            *Offer       `json:"epa,omitempty"`
	// Elements are the data month's public elements, which the worker
	// lays a VIN's decode out by and names a translation's unchecked
	// qualifiers by; ErrorTexts its error codes' texts, by code.
	Elements   contract.Elements `json:"elements,omitempty"`
	ErrorTexts map[int]string    `json:"error_texts,omitempty"`
	// DataRelease is the page's link to the data month's nitrovin-data
	// release, which only the server's build can name.
	DataRelease string `json:"data_release,omitempty"`
}

// workerFile is one of the server's files.
type workerFile struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Blake3  string `json:"blake3"`
	URL     string `json:"url"`
}

// BrowserConfig is what the server gives the browser's pages
// (ReadWorkerConfig): the Info the worker describes (the release, its
// elements and the server's files, what a Library links), the
// vocabulary the server offers, nil for none, the page's link to the
// data month's release, and the data's error-code texts, by code: what
// the worker's module cannot know itself.
type BrowserConfig struct {
	Info        contract.Info
	Offer       *Offer
	DataRelease string
	ErrorTexts  map[int]string
}

// ReadWorkerConfig reads a worker's config, /sw.js's.
func ReadWorkerConfig(config []byte) (BrowserConfig, error) {
	var c workerConfig
	if err := json.Unmarshal(config, &c); err != nil {
		return BrowserConfig{}, err
	}
	info := contract.Info{DecoderVersion: c.DecoderVersion, DataMonth: c.DataMonth, ArtifactHash: c.ArtifactHash, Elements: c.Elements, Vocabularies: []contract.VocabularyFile{}}
	for _, f := range c.Files {
		info.Vocabularies = append(info.Vocabularies, contract.VocabularyFile{Name: f.Name, Kind: f.Kind, Version: f.Version, Blake3: f.Blake3})
	}
	return BrowserConfig{Info: info, Offer: c.EPA, DataRelease: c.DataRelease, ErrorTexts: c.ErrorTexts}, nil
}

// serviceWorker answers /sw.js: the release's worker. Its bytes name the
// release and the server's files, so a new release, or a server restarted
// with other files, is a new worker, which the browser installs beside
// the old one and starts once the old one's pages close, or at once when
// the visitor asks for it (Update now). It is never cached without asking
// (no-cache), and it alone may run WebAssembly.
func (h *Handler) serviceWorker(w http.ResponseWriter, info contract.Info) error {
	base := assetsPath(Release(info))
	c := workerConfig{
		Assets: base, Release: Release(info), DecoderVersion: info.DecoderVersion, DataMonth: info.DataMonth, ArtifactHash: info.ArtifactHash,
		Files: []workerFile{}, EPA: h.offered(), Elements: info.Elements, ErrorTexts: h.errorTexts, DataRelease: h.links(info).DataRelease,
	}
	for _, f := range info.Vocabularies {
		if f.Kind == string(vocab.KindCatalog) || f.Kind == string(vocab.KindStock) {
			continue // a catalog and stock load on the server only: the browser has no fitment yet
		}
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
	return nil
}
