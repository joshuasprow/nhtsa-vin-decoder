package main

import (
	"regexp"
	"slices"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The rules are the research's (docs/research/epa-vocabulary.md on the
// research/epa-vocabulary branch, #144): an entry's make resolves by name
// or through makeTable, then its model through an ordered cascade in which
// the first rule that hits wins:
//
//  1. exact: the model name.
//  2. stripped model: the name with drive tokens, maker drive names and
//     parentheticals dropped, then trailing body and fuel words, one at a
//     time; the least-stripped candidate first.
//  3. longest prefix: the longest vPIC model name that is a whole-token
//     prefix of a candidate of 2, unless baseModel names another model
//     exactly that is not a prefix of it (the guard: sent to curation).
//  4. baseModel: EPA's baseModel values, exact, then stripped as in 2.
//  5. punctuation-insensitive: 1 to 4 with spaces, '-', '_', '.' and '\”
//     removed from both sides ('/' kept for 7).
//  6. make-name prefix: 1 to 3 and 5 on "<make> <model>" and on the model
//     less a leading make name, never landing on a vPIC model named like
//     the make.
//  7. slash alternatives: each '/' part of the model through 1 to 3, 5 and
//     6, one mapping per distinct model.
//
// Each rule is named in its mappings' note.
const (
	ruleExact    = "exact"
	ruleStripped = "stripped model"
	rulePrefix   = "longest prefix"
	ruleBase     = "baseModel"
	ruleFolded   = "punctuation-insensitive"
	ruleMakeName = "make-name prefix"
	ruleSlash    = "slash alternatives"
)

// makeAlias is one row of the make spelling table: an EPA make that is not
// a vPIC make's name, and the vPIC makes its models are under.
type makeAlias struct {
	makes []int32
	// prefix is put before the model's name: Scion's models are Toyota's
	// "Scion xB".
	prefix string
}

// makeTable is the research's make spelling table, by normalized EPA make.
var makeTable = map[string]makeAlias{
	"mclaren automotive":          {makes: []int32{2236}},
	"saleen performance":          {makes: []int32{4355}},
	"sterling":                    {makes: []int32{5555}},
	"ineos automotive":            {makes: []int32{12360}},
	"bugatti rimac":               {makes: []int32{454}},
	"american motors corporation": {makes: []int32{1124}},
	"ruf automobile":              {makes: []int32{12550}},
	"ruf automobile gmbh":         {makes: []int32{12550}},
	"avanti motor corporation":    {makes: []int32{5552}},
	"panoz auto-development":      {makes: []int32{4220}},
	"coda automotive":             {makes: []int32{1777}},
	"london taxi":                 {makes: []int32{5767}},
	"london coach co inc":         {makes: []int32{5767}},
	"excalibur autos":             {makes: []int32{7477}},
	"consulier industries inc":    {makes: []int32{10249, 5557}},
	"laforza automobile inc":      {makes: []int32{6307}},
	"panther car company limited": {makes: []int32{5938}},
	"asc incorporated":            {makes: []int32{1194}},
	"srt":                         {makes: []int32{476}}, // Dodge
	"bmw alpina":                  {makes: []int32{452}}, // BMW
	"vpg":                         {makes: []int32{606}}, // AM General: MV-1
	"mobility ventures llc":       {makes: []int32{606}}, // vPIC's Mobility Ventures has no models
	"roush performance":           {makes: []int32{460}}, // Ford
	"scion":                       {makes: []int32{448}, prefix: "scion "},
	"sti":                         {makes: []int32{523}}, // Subaru
}

// driveTokens are the drive suffixes, and makerDrives the maker drive
// names, that rule 2 drops wherever they stand in a name; both imply a
// Drive Type qualifier.
var (
	driveTokens = []string{"2wd", "4wd", "awd", "fwd", "rwd", "4x4", "4x2"}
	makerDrives = []string{"4matic", "4matic+", "4motion", "xdrive", "sdrive", "quattro", "all4", "sh-awd", "e-four"}
)

// trailingWords are the body and fuel words rule 2 drops from the end of a
// name, one at a time.
var trailingWords = []string{
	"wagon", "pickup", "van", "ffv", "hybrid", "coupe", "convertible", "sedan",
	"cabriolet", "roadster", "cab", "chassis", "hatchback", "cng", "phev", "mhev",
	"ev", "electric",
}

var parenthetical = regexp.MustCompile(`\([^()]*\)`)

// candidates are a normalized name and the names rule 2 strips it to,
// least stripped first, without repeats.
func candidates(s string) []string {
	out := []string{s}
	add := func(c string) bool {
		if c == "" || slices.Contains(out, c) {
			return false
		}
		out = append(out, c)
		return true
	}
	clean := func(c string) string {
		c = parenthetical.ReplaceAllString(c, " ")
		f := strings.Fields(c)
		f = slices.DeleteFunc(f, func(t string) bool {
			return slices.Contains(driveTokens, t) || slices.Contains(makerDrives, t)
		})
		return strings.Join(f, " ")
	}
	c := clean(s)
	add(c)
	for {
		f := strings.Fields(c)
		if len(f) < 2 || !slices.Contains(trailingWords, f[len(f)-1]) {
			break
		}
		c = clean(strings.Join(f[:len(f)-1], " "))
		add(c)
	}
	return out
}

// impliesDrive reports whether a normalized name carries a drive suffix or
// a maker drive name, and the first one it carries.
func impliesDrive(s string) (string, bool) {
	for _, t := range strings.Fields(parenthetical.ReplaceAllString(s, " ")) {
		if slices.Contains(driveTokens, t) || slices.Contains(makerDrives, t) {
			return t, true
		}
	}
	return "", false
}

// fold is a name punctuation- and space-insensitive: rule 5.
func fold(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '-', '_', '.', '\'':
			return -1
		}
		return r
	}, s)
}

// models indexes the vPIC models under the makes one EPA make resolves to.
type models struct {
	names    map[int32]string   // id → normalized name
	byName   map[string]int32   // normalized name → id, the lowest on a tie
	byFold   map[string][]int32 // folded name → ids, ascending
	makeName []string           // the makes' names and the EPA make, normalized and folded
}

func newModels(m vocab.Month, makes []int32, epaMake string) *models {
	ix := &models{names: map[int32]string{}, byName: map[string]int32{}, byFold: map[string][]int32{}}
	names := []string{epaMake}
	for _, mk := range makes {
		if n, ok := m.Make(mk); ok {
			names = append(names, vocab.Normalize(n))
		}
		for id, name := range m.Models(mk) {
			n := vocab.Normalize(name)
			ix.names[id] = n
			if old, ok := ix.byName[n]; !ok || id < old {
				ix.byName[n] = id
			}
			ix.byFold[fold(n)] = append(ix.byFold[fold(n)], id)
		}
	}
	for k, ids := range ix.byFold {
		slices.Sort(ids)
		ix.byFold[k] = slices.Compact(ids)
	}
	for _, n := range names {
		ix.makeName = append(ix.makeName, n, fold(n))
	}
	return ix
}

// lookup finds a model by normalized name, or by folded name when folded;
// skipMake refuses models named like the make (rule 6). A folded name two
// models share finds neither.
func (ix *models) lookup(s string, folded, skipMake bool) (int32, bool) {
	var id int32
	if folded {
		ids := ix.byFold[fold(s)]
		if len(ids) != 1 {
			return 0, false
		}
		id = ids[0]
	} else {
		var ok bool
		if id, ok = ix.byName[s]; !ok {
			return 0, false
		}
	}
	if skipMake && slices.Contains(ix.makeName, fold(ix.names[id])) || skipMake && slices.Contains(ix.makeName, ix.names[id]) {
		return 0, false
	}
	return id, true
}

// prefix is rule 3 over cands: the longest vPIC model name that is a
// whole-token proper prefix of one of them.
func (ix *models) prefix(cands []string, folded, skipMake bool) (int32, bool) {
	var best int32
	bestLen := 0
	for _, c := range cands {
		f := strings.Fields(c)
		for k := len(f) - 1; k >= 1; k-- {
			p := strings.Join(f[:k], " ")
			id, ok := ix.lookup(p, folded, skipMake)
			if !ok {
				continue
			}
			if n := len(fold(ix.names[id])); n > bestLen {
				best, bestLen = id, n
			}
			break
		}
	}
	return best, bestLen > 0
}

// hit is one model an entry resolved to and the rule that found it.
type hit struct {
	model int32
	rule  string
}

// nameRules is rules 1 to 3 on one name, exact or folded. guard is the
// model baseModel names exactly, 0 for none: a prefix hit it disagrees
// with is refused.
func (ix *models) nameRules(s string, folded, skipMake bool, guard int32) (h hit, ok, guarded bool) {
	cands := candidates(s)
	exact, stripped := ruleExact, ruleStripped
	if folded {
		exact, stripped = ruleFolded, ruleFolded
	}
	if id, ok := ix.lookup(cands[0], folded, skipMake); ok {
		return hit{id, exact}, true, false
	}
	for _, c := range cands[1:] {
		if id, ok := ix.lookup(c, folded, skipMake); ok {
			return hit{id, stripped}, true, false
		}
	}
	id, ok := ix.prefix(cands, folded, skipMake)
	if !ok {
		return hit{}, false, false
	}
	if guard != 0 && guard != id && !isTokenPrefix(ix.names[guard], ix.names[id]) {
		return hit{}, false, true
	}
	rule := rulePrefix
	if folded {
		rule = ruleFolded
	}
	return hit{id, rule}, true, false
}

// isTokenPrefix reports whether p is s or a whole-token prefix of it.
func isTokenPrefix(p, s string) bool {
	return p == s || strings.HasPrefix(s, p+" ")
}

// resolution is what the rules make of one entry.
type resolution struct {
	hits []hit
	// guarded: the guard refused a prefix hit, so the entry goes to
	// curation.
	guarded bool
}

// resolve runs the cascade on an entry's normalized model and baseModels
// within ix. prefix is the make table's model prefix.
func (ix *models) resolve(model string, bases []string, prefix string) resolution {
	if prefix != "" {
		model = prefix + model
		prefixed := make([]string, len(bases))
		for i, b := range bases {
			prefixed[i] = prefix + b
		}
		bases = prefixed
	}
	var guard int32
	for _, b := range bases {
		if id, ok := ix.lookup(b, false, false); ok {
			guard = id
			break
		}
	}
	var r resolution
	for _, folded := range []bool{false, true} {
		h, ok, guarded := ix.nameRules(model, folded, false, guard)
		if ok {
			if folded && h.rule != ruleFolded {
				h.rule = ruleFolded
			}
			return resolution{hits: []hit{h}}
		}
		r.guarded = r.guarded || guarded
		if r.guarded {
			return r
		}
		for _, b := range bases {
			for _, c := range candidates(b) {
				if id, ok := ix.lookup(c, folded, false); ok {
					rule := ruleBase
					if folded {
						rule = ruleFolded
					}
					return resolution{hits: []hit{{id, rule}}}
				}
			}
		}
	}
	if h, ok := ix.makeNamePrefix(model); ok {
		return resolution{hits: []hit{h}}
	}
	if strings.Contains(model, "/") {
		for part := range strings.SplitSeq(model, "/") {
			part = vocab.Normalize(part)
			if part == "" {
				continue
			}
			h, ok := ix.part(part)
			if ok && !slices.ContainsFunc(r.hits, func(x hit) bool { return x.model == h.model }) {
				r.hits = append(r.hits, hit{h.model, ruleSlash})
			}
		}
	}
	return r
}

// part resolves one slash alternative: rules 1 to 3, 5 and 6.
func (ix *models) part(s string) (hit, bool) {
	for _, folded := range []bool{false, true} {
		if h, ok, _ := ix.nameRules(s, folded, false, 0); ok {
			return h, true
		}
	}
	return ix.makeNamePrefix(s)
}

// makeNamePrefix is rule 6: "<make> <model>", then the model less a
// leading make name.
func (ix *models) makeNamePrefix(model string) (hit, bool) {
	var names []string
	for i := 0; i < len(ix.makeName); i += 2 {
		mk := ix.makeName[i]
		names = append(names, mk+" "+model)
		if rest, ok := strings.CutPrefix(model, mk+" "); ok {
			names = append(names, rest)
		}
	}
	for _, n := range names {
		for _, folded := range []bool{false, true} {
			if h, ok, _ := ix.nameRules(n, folded, true, 0); ok {
				return hit{h.model, ruleMakeName}, true
			}
		}
	}
	return hit{}, false
}
