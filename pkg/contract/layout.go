package contract

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Attributes is a decode's attributes in one of the two layouts:
// VpicAttributes or NitrovinAttributes.
type Attributes interface {
	// Shape is the layout these attributes are in.
	Shape() Shape
}

// VpicAttributes is the vpic layout, vPIC's DecodeVinValues: one key per
// public element, named by its element code and "" when undecoded, plus
// MakeID, ManufacturerId and ModelID.
type VpicAttributes map[string]string

// Shape is ShapeVPIC.
func (VpicAttributes) Shape() Shape { return ShapeVPIC }

// NitrovinAttributes is the nitrovin layout: decoded elements only, keyed
// by variable name.
type NitrovinAttributes map[string]Attribute

// Shape is ShapeNitrovin.
func (NitrovinAttributes) Shape() Shape { return ShapeNitrovin }

// Attribute is one decoded element in the nitrovin layout. A multi-valued
// element carries Values, never Value, however many it has; every other
// element carries Value.
type Attribute struct {
	ElementID int32
	// Value is a single-valued element's value.
	Value string
	// AttributeID is a single-valued lookup element's attribute id, when it
	// is an integer; never on Error Code (143), whose id is the code list.
	AttributeID Null[int32]
	// Values is non-nil exactly for a multi-valued element: every value,
	// duplicates kept, in byte order.
	Values []string
}

// MarshalJSON writes a SingleAttribute or a MultiAttribute of
// decode.schema.json.
func (a Attribute) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 64)
	b = append(b, `{"element_id":`...)
	b = strconv.AppendInt(b, int64(a.ElementID), 10)
	if a.Values != nil {
		b = append(b, `,"values":[`...)
		for i, v := range a.Values {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendString(b, v)
		}
		return append(b, "]}"...), nil
	}
	b = append(b, `,"value":`...)
	b = appendString(b, a.Value)
	if a.AttributeID.Valid {
		b = append(b, `,"attribute_id":`...)
		b = strconv.AppendInt(b, int64(a.AttributeID.V), 10)
	}
	return append(b, '}'), nil
}

// Element ids the layouts single out.
const (
	elementMake         = 26
	elementManufacturer = 27
	elementModel        = 28
	elementModelYear    = 29
	elementSuggestedVIN = 142
	elementErrorCode    = 143
)

// Layout lays items out in shape (empty means ShapeVPIC). It is a pure
// function of the items and the data month's public elements: the items'
// order does not matter, and items is not modified.
func Layout(shape Shape, items []Item, elements Elements) (Attributes, error) {
	switch shape {
	case "", ShapeVPIC:
		return Vpic(items, elements)
	case ShapeNitrovin:
		return Nitrovin(items, elements)
	default:
		return nil, fmt.Errorf("unknown shape %q", shape)
	}
}

// Vpic lays items out as vPIC's DecodeVinValues: every public element's
// code, "" when no item has a value for it; a multi-valued element's
// distinct values joined with ", " in byte order; MakeID, ManufacturerId
// and ModelID from the attribute ids of Make, Manufacturer Name and Model.
// NULL and "" both read as "", as vPIC flattens them.
func Vpic(items []Item, elements Elements) (VpicAttributes, error) {
	if err := checkItems(items, elements); err != nil {
		return nil, err
	}
	attrs := make(VpicAttributes, len(elements)+3)
	for _, e := range elements {
		attrs[e.Code] = ""
	}
	attrs["MakeID"], attrs["ManufacturerId"], attrs["ModelID"] = "", "", ""
	var multi map[int32][]string
	for _, it := range items {
		e, _ := elements.Find(it.ElementID)
		if e.MultiValued {
			if it.Value.Valid {
				if multi == nil {
					multi = make(map[int32][]string)
				}
				multi[e.ElementID] = append(multi[e.ElementID], it.Value.V)
			}
			continue
		}
		attrs[e.Code] = it.Value.V
		switch e.ElementID {
		case elementMake:
			attrs["MakeID"] = it.AttributeID.V
		case elementManufacturer:
			attrs["ManufacturerId"] = it.AttributeID.V
		case elementModel:
			attrs["ModelID"] = it.AttributeID.V
		}
	}
	for id, vs := range multi {
		e, _ := elements.Find(id)
		slices.Sort(vs)
		attrs[e.Code] = strings.Join(slices.Compact(vs), ", ")
	}
	return attrs, nil
}

// Nitrovin lays items out keyed by variable name: one Attribute per element
// with a non-NULL value. An item whose value is NULL is not decoded, so
// NULL is absent and "" is present.
func Nitrovin(items []Item, elements Elements) (NitrovinAttributes, error) {
	if err := checkItems(items, elements); err != nil {
		return nil, err
	}
	attrs := make(NitrovinAttributes, len(items))
	for _, it := range items {
		if !it.Value.Valid {
			continue
		}
		e, _ := elements.Find(it.ElementID)
		if e.MultiValued {
			a := attrs[e.Variable]
			a.ElementID = e.ElementID
			a.Values = append(a.Values, it.Value.V)
			attrs[e.Variable] = a
			continue
		}
		a := Attribute{ElementID: e.ElementID, Value: it.Value.V}
		if e.DataType == "lookup" && e.ElementID != elementErrorCode && it.AttributeID.Valid {
			if id, err := strconv.ParseInt(it.AttributeID.V, 10, 32); err == nil {
				a.AttributeID = Value(int32(id))
			}
		}
		attrs[e.Variable] = a
	}
	for _, a := range attrs {
		slices.Sort(a.Values) // shares the map value's backing array
	}
	return attrs, nil
}

// checkItems checks what the layouts rely on: every item's element is a
// public element of the data month, and only multi-valued elements repeat.
// A failure is a fault in the decoder or a mismatch between it and the
// element table, never a property of the VIN.
func checkItems(items []Item, elements Elements) error {
	seen := make([]bool, len(elements))
	for _, it := range items {
		i := elements.Index(it.ElementID)
		if i < 0 {
			return fmt.Errorf("item for element %d, which is not a public element of the data month", it.ElementID)
		}
		if e := elements[i]; !e.MultiValued {
			if seen[i] {
				return fmt.Errorf("element %d (%s) has several items, but it is not multi-valued", e.ElementID, e.Variable)
			}
			seen[i] = true
		}
	}
	return nil
}
