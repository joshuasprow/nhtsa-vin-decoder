package vocab

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A stock file (ADR 0019) is one stock source for callers with no
// database: a header naming the source, its kind (shelf or yard), its time
// and, for a shelf, the catalog its numbers belong to, then one row a
// line. Read refuses a malformed row; Link a shelf whose catalog is not
// loaded, and resolves each row's part and vehicle against the files
// loaded with it; Drift reports orphan rows and field names that look
// personal. Package stock answers from it.

// StockHolds is what a stock source holds: shelf parts or yard vehicles.
type StockHolds string

// What stock sources hold.
const (
	StockShelf StockHolds = "shelf"
	StockYard  StockHolds = "yard"
)

// StockRow is one line of a stock file: a shelf part or a yard vehicle.
// nitrovin interprets its id, location and route; Fields pass through.
type StockRow struct {
	// Line is the row's line in its file, from 1.
	Line int
	// ID is the source's own stock id, unique in the file.
	ID string
	// Location is opaque: a request filters on it.
	Location string

	// Key is a shelf row's fitment key, nil for a row with no number.
	Key *FitmentKey
	// Part is a shelf row's part type when it has no number, as the row
	// names it: an entry of the catalog's part spine (Spine and ID) or of
	// a part vocabulary on that spine (Vocabulary and Entry).
	Part *StockPart
	// PartTypes are the catalog's part types Part reaches, set by Link.
	PartTypes []string

	// Decoded is a yard row's precomputed vPIC decode; VIN its VIN.
	Decoded *StockDecoded
	VIN     string

	// Vehicle is the catalog vehicle the row names, with Year (a shelf row
	// with no number, or a yard row's last route), set by Link; nil when
	// the row names none.
	Vehicle *Vehicle
	Year    *int
	vehicle []byte // as the line writes it, resolved by Link

	// Fields are the row's own fields, verbatim: a JSON object, "{}" when
	// the line has none.
	Fields json.RawMessage
}

// StockPart is a shelf row's part type, as the row names it.
type StockPart struct {
	Spine, ID  string
	Vocabulary string
	Entry      map[string]string
}

// StockDecoded is a yard vehicle's vPIC make, model and year, as its
// source decoded them.
type StockDecoded struct {
	Make, Model int32
	Year        int
}

// stockHeader reads a stock header's own fields.
func (f *File) stockHeader(source, asOf, catalog, release []byte) error {
	s, err := stringAt(source, "/source")
	if err != nil {
		return err
	}
	f.Holds = StockHolds(s)
	if f.Holds != StockShelf && f.Holds != StockYard {
		return errAt("/source", "unknown source %q: shelf or yard", s)
	}
	if f.AsOf, err = nonEmptyAt(asOf, "/as_of"); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339, f.AsOf); err != nil {
		if _, err := time.Parse(time.DateOnly, f.AsOf); err != nil {
			return errAt("/as_of", "%q is not a time: RFC 3339 or YYYY-MM-DD", f.AsOf)
		}
	}
	switch f.Holds {
	case StockShelf:
		if catalog == nil {
			return errAt("", "missing field \"catalog\": a shelf source names the catalog its numbers belong to")
		}
		if f.StockCatalog, err = nameAt(catalog, "/catalog"); err != nil {
			return err
		}
		if release != nil {
			if f.Release, err = nonEmptyAt(release, "/release"); err != nil {
				return err
			}
		}
	case StockYard:
		switch {
		case catalog != nil:
			return errAt("/catalog", "a yard source names no catalog: its vehicles carry no number")
		case release != nil:
			return errAt("/release", "a yard source names no catalog release")
		}
	}
	return nil
}

// stockRowNames are a stock row's fields; shelfFields and yardFields pick
// each source's.
var stockRowNames = []string{"id", "location", "type", "number", "side", "part", "vehicle", "year", "decoded", "vin", "fields"}

var (
	shelfFields = []string{"id", "location", "type", "number", "side", "part", "vehicle", "year", "fields"}
	yardFields  = []string{"id", "location", "decoded", "vin", "vehicle", "year", "fields"}
)

// stockRow reads a stock file's line n.
func (f *File) stockRow(n int, line []byte) (StockRow, error) {
	raw, err := lineValue(line)
	if err != nil {
		return StockRow{}, err
	}
	var v [11][]byte
	allowed := shelfFields
	if f.Holds == StockYard {
		allowed = yardFields
	}
	unknown, has, ok := members(raw, stockRowNames, v[:])
	switch {
	case !ok:
		return StockRow{}, errAt("", "must be an object")
	case has:
		return StockRow{}, errAt("", "unknown field %q", unknown)
	}
	for i, name := range stockRowNames {
		if v[i] != nil && !slices.Contains(allowed, name) {
			return StockRow{}, errAt("", "unknown field %q: a %s row has %s", name, f.Holds, strings.Join(allowed, ", "))
		}
	}
	r := StockRow{Line: n}
	if v[0] == nil {
		return r, errAt("", "missing field \"id\"")
	}
	if r.ID, err = nonEmptyAt(v[0], "/id"); err != nil {
		return r, err
	}
	if v[1] == nil {
		return r, errAt("", "missing field \"location\"")
	}
	if r.Location, err = stringAt(v[1], "/location"); err != nil {
		return r, err
	}
	if v[7] != nil {
		y, ok := integer(v[7])
		if !ok || y < yearBase || y >= yearBase+yearSpan {
			return r, errAt("/year", "must be a model year, an integer from %d to %d", yearBase, yearBase+yearSpan-1)
		}
		r.Year = new(int(y))
	}
	if v[6] != nil {
		if len(v[6]) == 0 || v[6][0] != '{' {
			return r, errAt("/vehicle", "must be an object: a catalog vehicle")
		}
		if r.Year == nil {
			return r, errAt("", "missing field \"year\": a vehicle is named with its model year")
		}
		r.vehicle = bytes.Clone(v[6])
	}
	if err := r.readFields(v[10], f.Holds); err != nil {
		return r, err
	}
	if f.Holds == StockShelf {
		return r, r.shelf(v[2], v[3], v[4], v[5], v[6])
	}
	return r, r.yard(v[8], v[9], v[6], v[7])
}

// shelf reads a shelf row's part: its fitment key, or its part, vehicle
// and year.
func (r *StockRow) shelf(typ, number, side, part, vehicle []byte) error {
	if number != nil || typ != nil || side != nil {
		switch {
		case part != nil || vehicle != nil || r.Year != nil:
			return errAt("", "a shelf row names its part by its fitment key (type, number, side) or by part, vehicle and year, not both")
		case typ == nil:
			return errAt("", "missing field \"type\": a fitment key has its part type")
		case number == nil:
			return errAt("", "missing field \"number\": a row with no number names part, vehicle and year")
		}
		k := FitmentKey{}
		var err error
		if k.Type, err = nonEmptyAt(typ, "/type"); err != nil {
			return err
		}
		if k.Number, err = nonEmptyAt(number, "/number"); err != nil {
			return err
		}
		if side != nil {
			s, err := stringAt(side, "/side")
			if err != nil {
				return err
			}
			var ok bool
			if k.Side, ok = ParseSide(s); !ok || k.Side == SideNone {
				return errAt("/side", "must be left, right or both")
			}
		}
		r.Key = &k
		return nil
	}
	switch {
	case part == nil:
		return errAt("", "missing field \"part\": a shelf row names its fitment key, or part, vehicle and year")
	case vehicle == nil:
		return errAt("", "missing field \"vehicle\": a row with no number names its catalog vehicle and year")
	}
	var p struct {
		Spine, ID, Vocabulary string
		Entry                 map[string]string
	}
	if unknown, has, ok := members(part, []string{"spine", "id", "vocabulary", "entry"}, make([][]byte, 4)); !ok || has {
		if !ok {
			return errAt("/part", "must be an object: {spine, id} or {vocabulary, entry}")
		}
		return errAt("/part", "unknown field %q: a part is {spine, id} or {vocabulary, entry}", unknown)
	}
	if err := json.Unmarshal(part, &p); err != nil {
		return errAt("/part", "must be {spine, id} or {vocabulary, entry}, each a string or an object of strings")
	}
	switch {
	case p.Spine != "" && p.ID != "" && p.Vocabulary == "" && p.Entry == nil:
	case p.Vocabulary != "" && len(p.Entry) > 0 && p.Spine == "" && p.ID == "":
	default:
		return errAt("/part", "must be {spine, id} or {vocabulary, entry}, whole")
	}
	r.Part = &StockPart{Spine: p.Spine, ID: p.ID, Vocabulary: p.Vocabulary, Entry: p.Entry}
	return nil
}

// yard reads a yard row's routes: at least one of decoded, vin, and
// vehicle with year.
func (r *StockRow) yard(decoded, vin, vehicle, year []byte) error {
	if decoded != nil {
		var vals [3][]byte
		unknown, has, ok := members(decoded, []string{"make", "model", "year"}, vals[:])
		switch {
		case !ok:
			return errAt("/decoded", "must be an object: {make, model, year}")
		case has:
			return errAt("/decoded", "unknown field %q: decoded is {make, model, year}", unknown)
		}
		d := StockDecoded{}
		for i, name := range []string{"make", "model", "year"} {
			if vals[i] == nil {
				return errAt("/decoded", "missing field %q", name)
			}
		}
		var err error
		if d.Make, err = idAt(vals[0], "/decoded/make"); err != nil {
			return err
		}
		if d.Model, err = idAt(vals[1], "/decoded/model"); err != nil {
			return err
		}
		y, ok := integer(vals[2])
		if !ok || y < yearBase || y >= yearBase+yearSpan {
			return errAt("/decoded/year", "must be a model year, an integer from %d to %d", yearBase, yearBase+yearSpan-1)
		}
		d.Year = int(y)
		r.Decoded = &d
	}
	if vin != nil {
		var err error
		if r.VIN, err = nonEmptyAt(vin, "/vin"); err != nil {
			return err
		}
	}
	switch {
	case year != nil && vehicle == nil:
		return errAt("/year", "a yard row's year goes with its vehicle")
	case decoded == nil && vin == nil && vehicle == nil:
		return errAt("", "names no vehicle: decoded, vin, or vehicle and year")
	}
	return nil
}

// readFields reads a row's fields: an object, flat (strings, numbers and
// null) for a shelf, any JSON for a yard.
func (r *StockRow) readFields(raw []byte, holds StockHolds) error {
	if raw == nil {
		r.Fields = json.RawMessage("{}")
		return nil
	}
	flat := true
	if !eachMember(raw, func(_, value []byte) {
		switch value[0] {
		case '{', '[', 't', 'f':
			flat = false
		}
	}) {
		return errAt("/fields", "must be an object")
	}
	if holds == StockShelf && !flat {
		return errAt("/fields", "a shelf row's fields are flat: strings, numbers and null")
	}
	r.Fields = json.RawMessage(bytes.Clone(raw))
	return nil
}

// addStockRow indexes r, about to be row i, refusing it when an earlier
// row has its id.
func (f *File) addStockRow(r *StockRow, i, _ int) error {
	if j, dup := f.stockIDs[r.ID]; dup {
		return errAt("/id", "duplicate id %q: line %d has it too", r.ID, f.Stock[j].Line)
	}
	f.stockIDs[r.ID] = i
	return nil
}

// linkStock checks stock file f against the files loaded with it, byName,
// and catalog, the one catalog loaded (nil: none), and resolves its rows'
// parts and vehicles: a shelf names a loaded catalog, a part names a part
// type of that catalog's spine, and a vehicle is one of the catalog's,
// named by exactly its vehicle vocabulary's fields. Its faults come in
// line order.
func linkStock(f *File, byName map[string]*File, catalog *File) Faults {
	var faults Faults
	if f.Holds == StockShelf {
		switch c := byName[f.StockCatalog]; {
		case c == nil:
			return Faults{fault(f.Path, 1, "/catalog: catalog %q is not loaded", f.StockCatalog)}
		case c.Kind != KindCatalog:
			return Faults{fault(f.Path, 1, "/catalog: %q is %s, not a catalog", f.StockCatalog, c.Kind.noun())}
		}
		catalog = byName[f.StockCatalog]
	}
	var vehicles *File
	if catalog != nil && catalog.Vehicles != Reserved {
		if g := byName[catalog.Vehicles]; g != nil && g.Kind == KindVocabulary {
			vehicles = g
		}
	}
	for i := range f.Stock {
		r := &f.Stock[i]
		if r.Part != nil {
			types, err := stockPartTypes(r.Part, catalog, byName)
			if err != nil {
				faults = append(faults, fault(f.Path, r.Line, "/part%v", err))
			}
			r.PartTypes = types
		}
		if r.vehicle == nil {
			continue
		}
		if catalog == nil {
			faults = append(faults, fault(f.Path, r.Line, "/vehicle: no catalog is loaded, so a catalog vehicle names nothing"))
			continue
		}
		v, err := stockVehicle(r.vehicle, catalog, vehicles)
		if err != nil {
			faults = append(faults, fault(f.Path, r.Line, "/vehicle%v", err))
			continue
		}
		r.Vehicle = v
	}
	return faults
}

// stockPartTypes are the part types of catalog's spine that part reaches.
// The error is ": what is wrong", to follow "/part".
func stockPartTypes(p *StockPart, catalog *File, byName map[string]*File) ([]string, error) {
	spine := byName[catalog.Spine]
	if spine == nil || spine.Kind != KindSpine {
		return nil, fmt.Errorf(": the catalog's spine %q is not loaded", catalog.Spine)
	}
	if p.Spine != "" {
		if p.Spine != catalog.Spine {
			return nil, fmt.Errorf("/spine: %q is not catalog %q's spine %q", p.Spine, catalog.Name, catalog.Spine)
		}
		if _, ok := spine.SpineEntry(p.ID); !ok {
			return nil, fmt.Errorf("/id: spine %q has no id %q", p.Spine, p.ID)
		}
		return []string{p.ID}, nil
	}
	g := byName[p.Vocabulary]
	if g == nil || g.Kind != KindVocabulary || g.Subject != SubjectPart || g.Spine != catalog.Spine {
		return nil, fmt.Errorf("/vocabulary: %q is no loaded part vocabulary on the catalog's spine %q", p.Vocabulary, catalog.Spine)
	}
	e, ok := g.Entry(p.Entry)
	if !ok {
		return nil, fmt.Errorf("/entry: vocabulary %q has no entry %s", p.Vocabulary, entryText(p.Entry))
	}
	var out []string
	for _, mp := range e.Mappings {
		if mp.ID != "" && !slices.Contains(out, mp.ID) {
			out = append(out, mp.ID)
		}
	}
	slices.Sort(out)
	return out, nil
}

// stockVehicle reads a row's vehicle as catalog's lines name one: an entry
// of its vehicle vocabulary vehicles by exactly its fields, or, when its
// vehicles are vpic, one of make, model or ext. The error is ": what is
// wrong" or "/field: what is wrong", to follow "/vehicle".
func stockVehicle(raw []byte, catalog, vehicles *File) (*Vehicle, error) {
	if catalog.Vehicles == Reserved {
		var vals [3][]byte
		unknown, has, _ := members(raw, vpicVehicleNames, vals[:])
		if has {
			return nil, fmt.Errorf(": unknown field %q: a vpic catalog's vehicle is one of make, model or ext", unknown)
		}
		var t Target
		var err error
		switch {
		case vals[0] != nil && vals[1] == nil && vals[2] == nil:
			t.Make, err = idAt(vals[0], "/make")
		case vals[1] != nil && vals[0] == nil && vals[2] == nil:
			t.Model, err = idAt(vals[1], "/model")
		case vals[2] != nil && vals[0] == nil && vals[1] == nil:
			t.Ext, err = extRefAt(vals[2], "/ext")
		default:
			return nil, fmt.Errorf(": names one of make, model or ext")
		}
		if err != nil {
			return nil, err
		}
		v := &Vehicle{Target: t}
		v.key = string(appendVehicleKey(nil, v))
		return v, nil
	}
	var entry map[string]string
	if err := json.Unmarshal(raw, &entry); err != nil || len(entry) == 0 {
		return nil, fmt.Errorf(": must be an object of strings: an entry of vocabulary %q, by its fields", catalog.Vehicles)
	}
	if vehicles != nil {
		names := slices.Sorted(maps.Keys(entry))
		if want := slices.Sorted(slices.Values(vehicles.Fields)); !slices.Equal(names, want) {
			return nil, fmt.Errorf(": fields %s are not vocabulary %s's: %s", quoted(names), strconv.Quote(vehicles.Name), quoted(vehicles.Fields))
		}
		if _, ok := vehicles.Entry(entry); !ok {
			return nil, fmt.Errorf(": vocabulary %s has no entry %s, so the row could never match", strconv.Quote(vehicles.Name), entryText(entry))
		}
	}
	v := &Vehicle{Entry: entry}
	v.key = string(appendVehicleKey(nil, v))
	return v, nil
}

// VehicleKey is v's identity: equal for vehicles equal after Normalize.
func VehicleKey(v *Vehicle) string {
	if v.key == "" {
		return string(appendVehicleKey(nil, v))
	}
	return v.key
}

// personalWords are the words that make a field name look personal
// (ADR 0019).
var personalWords = []string{"email", "phone", "name", "address", "license"}

// personal reports whether field name looks personal: it holds one of
// personalWords, in any case. A false positive costs one drift line; a
// miss defeats the check, so any substring counts (`phones`,
// `phonenumber`, `firstName`, and `part_name` too).
func personal(name string) bool {
	name = strings.ToLower(name)
	return slices.ContainsFunc(personalWords, func(w string) bool { return strings.Contains(name, w) })
}

// eachField calls fn with every object member name in fields, nested
// ones included, through arrays too: the member's JSON Pointer under
// "/fields", its pointer with every array index as "-" (one field however
// many elements have it), and its name.
func eachField(fields []byte, fn func(ptr, key, name string)) {
	var walk func(raw []byte, ptr, key string)
	walk = func(raw []byte, ptr, key string) {
		if len(raw) == 0 {
			return
		}
		switch raw[0] {
		case '{':
			eachMember(raw, func(k, v []byte) {
				name, _ := str(k)
				tok := "/" + pointerToken(name)
				fn(ptr+tok, key+tok, name)
				walk(v, ptr+tok, key+tok)
			})
		case '[':
			vs, _ := items(raw, nil)
			for i, v := range vs {
				walk(v, ptr+"/"+strconv.Itoa(i), key+"/-")
			}
		}
	}
	walk(fields, "/fields", "/fields")
}

// stock measures d.f, a stock file: of a shelf whose catalog is loaded,
// each orphan row, whose number the catalog lacks, current or retired;
// and each field that looks personal, nested ones included, once, at the
// first row that has it.
func (d *drifts) stock(catalog *Catalog) {
	f := d.f
	start := len(d.out)
	if f.Holds == StockShelf && catalog != nil {
		for i := range f.Stock {
			r := &f.Stock[i]
			if r.Key == nil {
				continue
			}
			if fit, _ := catalog.ByNumber(r.Key.Type, r.Key.Number, r.Key.Side); fit == nil {
				d.addFile(r.Line, DriftOrphan, "/number: catalog %q has no fitment %s, current or retired", catalog.f.Name, r.Key)
			}
		}
	}
	type field struct {
		use
		ptr, name string
	}
	uses := map[string]*field{} // by the field's pointer, array indexes as "-"
	var order []*field
	for i := range f.Stock {
		r := &f.Stock[i]
		eachField(r.Fields, func(ptr, key, name string) {
			if !personal(name) {
				return
			}
			u := uses[key]
			if u == nil {
				u = &field{ptr: ptr, name: name}
				uses[key] = u
				order = append(order, u)
			}
			u.see(r.Line, 0)
		})
	}
	for _, u := range order {
		d.addFile(u.line, DriftPersonal, "%s: %q looks like personal data, which a stock source never supplies%s", u.ptr, u.name, u.more())
	}
	slices.SortStableFunc(d.out[start:], func(a, b Finding) int { return a.Line - b.Line })
}
