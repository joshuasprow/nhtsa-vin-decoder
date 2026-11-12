package vocab

import (
	"fmt"
	"iter"
	"strconv"
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
// answers. A compiled catalog is not measured again in full: its findings
// are those its compiling stored, against its Compiled DriftMonth
// (ADR 0021), but for its vehicles' against the loaded vehicle vocabulary,
// measured again; Expand it to measure all of it. Findings come in file
// and line order. Drift never refuses: the files are served as they are.
// It is what load measures; ModelDrift is the rest.
func Drift(m Month, files []*File) []Finding {
	return newDrifts(m, nil).walk(files)
}

// ModelDrift measures linked files' qualifiers on mappings, and on a
// catalog's applications, whose vehicle is a vPIC model against that
// model's model values in m (ADR 0015): each value m decodes, so Drift
// finds nothing wrong with it, that the model never decodes to is a
// DriftModelQualifier finding. Elements m refuses model values for (Model
// Year, the Corrections elements) and make or extension targets are not
// tested, nor are years. Findings come in file and line order. It is
// apart from Drift because the model values index it builds costs about
// 0.15 s and 100 MB: `nitrovin vocab check` runs it, /v1/info on first
// ask, and load never does.
func ModelDrift(m ModelMonth, files []*File) []Finding {
	return newDrifts(m, m).walk(files)
}

func newDrifts(m Month, mm ModelMonth) *drifts {
	return &drifts{m: m, mm: mm, month: m.Name(), models: map[int32]map[string]int32{}}
}

func (d *drifts) walk(files []*File) []Finding {
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
			if c := f.compiled; c != nil {
				d.compiled(c, byName[f.Vehicles])
				continue
			}
			d.catalog(byName[f.Vehicles])
		}
	}
	return d.out
}

// drifts collects the findings of one Drift or ModelDrift call.
type drifts struct {
	m Month
	// mm is m for ModelDrift, which keeps only DriftModelQualifier
	// findings; nil for Drift, which keeps every other kind and tests no
	// model values.
	mm    ModelMonth
	month string // m's name
	f     *File
	out   []Finding
	// makes and models index vPIC's names, normalized, as needed.
	makes  map[string]int32
	models map[int32]map[string]int32
	// modelSets caches ModelValues per (model, element), nil where the
	// month refuses the element.
	modelSets map[[2]int32]map[string]bool
}

// keeps reports whether this call reports findings of kind.
func (d *drifts) keeps(kind DriftKind) bool {
	return (d.mm != nil) == (kind == DriftModelQualifier)
}

// add adds a finding about the data month, saying what month the file was
// built against when that is another.
func (d *drifts) add(line int, kind DriftKind, format string, a ...any) {
	if !d.keeps(kind) {
		return
	}
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
	var model int32 // the mapping's vPIC model, when the month has it
	if mp.Make != 0 {
		if _, ok := d.m.Make(mp.Make); !ok {
			d.add(line, DriftMake, "/mappings/%d/make: vPIC make %d is not in data month %s", i, mp.Make, month)
		}
	}
	if mp.Model != 0 {
		if _, _, ok := d.m.Model(mp.Model); !ok {
			d.add(line, DriftModel, "/mappings/%d/model: vPIC model %d is not in data month %s", i, mp.Model, month)
		} else {
			d.modelYears(line, mp.Model, mp.First, mp.Last, func() string { return fmt.Sprintf("/mappings/%d", i) }, "")
			model = mp.Model
		}
	}
	d.qualifiers(mp.Qualifiers, model, func(kind DriftKind, ptr, detail string) {
		d.add(line, kind, "/mappings/%d/qualifiers%s: %s", i, ptr, detail)
	})
}

// qualifiers calls drift with each way qs drift: the kind, the pointer
// into the qualifiers, and what drifted. model, when not 0, is the vPIC
// model qs qualify, whose model values a value the month decodes is then
// tested against (ModelDrift only).
func (d *drifts) qualifiers(qs []Qualifier, model int32, drift func(kind DriftKind, ptr, detail string)) {
	month := d.month
	for _, q := range qs {
		lookup, ok := d.m.Element(q.Element)
		if !ok {
			drift(DriftQualifier, fmt.Sprintf("/%d", q.Element), fmt.Sprintf("element %d is not a public element of data month %s", q.Element, month))
			continue
		}
		for j, v := range q.Values {
			kind, detail := DriftQualifier, ""
			switch {
			case lookup && v.IsText:
				detail = fmt.Sprintf("element %d is a lookup element in data month %s: its values are attribute ids, not %q", q.Element, month, v.Text)
			case !lookup && !v.IsText:
				detail = fmt.Sprintf("element %d is a text element in data month %s: its values are strings, not %d", q.Element, month, v.Attribute)
			case lookup && !d.m.Attribute(q.Element, v.Attribute):
				detail = fmt.Sprintf("data month %s never decodes element %d to attribute id %d", month, q.Element, v.Attribute)
			case !lookup && !d.m.Text(q.Element, v.Text):
				detail = fmt.Sprintf("data month %s never decodes element %d to %q", month, q.Element, v.Text)
			case model != 0 && d.mm != nil:
				if detail = d.modelQualifier(model, q.Element, v); detail == "" {
					continue
				}
				kind = DriftModelQualifier
			default:
				continue
			}
			drift(kind, fmt.Sprintf("/%d/%d", q.Element, j), detail)
		}
	}
}

// modelQualifier says how value v of element, one the month decodes, is
// not among vPIC model's model values for element (ADR 0015), or "" when
// it is. Nothing is flagged for an element the month refuses model values
// for (Model Year and the Corrections elements, decoded from the VIN
// itself). Years are not tested.
func (d *drifts) modelQualifier(model, element int32, v Value) string {
	values, ok := d.modelValues(model, element)
	switch {
	case !ok:
		return ""
	case v.IsText && !values[v.Text]:
		return fmt.Sprintf("vPIC model %d never decodes element %d to %q in data month %s", model, element, v.Text, d.month)
	case !v.IsText && !values[strconv.FormatInt(int64(v.Attribute), 10)]:
		return fmt.Sprintf("vPIC model %d never decodes element %d to attribute id %d in data month %s", model, element, v.Attribute, d.month)
	}
	return ""
}

// modelValues is model's model values for element as a set, cached for
// the ModelDrift call; ok is false when the month refuses the element.
func (d *drifts) modelValues(model, element int32) (map[string]bool, bool) {
	k := [2]int32{model, element}
	if s, ok := d.modelSets[k]; ok {
		return s, s != nil
	}
	values, ok := d.mm.ModelValues(model, element)
	var s map[string]bool
	if ok {
		s = make(map[string]bool, len(values))
		for _, v := range values {
			s[v] = true
		}
	}
	if d.modelSets == nil {
		d.modelSets = map[[2]int32]map[string]bool{}
	}
	d.modelSets[k] = s
	return s, ok
}

// modelYears reports DriftModelYears when model years [first, last] (nil:
// unbounded) fall outside vPIC model's years, about the thing on line at
// the pointer ptr gives, built only for a finding: a mapping, or a catalog
// application whose vehicle is the model. more ends the detail: a
// catalog's "(and N more)", or "". A model with unknown years, and years
// unbounded both ways, are never flagged.
func (d *drifts) modelYears(line int, model int32, first, last *int, ptr func() string, more string) {
	if first == nil && last == nil {
		return
	}
	years := modelYears(d.m, model)
	how := outside(years, first, last)
	if how == "" {
		return
	}
	d.add(line, DriftModelYears, "%s: model years %s fall %s outside vPIC model %d's years in data month %s: %s%s", ptr(), mappingYears(first, last), how, model, d.month, yearsText(years), more)
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
