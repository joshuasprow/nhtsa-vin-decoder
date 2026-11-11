package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"iter"
	"log"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
	"github.com/zeebo/blake3"
)

// fixtureMonth is the vPIC entries testdata/vocab maps to, as 2026_10
// names them, as a spine bundle's source.
type fixtureMonth struct{}

var (
	fixtureMakes  = map[int32]string{467: "Chevrolet", 476: "Dodge", 496: "Ram"}
	fixtureModels = map[int32]struct {
		make int32
		name string
	}{1850: {467, "Silverado"}, 1938: {476, "Ram"}, 13620: {496, "1500"}}
)

func (fixtureMonth) Name() string { return "2026_10" }
func (fixtureMonth) Make(id int32) (string, bool) {
	n, ok := fixtureMakes[id]
	return n, ok
}
func (fixtureMonth) Model(id int32) (int32, string, bool) {
	m, ok := fixtureModels[id]
	return m.make, m.name, ok
}
func (fixtureMonth) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range slices.Sorted(maps.Keys(fixtureMakes)) {
			if !yield(id, fixtureMakes[id]) {
				return
			}
		}
	}
}
func (fixtureMonth) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, id := range slices.Sorted(maps.Keys(fixtureModels)) {
			if m := fixtureModels[id]; m.make == make && !yield(id, m.name) {
				return
			}
		}
	}
}
func (fixtureMonth) ModelIDs() iter.Seq[int32] { return maps.Keys(fixtureModels) }
func (fixtureMonth) Elements() iter.Seq[int32] { return slices.Values([]int32{15, 26, 28, 29, 34, 39}) }
func (fixtureMonth) Element(id int32) (bool, bool) {
	return id != 34, slices.Contains([]int32{15, 26, 28, 29, 34, 39}, id)
}
func (fixtureMonth) Attribute(element, id int32) bool  { return element != 34 }
func (fixtureMonth) Text(element int32, s string) bool { return element == 34 }
func (fixtureMonth) Values() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		for _, v := range []struct {
			e int32
			s string
		}{{15, "2"}, {34, "1500"}, {34, "DS (1500)"}, {39, "1"}, {39, "2"}} {
			if !yield(v.e, v.s) {
				return
			}
		}
	}
}

// memDisk is a Disk in memory, which counts what it is asked.
type memDisk struct {
	mu        sync.Mutex
	records   map[string]Record
	texts     map[string][]byte
	indexes   map[string][]byte
	selection Selection
	// textReads counts Text calls; fail fails Change.
	textReads int
	fail      error
}

func newDisk() *memDisk {
	return &memDisk{records: map[string]Record{}, texts: map[string][]byte{}, indexes: map[string][]byte{}}
}

func (d *memDisk) Records() ([]Record, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Collect(maps.Values(d.records)), nil
}

func (d *memDisk) Index(b string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.indexes[b], nil
}

func (d *memDisk) PutIndex(b string, idx []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.indexes[b] = idx
	return nil
}

func (d *memDisk) Text(b string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.textReads++
	t, ok := d.texts[b]
	if !ok {
		return nil, errors.New("its text is not stored")
	}
	return t, nil
}

func (d *memDisk) Selection() (Selection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cloneSelection(d.selection), nil
}

// Change does what worker.js's change does: puts, deletes, stores the
// selection, and drops texts and indexes no record keeps.
func (d *memDisk) Change(put []Stored, remove []string, selection Selection) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail != nil {
		return d.fail
	}
	for _, n := range remove {
		delete(d.records, n)
	}
	for _, p := range put {
		d.records[p.Name] = p.Record
		d.texts[p.Blake3] = p.Text
		d.indexes[p.Blake3] = p.Index
	}
	d.selection = d.cloneSelection(selection)
	kept := map[string]bool{}
	for _, r := range d.records {
		kept[r.Blake3] = true
	}
	for b := range d.texts {
		if !kept[b] {
			delete(d.texts, b)
			delete(d.indexes, b)
		}
	}
	return nil
}

// cloneSelection is s, its lists the disk's own, as IndexedDB keeps a copy.
func (d *memDisk) cloneSelection(s Selection) Selection {
	if s == nil {
		return nil
	}
	out := Selection{}
	for spine, names := range s {
		out[spine] = slices.Clone(names)
	}
	return out
}

// fakeServer is the server as a Remote: the bundle, its files, and
// decodes, each answering status with body; pages, when set, answers a
// VIN's decode as the page shows it (decodedPath); down fails every
// request.
type fakeServer struct {
	info    contract.Info
	files   map[string][]byte // by path
	decodes map[string]string // body by VIN
	pages   http.Handler
	status  int
	down    bool
	gets    []string
}

func (s *fakeServer) Get(path string) (int, []byte, error) {
	s.gets = append(s.gets, path)
	if s.down {
		return 0, nil, errors.New("Failed to fetch")
	}
	if b, ok := s.files[path]; ok {
		return http.StatusOK, b, nil
	}
	if s.pages != nil && strings.HasPrefix(path, decodedPath+"?") {
		w := httptest.NewRecorder()
		s.pages.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w.Code, w.Body.Bytes(), nil
	}
	if vin, ok := strings.CutPrefix(path, "/v1/decode/"); ok {
		vin, _, _ = strings.Cut(vin, "?")
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		return status, []byte(s.decodes[vin]), nil
	}
	return http.StatusNotFound, nil, nil
}

// fixtureFile is testdata/vocab/name.jsonl's bytes.
func fixtureFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "vocab", name+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// server is a fakeServer of release testInfo's month, with the fixture
// files names loaded.
func server(t *testing.T, names ...string) *fakeServer {
	t.Helper()
	var bundle bytes.Buffer
	if err := vocab.WriteBundle(&bundle, fixtureMonth{}); err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{info: testInfo, files: map[string][]byte{}, decodes: map[string]string{}}
	s.info.Vocabularies = nil
	for _, n := range names {
		b := fixtureFile(t, n)
		sum := blake3.Sum256(b)
		s.info.Vocabularies = append(s.info.Vocabularies, contract.VocabularyFile{Name: n, Blake3: fmt.Sprintf("%x", sum)})
	}
	for i, n := range names {
		s.files[FilePath(s.info, s.info.Vocabularies[i])] = fixtureFile(t, n)
	}
	s.files[SpinePath(s.info)] = bundle.Bytes()
	return s
}

func open(t *testing.T, s *fakeServer, d Disk) *Library {
	t.Helper()
	l, err := OpenLibrary(s.info, s, d)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func pick(t *testing.T, names ...string) []Picked {
	var out []Picked
	for _, n := range names {
		out = append(out, Picked{FileName: n + ".jsonl", Data: fixtureFile(t, n)})
	}
	return out
}

func quiet(string) {}

// stored is the library's files as "name: fault" lines, "name" when they
// link.
func stored(l *Library) []string {
	var out []string
	for _, f := range l.Files() {
		s := f.Name
		if f.Fault != "" {
			s += ": " + f.Fault
		}
		out = append(out, s)
	}
	return out
}

// translateLines is a translation's spine and targets as lines.
func translateLines(t *testing.T, l *Library, q string) []string {
	t.Helper()
	v, err := url.ParseQuery(q)
	if err != nil {
		t.Fatal(err)
	}
	req, err := contract.ParseTranslateQuery(v)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := l.Translate(req)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	out := []string{string(tr.From.Status)}
	for _, s := range tr.Spine {
		out = append(out, refText(s.SpineRef))
	}
	for _, to := range tr.To {
		for _, e := range to.Entries {
			out = append(out, to.Vocabulary+": "+e.Entry["make"]+" "+e.Entry["model"]+e.Entry["category"])
		}
	}
	return out
}

// TestLibraryBatch: files picked together link as one batch whatever their
// order, with the server's; a batch that does not link stores nothing.
func TestLibraryBatch(t *testing.T) {
	s, d := server(t, "classics"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "listing", "yard", "parts"), quiet); err != nil {
		t.Fatal(err)
	}
	if got := stored(l); !slices.Equal(got, []string{"listing", "parts", "yard"}) {
		t.Errorf("stored %q", got)
	}
	got := translateLines(t, l, "from.vocabulary=yard&from.entry.make=Packard&from.entry.model=Clipper&to=yard")
	if want := []string{"mapped", "ext:classics/packard-clipper", "ext:classics/packard", "yard: Packard Clipper"}; !slices.Equal(got, want) {
		t.Errorf("Packard Clipper through the server's classics: %q; want %q", got, want)
	}

	// Without the server's classics, yard alone does not link.
	s2, d2 := server(t), newDisk()
	l2 := open(t, s2, d2)
	_, err := l2.Load(pick(t, "yard"), quiet)
	if err == nil || !strings.Contains(err.Error(), `extension set "classics" is not loaded`) || !strings.Contains(err.Error(), "yard.jsonl") {
		t.Errorf("yard without classics: %v", err)
	}
	if len(d2.records) != 0 || len(stored(l2)) != 0 {
		t.Errorf("a refused batch stored %v", d2.records)
	}
	// A file named like the server's is refused: one name, one file.
	if _, err := l.Load(pick(t, "classics"), quiet); err == nil || !strings.Contains(err.Error(), "like a file this server loaded") {
		t.Errorf("classics again: %v", err)
	}
	// A file that does not read is refused, naming it and its line.
	bad := Picked{FileName: "bad.jsonl", Data: []byte(`{"format":1,"kind":"spine","name":"bad","version":"1"}` + "\n" + `{"id":"x","parent":"y"}`)}
	if _, err := l.Load([]Picked{bad}, quiet); err == nil || !strings.Contains(err.Error(), "bad.jsonl:2:") {
		t.Errorf("a faulty file: %v", err)
	}
	// Storage failing stores nothing and changes nothing.
	d.fail = errors.New("QuotaExceededError")
	if _, err := l.Load(pick(t, "listing"), quiet); err == nil {
		t.Errorf("a load storage refused succeeded")
	}
	d.fail = nil
	if got := stored(l); !slices.Equal(got, []string{"listing", "parts", "yard"}) {
		t.Errorf("after refusals: stored %q", got)
	}
}

// TestLibraryColdStart: a library opened again on the same disk reads the
// stored indexes, parsing no file, and answers the same.
func TestLibraryColdStart(t *testing.T) {
	s, d := server(t, "classics"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "listing", "yard", "parts"), quiet); err != nil {
		t.Fatal(err)
	}
	const q = "from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500&model_year=2010&to=yard"
	want := translateLines(t, l, q)
	d.textReads = 0
	again := open(t, s, d)
	if d.textReads != 0 {
		t.Errorf("a cold start read %d files' texts; want their indexes alone", d.textReads)
	}
	if got := translateLines(t, again, q); !slices.Equal(got, want) {
		t.Errorf("cold: %q; want %q", got, want)
	}
	// Without an index (another release's was dropped), the text is
	// parsed again and its index stored.
	clear(d.indexes)
	open(t, s, d)
	if d.textReads != 3 || len(d.indexes) != 4 {
		t.Errorf("without indexes: %d texts read, %d indexes stored; want 3, 4 (the server's classics too)", d.textReads, len(d.indexes))
	}
}

// TestLibraryFaults: a stored file that no longer reads, or no longer
// links, is listed with its fault, left out, and can be removed.
func TestLibraryFaults(t *testing.T) {
	s, d := server(t, "classics"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "yard", "listing", "parts"), quiet); err != nil {
		t.Fatal(err)
	}
	// The listing's text now fails to read, and there is no index for it.
	for b, r := range d.records {
		if r.Name == "listing" {
			d.texts[r.Blake3] = []byte(`{"format":1,"kind":"vocabulary"}`)
			delete(d.indexes, r.Blake3)
		}
		_ = b
	}
	// The server no longer loads classics, which yard maps into.
	s2 := server(t)
	s2.files[SpinePath(s2.info)] = s.files[SpinePath(s.info)]
	l = open(t, s2, d)
	got := stored(l)
	if len(got) != 3 || !strings.HasPrefix(got[0], "listing: ") || got[1] != "parts" || !strings.Contains(got[2], `yard: yard.jsonl:7: /mappings/0/ext: extension set "classics" is not loaded`) {
		t.Errorf("stored %q", got)
	}
	if err := l.Remove([]string{"listing", "yard"}, false); err != nil {
		t.Fatal(err)
	}
	if got := stored(l); !slices.Equal(got, []string{"parts"}) || len(d.records) != 1 {
		t.Errorf("after removing the faulty: %q, %d records", got, len(d.records))
	}
}

// TestLibraryRemove: removing a file others link to is refused, naming
// them, and changes nothing; with all, they go too.
func TestLibraryRemove(t *testing.T) {
	s, d := server(t), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "classics", "yard", "parts", "listing"), quiet); err != nil {
		t.Fatal(err)
	}
	err := l.Remove([]string{"classics"}, false)
	refusal, ok := errors.AsType[*removeRefusal](err)
	if !ok || !slices.Equal(refusal.Dependants, []string{"yard"}) {
		t.Fatalf("removing classics: %v", err)
	}
	if got := stored(l); len(got) != 4 || len(d.records) != 4 {
		t.Errorf("a refused remove changed %q", got)
	}
	if got := translateLines(t, l, "from.vocabulary=yard&from.entry.make=Packard&from.entry.model=Clipper&to=yard"); got[0] != "mapped" {
		t.Errorf("after a refused remove: %q", got)
	}
	if err := l.Remove([]string{"classics"}, true); err != nil {
		t.Fatal(err)
	}
	if got := stored(l); !slices.Equal(got, []string{"listing", "parts"}) || len(d.records) != 2 {
		t.Errorf("after removing all: %q", got)
	}
	if err := l.Remove([]string{"yard"}, false); err == nil || !strings.Contains(err.Error(), `No file named "yard"`) {
		t.Errorf("removing what is gone: %v", err)
	}
	// Replacing a file in place keeps its name, once.
	if _, err := l.Load(pick(t, "parts"), quiet); err != nil {
		t.Fatal(err)
	}
	if got := stored(l); !slices.Equal(got, []string{"listing", "parts"}) {
		t.Errorf("after replacing parts: %q", got)
	}
}

// TestLibrarySelection: each vocabulary loaded joins the selected targets
// on its spine, once, beside what the visitor selected before; a spine or
// an extension set does not. A translation's targets replace those on
// their spine alone. A removed file leaves the selection, and so do the
// dependants removed with it.
func TestLibrarySelection(t *testing.T) {
	s, d := server(t), newDisk()
	l := open(t, s, d)
	d.selection = Selection{"vpic": {"elsewhere"}}
	check := func(step, want string) {
		t.Helper()
		got, err := l.Selection()
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(got) != want {
			t.Errorf("%s: selection %v; want %s", step, got, want)
		}
	}
	if _, err := l.Load(pick(t, "classics", "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	check("a load", "map[vpic:[elsewhere yard]]")
	if _, err := l.Load(pick(t, "parts", "listing"), quiet); err != nil {
		t.Fatal(err)
	}
	check("a second load", "map[parts:[listing] vpic:[elsewhere yard]]")
	if _, err := l.Load(pick(t, "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	check("yard replaced", "map[parts:[listing] vpic:[elsewhere yard]]")
	if err := l.Select("vpic", []string{"yard"}); err != nil {
		t.Fatal(err)
	}
	check("a translation to yard", "map[parts:[listing] vpic:[yard]]")
	if err := l.Remove([]string{"classics"}, false); err == nil {
		t.Fatal("removing classics alone: no refusal")
	}
	check("a refused remove", "map[parts:[listing] vpic:[yard]]")
	if err := l.Remove([]string{"classics"}, true); err != nil {
		t.Fatal(err)
	}
	check("classics removed with yard", "map[parts:[listing] vpic:[]]")
}

// TestLibraryDecode: a VIN is decoded by the server; a decode of another
// data month is refused, a server that cannot be reached is said in words,
// and the server's refusal is passed through.
func TestLibraryDecode(t *testing.T) {
	s, d := server(t, "classics"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	const decode = `{"vin":"1D7RV1GT5AS123456","wmi":"1D7","check_digit_valid":true,"data_month":"%s","decoder_version":"v9","items":[` +
		`{"element_id":26,"value":"DODGE","attribute_id":"476"},{"element_id":28,"value":"Ram","attribute_id":"1938"},{"element_id":29,"value":"2010","attribute_id":"2010"},{"element_id":34,"value":"DS (1500)","attribute_id":"DS (1500)"}]}`
	s.decodes["1D7RV1GT5AS123456"] = fmt.Sprintf(decode, "2026_10")
	req := contract.TranslateRequest{From: contract.TranslateFrom{VIN: "1D7RV1GT5AS123456"}, To: []string{"yard"}}
	tr, err := l.Translate(req)
	if err != nil {
		t.Fatal(err)
	}
	if tr.DataMonth != "2026_10" || tr.DecoderVersion != "v9" || len(tr.To[0].Entries) == 0 {
		t.Errorf("translation %+v", tr)
	}
	s.decodes["1D7RV1GT5AS123456"] = fmt.Sprintf(decode, "2026_11")
	_, err = l.Translate(req)
	if e, ok := errors.AsType[*pageError](err); !ok || e.Status != http.StatusConflict || !strings.Contains(e.Message, "data month 2026_11") {
		t.Errorf("another month: %v", err)
	}
	s.status, s.decodes["1D7RV1GT5AS123456"] = http.StatusBadRequest, `{"type":"https://nitrovin.app/problems/invalid-request","title":"Invalid request","status":400,"detail":"vin is too long","pointer":"/vin"}`
	_, err = l.Translate(req)
	if p, ok := errors.AsType[*contract.Problem](err); !ok || p.Status != 400 || p.Detail != "vin is too long" {
		t.Errorf("the server's refusal: %v", err)
	}
	s.down = true
	_, err = l.Translate(req)
	if e, ok := errors.AsType[*pageError](err); !ok || e.Status != http.StatusServiceUnavailable || !strings.Contains(e.Message, "works without it") || strings.Contains(e.Message, "Failed to fetch") {
		t.Errorf("offline: %v", err)
	}
	// A name still translates offline.
	if got := translateLines(t, l, "from.vocabulary=yard&from.entry.make=Chevy&from.entry.model=Silverado&to=yard"); got[0] != "mapped" {
		t.Errorf("offline name: %q", got)
	}
}

// TestBrowserPages: the worker's handler lists the merged catalog, each
// row marked by where it lives, with the drift of each file, offers EPA's
// vocabulary unless the browser holds it, and refuses a remove with
// dependants as the panel, sent to it whatever the status; the server's
// says the files need the worker.
func TestBrowserPages(t *testing.T) {
	s, d := server(t, "parts"), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "classics", "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	var yard string
	for _, f := range l.Files() {
		if f.Name == "yard" {
			yard = f.Blake3
		}
	}
	offer := &Offer{Name: "yard", Version: "2026.12", Blake3: "ff00", Size: 956_057, URL: testAssets + "/vocab/ff00/yard.jsonl"}
	h := NewInBrowser(l, offer, log.New(io.Discard, "", 0))
	status, _, body := response(t, h, "GET", "/ui/files", htmx)
	for _, want := range []string{
		`<tr data-where="vpic"><th scope="row"><a href="/ui/vocabularies/vpic">vpic</a></th>` + "\n<td>spine of vehicles\n", `<dd class="version">2026_10</dd>` + "\n<dt>Kept in</dt><dd>built in</dd>",
		`<tr data-where="server"><th scope="row"><a href="/ui/vocabularies/parts">parts</a></th>` + "\n<td>spine of parts\n", `<dd class="version">2026.11</dd>` + "\n<dt>Kept in</dt><dd>this server</dd>",
		`<tr data-where="browser"><th scope="row"><a href="/ui/vocabularies/classics">classics</a></th>` + "\n<td>extension set of vehicles → vpic, ", "<dt>Kept in</dt><dd>this browser: classics.jsonl, ",
		`<td>vocabulary of vehicles: make, model → vpic, `,
		`<button type="submit">Remove<span class="visually-hidden"> yard</span></button>`,
		`id="vocab-files"`,
		`id="load-epa" data-url="` + testAssetsAttr + `/vocab/ff00/yard.jsonl" data-file="yard.jsonl"`,
		"956 kB", "replacing version 2026.11",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("files lack %s:\n%s", want, body)
		}
	}
	if status != 200 || strings.Contains(body, "Remove parts") || strings.Contains(body, "No files are kept") {
		t.Errorf("files: %d\n%s", status, body)
	}
	offer.Blake3 = yard
	if _, _, body := response(t, h, "GET", "/ui/files", htmx); strings.Contains(body, "load-epa") || !strings.Contains(body, "EPA's vocabulary, yard 2026.12, is kept in this browser.") {
		t.Errorf("files with the offer held:\n%s", body)
	}
	form := func(v string, header http.Header) (int, http.Header, string) {
		r := httptest.NewRequest("POST", "/ui/files/remove", strings.NewReader(v))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, vs := range header {
			r.Header[k] = vs
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code, w.Result().Header, w.Body.String()
	}
	status, hdr, body := form("name=classics", htmx)
	if status != http.StatusConflict || hdr.Get("HX-Retarget") != filesTarget || !strings.Contains(body, "classics cannot go alone: yard link to it.") || !strings.Contains(body, `name="all" value="true"`) {
		t.Errorf("refused remove: %d %v\n%s", status, hdr, body)
	}
	status, hdr, _ = form("name=classics&all=true", nil)
	if status != http.StatusSeeOther || hdr.Get("Location") != homePath || len(l.Files()) != 0 {
		t.Errorf("remove all without htmx: %d %v, %d files", status, hdr, len(l.Files()))
	}

	srv := New(backend{info: testInfo}, log.New(io.Discard, "", 0))
	for _, m := range []string{"GET /ui/files", "POST /ui/files/remove"} {
		method, path, _ := strings.Cut(m, " ")
		status, _, body := response(t, srv, method, path, nil)
		if status != http.StatusNotFound || !strings.Contains(body, "never uploaded") {
			t.Errorf("server %s: %d\n%s", m, status, body)
		}
	}
}

// TestBrowserDrift: the panel counts each file's drift by kind, as
// /v1/info does, its examples behind a disclosure, and lists a stored file
// that faults with its fault and a Remove.
func TestBrowserDrift(t *testing.T) {
	s, d := server(t), newDisk()
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "drift/classics", "drift/yard"), quiet); err != nil {
		t.Fatal(err)
	}
	byName := map[string]StoredFile{}
	for _, f := range l.Files() {
		byName[f.Name] = f
	}
	if c := byName["yard"].Drift; len(c) != 3 || c[0].Kind != "make" || c[0].Count != 1 || c[1].Kind != "model" || c[1].Count != 1 || c[2].Kind != "qualifier" || c[2].Count != 2 {
		t.Errorf("yard's drift: %+v", c)
	}
	if c := byName["classics"].Drift; len(c) != 1 || c[0].Kind != "extension" || c[0].Examples[0].Line != 2 {
		t.Errorf("classics's drift: %+v", c)
	}
	d.records["broken"] = Record{Name: "broken", FileName: "broken.jsonl", Blake3: "00", Size: 3}
	d.texts["00"] = []byte("{}\n")
	h := NewInBrowser(open(t, s, d), nil, log.New(io.Discard, "", 0))
	_, _, body := response(t, h, "GET", "/ui/files", htmx)
	for _, want := range []string{
		"<details><summary>Drift: 1 make, 1 model, 2 qualifier</summary><ul><li>line 4: /mappings/0/make: vPIC make 99999 is not in data month 2026_10</li><li>line 5: /mappings/0/model: vPIC model 99999 is not in data month 2026_10</li><li>line 2: ",
		"<details><summary>Drift: 1 extension</summary>",
		`<tr data-where="browser"><th scope="row">broken</th>`,
		`<span class="error">Left out: broken.jsonl:1: `,
		`<button type="submit">Remove<span class="visually-hidden"> broken</span></button>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("files lack %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "load-epa") {
		t.Errorf("an offer, with none made:\n%s", body)
	}
}

// TestServiceWorker: /sw.js and the module only when the server has one;
// /sw.js names the release and the server's files, is never cached
// without asking, and alone allows WebAssembly.
func TestServiceWorker(t *testing.T) {
	h := New(backend{info: testInfo}, log.New(io.Discard, "", 0))
	if status, _, _ := response(t, h, "GET", "/sw.js", nil); status != 404 {
		t.Errorf("/sw.js without a module: %d", status)
	}
	if status, _, _ := response(t, h, "GET", testAssets+"/sw.wasm", nil); status != 404 {
		t.Errorf("sw.wasm without a module: %d", status)
	}
	_, _, body := response(t, h, "GET", "/", nil)
	if !strings.Contains(body, "built without its browser translator") || strings.Contains(body, "data-worker") {
		t.Errorf("the page without a module:\n%s", body)
	}

	h.WithWorker(Worker{Module: []byte("\x00asm"), Exec: []byte("// go")})
	status, hdr, body := response(t, h, "GET", "/sw.js", nil)
	if status != 200 || hdr.Get("Cache-Control") != "no-cache" || hdr.Get("Content-Security-Policy") != workerCSP || !strings.HasPrefix(hdr.Get("Content-Type"), "text/javascript") {
		t.Errorf("/sw.js: %d %v", status, hdr)
	}
	for _, want := range []string{`"release":"` + Release(testInfo) + `"`, `"name":"yard"`, `"blake3":"ab12"`, `importScripts(...["` + testAssets + `/wasm_exec.js","` + testAssets + `/worker.js"]);`} {
		if !strings.Contains(body, want) {
			t.Errorf("/sw.js lacks %s:\n%s", want, body)
		}
	}
	if strings.Contains(CSP, "wasm-unsafe-eval") {
		t.Error("the pages' CSP allows WebAssembly")
	}
	status, hdr, body = response(t, h, "GET", testAssets+"/sw.wasm", nil)
	if status != 200 || hdr.Get("Content-Type") != "application/wasm" || hdr.Get("Cache-Control") != immutable || body != "\x00asm" {
		t.Errorf("sw.wasm: %d %v", status, hdr)
	}
	_, _, body = response(t, h, "GET", "/", nil)
	if !strings.Contains(body, `data-worker="/sw.js"`) || !strings.Contains(body, "/vocab.js") {
		t.Errorf("the page with a module:\n%s", body)
	}
	// A link naming a file this server lacks says where such a file lives.
	status, _, body = response(t, h, "GET", "/?from.vocabulary=mine&from.entry.make=x&to=mine", nil)
	if status != 404 || !strings.Contains(body, "open this link in the browser that holds it") {
		t.Errorf("a browser file's link on the server: %d\n%s", status, body)
	}

	// EPA's vocabulary is offered, and published among the release's
	// assets, when it is of the server's data month and the server loaded
	// no file of its name.
	epa, err := os.ReadFile(filepath.Join("..", "..", "cmd", "nitrovin-epa", "testdata", "vehicles.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sum := blake3.Sum256(epa)
	epaURL := fmt.Sprintf("%s/vocab/%x/epa-vehicles.jsonl", testAssets, sum)
	h = New(backend{info: testInfo}, log.New(io.Discard, "", 0)).WithWorker(Worker{Module: []byte("\x00asm"), Exec: []byte("// go"), EPA: epa})
	_, _, body = response(t, h, "GET", "/sw.js", nil)
	if want := fmt.Sprintf(`"epa":{"name":"epa-vehicles","version":"2026-06-09+2026_10","blake3":"%x","size":%d,"url":"%s"}`, sum, len(epa), epaURL); !strings.Contains(body, want) {
		t.Errorf("/sw.js lacks %s:\n%s", want, body)
	}
	status, hdr, body = response(t, h, "GET", epaURL, nil)
	if status != 200 || hdr.Get("Content-Type") != "application/jsonl" || hdr.Get("Cache-Control") != immutable || body != string(epa) {
		t.Errorf("EPA's vocabulary: %d %v", status, hdr)
	}
	for name, info := range map[string]contract.Info{
		"another data month": {DecoderVersion: testInfo.DecoderVersion, DataMonth: "2026_09", ArtifactHash: testInfo.ArtifactHash},
		"a server file of its name": {DecoderVersion: testInfo.DecoderVersion, DataMonth: testInfo.DataMonth, ArtifactHash: testInfo.ArtifactHash,
			Vocabularies: []contract.VocabularyFile{{Name: "epa-vehicles", Blake3: "ab12"}}},
	} {
		h := New(backend{info: info}, log.New(io.Discard, "", 0)).WithWorker(Worker{Module: []byte("\x00asm"), Exec: []byte("// go"), EPA: epa})
		if _, _, body := response(t, h, "GET", "/sw.js", nil); strings.Contains(body, `"epa"`) {
			t.Errorf("%s: EPA offered:\n%s", name, body)
		}
	}
}
