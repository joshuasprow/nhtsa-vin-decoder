package ui

import (
	"bytes"
	"cmp"
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

// The vocabulary pages (ADR 0013). /vocab is the entry page, the server's:
// the catalog of what is loaded, each file marked by where it lives, the
// visitor's own files, which need JavaScript, and the translate forms.
// /ui/translate translates, its query GET /v1/translate's, below the same
// forms; both offer examples, one from each vocabulary. /ui/vocabularies
// sends the visitor to /vocab, the one list; /ui/vocabularies/{name} lists
// one file's entries, its query GET /v1/vocabularies's. Each answers the same on
// the server and in the browser's Service Worker, which answers only the
// requests naming a file the browser holds and forwards the rest: so a
// link means one thing with JavaScript on or off, and a link naming a
// browser file, opened where the file is not, says where it lives.
// /ui/files is the worker's /vocab panel: the merged catalog (vPIC, the
// server's files and the browser's) with the browser's files' storage,
// drift and removal, the one-click load of EPA's vocabulary, and the
// translate forms over the merged catalog, the visitor's selection of
// targets ticked; /ui/files/remove drops browser files. Both are the
// worker's alone, which the server answers by saying so. /sw.js is the
// worker itself, the only response whose CSP allows WebAssembly.

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

// vocabPage is /vocab: the server's catalog, whether the server has the
// worker to load the visitor's own files, and the translate forms.
type vocabPage struct {
	Catalog   catalogTable
	Worker    bool
	Translate translatePage
}

func (h *Handler) vocab(*http.Request) (view, error) {
	cat, err := h.catalog()
	if err != nil {
		return view{}, err
	}
	t, err := h.translateForms(cat, url.Values{})
	if err != nil {
		return view{}, err
	}
	return view{title: "Vocabularies", template: "vocab", vocab: true, data: vocabPage{Catalog: catalogTable{Rows: h.rows(cat)}, Worker: h.worker != nil, Translate: t}}, nil
}

// catalogTable is the catalog as /vocab shows it; Manage adds the
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

// rows is the catalog cat as /vocab shows it, vPIC first. On the server,
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

// translatePage is the translate forms, with examples to try: on
// /ui/translate with the translation asked for, and on /vocab.
type translatePage struct {
	// Vocabularies are the catalog's vocabularies, the targets.
	Vocabularies []contract.CatalogEntry
	// From is the vocabulary an entry is translated from, nil before one
	// is chosen; Fields its entry's fields with the query's values.
	From   *contract.CatalogEntry
	Fields []fieldValue
	// FromVIN, FromMake, FromModel, VIN and ModelYear are the query's.
	FromVIN, FromMake, FromModel, VIN, ModelYear string
	// To are the targets ticked: the query's, or on /vocab in the browser
	// the visitor's selection.
	To       []string
	Examples []example
	Result   *translationView
}

type fieldValue struct{ Name, Value string }

// target is a vocabulary the form offers as a target, and whether it is
// ticked.
type target struct {
	Name    string
	Checked bool
}

// targetSet is the targets a form offers: the vocabularies on Spine.
type targetSet struct {
	Spine string
	List  []target
}

// TargetsOn are the vocabularies a translation through spine can reach:
// a VIN's and a vPIC entry's through vpic, an entry's through its
// vocabulary's spine.
func (p translatePage) TargetsOn(spine string) targetSet {
	out := targetSet{Spine: spine}
	for _, v := range p.Vocabularies {
		if v.Spine == spine {
			out.List = append(out.List, target{v.Name, slices.Contains(p.To, v.Name)})
		}
	}
	return out
}

// entryForm is a form from a vocabulary's entry, over the vocabularies on
// one spine and offering that spine's targets, whether a vocabulary is
// chosen yet or not: a translation crosses exactly one spine (ADR 0010).
// Key names its elements ("entry" for vPIC's); From is the vocabulary
// chosen, nil when it is not on this spine, and Fields, VIN and Year its
// entry's values as asked.
type entryForm struct {
	Spine, Key   string
	Vocabularies []contract.CatalogEntry
	From         *contract.CatalogEntry
	Targets      targetSet
	Fields       []fieldValue
	VIN, Year    string
}

// EntryForms are the forms from a vocabulary's entry: one for the vehicle
// vocabularies, on vpic, then one for each declared spine's part
// vocabularies, by spine; none for a spine no vocabulary is on.
func (p translatePage) EntryForms() []entryForm {
	var spines, declared []string
	for _, v := range p.Vocabularies {
		switch {
		case v.Spine == vocab.Reserved:
			spines = []string{vocab.Reserved}
		case !slices.Contains(declared, v.Spine):
			declared = append(declared, v.Spine)
		}
	}
	slices.Sort(declared)
	spines = append(spines, declared...)
	out := make([]entryForm, len(spines))
	for i, s := range spines {
		f := entryForm{Spine: s, Key: "entry", Targets: p.TargetsOn(s)}
		if s != vocab.Reserved {
			f.Key = "entry-" + s
		}
		for _, v := range p.Vocabularies {
			if v.Spine == s {
				f.Vocabularies = append(f.Vocabularies, v)
			}
		}
		if p.From != nil && p.From.Spine == s {
			f.From = p.From
			f.Fields, f.VIN = p.Fields, p.VIN
		}
		f.Year = p.YearIn("entry")
		out[i] = f
	}
	return out
}

// YearIn is the model year the form named shows: the query's, in the
// form ("vin", "vpic" or "entry") its from came from.
func (p translatePage) YearIn(form string) string {
	from := "entry"
	switch {
	case p.FromVIN != "":
		from = "vin"
	case p.FromMake != "" || p.FromModel != "":
		from = "vpic"
	}
	if form != from {
		return ""
	}
	return p.ModelYear
}

// example is a translation to try: a link to the translate page with its
// form filled in and the translation below it. Its targets are the ticked
// ones on its Spine, else DefaultTo (names, space-separated: none for
// the examples that need no file). vocab.js sends an example clicked on
// /vocab to the targets ticked there at that moment.
type example struct {
	Label, Hint, Href, Spine, DefaultTo string
}

// The examples that need no file: a 2015 Ram, a VIN, and the vPIC model
// it decodes to, in its year.
const (
	exampleRam   = "1C6RR7LT5FS123456"
	exampleVIN   = "1HGCM82633A004352"
	exampleModel = "1861" // vPIC's Honda Accord, under make 474
	exampleYear  = "2003"
)

// exampleScan is how many of a vocabulary's first entries are looked
// through for its example, the first with a mapping.
const exampleScan = 100

// translateForms are the forms over the catalog cat, filled from q, a
// translate query, and their examples: those that need no file, then one
// from each vocabulary, its first mapped entry, in a year its mapping
// holds for.
func (h *Handler) translateForms(cat []contract.CatalogEntry, q url.Values) (translatePage, error) {
	p := translatePage{FromVIN: q.Get("from.vin"), FromMake: q.Get("from.make"), FromModel: q.Get("from.model"), VIN: q.Get("vin"), ModelYear: q.Get("model_year"), To: q["to"]}
	for _, c := range cat {
		if c.Kind == "vocabulary" {
			p.Vocabularies = append(p.Vocabularies, c)
		}
	}
	// By name: where a file lives (the server, the browser) changes the
	// catalog's load order, never the form.
	slices.SortFunc(p.Vocabularies, func(a, b contract.CatalogEntry) int { return strings.Compare(a.Name, b.Name) })
	if name := q.Get("from.vocabulary"); name != "" {
		if i := slices.IndexFunc(p.Vocabularies, func(c contract.CatalogEntry) bool { return c.Name == name }); i >= 0 {
			p.From = &p.Vocabularies[i]
			for _, f := range p.From.Fields {
				p.Fields = append(p.Fields, fieldValue{f, q.Get("from.entry." + f)})
			}
		}
	}
	p.Examples = []example{
		p.example(exampleRam, "a VIN: a 2015 Ram 1500", vocab.Reserved, nil, url.Values{"from.vin": {exampleRam}}),
		p.example(exampleVIN, "a VIN: a 2003 Honda Accord", vocab.Reserved, nil, url.Values{"from.vin": {exampleVIN}}),
		p.example("vPIC model "+exampleModel, "the Honda Accord, model year "+exampleYear, vocab.Reserved, nil, url.Values{"from.model": {exampleModel}, "model_year": {exampleYear}}),
	}
	for _, v := range p.Vocabularies {
		l, err := h.p.List(contract.ListRequest{Of: v.Name, Limit: exampleScan})
		if err != nil {
			return translatePage{}, err
		}
		var e contract.VocabularyEntry
		for _, le := range l.Entries {
			if ve, ok := le.(contract.VocabularyEntry); ok && len(ve.Mappings) > 0 {
				e = ve
				break
			}
		}
		if len(e.Mappings) == 0 {
			continue
		}
		q := url.Values{"from.vocabulary": {v.Name}}
		var label []string
		for _, f := range v.Fields {
			if s := e.Entry[f]; s != "" {
				q.Set("from.entry."+f, s)
				label = append(label, s)
			}
		}
		hint := "an entry of " + v.Name
		if year := cmp.Or(e.Mappings[0].First, e.Mappings[0].Last); year.Valid && v.Spine == vocab.Reserved {
			q.Set("model_year", strconv.Itoa(int(year.V)))
			hint += ", model year " + strconv.Itoa(int(year.V))
		}
		ex := p.example(strings.Join(label, " · "), hint, v.Spine, []string{p.sensibleTarget(v)}, q)
		if p.sensibleTarget(v) != v.Name {
			// Kept only when it names something in another vocabulary.
			u, err := url.Parse(ex.Href)
			if err != nil {
				continue
			}
			req, err := contract.ParseTranslateQuery(u.Query())
			if err != nil {
				continue
			}
			tr, err := h.p.Translate(req)
			if err != nil || !slices.ContainsFunc(tr.To, func(t contract.Translated) bool { return t.Vocabulary != v.Name && len(t.Entries) > 0 }) {
				continue
			}
		}
		p.Examples = append(p.Examples, ex)
	}
	return p, nil
}

// example is the example from q, a translate query without targets, to
// the ticked targets on spine, else to def.
func (p translatePage) example(label, hint, spine string, def []string, q url.Values) example {
	var to []string
	for _, t := range p.TargetsOn(spine).List {
		if t.Checked {
			to = append(to, t.Name)
		}
	}
	if len(to) == 0 {
		to = def
	}
	if len(to) > 0 {
		q["to"] = to
	}
	return example{Label: label, Hint: hint, Href: translatePath + "?" + q.Encode(), Spine: spine, DefaultTo: strings.Join(def, " ")}
}

// sensibleTarget is what v's example translates to when nothing on its
// spine is ticked: the first other vocabulary on it, else v itself.
func (p translatePage) sensibleTarget(v contract.CatalogEntry) string {
	for _, t := range p.TargetsOn(v.Spine).List {
		if t.Name != v.Name {
			return t.Name
		}
	}
	return v.Name
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
	explicit := q.Has("to")
	if !explicit {
		to, err := h.defaultTargets(q, cat)
		if err != nil {
			return view{}, err
		}
		if len(to) > 0 {
			q["to"] = to
		}
	}
	p, err := h.translateForms(cat, q)
	if err != nil {
		return view{}, err
	}
	if explicit {
		h.remember(r, q, p)
	}
	entry := slices.ContainsFunc(p.Fields, func(f fieldValue) bool { return f.Value != "" })
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
	p.Result = newTranslationView(tr, cat, h.p.Info().Elements, q.Get("model_year"))
	return view{title: "Translate", template: "translate", data: p}, nil
}

// remember makes the targets q names, in the browser, the visitor's
// selection on their spine: a link's own to= wins over the selection, and
// /vocab ticks what was last translated to. History going back to an
// older page changes nothing. worker.js does the same for a translation
// it sends to the server.
func (h *Handler) remember(r *http.Request, q url.Values, p translatePage) {
	if h.lib == nil || !q.Has("to") || r.Header.Get("HX-History-Restore-Request") == "true" {
		return
	}
	spine := vocab.Reserved
	switch {
	case q.Has("from.spine"):
		spine = q.Get("from.spine")
	case p.From != nil:
		spine = p.From.Spine
	case q.Has("from.vocabulary"):
		return // nothing loaded by its name: the translation is refused
	}
	if err := h.lib.Select(spine, q["to"]); err != nil {
		h.log.Printf("ui: %v", err)
	}
}

// defaultTargets are a translation's targets when q names none: in the
// browser the visitor's selection on the from's spine, else every
// vocabulary loaded on that spine but the from's own; none before a from.
func (h *Handler) defaultTargets(q url.Values, cat []contract.CatalogEntry) ([]string, error) {
	if !slices.ContainsFunc(slices.Collect(maps.Keys(q)), func(k string) bool { return strings.HasPrefix(k, "from.") && k != "from.vocabulary" }) {
		return nil, nil
	}
	spine, from := vocab.Reserved, q.Get("from.vocabulary")
	switch {
	case q.Has("from.spine"):
		spine = q.Get("from.spine")
	case from != "":
		i := slices.IndexFunc(cat, func(c contract.CatalogEntry) bool { return c.Name == from && c.Kind == "vocabulary" })
		if i < 0 {
			return nil, nil
		}
		spine = cat[i].Spine
	}
	loaded := func(n string) bool {
		return slices.ContainsFunc(cat, func(c contract.CatalogEntry) bool { return c.Name == n && c.Kind == "vocabulary" && c.Spine == spine })
	}
	if h.lib != nil {
		s, err := h.lib.Selection()
		if err != nil {
			return nil, err
		}
		if names, ok := s[spine]; ok {
			return slices.DeleteFunc(slices.Clone(names), func(n string) bool { return !loaded(n) }), nil
		}
	}
	var out []string
	for _, c := range cat {
		if c.Kind == "vocabulary" && c.Spine == spine && c.Name != from {
			out = append(out, c.Name)
		}
	}
	return out, nil
}

// translationView is a translation in words: names first, the spine, ids
// and provenance in Details.
type translationView struct {
	Heading, From, Status, Note, Vehicle string
	Spine                                []string
	To                                   []targetView
	Files                                []string
	DataMonth, DecoderVersion            string
}

type targetView struct {
	Vocabulary string
	Entries    []entryView
}

// entryView is one entry reached: its name, a link translating it back,
// to the request's source vocabulary and the other targets, and how it
// was reached.
type entryView struct {
	Name, Href string
	Details    []string
}

func newTranslationView(tr contract.Translation, cat []contract.CatalogEntry, elements contract.Elements, year string) *translationView {
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
			if entry[f] != "" {
				vs = append(vs, entry[f])
			}
		}
		return strings.Join(vs, " ")
	}
	names := map[contract.SpineRef]string{}
	for _, s := range tr.Spine {
		names[s.SpineRef] = s.Name
	}
	named := func(r contract.SpineRef) string {
		if n := names[r]; n != "" {
			return refText(r) + " (" + n + ")"
		}
		return refText(r)
	}
	v := &translationView{Note: tr.From.Note, DataMonth: tr.DataMonth, DecoderVersion: tr.DecoderVersion}
	switch tr.From.Status {
	case contract.FromUnmapped:
		v.Status = "listed, but not mapped yet"
	case contract.FromUnknown:
		v.Status = "not found"
	}
	switch f := tr.From; {
	case f.VIN != "":
		v.From = "VIN " + f.VIN
	case f.Vocabulary != "":
		v.From = f.Vocabulary + ": " + fields(f.Vocabulary, f.Entry)
	default:
		v.From = named(f.SpineRef)
	}
	// The heading names the vehicle: its model year, then the model
	// reached under its make, "2015 Ram 1500".
	if x := tr.Vehicle; x != nil && x.ModelYear.Valid {
		year = strconv.Itoa(int(x.ModelYear.V))
	}
	for _, s := range tr.Spine {
		if v.Heading != "" || s.Name == "" {
			continue
		}
		switch {
		case s.Model != 0 || s.Ext != "":
			v.Heading = s.Name
			if s.Under != nil && names[*s.Under] != "" && !strings.HasPrefix(s.Name, names[*s.Under]) {
				v.Heading = names[*s.Under] + " " + s.Name
			}
		case s.Make != 0:
			v.Heading = s.Name
		}
	}
	if v.Heading == "" {
		if tr.From.Vocabulary != "" {
			v.Heading = fields(tr.From.Vocabulary, tr.From.Entry)
		} else {
			v.Heading = v.From
		}
	}
	if year != "" && tr.From.Spine == "" {
		v.Heading = year + " " + v.Heading
	}
	if x := tr.Vehicle; x != nil {
		v.Vehicle = "VIN " + x.VIN + " decodes to " + nullText("vPIC make", x.Make) + ", " + nullText("vPIC model", x.Model) + ", " + nullText("model year", x.ModelYear) + "."
	}
	for _, s := range tr.Spine {
		line := refText(s.SpineRef)
		if s.Name != "" {
			line += ": " + s.Name
		}
		if s.Drift {
			line += " (not in data month " + tr.DataMonth + ")"
		}
		if s.Under != nil {
			line += ", under " + named(*s.Under)
		}
		v.Spine = append(v.Spine, line)
		if u := unconfirmedText(s.Unconfirmed, elements); u != "" {
			v.Spine = append(v.Spine, refText(s.SpineRef)+": "+u)
		}
	}
	// An entry's link back goes to the source vocabulary, then the
	// request's other targets, in the same model year.
	var back []string
	if tr.From.Vocabulary != "" {
		back = append(back, tr.From.Vocabulary)
	}
	for _, t := range tr.To {
		if !slices.Contains(back, t.Vocabulary) {
			back = append(back, t.Vocabulary)
		}
	}
	for _, t := range tr.To {
		tv := targetView{Vocabulary: t.Vocabulary}
		for _, e := range t.Entries {
			q := url.Values{"from.vocabulary": {t.Vocabulary}}
			for k, s := range e.Entry {
				q.Set("from.entry."+k, s)
			}
			if year != "" {
				q.Set("model_year", year)
			}
			for _, n := range back {
				if n != t.Vocabulary {
					q.Add("to", n)
				}
			}
			ev := entryView{Name: fields(t.Vocabulary, e.Entry), Href: translatePath + "?" + q.Encode()}
			for _, via := range e.Via {
				ev.Details = append(ev.Details, viaText(via, named))
			}
			if e.Note != "" {
				ev.Details = append(ev.Details, "Note: "+e.Note)
			}
			if u := unconfirmedText(e.Unconfirmed, elements); u != "" {
				ev.Details = append(ev.Details, u)
			}
			tv.Entries = append(tv.Entries, ev)
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

// catalogPage is /ui/vocabularies: one list of vocabularies, /vocab's,
// so it sends the visitor there.
func (h *Handler) catalogPage(*http.Request) (view, error) {
	return view{location: vocabPath, status: http.StatusSeeOther}, nil
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

// filesPanel is /ui/files: the merged catalog with the browser's files'
// removal, a remove it refused, and EPA's vocabulary to load; and /vocab's
// translate forms over the merged catalog, the visitor's selection
// ticked, which htmx swaps over the server's out of band.
type filesPanel struct {
	Catalog   catalogTable
	Offer     *offerView
	DataMonth string
	Release   string
	Refusal   *removeRefusal
	Translate translatePage
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
	selection, err := h.lib.Selection()
	if err != nil {
		return filesPanel{}, err
	}
	t, err := h.translateForms(cat, url.Values{"to": selection.Names()})
	if err != nil {
		return filesPanel{}, err
	}
	p := filesPanel{Catalog: catalogTable{Rows: h.rows(cat), Manage: true}, DataMonth: h.lib.Month(), Release: Release(h.lib.Info()), Translate: t}
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
		return view{location: vocabPath, status: http.StatusSeeOther}, nil
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
	info := h.p.Info()
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
	// Elements are the data month's element names by id, which a
	// translation's unchecked qualifiers are named by.
	Elements map[int32]string `json:"elements,omitempty"`
}

// workerFile is one of the server's files. Spine is a vocabulary's, which
// worker.js keeps the selection by when it sends a translation from it to
// the server.
type workerFile struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Version string `json:"version"`
	Blake3  string `json:"blake3"`
	URL     string `json:"url"`
	Spine   string `json:"spine,omitempty"`
}

// ReadWorkerConfig reads a worker's config: the Info it describes (the
// release and the server's files, what a Library links) and the
// vocabulary the server offers, nil for none (NewInBrowser).
func ReadWorkerConfig(config []byte) (contract.Info, *Offer, error) {
	var c workerConfig
	if err := json.Unmarshal(config, &c); err != nil {
		return contract.Info{}, nil, err
	}
	info := contract.Info{DecoderVersion: c.DecoderVersion, DataMonth: c.DataMonth, ArtifactHash: c.ArtifactHash, Vocabularies: []contract.VocabularyFile{}}
	for _, id := range slices.Sorted(maps.Keys(c.Elements)) {
		info.Elements = append(info.Elements, contract.Element{ElementID: id, Variable: c.Elements[id]})
	}
	for _, f := range c.Files {
		info.Vocabularies = append(info.Vocabularies, contract.VocabularyFile{Name: f.Name, Kind: f.Kind, Version: f.Version, Blake3: f.Blake3})
	}
	return info, c.EPA, nil
}

// serviceWorker answers /sw.js: the release's worker. Its bytes name the
// release and the server's files, so a new release, or a server restarted
// with other files, is a new worker, which the browser installs beside
// the old one and starts once the old one's pages close. It is never
// cached without asking (no-cache), and it alone may run WebAssembly.
func (h *Handler) serviceWorker(w http.ResponseWriter, info contract.Info, base string) error {
	cat, err := h.catalog()
	if err != nil {
		return err
	}
	c := workerConfig{Assets: base, Release: Release(info), DecoderVersion: info.DecoderVersion, DataMonth: info.DataMonth, ArtifactHash: info.ArtifactHash, Files: []workerFile{}, EPA: h.offered()}
	if len(info.Elements) > 0 {
		c.Elements = map[int32]string{}
		for _, e := range info.Elements {
			c.Elements[e.ElementID] = e.Variable
		}
	}
	for _, f := range info.Vocabularies {
		wf := workerFile{Name: f.Name, Kind: f.Kind, Version: f.Version, Blake3: f.Blake3, URL: FilePath(info, f)}
		if i := slices.IndexFunc(cat, func(e contract.CatalogEntry) bool { return e.Name == f.Name }); i >= 0 && cat[i].Kind == "vocabulary" {
			wf.Spine = cat[i].Spine
		}
		c.Files = append(c.Files, wf)
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
