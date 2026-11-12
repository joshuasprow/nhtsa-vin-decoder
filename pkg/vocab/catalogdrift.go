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
	start := len(d.out)

	// The first line naming each vehicle, and how many do.
	firsts := make([]use, len(x.vehicleList))
	for i := range f.Fitments {
		fit := &f.Fitments[i]
		for j := range fit.Applications {
			firsts[fit.Applications[j].Vehicle.id].see(fit.Line, j)
		}
	}
	for i := range f.Redirects {
		firsts[f.Redirects[i].Vehicle.id].see(f.Redirects[i].Line, -1)
	}
	known := make([]bool, len(x.vehicleList)) // a vPIC model the month has
	for _, v := range x.vehicleList {
		u := firsts[v.id]
		switch {
		case vehicles != nil:
			d.vehicleEntry(v, &u, vehicles)
		case v.Make != 0:
			if _, ok := d.m.Make(v.Make); !ok {
				d.add(u.line, DriftMake, "%s/vehicle/make: vPIC make %d is not in data month %s%s", u.ptr(), v.Make, d.month, u.more())
			}
		case v.Model != 0:
			_, _, known[v.id] = d.m.Model(v.Model)
			if !known[v.id] {
				d.add(u.line, DriftModel, "%s/vehicle/model: vPIC model %d is not in data month %s%s", u.ptr(), v.Model, d.month, u.more())
			}
		}
	}

	// Model years (ADR 0022), where the vehicle is a vPIC model the month
	// has: each model and year range once, at the first application with
	// it.
	type modelRange struct {
		model             int32
		first, last       int
		hasFirst, hasLast bool
	}
	ranges := map[modelRange]*use{}
	var order []modelRange
	for i := range f.Fitments {
		fit := &f.Fitments[i]
		for j := range fit.Applications {
			a := &fit.Applications[j]
			if !known[a.Vehicle.id] || a.First == nil && a.Last == nil {
				continue
			}
			k := modelRange{model: a.Vehicle.Model}
			if a.First != nil {
				k.first, k.hasFirst = *a.First, true
			}
			if a.Last != nil {
				k.last, k.hasLast = *a.Last, true
			}
			u := ranges[k]
			if u == nil {
				u = &use{}
				ranges[k] = u
				order = append(order, k)
			}
			u.see(fit.Line, j)
		}
	}
	for _, k := range order {
		var first, last *int
		if k.hasFirst {
			first = new(k.first)
		}
		if k.hasLast {
			last = new(k.last)
		}
		u := ranges[k]
		d.modelYears(u.line, k.model, first, last, u.ptr, u.more())
	}

	// Each qualifier drift once, at the first application with it. A
	// reader shares one slice among the applications that write the same
	// qualifiers, so most are measured once per vehicle model: an
	// application whose vehicle is a vPIC model the month has is also
	// tested against the model's values (ModelDrift only, ADR 0015).
	type drifted struct {
		kind       DriftKind
		at, detail string // at: the pointer into the qualifiers
	}
	type qualifiersOf struct {
		qs    *Qualifier
		model int32
	}
	measured := map[qualifiersOf][]drifted{}
	type qualifierUse struct {
		use
		drifted
	}
	var uses []*qualifierUse
	byDetail := map[string]*qualifierUse{}
	for i := range f.Fitments {
		fit := &f.Fitments[i]
		for j := range fit.Applications {
			a := &fit.Applications[j]
			qs := a.Qualifiers
			if len(qs) == 0 {
				continue
			}
			k := qualifiersOf{qs: &qs[0]}
			if d.mm != nil && known[a.Vehicle.id] {
				k.model = a.Vehicle.Model
			}
			ds, ok := measured[k]
			if !ok {
				d.qualifiers(qs, k.model, func(kind DriftKind, ptr, detail string) { ds = append(ds, drifted{kind, ptr, detail}) })
				measured[k] = ds
			}
			for _, dr := range ds {
				q := byDetail[dr.detail]
				if q == nil {
					q = &qualifierUse{drifted: dr}
					byDetail[dr.detail] = q
					uses = append(uses, q)
				}
				q.see(fit.Line, j)
			}
		}
	}
	for _, q := range uses {
		d.add(q.line, q.kind, "%s/qualifiers%s: %s%s", q.ptr(), q.at, q.detail, q.more())
	}

	for i := range f.Redirects {
		if rd := &f.Redirects[i]; !x.answers(f, rd) {
			d.addFile(rd.Line, DriftRedirect, "/redirect/to: part type %q has no application for this vehicle in %s", rd.To, rangeText(rd.First, rd.Last))
		}
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
