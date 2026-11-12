package nitrovin

import (
	"cmp"
	"encoding/hex"
	"slices"
	"sync"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/fitment"
	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Translator is a Decoder with the vocabularies, extension sets, declared
// spines, catalog and stock files loaded beside it (ADR 0011, ADR 0012,
// ADR 0017, ADR 0019): the
// files never change for its life, and the Decoder stays vocabulary-free.
// Only a connector's stock (ADR 0020) is read while it runs.
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
	// live are the connectors' sources, by their file's index in Info's
	// vocabularies: their status is read on every Info.
	live map[int]stock.Live
}

// NewTranslator loads files beside d, as --vocab and NITROVIN_VOCAB do: it
// links them (vocab.Link), refusing with vocab.Faults, and measures their
// drift against d's data month, which it reports and serves. With no
// files it still translates a VIN or a vPIC id to the spine, and lists
// vPIC. It is NewTranslatorWith with the zero TranslatorConfig: a
// connector's stock file (ADR 0020) loads as a source that is always
// unavailable.
func NewTranslator(d *Decoder, files ...*vocab.File) (*Translator, error) {
	return NewTranslatorWith(d, TranslatorConfig{}, files...)
}

// TranslatorConfig is what a Translator is given beyond its decoder and
// files; its zero value is NewTranslator's.
type TranslatorConfig struct {
	// Connect opens each connector's stock file (ADR 0020): the nitrovin
	// binary passes pkg/stock/pg's. Nil, as in a build without a database
	// driver (the C library), loads each as stock.Down.
	Connect stock.Connect
}

// NewTranslatorWith is NewTranslator configured by c. Nothing connects
// here: a connector's source connects on its first lookup, and Close
// releases it.
func NewTranslatorWith(d *Decoder, c TranslatorConfig, files ...*vocab.File) (*Translator, error) {
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
	decode := t.core.Translator.StockDecode(time.Now())
	live := map[*vocab.File]stock.Live{}
	for _, f := range set.Stock() {
		if f.Connector == nil {
			t.core.Stock = append(t.core.Stock, stock.NewFile(f, decode))
			continue
		}
		var src stock.Live
		if c.Connect == nil {
			info := stock.Info{Name: f.Name, Holds: f.Holds, Kind: stock.KindLive, Catalog: f.StockCatalog, Connector: vocab.ConnectorPostgres, Relation: f.Connector.Relation}
			src = stock.Down(info, "this build reads stock files only: a connector needs the nitrovin binary")
		} else {
			catalog, vehicles := connectorCatalog(set, f)
			src = c.Connect(stock.Declared{File: f, Catalog: catalog, Vehicles: vehicles})
		}
		live[f] = src
		t.core.Stock = append(t.core.Stock, src)
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
			if cc := f.Compiled(); cc != nil {
				v.Compiled = &contract.CompiledCatalog{Format: cc.Format, SourceBlake3: hex.EncodeToString(cc.SourceHash[:]), Writer: cc.Writer,
					DriftMonth: cc.DriftMonth, DriftMonthDiffers: cc.DriftMonth != info.DataMonth, SkippedDrift: []string{}}
				for _, k := range cc.Skipped {
					v.Compiled.SkippedDrift = append(v.Compiled.SkippedDrift, string(k))
				}
				for _, l := range cc.Links {
					j := slices.IndexFunc(files, func(g *vocab.File) bool { return g.Name == l.Name })
					if j < 0 || files[j].Hash != l.Hash {
						v.Compiled.LinksDiffer = true
					}
				}
			}
			if f.Kind == vocab.KindStock {
				v.Release = f.Release
				v.StockCounts = &contract.StockCounts{Source: string(f.Holds), Catalog: f.StockCatalog, AsOf: f.AsOf}
				if f.Connector != nil {
					v.StockCounts.Connector, v.StockCounts.Relation = vocab.ConnectorPostgres, f.Connector.Relation
				} else {
					v.StockCounts.Rows = new(len(f.Stock))
				}
			}
			info.Vocabularies[i] = v
		}
		return info
	}
	t.live = map[int]stock.Live{}
	for i, f := range files {
		if src, ok := live[f]; ok {
			t.live[i] = src
		}
	}
	t.load = infoWith(t.drift)
	t.info = sync.OnceValue(func() contract.Info { return infoWith(t.all()) })
	return t, nil
}

// connectorCatalog is the catalog a connector's rows name vehicles of, and
// its vehicle vocabulary: a shelf's own catalog, a yard's the one loaded.
// Either is nil when there is none, or the vehicles are vpic.
func connectorCatalog(set *vocab.Set, f *vocab.File) (catalog, vehicles *vocab.File) {
	switch {
	case f.Holds == vocab.StockShelf:
		catalog = set.File(f.StockCatalog)
	case set.Catalog() != nil:
		catalog = set.File(set.Catalog().Info().Name)
	}
	if catalog != nil && catalog.Vehicles != vocab.Reserved {
		vehicles = set.File(catalog.Vehicles)
	}
	return catalog, vehicles
}

// Decoder is the Decoder the files are loaded beside.
func (t *Translator) Decoder() *Decoder { return t.d }

// Info is the Decoder's Info with the loaded files and all their drift,
// model-qualifier included: GET /v1/info. The first call measures the
// model-qualifier drift (AllDrift); LoadInfo does not. A connector's
// status is what its latest lookup found.
func (t *Translator) Info() contract.Info { return t.withStatus(t.info()) }

// LoadInfo is Info with only the drift measured at load, no
// model-qualifier counts: what a page that shows no drift reads, at no
// cost (ADR 0015).
func (t *Translator) LoadInfo() contract.Info { return t.withStatus(t.load) }

// withStatus is info with each connector's status as of now, its own copy
// of what changes.
func (t *Translator) withStatus(info contract.Info) contract.Info {
	if len(t.live) == 0 {
		return info
	}
	info.Vocabularies = slices.Clone(info.Vocabularies)
	for i, src := range t.live {
		c := *info.Vocabularies[i].StockCounts
		switch h := src.Health(); {
		case !h.Checked:
			c.Status = contract.StockUnchecked
		case h.Down != nil:
			c.Status, c.Reason = contract.StockUnavailable, h.Down.Reason
		default:
			c.Status = contract.StockAvailable
		}
		info.Vocabularies[i].StockCounts = &c
	}
	return info
}

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

// Close releases what the Translator holds open: each connector's
// connections and its pool's background work (ADR 0020). A fit after it
// answers a connector's lists unavailable; files are unaffected. It is
// safe to call more than once, and does nothing without connectors.
func (t *Translator) Close() {
	for _, src := range t.live {
		src.Close()
	}
}
