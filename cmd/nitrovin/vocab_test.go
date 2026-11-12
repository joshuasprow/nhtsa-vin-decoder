package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The synthetic fixture: testdata/vocab loads clean against the embedded
// month (its headers say 2026_10; its ids and values are long-lived), its
// drift/ variant with one drift of each kind, and fitment/ a catalog with
// its spine and vehicle vocabulary.
var (
	fixture        = abs("../../testdata/vocab")
	driftFixture   = abs("../../testdata/vocab/drift")
	fitmentFixture = abs("../../testdata/vocab/fitment")
)

func abs(path string) string {
	p, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return p
}

// fixtureMonth is the data month testdata/vocab's headers say it was built
// against.
const fixtureMonth = "2026_10"

// fixtureData marks a test that runs the fixture against the embedded data:
// when it fails, it names the month, so a refresh that breaks the fixture
// says which month did.
func fixtureData(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("on the embedded data month %s; testdata/vocab was built against %s", embedded.Month, fixtureMonth)
		}
	})
}

// driftDetail is a drift finding's detail on testdata/vocab/drift as the
// embedded month words it: format's %[1]s is the month, and a month other
// than the files' own adds what they were built against.
func driftDetail(format string) string {
	s := fmt.Sprintf(format, embedded.Month)
	if embedded.Month != fixtureMonth {
		s += " (the file was built against " + fixtureMonth + ")"
	}
	return s
}

// epaSample is the EPA sample, cmd/nitrovin-epa/testdata/vehicles.jsonl,
// as built against the embedded month (that package's TestBuild holds a
// build to differ from it in the month alone, which its header names): a
// copy's path and its bytes. The server offers EPA's vocabulary only for
// its own month.
func epaSample(t *testing.T) (string, []byte) {
	t.Helper()
	b, err := os.ReadFile("../nitrovin-epa/testdata/vehicles.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	f, err := vocab.Read("vehicles.jsonl", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte(f.DataMonth), []byte(embedded.Month))
	p := filepath.Join(t.TempDir(), "vehicles.jsonl")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, b
}

// faultyFile writes a vocabulary whose line 2 is a fault.
func faultyFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "faulty.jsonl")
	data := `{"format":1,"kind":"vocabulary","name":"yard","version":"1","subject":"vehicle","fields":["make","model"]}` + "\n" +
		`{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"first":2014,"last":2012}]}` + "\n"
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// findings reads JSON lines of vocab.Finding.
func findings(t *testing.T, s string) []vocab.Finding {
	t.Helper()
	var out []vocab.Finding
	for line := range strings.Lines(s) {
		var f vocab.Finding
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			t.Fatalf("not a finding line: %q", line)
		}
		out = append(out, f)
	}
	return out
}

// TestVocabCheck: `vocab check` exits 0 on clean files and 1, with a line
// per finding, on faulty or drifting ones; it checks its arguments alone.
func TestVocabCheck(t *testing.T) {
	fixtureData(t)
	if r := cli(t, []string{"NITROVIN_VOCAB=" + faultyFile(t)}, "", "vocab", "check", fixture); r.exit != 0 || r.stdout != "" || r.stderr != "" {
		t.Errorf("vocab check on the fixture: exit %d, stdout %q, stderr %q; want exit 0 and nothing", r.exit, r.stdout, r.stderr)
	}

	faulty := faultyFile(t)
	r := cli(t, nil, "", "vocab", "check", faulty)
	if fs := findings(t, r.stdout); r.exit != 1 || r.stderr != "" || len(fs) != 1 ||
		fs[0].File != faulty || fs[0].Line != 2 || fs[0].Severity != vocab.SeverityFault || !strings.Contains(fs[0].Detail, "first year 2014 is after last year 2012") {
		t.Errorf("vocab check on a faulty file: exit %d, stdout %q, stderr %q; want exit 1 and one fault at line 2", r.exit, r.stdout, r.stderr)
	}

	r = cli(t, nil, "", "vocab", "check", driftFixture)
	var got []string
	for _, f := range findings(t, r.stdout) {
		got = append(got, string(f.Severity)+" "+filepath.Base(f.File)+":"+strconv.Itoa(f.Line))
	}
	if want := []string{"drift classics.jsonl:2", "drift yard.jsonl:2", "drift yard.jsonl:3", "drift yard.jsonl:4", "drift yard.jsonl:5", "drift yard.jsonl:7", "drift yard.jsonl:8"}; r.exit != 1 || r.stderr != "" || !slices.Equal(got, want) {
		t.Errorf("vocab check on the drift fixture: exit %d, findings %v, stderr %q; want exit 1 and %v", r.exit, got, r.stderr, want)
	}

	for _, args := range [][]string{{"vocab"}, {"vocab", "check"}, {"vocab", "lint", fixture}} {
		if r := cli(t, nil, "", args...); r.exit != 2 || r.stdout != "" || !strings.Contains(r.stderr, "usage: nitrovin vocab check") {
			t.Errorf("nitrovin %q: exit %d, stdout %q, stderr %q; want a usage error", args, r.exit, r.stdout, r.stderr)
		}
	}
}

// vocabularies reads the names in `nitrovin info`'s vocabularies.
func vocabularies(t *testing.T, stdout string) []string {
	t.Helper()
	var info contract.Info
	if err := json.Unmarshal([]byte(stdout), &info); err != nil {
		t.Fatalf("info: %v: %q", err, stdout)
	}
	names := []string{}
	for _, v := range info.Vocabularies {
		names = append(names, v.Name)
	}
	return names
}

// TestVocabLoad: --vocab (repeatable, on the root and the command), else
// NITROVIN_VOCAB, loads files beside the data on every command; a fault
// refuses with an internal problem naming the flag or variable, the file
// and the line; drift is written to stderr and served.
func TestVocabLoad(t *testing.T) {
	fixtureData(t)
	parts, listing := filepath.Join(fixture, "parts.jsonl"), filepath.Join(fixture, "listing.jsonl")
	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	for _, tc := range []struct {
		env   []string
		args  []string
		names []string
	}{
		{nil, []string{"info"}, []string{}},
		{nil, []string{"--vocab", fixture, "info"}, []string{"classics", "listing", "parts", "yard"}},
		{nil, []string{"--vocab", parts, "info", "--vocab", listing}, []string{"parts", "listing"}},
		{[]string{"NITROVIN_VOCAB=" + parts + string(filepath.ListSeparator) + listing}, []string{"info"}, []string{"parts", "listing"}},
		{[]string{"NITROVIN_VOCAB=" + missing}, []string{"info", "--vocab", parts}, []string{"parts"}},
	} {
		r := cli(t, tc.env, "", tc.args...)
		if r.exit != 0 || r.stderr != "" {
			t.Errorf("%q nitrovin %q: exit %d, stderr %q", tc.env, tc.args, r.exit, r.stderr)
			continue
		}
		if got := vocabularies(t, r.stdout); !slices.Equal(got, tc.names) {
			t.Errorf("%q nitrovin %q: vocabularies %v; want %v", tc.env, tc.args, got, tc.names)
		}
	}

	// Drift: on stderr, one finding per line, and in info.
	// Load leaves model-qualifier drift out of stderr (ADR 0015's cost);
	// info counts it, all five of yard's kinds.
	r := cli(t, []string{"NITROVIN_VOCAB=" + driftFixture}, "", "info")
	if fs := findings(t, r.stderr); r.exit != 0 || len(fs) != 6 || fs[0].Severity != vocab.SeverityDrift {
		t.Errorf("info with the drift fixture: exit %d, stderr %q; want exit 0 and six drift lines", r.exit, r.stderr)
	}
	var info contract.Info
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil || len(info.Vocabularies) != 2 || len(info.Vocabularies[1].Drift) != 5 || info.Vocabularies[1].Drift[4].Kind != "model-qualifier" {
		t.Errorf("info with the drift fixture: %v, %+v", err, info.Vocabularies)
	}
	clock, _ := contract.ParseNow(now)
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	req := contract.Request{VIN: "1HGCM82633A004352", Now: clock}
	if r, w := cli(t, nil, "", "--vocab", driftFixture, "decode", "--now", now, req.VIN), want(t, d, []contract.Request{req}); r.exit != 0 || r.stdout != w || len(findings(t, r.stderr)) != 6 {
		t.Errorf("decode with the drift fixture: exit %d, stdout %q, stderr %q; want the decode, and the drift on stderr", r.exit, r.stdout, r.stderr)
	}

	// Faults refuse every command.
	faulty := faultyFile(t)
	for _, tc := range []struct {
		env    []string
		args   []string
		detail string
	}{
		{nil, []string{"--vocab", faulty, "decode", req.VIN}, "--vocab: " + faulty + ":2: /mappings/0: first year 2014"},
		{nil, []string{"info", "--vocab", fixture, "--vocab", faulty}, "--vocab: " + faulty + ":2: "},
		{nil, []string{"--vocab", faulty, "--version"}, "--vocab: " + faulty + ":2: "},
		{[]string{"NITROVIN_VOCAB=" + faulty}, []string{"stdio"}, "NITROVIN_VOCAB: " + faulty + ":2: "},
		{nil, []string{"--vocab", missing, "info"}, "--vocab: " + missing + ": stat: "},
	} {
		r := cli(t, tc.env, "", tc.args...)
		p := problem(t, r)
		if r.exit != 1 || r.stdout != "" || p.Type != contract.Internal || !strings.HasPrefix(p.Detail, tc.detail) {
			t.Errorf("%q nitrovin %q: exit %d, stdout %q, problem %+v; want exit 1 and an internal problem starting %q", tc.env, tc.args, r.exit, r.stdout, p, tc.detail)
		}
	}
}

// TestServeVocabularies: /v1/info lists the files serve loaded, as
// `nitrovin info` does.
func TestServeVocabularies(t *testing.T) {
	fixtureData(t)
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(driftFixture)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAPI(loaded{tr.Decoder(), tr}, log.New(io.Discard, "", 0), io.Discard, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	resp, err := client.Get(srv.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var info contract.Info
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !reflect.DeepEqual(info.Vocabularies, tr.Info().Vocabularies) {
		t.Errorf("GET /v1/info: status %d, vocabularies %+v; want %+v", resp.StatusCode, info.Vocabularies, tr.Info().Vocabularies)
	}
}

// TestVocabCheckCatalog: `vocab check` checks a catalog with the files it
// names: clean, refused without its spine and vehicle vocabulary, and
// drifting; `info` and GET /v1/info list it with its release and counts.
func TestVocabCheckCatalog(t *testing.T) {
	fixtureData(t)
	if r := cli(t, nil, "", "vocab", "check", fixture, fitmentFixture); r.exit != 0 || r.stdout != "" || r.stderr != "" {
		t.Errorf("vocab check on the catalog fixture: exit %d, stdout %q, stderr %q; want exit 0 and nothing", r.exit, r.stdout, r.stderr)
	}

	catalog := filepath.Join(fitmentFixture, "catalog.jsonl")
	r := cli(t, nil, "", "vocab", "check", catalog)
	var got []string
	for _, f := range findings(t, r.stdout) {
		got = append(got, string(f.Severity)+" "+strconv.Itoa(f.Line)+": "+f.Detail)
	}
	if want := []string{`fault 1: /spine: spine "part-types" is not loaded`, `fault 1: /vehicles: vocabulary "catalog-models" is not loaded`}; r.exit != 1 || !slices.Equal(got, want) {
		t.Errorf("vocab check on the catalog alone: exit %d, findings %v; want exit 1 and %v", r.exit, got, want)
	}

	dir := t.TempDir()
	data, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, `{"redirect":{"vehicle":{"make":"Dodge","model":"Ram 1500"},"from":"engine","to":"mirror","first":2015}}`+"\n"...)
	if err := os.WriteFile(filepath.Join(dir, "catalog.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	r = cli(t, nil, "", "vocab", "check", filepath.Join(fitmentFixture, "part-types.jsonl"), filepath.Join(fitmentFixture, "catalog-models.jsonl"), dir)
	fs := findings(t, r.stdout)
	if r.exit != 1 || len(fs) != 1 || fs[0].Severity != vocab.SeverityDrift || fs[0].Line != 9 || fs[0].Detail != `/redirect/to: part type "mirror" has no application for this vehicle in 2015 on` {
		t.Errorf("vocab check on a drifting catalog: exit %d, findings %+v", r.exit, fs)
	}

	r = cli(t, nil, "", "--vocab", fitmentFixture, "info")
	var info contract.Info
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil || r.exit != 0 {
		t.Fatalf("info with the catalog: exit %d, %v, %q", r.exit, err, r.stdout)
	}
	i := slices.IndexFunc(info.Vocabularies, func(v contract.VocabularyFile) bool { return v.Kind == "catalog" })
	if i < 0 || info.Vocabularies[i].Release != "7.02" || info.Vocabularies[i].CatalogCounts == nil || info.Vocabularies[i].Fitments != 5 {
		t.Errorf("info's catalog: %+v", info.Vocabularies)
	}

	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(fitmentFixture)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAPI(loaded{tr.Decoder(), tr}, log.New(io.Discard, "", 0), io.Discard, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	resp, err := client.Get(srv.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var served contract.Info
	if err := json.NewDecoder(resp.Body).Decode(&served); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !reflect.DeepEqual(served.Vocabularies, tr.Info().Vocabularies) {
		t.Errorf("GET /v1/info: status %d, vocabularies %+v; want %+v", resp.StatusCode, served.Vocabularies, tr.Info().Vocabularies)
	}
}

// TestVocabCompile: `vocab compile` checks its arguments as `vocab check`
// does, refuses on a fault and writes nothing, and otherwise writes the
// catalog compiled (ADR 0021), the same bytes each time; --vocab loads the
// compiled file, refusing it without its spine; `info` lists it with the
// drift its compiling stored; and `vocab check` reads it in full and
// measures it again, as its source.
func TestVocabCompile(t *testing.T) {
	fixtureData(t)
	out := filepath.Join(t.TempDir(), "catalog.compiled")
	catalog := filepath.Join(fitmentFixture, "catalog.jsonl")
	spine, models := filepath.Join(fitmentFixture, "part-types.jsonl"), filepath.Join(fitmentFixture, "catalog-models.jsonl")

	r := cli(t, nil, "", "vocab", "compile", "-o", out, catalog)
	var got []string
	for _, f := range findings(t, r.stdout) {
		got = append(got, string(f.Severity)+" "+strconv.Itoa(f.Line)+": "+f.Detail)
	}
	if want := []string{`fault 1: /spine: spine "part-types" is not loaded`, `fault 1: /vehicles: vocabulary "catalog-models" is not loaded`}; r.exit != 1 || !slices.Equal(got, want) {
		t.Errorf("vocab compile of the catalog alone: exit %d, findings %v; want exit 1 and %v", r.exit, got, want)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a refused compile wrote %s: %v", out, err)
	}

	var compiled [2][]byte
	for i := range compiled {
		if r := cli(t, nil, "", "vocab", "compile", fitmentFixture, "-o", out); r.exit != 0 || r.stdout != "" || r.stderr != "" {
			t.Fatalf("vocab compile: exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		compiled[i] = b
	}
	if !bytes.Equal(compiled[0], compiled[1]) || !bytes.HasPrefix(compiled[0], []byte("nvcatalg")) {
		t.Errorf("two compiles differ, or are not compiled catalogs: %d and %d bytes", len(compiled[0]), len(compiled[1]))
	}

	r = cli(t, nil, "", "--vocab", spine, "--vocab", models, "--vocab", out, "info")
	var info contract.Info
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil || r.exit != 0 || r.stderr != "" || len(info.Vocabularies) != 3 {
		t.Fatalf("info with the compiled catalog: exit %d, %v, stdout %q, stderr %q", r.exit, err, r.stdout, r.stderr)
	}
	source, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	src, err := vocab.Read(catalog, bytes.NewReader(source))
	if err != nil {
		t.Fatal(err)
	}
	c := info.Vocabularies[2]
	if c.Kind != "catalog" || c.Path != out || c.Fitments != 5 || c.Compiled == nil || c.Compiled.SourceBlake3 != fmt.Sprintf("%x", src.Hash) ||
		c.Compiled.Writer != nitrovin.Version() || c.Compiled.DriftMonth != embedded.Month || c.Compiled.DriftMonthDiffers || len(c.Drift) != 0 {
		t.Errorf("info's compiled catalog: %+v, compiled %+v", c, c.Compiled)
	}

	r = cli(t, nil, "", "--vocab", models, "--vocab", out, "info")
	if p := problem(t, r); r.exit != 1 || !strings.Contains(p.Detail, out+`: /spine: spine "part-types" is not loaded`) {
		t.Errorf("info with the compiled catalog and no spine: exit %d, problem %+v", r.exit, p)
	}

	if r := cli(t, nil, "", "vocab", "check", spine, models, out); r.exit != 0 || r.stdout != "" || r.stderr != "" {
		t.Errorf("vocab check on the compiled catalog: exit %d, stdout %q, stderr %q; want exit 0 and nothing", r.exit, r.stdout, r.stderr)
	}
	// A drifting catalog: check finds the same drift in either form, and
	// compile writes it to stderr.
	dir := t.TempDir()
	data := append(slices.Clone(source), `{"redirect":{"vehicle":{"make":"Dodge","model":"Ram 1500"},"from":"engine","to":"mirror","first":2015}}`+"\n"...)
	if err := os.WriteFile(filepath.Join(dir, "catalog.jsonl"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	r = cli(t, nil, "", "vocab", "compile", "-o", out, spine, models, dir)
	if fs := findings(t, r.stderr); r.exit != 0 || r.stdout != "" || len(fs) != 1 || fs[0].Line != 9 || fs[0].Severity != vocab.SeverityDrift {
		t.Errorf("vocab compile of a drifting catalog: exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	jsonl := cli(t, nil, "", "vocab", "check", spine, models, dir)
	r = cli(t, nil, "", "vocab", "check", spine, models, out)
	if want := strings.ReplaceAll(jsonl.stdout, strconv.Quote(filepath.Join(dir, "catalog.jsonl")), strconv.Quote(out)); r.exit != 1 || jsonl.exit != 1 || r.stdout != want || len(findings(t, r.stdout)) != 1 {
		t.Errorf("vocab check on the compiled drifting catalog: exit %d, stdout %q; want exit 1 and %q", r.exit, r.stdout, want)
	}

	for _, args := range [][]string{{"vocab", "compile", fitmentFixture}, {"vocab", "check", "-o", out, fitmentFixture}, {"vocab", "compile", "-o", out}} {
		if r := cli(t, nil, "", args...); r.exit != 2 || r.stdout != "" || !strings.Contains(r.stderr, "usage: nitrovin vocab check") {
			t.Errorf("nitrovin %q: exit %d, stdout %q, stderr %q; want a usage error", args, r.exit, r.stdout, r.stderr)
		}
	}
}
