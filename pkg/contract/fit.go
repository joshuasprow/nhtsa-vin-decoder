package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// FitRequest is one fit request, the same on every surface (ADR 0018):
// from a vehicle or a fitment, shaped by its inputs. A vehicle alone asks
// for its part types; with Part, for candidates; with Related, for the
// vehicles that share its fitments. A fitment asks for its vehicles. Its
// zero values are the contract's defaults, except From, which is required.
type FitRequest struct {
	From FitFrom
	// ModelYear is the vehicle's year when no VIN gives one, and a VIN's
	// decode hint; a decoded year wins over it. With a fitment it keeps
	// the applications whose years hold it.
	ModelYear Null[int32]
	// Part is the part type asked for; nil asks for none.
	Part *FitPart
	// Side keeps the fitments that can go on that side: SideLeft,
	// SideRight or SideBoth; "" keeps every one.
	Side string
	// To names the vehicle vocabularies vehicles are named in; none names
	// them as spine entries.
	To []string
	// Related asks for the vehicles that share fitments with From.
	Related bool
	// Stock asks for the stock of the answer's fitments (ADR 0019): of
	// StockShelf, StockYard or both, each once.
	Stock []string
	// Location keeps the stock rows at one of these locations; none keeps
	// every row. It takes Stock.
	Location []string
	// Limit is the page size, 1 to MaxListLimit; 0 means DefaultListLimit.
	Limit int
	// After is the previous page's Next; "" starts at the first.
	After string
	// Now is the frozen clock for a VIN's decode. Zero means the wall
	// clock, read once by the caller.
	Now time.Time
}

// FitFrom is a fit's from: exactly one of a VIN, a vocabulary entry
// (Vocabulary and Entry), a vPIC make or model or an extension (SpineRef's
// Make, Model or Ext), or a fitment key (Fitment).
type FitFrom struct {
	VIN        string
	Vocabulary string
	Entry      map[string]string
	SpineRef
	Fitment *FitmentKey
}

// FitmentKey names a fitment within its catalog (ADR 0016): its part type,
// its number, and its side, "" for a fitment that has none.
type FitmentKey struct {
	Type   string `json:"type"`
	Number string `json:"number"`
	Side   string `json:"side,omitempty"`
}

// FitPart is a part type: an entry of the catalog's part spine (Spine and
// ID), or of a part vocabulary on that spine (Vocabulary and Entry).
type FitPart struct {
	Vocabulary string
	Entry      map[string]string
	Spine      string
	ID         string
}

// The sides a fitment states (ADR 0016), and a fit filters on.
const (
	SideLeft  = "left"
	SideRight = "right"
	SideBoth  = "both"
)

// Validate checks r against the request rules a Go caller can break:
// exactly one from, a vehicle or a fitment key with its type and number;
// part and related only beside a vehicle; exactly one part type; a side;
// a limit; VIN lengths; the range of Now. What the loaded files decide
// (vocabulary names, entry fields, the catalog's spine) is the Fitter's
// to refuse. The error is a *Problem.
func (r FitRequest) Validate() error {
	if p := r.validate(""); p != nil {
		return p
	}
	return nil
}

func (r FitRequest) validate(prefix string) *Problem {
	f := r.From
	kinds := 0
	for _, named := range []bool{f.VIN != "", f.Vocabulary != "" || f.Entry != nil, !f.SpineRef.IsZero(), f.Fitment != nil} {
		if named {
			kinds++
		}
	}
	switch {
	case kinds == 0:
		return NewProblem(InvalidRequest, prefix+"/from", "from must be a VIN, a vocabulary entry, a make, a model, an extension or a fitment")
	case kinds > 1:
		return NewProblem(InvalidRequest, prefix+"/from", "from names more than one of a VIN, a vocabulary entry, a spine entry and a fitment")
	case f.VIN != "":
		if p := checkVIN(f.VIN, prefix+"/from", "from"); p != nil {
			return p
		}
	case f.Vocabulary != "" || f.Entry != nil:
		if f.Vocabulary == "" {
			return NewProblem(InvalidRequest, prefix+"/from/vocabulary", "vocabulary is required beside entry")
		}
		if f.Entry == nil {
			return NewProblem(InvalidRequest, prefix+"/from/entry", "entry is required beside vocabulary")
		}
	case f.Fitment != nil:
		if p := f.Fitment.validate(prefix + "/from/fitment"); p != nil {
			return p
		}
		if r.Part != nil {
			return NewProblem(InvalidRequest, prefix+"/part", "part is given with a fitment: a fitment has its part type")
		}
		if r.Related {
			return NewProblem(InvalidRequest, prefix+"/related", "related is given with a fitment: related vehicles are a vehicle's")
		}
	default:
		if f.Spine != "" || f.ID != "" {
			return NewProblem(InvalidRequest, prefix+"/from", "from names a vehicle or a fitment: a declared spine's entry is a part type, given as part")
		}
		if p := f.SpineRef.validate(prefix + "/from"); p != nil {
			return p
		}
	}
	if r.Part != nil {
		if p := r.Part.validate(prefix + "/part"); p != nil {
			return p
		}
	}
	if r.Side != "" && !validSide(r.Side) {
		return NewProblem(InvalidRequest, prefix+"/side", "side must be left, right or both")
	}
	for i, name := range r.To {
		if name == "" {
			return NewProblem(InvalidRequest, prefix+"/to/"+strconv.Itoa(i), "a vocabulary name is not empty")
		}
	}
	for i, s := range r.Stock {
		ptr := prefix + "/stock/" + strconv.Itoa(i)
		switch {
		case s != StockShelf && s != StockYard:
			return NewProblem(InvalidRequest, ptr, "stock is shelf or yard")
		case slices.Index(r.Stock, s) < i:
			return NewProblem(InvalidRequest, ptr, fmt.Sprintf("stock names %s twice", s))
		}
	}
	if len(r.Stock) > 0 && r.Part == nil && !r.Related && f.Fitment == nil {
		return NewProblem(InvalidRequest, prefix+"/stock", "stock is a fitment's: give it with part, related or a fitment")
	}
	if r.Related && f.Fitment == nil && slices.Contains(r.Stock, StockShelf) {
		return NewProblem(InvalidRequest, prefix+"/stock", `related vehicles carry yard counts only: give stock ["yard"] with related`)
	}
	for i, l := range r.Location {
		if l == "" {
			return NewProblem(InvalidRequest, prefix+"/location/"+strconv.Itoa(i), "a location is not empty")
		}
	}
	if len(r.Location) > 0 && len(r.Stock) == 0 {
		return NewProblem(InvalidRequest, prefix+"/location", "location filters stock: give it with stock")
	}
	if r.Limit < 0 || r.Limit > MaxListLimit {
		return NewProblem(InvalidRequest, prefix+"/limit", fmt.Sprintf("limit must be an integer from 1 to %d", MaxListLimit))
	}
	return checkNow(r.Now, prefix+"/now")
}

func validSide(s string) bool { return s == SideLeft || s == SideRight || s == SideBoth }

// validate checks a fitment key at ptr: its type and number, and its side.
func (k *FitmentKey) validate(ptr string) *Problem {
	switch {
	case k.Type == "":
		return NewProblem(InvalidRequest, ptr+"/type", "a fitment key has a type and a number: type is missing or empty")
	case k.Number == "":
		return NewProblem(InvalidRequest, ptr+"/number", "a fitment key has a type and a number: number is missing or empty")
	case k.Side != "" && !validSide(k.Side):
		return NewProblem(InvalidRequest, ptr+"/side", "side must be left, right or both")
	}
	return nil
}

// validate checks a part at ptr: exactly one of a spine entry and a
// vocabulary entry, whole.
func (p *FitPart) validate(ptr string) *Problem {
	entry := p.Vocabulary != "" || p.Entry != nil
	spine := p.Spine != "" || p.ID != ""
	switch {
	case entry == spine:
		return NewProblem(InvalidRequest, ptr, "part is exactly one of {spine, id} and {vocabulary, entry}")
	case entry && p.Vocabulary == "":
		return NewProblem(InvalidRequest, ptr+"/vocabulary", "vocabulary is required beside entry")
	case entry && p.Entry == nil:
		return NewProblem(InvalidRequest, ptr+"/entry", "entry is required beside vocabulary")
	case spine && p.Spine == "":
		return NewProblem(InvalidRequest, ptr+"/spine", "spine is required beside id")
	case spine && p.ID == "":
		return NewProblem(InvalidRequest, ptr+"/id", "id is required beside spine")
	}
	return nil
}

// fitFields are FitRequest's properties.
var fitFields = []string{"from", "model_year", "part", "side", "to", "related", "stock", "location", "limit", "after", "now"}

// partFields are the properties a part object may have.
var partFields = []string{"vocabulary", "entry", "spine", "id"}

// fitmentKeyFields are the properties a fitment key may have.
var fitmentKeyFields = []string{"type", "number", "side"}

// ParseFitRequest parses one fit request object: a FitRequest of
// decode.schema.json. Unknown fields are refused. The error is a *Problem.
func ParseFitRequest(data []byte) (FitRequest, error) {
	if !json.Valid(data) {
		return FitRequest{}, NewProblem(MalformedJSON, "", "the request is not JSON")
	}
	r, p := parseFit(data, "")
	if p != nil {
		return FitRequest{}, p
	}
	return r, nil
}

// ParseFitBatch parses the body of POST /v1/fit: an array of 1 to MaxBatch
// fit request objects in at most MaxBody bytes. A problem's pointer names
// the item ("/3/part"). The error is a *Problem.
func ParseFitBatch(data []byte) ([]FitRequest, error) {
	raw, p := parseBatch(data)
	if p != nil {
		return nil, p
	}
	reqs := make([]FitRequest, len(raw))
	for i, item := range raw {
		r, p := parseFit(item, "/"+strconv.Itoa(i))
		if p != nil {
			return nil, p
		}
		reqs[i] = r
	}
	return reqs, nil
}

// ParseFitQuery parses GET /v1/fit: the request's paths as dotted
// parameters. from is from.vin, a VIN; from.vocabulary with one
// from.entry.<field> per entry field; from.make, from.model or from.ext;
// or from.fitment.type, from.fitment.number and from.fitment.side. part is
// part.spine with part.id, or part.vocabulary with one part.entry.<field>
// per field. to, stock and location are repeated, one value each; side,
// model_year, related (true or false), limit, after and now are the
// request's. Every other parameter is given at most once, and an unknown
// one is refused.
// As in ParseTranslateQuery, the parameters are read as the JSON request
// they spell, and a problem's pointer is the parameter's path
// ("/part/entry/code"). The error is a *Problem.
func ParseFitQuery(query url.Values) (FitRequest, error) {
	req := map[string]json.RawMessage{}
	objects := map[string]map[string]json.RawMessage{} // from, from.entry, from.fitment, part, part.entry
	set := func(object, field string, v json.RawMessage) {
		if objects[object] == nil {
			objects[object] = map[string]json.RawMessage{}
		}
		objects[object][field] = v
	}
	var vin json.RawMessage
	for _, name := range slices.Sorted(maps.Keys(query)) {
		vs := query[name]
		member := "from.entry."
		if strings.HasPrefix(name, "part.") {
			member = "part.entry."
		}
		ptr := queryPointer(name, member)
		if name == "to" || name == "stock" || name == "location" {
			to := make([]json.RawMessage, len(vs))
			for i, v := range vs {
				to[i] = queryString(v)
			}
			req[name] = marshalRaw(to)
			continue
		}
		if len(vs) != 1 {
			return FitRequest{}, NewProblem(InvalidRequest, ptr, name+" is given more than once")
		}
		v := vs[0]
		switch name {
		case "from.vin":
			vin = queryString(v)
		case "from.vocabulary", "from.ext":
			set("from", strings.TrimPrefix(name, "from."), queryString(v))
		case "from.make", "from.model":
			set("from", strings.TrimPrefix(name, "from."), queryNumber(v))
		case "from.fitment.type", "from.fitment.number", "from.fitment.side":
			set("from.fitment", strings.TrimPrefix(name, "from.fitment."), queryString(v))
		case "part.vocabulary", "part.spine", "part.id":
			set("part", strings.TrimPrefix(name, "part."), queryString(v))
		case "model_year", "limit":
			req[name] = queryNumber(v)
		case "related":
			req[name] = queryBool(v)
		case "side", "after", "now":
			req[name] = queryString(v)
		default:
			if field, ok := strings.CutPrefix(name, "from.entry."); ok {
				set("from.entry", field, queryString(v))
				continue
			}
			if field, ok := strings.CutPrefix(name, "part.entry."); ok {
				set("part.entry", field, queryString(v))
				continue
			}
			return FitRequest{}, NewProblem(InvalidRequest, ptr, "unknown query parameter "+strconv.Quote(name))
		}
	}
	if e, ok := objects["from.entry"]; ok {
		set("from", "entry", marshalRaw(e))
	}
	if k, ok := objects["from.fitment"]; ok {
		set("from", "fitment", marshalRaw(k))
	}
	if e, ok := objects["part.entry"]; ok {
		set("part", "entry", marshalRaw(e))
	}
	from := objects["from"]
	switch {
	case vin != nil && len(from) > 0:
		return FitRequest{}, NewProblem(InvalidRequest, "/from", "from names more than one of a VIN, a vocabulary entry, a spine entry and a fitment")
	case vin != nil:
		req["from"] = vin
	case len(from) > 0:
		req["from"] = marshalRaw(from)
	}
	if part, ok := objects["part"]; ok {
		req["part"] = marshalRaw(part)
	}
	r, p := parseFit(marshalRaw(req), "")
	if p != nil {
		return FitRequest{}, p
	}
	return r, nil
}

// queryBool is a query value where the request has a boolean: true or
// false, else the string, which the request's parser refuses.
func queryBool(v string) json.RawMessage {
	if v == "true" || v == "false" {
		return json.RawMessage(v)
	}
	return queryString(v)
}

// parseFit parses one valid JSON value as a fit request; prefix is the
// JSON Pointer to it.
func parseFit(data []byte, prefix string) (FitRequest, *Problem) {
	fields, p := objectFields(data, prefix, "a request", fitFields)
	if p != nil {
		return FitRequest{}, p
	}
	return readFit(fields, prefix)
}

// readFit reads a fit request's fields, unknown ones already refused;
// prefix is the JSON Pointer to the object.
func readFit(fields map[string]json.RawMessage, prefix string) (FitRequest, *Problem) {
	var r FitRequest
	var p *Problem
	raw, ok := fields["from"]
	if !ok {
		return r, NewProblem(InvalidRequest, prefix+"/from", "from is required")
	}
	if r.From, p = parseFitFrom(raw, prefix+"/from"); p != nil {
		return r, p
	}
	if raw, ok := fields["model_year"]; ok && !bytes.Equal(raw, []byte("null")) {
		y, ok := jsonInt32(raw)
		if !ok {
			return r, NewProblem(InvalidRequest, prefix+"/model_year", "model_year must be a 32-bit integer or null")
		}
		r.ModelYear = Value(y)
	}
	if raw, ok := fields["part"]; ok {
		part, p := parseFitPart(raw, prefix+"/part")
		if p != nil {
			return r, p
		}
		r.Part = &part
	}
	if raw, ok := fields["side"]; ok {
		if !jsonString(raw, &r.Side) || !validSide(r.Side) {
			return r, NewProblem(InvalidRequest, prefix+"/side", "side must be left, right or both")
		}
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
	if raw, ok := fields["related"]; ok {
		switch string(raw) {
		case "true":
			r.Related = true
		case "false":
		default:
			return r, NewProblem(InvalidRequest, prefix+"/related", "related must be true or false")
		}
	}
	for _, name := range []string{"stock", "location"} {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		var items []json.RawMessage
		if firstByte(raw) != '[' || json.Unmarshal(raw, &items) != nil || len(items) == 0 {
			return r, NewProblem(InvalidRequest, prefix+"/"+name, name+" must be a non-empty array of strings")
		}
		out := make([]string, len(items))
		for i, item := range items {
			if !jsonString(item, &out[i]) {
				return r, NewProblem(InvalidRequest, prefix+"/"+name+"/"+strconv.Itoa(i), name+" must be an array of strings")
			}
		}
		if name == "stock" {
			r.Stock = out
		} else {
			r.Location = out
		}
	}
	if raw, ok := fields["limit"]; ok {
		n, ok := jsonInt32(raw)
		if !ok || n < 1 || n > MaxListLimit {
			return r, NewProblem(InvalidRequest, prefix+"/limit", fmt.Sprintf("limit must be an integer from 1 to %d", MaxListLimit))
		}
		r.Limit = int(n)
	}
	if raw, ok := fields["after"]; ok {
		if !jsonString(raw, &r.After) || r.After == "" {
			return r, NewProblem(InvalidRequest, prefix+"/after", "after must be a non-empty string")
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
		return FitRequest{}, p
	}
	return r, nil
}

// parseFitFrom parses a fit's from, at ptr: a VIN string, a vehicle
// object as translate's from has it but a declared spine's entry, or
// {"fitment": key}.
func parseFitFrom(raw json.RawMessage, ptr string) (FitFrom, *Problem) {
	if firstByte(raw) == '{' {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return FitFrom{}, NewProblem(InvalidRequest, ptr, "from must be a VIN string or an object")
		}
		if key, ok := fields["fitment"]; ok {
			delete(fields, "fitment")
			if p := unknownField(fields, ptr, nil); p != nil {
				return FitFrom{}, NewProblem(InvalidRequest, p.Pointer, "from names a vehicle or a fitment, not both")
			}
			k, p := parseFitmentKey(key, ptr+"/fitment")
			if p != nil {
				return FitFrom{}, p
			}
			return FitFrom{Fitment: &k}, nil
		}
		for _, k := range []string{"id", "spine"} {
			if _, ok := fields[k]; ok {
				return FitFrom{}, NewProblem(InvalidRequest, pointerTo(ptr, k), "from names a vehicle or a fitment: a declared spine's entry is a part type, given as part")
			}
		}
	}
	f, p := parseFrom(raw, ptr)
	if p != nil {
		return FitFrom{}, p
	}
	return FitFrom{VIN: f.VIN, Vocabulary: f.Vocabulary, Entry: f.Entry, SpineRef: f.SpineRef}, nil
}

// parseFitmentKey parses a fitment key at ptr.
func parseFitmentKey(raw json.RawMessage, ptr string) (FitmentKey, *Problem) {
	fields, p := objectFields(raw, ptr, "a fitment key", fitmentKeyFields)
	if p != nil {
		return FitmentKey{}, p
	}
	var k FitmentKey
	for i, dst := range []*string{&k.Type, &k.Number, &k.Side} {
		name := fitmentKeyFields[i]
		raw, ok := fields[name]
		if !ok {
			continue
		}
		if !jsonString(raw, dst) {
			return FitmentKey{}, NewProblem(InvalidRequest, ptr+"/"+name, name+" must be a string")
		}
	}
	if _, ok := fields["side"]; ok && !validSide(k.Side) {
		return FitmentKey{}, NewProblem(InvalidRequest, ptr+"/side", "side must be left, right or both")
	}
	return k, k.validate(ptr)
}

// parseFitPart parses a fit's part, at ptr: {spine, id} or {vocabulary,
// entry}.
func parseFitPart(raw json.RawMessage, ptr string) (FitPart, *Problem) {
	if _, p := objectFields(raw, ptr, "part", partFields); p != nil {
		return FitPart{}, p
	}
	f, p := parseFrom(raw, ptr)
	if p != nil {
		return FitPart{}, p
	}
	part := FitPart{Vocabulary: f.Vocabulary, Entry: f.Entry, Spine: f.Spine, ID: f.ID}
	return part, part.validate(ptr)
}

// FitStatus is what a fit found (ADR 0018). Nothing to fit is a status
// with empty lists, never a refusal.
type FitStatus string

// The statuses of a fit.
const (
	// FitFound: the answer's list has something.
	FitFound FitStatus = "found"
	// FitUnknown: from or part names nothing the loaded files have: a VIN
	// that decodes to no vPIC make or model, an entry or a spine entry its
	// vocabulary or spine lacks, a fitment key that is neither a fitment
	// nor a retired number.
	FitUnknown FitStatus = "unknown"
	// FitNoVehicle: the vehicle reaches none of the catalog's vehicles.
	FitNoVehicle FitStatus = "no-vehicle"
	// FitNoFitment: the catalog has the vehicle or the fitment, but
	// nothing fits: no part type, candidate, related vehicle or
	// application is left.
	FitNoFitment FitStatus = "no-fitment"
	// FitNoCatalog: no catalog is loaded.
	FitNoCatalog FitStatus = "no-catalog"
)

// Fit is the answer to a FitRequest (ADR 0018). Exactly one of PartTypes,
// Candidates, Vehicles and Related is present, the one the request's
// inputs ask for, empty when nothing fits.
type Fit struct {
	Status FitStatus `json:"status"`
	// Catalog is the loaded catalog; nil with FitNoCatalog.
	Catalog *FitCatalog `json:"catalog,omitempty"`
	// Vehicle is the VIN's decode; nil without a VIN.
	Vehicle *Vehicle `json:"vehicle,omitempty"`
	// Models are the catalog's vehicles a vehicle reached, so a person can
	// be asked which one when there are several; nil from a fitment.
	Models []FitModel `json:"models,omitzero"`
	// Fitment, Reached and Retired are a fitment's: the fitment a key
	// reached, directly or through the retired number asked for.
	Fitment *Fitment    `json:"fitment,omitempty"`
	Reached FitReach    `json:"reached,omitempty"`
	Retired *FitRetired `json:"retired,omitempty"`

	PartTypes  []FitPartType    `json:"part_types,omitzero"`
	Candidates []FitCandidate   `json:"candidates,omitzero"`
	Vehicles   []FitVehicle     `json:"vehicles,omitzero"`
	Related    []RelatedVehicle `json:"related,omitzero"`
	// Shelf and Yard are a fitment's stock lists, from a fitment key with
	// stock, paged with Vehicles: after and limit page every list of the
	// answer together. ShelfByVehicle is, with part and stock shelf, the
	// shelf rows with no number for the part types and the vehicle's exact
	// vehicle and year, paged with Candidates: next stays while either has
	// more. Status and Total are the candidates' alone, so ShelfByVehicle
	// can hold rows when Status is FitNoFitment. Each is absent when stock
	// does not ask for it.
	Shelf          *StockList `json:"shelf,omitempty"`
	Yard           *StockList `json:"yard,omitempty"`
	ShelfByVehicle *StockList `json:"shelf_by_vehicle,omitempty"`
	// Total is how many the list holds across every page.
	Total int `json:"total"`
	// Next is the After of the next page; absent on the last.
	Next string `json:"next,omitempty"`

	Now            time.Time `json:"now"`
	DataMonth      string    `json:"data_month"`
	DecoderVersion string    `json:"decoder_version"`
}

// FitCatalog names the catalog a fit answered from, and the files it
// names: what a caller asks with, list's of included, learned from any fit
// answer.
type FitCatalog struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Release is the publisher's, absent when the catalog gives none.
	Release string `json:"release,omitempty"`
	// Spine is the declared spine its part types come from (part's spine);
	// Vehicles the vehicle vocabulary its vehicles are entries of, or vpic.
	Spine    string `json:"spine"`
	Vehicles string `json:"vehicles"`
}

// CatalogVehicle is a catalog's vehicle as its file writes it: an entry
// of its vehicle vocabulary, by its fields, or, when its vehicles are
// vpic, a vPIC make or model or an extension.
type CatalogVehicle struct {
	Entry map[string]string
	Make  int32
	Model int32
	Ext   string
}

// MarshalJSON writes the entry's fields, or {"make"}, {"model"} or {"ext"}.
func (v CatalogVehicle) MarshalJSON() ([]byte, error) {
	if v.Entry != nil {
		return json.Marshal(v.Entry)
	}
	return json.Marshal(SpineRef{Make: v.Make, Model: v.Model, Ext: v.Ext})
}

// FitModel is one of the catalog's vehicles a vehicle reached: the route
// to it as a translation has it.
type FitModel struct {
	Vehicle CatalogVehicle `json:"vehicle"`
	// Name is a vPIC make's or model's, or an extension's, when the
	// catalog's vehicles are vpic.
	Name string `json:"name,omitempty"`
	// Note is the vehicle vocabulary entry's own.
	Note string `json:"note,omitempty"`
	// Via are the mappings that reached it, as a translation's target
	// entry has them; none when from is the entry itself, or a vpic
	// catalog's vehicle was reached from a VIN or itself.
	Via         []Via       `json:"via,omitempty"`
	Unconfirmed Unconfirmed `json:"unconfirmed,omitzero"`
}

// Fitment is one part (ADR 0016): its key and note.
type Fitment struct {
	Type   string `json:"type"`
	Number string `json:"number"`
	Side   string `json:"side,omitempty"`
	Note   string `json:"note,omitempty"`
}

// FitReach is how a fit reached a fitment.
type FitReach string

// The ways a fitment is reached.
const (
	// ReachedDirect: the part type, or the number, asked for.
	ReachedDirect FitReach = "direct"
	// ReachedRedirect: the vehicle had no fitment of the part type asked
	// for, and a redirect lists it under this one.
	ReachedRedirect FitReach = "redirect"
	// ReachedRetired: the number asked for is retired, and this fitment
	// replaced it.
	ReachedRetired FitReach = "retired"
)

// FitRetired is the retired number a fitment was reached through: the key
// asked for, and the catalog's note on it.
type FitRetired struct {
	Type   string `json:"type"`
	Number string `json:"number"`
	Side   string `json:"side,omitempty"`
	Note   string `json:"note,omitempty"`
}

// FitRedirect is a redirect line as the catalog writes it: for a vehicle
// and years, part type From is listed under To.
type FitRedirect struct {
	Vehicle CatalogVehicle `json:"vehicle"`
	First   Null[int32]    `json:"first,omitzero"`
	Last    Null[int32]    `json:"last,omitzero"`
	From    string         `json:"from"`
	To      string         `json:"to"`
	Options string         `json:"options,omitempty"`
}

// FitPartType is a part type with a count of fitments: a vehicle's, or
// those a related vehicle shares.
type FitPartType struct {
	Type string `json:"type"`
	// Name is the spine entry's, absent when it has none.
	Name  string `json:"name,omitempty"`
	Count int    `json:"count"`
	// Reached is how a vehicle's part types were reached: directly, or,
	// with no fitment of the type, through a redirect. Absent for a
	// related vehicle's.
	Reached FitReach `json:"reached,omitempty"`
}

// FitCandidate is one fitment that may be the part for a vehicle, with the
// applications of it that matched.
type FitCandidate struct {
	Fitment      Fitment          `json:"fitment"`
	Applications []FitApplication `json:"applications"`
	Reached      FitReach         `json:"reached"`
	// Redirects are the redirects that reached it, when Reached is
	// ReachedRedirect.
	Redirects []FitRedirect `json:"redirects,omitempty"`
	// Shelf and Yard are its stock lists, each its first StockFirst rows,
	// when stock asks for them: shelf the rows of the fitment, yard the
	// yard vehicles among its applications' vehicles and years.
	Shelf *StockList `json:"shelf,omitempty"`
	Yard  *StockList `json:"yard,omitempty"`
}

// FitApplication is one application of a candidate that matched: the
// catalog's vehicle, the route to it, its years, its option text verbatim,
// and what could not be checked.
type FitApplication struct {
	Vehicle CatalogVehicle `json:"vehicle"`
	// Via are the mappings that reached the vehicle (its model's).
	Via     []Via       `json:"via,omitempty"`
	First   Null[int32] `json:"first,omitzero"`
	Last    Null[int32] `json:"last,omitzero"`
	Options string      `json:"options"`
	// Unconfirmed is what applied on the route but could not be checked:
	// the application's qualifiers and years, its model's and its part
	// type's; absent when everything was checked.
	Unconfirmed Unconfirmed `json:"unconfirmed,omitzero"`
}

// FitNames names a catalog's vehicle as a translation from it would: in
// the request's to vocabularies, or without to as spine entries.
type FitNames struct {
	Spine []SpineEntry `json:"spine,omitzero"`
	To    []Translated `json:"to,omitzero"`
}

// FitVehicle is one application of a fitment: its vehicle, years and
// option text, and the vehicle named.
type FitVehicle struct {
	Vehicle CatalogVehicle `json:"vehicle"`
	First   Null[int32]    `json:"first,omitzero"`
	Last    Null[int32]    `json:"last,omitzero"`
	Options string         `json:"options"`
	FitNames
}

// RelatedVehicle is a vehicle that shares fitments with a fit's from: a
// catalog vehicle and a year range, as its applications have them, with
// how many fitments it shares, per part type.
type RelatedVehicle struct {
	Vehicle CatalogVehicle `json:"vehicle"`
	First   Null[int32]    `json:"first,omitzero"`
	Last    Null[int32]    `json:"last,omitzero"`
	// Shared is the count of fitments shared, every part type's.
	Shared    int           `json:"shared"`
	PartTypes []FitPartType `json:"part_types"`
	// Yard counts, with stock yard, the yard vehicles among the vehicle
	// and its years: Total and Sources, no rows.
	Yard *StockList `json:"yard,omitempty"`
	FitNames
}

// FitmentLine is one fitment line of a catalog as its file has it, an
// entry of list's page of a catalog.
type FitmentLine struct {
	Fitment      FitmentKey        `json:"fitment"`
	Note         string            `json:"note,omitempty"`
	Applications []LineApplication `json:"applications"`
}

func (FitmentLine) listEntry() {}

// LineApplication is one application as its catalog's file has it.
type LineApplication struct {
	Vehicle CatalogVehicle `json:"vehicle"`
	First   Null[int32]    `json:"first,omitzero"`
	Last    Null[int32]    `json:"last,omitzero"`
	Options string         `json:"options"`
	// Qualifiers are element id (as a decimal string) → values.
	Qualifiers map[string][]QualifierValue `json:"qualifiers,omitempty"`
}
