package nitrovin

import (
	"encoding/hex"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Translator is a Decoder with the vocabularies, extension sets and
// declared spines loaded beside it (ADR 0011, ADR 0012): the files never
// change for its life, and the Decoder stays vocabulary-free. It
// translates and lists through them (Translate, List). It is safe for
// concurrent use.
type Translator struct {
	d     *Decoder
	set   *vocab.Set
	drift []vocab.Finding
	info  contract.Info
}

// NewTranslator loads files beside d, as --vocab and NITROVIN_VOCAB do: it
// links them (vocab.Link), refusing with vocab.Faults, and measures their
// drift against d's data month, which it reports and serves. With no
// files it still translates a VIN or a vPIC id to the spine, and lists
// vPIC.
func NewTranslator(d *Decoder, files ...*vocab.File) (*Translator, error) {
	m := d.month
	set, err := vocab.NewSet(m, files)
	if err != nil {
		return nil, err
	}
	t := &Translator{d: d, set: set, info: d.Info()}
	if len(files) > 0 {
		t.drift = vocab.Drift(m, files)
	}
	t.info.Vocabularies = make([]contract.VocabularyFile, len(files))
	for i, f := range files {
		t.info.Vocabularies[i] = contract.VocabularyFile{
			Name:    f.Name,
			Kind:    string(f.Kind),
			Version: f.Version,
			Blake3:  hex.EncodeToString(f.Hash[:]),
			Path:    f.Path,
			Drift:   driftCounts(f, t.drift),
		}
	}
	return t, nil
}

// driftKinds orders a file's DriftCounts.
var driftKinds = []vocab.DriftKind{vocab.DriftMake, vocab.DriftModel, vocab.DriftQualifier, vocab.DriftExtension}

// driftCounts counts f's findings by kind, keeping the first
// contract.MaxDriftExamples of each.
func driftCounts(f *vocab.File, drift []vocab.Finding) []contract.DriftCount {
	out := []contract.DriftCount{}
	for _, kind := range driftKinds {
		c := contract.DriftCount{Kind: string(kind), Examples: []contract.DriftExample{}}
		for _, d := range drift {
			if d.File != f.Path || d.Drift != kind {
				continue
			}
			c.Count++
			if len(c.Examples) < contract.MaxDriftExamples {
				c.Examples = append(c.Examples, contract.DriftExample{Line: d.Line, Detail: d.Detail})
			}
		}
		if c.Count > 0 {
			out = append(out, c)
		}
	}
	return out
}

// Decoder is the Decoder the files are loaded beside.
func (t *Translator) Decoder() *Decoder { return t.d }

// Info is the Decoder's Info with the loaded files and their drift:
// GET /v1/info.
func (t *Translator) Info() contract.Info { return t.info }

// Drift is every drift finding of the loaded files, in file and line
// order: what `nitrovin` reports on stderr at load.
func (t *Translator) Drift() []vocab.Finding { return t.drift }
