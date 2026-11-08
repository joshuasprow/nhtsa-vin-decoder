package contract

import (
	"cmp"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
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
// dedup (spvindecode_core.sql:193), which package decoder's dedup reads from
// here. It is the procedure's, not the data's; the data month decides which
// of them are public (2026_10: all but 186).
func MultiValued(id int32) bool {
	switch id {
	case 114, 121, 129, 150, 154, 155, 169, 186:
		return true
	}
	return false
}
