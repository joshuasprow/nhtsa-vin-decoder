package contract

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// List limits from docs/api/README.md.
const (
	// DefaultListLimit is a list page's size when the request gives none.
	DefaultListLimit = 100
	// MaxListLimit caps a list page.
	MaxListLimit = 1000
)

// ListRequest is one list request, the same on every surface (ADR 0012).
// Without Of it asks for the catalog of what is loaded; with Of it pages
// through vPIC ("vpic") or one loaded file.
type ListRequest struct {
	Of string
	// Where filters by fields, matched after the fixed normalization: a
	// vocabulary's fields, or vPIC's "make", a make id, to list its models.
	Where map[string]string
	// Limit is the page size, 1 to MaxListLimit; 0 means DefaultListLimit.
	Limit int
	// After is the previous page's Next; "" starts at the first entry.
	After string
}

// listFields are ListRequest's properties.
var listFields = []string{"of", "where", "limit", "after"}

// ParseListRequest parses one list request object: a ListRequest of
// decode.schema.json. Unknown fields are refused; what the loaded files
// decide (of, where's fields, after) is the Translator's to refuse. The
// error is a *Problem.
func ParseListRequest(data []byte) (ListRequest, error) {
	if !json.Valid(data) {
		return ListRequest{}, NewProblem(MalformedJSON, "", "the request is not JSON")
	}
	fields, p := objectFields(data, "", "a request", listFields)
	if p != nil {
		return ListRequest{}, p
	}
	r, p := readList(fields)
	if p != nil {
		return ListRequest{}, p
	}
	return r, nil
}

// readList reads a list request's fields, unknown ones already refused.
func readList(fields map[string]json.RawMessage) (ListRequest, *Problem) {
	var r ListRequest
	for _, k := range []string{"of", "after"} {
		raw, ok := fields[k]
		if !ok {
			continue
		}
		var s string
		if !jsonString(raw, &s) || s == "" {
			return ListRequest{}, NewProblem(InvalidRequest, "/"+k, k+" must be a non-empty string")
		}
		if k == "of" {
			r.Of = s
		} else {
			r.After = s
		}
	}
	if raw, ok := fields["where"]; ok {
		values, p := objectFields(raw, "/where", "where", nil)
		if p != nil {
			return ListRequest{}, p
		}
		r.Where = make(map[string]string, len(values))
		for _, k := range slices.Sorted(maps.Keys(values)) {
			var s string
			if id, ok := jsonInt32(values[k]); ok {
				s = strconv.Itoa(int(id))
			} else if !jsonString(values[k], &s) {
				return ListRequest{}, NewProblem(InvalidRequest, pointerTo("/where", k), "a where value is a string, or an integer id")
			}
			r.Where[k] = s
		}
	}
	if raw, ok := fields["limit"]; ok {
		n, ok := jsonInt32(raw)
		if !ok || n < 1 || n > MaxListLimit {
			return ListRequest{}, NewProblem(InvalidRequest, "/limit", fmt.Sprintf("limit must be an integer from 1 to %d", MaxListLimit))
		}
		r.Limit = int(n)
	}
	return r, nil
}

// ParseListQuery parses GET /v1/vocabularies, the catalog, with of "", and
// GET /v1/vocabularies/{name}/entries, with of the path's name: one
// where.<field> per field matched, limit and after, each at most once; an
// unknown parameter is refused. As in ParseTranslateQuery, the parameters
// are read as the JSON request they spell, and a problem's pointer is the
// parameter's path ("/where/make"). The error is a *Problem.
func ParseListQuery(of string, query url.Values) (ListRequest, error) {
	req := map[string]json.RawMessage{}
	if of != "" {
		req["of"] = queryString(of)
	}
	where := map[string]json.RawMessage{}
	for _, name := range slices.Sorted(maps.Keys(query)) {
		vs := query[name]
		ptr := queryPointer(name, "where.")
		if len(vs) != 1 {
			return ListRequest{}, NewProblem(InvalidRequest, ptr, name+" is given more than once")
		}
		v := vs[0]
		switch name {
		case "limit":
			req[name] = queryNumber(v)
		case "after":
			req[name] = queryString(v)
		default:
			field, ok := strings.CutPrefix(name, "where.")
			if !ok {
				return ListRequest{}, NewProblem(InvalidRequest, ptr, "unknown query parameter "+strconv.Quote(name))
			}
			where[field] = queryString(v)
		}
	}
	if len(where) > 0 {
		req["where"] = marshalRaw(where)
	}
	return ParseListRequest(marshalRaw(req))
}

// List is the answer to a ListRequest: the catalog, or one page of what Of
// names.
type List struct {
	// Of is the request's; absent for the catalog.
	Of string `json:"of,omitempty"`
	// Entries are CatalogEntry values for the catalog, VocabularyEntry
	// values for a vocabulary, and SpineEntry values for vPIC, a declared
	// spine or an extension set.
	Entries []ListEntry `json:"entries"`
	// Next is the After of the next page; absent on the last.
	Next string `json:"next,omitempty"`
	// Vocabularies are the files listed: every loaded file for the
	// catalog, the one Of names, none for vPIC.
	Vocabularies   []FileVersion `json:"vocabularies"`
	DataMonth      string        `json:"data_month"`
	DecoderVersion string        `json:"decoder_version"`
}

// ListEntry is one entry of a List: a CatalogEntry, a VocabularyEntry or a
// SpineEntry.
type ListEntry interface {
	listEntry()
}

func (CatalogEntry) listEntry()    {}
func (VocabularyEntry) listEntry() {}
func (SpineEntry) listEntry()      {}

// CatalogEntry is one thing a request can name: vPIC, or a loaded
// vocabulary, extension set or declared spine.
type CatalogEntry struct {
	Name string `json:"name"`
	// Kind is "vocabulary", "extension-set" or "spine"; vPIC is a spine.
	Kind string `json:"kind"`
	// Version is a file's version; vPIC's is the data month.
	Version string `json:"version"`
	// Subject is "vehicle" or "part": what its entries name.
	Subject string `json:"subject"`
	// Fields are a vocabulary's, the fields that name an entry, in order.
	Fields []string `json:"fields,omitempty"`
	// Spine is the spine a vocabulary or extension set routes through:
	// "vpic" or a declared spine's name. Absent for a spine.
	Spine string `json:"spine,omitempty"`
}

// VocabularyEntry is one entry of a vocabulary as its file has it: its
// fields by name, every mapping, none for an unmapped entry, and its note.
type VocabularyEntry struct {
	Entry    map[string]string   `json:"entry"`
	Mappings []VocabularyMapping `json:"mappings"`
	Note     string              `json:"note,omitempty"`
}

// VocabularyMapping is one mapping as its file has it: vocabulary.schema.json's
// Mapping.
type VocabularyMapping struct {
	Make  int32       `json:"make,omitempty"`
	Model int32       `json:"model,omitempty"`
	Ext   string      `json:"ext,omitempty"`
	ID    string      `json:"id,omitempty"`
	First Null[int32] `json:"first,omitzero"`
	Last  Null[int32] `json:"last,omitzero"`
	// Qualifiers are element id (as a decimal string) → values.
	Qualifiers map[string][]QualifierValue `json:"qualifiers,omitempty"`
	Origin     string                      `json:"origin,omitempty"`
	Note       string                      `json:"note,omitempty"`
}

// QualifierValue is one value a qualifier holds for: an attribute id of a
// lookup element, or the exact text of a text element.
type QualifierValue struct {
	IsText    bool
	Attribute int32
	Text      string
}

// MarshalJSON writes the attribute id as a JSON integer, the text as a
// string.
func (v QualifierValue) MarshalJSON() ([]byte, error) {
	if v.IsText {
		return json.Marshal(v.Text)
	}
	return strconv.AppendInt(nil, int64(v.Attribute), 10), nil
}
