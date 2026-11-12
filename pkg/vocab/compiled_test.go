package vocab

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// compile compiles the JSON Lines catalog among files, measured against
// m, to a file in a temporary directory, returning its path.
func compile(t testing.TB, m Month, files []*File) string {
	t.Helper()
	var b bytes.Buffer
	if _, err := compileFiles(t, &b, m, files); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "catalog.compiled")
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// compileFiles compiles the JSON Lines catalog among files to w as
// `nitrovin vocab compile` does: streamed from its file, beside the rest.
func compileFiles(t testing.TB, w io.Writer, m Month, files []*File) ([]Finding, error) {
	t.Helper()
	var others []*File
	source := ""
	for _, f := range files {
		if f.Kind == KindCatalog && f.compiled == nil && source == "" {
			source = f.Path
			continue
		}
		others = append(others, f)
	}
	if source == "" {
		t.Fatal("no JSON Lines catalog to compile")
	}
	return compileSource(t, w, m, others, source)
}

// compileSource compiles the JSON Lines catalog at path, beside files, to
// w.
func compileSource(t testing.TB, w io.Writer, m Month, files []*File, path string) ([]Finding, error) {
	t.Helper()
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	return Compile(w, m, files, src, path, DirScratch(t.TempDir()), CompileOptions{Writer: "v0.0.0-test"})
}

// swapCatalog is files with the catalog swapped for the compiled one at
// path, opened beside the others.
func swapCatalog(t *testing.T, files []*File, path string) []*File {
	t.Helper()
	var paths []string
	for _, f := range files {
		if f.Kind != KindCatalog {
			paths = append(paths, f.Path)
		}
	}
	got, err := Open(append(paths, path)...)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// catalogOf is the catalog among files.
func catalogOf(t *testing.T, m Month, files []*File) *Catalog {
	t.Helper()
	set, err := NewSet(m, files)
	if err != nil {
		t.Fatal(err)
	}
	if set.Catalog() == nil {
		t.Fatal("no catalog")
	}
	return set.Catalog()
}

// compiledFixture is testdata/vocab/fitment's catalog, compiled.
func compiledFixture(t *testing.T) *Catalog {
	t.Helper()
	files, err := Open("../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	return catalogOf(t, fixtureMonth{}, swapCatalog(t, files, compile(t, fixtureMonth{}, files)))
}

// describeShared is what Shared answers, keeping every other candidate:
// each candidate passed to keep, as Matching's, and the spans of each
// kept, through two calls of Spans, which must agree.
func describeShared(c *Catalog, vs []Vehicle, y *int, s *Side) string {
	var cands []string
	sh := c.Shared(vs, y, s, func(cand *Candidate) bool {
		cands = append(cands, describeCandidates([]Candidate{*cand}))
		return len(cands)%2 == 1
	})
	if sh.Len() != (len(cands)+1)/2 {
		return fmt.Sprintf("%d kept of %d", sh.Len(), len(cands))
	}
	pass := func() []string {
		spans := make([]string, sh.Len())
		sh.Spans(func(k int, ss []SharedSpan) {
			var b strings.Builder
			for _, sp := range ss {
				b.WriteString(describeSpan(sp))
			}
			spans[k] += b.String()
		})
		return spans
	}
	first, again := pass(), pass()
	var b strings.Builder
	for i, cand := range cands {
		b.WriteString(cand)
		if i%2 == 0 {
			if first[i/2] != again[i/2] {
				return fmt.Sprintf("candidate %d's spans read again:%s\nfirst:%s", i, again[i/2], first[i/2])
			}
			b.WriteString(first[i/2])
		}
	}
	return b.String()
}

func describeSpan(sp SharedSpan) string {
	return fmt.Sprintf("\n\tspan %d %d-%d %b", sp.Vehicle, sp.First, sp.Last, sp.Bounds)
}

// describe is everything a fitment says, for comparing two catalogs'.
func describe(f *Fitment) string {
	if f == nil {
		return "<nil>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d %s %q apps=%d nil=%v", f.Line, f.Key, f.Note, len(f.Applications), f.Applications == nil)
	for i := range f.Applications {
		b.WriteString("\n\t" + describeApp(&f.Applications[i]))
	}
	return b.String()
}

func describeApp(a *Application) string {
	return fmt.Sprintf("%s %s %q %v nil=%v", a.Vehicle.key, rangeText(a.First, a.Last), a.Options, a.Qualifiers, a.Qualifiers == nil)
}

func describeCandidates(cs []Candidate) string {
	if cs == nil {
		return "<nil>"
	}
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(describe(c.Fitment) + "\n\tmatched")
		for _, a := range c.Applications {
			i := -1
			for j := range c.Fitment.Applications {
				if &c.Fitment.Applications[j] == a {
					i = j
				}
			}
			b.WriteString(" " + strconv.Itoa(i))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func describeRedirects(rds []*Redirect) string {
	if rds == nil {
		return "<nil>"
	}
	var b strings.Builder
	for _, rd := range rds {
		fmt.Fprintf(&b, "%d %s %s %s→%s %q\n", rd.Line, rd.Vehicle.key, rangeText(rd.First, rd.Last), rd.From, rd.To, rd.Options)
	}
	return b.String()
}

func describeRetired(rt *Retired) string {
	if rt == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d %s %s %q", rt.Line, rt.Key, rt.To, rt.Note)
}

// generated writes a synthetic catalog of n fitments, its vehicles vpic or
// the entries of a vocabulary, with every shape a catalog line takes: no
// side and each side, no note, zero to four applications or more than
// wideApplications, open and
// unbounded years and years far from today's, option text shared and
// not, qualifiers of attributes and text, redirects and retired numbers;
// and the files it links to. It returns them, opened.
func generated(t testing.TB, vpic bool, n int) []*File {
	t.Helper()
	rng := rand.New(rand.NewPCG(1, uint64(n)+uint64(b2i(vpic))))
	types := []string{"pads", "rotor", "mirror", "glass", "door", "hood"}
	var vehicles []string
	if vpic {
		vehicles = []string{`{"make":476}`, `{"model":1938}`, `{"model":88888}`, `{"ext":"ext:classics/studebaker"}`, `{"ext":"ext:classics/lark"}`}
	} else {
		vehicles = []string{`{"make":"Dodge","model":"Ram"}`, `{"make":"dodge","model":" RAM "}`, `{"make":"Dodge","model":"Lark"}`, `{"make":"Dodge","model":"Nova"}`,
			`{"make":"Chevrolet","model":"Silverado"}`, `{"model":"Dart","make":"Dodge"}`, `{"make":"Ford","model":"Ranger"}`}
	}
	options := []string{"", "", "4WD", "w/o sunroof", "w/ towing package", "LH", "a \"quoted\" one", "é"}
	qualifiers := []string{`{"15":[2]}`, `{"15":[2,7]}`, `{"34":["1500"]}`, `{"34":["1500","2500"],"15":[3]}`}
	years := func() string {
		var s string
		pick := func() int { return []int{1850, 1879, 1880, 1999, 2005, 2010, 2020, 2199, 2200, 2300}[rng.IntN(10)] }
		first, last := pick(), pick()
		if first > last {
			first, last = last, first
		}
		if rng.IntN(4) > 0 {
			s += fmt.Sprintf(`,"first":%d`, first)
		}
		if rng.IntN(4) > 0 {
			s += fmt.Sprintf(`,"last":%d`, last)
		}
		return s
	}
	header := `{"format":1,"kind":"catalog","name":"gen","version":"1","release":"r1","data_month":"2026_09","spine":"parts","vehicles":"yard"}`
	if vpic {
		header = strings.Replace(header, `"yard"`, `"vpic"`, 1)
	}
	cat := []string{header}
	type key struct {
		typ, number, side string
	}
	keyJSON := func(k key) string {
		s := fmt.Sprintf(`{"type":%q,"number":%q`, k.typ, k.number)
		if k.side != "" {
			s += fmt.Sprintf(`,"side":%q`, k.side)
		}
		return s + "}"
	}
	var live []key
	seen := map[key]bool{}
	for i := 0; len(live) < n; i++ {
		k := key{types[rng.IntN(len(types))], fmt.Sprintf("N-%d", rng.IntN(n)), []string{"", "", "left", "right", "both"}[rng.IntN(5)]}
		if seen[k] {
			continue
		}
		seen[k] = true
		live = append(live, k)
		var apps []string
		n := rng.IntN(5)
		wide := rng.IntN(30) == 0 // more than a block copies whole
		if wide {
			n = wideApplications + 1 + rng.IntN(8)
		}
		for j := range n {
			opt := options[rng.IntN(len(options))]
			if wide || rng.IntN(3) == 0 {
				opt = fmt.Sprintf("%s, own words %d-%d", opt, i, j)
			}
			o, _ := json.Marshal(opt)
			a := fmt.Sprintf(`{"vehicle":%s%s,"options":%s`, vehicles[rng.IntN(len(vehicles))], years(), o)
			if rng.IntN(2) == 0 && !strings.Contains(a, "ext:") {
				a += `,"qualifiers":` + qualifiers[rng.IntN(len(qualifiers))]
			}
			apps = append(apps, a+"}")
		}
		apps = slices.Compact(apps) // a duplicate application is a fault
		line := fmt.Sprintf(`{"fitment":%s`, keyJSON(k))
		if rng.IntN(3) == 0 {
			line += fmt.Sprintf(`,"note":"note %d"`, rng.IntN(4))
		}
		cat = append(cat, line+`,"applications":[`+strings.Join(apps, ",")+"]}")
		if rng.IntN(8) == 0 {
			r := fmt.Sprintf(`{"redirect":{"vehicle":%s%s,"from":%q,"to":%q`, vehicles[rng.IntN(len(vehicles))], years(), types[rng.IntN(len(types))], types[rng.IntN(len(types))])
			if rng.IntN(2) == 0 {
				r += `,"options":"listed elsewhere"`
			}
			cat = append(cat, r+"}}")
		}
		if rng.IntN(20) == 0 {
			to := live[rng.IntN(len(live))]
			old := key{to.typ, fmt.Sprintf("R-%d", i), to.side}
			cat = append(cat, fmt.Sprintf(`{"retired":%s,"to":%s,"note":"old"}`, keyJSON(old), keyJSON(to)))
		}
	}
	spine := []string{spineHeader}
	for _, typ := range types {
		spine = append(spine, fmt.Sprintf(`{"id":%q}`, typ))
	}
	files := []file{{"a.jsonl", spine}, {"c.jsonl", cat}}
	if vpic {
		files = append(files, file{"b.jsonl", lines(setHeader, `{"id":"studebaker","make_name":"Studebaker"}`, `{"id":"lark","make":476,"name":"Lark"}`)})
	} else {
		files = append(files, file{"b.jsonl", lines(vehicleHeader, ram, `{"entry":{"make":"Dodge","model":"Lark"},"mappings":[]}`, `{"entry":{"make":"Dodge","model":"Dart"},"mappings":[{"model":1938}]}`)})
	}
	_, got, err := openFiles(t, files)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestCompiledAnswersAsJSONLines: a compiled catalog answers every
// fitment.Reader question as its JSON Lines source does, on generated
// catalogs of each kind of vehicle: by vehicle for every part type,
// vehicle, year and side; by every number, live, retired and unknown;
// redirects; and every page, after every key and at every limit.
func TestCompiledAnswersAsJSONLines(t *testing.T) {
	for _, vpic := range []bool{false, true} {
		t.Run(fmt.Sprintf("vpic=%v", vpic), func(t *testing.T) {
			files := generated(t, vpic, 300)
			jl := catalogOf(t, month{}, files)
			cc := catalogOf(t, month{}, swapCatalog(t, files, compile(t, month{}, files)))
			if jl.File().compiled != nil || cc.File().compiled == nil {
				t.Fatal("the forms are not the forms")
			}
			if !slices.ContainsFunc(jl.File().Fitments, func(f Fitment) bool { return len(f.Applications) > wideApplications }) {
				t.Fatal("no fitment wider than a block copies whole")
			}
			if got, want := cc.Info(), jl.Info(); got != want {
				t.Errorf("Info %+v; want %+v", got, want)
			}
			key := func(vs []*Vehicle) (out []string) {
				for _, v := range vs {
					out = append(out, v.key)
				}
				return out
			}
			if got, want := key(cc.Vehicles()), key(jl.Vehicles()); !slices.Equal(got, want) {
				t.Errorf("Vehicles %q; want %q", got, want)
			}

			var vehicles [][]Vehicle
			for _, v := range jl.Vehicles() {
				vehicles = append(vehicles, []Vehicle{*v})
			}
			all := make([]Vehicle, 0, len(jl.Vehicles()))
			for _, v := range jl.Vehicles() {
				all = append(all, *v)
			}
			vehicles = append(vehicles, all, append(slices.Clone(all[:2]), all[0]), []Vehicle{{Entry: map[string]string{"make": "Kia", "model": "Rio"}}}, []Vehicle{{Target: Target{Model: 1}}}, nil)
			types := []string{"pads", "rotor", "mirror", "glass", "door", "hood", "wipers", ""}
			years := []*int{nil, new(1850), new(1880), new(2005), new(2020), new(2200), new(2300)}
			sides := []*Side{nil, new(SideNone), new(SideLeft), new(SideRight), new(SideBoth)}
			asked := 0
			for _, typ := range types {
				for _, vs := range vehicles {
					for _, y := range years {
						for _, s := range sides {
							got, want := describeCandidates(cc.ByVehicle(typ, vs, y, s)), describeCandidates(jl.ByVehicle(typ, vs, y, s))
							if got != want {
								t.Fatalf("ByVehicle(%q, %d vehicles, %v, %v):\n%s\nwant\n%s", typ, len(vs), y, s, got, want)
							}
							matched := describeCandidates(jl.Matching(typ, vs, y, s))
							cm := cc.Matching(typ, vs, y, s)
							if got := describeCandidates(cm); got != matched {
								t.Fatalf("Matching(%q, %d vehicles, %v, %v):\n%s\nwant\n%s", typ, len(vs), y, s, got, matched)
							}
							for _, c := range cm {
								if len(c.Applications) != len(c.Fitment.Applications) {
									t.Fatalf("Matching(%q, …): %s holds %d applications, %d matched", typ, c.Fitment.Key, len(c.Fitment.Applications), len(c.Applications))
								}
							}
							asked += strings.Count(want, "matched")
							if typ == "" {
								shared := describeShared(jl, vs, y, s)
								if got := describeShared(cc, vs, y, s); got != shared {
									t.Fatalf("Shared(%d vehicles, %v, %v):\n%s\nwant\n%s", len(vs), y, s, got, shared)
								}
								// Of every fitment ByVehicle has, Matching's
								// candidate, and every other one's every
								// application's span.
								var b strings.Builder
								for i, c := range jl.ByVehicle("", vs, y, s) {
									b.WriteString(describeCandidates(jl.Matching("", vs, y, s)[i : i+1]))
									if i%2 == 1 {
										continue
									}
									for j := range c.Fitment.Applications {
										b.WriteString(describeSpan(sharedSpan(&c.Fitment.Applications[j])))
									}
								}
								if shared != b.String() {
									t.Fatalf("Shared(%d vehicles, %v, %v):\n%s\nwant\n%s", len(vs), y, s, shared, b.String())
								}
							}
						}
						got, want := describeRedirects(cc.Redirect(typ, vs, y)), describeRedirects(jl.Redirect(typ, vs, y))
						if got != want {
							t.Fatalf("Redirect(%q, %d vehicles, %v):\n%s\nwant\n%s", typ, len(vs), y, got, want)
						}
					}
				}
			}
			if asked == 0 {
				t.Fatal("no candidates asked of")
			}

			var keys []FitmentKey
			for _, f := range jl.File().Fitments {
				keys = append(keys, f.Key, FitmentKey{f.Key.Type, f.Key.Number + "x", f.Key.Side}, FitmentKey{f.Key.Type, f.Key.Number, (f.Key.Side + 1) % 4})
			}
			for _, rt := range jl.File().Retired {
				keys = append(keys, rt.Key)
			}
			keys = append(keys, FitmentKey{"wipers", "N-1", SideNone}, FitmentKey{"", "", SideNone})
			for _, k := range keys {
				gf, gr := cc.ByNumber(k.Type, k.Number, k.Side)
				wf, wr := jl.ByNumber(k.Type, k.Number, k.Side)
				if describe(gf) != describe(wf) || describeRetired(gr) != describeRetired(wr) {
					t.Fatalf("ByNumber(%s): %s, %s; want %s, %s", k, describe(gf), describeRetired(gr), describe(wf), describeRetired(wr))
				}
			}
			// RetiredTo, the reverse of ByNumber's retired step: of every
			// key, a retired number's successor among them.
			retiredTo := func(rts []*Retired) string {
				var b strings.Builder
				fmt.Fprintf(&b, "%d:", len(rts))
				for _, rt := range rts {
					b.WriteString(" " + describeRetired(rt))
				}
				return b.String()
			}
			successors := 0
			for _, rt := range jl.File().Retired {
				keys = append(keys, rt.To)
			}
			for _, k := range keys {
				got, want := retiredTo(cc.RetiredTo(k)), retiredTo(jl.RetiredTo(k))
				if got != want {
					t.Fatalf("RetiredTo(%s): %s; want %s", k, got, want)
				}
				if len(jl.RetiredTo(k)) > 0 {
					successors++
				}
			}
			if len(jl.File().Retired) > 0 && successors == 0 {
				t.Fatal("no successor of a retired number asked of")
			}

			page := func(fs []*Fitment) string {
				var b strings.Builder
				fmt.Fprintf(&b, "nil=%v\n", fs == nil)
				for _, f := range fs {
					b.WriteString(describe(f) + "\n")
				}
				return b.String()
			}
			afters := []*FitmentKey{nil}
			for i := 0; i < len(keys); i += 5 {
				afters = append(afters, &keys[i])
			}
			for _, typ := range types {
				for _, after := range afters {
					for _, limit := range []int{0, 1, 3, 1000} {
						if got, want := page(cc.Fitments(typ, after, limit)), page(jl.Fitments(typ, after, limit)); got != want {
							t.Fatalf("Fitments(%q, %v, %d):\n%s\nwant\n%s", typ, after, limit, got, want)
						}
					}
				}
			}
			// Paging through, seven at a time.
			var paged []*Fitment
			var after *FitmentKey
			for {
				fs := cc.Fitments("", after, 7)
				if len(fs) == 0 {
					break
				}
				paged = append(paged, fs...)
				after = &fs[len(fs)-1].Key
			}
			if got, want := page(paged), page(jl.Fitments("", nil, 0)); got != want {
				t.Errorf("paged seven at a time:\n%s\nwant\n%s", got, want)
			}
		})
	}
}

// TestCompileIsDeterministic: compiling the same catalog again, loaded
// again and from another path, gives the same bytes.
func TestCompileIsDeterministic(t *testing.T) {
	files := generated(t, false, 200)
	first, err := os.ReadFile(compile(t, month{}, files))
	if err != nil {
		t.Fatal(err)
	}
	// The same bytes under other names, loaded afresh.
	dir := t.TempDir()
	var paths []string
	for _, f := range files {
		b, err := os.ReadFile(f.Path)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, "other-"+filepath.Base(f.Path))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	again, err := Open(paths...)
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(compile(t, month{}, again))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("two compiles differ: %d and %d bytes", len(first), len(second))
	}
}

// TestCompileAcrossBatches: compiling reads its source a batch at a time,
// and a catalog read in batches of a few lines, lines longer than a batch
// among them, compiles to the bytes of one read whole: every line keeps
// its number.
func TestCompileAcrossBatches(t *testing.T) {
	files := generated(t, true, 300)
	whole, err := os.ReadFile(compile(t, month{}, files))
	if err != nil {
		t.Fatal(err)
	}
	defer func(n int) { sourceBatch = n }(sourceBatch)
	for _, n := range []int{1, 300, 1 << 10} {
		sourceBatch = n
		batched, err := os.ReadFile(compile(t, month{}, files))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(batched, whole) {
			t.Errorf("read %d bytes at a time: %d bytes, not the %d of one read", n, len(batched), len(whole))
		}
	}
}

// TestCompileAcrossRuns: compiling sorts in runs of runBytes, merged a
// cursorBytes read at a time, and a catalog sorted in runs of a few
// records, merged a few bytes at a time (every record longer than a read),
// compiles to the bytes of one run, so it answers as one does
// (TestCompiledAnswersAsJSONLines): its fitments, redirects and retired
// numbers merge from many runs in their one order.
func TestCompileAcrossRuns(t *testing.T) {
	for _, vpic := range []bool{false, true} {
		files := generated(t, vpic, 300)
		whole, err := os.ReadFile(compile(t, month{}, files))
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func(r, c int) { runBytes, cursorBytes = r, c }(runBytes, cursorBytes)
			runBytes, cursorBytes = 256, 16
			runs, err := os.ReadFile(compile(t, month{}, files))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(runs, whole) {
				t.Errorf("vpic=%v: sorted in tiny runs, %d bytes, not the %d of one run", vpic, len(runs), len(whole))
			}
		}()
	}
}

// TestCompiledHeader: a compiled catalog records its source's hash, the
// files it was linked against, its writer, its drift and the drift kinds
// its month could not measure, and is identified by its own bytes' hash.
func TestCompiledHeader(t *testing.T) {
	files, err := Open("../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	path := compile(t, fixtureMonth{}, files)
	cc := catalogOf(t, fixtureMonth{}, swapCatalog(t, files, path)).File()
	byName := map[string]*File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	m := cc.Compiled()
	want := []LinkedFile{{"part-types", "2026.11", byName["part-types"].Hash}, {"catalog-models", "2026.11", byName["catalog-models"].Hash}}
	skipped := []DriftKind{DriftModelYears, DriftModelQualifier} // fixtureMonth knows no model's years or values
	if m == nil || m.Format != CompiledFormat || m.SourceHash != byName["parts-catalog"].Hash || !slices.Equal(m.Links, want) || m.Writer != "v0.0.0-test" || m.DriftMonth != "2026_10" || m.Drift != nil || !slices.Equal(m.Skipped, skipped) {
		t.Errorf("Compiled %+v", m)
	}
	// A month that knows them skips none.
	full := compile(t, fullMonth{}, files)
	if m := catalogOf(t, fullMonth{}, swapCatalog(t, files, full)).File().Compiled(); m.Skipped != nil {
		t.Errorf("skipped %v beside a month of model years and values; want none", m.Skipped)
	}
	if cc.Kind != KindCatalog || cc.Name != "parts-catalog" || cc.Release != "7.02" || cc.Spine != "part-types" || cc.Path != path {
		t.Errorf("header %+v", cc)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Hash != [32]byte(b[32:64]) || cc.Hash == m.SourceHash {
		t.Errorf("the file's hash %x", cc.Hash)
	}
	if byName["parts-catalog"].Compiled() != nil {
		t.Error("a JSON Lines catalog is Compiled")
	}
}

// fullMonth is a month of model years and model values, as a server's.
type fullMonth struct{ modelMonth }

func (fullMonth) ModelYears(int32) []YearRange { return nil }

// TestCompiledRefused: --vocab refuses a compiled catalog in a format it
// cannot read, format 1 included, saying to compile it again; one cut
// short or damaged; and one without the spine or vehicle vocabulary it
// links to. Expand refuses one whose bytes do not hash to its header's
// blake3. Compile refuses a catalog with a fault, a compiled one, and a
// source that is no catalog.
func TestCompiledRefused(t *testing.T) {
	files, err := Open("../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	path := compile(t, fixtureMonth{}, files)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	write := func(b []byte) string {
		p := filepath.Join(t.TempDir(), "x.compiled")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	version, format1 := slices.Clone(good), slices.Clone(good)
	binary.LittleEndian.PutUint32(version[8:], CompiledFormat+1)
	binary.LittleEndian.PutUint32(format1[8:], 1)
	for _, tc := range []struct {
		name   string
		paths  []string
		detail string
	}{
		{"a format it cannot read", []string{write(version)}, "compiled catalog format 4: this nitrovin reads format 3 only; compile the catalog again with this release"},
		{"format 1", []string{write(format1)}, "compiled catalog format 1: this nitrovin reads format 3 only; compile the catalog again with this release"},
		{"cut short", []string{write(good[:len(good)-1])}, "it was cut short or added to"},
		{"shorter than a header", []string{write(good[:20])}, "shorter than its header"},
		{"no spine", []string{byName["catalog-models"].Path, path}, `/spine: spine "part-types" is not loaded`},
		{"no vehicle vocabulary", []string{byName["part-types"].Path, path}, `/vehicles: vocabulary "catalog-models" is not loaded`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Open(tc.paths...)
			fs, ok := err.(Faults)
			if !ok || !strings.Contains(fs.Error(), tc.detail) || fs[0].Line != 0 {
				t.Errorf("Open: %v; want a fault about the file saying %q", err, tc.detail)
			}
		})
	}

	// Another spine, which lacks a part type it was compiled with.
	spine := write([]byte(`{"format":1,"kind":"spine","name":"part-types","version":"other"}` + "\n" + `{"id":"mirror"}` + "\n"))
	_, err = Open(spine, byName["catalog-models"].Path, path)
	if fs, ok := err.(Faults); !ok || !strings.Contains(fs.Error(), `part type "brake-rotor-front": spine "part-types" has no id "brake-rotor-front"`) {
		t.Errorf("Open beside another spine: %v", err)
	}

	damaged := slices.Clone(good)
	damaged[len(damaged)-1] ^= 1 // a byte of the last vehicle's block
	opened, err := Open(byName["part-types"].Path, byName["catalog-models"].Path, write(damaged))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opened[2].Expand(); err == nil || !strings.Contains(err.Error(), "the file is damaged") {
		t.Errorf("Expand of a damaged file: %v", err)
	}

	source := byName["parts-catalog"].Path
	if _, err := compileSource(t, &bytes.Buffer{}, fixtureMonth{}, []*File{byName["part-types"]}, source); err == nil || !strings.Contains(err.Error(), `/vehicles: vocabulary "catalog-models" is not loaded`) {
		t.Errorf("Compile without the vehicle vocabulary: %v", err)
	}
	if _, err := compileSource(t, &bytes.Buffer{}, fixtureMonth{}, opened, source); err == nil || !strings.Contains(err.Error(), "compiled already") {
		t.Errorf("Compile beside a compiled catalog: %v", err)
	}
	if _, err := compileSource(t, &bytes.Buffer{}, fixtureMonth{}, []*File{byName["catalog-models"]}, byName["part-types"].Path); err == nil || !strings.Contains(err.Error(), "is a spine, not a catalog") {
		t.Errorf("Compile of a spine: %v", err)
	}
	if _, err := Compile(&bytes.Buffer{}, fixtureMonth{}, nil, strings.NewReader("\n\n"), "empty.jsonl", DirScratch(t.TempDir()), CompileOptions{}); err == nil || !strings.Contains(err.Error(), "no header") {
		t.Errorf("Compile of an empty file: %v", err)
	}

	// OpenCompiled, as a browser opens a picked file: one with no
	// compiled catalog's magic is no compiled catalog.
	for _, name := range []string{source, byName["part-types"].Path} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := OpenCompiled(name, bytes.NewReader(b), int64(len(b))); err == nil || !strings.Contains(err.Error(), "it is not a compiled catalog") {
			t.Errorf("OpenCompiled of %s: %v; want it is not a compiled catalog", filepath.Base(name), err)
		}
	}

	// OpenCompile, as `vocab compile` reads its arguments: the catalog's
	// path, never read, and the rest opened.
	others, path, err := OpenCompile("../../testdata/vocab/fitment")
	if err != nil || path != source || len(others) != len(files)-1 || slices.ContainsFunc(others, func(f *File) bool { return f.Kind == KindCatalog }) {
		t.Errorf("OpenCompile: %d files, %q, %v; want the rest and %q", len(others), path, err, source)
	}
	for _, tc := range []struct {
		name   string
		paths  []string
		detail string
	}{
		{"no catalog", []string{byName["part-types"].Path}, "no catalog among the files to compile"},
		{"two catalogs", []string{source, source}, "is a catalog too: compile one at a time"},
		{"a compiled catalog", []string{byName["part-types"].Path, byName["catalog-models"].Path, write(good), source}, "compiled already"},
	} {
		if _, _, err := OpenCompile(tc.paths...); err == nil || !strings.Contains(err.Error(), tc.detail) {
			t.Errorf("OpenCompile of %s: %v; want %q", tc.name, err, tc.detail)
		}
	}
}

// TestCompiledDrift: loading a compiled catalog reports the drift its
// compiling measured, about the compiled file, whatever month it loads
// beside; Expand reads it in full, and checks and measures it again, as
// its source.
func TestCompiledDrift(t *testing.T) {
	files := generated(t, false, 300)
	path := compile(t, month{}, files)
	compiled := swapCatalog(t, files, path)
	var src *File
	for _, f := range files {
		if f.Kind == KindCatalog {
			src = f
		}
	}
	text := func(fs []Finding) (out []string) {
		for _, f := range fs {
			out = append(out, fmt.Sprintf("%s:%d %s %s", filepath.Base(f.File), f.Line, f.Drift, f.Detail))
		}
		return out
	}
	want := text(Drift(month{}, files))
	if !slices.ContainsFunc(want, func(s string) bool { return strings.HasPrefix(s, "c.jsonl") }) {
		t.Fatalf("the generated catalog does not drift: %q", want)
	}
	for i, s := range want {
		want[i] = strings.Replace(s, "c.jsonl:", "catalog.compiled:", 1)
	}
	// Another month, with the stored findings.
	if got := text(Drift(otherMonth{}, compiled)); !slices.Equal(got, want) {
		t.Errorf("stored drift\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	var expanded []*File
	for _, f := range compiled {
		e, err := f.Expand()
		if err != nil {
			t.Fatal(err)
		}
		expanded = append(expanded, e)
	}
	if err := Link(expanded); err != nil {
		t.Fatal(err)
	}
	e := expanded[len(expanded)-1]
	if e.compiled != nil || len(e.Fitments) != len(src.Fitments) || len(e.Redirects) != len(src.Redirects) || len(e.Retired) != len(src.Retired) {
		t.Fatalf("expanded: %d fitments, %d redirects, %d retired", len(e.Fitments), len(e.Redirects), len(e.Retired))
	}
	for i := range src.Fitments {
		if got, want := describe(&e.Fitments[i]), describe(&src.Fitments[i]); got != want {
			t.Fatalf("expanded fitment %d:\n%s\nwant\n%s", i, got, want)
		}
	}
	for i := range src.Retired {
		if got, want := describeRetired(&e.Retired[i]), describeRetired(&src.Retired[i]); got != want {
			t.Fatalf("expanded retired %d: %s; want %s", i, got, want)
		}
	}
	if got, want := describeRedirects(redirectPtrs(e)), describeRedirects(redirectPtrs(src)); got != want {
		t.Fatalf("expanded redirects:\n%s\nwant\n%s", got, want)
	}
	// Measured again, against another month.
	again := text(Drift(otherMonth{}, files))
	for i, s := range again {
		again[i] = strings.Replace(s, "c.jsonl:", "catalog.compiled:", 1)
	}
	if got := text(Drift(otherMonth{}, expanded)); !slices.Equal(got, again) || slices.Equal(again, want) {
		t.Errorf("drift measured again\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(again, "\n"))
	}
}

func redirectPtrs(f *File) []*Redirect {
	out := []*Redirect{}
	for i := range f.Redirects {
		out = append(out, &f.Redirects[i])
	}
	return out
}

// otherMonth is month with nothing of element 34's text: a month after.
type otherMonth struct{ month }

func (otherMonth) Name() string                      { return "2026_11" }
func (otherMonth) Text(element int32, _ string) bool { return false }

// TestCompiledDamagedCount: a fitment record whose application count is
// damaged ends in the reader's own panic, about a damaged file, before
// anything is allocated by the count.
func TestCompiledDamagedCount(t *testing.T) {
	files, err := Open("../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(compile(t, fixtureMonth{}, files))
	if err != nil {
		t.Fatal(err)
	}
	// The first fitment of keyed, after any retired numbers: its kind,
	// part type, side, number, line and note, then its count of
	// applications, written over as 2^63-1, the bytes after it too, so the
	// file keeps its length.
	keyed := int(binary.LittleEndian.Uint64(b[headerSize+secKeyed*16:]))
	r := &bcursor{b: b[keyed:]}
	for r.byte() == recRetired {
		r.uint()
		r.byte()
		r.bytes()
		r.uint()
		r.bytes()
		r.uint()
		r.byte()
		r.bytes()
	}
	r.uint()
	r.byte()
	r.bytes()
	r.uint()
	r.bytes()
	copy(b[len(b)-len(r.b):], binary.AppendUvarint(nil, math.MaxInt64))
	f, err := Read("damaged.compiled", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err) // Open reads no record
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	func() {
		defer func() {
			e, ok := recover().(error)
			if !ok || !strings.Contains(e.Error(), "the file is damaged") {
				t.Errorf("Fitments of a damaged count: recovered %v; want the file is damaged", e)
			}
		}()
		f.Catalog().Fitments("", nil, 1)
	}()
	runtime.ReadMemStats(&after)
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Errorf("Fitments of a damaged count allocated %d bytes", n)
	}
}

// TestCompiledFarRecordOutOfRange: a far record whose place in keyed or
// in wide runs past it, its length wrapping round, ends in the reader's
// own panic about a damaged file, not a runtime error.
func TestCompiledFarRecordOutOfRange(t *testing.T) {
	c := &compiled{path: "damaged.compiled", vehicles: []*Vehicle{{}}}
	c.sec[secKeyed].n, c.sec[secWide].n = 20, 20
	var places [][4]uint64
	for _, bad := range [][2]uint64{{10, math.MaxInt64 - 5}, {10, 11}, {21, 1}, {0, 0}, {math.MaxUint64, 1}} {
		places = append(places, [4]uint64{bad[0], bad[1], 0, 5}, [4]uint64{0, 5, bad[0], bad[1]})
	}
	for _, place := range places {
		b := []byte{recFar, byte(SideNone)}
		b = appendStr(b, "N-1")
		b = binary.AppendUvarint(b, 1) // its line
		b = appendStr(b, "")           // its note
		b = binary.AppendUvarint(b, 1) // one application of its own
		b = binary.AppendUvarint(b, 0)
		b = appendApplication(b, &Application{}, 0)
		for _, n := range place {
			b = binary.AppendUvarint(b, n)
		}
		func() {
			defer func() {
				r := recover()
				if _, ok := r.(runtime.Error); ok {
					t.Fatalf("far record at %v: runtime error %v", place, r)
				}
				if e, ok := r.(error); !ok || !strings.Contains(e.Error(), "the file is damaged") {
					t.Errorf("far record at %v: recovered %v; want the file is damaged", place, r)
				}
			}()
			c.blockHit(&cursor{c, string(b), 0}, 0, 0, nil, nil)
		}()
	}
}

// TestReadFarSpan: far fitments read together are read in runs of at most
// 1 MiB, however close each lies to the next, so a related vehicles
// request's bytes stay within its reads × 1 MiB (ADR 0023).
func TestReadFarSpan(t *testing.T) {
	const step, n = 200 << 10, 7 // records 200 KiB apart: within farGap, 1.2 MiB in all
	wide := make([]byte, step*(n-1)+64)
	var hits []*hit
	for i := range n {
		rec := append([]byte{recFitment}, 0, byte(SideNone))
		rec = appendStr(rec, fmt.Sprintf("N-%d", i))
		copy(wide[i*step:], rec)
		hits = append(hits, &hit{typ: 0, side: SideNone, number: fmt.Sprintf("N-%d", i), far: true, off: int64(i * step), n: int64(len(rec))})
	}
	r := &spanReader{r: bytes.NewReader(wide)}
	c := &compiled{r: r, path: "far.compiled", types: []compiledType{{name: "pads"}}}
	c.sec[secKeyed].n = int64(len(wide))
	c.readFar(hits)
	if r.reads != 2 || r.most > 1<<20 {
		t.Errorf("%d far fitments %d KiB apart: %d reads, the longest %d bytes; want 2, none over 1 MiB", n, step>>10, r.reads, r.most)
	}
	for i, h := range hits {
		if r := h.rec; r.s == "" || r.side() != h.side || r.str() != h.number {
			t.Errorf("far fitment %d read wrongly", i)
		}
	}
}

// spanReader counts its reads and their longest.
type spanReader struct {
	r           io.ReaderAt
	reads, most int
}

func (s *spanReader) ReadAt(p []byte, off int64) (int, error) {
	s.reads, s.most = s.reads+1, max(s.most, len(p))
	return s.r.ReadAt(p, off)
}

// FuzzOpenCompiled: whatever bytes begin like a compiled catalog, opening
// them refuses them with Faults or opens them, and every lookup on what
// opens answers or panics with the reader's own error about a damaged
// file, never a runtime error or an allocation no memory holds.
func FuzzOpenCompiled(f *testing.F) {
	files, err := Open("../../testdata/vocab/fitment")
	if err != nil {
		f.Fatal(err)
	}
	for _, fs := range [][]*File{files, generated(f, true, 40), generated(f, false, 40)} {
		b, err := os.ReadFile(compile(f, month{}, fs))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		file, err := Read("fuzz.compiled", bytes.NewReader(b))
		if err != nil || file.compiled == nil {
			return
		}
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			if _, ok := r.(runtime.Error); ok {
				t.Fatalf("runtime error: %v", r)
			}
			if e, ok := r.(error); !ok || !strings.Contains(e.Error(), "the file is damaged") {
				t.Fatalf("panic %v; want only the reader's own, about a damaged file", r)
			}
		}()
		c := file.Catalog()
		c.Info()
		var vehicles []Vehicle
		for _, v := range c.Vehicles() {
			vehicles = append(vehicles, *v)
		}
		c.ByVehicle("", vehicles, nil, nil)
		c.ByVehicle("", vehicles[:min(1, len(vehicles))], new(2005), new(SideLeft))
		c.Matching("", vehicles, nil, nil)
		all := func(*Candidate) bool { return true }
		c.Shared(vehicles, nil, nil, all).Spans(func(int, []SharedSpan) {})
		c.Shared(vehicles[:min(1, len(vehicles))], new(2005), new(SideLeft), all).Spans(func(int, []SharedSpan) {})
		c.Redirect("", vehicles, nil)
		for _, typ := range file.compiled.types {
			c.ByVehicle(typ.name, vehicles, nil, nil)
			c.ByVehicle(typ.name, vehicles[:min(1, len(vehicles))], new(2005), new(SideLeft))
			c.Redirect(typ.name, vehicles, nil)
			for _, fit := range c.Fitments(typ.name, nil, 3) {
				c.ByNumber(fit.Key.Type, fit.Key.Number, fit.Key.Side)
				c.RetiredTo(fit.Key)
			}
			c.ByNumber(typ.name, "N-1", SideNone)
		}
		c.Fitments("", &FitmentKey{Type: "m"}, 2)
		file.Expand()
	})
}
