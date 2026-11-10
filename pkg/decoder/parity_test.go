package decoder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestParity is ADR 0008's offline gate: Project against an answer key of
// the freshened oracle (ADR 0006), with no Postgres. By default the key is
// the frozen one, testdata/parity/<month>/ (the one directory there):
// corpus.tsv, answer-key.csv as scripts/oracle.sh decode wrote it for that
// corpus, and manifest.json. Every input row of the key is one call,
// spvindecode(vin, year => hint) at the instant the run started; its rows
// must equal the key's on all 15 columns as a multiset, every value exactly
// (element 144 included), Project's items must keep the procedure's
// group-rank order (the key, sorted, has no order of its own to compare),
// and they must lay out in both shapes (contract.Vpic, contract.Nitrovin),
// so an element the procedure repeats but the contract does not call
// multi-valued fails here, not in a decode (#165).
// The key never holds a raise, since scripts/oracle.sh decode stops on one,
// so a Project error is a disagreement. One differing row fails the test.
//
// NITROVIN_ARTIFACT is the artifact of the key's month. On another month the
// test skips, as TestEngineAnswerKey does, until the refresh re-freezes:
//
//	NITROVIN_ARTIFACT=$PWD/nitrovin.artifact go test ./pkg/decoder -run TestParity
//
// Three flags serve the refresh. Each takes a scripts/oracle.sh run directory
// laid out as oracle.yml's artifact: corpus.tsv, freshened/answer-key.csv,
// optionally stock/answer-key.csv, and manifest.json with month,
// dump_bytes, dump_sha256, started_at, server, collate, ctype and
// keys.freshened.{wmiyearvalidchars_rows, answer_key_sha256}.
//
//   - -parity-run DIR checks the run's whole freshened key instead of the
//     frozen one: the refresh's gate, generated VINs included. A run of a
//     month other than the artifact's fails.
//   - -update-parity DIR re-freezes. The run's corpus.tsv must begin with
//     scripts/oracle.sh corpus of the committed corpus.tsv (the committed
//     VINs and any new witness); generated VINs may follow and are not
//     frozen. That prefix, the key's rows for it and a new manifest.json
//     become testdata/parity/<the run's month>/, replacing the previous
//     month's directory, and the check runs on the new key.
//   - -parity-report DIR writes the parity report there (writeParityReport),
//     with the run's stock key beside the freshened one when it has one.
//     ADR 0008's coverage figure is not the test's to give: run it with
//     -coverprofile and append go tool cover -func's total.
//
// A month's frozen directory, once replaced, lives on as the asset
// parity-<month>.tar.gz of that month's nitrovin-data release (ADR 0008):
// tar -C testdata/parity -czf parity-<month>.tar.gz <month>.
func TestParity(t *testing.T) {
	path := os.Getenv("NITROVIN_ARTIFACT")
	if path == "" {
		t.Skip("NITROVIN_ARTIFACT not set: no artifact to decode with")
	}
	if *updateParity != "" && *parityRun != "" {
		t.Fatal("-update-parity and -parity-run each name the key to check: give one")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := artifact.Open(data)
	if err != nil {
		t.Fatal(err)
	}

	var src paritySource
	switch {
	case *parityRun != "":
		src = runSource(t, *parityRun)
	case *updateParity != "":
		run := runSource(t, *updateParity)
		if run.month != a.Month() {
			t.Fatalf("the run is %s's; the artifact is %s", run.month, a.Month())
		}
		src = freezeParity(t, run)
	default:
		src = frozenSource(t)
	}
	if src.month != a.Month() {
		if *parityRun != "" {
			t.Fatalf("the run is %s's; the artifact is %s", src.month, a.Month())
		}
		t.Skipf("the frozen key is %s's; the artifact is %s", src.month, a.Month())
	}

	res := checkParity(t, newEngine(a), src)
	if *parityReportDir != "" {
		writeParityReport(t, *parityReportDir, src, res)
	}
}

var (
	updateParity    = flag.String("update-parity", "", "re-freeze testdata/parity from this scripts/oracle.sh run directory")
	parityRun       = flag.String("parity-run", "", "check the engine against this scripts/oracle.sh run directory's freshened key instead of the frozen one")
	parityReportDir = flag.String("parity-report", "", "write the parity report (report.md, report.jsonl) into this directory")
)

// parityRoot holds the frozen key: one directory, named for its month.
const parityRoot = "../../testdata/parity"

// keyHeader is an answer key's CSV header (scripts/oracle.sh KEY_COLUMNS).
const keyHeader = "input_row,vin,year_hint,groupname,variable,value,itempatternid,itemvinschemaid,itemkeys,itemelementid,itemattributeid,itemcreatedon,itemwmiid,code,datatype,decode,itemsource,itemtobeqced"

// parityManifest is a frozen key's manifest.json: the dump and oracle of
// the run it was cut from, the instant its calls are decoded at, and the
// hashes of the two files beside it.
type parityManifest struct {
	Month           string `json:"month"`
	DumpBytes       int64  `json:"dump_bytes"`
	DumpSHA256      string `json:"dump_sha256"`
	Oracle          string `json:"oracle"`
	Now             string `json:"now"`
	Server          string `json:"server"`
	Collate         string `json:"collate"`
	Ctype           string `json:"ctype"`
	CorpusSHA256    string `json:"corpus_sha256"`
	VINCount        int    `json:"vin_count"`
	AnswerRows      int    `json:"answer_rows"`
	AnswerKeySHA256 string `json:"answer_key_sha256"`
}

// runManifest is the part of a run's manifest.json (oracle.yml's) the
// freeze and the run check read.
type runManifest struct {
	Month      string `json:"month"`
	DumpBytes  int64  `json:"dump_bytes"`
	DumpSHA256 string `json:"dump_sha256"`
	StartedAt  string `json:"started_at"`
	Server     string `json:"server"`
	Collate    string `json:"collate"`
	Ctype      string `json:"ctype"`
	Keys       struct {
		Freshened struct {
			WmiyearvalidcharsRows int    `json:"wmiyearvalidchars_rows"`
			AnswerKeySHA256       string `json:"answer_key_sha256"`
		} `json:"freshened"`
	} `json:"keys"`
}

// paritySource is a key to check: its files, month and decode instant.
type paritySource struct {
	label, month string
	now          time.Time
	corpus, key  string
	stock        string // the stock key, or "" for none
	run          runManifest
}

// frozenSource is the frozen key, checked against its manifest.
func frozenSource(t *testing.T) paritySource {
	t.Helper()
	dir := frozenDir(t)
	var m parityManifest
	readJSON(t, filepath.Join(dir, "manifest.json"), &m)
	src := paritySource{
		label: dir, month: m.Month, now: parseInstant(t, m.Now),
		corpus: filepath.Join(dir, "corpus.tsv"), key: filepath.Join(dir, "answer-key.csv"),
	}
	switch {
	case filepath.Base(dir) != m.Month:
		t.Fatalf("%s: manifest month %s", dir, m.Month)
	case m.Oracle != "freshened":
		t.Fatalf("%s: oracle %q, want freshened", dir, m.Oracle)
	case fileSHA256(t, src.corpus) != m.CorpusSHA256:
		t.Fatalf("%s does not hash to the manifest's corpus_sha256: re-freeze it", src.corpus)
	case fileSHA256(t, src.key) != m.AnswerKeySHA256:
		t.Fatalf("%s does not hash to the manifest's answer_key_sha256: re-freeze it", src.key)
	}
	return src
}

// frozenDir is the one month directory under parityRoot.
func frozenDir(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(parityRoot)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) != 1 {
		t.Fatalf("%s holds %d month directories %v, want one", parityRoot, len(dirs), dirs)
	}
	return filepath.Join(parityRoot, dirs[0])
}

// runSource is a run directory's freshened key, refused unless the
// manifest says it is freshened and hashes it.
func runSource(t *testing.T, dir string) paritySource {
	t.Helper()
	var m runManifest
	readJSON(t, filepath.Join(dir, "manifest.json"), &m)
	src := paritySource{
		label: dir, month: m.Month, now: parseInstant(t, m.StartedAt), run: m,
		corpus: filepath.Join(dir, "corpus.tsv"), key: filepath.Join(dir, "freshened", "answer-key.csv"),
	}
	if rows := m.Keys.Freshened.WmiyearvalidcharsRows; rows != 0 {
		t.Fatalf("%s: the freshened key's database has %d wmiyearvalidchars rows, want 0", dir, rows)
	}
	if fileSHA256(t, src.key) != m.Keys.Freshened.AnswerKeySHA256 {
		t.Fatalf("%s does not hash to the manifest's keys.freshened.answer_key_sha256", src.key)
	}
	if stock := filepath.Join(dir, "stock", "answer-key.csv"); fileExists(stock) {
		src.stock = stock
	}
	return src
}

// freezeParity writes run's committed calls to parityRoot as the frozen
// key of run's month and returns that key, the run's stock key beside it.
func freezeParity(t *testing.T, run paritySource) paritySource {
	t.Helper()
	old := frozenDir(t)
	out, err := exec.Command("bash", "../../scripts/oracle.sh", "corpus", filepath.Join(old, "corpus.tsv")).Output()
	if err != nil {
		t.Fatalf("scripts/oracle.sh corpus: %v", err)
	}
	committed := string(out)
	runCorpus := readFile(t, run.corpus)
	if !strings.HasPrefix(string(runCorpus), committed) {
		t.Fatalf("%s does not begin with scripts/oracle.sh corpus %s/corpus.tsv: the run's list must be that, then any generated VINs", run.corpus, old)
	}
	n := len(corpusCalls(t, "the committed corpus", committed))
	keyData := readFile(t, run.key)
	calls, ends := parseParityKey(t, run.key, keyData)
	if len(calls) < n {
		t.Fatalf("%s has %d calls; the committed corpus %d", run.key, len(calls), n)
	}
	rows := 0
	for _, c := range calls[:n] {
		rows += len(c.rows)
	}
	key := keyData[:ends[n-1]]

	dir := filepath.Join(parityRoot, run.month)
	m := parityManifest{
		Month: run.month, DumpBytes: run.run.DumpBytes, DumpSHA256: run.run.DumpSHA256,
		Oracle: "freshened", Now: run.now.UTC().Format(time.RFC3339),
		Server: run.run.Server, Collate: run.run.Collate, Ctype: run.run.Ctype,
		CorpusSHA256: sha256Hex([]byte(committed)), VINCount: n,
		AnswerRows: rows, AnswerKeySHA256: sha256Hex(key),
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{
		"corpus.tsv": []byte(committed), "answer-key.csv": key, "manifest.json": append(manifest, '\n'),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if filepath.Clean(old) != filepath.Clean(dir) {
		if err := os.RemoveAll(old); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("froze %d of the run's %d calls, %d rows, into %s", n, len(calls), rows, dir)
	src := frozenSource(t)
	src.stock = run.stock
	return src
}

// parityCall is one input row of an answer key: the call, and its rows as
// COPY renders them (projectedRow's form), sorted.
type parityCall struct {
	row  int
	vin  string
	hint yearHint
	rows []string
}

// parseParityKey reads an answer key, one call per input row in order, and
// the byte offset just past each call's last row.
func parseParityKey(t *testing.T, name string, data []byte) ([]parityCall, []int) {
	t.Helper()
	recs, err := parsePgCSV(string(data))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if len(recs) == 0 || joinFields(recs[0].fields) != keyHeader {
		t.Fatalf("%s: not an answer key (header %q)", name, joinFields(recs[0].fields))
	}
	var calls []parityCall
	var ends []int
	for n, r := range recs[1:] {
		f := r.fields
		if len(f) != 18 {
			t.Fatalf("%s: row %d has %d fields", name, n+1, len(f))
		}
		row, err := strconv.Atoi(f[0].s)
		if err != nil {
			t.Fatalf("%s: row %d: input_row %q", name, n+1, f[0].s)
		}
		hint := yearHint{}
		if f[2].s != "" {
			y, err := strconv.Atoi(f[2].s)
			if err != nil {
				t.Fatalf("%s: row %d: year_hint %q", name, n+1, f[2].s)
			}
			hint = yearHint{y, true}
		}
		switch row {
		case len(calls) + 1:
			calls = append(calls, parityCall{row: row, vin: f[1].s, hint: hint})
			ends = append(ends, 0)
		case len(calls):
			if c := calls[row-1]; c.vin != f[1].s || c.hint != hint {
				t.Fatalf("%s: input row %d names two calls", name, row)
			}
		default:
			t.Fatalf("%s: row %d: input row %d out of order", name, n+1, row)
		}
		cols := make([]string, 15)
		for i, c := range f[3:] {
			cols[i] = `\N`
			if c.s != "" || c.quoted {
				cols[i] = escapeCopy(c.s)
			}
		}
		c := &calls[row-1]
		c.rows = append(c.rows, strings.Join(cols, "\t"))
		ends[row-1] = r.end
	}
	for i := range calls {
		slices.Sort(calls[i].rows)
	}
	return calls, ends
}

// corpusSkip is scripts/oracle.sh decode's test for a line that is no VIN.
var corpusSkip = regexp.MustCompile(`^[[:space:]]*(#|$)`)

// corpusCalls reads a VIN list as scripts/oracle.sh decode does: one call
// per line that is not blank or a comment, the VIN up to a tab, then the
// hint when the next field is not empty.
func corpusCalls(t *testing.T, name, text string) []parityCall {
	t.Helper()
	var calls []parityCall
	for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		l = strings.TrimSuffix(l, "\r")
		if corpusSkip.MatchString(l) {
			continue
		}
		f := strings.Split(l, "\t")
		c := parityCall{row: len(calls) + 1, vin: f[0]}
		if len(f) > 1 && f[1] != "" {
			y, err := strconv.Atoi(f[1])
			if err != nil {
				t.Fatalf("%s: %q: %v", name, l, err)
			}
			c.hint = yearHint{y, true}
		}
		calls = append(calls, c)
	}
	return calls
}

// parityResult is checkParity's outcome, per call of the key.
type parityResult struct {
	calls []parityCall
	ours  [][]string // Project's rows, sorted
	errs  []error    // Project's error, if it raised
	stock map[int][]string
	rows  int
	bad   int
}

// checkParity decodes every call of src's key with e, compares, and lays
// every decode out in both shapes.
func checkParity(t *testing.T, e *engine, src paritySource) parityResult {
	t.Helper()
	elements, err := contract.PublicElements(e.a)
	if err != nil {
		t.Fatal(err)
	}
	calls, _ := parseParityKey(t, src.key, readFile(t, src.key))
	corpus := corpusCalls(t, src.corpus, string(readFile(t, src.corpus)))
	if len(corpus) != len(calls) {
		t.Fatalf("%s lists %d calls; %s answers %d", src.corpus, len(corpus), src.key, len(calls))
	}
	for i, c := range corpus {
		if calls[i].vin != c.vin || calls[i].hint != c.hint {
			t.Fatalf("input row %d: %s lists %q hint %v; the key answers %q hint %v",
				c.row, src.corpus, c.vin, c.hint, calls[i].vin, calls[i].hint)
		}
	}
	res := parityResult{
		calls: calls, ours: make([][]string, len(calls)), errs: make([]error, len(calls)),
	}
	if src.stock != "" {
		stock, _ := parseParityKey(t, src.stock, readFile(t, src.stock))
		res.stock = map[int][]string{}
		for _, s := range stock {
			if s.row <= len(calls) && (s.vin != calls[s.row-1].vin || s.hint != calls[s.row-1].hint) {
				t.Fatalf("input row %d: the stock key answers %q hint %v, the freshened %q hint %v",
					s.row, s.vin, s.hint, calls[s.row-1].vin, calls[s.row-1].hint)
			}
			res.stock[s.row] = s.rows
		}
	}

	now := src.now.UnixMicro()
	misordered, unlaid := 0, 0
	for i, c := range calls {
		res.rows += len(c.rows)
		p, err := e.project(c.vin, c.hint, now)
		if err != nil {
			res.errs[i] = err
			if res.bad++; res.bad <= 10 {
				t.Errorf("input row %d, %q hint %v: Project raised %v; the oracle answered %d rows", c.row, c.vin, c.hint, err, len(c.rows))
			}
			continue
		}
		got := make([]string, len(p.Items))
		last := uint8(0)
		for j, it := range p.Items {
			got[j] = projectedRow(it)
			if rank := groupRank(it.GroupName.V); rank < last {
				misordered++
				t.Errorf("input row %d, %q hint %v: Project's item %d falls from group rank %d to %d", c.row, c.vin, c.hint, j, last, rank)
			} else {
				last = rank
			}
		}
		if _, err := contract.Vpic(p.Items, elements); err != nil {
			unlaid++
			t.Errorf("input row %d, %q hint %v: shape vpic: %v", c.row, c.vin, c.hint, err)
		} else if _, err := contract.Nitrovin(p.Items, elements); err != nil {
			unlaid++
			t.Errorf("input row %d, %q hint %v: shape nitrovin: %v", c.row, c.vin, c.hint, err)
		}
		slices.Sort(got)
		res.ours[i] = got
		if !slices.Equal(got, c.rows) {
			if res.bad++; res.bad <= 10 {
				t.Errorf("input row %d, %q hint %v:\n%s", c.row, c.vin, c.hint, rowsDiff(got, c.rows))
			}
		}
	}
	t.Logf("%s, %s at %s: %d calls, %d rows: %d disagree, %d misordered, %d refused by a layout",
		src.label, src.month, src.now.UTC().Format(time.RFC3339), len(calls), res.rows, res.bad, misordered, unlaid)
	return res
}

// reportRow is one row of a key or of Project, every column as the key's
// text, NULL as null.
type reportRow struct {
	GroupName   *string `json:"groupname"`
	Variable    *string `json:"variable"`
	Value       *string `json:"value"`
	PatternID   *string `json:"itempatternid"`
	VinSchemaID *string `json:"itemvinschemaid"`
	Keys        *string `json:"itemkeys"`
	ElementID   *string `json:"itemelementid"`
	AttributeID *string `json:"itemattributeid"`
	CreatedOn   *string `json:"itemcreatedon"`
	WmiID       *string `json:"itemwmiid"`
	Code        *string `json:"code"`
	DataType    *string `json:"datatype"`
	Decode      *string `json:"decode"`
	Source      *string `json:"itemsource"`
	ToBeQCed    *string `json:"itemtobeqced"`
}

func newReportRow(copyRow string) reportRow {
	f := make([]*string, 15)
	for i, c := range strings.Split(copyRow, "\t") {
		if c != `\N` {
			s := unescapeCopy(c)
			f[i] = &s
		}
	}
	return reportRow{f[0], f[1], f[2], f[3], f[4], f[5], f[6], f[7], f[8], f[9], f[10], f[11], f[12], f[13], f[14]}
}

// reportLine is one line of report.jsonl.
type reportLine struct {
	Row       int          `json:"row"`
	VIN       string       `json:"vin"`
	Year      *int         `json:"year"`
	Element   *int         `json:"element,omitempty"`
	Code      *string      `json:"code,omitempty"`
	Status    string       `json:"status"`
	Error     string       `json:"error,omitempty"`
	Freshened []reportRow  `json:"freshened"`
	Ours      []reportRow  `json:"ours"`
	Stock     *[]reportRow `json:"stock,omitempty"`
	Live      *string      `json:"live,omitempty"`
	LiveError string       `json:"live_error,omitempty"`
}

// liveLimit bounds the calls whose disagreements the report asks live vPIC
// about: a month red on thousands of VINs needs no more to be classified.
const liveLimit = 100

// writeParityReport writes ADR 0008's report into dir.
//
// report.jsonl has one line per (input row, element) where any two of the
// answers differ: status "differs" where Project's rows are not the
// freshened key's, "stale-cache" where they are but the stock key's are not
// (the class of docs/deviations/stale-cache.md). Each line gives the call
// (row, vin, year: the hint or null), the element and its code, and every
// row of that element on each side (freshened, ours, and stock when the run
// has a stock key) with all 15 columns, value, pattern and source among
// them. A call Project raised on is one line, status "raised", with the
// error and the oracle's rows. For the first liveLimit calls that differ or
// raised, live vPIC's DecodeVinValues (with the hint as modelyear) gives the
// element's live value, under its code; live_error says why there is none.
// Agreeing elements are counted, not listed.
//
// report.md is the summary for the refresh PR: the counts, and a table of
// the first differing elements.
func writeParityReport(t *testing.T, dir string, src paritySource, res parityResult) {
	t.Helper()
	var lines []reportLine
	liveCalls := 0
	disagreeCalls, disagreeElems, staleCalls, staleElems := 0, 0, 0, 0
	for i, c := range res.calls {
		year := (*int)(nil)
		if c.hint.valid {
			year = &c.hint.year
		}
		var stock []string
		if res.stock != nil {
			stock = res.stock[c.row]
		}
		var callLines []reportLine
		if res.errs[i] != nil {
			callLines = append(callLines, reportLine{
				Row: c.row, VIN: c.vin, Year: year, Status: "raised", Error: res.errs[i].Error(),
				Freshened: reportRows(c.rows), Ours: []reportRow{},
			})
		} else {
			fresh, ours, stk := byElement(c.rows), byElement(res.ours[i]), byElement(stock)
			for _, el := range elementIDs(fresh, ours, stk) {
				differs := !slices.Equal(fresh[el], ours[el])
				stale := res.stock != nil && !slices.Equal(fresh[el], stk[el])
				if !differs && !stale {
					continue
				}
				l := reportLine{
					Row: c.row, VIN: c.vin, Year: year, Element: &el, Status: "stale-cache",
					Freshened: reportRows(fresh[el]), Ours: reportRows(ours[el]),
				}
				if differs {
					l.Status = "differs"
				}
				if res.stock != nil {
					s := reportRows(stk[el])
					l.Stock = &s
				}
				for _, side := range [][]reportRow{l.Freshened, l.Ours, reportRows(stk[el])} {
					if len(side) > 0 && side[0].Code != nil && l.Code == nil {
						l.Code = side[0].Code
					}
				}
				callLines = append(callLines, l)
			}
		}
		differs, stale := 0, 0
		for _, l := range callLines {
			if l.Status == "stale-cache" {
				stale++
			} else {
				differs++
			}
		}
		if differs > 0 {
			disagreeCalls++
			disagreeElems += differs
		}
		if stale > 0 {
			staleCalls++
			staleElems += stale
		}
		if differs > 0 && liveCalls < liveLimit {
			liveCalls++
			live, err := liveValues(c.vin, c.hint)
			for j := range callLines {
				l := &callLines[j]
				switch {
				case l.Status == "stale-cache":
				case err != nil:
					l.LiveError = err.Error()
				case l.Code != nil:
					if v, ok := live[*l.Code]; ok {
						l.Live = &v
					}
				}
			}
		}
		lines = append(lines, callLines...)
	}

	var jsonl bytes.Buffer
	enc := json.NewEncoder(&jsonl)
	enc.SetEscapeHTML(false)
	for _, l := range lines {
		if err := enc.Encode(l); err != nil {
			t.Fatal(err)
		}
	}

	var md strings.Builder
	fmt.Fprintf(&md, "### Parity, data month %s\n\n", src.month)
	fmt.Fprintf(&md, "nitrovin's engine against the freshened oracle's answer key (`%s`), decoded at %s: every row of every call, all 15 columns, exact strings.\n\n",
		strings.TrimPrefix(src.key, "../../"), src.now.UTC().Format(time.RFC3339))
	md.WriteString("| Measure | Value |\n| --- | --- |\n")
	fmt.Fprintf(&md, "| Calls (VIN, hint) | %d |\n", len(res.calls))
	fmt.Fprintf(&md, "| Oracle rows | %d |\n", res.rows)
	fmt.Fprintf(&md, "| Calls that disagree | %d |\n", disagreeCalls)
	fmt.Fprintf(&md, "| Elements that disagree | %d |\n", disagreeElems)
	if res.stock != nil {
		fmt.Fprintf(&md, "| Calls where stock ≠ freshened (stale-cache) | %d |\n", staleCalls)
		fmt.Fprintf(&md, "| Elements where stock ≠ freshened | %d |\n", staleElems)
	}
	const shown = 50
	if disagreeElems > 0 {
		md.WriteString("\n| VIN | Hint | Element | Freshened | Ours | Pattern: freshened / ours | Source: freshened / ours | Stock | Live vPIC |\n")
		md.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		n := 0
		for _, l := range lines {
			if l.Status == "stale-cache" {
				continue
			}
			if n++; n > shown {
				break
			}
			hint, el := "", ""
			if l.Year != nil {
				hint = strconv.Itoa(*l.Year)
			}
			if l.Element != nil {
				el = strconv.Itoa(*l.Element)
				if l.Code != nil {
					el += " " + *l.Code
				}
			} else {
				el = "raised: " + l.Error
			}
			stock, live := "", ""
			if l.Stock != nil {
				stock = column(*l.Stock, func(r reportRow) *string { return r.Value })
			}
			switch {
			case l.Live != nil:
				live = *l.Live
			case l.LiveError != "":
				live = "unchecked"
			}
			pattern := func(r reportRow) *string { return r.PatternID }
			source := func(r reportRow) *string { return r.Source }
			value := func(r reportRow) *string { return r.Value }
			fmt.Fprintf(&md, "| `%s` | %s | %s | %s | %s | %s / %s | %s / %s | %s | %s |\n",
				l.VIN, hint, mdCell(el), column(l.Freshened, value), column(l.Ours, value),
				column(l.Freshened, pattern), column(l.Ours, pattern),
				column(l.Freshened, source), column(l.Ours, source), stock, mdCell(live))
		}
		if disagreeElems > shown {
			fmt.Fprintf(&md, "\n%d more in `report.jsonl`.\n", disagreeElems-shown)
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.jsonl"), jsonl.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("report: %d lines in %s (%d calls disagree, %d stale-cache)", len(lines), dir, disagreeCalls, staleCalls)
}

// byElement groups sorted COPY rows by element id, keeping their order.
func byElement(rows []string) map[int][]string {
	m := map[int][]string{}
	for _, r := range rows {
		el, _ := strconv.Atoi(strings.Split(r, "\t")[6])
		m[el] = append(m[el], r)
	}
	return m
}

func elementIDs(sides ...map[int][]string) []int {
	var ids []int
	for _, s := range sides {
		for id := range s {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	return ids
}

func reportRows(rows []string) []reportRow {
	out := make([]reportRow, len(rows))
	for i, r := range rows {
		out[i] = newReportRow(r)
	}
	return out
}

// column is one column of an element's rows for a markdown cell, NULL as
// NULL, rows joined by "; ".
func column(rows []reportRow, f func(reportRow) *string) string {
	if len(rows) == 0 {
		return "(none)"
	}
	s := make([]string, len(rows))
	for i, r := range rows {
		s[i] = "NULL"
		if v := f(r); v != nil {
			s[i] = *v
		}
	}
	return mdCell(strings.Join(s, "; "))
}

func mdCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// liveValues is live vPIC's DecodeVinValues for one call, by element code.
func liveValues(vin string, hint yearHint) (map[string]string, error) {
	u := "https://vpic.nhtsa.dot.gov/api/vehicles/DecodeVinValues/" + url.PathEscape(vin) + "?format=json"
	if hint.valid {
		u += "&modelyear=" + strconv.Itoa(hint.year)
	}
	c := http.Client{Timeout: time.Minute}
	resp, err := c.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	var body struct{ Results []map[string]any }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("GET %s: %v", u, err)
	}
	if len(body.Results) == 0 {
		return nil, fmt.Errorf("GET %s: no Results", u)
	}
	out := map[string]string{}
	for k, v := range body.Results[0] {
		switch v := v.(type) {
		case string:
			out[k] = v
		case nil:
			out[k] = ""
		default:
			out[k] = fmt.Sprint(v)
		}
	}
	return out, nil
}

// pgField is one field of Postgres's CSV, which tells NULL (unquoted and
// empty) from the empty string (quoted).
type pgField struct {
	s      string
	quoted bool
}

// pgRecord is one CSV record and the offset just past its line end.
type pgRecord struct {
	fields []pgField
	end    int
}

// parsePgCSV parses COPY … (format csv) output: RFC 4180 with \n line ends.
func parsePgCSV(data string) ([]pgRecord, error) {
	var recs []pgRecord
	var fields []pgField
	for i := 0; i < len(data); {
		var f pgField
		if data[i] == '"' {
			f.quoted = true
			var b strings.Builder
			for i++; ; {
				j := strings.IndexByte(data[i:], '"')
				if j < 0 {
					return nil, fmt.Errorf("unterminated quote in record %d", len(recs)+1)
				}
				b.WriteString(data[i : i+j])
				i += j + 1
				if i < len(data) && data[i] == '"' {
					b.WriteByte('"')
					i++
					continue
				}
				break
			}
			f.s = b.String()
		} else {
			j := strings.IndexAny(data[i:], ",\n")
			if j < 0 {
				j = len(data) - i
			}
			f.s = data[i : i+j]
			i += j
		}
		fields = append(fields, f)
		switch {
		case i == len(data):
			return nil, fmt.Errorf("record %d has no line end", len(recs)+1)
		case data[i] == ',':
			i++
		case data[i] == '\n':
			i++
			recs = append(recs, pgRecord{fields, i})
			fields = nil
		default:
			return nil, fmt.Errorf("record %d: %q after a quoted field", len(recs)+1, data[i])
		}
	}
	return recs, nil
}

func joinFields(f []pgField) string {
	s := make([]string, len(f))
	for i, x := range f {
		s[i] = x.s
	}
	return strings.Join(s, ",")
}

func parseInstant(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("decode instant %q: %v", s, err)
	}
	return at
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := json.Unmarshal(readFile(t, path), v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSHA256(t *testing.T, path string) string { return sha256Hex(readFile(t, path)) }

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
