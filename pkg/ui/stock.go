package ui

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Stock in the browser (ADR 0023, ADR 0019). A stock file, shelf or yard,
// loads as any other file: checked alone, linked with the rest, stored in
// IndexedDB with its index, and read from memory, as on the server; its
// faults and drift (orphans, fields that look personal) are the server's.
// A shelf loads only beside the catalog it names, which this browser must
// hold: the server's stays on the server, and so does stock beside it. A
// connector's file never loads here: connectors run on a server
// (ADR 0020).
//
// A yard row routed by its VIN is decoded once, when its file loads, by
// the server's /v1/decode (the VIN alone travels, never the row), and the
// decodes are kept with the file's index: by its blake3 and the release,
// whose data month they are of. A release that drops the index decodes
// them again when it opens the file. A row's fields never leave the
// browser (ADR 0013).

// vinDecode is what a yard VIN decodes to, as the server's stock reads it
// (contract.Translator.StockDecode): its vPIC model, and its model year
// when the decode gives one. A VIN that decodes to no model is kept as
// nil.
type vinDecode struct {
	Model int32 `json:"model"`
	Year  *int  `json:"year,omitempty"`
}

// yardVINs are the VINs of f's rows that a yard reaches by VIN: the rows
// with no precomputed decode, each VIN once, in file order. nil for a
// shelf, and for a yard with none.
func yardVINs(f *vocab.File) []string {
	if f.Kind != vocab.KindStock || f.Holds != vocab.StockYard {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i := range f.Stock {
		r := &f.Stock[i]
		if r.Decoded == nil && r.VIN != "" && !seen[r.VIN] {
			seen[r.VIN] = true
			out = append(out, r.VIN)
		}
	}
	return out
}

// decodeBatch is how many VINs one POST /v1/decode asks for: a full
// decode is about 18 kB, so a batch's answer stays near 1 MB in the
// module's memory.
const decodeBatch = 50

// decodeYard decodes the VINs of f, a yard file, through the server's
// /v1/decode, a batch at a time, telling progress how far it is: every
// VIN the server decodes, and nil for one that decodes to no model or that
// no decode accepts (empty, or too long), as a server's stock decodes
// each (stock.NewFile). Each batch's full decodes are reduced to a model
// and year as it arrives, so the module holds one batch's at a time. It
// refuses in words when the server cannot be reached or decodes with
// another data month.
func (l *Library) decodeYard(f *vocab.File, progress func(string)) (map[string]*vinDecode, error) {
	vins := yardVINs(f)
	if len(vins) == 0 {
		return nil, nil
	}
	out := make(map[string]*vinDecode, len(vins))
	var ask []string
	for _, vin := range vins {
		out[vin] = nil
		if (contract.Request{VIN: vin}).Validate() == nil {
			ask = append(ask, vin)
		}
	}
	bare, err := vocab.NewSet(l.month, nil)
	if err != nil {
		return nil, err
	}
	var got map[string]contract.VINDecode // the batch at hand's
	t := contract.Translator{Set: bare, Decode: func(vin string, _ contract.Null[int32], _ time.Time) (contract.VINDecode, error) {
		d, ok := got[vin]
		if !ok {
			return d, errors.New("not decoded")
		}
		return d, nil
	}}
	read := t.StockDecode(time.Time{})
	for start := 0; start < len(ask); start += decodeBatch {
		progress(fmt.Sprintf("Decoding the VINs of %s through this nitrovin's server: %d of %d…", f.Name, start, len(ask)))
		batch := ask[start:min(start+decodeBatch, len(ask))]
		decs, err := l.decodeBatch(f.Name, batch)
		if err != nil {
			return nil, err
		}
		got = make(map[string]contract.VINDecode, len(batch))
		for i, d := range decs {
			got[batch[i]] = d
		}
		for _, vin := range batch {
			if model, year, ok := read(vin); ok {
				out[vin] = &vinDecode{Model: model, Year: year}
			}
		}
	}
	return out, nil
}

// batchItem is one item of a POST /v1/decode the browser sends: a VIN,
// every item of its decode asked for.
type batchItem struct {
	VIN  string `json:"vin"`
	Full bool   `json:"full"`
}

// decodeBatch is vins' decodes by the server, one POST /v1/decode, in
// order; name is the file they are of, for the error's words.
func (l *Library) decodeBatch(name string, vins []string) ([]contract.VINDecode, error) {
	items := make([]batchItem, len(vins))
	for i, vin := range vins {
		items[i] = batchItem{VIN: vin, Full: true}
	}
	body, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	status, answer, err := l.remote.Post("/v1/decode", body)
	if err != nil {
		return nil, &pageError{Status: http.StatusServiceUnavailable, Message: "Decoding the VINs of " + name +
			" needs this nitrovin's server, which could not be reached: this browser may be offline. Nothing was loaded; try again once it is reachable."}
	}
	if status != http.StatusOK {
		var p contract.Problem
		_ = json.Unmarshal(answer, &p)
		return nil, &pageError{Status: http.StatusBadGateway, Message: fmt.Sprintf("The server answered HTTP %d to decoding the VINs of %s: %s", status, name, p.Detail)}
	}
	var decs []serverDecode
	if err := json.Unmarshal(answer, &decs); err != nil || len(decs) != len(vins) {
		return nil, fmt.Errorf("reading the server's decodes of the VINs of %s: %d of %d (%v)", name, len(decs), len(vins), err)
	}
	out := make([]contract.VINDecode, len(decs))
	for i, d := range decs {
		if d.DataMonth != l.month.Name() {
			return nil, l.monthChanged(d.DataMonth)
		}
		out[i] = d.vinDecode()
	}
	return out, nil
}

// stockDecode is a yard file's decodes as its stock.Source reads them.
func stockDecode(decodes map[string]*vinDecode) stock.Decode {
	return func(vin string) (int32, *int, bool) {
		d := decodes[vin]
		if d == nil {
			return 0, nil, false
		}
		return d.Model, d.Year, true
	}
}

// stockStored is what a stock file's stored index keeps beside its
// vocabulary index: a yard's decodes, and a shelf's drift as measured
// against the browser's catalog.
type stockStored struct {
	Decodes map[string]*vinDecode `json:"decodes,omitempty"`
	Drift   *stockDrift           `json:"drift,omitempty"`
}

// stockDrift is a shelf's drift (its orphans and its fields that look
// personal), measured against the catalog whose blake3 is Catalog: an
// orphan is a read of that catalog a numbered row, so it is measured once
// a catalog, not on every start of the module.
type stockDrift struct {
	Catalog  string          `json:"catalog"`
	Findings []storedFinding `json:"findings"`
}

// storedFinding is one drift finding of a stock file, its file implied.
type storedFinding struct {
	Line   int    `json:"line"`
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// newStockDrift keeps findings, f's drift against catalog.
func newStockDrift(catalog string, findings []vocab.Finding) *stockDrift {
	d := &stockDrift{Catalog: catalog, Findings: make([]storedFinding, len(findings))}
	for i, g := range findings {
		d.Findings[i] = storedFinding{Line: g.Line, Kind: string(g.Drift), Detail: g.Detail}
	}
	return d
}

// of is d's findings as f's.
func (d *stockDrift) of(f *vocab.File) []vocab.Finding {
	out := make([]vocab.Finding, len(d.Findings))
	for i, g := range d.Findings {
		out[i] = vocab.Finding{File: f.Path, Line: g.Line, Severity: vocab.SeverityDrift, Detail: g.Detail, Drift: vocab.DriftKind(g.Kind)}
	}
	return out
}

// A stored file's index is vocab.WriteIndex's, but for a stock file with
// something to keep beside it: stockIndex, the length of the vocabulary
// index (a uvarint), that index, then a stockStored as JSON.
const stockIndex = "nitrovin stock index\n"

// storedIndex is f's index to store, with kept beside it when it keeps
// anything.
func storedIndex(f *vocab.File, kept stockStored) ([]byte, error) {
	var idx bytes.Buffer
	if err := vocab.WriteIndex(&idx, f); err != nil {
		return nil, err
	}
	if kept.Decodes == nil && kept.Drift == nil {
		return idx.Bytes(), nil
	}
	js, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	out := binary.AppendUvarint([]byte(stockIndex), uint64(idx.Len()))
	return slices.Concat(out, idx.Bytes(), js), nil
}

// readStoredIndex reads what storedIndex wrote.
func readStoredIndex(b []byte) (*vocab.File, stockStored, error) {
	rest, ok := bytes.CutPrefix(b, []byte(stockIndex))
	if !ok {
		f, err := vocab.ReadIndex(b)
		return f, stockStored{}, err
	}
	damaged := errors.New("stock index: damaged")
	n, k := binary.Uvarint(rest)
	if k <= 0 || n > uint64(len(rest)-k) {
		return nil, stockStored{}, damaged
	}
	rest = rest[k:]
	f, err := vocab.ReadIndex(rest[:n])
	if err != nil {
		return nil, stockStored{}, err
	}
	var kept stockStored
	if err := json.Unmarshal(rest[n:], &kept); err != nil || kept.Decodes == nil && kept.Drift == nil {
		return nil, stockStored{}, damaged
	}
	return f, kept, nil
}

// stockDriftOf is h's drift, a linked stock file's, beside cat, the
// browser's catalog (nil for none). A yard's is measured, from memory; a
// shelf's is the one kept against cat, else measured, each orphan a read
// of cat, and kept, in memory and in its stored index, so the next start
// of the module reads none.
func (l *Library) stockDriftOf(h, cat *held) []vocab.Finding {
	if h.file.Holds != vocab.StockShelf || cat == nil {
		return measureStock(l.month, h.file, nil)
	}
	l.driftMu.Lock()
	defer l.driftMu.Unlock()
	if d := l.shelfDrift[h.rec.Blake3]; d != nil && d.Catalog == cat.rec.Blake3 {
		return d.of(h.file)
	}
	found := measureStock(l.month, h.file, cat.file)
	d := newStockDrift(cat.rec.Blake3, found)
	l.shelfDrift[h.rec.Blake3] = d
	// The stored index is a cache: one this browser refuses to store is
	// measured again by the next start of the module, and the page shows
	// the drift all the same.
	if idx, err := storedIndex(h.file, stockStored{Decodes: h.decodes, Drift: d}); err == nil {
		_ = l.disk.PutIndex(h.rec.Blake3, idx)
	}
	return found
}

// measureStock is f's drift, a stock file's, beside catalog (nil for
// none): Drift of the two, f's findings alone.
func measureStock(m vocab.Month, f, catalog *vocab.File) []vocab.Finding {
	files := []*vocab.File{f}
	if catalog != nil {
		files = append(files, catalog)
	}
	return slices.DeleteFunc(vocab.Drift(m, files), func(g vocab.Finding) bool { return g.File != f.Path })
}

// admitStock refuses f, a stock file picked as fileName, when this
// browser cannot load it: a connector's, and a shelf whose catalog the
// browser does not hold, linked, among cur's files.
func (l *Library) admitStock(f *vocab.File, fileName string, cur *shelf) error {
	if f.Connector != nil {
		return &pageError{Status: http.StatusUnprocessableEntity, Message: fmt.Sprintf(
			"%s declares a connector, reading %s from a database: connectors run on a server (ADR 0020), so it does not load in this browser. Load it with nitrovin serve --vocab, or pick a stock file whose lines are its rows.",
			fileName, f.Connector.Relation)}
	}
	if f.Holds != vocab.StockShelf || slices.ContainsFunc(cur.browser, func(h *held) bool { return h.catalog && h.linked() && h.name == f.StockCatalog }) {
		return nil
	}
	msg := fmt.Sprintf("%s is a shelf of catalog %q, which this browser does not hold: a shelf loads beside the catalog its numbers belong to.", fileName, f.StockCatalog)
	switch i := slices.IndexFunc(cur.browser, func(h *held) bool { return h.catalog && h.linked() }); {
	case f.StockCatalog == l.serverCatalog:
		msg = fmt.Sprintf("%s is a shelf of %s, this server's catalog, which stays on the server: stock beside it loads there too, with nitrovin serve --vocab.", fileName, f.StockCatalog)
	case i >= 0:
		msg += fmt.Sprintf(" This browser holds %s.", cur.browser[i].name)
	default:
		msg += " Compile the catalog here first, then load the shelf."
	}
	return &pageError{Status: http.StatusConflict, Message: msg}
}

// stockFile is f, a stock file the browser holds as h, as /v1/info lists
// it.
func stockFile(h *held) contract.VocabularyFile {
	f := h.file
	return contract.VocabularyFile{Name: h.name, Kind: string(vocab.KindStock), Version: f.Version, Release: f.Release, Blake3: h.rec.Blake3, Path: f.Path,
		StockCounts: &contract.StockCounts{Source: string(f.Holds), Catalog: f.StockCatalog, AsOf: f.AsOf, Rows: new(len(f.Stock))}}
}
