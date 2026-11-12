package main

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Elements a qualifier names (ADR 0009).
const (
	elementDriveType = 15 // lookup DriveType
	elementSeries    = 34 // text
)

// modelValues are a month's per-model values (ADR 0015) for the two
// elements a derived qualifier names. A qualifier holds only values its
// own model decodes to, which vary by model (2WD trucks decode 4x2,
// Toyota's AWD decodes 4WD), so they come from the model and not from one
// table, nor from other models sharing its VIN schemas.
type modelValues struct{ m vocab.ModelMonth }

// drive is the Drive Type attribute ids model decodes to.
func (mv modelValues) drive(model int32) map[int32]bool {
	vs, _ := mv.m.ModelValues(model, elementDriveType)
	out := make(map[int32]bool, len(vs))
	for _, v := range vs {
		if id, err := strconv.Atoi(v); err == nil {
			out[int32(id)] = true
		}
	}
	return out
}

// vPIC's Drive Type attribute ids (2026_10 lookup drivetype).
const (
	driveFWD = 1 // FWD/Front-Wheel Drive
	drive4WD = 2 // 4WD/4-Wheel Drive/4x4
	driveAWD = 3 // AWD/All-Wheel Drive
	driveRWD = 4 // RWD/Rear-Wheel Drive
	drive4x2 = 7 // 4x2
)

// driveColumn is, for each value of EPA's drive column, the Drive Type
// ids it can decode to, as tiers in order of preference: the ids that say
// what EPA says first, then the ones a model decodes the same drive as
// (2WD trucks and SUVs decode 4x2; some makers' AWD decodes 4WD). A
// qualifier takes the first tier the model decodes any id of, and only
// the ids of it the model decodes: "Front-Wheel Drive" is FWD on a model
// that decodes FWD, 4x2 on one that decodes 4x2 and no FWD, and never
// both.
var driveColumn = map[string][][]int32{
	"Front-Wheel Drive":          {{driveFWD}, {drive4x2}},
	"Rear-Wheel Drive":           {{driveRWD}, {drive4x2}},
	"2-Wheel Drive":              {{drive4x2}, {driveFWD, driveRWD}},
	"All-Wheel Drive":            {{driveAWD}, {drive4WD}},
	"4-Wheel or All-Wheel Drive": {{drive4WD, driveAWD}},
	"4-Wheel Drive":              {{drive4WD}, {driveAWD}},
	"Part-time 4-Wheel Drive":    {{drive4WD}, {driveAWD}},
}

// driveToken is the drive column value a drive token in the name stands
// for, the fallback when no row has one; a maker drive name stands for
// either 4WD or AWD, whichever the model decodes.
func driveToken(t string) string {
	switch t {
	case "fwd":
		return "Front-Wheel Drive"
	case "rwd":
		return "Rear-Wheel Drive"
	case "2wd", "4x2":
		return "2-Wheel Drive"
	case "awd":
		return "All-Wheel Drive"
	case "4wd", "4x4":
		return "4-Wheel Drive"
	}
	return "4-Wheel or All-Wheel Drive"
}

// driveQualifier is the Drive Type values an entry whose name carries a
// drive token holds for on model: for each drive its rows' drive column
// (or else the token) says, the first of driveColumn's tiers the model
// decodes, narrowed to the ids the model decodes. None when the name
// carries no token or the model decodes none of them.
func (mv modelValues) driveQualifier(e *entry, normModel string, model int32) []int32 {
	tok, ok := impliesDrive(normModel)
	if !ok {
		return nil
	}
	drives := slices.Collect(maps.Keys(e.drives))
	if len(drives) == 0 {
		drives = []string{driveToken(tok)}
	}
	has := mv.drive(model)
	var out []int32
	for _, d := range drives {
		for _, tier := range driveColumn[d] {
			n := len(out)
			for _, v := range tier {
				if has[v] {
					out = append(out, v)
				}
			}
			if len(out) > n {
				break
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
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
// on model: every spelling of that tonnage the model itself decodes to.
// None for other makes, other names, or when the model decodes none.
func (mv modelValues) seriesQualifier(e *entry, normModel string, model int32) []string {
	if !slices.Contains(seriesMakes, e.makeKey) {
		return nil
	}
	c := tonnage(normModel)
	if c == "" {
		return nil
	}
	var out []string
	vs, _ := mv.m.ModelValues(model, elementSeries)
	for _, v := range vs {
		if seriesClass(v, c) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}
