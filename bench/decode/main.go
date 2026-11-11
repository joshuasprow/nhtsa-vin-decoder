//go:build unix

// Command decode is the README's benchmark harness (docs/benchmarks.md):
// nitrovin's own throughput rows, and the tools scripts/bench.sh runs the
// other engines' rows with. It is not shipped.
//
//	go run ./bench/decode throughput [-workers N] [-now T] [-warm N] [-seconds S] [-cpuprofile F] CORPUS
//	go run ./bench/decode refused [-workers N] [-now T] CORPUS
//	go run ./bench/decode values [-now T] VINS
//	go run ./bench/decode agree KEY.csv VALUES.jsonl
//	go run ./bench/decode time [-in FILE] [-n N] -- COMMAND [ARG]...
//
// throughput decodes CORPUS, one VIN per line, in process through the Go
// module: nitrovin.Embedded, then Decoder.Decode with shape vpic and a
// frozen now, on -workers goroutines under GOMAXPROCS=-workers. It decodes
// the first -warm VINs untimed, then times complete passes over the whole
// corpus until at least -seconds have elapsed, and prints one JSON object.
// The method is ultravin's (scripts/bench/ and docs/BENCHMARKS.md of
// github.com/blackthorn-interstellar/ultravin, MIT): a frozen clock, a
// warm-up, complete passes, total rows over elapsed time. ultravin warms on
// the whole corpus to fill its per-thread caches; nitrovin keeps none, so a
// prefix warms it as well. A decode that fails, which is always a nitrovin
// bug, is counted in "errors" and the pass goes on.
//
// refused prints the VINs of CORPUS whose decode fails, one per line:
// `nitrovin decode -` stops at the first, so its row decodes without them.
//
// values decodes each line of VINS (full) and prints one JSON line per VIN:
// {"vin", "values": [[element id, value], ...]}, NULL as "". agree compares
// such a file with an oracle answer key (scripts/oracle.sh decode), line n
// with input row n: a VIN agrees when every element either side emits has
// the same values, as a multiset, NULL and "" alike. It prints the share
// that agree.
//
// time runs COMMAND -n times, stdin from -in, stdout discarded, and prints
// one JSON line per run: wall and CPU seconds, peak resident memory and the
// lines COMMAND wrote.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

const usage = `usage:
  decode throughput [-workers N] [-now T] [-warm N] [-seconds S] [-cpuprofile F] CORPUS
  decode refused [-workers N] [-now T] CORPUS
  decode values [-now T] VINS
  decode agree KEY.csv VALUES.jsonl
  decode time [-in FILE] [-n N] -- COMMAND [ARG]...`

// defaultNow is the frozen decode clock, ultravin's ULTRAVIN_NOW_MICROS
// 1788220800000000.
const defaultNow = "2026-09-01T00:00:00Z"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "bench/decode:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cmd, args := args[0], args[1:]
	fs := flag.NewFlagSet("decode "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch cmd {
	case "throughput":
		workers := fs.Int("workers", 1, "decoding goroutines, and GOMAXPROCS")
		now := fs.String("now", defaultNow, "frozen decode clock, RFC 3339")
		warm := fs.Int("warm", 1_000_000, "VINs decoded untimed first, from the corpus's start (0: none)")
		seconds := fs.Float64("seconds", 10, "time complete passes until at least this many seconds")
		profile := fs.String("cpuprofile", "", "write a CPU profile of the timed passes to `file`")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 || *workers < 1 || *warm < 0 || *seconds <= 0 {
			return errors.New(usage)
		}
		at, err := contract.ParseNow(*now)
		if err != nil {
			return err
		}
		return throughput(stdout, fs.Arg(0), *workers, at, *warm, *seconds, *profile)
	case "refused":
		workers := fs.Int("workers", runtime.NumCPU(), "decoding goroutines")
		now := fs.String("now", defaultNow, "frozen decode clock, RFC 3339")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 || *workers < 1 {
			return errors.New(usage)
		}
		at, err := contract.ParseNow(*now)
		if err != nil {
			return err
		}
		vins, err := readVINs(fs.Arg(0))
		if err != nil {
			return err
		}
		d, err := nitrovin.Embedded()
		if err != nil {
			return err
		}
		failed := pass(d, vins, *workers, at)
		slices.Sort(failed.vins)
		w := bufio.NewWriter(stdout)
		for _, vin := range failed.vins {
			fmt.Fprintln(w, vin)
		}
		return w.Flush()
	case "values":
		now := fs.String("now", defaultNow, "frozen decode clock, RFC 3339")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New(usage)
		}
		at, err := contract.ParseNow(*now)
		if err != nil {
			return err
		}
		return values(stdout, fs.Arg(0), at)
	case "agree":
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() != 2 {
			return errors.New(usage)
		}
		return agree(stdout, fs.Arg(0), fs.Arg(1))
	case "time":
		in := fs.String("in", "", "stdin of each run (default: none)")
		n := fs.Int("n", 1, "runs")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 || *n < 1 {
			return errors.New(usage)
		}
		return timeRuns(stdout, stderr, *in, *n, fs.Args())
	}
	return errors.New(usage)
}

// readVINs reads one VIN per line, skipping empty lines.
func readVINs(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	vins := make([]string, 0, bytes.Count(b, []byte{'\n'})+1)
	for line := range bytes.Lines(b) {
		if line = bytes.TrimRight(line, "\r\n"); len(line) > 0 {
			vins = append(vins, string(line))
		}
	}
	if len(vins) == 0 {
		return nil, fmt.Errorf("%s: no VINs", path)
	}
	return vins, nil
}

func throughput(stdout io.Writer, path string, workers int, now time.Time, warm int, seconds float64, profile string) error {
	runtime.GOMAXPROCS(workers)
	vins, err := readVINs(path)
	if err != nil {
		return err
	}
	loadStarted := time.Now()
	d, err := nitrovin.Embedded()
	if err != nil {
		return err
	}
	load := time.Since(loadStarted)

	warm = min(warm, len(vins))
	pass(d, vins[:warm], workers, now)
	if profile != "" {
		f, err := os.Create(profile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
	}
	before := rusage()
	started := time.Now()
	passes := 0
	var failed passErrors
	for time.Since(started).Seconds() < seconds {
		failed = pass(d, vins, workers, now)
		passes++
	}
	elapsed := time.Since(started).Seconds()
	after := rusage()
	if profile != "" {
		pprof.StopCPUProfile()
	}
	rows := passes * len(vins)
	info := d.Info()
	return json.NewEncoder(stdout).Encode(map[string]any{
		"engine":             "nitrovin",
		"surface":            "go",
		"workers":            workers,
		"corpus":             path,
		"corpus_vins":        len(vins),
		"warm_vins":          warm,
		"passes":             passes,
		"rows":               rows,
		"errors":             failed.n,
		"first_errors":       failed.first,
		"seconds":            elapsed,
		"vins_per_second":    float64(rows) / elapsed,
		"load_seconds":       load.Seconds(),
		"user_cpu_seconds":   after.user - before.user,
		"system_cpu_seconds": after.system - before.system,
		"max_rss_bytes":      after.maxRSS,
		"now":                now.Format(time.RFC3339),
		"go":                 runtime.Version(),
		"decoder_version":    info.DecoderVersion,
		"data_month":         info.DataMonth,
	})
}

// chunk is how many VINs a worker takes at a time.
const chunk = 256

// passErrors counts the decodes of one pass that ended in an error, a
// *contract.Problem, and keeps the first few.
type passErrors struct {
	n     int
	first []string // the first few errors
	vins  []string // every VIN that failed
}

// pass decodes every VIN once on workers goroutines, discarding the decodes.
// A decode that fails is counted and the pass goes on: the error is
// nitrovin's (a VIN is never an error), and the rows still measure it.
func pass(d *nitrovin.Decoder, vins []string, workers int, now time.Time) passErrors {
	var next atomic.Int64
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed passErrors
	for range workers {
		wg.Go(func() {
			for {
				lo := int(next.Add(chunk)) - chunk
				if lo >= len(vins) {
					return
				}
				for _, vin := range vins[lo:min(lo+chunk, len(vins))] {
					if _, err := d.Decode(contract.Request{VIN: vin, Now: now}); err != nil {
						mu.Lock()
						failed.n++
						failed.vins = append(failed.vins, vin)
						if len(failed.first) < 5 {
							failed.first = append(failed.first, vin+": "+err.Error())
						}
						mu.Unlock()
					}
				}
			}
		})
	}
	wg.Wait()
	return failed
}

// cpuUsage is CPU time and peak resident memory.
type cpuUsage struct {
	user, system float64
	maxRSS       int64
}

// rusage is this process's CPU time and peak resident memory so far.
func rusage() cpuUsage {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return fromRusage(&ru)
}

func fromRusage(ru *syscall.Rusage) cpuUsage {
	rss := int64(ru.Maxrss)
	if runtime.GOOS != "darwin" {
		rss *= 1024 // kilobytes everywhere but macOS
	}
	return cpuUsage{
		user:   time.Duration(ru.Utime.Nano()).Seconds(),
		system: time.Duration(ru.Stime.Nano()).Seconds(),
		maxRSS: rss,
	}
}

// valuesLine is one line of a values file.
type valuesLine struct {
	VIN    string      `json:"vin"`
	Values [][2]string `json:"values"`
	// Error is why the decode failed; such a VIN never agrees.
	Error string `json:"error,omitempty"`
}

func values(stdout io.Writer, path string, now time.Time) error {
	vins, err := readVINs(path)
	if err != nil {
		return err
	}
	d, err := nitrovin.Embedded()
	if err != nil {
		return err
	}
	w := bufio.NewWriter(stdout)
	enc := json.NewEncoder(w)
	for _, vin := range vins {
		dec, err := d.Decode(contract.Request{VIN: vin, Full: true, Now: now})
		if err != nil {
			if err := enc.Encode(valuesLine{VIN: vin, Values: [][2]string{}, Error: err.Error()}); err != nil {
				return err
			}
			continue
		}
		line := valuesLine{VIN: vin, Values: make([][2]string, len(dec.Items))}
		for i, it := range dec.Items {
			line.Values[i] = [2]string{strconv.Itoa(int(it.ElementID)), it.Value.V}
		}
		if err := enc.Encode(line); err != nil {
			return err
		}
	}
	return w.Flush()
}

// decodeValues is one VIN's values, element id to its values in byte order.
type decodeValues map[string][]string

func (v decodeValues) add(element, value string) { v[element] = append(v[element], value) }

func (v decodeValues) equal(o decodeValues) bool {
	if len(v) != len(o) {
		return false
	}
	for el, vs := range v {
		ws, ok := o[el]
		if !ok || len(vs) != len(ws) {
			return false
		}
		slices.Sort(vs)
		slices.Sort(ws)
		if !slices.Equal(vs, ws) {
			return false
		}
	}
	return true
}

func agree(stdout io.Writer, keyPath, valuesPath string) error {
	key, keyVINs, err := readKey(keyPath)
	if err != nil {
		return err
	}
	f, err := os.Open(valuesPath)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	n, agreed, failed := 0, 0, 0
	var differ []string
	for {
		var line valuesLine
		if err := dec.Decode(&line); err == io.EOF {
			break
		} else if err != nil {
			return fmt.Errorf("%s line %d: %w", valuesPath, n+1, err)
		}
		if n >= len(key) {
			return fmt.Errorf("%s has more lines than %s has input rows (%d)", valuesPath, keyPath, len(key))
		}
		if line.VIN != keyVINs[n] {
			return fmt.Errorf("%s line %d is %q, %s input row %d is %q", valuesPath, n+1, line.VIN, keyPath, n+1, keyVINs[n])
		}
		got := decodeValues{}
		for _, v := range line.Values {
			got.add(v[0], v[1])
		}
		if line.Error != "" {
			failed++
		}
		if line.Error == "" && got.equal(key[n]) {
			agreed++
		} else if len(differ) < 20 {
			differ = append(differ, line.VIN)
		}
		n++
	}
	if n != len(key) {
		return fmt.Errorf("%s has %d lines, %s %d input rows", valuesPath, n, keyPath, len(key))
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"key":          keyPath,
		"values":       valuesPath,
		"vins":         n,
		"agree":        agreed,
		"share":        float64(agreed) / float64(n),
		"errors":       failed,
		"first_differ": differ,
	})
}

// readKey reads an answer key's values per input row, rows 1…n in order.
func readKey(path string) ([]decodeValues, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	r := csv.NewReader(bufio.NewReader(f))
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, c := range []string{"input_row", "vin", "itemelementid", "value"} {
		if _, ok := col[c]; !ok {
			return nil, nil, fmt.Errorf("%s: no column %s", path, c)
		}
	}
	var key []decodeValues
	var vins []string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", path, err)
		}
		row, err := strconv.Atoi(rec[col["input_row"]])
		if err != nil || row < len(key) || row > len(key)+1 {
			return nil, nil, fmt.Errorf("%s: input_row %q out of order", path, rec[col["input_row"]])
		}
		if row > len(key) {
			key = append(key, decodeValues{})
			vins = append(vins, rec[col["vin"]])
		}
		key[row-1].add(rec[col["itemelementid"]], rec[col["value"]])
	}
	return key, vins, nil
}

// lineCounter counts the newlines written to it, discarding the bytes.
type lineCounter int

func (c *lineCounter) Write(p []byte) (int, error) {
	*c += lineCounter(bytes.Count(p, []byte{'\n'}))
	return len(p), nil
}

func timeRuns(stdout, stderr io.Writer, in string, n int, argv []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	enc := json.NewEncoder(stdout)
	for i := range n {
		var lines lineCounter
		c := exec.CommandContext(ctx, argv[0], argv[1:]...)
		c.Stdout = &lines
		c.Stderr = stderr
		if in != "" {
			f, err := os.Open(in)
			if err != nil {
				return err
			}
			c.Stdin = f
			defer f.Close()
		}
		started := time.Now()
		err := c.Run()
		wall := time.Since(started).Seconds()
		if err != nil {
			return fmt.Errorf("run %d: %w", i+1, err)
		}
		u := fromRusage(c.ProcessState.SysUsage().(*syscall.Rusage))
		if err := enc.Encode(map[string]any{
			"run":                i + 1,
			"wall_seconds":       wall,
			"user_cpu_seconds":   u.user,
			"system_cpu_seconds": u.system,
			"max_rss_bytes":      u.maxRSS,
			"stdout_lines":       int(lines),
		}); err != nil {
			return err
		}
	}
	return nil
}
