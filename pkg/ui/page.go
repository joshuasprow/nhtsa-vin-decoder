package ui

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The page (ADR 0013). GET / is nitrovin's one page for browsers. At its
// top is a box of controls with a tab for each kind of lookup: a VIN,
// which is decoded and translated; an entry of a vehicle vocabulary, a
// year, make and model; an entry of each declared spine's vocabularies;
// and a vPIC make or model id. Each tab is a link (/?tab=…), so the tabs
// need no script, and each holds one form, which asks the page itself:
// its query is GET /v1/translate's, with a VIN's decode options (shape,
// full) beside it, so every answer is a shareable URL, and a query's from
// says which tab it is of. Below the controls is the answer: for a VIN
// its decode, and for every lookup its translation drawn as a map
// (map.go), with the JSON of both behind a button, in a dialog. A refusal
// is the page still, said in the controls beside the field it names, with
// its status. The vocabularies loaded are behind another button, fixed to
// the window's corner, in a dialog of their own (vocab.go), which the
// layout holds beside the page's main, so no answer replaces it.
//
// The page answers the same on the server and in the browser's Service
// Worker, which answers it once the browser holds a file, over the
// server's files and the browser's; a VIN's decode is the server's there
// too (decodedPath). Without JavaScript every step is a page load, and
// both dialogs still open: they are popovers. Old links, to /ui/decode,
// /ui/translate, /vocab and the form at /?vin=…, move to the page.

const (
	homePath = "/"
	// homeTitle is the page's title before it has an answer.
	homeTitle = "Decode and translate"
	// tabParam names the tab a link opens; a query's from names its own.
	tabParam = "tab"
	// tabVIN and tabID are the tabs of a VIN and of a vPIC id; an entry
	// form's is its Key.
	tabVIN = "vin"
	tabID  = "id"
)

// decodeParams are the page's parameters a VIN's decode reads; shape and
// full are the decode's alone, which the translation is not asked with.
var decodeParams = []string{"model_year", "shape", "full", "now"}

// homePage is what the page's template renders.
type homePage struct {
	Controls *controls
	// Result is the answer, nil before anything is asked and for a
	// refusal.
	Result                    *result
	DataMonth, DecoderVersion string
	Links                     formLinks
}

// tab is one of the controls' tabs: a link to the page with its form.
type tab struct {
	Key, Label, Href string
	Current          bool
}

// controls is the box of controls: the tabs, the current tab's form
// filled from the query, and examples to try in it.
type controls struct {
	Tabs []tab
	// Tab is the current tab's key.
	Tab string
	// Vocabularies are the catalog's vocabularies, the targets.
	Vocabularies []contract.CatalogEntry
	// From is the vocabulary an entry is translated from: the query's,
	// else on an entry tab its first; Fields its entry's fields with the
	// query's values.
	From   *contract.CatalogEntry
	Fields []fieldValue
	// FromVIN, FromMake, FromModel, VIN, ModelYear, Shape, Full and
	// ShowEmpty are the query's.
	FromVIN, FromMake, FromModel, VIN, ModelYear, Shape string
	Full, ShowEmpty                                     bool
	// To are the targets ticked: the query's, or in the browser the
	// visitor's selection.
	To []string
	// Errors holds a refusal's detail under its field's name (formFields),
	// or under "" when no field holds the offending value.
	Errors   map[string]string
	Examples []example
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
func (c *controls) TargetsOn(spine string) targetSet {
	out := targetSet{Spine: spine}
	for _, v := range c.Vocabularies {
		if v.Spine == spine {
			out.List = append(out.List, target{v.Name, slices.Contains(c.To, v.Name)})
		}
	}
	return out
}

// entryForm is a form from a vocabulary's entry, over the vocabularies on
// one spine and offering that spine's targets, whether a vocabulary is
// chosen yet or not: a translation crosses exactly one spine (ADR 0010).
// Key is its tab's, and names its elements ("entry" for vPIC's); From is
// the vocabulary chosen, nil on a tab that is not the current one, and
// Fields, VIN and Year its entry's values as asked.
type entryForm struct {
	Spine, Key   string
	Vocabularies []contract.CatalogEntry
	From         *contract.CatalogEntry
	Targets      targetSet
	Fields       []fieldValue
	VIN, Year    string
}

// entryKey is the key of the entry form over spine.
func entryKey(spine string) string {
	if spine == vocab.Reserved {
		return "entry"
	}
	return "entry-" + spine
}

// Label is the form's tab: the vehicle vocabularies' by what they ask
// for, a declared spine's by what its vocabularies name.
func (f entryForm) Label() string {
	if f.Spine == vocab.Reserved {
		return "Year, make and model"
	}
	subject := "entry"
	if len(f.Vocabularies) > 0 && f.Vocabularies[0].Subject != "" {
		subject = f.Vocabularies[0].Subject
	}
	return strings.ToUpper(subject[:1]) + subject[1:] + " (" + f.Spine + " spine)"
}

// EntryForms are the forms from a vocabulary's entry: one for the vehicle
// vocabularies, on vpic, then one for each declared spine's vocabularies,
// by spine; none for a spine no vocabulary is on.
func (c *controls) EntryForms() []entryForm {
	var spines, declared []string
	for _, v := range c.Vocabularies {
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
		f := entryForm{Spine: s, Key: entryKey(s), Targets: c.TargetsOn(s)}
		for _, v := range c.Vocabularies {
			if v.Spine == s {
				f.Vocabularies = append(f.Vocabularies, v)
			}
		}
		if c.From != nil && c.From.Spine == s {
			f.From, f.Fields, f.VIN, f.Year = c.From, c.Fields, c.VIN, c.ModelYear
		}
		out[i] = f
	}
	return out
}

// Entry is the current tab's entry form, nil on the VIN's and the vPIC
// id's tabs.
func (c *controls) Entry() *entryForm {
	for _, f := range c.EntryForms() {
		if f.Key == c.Tab {
			return &f
		}
	}
	return nil
}

// example is a lookup to try: a link to the page with its form filled in
// and its answer below. Its targets are the ticked ones on its Spine,
// else DefaultTo (names, space-separated: none for the examples that need
// no file). vocab.js sends an example clicked to the targets ticked at
// that moment.
type example struct {
	Label, Hint, Href, Spine, DefaultTo string
}

// The examples that need no file: three VINs, and the vPIC model the
// first decodes to, in its year.
var exampleVINs = []struct{ vin, hint string }{
	{"1HGCM82633A004352", "a 2003 Honda Accord"},
	{"5YJ3E1EA2JF000316", "a 2018 Tesla Model 3"},
	{"1C6RR7LT9FS123456", "a 2015 Ram 1500"}, // check digit valid; the README's ...5FS... is not
}

const (
	exampleModel = "1861" // vPIC's Honda Accord, under make 474
	exampleYear  = "2003"
	// exampleScan is how many of a vocabulary's first entries are looked
	// through for its example, the first with a mapping.
	exampleScan = 100
)

// newControls are the controls over the catalog cat for the page asked
// with raw, its query as sent, and q, the lookup it asks for: the tabs,
// the current one's form and its examples, a VIN's and a vPIC id's that
// need no file, an entry tab's one from each of its vocabularies, the
// first mapped entry, in a year its mapping holds for.
func (h *Handler) newControls(cat []contract.CatalogEntry, raw, q url.Values) (*controls, error) {
	c := &controls{
		FromVIN: raw.Get("from.vin"), FromMake: q.Get("from.make"), FromModel: q.Get("from.model"), VIN: q.Get("vin"), ModelYear: q.Get("model_year"),
		Shape: cmp.Or(q.Get("shape"), string(contract.ShapeVPIC)), Full: q.Get("full") == "true", ShowEmpty: raw.Get(showEmpty) == "true",
		To: q["to"],
	}
	for _, e := range cat {
		if e.Kind == "vocabulary" {
			c.Vocabularies = append(c.Vocabularies, e)
		}
	}
	// By name: where a file lives (the server, the browser) changes the
	// catalog's load order, never the form.
	slices.SortFunc(c.Vocabularies, func(a, b contract.CatalogEntry) int { return strings.Compare(a.Name, b.Name) })
	choose := func(v *contract.CatalogEntry) {
		c.From, c.Fields = v, nil
		for _, f := range v.Fields {
			c.Fields = append(c.Fields, fieldValue{f, q.Get("from.entry." + f)})
		}
	}

	// The tab is the from's own, whatever it holds, else the link's.
	switch {
	case raw.Has("from.vin"):
		c.Tab = tabVIN
	case raw.Has("from.vocabulary"):
		if i := slices.IndexFunc(c.Vocabularies, func(v contract.CatalogEntry) bool { return v.Name == raw.Get("from.vocabulary") }); i >= 0 {
			choose(&c.Vocabularies[i])
			c.Tab = entryKey(c.From.Spine)
		}
	case raw.Has("from.spine"):
		c.Tab = entryKey(raw.Get("from.spine"))
	case raw.Has("from.make"), raw.Has("from.model"), raw.Has("from.ext"):
		c.Tab = tabID
	default:
		c.Tab = raw.Get(tabParam)
	}
	c.Tabs = []tab{{Key: tabVIN, Label: "VIN", Href: homePath}}
	for _, f := range c.EntryForms() {
		c.Tabs = append(c.Tabs, tab{Key: f.Key, Label: f.Label(), Href: homePath + "?" + tabParam + "=" + url.QueryEscape(f.Key)})
	}
	c.Tabs = append(c.Tabs, tab{Key: tabID, Label: "vPIC id", Href: homePath + "?" + tabParam + "=" + tabID})
	current := slices.IndexFunc(c.Tabs, func(t tab) bool { return t.Key == c.Tab })
	if current < 0 {
		current, c.Tab = 0, tabVIN
	}
	c.Tabs[current].Current = true
	// A tab opens on its first vocabulary: its select changes it.
	if f := c.Entry(); f != nil && c.From == nil {
		i := slices.IndexFunc(c.Vocabularies, func(v contract.CatalogEntry) bool { return v.Name == f.Vocabularies[0].Name })
		choose(&c.Vocabularies[i])
	}

	switch f := c.Entry(); {
	case c.Tab == tabVIN:
		for _, e := range exampleVINs {
			c.Examples = append(c.Examples, c.example(e.vin, e.hint, vocab.Reserved, nil, url.Values{"from.vin": {e.vin}}))
		}
	case c.Tab == tabID:
		c.Examples = []example{
			c.example("vPIC model "+exampleModel, "the Honda Accord, model year "+exampleYear, vocab.Reserved, nil, url.Values{"from.model": {exampleModel}, "model_year": {exampleYear}}),
		}
	case f != nil:
		for _, v := range f.Vocabularies {
			ex, ok, err := h.entryExample(c, v)
			if err != nil {
				return nil, err
			}
			if ok {
				c.Examples = append(c.Examples, ex)
			}
		}
	}
	return c, nil
}

// entryExample is the example from vocabulary v: its first mapped entry,
// in a year its mapping holds for; none (false) for a vocabulary whose
// first entries map nowhere, and for one whose entry names nothing in
// another vocabulary on its spine.
func (h *Handler) entryExample(c *controls, v contract.CatalogEntry) (example, bool, error) {
	l, err := h.p.List(contract.ListRequest{Of: v.Name, Limit: exampleScan})
	if err != nil {
		return example{}, false, err
	}
	var e contract.VocabularyEntry
	for _, le := range l.Entries {
		if ve, ok := le.(contract.VocabularyEntry); ok && len(ve.Mappings) > 0 {
			e = ve
			break
		}
	}
	if len(e.Mappings) == 0 {
		return example{}, false, nil
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
	ex := c.example(strings.Join(label, " · "), hint, v.Spine, []string{c.sensibleTarget(v)}, q)
	if c.sensibleTarget(v) != v.Name {
		// Kept only when it names something in another vocabulary, or
		// the only target ticked is its own.
		u, err := url.Parse(ex.Href)
		if err != nil {
			return example{}, false, nil
		}
		req, err := contract.ParseTranslateQuery(u.Query())
		if err != nil {
			return example{}, false, nil
		}
		tr, err := h.p.Translate(req)
		if err != nil {
			return example{}, false, nil
		}
		if !slices.ContainsFunc(tr.To, func(t contract.Translated) bool {
			return len(t.Entries) > 0 && (t.Vocabulary != v.Name || len(tr.To) == 1)
		}) {
			return example{}, false, nil
		}
	}
	return ex, true, nil
}

// example is the example from q, a lookup without targets, to the ticked
// targets on spine, else to def.
func (c *controls) example(label, hint, spine string, def []string, q url.Values) example {
	var to []string
	for _, t := range c.TargetsOn(spine).List {
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
	return example{Label: label, Hint: hint, Href: homePath + "?" + q.Encode(), Spine: spine, DefaultTo: strings.Join(def, " ")}
}

// sensibleTarget is what v's example translates to when nothing on its
// spine is ticked: the first other vocabulary on it, else v itself.
func (c *controls) sensibleTarget(v contract.CatalogEntry) string {
	for _, t := range c.TargetsOn(v.Spine).List {
		if t.Name != v.Name {
			return t.Name
		}
	}
	return v.Name
}

// result is the page's answer: a VIN's decode, nil for another lookup,
// and the translation as its map and as JSON.
type result struct {
	// Heading names what was looked up: the vehicle, "2015 Ram 1500".
	Heading string
	Decode  *formDecode
	// ShowEmpty and EmptyToggle are the decode's empty values, shown or
	// hidden, and this page with them the other way: the vpic shape's.
	ShowEmpty   bool
	EmptyToggle string
	Map         mapPage
	// JSON are the answer's objects, each in the dialog: the translation,
	// and a VIN's decode.
	JSON []jsonDoc
	// URL is this page's.
	URL string
}

// jsonDoc is one JSON object the dialog shows: its element's id, what it
// is, in a heading and for its copy button, the request on /v1 that
// answers it ("" for a translation, which only this browser may be able
// to answer), and its text, indented.
type jsonDoc struct {
	ID, Title, What, Href, Text string
}

// oldForm reports whether q is an old link to the decode form, /?vin=…:
// a VIN that is no translation's context.
func oldForm(q url.Values) bool {
	return q.Has("vin") && !slices.ContainsFunc(slices.Collect(maps.Keys(q)), func(k string) bool { return strings.HasPrefix(k, "from.") })
}

// moved answers a page that the one page holds now by sending the
// visitor there, its query along: the decode form's VIN is the page's
// from.
func (h *Handler) moved(r *http.Request) (view, error) {
	q := r.URL.Query()
	if r.URL.Path == decodePath || oldForm(q) {
		if vs, ok := q["vin"]; ok {
			q["from.vin"] = vs
			delete(q, "vin")
		}
	}
	loc := homePath
	if len(q) > 0 {
		loc += "?" + q.Encode()
	}
	return view{location: loc, status: http.StatusMovedPermanently}, nil
}

// home is GET /: the controls without a query, otherwise the controls
// filled in from the query and the answer below them, or the refusal in
// them. A refusal is the page still, with its status, so htmx sends it to
// the page's main and not to the error slot.
func (h *Handler) home(r *http.Request) (view, error) {
	info := h.p.Info()
	page := &homePage{DataMonth: info.DataMonth, DecoderVersion: info.DecoderVersion, Links: h.links(info)}
	v := view{title: homeTitle, template: "home", data: page, target: mainTarget, aside: true}
	raw, malformed := url.ParseQuery(r.URL.RawQuery)
	if malformed != nil {
		raw = url.Values{}
	}
	if oldForm(raw) {
		return h.moved(r)
	}
	// The lookup: the query without the page's own parameters and the
	// fields a form sent empty.
	q := url.Values{}
	for k, vs := range raw {
		if k == tabParam || k == showEmpty {
			continue
		}
		for _, s := range vs {
			if strings.TrimSpace(s) != "" {
				q[k] = append(q[k], s)
			}
		}
	}
	cat, err := h.catalog()
	if err != nil {
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
	c, err := h.newControls(cat, raw, q)
	if err != nil {
		return view{}, err
	}
	page.Controls = c
	refuse := func(err error) (view, error) {
		var field, detail string
		if e, ok := errors.AsType[*pageError](err); ok {
			v.status, detail = e.Status, e.Message
		} else {
			p := contract.AsProblem(err)
			if p.Type == contract.Internal {
				return view{}, err // a fault in nitrovin: the error page, logged
			}
			v.status, detail = p.Status, p.Detail
			if f := strings.TrimPrefix(p.Pointer, "/"); c.Tab == tabVIN && slices.Contains(formFields, f) {
				field = f
			}
		}
		c.Errors = map[string]string{field: detail}
		return v, nil
	}
	if malformed != nil {
		return refuse(contract.NewProblem(contract.InvalidRequest, "", "the query string is malformed: "+malformed.Error()))
	}
	if err := h.notHere(named(q), cat); err != nil {
		return refuse(err)
	}
	entry := slices.ContainsFunc(c.Fields, func(f fieldValue) bool { return f.Value != "" })
	if !(raw.Has("from.vin") || entry || q.Has("from.make") || q.Has("from.model") || q.Has("from.ext") || q.Has("from.spine")) {
		return v, nil
	}
	// A lookup's own targets are the visitor's selection from now on; a
	// form's, before anything is asked, are only ticked.
	if explicit {
		h.remember(r, q, cat)
	}

	res := &result{ShowEmpty: c.ShowEmpty, URL: requestOrigin(r) + r.URL.RequestURI()}
	if c.Tab == tabVIN {
		dq := url.Values{}
		for _, k := range decodeParams {
			if vs, ok := q[k]; ok {
				dq[k] = vs
			}
		}
		if res.Decode, err = h.decoded(r, c.FromVIN, dq, c.ShowEmpty); err != nil {
			return refuse(err)
		}
	}
	tq := maps.Clone(q)
	delete(tq, "shape")
	delete(tq, "full")
	req, err := contract.ParseTranslateQuery(tq)
	if err != nil {
		return refuse(err)
	}
	tr, err := h.p.Translate(req)
	if err != nil {
		return refuse(err)
	}
	res.Map = newMapPage(tr, cat, info.Elements, q.Get("model_year"))
	res.Heading = res.Map.Heading
	// The files used, by name: where a file lives (the server, the
	// browser) changes their load order, never the page.
	tr.Vocabularies = slices.Clone(tr.Vocabularies)
	slices.SortFunc(tr.Vocabularies, func(a, b contract.FileVersion) int { return strings.Compare(a.Name, b.Name) })
	body, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		return view{}, err
	}
	res.JSON = []jsonDoc{{ID: "translation-json", Title: "The translation", What: "the translation's JSON", Text: string(body)}}
	if d := res.Decode; d != nil {
		var text bytes.Buffer
		if err := json.Indent(&text, []byte(d.Copy.JSON), "", "  "); err != nil {
			return view{}, err
		}
		res.JSON = append(res.JSON, jsonDoc{ID: "decode-json", Title: "The decode", What: "the decode's JSON", Href: d.JSON, Text: text.String()})
		if d.VPIC {
			toggle := maps.Clone(raw)
			if c.ShowEmpty {
				toggle.Del(showEmpty)
			} else {
				toggle.Set(showEmpty, "true")
			}
			res.EmptyToggle = homePath + "?" + toggle.Encode()
		}
	}
	page.Result = res
	v.title = res.Heading
	return v, nil
}

// asidePage is the page's vocabularies, behind their button: the catalog,
// and whether the visitor's own files can be loaded, which needs the
// worker's module on the server.
type asidePage struct {
	Catalog catalogTable
	Worker  bool
}

// asideOf is the vocabularies of a page that shows them, drawn beside its
// main when the page is whole.
func (h *Handler) asideOf() (asidePage, error) {
	cat, err := h.catalog()
	if err != nil {
		return asidePage{}, err
	}
	return asidePage{Catalog: catalogTable{Rows: h.rows(cat)}, Worker: h.worker != nil || h.lib != nil}, nil
}
