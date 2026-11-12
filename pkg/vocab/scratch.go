package vocab

import (
	"bufio"
	"bytes"
	"container/heap"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"slices"
)

// Scratch is the storage Compile sorts and assembles a compiled catalog in
// (ADR 0023): more than its memory holds, written once and read back. The
// CLI's is a temporary directory (DirScratch); a browser's can be
// IndexedDB.
type Scratch interface {
	// Create returns a new, empty scratch file.
	Create() (ScratchFile, error)
}

// ScratchFile is one file of Scratch: written front to back, then read in
// place. Compile calls ReadAt only after its last Write, and Close once,
// when it no longer needs the file, which may then be deleted.
type ScratchFile interface {
	io.Writer
	io.ReaderAt
	Close() error
}

// DirScratch is Scratch in directory dir: each file a temporary file there,
// removed when closed.
func DirScratch(dir string) Scratch { return dirScratch(dir) }

type dirScratch string

func (d dirScratch) Create() (ScratchFile, error) {
	f, err := os.CreateTemp(string(d), "nitrovin-compile-*")
	if err != nil {
		return nil, err
	}
	return tempFile{f}, nil
}

// tempFile is a DirScratch file.
type tempFile struct{ *os.File }

func (t tempFile) Close() error {
	err := t.File.Close()
	if rerr := os.Remove(t.Name()); err == nil {
		err = rerr
	}
	return err
}

// spill is a ScratchFile being written, buffered, and its size so far.
type spill struct {
	f    ScratchFile
	w    *bufio.Writer
	size int64
}

func newSpill(s Scratch) (*spill, error) {
	f, err := s.Create()
	if err != nil {
		return nil, err
	}
	return &spill{f: f, w: bufio.NewWriterSize(f, 256<<10)}, nil
}

func (s *spill) Write(b []byte) (int, error) {
	n, err := s.w.Write(b)
	s.size += int64(n)
	return n, err
}

// done ends writing: what follows reads.
func (s *spill) done() error { return s.w.Flush() }

// copyTo writes bytes [off, off+n) of s to w.
func (s *spill) copyTo(w io.Writer, off, n int64) error {
	_, err := io.Copy(w, io.NewSectionReader(s.f, off, n))
	return err
}

// sorter sorts records, each a key and a payload, by key: in memory, in
// runs of at most runBytes, each run sorted and written to one scratch
// file, then merged. Keys are unique, so the order is total.
type sorter struct {
	scratch Scratch
	buf     []byte   // the run being gathered: records, as written
	at      []uint32 // where each of buf's records starts
	runs    []span64 // the runs written, in out
	run     int      // the most bytes of a run
	out     *spill
	err     error
}

type span64 struct{ off, n int64 }

// runBytes is how much of the records a sorter holds in memory at once,
// and cursorBytes how much of each run a merge reads at a time, or more
// for a longer record; variables, for tests to make runs and reads tiny.
var (
	runBytes    = 16 << 20
	cursorBytes = 64 << 10
)

// newSorter is a sorter in s holding a run of at most runBytes/share.
func newSorter(s Scratch, share int) *sorter { return &sorter{scratch: s, run: runBytes / share} }

// add adds the record of key and payload.
func (s *sorter) add(key, payload []byte) {
	if len(s.buf)+len(key)+len(payload)+2*binary.MaxVarintLen32 > s.run && len(s.at) > 0 {
		s.spill()
	}
	s.at = append(s.at, uint32(len(s.buf)))
	s.buf = binary.AppendUvarint(s.buf, uint64(len(key)))
	s.buf = append(s.buf, key...)
	s.buf = binary.AppendUvarint(s.buf, uint64(len(payload)))
	s.buf = append(s.buf, payload...)
}

// record reads the record at b's start: its key, its payload and its
// length.
func record(b []byte) (key, payload []byte, n int) {
	k, i := binary.Uvarint(b)
	key = b[i : i+int(k)]
	n = i + int(k)
	p, j := binary.Uvarint(b[n:])
	payload = b[n+j : n+j+int(p)]
	return key, payload, n + j + int(p)
}

// spill sorts the run held and writes it out.
func (s *sorter) spill() {
	if s.err != nil {
		s.buf, s.at = s.buf[:0], s.at[:0]
		return
	}
	if s.out == nil {
		if s.out, s.err = newSpill(s.scratch); s.err != nil {
			return
		}
	}
	slices.SortFunc(s.at, func(a, b uint32) int {
		ka, _, _ := record(s.buf[a:])
		kb, _, _ := record(s.buf[b:])
		return bytes.Compare(ka, kb)
	})
	start := s.out.size
	for _, a := range s.at {
		_, _, n := record(s.buf[a:])
		if _, err := s.out.Write(s.buf[a : int(a)+n]); err != nil {
			s.err = err
			return
		}
	}
	s.runs = append(s.runs, span64{start, s.out.size - start})
	s.buf, s.at = s.buf[:0], s.at[:0]
}

// each calls f with every record in key order; key and payload are valid
// only until f returns. It ends the sorter, whose scratch is closed.
func (s *sorter) each(f func(key, payload []byte) error) (err error) {
	m, err := s.sorted()
	if err != nil {
		return err
	}
	defer func() {
		if cerr := m.close(); err == nil {
			err = cerr
		}
	}()
	for {
		key, payload, ok, err := m.next()
		if err != nil || !ok {
			return err
		}
		if err := f(key, payload); err != nil {
			return err
		}
	}
}

// sorted ends the sorter, returning its records in key order, merged from
// its runs; close closes its scratch.
func (s *sorter) sorted() (*merge, error) {
	if len(s.at) > 0 {
		s.spill()
	}
	s.buf, s.at = nil, nil
	m := &merge{out: s.out}
	s.out = nil
	if m.out != nil && s.err == nil {
		s.err = m.out.done()
	}
	if s.err != nil {
		m.close()
		return nil, s.err
	}
	for _, r := range s.runs {
		c := &runCursor{f: m.out.f, off: r.off, end: r.off + r.n, buf: make([]byte, 0, cursorBytes)}
		if ok, err := c.next(); err != nil {
			m.close()
			return nil, err
		} else if ok {
			m.h = append(m.h, c)
		}
	}
	heap.Init(&m.h)
	return m, nil
}

// merge is a sorter's records in key order, a record at a time.
type merge struct {
	h       runHeap
	out     *spill
	started bool
}

// next is the next record, ok false after the last; key and payload are
// valid until the next call.
func (m *merge) next() (key, payload []byte, ok bool, err error) {
	if m.started && len(m.h) > 0 {
		ok, err := m.h[0].next()
		switch {
		case err != nil:
			return nil, nil, false, err
		case ok:
			heap.Fix(&m.h, 0)
		default:
			heap.Pop(&m.h)
		}
	}
	m.started = true
	if len(m.h) == 0 {
		return nil, nil, false, nil
	}
	return m.h[0].key, m.h[0].payload, true, nil
}

// close closes the merge's scratch, if it has any.
func (m *merge) close() error {
	if m.out == nil {
		return nil
	}
	out := m.out
	m.out = nil
	return out.f.Close()
}

// runCursor reads one sorted run, a record at a time.
type runCursor struct {
	f            io.ReaderAt
	off, end     int64 // the next byte to read into buf, and the run's end
	buf          []byte
	at           int // the next record in buf
	key, payload []byte
}

// next reads the next record into key and payload; false at the run's
// end.
func (c *runCursor) next() (bool, error) {
	for {
		rest := c.buf[c.at:]
		if n, ok := whole(rest); ok {
			c.key, c.payload, _ = record(rest)
			c.at += n
			return true, nil
		}
		if c.off == c.end {
			if len(rest) > 0 {
				return false, fmt.Errorf("a sorted run is cut short")
			}
			return false, nil
		}
		// Keep the part-read record and read more after it, growing the
		// buffer for a record longer than it.
		keep := copy(c.buf[:cap(c.buf)], rest)
		if keep == cap(c.buf) {
			c.buf = append(c.buf[:keep], make([]byte, max(keep, 64))...)
		}
		c.buf = c.buf[:cap(c.buf)]
		n := min(int64(len(c.buf)-keep), c.end-c.off)
		if got, err := c.f.ReadAt(c.buf[keep:keep+int(n)], c.off); got < int(n) {
			return false, fmt.Errorf("a sorted run read %d of %d bytes: %v", got, n, err)
		}
		c.off += n
		c.buf, c.at = c.buf[:keep+int(n)], 0
	}
}

// whole reports whether b begins with a whole record, and its length.
func whole(b []byte) (int, bool) {
	k, i := binary.Uvarint(b)
	if i <= 0 || uint64(len(b)-i) < k {
		return 0, false
	}
	n := i + int(k)
	p, j := binary.Uvarint(b[n:])
	if j <= 0 || uint64(len(b)-n-j) < p {
		return 0, false
	}
	return n + j + int(p), true
}

// runHeap orders run cursors by their record's key.
type runHeap []*runCursor

func (h runHeap) Len() int           { return len(h) }
func (h runHeap) Less(i, j int) bool { return bytes.Compare(h[i].key, h[j].key) < 0 }
func (h runHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *runHeap) Push(x any)        { *h = append(*h, x.(*runCursor)) }
func (h *runHeap) Pop() any {
	old := *h
	c := old[len(old)-1]
	*h = old[:len(old)-1]
	return c
}

// appendOrdered appends s to b so that the bytes compare as the strings
// do and nothing appended after it changes that: each 0 byte doubled as 0
// 0xff, and 0 1 at the end.
func appendOrdered(b []byte, s string) []byte {
	for i := range len(s) {
		b = append(b, s[i])
		if s[i] == 0 {
			b = append(b, 0xff)
		}
	}
	return append(b, 0, 1)
}
