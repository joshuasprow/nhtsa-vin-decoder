package main

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// Elements a qualifier names (ADR 0009).
const (
	elementDriveType = 15 // lookup DriveType
	elementModel     = 28
	elementSeries    = 34 // text
)

// modelValues are, per vPIC model, the Drive Type attribute ids and Series
// texts its VIN schemas' patterns give: the patterns of every schema with a
// Model pattern for it. A qualifier holds only values its model's schemas
// emit, which vary by model (2WD trucks decode 4x2, Toyota's AWD decodes
// 4WD), so they come from here and not from one table.
type modelValues struct {
	drive  map[int32]map[int32]bool
	series map[int32]map[string]bool
}

func readModelValues(a *artifact.Artifact) *modelValues {
	p := a.Table("pattern")
	schemas, elements, values := p.Int32s("vinschemaid"), p.Int32s("elementid"), p.Strings("attributeid")
	schemaModels := map[int32][]int32{}
	schemaDrive := map[int32]map[int32]bool{}
	schemaSeries := map[int32]map[string]bool{}
	for r, el := range elements {
		s := schemas[r]
		switch el {
		case elementModel:
			v, _ := values.At(r)
			if id, err := strconv.Atoi(v); err == nil {
				schemaModels[s] = append(schemaModels[s], int32(id))
			}
		case elementDriveType:
			v, _ := values.At(r)
			if id, err := strconv.Atoi(v); err == nil {
				if schemaDrive[s] == nil {
					schemaDrive[s] = map[int32]bool{}
				}
				schemaDrive[s][int32(id)] = true
			}
		case elementSeries:
			if v, ok := values.At(r); ok && v != "" {
				if schemaSeries[s] == nil {
					schemaSeries[s] = map[string]bool{}
				}
				schemaSeries[s][v] = true
			}
		}
	}
	mv := &modelValues{drive: map[int32]map[int32]bool{}, series: map[int32]map[string]bool{}}
	for s, ms := range schemaModels {
		for _, m := range ms {
			if mv.drive[m] == nil {
				mv.drive[m] = map[int32]bool{}
				mv.series[m] = map[string]bool{}
			}
			maps.Copy(mv.drive[m], schemaDrive[s])
			maps.Copy(mv.series[m], schemaSeries[s])
		}
	}
	return mv
}

// vPIC's Drive Type attribute ids (2026_10 lookup drivetype).
const (
	driveFWD = 1 // FWD/Front-Wheel Drive
	drive4WD = 2 // 4WD/4-Wheel Drive/4x4
	driveAWD = 3 // AWD/All-Wheel Drive
	driveRWD = 4 // RWD/Rear-Wheel Drive
	drive4x2 = 7 // 4x2
)

// driveColumn is what each value of EPA's drive column can decode to:
// 2WD trucks and SUVs decode 4x2, and some makers' AWD decodes 4WD.
var driveColumn = map[string][]int32{
	"Front-Wheel Drive":          {driveFWD, drive4x2},
	"Rear-Wheel Drive":           {driveRWD, drive4x2},
	"2-Wheel Drive":              {driveFWD, driveRWD, drive4x2},
	"All-Wheel Drive":            {drive4WD, driveAWD},
	"4-Wheel or All-Wheel Drive": {drive4WD, driveAWD},
	"4-Wheel Drive":              {drive4WD, driveAWD},
	"Part-time 4-Wheel Drive":    {drive4WD, driveAWD},
}

// driveToken is the same for a drive token in the name, the fallback when
// no row has a drive column value; the maker drive names are all 4WD or AWD.
func driveToken(t string) []int32 {
	switch t {
	case "fwd":
		return driveColumn["Front-Wheel Drive"]
	case "rwd":
		return driveColumn["Rear-Wheel Drive"]
	case "2wd", "4x2":
		return driveColumn["2-Wheel Drive"]
	}
	return driveColumn["4-Wheel Drive"]
}

// driveQualifier is the Drive Type values an entry whose name carries a
// drive token holds for on model: what its rows' drive column (or else
// the token) says, narrowed to what the model's schemas emit. None when
// the name carries no token or nothing is left.
func (mv *modelValues) driveQualifier(e *entry, normModel string, model int32) []int32 {
	tok, ok := impliesDrive(normModel)
	if !ok {
		return nil
	}
	want := map[int32]bool{}
	for d := range e.drives {
		for _, v := range driveColumn[d] {
			want[v] = true
		}
	}
	if len(want) == 0 {
		for _, v := range driveToken(tok) {
			want[v] = true
		}
	}
	var out []int32
	for v := range want {
		if mv.drive[model][v] {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// seriesMakes are the EPA makes whose model names carry a tonnage or GM
// chassis code that vPIC decodes as Series. Ram from 2013 and Ford make
// the tonnage the model.
var seriesMakes = []string{"chevrolet", "gmc", "dodge"}

var (
	// tonnageToken is a tonnage in an EPA name: 1500, K2500, 2500HD.
	tonnageToken = regexp.MustCompile(`^[ckrv]?(1500|2500|3500)(hd)?$`)
	// chassisToken is a GM (C10, K25) or Dodge (D150, W250) chassis code.
	chassisToken = regexp.MustCompile(`^(?:[ckrv](1|2|3)[05]|[dw](1|2|3)[05]0)$`)
	// seriesNumber is a tonnage in a vPIC Series text: 1500, DS (1500),
	// 3500W, 2500 HD.
	seriesNumber = regexp.MustCompile(`^[a-z]{0,2}(1500|2500|3500)[a-z]{0,2}$`)
	seriesSplit  = regexp.MustCompile(`[^a-z0-9/]+`)
)

// tonnage is the class ("1500", "2500", "3500") a normalized EPA model of
// a seriesMakes make names, "" for none.
func tonnage(normModel string) string {
	for _, t := range strings.Fields(normModel) {
		for _, part := range strings.Split(t, "/") {
			if m := tonnageToken.FindStringSubmatch(part); m != nil {
				return m[1]
			}
			if m := chassisToken.FindStringSubmatch(part); m != nil {
				return m[1] + m[2] + "500"
			}
		}
	}
	return ""
}

// tonPhrase is each class's tonnage in words, as vPIC also spells it.
var tonPhrase = map[string]string{"1500": "1/2 ton", "2500": "3/4 ton", "3500": "1 ton"}

// seriesClass reports whether vPIC Series text names tonnage class c.
func seriesClass(text, c string) bool {
	toks := seriesSplit.Split(strings.ToLower(text), -1)
	for i, t := range toks {
		if m := seriesNumber.FindStringSubmatch(t); m != nil && m[1] == c {
			return true
		}
		if i+1 < len(toks) && t+" "+toks[i+1] == tonPhrase[c] {
			return true
		}
	}
	return false
}

// seriesQualifier is the Series texts an entry naming a tonnage holds for
// on model: every spelling of that tonnage the model's schemas emit. None
// for other makes, other names, or when the model emits none.
func (mv *modelValues) seriesQualifier(e *entry, normModel string, model int32) []string {
	if !slices.Contains(seriesMakes, e.makeKey) {
		return nil
	}
	c := tonnage(normModel)
	if c == "" {
		return nil
	}
	var out []string
	for v := range mv.series[model] {
		if seriesClass(v, c) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}
