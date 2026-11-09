package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/decoder"
)

// The refresh's VIN list (ADR 0008): the committed corpus, one-character
// mutations of it for the error paths, and VINs synthesized from the
// month's own patterns until every WMI and VinSchema the month links is
// reached. A WMI or VinSchema is reached when a decode's items name it
// (itemwmiid, itemvinschemaid), which is what an answer key shows; the
// engine decides it here, and the oracle checks the same VINs.
//
// The synthesis follows ultravin's generator (scripts/parity/generator.py
// and crates/ultravin/src/generate.rs at dbc9777, MIT): a VIN is the WMI,
// one of the schema's pattern keys laid over fixed fills, a model-year
// character inside the band, and a computed check digit. It differs in
// that every candidate is decoded before it is kept, so a VIN is emitted
// only for a target it reaches and only while that target is unreached:
// the list is as short as the month allows.

const vinsUsage = `nitrovin-import vins [-now RFC3339] [-max N] [-o PREFIX] [-report FILE] ARTIFACT CORPUS...`

func vinsCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vins", flag.ContinueOnError)
	nowFlag := fs.String("now", "", "the decodes' clock, RFC 3339 (default: now); the oracle's NOW() should fall in the same year")
	perFile := fs.Int("max", 5000, "VINs per file: what one oracle.yml job decodes in both modes")
	prefix := fs.String("o", "vins", "write PREFIX-1.tsv, PREFIX-2.tsv, …")
	report := fs.String("report", "", "write the coverage report (JSON) here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 || *perFile < 1 {
		return fmt.Errorf("vins needs an artifact, at least one corpus file and -max ≥ 1\n%s", usage)
	}
	now := time.Now().UTC()
	if *nowFlag != "" {
		t, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			return fmt.Errorf("-now: %w", err)
		}
		now = t.UTC()
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	a, err := artifact.Open(b)
	if err != nil {
		return err
	}
	// The corpus opens the first file byte for byte: TestParity's
	// -update-parity freezes a run whose list begins with it.
	var corpus []vinLine
	var corpusText strings.Builder
	for _, path := range fs.Args()[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines, err := readVINs(path, data)
		if err != nil {
			return err
		}
		corpus = append(corpus, lines...)
		corpusText.Write(data)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			corpusText.WriteByte('\n')
		}
	}
	if len(corpus) > *perFile {
		return fmt.Errorf("the corpus holds %d VINs, more than -max %d: it must fit the first file", len(corpus), *perFile)
	}

	g := newVinsGen(a, now)
	run := g.generate(corpus)

	files, err := writeShards(*prefix, *perFile, corpusText.String(), len(corpus), run)
	if err != nil {
		return err
	}
	for _, f := range files {
		fmt.Fprintln(stdout, f)
	}
	rep := g.report(a.Month(), now, *perFile, files, len(corpus), run)
	fmt.Fprint(stderr, rep.summary())
	if *report != "" {
		j, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		if err := writeFile(*report, append(j, '\n')); err != nil {
			return err
		}
	}
	return nil
}

// vinLine is one line of a VIN list: the VIN and its model-year hint as
// scripts/oracle.sh decode reads them (0 is no hint), and the comment
// written before it saying why it is there.
type vinLine struct {
	vin  string
	hint int32
	note string
	kind source
}

type source uint8

const (
	unreached source = iota
	fromCorpus
	fromMutation
	fromGenerated
)

var sourceNames = [...]string{"unreached", "corpus", "mutations", "generated"}

// readVINs reads a VIN list, the contents data of path, as
// scripts/oracle.sh decode does: blank lines and lines whose first
// non-space is '#' skipped, a VIN, then optionally a tab and a model-year
// hint. The VIN is kept byte for byte.
func readVINs(path string, data []byte) ([]vinLine, error) {
	var out []vinLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		l := strings.TrimSuffix(sc.Text(), "\r")
		if t := strings.TrimLeft(l, " \t\v\f"); t == "" || t[0] == '#' {
			continue
		}
		vin, rest, _ := strings.Cut(l, "\t")
		hint, _, _ := strings.Cut(rest, "\t")
		v := vinLine{vin: vin, kind: fromCorpus}
		if hint != "" {
			y, err := strconv.ParseInt(hint, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: model-year hint %q: %v", path, n, hint, err)
			}
			v.hint = int32(y)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// vinsGen holds the month's targets and what the list reaches so far.
type vinsGen struct {
	engine *decoder.Engine
	now    time.Time

	wmiID   []int32 // by wmi row
	wmiText []string
	carLT   []bool // fVinModelYear2's car/MPV/light-truck test

	link struct{ wmi, schema, from, to []int32 } // wmi_vinschema, by (wmiid, id)

	patternSchema []int32 // pattern.vinschemaid, sorted
	patternKeys   artifact.Strings
	keys          map[int32][]string // a schema's sampled keys

	wmis, schemas map[int32]source // every target, and who reached it first
	seen          map[vinLine]bool // (vin, hint) already listed
	codes         [fromGenerated + 1]map[string]int
}

func newVinsGen(a *artifact.Artifact, now time.Time) *vinsGen {
	g := &vinsGen{
		engine:  decoder.NewEngine(a),
		now:     now,
		keys:    map[int32][]string{},
		wmis:    map[int32]source{},
		schemas: map[int32]source{},
		seen:    map[vinLine]bool{},
	}
	w := a.Table("wmi")
	g.wmiID = w.Int32s("id")
	texts := w.Strings("wmi")
	vt, tt := w.Int32s("vehicletypeid"), w.Int32s("trucktypeid")
	for r, id := range g.wmiID {
		s, _ := texts.At(r)
		g.wmiText = append(g.wmiText, s)
		g.carLT = append(g.carLT, vt[r] == 2 || vt[r] == 7 || vt[r] == 3 && tt[r] == 1)
		g.wmis[id] = unreached
	}
	l := a.Table("wmi_vinschema")
	g.link.wmi, g.link.schema = l.Int32s("wmiid"), l.Int32s("vinschemaid")
	g.link.from, g.link.to = l.Int32s("yearfrom"), l.Int32s("yearto")
	// A QC'd schema's items are dropped from every decode (W9): no VIN
	// reaches it, so it is not a target.
	s := a.Table("vinschema")
	qc := map[int32]bool{}
	sid, sqc := s.Int32s("id"), s.Bools("tobeqced")
	for r, id := range sid {
		qc[id], _ = sqc.At(r)
	}
	for _, id := range g.link.schema {
		if !qc[id] {
			g.schemas[id] = unreached
		}
	}
	p := a.Table("pattern")
	g.patternSchema, g.patternKeys = p.Int32s("vinschemaid"), p.Strings("keys")
	for i := range g.codes {
		g.codes[i] = map[string]int{}
	}
	return g
}

// decoded is what one decode reached.
type decoded struct {
	wmis, schemas []int32
	codes         string // element 143's attribute: the error codes, "1,11,400"
}

// decode runs the engine on (vin, hint); ok is false where the procedure
// raises, which would stop scripts/oracle.sh decode.
func (g *vinsGen) decode(vin string, hint int32) (d decoded, ok bool) {
	p, err := g.engine.Project(vin, contract.Null[int32]{V: hint, Valid: hint != 0}, g.now)
	if err != nil {
		return d, false
	}
	for _, it := range p.Items {
		if it.WmiID.Valid && !slices.Contains(d.wmis, it.WmiID.V) {
			d.wmis = append(d.wmis, it.WmiID.V)
		}
		// A Vehicle Specs item's itemvinschemaid is a VehicleSpecSchema id
		// (C11), not a VinSchema's.
		if it.VinSchemaID.Valid && it.Source.V != "Vehicle Specs" && !slices.Contains(d.schemas, it.VinSchemaID.V) {
			d.schemas = append(d.schemas, it.VinSchemaID.V)
		}
		if it.ElementID == 143 {
			d.codes = it.AttributeID.V
		}
	}
	return d, true
}

// add lists v unless it is listed already, crediting what d reached to
// v.kind where nothing reached it before.
func (g *vinsGen) add(run *[]vinLine, v vinLine, d decoded) bool {
	k := vinLine{vin: v.vin, hint: v.hint}
	if g.seen[k] {
		return false
	}
	g.seen[k] = true
	*run = append(*run, v)
	for _, id := range d.wmis {
		if s, ok := g.wmis[id]; ok && s == unreached {
			g.wmis[id] = v.kind
		}
	}
	for _, id := range d.schemas {
		if s, ok := g.schemas[id]; ok && s == unreached {
			g.schemas[id] = v.kind
		}
	}
	g.codes[v.kind][d.codes]++
	return true
}

// generate is the run's list: corpus, then mutations, then synthesized VINs.
func (g *vinsGen) generate(corpus []vinLine) []vinLine {
	var run []vinLine
	decodes := make([]decoded, len(corpus))
	ok := make([]bool, len(corpus))
	for i, v := range corpus {
		decodes[i], ok[i] = g.decode(v.vin, v.hint)
		if !ok[i] {
			decodes[i].codes = "raises"
		}
		if !g.add(&run, v, decodes[i]) {
			ok[i] = false // a repeat: mutate it once
		}
	}
	for i, v := range corpus {
		if ok[i] {
			if m, d, found := g.mutate(v); found {
				g.add(&run, m, d)
			}
		}
	}
	for r := range g.wmiID {
		g.sweepWMI(&run, r)
	}
	return run
}

// mutationAlphabet is every character a VIN may hold, then I, O and Q,
// which no VIN may (error code 400).
const mutationAlphabet = "0123456789ABCDEFGHJKLMNPRSTUVWXYZIOQ"

// mutationTries is how many one-character mutations of a VIN are decoded
// to choose the one kept.
const mutationTries = 24

// mutate is one one-character mutation of a 17-character corpus VIN: of
// mutationTries drawn deterministically from the VIN (any position, any
// character of mutationAlphabet), the one whose error codes the
// mutations listed so far show least often, the first on a tie. So the
// mutations spread over the error paths rather than repeat the commonest.
func (g *vinsGen) mutate(v vinLine) (vinLine, decoded, bool) {
	if len(v.vin) != 17 {
		return vinLine{}, decoded{}, false
	}
	h := fnv.New64a()
	h.Write([]byte(v.vin))
	seed := h.Sum64()
	var best vinLine
	var bestD decoded
	bestN := -1
	for k := range uint64(mutationTries) {
		x := splitmix(seed + k)
		pos := int(x % 17)
		c := mutationAlphabet[(x>>8)%uint64(len(mutationAlphabet))]
		if v.vin[pos] == c {
			continue
		}
		m := v.vin[:pos] + string(c) + v.vin[pos+1:]
		if g.seen[vinLine{vin: m, hint: v.hint}] {
			continue
		}
		d, ok := g.decode(m, v.hint)
		if !ok {
			continue
		}
		if n := g.codes[fromMutation][d.codes]; bestN < 0 || n < bestN {
			best = vinLine{vin: m, hint: v.hint, kind: fromMutation,
				note: fmt.Sprintf("mutation of %s: position %d %c→%c, codes %s", v.vin, pos+1, v.vin[pos], c, d.codes)}
			bestD, bestN = d, n
		}
	}
	return best, bestD, bestN >= 0
}

// splitmix is SplitMix64's output function, a deterministic mixer.
func splitmix(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	x = (x ^ x>>30) * 0xBF58476D1CE4E5B9
	x = (x ^ x>>27) * 0x94D049BB133111EB
	return x ^ x>>31
}

// sweepWMI lists VINs of the WMI at row r. Greedily first: of the
// candidates for the WMI's unreached schemas, the one that reaches the
// most schemas nothing reached yet, until none adds one. Then, for each of
// its schemas still unreached, the first candidate of more of its keys
// that reaches it, also with the year as the model-year hint. Then one
// VIN for the WMI itself if nothing reached it.
func (g *vinsGen) sweepWMI(run *[]vinLine, r int) {
	id := g.wmiID[r]
	lo, hi := artifact.EqualRange(g.link.wmi, 0, len(g.link.wmi), id)
	tried := map[vinLine]bool{}
	var cands []candidate
	for j := lo; j < hi; j++ {
		if g.unreachedSchema(g.link.schema[j]) {
			cands = g.candidates(cands, tried, r, j, keySample, false)
		}
	}
	for {
		best, gain := -1, 0
		for i, c := range cands {
			if n := len(g.newSchemas(c.d)); n > gain {
				best, gain = i, n
			}
		}
		if best < 0 {
			break
		}
		g.addCandidate(run, cands[best])
	}
	for j := lo; j < hi; j++ {
		s := g.link.schema[j]
		if !g.unreachedSchema(s) {
			continue
		}
		for _, c := range g.candidates(nil, tried, r, j, keyRetry, true) {
			if slices.Contains(c.d.schemas, s) {
				g.addCandidate(run, c)
				break
			}
		}
	}
	if g.wmis[id] != unreached {
		return
	}
	j := -1
	if lo < hi {
		j = lo
	}
	for _, c := range g.candidates(nil, map[vinLine]bool{}, r, j, 1, false) {
		if slices.Contains(c.d.wmis, id) {
			g.addCandidate(run, c)
			return
		}
	}
}

// candidate is a synthesized VIN and hint, decoded.
type candidate struct {
	vin, note string
	hint      int32
	d         decoded
}

func (g *vinsGen) unreachedSchema(s int32) bool {
	st, ok := g.schemas[s]
	return ok && st == unreached
}

// newSchemas is the schemas d reaches that nothing reached yet.
func (g *vinsGen) newSchemas(d decoded) []int32 {
	var out []int32
	for _, s := range d.schemas {
		if g.unreachedSchema(s) {
			out = append(out, s)
		}
	}
	return out
}

func (g *vinsGen) addCandidate(run *[]vinLine, c candidate) {
	reached := "WMI"
	if s := g.newSchemas(c.d); len(s) > 0 {
		slices.Sort(s)
		reached = "schemas " + strings.Trim(fmt.Sprint(s), "[]")
	}
	g.add(run, vinLine{vin: c.vin, hint: c.hint, kind: fromGenerated, note: reached + ": " + c.note}, c.d)
}

// candidates appends to dst the decoded candidates of WMI row r and link j
// (-1: no link, the WMI alone): up to nKeys of the schema's keys, each at
// every year pickYears gives the band, and with hinted also with that year
// as the model-year hint: a vehicle type whose model year is inconclusive
// otherwise decodes as the year 30 later when the WMI has a schema there.
// It skips what tried holds (and extends it) or the list holds, and
// candidates the procedure raises on.
func (g *vinsGen) candidates(dst []candidate, tried map[vinLine]bool, r, j, nKeys int, hinted bool) []candidate {
	keys := []string{""}
	years := []yearChoice{g.pickYears(0, artifact.NullInt32)[0], nullYear}
	if j >= 0 {
		keys = g.sampleKeys(g.link.schema[j], nKeys)
		years = g.pickYears(g.link.from[j], g.link.to[j])
	}
	for _, k := range keys {
		for _, y := range years {
			vin, ok := buildVIN(g.wmiText[r], k, y)
			if !ok {
				continue
			}
			hints := []int32{0}
			if hinted && y.year != 0 {
				hints = append(hints, int32(y.year))
			}
			for _, h := range hints {
				key := vinLine{vin: vin, hint: h}
				if tried[key] || g.seen[key] {
					continue
				}
				tried[key] = true
				d, ok := g.decode(vin, h)
				if !ok {
					continue
				}
				note := "WMI " + g.wmiText[r]
				if k != "" {
					note += ", keys " + k
				}
				dst = append(dst, candidate{vin, note + ", " + y.String() + ", codes " + d.codes, h, d})
			}
		}
	}
	return dst
}

// keySample is how many of a schema's distinct keys the greedy pass tries,
// keyRetry how many a schema it left unreached gets.
const (
	keySample = 8
	keyRetry  = 64
)

// sampleKeys is up to n of schema s's distinct pattern keys, spread over
// them in byte order.
func (g *vinsGen) sampleKeys(s int32, n int) []string {
	all, ok := g.keys[s]
	if !ok {
		lo, hi := artifact.EqualRange(g.patternSchema, 0, len(g.patternSchema), s)
		for i := lo; i < hi; i++ {
			if k, ok := g.patternKeys.At(i); ok {
				all = append(all, k)
			}
		}
		slices.Sort(all)
		all = slices.Compact(all)
		g.keys[s] = all
	}
	if len(all) <= n {
		return all
	}
	if n == 1 {
		return all[:1]
	}
	keys := make([]string, 0, n)
	for i := range n {
		keys = append(keys, all[i*(len(all)-1)/(n-1)])
	}
	return slices.Compact(keys)
}

// yearChoice is what a VIN says about its model year: position 10, and
// whether position 7 is a digit, which puts a car, MPV or light truck in
// 1980-2009 (fVinModelYear2).
type yearChoice struct {
	year  int // 0: no year
	char  byte
	digit bool
}

// nullYear is a position 10 fVinModelYear2 maps to no year: the pass runs
// with a NULL year, under which every band of the WMI applies (code 11).
var nullYear = yearChoice{char: '0'}

func (y yearChoice) String() string {
	if y.year == 0 {
		return "no model year"
	}
	return "model year " + strconv.Itoa(y.year)
}

// modelYearChars is position 10 for 1980-2009 and again for 2010-2039.
const modelYearChars = "ABCDEFGHJKLMNPRSTVWXY123456789"

func yearAt(year int, digit bool) yearChoice {
	return yearChoice{year: year, char: modelYearChars[(year-1980)%30], digit: digit}
}

// pickYears is the years tried for a band [from, to] (to NULL: open), in
// order: its newest year from 2010 up to two past now (as ultravin's
// pick_year), its newest before 2010 (with a digit at position 7), and no
// year at all.
func (g *vinsGen) pickYears(from, to int32) []yearChoice {
	hiYear := int32(9999)
	if to != artifact.NullInt32 {
		hiYear = to
	}
	var out []yearChoice
	if hi, lo := min(hiYear, int32(g.now.Year()+2), 2039), max(from, 2010); lo <= hi {
		out = append(out, yearAt(int(hi), false))
	}
	if hi, lo := min(hiYear, 2009), max(from, 1980); lo <= hi {
		out = append(out, yearAt(int(hi), true))
	}
	return append(out, nullYear)
}

// buildVIN lays pattern key keys over a VIN of wmi with model year y: the
// key's characters where it pins one, fixed fills elsewhere, and the check
// digit computed last. keys reads as the pattern pass matches it against
// var_keys (positions 4-8, '|', positions 10-17): '*' and '_' any
// character, '#' a digit (formula keys), [...] its lowest member legal in
// a VIN, anything else itself. ok is false when the key cannot match a
// VIN of this WMI: longer than var_keys, not accepting the '|', or
// pinning a position a six-character WMI holds to another character.
func buildVIN(wmi, keys string, y yearChoice) (string, bool) {
	if len(wmi) != 3 && len(wmi) != 6 {
		return "", false
	}
	vin := []byte("AAAAAAAA0AA111111")
	copy(vin, wmi[:3])
	if y.digit {
		vin[6] = '1'
	}
	vin[9] = y.char
	for i, k := 0, 0; k < len(keys); i++ {
		if i >= 14 {
			return "", false
		}
		c, n := keyToken(keys[k:])
		k += n
		switch {
		case i == 5:
			if c != 0 && c != '|' {
				return "", false
			}
		case c != 0:
			vin[varKeyPos(i)] = c
		}
	}
	if len(wmi) == 6 {
		for i := range 3 {
			vin[11+i] = wmi[3+i]
		}
		if !acceptsWMITail(keys, wmi[3:]) {
			return "", false
		}
	}
	vin[8] = checkDigit(vin)
	return string(vin), true
}

// varKeyPos is the VIN index (0-based) of var_keys index i, i ≠ 5.
func varKeyPos(i int) int {
	if i < 5 {
		return 3 + i
	}
	return i + 3
}

// keyToken reads the token keys starts with: the character it pins (0 for
// a wildcard) and its length in bytes.
func keyToken(keys string) (c byte, n int) {
	switch keys[0] {
	case '*', '_':
		return 0, 1
	case '#':
		return '1', 1
	case '[':
		end := strings.IndexByte(keys, ']')
		if end < 0 {
			return '[', 1
		}
		return classMember(keys[1:end]), end + 1
	}
	return keys[0], 1
}

// classMember is the lowest character of a bracket class body that a VIN
// may hold (0-9, A-Z but I, O and Q), or the body's first byte when it has
// none, which the decode then rejects; a-b is a range when a byte follows
// the '-'.
func classMember(body string) byte {
	best := byte(0)
	for i := 0; i < len(body); i++ {
		lo, hi := body[i], body[i]
		if i+2 < len(body) && body[i+1] == '-' {
			hi = body[i+2]
			i += 2
		}
		for c := lo; c <= hi && c >= lo; c++ {
			if isVINChar(c) && (best == 0 || c < best) {
				best = c
				break
			}
		}
	}
	if best == 0 && body != "" {
		return body[0]
	}
	return best
}

func isVINChar(c byte) bool {
	return '0' <= c && c <= '9' || 'A' <= c && c <= 'Z' && c != 'I' && c != 'O' && c != 'Q'
}

// acceptsWMITail reports whether keys leaves positions 12-14 (var_keys
// 8-10) to tail or pins them to it.
func acceptsWMITail(keys, tail string) bool {
	for i, k := 0, 0; k < len(keys) && i < 11; i++ {
		c, n := keyToken(keys[k:])
		k += n
		if i >= 8 && c != 0 && c != tail[i-8] {
			return false
		}
	}
	return true
}

// checkDigit is position 9 as the check digit defines it (transliterated
// characters times position weights, mod 11, 10 as 'X'), '0' when a
// character has no value.
func checkDigit(vin []byte) byte {
	const weights = "\x08\x07\x06\x05\x04\x03\x02\x0a\x00\x09\x08\x07\x06\x05\x04\x03\x02"
	sum := 0
	for i, c := range vin {
		v, ok := transliterate(c)
		if !ok {
			return '0'
		}
		sum += v * int(weights[i])
	}
	if r := sum % 11; r < 10 {
		return byte('0' + r)
	}
	return 'X'
}

func transliterate(c byte) (int, bool) {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0'), true
	case 'A' <= c && c <= 'H':
		return int(c-'A') + 1, true
	case 'J' <= c && c <= 'N':
		return int(c-'J') + 1, true
	case c == 'P':
		return 7, true
	case c == 'R':
		return 9, true
	case 'S' <= c && c <= 'Z':
		return int(c-'S') + 2, true
	}
	return 0, false
}

// listHeader opens what nitrovin-import vins adds to the corpus.
const listHeader = "# The refresh's VIN list (ADR 0008) from here on is nitrovin-import vins':\n" +
	"# VIN, then optionally a tab and a model-year hint; each comment says why the VIN after it is here.\n"

// writeShards writes the list as PREFIX-1.tsv, PREFIX-2.tsv, … of at most
// perFile VINs each and returns their names. The first file opens with
// corpusText, the corpus's corpusVINs VINs as given; then come run's
// mutations and generated VINs, each after its note.
func writeShards(prefix string, perFile int, corpusText string, corpusVINs int, run []vinLine) ([]string, error) {
	var files []string
	var b strings.Builder
	b.WriteString(corpusText)
	n, kind := corpusVINs, fromCorpus
	flush := func() error {
		name := fmt.Sprintf("%s-%d.tsv", prefix, len(files)+1)
		if err := writeFile(name, []byte(b.String())); err != nil {
			return err
		}
		files = append(files, name)
		b.Reset()
		n, kind = 0, unreached
		return nil
	}
	for _, v := range run {
		if v.kind == fromCorpus {
			continue
		}
		if n == perFile {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		if kind == fromCorpus || b.Len() == 0 {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(listHeader)
		}
		if v.kind != kind {
			kind = v.kind
			fmt.Fprintf(&b, "\n# %s\n", sourceNames[kind])
		}
		if v.note != "" {
			fmt.Fprintf(&b, "# %s\n", v.note)
		}
		b.WriteString(v.vin)
		if v.hint != 0 {
			fmt.Fprintf(&b, "\t%d", v.hint)
		}
		b.WriteByte('\n')
		n++
	}
	if n > 0 || len(files) == 0 {
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// vinsReport is the coverage report: how many VINs each part of the list
// holds, how many of the month's WMIs and VinSchemas each part reaches
// first, and which ones the corpus alone misses or nothing reaches.
type vinsReport struct {
	Month string    `json:"month"`
	Now   time.Time `json:"now"`
	Max   int       `json:"max_per_file"`
	Files []string  `json:"files"`
	VINs  struct {
		Corpus    int `json:"corpus"`
		Mutations int `json:"mutations"`
		Generated int `json:"generated"`
		Total     int `json:"total"`
	} `json:"vins"`
	WMIs       targetReport   `json:"wmis"`
	Schemas    targetReport   `json:"schemas"`
	ErrorCodes map[string]any `json:"error_codes"`
}

type targetReport struct {
	Month     int `json:"month"`
	Corpus    int `json:"corpus"`
	Mutations int `json:"mutations"`
	Generated int `json:"generated"`
	// CorpusMisses are the targets the corpus alone does not reach.
	CorpusMisses []string `json:"corpus_misses"`
	// Unreached are the targets nothing in the list reaches.
	Unreached []string `json:"unreached"`
}

func (g *vinsGen) report(month string, now time.Time, perFile int, files []string, corpusVINs int, run []vinLine) vinsReport {
	var r vinsReport
	r.Month, r.Now, r.Max, r.Files = month, now, perFile, files
	r.VINs.Corpus = corpusVINs
	for _, v := range run {
		switch v.kind {
		case fromMutation:
			r.VINs.Mutations++
		case fromGenerated:
			r.VINs.Generated++
		}
	}
	r.VINs.Total = r.VINs.Corpus + r.VINs.Mutations + r.VINs.Generated
	wmiText := map[int32]string{}
	for i, id := range g.wmiID {
		wmiText[id] = g.wmiText[i]
	}
	r.WMIs = tally(g.wmis, func(id int32) string { return wmiText[id] })
	r.Schemas = tally(g.schemas, func(id int32) string { return strconv.Itoa(int(id)) })
	r.ErrorCodes = map[string]any{"corpus": g.codes[fromCorpus], "mutations": g.codes[fromMutation], "generated": g.codes[fromGenerated]}
	return r
}

func tally(targets map[int32]source, name func(int32) string) targetReport {
	t := targetReport{Month: len(targets), CorpusMisses: []string{}, Unreached: []string{}}
	ids := make([]int32, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		switch targets[id] {
		case fromCorpus:
			t.Corpus++
			continue
		case fromMutation:
			t.Mutations++
		case fromGenerated:
			t.Generated++
		case unreached:
			t.Unreached = append(t.Unreached, name(id))
		}
		t.CorpusMisses = append(t.CorpusMisses, name(id))
	}
	return t
}

func (r vinsReport) summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d VINs in %d files of at most %d: %d corpus, %d mutations, %d generated\n",
		r.Month, r.VINs.Total, len(r.Files), r.Max, r.VINs.Corpus, r.VINs.Mutations, r.VINs.Generated)
	for _, t := range []struct {
		name string
		t    targetReport
	}{{"WMIs", r.WMIs}, {"VinSchemas", r.Schemas}} {
		fmt.Fprintf(&b, "%s: %d in the month; reached by the corpus %d, then mutations %d, generated %d; corpus misses %d; unreached %d\n",
			t.name, t.t.Month, t.t.Corpus, t.t.Mutations, t.t.Generated, len(t.t.CorpusMisses), len(t.t.Unreached))
	}
	return b.String()
}
