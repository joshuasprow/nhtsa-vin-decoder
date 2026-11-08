package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

func init() {
	register(command{
		name:     "decode",
		synopsis: "[flags] <vin>... | -",
		summary:  "Decode the VINs given as arguments, or with - one per stdin line: VIN, VIN,YEAR or VIN<tab>YEAR.",
		setup:    decodeSetup,
	})
}

// decodeOptions are decode's flags: the request fields every VIN shares,
// and the output format.
type decodeOptions struct {
	modelYear contract.Null[int32]
	full      bool
	shape     string
	now       string
	output    string
}

func decodeSetup(fs *flag.FlagSet) func(*env, []string) error {
	var o decodeOptions
	fs.Func("model-year", "model-year hint `N` for every VIN without its own", func(s string) error {
		y, err := strconv.ParseInt(s, 10, 32)
		if err != nil {
			return errors.New("must be a 32-bit integer")
		}
		o.modelYear = contract.Value(int32(y))
		return nil
	})
	fs.BoolVar(&o.full, "full", false, "add the items, every row the decoder emitted (JSON only)")
	fs.StringVar(&o.shape, "shape", string(contract.ShapeVPIC), "layout of the attributes: vpic or nitrovin (JSON only)")
	fs.StringVar(&o.now, "now", "", "decode as of `instant`, RFC 3339 or YYYY-MM-DD (default: the clock, read once)")
	fs.StringVar(&o.output, "o", "json", "output `format`: json, or text (one row per value)")
	return func(e *env, args []string) error { return o.run(e, args) }
}

func (o *decodeOptions) run(e *env, args []string) error {
	base := contract.Request{ModelYear: o.modelYear, Full: o.full, Shape: contract.Shape(o.shape)}
	switch base.Shape {
	case contract.ShapeVPIC, contract.ShapeNitrovin:
	default:
		return usageError(fmt.Sprintf("--shape must be %s or %s", contract.ShapeVPIC, contract.ShapeNitrovin))
	}
	text := false
	switch o.output {
	case "json":
	case "text":
		if o.full {
			return usageError("--full has no text form: use -o json")
		}
		text, base.Shape = true, contract.ShapeNitrovin
	default:
		return usageError("-o must be json or text")
	}
	if o.now != "" {
		t, err := contract.ParseNow(o.now)
		if err != nil {
			return usageError("--now: " + err.Error())
		}
		base.Now = t
	} else {
		base.Now = time.Now() // once per run, as the contract fixes
	}
	switch {
	case len(args) == 0:
		return usageError("no VIN: give one or more, or - to read them from stdin")
	case slices.Contains(args, "-") && len(args) > 1:
		return usageError("- reads the VINs from stdin and takes no other argument")
	}

	out := decodeWriter{w: bufio.NewWriter(e.stdout), text: text}
	if args[0] == "-" {
		return o.runStdin(e, base, &out)
	}

	reqs := make([]contract.Request, len(args))
	for i, vin := range args {
		reqs[i] = base
		reqs[i].VIN = vin
		if err := reqs[i].Validate(); err != nil {
			return refusal{naming(err, fmt.Sprintf("argument %d", i+1))}
		}
	}
	d, err := e.decoder()
	if err != nil {
		return err
	}
	if len(reqs) == 1 && !text {
		dec, err := d.Decode(reqs[0])
		if err != nil {
			return err
		}
		return writeObject(e, dec)
	}
	for _, req := range reqs {
		if err := out.decode(d, req); err != nil {
			out.w.Flush()
			return err
		}
	}
	return out.w.Flush()
}

// runStdin decodes one VIN per line of stdin, writing each decode before
// reading the next line's. A refused line stops the run; the decodes
// already written are complete.
func (o *decodeOptions) runStdin(e *env, base contract.Request, out *decodeWriter) error {
	d, err := e.decoder()
	if err != nil {
		return err
	}
	in := bufio.NewReader(e.stdin)
	for n := 1; ; n++ {
		if in.Buffered() == 0 { // about to block: show what is decoded so far
			if err := out.w.Flush(); err != nil {
				return err
			}
		}
		line, rerr := in.ReadString('\n')
		if rerr != nil && rerr != io.EOF {
			out.w.Flush()
			return rerr
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line != "" {
			req, err := stdinRequest(base, line)
			if err == nil {
				err = out.decode(d, req)
			}
			if err != nil {
				out.w.Flush()
				return naming(err, fmt.Sprintf("line %d", n))
			}
		}
		if rerr == io.EOF {
			return out.w.Flush()
		}
	}
}

// stdinRequest reads one stdin line: a VIN, optionally followed by a comma
// or a tab and that VIN's model year. An empty year is none: the line takes
// --model-year.
func stdinRequest(base contract.Request, line string) (contract.Request, error) {
	req := base
	req.VIN = line
	if i := strings.IndexAny(line, ",\t"); i >= 0 {
		req.VIN = line[:i]
		if s := strings.TrimSpace(line[i+1:]); s != "" {
			y, err := strconv.ParseInt(s, 10, 32)
			if err != nil {
				return req, contract.NewProblem(contract.InvalidRequest, "/model_year", fmt.Sprintf("model year %q is not a 32-bit integer", s))
			}
			req.ModelYear = contract.Value(int32(y))
		}
	}
	return req, req.Validate()
}

// naming returns err as a problem whose detail names the input it is
// about: "argument 2", "line 7".
func naming(err error, input string) *contract.Problem {
	p := *contract.AsProblem(err)
	if p.Detail == "" {
		p.Detail = input
	} else {
		p.Detail = input + ": " + p.Detail
	}
	return &p
}

// decodeWriter writes decodes as JSON lines, or as text blocks.
type decodeWriter struct {
	w     *bufio.Writer
	text  bool
	count int
}

func (o *decodeWriter) decode(d *nitrovin.Decoder, req contract.Request) error {
	dec, err := d.Decode(req)
	if err != nil {
		return err
	}
	o.count++
	if o.text {
		if o.count > 1 {
			o.w.WriteByte('\n')
		}
		return writeText(o.w, dec)
	}
	b, err := json.Marshal(dec)
	if err != nil {
		return err
	}
	o.w.Write(b)
	return o.w.WriteByte('\n')
}

// writeText writes dec as a two-column table: the VIN, then every
// non-empty value of every decoded element, by element id, one row per
// value of a multi-valued element.
func writeText(w io.Writer, dec contract.Decode) error {
	type row struct {
		id       int32
		variable string
		value    string
	}
	var rows []row
	for variable, a := range dec.Attributes.(contract.NitrovinAttributes) {
		values := a.Values
		if values == nil {
			values = []string{a.Value}
		}
		for _, v := range values {
			if v != "" {
				rows = append(rows, row{a.ElementID, variable, v})
			}
		}
	}
	slices.SortFunc(rows, func(a, b row) int {
		return cmp.Or(cmp.Compare(a.id, b.id), strings.Compare(a.value, b.value))
	})
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "VIN\t%s\n", dec.VIN)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\n", r.variable, r.value)
	}
	return tw.Flush()
}
