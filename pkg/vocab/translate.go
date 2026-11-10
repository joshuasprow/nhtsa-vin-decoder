package vocab

import (
	"maps"
	"slices"
	"strconv"
)

// Ref names one spine entry, as a mapping target names it: a vPIC make or
// model or an extension (Spine ""), or an entry ID of the declared spine
// named Spine.
type Ref struct {
	Spine string
	Target
}

// Query is one translation (ADR 0012): from a VIN, a vocabulary entry or a
// spine entry, through that one spine, to the target vocabularies.
type Query struct {
	From From
	// To names the target vocabularies; none answers the spine alone.
	To []string
	// ModelYear is the model year when Decoded gives none.
	ModelYear *int
	// Decoded is the decode of the request's VIN, given as From or as
	// context; nil without one. It alone can check a qualifier.
	Decoded *Decoded
}

// From is where a translation starts: a VIN (VIN, whose decode is the
// Query's Decoded), a vocabulary's entry (Vocabulary and Entry, the
// entry's fields by name) or a spine entry (Ref).
type From struct {
	VIN        bool
	Vocabulary string
	Entry      map[string]string
	Ref        Ref
}

// Decoded is what a VIN's decode says, as a translation reads it: its
// make, model and model year, and the values of each element it decoded.
// NewDecoded reads it from the decode's items.
type Decoded struct {
	// Make and Model are the decoded vPIC ids, 0 when undecoded.
	Make, Model int32
	// ModelYear is the decoded model year, nil when undecoded.
	ModelYear *int
	// Values holds the values of every element the decode gave a non-empty
	// value: attribute ids for a lookup element, text for a text element.
	Values map[int32][]Value
}

// Item is one item of a VIN's decode as a translation reads it: its
// element, its value and its attribute id, each "" when null.
type Item struct {
	ElementID   int32
	Value       string
	AttributeID string
}

// Element ids a translation reads from a decode.
const (
	elementMake      = 26
	elementModel     = 28
	elementModelYear = 29
)

// NewDecoded is what a VIN's decode says to a translation: its items, read
// against m, the month that decoded them. This is translation's
// decoder-free core (ADR 0013): whatever decoded the VIN, the engine or
// /v1/decode, its items mean the same here. m.Element tells a lookup
// element, whose value is its attribute id, from a text element, whose
// value is its text. An item with no value, of an element m lacks, or of
// a lookup element whose attribute id is no number (Error Code's code
// list) says nothing.
func NewDecoded(m Month, items []Item) *Decoded {
	d := &Decoded{Values: map[int32][]Value{}}
	for _, it := range items {
		if it.Value == "" {
			continue
		}
		lookup, ok := m.Element(it.ElementID)
		if !ok {
			continue
		}
		if !lookup {
			d.Values[it.ElementID] = append(d.Values[it.ElementID], Value{IsText: true, Text: it.Value})
			if it.ElementID == elementModelYear {
				if y, err := strconv.Atoi(it.Value); err == nil {
					d.ModelYear = &y
				}
			}
			continue
		}
		id, err := strconv.ParseInt(it.AttributeID, 10, 32)
		if err != nil {
			continue
		}
		d.Values[it.ElementID] = append(d.Values[it.ElementID], Value{Attribute: int32(id)})
		switch it.ElementID {
		case elementMake:
			d.Make = int32(id)
		case elementModel:
			d.Model = int32(id)
		}
	}
	return d
}

// Status is what a translation's from is to its vocabulary.
type Status string

// The statuses of a translation's from.
const (
	// StatusMapped: the entry has mappings, or the VIN or spine entry
	// reaches the spine.
	StatusMapped Status = "mapped"
	// StatusUnmapped: the vocabulary lists the entry without mappings.
	StatusUnmapped Status = "unmapped"
	// StatusUnknown: the vocabulary or spine has no such entry, or the VIN
	// decodes to no vPIC make or model.
	StatusUnknown Status = "unknown"
)

// Translation is a translation's route: the spine entries reached and,
// per target vocabulary, the entries reached from them.
type Translation struct {
	Status Status
	// Vocabulary is the from entry's vocabulary, and Entry the entry, when
	// the vocabulary has it.
	Vocabulary *File
	Entry      *Entry
	// Spine is every spine entry reached, once each, in route order: each
	// mapping's spine entry, then the make a model implies.
	Spine []Reached
	// To is one per target vocabulary, in request order, each named once.
	To []Translated
	// Files are the files the translation used, in load order.
	Files []*File
}

// Reached is one spine entry: its name from the data month or its file,
// the make it sits under, and the mappings that reached it.
type Reached struct {
	Ref Ref
	// Name is "" for a spine entry its file leaves unnamed, and for one
	// the data month lacks (Drift).
	Name  string
	Drift bool
	// Under is the make a vPIC or extension model sits under.
	Under *Ref
	// Via are the from entry's mappings that reached this spine entry;
	// none when a VIN or the spine entry itself is the from.
	Via []Via
	// Unconfirmed is what no Via could check, empty when one checked
	// everything.
	Unconfirmed Unconfirmed
}

// Via is one mapping that held on a route: the spine entry it names (Ref)
// and what of it could not be checked, along the whole route to it.
type Via struct {
	Ref         Ref
	Mapping     *Mapping
	Unconfirmed Unconfirmed
}

// Translated is the entries one target vocabulary reaches from the spine.
type Translated struct {
	Vocabulary *File
	// Matches are in list order: by normalized fields.
	Matches []Match
}

// Match is one target entry, with every mapping of it that held.
type Match struct {
	Entry *Entry
	// Via are in spine order, then mapping order.
	Via []Via
	// Unconfirmed is what no Via could check, empty when one checked
	// everything.
	Unconfirmed Unconfirmed
}

// Unconfirmed is what applied on a route but could not be checked
// (ADR 0012): qualifier elements with no decode, or that the decode left
// empty, and the model year when none was known.
type Unconfirmed struct {
	// Elements are ascending element ids.
	Elements  []int32
	ModelYear bool
}

// IsZero reports whether everything was checked.
func (u Unconfirmed) IsZero() bool { return len(u.Elements) == 0 && !u.ModelYear }

// union is what is unchecked in u or v.
func (u Unconfirmed) union(v Unconfirmed) Unconfirmed {
	out := Unconfirmed{ModelYear: u.ModelYear || v.ModelYear}
	if len(u.Elements)+len(v.Elements) > 0 {
		out.Elements = append(slices.Clone(u.Elements), v.Elements...)
		slices.Sort(out.Elements)
		out.Elements = slices.Compact(out.Elements)
	}
	return out
}

// unconfirmedOf is the Unconfirmed of something reached by vias: nothing
// when one of them checked everything, else what any left unchecked.
func unconfirmedOf(vias []Via) Unconfirmed {
	var u Unconfirmed
	for _, v := range vias {
		if v.Unconfirmed.IsZero() {
			return Unconfirmed{}
		}
		u = u.union(v.Unconfirmed)
	}
	return u
}

// conditions checks mappings against what a translation knows: the model
// year, and the decode for qualifiers.
type conditions struct {
	year    *int
	decoded *Decoded
}

// check is whether mp holds (ADR 0009): a known model year outside its
// range, or a decoded value outside a qualifier's set, rules it out. What
// could not be checked keeps it, and is returned.
func (c conditions) check(mp *Mapping) (Unconfirmed, bool) {
	var u Unconfirmed
	if mp.First != nil || mp.Last != nil {
		switch y := c.year; {
		case y == nil:
			u.ModelYear = true
		case mp.First != nil && *y < *mp.First, mp.Last != nil && *y > *mp.Last:
			return Unconfirmed{}, false
		}
	}
	for _, q := range mp.Qualifiers {
		var got []Value
		if c.decoded != nil {
			got = c.decoded.Values[q.Element]
		}
		if len(got) == 0 {
			u.Elements = append(u.Elements, q.Element)
			continue
		}
		if !slices.ContainsFunc(q.Values, func(v Value) bool { return slices.Contains(got, v) }) {
			return Unconfirmed{}, false
		}
	}
	return u, true
}

// Translate answers q (ADR 0012) through exactly one spine: vPIC's for a
// VIN, a vPIC id, an extension or a vehicle vocabulary, a declared spine's
// for its ids and its part vocabularies. Year scope and qualifiers are
// checked against q's model year and decode (ADR 0009); a vPIC or extension
// model reaches the make it sits under too. Nothing to translate is never
// an error: the error is a *RequestError, for a vocabulary or spine nothing
// loaded has, entry fields other than the vocabulary's, or a target
// vocabulary on another spine.
func (s *Set) Translate(q Query) (*Translation, error) {
	c := conditions{year: q.ModelYear, decoded: q.Decoded}
	if q.Decoded != nil && q.Decoded.ModelYear != nil {
		c.year = q.Decoded.ModelYear
	}
	t := &Translation{Status: StatusUnknown}
	used := map[*File]bool{}
	r := route{s: s, at: map[Ref]int{}}
	var spine string
	switch from := q.From; {
	case from.VIN:
		spine = Reserved
		if d := q.Decoded; d != nil {
			if d.Model != 0 {
				r.add(Ref{Target: Target{Model: d.Model}}, nil)
			}
			if d.Make != 0 {
				r.add(Ref{Target: Target{Make: d.Make}}, nil)
			}
		}
		if len(r.spine) > 0 {
			t.Status = StatusMapped
		}
	case from.Vocabulary != "":
		f, err := s.vocabulary(from.Vocabulary, "/from/vocabulary")
		if err != nil {
			return nil, err
		}
		for _, k := range slices.Sorted(maps.Keys(from.Entry)) {
			if !slices.Contains(f.Fields, k) {
				return nil, refuse("/from/entry/"+pointerToken(k), "vocabulary %q has no field %q: its fields are %s", f.Name, k, quoted(f.Fields))
			}
		}
		for _, k := range f.Fields {
			if _, ok := from.Entry[k]; !ok {
				return nil, refuse("/from/entry", "missing field %q: vocabulary %q's fields are %s", k, f.Name, quoted(f.Fields))
			}
		}
		used[f] = true
		t.Vocabulary = f
		if spine = spineOf(f); spine != Reserved {
			used[s.byName[spine]] = true
		}
		e, ok := f.Entry(from.Entry)
		if !ok {
			break
		}
		t.Entry = e
		t.Status = StatusUnmapped
		if len(e.Mappings) > 0 {
			t.Status = StatusMapped
		}
		for i := range e.Mappings {
			mp := &e.Mappings[i]
			u, ok := c.check(mp)
			if !ok {
				continue
			}
			ref := Ref{Spine: f.Spine, Target: mp.Target}
			r.add(ref, &Via{Ref: ref, Mapping: mp, Unconfirmed: u})
		}
	default:
		ref := from.Ref
		var ok bool
		switch spine = Reserved; {
		case ref.ID != "":
			f := s.byName[ref.Spine]
			switch {
			case ref.Spine == Reserved:
				return nil, refuse("/from/spine", "vPIC's entries are named by make or model, not by id")
			case f == nil:
				return nil, refuse("/from/spine", "no spine %q is loaded", ref.Spine)
			case f.Kind != KindSpine:
				return nil, refuse("/from/spine", "%q is %s, not a spine", ref.Spine, f.Kind.noun())
			}
			spine = ref.Spine
			used[f] = true
			_, ok = f.SpineEntry(ref.ID)
		case ref.Ext != "":
			if !extPattern.MatchString(ref.Ext) {
				return nil, refuse("/from/ext", "%q is not an extension reference, ext:<set>/<id>", ref.Ext)
			}
			name, id := splitExt(ref.Ext)
			f := s.byName[name]
			switch {
			case f == nil:
				return nil, refuse("/from/ext", "no extension set %q is loaded", name)
			case f.Kind != KindExtensionSet:
				return nil, refuse("/from/ext", "%q is %s, not an extension set", name, f.Kind.noun())
			}
			_, ok = f.Extension(id)
		case ref.Model != 0:
			_, _, ok = s.month.Model(ref.Model)
		case ref.Make != 0:
			_, ok = s.month.Make(ref.Make)
		default:
			return nil, refuse("/from", "from names no VIN, vocabulary entry or spine entry")
		}
		if ok {
			t.Status = StatusMapped
			r.add(ref, nil)
		}
	}
	for i := range r.spine {
		it := &r.spine[i]
		it.Unconfirmed = unconfirmedOf(it.Via)
		if set := s.describe(it); set != nil {
			used[set] = true
		}
	}
	t.Spine = r.spine

	t.To = []Translated{}
	named := map[string]bool{}
	for i, name := range q.To {
		ptr := "/to/" + strconv.Itoa(i)
		f, err := s.vocabulary(name, ptr)
		if err != nil {
			return nil, err
		}
		if to := spineOf(f); to != spine {
			return nil, refuse(ptr, "vocabulary %q maps onto spine %q; this translation routes through %q, and a translation crosses exactly one spine", name, to, spine)
		}
		if named[name] {
			continue
		}
		named[name] = true
		used[f] = true
		t.To = append(t.To, s.match(f, r.spine, c))
	}
	for _, f := range s.files {
		if used[f] {
			t.Files = append(t.Files, f)
		}
	}
	return t, nil
}

// route collects the spine entries a translation reaches, once each.
type route struct {
	s     *Set
	spine []Reached
	at    map[Ref]int // spine entry → index in spine
}

// add reaches ref, and the make a model ref sits under, through via (nil
// when the from is the spine entry or a VIN).
func (r *route) add(ref Ref, via *Via) {
	r.put(ref, via)
	if mk, ok := r.s.makeOf(ref); ok {
		r.put(mk, via)
	}
}

func (r *route) put(ref Ref, via *Via) {
	i, ok := r.at[ref]
	if !ok {
		i = len(r.spine)
		r.at[ref] = i
		r.spine = append(r.spine, Reached{Ref: ref})
	}
	if via != nil {
		r.spine[i].Via = append(r.spine[i].Via, *via)
	}
}

// makeOf is the make a vPIC or extension model sits under.
func (s *Set) makeOf(ref Ref) (Ref, bool) {
	switch {
	case ref.Model != 0:
		if mk, _, ok := s.month.Model(ref.Model); ok {
			return Ref{Target: Target{Make: mk}}, true
		}
	case ref.Ext != "":
		if x := s.extension(ref.Ext); x != nil && !x.IsMake() {
			if x.MakeExt != "" {
				return Ref{Target: Target{Ext: x.MakeExt}}, true
			}
			return Ref{Target: Target{Make: x.Make}}, true
		}
	}
	return Ref{}, false
}

// extension finds an extension by reference, nil when no loaded set
// defines it.
func (s *Set) extension(ref string) *Extension {
	name, id := splitExt(ref)
	if f := s.byName[name]; f != nil && f.Kind == KindExtensionSet {
		if x, ok := f.Extension(id); ok {
			return x
		}
	}
	return nil
}

// describe names it from the data month or its file, marks a vPIC id the
// month lacks as drift, and sets the make a model sits under. It returns
// the extension set an extension is defined in.
func (s *Set) describe(it *Reached) *File {
	switch ref := it.Ref; {
	case ref.Make != 0:
		var ok bool
		it.Name, ok = s.month.Make(ref.Make)
		it.Drift = !ok
	case ref.Model != 0:
		mk, name, ok := s.month.Model(ref.Model)
		it.Name, it.Drift = name, !ok
		if ok {
			it.Under = &Ref{Target: Target{Make: mk}}
		}
	case ref.Ext != "":
		x := s.extension(ref.Ext)
		if x == nil {
			return nil
		}
		it.Name = x.Name
		if x.IsMake() {
			it.Name = x.MakeName
		} else if mk, ok := s.makeOf(ref); ok {
			it.Under = &mk
		}
		name, _ := splitExt(ref.Ext)
		return s.byName[name]
	case ref.ID != "":
		if e, ok := s.byName[ref.Spine].SpineEntry(ref.ID); ok {
			it.Name = e.Name
		}
	}
	return nil
}

// match is the entries of vocabulary f that the spine entries reach: each
// entry once, with every mapping of it to one of them that holds.
func (s *Set) match(f *File, spine []Reached, c conditions) Translated {
	at := map[*Entry]int{}
	var ms []Match
	for _, it := range spine {
		for _, e := range f.EntriesFor(it.Ref.Target) {
			for i := range e.Mappings {
				mp := &e.Mappings[i]
				if mp.Target != it.Ref.Target {
					continue
				}
				u, ok := c.check(mp)
				if !ok {
					continue
				}
				j, seen := at[e]
				if !seen {
					j = len(ms)
					at[e] = j
					ms = append(ms, Match{Entry: e})
				}
				ms[j].Via = append(ms[j].Via, Via{Ref: it.Ref, Mapping: mp, Unconfirmed: u.union(it.Unconfirmed)})
			}
		}
	}
	keys := make(map[*Entry][]string, len(ms))
	for i := range ms {
		ms[i].Unconfirmed = unconfirmedOf(ms[i].Via)
		keys[ms[i].Entry] = normalized(ms[i].Entry.Fields)
	}
	slices.SortFunc(ms, func(a, b Match) int { return compareKeys(keys[a.Entry], keys[b.Entry]) })
	return Translated{Vocabulary: f, Matches: ms}
}
