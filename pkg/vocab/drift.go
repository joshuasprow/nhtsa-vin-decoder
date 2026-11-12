package vocab

import (
	"fmt"
	"iter"
)

// Month is the data month drift is measured against: vPIC's makes and
// models, and the values its decode can produce. Package nitrovin
// implements it on a loaded artifact; anything that knows the same facts
// can.
type Month interface {
	// Name is the data month, YYYY_MM.
	Name() string
	// Make is vPIC make id's name.
	Make(id int32) (name string, ok bool)
	// Model is vPIC model id's make and name.
	Model(id int32) (make int32, name string, ok bool)
	// Makes yields every vPIC make's id and name.
	Makes() iter.Seq2[int32, string]
	// Models yields the id and name of every vPIC model under make.
	Models(make int32) iter.Seq2[int32, string]
	// Element reports whether element id is one of the month's public
	// elements, and whether it is a lookup element, whose qualifier values
	// are attribute ids, rather than a text one, whose values are strings.
	Element(id int32) (lookup, ok bool)
	// Attribute reports whether the month's decode can give lookup element
	// the attribute id.
	Attribute(element, id int32) bool
	// Text reports whether the month's decode can give text element exactly
	// the value text.
	Text(element int32, text string) bool
}

// Drift measures linked files against month m (ADR 0011, ADR 0017): vPIC
// make and model ids it lacks, qualifiers its decode cannot satisfy,
// extensions named, after Normalize, like a vPIC make or like a vPIC
// model under the same make, and of a catalog, vehicles its vehicle
// vocabulary lacks or lists unmapped and redirects no application
// answers. Findings come in file and line order. Drift never refuses: the
// files are served as they are.
func Drift(m Month, files []*File) []Finding {
	d := &drifts{m: m, month: m.Name(), models: map[int32]map[string]int32{}}
	byName := map[string]*File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	for _, f := range files {
		d.f = f
		switch f.Kind {
		case KindVocabulary:
			for i := range f.Entries {
				e := &f.Entries[i]
				for j := range e.Mappings {
					d.mapping(e.Line, j, &e.Mappings[j])
				}
			}
		case KindExtensionSet:
			for _, x := range f.Extensions {
				d.extension(x)
			}
		case KindCatalog:
			d.catalog(byName[f.Vehicles])
		}
	}
	return d.out
}

// drifts collects the findings of one Drift call.
type drifts struct {
	m     Month
	month string // m's name
	f     *File
	out   []Finding
	// makes and models index vPIC's names, normalized, as needed.
	makes  map[string]int32
	models map[int32]map[string]int32
}

// add adds a finding about the data month, saying what month the file was
// built against when that is another.
func (d *drifts) add(line int, kind DriftKind, format string, a ...any) {
	detail := fmt.Sprintf(format, a...)
	if d.f.DataMonth != "" && d.f.DataMonth != d.month {
		detail += fmt.Sprintf(" (the file was built against %s)", d.f.DataMonth)
	}
	d.out = append(d.out, Finding{File: d.f.Path, Line: line, Severity: SeverityDrift, Detail: detail, Drift: kind})
}

// mapping measures mapping i of the entry on line, building a finding's
// pointer only for a finding.
func (d *drifts) mapping(line, i int, mp *Mapping) {
	month := d.month
	if mp.Make != 0 {
		if _, ok := d.m.Make(mp.Make); !ok {
			d.add(line, DriftMake, "/mappings/%d/make: vPIC make %d is not in data month %s", i, mp.Make, month)
		}
	}
	if mp.Model != 0 {
		if _, _, ok := d.m.Model(mp.Model); !ok {
			d.add(line, DriftModel, "/mappings/%d/model: vPIC model %d is not in data month %s", i, mp.Model, month)
		} else {
			d.modelYears(line, mp.Model, mp.First, mp.Last, func() string { return fmt.Sprintf("/mappings/%d", i) })
		}
	}
	d.qualifiers(mp.Qualifiers, func(ptr, detail string) {
		d.add(line, DriftQualifier, "/mappings/%d/qualifiers%s: %s", i, ptr, detail)
	})
}

// qualifiers calls drift with each way qs drift: the pointer into the
// qualifiers, and what drifted.
func (d *drifts) qualifiers(qs []Qualifier, drift func(ptr, detail string)) {
	month := d.month
	for _, q := range qs {
		lookup, ok := d.m.Element(q.Element)
		if !ok {
			drift(fmt.Sprintf("/%d", q.Element), fmt.Sprintf("element %d is not a public element of data month %s", q.Element, month))
			continue
		}
		for j, v := range q.Values {
			var detail string
			switch {
			case lookup && v.IsText:
				detail = fmt.Sprintf("element %d is a lookup element in data month %s: its values are attribute ids, not %q", q.Element, month, v.Text)
			case !lookup && !v.IsText:
				detail = fmt.Sprintf("element %d is a text element in data month %s: its values are strings, not %d", q.Element, month, v.Attribute)
			case lookup && !d.m.Attribute(q.Element, v.Attribute):
				detail = fmt.Sprintf("data month %s never decodes element %d to attribute id %d", month, q.Element, v.Attribute)
			case !lookup && !d.m.Text(q.Element, v.Text):
				detail = fmt.Sprintf("data month %s never decodes element %d to %q", month, q.Element, v.Text)
			default:
				continue
			}
			drift(fmt.Sprintf("/%d/%d", q.Element, j), detail)
		}
	}
}

// modelYears reports DriftModelYears when model years [first, last] (nil:
// unbounded) fall outside vPIC model's years, about the thing on line at
// the pointer ptr gives, built only for a finding: a mapping, or a catalog
// application whose vehicle is the model. A model with unknown years, and
// years unbounded both ways, are never flagged.
func (d *drifts) modelYears(line int, model int32, first, last *int, ptr func() string) {
	if first == nil && last == nil {
		return
	}
	years := modelYears(d.m, model)
	how := outside(years, first, last)
	if how == "" {
		return
	}
	d.add(line, DriftModelYears, "%s: model years %s fall %s outside vPIC model %d's years in data month %s: %s", ptr(), mappingYears(first, last), how, model, d.month, yearsText(years))
}

// mappingYears is [first, last] as "2001-2004", "2001-" or "-2004".
func mappingYears(first, last *int) string {
	s := "-"
	if first != nil {
		s = fmt.Sprint(*first) + s
	}
	if last != nil {
		s += fmt.Sprint(*last)
	}
	return s
}

func (d *drifts) extension(x Extension) {
	month := d.m.Name()
	switch {
	case x.IsMake():
		if id, ok := d.makeNamed(x.MakeName); ok {
			name, _ := d.m.Make(id)
			d.add(x.Line, DriftExtension, "extension %q is named like vPIC make %d (%s) in data month %s", x.ID, id, name, month)
		}
	case x.Make != 0:
		makeName, ok := d.m.Make(x.Make)
		if !ok {
			d.add(x.Line, DriftMake, "/make: vPIC make %d is not in data month %s", x.Make, month)
			return
		}
		if id, ok := d.modelNamed(x.Make, x.Name); ok {
			_, name, _ := d.m.Model(id)
			d.add(x.Line, DriftExtension, "extension %q is named like vPIC model %d (%s) under make %d (%s) in data month %s", x.ID, id, name, x.Make, makeName, month)
		}
	}
}

// makeNamed finds the vPIC make named name after Normalize, the lowest id
// when several are.
func (d *drifts) makeNamed(name string) (int32, bool) {
	if d.makes == nil {
		d.makes = index(d.m.Makes())
	}
	id, ok := d.makes[Normalize(name)]
	return id, ok
}

// modelNamed finds the vPIC model under make named name after Normalize,
// the lowest id when several are.
func (d *drifts) modelNamed(make int32, name string) (int32, bool) {
	models, ok := d.models[make]
	if !ok {
		models = index(d.m.Models(make))
		d.models[make] = models
	}
	id, ok := models[Normalize(name)]
	return id, ok
}

// index maps each normalized name to its lowest id.
func index(names iter.Seq2[int32, string]) map[string]int32 {
	m := map[string]int32{}
	for id, name := range names {
		k := Normalize(name)
		if prev, ok := m[k]; !ok || id < prev {
			m[k] = id
		}
	}
	return m
}
