// Package spine is the PROTOTYPE spine bundle (#147): one data month's vPIC
// makes, models, public elements and the values its decode can give each
// element, as one JSON document a browser fetches once per release. Bundle
// satisfies vocab.Month, so pkg/vocab measures drift and translates in the
// browser without the artifact. Throwaway: not the shape a build ticket
// must keep.
package spine

import (
	"encoding/json"
	"iter"
	"slices"
	"strconv"
)

// Doc is the bundle as JSON.
type Doc struct {
	Month string `json:"month"`
	// Elements are [id, 1 if lookup else 0], ascending by id.
	Elements [][2]int32 `json:"elements"`
	// Makes are [id, name], ascending by id.
	Makes []MakeRow `json:"makes"`
	// Models are [id, make, name], ascending by make then id.
	Models []ModelRow `json:"models"`
	// Values are element id → every value its decode can give: attribute
	// ids as text for lookup elements, text otherwise.
	Values map[string][]string `json:"values"`
}

type MakeRow struct {
	ID   int32
	Name string
}

func (r MakeRow) MarshalJSON() ([]byte, error) { return json.Marshal([]any{r.ID, r.Name}) }
func (r *MakeRow) UnmarshalJSON(b []byte) error {
	var a []json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 2 {
		return err
	}
	if err := json.Unmarshal(a[0], &r.ID); err != nil {
		return err
	}
	return json.Unmarshal(a[1], &r.Name)
}

type ModelRow struct {
	ID, Make int32
	Name     string
}

func (r ModelRow) MarshalJSON() ([]byte, error) { return json.Marshal([]any{r.ID, r.Make, r.Name}) }
func (r *ModelRow) UnmarshalJSON(b []byte) error {
	var a []json.RawMessage
	if err := json.Unmarshal(b, &a); err != nil || len(a) != 3 {
		return err
	}
	if err := json.Unmarshal(a[0], &r.ID); err != nil {
		return err
	}
	if err := json.Unmarshal(a[1], &r.Make); err != nil {
		return err
	}
	return json.Unmarshal(a[2], &r.Name)
}

// Bundle is a parsed Doc, indexed: a vocab.Month.
type Bundle struct {
	doc      Doc
	makes    map[int32]string
	models   map[int32]ModelRow
	byMake   map[int32][]int32
	elements map[int32]bool
	values   map[int32]map[string]bool
}

// Parse reads a bundle.
func Parse(data []byte) (*Bundle, error) {
	b := &Bundle{makes: map[int32]string{}, models: map[int32]ModelRow{}, byMake: map[int32][]int32{}, elements: map[int32]bool{}, values: map[int32]map[string]bool{}}
	if err := json.Unmarshal(data, &b.doc); err != nil {
		return nil, err
	}
	for _, m := range b.doc.Makes {
		b.makes[m.ID] = m.Name
	}
	for _, m := range b.doc.Models {
		b.models[m.ID] = m
		b.byMake[m.Make] = append(b.byMake[m.Make], m.ID)
	}
	for _, e := range b.doc.Elements {
		b.elements[e[0]] = e[1] == 1
	}
	for k, vs := range b.doc.Values {
		id, err := strconv.Atoi(k)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(vs))
		for _, v := range vs {
			set[v] = true
		}
		b.values[int32(id)] = set
	}
	return b, nil
}

func (b *Bundle) Name() string { return b.doc.Month }

func (b *Bundle) Make(id int32) (string, bool) {
	n, ok := b.makes[id]
	return n, ok
}

func (b *Bundle) Model(id int32) (int32, string, bool) {
	m, ok := b.models[id]
	return m.Make, m.Name, ok
}

func (b *Bundle) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, m := range b.doc.Makes {
			if !yield(m.ID, m.Name) {
				return
			}
		}
	}
}

func (b *Bundle) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range b.byMake[make] {
			if !yield(id, b.models[id].Name) {
				return
			}
		}
	}
}

func (b *Bundle) Element(id int32) (lookup, ok bool) {
	lookup, ok = b.elements[id]
	return lookup, ok
}

func (b *Bundle) Attribute(element, id int32) bool {
	return b.values[element][strconv.Itoa(int(id))]
}

func (b *Bundle) Text(element int32, text string) bool { return b.values[element][text] }

// Sorted sorts d's rows into the documented order.
func (d *Doc) Sorted() {
	slices.SortFunc(d.Makes, func(a, b MakeRow) int { return int(a.ID - b.ID) })
	slices.SortFunc(d.Models, func(a, b ModelRow) int {
		if a.Make != b.Make {
			return int(a.Make - b.Make)
		}
		return int(a.ID - b.ID)
	})
	for _, vs := range d.Values {
		slices.Sort(vs)
	}
}
