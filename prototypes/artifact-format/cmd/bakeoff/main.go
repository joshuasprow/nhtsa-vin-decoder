// Command bakeoff is PROTOTYPE tooling for the artifact format bake-off. Throwaway.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin/prototypes/artifact-format/bake"
	"github.com/joshuasprow/nitrovin/prototypes/artifact-format/model"
)

var start = time.Now()

var loadAllFlag *bool

// The engine's clock is an input (port plan §7); fixed so runs compare.
var now = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bakeoff extract|build|genvins|bench|decode|dump|cold ...")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	tsv := fs.String("tsv", "work/tsv", "extracted tables")
	wyvc := fs.Bool("wyvc", false, "include wmiyearvalidchars")
	format := fs.String("format", "custom", "custom|sqlite|fb")
	out := fs.String("out", "", "output path")
	artifact := fs.String("artifact", "", "artifact path")
	zst := fs.Bool("zstd", false, "artifact is zstd-compressed")
	vinsPath := fs.String("vins", "work/vins.txt", "one VIN per line")
	n := fs.Int("n", 5000, "count")
	dur := fs.Duration("dur", 3*time.Second, "bench duration")
	fs.BoolVar(&bake.SQLitePushdown, "pushdown", false, "sqlite: LIKE match and element filter in SQL")
	loadAllFlag = fs.Bool("loadall", false, "sqlite: also time reading every table into Go slices")
	fs.Parse(args)
	var err error
	switch cmd {
	case "extract":
		t := time.Now()
		err = model.Extract("nhtsa-oracle", *tsv, *wyvc)
		fmt.Printf("extract_s=%.2f\n", time.Since(t).Seconds())
	case "build":
		err = build(*tsv, *wyvc, *format, *out)
	case "genvins":
		var d *model.Data
		if d, err = model.LoadTSV(*tsv, false); err == nil {
			err = os.WriteFile(*out, []byte(strings.Join(bake.GenVINs(d, *n, 17), "\n")+"\n"), 0o644)
		}
	case "bench":
		err = bench(*format, *artifact, *zst, *vinsPath, *dur)
	case "decode":
		err = decode(*format, *artifact, *zst, fs.Args())
	case "dump":
		err = dump(*artifact, fs.Args())
	case "cold":
		err = cold(*n, fs.Args())
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bakeoff:", err)
		os.Exit(1)
	}
}

func build(tsv string, wyvc bool, format, out string) error {
	t0 := time.Now()
	d, err := model.LoadTSV(tsv, wyvc)
	if err != nil {
		return err
	}
	load := time.Since(t0)
	t1 := time.Now()
	switch format {
	case "custom":
		err = os.WriteFile(out, bake.BuildCustom(d), 0o644)
	case "fb":
		err = os.WriteFile(out, bake.BuildFB(d), 0o644)
	case "sqlite":
		err = bake.BuildSQLite(d, out)
	default:
		err = fmt.Errorf("unknown format %q", format)
	}
	if err != nil {
		return err
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	fmt.Printf("format=%s load_tsv_s=%.2f build_s=%.2f bytes=%d sha256=%x\n", format, load.Seconds(), time.Since(t1).Seconds(), len(b), sha256.Sum256(b))
	return nil
}

func readVINs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var vins []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(strings.SplitN(sc.Text(), "\t", 2)[0])
		if l != "" && !strings.HasPrefix(l, "#") {
			vins = append(vins, l)
		}
	}
	return vins, sc.Err()
}

func bench(format, path string, zst bool, vinsPath string, dur time.Duration) error {
	runtime.GOMAXPROCS(1)
	vins, err := readVINs(vinsPath)
	if err != nil {
		return err
	}
	t0 := time.Now()
	s, err := bake.OpenFile(format, path, zst)
	if err != nil {
		return err
	}
	open := time.Since(t0)
	var verify time.Duration
	if c, ok := s.(*bake.Custom); ok {
		t := time.Now()
		if err := c.Verify(); err != nil {
			return err
		}
		verify = time.Since(t)
	}
	if sq, ok := s.(*bake.SQLite); ok && *loadAllFlag {
		t := time.Now()
		rows, err := sq.LoadAll()
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "sqlite_loadall_ms=%.1f rows=%d\n", ms(time.Since(t)), rows)
	}
	d := bake.NewDecoder(s)
	nowUS := now.UnixMicro()
	t1 := time.Now()
	d.Decode(vins[0], nowUS, 2026)
	first := time.Since(t1)

	// One checksum pass, also the warm-up.
	var sum uint64
	matched := 0
	for _, v := range vins {
		items := d.Decode(v, nowUS, 2026)
		if len(items) > 0 {
			matched++
		}
		sum = bake.Checksum(sum, v, items)
	}
	d.Scanned = 0
	lat := make([]time.Duration, 0, 1<<20)
	var ms0, ms1 runtime.MemStats
	runtime.ReadMemStats(&ms0)
	t2 := time.Now()
	decodes := 0
	for time.Since(t2) < dur {
		for _, v := range vins {
			a := time.Now()
			d.Decode(v, nowUS, 2026)
			lat = append(lat, time.Since(a))
		}
		decodes += len(vins)
	}
	elapsed := time.Since(t2)
	runtime.ReadMemStats(&ms1)
	slices.Sort(lat)
	q := func(p float64) float64 { return float64(lat[int(p*float64(len(lat)-1))].Nanoseconds()) / 1e3 }
	res := map[string]any{
		"format": format, "zstd": zst, "vins": len(vins), "matched": matched,
		"open_ms": ms(open), "verify_ms": ms(verify), "first_decode_us": float64(first.Nanoseconds()) / 1e3,
		"vin_per_s": float64(decodes) / elapsed.Seconds(),
		"p50_us":    q(0.5), "p99_us": q(0.99), "p999_us": q(0.999), "max_us": q(1),
		"allocs_per_decode":  float64(ms1.Mallocs-ms0.Mallocs) / float64(decodes),
		"scanned_per_decode": float64(d.Scanned) / float64(decodes),
		"checksum":           fmt.Sprintf("%016x", sum),
		"heap_mb":            float64(ms1.HeapAlloc) / 1e6,
	}
	j, _ := json.Marshal(res)
	fmt.Println(string(j))
	return s.Close()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1e3 }

// decode is the CLI shape: open, decode the given VINs, print, exit.
func decode(format, path string, zst bool, vins []string) error {
	t0 := time.Now()
	s, err := bake.OpenFile(format, path, zst)
	if err != nil {
		return err
	}
	open := time.Since(t0)
	d := bake.NewDecoder(s)
	for _, v := range vins {
		t := time.Now()
		items := d.Decode(v, now.UnixMicro(), 2026)
		took := time.Since(t)
		vals := map[int32]string{}
		for _, it := range items {
			vals[it.ElementID] = it.Value
		}
		j, _ := json.Marshal(map[string]any{"vin": v, "decode_us": took.Microseconds(), "elements": vals})
		fmt.Println(string(j))
	}
	fmt.Fprintf(os.Stderr, "open_ms=%.2f since_main_ms=%.2f\n", ms(open), ms(time.Since(start)))
	return s.Close()
}

func dump(path string, args []string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	c, err := bake.OpenCustom(b)
	if err != nil {
		return err
	}
	table, col, val := "", "", ""
	if len(args) > 0 {
		table = args[0]
	}
	if len(args) > 1 {
		col, val, _ = strings.Cut(args[1], "=")
	}
	fmt.Print(c.Dump(table, col, val, 50))
	return nil
}

// cold runs a command n times and reports the median and min wall time: a
// fresh process each time (page cache warm).
func cold(n int, argv []string) error {
	var ts []time.Duration
	for range n {
		cmd := exec.Command(argv[0], argv[1:]...)
		t := time.Now()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		ts = append(ts, time.Since(t))
	}
	slices.Sort(ts)
	fmt.Printf("cold_median_ms=%.1f cold_min_ms=%.1f n=%d\n", ms(ts[len(ts)/2]), ms(ts[0]), n)
	return nil
}
