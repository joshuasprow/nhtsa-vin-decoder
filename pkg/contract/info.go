package contract

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// DataSourceEmbedded is Info.DataSource for the artifact built into the
// binary.
const DataSourceEmbedded = "embedded"

// Info describes the loaded build and data: GET /v1/info, `nitrovin info`,
// nitrovin_info.
type Info struct {
	DecoderVersion string `json:"decoder_version"`
	DataMonth      string `json:"data_month"`
	// ArtifactHash is "<algorithm>:<hex>" of the loaded artifact, the
	// algorithm being the artifact format's.
	ArtifactHash string `json:"artifact_hash"`
	// DataSource is DataSourceEmbedded, or the absolute path of the
	// artifact loaded with --data, NITROVIN_DATA or nitrovin_open.
	DataSource string   `json:"data_source"`
	Elements   Elements `json:"elements"`
	// Vocabularies are the files loaded beside the data with --vocab,
	// NITROVIN_VOCAB or nitrovin.NewTranslator, in load order; empty, never
	// null, when there are none.
	Vocabularies []VocabularyFile `json:"vocabularies"`
}

// VocabularyFile is one loaded vocabulary, extension set, declared spine
// or catalog in Info, with its drift against the data month (ADR 0011,
// ADR 0017).
type VocabularyFile struct {
	Name string `json:"name"`
	// Kind is "vocabulary", "extension-set", "spine" or "catalog".
	Kind    string `json:"kind"`
	Version string `json:"version"`
	// Release is a catalog's publisher's release, "" (absent) when it
	// gives none and for every other kind.
	Release string `json:"release,omitempty"`
	// Blake3 is the hex blake3 hash of the file's bytes.
	Blake3 string `json:"blake3"`
	// Path is where the file was loaded from: an absolute path for --vocab
	// and NITROVIN_VOCAB, the name given to vocab.Read otherwise.
	Path string `json:"path"`
	// Drift counts the file's drift findings by kind ("make", "model",
	// "qualifier", "extension", "model-years", "vehicle", "redirect",
	// "model-qualifier"), only the kinds it has, in that order; empty when
	// the file agrees with the data month.
	Drift []DriftCount `json:"drift"`
	// CatalogCounts are a catalog's, nil (absent) for every other kind.
	*CatalogCounts
}

// CatalogCounts are how many fitments, applications, redirects and
// retired numbers a catalog holds (ADR 0017).
type CatalogCounts struct {
	Fitments     int `json:"fitments"`
	Applications int `json:"applications"`
	Redirects    int `json:"redirects"`
	Retired      int `json:"retired"`
}

// DriftCount is how many drift findings of one kind a file has, with the
// first few as examples.
type DriftCount struct {
	Kind     string         `json:"kind"`
	Count    int            `json:"count"`
	Examples []DriftExample `json:"examples"`
}

// MaxDriftExamples is the most examples a DriftCount carries.
const MaxDriftExamples = 3

// DriftExample is one drift finding: its line in the file and what drifted.
type DriftExample struct {
	Line   int    `json:"line"`
	Detail string `json:"detail"`
}

// driftKinds orders a file's DriftCounts.
var driftKinds = []vocab.DriftKind{vocab.DriftMake, vocab.DriftModel, vocab.DriftQualifier, vocab.DriftExtension, vocab.DriftModelYears, vocab.DriftVehicle, vocab.DriftRedirect, vocab.DriftModelQualifier}

// DriftCounts counts the findings of drift about the file loaded from
// path by kind, keeping the first MaxDriftExamples of each: what Info
// says of the file, and what the pages show of it.
func DriftCounts(path string, drift []vocab.Finding) []DriftCount {
	out := []DriftCount{}
	for _, kind := range driftKinds {
		c := DriftCount{Kind: string(kind), Examples: []DriftExample{}}
		for _, d := range drift {
			if d.File != path || d.Drift != kind {
				continue
			}
			c.Count++
			if len(c.Examples) < MaxDriftExamples {
				c.Examples = append(c.Examples, DriftExample{Line: d.Line, Detail: d.Detail})
			}
		}
		if c.Count > 0 {
			out = append(out, c)
		}
	}
	return out
}

// Element is one public element of a data month.
type Element struct {
	ElementID   int32        `json:"element_id"`
	Variable    string       `json:"variable"`
	Code        string       `json:"code"`
	GroupName   Null[string] `json:"group_name"`
	DataType    string       `json:"data_type"`
	MultiValued bool         `json:"multi_valued"`
}

// Elements is a data month's public elements in ascending ElementID.
type Elements []Element

// Index is the position of element id in es, or -1.
func (es Elements) Index(id int32) int {
	if i, ok := slices.BinarySearchFunc(es, id, func(e Element, id int32) int { return cmp.Compare(e.ElementID, id) }); ok {
		return i
	}
	return -1
}

// Find returns element id.
func (es Elements) Find(id int32) (Element, bool) {
	if i := es.Index(id); i >= 0 {
		return es[i], true
	}
	return Element{}, false
}

// NewInfo describes artifact a loaded from source (DataSourceEmbedded or an
// absolute path) by decoder version version.
func NewInfo(a *artifact.Artifact, source, version string) (Info, error) {
	elements, err := PublicElements(a)
	if err != nil {
		return Info{}, err
	}
	h := a.Hash()
	return Info{
		DecoderVersion: version,
		DataMonth:      a.Month(),
		ArtifactHash:   "blake3:" + hex.EncodeToString(h[:]),
		DataSource:     source,
		Elements:       elements,
		Vocabularies:   []VocabularyFile{},
	}, nil
}

// PublicElements reads the data month's public elements from the
// artifact's element table: those spvindecode can output, with a non-empty
// Decode and not private (spvindecode.sql's projection, W11).
func PublicElements(a *artifact.Artifact) (Elements, error) {
	t := a.Table("element")
	if t == nil {
		return nil, fmt.Errorf("artifact %s has no element table", a.Month())
	}
	ids := t.Int32s("id")
	name, code, group := t.Strings("name"), t.Strings("code"), t.Strings("groupname")
	dataType, decode, private := t.Strings("datatype"), t.Strings("decode"), t.Bools("isprivate")
	var es Elements
	for i, id := range ids {
		if d, _ := decode.At(i); d == "" {
			continue
		}
		if p, _ := private.At(i); p {
			continue
		}
		e := Element{ElementID: id, MultiValued: MultiValued(id)}
		var ok [3]bool
		e.Variable, ok[0] = name.At(i)
		e.Code, ok[1] = code.At(i)
		e.DataType, ok[2] = dataType.At(i)
		if ok != [3]bool{true, true, true} {
			return nil, fmt.Errorf("public element %d lacks a name, code or data type", id)
		}
		e.GroupName.V, e.GroupName.Valid = group.At(i)
		es = append(es, e)
	}
	if !slices.IsSortedFunc(es, func(a, b Element) int { return cmp.Compare(a.ElementID, b.ElementID) }) {
		return nil, fmt.Errorf("artifact %s: element table is not in id order", a.Month())
	}
	return es, nil
}

// MultiValued reports whether element id can resolve to several attributes
// for one VIN: the elements spvindecode_core exempts from its per-element
// dedup (DedupExempt), plus Other Battery Info (1), which its Vehicle Specs
// step alone never counts as held, so a Vehicle Specs row can repeat an
// element 1 a pattern already set (#165). Package decoder's Vehicle Specs
// step reads it from here. It is the procedure's, not the data's; the data
// month decides which of them are public (2026_10: all but 186).
func MultiValued(id int32) bool {
	return id == 1 || DedupExempt(id)
}

// DedupExempt reports whether spvindecode_core's per-element dedup keeps
// every item of element id (spvindecode_core.sql:193), which package
// decoder's dedup reads from here.
func DedupExempt(id int32) bool {
	switch id {
	case 114, 121, 129, 150, 154, 155, 169, 186:
		return true
	}
	return false
}
