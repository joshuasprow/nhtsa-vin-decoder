package contract

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Translator answers translate and list requests (ADR 0012) through loaded
// files without a decoder. Translation's core is pkg/vocab's and
// decoder-free (ADR 0013): a vocab.Set over a data month, and a decode's
// items or none (vocab.NewDecoded). A Translator turns its answers into
// the contract's, and Decode is the decoder it is given: pkg/nitrovin's
// Translator gives it the engine, the browser's Service Worker /v1/decode.
// It is safe for concurrent use when Decode is.
type Translator struct {
	Set *vocab.Set
	// Decode decodes the VIN a translate request names, as from or as
	// context, with the request's model year as its hint, at its now
	// (truncated to the second). Its error is the translation's. Only a
	// request naming a VIN calls it.
	Decode func(vin string, modelYear Null[int32], now time.Time) (VINDecode, error)
	// DataMonth and DecoderVersion are what an answer carries when no
	// decode gave them: every list, and a translation without a VIN.
	DataMonth      string
	DecoderVersion string
}

// VINDecode is a VIN's decode as a translation takes it: the projection,
// and the data month and decoder version that gave it, which the
// translation answers with.
type VINDecode struct {
	Projection
	DataMonth      string
	DecoderVersion string
}

// Translate answers one translate request: from a VIN, a vocabulary entry
// or a spine entry, through exactly one spine, to the target vocabularies.
// A VIN, as from or as context, is decoded (Decode); its decode checks
// qualifiers and gives the year. When req.Now is zero it reads the wall
// clock once. Nothing to translate is never an error: the error is a
// *Problem, an InvalidRequest for a request Validate refuses or that
// names a vocabulary, entry field or spine the loaded files lack, Decode's
// error, or an Internal for a fault in nitrovin.
func (t Translator) Translate(req TranslateRequest) (Translation, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	if err := req.Validate(); err != nil {
		return Translation{}, err
	}
	now := req.Now.UTC().Truncate(time.Second)
	q := vocab.Query{
		From: vocab.From{
			VIN:        req.From.VIN != "",
			Vocabulary: req.From.Vocabulary,
			Entry:      req.From.Entry,
			Ref:        vocabRef(req.From.SpineRef),
		},
		To: req.To,
	}
	if req.ModelYear.Valid {
		q.ModelYear = new(int(req.ModelYear.V))
	}
	dataMonth, decoderVersion := t.DataMonth, t.DecoderVersion
	var vehicle *Vehicle
	if vin := req.From.VIN + req.VIN; vin != "" { // Validate allows one
		d, err := t.Decode(vin, req.ModelYear, now)
		if err != nil {
			return Translation{}, AsProblem(err)
		}
		dataMonth, decoderVersion = d.DataMonth, d.DecoderVersion
		q.Decoded = vocab.NewDecoded(t.Set.Month(), vocabItems(d.Items))
		vehicle = newVehicle(d.VIN, q.Decoded)
	}
	tr, err := t.Set.Translate(q)
	if err != nil {
		return Translation{}, vocabProblem(err)
	}

	out := Translation{
		From:           TranslationFrom{Status: FromStatus(tr.Status)},
		Vehicle:        vehicle,
		Spine:          make([]SpineEntry, len(tr.Spine)),
		To:             make([]Translated, len(tr.To)),
		Vocabularies:   fileVersions(tr.Files),
		Now:            now,
		DataMonth:      dataMonth,
		DecoderVersion: decoderVersion,
	}
	switch {
	case vehicle != nil && req.From.VIN != "":
		out.From.VIN = vehicle.VIN
	case req.From.Vocabulary != "":
		out.From.Vocabulary, out.From.Entry = req.From.Vocabulary, req.From.Entry
		if tr.Entry != nil {
			out.From.Entry = tr.Vocabulary.FieldsOf(tr.Entry)
			out.From.Note = tr.Entry.Note
		}
	default:
		out.From.SpineRef = req.From.SpineRef
	}
	for i, it := range tr.Spine {
		out.Spine[i] = spineEntry(it)
	}
	for i, to := range tr.To {
		c := Translated{Vocabulary: to.Vocabulary.Name, Entries: make([]TranslatedEntry, len(to.Matches))}
		for j, m := range to.Matches {
			c.Entries[j] = TranslatedEntry{
				Entry:       to.Vocabulary.FieldsOf(m.Entry),
				Note:        m.Entry.Note,
				Via:         vias(m.Via),
				Unconfirmed: unconfirmed(m.Unconfirmed),
			}
		}
		out.To[i] = c
	}
	return out, nil
}

// TranslateBatch translates requests in order, reading the wall clock once
// for every request whose Now is zero, as the contract fixes for one HTTP
// request. It stops at the first error, a *Problem whose pointer names the
// request ("/3/to/0").
func (t Translator) TranslateBatch(reqs []TranslateRequest) ([]Translation, error) {
	now := time.Now()
	out := make([]Translation, len(reqs))
	for i, req := range reqs {
		if req.Now.IsZero() {
			req.Now = now
		}
		tr, err := t.Translate(req)
		if err != nil {
			p := *AsProblem(err)
			p.Pointer = fmt.Sprintf("/%d%s", i, p.Pointer)
			return nil, &p
		}
		out[i] = tr
	}
	return out, nil
}

// List answers one list request: the catalog without Of, else one page of
// vPIC's makes, a make's models or a model's values for an element, a
// vocabulary's entries with their mappings, a declared spine's entries or
// an extension set's extensions. The error is a *Problem, an
// InvalidRequest for an Of nothing loaded has, a where field the listing
// lacks or a vPIC where it refuses (ADR 0015), a limit out of range or an
// after it never gave.
func (t Translator) List(req ListRequest) (List, error) {
	l, err := t.Set.List(vocab.ListQuery{Of: req.Of, Where: req.Where, Limit: req.Limit, After: req.After})
	if err != nil {
		return List{}, vocabProblem(err)
	}
	out := List{
		Of:             req.Of,
		Entries:        []ListEntry{},
		Next:           l.Next,
		Vocabularies:   []FileVersion{},
		DataMonth:      t.DataMonth,
		DecoderVersion: t.DecoderVersion,
	}
	switch {
	case req.Of == "":
		out.Vocabularies = fileVersions(t.Set.Files())
		for _, c := range l.Catalog {
			out.Entries = append(out.Entries, CatalogEntry{
				Name: c.Name, Kind: string(c.Kind), Version: c.Version, Subject: string(c.Subject), Fields: c.Fields, Spine: c.Spine,
			})
		}
	case l.File != nil:
		out.Vocabularies = fileVersions([]*vocab.File{l.File})
	}
	for _, e := range l.Entries {
		out.Entries = append(out.Entries, vocabularyEntry(l.File, e))
	}
	for _, it := range l.Spine {
		out.Entries = append(out.Entries, spineEntry(it))
	}
	for _, v := range l.ModelValues {
		e := ModelValueEntry{Value: QualifierValue{IsText: !v.Lookup, Text: v.Value}, Name: v.Name}
		if v.Lookup {
			id, _ := strconv.ParseInt(v.Value, 10, 32) // vocab lists decimal ids only
			e.Value = QualifierValue{Attribute: int32(id)}
		}
		out.Entries = append(out.Entries, e)
	}
	return out, nil
}

// vocabProblem is a vocab.RequestError as the InvalidRequest it is,
// anything else as Internal.
func vocabProblem(err error) *Problem {
	if r, ok := errors.AsType[*vocab.RequestError](err); ok {
		return NewProblem(InvalidRequest, r.Pointer, r.Detail)
	}
	return AsProblem(err)
}

// vocabItems are a decode's items as a translation reads them.
func vocabItems(items []Item) []vocab.Item {
	out := make([]vocab.Item, len(items))
	for i, it := range items {
		out[i] = vocab.Item{ElementID: it.ElementID, Value: it.Value.V, AttributeID: it.AttributeID.V}
	}
	return out
}

// newVehicle is the vehicle a translation answers with: the VIN as
// decoded, and the make, model and year its decode gave.
func newVehicle(vin string, d *vocab.Decoded) *Vehicle {
	v := &Vehicle{VIN: vin}
	if d.Make != 0 {
		v.Make = Value(d.Make)
	}
	if d.Model != 0 {
		v.Model = Value(d.Model)
	}
	if d.ModelYear != nil {
		v.ModelYear = Value(int32(*d.ModelYear))
	}
	return v
}

func vocabRef(r SpineRef) vocab.Ref {
	return vocab.Ref{Spine: r.Spine, Target: vocab.Target{Make: r.Make, Model: r.Model, Ext: r.Ext, ID: r.ID}}
}

func spineRef(r vocab.Ref) SpineRef {
	return SpineRef{Make: r.Make, Model: r.Model, Ext: r.Ext, Spine: r.Spine, ID: r.ID}
}

func spineEntry(it vocab.Reached) SpineEntry {
	e := SpineEntry{
		SpineRef:    spineRef(it.Ref),
		Name:        it.Name,
		Drift:       it.Drift,
		Via:         vias(it.Via),
		Unconfirmed: unconfirmed(it.Unconfirmed),
	}
	if it.Under != nil {
		u := spineRef(*it.Under)
		e.Under = &u
	}
	for _, r := range it.Years {
		y := YearRange{First: int32(r.First)}
		if r.Last != 0 {
			y.Last = Value(int32(r.Last))
		}
		e.Years = append(e.Years, y)
	}
	return e
}

func vias(vs []vocab.Via) []Via {
	if len(vs) == 0 {
		return nil
	}
	out := make([]Via, len(vs))
	for i, v := range vs {
		out[i] = Via{
			SpineRef:    spineRef(v.Ref),
			First:       year(v.Mapping.First),
			Last:        year(v.Mapping.Last),
			Origin:      v.Mapping.Origin,
			Note:        v.Mapping.Note,
			Unconfirmed: unconfirmed(v.Unconfirmed),
		}
	}
	return out
}

func year(y *int) Null[int32] {
	if y == nil {
		return Null[int32]{}
	}
	return Value(int32(*y))
}

func unconfirmed(u vocab.Unconfirmed) Unconfirmed {
	return Unconfirmed{Elements: u.Elements, ModelYear: u.ModelYear}
}

// vocabularyEntry is one entry of vocabulary f as the file has it.
func vocabularyEntry(f *vocab.File, e *vocab.Entry) VocabularyEntry {
	out := VocabularyEntry{Entry: f.FieldsOf(e), Mappings: make([]VocabularyMapping, len(e.Mappings)), Note: e.Note}
	for i, mp := range e.Mappings {
		m := VocabularyMapping{
			Make: mp.Make, Model: mp.Model, Ext: mp.Ext, ID: mp.ID,
			First: year(mp.First), Last: year(mp.Last), Origin: mp.Origin, Note: mp.Note,
		}
		if len(mp.Qualifiers) > 0 {
			m.Qualifiers = qualifierValues(mp.Qualifiers)
		}
		out.Mappings[i] = m
	}
	return out
}

func fileVersions(files []*vocab.File) []FileVersion {
	out := make([]FileVersion, len(files))
	for i, f := range files {
		out[i] = FileVersion{Name: f.Name, Version: f.Version}
	}
	return out
}

// qualifierValues are qualifiers as a file writes them: element id (as a
// decimal string) → values.
func qualifierValues(qs []vocab.Qualifier) map[string][]QualifierValue {
	out := make(map[string][]QualifierValue, len(qs))
	for _, q := range qs {
		vs := make([]QualifierValue, len(q.Values))
		for j, v := range q.Values {
			vs[j] = QualifierValue{IsText: v.IsText, Attribute: v.Attribute, Text: v.Text}
		}
		out[strconv.Itoa(int(q.Element))] = vs
	}
	return out
}
