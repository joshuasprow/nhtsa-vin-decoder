package vocab

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"slices"
	"strconv"
)

// A spine bundle is a data month as one JSON document: everything a Month
// answers, so that a translation runs where the artifact is not, in a
// browser's Service Worker (ADR 0013). `nitrovin serve` publishes the
// embedded month's at /ui/r/<release>/spine.json. Its JSON is read only by
// the release that wrote it, whose URL names it, so it may change in any
// release; its format field guards against a mix-up.

// BundleFormat is the spine bundle's format, its format field.
const BundleFormat = 1

// BundleSource is a Month a spine bundle is written from: it also lists
// what its questions range over.
type BundleSource interface {
	Month
	// ModelIDs yields every vPIC model id Model knows, those under no make
	// and those Models lists under none included.
	ModelIDs() iter.Seq[int32]
	// Elements yields the id of every element Element knows.
	Elements() iter.Seq[int32]
	// Values yields every element and value Attribute or Text holds,
	// lookup elements' attribute ids in decimal, each pair at least once:
	// Attribute(e, id) is whether it yields e and id in decimal, and
	// Text(e, v) whether it yields e and v.
	Values() iter.Seq2[int32, string]
}

// bundle is a spine bundle's JSON, columns rather than rows: each table's
// columns hold one value per row.
type bundle struct {
	Format int    `json:"format"`
	Month  string `json:"month"`
	// Makes are Makes' rows, in its order.
	Makes struct {
		IDs   []int32  `json:"ids"`
		Names []string `json:"names"`
	} `json:"makes"`
	// Models are Models' rows, by make ascending, each make's in its order.
	// A model's make (Model) is the one nonzero make it is listed under,
	// else 0.
	Models struct {
		IDs   []int32  `json:"ids"`
		Makes []int32  `json:"makes"`
		Names []string `json:"names"`
	} `json:"models"`
	// Unlisted are the models Models lists under no make, ascending: their
	// make is 0.
	Unlisted struct {
		IDs   []int32  `json:"ids"`
		Names []string `json:"names"`
	} `json:"unlisted"`
	// Elements are every element id, ascending, and whether each is a
	// lookup element.
	Elements struct {
		IDs    []int32 `json:"ids"`
		Lookup []bool  `json:"lookup"`
	} `json:"elements"`
	// Values are each element's values (by element id in decimal),
	// ascending.
	Values map[string][]string `json:"values"`
}

// WriteBundle writes m's spine bundle to w.
func WriteBundle(w io.Writer, m BundleSource) error {
	b := bundle{Format: BundleFormat, Month: m.Name(), Values: map[string][]string{}}
	for id, name := range m.Makes() {
		b.Makes.IDs = append(b.Makes.IDs, id)
		b.Makes.Names = append(b.Makes.Names, name)
	}
	// Every make a model is listed under is its make (Model), or 0.
	models := map[int32]string{}
	makes := map[int32]bool{0: true}
	for id := range m.ModelIDs() {
		mk, name, ok := m.Model(id)
		if !ok {
			return fmt.Errorf("spine bundle: ModelIDs yields model %d, which Model does not know", id)
		}
		models[id] = name
		makes[mk] = true
	}
	listed := map[int32]bool{}
	for _, mk := range slices.Sorted(maps.Keys(makes)) {
		for id, name := range m.Models(mk) {
			b.Models.IDs = append(b.Models.IDs, id)
			b.Models.Makes = append(b.Models.Makes, mk)
			b.Models.Names = append(b.Models.Names, name)
			listed[id] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(models)) {
		if !listed[id] {
			b.Unlisted.IDs = append(b.Unlisted.IDs, id)
			b.Unlisted.Names = append(b.Unlisted.Names, models[id])
		}
	}
	for _, id := range slices.Sorted(m.Elements()) {
		lookup, _ := m.Element(id)
		b.Elements.IDs = append(b.Elements.IDs, id)
		b.Elements.Lookup = append(b.Elements.Lookup, lookup)
	}
	values := map[int32]map[string]bool{}
	for el, v := range m.Values() {
		if values[el] == nil {
			values[el] = map[string]bool{}
		}
		values[el][v] = true
	}
	for el, vs := range values {
		b.Values[strconv.Itoa(int(el))] = slices.Sorted(maps.Keys(vs))
	}
	return json.NewEncoder(w).Encode(&b)
}

// Bundle is a spine bundle read back: a Month answering exactly as the
// one it was written from.
type Bundle struct {
	month string
	makes struct {
		ids   []int32
		names []string
		byID  map[int32]string
	}
	// models is every model's make and name; byMake is Models' lists.
	models map[int32]bundleModel
	byMake map[int32][]int32
	// elements is whether each element is a lookup element.
	elements map[int32]bool
	values   map[int32]map[string]bool
}

type bundleModel struct {
	make int32
	name string
}

var _ BundleSource = (*Bundle)(nil)

// ReadBundle reads a spine bundle WriteBundle wrote, refusing one of
// another format.
func ReadBundle(r io.Reader) (*Bundle, error) {
	var b bundle
	if err := json.NewDecoder(r).Decode(&b); err != nil {
		return nil, fmt.Errorf("spine bundle: %w", err)
	}
	if b.Format != BundleFormat {
		return nil, fmt.Errorf("spine bundle: format %d; this release reads format %d", b.Format, BundleFormat)
	}
	columns := func(table string, lens ...int) error {
		if slices.Min(lens) != slices.Max(lens) {
			return fmt.Errorf("spine bundle: the %s columns hold %v values", table, lens)
		}
		return nil
	}
	if err := cmp.Or(
		columns("makes", len(b.Makes.IDs), len(b.Makes.Names)),
		columns("models", len(b.Models.IDs), len(b.Models.Makes), len(b.Models.Names)),
		columns("unlisted", len(b.Unlisted.IDs), len(b.Unlisted.Names)),
		columns("elements", len(b.Elements.IDs), len(b.Elements.Lookup)),
	); err != nil {
		return nil, err
	}
	m := &Bundle{
		month:    b.Month,
		models:   make(map[int32]bundleModel, len(b.Models.IDs)+len(b.Unlisted.IDs)),
		byMake:   map[int32][]int32{},
		elements: make(map[int32]bool, len(b.Elements.IDs)),
		values:   make(map[int32]map[string]bool, len(b.Values)),
	}
	m.makes.ids, m.makes.names = b.Makes.IDs, b.Makes.Names
	m.makes.byID = make(map[int32]string, len(b.Makes.IDs))
	for i, id := range b.Makes.IDs {
		m.makes.byID[id] = b.Makes.Names[i]
	}
	for i, id := range b.Models.IDs {
		mk := b.Models.Makes[i]
		m.byMake[mk] = append(m.byMake[mk], id)
		if prev, ok := m.models[id]; !ok || prev.make == 0 {
			m.models[id] = bundleModel{mk, b.Models.Names[i]}
		}
	}
	for i, id := range b.Unlisted.IDs {
		m.models[id] = bundleModel{0, b.Unlisted.Names[i]}
	}
	for i, id := range b.Elements.IDs {
		m.elements[id] = b.Elements.Lookup[i]
	}
	for key, vs := range b.Values {
		el, err := strconv.ParseInt(key, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("spine bundle: values of %q: not an element id", key)
		}
		set := make(map[string]bool, len(vs))
		for _, v := range vs {
			set[v] = true
		}
		m.values[int32(el)] = set
	}
	return m, nil
}

func (m *Bundle) Name() string { return m.month }

func (m *Bundle) Make(id int32) (string, bool) {
	name, ok := m.makes.byID[id]
	return name, ok
}

func (m *Bundle) Model(id int32) (int32, string, bool) {
	mo, ok := m.models[id]
	return mo.make, mo.name, ok
}

func (m *Bundle) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for i, id := range m.makes.ids {
			if !yield(id, m.makes.names[i]) {
				return
			}
		}
	}
}

func (m *Bundle) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range m.byMake[make] {
			if !yield(id, m.models[id].name) {
				return
			}
		}
	}
}

func (m *Bundle) Element(id int32) (lookup, ok bool) {
	lookup, ok = m.elements[id]
	return lookup, ok
}

func (m *Bundle) Attribute(element, id int32) bool {
	return m.values[element][strconv.Itoa(int(id))]
}

func (m *Bundle) Text(element int32, text string) bool { return m.values[element][text] }

func (m *Bundle) ModelIDs() iter.Seq[int32] { return maps.Keys(m.models) }

func (m *Bundle) Elements() iter.Seq[int32] { return maps.Keys(m.elements) }

func (m *Bundle) Values() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for el, vs := range m.values {
			for v := range vs {
				if !yield(el, v) {
					return
				}
			}
		}
	}
}
