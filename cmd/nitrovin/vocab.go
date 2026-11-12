package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"

	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

func init() {
	register(command{
		name:     "vocab",
		synopsis: "check <file|dir>... | compile -o <file> <file|dir>...",
		summary: `Check vocabulary, extension set, spine and catalog files, loaded together against the data: one {"file", "line", "severity", "detail"} line per fault or drift; exit 1 on any. ` +
			`compile checks them the same way, refuses on a fault, and writes the one catalog among them compiled to -o; drift is written to stderr and recorded in the file. ` +
			`The arguments are what is checked, never --vocab or $NITROVIN_VOCAB.`,
		setup: vocabSetup,
	})
}

// vocabSetup is `nitrovin vocab check`: what loading the files would refuse
// (faults) and, when nothing does, where they disagree with the data
// (drift), for an author's CI; a compiled catalog is read in full and
// checked and measured again. And `nitrovin vocab compile`: the same check,
// then the one catalog among the files compiled (ADR 0021).
func vocabSetup(fs *flag.FlagSet) func(*env, []string) error {
	out := fs.String("o", "", "compile: write the compiled catalog to `file`")
	return func(e *env, args []string) error {
		switch {
		case len(args) == 0:
			return usageError("vocab takes a subcommand: check or compile")
		case args[0] != "check" && args[0] != "compile":
			return usageError("unknown vocab subcommand " + args[0])
		case len(args) == 1:
			return usageError("vocab " + args[0] + " takes at least one file or directory")
		case args[0] == "check" && *out != "":
			return usageError("-o is vocab compile's")
		case args[0] == "compile" && *out == "":
			return usageError("vocab compile takes -o, the file to write")
		}
		d, _, err := e.loadData()
		if err != nil {
			return err
		}
		if args[0] == "compile" {
			files, source, err := vocab.OpenCompile(args[1:]...)
			if err == nil {
				err = compile(e, d, files, source, *out)
			}
			return writeFaults(e, err)
		}
		files, err := vocab.Open(args[1:]...)
		for i := 0; err == nil && i < len(files); i++ {
			files[i], err = files[i].Expand()
		}
		var t *nitrovin.Translator
		if err == nil {
			t, err = nitrovin.NewTranslator(d, files...)
		}
		if err != nil {
			return writeFaults(e, err)
		}
		findings := t.AllDrift()
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

// writeFaults writes err's faults to stdout, one finding a line, and ends
// the command with errFindings; any other err is returned as it is.
func writeFaults(e *env, err error) error {
	faults, ok := errors.AsType[vocab.Faults](err)
	if !ok {
		return err
	}
	for _, f := range faults {
		if err := writeFinding(e.stdout, f); err != nil {
			return err
		}
	}
	return errFindings
}

// compile compiles the JSON Lines catalog at source, checked beside files
// against d's data month, to path: streamed (ADR 0023), sorting in a
// temporary directory, written beside path and renamed into place, so a
// reader never sees half a file. Its drift goes to stderr, as a load's
// does.
func compile(e *env, d *nitrovin.Decoder, files []*vocab.File, source, path string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	scratch, err := os.MkdirTemp("", "nitrovin-compile-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	drift, err := vocab.Compile(tmp, d.Month(), files, src, source, vocab.CompileOptions{Scratch: vocab.DirScratch(scratch), Writer: nitrovin.Version()})
	if err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	for _, f := range drift {
		writeFinding(e.stderr, f)
	}
	return nil
}
