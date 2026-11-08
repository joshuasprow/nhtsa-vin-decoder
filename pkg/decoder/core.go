package decoder

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// item is one row of spvindecode_core's DecodingItems (vpic."tblDecodingItem")
// before the wrapper resolves values: an 'XXX' value is resolved later
// through the element's lookup. NULL is artifact.NullInt32, artifact.NullInt64,
// artifact.NullBool or a zero sqlText. The table's serial id is the item's
// index in the slice core returns: insertion order, which the dedup and the
// element-18 pick break ties on.
type item struct {
	createdOn   int64 // coalesce(UpdatedOn, CreatedOn) of the source row, µs
	patternID   int32
	keys        sqlText
	vinSchemaID int32
	wmiID       int32
	elementID   int32
	attributeID sqlText
	value       sqlText
	source      string
	priority    int32
	toBeQCed    uint8 // 0, 1 or artifact.NullBool
}

// sqlText is a varchar that can be NULL; the zero value is NULL.
type sqlText struct {
	s     string
	valid bool
}

func text(s string) sqlText { return sqlText{s, true} }

// unresolved is the value of an item whose AttributeId the wrapper resolves.
var unresolved = text("XXX")

// Dedup-exempt elements (spvindecode_core.sql:193): every item of these
// survives C8.
func dedupExempt(id int32) bool {
	switch id {
	case 114, 121, 129, 150, 154, 155, 169, 186:
		return true
	}
	return false
}

// engine is the decode-time view of one artifact: the columns a pass reads,
// and a pattern index and a charset per VinSchema, each built lazily. It is
// safe for concurrent use.
type engine struct {
	a *artifact.Artifact

	wmi struct {
		wmi                                            artifact.Strings
		id, manufacturerID, vehicleTypeID, truckTypeID []int32
		availableFrom, changedOn                       []int64
	}
	wmiVinSchema struct{ wmiID, vinSchemaID, yearFrom, yearTo []int32 }
	vinSchema    struct {
		id       []int32
		toBeQCed artifact.Bools
	}
	pattern struct {
		vinSchemaID, id, elementID []int32
		keys, attributeID          artifact.Strings
		changedOn                  []int64
	}
	element struct {
		row      []int32 // by element id: its row, -1 when there is none
		datatype artifact.Strings
		weight   []int32
	}
	makeModel          struct{ modelID, makeID []int32 }
	wmiMake            struct{ wmiID, makeID []int32 }
	engineModels       map[string][]int32 // lower(trim(name)) → ids
	engineModelPattern struct {
		engineModelID, elementID []int32
		attributeID              artifact.Strings
		changedOn                []int64
	}
	defaultValue struct {
		vehicleTypeID, elementID []int32
		value                    artifact.Strings
		changedOn                []int64
	}
	conversion struct {
		fromElementID, id, toElementID []int32
		formula                        artifact.Strings
	}
	specSchema struct {
		id, makeID, vehicleTypeID []int32
		toBeQCed                  artifact.Bools
	}
	specSchemaPattern struct{ schemaID, id []int32 }
	specPattern       struct {
		schemaPatternID []int32
		isKey           artifact.Bools
		elementID       []int32
		attributeID     artifact.Strings
		changedOn       []int64
	}
	specSchemaModel struct{ modelID, schemaID []int32 }
	specSchemaYear  struct{ schemaID, year []int32 }
	vinException    struct {
		vin        artifact.Strings
		checkDigit artifact.Bools
	}
	errorCodes struct {
		id, weight           []int32
		name, additionalText artifact.Strings
	}
	makes         lookupTable
	manufacturers lookupTable
	vehicleTypes  lookupTable

	patternElements []bool
	schemas         []atomic.Pointer[patternIndex]  // by VinSchema id
	charsets        []atomic.Pointer[schemaCharset] // by VinSchema id
}

// lookupTable is one lookup relation's rows in the artifact's lookup union.
type lookupTable struct {
	id     []int32
	name   artifact.Strings
	lo, hi int
}

// name is the row whose id is id, as an inner join on it reads it.
func (l *lookupTable) nameOf(id int32) (string, bool) {
	i, j := artifact.EqualRange(l.id, l.lo, l.hi, id)
	if i == j {
		return "", false
	}
	return l.name.At(i)
}

func newEngine(a *artifact.Artifact) *engine {
	e := &engine{a: a}

	t := a.Table("wmi")
	e.wmi.wmi = t.Strings("wmi")
	e.wmi.id = t.Int32s("id")
	e.wmi.manufacturerID = t.Int32s("manufacturerid")
	e.wmi.vehicleTypeID = t.Int32s("vehicletypeid")
	e.wmi.truckTypeID = t.Int32s("trucktypeid")
	e.wmi.availableFrom = t.Int64s("publicavailabilitydate")
	e.wmi.changedOn = t.Int64s("changedon")

	t = a.Table("wmi_vinschema")
	e.wmiVinSchema.wmiID = t.Int32s("wmiid")
	e.wmiVinSchema.vinSchemaID = t.Int32s("vinschemaid")
	e.wmiVinSchema.yearFrom = t.Int32s("yearfrom")
	e.wmiVinSchema.yearTo = t.Int32s("yearto")

	t = a.Table("vinschema")
	e.vinSchema.id = t.Int32s("id")
	e.vinSchema.toBeQCed = t.Bools("tobeqced")

	t = a.Table("pattern")
	e.pattern.vinSchemaID = t.Int32s("vinschemaid")
	e.pattern.id = t.Int32s("id")
	e.pattern.elementID = t.Int32s("elementid")
	e.pattern.keys = t.Strings("keys")
	e.pattern.attributeID = t.Strings("attributeid")
	e.pattern.changedOn = t.Int64s("changedon")
	if n := t.Rows(); n > 0 {
		e.schemas = make([]atomic.Pointer[patternIndex], e.pattern.vinSchemaID[n-1]+1)
		e.charsets = make([]atomic.Pointer[schemaCharset], e.pattern.vinSchemaID[n-1]+1)
	}

	t = a.Table("element")
	ids := t.Int32s("id")
	e.element.row = make([]int32, slices.Max(ids)+1)
	for i := range e.element.row {
		e.element.row[i] = -1
	}
	for r, id := range ids {
		e.element.row[id] = int32(r)
	}
	e.element.datatype = t.Strings("datatype")
	e.element.weight = t.Int32s("weight")
	e.patternElements = patternElements(a)

	t = a.Table("make_model")
	e.makeModel.modelID = t.Int32s("modelid")
	e.makeModel.makeID = t.Int32s("makeid")

	t = a.Table("wmi_make")
	e.wmiMake.wmiID = t.Int32s("wmiid")
	e.wmiMake.makeID = t.Int32s("makeid")

	t = a.Table("enginemodel")
	e.engineModels = map[string][]int32{}
	names := t.Strings("name")
	for r, id := range t.Int32s("id") {
		name, _ := names.At(r)
		k := strings.ToLower(trimSpaces(name))
		e.engineModels[k] = append(e.engineModels[k], id)
	}

	t = a.Table("enginemodelpattern")
	e.engineModelPattern.engineModelID = t.Int32s("enginemodelid")
	e.engineModelPattern.elementID = t.Int32s("elementid")
	e.engineModelPattern.attributeID = t.Strings("attributeid")
	e.engineModelPattern.changedOn = t.Int64s("changedon")

	t = a.Table("defaultvalue")
	e.defaultValue.vehicleTypeID = t.Int32s("vehicletypeid")
	e.defaultValue.elementID = t.Int32s("elementid")
	e.defaultValue.value = t.Strings("defaultvalue")
	e.defaultValue.changedOn = t.Int64s("changedon")

	t = a.Table("conversion")
	e.conversion.fromElementID = t.Int32s("fromelementid")
	e.conversion.id = t.Int32s("id")
	e.conversion.toElementID = t.Int32s("toelementid")
	e.conversion.formula = t.Strings("formula")

	t = a.Table("vehiclespecschema")
	e.specSchema.id = t.Int32s("id")
	e.specSchema.makeID = t.Int32s("makeid")
	e.specSchema.vehicleTypeID = t.Int32s("vehicletypeid")
	e.specSchema.toBeQCed = t.Bools("tobeqced")

	t = a.Table("vspecschemapattern")
	e.specSchemaPattern.schemaID = t.Int32s("schemaid")
	e.specSchemaPattern.id = t.Int32s("id")

	t = a.Table("vehiclespecpattern")
	e.specPattern.schemaPatternID = t.Int32s("vspecschemapatternid")
	e.specPattern.isKey = t.Bools("iskey")
	e.specPattern.elementID = t.Int32s("elementid")
	e.specPattern.attributeID = t.Strings("attributeid")
	e.specPattern.changedOn = t.Int64s("changedon")

	t = a.Table("vehiclespecschema_model")
	e.specSchemaModel.modelID = t.Int32s("modelid")
	e.specSchemaModel.schemaID = t.Int32s("vehiclespecschemaid")

	t = a.Table("vehiclespecschema_year")
	e.specSchemaYear.schemaID = t.Int32s("vehiclespecschemaid")
	e.specSchemaYear.year = t.Int32s("year")

	t = a.Table("vinexception")
	e.vinException.vin = t.Strings("vin")
	e.vinException.checkDigit = t.Bools("checkdigit")

	t = a.Table("errorcode")
	e.errorCodes.id = t.Int32s("id")
	e.errorCodes.name = t.Strings("name")
	e.errorCodes.additionalText = t.Strings("additionalerrortext")
	e.errorCodes.weight = t.Int32s("weight")

	t = a.Table("lookup")
	tables, lid, lname := t.Strings("table"), t.Int32s("id"), t.Strings("name")
	for _, l := range []struct {
		dst  *lookupTable
		name string
	}{{&e.makes, "make"}, {&e.manufacturers, "manufacturer"}, {&e.vehicleTypes, "vehicletype"}} {
		lo, hi := tables.EqualRange(0, t.Rows(), l.name)
		*l.dst = lookupTable{id: lid, name: lname, lo: lo, hi: hi}
	}
	return e
}

var emptyPatternIndex = &patternIndex{}

// patternIndex returns VinSchema id's index, building it on first use. Two
// decodes reaching a schema at once may both build it; one result is kept.
func (e *engine) patternIndex(id int32) *patternIndex {
	if id < 0 || int(id) >= len(e.schemas) {
		return emptyPatternIndex
	}
	slot := &e.schemas[id]
	if x := slot.Load(); x != nil {
		return x
	}
	slot.CompareAndSwap(nil, newPatternIndex(e.a, e.patternElements, id))
	return slot.Load()
}

func (e *engine) elementExists(id int32) bool {
	return id >= 0 && int(id) < len(e.element.row) && e.element.row[id] >= 0
}

// wmiAvailable is `PublicAvailabilityDate <= NOW()` for wmi row r; NULL
// is not available.
func (e *engine) wmiAvailable(r int, now int64) bool {
	d := e.wmi.availableFrom[r]
	return d != artifact.NullInt64 && d <= now
}

// inBand is `modelYear is null or modelYear between YearFrom and
// coalesce(YearTo, 2999)` for wmi_vinschema row r; modelYear 0 is NULL.
func (e *engine) inBand(r int, modelYear int) bool {
	if modelYear == 0 {
		return true
	}
	to := e.wmiVinSchema.yearTo[r]
	if to == artifact.NullInt32 {
		to = 2999
	}
	return int(e.wmiVinSchema.yearFrom[r]) <= modelYear && modelYear <= int(to)
}

// core ports spvindecode_core's items up to the error codes (stages
// C1-C11): the WMI row, the pattern pass, engine model, vehicle type,
// manufacturer and model year items, formula patterns, the per-element
// dedup, make, conversions and vehicle specs, in that order. pass runs the
// rest of the procedure on them.
//
// vin is sanitized. modelYear is the pass's year, 0 for NULL;
// modelYearSource is the text the procedure stores as the year item's keys.
// now is NOW() in microseconds since 1970-01-01 UTC, the dump's zoneless
// time read as UTC (the oracle's TimeZone).
//
// A nil slice with a nil error is the procedure's code 7: no available WMI
// row, no items. An error is a case where the procedure raises and the
// decode aborts: a pattern key Postgres rejects (see compileKey), a Model or
// Vehicle Type attribute that is not an integer, or a conversion convert does
// not evaluate.
func (e *engine) core(vin string, modelYear int, modelYearSource string, now int64) ([]item, error) {
	vk := varKeys(vin)
	varWMI := wmi(vin)
	wlo, whi := e.wmi.wmi.EqualRange(0, len(e.wmi.id), varWMI)

	// C1 (:69): the first available wmi row.
	wmiRow := -1
	for r := wlo; r < whi; r++ {
		if e.wmiAvailable(r, now) {
			wmiRow = r
			break
		}
	}
	if wmiRow < 0 {
		return nil, nil
	}
	wmiID := e.wmi.id[wmiRow]

	items, err := e.patternPass(vk, wlo, whi, modelYear, now)
	if err != nil {
		return nil, err
	}
	items = e.engineModelItems(items, wmiID)

	// C4 (:126-133): Vehicle Type from every available wmi row.
	upperWMI := text(strings.ToUpper(varWMI))
	for r := wlo; r < whi; r++ {
		if !e.wmiAvailable(r, now) {
			continue
		}
		vt := e.wmi.vehicleTypeID[r]
		name, ok := e.vehicleTypes.nameOf(vt)
		if !ok {
			continue
		}
		items = append(items, item{
			createdOn: e.wmi.changedOn[r], patternID: artifact.NullInt32, keys: upperWMI,
			vinSchemaID: artifact.NullInt32, wmiID: e.wmi.id[r], elementID: 39,
			attributeID: text(itoa(vt)), value: text(strings.ToUpper(name)),
			source: "VehType", priority: 100, toBeQCed: artifact.NullBool,
		})
	}

	// C5 (:135-144): the manufacturer of the first available wmi row with
	// one, inserted even when there is none (NULL id and name).
	var mfrID, mfrName sqlText
	for r := wlo; r < whi; r++ {
		if !e.wmiAvailable(r, now) {
			continue
		}
		id := e.wmi.manufacturerID[r]
		if name, ok := e.manufacturers.nameOf(id); ok {
			mfrID, mfrName = text(itoa(id)), text(strings.ToUpper(name))
			break
		}
	}
	items = append(items,
		item{
			createdOn: artifact.NullInt64, patternID: artifact.NullInt32, keys: upperWMI,
			vinSchemaID: artifact.NullInt32, wmiID: wmiID, elementID: 27,
			attributeID: mfrID, value: mfrName,
			source: "Manu. Name", priority: 100, toBeQCed: artifact.NullBool,
		},
		item{
			createdOn: artifact.NullInt64, patternID: artifact.NullInt32, keys: upperWMI,
			vinSchemaID: artifact.NullInt32, wmiID: wmiID, elementID: 157,
			attributeID: mfrID, value: mfrID,
			source: "Manu. Id", priority: 100, toBeQCed: artifact.NullBool,
		})

	// C6 (:146-149): the pass's model year.
	if modelYear != 0 {
		y := text(strconv.Itoa(modelYear))
		items = append(items, item{
			createdOn: artifact.NullInt64, patternID: artifact.NullInt32, keys: text(modelYearSource),
			vinSchemaID: artifact.NullInt32, wmiID: artifact.NullInt32, elementID: 29,
			attributeID: y, value: y,
			source: "ModelYear", priority: 100, toBeQCed: artifact.NullBool,
		})
	}

	if items, err = e.formulaItems(items, vk, wlo, whi, modelYear); err != nil {
		return nil, err
	}
	items = dedup(items)

	// C9 (:198-231) reads var_modelId once, after the dedup, and the vehicle
	// specs reuse it.
	model := -1
	for i := range items {
		if items[i].elementID == 28 {
			model = i
			break
		}
	}
	modelID := int32(artifact.NullInt32)
	if model >= 0 {
		if modelID, err = sqlInt4(items[model].attributeID); err != nil {
			return nil, err
		}
	}
	items = e.makeItems(items, model, modelID, varWMI, wlo, whi, now)
	if items, err = e.conversionItems(items); err != nil {
		return nil, err
	}
	return e.specItems(items, modelID, modelYear, wlo, whi)
}

// patternPass is C2 (:75-104): every pattern of every year-band schema of
// an available wmi row whose key matches var_keys, in Pattern.Id order, one
// item per band (the join's rows), priority the band's YearFrom.
func (e *engine) patternPass(vk string, wlo, whi, modelYear int, now int64) ([]item, error) {
	type hit struct {
		row         int32 // pattern row
		band        int32 // wmi_vinschema row
		vinSchemaID int32
		toBeQCed    uint8
	}
	var hits []hit
	var rows []int32
	wvs := &e.wmiVinSchema
	for r := wlo; r < whi; r++ {
		if !e.wmiAvailable(r, now) {
			continue
		}
		lo, hi := artifact.EqualRange(wvs.wmiID, 0, len(wvs.wmiID), e.wmi.id[r])
		for b := lo; b < hi; b++ {
			if !e.inBand(b, modelYear) {
				continue
			}
			sid := wvs.vinSchemaID[b]
			vs, vsEnd := artifact.EqualRange(e.vinSchema.id, 0, len(e.vinSchema.id), sid)
			if vs == vsEnd {
				continue // inner join VinSchema
			}
			qc, notNull := e.vinSchema.toBeQCed.At(vs)
			if qc {
				continue
			}
			var err error
			if rows, err = e.patternIndex(sid).patternHits(vk, rows[:0]); err != nil {
				return nil, fmt.Errorf("decoder: VinSchema %d: %w", sid, err)
			}
			flag := artifact.NullBool
			if notNull {
				flag = 0
			}
			for _, p := range rows {
				hits = append(hits, hit{p, int32(b), sid, flag})
			}
		}
	}
	// ORDER BY P.Id: a pattern in two bands has two rows, in band order.
	slices.SortStableFunc(hits, func(x, y hit) int {
		return cmp.Compare(e.pattern.id[x.row], e.pattern.id[y.row])
	})
	items := make([]item, 0, len(hits)+16)
	for _, h := range hits {
		keys, _ := e.pattern.keys.At(int(h.row))
		attr, _ := e.pattern.attributeID.At(int(h.row))
		items = append(items, item{
			createdOn: e.pattern.changedOn[h.row], patternID: e.pattern.id[h.row],
			keys: text(strings.ToUpper(keys)), vinSchemaID: h.vinSchemaID,
			wmiID: wvs.wmiID[h.band], elementID: e.pattern.elementID[h.row],
			attributeID: text(attr), value: unresolved,
			source: "Pattern", priority: wvs.yearFrom[h.band], toBeQCed: h.toBeQCed,
		})
	}
	return items, nil
}

// engineModelItems is C3 (:106-124): the element-18 pattern item ranked
// first by (Priority DESC, CreatedOn DESC, id DESC) names an engine model,
// matched on lower(trim(name)); each of the model's engine model patterns
// becomes an item under that pattern item's id, keys and schema.
func (e *engine) engineModelItems(items []item, wmiID int32) []item {
	pick := -1
	for i := range items {
		if items[i].elementID != 18 {
			continue
		}
		if pick < 0 {
			pick = i
			continue
		}
		a, b := &items[i], &items[pick]
		c := cmp.Compare(a.priority, b.priority)
		if c == 0 {
			c = -compareCreatedDesc(a.createdOn, b.createdOn)
		}
		if c >= 0 { // a later id wins a full tie
			pick = i
		}
	}
	if pick < 0 || !items[pick].attributeID.valid {
		return items
	}
	from := items[pick]
	emp := &e.engineModelPattern
	for _, id := range e.engineModels[strings.ToLower(trimSpaces(from.attributeID.s))] {
		lo, hi := artifact.EqualRange(emp.engineModelID, 0, len(emp.engineModelID), id)
		for r := lo; r < hi; r++ {
			if !e.elementExists(emp.elementID[r]) {
				continue
			}
			attr, _ := emp.attributeID.At(r)
			items = append(items, item{
				createdOn: emp.changedOn[r], patternID: from.patternID, keys: from.keys,
				vinSchemaID: from.vinSchemaID, wmiID: wmiID, elementID: emp.elementID[r],
				attributeID: text(attr), value: unresolved,
				source: "EngineModelPattern", priority: 50, toBeQCed: artifact.NullBool,
			})
		}
	}
	return items
}

// formulaItems is C7 (:151-174): formula patterns of every schema in a band
// of any wmi row with the WMI (availability and TobeQCed are not checked),
// each schema once, whose key matches formulaKeys; the value is var_keys
// from the key's first '#' to its last.
func (e *engine) formulaItems(items []item, vk string, wlo, whi, modelYear int) ([]item, error) {
	wvs := &e.wmiVinSchema
	var schemas []int32
	for r := wlo; r < whi; r++ {
		lo, hi := artifact.EqualRange(wvs.wmiID, 0, len(wvs.wmiID), e.wmi.id[r])
		for b := lo; b < hi; b++ {
			if e.inBand(b, modelYear) && !slices.Contains(schemas, wvs.vinSchemaID[b]) {
				schemas = append(schemas, wvs.vinSchemaID[b])
			}
		}
	}
	var rows []int32
	for _, sid := range schemas {
		var err error
		if rows, err = e.patternIndex(sid).formulaHits(vk, rows[:0]); err != nil {
			return nil, fmt.Errorf("decoder: VinSchema %d: %w", sid, err)
		}
		for _, r := range rows {
			el := e.pattern.elementID[r]
			if !e.elementExists(el) {
				continue
			}
			keys, _ := e.pattern.keys.At(int(r))
			attr, _ := e.pattern.attributeID.At(int(r))
			items = append(items, item{
				createdOn: e.pattern.changedOn[r], patternID: e.pattern.id[r], keys: text(keys),
				vinSchemaID: sid, wmiID: artifact.NullInt32, elementID: el,
				attributeID: text(attr), value: text(formulaValue(vk, keys)),
				source: "Formula Pattern", priority: 100, toBeQCed: artifact.NullBool,
			})
		}
	}
	return items, nil
}

// formulaValue is SUBSTRING(var_keys, STRPOS(keys, '#'), last - first + 1)
// with first and last the 1-based positions of the key's first and last '#'.
func formulaValue(vk, keys string) string {
	first, last := strings.IndexByte(keys, '#'), strings.LastIndexByte(keys, '#')
	if first < 0 || first >= len(vk) {
		return ""
	}
	return vk[first:min(last+1, len(vk))]
}

// dedup is C8 (:176-196): per element outside dedupExempt, only the item
// ranked first by RANK() over (Priority DESC, CreatedOn DESC, length of keys
// without '*' ASC, keys without '[' and ']' ASC, id ASC) survives. CreatedOn
// DESC puts NULL first, Postgres's default for DESC; keys compare by bytes,
// the C collation; a NULL key is the empty string.
func dedup(items []item) []item {
	best := map[int32]int{}
	for i := range items {
		el := items[i].elementID
		if dedupExempt(el) {
			continue
		}
		if b, ok := best[el]; !ok || dedupCompare(&items[i], &items[b]) < 0 {
			best[el] = i
		}
	}
	out := items[:0]
	for i, it := range items {
		if dedupExempt(it.elementID) || best[it.elementID] == i {
			out = append(out, it)
		}
	}
	return out
}

// dedupCompare orders two items of one element as C8's window does, short
// of the final id ASC, which is the caller's iteration order.
func dedupCompare(a, b *item) int {
	if c := cmp.Compare(b.priority, a.priority); c != 0 {
		return c
	}
	if c := compareCreatedDesc(a.createdOn, b.createdOn); c != 0 {
		return c
	}
	if c := cmp.Compare(keyLength(a.keys.s), keyLength(b.keys.s)); c != 0 {
		return c
	}
	return strings.Compare(stripBrackets(a.keys.s), stripBrackets(b.keys.s))
}

// compareCreatedDesc orders CreatedOn DESC with NULL first.
func compareCreatedDesc(a, b int64) int {
	switch {
	case a == b:
		return 0
	case a == artifact.NullInt64:
		return -1
	case b == artifact.NullInt64:
		return 1
	}
	return cmp.Compare(b, a)
}

// keyLength is the window's LENGTH(REPLACE(keys, '*', <empty>)): characters
// other than '*'.
func keyLength(keys string) int {
	return utf8.RuneCountInString(keys) - strings.Count(keys, "*")
}

func stripBrackets(keys string) string {
	if strings.ContainsAny(keys, "[]") {
		return strings.NewReplacer("[", "", "]", "").Replace(keys)
	}
	return keys
}

// makeItems is C9 (:198-231): with a Model item, every make of the model
// (priority 1000, under the model item's pattern id, keys and schema);
// otherwise the WMI's make when the available wmi rows' Wmi_Make rows join
// exactly one Make row (priority -100). The dedup is not rerun.
func (e *engine) makeItems(items []item, model int, modelID int32, varWMI string, wlo, whi int, now int64) []item {
	if model >= 0 {
		m := items[model]
		lo, hi := artifact.EqualRange(e.makeModel.modelID, 0, len(e.makeModel.modelID), modelID)
		for r := lo; r < hi; r++ {
			id := e.makeModel.makeID[r]
			name, ok := e.makes.nameOf(id)
			if !ok {
				continue
			}
			items = append(items, item{
				createdOn: artifact.NullInt64, patternID: m.patternID, keys: m.keys,
				vinSchemaID: m.vinSchemaID, wmiID: artifact.NullInt32, elementID: 26,
				attributeID: text(itoa(id)), value: text(strings.ToUpper(name)),
				source: "pattern - model", priority: 1000, toBeQCed: artifact.NullBool,
			})
		}
		return items
	}
	type wmiMake struct {
		wmiRow int
		makeID int32
		name   string
	}
	var found []wmiMake
	for r := wlo; r < whi; r++ {
		if !e.wmiAvailable(r, now) {
			continue
		}
		lo, hi := artifact.EqualRange(e.wmiMake.wmiID, 0, len(e.wmiMake.wmiID), e.wmi.id[r])
		for m := lo; m < hi; m++ {
			if name, ok := e.makes.nameOf(e.wmiMake.makeID[m]); ok {
				found = append(found, wmiMake{r, e.wmiMake.makeID[m], name})
			}
		}
	}
	if len(found) != 1 {
		return items
	}
	f := found[0]
	return append(items, item{
		createdOn: e.wmi.changedOn[f.wmiRow], patternID: artifact.NullInt32, keys: text(varWMI),
		vinSchemaID: artifact.NullInt32, wmiID: e.wmi.id[f.wmiRow], elementID: 26,
		attributeID: text(itoa(f.makeID)), value: text(strings.ToUpper(f.name)),
		source: "Make", priority: -100, toBeQCed: artifact.NullBool,
	})
}

// conversionItems is C10 (:233-286): a cursor over items × conversion on
// FromElementId, ordered by (Priority DESC, CreatedOn DESC, conversion id),
// read before the loop inserts anything; each row whose ToElementId no item
// holds yet adds one, valued by convert on the item's AttributeId.
func (e *engine) conversionItems(items []item) ([]item, error) {
	type cursorRow struct {
		item int
		conv int
	}
	cv := &e.conversion
	var rows []cursorRow
	for i := range items {
		lo, hi := artifact.EqualRange(cv.fromElementID, 0, len(cv.fromElementID), items[i].elementID)
		for c := lo; c < hi; c++ {
			if e.elementExists(cv.toElementID[c]) {
				rows = append(rows, cursorRow{i, c})
			}
		}
	}
	slices.SortStableFunc(rows, func(x, y cursorRow) int {
		a, b := &items[x.item], &items[y.item]
		if c := cmp.Compare(b.priority, a.priority); c != 0 {
			return c
		}
		if c := compareCreatedDesc(a.createdOn, b.createdOn); c != 0 {
			return c
		}
		return cmp.Compare(cv.id[x.conv], cv.id[y.conv])
	})
	for _, row := range rows {
		to := cv.toElementID[row.conv]
		if slices.ContainsFunc(items, func(it item) bool { return it.elementID == to }) {
			continue
		}
		from := items[row.item]
		// From items are pattern and engine model pattern rows, whose
		// attributeid is NOT NULL, so the formula text is never NULL.
		formula, _ := cv.formula.At(row.conv)
		result, ok, err := convert(formula, from.attributeID.s)
		if err != nil {
			return nil, fmt.Errorf("decoder: conversion %d on %q: %w", cv.id[row.conv], from.attributeID.s, err)
		}
		r := sqlText{result, ok}
		source := "Conversion " + itoa(cv.id[row.conv]) + ": " + strings.ReplaceAll(formula, "#x#", from.attributeID.s)
		items = append(items, item{
			createdOn: artifact.NullInt64, patternID: from.patternID, keys: from.keys,
			vinSchemaID: from.vinSchemaID, wmiID: from.wmiID, elementID: to,
			attributeID: r, value: r,
			source: leftChars(source, 50), priority: 100, toBeQCed: artifact.NullBool,
		})
	}
	return items, nil
}

// specItems is C11 (:288-379). A VSpecSchemaPattern is a candidate when its
// schema has a make of the WMI (through Wmi_Make of any wmi row with the
// WMI), the Vehicle Type item's type, the model, the year or no years, is not
// QC'd, and the pattern has a key row. It survives when, joining each key
// row to the items of its element with an equal lower(AttributeId), the
// join's row count equals its count of distinct items: every key row matches
// and no two match the same item. The surviving patterns' non-key rows for
// elements no item holds (elements 1, 114, 121, 129, 150, 154, 155, 169 and
// 186 never count as held) become items, one per element, the latest
// ChangedOn first. Postgres leaves a tie on ChangedOn to the executor; ties
// go to the higher VSpecSchemaPattern id, then the higher schema id, then
// the earlier row, as ultravin breaks them.
func (e *engine) specItems(items []item, modelID int32, modelYear int, wlo, whi int) ([]item, error) {
	vt := -1
	for i := range items {
		if items[i].elementID == 39 {
			vt = i
			break
		}
	}
	if vt < 0 || modelID == artifact.NullInt32 {
		return items, nil
	}
	vehicleType, err := sqlInt4(items[vt].attributeID)
	if err != nil || vehicleType == artifact.NullInt32 {
		return items, err
	}
	var makes []int32
	for r := wlo; r < whi; r++ {
		lo, hi := artifact.EqualRange(e.wmiMake.wmiID, 0, len(e.wmiMake.wmiID), e.wmi.id[r])
		makes = append(makes, e.wmiMake.makeID[lo:hi]...)
	}

	type candidate struct {
		schemaPattern, schemaID int32
		toBeQCed                uint8
		lo, hi                  int // its vehiclespecpattern rows
	}
	var candidates []candidate
	ss, ssp, sp := &e.specSchema, &e.specSchemaPattern, &e.specPattern
	mlo, mhi := artifact.EqualRange(e.specSchemaModel.modelID, 0, len(e.specSchemaModel.modelID), modelID)
	for m := mlo; m < mhi; m++ {
		sid := e.specSchemaModel.schemaID[m]
		s, sEnd := artifact.EqualRange(ss.id, 0, len(ss.id), sid)
		if s == sEnd || ss.vehicleTypeID[s] != vehicleType || !slices.Contains(makes, ss.makeID[s]) {
			continue
		}
		qc, notNull := ss.toBeQCed.At(s)
		if qc {
			continue
		}
		ylo, yhi := artifact.EqualRange(e.specSchemaYear.schemaID, 0, len(e.specSchemaYear.schemaID), sid)
		if ylo < yhi && (modelYear == 0 || !slices.Contains(e.specSchemaYear.year[ylo:yhi], int32(modelYear))) {
			continue
		}
		flag := artifact.NullBool
		if notNull {
			flag = 0
		}
		plo, phi := artifact.EqualRange(ssp.schemaID, 0, len(ssp.schemaID), sid)
		for p := plo; p < phi; p++ {
			id := ssp.id[p]
			if slices.ContainsFunc(candidates, func(c candidate) bool { return c.schemaPattern == id }) {
				continue
			}
			lo, hi := artifact.EqualRange(sp.schemaPatternID, 0, len(sp.schemaPatternID), id)
			for r := lo; r < hi; r++ {
				if k, _ := sp.isKey.At(r); k {
					candidates = append(candidates, candidate{id, sid, flag, lo, hi})
					break
				}
			}
		}
	}

	held := map[int32]bool{}
	for i := range items {
		switch el := items[i].elementID; el {
		case 1, 114, 121, 129, 150, 154, 155, 169, 186:
		default:
			held[el] = true
		}
	}
	type specRow struct {
		c   *candidate
		row int
	}
	best := map[int32]specRow{}
	var order []int32
	var matched []int
	for ci := range candidates {
		c := &candidates[ci]
		total := 0
		matched = matched[:0]
		for r := c.lo; r < c.hi; r++ {
			if k, _ := sp.isKey.At(r); !k {
				continue
			}
			attr, _ := sp.attributeID.At(r)
			attr = strings.ToLower(attr)
			n := 0
			for i := range items {
				it := &items[i]
				if it.elementID == sp.elementID[r] && it.attributeID.valid && strings.ToLower(it.attributeID.s) == attr {
					n++
					if !slices.Contains(matched, i) {
						matched = append(matched, i)
					}
				}
			}
			total += max(n, 1)
		}
		if total != len(matched) {
			continue
		}
		for r := c.lo; r < c.hi; r++ {
			el := sp.elementID[r]
			if k, _ := sp.isKey.At(r); k || held[el] {
				continue
			}
			cur, seen := best[el]
			if !seen {
				order = append(order, el)
				best[el] = specRow{c, r}
				continue
			}
			ch := compareCreatedDesc(sp.changedOn[r], sp.changedOn[cur.row])
			if ch == 0 {
				ch = cmp.Compare(cur.c.schemaPattern, c.schemaPattern)
			}
			if ch == 0 {
				ch = cmp.Compare(cur.c.schemaID, c.schemaID)
			}
			if ch < 0 {
				best[el] = specRow{c, r}
			}
		}
	}
	for _, el := range order {
		b := best[el]
		attr, _ := sp.attributeID.At(b.row)
		items = append(items, item{
			createdOn: sp.changedOn[b.row], patternID: b.c.schemaPattern, keys: text(""),
			vinSchemaID: b.c.schemaID, wmiID: artifact.NullInt32, elementID: el,
			attributeID: text(attr), value: unresolved,
			source: "Vehicle Specs", priority: -100, toBeQCed: b.c.toBeQCed,
		})
	}
	return items, nil
}

// defaultItems is C16 (:458-476): the DefaultValue rows of the Vehicle Type
// item's type for elements no item holds, value 'Not Applicable' for a
// lookup element defaulting to '0', distinct.
func (e *engine) defaultItems(items []item) ([]item, error) {
	vt := -1
	for i := range items {
		if items[i].elementID == 39 {
			vt = i
			break
		}
	}
	if vt < 0 {
		return items, nil
	}
	vehicleType, err := sqlInt4(items[vt].attributeID)
	if err != nil || vehicleType == artifact.NullInt32 {
		return items, err
	}
	held := map[int32]bool{}
	for i := range items {
		held[items[i].elementID] = true
	}
	n := len(items)
	dv := &e.defaultValue
	lo, hi := artifact.EqualRange(dv.vehicleTypeID, 0, len(dv.vehicleTypeID), vehicleType)
	for r := lo; r < hi; r++ {
		el := dv.elementID[r]
		value, ok := dv.value.At(r)
		if !ok || held[el] || !e.elementExists(el) {
			continue
		}
		v := unresolved
		if dt, _ := e.element.datatype.At(int(e.element.row[el])); dt == "lookup" && value == "0" {
			v = text("Not Applicable")
		}
		it := item{
			createdOn: dv.changedOn[r], patternID: artifact.NullInt32, keys: sqlText{},
			vinSchemaID: artifact.NullInt32, wmiID: artifact.NullInt32, elementID: el,
			attributeID: text(value), value: v,
			source: "Default", priority: 10, toBeQCed: artifact.NullBool,
		}
		if !slices.Contains(items[n:], it) {
			items = append(items, it)
		}
	}
	return items, nil
}

// sqlInt4 is a varchar cast to integer: NULL stays NULL, surrounding
// whitespace is allowed, anything else that is not a decimal int4 raises.
func sqlInt4(t sqlText) (int32, error) {
	if !t.valid {
		return artifact.NullInt32, nil
	}
	n, err := strconv.ParseInt(strings.Trim(t.s, " \t\n\r\v\f"), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("decoder: invalid input syntax for type integer: %q", t.s)
	}
	return int32(n), nil
}

// trimSpaces is Postgres's trim(): spaces only.
func trimSpaces(s string) string { return strings.Trim(s, " ") }

// leftChars is left(s, n): the first n characters.
func leftChars(s string, n int) string {
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

func itoa(n int32) string { return strconv.Itoa(int(n)) }
