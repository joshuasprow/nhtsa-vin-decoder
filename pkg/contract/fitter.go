package contract

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/fitment"
	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Fitter answers fit requests (ADR 0018) from a loaded catalog, read
// through fitment.Reader (ADR 0017), beside a Translator: the Translator
// reaches the catalog's vehicles and part types from what a request
// names, and decodes its VIN. It also answers list, and pages a catalog's
// fitments (List). It is safe for concurrent use when the Translator's
// Decode is.
type Fitter struct {
	Translator
	// Catalog is the loaded catalog; nil answers every fit FitNoCatalog.
	Catalog fitment.Reader
	// Stock are the loaded stock sources (ADR 0019), in load order: a
	// fit's stock lists hold the rows of every one of their kind.
	Stock []stock.Source
}

// fitKind is the answer a fit's inputs ask for.
type fitKind string

const (
	fitPartTypes  fitKind = "part-types"
	fitCandidates fitKind = "candidates"
	fitVehicles   fitKind = "vehicles"
	fitRelated    fitKind = "related"
)

func (r FitRequest) kind() fitKind {
	switch {
	case r.From.Fitment != nil:
		return fitVehicles
	case r.Related:
		return fitRelated
	case r.Part != nil:
		return fitCandidates
	}
	return fitPartTypes
}

// Fit answers one fit request (ADR 0018): from a vehicle, its part types
// with counts, with Part its candidates, with Related the vehicles that
// share its fitments; from a fitment key, its vehicles. A VIN is decoded
// (Decode), and its decode checks qualifiers and gives the year. When
// req.Now is zero it reads the wall clock once. Nothing to fit is never
// an error, a missing catalog included: it is a status. The error is a
// *Problem: an InvalidRequest for a request Validate refuses, or that
// names a vocabulary or spine the loaded files lack or of the wrong kind
// (a part vocabulary or spine off the catalog's spine, a to that is no
// vehicle vocabulary), entry fields other than the vocabulary's, or an
// after it never gave; Decode's error; or an Internal for a fault in
// nitrovin. ctx bounds its stock lookups: a source that has not answered
// when ctx ends is unavailable, with reason StockCancelled, and the rest
// of the answer stands.
// A panic in a stock lookup, which runs on a goroutine of its own, panics
// Fit's caller as a *LookupPanic. One in a live lookup Fit stopped waiting
// for, its ctx having ended, is dropped without a trace: nothing logs it,
// and the answer already says the source is unavailable.
func (f Fitter) Fit(ctx context.Context, req FitRequest) (Fit, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	if err := req.Validate(); err != nil {
		return Fit{}, err
	}
	kind := req.kind()
	out := Fit{Now: req.Now.UTC().Truncate(time.Second), DataMonth: f.DataMonth, DecoderVersion: f.DecoderVersion}
	switch kind {
	case fitPartTypes:
		out.Models, out.PartTypes = []FitModel{}, []FitPartType{}
	case fitCandidates:
		out.Models, out.Candidates = []FitModel{}, []FitCandidate{}
	case fitRelated:
		out.Models, out.Related = []FitModel{}, []RelatedVehicle{}
	case fitVehicles:
		out.Vehicles = []FitVehicle{}
	}
	if f.Catalog == nil {
		out.Status = FitNoCatalog
		return out, nil
	}
	after, p := decodeFitCursor(req.After, kind)
	if p != nil {
		return Fit{}, p
	}
	if p := f.checkTo(req.To); p != nil {
		return Fit{}, p
	}
	info := f.Catalog.Info()
	out.Catalog = &FitCatalog{Name: info.Name, Version: info.Version, Release: info.Release, Spine: info.Spine, Vehicles: info.Vehicles}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	pg := pager{after: after, limit: limit, kind: kind}
	if kind == fitVehicles {
		if err := f.vehicles(ctx, req, info, &out, pg); err != nil {
			return Fit{}, err
		}
		return out, nil
	}

	// A vehicle: the catalog's own vehicles it reaches, and the part types
	// asked for, before any status, so a malformed part is refused
	// whatever the vehicle.
	v, err := f.reach(req, info)
	if err != nil {
		return Fit{}, err
	}
	out.DataMonth, out.DecoderVersion, out.Vehicle = v.dataMonth, v.decoderVersion, v.decode
	out.Models = v.models
	var types []partType
	partKnown := true
	if req.Part != nil {
		if types, partKnown, err = f.partTypes(req, info, v); err != nil {
			return Fit{}, err
		}
	}
	switch {
	case !v.known || !partKnown:
		out.Status = FitUnknown
		return out, nil
	case len(v.vehicles) == 0:
		out.Status = FitNoVehicle
		return out, nil
	}
	if req.Part == nil {
		types = f.spineTypes(info)
	}
	side := sideOf(req.Side)
	if kind != fitCandidates {
		f.readEvery(v, kind == fitRelated)
	}
	switch kind {
	case fitPartTypes:
		var all []FitPartType
		for _, t := range types {
			cs, reached := f.candidates(t, v, side)
			if len(cs) > 0 {
				all = append(all, FitPartType{Type: t.id, Name: t.name, Count: len(cs), Reached: reached})
			}
		}
		out.Total, out.Next, out.PartTypes = page(all, pg, out.PartTypes)
	case fitCandidates:
		var all []FitCandidate
		for _, t := range types {
			cs, _ := f.candidates(t, v, side)
			all = append(all, cs...)
		}
		all = mergeCandidates(all)
		out.Total, out.Next, out.Candidates = page(all, pg, out.Candidates)
		concurrently(
			func() { f.candidateStock(ctx, req, out.Candidates) },
			func() {
				if req.wants(StockShelf) {
					out.ShelfByVehicle = f.shelfByVehicle(ctx, types, v, req.Location, pg)
				}
			})
		// shelf_by_vehicle pages with the candidates: next stays while
		// either has more.
		pageStock(&out, pg, out.ShelfByVehicle)
	case fitRelated:
		all := f.related(types, v, side)
		var pageOf []relatedGroup
		out.Total, out.Next, pageOf = page(all, pg, []relatedGroup{})
		for _, g := range pageOf {
			r := RelatedVehicle{Vehicle: catalogVehicle(g.vehicle), First: year(g.first), Last: year(g.last), Shared: g.shared, PartTypes: g.partTypes}
			if r.FitNames, err = f.names(info, g.vehicle, g.first, g.last, nil, req.To); err != nil {
				return Fit{}, err
			}
			out.Related = append(out.Related, r)
		}
		if req.wants(StockYard) {
			apps := make([][]*vocab.Application, len(pageOf))
			for i, g := range pageOf {
				apps[i] = []*vocab.Application{{Vehicle: g.vehicle, First: g.first, Last: g.last}}
			}
			for i, l := range f.yardLists(ctx, apps, 0, 0, req.Location) {
				out.Related[i].Yard = l
			}
		}
	}
	out.Status = FitFound
	if out.Total == 0 {
		out.Status = FitNoFitment
	}
	return out, nil
}

// FitBatch fits requests in order, reading the wall clock once for every
// request whose Now is zero, as the contract fixes for one HTTP request.
// It stops at the first error, a *Problem whose pointer names the request
// ("/3/part"). ctx bounds every fit's stock lookups, as Fit's.
func (f Fitter) FitBatch(ctx context.Context, reqs []FitRequest) ([]Fit, error) {
	now := time.Now()
	out := make([]Fit, len(reqs))
	for i, req := range reqs {
		if req.Now.IsZero() {
			req.Now = now
		}
		fit, err := f.Fit(ctx, req)
		if err != nil {
			p := *AsProblem(err)
			p.Pointer = fmt.Sprintf("/%d%s", i, p.Pointer)
			return nil, &p
		}
		out[i] = fit
	}
	return out, nil
}

// checkTo refuses a to name that is no loaded vehicle vocabulary.
func (f Fitter) checkTo(to []string) *Problem {
	for i, name := range to {
		ptr := "/to/" + strconv.Itoa(i)
		if p := f.vocabularyOf(name, ptr); p != nil {
			return p
		}
		if g := f.Set.File(name); g.Subject != vocab.SubjectVehicle {
			return NewProblem(InvalidRequest, ptr, fmt.Sprintf("%q is a part vocabulary: to names vehicle vocabularies", name))
		}
	}
	return nil
}

// vocabularyOf refuses, at ptr, a name that is no loaded vocabulary.
func (f Fitter) vocabularyOf(name, ptr string) *Problem {
	g := f.Set.File(name)
	switch {
	case name == vocab.Reserved:
		return NewProblem(InvalidRequest, ptr, fmt.Sprintf("%q is the vehicle spine, not a vocabulary", name))
	case g == nil:
		return NewProblem(InvalidRequest, ptr, fmt.Sprintf("no vocabulary %q is loaded", name))
	case g.Kind != vocab.KindVocabulary:
		return NewProblem(InvalidRequest, ptr, fmt.Sprintf("%q is a %s, not a vocabulary", name, g.Kind))
	}
	return nil
}

// reached is a vehicle a fit starts from, and what it reached.
type reached struct {
	// known is false when from names nothing the loaded files have.
	known bool
	// vehicles are the catalog's own (fitment.Vehicles), and models the
	// same, one for one, as the answer has them.
	vehicles []vocab.Vehicle
	models   []FitModel
	// year is the decoded year, else the request's; decoded is the VIN's.
	year                      *int
	decoded                   *vocab.Decoded
	decode                    *Vehicle
	dataMonth, decoderVersion string
	// modelIdx remembers modelOf's answers.
	modelIdx map[*vocab.Vehicle]int
	// byType and redirects are, when every is set, the vehicles'
	// candidates and redirects of every part type, by part type, read once
	// for a request about every part type: a compiled catalog answers that
	// from one read of each vehicle's block (ADR 0023), and one lookup per
	// part type would cost reads for each.
	every     bool
	byType    map[string][]vocab.Candidate
	redirects map[string][]*vocab.Redirect
}

// readEvery reads the vehicles' candidates and redirects of every part
// type, once, for byVehicle and redirect to answer from: their fitments
// whole for related vehicles, which need every application, and only
// their matching applications otherwise.
func (f Fitter) readEvery(v *reached, whole bool) {
	v.every, v.byType, v.redirects = true, map[string][]vocab.Candidate{}, map[string][]*vocab.Redirect{}
	read := f.Catalog.Matching
	if whole {
		read = f.Catalog.ByVehicle
	}
	for _, c := range read("", v.vehicles, v.year, nil) {
		t := c.Fitment.Key.Type
		v.byType[t] = append(v.byType[t], c)
	}
	for _, rd := range f.Catalog.Redirect("", v.vehicles, v.year) {
		v.redirects[rd.From] = append(v.redirects[rd.From], rd)
	}
}

// byVehicle is the catalog's candidates of part type t for the vehicle,
// or, given one (by, of the vehicle's), for that one alone: from what
// readEvery read, else their matching applications (Matching).
func (f Fitter) byVehicle(t string, v *reached, by *vocab.Vehicle) []vocab.Candidate {
	if !v.every {
		vehicles := v.vehicles
		if by != nil {
			vehicles = []vocab.Vehicle{*by}
		}
		return f.Catalog.Matching(t, vehicles, v.year, nil)
	}
	if by == nil {
		return v.byType[t]
	}
	want := catalogVehicle(by)
	var out []vocab.Candidate
	for _, c := range v.byType[t] {
		var apps []*vocab.Application
		for _, a := range c.Applications {
			if sameVehicle(catalogVehicle(a.Vehicle), want) {
				apps = append(apps, a)
			}
		}
		if len(apps) > 0 {
			out = append(out, vocab.Candidate{Fitment: c.Fitment, Applications: apps})
		}
	}
	return out
}

// redirect is the catalog's redirects from part type t for the vehicle.
func (f Fitter) redirect(t string, v *reached) []*vocab.Redirect {
	if v.every {
		return v.redirects[t]
	}
	return f.Catalog.Redirect(t, v.vehicles, v.year)
}

// reach translates a fit's vehicle to the catalog's vehicles, decoding its
// VIN: to the catalog's vehicle vocabulary, or the spine when the
// catalog's vehicles are vpic.
func (f Fitter) reach(req FitRequest, info vocab.CatalogInfo) (*reached, error) {
	from := req.From
	if from.Vocabulary != "" {
		if p := f.vocabularyOf(from.Vocabulary, "/from/vocabulary"); p != nil {
			return nil, p
		}
		if g := f.Set.File(from.Vocabulary); g.Subject != vocab.SubjectVehicle {
			return nil, NewProblem(InvalidRequest, "/from/vocabulary", fmt.Sprintf("%q is a part vocabulary: from names a vehicle, and a part is given as part", from.Vocabulary))
		}
	}
	q := vocab.Query{From: vocab.From{VIN: from.VIN != "", Vocabulary: from.Vocabulary, Entry: from.Entry, Ref: vocabRef(from.SpineRef)}}
	if info.Vehicles != vocab.Reserved {
		q.To = []string{info.Vehicles}
	}
	v := &reached{dataMonth: f.DataMonth, decoderVersion: f.DecoderVersion}
	if req.ModelYear.Valid {
		q.ModelYear = new(int(req.ModelYear.V))
	}
	if from.VIN != "" {
		d, err := f.Decode(from.VIN, req.ModelYear, req.Now.UTC().Truncate(time.Second))
		if err != nil {
			return nil, AsProblem(err)
		}
		v.dataMonth, v.decoderVersion = d.DataMonth, d.DecoderVersion
		q.Decoded = vocab.NewDecoded(f.Set.Month(), vocabItems(d.Items))
		v.decoded, v.decode = q.Decoded, newVehicle(d.VIN, q.Decoded)
	}
	v.year = q.ModelYear
	if v.decoded != nil && v.decoded.ModelYear != nil {
		v.year = v.decoded.ModelYear
	}
	tr, err := f.Set.Translate(q)
	if err != nil {
		return nil, vocabProblem(err)
	}
	v.known = tr.Status != vocab.StatusUnknown
	v.vehicles = fitment.Vehicles(info, tr)
	v.models = models(info, tr)
	if len(v.models) != len(v.vehicles) { // fitment.Vehicles walks the translation as models does
		panic(fmt.Sprintf("fit: %d models for %d vehicles", len(v.models), len(v.vehicles)))
	}
	return v, nil
}

// models are the catalog's vehicles a translation reached, as an answer
// has them, in fitment.Vehicles's order.
func models(info vocab.CatalogInfo, tr *vocab.Translation) []FitModel {
	out := []FitModel{}
	if info.Vehicles == vocab.Reserved {
		for _, r := range tr.Spine {
			if r.Ref.ID == "" {
				out = append(out, FitModel{
					Vehicle: CatalogVehicle{Make: r.Ref.Make, Model: r.Ref.Model, Ext: r.Ref.Ext}, Name: r.Name,
					Via: vias(r.Via), Unconfirmed: unconfirmed(r.Unconfirmed),
				})
			}
		}
		return out
	}
	if tr.Vocabulary != nil && tr.Vocabulary.Name == info.Vehicles && tr.Entry != nil {
		return append(out, FitModel{Vehicle: CatalogVehicle{Entry: tr.Vocabulary.FieldsOf(tr.Entry)}, Note: tr.Entry.Note})
	}
	for _, to := range tr.To {
		if to.Vocabulary.Name != info.Vehicles {
			continue
		}
		for _, m := range to.Matches {
			out = append(out, FitModel{
				Vehicle: CatalogVehicle{Entry: to.Vocabulary.FieldsOf(m.Entry)}, Note: m.Entry.Note,
				Via: vias(m.Via), Unconfirmed: unconfirmed(m.Unconfirmed),
			})
		}
	}
	return out
}

// partType is a part type of the catalog's spine a fit asks about, and
// what its route left unchecked.
type partType struct {
	id, name    string
	unconfirmed vocab.Unconfirmed
}

// partTypes are the part types req.Part reaches, in id order, and whether
// it names anything the loaded files have.
func (f Fitter) partTypes(req FitRequest, info vocab.CatalogInfo, v *reached) ([]partType, bool, error) {
	part := req.Part
	q := vocab.Query{ModelYear: v.year, Decoded: v.decoded}
	if part.Spine != "" {
		g := f.Set.File(part.Spine)
		switch {
		case g == nil:
			return nil, false, NewProblem(InvalidRequest, "/part/spine", fmt.Sprintf("no spine %q is loaded", part.Spine))
		case g.Kind != vocab.KindSpine:
			return nil, false, NewProblem(InvalidRequest, "/part/spine", fmt.Sprintf("%q is a %s, not a spine", part.Spine, g.Kind))
		case part.Spine != info.Spine:
			return nil, false, NewProblem(InvalidRequest, "/part/spine", fmt.Sprintf("spine %q is not the catalog's: its part types are spine %q's", part.Spine, info.Spine))
		}
		q.From.Ref = vocab.Ref{Spine: part.Spine, Target: vocab.Target{ID: part.ID}}
	} else {
		if p := f.vocabularyOf(part.Vocabulary, "/part/vocabulary"); p != nil {
			return nil, false, p
		}
		if g := f.Set.File(part.Vocabulary); g.Subject != vocab.SubjectPart || g.Spine != info.Spine {
			return nil, false, NewProblem(InvalidRequest, "/part/vocabulary", fmt.Sprintf("vocabulary %q is not a part vocabulary on the catalog's spine %q", part.Vocabulary, info.Spine))
		}
		q.From.Vocabulary, q.From.Entry = part.Vocabulary, part.Entry
	}
	tr, err := f.Set.Translate(q)
	if err != nil {
		p := *vocabProblem(err)
		if rest, ok := strings.CutPrefix(p.Pointer, "/from"); ok {
			p.Pointer = "/part" + rest
		}
		return nil, false, &p
	}
	var out []partType
	for _, r := range tr.Spine {
		if r.Ref.Spine == info.Spine && r.Ref.ID != "" {
			out = append(out, partType{id: r.Ref.ID, name: r.Name, unconfirmed: r.Unconfirmed})
		}
	}
	slices.SortFunc(out, func(a, b partType) int { return strings.Compare(a.id, b.id) })
	return out, tr.Status != vocab.StatusUnknown, nil
}

// spineTypes are every part type of the catalog's spine, in id order.
func (f Fitter) spineTypes(info vocab.CatalogInfo) []partType {
	g := f.Set.File(info.Spine)
	if g == nil {
		return nil
	}
	out := make([]partType, len(g.SpineEntries))
	for i, e := range g.SpineEntries {
		out[i] = partType{id: e.ID, name: e.Name}
	}
	slices.SortFunc(out, func(a, b partType) int { return strings.Compare(a.id, b.id) })
	return out
}

// sideOf is a request's side as a fitment states it, SideNone for any.
func sideOf(s string) vocab.Side {
	side, _ := vocab.ParseSide(s)
	return side
}

// keeps reports whether a side filter keeps a fitment of side s. A
// fitment drops out only when its side excludes the one asked for: a part
// for both sides goes on the left, and a fitment that states no side is
// never ruled out by one, as an unknown value keeps an application
// (ADR 0009). Asked for both, only a part for both sides, or one with no
// side, is kept.
func keeps(want, s vocab.Side) bool {
	switch want {
	case vocab.SideNone:
		return true
	case vocab.SideBoth:
		return s == vocab.SideBoth || s == vocab.SideNone
	}
	return s == want || s == vocab.SideBoth || s == vocab.SideNone
}

// candidates are the candidates of part type t for the vehicle, side kept,
// each with the applications that hold: directly, and through each
// redirect listed for one of its vehicles and years that no direct
// candidate covers. Reached is direct when any candidate is.
func (f Fitter) candidates(t partType, v *reached, side vocab.Side) ([]FitCandidate, FitReach) {
	direct := f.matching(f.byVehicle(t.id, v, nil), t, v, side, nil)
	out := direct
	for _, rd := range f.redirect(t.id, v) {
		if covers(direct, rd) {
			continue
		}
		if len(out) == len(direct) {
			out = slices.Clone(direct) // what follows never writes into direct
		}
		for _, c := range f.matching(f.byVehicle(rd.To, v, rd.Vehicle), t, v, side, rd) {
			c.Reached = ReachedRedirect
			c.Redirects = []FitRedirect{fitRedirect(rd)}
			out = append(out, c)
		}
	}
	if len(out) == len(direct) {
		return direct, ReachedDirect
	}
	out = mergeCandidates(out)
	if len(direct) > 0 {
		return out, ReachedDirect
	}
	return out, ReachedRedirect
}

// covers reports whether a direct candidate has an application to rd's
// vehicle in years that overlap rd's: a redirect is for a vehicle and
// years (ADR 0016), and is followed wherever the part type asked for has
// no fitment of its own.
func covers(direct []FitCandidate, rd *vocab.Redirect) bool {
	rv := catalogVehicle(rd.Vehicle)
	for _, c := range direct {
		for _, a := range c.Applications {
			if sameVehicle(a.Vehicle, rv) && overlaps(intOf(a.First), intOf(a.Last), rd.First, rd.Last) {
				return true
			}
		}
	}
	return false
}

// intOf is a year as the catalog has it, nil when absent.
func intOf(y Null[int32]) *int {
	if !y.Valid {
		return nil
	}
	return new(int(y.V))
}

// matching is cs, side kept, as an answer has them: each with the
// applications that hold against the vehicle's year and decode (and the
// redirect's years, through rd), the unchecked of the route to each.
func (f Fitter) matching(cs []vocab.Candidate, t partType, v *reached, side vocab.Side, rd *vocab.Redirect) []FitCandidate {
	out := make([]FitCandidate, 0, len(cs))
	for _, c := range cs {
		if !keeps(side, c.Fitment.Key.Side) {
			continue
		}
		fc := FitCandidate{Fitment: fitmentOf(c.Fitment), Reached: ReachedDirect, Applications: make([]FitApplication, 0, len(c.Applications))}
		for _, a := range c.Applications {
			if rd != nil && v.year == nil && !overlaps(a.First, a.Last, rd.First, rd.Last) {
				continue
			}
			u, ok := a.Check(v.year, v.decoded)
			if !ok {
				continue
			}
			fa := FitApplication{Vehicle: catalogVehicle(a.Vehicle), First: year(a.First), Last: year(a.Last), Options: a.Options}
			uc := Unconfirmed{Elements: u.Elements, ModelYear: u.ModelYear}
			if i := v.modelOf(a.Vehicle); i >= 0 {
				fa.Via = v.models[i].Via
				uc = uc.union(v.models[i].Unconfirmed)
			}
			fa.Unconfirmed = uc.union(unconfirmed(t.unconfirmed))
			fc.Applications = append(fc.Applications, fa)
		}
		if len(fc.Applications) > 0 {
			out = append(out, fc)
		}
	}
	return out
}

// overlaps reports whether years a1 to a2 and b1 to b2 share one, nil
// being unbounded.
func overlaps(a1, a2, b1, b2 *int) bool {
	return (a1 == nil || b2 == nil || *a1 <= *b2) && (b1 == nil || a2 == nil || *b1 <= *a2)
}

// union is what is unchecked in u or w.
func (u Unconfirmed) union(w Unconfirmed) Unconfirmed {
	switch {
	case w.IsZero():
		return u
	case u.IsZero():
		return w
	}
	out := Unconfirmed{ModelYear: u.ModelYear || w.ModelYear}
	if len(u.Elements)+len(w.Elements) > 0 {
		out.Elements = append(slices.Clone(u.Elements), w.Elements...)
		slices.Sort(out.Elements)
		out.Elements = slices.Compact(out.Elements)
	}
	return out
}

// mergeCandidates orders candidates by part type, side and number, and
// makes one of each fitment reached more than once: its applications
// once each, direct if any way was, and every redirect that reached it.
func mergeCandidates(cs []FitCandidate) []FitCandidate {
	slices.SortStableFunc(cs, func(a, b FitCandidate) int { return compareFitments(a.Fitment, b.Fitment) })
	out := cs[:0]
	for _, c := range cs {
		n := len(out)
		if n == 0 || compareFitments(out[n-1].Fitment, c.Fitment) != 0 {
			out = append(out, c)
			continue
		}
		last := &out[n-1]
		for _, a := range c.Applications {
			if !slices.ContainsFunc(last.Applications, func(b FitApplication) bool { return sameApplication(a, b) }) {
				last.Applications = append(last.Applications, a)
			}
		}
		switch {
		case last.Reached == ReachedDirect:
		case c.Reached == ReachedDirect:
			last.Reached, last.Redirects = ReachedDirect, nil
		default:
			last.Redirects = append(last.Redirects, c.Redirects...)
		}
	}
	return out
}

func sameApplication(a, b FitApplication) bool {
	return a.Options == b.Options && a.First == b.First && a.Last == b.Last && sameVehicle(a.Vehicle, b.Vehicle)
}

func sameVehicle(a, b CatalogVehicle) bool {
	if a.Entry == nil || b.Entry == nil {
		return a.Entry == nil && b.Entry == nil && a.Make == b.Make && a.Model == b.Model && a.Ext == b.Ext
	}
	if len(a.Entry) != len(b.Entry) {
		return false
	}
	for k, v := range a.Entry {
		w, ok := b.Entry[k]
		if !ok || vocab.Normalize(v) != vocab.Normalize(w) {
			return false
		}
	}
	return true
}

// compareFitments orders fitments by part type, side and number: the order
// candidates come in (ADR 0018).
func compareFitments(a, b Fitment) int {
	sa, _ := vocab.ParseSide(a.Side)
	sb, _ := vocab.ParseSide(b.Side)
	return cmp.Or(strings.Compare(a.Type, b.Type), cmp.Compare(sa, sb), strings.Compare(a.Number, b.Number))
}

// modelOf is the index in r.vehicles of the catalog's vehicle v, -1 when
// it is none of them, remembered for v: a catalog names each vehicle on
// many lines.
func (r *reached) modelOf(v *vocab.Vehicle) int {
	if i, ok := r.modelIdx[v]; ok {
		return i
	}
	i, cv := -1, catalogVehicle(v)
	for j := range r.vehicles {
		if sameVehicle(catalogVehicle(&r.vehicles[j]), cv) {
			i = j
			break
		}
	}
	if r.modelIdx == nil {
		r.modelIdx = map[*vocab.Vehicle]int{}
	}
	r.modelIdx[v] = i
	return i
}

func catalogVehicle(v *vocab.Vehicle) CatalogVehicle {
	return CatalogVehicle{Entry: v.Entry, Make: v.Make, Model: v.Model, Ext: v.Ext}
}

func fitmentOf(fit *vocab.Fitment) Fitment {
	return Fitment{Type: fit.Key.Type, Number: fit.Key.Number, Side: fit.Key.Side.String(), Note: fit.Note}
}

func fitmentKey(k vocab.FitmentKey) FitmentKey {
	return FitmentKey{Type: k.Type, Number: k.Number, Side: k.Side.String()}
}

func fitRedirect(rd *vocab.Redirect) FitRedirect {
	return FitRedirect{Vehicle: catalogVehicle(rd.Vehicle), First: year(rd.First), Last: year(rd.Last), From: rd.From, To: rd.To, Options: rd.Options}
}

// relatedGroup is a related vehicle: a catalog vehicle and a year range,
// and the fitments it shares in every year of it.
type relatedGroup struct {
	vehicle     *vocab.Vehicle
	first, last *int
	name        []string
	shared      int
	partTypes   []FitPartType
}

// sharedSpan is a shared fitment's application to a related vehicle: the
// fitment's index among those shared, and its years, math.MinInt and
// math.MaxInt for unbounded.
type sharedSpan struct{ fit, lo, hi int }

// related are the vehicles that share a fitment with v (of types, side
// kept, an application of v's holding), other than v's own applications.
// Each catalog vehicle's years are cut where the set of fitments it
// shares changes: a row is a vehicle and the longest year range over
// which it shares the same fitments, so shared counts every fitment
// shared in each year of it. Rows are ordered by shared count, most
// first, then name and years.
func (f Fitter) related(types []partType, v *reached, side vocab.Side) []relatedGroup {
	type spans struct {
		vehicle *vocab.Vehicle
		spans   []sharedSpan
	}
	// Vehicles group by their key, not by pointer: a reader need not
	// share one Vehicle among the lines naming it. keys remembers each
	// Vehicle's.
	byKey := map[string]*spans{}
	keys := map[*vocab.Vehicle]string{}
	var order []*spans
	var fits []int32 // each shared fitment's part type, an index in types
	for ti, t := range types {
		for _, c := range f.byVehicle(t.id, v, nil) {
			if !keeps(side, c.Fitment.Key.Side) || !slices.ContainsFunc(c.Applications, func(a *vocab.Application) bool {
				_, ok := a.Check(v.year, v.decoded)
				return ok
			}) {
				continue
			}
			fi := len(fits)
			fits = append(fits, int32(ti))
			for i := range c.Fitment.Applications {
				a := &c.Fitment.Applications[i]
				if v.modelOf(a.Vehicle) >= 0 && (v.year == nil || holds(a.First, a.Last, *v.year)) {
					continue // the vehicle itself
				}
				k, ok := keys[a.Vehicle]
				if !ok {
					k = vehicleKey(catalogVehicle(a.Vehicle))
					keys[a.Vehicle] = k
				}
				g := byKey[k]
				if g == nil {
					g = &spans{vehicle: a.Vehicle}
					byKey[k] = g
					order = append(order, g)
				}
				sp := sharedSpan{fit: fi, lo: math.MinInt, hi: math.MaxInt}
				if a.First != nil {
					sp.lo = *a.First
				}
				if a.Last != nil {
					sp.hi = *a.Last
				}
				g.spans = append(g.spans, sp)
			}
		}
	}
	var out []relatedGroup
	for _, g := range order {
		name := f.vehicleName(g.vehicle)
		for _, r := range splitShared(g.spans) {
			rg := relatedGroup{vehicle: g.vehicle, name: name, shared: len(r.fits)}
			if r.lo != math.MinInt {
				rg.first = new(r.lo)
			}
			if r.hi != math.MaxInt {
				rg.last = new(r.hi)
			}
			for _, fi := range r.fits { // ascending, so by part type
				t := &types[fits[fi]]
				if n := len(rg.partTypes); n > 0 && rg.partTypes[n-1].Type == t.id {
					rg.partTypes[n-1].Count++
				} else {
					rg.partTypes = append(rg.partTypes, FitPartType{Type: t.id, Name: t.name, Count: 1})
				}
			}
			out = append(out, rg)
		}
	}
	slices.SortFunc(out, func(a, b relatedGroup) int {
		return cmp.Or(cmp.Compare(b.shared, a.shared), slices.Compare(a.name, b.name), compareYear(a.first, b.first), compareYear(a.last, b.last))
	})
	return out
}

// sharedRange is a year range, lo to hi (math.MinInt and math.MaxInt for
// unbounded), and the fitments shared in every year of it, ascending.
type sharedRange struct {
	lo, hi int
	fits   []int
}

// splitShared cuts one vehicle's shared spans into the longest year
// ranges over which the set of fitments shared stays the same, in year
// order, leaving out years that share none.
func splitShared(spans []sharedSpan) []sharedRange {
	// Every range starts at a span's first year or the year after one's
	// last: within one, each span holds every year or none.
	var cuts []int
	for _, sp := range spans {
		cuts = append(cuts, sp.lo)
		if sp.hi != math.MaxInt {
			cuts = append(cuts, sp.hi+1)
		}
	}
	slices.Sort(cuts)
	cuts = slices.Compact(cuts)
	var out []sharedRange
	for i, lo := range cuts {
		hi := math.MaxInt
		if i+1 < len(cuts) {
			hi = cuts[i+1] - 1
		}
		var set []int
		for _, sp := range spans {
			if sp.lo <= lo && hi <= sp.hi {
				set = append(set, sp.fit)
			}
		}
		slices.Sort(set)
		set = slices.Compact(set)
		switch n := len(out); {
		case len(set) == 0:
		case n > 0 && out[n-1].hi == lo-1 && slices.Equal(out[n-1].fits, set):
			out[n-1].hi = hi
		default:
			out = append(out, sharedRange{lo: lo, hi: hi, fits: set})
		}
	}
	return out
}

func holds(first, last *int, y int) bool {
	return (first == nil || y >= *first) && (last == nil || y <= *last)
}

// compareYear orders years, unbounded (nil) first.
func compareYear(a, b *int) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	return cmp.Compare(*a, *b)
}

// vehicleKey identifies a catalog vehicle: its fields in name order, each
// normalized, or its target.
func vehicleKey(v CatalogVehicle) string {
	if v.Entry == nil {
		return fmt.Sprintf("\x00%d\x00%d\x00%s", v.Make, v.Model, v.Ext)
	}
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(v.Entry)) {
		val := vocab.Normalize(v.Entry[k])
		fmt.Fprintf(&b, "%d:%s%d:%s", len(k), k, len(val), val)
	}
	return b.String()
}

// vehicleName is what related vehicles are ordered by after their count:
// an entry's fields in its vocabulary's order, normalized, or a vPIC
// make's, model's or extension's name.
func (f Fitter) vehicleName(v *vocab.Vehicle) []string {
	if v.Entry != nil {
		var fields []string
		if g := f.Set.File(f.Catalog.Info().Vehicles); g != nil {
			fields = g.Fields
		}
		out := make([]string, len(fields))
		for i, k := range fields {
			out[i] = vocab.Normalize(v.Entry[k])
		}
		return out
	}
	m := f.Set.Month()
	switch {
	case v.Make != 0:
		name, _ := m.Make(v.Make)
		return []string{vocab.Normalize(name)}
	case v.Model != 0:
		_, name, _ := m.Model(v.Model)
		return []string{vocab.Normalize(name)}
	}
	return []string{v.Ext}
}

// names names catalog vehicle v as a translation from it does, in to's
// vocabularies or, without to, as spine entries: at year, else in the
// years first to last.
func (f Fitter) names(info vocab.CatalogInfo, v *vocab.Vehicle, first, last, at *int, to []string) (FitNames, error) {
	q := vocab.Query{To: to, ModelYear: at, First: first, Last: last}
	if info.Vehicles == vocab.Reserved {
		q.From.Ref = vocab.Ref{Target: v.Target}
	} else {
		q.From.Vocabulary, q.From.Entry = info.Vehicles, v.Entry
	}
	tr, err := f.Set.Translate(q)
	if err != nil {
		return FitNames{}, vocabProblem(err)
	}
	var out FitNames
	if len(to) == 0 {
		out.Spine = make([]SpineEntry, len(tr.Spine))
		for i, it := range tr.Spine {
			out.Spine[i] = spineEntry(it)
		}
		return out, nil
	}
	for _, t := range tr.To {
		c := Translated{Vocabulary: t.Vocabulary.Name, Entries: make([]TranslatedEntry, len(t.Matches))}
		for j, m := range t.Matches {
			c.Entries[j] = TranslatedEntry{Entry: t.Vocabulary.FieldsOf(m.Entry), Note: m.Entry.Note, Via: vias(m.Via), Unconfirmed: unconfirmed(m.Unconfirmed)}
		}
		out.To = append(out.To, c)
	}
	return out, nil
}

// vehicles answers a fit from a fitment key: the fitment, directly or
// through the retired number asked for, and a page of its applications'
// vehicles, named.
func (f Fitter) vehicles(ctx context.Context, req FitRequest, info vocab.CatalogInfo, out *Fit, pg pager) error {
	k := req.From.Fitment
	fit, rt := f.Catalog.ByNumber(k.Type, k.Number, sideOf(k.Side))
	if fit == nil {
		out.Status = FitUnknown
		return nil
	}
	fm := fitmentOf(fit)
	out.Fitment, out.Reached = &fm, ReachedDirect
	if rt != nil {
		out.Reached = ReachedRetired
		out.Retired = &FitRetired{Type: rt.Key.Type, Number: rt.Key.Number, Side: rt.Key.Side.String(), Note: rt.Note}
	}
	var at *int
	if req.ModelYear.Valid {
		at = new(int(req.ModelYear.V))
	}
	var apps []*vocab.Application
	if keeps(sideOf(req.Side), fit.Key.Side) {
		for i := range fit.Applications {
			if a := &fit.Applications[i]; at == nil || holds(a.First, a.Last, *at) {
				apps = append(apps, a)
			}
		}
	}
	var err error
	all := apps
	out.Total, out.Next, apps = page(apps, pg, []*vocab.Application{})
	f.fitmentStock(ctx, req, fit, all, out, pg)
	for _, a := range apps {
		fv := FitVehicle{Vehicle: catalogVehicle(a.Vehicle), First: year(a.First), Last: year(a.Last), Options: a.Options}
		if fv.FitNames, err = f.names(info, a.Vehicle, a.First, a.Last, at, req.To); err != nil {
			return err
		}
		out.Vehicles = append(out.Vehicles, fv)
	}
	out.Status = FitFound
	if out.Total == 0 {
		out.Status = FitNoFitment
	}
	return nil
}

// pager is a fit's page: after the first after items, at most limit.
type pager struct {
	after, limit int
	kind         fitKind
}

// page is all's total, the cursor of the page after pg's (or ""), and
// pg's page of all appended to into.
func page[T any](all []T, pg pager, into []T) (int, string, []T) {
	lo := min(pg.after, len(all))
	hi := min(lo+pg.limit, len(all))
	next := ""
	if hi < len(all) {
		next = encodeFitCursor(pg.kind, hi)
	}
	return len(all), next, append(into, all[lo:hi]...)
}

// encodeFitCursor is a fit's cursor: opaque to clients, the JSON of its
// answer's kind and how many items came before the next page, in unpadded
// base64url, as list's cursors are.
func encodeFitCursor(kind fitKind, n int) string {
	b, _ := json.Marshal([]any{string(kind), n})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeFitCursor reads after, refusing one that is not a cursor of the
// kind of answer asked for; "" is 0.
func decodeFitCursor(after string, kind fitKind) (int, *Problem) {
	if after == "" {
		return 0, nil
	}
	refuse := NewProblem(InvalidRequest, "/after", fmt.Sprintf("%q is not a cursor of this fit's %s", after, kind))
	b, err := base64.RawURLEncoding.DecodeString(after)
	if err != nil {
		return 0, refuse
	}
	var c []json.RawMessage
	if json.Unmarshal(b, &c) != nil || len(c) != 2 {
		return 0, refuse
	}
	var k string
	n, ok := jsonInt32(c[1])
	if !jsonString(c[0], &k) || k != string(kind) || !ok || n < 1 {
		return 0, refuse
	}
	return int(n), nil
}

// List answers one list request as the Translator's List does, and, with
// Of naming the loaded catalog, pages through its fitments (ADR 0018), of
// one part type with where.type, each a FitmentLine as the file has it, in
// key order (part type, side, number), through fitment.Reader.Fitments.
func (f Fitter) List(req ListRequest) (List, error) {
	if f.Catalog == nil || req.Of == "" || req.Of != f.Catalog.Info().Name {
		return f.Translator.List(req)
	}
	info := f.Catalog.Info()
	for _, k := range slices.Sorted(maps.Keys(req.Where)) {
		if k != "type" {
			return List{}, NewProblem(InvalidRequest, pointerTo("/where", k), fmt.Sprintf("catalog %q's fitments take where.type, a part type, and no other field", info.Name))
		}
	}
	limit := req.Limit
	switch {
	case limit == 0:
		limit = DefaultListLimit
	case limit < 1 || limit > MaxListLimit:
		return List{}, NewProblem(InvalidRequest, "/limit", fmt.Sprintf("limit must be an integer from 1 to %d", MaxListLimit))
	}
	var after *vocab.FitmentKey
	if req.After != "" {
		k, ok := decodeKeyCursor(req.After)
		if !ok {
			return List{}, NewProblem(InvalidRequest, "/after", fmt.Sprintf("%q is not a cursor of this list", req.After))
		}
		after = &k
	}
	fits := f.Catalog.Fitments(req.Where["type"], after, limit+1)
	out := List{
		Of:             req.Of,
		Entries:        []ListEntry{},
		Vocabularies:   []FileVersion{{Name: info.Name, Version: info.Version}},
		DataMonth:      f.DataMonth,
		DecoderVersion: f.DecoderVersion,
	}
	if len(fits) > limit {
		fits = fits[:limit]
		out.Next = encodeKeyCursor(fits[limit-1].Key)
	}
	for _, fit := range fits {
		out.Entries = append(out.Entries, fitmentLine(fit))
	}
	return out, nil
}

// fitmentLine is a fitment as its file has it.
func fitmentLine(fit *vocab.Fitment) FitmentLine {
	l := FitmentLine{Fitment: fitmentKey(fit.Key), Note: fit.Note, Applications: make([]LineApplication, len(fit.Applications))}
	for i := range fit.Applications {
		a := &fit.Applications[i]
		la := LineApplication{Vehicle: catalogVehicle(a.Vehicle), First: year(a.First), Last: year(a.Last), Options: a.Options}
		if len(a.Qualifiers) > 0 {
			la.Qualifiers = qualifierValues(a.Qualifiers)
		}
		l.Applications[i] = la
	}
	return l
}

// encodeKeyCursor is a catalog list's cursor: its last fitment's key,
// part type, side and number, as list's cursors encode a key.
func encodeKeyCursor(k vocab.FitmentKey) string {
	b, _ := json.Marshal([]string{k.Type, k.Side.String(), k.Number})
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeKeyCursor(cursor string) (vocab.FitmentKey, bool) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return vocab.FitmentKey{}, false
	}
	var key []string
	if json.Unmarshal(b, &key) != nil || len(key) != 3 {
		return vocab.FitmentKey{}, false
	}
	side, ok := vocab.ParseSide(key[1])
	if !ok {
		return vocab.FitmentKey{}, false
	}
	return vocab.FitmentKey{Type: key[0], Side: side, Number: key[2]}, true
}
