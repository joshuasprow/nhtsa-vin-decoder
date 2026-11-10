package ui

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// A Library is the files the browser's Service Worker translates with
// (ADR 0013): the server's, which it links from their published URLs
// (FilePath), and the browser's own, kept in IndexedDB (a Disk) and never
// sent anywhere. It answers the vocabulary pages as the server does, through
// the same core (contract.Translator over a vocab.Set) beside the release's
// spine bundle; a VIN is decoded by the server's /v1/decode. It holds no
// JavaScript, so its rules are tested where Go runs: cmd/nitrovin-sw gives
// it the browser's Disk and fetch.
//
// Each stored file keeps its parsed index (vocab.WriteIndex) beside its
// text, keyed by its blake3 and the release, so a cold start reads indexes
// and parses nothing. A stored file this release cannot read, or that no
// longer links, is listed with its fault and left out. Picking several
// files loads them as one batch: it links, replacing stored files of the
// same names, or nothing changes. Removing a file others link to is
// refused, naming them, unless all of them go. A change is stored before
// the Library takes it, so what it answers from is always what IndexedDB
// holds.
type Library struct {
	info   contract.Info
	month  *vocab.Bundle
	disk   Disk
	remote Remote
	now    func() time.Time
	// opened is how long each step of opening took, as Server-Timing
	// metrics: what a cold start costs.
	opened string
	// indexRead and indexParse are the time opening spent getting stored
	// indexes and reading them.
	indexRead, indexParse time.Duration

	// mu serializes changes; state is what the pages answer from.
	mu    sync.Mutex
	state atomic.Pointer[shelf]
}

// Disk is the browser's IndexedDB, as the worker's script keeps it:
// stored files' records and texts, and their indexes of this release.
type Disk interface {
	// Records are the stored files.
	Records() ([]Record, error)
	// Index is the index of the file with blake3 hash, stored by this
	// release; nil when there is none.
	Index(blake3 string) ([]byte, error)
	PutIndex(blake3 string, index []byte) error
	// Text is a stored file's bytes.
	Text(blake3 string) ([]byte, error)
	// Change stores put (replacing records of the same names) and drops
	// the records named remove, in one transaction: all of it, or none.
	Change(put []Stored, remove []string) error
}

// Remote is the server, from the browser: a GET of path on its origin.
type Remote interface {
	Get(path string) (status int, body []byte, err error)
}

// Record is one stored file: its name (the header's), the name of the file
// it was picked as, its blake3 in hex, its size and when it was stored.
type Record struct {
	Name     string    `json:"name"`
	FileName string    `json:"file_name"`
	Blake3   string    `json:"blake3"`
	Size     int       `json:"size"`
	Stored   time.Time `json:"stored"`
}

// Stored is a file to store: its record, text and index.
type Stored struct {
	Record
	Text, Index []byte
}

// Picked is a file the visitor picked to load: its name and bytes.
type Picked struct {
	FileName string
	Data     []byte
}

// StoredFile is a file as the pages list it: its record (the browser's
// files alone have one), what it holds, why it is left out ("" when it
// links), and where it drifts from the release's data month.
type StoredFile struct {
	Record
	Kind, Version, Fault string
	// Lines are its entries, extensions or spine entries.
	Lines int
	// Drift counts its drift by kind, as /v1/info does; empty for a file
	// left out.
	Drift []contract.DriftCount
}

// shelf is the files at one moment, and the set the linked ones make.
type shelf struct {
	server, browser []*held
	set             *vocab.Set
	// drift is the linked files' drift, measured once, when a page first
	// shows it: a translation never needs it.
	driftOnce sync.Once
	drift     []vocab.Finding
}

// held is one file: the server's or the browser's, read (file) or not,
// and why it is left out (fault).
type held struct {
	name   string
	server bool
	rec    Record
	file   *vocab.File
	fault  string
}

// OpenLibrary opens the browser's files beside the server whose release
// info names: the release's spine bundle and the server's files, from
// remote, and the stored files, from disk. A stored file is read from its
// index when this release stored one, else parsed again and its index
// stored. The error is a Library that cannot answer at all: no spine
// bundle, or a stored list it cannot read.
func OpenLibrary(info contract.Info, remote Remote, disk Disk) (*Library, error) {
	var timings []string
	step := time.Now()
	took := func(name string) {
		timings = append(timings, fmt.Sprintf("%s;dur=%.1f", name, float64(time.Since(step).Microseconds())/1000))
		step = time.Now()
	}
	status, body, err := remote.Get(SpinePath(info))
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("HTTP %d", status)
	}
	if err != nil {
		return nil, fmt.Errorf("fetching the spine bundle %s: %w", SpinePath(info), err)
	}
	month, err := vocab.ReadBundle(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	took("spine")
	if month.Name() != info.DataMonth {
		return nil, fmt.Errorf("the spine bundle is of data month %s, the release's %s", month.Name(), info.DataMonth)
	}
	l := &Library{info: info, month: month, disk: disk, remote: remote, now: time.Now}
	s := &shelf{}
	for _, v := range info.Vocabularies {
		h := &held{name: v.Name, server: true}
		h.file, err = l.read(v.Blake3, serverPath(v.Name), func() ([]byte, error) {
			status, body, err := remote.Get(FilePath(info, v))
			if err == nil && status != http.StatusOK {
				err = fmt.Errorf("HTTP %d from %s", status, FilePath(info, v))
			}
			return body, err
		})
		if err != nil {
			h.fault = err.Error()
		}
		s.server = append(s.server, h)
	}
	took("server-files")
	records, err := disk.Records()
	if err != nil {
		return nil, fmt.Errorf("reading the stored files: %w", err)
	}
	for _, rec := range records {
		h := &held{name: rec.Name, rec: rec}
		h.file, err = l.read(rec.Blake3, browserPath(rec.FileName, rec.Name), func() ([]byte, error) { return disk.Text(rec.Blake3) })
		if err != nil {
			h.fault = err.Error()
		} else if h.file.Name != rec.Name {
			h.file, h.fault = nil, fmt.Sprintf("its header names it %q now", h.file.Name)
		}
		s.browser = append(s.browser, h)
	}
	slices.SortFunc(s.browser, func(a, b *held) int { return strings.Compare(a.name, b.name) })
	took("stored-files")
	timings = append(timings, fmt.Sprintf("index-get;dur=%.1f, index-read;dur=%.1f",
		float64(l.indexRead.Microseconds())/1000, float64(l.indexParse.Microseconds())/1000))
	set, dropped, err := l.link(s.all(), nil)
	if err != nil {
		return nil, err
	}
	s.set = set
	for _, h := range s.all() {
		if why, ok := dropped[h]; ok {
			h.fault = why
		}
	}
	took("link")
	l.opened = strings.Join(timings, ", ")
	l.state.Store(s)
	return l, nil
}

// serverPath and browserPath are the names a file's faults carry: the
// server's by its name, the browser's by the file it was picked as and,
// when that is not "<name>.jsonl", its name, so no two are alike.
func serverPath(name string) string { return name + ".jsonl on the server" }

func browserPath(fileName, name string) string {
	if fileName == name+".jsonl" {
		return fileName
	}
	return fileName + " (" + name + ")"
}

// read is the file with hash blake3: from its stored index, else parsed
// from text's bytes, its index then stored.
func (l *Library) read(blake3, path string, text func() ([]byte, error)) (*vocab.File, error) {
	t0 := time.Now()
	idx, err := l.disk.Index(blake3)
	t1 := time.Now()
	l.indexRead += t1.Sub(t0)
	if err == nil && idx != nil {
		f, err := vocab.ReadIndex(idx)
		l.indexParse += time.Since(t1)
		if err == nil && hex.EncodeToString(f.Hash[:]) == blake3 && f.Path == path {
			return f, nil
		}
	}
	data, err := text()
	if err != nil {
		return nil, err
	}
	f, err := vocab.Read(path, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New(faultText(err))
	}
	if got := hex.EncodeToString(f.Hash[:]); got != blake3 {
		return nil, fmt.Errorf("its bytes changed: blake3 %s, not %s", got, blake3)
	}
	var written bytes.Buffer
	if err := vocab.WriteIndex(&written, f); err != nil {
		return nil, err
	}
	if err := l.disk.PutIndex(blake3, written.Bytes()); err != nil {
		return nil, fmt.Errorf("storing its index: %w", err)
	}
	return f, nil
}

// faultText is a load's error in words: its first faults, and how many
// more there are.
func faultText(err error) string {
	faults, ok := errors.AsType[vocab.Faults](err)
	if !ok {
		return err.Error()
	}
	const shown = 3
	var b strings.Builder
	for i, f := range faults[:min(shown, len(faults))] {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(f.String())
	}
	if n := len(faults) - shown; n > 0 {
		fmt.Fprintf(&b, "; and %d more", n)
	}
	return b.String()
}

func (s *shelf) all() []*held { return append(slices.Clone(s.server), s.browser...) }

// linked reports whether h is in the shelf's set.
func (h *held) linked() bool { return h.file != nil && h.fault == "" }

// linkedServer are s's server files that link.
func (s *shelf) linkedServer() []*held {
	return slices.DeleteFunc(slices.Clone(s.server), func(h *held) bool { return !h.linked() })
}

// link makes the set of the files of hs that read, leaving out those that
// do not link (Link's faults), each with its fault, until the rest link.
// A file of required that does not link refuses: the error says why.
func (l *Library) link(hs, required []*held) (*vocab.Set, map[*held]string, error) {
	dropped := map[*held]string{}
	for {
		var files []*vocab.File
		byPath := map[string]*held{}
		for _, h := range hs {
			if _, out := dropped[h]; h.file != nil && !out {
				files = append(files, h.file)
				byPath[h.file.Path] = h
			}
		}
		set, err := vocab.NewSet(l.month, files)
		if err == nil {
			return set, dropped, nil
		}
		faults, ok := errors.AsType[vocab.Faults](err)
		if !ok {
			return nil, nil, err
		}
		var refused []string
		for _, f := range faults {
			h := byPath[f.File]
			switch {
			case h == nil:
				return nil, nil, err
			case slices.Contains(required, h):
				refused = append(refused, f.String())
			case dropped[h] == "":
				dropped[h] = f.String()
			}
		}
		if len(refused) > 0 {
			return nil, dropped, &pageError{Status: http.StatusConflict, Message: strings.Join(refused, "; ")}
		}
	}
}

// Opened is how long each step of opening the Library took, as
// Server-Timing metrics: spine, server-files, stored-files, link.
func (l *Library) Opened() string { return l.opened }

// Info is the server's /v1/info as the worker was given it: its release.
func (l *Library) Info() contract.Info { return l.info }

// Month is the data month of the release's spine bundle.
func (l *Library) Month() string { return l.month.Name() }

// Files are the stored files, by name.
func (l *Library) Files() []StoredFile {
	s := l.state.Load()
	return l.list(s, s.browser)
}

// Server are the server's files, as it publishes them, each with what
// it holds or the fault that left it out.
func (l *Library) Server() []StoredFile {
	s := l.state.Load()
	return l.list(s, s.server)
}

// list is hs, files of s, as the pages list them.
func (l *Library) list(s *shelf, hs []*held) []StoredFile {
	out := make([]StoredFile, len(hs))
	for i, h := range hs {
		out[i] = StoredFile{Record: h.rec, Fault: h.fault}
		out[i].Name = h.name
		if f := h.file; f != nil {
			out[i].Kind, out[i].Version = string(f.Kind), f.Version
			out[i].Lines = len(f.Entries) + len(f.Extensions) + len(f.SpineEntries)
		}
		if h.linked() {
			out[i].Drift = contract.DriftCounts(h.file.Path, l.drift(s))
		}
	}
	return out
}

// drift is the drift of s's linked files from the release's data month,
// measured on first use.
func (l *Library) drift(s *shelf) []vocab.Finding {
	s.driftOnce.Do(func() {
		var files []*vocab.File
		for _, h := range s.all() {
			if h.linked() {
				files = append(files, h.file)
			}
		}
		s.drift = vocab.Drift(l.month, files)
	})
	return s.drift
}

// translator is the core over s's set.
func (l *Library) translator(s *shelf) contract.Translator {
	return contract.Translator{Set: s.set, DataMonth: l.month.Name(), DecoderVersion: l.info.DecoderVersion}
}

// Translate answers as /v1/translate does, a VIN decoded by the server's
// /v1/decode: refused when the server's data month is not the release's
// (a newer server: the worker updates once its pages close), and in words
// when the server cannot be reached.
func (l *Library) Translate(req contract.TranslateRequest) (contract.Translation, error) {
	t := l.translator(l.state.Load())
	var decodeErr error
	t.Decode = func(vin string, year contract.Null[int32], now time.Time) (contract.VINDecode, error) {
		d, err := l.decode(vin, year, now)
		decodeErr = err
		return d, err
	}
	tr, err := t.Translate(req)
	if err != nil && decodeErr != nil {
		return tr, decodeErr
	}
	return tr, err
}

// List answers as /v1/vocabularies does.
func (l *Library) List(req contract.ListRequest) (contract.List, error) {
	return l.translator(l.state.Load()).List(req)
}

// decode is vin's decode by the server, every item.
func (l *Library) decode(vin string, year contract.Null[int32], now time.Time) (contract.VINDecode, error) {
	u := "/v1/decode/" + url.PathEscape(vin) + "?full=true&now=" + url.QueryEscape(now.UTC().Format(time.RFC3339))
	if year.Valid {
		u += "&model_year=" + strconv.Itoa(int(year.V))
	}
	status, body, err := l.remote.Get(u)
	if err != nil {
		// err is the browser's ("TypeError: Failed to fetch"), which says
		// nothing more than that the request failed.
		return contract.VINDecode{}, &pageError{Status: http.StatusServiceUnavailable, Message: "Decoding the VIN " + vin +
			" needs this nitrovin's server, which could not be reached: this browser may be offline. Translating a vocabulary entry works without it."}
	}
	if status != http.StatusOK {
		var p contract.Problem
		if json.Unmarshal(body, &p) == nil && p.Status != 0 && p.Type != contract.Internal {
			return contract.VINDecode{}, &p
		}
		return contract.VINDecode{}, &pageError{Status: http.StatusBadGateway, Message: fmt.Sprintf("The server answered HTTP %d to the decode of %s: %s", status, vin, p.Detail)}
	}
	var d struct {
		VIN             string `json:"vin"`
		WMI             string `json:"wmi"`
		CheckDigitValid bool   `json:"check_digit_valid"`
		DataMonth       string `json:"data_month"`
		DecoderVersion  string `json:"decoder_version"`
		Items           []struct {
			ElementID   int32                 `json:"element_id"`
			Value       contract.Null[string] `json:"value"`
			AttributeID contract.Null[string] `json:"attribute_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return contract.VINDecode{}, fmt.Errorf("reading the server's decode of %s: %w", vin, err)
	}
	if d.DataMonth != l.month.Name() {
		return contract.VINDecode{}, &pageError{Status: http.StatusConflict, Message: fmt.Sprintf(
			"This server now decodes with data month %s, but the translator in this browser is of data month %s (release %s): close every nitrovin tab and open it again to update it.",
			d.DataMonth, l.month.Name(), Release(l.info))}
	}
	items := make([]contract.Item, len(d.Items))
	for i, it := range d.Items {
		items[i] = contract.Item{ElementID: it.ElementID, Value: it.Value, AttributeID: it.AttributeID}
	}
	return contract.VINDecode{
		Projection: contract.Projection{VIN: d.VIN, WMI: d.WMI, CheckDigitValid: d.CheckDigitValid, Items: items},
		DataMonth:  d.DataMonth, DecoderVersion: d.DecoderVersion,
	}, nil
}

// Load reads picked as one batch and stores it: each file checked alone
// (vocab.Read), then all linked with the server's and the stored files, a
// stored file of the same name replaced. A batch that does not link, or
// that would leave a linked stored file unable to, changes nothing: the
// error says which file and why. progress is told each step.
func (l *Library) Load(picked []Picked, progress func(string)) ([]StoredFile, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.state.Load()
	var news []*held
	for _, p := range picked {
		progress("Reading " + p.FileName + "…")
		f, err := vocab.Read(p.FileName, bytes.NewReader(p.Data))
		if err != nil {
			return nil, &pageError{Status: http.StatusUnprocessableEntity, Message: faultText(err)}
		}
		if slices.ContainsFunc(cur.server, func(h *held) bool { return h.name == f.Name }) {
			return nil, &pageError{Status: http.StatusConflict, Message: fmt.Sprintf(
				"%s is named %q, like a file this server loaded: one name is one file (ADR 0011), so rename it in its header to load it here.", p.FileName, f.Name)}
		}
		f.Path = browserPath(p.FileName, f.Name)
		news = append(news, &held{name: f.Name, file: f, rec: Record{
			Name: f.Name, FileName: p.FileName, Blake3: hex.EncodeToString(f.Hash[:]), Size: len(p.Data), Stored: l.now().UTC().Truncate(time.Second),
		}})
	}
	progress("Linking…")
	replaced := func(h *held) bool { return slices.ContainsFunc(news, func(n *held) bool { return n.name == h.name }) }
	kept := slices.DeleteFunc(slices.Clone(cur.browser), replaced)
	next := &shelf{server: cur.server, browser: append(kept, news...)}
	slices.SortFunc(next.browser, func(a, b *held) int { return strings.Compare(a.name, b.name) })
	set, dropped, err := l.link(next.all(), append(cur.linkedServer(), news...))
	if err != nil {
		return nil, err
	}
	if broken := breaks(kept, dropped); broken != "" {
		return nil, &pageError{Status: http.StatusConflict, Message: "This batch would leave stored files unable to link, so nothing was loaded: " + broken}
	}
	progress("Storing…")
	put := make([]Stored, len(news))
	for i, n := range news {
		var idx bytes.Buffer
		if err := vocab.WriteIndex(&idx, n.file); err != nil {
			return nil, err
		}
		put[i] = Stored{Record: n.rec, Text: picked[i].Data, Index: idx.Bytes()}
	}
	if err := l.disk.Change(put, nil); err != nil {
		return nil, fmt.Errorf("storing the batch in this browser: %w", err)
	}
	next.set = next.relink(set, dropped)
	l.state.Store(next)
	return l.Files(), nil
}

// breaks says which of kept, linked now, dropped leaves out, and why; ""
// when none.
func breaks(kept []*held, dropped map[*held]string) string {
	var out []string
	for _, h := range kept {
		if why, ok := dropped[h]; ok && h.linked() {
			out = append(out, why)
		}
	}
	return strings.Join(out, "; ")
}

// relink copies s's files with their faults as dropped has them, a file
// that links again losing its old fault, so the shelf before stays as it
// was; and returns set.
func (s *shelf) relink(set *vocab.Set, dropped map[*held]string) *vocab.Set {
	s.server = slices.Clone(s.server)
	for _, hs := range [][]*held{s.server, s.browser} {
		for i, h := range hs {
			c := *h
			if why, ok := dropped[h]; ok {
				c.fault = why
			} else if c.file != nil {
				c.fault = ""
			}
			hs[i] = &c
		}
	}
	return set
}

// removeRefusal is a remove that would leave other stored files unable to
// link: they map into what goes.
type removeRefusal struct {
	Names, Dependants []string
}

func (e *removeRefusal) Error() string {
	return fmt.Sprintf("%s cannot go alone: %s link to it.", strings.Join(e.Names, ", "), strings.Join(e.Dependants, ", "))
}

// Remove drops the stored files named names. When stored files that link
// now would not link without them, it refuses (a *removeRefusal naming
// them) unless all is set, which drops them too.
func (l *Library) Remove(names []string, all bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.state.Load()
	if len(names) == 0 {
		return &pageError{Status: http.StatusBadRequest, Message: "Name a file to remove."}
	}
	for _, n := range names {
		if !slices.ContainsFunc(cur.browser, func(h *held) bool { return h.name == n }) {
			return &pageError{Status: http.StatusNotFound, Message: fmt.Sprintf("No file named %q is kept in this browser.", n)}
		}
	}
	gone := func(h *held) bool { return slices.Contains(names, h.name) }
	kept := slices.DeleteFunc(slices.Clone(cur.browser), gone)
	next := &shelf{server: cur.server, browser: kept}
	set, dropped, err := l.link(next.all(), cur.linkedServer())
	if err != nil {
		return err
	}
	var dependants []string
	for _, h := range kept {
		if _, ok := dropped[h]; ok && h.linked() {
			dependants = append(dependants, h.name)
		}
	}
	if len(dependants) > 0 {
		if !all {
			return &removeRefusal{Names: names, Dependants: dependants}
		}
		names = append(names, dependants...)
		next.browser = slices.DeleteFunc(kept, gone)
	}
	if err := l.disk.Change(nil, names); err != nil {
		return fmt.Errorf("removing from this browser: %w", err)
	}
	next.set = next.relink(set, dropped)
	l.state.Store(next)
	return nil
}
