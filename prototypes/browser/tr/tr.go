// Package tr is the PROTOTYPE decoder-free translator (#147): what
// pkg/nitrovin's Translator does, over a vocab.Month (the spine bundle)
// instead of a *Decoder, taking a VIN's decode as fetched from /v1/decode.
// The contract mapping below is a fork of pkg/nitrovin/translate.go's
// unexported helpers: a build ticket would move them where both can use
// them instead. Builds for js/wasm with Go and TinyGo.
package tr

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

const (
	elementMake      = 26
	elementModel     = 28
	elementModelYear = 29
)

// Local is a vocab.Set over a spine bundle, with the files' info.
type Local struct {
	month vocab.Month
	set   *vocab.Set
	drift []vocab.Finding
}

// New is a translator over set, indexed beside m (vocab.NewSet); it
// measures the files' drift against m.
func New(m vocab.Month, set *vocab.Set) *Local {
	l := &Local{month: m, set: set}
	if files := set.Files(); len(files) > 0 {
		l.drift = vocab.Drift(m, files)
	}
	return l
}
// Files are the loaded files, in load order.
func (l *Local) Files() []*vocab.File { return l.set.Files() }

// DriftOf counts f's drift findings.
func (l *Local) DriftOf(f *vocab.File) int {
	n := 0
	for _, d := range l.drift {
		if d.File == f.Path {
			n++
		}
	}
	return n
}

// Month is the spine's data month.
func (l *Local) Month() string { return l.month.Name() }

// Decoded is a VIN's decode as /v1/decode/{vin}?full=true answers it,
// only the parts a translation reads.
type Decoded struct {
	VIN       string `json:"vin"`
	DataMonth string `json:"data_month"`
	Items     []struct {
		ElementID   int32   `json:"element_id"`
		Value       *string `json:"value"`
		AttributeID *string `json:"attribute_id"`
	} `json:"items"`
}

// Translate answers req as Translator.Translate does; dec is the decode of
// req's VIN (from or context), nil when it names none.
func (l *Local) Translate(req contract.TranslateRequest, dec *Decoded) (contract.Translation, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	if err := req.Validate(); err != nil {
		return contract.Translation{}, err
	}
	q := vocab.Query{
		From: vocab.From{
			VIN:        req.From.VIN != "",
			Vocabulary: req.From.Vocabulary,
			Entry:      req.From.Entry,
			Ref:        vocab.Ref{Spine: req.From.Spine, Target: vocab.Target{Make: req.From.Make, Model: req.From.Model, Ext: req.From.Ext, ID: req.From.ID}},
		},
		To: req.To,
	}
	if req.ModelYear.Valid {
		y := int(req.ModelYear.V)
		q.ModelYear = &y
	}
	var vehicle *contract.Vehicle
	if dec != nil {
		vehicle, q.Decoded = l.decoded(dec)
	}
	t, err := l.set.Translate(q)
	if err != nil {
		return contract.Translation{}, problem(err)
	}
	out := contract.Translation{
		From:         contract.TranslationFrom{Status: contract.FromStatus(t.Status)},
		Vehicle:      vehicle,
		Spine:        make([]contract.SpineEntry, len(t.Spine)),
		To:           make([]contract.Translated, len(t.To)),
		Vocabularies: fileVersions(t.Files),
		Now:          req.Now.UTC().Truncate(time.Second),
		DataMonth:    l.month.Name(),
	}
	switch {
	case vehicle != nil && req.From.VIN != "":
		out.From.VIN = vehicle.VIN
	case req.From.Vocabulary != "":
		out.From.Vocabulary, out.From.Entry = req.From.Vocabulary, req.From.Entry
		if t.Entry != nil {
			out.From.Entry = t.Vocabulary.FieldsOf(t.Entry)
			out.From.Note = t.Entry.Note
		}
	default:
		out.From.SpineRef = req.From.SpineRef
	}
	for i, it := range t.Spine {
		out.Spine[i] = spineEntry(it)
	}
	for i, to := range t.To {
		c := contract.Translated{Vocabulary: to.Vocabulary.Name, Entries: make([]contract.TranslatedEntry, len(to.Matches))}
		for j, m := range to.Matches {
			c.Entries[j] = contract.TranslatedEntry{Entry: to.Vocabulary.FieldsOf(m.Entry), Note: m.Entry.Note, Via: vias(m.Via), Unconfirmed: unconfirmed(m.Unconfirmed)}
		}
		out.To[i] = c
	}
	return out, nil
}

// List answers req as Translator.List does.
func (l *Local) List(req contract.ListRequest) (contract.List, error) {
	ls, err := l.set.List(vocab.ListQuery{Of: req.Of, Where: req.Where, Limit: req.Limit, After: req.After})
	if err != nil {
		return contract.List{}, problem(err)
	}
	out := contract.List{Of: req.Of, Entries: []contract.ListEntry{}, Next: ls.Next, Vocabularies: []contract.FileVersion{}, DataMonth: l.month.Name()}
	switch {
	case req.Of == "":
		out.Vocabularies = fileVersions(l.set.Files())
		for _, c := range ls.Catalog {
			out.Entries = append(out.Entries, contract.CatalogEntry{Name: c.Name, Kind: string(c.Kind), Version: c.Version, Subject: string(c.Subject), Fields: c.Fields, Spine: c.Spine})
		}
	case ls.File != nil:
		out.Vocabularies = fileVersions([]*vocab.File{ls.File})
	}
	for _, e := range ls.Entries {
		out.Entries = append(out.Entries, vocabularyEntry(ls.File, e))
	}
	for _, it := range ls.Spine {
		out.Entries = append(out.Entries, spineEntry(it))
	}
	return out, nil
}

// Hash is f's blake3 in hex: with its name, its IndexedDB key.
func Hash(f *vocab.File) string { return hex.EncodeToString(f.Hash[:]) }

func problem(err error) *contract.Problem {
	var r *vocab.RequestError
	if errors.As(err, &r) {
		return contract.NewProblem(contract.InvalidRequest, r.Pointer, r.Detail)
	}
	return contract.AsProblem(err)
}

func (l *Local) decoded(dec *Decoded) (*contract.Vehicle, *vocab.Decoded) {
	v := &contract.Vehicle{VIN: dec.VIN}
	d := &vocab.Decoded{Values: map[int32][]vocab.Value{}}
	for _, it := range dec.Items {
		if it.Value == nil || *it.Value == "" {
			continue
		}
		lookup, ok := l.month.Element(it.ElementID)
		if !ok {
			continue
		}
		if !lookup {
			d.Values[it.ElementID] = append(d.Values[it.ElementID], vocab.Value{IsText: true, Text: *it.Value})
			if it.ElementID == elementModelYear {
				if y, err := strconv.Atoi(*it.Value); err == nil {
					d.ModelYear = &y
					v.ModelYear = contract.Value(int32(y))
				}
			}
			continue
		}
		if it.AttributeID == nil {
			continue
		}
		id, err := strconv.ParseInt(*it.AttributeID, 10, 32)
		if err != nil {
			continue
		}
		d.Values[it.ElementID] = append(d.Values[it.ElementID], vocab.Value{Attribute: int32(id)})
		switch it.ElementID {
		case elementMake:
			d.Make, v.Make = int32(id), contract.Value(int32(id))
		case elementModel:
			d.Model, v.Model = int32(id), contract.Value(int32(id))
		}
	}
	return v, d
}

func spineRef(r vocab.Ref) contract.SpineRef {
	return contract.SpineRef{Make: r.Make, Model: r.Model, Ext: r.Ext, Spine: r.Spine, ID: r.ID}
}

func spineEntry(it vocab.Reached) contract.SpineEntry {
	e := contract.SpineEntry{SpineRef: spineRef(it.Ref), Name: it.Name, Drift: it.Drift, Via: vias(it.Via), Unconfirmed: unconfirmed(it.Unconfirmed)}
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
		out[i] = contract.Via{SpineRef: spineRef(v.Ref), First: year(v.Mapping.First), Last: year(v.Mapping.Last), Origin: v.Mapping.Origin, Note: v.Mapping.Note, Unconfirmed: unconfirmed(v.Unconfirmed)}
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

func vocabularyEntry(f *vocab.File, e *vocab.Entry) contract.VocabularyEntry {
	out := contract.VocabularyEntry{Entry: f.FieldsOf(e), Mappings: make([]contract.VocabularyMapping, len(e.Mappings)), Note: e.Note}
	for i, mp := range e.Mappings {
		m := contract.VocabularyMapping{Make: mp.Make, Model: mp.Model, Ext: mp.Ext, ID: mp.ID, First: year(mp.First), Last: year(mp.Last), Origin: mp.Origin, Note: mp.Note}
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

// JSON marshals v, for the plain-JS variant.
func JSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":` + strconv.Quote(err.Error()) + `}`
	}
	return string(b)
}
