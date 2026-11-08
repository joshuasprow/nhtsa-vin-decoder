package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

func inspectCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	verify := fs.Bool("verify", false, "recompute the hash and check every table's row order")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("inspect needs an artifact\n%s", usage)
	}
	b, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	a, err := artifact.Open(b)
	if err != nil {
		return err
	}
	if *verify {
		if err := a.Verify(); err != nil {
			return err
		}
	}
	w := bufio.NewWriter(stdout)
	if fs.NArg() == 1 {
		err = printDirectory(w, a)
	} else {
		err = printRows(w, a, fs.Arg(1), fs.Args()[2:])
	}
	if err != nil {
		return err
	}
	return w.Flush()
}

func printDirectory(w io.Writer, a *artifact.Artifact) error {
	m := a.Manifest()
	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintf(tw, "format\t%d\ndata month\t%s\nbytes\t%d\nblake3\t%s\nstrings\t%d\n\n", m.Format, m.Month, m.Bytes, m.Blake3, m.Strings)
	fmt.Fprintln(tw, "table\trows\tkey\tcolumns")
	for _, t := range a.Tables() {
		var cols []string
		for _, c := range t.Columns() {
			cols = append(cols, c.Name+":"+c.Type.String())
		}
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\n", t.Name(), t.Rows(), t.Key(), strings.Join(cols, " "))
	}
	return tw.Flush()
}

// printRows prints the table's column names, then every row matching all
// filters in COPY text format.
func printRows(w io.Writer, a *artifact.Artifact, table string, filters []string) error {
	t := a.Table(table)
	if t == nil {
		return fmt.Errorf("no table %q", table)
	}
	match, err := rowFilter(t, filters)
	if err != nil {
		return err
	}
	var names []string
	for _, c := range t.Columns() {
		names = append(names, c.Name)
	}
	fmt.Fprintln(w, strings.Join(names, "\t"))
	f := newRowFormatter(t)
	for i := range t.Rows() {
		if match(i) {
			fmt.Fprintln(w, f.row(i))
		}
	}
	return nil
}

func rowFilter(t *artifact.Table, filters []string) (func(int) bool, error) {
	var tests []func(int) bool
	for _, f := range filters {
		name, value, ok := strings.Cut(f, "=")
		if !ok {
			return nil, fmt.Errorf("filter %q is not COLUMN=VALUE", f)
		}
		var typ artifact.Type
		for _, c := range t.Columns() {
			if c.Name == name {
				typ = c.Type
			}
		}
		null := value == `\N`
		switch typ {
		case 0:
			return nil, fmt.Errorf("table %s has no column %q", t.Name(), name)
		case artifact.TypeString:
			col := t.Strings(name)
			want, err := unescape([]byte(value))
			if err != nil {
				return nil, err
			}
			tests = append(tests, func(i int) bool {
				s, ok := col.At(i)
				if null {
					return !ok
				}
				return ok && s == want
			})
		default:
			v, err := parseScalar(typ, value, null)
			if err != nil {
				return nil, fmt.Errorf("filter %s: %w", f, err)
			}
			get := scalarColumn(t, name, typ)
			tests = append(tests, func(i int) bool { return get(i) == v })
		}
	}
	return func(i int) bool {
		for _, test := range tests {
			if !test(i) {
				return false
			}
		}
		return true
	}, nil
}

// scalarColumn reads a non-string column as the int64 parseScalar returns.
func scalarColumn(t *artifact.Table, name string, typ artifact.Type) func(int) int64 {
	switch typ {
	case artifact.TypeInt32:
		col := t.Int32s(name)
		return func(i int) int64 { return int64(col[i]) }
	case artifact.TypeInt64:
		col := t.Int64s(name)
		return func(i int) int64 { return col[i] }
	}
	col := t.Bools(name)
	return func(i int) int64 { return int64(col[i]) }
}

// rowFormatter renders rows exactly as the extract's COPY printed them.
type rowFormatter struct {
	cols []func(int) string
}

func newRowFormatter(t *artifact.Table) rowFormatter {
	var f rowFormatter
	for _, c := range t.Columns() {
		switch c.Type {
		case artifact.TypeString:
			col := t.Strings(c.Name)
			f.cols = append(f.cols, func(i int) string {
				s, ok := col.At(i)
				if !ok {
					return `\N`
				}
				return escape(s)
			})
		case artifact.TypeBool:
			col := t.Bools(c.Name)
			f.cols = append(f.cols, func(i int) string {
				v, ok := col.At(i)
				switch {
				case !ok:
					return `\N`
				case v:
					return "t"
				}
				return "f"
			})
		case artifact.TypeInt32:
			col := t.Int32s(c.Name)
			f.cols = append(f.cols, func(i int) string {
				if col[i] == artifact.NullInt32 {
					return `\N`
				}
				return strconv.Itoa(int(col[i]))
			})
		case artifact.TypeInt64:
			col := t.Int64s(c.Name)
			f.cols = append(f.cols, func(i int) string {
				if col[i] == artifact.NullInt64 {
					return `\N`
				}
				return strconv.FormatInt(col[i], 10)
			})
		}
	}
	return f
}

func (f rowFormatter) row(i int) string {
	fields := make([]string, len(f.cols))
	for c, col := range f.cols {
		fields[c] = col(i)
	}
	return strings.Join(fields, "\t")
}

// escape is COPY text format's escaping, the inverse of unescape.
var escape = strings.NewReplacer(`\`, `\\`, "\b", `\b`, "\f", `\f`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "\v", `\v`).Replace
