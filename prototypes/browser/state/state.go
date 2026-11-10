// Package state is the PROTOTYPE browser-side state (#147): the spine
// bundle and the vocabularies held in IndexedDB, indexed by pkg/vocab. The
// JavaScript side owns IndexedDB; this owns parsing, linking and the Set.
// No syscall/js or html/template here, so it builds natively, with Go and
// with TinyGo.
package state

import (
	"bytes"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
	"github.com/joshuasprow/nitrovin/prototypes/browser/spine"
	"github.com/joshuasprow/nitrovin/prototypes/browser/tr"
)

// Record is one IndexedDB row: its key (name@blake3) and the file's text.
type Record struct {
	Key  string
	Data []byte
}

// State is the browser's loaded files over the spine bundle.
type State struct {
	mu     sync.Mutex
	bundle *spine.Bundle
	files  []*vocab.File
	sizes  map[*vocab.File]int
	local  *tr.Local
}

// Timing is what one SetSpine, Index or Load took, in milliseconds.
type Timing struct {
	ParseMs float64  `json:"parse_ms"`
	LinkMs  float64  `json:"link_ms"`
	DriftMs float64  `json:"drift_ms"`
	Files   int      `json:"files"`
	Entries int      `json:"entries"`
	Faults  []string `json:"faults,omitempty"`
	Key     string   `json:"key,omitempty"`
	Error   string   `json:"error,omitempty"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// Local is the translator over the current files; nil before SetSpine.
func (s *State) Local() *tr.Local {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.local
}

// SetSpine parses the spine bundle and re-indexes the files over it.
func (s *State) SetSpine(data []byte) (Timing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := time.Now()
	b, err := spine.Parse(data)
	if err != nil {
		return Timing{}, err
	}
	s.bundle = b
	t := Timing{ParseMs: ms(time.Since(start))}
	return t, s.rebuild(&t)
}

// Index replaces the files with recs, as a cold start does: every record is
// read again. A record that faults is dropped and reported.
func (s *State) Index(recs []Record) (Timing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Timing
	start := time.Now()
	s.files, s.sizes = nil, map[*vocab.File]int{}
	for _, r := range recs {
		f, err := vocab.Read(r.Key, bytes.NewReader(r.Data))
		if err != nil {
			t.Faults = append(t.Faults, err.Error())
			continue
		}
		s.files = append(s.files, f)
		s.sizes[f] = len(r.Data)
		t.Entries += len(f.Entries)
	}
	t.ParseMs = ms(time.Since(start))
	return t, s.rebuild(&t)
}

// Load reads one more file and links it with the rest; a fault refuses it
// and keeps the state. Its key, name@blake3, is in the Timing.
func (s *State) Load(name string, data []byte) (Timing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var t Timing
	start := time.Now()
	f, err := vocab.Read(name, bytes.NewReader(data))
	if err != nil {
		return t, err
	}
	key := f.Name + "@" + tr.Hash(f)
	f.Path = key
	for _, g := range s.files {
		if g.Path == key {
			return t, errors.New(f.Name + " is loaded already")
		}
	}
	t.ParseMs, t.Entries, t.Key = ms(time.Since(start)), len(f.Entries), key
	if s.sizes == nil {
		s.sizes = map[*vocab.File]int{}
	}
	prev := s.files
	s.files = append(slices.Clone(s.files), f)
	s.sizes[f] = len(data)
	if err := s.rebuild(&t); err != nil {
		s.files = prev
		delete(s.sizes, f)
		_ = s.rebuild(&Timing{})
		return t, err
	}
	return t, nil
}

// Remove drops the file under key.
func (s *State) Remove(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.files, func(f *vocab.File) bool { return f.Path == key })
	if i < 0 {
		return errors.New("no file " + key)
	}
	delete(s.sizes, s.files[i])
	s.files = slices.Delete(slices.Clone(s.files), i, i+1)
	return s.rebuild(&Timing{})
}

// rebuild links and indexes the files over the bundle, then measures drift.
func (s *State) rebuild(t *Timing) error {
	if s.bundle == nil {
		return nil
	}
	start := time.Now()
	set, err := vocab.NewSet(s.bundle, s.files)
	if err != nil {
		return err
	}
	t.LinkMs = ms(time.Since(start))
	start = time.Now()
	s.local = tr.New(s.bundle, set)
	t.DriftMs = ms(time.Since(start))
	t.Files = len(s.files)
	return nil
}

// Month is the bundle's data month.
func (s *State) Month() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bundle == nil {
		return ""
	}
	return s.bundle.Name()
}

// Row is one held file, for the files table.
type Row struct {
	Key, Name, Kind, Version, Blake3 string
	Entries, Drift, Bytes            int
}

// Rows lists the held files.
func (s *State) Rows() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, len(s.files))
	for i, f := range s.files {
		out[i] = Row{Key: f.Path, Name: f.Name, Kind: string(f.Kind), Version: f.Version, Blake3: tr.Hash(f), Entries: len(f.Entries) + len(f.Extensions) + len(f.SpineEntries), Drift: s.local.DriftOf(f), Bytes: s.sizes[f]}
	}
	return out
}
