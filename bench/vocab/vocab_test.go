// Package vocab benchmarks vocabularies against the performance budget
// (https://github.com/joshuasprow/nitrovin/issues/142): load, translate and
// list on the fixture, the EPA vocabulary and a generated 1M-entry one, and
// decode alone. release.yml runs them and puts the table bench/vocab/notes
// makes in the release notes; nothing here is a gate.
//
//	gh release download "$(go list -m -f '{{.Version}}' github.com/joshuasprow/nitrovin-data)" \
//	  -R joshuasprow/nitrovin-data -p epa-vehicles.jsonl -D /tmp
//	export NITROVIN_EPA_VOCAB=/tmp/epa-vehicles.jsonl
//	GOMAXPROCS=1 go test -run '^$' -bench . ./bench/vocab | tee bench.txt
//	go test -run '^$' -bench Load ./bench/vocab | tee -a bench.txt
//	go run ./bench/vocab/notes bench.txt
//
// Requests are budgeted per core, so the table reads them at GOMAXPROCS=1;
// load is read at every GOMAXPROCS run. Set GOMAXPROCS in the environment,
// not with -cpu, which sub-benchmarks do not all follow. Without
// NITROVIN_EPA_VOCAB the EPA set is skipped. The 1M-entry file is written
// to a temporary directory on first use, about 160 MB.
//
// Each request benchmark reports p50-ns and p99-ns beside ns/op, timed per
// iteration (the clock reads included). Each load (the embedded Decoder,
// vocab.Open and NewTranslator, what a process loading --vocab does)
// reports entries and peak-RSS-MB, the maximum resident set of a child
// process that loads the set once.
package vocab

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"math/bits"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// peakEnv makes the test binary a child that loads the paths it names
// (separated by the OS's list separator) and exits: BenchmarkLoad reads its
// peak RSS.
const peakEnv = "NITROVIN_BENCH_PEAK"

// decodeVIN is decode's budget VIN, and the EPA and MVL sets' translate VIN.
const decodeVIN = "1HGCM82633A004352"

// now freezes the decode clock.
var now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

var tmp string // the 1M-entry file's directory, removed after the run

func TestMain(m *testing.M) {
	if paths := os.Getenv(peakEnv); paths != "" {
		if _, err := load(filepath.SplitList(paths)); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	flag.Parse()
	if flag.Lookup("test.bench").Value.String() != "" {
		fmt.Printf("go: %s\n", runtime.Version()) // a configuration line, for notes
	}
	code := m.Run()
	if tmp != "" {
		os.RemoveAll(tmp)
	}
	os.Exit(code)
}

// set is one vocabulary the benchmarks load, translate through and list.
type set struct {
	name  string
	paths func(b *testing.B) []string
	to    string // the vocabulary translated to and listed
	vin   string // a VIN that reaches entries of to

	once sync.Once
	tr   *nitrovin.Translator
	f    *vocab.File
	err  error
}

var sets = []*set{
	{name: "fixture", paths: fixture, to: "yard", vin: "1D7RV1GT5AS123456"},
	{name: "epa", paths: epa, to: "epa-vehicles", vin: decodeVIN},
	{name: "mvl-1M", paths: mvl, to: "mvl", vin: decodeVIN},
}

func fixture(*testing.B) []string { return []string{"../../testdata/vocab"} }

func epa(b *testing.B) []string {
	p := os.Getenv("NITROVIN_EPA_VOCAB")
	if p == "" {
		b.Skip("NITROVIN_EPA_VOCAB names no epa-vehicles.jsonl (nitrovin-data's release asset)")
	}
	return []string{p}
}

var mvlOnce struct {
	sync.Once
	path string
	err  error
}

// mvl writes the 1M-entry vocabulary once per run.
func mvl(b *testing.B) []string {
	mvlOnce.Do(func() {
		d, err := nitrovin.Embedded()
		if err != nil {
			mvlOnce.err = err
			return
		}
		if tmp, err = os.MkdirTemp("", "nitrovin-bench-"); err != nil {
			mvlOnce.err = err
			return
		}
		mvlOnce.path = filepath.Join(tmp, "mvl.jsonl")
		mvlOnce.err = writeMVL(mvlOnce.path, d.Month(), 1_000_000)
	})
	if mvlOnce.err != nil {
		b.Fatal(mvlOnce.err)
	}
	return []string{mvlOnce.path}
}

// writeMVL writes n entries at the scale of eBay's Master Vehicle List:
// every vPIC model of the month × trims × model years, fields make, model,
// trim, year, each mapping to its vPIC model for that one year. The Accord,
// decodeVIN's model, gets every year 1981–2026 so the VIN reaches entries.
func writeMVL(path string, m vocab.Month, n int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	fmt.Fprintf(w, `{"format":1,"kind":"vocabulary","name":"mvl","version":"synthetic","data_month":%q,"subject":"vehicle","fields":["make","model","trim","year"]}`+"\n", m.Name())
	type model struct {
		make, name string
		id         int32
	}
	var all []model
	for mk, mkName := range m.Makes() {
		for id, name := range m.Models(mk) {
			all = append(all, model{mkName, name, id})
		}
	}
	perModel := n/len(all) + 1
	written := 0
	for _, x := range all {
		k := perModel
		if x.name == "Accord" {
			k = 8 * 46
		}
		for i := 0; i < k && written < n; i++ {
			trim, year := fmt.Sprintf("Trim %d", i%8), 1981+i/8
			e, err := json.Marshal(map[string]string{"make": x.make, "model": x.name, "trim": trim, "year": fmt.Sprint(year)})
			if err != nil {
				return err
			}
			fmt.Fprintf(w, `{"entry":%s,"mappings":[{"model":%d,"first":%d,"last":%d,"origin":"derived"}]}`+"\n", e, x.id, year, year)
			written++
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}

// load is what a process loading --vocab does: the embedded Decoder, then
// vocab.Open and NewTranslator. It returns the entries loaded.
func load(paths []string) (int, error) {
	d, err := nitrovin.Embedded()
	if err != nil {
		return 0, err
	}
	files, err := vocab.Open(paths...)
	if err != nil {
		return 0, err
	}
	if _, err := nitrovin.NewTranslator(d, files...); err != nil {
		return 0, err
	}
	entries := 0
	for _, f := range files {
		entries += len(f.Entries)
	}
	return entries, nil
}

// translator loads s once for the request benchmarks.
func (s *set) translator(b *testing.B) (*nitrovin.Translator, *vocab.File) {
	paths := s.paths(b)
	s.once.Do(func() {
		d, err := nitrovin.Embedded()
		if err != nil {
			s.err = err
			return
		}
		files, err := vocab.Open(paths...)
		if err != nil {
			s.err = err
			return
		}
		for _, f := range files {
			if f.Name == s.to {
				s.f = f
			}
		}
		if s.f == nil {
			s.err = fmt.Errorf("%s: no vocabulary %s", s.name, s.to)
			return
		}
		s.tr, s.err = nitrovin.NewTranslator(d, files...)
	})
	if s.err != nil {
		b.Fatal(s.err)
	}
	return s.tr, s.f
}

// histogram counts durations in buckets 1/64 of a power of two wide, so a
// percentile is within 1.6% and recording allocates nothing.
type histogram [64 * 64]int64

func (h *histogram) record(d time.Duration) {
	ns := uint64(max(d, 1))
	e := bits.Len64(ns) - 1
	m := 0
	if e >= 6 {
		m = int(ns>>(e-6)) & 63
	} else {
		m = int(ns<<(6-e)) & 63
	}
	h[e*64+m]++
}

// quantile is the lower bound of the bucket holding quantile q.
func (h *histogram) quantile(q float64) float64 {
	var total int64
	for _, c := range h {
		total += c
	}
	rank := int64(q * float64(total-1))
	for i, c := range h {
		if rank < c {
			e, m := i/64, i%64
			return float64(uint64(64+m)<<e) / 64
		}
		rank -= c
	}
	return 0
}

// measure runs f under b.Loop and reports p50-ns and p99-ns beside ns/op.
func measure(b *testing.B, f func() error) {
	b.Helper()
	if err := f(); err != nil {
		b.Fatal(err)
	}
	h := new(histogram)
	b.ReportAllocs()
	for b.Loop() {
		t := time.Now()
		if err := f(); err != nil {
			b.Fatal(err)
		}
		h.record(time.Since(t))
	}
	b.ReportMetric(h.quantile(0.50), "p50-ns")
	b.ReportMetric(h.quantile(0.99), "p99-ns")
}

func BenchmarkDecode(b *testing.B) {
	d, err := nitrovin.Embedded()
	if err != nil {
		b.Fatal(err)
	}
	measure(b, func() error {
		_, err := d.Decode(contract.Request{VIN: decodeVIN, Now: now})
		return err
	})
}

func BenchmarkLoad(b *testing.B) {
	for _, s := range sets {
		b.Run(s.name, func(b *testing.B) {
			paths := s.paths(b)
			entries := 0
			b.ReportAllocs()
			for b.Loop() {
				var err error
				if entries, err = load(paths); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(entries), "entries")
			peak, err := peakRSS(paths)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportMetric(peak/1e6, "peak-RSS-MB")
		})
	}
}

// peakRSS is the maximum resident set of this test binary run as a child
// that loads paths once, on this benchmark's GOMAXPROCS.
func peakRSS(paths []string) (float64, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(),
		peakEnv+"="+strings.Join(paths, string(os.PathListSeparator)),
		fmt.Sprintf("GOMAXPROCS=%d", runtime.GOMAXPROCS(0)))
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("peak RSS: %v: %s", err, out)
	}
	return maxRSS(cmd.ProcessState)
}

func BenchmarkTranslate(b *testing.B) {
	for _, s := range sets {
		b.Run(s.name, func(b *testing.B) {
			tr, f := s.translator(b)
			name, ref, year := probe(f)
			b.Run("vin", func(b *testing.B) {
				measure(b, func() error {
					_, err := tr.Translate(contract.TranslateRequest{From: contract.TranslateFrom{VIN: s.vin}, To: []string{s.to}, Now: now})
					return err
				})
			})
			b.Run("name", func(b *testing.B) {
				measure(b, func() error {
					_, err := tr.Translate(contract.TranslateRequest{From: contract.TranslateFrom{Vocabulary: s.to, Entry: name}, ModelYear: year, Now: now})
					return err
				})
			})
			b.Run("spine", func(b *testing.B) {
				measure(b, func() error {
					_, err := tr.Translate(contract.TranslateRequest{From: contract.TranslateFrom{SpineRef: ref}, To: []string{s.to}, ModelYear: year, Now: now})
					return err
				})
			})
		})
	}
}

// probe is the first mapped entry from the middle of f: its name, its first
// mapping's spine entry, and that mapping's first year.
func probe(f *vocab.File) (map[string]string, contract.SpineRef, contract.Null[int32]) {
	var e *vocab.Entry
	for i := len(f.Entries) / 2; i < len(f.Entries); i++ {
		if len(f.Entries[i].Mappings) > 0 {
			e = &f.Entries[i]
			break
		}
	}
	m := e.Mappings[0]
	ref := contract.SpineRef{Make: m.Make, Model: m.Model, Ext: m.Ext, ID: m.ID}
	if m.ID != "" {
		ref.Spine = f.Spine
	}
	var year contract.Null[int32]
	if m.First != nil {
		year = contract.Null[int32]{V: int32(*m.First), Valid: true}
	}
	return f.FieldsOf(e), ref, year
}

func BenchmarkList(b *testing.B) {
	for _, s := range sets {
		b.Run(s.name, func(b *testing.B) {
			tr, f := s.translator(b)
			name, _, _ := probe(f)
			first, err := tr.List(contract.ListRequest{Of: s.to})
			if err != nil {
				b.Fatal(err)
			}
			b.Run("first", func(b *testing.B) {
				measure(b, func() error {
					_, err := tr.List(contract.ListRequest{Of: s.to})
					return err
				})
			})
			if first.Next != "" {
				b.Run("next", func(b *testing.B) {
					measure(b, func() error {
						_, err := tr.List(contract.ListRequest{Of: s.to, After: first.Next})
						return err
					})
				})
			}
			b.Run("where", func(b *testing.B) {
				where := map[string]string{f.Fields[0]: name[f.Fields[0]]}
				measure(b, func() error {
					_, err := tr.List(contract.ListRequest{Of: s.to, Where: where})
					return err
				})
			})
			b.Run("vpic-make", func(b *testing.B) {
				where := map[string]string{"make": "474"}
				measure(b, func() error {
					_, err := tr.List(contract.ListRequest{Of: "vpic", Where: where})
					return err
				})
			})
		})
	}
}
