package nitrovin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// readBudgets are ADR 0023's reads per request on a compiled catalog: what
// keeps a request inside ADR 0018's budget where a read costs about
// 0.25 ms, as in a browser's store.
var readBudgets = map[string]int64{
	"candidates":          10,
	"part-types":          20,
	"part-types-any-year": 20,
	"related":             100,
	"related-any-year":    100,
	"reverse":             2,
	"reverse-retired":     2,
	"page":                8,
}

// counter counts the reads through it, and their bytes.
type counter struct {
	r        io.ReaderAt
	n, bytes atomic.Int64
}

func (c *counter) ReadAt(p []byte, off int64) (int, error) {
	c.n.Add(1)
	c.bytes.Add(int64(len(p)))
	return c.r.ReadAt(p, off)
}

// farSpan is package vocab's: the most bytes one read of wide fitments
// spans. pageBytes bounds a page of fitments in key order in this
// catalog: 64 fitments (package vocab's pageFitments), each of at most
// budgetWideMax applications of under 64 bytes.
const (
	farSpan   = 1 << 20
	pageBytes = 64 * budgetWideMax * 64
)

// TestReadBudget: every fit request, and a 1,000-fitment page of list,
// costs a compiled catalog no more reads than ADR 0023's budget, counted
// through its io.ReaderAt, and no more bytes than its own shape gives it.
//
// The catalog is generated in the catalog-scale fixture's shape
// (bench/vocab: 205 part types, 2.4 applications a fitment on average,
// half with qualifiers, a redirect for one fitment in eight, a third of
// its vehicles unmapped) at a fortieth of its size; then again with a
// tail of wide fitments like universal parts, one in two hundred of 50 to
// 500 applications on vehicles at random, so each vehicle has about a
// hundred.
//
// How the reads scale: candidates and part types read a vehicle's head and
// block, reverse and a page a page of fitments or a retired number's
// successor's page, whatever the catalog's size and whatever its wide
// fitments: a vehicle's block holds what answers for it of each, its own
// applications. So candidates' and part types' bytes with the wide tail
// stay within twice the
// plain catalog's, the most the wide fitments' own applications add to a
// block. Related vehicles alone read the wide fitments whole, in reads of
// at most farSpan, so their bytes stay within reads × farSpan; their reads
// grow with the wide fitments a vehicle has (bench/vocab's wide variants
// record them at full scale). Reverse and a page read pages of fitments in
// key order, which hold a wide fitment whole, as a reverse answer has it:
// within pageBytes a read.
func TestReadBudget(t *testing.T) {
	d := embeddedDecoder(t)
	plain := budgetReads(t, d, 0)
	wide := budgetReads(t, d, budgetFitments/200)
	for _, shape := range []struct {
		name  string
		worst map[string][2]int64
	}{{"plain", plain}, {"wide", wide}} {
		for kind, budget := range readBudgets {
			if got := shape.worst[kind][0]; got > budget {
				t.Errorf("%s, %s: %d reads; ADR 0023's budget is %d", shape.name, kind, got, budget)
			}
		}
		t.Logf("%s: reads and bytes, most per request: %v", shape.name, shape.worst)
	}
	for kind := range readBudgets {
		w, p := wide[kind], plain[kind]
		switch kind {
		case "related", "related-any-year":
			if w[1] > w[0]*farSpan {
				t.Errorf("wide, %s: %d bytes in %d reads, more than %d a read", kind, w[1], w[0], farSpan)
			}
		case "reverse", "reverse-retired", "page":
			if bound := w[0] * pageBytes; w[1] > bound {
				t.Errorf("wide, %s: %d bytes in %d reads, more than %d a page", kind, w[1], w[0], pageBytes)
			}
		default:
			if w[1] > 2*p[1] {
				t.Errorf("wide, %s: %d bytes; plain, %d: the wide fitments' other applications were read", kind, w[1], p[1])
			}
		}
	}
}

// budgetReads compiles the read budget's catalog with wide wide fitments,
// asks every kind of it, and returns the most reads and bytes of each.
func budgetReads(t *testing.T, d *Decoder, wide int) map[string][2]int64 {
	t.Helper()
	dir := t.TempDir()
	vehicles, err := writeBudgetCatalog(dir, d.month, wide)
	if err != nil {
		t.Fatal(err)
	}
	files, source, err := vocab.OpenCompile(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.compiled")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := vocab.Compile(out, d.month, files, src, source, vocab.DirScratch(t.TempDir()), vocab.CompileOptions{Writer: Version()}); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	c := &counter{r: f}
	cf, err := vocab.OpenCompiled(path, c, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, append(files, cf)...)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	worst := map[string][2]int64{}
	found := map[string]int{}
	mark := func() [2]int64 { return [2]int64{c.n.Load(), c.bytes.Load()} }
	record := func(kind string, from [2]int64) {
		to, w := mark(), worst[kind]
		worst[kind] = [2]int64{max(w[0], to[0]-from[0]), max(w[1], to[1]-from[1])}
	}
	ask := func(kind string, req contract.FitRequest) {
		t.Helper()
		req.Now = now
		from := mark()
		fit, err := tr.Fit(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		record(kind, from)
		if fit.Status == contract.FitFound {
			found[kind]++
		}
	}
	for k := range budgetCases {
		i := k * (budgetFitments / budgetCases)
		for budgetVehicleOf(i, 0) < budgetUnmapped { // a mapped vehicle, and a fitment a redirect is about
			i += 8
		}
		from := contract.FitFrom{Vocabulary: "budget-models", Entry: vehicles[budgetVehicleOf(i, 0)]}
		year := contract.Value(int32(budgetFirstOf(i, 0)))
		ask("candidates", contract.FitRequest{From: from, ModelYear: year, Part: &contract.FitPart{Spine: "budget-types", ID: budgetTypeOf(i)}})
		ask("candidates", contract.FitRequest{From: from, ModelYear: year, Part: &contract.FitPart{Spine: "budget-types", ID: budgetRedirectFrom(i)}})
		ask("candidates", contract.FitRequest{From: from, Part: &contract.FitPart{Spine: "budget-types", ID: budgetWideTypeOf(k)}})
		ask("part-types", contract.FitRequest{From: from, ModelYear: year})
		ask("part-types-any-year", contract.FitRequest{From: from})
		ask("related", contract.FitRequest{From: from, ModelYear: year, Related: true})
		ask("related-any-year", contract.FitRequest{From: from, Related: true})
		ask("reverse", contract.FitRequest{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: budgetTypeOf(i), Number: budgetNumberOf(i)}}})
		r := k % budgetRetired
		ask("reverse-retired", contract.FitRequest{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: budgetTypeOf(r * 461 % budgetFitments), Number: fmt.Sprintf("R%07d", r)}}})

		req := contract.ListRequest{Of: "budget-catalog", Limit: contract.MaxListLimit}
		if k%2 == 1 {
			req.Where = map[string]string{"type": budgetTypeOf(i)}
		}
		start := mark()
		list, err := tr.List(req)
		if err != nil {
			t.Fatal(err)
		}
		if len(list.Entries) > 0 {
			found["page"]++
		}
		if list.Next != "" { // the next page, after a key
			start = mark()
			req.After = list.Next
			if list, err = tr.List(req); err != nil {
				t.Fatal(err)
			}
		}
		record("page", start)
	}
	for kind := range readBudgets {
		if found[kind] < budgetCases/2 {
			t.Errorf("%d wide: %s: %d of %d requests answered found", wide, kind, found[kind], budgetCases)
		}
	}
	return worst
}

// The read budget's catalog: the catalog-scale fixture's shape
// (bench/vocab's writeCatalog), a fortieth of its fitments and redirects,
// and its wide fitments' shape (bench/vocab's wideVariants).
const (
	budgetFitments  = 20_000
	budgetRedirects = 2_500
	budgetRetired   = 50
	budgetTypes     = 205
	budgetModels    = 300
	budgetUnmapped  = 100
	budgetWideMin   = 50
	budgetWideMax   = 500
	budgetWideTypes = 20
	budgetCases     = 48
)

// budgetWideTypeOf is wide fitment w's part type.
func budgetWideTypeOf(w int) string {
	return fmt.Sprintf("%03d", w%budgetWideTypes*(budgetTypes/budgetWideTypes)+1)
}

var budgetApplications = []int{1, 1, 2, 2, 2, 3, 3, 4, 1, 5}

func budgetTypeOf(i int) string { return fmt.Sprintf("%03d", i/2%budgetTypes+1) }

func budgetNumberOf(i int) string { return fmt.Sprintf("N%07d", i) }

func budgetVehicleOf(i, j int) int { return (i*31 + j*977) % budgetModels }
func budgetFirstOf(i, j int) int   { return 1981 + (i+j*7)%40 }
func budgetLastOf(i, j int) int    { return budgetFirstOf(i, j) + (i+j)%6 }

func budgetRedirectFrom(i int) string { return fmt.Sprintf("%03d", (i+101)%budgetTypes+1) }

func budgetAppsOf(i int) int { return budgetApplications[i%len(budgetApplications)] }

// writeBudgetCatalog writes the read budget's spine, vehicle vocabulary
// and catalog, with wide wide fitments, to dir, returning the vocabulary's
// entries by index.
func writeBudgetCatalog(dir string, m vocab.Month, wide int) ([]map[string]string, error) {
	var names [][2]string
	var ids []int32
	for mk, mkName := range m.Makes() {
		for id, name := range m.Models(mk) {
			names, ids = append(names, [2]string{mkName, name}), append(ids, id)
		}
		if len(names) >= budgetModels {
			break
		}
	}
	if len(names) < budgetModels {
		return nil, fmt.Errorf("data month %s has %d models; the catalog needs %d", m.Name(), len(names), budgetModels)
	}
	vehicles := make([]map[string]string, budgetModels)
	text := make([]string, budgetModels)
	for i := range vehicles {
		vehicles[i] = map[string]string{"make": names[i][0], "model": fmt.Sprintf("%s #%d", names[i][1], i)}
		b, err := json.Marshal(vehicles[i])
		if err != nil {
			return nil, err
		}
		text[i] = string(b)
	}
	write := func(name string, lines func(w *bufio.Writer)) error {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		w := bufio.NewWriter(f)
		lines(w)
		if err := w.Flush(); err != nil {
			f.Close()
			return err
		}
		return f.Close()
	}
	if err := write("types.jsonl", func(w *bufio.Writer) {
		fmt.Fprintln(w, `{"format":1,"kind":"spine","name":"budget-types","version":"synthetic"}`)
		for t := range budgetTypes {
			fmt.Fprintf(w, `{"id":"%03d","name":"Part type %d"}`+"\n", t+1, t+1)
		}
	}); err != nil {
		return nil, err
	}
	if err := write("models.jsonl", func(w *bufio.Writer) {
		fmt.Fprintf(w, `{"format":1,"kind":"vocabulary","name":"budget-models","version":"synthetic","data_month":%q,"subject":"vehicle","fields":["make","model"]}`+"\n", m.Name())
		for i, v := range text {
			if i < budgetUnmapped {
				fmt.Fprintf(w, `{"entry":%s,"mappings":[]}`+"\n", v)
			} else {
				fmt.Fprintf(w, `{"entry":%s,"mappings":[{"model":%d}]}`+"\n", v, ids[i])
			}
		}
	}); err != nil {
		return nil, err
	}
	qualifiers := []string{`{"13":["2.4"]}`, `{"9":["6"]}`, `{"15":[2]}`, `{"5":[13],"24":[4]}`}
	return vehicles, write("catalog.jsonl", func(w *bufio.Writer) {
		fmt.Fprintf(w, `{"format":1,"kind":"catalog","name":"budget-catalog","version":"synthetic","release":"1.0","data_month":%q,"spine":"budget-types","vehicles":"budget-models"}`+"\n", m.Name())
		for i := range budgetFitments {
			fmt.Fprintf(w, `{"fitment":{"type":"%s","number":"%s"},"applications":[`, budgetTypeOf(i), budgetNumberOf(i))
			for j := range budgetAppsOf(i) {
				if j > 0 {
					w.WriteByte(',')
				}
				fmt.Fprintf(w, `{"vehicle":%s,"first":%d,"last":%d,"options":"w/ part %d-%d, from the parts tree"`, text[budgetVehicleOf(i, j)], budgetFirstOf(i, j), budgetLastOf(i, j), i, j)
				if (i+j)%2 == 0 {
					fmt.Fprintf(w, `,"qualifiers":%s`, qualifiers[(i/2+j)%len(qualifiers)])
				}
				w.WriteByte('}')
			}
			w.WriteString("]}\n")
		}
		for r := range budgetRedirects {
			i := r * 8 % budgetFitments
			fmt.Fprintf(w, `{"redirect":{"vehicle":%s,"first":%d,"last":%d,"from":"%s","to":"%s","options":"listed under %s"}}`+"\n",
				text[budgetVehicleOf(i, 0)], budgetFirstOf(i, 0), budgetLastOf(i, 0), budgetRedirectFrom(i), budgetTypeOf(i), budgetTypeOf(i))
		}
		rng := rand.New(rand.NewPCG(uint64(wide), 262))
		for k := range wide {
			fmt.Fprintf(w, `{"fitment":{"type":"%s","number":"W%07d"},"applications":[`, budgetWideTypeOf(k), k)
			for j := range budgetWideMin + rng.IntN(budgetWideMax-budgetWideMin+1) {
				if j > 0 {
					w.WriteByte(',')
				}
				first := 1981 + rng.IntN(40)
				fmt.Fprintf(w, `{"vehicle":%s,"first":%d,"last":%d,"options":"universal, fits %d-%d"`, text[rng.IntN(budgetModels)], first, first+rng.IntN(6), k, j)
				if j%2 == 0 {
					fmt.Fprintf(w, `,"qualifiers":%s`, qualifiers[(k+j)%len(qualifiers)])
				}
				w.WriteByte('}')
			}
			w.WriteString("]}\n")
		}
		for r := range budgetRetired {
			i := r * 461 % budgetFitments
			fmt.Fprintf(w, `{"retired":{"type":"%s","number":"R%07d"},"to":{"type":"%s","number":"%s"}}`+"\n", budgetTypeOf(i), r, budgetTypeOf(i), budgetNumberOf(i))
		}
	})
}
