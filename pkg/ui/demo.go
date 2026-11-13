package ui

import (
	"bytes"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The demo set (#278): small, invented files that show fitment, stock and
// translation with no file of the visitor's own. demo/ holds them, each
// named demo-*: a part spine, a part vocabulary on it, a vehicle
// vocabulary of popular vPIC models, an extension set for vehicles vPIC
// lacks, a catalog of those vehicles, a shelf of another catalog release
// and a yard. Every number, location and stock row in them is invented and
// says so. `nitrovin serve --vocab pkg/ui/demo` serves the same files.
//
// A server with the worker's module publishes them among the release's
// assets, by blake3, as it does EPA's vocabulary, and the worker's panel
// offers them as one "Load the demo": vocab.js loads the vocabularies as
// one batch, compiles the catalog in the browser, and loads the shelf and
// the yard, whose VINs alone go to the server to be decoded. One catalog
// per nitrovin holds (ADR 0023): the offer is refused, in words, while
// the server or the browser holds another catalog. Remove the demo removes
// every file of it at once.
//
// demo/demo-examples.json is the list of requests that show each feature,
// each with the user stories of #278 it shows, grouped by the fit page's
// tab its from is asked on: the fit page offers each tab its own as its
// examples while the demo's catalog answers it, and docs/vocabularies.md
// walks through the same list, tab by tab.

//go:embed demo
var demoFS embed.FS

const (
	// demoDir is the demo set's directory in the package, and in the
	// repository pkg/ui/demo.
	demoDir = "demo"
	// demoExamplesName is the list of its requests, beside its files.
	demoExamplesName = "demo-examples.json"
)

// demoFile is one file of the demo set: its name (its header's), the file
// it is picked as, its kind, version and blake3 in hex, and its bytes.
type demoFile struct {
	Name, FileName, Kind, Version, Blake3 string
	data                                  []byte
}

// demoExample is one request of the demo set's list: the user stories of
// #278 it shows, the link's label and hint, and the link, a fit page's or
// the translate page's.
type demoExample struct {
	Stories []int  `json:"stories"`
	Label   string `json:"label"`
	Hint    string `json:"hint"`
	Href    string `json:"href"`
}

// demoSet is the demo set: its files in name order, as --vocab loads a
// directory, its catalog among them, and its list of requests.
type demoSet struct {
	files    []demoFile
	catalog  demoFile
	examples []demoExample
}

// theDemo is the demo set built in, read on first need. It is the
// package's own, so an error is a fault of the build.
var theDemo = sync.OnceValue(func() *demoSet {
	d, err := readDemo(demoFS)
	if err != nil {
		panic(err)
	}
	return d
})

// readDemo reads the demo set from fsys's demo directory: every *.jsonl
// file, each read alone (vocab.Read), and the list of requests.
func readDemo(fsys fs.FS) (*demoSet, error) {
	des, err := fs.ReadDir(fsys, demoDir)
	if err != nil {
		return nil, err
	}
	d := &demoSet{}
	for _, de := range des {
		if !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(demoDir, de.Name()))
		if err != nil {
			return nil, err
		}
		f, err := vocab.Read(de.Name(), bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("the demo set's %s: %w", de.Name(), err)
		}
		df := demoFile{Name: f.Name, FileName: de.Name(), Kind: string(f.Kind), Version: f.Version, Blake3: hex.EncodeToString(f.Hash[:]), data: b}
		d.files = append(d.files, df)
		if f.Kind == vocab.KindCatalog {
			d.catalog = df
		}
	}
	if d.catalog.Name == "" {
		return nil, fmt.Errorf("the demo set has no catalog")
	}
	b, err := fs.ReadFile(fsys, path.Join(demoDir, demoExamplesName))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &d.examples); err != nil {
		return nil, fmt.Errorf("the demo set's %s: %w", demoExamplesName, err)
	}
	return d, nil
}

// file is the demo's file named name, nil for none.
func (d *demoSet) file(name string) *demoFile {
	i := slices.IndexFunc(d.files, func(f demoFile) bool { return f.Name == name })
	if i < 0 {
		return nil
	}
	return &d.files[i]
}

// demoOffered reports whether the handler offers the demo set: on a server
// with the worker's module, which publishes it, and in the browser.
func (h *Handler) demoOffered() bool { return h.worker != nil || h.lib != nil }

// demoServerRefusal is why the demo set cannot load beside a server whose
// files are files, in words, "" when it can: the server has a catalog (one
// catalog per nitrovin), or a file named like one of the demo's (one name
// is one file). The panel refuses the offer with it, and no page tells a
// visitor to load the demo while it is not "".
func demoServerRefusal(files []contract.VocabularyFile) string {
	for _, f := range files {
		if f.Kind == string(vocab.KindCatalog) {
			return fmt.Sprintf("The demo brings its own catalog, and this server has one, %s: one catalog per nitrovin, so the demo cannot load here.", f.Name)
		}
	}
	for _, f := range files {
		if theDemo().file(f.Name) != nil {
			return fmt.Sprintf("This server loaded %s, named like a file of the demo's: one name is one file (ADR 0011), so the demo cannot load here.", f.Name)
		}
	}
	return ""
}

// serverFiles are the server's files as the handler knows them: its own
// on the server; in the browser, the ones the server gave the worker, its
// catalog among them.
func (h *Handler) serverFiles() []contract.VocabularyFile {
	if h.lib != nil {
		return h.lib.info.Vocabularies
	}
	return h.info().Vocabularies
}

// demoLoadable reports whether the demo set can load here: it is offered
// and the server's files refuse it no reason (demoServerRefusal). Only
// then does a page tell a visitor to load it.
func (h *Handler) demoLoadable() bool {
	return h.demoOffered() && demoServerRefusal(h.serverFiles()) == ""
}

// demoLoadText is what a page says of the demo where a request needs it:
// how to load it.
const demoLoadText = "This nitrovin offers a demo set to load into your browser in one click: open Vocabularies and Load the demo."

// isDemoCatalog reports whether info's catalog is the demo set's, by its
// name and version: the fit page then offers the demo's requests.
func isDemoCatalog(info contract.Info) bool {
	c := theDemo().catalog
	return slices.ContainsFunc(info.Vocabularies, func(f contract.VocabularyFile) bool {
		return f.Kind == string(vocab.KindCatalog) && f.Name == c.Name && f.Version == c.Version
	})
}

// demoExamples are the demo set's requests that what is loaded answers:
// each whose files are loaded, among cat (the vocabularies, extension sets
// and spines) or info's files (the catalog and stock), with the kinds of
// stock it asks for. A request of another page than the fit page loads
// whole (Elsewhere). The fit page keeps the current tab's (ownExamples).
func demoExamples(info contract.Info, cat []contract.CatalogEntry) []example {
	loaded := map[string]bool{}
	for _, f := range info.Vocabularies {
		loaded[f.Name] = true
	}
	for _, c := range cat {
		loaded[c.Name] = true
	}
	stock := stockLoaded(info)
	var out []example
	for _, e := range theDemo().examples {
		u, err := url.Parse(e.Href)
		if err != nil {
			continue
		}
		q := u.Query()
		if !slices.ContainsFunc(fitNamed(q), func(n string) bool { return !loaded[n] }) &&
			!slices.ContainsFunc(q["stock"], func(s string) bool { return !slices.Contains(stock, s) }) {
			out = append(out, example{Label: e.Label, Hint: e.Hint, Href: e.Href, Elsewhere: u.Path != fitPath})
		}
	}
	return out
}

// demoPublished is the demo's file published as name, its blake3 hash,
// nil for none.
func demoPublished(name, hash string) []byte {
	f := theDemo().file(name)
	if f == nil || f.Blake3 != hash {
		return nil
	}
	return f.data
}

// demoView is the demo set as the worker's panel offers it: why it cannot
// load here, in words ("" when it can), or whether this browser holds it,
// every file of this release's set linked; and else what a click loads
// (Load, JSON for vocab.js), the files of an older or partial demo it
// removes first (Stale) among it.
type demoView struct {
	Version string
	Size    int
	Refused string
	Held    bool
	Stale   []string
	// Names are the demo's files, which Remove the demo removes.
	Names []string
	Load  string
}

// SizeText is the demo set's size in words.
func (v *demoView) SizeText() string { return sizeText(v.Size) }

// demoLoad is what vocab.js loads of the demo set: each file at its URL,
// picked as its file name, and the files it removes first.
type demoLoad struct {
	Files  []demoLoadFile `json:"files"`
	Remove []string       `json:"remove"`
}

type demoLoadFile struct {
	Name string `json:"name"`
	File string `json:"file"`
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// demoPanel is the demo as the browser's panel shows it.
func (h *Handler) demoPanel() *demoView {
	d := theDemo()
	info := h.lib.Info()
	v := &demoView{Version: d.catalog.Version}
	load := demoLoad{Remove: []string{}}
	for _, f := range d.files {
		v.Size += len(f.data)
		v.Names = append(v.Names, f.Name)
		load.Files = append(load.Files, demoLoadFile{Name: f.Name, File: f.FileName, Kind: f.Kind,
			URL: FilePath(info, contract.VocabularyFile{Name: f.Name, Blake3: f.Blake3})})
	}
	if v.Refused = demoServerRefusal(h.serverFiles()); v.Refused != "" {
		return v
	}
	held := 0
	for _, f := range h.lib.Files() {
		df := d.file(f.Name)
		if f.Catalog && df == nil {
			v.Refused = fmt.Sprintf("The demo brings its own catalog, and this browser holds one, %s: one catalog per nitrovin. Remove %s to load the demo.", f.Name, f.Name)
			return v
		}
		if df == nil {
			continue
		}
		v.Stale = append(v.Stale, f.Name)
		hash := f.Blake3
		if f.Catalog {
			hash = f.Source
		}
		if f.Fault == "" && hash == df.Blake3 {
			held++
		}
	}
	if held == len(d.files) {
		v.Held, v.Stale = true, nil
		return v
	}
	load.Remove = append(load.Remove, v.Stale...)
	b, err := json.Marshal(load)
	if err != nil {
		panic(err)
	}
	v.Load = string(b)
	return v
}
