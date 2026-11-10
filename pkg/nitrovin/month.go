package nitrovin

import (
	"iter"
	"slices"
	"strconv"
	"sync"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// month is the vocab.Month of a loaded artifact: vPIC's makes and models
// from its lookup and make_model tables, and each element's values from
// the rows a decode takes values from.
type month struct {
	a        *artifact.Artifact
	elements contract.Elements
	makes    map[int32]string
	makeIDs  []int32 // ascending
	models   map[int32]model
	byMake   map[int32][]int32 // make → its model ids, ascending

	mu sync.Mutex
	// values holds, per element asked about, every attribute id (as text)
	// or text value the month's rows can give it.
	values map[int32]map[string]bool
}

type model struct {
	make int32
	name string
}

var _ vocab.Month = (*month)(nil)

// Month is d's data month as a vocab.Month, the one NewTranslator measures
// drift against: vPIC's makes and models, and the values its decode can
// give each element. A builder matches an author's names to vPIC ids
// through it.
func (d *Decoder) Month() vocab.Month { return newMonth(d.artifact, d.info.Elements) }

func newMonth(a *artifact.Artifact, elements contract.Elements) *month {
	m := &month{a: a, elements: elements, makes: map[int32]string{}, models: map[int32]model{}, byMake: map[int32][]int32{}, values: map[int32]map[string]bool{}}
	t := a.Table("lookup")
	tables, ids, names := t.Strings("table"), t.Int32s("id"), t.Strings("name")
	lo, hi := tables.EqualRange(0, t.Rows(), "make")
	for r := lo; r < hi; r++ {
		if name, ok := names.At(r); ok {
			m.makes[ids[r]] = name
			m.makeIDs = append(m.makeIDs, ids[r])
		}
	}
	lo, hi = tables.EqualRange(0, t.Rows(), "model")
	for r := lo; r < hi; r++ {
		if name, ok := names.At(r); ok {
			m.models[ids[r]] = model{name: name}
		}
	}
	mm := a.Table("make_model")
	modelIDs, makeIDs := mm.Int32s("modelid"), mm.Int32s("makeid")
	for r, id := range modelIDs {
		md, ok := m.models[id]
		if !ok || md.make != 0 {
			continue // a model the lookup lacks, or its make already linked
		}
		md.make = makeIDs[r]
		m.models[id] = md
		m.byMake[md.make] = append(m.byMake[md.make], id) // ascending: rows sort by modelid
	}
	slices.Sort(m.makeIDs)
	return m
}

func (m *month) Name() string { return m.a.Month() }

func (m *month) Make(id int32) (string, bool) {
	name, ok := m.makes[id]
	return name, ok
}

func (m *month) Model(id int32) (int32, string, bool) {
	md, ok := m.models[id]
	return md.make, md.name, ok
}

func (m *month) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range m.makeIDs {
			if !yield(id, m.makes[id]) {
				return
			}
		}
	}
}

func (m *month) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range m.byMake[make] {
			if !yield(id, m.models[id].name) {
				return
			}
		}
	}
}

func (m *month) Element(id int32) (lookup, ok bool) {
	e, ok := m.elements.Find(id)
	return ok && e.DataType == "lookup", ok
}

func (m *month) Attribute(element, id int32) bool {
	return m.valuesOf(element)[strconv.Itoa(int(id))]
}

func (m *month) Text(element int32, text string) bool {
	return m.valuesOf(element)[text]
}

// valuesOf collects, once per element, the values the month's decode can
// give it: the attribute column of its pattern, engine-model pattern,
// vehicle-spec pattern and default-value rows, and for Make (26),
// Manufacturer Name (27) and Vehicle Type (39) the ids the WMI and
// make_model tables give them. Values a formula pattern computes from the
// VIN are not among them.
func (m *month) valuesOf(element int32) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if vs, ok := m.values[element]; ok {
		return vs
	}
	vs := map[string]bool{}
	for _, src := range []struct{ table, column string }{
		{"pattern", "attributeid"},
		{"enginemodelpattern", "attributeid"},
		{"vehiclespecpattern", "attributeid"},
		{"defaultvalue", "defaultvalue"},
	} {
		t := m.a.Table(src.table)
		values := t.Strings(src.column)
		for r, el := range t.Int32s("elementid") {
			if el != element {
				continue
			}
			if v, ok := values.At(r); ok {
				vs[v] = true
			}
		}
	}
	ids := func(table, column string) {
		for _, id := range m.a.Table(table).Int32s(column) {
			if id != artifact.NullInt32 {
				vs[strconv.Itoa(int(id))] = true
			}
		}
	}
	switch element {
	case 26:
		ids("wmi", "makeid")
		ids("wmi_make", "makeid")
		ids("make_model", "makeid")
	case 27:
		ids("wmi", "manufacturerid")
	case 39:
		ids("wmi", "vehicletypeid")
	}
	m.values[element] = vs
	return vs
}
