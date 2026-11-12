package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

func init() {
	register(command{
		name:     "fit",
		synopsis: "[flags] <vin>... | -",
		summary:  `Fit each VIN given as an argument against the loaded catalog: its part types, with --part its candidates, with --related the vehicles that share its fitments; or with - one fit request object per stdin line ({"from": …, "part": …}).`,
		setup:    fitSetup,
	})
}

// fitOptions are fit's flags: the request fields every VIN shares, and,
// --after aside, what fills a stdin request that leaves them out.
type fitOptions struct {
	part      *contract.FitPart
	side      string
	to        []string
	modelYear contract.Null[int32]
	related   bool
	stock     []string
	location  []string
	limit     int
	after     string
	now       string
}

func fitSetup(fs *flag.FlagSet) func(*env, []string) error {
	var o fitOptions
	fs.Func("part", "ask for the candidates of part type `spine:id`, an entry of the catalog's part spine", func(s string) error {
		spine, id, ok := strings.Cut(s, ":")
		if !ok || spine == "" || id == "" {
			return errors.New("must be spine:id")
		}
		o.part = &contract.FitPart{Spine: spine, ID: id}
		return nil
	})
	fs.Func("side", "keep the fitments that go on `side`: left, right or both", func(s string) error {
		if s != contract.SideLeft && s != contract.SideRight && s != contract.SideBoth {
			return errors.New("must be left, right or both")
		}
		o.side = s
		return nil
	})
	fs.Func("to", "name vehicles in the vocabularies `a,b`, comma-separated; repeatable (default: as spine entries)", func(s string) error {
		for name := range strings.SplitSeq(s, ",") {
			if name == "" {
				return errors.New("must be vocabulary names, comma-separated")
			}
			o.to = append(o.to, name)
		}
		return nil
	})
	modelYearFlag(fs, &o.modelYear, "model year `N`: the VINs' decode hint, and the year when none decodes one")
	fs.BoolVar(&o.related, "related", false, "ask for the vehicles that share fitments with each VIN's")
	fs.Func("stock", "ask for the stock of the answer's fitments: `shelf,yard`, comma-separated", func(s string) error {
		for kind := range strings.SplitSeq(s, ",") {
			if kind != contract.StockShelf && kind != contract.StockYard {
				return errors.New("must be shelf, yard or shelf,yard")
			}
			o.stock = append(o.stock, kind)
		}
		return nil
	})
	fs.Func("location", "keep the stock rows at `location`; repeatable", func(s string) error {
		if s == "" {
			return errors.New("must not be empty")
		}
		o.location = append(o.location, s)
		return nil
	})
	fs.Func("limit", fmt.Sprintf("at most `N` answers a page, 1 to %d (default %d)", contract.MaxListLimit, contract.DefaultListLimit), func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > contract.MaxListLimit {
			return fmt.Errorf("must be an integer from 1 to %d", contract.MaxListLimit)
		}
		o.limit = n
		return nil
	})
	fs.Func("after", "continue after `cursor`, the previous page's next", func(s string) error {
		if s == "" {
			return errors.New("must be a page's next")
		}
		o.after = s
		return nil
	})
	fs.StringVar(&o.now, "now", "", "decode the VINs as of `instant`, RFC 3339 or YYYY-MM-DD (default: the clock, read once)")
	return func(e *env, args []string) error { return o.run(e, args) }
}

func (o *fitOptions) run(e *env, args []string) error {
	base := contract.FitRequest{Part: o.part, Side: o.side, To: o.to, ModelYear: o.modelYear, Related: o.related, Stock: o.stock, Location: o.location, Limit: o.limit, After: o.after}
	var err error
	if base.Now, err = clock(o.now); err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		return usageError("no VIN: give one or more, or - to read fit requests from stdin")
	case slices.Contains(args, "-") && len(args) > 1:
		return usageError("- reads the requests from stdin and takes no other argument")
	case args[0] == "-" && o.after != "":
		return usageError("--after continues one answer: give it on a stdin line, as after")
	}
	if args[0] == "-" {
		return o.runStdin(e, base)
	}

	reqs := make([]contract.FitRequest, len(args))
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
	// Every answer before the first is written: a --part or --to the
	// loaded files refuse leaves stdout empty.
	answers := make([]contract.Fit, len(reqs))
	for i, req := range reqs {
		if answers[i], err = t.Fit(context.Background(), req); err != nil {
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

// runStdin fits one request object per stdin line, writing each answer
// before reading the next line's. A line's own to, side, model_year, limit
// and now beat the flags'; a line without one takes the flag's, and a line
// whose from is a vehicle and that gives neither part nor related takes
// --part and --related. A refused line stops the run; the answers already
// written are complete.
func (o *fitOptions) runStdin(e *env, base contract.FitRequest) error {
	t, err := e.translator()
	if err != nil {
		return err
	}
	w := bufio.NewWriter(e.stdout)
	return eachLine(e, w, func(line string) error {
		if strings.TrimSpace(line) == "" {
			return nil
		}
		req, err := contract.ParseFitRequest([]byte(line))
		if err != nil {
			return err
		}
		if len(req.To) == 0 {
			req.To = base.To
		}
		if req.Side == "" {
			req.Side = base.Side
		}
		if !req.ModelYear.Valid {
			req.ModelYear = base.ModelYear
		}
		if req.Now.IsZero() {
			req.Now = base.Now
		}
		if req.Limit == 0 {
			req.Limit = base.Limit
		}
		if req.From.Fitment == nil && req.Part == nil && !req.Related {
			req.Part, req.Related = base.Part, base.Related
		}
		a, err := t.Fit(context.Background(), req)
		if err != nil {
			return err
		}
		return writeLine(w, a)
	})
}
