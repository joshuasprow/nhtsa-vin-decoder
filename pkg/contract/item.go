package contract

import (
	"cmp"
	"encoding/json"
	"slices"
	"strconv"
)

// Item is one row the decoder emitted for a VIN: all 15 of spvindecode's
// columns, SQL NULL kept as NULL. Parity is gated on items.
type Item struct {
	GroupName   Null[string] // groupname
	Variable    string       // variable
	Value       Null[string] // value
	PatternID   Null[int32]  // itempatternid
	VinSchemaID Null[int32]  // itemvinschemaid
	Keys        Null[string] // itemkeys
	ElementID   int32        // itemelementid
	// AttributeID is itemattributeid as text: a lookup id, a code list
	// ("1,11,400") or the raw attribute.
	AttributeID Null[string]
	// CreatedOn is itemcreatedon as Postgres renders a timestamp
	// ("2015-04-21 11:37:26.607").
	CreatedOn Null[string]
	WmiID     Null[int32]  // itemwmiid
	Code      Null[string] // code, the element code
	DataType  Null[string] // datatype
	Decode    Null[string] // decode
	Source    Null[string] // itemsource, verbatim
	ToBeQCed  Null[bool]   // itemtobeqced
}

// CompareItems is item order, total and independent of the procedure's:
// element_id, then value, then the other fields in column order. Strings
// compare bytewise, integers numerically, false before true, NULL before
// any value.
func CompareItems(a, b Item) int {
	if c := cmp.Compare(a.ElementID, b.ElementID); c != 0 {
		return c
	}
	if c := cmpNull(a.Value, b.Value); c != 0 {
		return c
	}
	if c := cmpNull(a.GroupName, b.GroupName); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Variable, b.Variable); c != 0 {
		return c
	}
	if c := cmpNull(a.PatternID, b.PatternID); c != 0 {
		return c
	}
	if c := cmpNull(a.VinSchemaID, b.VinSchemaID); c != 0 {
		return c
	}
	if c := cmpNull(a.Keys, b.Keys); c != 0 {
		return c
	}
	if c := cmpNull(a.AttributeID, b.AttributeID); c != 0 {
		return c
	}
	if c := cmpNull(a.CreatedOn, b.CreatedOn); c != 0 {
		return c
	}
	if c := cmpNull(a.WmiID, b.WmiID); c != 0 {
		return c
	}
	if c := cmpNull(a.Code, b.Code); c != 0 {
		return c
	}
	if c := cmpNull(a.DataType, b.DataType); c != 0 {
		return c
	}
	if c := cmpNull(a.Decode, b.Decode); c != 0 {
		return c
	}
	if c := cmpNull(a.Source, b.Source); c != 0 {
		return c
	}
	return cmpNullBool(a.ToBeQCed, b.ToBeQCed)
}

// SortItems puts items in item order, in place.
func SortItems(items []Item) { slices.SortFunc(items, CompareItems) }

func cmpNull[T cmp.Ordered](a, b Null[T]) int {
	if a.Valid && b.Valid {
		return cmp.Compare(a.V, b.V)
	}
	return cmpValid(a.Valid, b.Valid)
}

func cmpNullBool(a, b Null[bool]) int {
	if a.Valid && b.Valid {
		return cmpValid(a.V, b.V)
	}
	return cmpValid(a.Valid, b.Valid)
}

// cmpValid orders false before true; for validity, NULL first.
func cmpValid(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

// MarshalJSON writes the item as decode.schema.json's Item, snake_case
// columns in table order, in one buffer: a full decode carries a few dozen
// items, and going through Null's marshaller would allocate per field.
func (it Item) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 512)
	b = append(b, `{"group_name":`...)
	b = appendNullString(b, it.GroupName)
	b = append(b, `,"variable":`...)
	b = appendString(b, it.Variable)
	b = append(b, `,"value":`...)
	b = appendNullString(b, it.Value)
	b = append(b, `,"pattern_id":`...)
	b = appendNullInt(b, it.PatternID)
	b = append(b, `,"vin_schema_id":`...)
	b = appendNullInt(b, it.VinSchemaID)
	b = append(b, `,"keys":`...)
	b = appendNullString(b, it.Keys)
	b = append(b, `,"element_id":`...)
	b = strconv.AppendInt(b, int64(it.ElementID), 10)
	b = append(b, `,"attribute_id":`...)
	b = appendNullString(b, it.AttributeID)
	b = append(b, `,"created_on":`...)
	b = appendNullString(b, it.CreatedOn)
	b = append(b, `,"wmi_id":`...)
	b = appendNullInt(b, it.WmiID)
	b = append(b, `,"code":`...)
	b = appendNullString(b, it.Code)
	b = append(b, `,"data_type":`...)
	b = appendNullString(b, it.DataType)
	b = append(b, `,"decode":`...)
	b = appendNullString(b, it.Decode)
	b = append(b, `,"source":`...)
	b = appendNullString(b, it.Source)
	b = append(b, `,"to_be_qced":`...)
	switch {
	case !it.ToBeQCed.Valid:
		b = append(b, "null"...)
	case it.ToBeQCed.V:
		b = append(b, "true"...)
	default:
		b = append(b, "false"...)
	}
	return append(b, '}'), nil
}

func appendNullString(b []byte, s Null[string]) []byte {
	if !s.Valid {
		return append(b, "null"...)
	}
	return appendString(b, s.V)
}

func appendNullInt(b []byte, n Null[int32]) []byte {
	if !n.Valid {
		return append(b, "null"...)
	}
	return strconv.AppendInt(b, int64(n.V), 10)
}

// appendString appends s as a JSON string exactly as encoding/json writes
// it. Printable ASCII without '"', '\\' or the HTML characters is copied as
// is; anything else, rare in vPIC's text, goes through encoding/json.
func appendString(b []byte, s string) []byte {
	for i := range len(s) {
		if c := s[i]; c < 0x20 || c >= 0x80 || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			q, _ := json.Marshal(s) // a string always marshals
			return append(b, q...)
		}
	}
	b = append(b, '"')
	b = append(b, s...)
	return append(b, '"')
}
