package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

func init() {
	register(command{
		name:     "translate",
		synopsis: "[flags] <vin>... | -",
		summary:  `Translate the VINs given as arguments through the spine to the --to vocabularies, or with - one translate request object per stdin line ({"from": …, "to": […]}).`,
		setup:    translateSetup,
	})
}

// translateOptions are translate's flags: the request fields every VIN
// shares, and every stdin request that lacks its own.
type translateOptions struct {
	to        []string
	modelYear contract.Null[int32]
	now       string
}

func translateSetup(fs *flag.FlagSet) func(*env, []string) error {
	var o translateOptions
	fs.Func("to", "translate to the vocabularies `a,b`, comma-separated; repeatable (default: the spine alone)", func(s string) error {
		for name := range strings.SplitSeq(s, ",") {
			if name == "" {
				return errors.New("must be vocabulary names, comma-separated")
			}
			o.to = append(o.to, name)
		}
		return nil
	})
	modelYearFlag(fs, &o.modelYear, "model year `N`: the VINs' decode hint, and the year when none decodes one")
	fs.StringVar(&o.now, "now", "", "decode the VINs as of `instant`, RFC 3339 or YYYY-MM-DD (default: the clock, read once)")
	return func(e *env, args []string) error { return o.run(e, args) }
}

func (o *translateOptions) run(e *env, args []string) error {
	base := contract.TranslateRequest{To: o.to, ModelYear: o.modelYear}
	var err error
	if base.Now, err = clock(o.now); err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		return usageError("no VIN: give one or more, or - to read translate requests from stdin")
	case slices.Contains(args, "-") && len(args) > 1:
		return usageError("- reads the requests from stdin and takes no other argument")
	}
	if args[0] == "-" {
		return o.runStdin(e, base)
	}

	reqs := make([]contract.TranslateRequest, len(args))
	for i, vin := range args {
		reqs[i] = base
		reqs[i].From.VIN = vin
		if err := reqs[i].Validate(); err != nil {
			return refusal{naming(err, fmt.Sprintf("argument %d", i+1))}
		}
	}
	t, err := e.translator()
	if err != nil {
		return err
	}
	// Every answer before the first is written: a --to the loaded files
	// refuse leaves stdout empty.
	answers := make([]contract.Translation, len(reqs))
	for i, req := range reqs {
		if answers[i], err = t.Translate(req); err != nil {
			return refused(err, fmt.Sprintf("argument %d", i+1))
		}
	}
	if len(answers) == 1 {
		return writeObject(e, answers[0])
	}
	w := bufio.NewWriter(e.stdout)
	for _, a := range answers {
		if err := writeLine(w, a); err != nil {
			w.Flush()
			return err
		}
	}
	return w.Flush()
}

// runStdin translates one request object per stdin line, writing each
// answer before reading the next line's. A line's own to, model_year and
// now beat the flags'; a line without one takes the flag's. A refused line
// stops the run; the answers already written are complete.
func (o *translateOptions) runStdin(e *env, base contract.TranslateRequest) error {
	t, err := e.translator()
	if err != nil {
		return err
	}
	w := bufio.NewWriter(e.stdout)
	return eachLine(e, w, func(line string) error {
		if strings.TrimSpace(line) == "" {
			return nil
		}
		req, err := contract.ParseTranslateRequest([]byte(line))
		if err != nil {
			return err
		}
		if len(req.To) == 0 {
			req.To = base.To
		}
		if !req.ModelYear.Valid {
			req.ModelYear = base.ModelYear
		}
		if req.Now.IsZero() {
			req.Now = base.Now
		}
		a, err := t.Translate(req)
		if err != nil {
			return err
		}
		return writeLine(w, a)
	})
}

// refused is err, a Translator's answer to a request the command line
// carries, named by input: a refusal (exit 2) when the request is invalid,
// else the problem it is (exit 1).
func refused(err error, input string) error {
	p := naming(err, input)
	if p.Type == contract.InvalidRequest {
		return refusal{p}
	}
	return p
}
