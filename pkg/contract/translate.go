package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// TranslateRequest is one translation request, the same on every surface
// (ADR 0012): from a VIN, a vocabulary entry or a spine entry, to target
// vocabularies through exactly one spine. Its zero values are the
// contract's defaults, except From, which is required.
type TranslateRequest struct {
	From TranslateFrom
	// To names the target vocabularies; none answers the spine alone.
	To []string
	// ModelYear is the model year: the VIN's decode hint, and the year
	// when no VIN gives one. A VIN's decoded year wins over it.
	ModelYear Null[int32]
	// VIN is the context VIN when From is an entry: its decode checks
	// qualifiers and gives the year. "" for none; never beside From.VIN.
	VIN string
	// Now is the frozen clock for the VIN's decode. Zero means the wall
	// clock, read once by the caller.
	Now time.Time
}

// TranslateFrom is a translation's from: exactly one of a VIN, a
// vocabulary entry (Vocabulary and Entry, the entry's fields by name), or
// a spine entry (SpineRef).
type TranslateFrom struct {
	VIN        string
	Vocabulary string
	Entry      map[string]string
	SpineRef
}

// SpineRef names one spine entry as a mapping target does: exactly one of
// a vPIC make id, a vPIC model id, an extension reference
// (ext:<set>/<id>), or a declared spine's name with one of its entry ids.
type SpineRef struct {
	Make  int32  `json:"make,omitempty"`
	Model int32  `json:"model,omitempty"`
	Ext   string `json:"ext,omitempty"`
	Spine string `json:"spine,omitempty"`
	ID    string `json:"id,omitempty"`
}

// IsZero reports whether r names nothing.
func (r SpineRef) IsZero() bool { return r == SpineRef{} }

// extPattern is an extension reference, vocabulary.schema.json's
// ExtensionRef.
var extPattern = regexp.MustCompile(`^ext:[a-z0-9][a-z0-9._-]*/[^/\s]+$`)

// Validate checks r against the request rules a Go caller can break:
// exactly one from, VIN lengths after trimming, a VIN given once, the range
// of Now. What the loaded files decide (vocabulary names, entry fields) is
// the Translator's to refuse. The error is a *Problem.
func (r TranslateRequest) Validate() error {
	if p := r.validate(""); p != nil {
		return p
	}
	return nil
}

func (r TranslateRequest) validate(prefix string) *Problem {
	f := r.From
	kinds := 0
	for _, named := range []bool{f.VIN != "", f.Vocabulary != "" || f.Entry != nil, !f.SpineRef.IsZero()} {
		if named {
			kinds++
		}
	}
	switch {
	case kinds == 0:
		return NewProblem(InvalidRequest, prefix+"/from", "from must be a VIN, a vocabulary entry or a spine entry")
	case kinds > 1:
		return NewProblem(InvalidRequest, prefix+"/from", "from names more than one of a VIN, a vocabulary entry and a spine entry")
	case f.VIN != "":
		if p := checkVIN(f.VIN, prefix+"/from", "from"); p != nil {
			return p
		}
		if r.VIN != "" {
			return NewProblem(InvalidRequest, prefix+"/vin", "vin is given both as from and as context")
		}
	case f.Vocabulary != "" || f.Entry != nil:
		if f.Vocabulary == "" {
			return NewProblem(InvalidRequest, prefix+"/from/vocabulary", "vocabulary is required beside entry")
		}
		if f.Entry == nil {
			return NewProblem(InvalidRequest, prefix+"/from/entry", "entry is required beside vocabulary")
		}
	default:
		if p := f.SpineRef.validate(prefix + "/from"); p != nil {
			return p
		}
	}
	if r.VIN != "" {
		if p := checkVIN(r.VIN, prefix+"/vin", "vin"); p != nil {
			return p
		}
	}
	for i, name := range r.To {
		if name == "" {
			return NewProblem(InvalidRequest, prefix+"/to/"+strconv.Itoa(i), "a vocabulary name is not empty")
		}
	}
	return checkNow(r.Now, prefix+"/now")
}

// validate checks that r names exactly one spine entry, at ptr.
func (r SpineRef) validate(ptr string) *Problem {
	n := 0
	for _, named := range []bool{r.Make != 0, r.Model != 0, r.Ext != "", r.Spine != "" || r.ID != ""} {
		if named {
			n++
		}
	}
	switch {
	case n != 1:
		return NewProblem(InvalidRequest, ptr, "a spine entry is exactly one of {make}, {model}, {ext} or {spine, id}")
	case r.Make < 0:
		return NewProblem(InvalidRequest, ptr+"/make", "make must be a vPIC make id, from 1")
	case r.Model < 0:
		return NewProblem(InvalidRequest, ptr+"/model", "model must be a vPIC model id, from 1")
	case r.Ext != "" && !extPattern.MatchString(r.Ext):
		return NewProblem(InvalidRequest, ptr+"/ext", fmt.Sprintf("%q is not an extension reference, ext:<set>/<id>", r.Ext))
	case (r.Spine != "" || r.ID != "") && r.Spine == "":
		return NewProblem(InvalidRequest, ptr+"/spine", "spine is required beside id")
	case (r.Spine != "" || r.ID != "") && r.ID == "":
		return NewProblem(InvalidRequest, ptr+"/id", "id is required beside spine")
	}
	return nil
}

// translateFields are TranslateRequest's properties.
var translateFields = []string{"from", "to", "model_year", "vin", "now"}

// fromFields are the properties a from object may have.
var fromFields = []string{"vocabulary", "entry", "make", "model", "ext", "spine", "id"}

// ParseTranslateRequest parses one translate request object: a
// TranslateRequest of decode.schema.json. Unknown fields are refused. The
// error is a *Problem.
func ParseTranslateRequest(data []byte) (TranslateRequest, error) {
	if !json.Valid(data) {
		return TranslateRequest{}, NewProblem(MalformedJSON, "", "the request is not JSON")
	}
	r, p := parseTranslate(data, "")
	if p != nil {
		return TranslateRequest{}, p
	}
	return r, nil
}

// parseTranslate parses one valid JSON value as a translate request; prefix
// is the JSON Pointer to it.
func parseTranslate(data []byte, prefix string) (TranslateRequest, *Problem) {
	fields, p := objectFields(data, prefix, "a request", translateFields)
	if p != nil {
		return TranslateRequest{}, p
	}
	var r TranslateRequest
	raw, ok := fields["from"]
	if !ok {
		return r, NewProblem(InvalidRequest, prefix+"/from", "from is required")
	}
	if r.From, p = parseFrom(raw, prefix+"/from"); p != nil {
		return r, p
	}
	if raw, ok := fields["to"]; ok {
		var items []json.RawMessage
		if firstByte(raw) != '[' || json.Unmarshal(raw, &items) != nil {
			return r, NewProblem(InvalidRequest, prefix+"/to", "to must be an array of vocabulary names")
		}
		r.To = make([]string, len(items))
		for i, item := range items {
			if !jsonString(item, &r.To[i]) || r.To[i] == "" {
				return r, NewProblem(InvalidRequest, prefix+"/to/"+strconv.Itoa(i), "a vocabulary name is a non-empty string")
			}
		}
	}
	if raw, ok := fields["model_year"]; ok && !bytes.Equal(raw, []byte("null")) {
		y, ok := jsonInt32(raw)
		if !ok {
			return r, NewProblem(InvalidRequest, prefix+"/model_year", "model_year must be a 32-bit integer or null")
		}
		r.ModelYear = Value(y)
	}
	if raw, ok := fields["vin"]; ok {
		if !jsonString(raw, &r.VIN) {
			return r, NewProblem(InvalidRequest, prefix+"/vin", "vin must be a string")
		}
		if p := checkVIN(r.VIN, prefix+"/vin", "vin"); p != nil {
			return r, p
		}
	}
	if raw, ok := fields["now"]; ok {
		var s string
		if !jsonString(raw, &s) {
			return r, NewProblem(InvalidRequest, prefix+"/now", "now must be a string")
		}
		t, err := ParseNow(s)
		if err != nil {
			return r, NewProblem(InvalidRequest, prefix+"/now", err.Error())
		}
		r.Now = t
	}
	if p := r.validate(prefix); p != nil {
		return TranslateRequest{}, p
	}
	return r, nil
}

// parseFrom parses a translate request's from, at ptr: a VIN string or an
// object.
func parseFrom(raw json.RawMessage, ptr string) (TranslateFrom, *Problem) {
	var f TranslateFrom
	if firstByte(raw) == '"' {
		if !jsonString(raw, &f.VIN) {
			return f, NewProblem(InvalidRequest, ptr, "from must be a VIN string or an object")
		}
		if p := checkVIN(f.VIN, ptr, "from"); p != nil {
			return f, p
		}
		return f, nil
	}
	if firstByte(raw) != '{' {
		return f, NewProblem(InvalidRequest, ptr, "from must be a VIN string or an object")
	}
	fields, p := objectFields(raw, ptr, "from", fromFields)
	if p != nil {
		return f, p
	}
	_, vocabulary := fields["vocabulary"]
	_, entry := fields["entry"]
	if vocabulary || entry {
		for _, k := range fromFields[2:] {
			if _, ok := fields[k]; ok {
				return f, NewProblem(InvalidRequest, pointerTo(ptr, k), "from names a vocabulary entry or a spine entry, not both")
			}
		}
		if !vocabulary {
			return f, NewProblem(InvalidRequest, ptr+"/vocabulary", "vocabulary is required beside entry")
		}
		if !entry {
			return f, NewProblem(InvalidRequest, ptr+"/entry", "entry is required beside vocabulary")
		}
		if !jsonString(fields["vocabulary"], &f.Vocabulary) || f.Vocabulary == "" {
			return f, NewProblem(InvalidRequest, ptr+"/vocabulary", "vocabulary must be a non-empty string")
		}
		values, p := objectFields(fields["entry"], ptr+"/entry", "entry", nil)
		if p != nil {
			return f, p
		}
		f.Entry = make(map[string]string, len(values))
		for _, k := range slices.Sorted(maps.Keys(values)) {
			var v string
			if !jsonString(values[k], &v) {
				return f, NewProblem(InvalidRequest, pointerTo(ptr+"/entry", k), "an entry's field values are strings")
			}
			f.Entry[k] = v
		}
		return f, nil
	}
	for _, k := range []string{"make", "model"} {
		if raw, ok := fields[k]; ok {
			id, ok := jsonInt32(raw)
			if !ok || id < 1 {
				return f, NewProblem(InvalidRequest, ptr+"/"+k, k+" must be a vPIC "+k+" id, an integer from 1")
			}
			if k == "make" {
				f.Make = id
			} else {
				f.Model = id
			}
		}
	}
	for _, k := range []string{"ext", "spine", "id"} {
		if raw, ok := fields[k]; ok {
			var s string
			if !jsonString(raw, &s) || s == "" {
				return f, NewProblem(InvalidRequest, ptr+"/"+k, k+" must be a non-empty string")
			}
			switch k {
			case "ext":
				f.Ext = s
			case "spine":
				f.Spine = s
			case "id":
				f.ID = s
			}
		}
	}
	return f, f.SpineRef.validate(ptr)
}

// objectFields reads one JSON value, at ptr, as an object (what names it
// in a refusal), refusing a member not in allowed (nil allows any) in byte
// order, so the refusal is deterministic.
func objectFields(data []byte, ptr, what string, allowed []string) (map[string]json.RawMessage, *Problem) {
	if firstByte(data) != '{' {
		return nil, NewProblem(InvalidRequest, ptr, what+" is a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, NewProblem(InvalidRequest, ptr, err.Error())
	}
	if allowed != nil {
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			if !slices.Contains(allowed, name) {
				return nil, NewProblem(InvalidRequest, pointerTo(ptr, name), "unknown field "+strconv.Quote(name))
			}
		}
	}
	return fields, nil
}

// Translation is the answer to a TranslateRequest (ADR 0012): the route
// from from through the spine to each target vocabulary.
type Translation struct {
	From TranslationFrom `json:"from"`
	// Vehicle is the VIN's decode, from or context; nil without a VIN.
	Vehicle *Vehicle `json:"vehicle,omitempty"`
	// Spine is every spine entry reached, once each, in route order.
	Spine []SpineEntry `json:"spine"`
	// To is one per target vocabulary, in request order.
	To []Translated `json:"to"`
	// Vocabularies are the files the translation used, in load order.
	Vocabularies   []FileVersion `json:"vocabularies"`
	Now            time.Time     `json:"now"`
	DataMonth      string        `json:"data_month"`
	DecoderVersion string        `json:"decoder_version"`
}

// FromStatus is what a translation's from is to its vocabulary.
type FromStatus string

// The statuses of a translation's from.
const (
	// FromMapped: the entry has mappings, or the VIN or spine entry reaches
	// the spine.
	FromMapped FromStatus = "mapped"
	// FromUnmapped: the vocabulary lists the entry without mappings.
	FromUnmapped FromStatus = "unmapped"
	// FromUnknown: the vocabulary or spine has no such entry, or the VIN
	// decodes to no vPIC make or model.
	FromUnknown FromStatus = "unknown"
)

// TranslationFrom is the request's from with its status: the VIN as
// decoded, the entry as its vocabulary writes it (as the request did when
// unknown) with the entry's note, or the spine entry.
type TranslationFrom struct {
	VIN        string            `json:"vin,omitempty"`
	Vocabulary string            `json:"vocabulary,omitempty"`
	Entry      map[string]string `json:"entry,omitempty"`
	SpineRef
	Status FromStatus `json:"status"`
	Note   string     `json:"note,omitempty"`
}

// Vehicle is what a translation reads from a VIN's decode: the VIN as
// decoded, its vPIC make and model ids and its model year, each null when
// undecoded.
type Vehicle struct {
	VIN       string      `json:"vin"`
	Make      Null[int32] `json:"make"`
	Model     Null[int32] `json:"model"`
	ModelYear Null[int32] `json:"model_year"`
}

// SpineEntry is one spine entry in an answer: a translation's, or a
// list's of vPIC, a declared spine or an extension set.
type SpineEntry struct {
	SpineRef
	// Name is from the data month or the spine entry's file; absent for an
	// unnamed declared-spine entry and for Drift.
	Name string `json:"name,omitempty"`
	// Drift marks a vPIC id the data month lacks: a mapping names it, and
	// it is served (ADR 0011).
	Drift bool `json:"drift,omitempty"`
	// Under is the make a vPIC or extension model sits under.
	Under *SpineRef `json:"under,omitempty"`
	// Via are the from entry's mappings that reached it, in a
	// translation from a vocabulary entry.
	Via []Via `json:"via,omitempty"`
	// Unconfirmed is what no Via could check; absent when one checked
	// everything.
	Unconfirmed Unconfirmed `json:"unconfirmed,omitzero"`
}

// Via is one mapping that held on a route: the spine entry it names, its
// model years, origin and note as its file has them, and what could not be
// checked on the route through it.
type Via struct {
	SpineRef
	First       Null[int32] `json:"first,omitzero"`
	Last        Null[int32] `json:"last,omitzero"`
	Origin      string      `json:"origin,omitempty"`
	Note        string      `json:"note,omitempty"`
	Unconfirmed Unconfirmed `json:"unconfirmed,omitzero"`
}

// Translated is one target vocabulary's entries.
type Translated struct {
	Vocabulary string `json:"vocabulary"`
	// Entries are each entry reached once, by normalized fields.
	Entries []TranslatedEntry `json:"entries"`
}

// TranslatedEntry is one entry a translation reaches, with every mapping
// of it that held.
type TranslatedEntry struct {
	Entry map[string]string `json:"entry"`
	// Note is the entry's own.
	Note string `json:"note,omitempty"`
	Via  []Via  `json:"via"`
	// Unconfirmed is what no Via could check; absent when one checked
	// everything, so a client can keep only fully confirmed entries.
	Unconfirmed Unconfirmed `json:"unconfirmed,omitzero"`
}

// Unconfirmed is what applied but could not be checked (ADR 0012): the
// qualifier elements with no decode, or that the decode left empty, and
// the model year when none was known. It marshals as an array: element
// ids ascending, then "model_year".
type Unconfirmed struct {
	Elements  []int32
	ModelYear bool
}

// IsZero reports whether everything was checked.
func (u Unconfirmed) IsZero() bool { return len(u.Elements) == 0 && !u.ModelYear }

// MarshalJSON writes [15, 34, "model_year"].
func (u Unconfirmed) MarshalJSON() ([]byte, error) {
	b := []byte{'['}
	for i, e := range u.Elements {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, int64(e), 10)
	}
	if u.ModelYear {
		if len(u.Elements) > 0 {
			b = append(b, ',')
		}
		b = append(b, `"model_year"`...)
	}
	return append(b, ']'), nil
}

// FileVersion names a loaded file an answer used, by name and version.
type FileVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
