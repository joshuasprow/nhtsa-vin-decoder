package vocab

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
)

// A catalog's drift is reported once per vehicle, per qualifier value, per
// model and year range and per redirect, not once per line: a catalog
// names each vehicle on thousands of lines, and one unmapped vehicle is
// one thing to curate. A finding is about the first application or
// redirect that has it, and counts the others as "(and N more)".

// catalog measures d.f, a catalog whose vehicles are entries of vehicles,
// nil when they are vpic: vehicles the vocabulary lacks or lists without
// a mapping, vPIC ids the month lacks, applications to a vPIC model in
// years outside the model's (ADR 0022), qualifiers its decode cannot
// satisfy, and redirects whose target part type has no application for
// the redirect's vehicle and years.
func (d *drifts) catalog(vehicles *File) {
	f, x := d.f, d.f.catalog
	g := d.catalogDrift(vehicles)
	for _, v := range x.vehicleList {
		g.vehicle(v)
	}
	for i := range f.Fitments {
		fit := &f.Fitments[i]
		for j := range fit.Applications {
			a := &fit.Applications[j]
			var qs qualifierKey
			if len(a.Qualifiers) > 0 {
				qs.p = &a.Qualifiers[0]
			}
			g.application(fit.Line, j, a, qs)
		}
	}
	for i := range f.Redirects {
		rd := &f.Redirects[i]
		g.redirect(rd)
		if !x.answers(f, rd) {
			g.unanswered(rd)
		}
	}
	g.report()
}

// catalogDrift gathers a catalog's drift as its vehicles, applications and
// redirects are seen, in line order, for one Drift or ModelDrift call: a
// JSON Lines catalog's from its lines in memory, a compile's from its
// source as it streams by. report adds the findings.
type catalogDrift struct {
	d        *drifts
	vehicles *File // its vehicle vocabulary; nil when vpic
	list     []*Vehicle
	// firsts are, by vehicle id, the first naming and how many name it;
	// known, whether it is a vPIC model the month has.
	firsts []use
	known  []bool
	// Model years (ADR 0022), where the vehicle is a vPIC model the month
	// has: each model and year range once, at the first application with
	// it.
	ranges map[modelRange]*use
	order  []modelRange
	// Each qualifier drift once, at the first application with it. Most
	// applications share their qualifiers with others, and are measured
	// once per set and vehicle model: an application whose vehicle is a
	// vPIC model the month has is also tested against the model's values
	// (ModelDrift only, ADR 0015).
	measured   map[qualifiersOf][]drifted
	qualifiers []*qualifierUse
	byDetail   map[string]*qualifierUse
	redirects  []*Redirect // those no application answers
}

type modelRange struct {
	model             int32
	first, last       int
	hasFirst, hasLast bool
}

// qualifierKey is one set of qualifiers: the first of a slice that
// applications share, or a compile's id for it.
type qualifierKey struct {
	p  *Qualifier
	id uint32
}

type qualifiersOf struct {
	qs    qualifierKey
	model int32
}

type drifted struct {
	kind       DriftKind
	at, detail string // at: the pointer into the qualifiers
}

type qualifierUse struct {
	use
	drifted
}

func (d *drifts) catalogDrift(vehicles *File) *catalogDrift {
	return &catalogDrift{d: d, vehicles: vehicles, ranges: map[modelRange]*use{}, measured: map[qualifiersOf][]drifted{}, byDetail: map[string]*qualifierUse{}}
}

// vehicle sees vehicle v, numbered next, before any line naming it.
func (g *catalogDrift) vehicle(v *Vehicle) {
	known := false
	if g.vehicles == nil && v.Model != 0 {
		_, _, known = g.d.m.Model(v.Model)
	}
	g.list, g.firsts, g.known = append(g.list, v), append(g.firsts, use{}), append(g.known, known)
}

// application sees application j on line, whose qualifiers are qs.
func (g *catalogDrift) application(line, j int, a *Application, qs qualifierKey) {
	id := a.Vehicle.id
	g.firsts[id].see(line, j)
	if !g.known[id] {
		if len(a.Qualifiers) > 0 {
			g.qualifier(line, j, a, qualifiersOf{qs: qs})
		}
		return
	}
	if a.First != nil || a.Last != nil {
		k := modelRange{model: a.Vehicle.Model}
		if a.First != nil {
			k.first, k.hasFirst = *a.First, true
		}
		if a.Last != nil {
			k.last, k.hasLast = *a.Last, true
		}
		u := g.ranges[k]
		if u == nil {
			u = &use{}
			g.ranges[k] = u
			g.order = append(g.order, k)
		}
		u.see(line, j)
	}
	if len(a.Qualifiers) > 0 {
		k := qualifiersOf{qs: qs}
		if g.d.mm != nil {
			k.model = a.Vehicle.Model
		}
		g.qualifier(line, j, a, k)
	}
}

func (g *catalogDrift) qualifier(line, j int, a *Application, k qualifiersOf) {
	ds, ok := g.measured[k]
	if !ok {
		g.d.qualifiers(a.Qualifiers, k.model, func(kind DriftKind, ptr, detail string) { ds = append(ds, drifted{kind, ptr, detail}) })
		g.measured[k] = ds
	}
	for _, dr := range ds {
		q := g.byDetail[dr.detail]
		if q == nil {
			q = &qualifierUse{drifted: dr}
			g.byDetail[dr.detail] = q
			g.qualifiers = append(g.qualifiers, q)
		}
		q.see(line, j)
	}
}

// redirect sees redirect rd.
func (g *catalogDrift) redirect(rd *Redirect) { g.firsts[rd.Vehicle.id].see(rd.Line, -1) }

// unanswered sees that no application of rd's target part type is for
// its vehicle in its years.
func (g *catalogDrift) unanswered(rd *Redirect) { g.redirects = append(g.redirects, rd) }

// report adds the findings, in line order: on one line, a vehicle's
// first, then model years', qualifiers' and the redirect's.
func (g *catalogDrift) report() {
	d := g.d
	start := len(d.out)
	for _, v := range g.list {
		u := g.firsts[v.id]
		switch {
		case g.vehicles != nil:
			d.vehicleEntry(v, &u, g.vehicles)
		case v.Make != 0:
			if _, ok := d.m.Make(v.Make); !ok {
				d.add(u.line, DriftMake, "%s/vehicle/make: vPIC make %d is not in data month %s%s", u.ptr(), v.Make, d.month, u.more())
			}
		case v.Model != 0 && !g.known[v.id]:
			d.add(u.line, DriftModel, "%s/vehicle/model: vPIC model %d is not in data month %s%s", u.ptr(), v.Model, d.month, u.more())
		}
	}
	for _, k := range g.order {
		var first, last *int
		if k.hasFirst {
			first = new(k.first)
		}
		if k.hasLast {
			last = new(k.last)
		}
		u := g.ranges[k]
		d.modelYears(u.line, k.model, first, last, u.ptr, u.more())
	}
	for _, q := range g.qualifiers {
		d.add(q.line, q.kind, "%s/qualifiers%s: %s%s", q.ptr(), q.at, q.detail, q.more())
	}
	slices.SortStableFunc(g.redirects, func(a, b *Redirect) int { return cmp.Compare(a.Line, b.Line) })
	for _, rd := range g.redirects {
		d.addFile(rd.Line, DriftRedirect, "/redirect/to: part type %q has no application for this vehicle in %s", rd.To, rangeText(rd.First, rd.Last))
	}
	slices.SortStableFunc(d.out[start:], func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
}

// addFile adds a finding about the file and the files loaded with it,
// whatever the data month.
func (d *drifts) addFile(line int, kind DriftKind, format string, a ...any) {
	if !d.keeps(kind) {
		return
	}
	d.out = append(d.out, Finding{File: d.f.Path, Line: line, Severity: SeverityDrift, Detail: fmt.Sprintf(format, a...), Drift: kind})
}

// vehicleEntry measures catalog vehicle v, which u names first, against
// its vehicle vocabulary: an entry it lacks, or lists without a mapping.
// It depends on the vocabulary loaded, not the month, so a compiled
// catalog's is measured at load (vehicleDrift).
func (d *drifts) vehicleEntry(v *Vehicle, u *use, vehicles *File) {
	e, ok := vehicles.Entry(v.Entry)
	switch {
	case !ok:
		d.addFile(u.line, DriftVehicle, "%s/vehicle: vocabulary %q has no entry %s%s", u.ptr(), vehicles.Name, entryText(v.Entry), u.more())
	case len(e.Mappings) == 0:
		d.addFile(u.line, DriftVehicle, "%s/vehicle: vocabulary %q lists %s without a mapping, so no translation reaches it%s", u.ptr(), vehicles.Name, entryText(v.Entry), u.more())
	}
}

// compiled reports a compiled catalog's drift: the drift its compiling
// stored, against its month, model-qualifier included (Drift keeps every
// other kind, ModelDrift that one), but its vehicle drift measured again
// against vehicles, the vehicle vocabulary loaded now (nil when its
// vehicles are vpic). That takes one look per vehicle, not per application, and a
// vocabulary loaded beside it may not be the one it was compiled with.
// Findings come as Drift's for its source: in line order, a line's
// vehicle drift first.
func (d *drifts) compiled(c *compiled, vehicles *File) {
	start := len(d.out)
	if vehicles != nil {
		for i, v := range c.vehicles {
			d.vehicleEntry(v, &c.uses[i], vehicles)
		}
	}
	for _, f := range c.meta.Drift {
		if f.Drift != DriftVehicle && d.keeps(f.Drift) {
			d.out = append(d.out, f)
		}
	}
	slices.SortStableFunc(d.out[start:], func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
}

// use is where a catalog first names something, and how many times it
// does.
type use struct {
	line int
	app  int // the application on line, -1 for a redirect
	n    int
}

// see counts one more naming, application app on line.
func (u *use) see(line, app int) {
	if u.n == 0 || line < u.line {
		u.line, u.app = line, app
	}
	u.n++
}

// ptr is the pointer into the first line to what it names.
func (u *use) ptr() string {
	if u.app < 0 {
		return "/redirect"
	}
	return fmt.Sprintf("/applications/%d", u.app)
}

// more counts the namings after the first.
func (u *use) more() string {
	if u.n > 1 {
		return fmt.Sprintf(" (and %d more)", u.n-1)
	}
	return ""
}

// answers reports whether some application of redirect rd's target part
// type is for its vehicle in years that overlap its own.
func (x *catalogIndex) answers(f *File, rd *Redirect) bool {
	s, ok := x.byVehicle[typeVehicle{rd.To, rd.Vehicle.id}]
	if !ok {
		return false
	}
	sp := x.spans[s]
	for _, p := range x.postings[sp.lo:sp.hi] {
		a := &f.Fitments[x.order[p.rank]].Applications[p.app]
		if (a.First == nil || rd.Last == nil || *a.First <= *rd.Last) && (rd.First == nil || a.Last == nil || *rd.First <= *a.Last) {
			return true
		}
	}
	return false
}

// entryText is a vocabulary entry's fields as a file writes them.
func entryText(fields map[string]string) string {
	b, _ := json.Marshal(fields) // keys sorted
	return string(b)
}

// rangeText says a model-year range in words.
func rangeText(first, last *int) string {
	switch {
	case first != nil && last != nil && *first == *last:
		return fmt.Sprintf("%d", *first)
	case first != nil && last != nil:
		return fmt.Sprintf("%d–%d", *first, *last)
	case first != nil:
		return fmt.Sprintf("%d on", *first)
	case last != nil:
		return fmt.Sprintf("years through %d", *last)
	}
	return "any year"
}
