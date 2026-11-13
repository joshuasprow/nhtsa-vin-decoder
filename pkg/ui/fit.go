package ui

import (
	"cmp"
	"context"
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

// The fit page (ADR 0023). GET /ui/fit answers a fit (ADR 0018) from the
// catalog loaded where it is answered: its query is GET /v1/fit's, so a
// page link and an API call say the same thing, and the page draws the
// answer. At its top are the translate page's inputs as tabs, each a
// link (/ui/fit?tab=…) holding one form that asks the page itself: a VIN,
// an entry of a vehicle vocabulary, a vPIC make or model id, each with a
// year; and a fitment's key. Below them is the answer:
//
//   - from a vehicle, its part types with their counts, each a link to
//     its candidates;
//   - with a part, the candidates, each application with how it was
//     reached (via) and what could not be checked (unconfirmed), each
//     fitment's number a link to its vehicles;
//   - with related, the related vehicles, a section of their own that
//     each vehicle's answer links;
//   - from a fitment key, its vehicles.
//
// Every status reads as a sentence, no-catalog included; every list
// pages with the API's cursors (after, next); and the stock lists show
// when stock is loaded: the links the page draws to candidates, a
// fitment's vehicles and related vehicles ask for every loaded kind of
// stock each takes. The page works without JavaScript, every step a page
// load; with it, htmx fills the layout's main and pushes the URL.
//
// The server and the browser's Service Worker answer it with this same
// handler, over their Pages' Fit: the worker when the browser holds the
// catalog, the server otherwise.

const (
	fitPath  = "/ui/fit"
	fitTitle = "Fit parts"
	// The fit page's tabs: a VIN, a vehicle vocabulary's entry, a vPIC
	// id, and a fitment's key.
	fitTabVIN     = "vin"
	fitTabEntry   = "entry"
	fitTabID      = "id"
	fitTabFitment = "fitment"
)

// The answers a fit's inputs ask for (ADR 0018).
const (
	fitPartTypes  = "part-types"
	fitCandidates = "candidates"
	fitVehicles   = "vehicles"
	fitRelated    = "related"
)

// fitPage is what the fit page's template renders.
type fitPage struct {
	Controls *fitControls
	// NoCatalog says, before anything is asked, that no catalog is loaded
	// here; "" when one is.
	NoCatalog string
	// Demo marks a page the demo set's catalog answers, which says its
	// data is invented.
	Demo bool
	// Result is the answer, nil before anything is asked and for a
	// refusal.
	Result                    *fitResult
	DataMonth, DecoderVersion string
	Links                     pageLinks
}

// fitControls are the fit page's tabs, the current one's form filled in
// from the query, and examples to try.
type fitControls struct {
	Tabs []tab
	Tab  string
	// Vocabularies are the vehicle vocabularies on vPIC: an entry's
	// vocabulary, and the targets (to).
	Vocabularies []contract.CatalogEntry
	// From is the vocabulary an entry is of, and Fields its entry's
	// fields with the query's values.
	From   *contract.CatalogEntry
	Fields []fieldValue
	// The query's values.
	VIN, Make, Model, Ext, ModelYear, Side  string
	FitmentType, FitmentNumber, FitmentSide string
	To                                      []string
	// ShowExt shows the extension's field: the query asks for one.
	ShowExt bool
	// Stock are the kinds of stock a fitment's form asks for: every kind
	// loaded.
	Stock []string
	// Errors holds a refusal's detail under the name of the form's field
	// holding the offending value, or under "" when none does.
	Errors   map[string]string
	Examples []example
}

// Sides are the sides a form offers, "" first, for none.
func (*fitControls) Sides() []string {
	return []string{"", contract.SideLeft, contract.SideRight, contract.SideBoth}
}

// formField is one field of a fit form: its parameter, element id,
// label, hint, value and the refusal it holds, if any. Numeric marks a
// number's field.
type formField struct {
	Name, ID, Label, Hint, Value, Error string
	Numeric                             bool
	// Options are a select's, Sides.
	Options []string
}

// Field is the text field of parameter name.
func (c *fitControls) Field(name, id, label, hint, value string) formField {
	return formField{Name: name, ID: id, Label: label, Hint: hint, Value: value, Error: c.Errors[name],
		Numeric: name == "model_year" || name == "from.make" || name == "from.model"}
}

// SideField is the select of a side, of parameter name.
func (c *fitControls) SideField(name, id, label, value string) formField {
	return formField{Name: name, ID: id, Label: label, Value: value, Error: c.Errors[name], Options: c.Sides()}
}

// ModelYearHint is what the model year's field does on the current tab.
func (c *fitControls) ModelYearHint() string {
	switch c.Tab {
	case fitTabFitment:
		return "(optional: keeps the applications of that year)"
	case fitTabVIN:
		return "(optional hint: a decoded year wins)"
	}
	return "(optional: every year without it)"
}

// Targets are the vehicle vocabularies a fit can name vehicles in, ticked
// as the query asks.
func (c *fitControls) Targets() targetSet {
	out := targetSet{Spine: vocab.Reserved}
	for _, v := range c.Vocabularies {
		out.List = append(out.List, target{v.Name, slices.Contains(c.To, v.Name)})
	}
	return out
}

// fields are the names of the current form's fields.
func (c *fitControls) fields() []string {
	switch c.Tab {
	case fitTabVIN:
		return []string{"from.vin", "model_year", "side"}
	case fitTabID:
		return []string{"from.make", "from.model", "from.ext", "model_year", "side"}
	case fitTabFitment:
		return []string{"from.fitment.type", "from.fitment.number", "from.fitment.side", "model_year"}
	}
	out := []string{"from.vocabulary", "model_year", "side"}
	for _, f := range c.Fields {
		out = append(out, "from.entry."+f.Name)
	}
	return out
}

// field is the current form's field holding the value a refusal's
// pointer names, "" for none: the pointer's path, dotted, as the query
// names its parameter. A VIN's pointer is /from.
func (c *fitControls) field(pointer string) string {
	if pointer == "" {
		return ""
	}
	unescape := strings.NewReplacer("~1", "/", "~0", "~")
	var path []string
	for tok := range strings.SplitSeq(strings.TrimPrefix(pointer, "/"), "/") {
		path = append(path, unescape.Replace(tok))
	}
	name := strings.Join(path, ".")
	if name == "from" && c.Tab == fitTabVIN {
		name = "from.vin"
	}
	if slices.Contains(c.fields(), name) {
		return name
	}
	return ""
}

// fitResult is the fit page's answer.
type fitResult struct {
	// Kind is the answer the inputs asked for: fitPartTypes,
	// fitCandidates, fitVehicles or fitRelated.
	Kind    string
	Heading string
	Status  contract.FitStatus
	// Sentence is the status in words.
	Sentence string
	// Catalog names the catalog answering; Vehicle is a VIN's decode in
	// words.
	Catalog string
	Vehicle string
	Models  []fitModelView
	// Fitment and Reached are a fitment key's: the fitment it reached and
	// how, in words.
	Fitment *fitmentView
	Reached string

	PartTypes      []partTypeRow
	Candidates     []candidateView
	ShelfByVehicle *stockView
	Vehicles       []fitVehicleRow
	Shelf, Yard    *stockView
	Related        []relatedRow
	// Showing says how many of Total this page shows; Next and First are
	// the next page and the first, "" when there is none or this is it.
	Showing     string
	Next, First string
	// PartTypesHref and RelatedHref lead from a vehicle's answer to its
	// part types and its related vehicles, "" on that answer itself and
	// from a fitment.
	PartTypesHref, RelatedHref string
	JSON                       []jsonDoc
}

// fitModelView is one of the catalog's vehicles a vehicle reached.
type fitModelView struct {
	Name        string
	Via         []string
	Unconfirmed string
}

// fitmentView is a fitment in words: its key, and its link to its
// vehicles, "" on that answer itself.
type fitmentView struct {
	Type, Number, Side, Note string
	Href                     string
}

// Label is the fitment's number and side: "MR-1001, left".
func (f fitmentView) Label() string {
	if f.Side != "" {
		return f.Number + ", " + f.Side
	}
	return f.Number
}

// partTypeRow is one of a vehicle's part types, linked to its candidates.
type partTypeRow struct {
	Type, Name, Href string
	Count            int
	Reached          string
}

// candidateView is one candidate: its fitment, how it was reached, its
// applications that matched and its stock lists.
type candidateView struct {
	Fitment      fitmentView
	Reached      string
	Applications []applicationRow
	Shelf, Yard  *stockView
}

// applicationRow is one application of a candidate.
type applicationRow struct {
	Vehicle, Years, Options string
	Via                     []string
	Unconfirmed             string
}

// fitVehicleRow is one application of a fitment: its vehicle, linked to
// its part types, and the vehicle named.
type fitVehicleRow struct {
	Vehicle, Href, Years, Options, Names string
}

// relatedRow is one related vehicle.
type relatedRow struct {
	Vehicle, Href, Years, Names string
	Shared                      int
	PartTypes                   string
	Yard                        string
}

// stockView is a stock list as the page shows it: its rows, its sources
// in words, and a link to the whole list when the page shows only its
// first rows.
type stockView struct {
	Title string
	// Unknown marks a list a source of which is unavailable: its stock is
	// unknown, not empty.
	Unknown bool
	// Level is its heading's: 3 for the answer's own list, 4 for a
	// candidate's, under the candidate's heading.
	Level   int
	Total   int
	Rows    []stockRow
	Sources []string
	More    string
}

// stockRow is one stock row in words, its fields as the source wrote them.
type stockRow struct {
	Source, ID, Location, Matched, AsOf, Fields string
}

// fit is GET /ui/fit: the controls without a query, otherwise the controls
// filled in from the query and the answer below them, or the refusal in
// them, with its status.
func (h *Handler) fit(r *http.Request) (view, error) {
	info := h.info()
	page := &fitPage{DataMonth: info.DataMonth, DecoderVersion: info.DecoderVersion, Links: h.links(info)}
	v := view{title: fitTitle, template: "fit", data: page, target: mainTarget, vocabDialog: true, current: navFit}
	raw, malformed := url.ParseQuery(r.URL.RawQuery)
	if malformed != nil {
		raw = url.Values{}
	}
	// The fit: the query without the page's own tab and the fields a form
	// sent empty, a VIN aside, which is refused empty. A query with empty
	// fields, as a form sends it, moves to the query without them, so the
	// page's URL is always /v1/fit's query, which refuses them.
	q := url.Values{}
	dropped := false
	for k, vs := range raw {
		if k == tabParam {
			continue
		}
		for _, s := range vs {
			if strings.TrimSpace(s) != "" || k == "from.vin" {
				q[k] = append(q[k], s)
			} else {
				dropped = true
			}
		}
	}
	if dropped {
		clean := maps.Clone(q)
		if t, ok := raw[tabParam]; ok {
			clean[tabParam] = t
		}
		loc := fitPath
		if len(clean) > 0 {
			loc += "?" + clean.Encode()
		}
		return view{location: loc, status: http.StatusSeeOther}, nil
	}
	cat, err := h.catalog()
	if err != nil {
		return view{}, err
	}
	stock := stockLoaded(info)
	c := h.newFitControls(cat, raw, q, stock)
	page.Controls = c
	catalogName := catalogOf(info)
	page.Demo = isDemoCatalog(info)
	if catalogName == "" {
		page.NoCatalog = noCatalogText
		if h.demoLoadable() {
			page.NoCatalog += " " + demoLoadText
		}
	}
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
			field = c.field(p.Pointer)
		}
		c.Errors = map[string]string{field: detail}
		return v, nil
	}
	asked := slices.ContainsFunc(slices.Collect(maps.Keys(q)), func(k string) bool {
		return strings.HasPrefix(k, "from.") && k != "from.vocabulary"
	})
	if !asked {
		switch {
		case malformed != nil || catalogName == "":
		case page.Demo:
			c.Examples = c.ownExamples(demoExamples(info, cat), q.Get("from.vocabulary"))
		default:
			c.Examples = c.ownExamples(h.fitExamples(r.Context(), catalogName, stock, c.Tab), q.Get("from.vocabulary"))
		}
		if malformed != nil {
			return refuse(contract.NewProblem(contract.InvalidRequest, "", "the query string is malformed: "+malformed.Error()))
		}
		return v, nil
	}
	if malformed != nil {
		return refuse(contract.NewProblem(contract.InvalidRequest, "", "the query string is malformed: "+malformed.Error()))
	}
	if err := h.notHere(fitNamed(q), cat); err != nil {
		return refuse(err)
	}
	if err := h.serverFits(fitNamed(q)); err != nil {
		return refuse(err)
	}
	req, err := contract.ParseFitQuery(q)
	if err != nil {
		return refuse(err)
	}
	answer, err := h.p.Fit(r.Context(), req)
	if err != nil {
		return refuse(err)
	}
	res, err := h.newFitResult(req, q, answer, cat, info, stock)
	if err != nil {
		return view{}, err
	}
	if res.Status == contract.FitNoCatalog && h.demoLoadable() {
		res.Sentence += " " + demoLoadText
	}
	page.Result = res
	v.title = res.Heading
	return v, nil
}

// noCatalogText is no-catalog in words: where a catalog comes from, and
// that a browser's stays in it.
const noCatalogText = "No catalog is loaded here, so there is nothing to fit against. A server loads one beside its vocabularies (nitrovin serve --vocab). " +
	"A catalog loaded into a browser stays in that browser: open this link in the browser that holds it."

// catalogOf is the name of the catalog info lists, "" for none.
func catalogOf(info contract.Info) string {
	for _, f := range info.Vocabularies {
		if f.Kind == string(vocab.KindCatalog) {
			return f.Name
		}
	}
	return ""
}

// stockLoaded are the kinds of stock info lists a source of, shelf before
// yard.
func stockLoaded(info contract.Info) []string {
	var out []string
	for _, kind := range []string{contract.StockShelf, contract.StockYard} {
		if slices.ContainsFunc(info.Vocabularies, func(f contract.VocabularyFile) bool {
			return f.StockCounts != nil && f.StockCounts.Source == kind
		}) {
			out = append(out, kind)
		}
	}
	return out
}

// serverFits refuses, in a browser, a fit naming one of the browser's
// files while the server has the catalog: the server's catalog answers
// fit, and the browser's files never leave it (ADR 0013, ADR 0023). The
// worker sends every other fit to the server then.
func (h *Handler) serverFits(names []string) error {
	if h.lib == nil || h.lib.ServerCatalog() == "" {
		return nil
	}
	for _, f := range h.lib.Files() {
		if slices.Contains(names, f.Name) {
			return &pageError{Status: http.StatusConflict, Message: fmt.Sprintf(
				"This link names %s, a file of this browser's. This server's catalog, %s, answers fit, and this browser's files never leave it, so it can't fit this here: pick one of the server's files instead.", f.Name, h.lib.ServerCatalog())}
		}
	}
	return nil
}

// fitNamed is what a fit names of the loaded files: the from's and the
// part's vocabularies and spines, an extension's set, and the targets.
func fitNamed(q url.Values) []string {
	out := named(q)
	for _, k := range []string{"part.vocabulary", "part.spine"} {
		if v := q.Get(k); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// newFitControls are the controls over the catalog cat for the page asked
// with raw, its query as sent, and q, the fit it asks for.
func (h *Handler) newFitControls(cat []contract.CatalogEntry, raw, q url.Values, stock []string) *fitControls {
	c := &fitControls{
		VIN: raw.Get("from.vin"), Make: q.Get("from.make"), Model: q.Get("from.model"), Ext: q.Get("from.ext"),
		ModelYear: q.Get("model_year"), Side: q.Get("side"),
		FitmentType: q.Get("from.fitment.type"), FitmentNumber: q.Get("from.fitment.number"), FitmentSide: q.Get("from.fitment.side"),
		To: q["to"], ShowExt: raw.Has("from.ext"), Stock: stock,
	}
	for _, e := range cat {
		if e.Kind == "vocabulary" && e.Subject == "vehicle" && e.Spine == vocab.Reserved {
			c.Vocabularies = append(c.Vocabularies, e)
		}
	}
	slices.SortFunc(c.Vocabularies, func(a, b contract.CatalogEntry) int { return strings.Compare(a.Name, b.Name) })
	c.Tab = cmp.Or(fitTab(raw), raw.Get(tabParam))
	c.Tabs = []tab{{Key: fitTabVIN, Label: "VIN", Href: fitPath}}
	if len(c.Vocabularies) > 0 {
		c.Tabs = append(c.Tabs, tab{Key: fitTabEntry, Label: "Year, make and model", Href: fitPath + "?" + tabParam + "=" + fitTabEntry})
	}
	c.Tabs = append(c.Tabs,
		tab{Key: fitTabID, Label: "vPIC id", Href: fitPath + "?" + tabParam + "=" + fitTabID},
		tab{Key: fitTabFitment, Label: "Fitment", Href: fitPath + "?" + tabParam + "=" + fitTabFitment})
	current := slices.IndexFunc(c.Tabs, func(t tab) bool { return t.Key == c.Tab })
	if current < 0 {
		current, c.Tab = 0, fitTabVIN
	}
	c.Tabs[current].Current = true
	if c.Tab == fitTabEntry {
		// The query's vocabulary, else the first: its select changes it.
		i := slices.IndexFunc(c.Vocabularies, func(v contract.CatalogEntry) bool { return v.Name == q.Get("from.vocabulary") })
		if i < 0 {
			i = 0
		}
		c.From = &c.Vocabularies[i]
		for _, f := range c.From.Fields {
			c.Fields = append(c.Fields, fieldValue{f, q.Get("from.entry." + f)})
		}
	}
	return c
}

// fitTab is the tab whose form asks q's from, "" for a query without
// one: a VIN's, a vocabulary's entry's, a vPIC make's, model's or an
// extension's, or a fitment key's.
func fitTab(q url.Values) string {
	switch {
	case q.Has("from.vin"):
		return fitTabVIN
	case q.Has("from.vocabulary"):
		return fitTabEntry
	case q.Has("from.make"), q.Has("from.model"), q.Has("from.ext"):
		return fitTabID
	case q.Has("from.fitment.type"), q.Has("from.fitment.number"), q.Has("from.fitment.side"):
		return fitTabFitment
	}
	return ""
}

// ownExamples are those of exs the current tab's form asks, as the
// translate page offers a tab its own: each whose from is the tab's
// (fitTab), with its part, related and stock, and on the entry tab an
// entry of vocabulary, the one the query selects, when it selects one.
// A link to another page counts by its from too: the demo's translation
// asks from an entry.
func (c *fitControls) ownExamples(exs []example, vocabulary string) []example {
	return slices.DeleteFunc(exs, func(e example) bool {
		u, err := url.Parse(e.Href)
		if err != nil {
			return true
		}
		q := u.Query()
		return fitTab(q) != c.Tab || vocabulary != "" && q.Get("from.vocabulary") != vocabulary
	})
}

// fitExamples are fits to try on the catalog named, from the tab given:
// on the fitment tab its first fitment's vehicles; on the entry and the
// id tab the part types of that fitment's first vehicle, in its first
// year, which ownExamples keeps on the one its vehicle is asked on; on
// the VIN tab none, a catalog naming no VIN. A catalog that cannot be
// read for them is logged, and offers none.
func (h *Handler) fitExamples(ctx context.Context, catalog string, stock []string, tab string) []example {
	if tab == fitTabVIN {
		return nil
	}
	skip := func(err error) []example {
		h.log.Printf("ui: the fit page's examples from %s: %v", catalog, err)
		return nil
	}
	l, err := h.p.List(contract.ListRequest{Of: catalog, Limit: 1})
	if err != nil {
		return skip(err)
	}
	if len(l.Entries) == 0 {
		return nil
	}
	line, ok := l.Entries[0].(contract.FitmentLine)
	if !ok || len(line.Applications) == 0 {
		return skip(fmt.Errorf("its first entry is no fitment line: %T", l.Entries[0]))
	}
	key := line.Fitment
	if tab == fitTabFitment {
		label := key.Type + " " + key.Number
		if key.Side != "" {
			label += ", " + key.Side
		}
		return []example{{Label: label, Hint: "a fitment of " + catalog + ": its vehicles", Href: fitPath + "?" + fitmentQuery(key, nil, stock).Encode()}}
	}
	// The fitment's answer names the catalog's vehicle vocabulary: asked
	// without stock, so drawing the page never waits on a stock source.
	req, err := contract.ParseFitQuery(fitmentQuery(key, nil, nil))
	if err != nil {
		return skip(err)
	}
	req.Limit = 1
	fit, err := h.p.Fit(ctx, req)
	if err != nil {
		return skip(err)
	}
	if fit.Catalog == nil {
		return nil
	}
	app := line.Applications[0]
	vq := vehicleQuery(app.Vehicle, fit.Catalog.Vehicles)
	hint := "a vehicle of " + fit.Catalog.Name + ": its part types"
	if y := app.First; y.Valid || app.Last.Valid {
		if !y.Valid {
			y = app.Last
		}
		vq.Set("model_year", strconv.Itoa(int(y.V)))
		hint = "a vehicle of " + fit.Catalog.Name + ", model year " + strconv.Itoa(int(y.V)) + ": its part types"
	}
	return []example{{Label: vehicleText(app.Vehicle, fit.Catalog.Vehicles, nil), Hint: hint, Href: fitPath + "?" + vq.Encode()}}
}

// vehicleQuery is the query of a fit from v, a catalog's vehicle, an
// entry of its vehicle vocabulary vehicles or a vPIC make, model or
// extension.
func vehicleQuery(v contract.CatalogVehicle, vehicles string) url.Values {
	q := url.Values{}
	switch {
	case v.Entry != nil:
		q.Set("from.vocabulary", vehicles)
		for f, s := range v.Entry {
			q.Set("from.entry."+f, s)
		}
	case v.Make != 0:
		q.Set("from.make", strconv.Itoa(int(v.Make)))
	case v.Model != 0:
		q.Set("from.model", strconv.Itoa(int(v.Model)))
	case v.Ext != "":
		q.Set("from.ext", v.Ext)
	}
	return q
}

// fitmentQuery is the query of a fit from key, naming vehicles in to,
// with the stock kinds given.
func fitmentQuery(key contract.FitmentKey, to, stock []string) url.Values {
	q := url.Values{"from.fitment.type": {key.Type}, "from.fitment.number": {key.Number}}
	if key.Side != "" {
		q.Set("from.fitment.side", key.Side)
	}
	if len(to) > 0 {
		q["to"] = slices.Clone(to)
	}
	if len(stock) > 0 {
		q["stock"] = slices.Clone(stock)
	}
	return q
}

// vehicleText is a catalog's vehicle in words: its entry's fields in its
// vocabulary's order ("Dodge Ram 1500"), or a vPIC id or extension.
func vehicleText(v contract.CatalogVehicle, vehicles string, cat []contract.CatalogEntry) string {
	if v.Entry != nil {
		return entryText(cat, vehicles, v.Entry)
	}
	return refText(contract.SpineRef{Make: v.Make, Model: v.Model, Ext: v.Ext})
}

// newFitResult is the fit page's answer to req, its query q, from fit.
func (h *Handler) newFitResult(req contract.FitRequest, q url.Values, fit contract.Fit, cat []contract.CatalogEntry, info contract.Info, stock []string) (*fitResult, error) {
	res := &fitResult{Kind: fitKindOf(req), Status: fit.Status}
	vehicles := ""
	if fit.Catalog != nil {
		vehicles = fit.Catalog.Vehicles
		res.Catalog = fit.Catalog.Name + " " + fit.Catalog.Version
		if fit.Catalog.Release != "" {
			res.Catalog += ", release " + fit.Catalog.Release
		}
	}
	name := func(v contract.CatalogVehicle) string { return vehicleText(v, vehicles, cat) }
	if d := fit.Vehicle; d != nil {
		res.Vehicle = "VIN " + d.VIN + " decodes to " + decodedText(*d) + "."
	}
	for _, m := range fit.Models {
		mv := fitModelView{Name: name(m.Vehicle), Unconfirmed: unconfirmedText(m.Unconfirmed, info.Elements)}
		if m.Name != "" {
			mv.Name += " (" + m.Name + ")"
		}
		for _, v := range m.Via {
			mv.Via = append(mv.Via, viaText(v, refText))
		}
		res.Models = append(res.Models, mv)
	}

	// The vehicle's own query, the base of every link from its answer: its
	// from, year, side, targets, stock locations and clock.
	base := url.Values{}
	for k, vs := range q {
		if strings.HasPrefix(k, "from.") || k == "model_year" || k == "side" || k == "to" || k == "location" || k == "now" {
			base[k] = vs
		}
	}
	withStock := func(v url.Values, kinds []string) string {
		v = maps.Clone(v)
		if len(kinds) > 0 {
			v["stock"] = kinds
		} else {
			delete(v, "location")
		}
		return fitPath + "?" + v.Encode()
	}
	yard := slices.DeleteFunc(slices.Clone(stock), func(k string) bool { return k != contract.StockYard })
	fitmentHref := func(f contract.Fitment) string {
		fq := fitmentQuery(contract.FitmentKey{Type: f.Type, Number: f.Number, Side: f.Side}, req.To, stock)
		if len(stock) > 0 {
			if loc, ok := q["location"]; ok {
				fq["location"] = loc
			}
		}
		return fitPath + "?" + fq.Encode()
	}
	if res.Kind != fitVehicles {
		if res.Kind != fitPartTypes {
			res.PartTypesHref = withStock(base, nil)
		}
		if res.Kind != fitRelated {
			rq := maps.Clone(base)
			rq.Set("related", "true")
			res.RelatedHref = withStock(rq, yard)
		}
	}
	subject := subjectText(req, fit, cat)

	switch res.Kind {
	case fitPartTypes:
		res.Heading = "Part types: " + subject
		spine := ""
		if fit.Catalog != nil {
			spine = fit.Catalog.Spine
		}
		for _, p := range fit.PartTypes {
			pq := maps.Clone(base)
			pq.Set("part.spine", spine)
			pq.Set("part.id", p.Type)
			row := partTypeRow{Type: p.Type, Name: p.Name, Count: p.Count, Href: withStock(pq, stock)}
			if p.Reached == contract.ReachedRedirect {
				row.Reached = "through a redirect"
			}
			res.PartTypes = append(res.PartTypes, row)
		}
	case fitCandidates:
		res.Heading = "Candidates for " + partText(req.Part) + ": " + subject
		for _, cd := range fit.Candidates {
			cv := candidateView{Fitment: newFitmentView(cd.Fitment, fitmentHref(cd.Fitment)), Reached: candidateReached(cd)}
			for _, a := range cd.Applications {
				row := applicationRow{Vehicle: name(a.Vehicle), Years: yearsText(a.First, a.Last), Options: a.Options, Unconfirmed: unconfirmedText(a.Unconfirmed, info.Elements)}
				for _, v := range a.Via {
					row.Via = append(row.Via, viaText(v, refText))
				}
				cv.Applications = append(cv.Applications, row)
			}
			cv.Shelf = newStockView("On the shelf", 4, cd.Shelf, name, cv.Fitment.Href)
			cv.Yard = newStockView("In the yard", 4, cd.Yard, name, cv.Fitment.Href)
			res.Candidates = append(res.Candidates, cv)
		}
		res.ShelfByVehicle = newStockView("On the shelf with no number, for this vehicle", 3, fit.ShelfByVehicle, name, "")
	case fitVehicles:
		key := req.From.Fitment
		res.Heading = "Vehicles of " + key.Type + " " + key.Number
		if key.Side != "" {
			res.Heading += ", " + key.Side
		}
		if f := fit.Fitment; f != nil {
			fv := newFitmentView(*f, "")
			res.Fitment = &fv
			switch fit.Reached {
			case contract.ReachedRetired:
				res.Reached = "The number asked for is retired: " + f.Type + " " + fv.Label() + " replaced it"
				if r := fit.Retired; r != nil && r.Note != "" {
					res.Reached += " (" + r.Note + ")"
				}
				res.Reached += "."
			}
		}
		for _, fv := range fit.Vehicles {
			res.Vehicles = append(res.Vehicles, fitVehicleRow{
				Vehicle: name(fv.Vehicle), Href: vehicleHref(fv.Vehicle, vehicles, req.To),
				Years: yearsText(fv.First, fv.Last), Options: fv.Options, Names: namesText(fv.FitNames, cat),
			})
		}
		res.Shelf = newStockView("On the shelf", 3, fit.Shelf, name, "")
		res.Yard = newStockView("In the yard", 3, fit.Yard, name, "")
	case fitRelated:
		res.Heading = "Related vehicles: " + subject
		for _, rv := range fit.Related {
			row := relatedRow{Vehicle: name(rv.Vehicle), Href: vehicleHref(rv.Vehicle, vehicles, req.To), Years: yearsText(rv.First, rv.Last),
				Names: namesText(rv.FitNames, cat), Shared: rv.Shared}
			var types []string
			for _, p := range rv.PartTypes {
				types = append(types, cmpName(p)+" "+strconv.Itoa(p.Count))
			}
			row.PartTypes = strings.Join(types, ", ")
			if rv.Yard != nil {
				row.Yard = countText(rv.Yard.Total, "vehicle", "vehicles") + " in the yard"
			}
			res.Related = append(res.Related, row)
		}
	}
	res.Sentence = statusText(res.Kind, fit, req, subject)
	if fit.Status != contract.FitNoCatalog {
		res.Showing = showingText(res, fit)
	}
	if fit.Next != "" {
		nq := maps.Clone(q)
		nq.Set("after", fit.Next)
		res.Next = fitPath + "?" + nq.Encode()
	}
	if q.Has("after") {
		fq := maps.Clone(q)
		fq.Del("after")
		res.First = fitPath + "?" + fq.Encode()
	}
	body, err := jsonText(fit)
	if err != nil {
		return nil, err
	}
	doc := jsonDoc{ID: "fit-json", Title: "The fit", What: "the fit's JSON", Text: body}
	if h.b != nil {
		// Only the server's: /v1/fit never answers from a browser's catalog.
		doc.Href = "/v1/fit?" + q.Encode()
	}
	res.JSON = []jsonDoc{doc}
	return res, nil
}

// showingText says how much of its list an answer's page shows, and when
// stock lists that page with the list (a fitment's shelf and yard, the
// candidates' shelf by vehicle) go on past it.
func showingText(res *fitResult, fit contract.Fit) string {
	noun := map[string][2]string{
		fitPartTypes:  {"part type", "part types"},
		fitCandidates: {"candidate", "candidates"},
		fitVehicles:   {"application", "applications"},
		fitRelated:    {"related vehicle", "related vehicles"},
	}[res.Kind]
	n := len(fit.PartTypes) + len(fit.Candidates) + len(fit.Vehicles) + len(fit.Related)
	var paged []*stockView
	switch res.Kind {
	case fitVehicles:
		paged = []*stockView{res.Shelf, res.Yard}
	case fitCandidates:
		paged = []*stockView{res.ShelfByVehicle}
	}
	stockRows := 0
	for _, sv := range paged {
		if sv != nil {
			stockRows += len(sv.Rows)
		}
	}
	s := fmt.Sprintf("Showing %d of %d %s.", n, fit.Total, noun[1])
	if fit.Total == 1 {
		s = fmt.Sprintf("Showing %d of 1 %s.", n, noun[0])
	}
	if n == 0 && fit.Total > 0 && stockRows > 0 {
		s = "Every one of the " + countText(fit.Total, noun[0], noun[1]) + " is on an earlier page: this page goes on with the stock lists."
		if fit.Total == 1 {
			s = "The 1 " + noun[0] + " is on an earlier page: this page goes on with the stock lists."
		}
	}
	if fit.Next != "" && stockRows > 0 {
		s += " The stock lists page with the " + noun[1] + ", so the next page may hold stock alone."
	}
	return s
}

// fitKindOf is the answer req's inputs ask for.
func fitKindOf(req contract.FitRequest) string {
	switch {
	case req.From.Fitment != nil:
		return fitVehicles
	case req.Related:
		return fitRelated
	case req.Part != nil:
		return fitCandidates
	}
	return fitPartTypes
}

// subjectText is the vehicle a fit is from, in words, with its year:
// "VIN 1D7RV1GT5AS123456", "Dodge Ram 1500, 2010".
func subjectText(req contract.FitRequest, fit contract.Fit, cat []contract.CatalogEntry) string {
	f := req.From
	var s string
	switch {
	case f.VIN != "":
		s = "VIN " + f.VIN
	case f.Vocabulary != "":
		s = entryText(cat, f.Vocabulary, f.Entry)
		if s == "" {
			s = "an entry of " + f.Vocabulary
		}
	default:
		s = refText(f.SpineRef)
	}
	year := req.ModelYear
	if fit.Vehicle != nil && fit.Vehicle.ModelYear.Valid {
		year = fit.Vehicle.ModelYear
	}
	if year.Valid && f.VIN == "" {
		s += ", " + strconv.Itoa(int(year.V))
	}
	return s
}

// partText is the part type asked for in words: "part-types mirror",
// "part-codes ROT".
func partText(p *contract.FitPart) string {
	if p == nil {
		return "every part type"
	}
	if p.Vocabulary != "" {
		vs := make([]string, 0, len(p.Entry))
		for _, k := range slices.Sorted(maps.Keys(p.Entry)) {
			vs = append(vs, p.Entry[k])
		}
		return p.Vocabulary + " " + strings.Join(vs, " ")
	}
	return p.ID
}

// decodedText is a VIN's decode in words: "vPIC make 476, model 1938,
// model year 2010".
func decodedText(v contract.Vehicle) string {
	var parts []string
	if v.Make.Valid {
		parts = append(parts, "vPIC make "+strconv.Itoa(int(v.Make.V)))
	}
	if v.Model.Valid {
		parts = append(parts, "model "+strconv.Itoa(int(v.Model.V)))
	}
	if v.ModelYear.Valid {
		parts = append(parts, "model year "+strconv.Itoa(int(v.ModelYear.V)))
	}
	if len(parts) == 0 {
		return "no vPIC make or model"
	}
	return strings.Join(parts, ", ")
}

// newFitmentView is f, linked to href.
func newFitmentView(f contract.Fitment, href string) fitmentView {
	return fitmentView{Type: f.Type, Number: f.Number, Side: f.Side, Note: f.Note, Href: href}
}

// candidateReached is how a candidate was reached, in words, "" when
// directly.
func candidateReached(c contract.FitCandidate) string {
	switch c.Reached {
	case contract.ReachedRedirect:
		var why []string
		for _, r := range c.Redirects {
			s := "the catalog lists " + r.From + " under " + r.To
			if y := yearsText(r.First, r.Last); y != "" {
				s += " for " + y
			}
			if r.Options != "" {
				s += ": " + r.Options
			}
			why = append(why, s)
		}
		return "Reached through a redirect: " + strings.Join(why, "; ") + "."
	case contract.ReachedRetired:
		return "Reached through a retired number."
	}
	return ""
}

// vehicleHref is the link to a catalog vehicle's part types in every
// year, naming vehicles in to.
func vehicleHref(v contract.CatalogVehicle, vehicles string, to []string) string {
	q := vehicleQuery(v, vehicles)
	if len(to) > 0 {
		q["to"] = slices.Clone(to)
	}
	return fitPath + "?" + q.Encode()
}

// namesText is a vehicle named, in words: as spine entries ("vPIC model
// 1938 Ram"), or in each to vocabulary ("yard: Dodge Ram").
func namesText(n contract.FitNames, cat []contract.CatalogEntry) string {
	var out []string
	for _, e := range n.Spine {
		s := refText(e.SpineRef)
		if e.Name != "" {
			s += " " + e.Name
		}
		out = append(out, s)
	}
	for _, t := range n.To {
		var es []string
		for _, e := range t.Entries {
			es = append(es, entryText(cat, t.Vocabulary, e.Entry))
		}
		if len(es) == 0 {
			es = []string{"nothing"}
		}
		out = append(out, t.Vocabulary+": "+strings.Join(es, "; "))
	}
	return strings.Join(out, "; ")
}

// cmpName is a part type's name, else its id.
func cmpName(p contract.FitPartType) string {
	if p.Name != "" {
		return p.Name
	}
	return p.Type
}

// countText is n with the noun it counts: "1 vehicle", "2 vehicles".
func countText(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// newStockView is l as the page shows it, titled at level; nil for no
// list. more
// is the whole list's page, linked when l has more rows than it shows.
func newStockView(title string, level int, l *contract.StockList, name func(contract.CatalogVehicle) string, more string) *stockView {
	if l == nil {
		return nil
	}
	sv := &stockView{Title: title, Level: level, Total: l.Total}
	if l.Total > len(l.Rows) {
		sv.More = more
	}
	for _, r := range l.Rows {
		row := stockRow{Source: r.Source, ID: r.ID, Location: r.Location, AsOf: r.AsOf, Fields: string(r.Fields), Matched: stockMatched(r, name)}
		sv.Rows = append(sv.Rows, row)
	}
	for _, s := range l.Sources {
		sv.Sources = append(sv.Sources, sourceText(s))
		if s.Status == contract.StockUnavailable {
			sv.Unknown = true
		}
	}
	return sv
}

// stockMatched is how a stock row matched, in words.
func stockMatched(r contract.StockRow, name func(contract.CatalogVehicle) string) string {
	year := ""
	if r.Year.Valid {
		year = ", " + strconv.Itoa(int(r.Year.V))
	}
	switch r.Matched {
	case "fitment":
		if r.Fitment == nil {
			return "by its number"
		}
		key := r.Fitment.Type + " " + r.Fitment.Number
		if r.Fitment.Side != "" {
			key += ", " + r.Fitment.Side
		}
		if r.Reached == contract.ReachedRetired {
			return "filed under the retired number " + key
		}
		return "by its number, " + key
	case "vehicle":
		s := "by part type " + r.Part
		if r.Vehicle != nil {
			s += " for " + name(*r.Vehicle) + year
		}
		return s
	case "vin":
		if d := r.Decoded; d != nil {
			return fmt.Sprintf("by its source's decode, vPIC model %d, %d", d.Model, d.Year)
		}
		if r.VIN != "" {
			return "by its VIN, " + r.VIN
		}
		return "by its VIN"
	case "catalog-vehicle":
		if r.Vehicle != nil {
			return "by the catalog's vehicle, " + name(*r.Vehicle) + year
		}
		return "by the catalog's vehicle"
	}
	return r.Matched
}

// sourceText is a stock source's block in words: "shelf-stock, a file, as
// of …: numbered under release 7.01, not the catalog's".
func sourceText(s contract.StockSource) string {
	out := s.Name + ", "
	if s.Kind == "live" {
		out += "read live"
		if s.Relation != "" {
			out += " from " + s.Relation
		}
	} else {
		out += "a file"
	}
	if s.AsOf != "" {
		out += ", as of " + s.AsOf
	}
	if s.Status == contract.StockUnavailable {
		out += ": unavailable, so its stock is unknown, not empty"
		if s.Reason != "" {
			out += " (" + s.Reason + ")"
		}
	}
	if s.CatalogMismatch {
		out += ": its numbers are under release " + s.Release + ", not the loaded catalog's"
	}
	return out + "."
}

// statusText is a fit's status in words, for an answer of kind about
// subject.
func statusText(kind string, fit contract.Fit, req contract.FitRequest, subject string) string {
	catalog := "The catalog"
	if fit.Catalog != nil {
		catalog = fit.Catalog.Name
	}
	switch fit.Status {
	case contract.FitNoCatalog:
		return noCatalogText
	case contract.FitUnknown:
		if kind == fitVehicles {
			return catalog + " has no fitment and no retired number " + req.From.Fitment.Type + " " + req.From.Fitment.Number + "."
		}
		if req.Part != nil {
			return "Nothing loaded here knows the vehicle or the part type asked for: a VIN that decodes to no vPIC make or model, or an entry or spine entry its vocabulary or spine lacks."
		}
		return "Nothing loaded here knows the vehicle asked for: a VIN that decodes to no vPIC make or model, or an entry or spine entry its vocabulary or spine lacks."
	case contract.FitNoVehicle:
		return subject + " reaches none of " + catalog + "'s vehicles."
	case contract.FitNoFitment:
		switch kind {
		case fitPartTypes:
			return catalog + " has this vehicle, but no fitment of any part type fits it" + sideText(req.Side) + "."
		case fitCandidates:
			return catalog + " has this vehicle, but no fitment of " + partText(req.Part) + " fits it" + sideText(req.Side) + "."
		case fitRelated:
			return "No other vehicle shares a fitment with this one" + sideText(req.Side) + "."
		}
		if req.ModelYear.Valid {
			return "None of the fitment's applications holds for model year " + strconv.Itoa(int(req.ModelYear.V)) + "."
		}
		return "None of the fitment's applications is left."
	case contract.FitFound:
		switch kind {
		case fitPartTypes:
			return countText(fit.Total, "part type has", "part types have") + " fitments for this vehicle" + sideText(req.Side) + "."
		case fitCandidates:
			return countText(fit.Total, "fitment", "fitments") + " may be the part" + sideText(req.Side) + "."
		case fitRelated:
			return countText(fit.Total, "vehicle shares", "vehicles share") + " fitments with this one" + sideText(req.Side) + "."
		}
		return "The fitment fits " + countText(fit.Total, "application", "applications") + "."
	}
	return "The fit's status is " + string(fit.Status) + "."
}

// sideText narrows a sentence to the side asked for.
func sideText(side string) string {
	switch side {
	case "":
		return ""
	case contract.SideBoth:
		return ", for both sides"
	}
	return ", on the " + side + " side"
}
