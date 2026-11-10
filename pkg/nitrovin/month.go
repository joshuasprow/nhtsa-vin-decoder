package nitrovin

import (
	"iter"
	"strconv"
	"sync"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// month is the vocab.Month of a loaded artifact: vPIC's makes and models
// from its lookup and make_model tables, and each element's values from
// the rows a decode takes values from. It finds a make or model by binary
// search in the tables' sorted rows, so it costs nothing to make; what it
// collects (models per make, element values) it collects on first use.
type month struct {
	a        *artifact.Artifact
	elements contract.Elements
	// ids and names are the lookup table's, whose rows sort by table, id
	// and name; makes and models are its make and model rows.
	ids                 []int32
	names               artifact.Strings
	makes, models       [2]int
	modelIDs, modelMake []int32 // make_model's, sorted by model then make
	// byMake is each make's model ids, ascending.
	byMake func() map[int32][]int32

	mu sync.Mutex
	// values holds, per element asked about, every attribute id (as text)
	// or text value the month's rows can give it.
	values map[int32]map[string]bool
}

var _ vocab.Month = (*month)(nil)

// Month is d's data month as a vocab.Month, the one NewTranslator measures
// drift against: vPIC's makes and models, and the values its decode can
// give each element. A builder matches an author's names to vPIC ids
// through it. It is one per Decoder, so what it collects on first use is
// collected once.
func (d *Decoder) Month() vocab.Month { return d.month }

func newMonth(a *artifact.Artifact, elements contract.Elements) *month {
	t := a.Table("lookup")
	tables := t.Strings("table")
	mm := a.Table("make_model")
	m := &month{a: a, elements: elements, ids: t.Int32s("id"), names: t.Strings("name"),
		modelIDs: mm.Int32s("modelid"), modelMake: mm.Int32s("makeid"), values: map[int32]map[string]bool{}}
	m.makes[0], m.makes[1] = tables.EqualRange(0, t.Rows(), "make")
	m.models[0], m.models[1] = tables.EqualRange(0, t.Rows(), "model")
	m.byMake = sync.OnceValue(m.modelsByMake)
	return m
}

// name is the name of id among the lookup rows in rows: its last row's
// that has one.
func (m *month) name(rows [2]int, id int32) (string, bool) {
	lo, hi := artifact.EqualRange(m.ids, rows[0], rows[1], id)
	for r := hi - 1; r >= lo; r-- {
		if name, ok := m.names.At(r); ok {
			return name, true
		}
	}
	return "", false
}

// makeOf is model id's make: the first nonzero make make_model gives it.
func (m *month) makeOf(id int32) int32 {
	lo, hi := artifact.EqualRange(m.modelIDs, 0, len(m.modelIDs), id)
	for r := lo; r < hi; r++ {
		if m.modelMake[r] != 0 {
			return m.modelMake[r]
		}
	}
	return 0
}

// modelsByMake lists each lookup model under its make, as makeOf links
// it, in make_model's model order.
func (m *month) modelsByMake() map[int32][]int32 {
	byMake := map[int32][]int32{}
	linked := false // the current model has its make
	for r, id := range m.modelIDs {
		if r > 0 && m.modelIDs[r-1] != id {
			linked = false
		}
		if linked {
			continue
		}
		if _, ok := m.name(m.models, id); !ok {
			continue // a model the lookup lacks
		}
		mk := m.modelMake[r]
		byMake[mk] = append(byMake[mk], id)
		linked = mk != 0
	}
	return byMake
}

func (m *month) Name() string { return m.a.Month() }

func (m *month) Make(id int32) (string, bool) { return m.name(m.makes, id) }

func (m *month) Model(id int32) (int32, string, bool) {
	name, ok := m.name(m.models, id)
	if !ok {
		return 0, "", false
	}
	return m.makeOf(id), name, true
}

func (m *month) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for r := m.makes[0]; r < m.makes[1]; r++ {
			if _, ok := m.names.At(r); !ok {
				continue
			}
			name, _ := m.Make(m.ids[r])
			if !yield(m.ids[r], name) {
				return
			}
		}
	}
}

func (m *month) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range m.byMake()[make] {
			name, _ := m.name(m.models, id)
			if !yield(id, name) {
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
