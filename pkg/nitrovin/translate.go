package nitrovin

import (
	"errors"
	"strconv"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Element ids a translation reads from a decode.
const (
	elementMake      = 26
	elementModel     = 28
	elementModelYear = 29
)

// Translate answers one translate request (ADR 0012): from a VIN, a
// vocabulary entry or a spine entry, through exactly one spine, to the
// target vocabularies. A VIN, as from or as context, is decoded with the
// request's model year as its hint; its decode checks qualifiers and gives
// the year. When req.Now is zero it reads the wall clock once. Nothing to
// translate is never an error: the error is a *contract.Problem, an
// InvalidRequest for a request Validate refuses or that names a
// vocabulary, entry field or spine the loaded files lack, an Internal for
// a fault in nitrovin.
func (t *Translator) Translate(req contract.TranslateRequest) (contract.Translation, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	if err := req.Validate(); err != nil {
		return contract.Translation{}, err
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
		y := int(req.ModelYear.V)
		q.ModelYear = &y
	}
	var vehicle *contract.Vehicle
	if vin := req.From.VIN + req.VIN; vin != "" { // Validate allows one
		p, err := t.d.engine.Project(vin, req.ModelYear, now)
		if err != nil {
			return contract.Translation{}, contract.AsProblem(err)
		}
		vehicle, q.Decoded = decoded(p, t.d.info.Elements)
	}
	tr, err := t.set.Translate(q)
	if err != nil {
		return contract.Translation{}, problem(err)
	}

	out := contract.Translation{
		From:           contract.TranslationFrom{Status: contract.FromStatus(tr.Status)},
		Vehicle:        vehicle,
		Spine:          make([]contract.SpineEntry, len(tr.Spine)),
		To:             make([]contract.Translated, len(tr.To)),
		Vocabularies:   fileVersions(tr.Files),
		Now:            now,
		DataMonth:      t.info.DataMonth,
		DecoderVersion: t.info.DecoderVersion,
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
		c := contract.Translated{Vocabulary: to.Vocabulary.Name, Entries: make([]contract.TranslatedEntry, len(to.Matches))}
		for j, m := range to.Matches {
			c.Entries[j] = contract.TranslatedEntry{
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

// List answers one list request (ADR 0012): the catalog without Of, else
// one page of vPIC's makes or a make's models, a vocabulary's entries with
// their mappings, a declared spine's entries or an extension set's
// extensions. The error is a *contract.Problem, an InvalidRequest for an
// Of nothing loaded has, a where field the listing lacks, a limit out of
// range or an after it never gave.
func (t *Translator) List(req contract.ListRequest) (contract.List, error) {
	l, err := t.set.List(vocab.ListQuery{Of: req.Of, Where: req.Where, Limit: req.Limit, After: req.After})
	if err != nil {
		return contract.List{}, problem(err)
	}
	out := contract.List{
		Of:             req.Of,
		Entries:        []contract.ListEntry{},
		Next:           l.Next,
		Vocabularies:   []contract.FileVersion{},
		DataMonth:      t.info.DataMonth,
		DecoderVersion: t.info.DecoderVersion,
	}
	switch {
	case req.Of == "":
		out.Vocabularies = fileVersions(t.set.Files())
		for _, c := range l.Catalog {
			out.Entries = append(out.Entries, contract.CatalogEntry{
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
	return out, nil
}

// problem is a vocab.RequestError as the InvalidRequest it is, anything
// else as Internal.
func problem(err error) *contract.Problem {
	if r, ok := errors.AsType[*vocab.RequestError](err); ok {
		return contract.NewProblem(contract.InvalidRequest, r.Pointer, r.Detail)
	}
	return contract.AsProblem(err)
}

// decoded reads a VIN's projection as a translation does: the vehicle it
// answers with, and the values qualifiers are checked against, attribute
// ids for lookup elements and text for the others.
func decoded(p contract.Projection, elements contract.Elements) (*contract.Vehicle, *vocab.Decoded) {
	v := &contract.Vehicle{VIN: p.VIN}
	d := &vocab.Decoded{Values: map[int32][]vocab.Value{}}
	for _, it := range p.Items {
		if !it.Value.Valid || it.Value.V == "" {
			continue
		}
		e, ok := elements.Find(it.ElementID)
		if !ok {
			continue
		}
		if e.DataType != "lookup" {
			d.Values[e.ElementID] = append(d.Values[e.ElementID], vocab.Value{IsText: true, Text: it.Value.V})
			if e.ElementID == elementModelYear {
				if y, err := strconv.Atoi(it.Value.V); err == nil {
					d.ModelYear = &y
					v.ModelYear = contract.Value(int32(y))
				}
			}
			continue
		}
		if !it.AttributeID.Valid {
			continue
		}
		id, err := strconv.ParseInt(it.AttributeID.V, 10, 32)
		if err != nil {
			continue // Error Code's code list
		}
		d.Values[e.ElementID] = append(d.Values[e.ElementID], vocab.Value{Attribute: int32(id)})
		switch e.ElementID {
		case elementMake:
			d.Make, v.Make = int32(id), contract.Value(int32(id))
		case elementModel:
			d.Model, v.Model = int32(id), contract.Value(int32(id))
		}
	}
	return v, d
}

func vocabRef(r contract.SpineRef) vocab.Ref {
	return vocab.Ref{Spine: r.Spine, Target: vocab.Target{Make: r.Make, Model: r.Model, Ext: r.Ext, ID: r.ID}}
}

func spineRef(r vocab.Ref) contract.SpineRef {
	return contract.SpineRef{Make: r.Make, Model: r.Model, Ext: r.Ext, Spine: r.Spine, ID: r.ID}
}

func spineEntry(it vocab.Reached) contract.SpineEntry {
	e := contract.SpineEntry{
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
	return e
}

func vias(vs []vocab.Via) []contract.Via {
	if len(vs) == 0 {
		return nil
	}
	out := make([]contract.Via, len(vs))
	for i, v := range vs {
		out[i] = contract.Via{
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

func year(y *int) contract.Null[int32] {
	if y == nil {
		return contract.Null[int32]{}
	}
	return contract.Value(int32(*y))
}

func unconfirmed(u vocab.Unconfirmed) contract.Unconfirmed {
	return contract.Unconfirmed{Elements: u.Elements, ModelYear: u.ModelYear}
}

// vocabularyEntry is one entry of vocabulary f as the file has it.
func vocabularyEntry(f *vocab.File, e *vocab.Entry) contract.VocabularyEntry {
	out := contract.VocabularyEntry{Entry: f.FieldsOf(e), Mappings: make([]contract.VocabularyMapping, len(e.Mappings)), Note: e.Note}
	for i, mp := range e.Mappings {
		m := contract.VocabularyMapping{
			Make: mp.Make, Model: mp.Model, Ext: mp.Ext, ID: mp.ID,
			First: year(mp.First), Last: year(mp.Last), Origin: mp.Origin, Note: mp.Note,
		}
		if len(mp.Qualifiers) > 0 {
			m.Qualifiers = make(map[string][]contract.QualifierValue, len(mp.Qualifiers))
			for _, q := range mp.Qualifiers {
				vs := make([]contract.QualifierValue, len(q.Values))
				for j, v := range q.Values {
					vs[j] = contract.QualifierValue{IsText: v.IsText, Attribute: v.Attribute, Text: v.Text}
				}
				m.Qualifiers[strconv.Itoa(int(q.Element))] = vs
			}
		}
		out.Mappings[i] = m
	}
	return out
}

func fileVersions(files []*vocab.File) []contract.FileVersion {
	out := make([]contract.FileVersion, len(files))
	for i, f := range files {
		out[i] = contract.FileVersion{Name: f.Name, Version: f.Version}
	}
	return out
}
