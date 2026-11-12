package nitrovin

import (
	"cmp"
	"encoding/hex"
	"slices"
	"sync"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/fitment"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Translator is a Decoder with the vocabularies, extension sets, declared
// spines and catalog loaded beside it (ADR 0011, ADR 0012, ADR 0017): the
// files never change for its life, and the Decoder stays vocabulary-free.
// It translates, lists and fits through them (Translate, List, Fit),
// wrapping the decoder-free core (contract.Translator over a vocab.Set,
// ADR 0013, and contract.Fitter over the catalog's fitment.Reader,
// ADR 0018) with the Decoder's engine, and reads the catalog through
// Catalog. It is safe for concurrent use.
type Translator struct {
	d       *Decoder
	core    contract.Fitter
	catalog fitment.Reader
	drift   []vocab.Finding
	// load is Info with the drift measured at load; all is drift with the
	// model-qualifier findings (vocab.ModelDrift), and info Info with them
	// counted: both built on first ask, since the model values index
	// costs about 0.15 s and 100 MB (ADR 0015).
	load contract.Info
	all  func() []vocab.Finding
	info func() contract.Info
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
	base := d.Info()
	t := &Translator{d: d}
	t.core.Translator = contract.Translator{Set: set, Decode: t.decode, DataMonth: base.DataMonth, DecoderVersion: base.DecoderVersion}
	if c := set.Catalog(); c != nil {
		t.catalog = c
		t.core.Catalog = c
	}
	if len(files) > 0 {
		t.drift = vocab.Drift(m, files)
	}
	t.all = sync.OnceValue(func() []vocab.Finding {
		model := vocab.ModelDrift(m, files)
		if len(model) == 0 {
			return t.drift
		}
		order := make(map[string]int, len(files))
		for i, f := range files {
			order[f.Path] = i
		}
		all := slices.Concat(t.drift, model)
		slices.SortStableFunc(all, func(a, b vocab.Finding) int {
			return cmp.Or(cmp.Compare(order[a.File], order[b.File]), cmp.Compare(a.Line, b.Line))
		})
		return all
	})
	infoWith := func(drift []vocab.Finding) contract.Info {
		info := base
		info.Vocabularies = make([]contract.VocabularyFile, len(files))
		for i, f := range files {
			v := contract.VocabularyFile{
				Name:    f.Name,
				Kind:    string(f.Kind),
				Version: f.Version,
				Blake3:  hex.EncodeToString(f.Hash[:]),
				Path:    f.Path,
				Drift:   contract.DriftCounts(f.Path, drift),
			}
			if c := f.Catalog(); c != nil {
				ci := c.Info()
				v.Release = ci.Release
				v.CatalogCounts = &contract.CatalogCounts{Fitments: ci.Fitments, Applications: ci.Applications, Redirects: ci.Redirects, Retired: ci.Retired}
			}
			info.Vocabularies[i] = v
		}
		return info
	}
	t.load = infoWith(t.drift)
	t.info = sync.OnceValue(func() contract.Info { return infoWith(t.all()) })
	return t, nil
}

// Decoder is the Decoder the files are loaded beside.
func (t *Translator) Decoder() *Decoder { return t.d }

// Info is the Decoder's Info with the loaded files and all their drift,
// model-qualifier included: GET /v1/info. The first call measures the
// model-qualifier drift (AllDrift); LoadInfo does not.
func (t *Translator) Info() contract.Info { return t.info() }

// LoadInfo is Info with only the drift measured at load, no
// model-qualifier counts: what a page that shows no drift reads, at no
// cost (ADR 0015).
func (t *Translator) LoadInfo() contract.Info { return t.load }

// Drift is the drift findings of the loaded files measured at load (every
// kind but model-qualifier), in file and line order: what `nitrovin`
// reports on stderr at load.
func (t *Translator) Drift() []vocab.Finding { return t.drift }

// AllDrift is Drift with the model-qualifier findings (vocab.ModelDrift),
// in file and line order: what `nitrovin vocab check` prints. The first
// call builds the model values index it needs, about 0.15 s and 100 MB on
// an EPA-sized vocabulary (ADR 0015); later calls reuse it.
func (t *Translator) AllDrift() []vocab.Finding { return t.all() }

// Catalog is the loaded catalog, read through fitment.Reader (ADR 0017),
// nil when none is loaded. Its vehicles are its own: a request reaches
// them by a translation to its vehicle vocabulary (fitment.Vehicles).
func (t *Translator) Catalog() fitment.Reader { return t.catalog }
