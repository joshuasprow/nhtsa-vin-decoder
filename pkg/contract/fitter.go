package contract

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/binary"
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
	if kind == fitPartTypes {
		f.readEvery(v)
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
		var pageOf []relatedGroup
		rel := f.related(types, v, side)
		out.Total, out.Next, pageOf = rel.page(pg)
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

// readEvery reads the vehicles' candidates, their matching applications,
// and redirects of every part type, once, for byVehicle and redirect to
// answer from.
func (f Fitter) readEvery(v *reached) {
	v.every, v.byType, v.redirects = true, map[string][]vocab.Candidate{}, map[string][]*vocab.Redirect{}
	for _, c := range f.Catalog.Matching("", v.vehicles, v.year, nil) {
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
	shared      int
	partTypes   []FitPartType
}

// relatedRow is a row of related vehicles, before its page builds it: a
// vehicle (an index among those found), a year range lo to hi
// (math.MinInt and math.MaxInt for unbounded), and the count of fitments
// shared in every year of it.
type relatedRow struct {
	vehicle, shared int32
	lo, hi          int
}

// relatedRows are the related vehicles of a fit, as each vehicle's
// years, cut where the set of fitments it shares changes, and what
// orders them and builds a page's rows. Rows that tie on count, name and
// years are of different vehicles, and come in the order of the
// vehicles' first spans: by the shared fitments' part types, as types has
// them, then by key, then by application.
//
// A vehicle's years are kept as cells, one a year from its base: 0 where
// the set of fitments it shares stays as it was the year before, else
// twice the change in their count, plus one. Each fitment's applications
// to the vehicle, joined, start or end its share of the set; where one
// ends as another starts, the count stays but the set changes. below is
// the count from the earliest year, of fitments shared without a first
// year. So the rows are the vehicle's cells read in year order, and a
// request holds a few cells a vehicle and the joined spans, a few bytes
// each (sharedLog), for the page's part types: never every span, which
// pass through from Spans.
type relatedRows struct {
	types []partType
	v     *reached
	// vehicles are the catalog's.
	vehicles []*vocab.Vehicle
	// order is, by kept candidate, its fitment's index among those
	// shared, by part type as types has them, then in key order; fits,
	// by that index, its part type, an index in types.
	order, fits []int32
	// local is, by catalog vehicle, its index among the vehicles found
	// plus one, 0 for one not found.
	local []int32
	found []foundVehicle
	// cells is the chunk vehicles' cells are cut from.
	cells []int32
	// lo and hi bound the years cells have held, a vehicle's first
	// cells' guess at its years.
	lo, hi int
	log    sharedLog
	// add's: its count of calls; a fitment's spans, those of vehicles
	// with more than one in it, and those again by vehicle, at each
	// one's slot.
	call              int32
	ys, joins, joined []yearSpan
	at                []int32
	// total is the count of rows, most the most fitments a row shares.
	total int
	most  int32
}

// sharedLog is the shared fitments' spans as related joined them, for a
// page's part types to read again: each fitment's index, then its
// vehicles' spans, each a vehicle found and years, in uvarints, about
// four bytes a span, in chunks of at most logChunk bytes, the first
// smaller, cur the last.
type sharedLog struct {
	chunks [][]byte
	cur    []byte
	// fi is the fitment whose spans come next, its index not yet
	// written, or -1.
	fi int32
	// base is the year years are written from, once based.
	base  int
	based bool
}

const logChunk = 64 << 10

// span writes vehicle l's span lo to hi, of the fitment last begun.
func (g *sharedLog) span(l int32, lo, hi int) {
	b := g.cur
	if cap(b)-len(b) < 4*binary.MaxVarintLen64 {
		if b != nil {
			g.chunks = append(g.chunks, b)
		}
		b = make([]byte, 0, min(max(2*cap(b), logChunk/16), logChunk))
	}
	if g.fi >= 0 {
		b = binary.AppendUvarint(b, uint64(g.fi)<<1)
		g.fi = -1
	}
	if !g.based {
		switch {
		case lo != math.MinInt:
			g.base, g.based = lo, true
		case hi != math.MaxInt:
			g.base, g.based = hi, true
		}
	}
	switch {
	case lo != math.MinInt && hi != math.MaxInt:
		b = binary.AppendUvarint(b, uint64(l)<<3|3<<1|1)
		b = binary.AppendVarint(b, int64(lo-g.base))
		b = binary.AppendUvarint(b, uint64(hi-lo))
	case lo != math.MinInt:
		b = binary.AppendUvarint(b, uint64(l)<<3|1<<1|1)
		b = binary.AppendVarint(b, int64(lo-g.base))
	case hi != math.MaxInt:
		b = binary.AppendUvarint(b, uint64(l)<<3|2<<1|1)
		b = binary.AppendVarint(b, int64(hi-g.base))
	default:
		b = binary.AppendUvarint(b, uint64(l)<<3|1)
	}
	g.cur = b
}

// each reads it back: each fitment's index, then its spans.
func (g *sharedLog) each(fitment func(fi int32), span func(l int32, lo, hi int)) {
	for c := range len(g.chunks) + 1 {
		b := g.cur
		if c < len(g.chunks) {
			b = g.chunks[c]
		}
		for len(b) > 0 {
			v, k := binary.Uvarint(b)
			b = b[k:]
			if v&1 == 0 {
				fitment(int32(v >> 1))
				continue
			}
			l, flags := int32(v>>3), v>>1&3
			lo, hi := math.MinInt, math.MaxInt
			if flags&1 != 0 {
				d, k := binary.Varint(b)
				lo, b = g.base+int(d), b[k:]
			}
			switch {
			case flags&2 == 0:
			case flags&1 != 0:
				d, k := binary.Uvarint(b)
				hi, b = lo+int(d), b[k:]
			default:
				d, k := binary.Varint(b)
				hi, b = g.base+int(d), b[k:]
			}
			span(l, lo, hi)
		}
	}
}

// foundVehicle is a vehicle found among the shared fitments' spans: its
// catalog index; whether it is one of the fit's own; its first span, as
// fitment index << 32 | application index, math.MaxUint64 for none; its
// place in the order of first spans and the rank of its name; its cells,
// from year base, and below; and the last add that saw it, that saw it
// again, and that gave it a slot, and the slot.
type foundVehicle struct {
	id                   int32
	self                 bool
	first                uint64
	pos, rank            int32
	base                 int
	cells                []int32
	below                int32
	seen, again, slotted int32
	slot                 int32
}

// yearSpan is a span's years, lo to hi, of a vehicle found.
type yearSpan struct {
	vehicle int32
	lo, hi  int
}

// spanYears are a span's years, math.MinInt and math.MaxInt for
// unbounded.
func spanYears(sp *vocab.SharedSpan) (int, int) {
	lo, hi := math.MinInt, math.MaxInt
	if sp.Bounds&vocab.SpanFirst != 0 {
		lo = int(sp.First)
	}
	if sp.Bounds&vocab.SpanLast != 0 {
		hi = int(sp.Last)
	}
	return lo, hi
}

// related are the vehicles that share a fitment with v (of types, side
// kept, an application of v's holding), other than v's own applications.
// Each catalog vehicle's years are cut where the set of fitments it
// shares changes: a row is a vehicle and the longest year range over
// which it shares the same fitments, so shared counts every fitment
// shared in each year of it. Rows are ordered by shared count, most
// first, then name and years, and then in the order the vehicles were
// first found (relatedRows).
//
// Only a page's rows are built (page): every row is a vehicle's cells
// until then, so an answer of tens of thousands of rows costs a pass over
// the fitments' applications, a few cells a vehicle and a sort of the
// page's counts, however many applications they share.
func (f Fitter) related(types []partType, v *reached, side vocab.Side) *relatedRows {
	typeAt := make(map[string]int32, len(types))
	for ti := range types {
		typeAt[types[ti].id] = int32(ti)
	}
	// Each kept candidate's part type, in key order.
	var typeOf []int32
	keep := func(c *vocab.Candidate) bool {
		ti, ok := typeAt[c.Fitment.Key.Type]
		if !ok || !keeps(side, c.Fitment.Key.Side) || !slices.ContainsFunc(c.Applications, func(a *vocab.Application) bool {
			_, ok := a.Check(v.year, v.decoded)
			return ok
		}) {
			return false
		}
		typeOf = append(typeOf, ti)
		return true
	}
	rr := &relatedRows{types: types, v: v}
	vehicles, spans := f.shared(v, keep)
	rr.vehicles = vehicles
	// The shared fitments, by part type as types has them, then in key
	// order.
	at := make([]int32, len(types)+1)
	for _, ti := range typeOf {
		at[ti+1]++
	}
	for ti := range types {
		at[ti+1] += at[ti]
	}
	rr.order, rr.fits = make([]int32, len(typeOf)), make([]int32, len(typeOf))
	for k, ti := range typeOf {
		rr.order[k], rr.fits[at[ti]] = at[ti], ti
		at[ti]++
	}
	rr.local = make([]int32, len(rr.vehicles))
	rr.lo, rr.hi = math.MaxInt, math.MinInt
	spans(func(k int, spans []vocab.SharedSpan) { rr.add(rr.order[k], spans) })
	// The vehicles with spans, in the order of their first, and by name:
	// equal names, equal ranks.
	n := len(rr.found)
	spanned := make([]int32, 0, n)
	for l := range int32(n) {
		if rr.found[l].first != math.MaxUint64 {
			spanned = append(spanned, l)
		}
	}
	slices.SortFunc(spanned, func(a, b int32) int { return cmp.Compare(rr.found[a].first, rr.found[b].first) })
	names := make([][]string, n)
	for k, l := range spanned {
		rr.found[l].pos = int32(k)
		names[l] = f.vehicleName(rr.vehicles[rr.found[l].id])
	}
	slices.SortFunc(spanned, func(a, b int32) int { return slices.Compare(names[a], names[b]) })
	for k, l := range spanned {
		if k > 0 && slices.Equal(names[l], names[spanned[k-1]]) {
			rr.found[l].rank = rr.found[spanned[k-1]].rank
		} else {
			rr.found[l].rank = int32(k)
		}
	}
	rr.each(func(r relatedRow) {
		rr.total++
		rr.most = max(rr.most, r.shared)
	})
	return rr
}

// vehicle is catalog vehicle id's index among those found, found now if
// not before.
func (rr *relatedRows) vehicle(id int32) int32 {
	if l := rr.local[id] - 1; l >= 0 {
		return l
	}
	l := int32(len(rr.found))
	rr.local[id] = l + 1
	rr.found = append(rr.found, foundVehicle{id: id, self: rr.v.modelOf(rr.vehicles[id]) >= 0, first: math.MaxUint64})
	return l
}

// own reports whether sp, of one of the fit's own vehicles, is the
// vehicle itself: in its year.
func (rr *relatedRows) own(sp *vocab.SharedSpan) bool {
	y := rr.v.year
	return y == nil || (sp.Bounds&vocab.SpanFirst == 0 || int32(*y) >= sp.First) && (sp.Bounds&vocab.SpanLast == 0 || int32(*y) <= sp.Last)
}

// add takes in shared fitment fi's spans: each vehicle's, joined where
// they overlap or meet, start and end its share in the vehicle's cells.
func (rr *relatedRows) add(fi int32, spans []vocab.SharedSpan) {
	rr.call++
	rr.log.fi = fi
	// The spans of other vehicles, and of v's own outside its year; a
	// vehicle's second marks its spans to be joined.
	ys := rr.ys[:0]
	for j := range spans {
		sp := &spans[j]
		l := rr.vehicle(sp.Vehicle)
		fv := &rr.found[l]
		if fv.self && rr.own(sp) {
			continue
		}
		fv.first = min(fv.first, uint64(fi)<<32|uint64(j))
		if fv.seen == rr.call {
			fv.again = rr.call
		}
		fv.seen = rr.call
		lo, hi := spanYears(sp)
		ys = append(ys, yearSpan{l, lo, hi})
	}
	rr.ys = ys
	// The spans of vehicles seen again, together by vehicle, each its
	// slot among them.
	js, at := rr.joins[:0], rr.at[:0]
	for _, y := range ys {
		fv := &rr.found[y.vehicle]
		if fv.again != rr.call {
			rr.share(y.vehicle, y.lo, y.hi)
			continue
		}
		if fv.slotted != rr.call {
			fv.slotted, fv.slot = rr.call, int32(len(at))
			at = append(at, 0)
		}
		at[fv.slot]++
		js = append(js, y)
	}
	rr.joins, rr.at = js, at
	if len(js) == 0 {
		return
	}
	next := int32(0)
	for i, n := range at {
		at[i], next = next, next+n
	}
	byVehicle := slices.Grow(rr.joined[:0], len(js))[:len(js)]
	for _, y := range js {
		fv := &rr.found[y.vehicle]
		byVehicle[at[fv.slot]] = y
		at[fv.slot]++
	}
	rr.joined = byVehicle
	// Each vehicle's, in order of their first years, joined.
	for i := 0; i < len(byVehicle); {
		j := i + 1
		for ; j < len(byVehicle) && byVehicle[j].vehicle == byVehicle[i].vehicle; j++ {
			for k := j; k > i && byVehicle[k].lo < byVehicle[k-1].lo; k-- {
				byVehicle[k], byVehicle[k-1] = byVehicle[k-1], byVehicle[k]
			}
		}
		l, lo, hi := byVehicle[i].vehicle, byVehicle[i].lo, byVehicle[i].hi
		for _, y := range byVehicle[i+1 : j] {
			if hi != math.MaxInt && y.lo > hi+1 {
				rr.share(l, lo, hi)
				lo, hi = y.lo, y.hi
			} else {
				hi = max(hi, y.hi)
			}
		}
		rr.share(l, lo, hi)
		i = j
	}
}

// share takes in a fitment shared by vehicle l from lo to hi, and logs it.
func (rr *relatedRows) share(l int32, lo, hi int) {
	fv := &rr.found[l]
	if lo == math.MinInt {
		fv.below++
	} else {
		rr.cell(fv, lo, 2)
	}
	if hi != math.MaxInt {
		rr.cell(fv, hi+1, -2)
	}
	rr.log.span(l, lo, hi)
}

// cell marks a change of the set a vehicle shares in year y, by d, twice
// the change in its count.
func (rr *relatedRows) cell(fv *foundVehicle, y int, d int32) {
	i := y - fv.base
	if uint(i) >= uint(len(fv.cells)) {
		rr.grow(fv, y)
		i = y - fv.base
	}
	p := &fv.cells[i]
	*p = (*p | 1) + d
}

// cellChunk is the most cells allocated together, for the windows of
// many vehicles: a request's first chunks are smaller, doubling.
const cellChunk = 16 << 10

// grow moves a vehicle's cells to hold year y as well: a vehicle's first
// to every year cells have held, as a catalog's vehicles share their
// years; more, at least doubling them, toward y.
func (rr *relatedRows) grow(fv *foundVehicle, y int) {
	rr.lo, rr.hi = min(rr.lo, y), max(rr.hi, y+1)
	lo, hi := rr.lo, rr.hi
	n, was := hi-lo, len(fv.cells)
	if was > 0 {
		lo, hi = min(y, fv.base), max(y+1, fv.base+was)
		n = max(hi-lo, 2*was)
	}
	base := lo
	if was > 0 && y < fv.base {
		base = hi - n
	}
	var cells []int32
	if n > cellChunk/4 {
		cells = make([]int32, n)
	} else {
		if cap(rr.cells)-len(rr.cells) < n {
			rr.cells = make([]int32, 0, max(min(2*cap(rr.cells), cellChunk), cellChunk/16, n))
		}
		at := len(rr.cells)
		rr.cells = rr.cells[:at+n]
		cells = rr.cells[at : at+n : at+n]
	}
	if was > 0 {
		copy(cells[fv.base-base:], fv.cells)
	}
	fv.base, fv.cells = base, cells
}

// each calls row with every row, by vehicle found, then in year order:
// a vehicle's cells read in year order, from below.
func (rr *relatedRows) each(row func(relatedRow)) {
	for l := range rr.found {
		fv := &rr.found[l]
		shared, lo := fv.below, math.MinInt
		for i, d := range fv.cells {
			if d == 0 {
				continue
			}
			y := fv.base + i
			if shared > 0 {
				row(relatedRow{vehicle: int32(l), shared: shared, lo: lo, hi: y - 1})
			}
			shared, lo = shared+d>>1, y
		}
		if shared > 0 {
			row(relatedRow{vehicle: int32(l), shared: shared, lo: lo, hi: math.MaxInt})
		}
	}
}

// compare orders rows by shared count, most first, then name and years,
// and then by their vehicles in the order of their first spans.
func (rr *relatedRows) compare(a, b relatedRow) int {
	va, vb := &rr.found[a.vehicle], &rr.found[b.vehicle]
	return cmp.Or(cmp.Compare(b.shared, a.shared), cmp.Compare(va.rank, vb.rank), cmp.Compare(a.lo, b.lo), cmp.Compare(lastKey(a.hi), lastKey(b.hi)), cmp.Compare(va.pos, vb.pos))
}

// lastKey orders a range's last year as compareYear does, unbounded first.
func lastKey(hi int) int {
	if hi == math.MaxInt {
		return math.MinInt
	}
	return hi
}

// page is pg's page of the rows, built: each its vehicle, years, count and
// the count per part type of the fitments it shares.
//
// Rows are ordered by shared count first, so only the counts the page
// reaches are sorted: those of the page's rows, and the rows of the same
// counts before and after it.
func (rr *relatedRows) page(pg pager) (int, string, []relatedGroup) {
	total := rr.total
	lo := min(pg.after, total)
	hi := min(lo+pg.limit, total)
	next := ""
	if hi < total {
		next = encodeFitCursor(pg.kind, hi)
	}
	out := []relatedGroup{}
	if lo >= hi {
		return total, next, out
	}
	per := make([]int, rr.most+1)
	rr.each(func(r relatedRow) { per[r.shared]++ })
	// The counts from top down to bottom are the page's: start is the
	// place of top's first row.
	start, top, bottom, band := 0, rr.most, int32(0), 0
	for at, n := 0, rr.most; n >= 0; n-- {
		if at+per[n] <= lo {
			start = at + per[n]
			top = n - 1
		}
		if at < hi {
			bottom = n
		}
		at += per[n]
	}
	for n := bottom; n <= top; n++ {
		band += per[n]
	}
	rows := make([]relatedRow, 0, band)
	rr.each(func(r relatedRow) {
		if bottom <= r.shared && r.shared <= top {
			rows = append(rows, r)
		}
	})
	slices.SortFunc(rows, rr.compare)
	rows = rows[lo-start : hi-start]
	sets := rr.sets(rows)
	// Each row's part types, from one slab.
	types := 0
	for _, s := range sets {
		for i, fi := range s {
			if i == 0 || rr.fits[fi] != rr.fits[s[i-1]] {
				types++
			}
		}
	}
	slab := make([]FitPartType, 0, types)
	out = make([]relatedGroup, 0, len(rows))
	for i, r := range rows {
		g := relatedGroup{vehicle: rr.vehicles[rr.found[r.vehicle].id], shared: int(r.shared)}
		if r.lo != math.MinInt {
			g.first = new(r.lo)
		}
		if r.hi != math.MaxInt {
			g.last = new(r.hi)
		}
		at := len(slab)
		for _, fi := range sets[i] { // ascending, so by part type
			t := &rr.types[rr.fits[fi]]
			if n := len(slab); n > at && slab[n-1].Type == t.id {
				slab[n-1].Count++
			} else {
				slab = append(slab, FitPartType{Type: t.id, Name: t.name, Count: 1})
			}
		}
		g.partTypes = slab[at:len(slab):len(slab)]
		out = append(out, g)
	}
	return total, next, out
}

// sets are the fitments each row shares in every year of it, ascending:
// as the set is the same in each, those with a span holding its first,
// read again from the log, of the rows' vehicles alone.
func (rr *relatedRows) sets(rows []relatedRow) [][]int32 {
	head := make([]int32, len(rr.found))
	for l := range head {
		head[l] = -1
	}
	next := make([]int32, len(rows))
	sets := make([][]int32, len(rows))
	n := 0
	for _, r := range rows {
		n += int(r.shared)
	}
	all := make([]int32, n)
	for i, r := range rows {
		next[i], head[r.vehicle] = head[r.vehicle], int32(i)
		sets[i], all = all[:0:r.shared], all[r.shared:]
	}
	// A vehicle's spans of a fitment are joined: none overlaps another.
	fi := int32(0)
	rr.log.each(func(f int32) { fi = f }, func(l int32, lo, hi int) {
		for i := head[l]; i >= 0; i = next[i] {
			if y := rows[i].lo; lo <= y && y <= hi {
				sets[i] = append(sets[i], fi)
			}
		}
	})
	for _, s := range sets {
		slices.Sort(s)
	}
	return sets
}

// shared reads, of v's vehicles' fitments of every part type, the
// candidates keep keeps, as related vehicles read them, and the catalog's
// vehicles their spans index: through the Reader's Shared when it is a
// fitment.Sharer, else from ByVehicle's whole fitments, indexing the
// vehicles by key as they come.
func (f Fitter) shared(v *reached, keep func(*vocab.Candidate) bool) ([]*vocab.Vehicle, func(func(int, []vocab.SharedSpan))) {
	if s, ok := f.Catalog.(fitment.Sharer); ok {
		return s.Vehicles(), s.Shared(v.vehicles, v.year, nil, keep).Spans
	}
	var vehicles []*vocab.Vehicle
	ids := map[string]int32{}
	var spans []vocab.SharedSpan
	at := []int{0}
	for _, c := range f.Catalog.ByVehicle("", v.vehicles, v.year, nil) {
		if !keep(&c) {
			continue
		}
		for j := range c.Fitment.Applications {
			a := &c.Fitment.Applications[j]
			k := vehicleKey(catalogVehicle(a.Vehicle))
			id, ok := ids[k]
			if !ok {
				id = int32(len(vehicles))
				ids[k] = id
				vehicles = append(vehicles, a.Vehicle)
			}
			sp := vocab.SharedSpan{Vehicle: id}
			if a.First != nil {
				sp.First, sp.Bounds = int32(*a.First), sp.Bounds|vocab.SpanFirst
			}
			if a.Last != nil {
				sp.Last, sp.Bounds = int32(*a.Last), sp.Bounds|vocab.SpanLast
			}
			spans = append(spans, sp)
		}
		at = append(at, len(spans))
	}
	return vehicles, func(each func(int, []vocab.SharedSpan)) {
		for k := range len(at) - 1 {
			each(k, spans[at[k]:at[k+1]])
		}
	}
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
