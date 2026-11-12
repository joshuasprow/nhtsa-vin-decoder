package vocab

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The catalog-scale fixture (ADR 0017): about 790,000 fitments, 1.9 million
// applications, half of them with qualifiers, and 100,000 redirects, over
// 205 part types and 1,326 vehicle entries, 448 of them unmapped, as the
// first user's catalog measured. Every number and name is synthetic.
const (
	catalogFitments  = 790_000
	catalogRedirects = 100_000
	catalogRetired   = 1_700
	catalogTypes     = 205
	catalogModels    = 1_326
	catalogUnmapped  = 448
)

// applicationsPer cycles through each fitment's count of applications:
// 2.4 on average, so 1,896,000 in all.
var applicationsPer = []int{1, 1, 2, 2, 2, 3, 3, 4, 1, 5}

// Option text, as a catalog's builder leaves it: the leaf path of a tree,
// so six applications in ten have text of their own, about 40 bytes, on
// one of these phrases; the rest share a phrase.
var options = []string{"", "", "", "w/o sunroof", "w/ sunroof", "4WD", "2WD", "w/ towing package", "w/o towing package",
	"heated", "power", "manual", "from 3/15", "to 3/15", "w/ heavy duty brakes", "VIN T, 8th digit", "rear disc", "front disc",
	"sedan", "coupe", "wagon", "LH", "RH", "w/ HID", "w/ halogen", "2.4L", "3.5L", "5.7L", "6 cyl", "8 cyl"}

// Qualifier sets a builder derives from option text: Displacement (13),
// Engine Number of Cylinders (9), Drive Type (15), Body Class (5) and Fuel
// Type (24).
var qualifierSets = []string{`{"13":["2.4"]}`, `{"13":["3.5"]}`, `{"13":["5.7"]}`, `{"9":["4"]}`, `{"9":["6"]}`, `{"9":["8"]}`,
	`{"15":[2]}`, `{"15":[1,7]}`, `{"15":[3]}`, `{"5":[13]}`, `{"5":[3]}`, `{"5":[9]}`, `{"24":[4]}`, `{"24":[1]}`,
	`{"13":["2.4"],"15":[2]}`, `{"13":["5.7"],"9":["8"]}`, `{"5":[13],"24":[4]}`}

var catalogOnce struct {
	sync.Once
	dir      string
	vehicles []vocab.Vehicle // the catalog's, by index
	err      error
}

// catalogDir writes the catalog-scale fixture once per run, to the
// directory it returns: its part spine, types.jsonl, its vehicle
// vocabulary, models.jsonl, and the catalog, catalog.jsonl.
func catalogDir(b *testing.B) string {
	catalogOnce.Do(func() {
		d, err := nitrovin.Embedded()
		if err != nil {
			catalogOnce.err = err
			return
		}
		dir, err := benchTemp()
		if err != nil {
			catalogOnce.err = err
			return
		}
		dir = filepath.Join(dir, "catalog")
		if err := os.Mkdir(dir, 0o755); err != nil {
			catalogOnce.err = err
			return
		}
		catalogOnce.err = writeCatalog(dir, d.Month())
		catalogOnce.dir = dir
	})
	if catalogOnce.err != nil {
		b.Fatal(catalogOnce.err)
	}
	return catalogOnce.dir
}

var compiledOnce struct {
	sync.Once
	path string
	err  error
}

// compiledPath compiles the fixture's catalog once per run, as `nitrovin
// vocab compile` does, and returns the compiled file's path.
func compiledPath(b *testing.B) string {
	dir := catalogDir(b)
	compiledOnce.Do(func() {
		compiledOnce.path = filepath.Join(filepath.Dir(dir), "catalog.compiled")
		f, err := os.Create(compiledOnce.path)
		if err != nil {
			compiledOnce.err = err
			return
		}
		if compiledOnce.err = compileCatalog(f, dir); compiledOnce.err == nil {
			compiledOnce.err = f.Close()
		} else {
			f.Close()
		}
	})
	if compiledOnce.err != nil {
		b.Fatal(compiledOnce.err)
	}
	return compiledOnce.path
}

// compileCatalog compiles the fixture in dir to w as `nitrovin vocab
// compile` does: the embedded Decoder's month, vocab.OpenCompile, and
// vocab.Compile streaming the catalog, sorting in a temporary directory.
func compileCatalog(w io.Writer, dir string) error {
	d, err := nitrovin.Embedded()
	if err != nil {
		return err
	}
	files, source, err := vocab.OpenCompile(dir)
	if err != nil {
		return err
	}
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	scratch, err := os.MkdirTemp("", "nitrovin-bench-scratch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	_, err = vocab.Compile(w, d.Month(), files, src, source, vocab.CompileOptions{Scratch: vocab.DirScratch(scratch), Writer: nitrovin.Version()})
	return err
}

// countingReader counts the reads through it: what a compiled catalog
// costs per request where a read is dear, as in a browser (ADR 0023).
type countingReader struct {
	r io.ReaderAt
	n atomic.Int64
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.n.Add(1)
	return c.r.ReadAt(p, off)
}

// counted loads form as loadTranslator does, its compiled catalog, if it
// is one, opened through a countingReader, which it returns; nil for JSON
// Lines.
func counted(form catalogForm) (*nitrovin.Translator, *countingReader, error) {
	if form.name != "compiled" {
		tr, err := loadTranslator(form.paths)
		return tr, nil, err
	}
	d, err := nitrovin.Embedded()
	if err != nil {
		return nil, nil, err
	}
	files, err := vocab.Open(form.paths[:len(form.paths)-1]...)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(form.catalogFile)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	c := &countingReader{r: f}
	cf, err := vocab.OpenCompiled(form.catalogFile, c, fi.Size())
	if err != nil {
		return nil, nil, err
	}
	tr, err := nitrovin.NewTranslator(d, append(files, cf)...)
	return tr, c, err
}

// reads are the reads of each request a benchmark made.
type reads []int64

// report reports reads-p50, reads-p99 and reads-max.
func (r reads) report(b *testing.B) {
	if len(r) == 0 {
		return
	}
	slices.Sort(r)
	b.ReportMetric(float64(r[(len(r)-1)/2]), "reads-p50")
	b.ReportMetric(float64(r[(len(r)-1)*99/100]), "reads-p99")
	b.ReportMetric(float64(r[len(r)-1]), "reads-max")
}

// catalogForm is one form of the fixture's catalog: the paths --vocab
// loads it from, and the catalog file's own.
type catalogForm struct {
	name        string
	paths       []string
	catalogFile string
}

// catalogForms are the fixture's catalog as JSON Lines and compiled, each
// beside its spine and vehicle vocabulary.
func catalogForms(b *testing.B) []catalogForm {
	dir := catalogDir(b)
	compiled := compiledPath(b)
	return []catalogForm{
		{"jsonl", []string{dir}, filepath.Join(dir, "catalog.jsonl")},
		{"compiled", []string{filepath.Join(dir, "types.jsonl"), filepath.Join(dir, "models.jsonl"), compiled}, compiled},
	}
}

// writeCatalog writes the catalog-scale fixture to dir: the month's first
// catalogModels models as the catalog's vehicles, the first
// catalogUnmapped of them unmapped.
func writeCatalog(dir string, m vocab.Month) error {
	var models [][2]string // make, model
	var ids []int32
	for mk, mkName := range m.Makes() {
		for id, name := range m.Models(mk) {
			models, ids = append(models, [2]string{mkName, name}), append(ids, id)
		}
	}
	if len(models) < catalogModels {
		return fmt.Errorf("data month %s has %d models; the catalog needs %d", m.Name(), len(models), catalogModels)
	}
	models, ids = models[:catalogModels], ids[:catalogModels]
	vehicles := make([]string, len(models))
	catalogOnce.vehicles = make([]vocab.Vehicle, len(models))
	for i, mm := range models {
		entry := map[string]string{"make": mm[0], "model": mm[1] + fmt.Sprintf(" #%d", i)}
		v, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		vehicles[i] = string(v)
		catalogOnce.vehicles[i] = vocab.Vehicle{Entry: entry}
	}
	write := func(name string, lines func(w *bufio.Writer)) error {
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		defer f.Close()
		w := bufio.NewWriterSize(f, 1<<20)
		lines(w)
		if err := w.Flush(); err != nil {
			return err
		}
		return f.Close()
	}
	if err := write("types.jsonl", func(w *bufio.Writer) {
		fmt.Fprintln(w, `{"format":1,"kind":"spine","name":"bench-types","version":"synthetic"}`)
		for t := range catalogTypes {
			fmt.Fprintf(w, `{"id":"%03d","name":"Part type %d"}`+"\n", t+1, t+1)
		}
	}); err != nil {
		return err
	}
	if err := write("models.jsonl", func(w *bufio.Writer) {
		fmt.Fprintf(w, `{"format":1,"kind":"vocabulary","name":"bench-models","version":"synthetic","data_month":%q,"subject":"vehicle","fields":["make","model"]}`+"\n", m.Name())
		for i, v := range vehicles {
			if i < catalogUnmapped {
				fmt.Fprintf(w, `{"entry":%s,"mappings":[]}`+"\n", v)
			} else {
				fmt.Fprintf(w, `{"entry":%s,"mappings":[{"model":%d}]}`+"\n", v, ids[i])
			}
		}
	}); err != nil {
		return err
	}
	return write("catalog.jsonl", func(w *bufio.Writer) {
		fmt.Fprintf(w, `{"format":1,"kind":"catalog","name":"bench-catalog","version":"synthetic","release":"1.0","data_month":%q,"spine":"bench-types","vehicles":"bench-models"}`+"\n", m.Name())
		for i := range catalogFitments {
			fmt.Fprintf(w, `{"fitment":{"type":"%s","number":"%s"%s}`, catalogTypeOf(i), catalogNumberOf(i), catalogSide(i))
			if i%5 == 0 {
				w.WriteString(`,"note":"remanufactured"`)
			}
			w.WriteString(`,"applications":[`)
			for j := range applicationsPer[i%len(applicationsPer)] {
				if j > 0 {
					w.WriteByte(',')
				}
				opt := options[(i+j*3)%len(options)]
				if (i+j)%10 < 6 {
					opt = fmt.Sprintf("%s, w/ part %d-%d, from the parts tree", opt, i, j) // an application's own words
				}
				fmt.Fprintf(w, `{"vehicle":%s,"first":%d,"last":%d,"options":%q`, vehicles[catalogVehicleOf(i, j)], catalogFirstOf(i, j), catalogLastOf(i, j), opt)
				if (i+j)%2 == 0 {
					fmt.Fprintf(w, `,"qualifiers":%s`, qualifierSets[(i/2+j)%len(qualifierSets)])
				}
				w.WriteByte('}')
			}
			w.WriteString("]}\n")
		}
		// Each redirect points at a part type with an application for its
		// vehicle and years: fitment i's first.
		for r := range catalogRedirects {
			i := r * 7 % catalogFitments
			from := redirectFrom(i)
			fmt.Fprintf(w, `{"redirect":{"vehicle":%s,"first":%d,"last":%d,"from":"%s","to":"%s","options":"listed under %s"}}`+"\n",
				vehicles[catalogVehicleOf(i, 0)], catalogFirstOf(i, 0), catalogLastOf(i, 0), from, catalogTypeOf(i), catalogTypeOf(i))
		}
		for r := range catalogRetired {
			i := r * 461 % catalogFitments
			fmt.Fprintf(w, `{"retired":{"type":"%s","number":"R%07d"%s},"to":{"type":"%s","number":"%s"%s},"note":"0.9 → 1.0"}`+"\n",
				catalogTypeOf(i), r, catalogSide(i), catalogTypeOf(i), catalogNumberOf(i), catalogSide(i))
		}
	})
}

// BenchmarkLoadCatalog loads the catalog-scale fixture as --vocab does (the
// embedded Decoder, vocab.Open and NewTranslator), as JSON Lines and
// compiled, reporting its fitments, applications and redirects, its
// file's size, the heap it retains once loaded, and a loading process's
// peak RSS. ADR 0017's budget for JSON Lines is at most 15 s on one core,
// 1.5 GiB at peak and a file of 500 MB; ADR 0021's for the compiled form
// at most 1 s on one core, 512 MiB at peak, 256 MiB retained and a file
// of 500 MB.
func BenchmarkLoadCatalog(b *testing.B) {
	for _, form := range catalogForms(b) {
		b.Run(form.name, func(b *testing.B) {
			fi, err := os.Stat(form.catalogFile)
			if err != nil {
				b.Fatal(err)
			}
			var info vocab.CatalogInfo
			b.ReportAllocs()
			for b.Loop() {
				tr, err := loadTranslator(form.paths)
				if err != nil {
					b.Fatal(err)
				}
				info = tr.Catalog().Info()
			}
			b.StopTimer()
			heap, err := retained(form.paths)
			if err != nil {
				b.Fatal(err)
			}
			peak, err := peakRSS(form.paths)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(float64(info.Fitments), "fitments")
			b.ReportMetric(float64(info.Applications), "applications")
			b.ReportMetric(float64(info.Redirects), "redirects")
			b.ReportMetric(float64(fi.Size())/1e6, "file-MB")
			b.ReportMetric(heap/1e6, "heap-MB")
			b.ReportMetric(peak/1e6, "peak-RSS-MB")
		})
	}
}

// BenchmarkCompileCatalog compiles the catalog-scale fixture as `nitrovin
// vocab compile` does: stream it, check it, measure its drift and write
// the compiled file, reporting its size and a compiling process's peak
// RSS. Compile time has no budget (ADR 0021); it is recorded. Its peak has
// ADR 0023's: at most 256 MiB.
func BenchmarkCompileCatalog(b *testing.B) {
	dir := catalogDir(b)
	var w countingWriter
	for b.Loop() {
		w = 0
		if err := compileCatalog(&w, dir); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	peak, err := childPeakRSS(compileEnv, dir)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(w)/1e6, "file-MB")
	b.ReportMetric(peak/1e6, "peak-RSS-MB")
}

// countingWriter counts the bytes written to it, and keeps none.
type countingWriter int64

func (w *countingWriter) Write(p []byte) (int, error) {
	*w += countingWriter(len(p))
	return len(p), nil
}

// catalogQueries is how many different questions each lookup benchmark
// cycles through, so its p99 is over many parts of the file.
const catalogQueries = 4096

// BenchmarkCatalogReader times fitment.Reader's lookups on the
// catalog-scale fixture, as JSON Lines and compiled, each cycling through
// catalogQueries questions about fitments spread over the catalog:
//
//   - by-vehicle: a part type, a vehicle and a year, the candidates of a
//     fit request (ADR 0018: at most 5 ms p99);
//   - summary: every part type for a vehicle and a year, asked once (part
//     type ""), the lookup of a part-type summary (at most 10 ms p99);
//   - by-number: a fitment key, the lookup of a reverse request (at most
//     1 ms per key);
//   - redirect: a part type, a vehicle and a year;
//   - page: 1,000 fitments of a part type after a key, a list page.
//
// The compiled form also reports its reads per lookup (ADR 0023: a
// 1,000-fitment page at most 8).
func BenchmarkCatalogReader(b *testing.B) {
	vehicles := func(i int) []vocab.Vehicle { return []vocab.Vehicle{catalogOnce.vehicles[catalogVehicleOf(i, 0)]} }
	at := func(k int) int { return k * 7919 % catalogFitments } // the fitment question k is about
	for _, form := range catalogForms(b) {
		b.Run(form.name, func(b *testing.B) {
			tr, counter, err := counted(form)
			if err != nil {
				b.Fatal(err)
			}
			cat := tr.Catalog()
			cycle := func(ask func(i int) int) func() error {
				k := 0
				return func() error {
					i := at(k % catalogQueries)
					k++
					if ask(i) == 0 {
						return fmt.Errorf("no answer about fitment %d", i)
					}
					return nil
				}
			}
			measure := func(b *testing.B, f func() error) { measureReads(b, f, counter) }
			b.Run("by-vehicle", func(b *testing.B) {
				measure(b, cycle(func(i int) int {
					return len(cat.ByVehicle(catalogTypeOf(i), vehicles(i), new(catalogFirstOf(i, 0)), nil))
				}))
			})
			b.Run("summary", func(b *testing.B) {
				measure(b, cycle(func(i int) int {
					return len(cat.ByVehicle("", vehicles(i), new(catalogFirstOf(i, 0)), nil))
				}))
			})
			b.Run("by-number", func(b *testing.B) {
				measure(b, cycle(func(i int) int {
					fit, _ := cat.ByNumber(catalogTypeOf(i), catalogNumberOf(i), catalogSideAt(i))
					if fit == nil {
						return 0
					}
					return len(fit.Applications)
				}))
			})
			b.Run("redirect", func(b *testing.B) {
				// Redirect r is about fitment r*7%catalogFitments.
				measure(b, cycle(func(i int) int {
					r := i % catalogRedirects
					j := r * 7 % catalogFitments
					return len(cat.Redirect(redirectFrom(j), vehicles(j), new(catalogFirstOf(j, 0))))
				}))
			})
			b.Run("page", func(b *testing.B) {
				measure(b, cycle(func(i int) int {
					after := vocab.FitmentKey{Type: catalogTypeOf(i), Number: catalogNumberOf(i), Side: catalogSideAt(i)}
					return len(cat.Fitments(catalogTypeOf(i), &after, 1000)) + 1 // the last of a type has none after it
				}))
			})
		})
	}
}

// catalogSideAt is fitment i's side as a Reader takes it.
func catalogSideAt(i int) vocab.Side {
	s, _ := vocab.ParseSide(catalogSideOf(i))
	return s
}

// redirectFrom is the part type a redirect about fitment i redirects from.
func redirectFrom(i int) string { return fmt.Sprintf("%03d", (i+101)%catalogTypes+1) }

// loadTranslator is load's Translator.
func loadTranslator(paths []string) (*nitrovin.Translator, error) {
	d, err := nitrovin.Embedded()
	if err != nil {
		return nil, err
	}
	files, err := vocab.Open(paths...)
	if err != nil {
		return nil, err
	}
	return nitrovin.NewTranslator(d, files...)
}

// retained is the heap, in bytes, a load of paths holds once collected.
func retained(paths []string) (float64, error) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	tr, err := loadTranslator(paths)
	if err != nil {
		return 0, err
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(tr)
	return float64(after.HeapAlloc) - float64(before.HeapAlloc), nil
}

// The catalog-scale fixture's generator, which writeCatalog writes and
// BenchmarkFit asks from: fitment i's part type, number and side (one in
// three is one of a left and right pair: one part type, one number), and
// its application j's vehicle (an index into the vehicle entries) and
// years.
func catalogTypeOf(i int) string { return fmt.Sprintf("%03d", i/2%catalogTypes+1) }

func catalogNumberOf(i int) string {
	if i%6 < 2 {
		return fmt.Sprintf("N%07d", i-i%2)
	}
	return fmt.Sprintf("N%07d", i)
}

// catalogSideOf is fitment i's side, "" for none.
func catalogSideOf(i int) string {
	switch i % 6 {
	case 0:
		return "left"
	case 1:
		return "right"
	}
	return ""
}

// catalogSide is fitment i's side as its key's member, "" for none.
func catalogSide(i int) string {
	if s := catalogSideOf(i); s != "" {
		return `,"side":"` + s + `"`
	}
	return ""
}

func catalogVehicleOf(i, j int) int { return (i*31 + j*977) % catalogModels }
func catalogFirstOf(i, j int) int   { return 1981 + (i+j*7)%40 }
func catalogLastOf(i, j int) int    { return catalogFirstOf(i, j) + (i+j)%6 }
