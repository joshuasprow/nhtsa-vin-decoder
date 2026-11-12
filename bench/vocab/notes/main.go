// Command notes turns bench/vocab's benchmark output into the Markdown table
// release.yml puts in the release notes, beside the performance budget
// (https://github.com/joshuasprow/nitrovin/issues/142).
//
//	go run ./bench/vocab/notes bench.txt >bench.md
//
// It reads `go test -bench` output from the files named, or stdin: requests
// at GOMAXPROCS=1 (no -N suffix on their names), load at every GOMAXPROCS
// given. Missing benchmarks show as —.
package main

import (
	"bufio"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "notes:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, out io.Writer) error {
	r := newResults()
	if len(args) == 0 {
		if err := r.read(stdin); err != nil {
			return err
		}
	}
	for _, name := range args {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		err = r.read(f)
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if len(r.bench) == 0 {
		return fmt.Errorf("no benchmark results")
	}
	_, err := io.WriteString(out, r.table())
	return err
}

// key is one benchmark at one GOMAXPROCS: its name without "Benchmark" and
// without the -N suffix.
type key struct {
	name  string
	procs int
}

type results struct {
	config map[string]string // goos, goarch, cpu, go
	bench  map[key]map[string]float64
}

func newResults() *results {
	return &results{config: map[string]string{}, bench: map[key]map[string]float64{}}
}

var (
	configLine  = regexp.MustCompile(`^([a-z][^\s:]*): (.+)$`)
	procsSuffix = regexp.MustCompile(`-(\d+)$`)
)

// read parses the benchmark format: "key: value" configuration lines, and
// "BenchmarkName[-N] iterations value unit [value unit]..." result lines.
func (r *results) read(in io.Reader) error {
	s := bufio.NewScanner(in)
	for s.Scan() {
		line := s.Text()
		if m := configLine.FindStringSubmatch(line); m != nil {
			r.config[m[1]] = strings.TrimSpace(m[2])
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 || len(f)%2 != 0 || !strings.HasPrefix(f[0], "Benchmark") {
			continue
		}
		if _, err := strconv.Atoi(f[1]); err != nil {
			continue
		}
		k := key{name: strings.TrimPrefix(f[0], "Benchmark"), procs: 1}
		if m := procsSuffix.FindStringSubmatch(k.name); m != nil {
			k.procs, _ = strconv.Atoi(m[1])
			k.name = strings.TrimSuffix(k.name, m[0])
		}
		metrics := map[string]float64{}
		for i := 2; i < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				return fmt.Errorf("%q: %w", line, err)
			}
			metrics[f[i+1]] = v
		}
		r.bench[k] = metrics
	}
	return s.Err()
}

var sets = []struct{ name, label string }{
	{"fixture", "fixture"},
	{"epa", "EPA"},
	{"mvl-1M", "MVL-1M (synthetic)"},
}

var requests = []struct{ name, label, budget string }{
	{"Translate/%s/vin", "translate VIN (decode + translate)", "p99 < 1 ms"},
	{"Translate/%s/name", "translate name", "p99 < 1 ms"},
	{"Translate/%s/spine", "translate spine id (vPIC model → vocabulary)", "p99 < 1 ms"},
	{"List/%s/first", "list, first page", "p99 < 1 ms at 1M"},
	{"List/%s/next", "list, next page (cursor)", "p99 < 1 ms at 1M"},
	{"List/%s/where", "list, where first field", "p99 < 1 ms at 1M"},
	{"List/%s/vpic-make", "list `vpic` where make", "p99 < 1 ms at 1M"},
	{"List/%s/vpic-model", "list `vpic` where model and element", "none set"},
}

const loadBudget = "< 100 ms up to EPA scale; < 2 s and < 1 GB peak at 1M"

func (r *results) table() string {
	var b strings.Builder
	var present []int // indexes into sets
	loadProcs := map[int]bool{}
	for i, s := range sets {
		for k := range r.bench {
			if strings.Contains(k.name, "/"+s.name+"/") || k.name == "Load/"+s.name {
				present = append(present, i)
				break
			}
		}
		for k := range r.bench {
			if k.name == "Load/"+s.name {
				loadProcs[k.procs] = true
			}
		}
	}
	slices.Sort(present)
	procs := slices.Sorted(maps.Keys(loadProcs))

	b.WriteString("## Benchmarks\n\n")
	fmt.Fprintf(&b, "`bench/vocab` on %s. Requests run on one core: p50 / p99, then throughput. ", r.machine())
	b.WriteString("The [budgets](https://github.com/joshuasprow/nitrovin/issues/142) are stated for the reference machine (Apple M5 Pro class); ")
	b.WriteString("a CI runner measures about 2× slower, so these numbers are reported beside them, not held to them. ")
	b.WriteString("To reproduce them: [CONTRIBUTING.md](https://github.com/joshuasprow/nitrovin/blob/main/CONTRIBUTING.md#benchmarks-in-the-release).\n\n")

	if m, ok := r.bench[key{"Decode", 1}]; ok {
		fmt.Fprintf(&b, "**Decode alone** (`1HGCM82633A004352`): %s · %s. Budget: ≥ 20k VIN/s and < 1 ms.\n\n", quantiles(m), throughput(m["ns/op"]))
	}
	if m, ok := r.bench[key{"ModelValuesFirst", 1}]; ok {
		fmt.Fprintf(&b, "**Model values, first ask** (Series and Drive Type on a Decoder just loaded, ADR 0015): %s. No budget; ADR 0015 measured 0.14-0.16 s.\n\n", duration(m["ns/op"]))
	}

	b.WriteString("| |")
	for _, i := range present {
		label := sets[i].label
		for _, p := range procs {
			if m, ok := r.bench[key{"Load/" + sets[i].name, p}]; ok {
				label += fmt.Sprintf(", %s entries", count(m["entries"]))
				break
			}
		}
		fmt.Fprintf(&b, " %s |", label)
	}
	b.WriteString(" budget |\n|---|")
	for range present {
		b.WriteString("---|")
	}
	b.WriteString("---|\n")

	for _, p := range procs {
		cores := "1 core"
		if p != 1 {
			cores = fmt.Sprintf("%d cores", p)
		}
		fmt.Fprintf(&b, "| load, %s |", cores)
		for _, i := range present {
			m, ok := r.bench[key{"Load/" + sets[i].name, p}]
			if !ok {
				b.WriteString(" — |")
				continue
			}
			fmt.Fprintf(&b, " %s, %s peak |", duration(m["ns/op"]), size(m["peak-RSS-MB"]))
		}
		fmt.Fprintf(&b, " %s |\n", loadBudget)
	}
	for _, q := range requests {
		fmt.Fprintf(&b, "| %s |", q.label)
		for _, i := range present {
			m, ok := r.bench[key{fmt.Sprintf(q.name, sets[i].name), 1}]
			if !ok {
				b.WriteString(" — |")
				continue
			}
			fmt.Fprintf(&b, " %s · %s |", quantiles(m), throughput(m["ns/op"]))
		}
		fmt.Fprintf(&b, " %s |\n", q.budget)
	}
	b.WriteString(r.catalog())
	b.WriteString(r.fit())
	return b.String()
}

// fitRequests are BenchmarkFit's request kinds, with ADR 0018's budget
// and ADR 0023's for the compiled form's reads; each runs on every one of
// catalogForms.
var fitRequests = []struct{ name, label, budget, reads string }{
	{"candidates", "candidates (vehicle, year, part type)", "p99 ≤ 5 ms", "≤ 10"},
	{"part-types", "part types (vehicle, year)", "p99 ≤ 10 ms", "≤ 20"},
	{"related", "related vehicles", "p99 ≤ 50 ms", "≤ 100"},
	{"part-types-any-year", "part types, no year", "p99 ≤ 10 ms", "≤ 20"},
	{"related-any-year", "related vehicles, no year", "p99 ≤ 50 ms", "≤ 100"},
	{"reverse", "reverse (one fitment key)", "p99 ≤ 1 ms per key", "≤ 2"},
}

// fit is the fit requests' table on the catalog-scale fixture (ADR 0018),
// the catalog's forms side by side, and the compiled form's reads per
// request (ADR 0023), "" when they did not run.
func (r *results) fit() string {
	var b strings.Builder
	for _, q := range fitRequests {
		cells, items, any := "", 0.0, false
		for _, f := range catalogForms {
			m, ok := r.bench[key{"Fit/" + f.name + "/" + q.name, 1}]
			if !ok {
				cells += " — |"
				continue
			}
			any, items = true, m["items"]
			cells += fmt.Sprintf(" %s · %s |", quantiles(m), throughput(m["ns/op"]))
		}
		if !any {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("\n**Fit** (the catalog-scale fixture, one core): p50 / p99 · throughput, and the compiled form's reads per request, p99 / most.\n\n| |")
			for _, f := range catalogForms {
				b.WriteString(" " + f.label + " |")
			}
			b.WriteString(" items | budget | reads | read budget |\n|---|---|---|---|---|---|---|\n")
		}
		fmt.Fprintf(&b, "| %s |%s %.0f | %s | %s | %s |\n", q.label, cells, items, q.budget, reads(r.bench[key{"Fit/compiled/" + q.name, 1}]), q.reads)
	}
	return b.String()
}

// reads is a benchmark's reads per operation, "p99 / most", or — when it
// counted none.
func reads(m map[string]float64) string {
	if _, ok := m["reads-p99"]; !ok {
		return "—"
	}
	return fmt.Sprintf("%.0f / %.0f", m["reads-p99"], m["reads-max"])
}

// The catalog's forms, as BenchmarkLoadCatalog and BenchmarkCatalogReader
// name them, each with its budget: ADR 0017's interim one for JSON Lines,
// ADR 0021's for the compiled form.
var catalogForms = []struct{ name, label, budget string }{
	{"jsonl", "JSON Lines", "≤ 15 s on one core, ≤ 1.5 GiB peak, file ≤ 500 MB"},
	{"compiled", "compiled", "≤ 1 s on one core, ≤ 512 MiB peak, ≤ 256 MiB retained, file ≤ 500 MB"},
}

// catalogLookups are BenchmarkCatalogReader's, with ADR 0018's budget for
// the request each serves, and ADR 0023's for a page's reads.
var catalogLookups = []struct{ name, label, budget string }{
	{"by-vehicle", "by vehicle (a part type, a vehicle, a year)", "p99 ≤ 5 ms"},
	{"summary", "by vehicle, every part type", "p99 ≤ 10 ms"},
	{"by-number", "by number", "p99 ≤ 1 ms"},
	{"redirect", "redirect", ""},
	{"page", "a page of 1,000 fitments", "≤ 8 reads"},
}

// catalog is the catalog's table, both forms side by side (ADR 0017,
// ADR 0021), "" when its load did not run: its file, its load at each
// GOMAXPROCS, its compiling, and its lookups on one core.
func (r *results) catalog() string {
	var procs []int
	for k := range r.bench {
		if k.name == "LoadCatalog/"+catalogForms[0].name {
			procs = append(procs, k.procs)
		}
	}
	if len(procs) == 0 {
		return ""
	}
	slices.Sort(procs)
	var b strings.Builder
	m := r.bench[key{"LoadCatalog/" + catalogForms[0].name, procs[0]}]
	fmt.Fprintf(&b, "\n**Catalog** (synthetic, ADR 0017: %s fitments, %s applications, %s redirects), as JSON Lines and compiled (ADR 0021). Budgets: JSON Lines %s; compiled %s.\n\n",
		count(m["fitments"]), count(m["applications"]), count(m["redirects"]), catalogForms[0].budget, catalogForms[1].budget)
	b.WriteString("| |")
	for _, f := range catalogForms {
		b.WriteString(" " + f.label + " |")
	}
	b.WriteString(" budget |\n|---|---|---|---|\n")
	row := func(label, budget string, cell func(form string) string) {
		b.WriteString("| " + label + " |")
		for _, f := range catalogForms {
			b.WriteString(" " + cell(f.name) + " |")
		}
		b.WriteString(" " + budget + " |\n")
	}
	row("file", "≤ 500 MB", func(form string) string {
		if m, ok := r.bench[key{"LoadCatalog/" + form, procs[0]}]; ok {
			return size(m["file-MB"])
		}
		return "—"
	})
	for _, p := range procs {
		cores := "1 core"
		if p != 1 {
			cores = fmt.Sprintf("%d cores", p)
		}
		row("load, "+cores+": time, peak, retained heap", "", func(form string) string {
			m, ok := r.bench[key{"LoadCatalog/" + form, p}]
			if !ok {
				return "—"
			}
			return fmt.Sprintf("%s, %s, %s", duration(m["ns/op"]), size(m["peak-RSS-MB"]), size(m["heap-MB"]))
		})
	}
	row("compile, 1 core: time, peak", "time recorded; ≤ 256 MiB peak", func(form string) string {
		if m, ok := r.bench[key{"CompileCatalog", 1}]; ok && form == "compiled" {
			return fmt.Sprintf("%s, %s", duration(m["ns/op"]), size(m["peak-RSS-MB"]))
		}
		return "—"
	})
	for _, l := range catalogLookups {
		row(l.label, l.budget, func(form string) string {
			m, ok := r.bench[key{"CatalogReader/" + form + "/" + l.name, 1}]
			switch {
			case !ok:
				return "—"
			case form == "compiled":
				return quantiles(m) + " · " + reads(m) + " reads"
			}
			return quantiles(m)
		})
	}
	return b.String()
}

// machine names the hardware and toolchain from the configuration lines.
func (r *results) machine() string {
	var parts []string
	for _, k := range []string{"cpu", "goos", "go"} {
		v := r.config[k]
		if v == "" {
			continue
		}
		if k == "goos" && r.config["goarch"] != "" {
			v += "/" + r.config["goarch"]
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, ", ")
}

// quantiles is "p50 / p99 unit", or "p50 unit / p99 unit" across units.
func quantiles(m map[string]float64) string {
	p50, p99 := duration(m["p50-ns"]), duration(m["p99-ns"])
	v50, u50, _ := strings.Cut(p50, " ")
	v99, u99, _ := strings.Cut(p99, " ")
	if u50 == u99 {
		return v50 + " / " + v99 + " " + u99
	}
	return p50 + " / " + p99
}

// duration rounds ns to two or three significant figures.
func duration(ns float64) string {
	switch {
	case ns < 10e3:
		return fmt.Sprintf("%.1f µs", ns/1e3)
	case ns < 1e6:
		return fmt.Sprintf("%.0f µs", ns/1e3)
	case ns < 10e6:
		return fmt.Sprintf("%.1f ms", ns/1e6)
	case ns < 1e9:
		return fmt.Sprintf("%.0f ms", ns/1e6)
	default:
		return fmt.Sprintf("%.2f s", ns/1e9)
	}
}

// throughput is one core's operations per second at ns per operation.
func throughput(ns float64) string {
	ops := 1e9 / ns
	switch {
	case ops >= 1e6:
		return fmt.Sprintf("%.1fM/s", ops/1e6)
	case ops >= 1e4:
		return fmt.Sprintf("%.0fk/s", ops/1e3)
	case ops >= 1e3:
		return fmt.Sprintf("%.1fk/s", ops/1e3)
	default:
		return fmt.Sprintf("%.0f/s", ops)
	}
}

func size(mb float64) string {
	if mb < 1000 {
		return fmt.Sprintf("%.0f MB", mb)
	}
	return fmt.Sprintf("%.2f GB", mb/1000)
}

// count writes n with thousands separators.
func count(n float64) string {
	s := strconv.FormatInt(int64(n), 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
