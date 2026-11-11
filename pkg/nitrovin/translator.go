package nitrovin

import (
	"encoding/hex"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Translator is a Decoder with the vocabularies, extension sets and
// declared spines loaded beside it (ADR 0011, ADR 0012): the files never
// change for its life, and the Decoder stays vocabulary-free. It
// translates and lists through them (Translate, List), wrapping
// translation's decoder-free core (contract.Translator over a vocab.Set,
// ADR 0013) with the Decoder's engine. It is safe for concurrent use.
type Translator struct {
	d     *Decoder
	core  contract.Translator
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
	t := &Translator{d: d, info: d.Info()}
	t.core = contract.Translator{Set: set, Decode: t.decode, DataMonth: t.info.DataMonth, DecoderVersion: t.info.DecoderVersion}
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
			Drift:   contract.DriftCounts(f.Path, t.drift),
		}
	}
	return t, nil
}

// Decoder is the Decoder the files are loaded beside.
func (t *Translator) Decoder() *Decoder { return t.d }

// Info is the Decoder's Info with the loaded files and their drift:
// GET /v1/info.
func (t *Translator) Info() contract.Info { return t.info }

// Drift is every drift finding of the loaded files, in file and line
// order: what `nitrovin` reports on stderr at load.
func (t *Translator) Drift() []vocab.Finding { return t.drift }
