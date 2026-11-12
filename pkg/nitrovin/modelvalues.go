package nitrovin

import (
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/decoder"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

var _ vocab.ModelMonth = (*month)(nil)

// Elements whose values come from the WMI and the model (elementModel
// among them), not from patterns: the pattern pass skips them (decoder's
// excludedElement), as it does Model Year (29); and Engine Model, whose
// patterns name the engine model rows.
const (
	elementMake         = 26
	elementManufacturer = 27
	elementModelYear    = 29
	elementVehicleType  = 39
	elementEngineModel  = 18
)

// ModelValues is model's model values for element (ADR 0015), each once,
// in no order: what a VIN that decodes to model can decode element to,
// lookup elements' attribute ids in decimal. A value counts when
//
//   - a pattern of element's key overlaps one of model's Model (28) pattern
//     keys (decoder.Key.Overlaps) in the same VIN schema, or in a schema one
//     of its WMIs reaches in an overlapping year band: a pass with a model
//     year reads them together;
//   - an engine model row of element hangs off an Engine Model (18) pattern
//     whose key overlaps one of them;
//   - a vehicle spec names model, its make is one the model's WMIs carry and
//     its vehicle type one they have;
//   - a default value is for a vehicle type the model's WMIs have.
//
// The model's WMIs are those whose VIN schemas hold one of its Model
// patterns, with every WMI row of the same WMI. Make (26) is the model's
// makes, Manufacturer Name (27) and Vehicle Type (39) its WMIs', Model (28)
// the model itself. VIN schemas and spec schemas held for QC, which a
// decode skips, add nothing; nor do values a formula pattern computes
// from the VIN, or a conversion from another element. An unknown model has
// none.
//
// The index is built per element on first ask and kept, as the month's
// values are.
func (m *month) ModelValues(model, element int32) []string {
	w, ok := m.modelBase().wmis[model]
	if !ok {
		return nil
	}
	switch element {
	case elementModel:
		return []string{strconv.Itoa(int(model))}
	case elementMake:
		var out []string
		lo, hi := artifact.EqualRange(m.modelIDs, 0, len(m.modelIDs), model)
		for r := lo; r < hi; r++ {
			if _, ok := m.Make(m.modelMake[r]); ok {
				out = append(out, strconv.Itoa(int(m.modelMake[r])))
			}
		}
		return out
	case elementManufacturer:
		return itoas(w.manufacturers)
	case elementVehicleType:
		return itoas(w.types)
	case elementModelYear:
		return nil // the year is the pass's, not the model's
	}
	return m.modelIndex(element)()[model]
}

// AttributeName is the name of lookup element's attribute id, from the
// lookup table the element names.
func (m *month) AttributeName(element, id int32) (string, bool) {
	t, ok := m.lookupTables()[element]
	if !ok {
		return "", false
	}
	return m.name(t, id)
}

// lookupTables is, per element that names a lookup table the artifact
// carries, that table's rows in the lookup union.
func (m *month) lookupTables() map[int32][2]int {
	m.lookupOnce.Do(func() {
		t := m.a.Table("lookup")
		tables := t.Strings("table")
		el := m.a.Table("element")
		names := el.Strings("lookuptable")
		m.lookups = map[int32][2]int{}
		for r, id := range el.Int32s("id") {
			name, ok := names.At(r)
			if !ok || name == "" {
				continue
			}
			lo, hi := tables.EqualRange(0, t.Rows(), strings.ToLower(name))
			if lo < hi {
				m.lookups[id] = [2]int{lo, hi}
			}
		}
	})
	return m.lookups
}

func itoas(ids []int32) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.Itoa(int(id))
	}
	return out
}

// modelIndex is element's index, model → values, built by its first call.
func (m *month) modelIndex(element int32) func() map[int32][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.modelValues[element]
	if !ok {
		f = sync.OnceValue(func() map[int32][]string { return m.buildModelIndex(element) })
		m.modelValues[element] = f
	}
	return f
}

// modelBase is what every element's index shares: each VIN schema's Model
// pattern keys and the schemas a decode can read beside it, and each
// model's WMIs' makes, vehicle types and manufacturers.
type modelBase struct {
	schemas []schemaModels
	wmis    map[int32]*modelWMIs // by model id
	// engineModels is the engine model ids by lower(trim(name)), as an
	// Engine Model pattern's attribute names them.
	engineModels map[string][]int32
}

// schemaModels is one VIN schema's distinct Model keys, with the models
// each names, and its partners: the other schemas one of its WMIs reaches
// in a year band overlapping its own, whose patterns a decode reads in the
// same pass as its Model patterns.
type schemaModels struct {
	id       int32
	keys     []modelKey
	partners []int32
}

type modelKey struct {
	key    decoder.Key
	models []int32
}

// modelWMIs is what a model's WMIs carry, each set ascending.
type modelWMIs struct {
	makes, types, manufacturers []int32
}

func (m *month) buildModelBase() *modelBase {
	a := m.a
	b := &modelBase{wmis: map[int32]*modelWMIs{}, engineModels: map[string][]int32{}}

	// The VIN schemas a WMI reaches, by schema; a schema held for QC is
	// never read.
	wvs := a.Table("wmi_vinschema")
	wvsWMIs, wvsSchemas := wvs.Int32s("wmiid"), wvs.Int32s("vinschemaid")
	reached := map[int32][]int32{}
	for r, sid := range wvsSchemas {
		reached[sid] = append(reached[sid], wvsWMIs[r])
	}
	vs := a.Table("vinschema")
	qc := vs.Bools("tobeqced")
	for r, sid := range vs.Int32s("id") {
		if held, _ := qc.At(r); held {
			delete(reached, sid)
		}
	}

	p := a.Table("pattern")
	schemaIDs, elements := p.Int32s("vinschemaid"), p.Int32s("elementid")
	keys, attrs := p.Strings("keys"), p.Strings("attributeid")
	wmisOf := map[int32]map[int32]bool{} // model → WMI ids
	index := map[int32]int{}             // schema id → its b.schemas index
	byRef := map[uint32]int{}            // a schema's key ref → its keys index
	var models []int32                   // a schema's models
	for lo := 0; lo < len(schemaIDs); {
		sid := schemaIDs[lo]
		_, hi := artifact.EqualRange(schemaIDs, lo, len(schemaIDs), sid)
		wmis, ok := reached[sid]
		if !ok {
			lo = hi
			continue
		}
		s := schemaModels{id: sid}
		clear(byRef)
		models = models[:0]
		for r := lo; r < hi; r++ {
			if elements[r] != elementModel {
				continue
			}
			attr, _ := attrs.At(r)
			id, err := strconv.ParseInt(strings.TrimSpace(attr), 10, 32)
			if err != nil {
				continue // the decode raises on it
			}
			model := int32(id)
			k, seen := byRef[keys.Ref(r)]
			if !seen {
				key, _ := keys.At(r)
				k = len(s.keys)
				byRef[keys.Ref(r)] = k
				s.keys = append(s.keys, modelKey{key: decoder.CompileKey(key)})
			}
			if !slices.Contains(s.keys[k].models, model) {
				s.keys[k].models = append(s.keys[k].models, model)
				models = append(models, model)
			}
		}
		if len(s.keys) == 0 {
			lo = hi
			continue
		}
		index[sid] = len(b.schemas)
		b.schemas = append(b.schemas, s)
		slices.Sort(models)
		for _, model := range slices.Compact(models) {
			if wmisOf[model] == nil {
				wmisOf[model] = map[int32]bool{}
			}
			for _, w := range wmis {
				wmisOf[model][w] = true
			}
		}
		lo = hi
	}

	wmi := a.Table("wmi")
	wmiStrings, wmiIDs := wmi.Strings("wmi"), wmi.Int32s("id")
	rowOf := make(map[int32]int, len(wmiIDs))
	for r, id := range wmiIDs {
		rowOf[id] = r
	}

	// A pass reads the patterns of every schema in a band of every WMI row
	// with the VIN's WMI that holds its model year: the schemas whose
	// bands overlap under one WMI are partners.
	type band struct{ schema, from, to int32 }
	bands := map[string][]band{}
	from, to := wvs.Int32s("yearfrom"), wvs.Int32s("yearto")
	for r, sid := range wvsSchemas {
		w, ok := rowOf[wvsWMIs[r]]
		if _, reach := reached[sid]; !ok || !reach {
			continue
		}
		s, _ := wmiStrings.At(w)
		end := to[r]
		if end == artifact.NullInt32 {
			end = 2999
		}
		bands[s] = append(bands[s], band{sid, from[r], end})
	}
	partners := make([]map[int32]bool, len(b.schemas))
	for _, bs := range bands {
		for _, x := range bs {
			i, ok := index[x.schema]
			if !ok {
				continue
			}
			for _, y := range bs {
				if y.schema == x.schema || y.to < x.from || x.to < y.from {
					continue
				}
				if partners[i] == nil {
					partners[i] = map[int32]bool{}
				}
				partners[i][y.schema] = true
			}
		}
	}
	for i, ps := range partners {
		b.schemas[i].partners = slices.Sorted(maps.Keys(ps))
	}

	// Every WMI row with the same WMI as one the schemas reach: a decode
	// reads them all for its makes and vehicle type.
	types, manufacturers := wmi.Int32s("vehicletypeid"), wmi.Int32s("manufacturerid")
	wm := a.Table("wmi_make")
	wmWMI, wmMake := wm.Int32s("wmiid"), wm.Int32s("makeid")
	for model, wmis := range wmisOf {
		makes, vts, mfrs := map[int32]bool{}, map[int32]bool{}, map[int32]bool{}
		done := map[string]bool{}
		for w := range wmis {
			r, ok := rowOf[w]
			if !ok {
				continue
			}
			s, _ := wmiStrings.At(r)
			if done[s] {
				continue
			}
			done[s] = true
			lo, hi := wmiStrings.EqualRange(0, len(wmiIDs), s)
			for r := lo; r < hi; r++ {
				if types[r] != artifact.NullInt32 {
					vts[types[r]] = true
				}
				if manufacturers[r] != artifact.NullInt32 {
					mfrs[manufacturers[r]] = true
				}
				mlo, mhi := artifact.EqualRange(wmWMI, 0, len(wmWMI), wmiIDs[r])
				for _, mk := range wmMake[mlo:mhi] {
					makes[mk] = true
				}
			}
		}
		b.wmis[model] = &modelWMIs{
			makes:         slices.Sorted(maps.Keys(makes)),
			types:         slices.Sorted(maps.Keys(vts)),
			manufacturers: slices.Sorted(maps.Keys(mfrs)),
		}
	}

	em := a.Table("enginemodel")
	names := em.Strings("name")
	for r, id := range em.Int32s("id") {
		name, _ := names.At(r)
		k := strings.ToLower(strings.Trim(name, " "))
		b.engineModels[k] = append(b.engineModels[k], id)
	}
	return b
}

// buildModelIndex is element's model values for every model, as
// ModelValues describes them.
func (m *month) buildModelIndex(element int32) map[int32][]string {
	b, sets := m.modelBase(), modelSets{}
	m.patternValues(b, element, sets)
	m.specValues(b, element, sets)
	m.defaultValues(b, element, sets)
	out := make(map[int32][]string, len(sets))
	for model, s := range sets {
		out[model] = slices.Collect(maps.Keys(s))
	}
	return out
}

// modelSets is model values as they are collected: model → value → true.
type modelSets map[int32]map[string]bool

func (s modelSets) add(model int32, v string) {
	vs := s[model]
	if vs == nil {
		vs = map[string]bool{}
		s[model] = vs
	}
	vs[v] = true
}

// patternValues adds element's values from patterns, and from engine
// model rows through Engine Model patterns: a pattern's values count for
// the models of every Model key, in its own schema or a partner, its key
// overlaps.
func (m *month) patternValues(b *modelBase, element int32, sets modelSets) {
	a := m.a
	// The engine model rows of element, by engine model id.
	byEngine := map[int32][]string{}
	emp := a.Table("enginemodelpattern")
	empModels, empValues := emp.Int32s("enginemodelid"), emp.Strings("attributeid")
	for r, el := range emp.Int32s("elementid") {
		if el != element {
			continue
		}
		if v, ok := empValues.At(r); ok {
			byEngine[empModels[r]] = append(byEngine[empModels[r]], v)
		}
	}

	// Each reached schema's keys of element, each with the values its
	// rows give.
	type group struct {
		key    decoder.Key
		values []string
	}
	p := a.Table("pattern")
	schemaIDs, elements := p.Int32s("vinschemaid"), p.Int32s("elementid")
	keys, attrs := p.Strings("keys"), p.Strings("attributeid")
	bySchema := map[int32][]group{}
	at := map[[2]uint32]int{} // (schema, key ref) → its group in bySchema, -1 for a formula key
	for r, el := range elements {
		if el != element && (el != elementEngineModel || len(byEngine) == 0) {
			continue
		}
		attr, ok := attrs.At(r)
		if !ok {
			continue
		}
		sid := schemaIDs[r]
		k := [2]uint32{uint32(sid), keys.Ref(r)}
		g, seen := at[k]
		if !seen {
			key, _ := keys.At(r)
			g = -1
			if strings.IndexByte(key, '#') < 0 { // a formula pattern's value is the VIN's
				g = len(bySchema[sid])
				bySchema[sid] = append(bySchema[sid], group{key: decoder.CompileKey(key)})
			}
			at[k] = g
		}
		if g < 0 {
			continue
		}
		vs := &bySchema[sid][g].values
		if el == element {
			*vs = append(*vs, attr)
			continue
		}
		for _, id := range b.engineModels[strings.ToLower(strings.Trim(attr, " "))] {
			*vs = append(*vs, byEngine[id]...)
		}
	}
	for _, s := range b.schemas {
		for i := -1; i < len(s.partners); i++ {
			sid := s.id
			if i >= 0 {
				sid = s.partners[i]
			}
			for _, g := range bySchema[sid] {
				for _, mk := range s.keys {
					if !g.key.Overlaps(mk.key) {
						continue
					}
					for _, model := range mk.models {
						for _, v := range g.values {
							sets.add(model, v)
						}
					}
				}
			}
		}
	}
}

// specValues adds element's values from vehicle specs: a spec pattern
// with a key row gives its other rows of element to the models its schema
// names, when the schema's make is one the model's WMIs carry and its
// vehicle type one they have. The key rows are not evaluated.
func (m *month) specValues(b *modelBase, element int32, sets modelSets) {
	a := m.a
	sp := a.Table("vehiclespecpattern")
	spIDs, spKey, spElements, spValues := sp.Int32s("vspecschemapatternid"), sp.Bools("iskey"), sp.Int32s("elementid"), sp.Strings("attributeid")
	bySpecPattern := map[int32][]string{}
	for lo := 0; lo < len(spIDs); {
		_, hi := artifact.EqualRange(spIDs, lo, len(spIDs), spIDs[lo])
		var vs []string
		keyed := false
		for r := lo; r < hi; r++ {
			if k, _ := spKey.At(r); k {
				keyed = true
			} else if spElements[r] == element {
				if v, ok := spValues.At(r); ok {
					vs = append(vs, v)
				}
			}
		}
		if keyed && len(vs) > 0 {
			bySpecPattern[spIDs[lo]] = vs
		}
		lo = hi
	}
	if len(bySpecPattern) == 0 {
		return
	}
	ssp := a.Table("vspecschemapattern")
	sspIDs := ssp.Int32s("id")
	bySchema := map[int32][]string{}
	for r, sid := range ssp.Int32s("schemaid") {
		bySchema[sid] = append(bySchema[sid], bySpecPattern[sspIDs[r]]...)
	}
	ss := a.Table("vehiclespecschema")
	ssIDs, ssMakes, ssTypes, ssQC := ss.Int32s("id"), ss.Int32s("makeid"), ss.Int32s("vehicletypeid"), ss.Bools("tobeqced")
	sm := a.Table("vehiclespecschema_model")
	smSchemas := sm.Int32s("vehiclespecschemaid")
	for r, model := range sm.Int32s("modelid") {
		sid := smSchemas[r]
		vs := bySchema[sid]
		w := b.wmis[model]
		if len(vs) == 0 || w == nil {
			continue
		}
		i, j := artifact.EqualRange(ssIDs, 0, len(ssIDs), sid)
		if i == j {
			continue
		}
		if held, _ := ssQC.At(i); held || !slices.Contains(w.makes, ssMakes[i]) || !slices.Contains(w.types, ssTypes[i]) {
			continue
		}
		for _, v := range vs {
			sets.add(model, v)
		}
	}
}

// defaultValues adds element's default values to every model whose WMIs
// have the default's vehicle type.
func (m *month) defaultValues(b *modelBase, element int32, sets modelSets) {
	dv := m.a.Table("defaultvalue")
	dvTypes, dvElements, dvValues := dv.Int32s("vehicletypeid"), dv.Int32s("elementid"), dv.Strings("defaultvalue")
	byType := map[int32][]string{}
	for r, el := range dvElements {
		if el != element {
			continue
		}
		if v, ok := dvValues.At(r); ok {
			byType[dvTypes[r]] = append(byType[dvTypes[r]], v)
		}
	}
	if len(byType) == 0 {
		return
	}
	for model, w := range b.wmis {
		for _, vt := range w.types {
			for _, v := range byType[vt] {
				sets.add(model, v)
			}
		}
	}
}
