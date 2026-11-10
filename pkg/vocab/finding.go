package vocab

import (
	"fmt"
	"strconv"
)

// Severity says whether a finding refuses the file or only warns.
type Severity string

const (
	// SeverityFault refuses: the file is wrong whatever the data month.
	SeverityFault Severity = "fault"
	// SeverityDrift warns: the file disagrees with the loaded data month,
	// and is served anyway (ADR 0011).
	SeverityDrift Severity = "drift"
)

// DriftKind is what a drift finding is about.
type DriftKind string

// The kinds of drift.
const (
	// DriftMake: a vPIC make id the data month lacks.
	DriftMake DriftKind = "make"
	// DriftModel: a vPIC model id the data month lacks.
	DriftModel DriftKind = "model"
	// DriftQualifier: a qualifier the data month's decode cannot satisfy:
	// an element it lacks, a value of the wrong type for the element, or an
	// attribute id or text the month's patterns never produce.
	DriftQualifier DriftKind = "qualifier"
	// DriftExtension: an extension named like a vPIC entry under the same
	// make.
	DriftExtension DriftKind = "extension"
)

// Finding is one fault or drift, about one line of one file: a line of
// `nitrovin vocab check`, {"file", "line", "severity", "detail"}.
type Finding struct {
	// File is the file's Path.
	File string `json:"file"`
	// Line is the line the finding is about, from 1; 0 is the file as a
	// whole (one that cannot be read).
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	Detail   string   `json:"detail"`
	// Drift is the drift's kind, "" for a fault.
	Drift DriftKind `json:"-"`
}

// String is "file:line: detail", or "file: detail" for line 0.
func (f Finding) String() string {
	if f.Line == 0 {
		return f.File + ": " + f.Detail
	}
	return f.File + ":" + strconv.Itoa(f.Line) + ": " + f.Detail
}

// Faults are the faults that refuse a load, in file and line order: the
// error of Read, Open and Link. It is never empty.
type Faults []Finding

// Error is the first fault, and how many follow it.
func (fs Faults) Error() string {
	s := fs[0].String()
	if len(fs) > 1 {
		s += fmt.Sprintf(" (and %d more)", len(fs)-1)
	}
	return s
}

func fault(file string, line int, format string, a ...any) Finding {
	return Finding{File: file, Line: line, Severity: SeverityFault, Detail: fmt.Sprintf(format, a...)}
}
