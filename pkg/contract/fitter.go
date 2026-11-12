package contract

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/fitment"
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
// nitrovin.
func (f Fitter) Fit(req FitRequest) (Fit, error) {
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
	out.Catalog = &FitCatalog{Name: info.Name, Version: info.Version, Release: info.Release}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	pg := pager{after: after, limit: limit, kind: kind}
	if kind == fitVehicles {
		if err := f.vehicles(req, info, &out, pg); err != nil {
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
// ("/3/part").
func (f Fitter) FitBatch(reqs []FitRequest) ([]Fit, error) {
	now := time.Now()
	out := make([]Fit, len(reqs))
	for i, req := range reqs {
		if req.Now.IsZero() {
			req.Now = now
		}
		fit, err := f.Fit(req)
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
// each with the applications that hold, directly or, when it has none,
// through the redirects listed for its vehicles: how they were reached.
func (f Fitter) candidates(t partType, v *reached, side vocab.Side) ([]FitCandidate, FitReach) {
	if cs := f.matching(f.Catalog.ByVehicle(t.id, v.vehicles, v.year, nil), t, v, side, nil); len(cs) > 0 {
		return cs, ReachedDirect
	}
	var out []FitCandidate
	for _, rd := range f.Catalog.Redirect(t.id, v.vehicles, v.year) {
		for _, c := range f.matching(f.Catalog.ByVehicle(rd.To, []vocab.Vehicle{*rd.Vehicle}, v.year, nil), t, v, side, rd) {
			c.Reached = ReachedRedirect
			c.Redirects = []FitRedirect{fitRedirect(rd)}
			out = append(out, c)
		}
	}
	return mergeCandidates(out), ReachedRedirect
}

// matching is cs, side kept, as an answer has them: each with the
// applications that hold against the vehicle's year and decode (and the
// redirect's years, through rd), the unchecked of the route to each.
func (f Fitter) matching(cs []vocab.Candidate, t partType, v *reached, side vocab.Side, rd *vocab.Redirect) []FitCandidate {
	var out []FitCandidate
	for _, c := range cs {
		if !keeps(side, c.Fitment.Key.Side) {
			continue
		}
		fc := FitCandidate{Fitment: fitmentOf(c.Fitment), Reached: ReachedDirect}
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
			if i := modelOf(v.vehicles, a.Vehicle); i >= 0 {
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

// modelOf is the index in vehicles of the catalog's vehicle v, -1 when it
// is none of them.
func modelOf(vehicles []vocab.Vehicle, v *vocab.Vehicle) int {
	cv := catalogVehicle(v)
	for i := range vehicles {
		if sameVehicle(catalogVehicle(&vehicles[i]), cv) {
			return i
		}
	}
	return -1
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

// relatedGroup is a related vehicle being counted: a catalog vehicle and
// a year range, and the fitments it shares.
type relatedGroup struct {
	vehicle     *vocab.Vehicle
	first, last *int
	key         string // vehicle and years, for grouping
	name        []string
	shared      int
	partTypes   []FitPartType
	last1       *vocab.Fitment // the last fitment counted, so each counts once
}

// related are the vehicles, each a catalog vehicle and a year range, with
// an application of a fitment the vehicle has (types, side kept, its
// applications holding), other than the vehicle's own: ordered by shared
// count, most first, then name and years.
func (f Fitter) related(types []partType, v *reached, side vocab.Side) []relatedGroup {
	byKey := map[string]*relatedGroup{}
	var groups []*relatedGroup
	keys := map[*vocab.Vehicle]string{}
	keyOf := func(veh *vocab.Vehicle) string {
		k, ok := keys[veh]
		if !ok {
			k = vehicleKey(catalogVehicle(veh))
			keys[veh] = k
		}
		return k
	}
	own := map[string]bool{}
	for i := range v.vehicles {
		own[vehicleKey(catalogVehicle(&v.vehicles[i]))] = true
	}
	var buf []byte
	for _, t := range types {
		for _, c := range f.Catalog.ByVehicle(t.id, v.vehicles, v.year, nil) {
			if !keeps(side, c.Fitment.Key.Side) || !slices.ContainsFunc(c.Applications, func(a *vocab.Application) bool {
				_, ok := a.Check(v.year, v.decoded)
				return ok
			}) {
				continue
			}
			for i := range c.Fitment.Applications {
				a := &c.Fitment.Applications[i]
				vk := keyOf(a.Vehicle)
				if own[vk] && (v.year == nil || holds(a.First, a.Last, *v.year)) {
					continue // the vehicle itself
				}
				buf = appendYears(append(buf[:0], vk...), a.First, a.Last)
				g := byKey[string(buf)]
				if g == nil {
					g = &relatedGroup{vehicle: a.Vehicle, first: a.First, last: a.Last, key: string(buf), name: f.vehicleName(a.Vehicle)}
					byKey[g.key] = g
					groups = append(groups, g)
				}
				if g.last1 == c.Fitment {
					continue
				}
				g.last1 = c.Fitment
				g.shared++
				if n := len(g.partTypes); n > 0 && g.partTypes[n-1].Type == t.id {
					g.partTypes[n-1].Count++
				} else {
					g.partTypes = append(g.partTypes, FitPartType{Type: t.id, Name: t.name, Count: 1})
				}
			}
		}
	}
	slices.SortFunc(groups, func(a, b *relatedGroup) int {
		return cmp.Or(cmp.Compare(b.shared, a.shared), slices.Compare(a.name, b.name), compareYear(a.first, b.first), compareYear(a.last, b.last))
	})
	out := make([]relatedGroup, len(groups))
	for i, g := range groups {
		out[i] = *g
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

func appendYears(b []byte, first, last *int) []byte {
	for _, y := range []*int{first, last} {
		if y == nil {
			b = append(b, '-')
		} else {
			b = strconv.AppendInt(append(b, '='), int64(*y), 10)
		}
	}
	return b
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
func (f Fitter) vehicles(req FitRequest, info vocab.CatalogInfo, out *Fit, pg pager) error {
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
	out.Total, out.Next, apps = page(apps, pg, []*vocab.Application{})
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
