package nitrovin

import (
	"encoding/hex"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/fitment"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Translator is a Decoder with the vocabularies, extension sets, declared
// spines and catalog loaded beside it (ADR 0011, ADR 0012, ADR 0017): the
// files never change for its life, and the Decoder stays vocabulary-free.
// It translates and lists through them (Translate, List), wrapping
// translation's decoder-free core (contract.Translator over a vocab.Set,
// ADR 0013) with the Decoder's engine, and reads the catalog through
// Catalog. It is safe for concurrent use.
type Translator struct {
	d       *Decoder
	core    contract.Translator
	catalog fitment.Reader
	drift   []vocab.Finding
	info    contract.Info
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
	if c := set.Catalog(); c != nil {
		t.catalog = c
	}
	if len(files) > 0 {
		t.drift = vocab.Drift(m, files)
	}
	t.info.Vocabularies = make([]contract.VocabularyFile, len(files))
	for i, f := range files {
		v := contract.VocabularyFile{
			Name:    f.Name,
			Kind:    string(f.Kind),
			Version: f.Version,
			Blake3:  hex.EncodeToString(f.Hash[:]),
			Path:    f.Path,
			Drift:   contract.DriftCounts(f.Path, t.drift),
		}
		if c := f.Catalog(); c != nil {
			ci := c.Info()
			v.Release = ci.Release
			v.CatalogCounts = &contract.CatalogCounts{Fitments: ci.Fitments, Applications: ci.Applications, Redirects: ci.Redirects, Retired: ci.Retired}
		}
		t.info.Vocabularies[i] = v
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

// Catalog is the loaded catalog, read through fitment.Reader (ADR 0017),
// nil when none is loaded. Its vehicles are its own: a request reaches
// them by a translation to its vehicle vocabulary (fitment.Vehicles).
func (t *Translator) Catalog() fitment.Reader { return t.catalog }
