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
// each (stock.NewFile). It refuses in words when the server cannot be
// reached or decodes with another data month.
func (l *Library) decodeYard(f *vocab.File, progress func(string)) (map[string]*vinDecode, error) {
	vins := yardVINs(f)
	if len(vins) == 0 {
		return nil, nil
	}
	got := map[string]contract.VINDecode{}
	var ask []string
	for _, vin := range vins {
		if (contract.Request{VIN: vin}).Validate() == nil {
			ask = append(ask, vin)
		}
	}
	for start := 0; start < len(ask); start += decodeBatch {
		progress(fmt.Sprintf("Decoding the VINs of %s through this nitrovin's server: %d of %d…", f.Name, start, len(ask)))
		batch := ask[start:min(start+decodeBatch, len(ask))]
		decs, err := l.decodeBatch(f.Name, batch)
		if err != nil {
			return nil, err
		}
		for i, d := range decs {
			got[batch[i]] = d
		}
	}
	bare, err := vocab.NewSet(l.month, nil)
	if err != nil {
		return nil, err
	}
	t := contract.Translator{Set: bare, Decode: func(vin string, _ contract.Null[int32], _ time.Time) (contract.VINDecode, error) {
		d, ok := got[vin]
		if !ok {
			return d, errors.New("not decoded")
		}
		return d, nil
	}}
	read := t.StockDecode(time.Time{})
	out := make(map[string]*vinDecode, len(vins))
	for _, vin := range vins {
		out[vin] = nil
		if model, year, ok := read(vin); ok {
			out[vin] = &vinDecode{Model: model, Year: year}
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

// A stored file's index is vocab.WriteIndex's, but for a yard file whose
// rows' VINs were decoded: yardIndex, the length of the vocabulary index
// (a uvarint), that index, then its decodes as JSON ({VIN: {model,
// year}}, null for no model).
const yardIndex = "nitrovin yard index\n"

// storedIndex is f's index to store, decodes beside it when f has any.
func storedIndex(f *vocab.File, decodes map[string]*vinDecode) ([]byte, error) {
	var idx bytes.Buffer
	if err := vocab.WriteIndex(&idx, f); err != nil {
		return nil, err
	}
	if decodes == nil {
		return idx.Bytes(), nil
	}
	js, err := json.Marshal(decodes)
	if err != nil {
		return nil, err
	}
	out := binary.AppendUvarint([]byte(yardIndex), uint64(idx.Len()))
	return slices.Concat(out, idx.Bytes(), js), nil
}

// readStoredIndex reads what storedIndex wrote.
func readStoredIndex(b []byte) (*vocab.File, map[string]*vinDecode, error) {
	rest, ok := bytes.CutPrefix(b, []byte(yardIndex))
	if !ok {
		f, err := vocab.ReadIndex(b)
		return f, nil, err
	}
	n, k := binary.Uvarint(rest)
	if k <= 0 || n > uint64(len(rest)-k) {
		return nil, nil, errors.New("yard index: damaged")
	}
	rest = rest[k:]
	f, err := vocab.ReadIndex(rest[:n])
	if err != nil {
		return nil, nil, err
	}
	var decodes map[string]*vinDecode
	if err := json.Unmarshal(rest[n:], &decodes); err != nil || decodes == nil {
		return nil, nil, errors.New("yard index: damaged")
	}
	return f, decodes, nil
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
