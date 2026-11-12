package main

import (
	"errors"
	"flag"

	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

func init() {
	register(command{
		name:     "vocab",
		synopsis: "check <file|dir>...",
		summary:  `Check vocabulary, extension set and spine files, loaded together against the data: one {"file", "line", "severity", "detail"} line per fault or drift; exit 1 on any. The arguments are what is checked, never --vocab or $NITROVIN_VOCAB.`,
		setup:    vocabSetup,
	})
}

// vocabSetup is `nitrovin vocab check`: what loading the files would refuse
// (faults) and, when nothing does, where they disagree with the data
// (drift), for an author's CI.
func vocabSetup(*flag.FlagSet) func(*env, []string) error {
	return func(e *env, args []string) error {
		switch {
		case len(args) == 0:
			return usageError("vocab takes a subcommand: check")
		case args[0] != "check":
			return usageError("unknown vocab subcommand " + args[0])
		case len(args) == 1:
			return usageError("vocab check takes at least one file or directory")
		}
		d, _, err := e.loadData()
		if err != nil {
			return err
		}
		var findings []vocab.Finding
		files, err := vocab.Open(args[1:]...)
		var t *nitrovin.Translator
		if err == nil {
			t, err = nitrovin.NewTranslator(d, files...)
		}
		if faults, ok := errors.AsType[vocab.Faults](err); ok {
			findings = faults
		} else if err != nil {
			return err
		} else {
			findings = t.AllDrift()
		}
		for _, f := range findings {
			if err := writeFinding(e.stdout, f); err != nil {
				return err
			}
		}
		if len(findings) > 0 {
			return errFindings
		}
		return nil
	}
}
