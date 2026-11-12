package vocab

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
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

// Option text, as a catalog's builder leaves it: mostly a few phrases, some
// a fitment's own.
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
	paths []string
	err   error
}

// catalogPaths writes the catalog-scale fixture once per run: its part
// spine, its vehicle vocabulary and the catalog.
func catalogPaths(b *testing.B) []string {
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
		catalogOnce.paths = []string{dir}
	})
	if catalogOnce.err != nil {
		b.Fatal(catalogOnce.err)
	}
	return catalogOnce.paths
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
	for i, mm := range models {
		v, err := json.Marshal(map[string]string{"make": mm[0], "model": mm[1] + fmt.Sprintf(" #%d", i)})
		if err != nil {
			return err
		}
		vehicles[i] = string(v)
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
	// fitment i's part type, number and side, and its application j's
	// vehicle and years: functions of i and j, so redirects and retired
	// numbers can point at them. One fitment in three is one of a left and
	// right pair: one part type, one number.
	typeOf := func(i int) string { return fmt.Sprintf("%03d", i/2%catalogTypes+1) }
	numberOf := func(i int) string {
		if i%6 < 2 {
			return fmt.Sprintf("N%07d", i-i%2)
		}
		return fmt.Sprintf("N%07d", i)
	}
	sideOf := func(i int) string {
		switch i % 6 {
		case 0:
			return `,"side":"left"`
		case 1:
			return `,"side":"right"`
		}
		return ""
	}
	vehicleOf := func(i, j int) int { return (i*31 + j*977) % catalogModels }
	firstOf := func(i, j int) int { return 1981 + (i+j*7)%40 }
	lastOf := func(i, j int) int { return firstOf(i, j) + (i+j)%6 }
	return write("catalog.jsonl", func(w *bufio.Writer) {
		fmt.Fprintf(w, `{"format":1,"kind":"catalog","name":"bench-catalog","version":"synthetic","release":"1.0","data_month":%q,"spine":"bench-types","vehicles":"bench-models"}`+"\n", m.Name())
		for i := range catalogFitments {
			fmt.Fprintf(w, `{"fitment":{"type":"%s","number":"%s"%s}`, typeOf(i), numberOf(i), sideOf(i))
			if i%5 == 0 {
				w.WriteString(`,"note":"remanufactured"`)
			}
			w.WriteString(`,"applications":[`)
			for j := range applicationsPer[i%len(applicationsPer)] {
				if j > 0 {
					w.WriteByte(',')
				}
				opt := options[(i+j*3)%len(options)]
				if (i+j)%10 == 9 {
					opt = fmt.Sprintf("w/ part %d", i) // a fitment's own words
				}
				fmt.Fprintf(w, `{"vehicle":%s,"first":%d,"last":%d,"options":%q`, vehicles[vehicleOf(i, j)], firstOf(i, j), lastOf(i, j), opt)
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
			from := fmt.Sprintf("%03d", (i+101)%catalogTypes+1)
			fmt.Fprintf(w, `{"redirect":{"vehicle":%s,"first":%d,"last":%d,"from":"%s","to":"%s","options":"listed under %s"}}`+"\n",
				vehicles[vehicleOf(i, 0)], firstOf(i, 0), lastOf(i, 0), from, typeOf(i), typeOf(i))
		}
		for r := range catalogRetired {
			i := r * 461 % catalogFitments
			fmt.Fprintf(w, `{"retired":{"type":"%s","number":"R%07d"%s},"to":{"type":"%s","number":"%s"%s},"note":"0.9 → 1.0"}`+"\n",
				typeOf(i), r, sideOf(i), typeOf(i), numberOf(i), sideOf(i))
		}
	})
}

// BenchmarkLoadCatalog loads the catalog-scale fixture as --vocab does (the
// embedded Decoder, vocab.Open and NewTranslator), reporting its fitments,
// applications and redirects, its file's size, the heap it retains once
// loaded, and a loading process's peak RSS: ADR 0017's budget is at most
// 15 s on one core, 1.5 GiB at peak and a file of 500 MB.
func BenchmarkLoadCatalog(b *testing.B) {
	paths := catalogPaths(b)
	fi, err := os.Stat(filepath.Join(paths[0], "catalog.jsonl"))
	if err != nil {
		b.Fatal(err)
	}
	var info vocab.CatalogInfo
	b.ReportAllocs()
	for b.Loop() {
		tr, err := loadTranslator(paths)
		if err != nil {
			b.Fatal(err)
		}
		info = tr.Catalog().Info()
	}
	b.StopTimer()
	heap, err := retained(paths)
	if err != nil {
		b.Fatal(err)
	}
	peak, err := peakRSS(paths)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(info.Fitments), "fitments")
	b.ReportMetric(float64(info.Applications), "applications")
	b.ReportMetric(float64(info.Redirects), "redirects")
	b.ReportMetric(float64(fi.Size())/1e6, "file-MB")
	b.ReportMetric(heap/1e6, "heap-MB")
	b.ReportMetric(peak/1e6, "peak-RSS-MB")
}

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
