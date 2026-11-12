package vocab

import (
	"bytes"
	"cmp"
	"hash/maphash"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// A catalog (ADR 0016, ADR 0017) is one author's statement of which parts
// fit which vehicles: a header naming the declared spine its part types
// come from and the vehicle vocabulary its vehicles are entries of (or
// vpic), then fitment, redirect and retired lines. Read refuses what is
// wrong with the file alone; Link what is wrong against the files loaded
// with it; Drift measures it against the data month and the vehicle
// vocabulary. A Catalog answers from it (package fitment's Reader).

// Side is the one position a fitment states for itself (ADR 0016).
type Side uint8

// The sides.
const (
	// SideNone: the fitment states no side.
	SideNone Side = iota
	SideLeft
	SideRight
	SideBoth
)

var sideNames = [...]string{"", "left", "right", "both"}

// String is the side as a file writes it, "" for none.
func (s Side) String() string {
	if int(s) < len(sideNames) {
		return sideNames[s]
	}
	return "side(" + strconv.Itoa(int(s)) + ")"
}

// ParseSide reads a side as a file writes it: left, right or both, and ""
// for none.
func ParseSide(s string) (Side, bool) {
	for i, name := range sideNames {
		if s == name {
			return Side(i), true
		}
	}
	return SideNone, false
}

// FitmentKey identifies a fitment within its catalog.
type FitmentKey struct {
	// Type is an entry id of the catalog's declared spine.
	Type string
	// Number is the catalog's own string for the part.
	Number string
	Side   Side
}

// String is the key as a file writes it.
func (k FitmentKey) String() string {
	s := `{"type":` + strconv.Quote(k.Type) + `,"number":` + strconv.Quote(k.Number)
	if k.Side != SideNone {
		s += `,"side":` + strconv.Quote(k.Side.String())
	}
	return s + "}"
}

// compareFitmentKeys orders keys by part type, side and number: the order
// candidates come in (ADR 0018).
func compareFitmentKeys(a, b FitmentKey) int {
	return cmp.Or(strings.Compare(a.Type, b.Type), cmp.Compare(a.Side, b.Side), strings.Compare(a.Number, b.Number))
}

// Fitment is one line of a catalog: one part, and every application of it.
type Fitment struct {
	// Line is the fitment's line in its file, from 1.
	Line int
	Key  FitmentKey
	// Note is passed through, never interpreted; "" when absent.
	Note         string
	Applications []Application
}

// Application is one vehicle and model-year range a fitment fits, with the
// catalog's option text for it and, optionally, qualifiers (ADR 0016).
type Application struct {
	// Vehicle is shared by every line of the catalog that names it.
	Vehicle *Vehicle
	// First and Last bound the model years, inclusive; nil is unbounded.
	First, Last *int
	// Options is the catalog's option text, verbatim, possibly "".
	Options string
	// Qualifiers are in ascending element id, as a mapping's (ADR 0009).
	Qualifiers []Qualifier
}

// Vehicle is a vehicle a catalog's line names: an entry of its vehicle
// vocabulary, by its fields, or, when its vehicles are vpic, a vPIC make
// or model or an extension. Vehicles equal after Normalize are one
// vehicle.
type Vehicle struct {
	// Entry is the vocabulary entry's fields by name, as the file first
	// writes them; nil when the catalog's vehicles are vpic.
	Entry map[string]string
	// Target is the vPIC make or model, or the extension, when the
	// catalog's vehicles are vpic: Make, Model or Ext.
	Target

	key string // identity: appendVehicleKey's
	id  int32  // index in the catalog's vehicles
}

// appendVehicleKey appends v's identity to b: its entry fields in name
// order, each value normalized, or its target.
func appendVehicleKey(b []byte, v *Vehicle) []byte {
	if v.Entry == nil {
		return appendTargetKey(b, v.Target)
	}
	names := make([]string, 0, len(v.Entry))
	for name := range v.Entry {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b = appendField(b, name, v.Entry[name])
	}
	return b
}

// appendField appends one entry field to a vehicle's key: its name as it
// is, its value normalized, each length prefixed.
func appendField[S ~string | ~[]byte](b []byte, name, value S) []byte {
	b = strconv.AppendInt(b, int64(len(name)), 10)
	b = append(b, ':')
	b = append(b, name...)
	return appendNormalizedValue(b, value)
}

// appendTargetKey appends a vpic vehicle's key, which no entry's key
// begins like.
func appendTargetKey(b []byte, t Target) []byte {
	switch {
	case t.Make != 0:
		return strconv.AppendInt(append(b, 0, 'm'), int64(t.Make), 10)
	case t.Model != 0:
		return strconv.AppendInt(append(b, 0, 'o'), int64(t.Model), 10)
	}
	return append(append(b, 0, 'x'), t.Ext...)
}

// Redirect is one line of a catalog: for a vehicle and years, part type
// From is listed under part type To (ADR 0016).
type Redirect struct {
	// Line is the redirect's line in its file, from 1.
	Line    int
	Vehicle *Vehicle
	// First and Last bound the model years, inclusive; nil is unbounded.
	First, Last *int
	From, To    string
	// Options is the catalog's option text, verbatim; "" when absent.
	Options string
}

// Retired is one line of a catalog: a number it no longer uses, and the
// fitment that replaced it.
type Retired struct {
	// Line is the line in its file, from 1.
	Line int
	Key  FitmentKey
	To   FitmentKey
	// Note is passed through, never interpreted; "" when absent.
	Note string
}

// catalogHeader reads a catalog header's own fields.
func (f *File) catalogHeader(spine, vehicles, release []byte) error {
	var err error
	if f.Spine, err = nameAt(spine, "/spine"); err != nil {
		return err
	}
	if s, ok := str(vehicles); ok && s == Reserved {
		f.Vehicles = s
	} else if f.Vehicles, err = nameAt(vehicles, "/vehicles"); err != nil {
		return err
	}
	if release != nil {
		if f.Release, err = nonEmptyAt(release, "/release"); err != nil {
			return err
		}
	}
	return nil
}

// catalogLineNames are a catalog line's fields: the three kinds first.
var catalogLineNames = []string{"fitment", "redirect", "retired", "note", "applications", "to"}

// catalogLineFields are the indexes in catalogLineNames each kind of line
// takes.
var catalogLineFields = [3][]int{{0, 3, 4}, {1}, {2, 3, 5}}

// The fields of a catalog line's objects.
var (
	keyNames         = []string{"type", "number", "side"}
	applicationNames = []string{"vehicle", "first", "last", "options", "qualifiers"}
	redirectNames    = []string{"vehicle", "first", "last", "from", "to", "options"}
	vpicVehicleNames = []string{"make", "model", "ext"}
)

// catalogReader is a reader's state for a catalog's lines. It interns what
// lines repeat: vehicles, option text, qualifiers and years.
type catalogReader struct {
	line [6][]byte // catalogLineNames
	key  [3][]byte // keyNames
	app  [5][]byte // applicationNames
	red  [6][]byte // redirectNames
	vpic [3][]byte // vpicVehicleNames
	apps [][]byte

	pairs []pair // a vehicle's fields
	buf   []byte // a vehicle's key

	// appKeys holds a fitment's applications' keys, each ending at its
	// appEnds; seen finds a repeated one by hash.
	appKeys []byte
	appEnds []int
	seen    map[uint64]int
	seed    maphash.Seed

	vehicles   map[string]*Vehicle
	options    map[string]string
	qualifiers map[string][]Qualifier
	yearOf     [yearSpan]*int
	appS       slab[Application]
}

// pair is one member of a JSON object.
type pair struct{ name, value []byte }

// The model years a reader shares one int for.
const yearBase, yearSpan = 1880, 320

// readCatalogLine reads a catalog's line n, a fitment into its slot of the
// file's Fitments, a redirect or a retired number onto c's.
func (r *reader) readCatalogLine(c *chunk, slot, n int, line []byte) error {
	cr := &r.catalog
	raw, err := lineValue(line)
	if err != nil {
		return err
	}
	v := cr.line[:]
	unknown, has, ok := members(raw, catalogLineNames, v)
	if !ok {
		return errAt("", "must be an object")
	}
	kind, kinds := 0, 0
	for i := range 3 {
		if v[i] != nil {
			kind, kinds = i, kinds+1
		}
	}
	switch {
	case kinds == 0 && has:
		return errAt("", "unknown field %q", unknown)
	case kinds == 0:
		return errAt("", "holds none of fitment, redirect or retired")
	case kinds > 1:
		var named []string
		for i := range 3 {
			if v[i] != nil {
				named = append(named, catalogLineNames[i])
			}
		}
		return errAt("", "holds more than one of fitment, redirect or retired: %s", strings.Join(named, ", "))
	}
	for i := range catalogLineNames {
		if v[i] != nil && !slices.Contains(catalogLineFields[kind], i) && (!has || catalogLineNames[i] < unknown) {
			unknown, has = catalogLineNames[i], true
		}
	}
	if has {
		return errAt("", "unknown field %q", unknown)
	}
	switch kind {
	case 0:
		fit, err := r.fitment(n, v)
		if err != nil {
			return err
		}
		r.f.Fitments[slot] = fit
	case 1:
		rd, err := r.redirect(n, v[1])
		if err != nil {
			return under("/redirect", err)
		}
		c.redirects = append(c.redirects, rd)
	case 2:
		rt := Retired{Line: n}
		if rt.Key, err = r.fitmentKey(v[2]); err != nil {
			return under("/retired", err)
		}
		if v[5] == nil {
			return errAt("", "missing field \"to\"")
		}
		if rt.To, err = r.fitmentKey(v[5]); err != nil {
			return under("/to", err)
		}
		if v[3] != nil {
			if rt.Note, err = stringAt(v[3], "/note"); err != nil {
				return err
			}
		}
		c.retired = append(c.retired, rt)
	}
	return nil
}

// fitment reads a fitment line n, given its members.
func (r *reader) fitment(n int, v [][]byte) (Fitment, error) {
	cr := &r.catalog
	if v[4] == nil {
		return Fitment{}, errAt("", "missing field \"applications\"")
	}
	fit := Fitment{Line: n}
	var err error
	if fit.Key, err = r.fitmentKey(v[0]); err != nil {
		return Fitment{}, under("/fitment", err)
	}
	var ok bool
	if cr.apps, ok = items(v[4], cr.apps[:0]); !ok {
		return Fitment{}, errAt("/applications", "must be an array")
	}
	if len(cr.apps) > 0 {
		fit.Applications = cr.appS.take(len(cr.apps))
		for i, raw := range cr.apps {
			if err := r.application(raw, &fit.Applications[i]); err != nil {
				return Fitment{}, under("/applications/"+strconv.Itoa(i), err)
			}
		}
		if err := cr.repeated(fit.Applications); err != nil {
			return Fitment{}, err
		}
	}
	if v[3] != nil {
		if fit.Note, err = stringAt(v[3], "/note"); err != nil {
			return Fitment{}, err
		}
	}
	return fit, nil
}

// fitmentKey reads a fitment key: type, number and an optional side.
func (r *reader) fitmentKey(raw []byte) (FitmentKey, error) {
	v := r.catalog.key[:]
	unknown, has, ok := members(raw, keyNames, v)
	switch {
	case !ok:
		return FitmentKey{}, errAt("", "must be an object")
	case has:
		return FitmentKey{}, errAt("", "unknown field %q", unknown)
	case v[0] == nil:
		return FitmentKey{}, errAt("", "missing field \"type\"")
	case v[1] == nil:
		return FitmentKey{}, errAt("", "missing field \"number\"")
	}
	var k FitmentKey
	for i, p := range []*string{&k.Type, &k.Number} {
		s, ok := r.str(v[i])
		switch {
		case !ok:
			return FitmentKey{}, errAt("/"+keyNames[i], "must be a string")
		case s == "":
			return FitmentKey{}, errAt("/"+keyNames[i], "must not be empty")
		}
		*p = s
	}
	if v[2] != nil {
		s, ok := str(v[2])
		side, known := ParseSide(s)
		if !ok || !known || side == SideNone {
			return FitmentKey{}, errAt("/side", "must be left, right or both")
		}
		k.Side = side
	}
	return k, nil
}

// application reads one of a fitment's applications into a. Its error's
// pointer is into the application.
func (r *reader) application(raw []byte, a *Application) error {
	cr := &r.catalog
	v := cr.app[:]
	unknown, has, ok := members(raw, applicationNames, v)
	switch {
	case !ok:
		return errAt("", "must be an object")
	case has:
		return errAt("", "unknown field %q", unknown)
	case v[0] == nil:
		return errAt("", "missing field \"vehicle\"")
	case v[3] == nil:
		return errAt("", "missing field \"options\": the catalog's option text, \"\" for none")
	}
	var err error
	if a.Vehicle, err = r.vehicle(v[0]); err != nil {
		return under("/vehicle", err)
	}
	if v[4] != nil && a.Vehicle.Ext != "" {
		return errAt("/qualifiers", "an extension vehicle takes no qualifiers: no decode lands on an extension")
	}
	if a.First, a.Last, err = r.catalogYears(v[1], v[2]); err != nil {
		return err
	}
	if a.Options, err = r.option(v[3]); err != nil {
		return err
	}
	if v[4] != nil {
		// Qualifiers repeat: one slice per text of them, read once.
		if qs, ok := cr.qualifiers[string(v[4])]; ok {
			a.Qualifiers = qs
			return nil
		}
		if a.Qualifiers, err = qualifiersAt(v[4], "/qualifiers"); err != nil {
			return err
		}
		if cr.qualifiers == nil {
			cr.qualifiers = map[string][]Qualifier{}
		}
		cr.qualifiers[string(v[4])] = a.Qualifiers
	}
	return nil
}

// option reads option text, shared with every earlier application of the
// reader's that has the same.
func (r *reader) option(raw []byte) (string, error) {
	cr := &r.catalog
	b, plain := plainString(raw)
	if !plain {
		s, ok := str(raw)
		if !ok {
			return "", errAt("/options", "must be a string")
		}
		b = []byte(s)
	}
	if s, ok := cr.options[string(b)]; ok {
		return s, nil
	}
	if cr.options == nil {
		cr.options = map[string]string{}
	}
	s := r.text.of(b)
	cr.options[s] = s
	return s, nil
}

// catalogYears reads an application's or redirect's first and last years.
func (r *reader) catalogYears(first, last []byte) (*int, *int, error) {
	var years [2]*int
	for i, raw := range [2][]byte{first, last} {
		if raw == nil {
			continue
		}
		y, ok := integer(raw)
		if !ok || y < math.MinInt32 || y > math.MaxInt32 {
			return nil, nil, errAt("/"+applicationNames[1+i], "must be an integer year")
		}
		years[i] = r.year(int(y))
	}
	if years[0] != nil && years[1] != nil && *years[0] > *years[1] {
		return nil, nil, errAt("", "first year %d is after last year %d", *years[0], *years[1])
	}
	return years[0], years[1], nil
}

// year is y, shared with every other of the reader's years that is y.
func (r *reader) year(y int) *int {
	i := y - yearBase
	if i >= 0 && i < yearSpan && r.catalog.yearOf[i] != nil {
		return r.catalog.yearOf[i]
	}
	p := &r.years.take(1)[0]
	*p = y
	if i >= 0 && i < yearSpan {
		r.catalog.yearOf[i] = p
	}
	return p
}

// vehicle reads a line's vehicle, shared with every earlier line of the
// reader's that names the same one. Its error's pointer is into the
// vehicle.
func (r *reader) vehicle(raw []byte) (*Vehicle, error) {
	cr := &r.catalog
	var t Target
	if r.f.Vehicles == Reserved {
		v := cr.vpic[:]
		unknown, has, ok := members(raw, vpicVehicleNames, v)
		switch {
		case !ok:
			return nil, errAt("", "must be an object")
		case has:
			return nil, errAt("", "unknown field %q: a vpic catalog's vehicle is one of make, model or ext", unknown)
		}
		var named []string
		for i := range v {
			if v[i] != nil {
				named = append(named, vpicVehicleNames[i])
			}
		}
		switch {
		case len(named) == 0:
			return nil, errAt("", "names no vehicle: one of make, model or ext")
		case len(named) > 1:
			return nil, errAt("", "names more than one vehicle: %s", strings.Join(named, ", "))
		}
		var err error
		switch {
		case v[0] != nil:
			t.Make, err = idAt(v[0], "/make")
		case v[1] != nil:
			t.Model, err = idAt(v[1], "/model")
		default:
			t.Ext, err = extRefAt(v[2], "/ext")
		}
		if err != nil {
			return nil, err
		}
		cr.buf = appendTargetKey(cr.buf[:0], t)
	} else {
		cr.pairs = cr.pairs[:0]
		if !eachMember(raw, func(key, value []byte) {
			cr.pairs = append(cr.pairs, pair{key, value})
		}) {
			return nil, errAt("", "must be an object")
		}
		if len(cr.pairs) == 0 {
			return nil, errAt("", "names no field: a vehicle is an entry of vocabulary %q, by its fields", r.f.Vehicles)
		}
		// Each name and value as its text, any escape resolved.
		for i := range cr.pairs {
			p := &cr.pairs[i]
			if s, ok := plainString(p.name); ok {
				p.name = s
			} else {
				s, _ := str(p.name)
				p.name = []byte(s)
			}
			if s, ok := plainString(p.value); ok {
				p.value = s
				continue
			}
			s, ok := str(p.value)
			if !ok {
				return nil, errAt("/"+pointerToken(string(p.name)), "must be a string")
			}
			p.value = []byte(s)
		}
		// As encoding/json reads them into a map: the last of a repeated
		// name.
		slices.SortStableFunc(cr.pairs, func(a, b pair) int { return bytes.Compare(a.name, b.name) })
		kept := cr.pairs[:0]
		for i, p := range cr.pairs {
			if i+1 == len(cr.pairs) || !bytes.Equal(cr.pairs[i+1].name, p.name) {
				kept = append(kept, p)
			}
		}
		cr.pairs = kept
		cr.buf = cr.buf[:0]
		for _, p := range cr.pairs {
			cr.buf = appendField(cr.buf, p.name, p.value)
		}
	}
	if v, ok := cr.vehicles[string(cr.buf)]; ok {
		return v, nil
	}
	v := &Vehicle{Target: t, key: string(cr.buf)}
	if r.f.Vehicles != Reserved {
		v.Entry = make(map[string]string, len(cr.pairs))
		for _, p := range cr.pairs {
			v.Entry[string(p.name)] = string(p.value)
		}
	}
	if cr.vehicles == nil {
		cr.vehicles = map[string]*Vehicle{}
	}
	cr.vehicles[v.key] = v
	return v, nil
}

// redirect reads a redirect line's redirect.
func (r *reader) redirect(n int, raw []byte) (Redirect, error) {
	v := r.catalog.red[:]
	unknown, has, ok := members(raw, redirectNames, v)
	switch {
	case !ok:
		return Redirect{}, errAt("", "must be an object")
	case has:
		return Redirect{}, errAt("", "unknown field %q", unknown)
	}
	for _, i := range []int{0, 3, 4} {
		if v[i] == nil {
			return Redirect{}, errAt("", "missing field %q", redirectNames[i])
		}
	}
	rd := Redirect{Line: n}
	var err error
	if rd.Vehicle, err = r.vehicle(v[0]); err != nil {
		return Redirect{}, under("/vehicle", err)
	}
	if rd.First, rd.Last, err = r.catalogYears(v[1], v[2]); err != nil {
		return Redirect{}, err
	}
	for i, p := range []*string{&rd.From, &rd.To} {
		s, ok := r.str(v[3+i])
		switch {
		case !ok:
			return Redirect{}, errAt("/"+redirectNames[3+i], "must be a string")
		case s == "":
			return Redirect{}, errAt("/"+redirectNames[3+i], "must not be empty")
		}
		*p = s
	}
	if v[5] != nil {
		if rd.Options, err = r.option(v[5]); err != nil {
			return Redirect{}, err
		}
	}
	return rd, nil
}

// repeated refuses a fitment's applications when two are one: the same
// vehicle, years, option text and qualifiers.
func (cr *catalogReader) repeated(apps []Application) error {
	if len(apps) < 2 {
		return nil
	}
	if cr.seen == nil {
		cr.seen, cr.seed = map[uint64]int{}, maphash.MakeSeed()
	}
	clear(cr.seen)
	cr.appKeys, cr.appEnds = cr.appKeys[:0], cr.appEnds[:0]
	for i := range apps {
		start := len(cr.appKeys)
		cr.appKeys = appendApplicationKey(cr.appKeys, &apps[i])
		cr.appEnds = append(cr.appEnds, len(cr.appKeys))
		key := cr.appKeys[start:]
		h := maphash.Bytes(cr.seed, key)
		if j, ok := cr.seen[h]; ok {
			from := 0
			if j > 0 {
				from = cr.appEnds[j-1]
			}
			if string(cr.appKeys[from:cr.appEnds[j]]) == string(key) {
				return errAt("/applications/"+strconv.Itoa(i), "duplicate application: /applications/%d has the same vehicle, years, options and qualifiers", j)
			}
		}
		cr.seen[h] = i
	}
	return nil
}

// appendApplicationKey appends what makes an application itself: its
// vehicle, years, option text and qualifiers, each value set in order.
func appendApplicationKey(b []byte, a *Application) []byte {
	part := func(b []byte, s string) []byte {
		return append(append(strconv.AppendInt(b, int64(len(s)), 10), ':'), s...)
	}
	b = part(b, a.Vehicle.key)
	for _, y := range []*int{a.First, a.Last} {
		if y == nil {
			b = append(b, '-')
		} else {
			b = strconv.AppendInt(append(b, '='), int64(*y), 10)
		}
	}
	b = part(b, a.Options)
	for _, q := range a.Qualifiers {
		b = strconv.AppendInt(append(b, 'q'), int64(q.Element), 10)
		values := slices.Clone(q.Values)
		slices.SortFunc(values, compareValues)
		for _, v := range values {
			if v.IsText {
				b = part(append(b, 't'), v.Text)
			} else {
				b = strconv.AppendInt(append(b, 'a'), int64(v.Attribute), 10)
			}
		}
	}
	return b
}

// compareValues orders qualifier values: attribute ids, then text.
func compareValues(a, b Value) int {
	if a.IsText != b.IsText {
		if a.IsText {
			return 1
		}
		return -1
	}
	return cmp.Or(cmp.Compare(a.Attribute, b.Attribute), strings.Compare(a.Text, b.Text))
}

// catalogIndex is a catalog's indexes, made once when it is read.
type catalogIndex struct {
	applications int
	// vehicles finds a vehicle by its key; vehicleList is every vehicle the
	// catalog names, in the order of their first line.
	vehicles    map[string]int32
	vehicleList []*Vehicle
	// order is Fitments' indexes ordered by their keys, a fitment's rank
	// its place here.
	order []int32
	// byVehicle finds per part type and vehicle its span: of postings
	// that name both, (rank, application) pairs, ascending.
	byVehicle map[typeVehicle]int32
	spans     []span
	postings  []posting
	// redirects holds per part type redirected from and vehicle the
	// redirects' indexes in Redirects, ascending.
	redirects map[typeVehicle][]int32
	// retired finds a retired key's index in Retired.
	retired map[FitmentKey]int32
}

// typeVehicle is a part type and a vehicle's index in vehicleList.
type typeVehicle struct {
	typ     string
	vehicle int32
}

type span struct{ lo, hi int32 }

// posting is an application: its fitment's rank and its index in the
// fitment's Applications.
type posting struct{ rank, app int32 }

// indexCatalog keeps, of a catalog's lines, those that read, and indexes
// them, returning every fault in line order: the chunks', a repeated
// fitment key, and a retired number that is live, repeated or points at
// no fitment.
func (f *File) indexCatalog(chunks []*chunk) Faults {
	var faults Faults
	f.Fitments, faults = keep(f.Path, chunks, f.Fitments, func(x *Fitment) int { return x.Line },
		func(*Fitment, int, int) error { return nil })
	nr, nt := 0, 0
	for _, c := range chunks {
		nr, nt = nr+len(c.redirects), nt+len(c.retired)
	}
	if nr > 0 {
		f.Redirects = make([]Redirect, 0, nr)
	}
	if nt > 0 {
		f.Retired = make([]Retired, 0, nt)
	}
	for _, c := range chunks {
		f.Redirects = append(f.Redirects, c.redirects...)
		f.Retired = append(f.Retired, c.retired...)
		c.redirects, c.retired = nil, nil
	}
	faults = append(faults, f.indexLines(len(faults) == 0)...)
	slices.SortStableFunc(faults, func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
	return faults
}

// indexLines indexes a catalog's Fitments, Redirects and Retired, each in
// file order, returning the faults only the whole file shows: a repeated
// fitment key, and a retired number that is live, repeated or, when every
// line read, points at no fitment. Its vehicles become one Vehicle each,
// numbered in the order of the first line naming each.
func (f *File) indexLines(read bool) Faults {
	var faults Faults
	x := &catalogIndex{vehicles: map[string]int32{}}
	f.catalog = x

	// One Vehicle per vehicle, across the readers that each interned
	// their own.
	var last, lastCanon *Vehicle
	canon := func(v *Vehicle) *Vehicle {
		if v == last {
			return lastCanon
		}
		last = v
		if i, ok := x.vehicles[v.key]; ok {
			lastCanon = x.vehicleList[i]
			return lastCanon
		}
		v.id = int32(len(x.vehicleList))
		x.vehicles[v.key] = v.id
		x.vehicleList = append(x.vehicleList, v)
		lastCanon = v
		return v
	}
	ri := 0 // the next redirect, by line, so vehicles come in line order
	for i := range f.Fitments {
		fit := &f.Fitments[i]
		for ; ri < len(f.Redirects) && f.Redirects[ri].Line < fit.Line; ri++ {
			f.Redirects[ri].Vehicle = canon(f.Redirects[ri].Vehicle)
		}
		for j := range fit.Applications {
			a := &fit.Applications[j]
			a.Vehicle = canon(a.Vehicle)
		}
		x.applications += len(fit.Applications)
	}
	for ; ri < len(f.Redirects); ri++ {
		f.Redirects[ri].Vehicle = canon(f.Redirects[ri].Vehicle)
	}

	x.order = make([]int32, len(f.Fitments))
	for i := range x.order {
		x.order[i] = int32(i)
	}
	slices.SortFunc(x.order, func(a, b int32) int {
		return cmp.Or(compareFitmentKeys(f.Fitments[a].Key, f.Fitments[b].Key), cmp.Compare(a, b))
	})
	for k := 1; k < len(x.order); k++ {
		prev, fit := &f.Fitments[x.order[k-1]], &f.Fitments[x.order[k]]
		if prev.Key == fit.Key {
			faults = append(faults, fault(f.Path, fit.Line, "/fitment: duplicate fitment key: line %d has it too", prev.Line))
		}
	}

	// Postings, counted per part type and vehicle, then placed in rank
	// order, so each span is ascending.
	x.byVehicle = map[typeVehicle]int32{}
	spanOf := make([]int32, 0, x.applications) // each posting's span
	for _, i := range x.order {
		fit := &f.Fitments[i]
		for j := range fit.Applications {
			k := typeVehicle{fit.Key.Type, fit.Applications[j].Vehicle.id}
			s, ok := x.byVehicle[k]
			if !ok {
				s = int32(len(x.spans))
				x.byVehicle[k] = s
				x.spans = append(x.spans, span{})
			}
			x.spans[s].hi++
			spanOf = append(spanOf, s)
		}
	}
	at := int32(0)
	for i := range x.spans {
		n := x.spans[i].hi
		x.spans[i] = span{at, at}
		at += n
	}
	x.postings = make([]posting, x.applications)
	n := 0
	for rank, i := range x.order {
		for j := range f.Fitments[i].Applications {
			s := &x.spans[spanOf[n]]
			x.postings[s.hi] = posting{int32(rank), int32(j)}
			s.hi++
			n++
		}
	}

	x.redirects = map[typeVehicle][]int32{}
	for i := range f.Redirects {
		rd := &f.Redirects[i]
		k := typeVehicle{rd.From, rd.Vehicle.id}
		x.redirects[k] = append(x.redirects[k], int32(i))
	}

	x.retired = make(map[FitmentKey]int32, len(f.Retired))
	for i := range f.Retired {
		rt := &f.Retired[i]
		if j, dup := x.retired[rt.Key]; dup {
			faults = append(faults, fault(f.Path, rt.Line, "/retired: duplicate retired number: line %d retires it too", f.Retired[j].Line))
			continue
		}
		x.retired[rt.Key] = int32(i)
		if !read {
			continue // a fitment that did not read is not missing
		}
		if live := f.fitment(rt.Key); live != nil {
			faults = append(faults, fault(f.Path, rt.Line, "/retired: %s is a live fitment, on line %d", rt.Key, live.Line))
		}
		if f.fitment(rt.To) == nil {
			faults = append(faults, fault(f.Path, rt.Line, "/to: no fitment %s in the file: a retired number points at a fitment", rt.To))
		}
	}
	return faults
}

// fitment finds the fitment with key k, nil when there is none.
func (f *File) fitment(k FitmentKey) *Fitment {
	x := f.catalog
	i, ok := slices.BinarySearchFunc(x.order, k, func(i int32, k FitmentKey) int { return compareFitmentKeys(f.Fitments[i].Key, k) })
	if !ok {
		return nil
	}
	return &f.Fitments[x.order[i]]
}

// Catalog is a loaded catalog file (ADR 0016) as package fitment's Reader
// reads it: by vehicle, number, redirect and page. A JSON Lines catalog
// answers from its lines, through indexes made once when the file was
// read; a compiled catalog (ADR 0021) from its file, read in place. Both
// give the same answers. It never changes and is safe for concurrent use.
// It is a fitment catalog, not list's catalog of what is loaded
// (CatalogEntry).
type Catalog struct{ f *File }

// Catalog is f as a Catalog, nil unless f is a catalog.
func (f *File) Catalog() *Catalog {
	if f.Kind != KindCatalog || f.catalog == nil && f.compiled == nil {
		return nil
	}
	return &Catalog{f}
}

// File is the catalog's file.
func (c *Catalog) File() *File { return c.f }

// Candidate is a fitment that may be the part for a vehicle, with the
// applications of it that matched, in file order.
type Candidate struct {
	Fitment      *Fitment
	Applications []*Application
}

// ByVehicle returns the fitments of part type partType with an application
// to one of vehicles whose years hold year (nil: any year), and of side
// *side (nil: any side), each with its matching applications. Candidates
// come in key order: side, then number. The vehicles are the catalog's
// own, as its lines name them; turning a request's VIN or another
// vocabulary's entry into them is a translation's work, not the
// catalog's. Qualifiers are not checked: that takes a decode.
func (c *Catalog) ByVehicle(partType string, vehicles []Vehicle, year *int, side *Side) []Candidate {
	if cc := c.f.compiled; cc != nil {
		return cc.byVehicle(c, partType, vehicles, year, side)
	}
	x := c.f.catalog
	var ps []posting
	for i := range vehicles {
		id, ok := c.vehicleID(&vehicles[i])
		if !ok {
			continue
		}
		if s, ok := x.byVehicle[typeVehicle{partType, id}]; ok {
			sp := x.spans[s]
			ps = append(ps, x.postings[sp.lo:sp.hi]...)
		}
	}
	if len(vehicles) > 1 {
		slices.SortFunc(ps, func(a, b posting) int { return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.app, b.app)) })
		ps = slices.Compact(ps)
	}
	var out []Candidate
	for _, p := range ps {
		fit := &c.f.Fitments[x.order[p.rank]]
		a := &fit.Applications[p.app]
		if side != nil && fit.Key.Side != *side || !holds(a.First, a.Last, year) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Fitment == fit {
			out[n-1].Applications = append(out[n-1].Applications, a)
			continue
		}
		out = append(out, Candidate{Fitment: fit, Applications: []*Application{a}})
	}
	return out
}

// holds reports whether year falls within first and last, nil being
// unbounded; a nil year is any year.
func holds(first, last, year *int) bool {
	return year == nil || (first == nil || *year >= *first) && (last == nil || *year <= *last)
}

// vehicleID finds a vehicle the catalog names.
func (c *Catalog) vehicleID(v *Vehicle) (int32, bool) {
	if (v.Entry == nil) != (c.f.Vehicles == Reserved) {
		return 0, false
	}
	var ids map[string]int32
	if cc := c.f.compiled; cc != nil {
		ids = cc.vehicleAt
	} else {
		ids = c.f.catalog.vehicles
	}
	var buf [64]byte
	id, ok := ids[string(appendVehicleKey(buf[:0], v))]
	return id, ok
}

// ByNumber returns the fitment with part type partType, number number and
// side side: directly, or through a retired number, which retired then is.
// fitment is nil when there is neither.
func (c *Catalog) ByNumber(partType, number string, side Side) (fitment *Fitment, retired *Retired) {
	k := FitmentKey{Type: partType, Number: number, Side: side}
	if cc := c.f.compiled; cc != nil {
		return cc.byNumber(k)
	}
	if fit := c.f.fitment(k); fit != nil {
		return fit, nil
	}
	i, ok := c.f.catalog.retired[k]
	if !ok {
		return nil, nil
	}
	rt := &c.f.Retired[i]
	return c.f.fitment(rt.To), rt
}

// Redirect returns the redirects from part type partType for one of
// vehicles (the catalog's own, as ByVehicle takes them) whose years hold
// year (nil: any year), in file order, each once: each names the part
// type the catalog lists partType under for its vehicle.
func (c *Catalog) Redirect(partType string, vehicles []Vehicle, year *int) []*Redirect {
	if cc := c.f.compiled; cc != nil {
		return cc.redirect(c, partType, vehicles, year)
	}
	var idx []int32
	for i := range vehicles {
		if id, ok := c.vehicleID(&vehicles[i]); ok {
			idx = append(idx, c.f.catalog.redirects[typeVehicle{partType, id}]...)
		}
	}
	if len(vehicles) > 1 {
		slices.Sort(idx)
		idx = slices.Compact(idx)
	}
	var out []*Redirect
	for _, i := range idx {
		if rd := &c.f.Redirects[i]; holds(rd.First, rd.Last, year) {
			out = append(out, rd)
		}
	}
	return out
}

// Fitments pages through the catalog's fitments in key order (part type,
// side, number): those of part type partType ("" for every type) whose
// keys come after *after (nil: from the first), at most limit of them
// (limit < 1: every one). The last one's key is the next page's after.
func (c *Catalog) Fitments(partType string, after *FitmentKey, limit int) []*Fitment {
	if cc := c.f.compiled; cc != nil {
		return cc.page(partType, after, limit)
	}
	f, order := c.f, c.f.catalog.order
	key := func(i int) FitmentKey { return f.Fitments[order[i]].Key }
	lo, hi := 0, len(order)
	if partType != "" {
		lo = sort.Search(len(order), func(i int) bool { return key(i).Type >= partType })
		hi = lo + sort.Search(len(order)-lo, func(i int) bool { return key(lo+i).Type > partType })
	}
	if after != nil {
		lo += sort.Search(hi-lo, func(i int) bool { return compareFitmentKeys(key(lo+i), *after) > 0 })
	}
	if limit > 0 && hi-lo > limit {
		hi = lo + limit
	}
	out := make([]*Fitment, 0, hi-lo)
	for i := lo; i < hi; i++ {
		out = append(out, &f.Fitments[order[i]])
	}
	return out
}

// CatalogInfo is what a catalog is: its header, and how many lines of
// each kind and applications it holds.
type CatalogInfo struct {
	Name, Version string
	// Release and DataMonth are "" when the header gives none.
	Release, DataMonth string
	// Spine is the declared spine of its part types; Vehicles the vehicle
	// vocabulary its vehicles are entries of, or vpic.
	Spine, Vehicles string
	// Hash is the blake3 hash of the file's bytes: of a compiled catalog,
	// of the JSON Lines file it was compiled from.
	Hash                                       [32]byte
	Fitments, Applications, Redirects, Retired int
}

// Info describes the catalog.
func (c *Catalog) Info() CatalogInfo {
	if cc := c.f.compiled; cc != nil {
		return cc.info
	}
	f := c.f
	return CatalogInfo{
		Name: f.Name, Version: f.Version, Release: f.Release, DataMonth: f.DataMonth,
		Spine: f.Spine, Vehicles: f.Vehicles, Hash: f.Hash,
		Fitments: len(f.Fitments), Applications: f.catalog.applications, Redirects: len(f.Redirects), Retired: len(f.Retired),
	}
}

// Vehicles are every vehicle the catalog's lines name, once each, in the
// order of the first line naming each.
func (c *Catalog) Vehicles() []*Vehicle {
	if cc := c.f.compiled; cc != nil {
		return cc.vehicles
	}
	return c.f.catalog.vehicleList
}
