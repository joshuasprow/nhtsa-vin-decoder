package ui

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// fitNow is the clock of every fit a test asks for.
var fitNow = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// fitmentPath is testdata/vocab/fitment/name.jsonl, the fixture catalog
// and the files beside it.
func fitmentPath(name string) string {
	return filepath.Join("..", "..", "testdata", "vocab", "fitment", name+".jsonl")
}

func fitmentFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fitmentPath(name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decoderServer is a server of the embedded data month, as the browser
// reaches it: its spine bundle and the fitment fixture's files names, and
// /v1/decode answered by the embedded decoder, as `nitrovin serve` does.
type decoderServer struct {
	*fakeServer
	d *nitrovin.Decoder
	// decodesDown fails every POST /v1/decode, as a server out of reach.
	decodesDown bool
}

func newDecoderServer(t *testing.T, d *nitrovin.Decoder, names ...string) *decoderServer {
	t.Helper()
	var bundle bytes.Buffer
	if err := d.WriteSpineBundle(&bundle); err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{info: d.Info(), files: map[string][]byte{}}
	s.info.Vocabularies = nil
	for _, n := range names {
		b := fitmentFile(t, n)
		f, err := vocab.Read(n+".jsonl", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		v := contract.VocabularyFile{Name: n, Kind: string(f.Kind), Blake3: hex.EncodeToString(f.Hash[:])}
		s.info.Vocabularies = append(s.info.Vocabularies, v)
		s.files[FilePath(s.info, v)] = b
	}
	s.files[SpinePath(s.info)] = bundle.Bytes()
	return &decoderServer{fakeServer: s, d: d}
}

func (s *decoderServer) Get(path string) (int, []byte, error) {
	rest, ok := strings.CutPrefix(path, "/v1/decode/")
	if !ok {
		return s.fakeServer.Get(path)
	}
	s.gets = append(s.gets, path)
	vin, query, _ := strings.Cut(rest, "?")
	q, err := url.ParseQuery(query)
	if err != nil {
		return 0, nil, err
	}
	req := contract.Request{VIN: vin, Full: q.Get("full") == "true"}
	if now := q.Get("now"); now != "" {
		if req.Now, err = time.Parse(time.RFC3339, now); err != nil {
			return 0, nil, err
		}
	}
	if y := q.Get("model_year"); y != "" {
		n, err := strconv.Atoi(y)
		if err != nil {
			return 0, nil, err
		}
		req.ModelYear = contract.Value(int32(n))
	}
	dec, err := s.d.Decode(req)
	if p, ok := errors.AsType[*contract.Problem](err); ok {
		b, _ := json.Marshal(p)
		return p.Status, b, nil
	}
	if err != nil {
		return 0, nil, err
	}
	b, err := json.Marshal(dec)
	return http.StatusOK, b, err
}

// Post answers POST /v1/decode, a batch, as `nitrovin serve` does.
func (s *decoderServer) Post(path string, body []byte) (int, []byte, error) {
	s.posts = append(s.posts, body)
	if s.down || s.decodesDown {
		return 0, nil, errors.New("Failed to fetch")
	}
	if path != "/v1/decode" {
		return http.StatusNotFound, nil, nil
	}
	reqs, err := contract.ParseBatch(body)
	if err == nil {
		var decs []contract.Decode
		if decs, err = s.d.DecodeBatch(reqs); err == nil {
			b, err := json.Marshal(decs)
			return http.StatusOK, b, err
		}
	}
	p := contract.AsProblem(err)
	b, _ := json.Marshal(p)
	return p.Status, b, nil
}

// serve has s serve data as its file name.
func (s *decoderServer) serve(t *testing.T, name string, data []byte) {
	t.Helper()
	f, err := vocab.Read(name+".jsonl", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	v := contract.VocabularyFile{Name: name, Kind: string(f.Kind), Blake3: hex.EncodeToString(f.Hash[:])}
	s.info.Vocabularies = append(s.info.Vocabularies, v)
	s.files[FilePath(s.info, v)] = data
}

// serverTranslator is the server with the fitment fixture loaded,
// catalog.jsonl as JSON Lines among it: the answers a browser's catalog
// must give.
func serverTranslator(t *testing.T, d *nitrovin.Decoder) *nitrovin.Translator {
	t.Helper()
	var paths []string
	for _, n := range []string{"part-types", "part-codes", "catalog-models", "catalog"} {
		paths = append(paths, fitmentPath(n))
	}
	files, err := vocab.Open(paths...)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// compileFixture compiles the fixture's catalog.jsonl into l, sorting in a
// temporary directory, as a picked file.
func compileFixture(t *testing.T, l *Library, data []byte) (StoredFile, error) {
	t.Helper()
	var said []string
	f, err := l.Compile(bytes.NewReader(data), "catalog.jsonl", int64(len(data)), vocab.DirScratch(t.TempDir()), func(s string) { said = append(said, s) })
	if err == nil && (len(said) < 2 || !strings.HasPrefix(said[0], "Reading catalog.jsonl")) {
		t.Errorf("progress said %q", said)
	}
	return f, err
}

func embedded(t *testing.T) *nitrovin.Decoder {
	t.Helper()
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// TestLibraryCatalogFit: a catalog compiled in the browser, its spine the
// server's and its vehicle vocabulary the browser's, answers fit exactly
// as the server does with the same files loaded, its catalog as JSON
// Lines: every kind of request (part types, candidates directly and
// through a redirect, related vehicles, a fitment's vehicles directly and
// through a retired number), from VINs the server decodes, vocabulary
// entries and spine entries, in and across years, on every side, a page
// at a time and whole; and again once the browser opens its files anew,
// as after a reload.
func TestLibraryCatalogFit(t *testing.T) {
	d := embedded(t)
	srv := serverTranslator(t, d)
	s, disk := newDecoderServer(t, d, "part-types", "part-codes"), newDisk()
	l := openWith(t, s, disk)
	if _, err := l.Load([]Picked{{FileName: "catalog-models.jsonl", Data: fitmentFile(t, "catalog-models")}}, quiet); err != nil {
		t.Fatal(err)
	}
	f, err := compileFixture(t, l, fitmentFile(t, "catalog"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "parts-catalog" || !f.Catalog || f.Fitments != 5 || f.Fault != "" || f.FileName != "catalog.jsonl" || disk.catalog == nil || int(f.Size) != len(disk.catalogData) {
		t.Fatalf("compiled %+v", f)
	}
	// The spine bundle has no model years and no model values: the file
	// records what it skipped, which /v1/info's compiled says too.
	if !slices.Equal(f.Skipped, []string{"model-years", "model-qualifier"}) {
		t.Errorf("skipped %q", f.Skipped)
	}
	opened, err := vocab.OpenCompiled("catalog.compiled", bytes.NewReader(disk.catalogData), int64(len(disk.catalogData)))
	if err != nil {
		t.Fatal(err)
	}
	if cc := opened.Compiled(); hex.EncodeToString(cc.SourceHash[:]) != hex.EncodeToString(blake3Of(fitmentFile(t, "catalog"))) || cc.DriftMonth != d.Info().DataMonth {
		t.Errorf("the compiled file records source %x, drift month %s", cc.SourceHash, cc.DriftMonth)
	}

	found := sameFits(t, srv, l)
	t.Logf("%d answers found", found)
	if found < 50 {
		t.Fatalf("%d answers found; want many", found)
	}
	// A reload opens the files again: the catalog is read in place from
	// the disk, and answers the same.
	again := openWith(t, s, disk)
	if got := stored(again); !slices.Equal(got, []string{"catalog-models", "parts-catalog"}) {
		t.Fatalf("after a reload: %q", got)
	}
	if n := sameFits(t, srv, again); n != found {
		t.Errorf("after a reload %d answers found; before, %d", n, found)
	}

	// Kept in parts, as IndexedDB keeps it in a browser, of a size that
	// cuts records and sections: read across them, it answers the same.
	disk.partBytes = 97
	parted := openWith(t, s, disk)
	if n := sameFits(t, srv, parted); n != found || disk.spanning == 0 {
		t.Errorf("in parts %d answers found, %d reads across parts; before, %d", n, disk.spanning, found)
	}
	t.Logf("in parts: %d reads across parts", disk.spanning)
}

// openWith opens a Library on s, as open does on a fakeServer.
func openWith(t *testing.T, s *decoderServer, d Disk) *Library {
	t.Helper()
	l, err := OpenLibrary(s.info, s, d)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func blake3Of(b []byte) []byte {
	f, err := vocab.Read("x.jsonl", bytes.NewReader(b))
	if err != nil {
		panic(err)
	}
	return f.Hash[:]
}

// sameFits asks every request of the fixture of both, a page at a time,
// and fails unless they answer alike: the same JSON, or the same refusal.
// Each request is asked again with each of stock's stock and locations.
// It returns how many answers were found.
func sameFits(t *testing.T, srv *nitrovin.Translator, l *Library, stock ...stockAsk) int {
	t.Helper()
	entry := func(vocabulary string, fields ...string) contract.FitFrom {
		e := map[string]string{}
		for i := 0; i < len(fields); i += 2 {
			e[fields[i]] = fields[i+1]
		}
		return contract.FitFrom{Vocabulary: vocabulary, Entry: e}
	}
	model := func(id int32) contract.FitFrom { return contract.FitFrom{SpineRef: contract.SpineRef{Model: id}} }
	froms := []contract.FitFrom{
		{VIN: "1D7RV1GT5AS123456"}, {VIN: "1GCEK19J07Z123456"}, model(1938), model(1850), model(13620),
		entry("catalog-models", "make", "Dodge", "model", "Ram 1500"), entry("catalog-models", "make", "Chevrolet", "model", "Silverado 1500"),
		entry("catalog-models", "make", "Dodge", "model", "Dakota"),
	}
	parts := []*contract.FitPart{nil}
	for _, id := range []string{"mirror", "brake-rotor-front", "brake-rotor-rear", "engine"} {
		parts = append(parts, &contract.FitPart{Spine: "part-types", ID: id})
	}
	parts = append(parts, &contract.FitPart{Vocabulary: "part-codes", Entry: map[string]string{"code": "ROT"}})
	keys := []contract.FitmentKey{
		{Type: "mirror", Number: "MR-1001", Side: "left"}, {Type: "mirror", Number: "MR-1001", Side: "right"},
		{Type: "brake-rotor-front", Number: "BR-2001"}, {Type: "brake-rotor-front", Number: "BR-2002"}, {Type: "engine", Number: "EN-57"},
		{Type: "brake-rotor-front", Number: "BR-1999"}, {Type: "mirror", Number: "none"},
	}
	years := []contract.Null[int32]{{}, contract.Value[int32](2008), contract.Value[int32](2010), contract.Value[int32](2013)}
	tos := [][]string{nil, {"catalog-models"}}
	found := 0
	for _, from := range froms {
		for _, year := range years {
			for _, to := range tos {
				for _, part := range parts {
					for _, side := range []string{"", "left", "right", "both"} {
						for _, limit := range []int{0, 1} {
							found += sameFitStock(t, srv, l, stock, contract.FitRequest{From: from, ModelYear: year, Part: part, Side: side, To: to, Limit: limit})
						}
					}
				}
				for _, limit := range []int{0, 1} {
					found += sameFitStock(t, srv, l, stock, contract.FitRequest{From: from, ModelYear: year, To: to, Related: true, Limit: limit})
				}
			}
		}
	}
	for _, k := range keys {
		for _, year := range years {
			for _, to := range tos {
				for _, limit := range []int{0, 1} {
					found += sameFitStock(t, srv, l, stock, contract.FitRequest{From: contract.FitFrom{Fitment: &k}, ModelYear: year, To: to, Limit: limit})
				}
			}
		}
	}
	return found
}

// stockAsk is the stock a request asks for, and where.
type stockAsk struct {
	stock, location []string
}

// sameFitStock is sameFit of req, and of req asking each of asks.
func sameFitStock(t *testing.T, srv *nitrovin.Translator, l *Library, asks []stockAsk, req contract.FitRequest) int {
	t.Helper()
	found := sameFit(t, srv, l, req)
	for _, a := range asks {
		req.Stock, req.Location = a.stock, a.location
		found += sameFit(t, srv, l, req)
	}
	return found
}

// sameFit asks req of both, and of every page after the first.
func sameFit(t *testing.T, srv *nitrovin.Translator, l *Library, req contract.FitRequest) int {
	t.Helper()
	req.Now = fitNow
	found := 0
	for page := 1; ; page++ {
		want, werr := srv.Fit(context.Background(), req)
		got, gerr := l.Fit(context.Background(), req)
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		if (werr == nil) != (gerr == nil) || werr != nil && werr.Error() != gerr.Error() || !bytes.Equal(wb, gb) {
			rb, _ := json.Marshal(req)
			t.Fatalf("fit %s, page %d:\nbrowser %s (%v)\nserver %s (%v)", rb, page, gb, gerr, wb, werr)
		}
		if werr == nil && want.Status == contract.FitFound {
			found++
		}
		if werr != nil || want.Next == "" {
			return found
		}
		req.After = want.Next
	}
}

// TestLibraryCatalogRefusals: one catalog per nitrovin, counting the server
// and its browsers; a catalog with faults, and storage the browser
// refuses, store nothing.
func TestLibraryCatalogRefusals(t *testing.T) {
	d := embedded(t)
	catalog := fitmentFile(t, "catalog")

	// The server has a catalog: the browser compiles none.
	s := newDecoderServer(t, d, "part-types", "catalog-models")
	s.info.Vocabularies = append(s.info.Vocabularies, contract.VocabularyFile{Name: "their-catalog", Kind: "catalog", Blake3: "ab12"})
	disk := newDisk()
	l := openWith(t, s, disk)
	if l.ServerCatalog() != "their-catalog" || slices.ContainsFunc(s.gets, func(p string) bool { return strings.Contains(p, "their-catalog") }) {
		t.Errorf("server catalog %q, asked for %q", l.ServerCatalog(), s.gets)
	}
	if _, err := compileFixture(t, l, catalog); err == nil || !strings.Contains(err.Error(), "This server has a catalog, their-catalog; one catalog per nitrovin") {
		t.Errorf("with the server's catalog: %v", err)
	}
	if disk.catalog != nil || disk.writing != 0 {
		t.Error("a refused compile stored something")
	}

	s = newDecoderServer(t, d, "part-types", "catalog-models")
	l = openWith(t, s, disk)
	// Faults refuse it, naming the file and line.
	bad := append(slices.Clone(catalog), []byte(`{"fitment":{"type":"no-such-type","number":"X-1"},"applications":[{"vehicle":{"make":"Dodge","model":"Ram 1500"},"options":""}]}`+"\n")...)
	if _, err := compileFixture(t, l, bad); err == nil || !strings.Contains(err.Error(), "catalog.jsonl:9:") || !strings.Contains(err.Error(), "no-such-type") {
		t.Errorf("a faulty catalog: %v", err)
	}
	// Storage the browser refuses stores nothing, and says why.
	disk.quota = 1000
	_, err := compileFixture(t, l, catalog)
	if e, ok := errors.AsType[*pageError](err); !ok || e.Status != http.StatusInsufficientStorage || !strings.Contains(e.Message, "refused the storage") || !strings.Contains(e.Message, "nothing was stored") {
		t.Errorf("over the quota: %v", err)
	}
	if disk.catalog != nil || disk.writing != 0 || len(stored(l)) != 0 {
		t.Errorf("a refused compile stored %v, %d writing, %q", disk.catalog, disk.writing, stored(l))
	}
	disk.quota = 0
	if _, err := compileFixture(t, l, catalog); err != nil {
		t.Fatal(err)
	}
	// The browser holds one: it compiles no other.
	if _, err := compileFixture(t, l, catalog); err == nil || !strings.Contains(err.Error(), "This browser holds a catalog already, parts-catalog; one catalog per nitrovin") {
		t.Errorf("a second catalog: %v", err)
	}
	if _, err := openWith(t, s, disk).Compile(bytes.NewReader(catalog), "again.jsonl", int64(len(catalog)), vocab.DirScratch(t.TempDir()), quiet); err == nil {
		t.Error("a second catalog after a reload compiled")
	}
	// A vocabulary named like it is refused.
	renamed := strings.Replace(string(fitmentFile(t, "part-codes")), `"name":"part-codes"`, `"name":"parts-catalog"`, 1)
	if _, err := l.Load([]Picked{{FileName: "codes.jsonl", Data: []byte(renamed)}}, quiet); err == nil || !strings.Contains(err.Error(), "like the catalog this browser holds") {
		t.Errorf("a vocabulary named like the catalog: %v", err)
	}

	// The server gains a catalog since: the browser's is left out, listed
	// with the reason and no "recompile", and goes on Remove; fit answers
	// no catalog in the browser (the worker sends it to the server).
	s.info.Vocabularies = append(s.info.Vocabularies, contract.VocabularyFile{Name: "their-catalog", Kind: "catalog", Blake3: "ab12"})
	l = openWith(t, s, disk)
	got := stored(l)
	if len(got) != 1 || got[0] != "parts-catalog: this server has a catalog now, their-catalog; one catalog per nitrovin, so fit answers from the server's. Remove this browser's." {
		t.Errorf("the server gained a catalog: %q", got)
	}
	if f, err := l.Fit(context.Background(), contract.FitRequest{From: contract.FitFrom{SpineRef: contract.SpineRef{Model: 1938}}, Now: fitNow}); err != nil || f.Status != contract.FitNoCatalog {
		t.Errorf("fit beside the server's catalog: %v, %s", err, f.Status)
	}
	if err := l.Remove([]string{"parts-catalog"}, false); err != nil || disk.catalog != nil {
		t.Errorf("removing it: %v, %v", err, disk.catalog)
	}
}

// TestBrowserFitServerCatalog: while the server has the catalog, a fit
// link naming one of the browser's files, which the worker never sends to
// the server, is refused in the browser in words.
func TestBrowserFitServerCatalog(t *testing.T) {
	d := embedded(t)
	s := newDecoderServer(t, d, "part-types", "catalog-models")
	s.info.Vocabularies = append(s.info.Vocabularies, contract.VocabularyFile{Name: "their-catalog", Kind: "catalog", Blake3: "ab12"})
	l := openWith(t, s, newDisk())
	if _, err := l.Load([]Picked{{FileName: "part-codes.jsonl", Data: fitmentFile(t, "part-codes")}}, quiet); err != nil {
		t.Fatal(err)
	}
	h := NewInBrowser(l, BrowserConfig{}, log.New(io.Discard, "", 0))
	status, _, body := response(t, h, "GET", "/ui/fit?from.vocabulary=catalog-models&from.entry.make=Dodge&from.entry.model=Ram+1500&model_year=2010&part.vocabulary=part-codes&part.entry.code=ROT", htmx)
	if status != http.StatusConflict || !strings.Contains(body, "This link names part-codes, a file of this browser&#39;s. This server&#39;s catalog, their-catalog, answers fit") {
		t.Errorf("a fit naming the browser's file:\n%s", body)
	}
}

// TestCatalogPrivateWindow: a browser that stores no Blob in IndexedDB, as
// in a private window, says so in words.
func TestCatalogPrivateWindow(t *testing.T) {
	err := storageError("catalog.jsonl", errors.New("a sorted run read 0 of 104 bytes: UnknownError: Error preparing Blob/File data to be stored in object store"))
	if e, ok := errors.AsType[*pageError](err); !ok || !strings.HasPrefix(e.Message, "This browser keeps no files in a private window, so catalog.jsonl was not compiled and nothing was stored.") {
		t.Errorf("a private window: %v", err)
	}
}

// TestLibraryCatalogStale: a stored catalog this release cannot open, or
// whose spine or vehicle vocabulary changed since it was compiled, is
// listed with the reason and "pick the source again to recompile", and
// left out: fit answers no catalog. It is removed like any file, and a
// file it links to goes only with it.
func TestLibraryCatalogStale(t *testing.T) {
	d := embedded(t)
	s, disk := newDecoderServer(t, d, "part-types"), newDisk()
	l := openWith(t, s, disk)
	if _, err := l.Load([]Picked{{FileName: "catalog-models.jsonl", Data: fitmentFile(t, "catalog-models")}}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := compileFixture(t, l, fitmentFile(t, "catalog")); err != nil {
		t.Fatal(err)
	}
	noCatalog := func(l *Library) {
		t.Helper()
		f, err := l.Fit(context.Background(), contract.FitRequest{From: contract.FitFrom{SpineRef: contract.SpineRef{Model: 1938}}, Now: fitNow})
		if err != nil || f.Status != contract.FitNoCatalog {
			t.Errorf("fit without a linked catalog: %v, %s", err, f.Status)
		}
	}

	// Its vehicle vocabulary is replaced by another version.
	models := strings.Replace(string(fitmentFile(t, "catalog-models")), `"version":"2026.11"`, `"version":"2026.12"`, 1)
	if _, err := l.Load([]Picked{{FileName: "catalog-models.jsonl", Data: []byte(models)}}, quiet); err != nil {
		t.Fatal(err)
	}
	for _, l := range []*Library{l, openWith(t, s, disk)} {
		got := stored(l)
		if len(got) != 2 || got[0] != "catalog-models" || !strings.HasPrefix(got[1], `parts-catalog: its vehicle vocabulary "catalog-models" changed since it was compiled (version 2026.11 then, 2026.12 now`) || !strings.HasSuffix(got[1], recompile) {
			t.Errorf("stale vehicles: %q", got)
		}
		noCatalog(l)
	}

	// Picking the source again replaces it, stored over the stale one.
	if _, err := compileFixture(t, l, fitmentFile(t, "catalog")); err != nil {
		t.Fatalf("compiling a stale catalog again: %v", err)
	}
	for _, l := range []*Library{l, openWith(t, s, disk)} {
		if got := stored(l); !slices.Equal(got, []string{"catalog-models", "parts-catalog"}) {
			t.Errorf("compiled again: %q", got)
		}
	}
	if disk.writing != 0 {
		t.Errorf("compiled again: %d writing", disk.writing)
	}

	// Its spine changes: the server's part-types, another version.
	types := strings.Replace(string(fitmentFile(t, "part-types")), `"version":"2026.11"`, `"version":"2026.12"`, 1)
	s2 := newDecoderServer(t, d)
	s2.serve(t, "part-types", []byte(types))
	got := stored(openWith(t, s2, disk))
	if len(got) != 2 || !strings.HasPrefix(got[1], `parts-catalog: its spine "part-types" changed since it was compiled (version 2026.11 then, 2026.12 now`) || !strings.HasSuffix(got[1], recompile) {
		t.Errorf("stale spine: %q", got)
	}

	// A file this release cannot open: another compiled format.
	binary.LittleEndian.PutUint32(disk.catalogData[8:], 1)
	l = openWith(t, s, disk)
	if got := stored(l); len(got) != 2 || !strings.Contains(got[1], "compiled catalog format 1: this nitrovin reads format 3 only") || !strings.HasSuffix(got[1], recompile) {
		t.Errorf("format 1: %q", got)
	}
	noCatalog(l)

	// It goes like any file.
	if err := l.Remove([]string{"parts-catalog"}, false); err != nil {
		t.Fatal(err)
	}
	if disk.catalog != nil || !slices.Equal(stored(l), []string{"catalog-models"}) {
		t.Errorf("after removing the catalog: %v, %q", disk.catalog, stored(l))
	}

	// A file it links to goes only with it.
	disk2 := newDisk()
	l = openWith(t, s, disk2)
	if _, err := l.Load([]Picked{{FileName: "catalog-models.jsonl", Data: fitmentFile(t, "catalog-models")}}, quiet); err != nil {
		t.Fatal(err)
	}
	if _, err := compileFixture(t, l, fitmentFile(t, "catalog")); err != nil {
		t.Fatal(err)
	}
	err := l.Remove([]string{"catalog-models"}, false)
	if r, ok := errors.AsType[*removeRefusal](err); !ok || !slices.Equal(r.Dependants, []string{"parts-catalog"}) {
		t.Fatalf("removing its vehicle vocabulary: %v", err)
	}
	if err := l.Remove([]string{"catalog-models"}, true); err != nil {
		t.Fatal(err)
	}
	if disk2.catalog != nil || len(disk2.records) != 0 || len(stored(l)) != 0 {
		t.Errorf("after removing all: %v, %v, %q", disk2.catalog, disk2.records, stored(l))
	}
}

// TestJSONText: the fit page's JSON is the bytes json.MarshalIndent writes,
// escaped as html/template escapes a string, written in one pass.
func TestJSONText(t *testing.T) {
	d := embedded(t)
	srv := serverTranslator(t, d)
	for _, req := range []contract.FitRequest{
		{From: contract.FitFrom{SpineRef: contract.SpineRef{Model: 1938}}, Related: true, Now: fitNow},
		{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: "brake-rotor-front", Number: "BR-1999"}}, Now: fitNow},
	} {
		fit, err := srv.Fit(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.MarshalIndent(fit, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		got, err := jsonText(fit)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != htmlEscaped(string(want)) {
			t.Errorf("jsonText differs from MarshalIndent, escaped:\n%s\n%s", got, want)
		}
	}
	if got, _ := jsonText(map[string]string{"a": "<b> & 'c' + \"d\""}); got != `{
  &#34;a&#34;: &#34;\u003cb\u003e \u0026 &#39;c&#39; &#43; \&#34;d\&#34;&#34;
}` {
		t.Errorf("escaping: %s", got)
	}
}

// htmlEscaped is s as html/template escapes a string in an element's text.
func htmlEscaped(s string) string {
	var b strings.Builder
	if err := template.Must(template.New("").Parse(`{{.}}`)).Execute(&b, s); err != nil {
		panic(err)
	}
	return b.String()
}
