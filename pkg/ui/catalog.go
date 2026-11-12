package ui

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The browser's catalog (ADR 0023). A visitor picks a catalog's JSON
// Lines; a dedicated worker the page starts compiles it (Compile) with the
// same streaming compiler as `nitrovin vocab compile`, sorting in
// IndexedDB (a vocab.Scratch) and streaming the compiled file into
// IndexedDB (a CatalogWriter), and the Service Worker reads it in place
// from there (Disk.Catalog), linked with the rest of the browser's files.

// recompile is what a stored catalog left out says to do: it is never
// compiled again behind the visitor's back.
const recompile = "Pick the source again to recompile."

// openCatalog is the stored catalog rec, its bytes read through r, opened
// in place; its fault says why it is left out when it does not open.
func openCatalog(rec Record, r io.ReaderAt) *held {
	h := &held{name: rec.Name, rec: rec, catalog: true}
	f, err := vocab.OpenCompiled(browserPath(rec.FileName, rec.Name), r, int64(rec.Size))
	switch {
	case err != nil:
		h.fault = faultText(err)
	case f.Name != rec.Name:
		h.fault = fmt.Sprintf("its header names it %q now", f.Name)
	case hex.EncodeToString(f.Hash[:]) != rec.Blake3:
		h.fault = fmt.Sprintf("its bytes changed: blake3 %x, not %s", f.Hash, rec.Blake3)
	default:
		h.file = f
	}
	return h
}

// catalogFile is h, the browser's linked catalog, as /v1/info lists a
// loaded catalog: its counts, and what its compiling recorded; its drift
// is the Library's to count.
func catalogFile(h *held) contract.VocabularyFile {
	f := h.file
	ci := f.Catalog().Info()
	v := contract.VocabularyFile{Name: h.name, Kind: string(vocab.KindCatalog), Version: f.Version, Release: ci.Release, Blake3: h.rec.Blake3, Path: f.Path,
		CatalogCounts: &contract.CatalogCounts{Fitments: ci.Fitments, Applications: ci.Applications, Redirects: ci.Redirects, Retired: ci.Retired}}
	if cc := f.Compiled(); cc != nil {
		v.Compiled = &contract.CompiledCatalog{Format: cc.Format, SourceBlake3: hex.EncodeToString(cc.SourceHash[:]), Writer: cc.Writer, DriftMonth: cc.DriftMonth, SkippedDrift: []string{}}
		for _, k := range cc.Skipped {
			v.Compiled.SkippedDrift = append(v.Compiled.SkippedDrift, string(k))
		}
	}
	return v
}

// changedLinks is why f, a compiled catalog, is stale among files: its
// spine or vehicle vocabulary loaded is another file than the one it was
// compiled against. "" when it is not, or f is no compiled catalog; a
// file it was compiled against that is not loaded at all is Link's fault.
func changedLinks(f *vocab.File, files []*vocab.File) string {
	cc := f.Compiled()
	if cc == nil {
		return ""
	}
	for _, link := range cc.Links {
		i := slices.IndexFunc(files, func(g *vocab.File) bool { return g.Name == link.Name })
		if i < 0 || files[i].Hash == link.Hash {
			continue
		}
		what := "vehicle vocabulary"
		if files[i].Kind == vocab.KindSpine {
			what = "spine"
		}
		return fmt.Sprintf("its %s %q changed since it was compiled (version %s then, %s now; blake3 %x then, %x now).",
			what, link.Name, link.Version, files[i].Version, link.Hash[:4], files[i].Hash[:4])
	}
	return ""
}

// Compile compiles src, the JSON Lines catalog the visitor picked as
// fileName, of size bytes, into the browser as its catalog (ADR 0023): as
// `nitrovin vocab compile` does, checked against the files the browser
// links (its spine and vehicle vocabulary among them, the server's or the
// browser's) and the release's spine bundle, which carries no model years
// and no model values, so the compiled file records model-years and
// model-qualifier as skipped. src is read once, front to back, never
// held; the compile sorts in scratch, and the compiled file streams into
// the Disk's CatalogWriter, committed only once it opens and links.
//
// It refuses while the server has a catalog or the browser holds one that
// links: one catalog per nitrovin, counting the server and its browsers
// together. A stored catalog left out (stale, or one that no longer
// opens) is what "pick the source again" replaces: the compile takes its
// place, and its parts go once the new one is stored. A
// catalog with faults, one that does not link, and storage the browser
// refuses store nothing; the error says why in words. progress is told
// each step and how far along it is. It returns the stored catalog as the
// pages list it.
func (l *Library) Compile(src io.Reader, fileName string, size int64, scratch vocab.Scratch, progress func(string)) (StoredFile, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur := l.state.Load()
	if l.serverCatalog != "" {
		return StoredFile{}, &pageError{Status: http.StatusConflict, Message: fmt.Sprintf(
			"This server has a catalog, %s; one catalog per nitrovin, so this browser compiles none. Fit on this nitrovin answers from the server's catalog.", l.serverCatalog)}
	}
	if i := slices.IndexFunc(cur.browser, func(h *held) bool { return h.catalog && h.linked() }); i >= 0 {
		return StoredFile{}, &pageError{Status: http.StatusConflict, Message: fmt.Sprintf(
			"This browser holds a catalog already, %s; one catalog per nitrovin. Remove it to compile %s.", cur.browser[i].name, fileName)}
	}
	// A catalog left out is replaced: it is no longer among the browser's
	// files from here on.
	kept := slices.DeleteFunc(slices.Clone(cur.browser), func(h *held) bool { return h.catalog })
	var files []*vocab.File
	for _, h := range cur.all() {
		if h.linked() {
			files = append(files, h.file)
		}
	}
	out, err := l.disk.NewCatalog()
	if err != nil {
		return StoredFile{}, storageError(fileName, err)
	}
	h, err := l.compile(out, src, fileName, size, files, append(slices.Clone(cur.server), kept...), scratch, progress)
	if err != nil {
		if aerr := out.Abort(); aerr != nil {
			err = fmt.Errorf("%w; and what was written could not be dropped: %v", err, aerr)
		}
		return StoredFile{}, err
	}
	progress("Linking " + h.name + "…")
	next := &shelf{server: cur.server, browser: append(kept, h)}
	slices.SortFunc(next.browser, func(a, b *held) int { return strings.Compare(a.name, b.name) })
	set, dropped, err := l.link(next.all(), append(cur.linkedServer(), h))
	if err == nil && breaks(kept, dropped) != "" {
		err = &pageError{Status: http.StatusConflict, Message: "It would leave stored files unable to link: " + breaks(kept, dropped)}
	}
	if err == nil {
		progress("Storing " + h.name + "…")
		if err = out.Commit(h.rec); err != nil {
			err = storageError(fileName, err)
		}
	}
	if err != nil {
		if aerr := out.Abort(); aerr != nil {
			err = fmt.Errorf("%w; and what was written could not be dropped: %v", err, aerr)
		}
		return StoredFile{}, err
	}
	next.set = next.relink(set, dropped)
	l.state.Store(next)
	for _, f := range l.Files() {
		if f.Catalog {
			return f, nil
		}
	}
	return StoredFile{}, fmt.Errorf("the catalog %s was stored but is not listed", h.name)
}

// compile compiles src into out, and opens what it wrote: the catalog,
// not yet linked or committed. Its name must be none of others'.
func (l *Library) compile(out CatalogWriter, src io.Reader, fileName string, size int64, files []*vocab.File, others []*held, scratch vocab.Scratch, progress func(string)) (*held, error) {
	r := &readProgress{r: src, size: size, name: fileName, progress: progress}
	w := &writeProgress{w: out, name: fileName, progress: progress}
	progress("Reading " + fileName + "…")
	if _, err := vocab.Compile(w, l.month, files, r, fileName, scratch, vocab.CompileOptions{Writer: l.info.DecoderVersion}); err != nil {
		if _, ok := errors.AsType[vocab.Faults](err); ok {
			return nil, &pageError{Status: http.StatusUnprocessableEntity, Message: "It was not compiled: " + faultText(err)}
		}
		return nil, storageError(fileName, err)
	}
	written, err := out.Written()
	if err != nil {
		return nil, storageError(fileName, err)
	}
	f, err := vocab.OpenCompiled(fileName, written, w.n)
	if err == nil && browserPath(fileName, f.Name) != fileName {
		f, err = vocab.OpenCompiled(browserPath(fileName, f.Name), written, w.n)
	}
	if err != nil {
		return nil, fmt.Errorf("the compiled %s does not open: %s", fileName, faultText(err))
	}
	if slices.ContainsFunc(others, func(h *held) bool { return h.name == f.Name }) {
		return nil, &pageError{Status: http.StatusConflict, Message: fmt.Sprintf(
			"%s is named %q, like a file loaded already: one name is one file (ADR 0011), so rename it in its header to compile it here.", fileName, f.Name)}
	}
	return &held{name: f.Name, catalog: true, file: f, rec: Record{
		Name: f.Name, FileName: fileName, Blake3: hex.EncodeToString(f.Hash[:]), Size: int(w.n), Stored: l.now().UTC().Truncate(time.Second),
	}}, nil
}

// storageError is err, met compiling or storing fileName, in words: a
// quota the browser refused says so, and so does a private window, where
// some browsers (Safari's private browsing) store no Blob in IndexedDB.
func storageError(fileName string, err error) error {
	if strings.Contains(err.Error(), "Error preparing Blob") {
		return &pageError{Status: http.StatusInsufficientStorage, Message: fmt.Sprintf(
			"This browser keeps no files in a private window, so %s was not compiled and nothing was stored. Open nitrovin in a normal window to compile a catalog.", fileName)}
	}
	if strings.Contains(err.Error(), "QuotaExceeded") {
		return &pageError{Status: http.StatusInsufficientStorage, Message: fmt.Sprintf(
			"This browser refused the storage compiling %s needs (%v), so nothing was stored. Free some space for this site, or compile it on a server with nitrovin vocab compile.", fileName, err)}
	}
	return fmt.Errorf("compiling %s: %w", fileName, err)
}

// readProgress is a catalog's source, read: it tells progress how much,
// each whole percent.
type readProgress struct {
	r        io.Reader
	size, n  int64
	said     int64
	name     string
	progress func(string)
}

func (p *readProgress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.n += int64(n)
	if pc := p.n * 100 / max(p.size, 1); pc > p.said && pc < 100 {
		p.said = pc
		p.progress(fmt.Sprintf("Reading %s: %d%% of %s…", p.name, pc, sizeText(int(p.size))))
	}
	return n, err
}

// writeProgress is a compiled catalog, written: it tells progress how
// much, every progressBytes.
type writeProgress struct {
	w        io.Writer
	n, said  int64
	name     string
	progress func(string)
}

// progressBytes is how much of the compiled file is written between two
// words of progress.
const progressBytes = 8 << 20

func (p *writeProgress) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	if p.n-p.said >= progressBytes {
		p.said = p.n
		p.progress("Writing " + p.name + " compiled: " + sizeText(int(p.n)) + " so far…")
	}
	return n, err
}

// CatalogText says what a compiled catalog holds, for the load's word:
// "parts-catalog (790,000 fitments, 342 MB)".
func CatalogText(f StoredFile) string {
	return f.Name + " (" + thousands(f.Fitments) + " fitments, " + f.SizeText() + ")"
}

// FitmentsText is a catalog's count of fitments in words: "790,000".
func (f StoredFile) FitmentsText() string { return thousands(f.Fitments) }

// SkippedText is the drift kinds a catalog's compile skipped, in words:
// "model-years, model-qualifier".
func (f StoredFile) SkippedText() string { return strings.Join(f.Skipped, ", ") }

// thousands is n with its thousands separated: 790,000.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
