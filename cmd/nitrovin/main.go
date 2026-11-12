// Command nitrovin decodes VINs the way NHTSA's vPIC does, offline, against
// one month of its data.
//
//	nitrovin [--data FILE] [--vocab PATH]... <command> [flags] [arguments]
//	nitrovin [--data FILE] [--vocab PATH]... --version
//
// The data is the artifact named by --data (on the root or any command),
// then $NITROVIN_DATA, then the one embedded in the binary. Vocabularies,
// extension sets and declared spines load beside it from every --vocab
// file or directory (repeatable, on the root and any command), or else from
// $NITROVIN_VOCAB, a path list. Run `nitrovin -h` for the commands and
// `nitrovin <command> -h` for their flags; docs/api/README.md is the
// contract they follow.
//
// Exit codes: 0 when every request was answered, whatever error codes the
// VINs decode to; 1 when nitrovin could not answer (data or a vocabulary
// that will not load, a refused stdin line, a write failure), with a
// problem on stderr, or when `vocab check` has findings; 2 for a mistake
// on the command line, a refused argument VIN or translate or list request
// included, with nothing on stdout.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// command is one subcommand. Each lives in its own file, cmd/nitrovin/<name>.go,
// and adds itself to the command tree from that file:
//
//	func init() { register(command{name: "serve", …}) }
type command struct {
	name string
	// synopsis follows "nitrovin <name> " in the usage: "[flags] <vin>... | -".
	synopsis string
	// summary is the command's line in `nitrovin -h`.
	summary string
	// setup registers the command's flags on fs, beside the global --data,
	// and returns the command's body. The body runs with the positional
	// arguments (flags and arguments may interleave; "--" ends the flags).
	// Its error decides the exit: a usageError or a refusal exits 2, any
	// other error exits 1 with contract.AsProblem(err) on stderr.
	setup func(fs *flag.FlagSet) func(e *env, args []string) error
}

// commands is the command tree, filled by register from each command's file.
var commands = map[string]command{}

// register adds c to the command tree. Two commands with one name are a
// build mistake.
func register(c command) {
	if _, dup := commands[c.name]; dup {
		panic("nitrovin: command " + c.name + " registered twice")
	}
	commands[c.name] = c
}

// env is what a command runs with.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string
	// terminal reports whether stdout is a terminal: a single JSON object
	// is pretty-printed there and compact everywhere else.
	terminal bool
	// data is --data, empty when not given.
	data string
	// vocab is every --vocab, the root's then the command's; none means
	// $NITROVIN_VOCAB.
	vocab []string
}

// paths is a repeatable flag, each use appending its value.
type paths []string

func (p *paths) String() string { return strings.Join(*p, string(filepath.ListSeparator)) }

func (p *paths) Set(s string) error {
	*p = append(*p, s)
	return nil
}

// vocabUsage is --vocab's line in the usage.
const vocabUsage = "load the vocabulary, extension set, spine or catalog at `path`: a file, a compiled catalog included, or a directory of *.jsonl; repeatable (default $NITROVIN_VOCAB, a path list)"

// errFindings ends a command that has already written why it failed: exit
// 1, nothing more on stderr.
var errFindings = errors.New("findings written")

// usageError is a mistake on the command line: exit 2, the message and the
// command's usage on stderr, nothing on stdout.
type usageError string

func (e usageError) Error() string { return string(e) }

// refusal is a request the command line itself carries, refused: exit 2,
// the problem on stderr, nothing on stdout.
type refusal struct{ *contract.Problem }

func main() {
	os.Exit(run(os.Args[1:], &env{
		stdin:    os.Stdin,
		stdout:   os.Stdout,
		stderr:   os.Stderr,
		getenv:   os.Getenv,
		terminal: isTerminal(os.Stdout),
	}))
}

// isTerminal reports whether f is a character device: a terminal, or a
// device such as /dev/null where formatting cannot matter.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// run runs the command line args and returns the exit code.
func run(args []string, e *env) int {
	root := flag.NewFlagSet("nitrovin", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	root.StringVar(&e.data, "data", "", "load the data artifact `file` (default $NITROVIN_DATA, then the embedded one)")
	root.Var((*paths)(&e.vocab), "vocab", vocabUsage)
	version := root.Bool("version", false, "print the version and data month, and exit")
	switch err := root.Parse(args); {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(e.stdout, rootUsage(root))
		return 0
	case err != nil:
		return usageExit(e, err.Error(), rootUsage(root))
	}
	if *version {
		return finish(e, printVersion(e))
	}
	if root.NArg() == 0 {
		return usageExit(e, "no command", rootUsage(root))
	}
	name := root.Arg(0)
	c, ok := commands[name]
	if !ok {
		return usageExit(e, fmt.Sprintf("unknown command %q", name), rootUsage(root))
	}

	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&e.data, "data", e.data, "load the data artifact `file` (default $NITROVIN_DATA, then the embedded one)")
	fs.Var((*paths)(&e.vocab), "vocab", vocabUsage)
	body := c.setup(fs)
	pos, err := parseInterspersed(fs, root.Args()[1:])
	switch {
	case errors.Is(err, flag.ErrHelp):
		fmt.Fprint(e.stdout, commandUsage(c, fs))
		return 0
	case err != nil:
		return usageExit(e, err.Error(), commandUsage(c, fs))
	}
	err = body(e, pos)
	if u, ok := errors.AsType[usageError](err); ok {
		return usageExit(e, string(u), commandUsage(c, fs))
	}
	return finish(e, err)
}

// parseInterspersed parses args with fs, letting flags follow positional
// arguments as well as precede them, and returns the positional ones. "--"
// ends the flags; "-" is an argument.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// finish reports err, the outcome of a command, and returns the exit code.
func finish(e *env, err error) int {
	if err == nil {
		return 0
	}
	if r, ok := errors.AsType[refusal](err); ok {
		writeProblem(e.stderr, r.Problem)
		return 2
	}
	if errors.Is(err, errFindings) {
		return 1
	}
	writeProblem(e.stderr, contract.AsProblem(err))
	return 1
}

// writeProblem writes p to w as one line of compact JSON, as stdio does.
func writeProblem(w io.Writer, p *contract.Problem) {
	b, err := json.Marshal(p)
	if err != nil { // a Problem is strings and an int: unreachable
		panic(err)
	}
	w.Write(append(b, '\n'))
}

func usageExit(e *env, msg, usage string) int {
	fmt.Fprintf(e.stderr, "nitrovin: %s\n%s", msg, usage)
	return 2
}

func rootUsage(fs *flag.FlagSet) string {
	var b strings.Builder
	b.WriteString("usage: nitrovin [--data FILE] [--vocab PATH]... <command> [flags] [arguments]\n       nitrovin [--data FILE] [--vocab PATH]... --version\n\ncommands:\n")
	tw := tabwriter.NewWriter(&b, 0, 8, 2, ' ', 0)
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		fmt.Fprintf(tw, "  %s\t%s\n", name, commands[name].summary)
	}
	tw.Flush()
	b.WriteString("\nflags:\n")
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	return b.String()
}

func commandUsage(c command, fs *flag.FlagSet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "usage: nitrovin %s %s\n\n%s\n\nflags:\n", c.name, c.synopsis, c.summary)
	fs.SetOutput(&b)
	fs.PrintDefaults()
	fs.SetOutput(io.Discard)
	return b.String()
}

// decoder loads the data and the vocabularies beside it (translator).
func (e *env) decoder() (*nitrovin.Decoder, error) {
	t, _, err := e.load()
	if err != nil {
		return nil, err
	}
	return t.Decoder(), nil
}

// translator loads the data, then the vocabularies beside it: every
// --vocab, or else $NITROVIN_VOCAB. A fault in a file is an internal
// problem naming the flag or variable, the file and the line; drift is
// written to stderr, one vocab.Finding per line, and served.
func (e *env) translator() (*nitrovin.Translator, error) {
	t, _, err := e.load()
	return t, err
}

// load is translator with the artifact's bytes, for a command that reads
// more of the data than a decode carries: serve's form reads the
// error-code table.
func (e *env) load() (*nitrovin.Translator, []byte, error) {
	d, data, err := e.loadData()
	if err != nil {
		return nil, nil, err
	}
	paths, from := e.vocab, "--vocab"
	if len(paths) == 0 {
		paths, from = nil, "NITROVIN_VOCAB"
		for _, p := range filepath.SplitList(e.getenv("NITROVIN_VOCAB")) {
			if p != "" {
				paths = append(paths, p)
			}
		}
	}
	files, err := vocab.Open(paths...)
	var t *nitrovin.Translator
	if err == nil {
		t, err = nitrovin.NewTranslator(d, files...)
	}
	if err != nil {
		return nil, nil, contract.NewProblem(contract.Internal, "", fmt.Sprintf("%s: %v", from, err))
	}
	for _, f := range t.Drift() {
		writeFinding(e.stderr, f)
	}
	return t, data, nil
}

// writeFinding writes f to w as one line of compact JSON.
func writeFinding(w io.Writer, f vocab.Finding) error {
	b, err := json.Marshal(f)
	if err != nil { // a Finding is strings and an int: unreachable
		panic(err)
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// loadData loads the data alone: --data, then $NITROVIN_DATA, then the
// artifact embedded in the binary. It returns the artifact's bytes too.
func (e *env) loadData() (*nitrovin.Decoder, []byte, error) {
	path, from := e.data, "--data"
	if path == "" {
		path, from = e.getenv("NITROVIN_DATA"), "NITROVIN_DATA"
	}
	if path == "" {
		data := embedded.Artifact()
		d, err := nitrovin.Load(data, contract.DataSourceEmbedded)
		if err != nil {
			return nil, nil, contract.NewProblem(contract.Internal, "", fmt.Sprintf("embedded data: %v", err))
		}
		return d, data, nil
	}
	// nitrovin.Open, keeping the bytes.
	abs, err := filepath.Abs(path)
	var data []byte
	if err == nil {
		data, err = os.ReadFile(abs)
	}
	var d *nitrovin.Decoder
	if err == nil {
		d, err = nitrovin.Load(data, abs)
	}
	if err != nil {
		return nil, nil, contract.NewProblem(contract.Internal, "", fmt.Sprintf("%s %s: %v", from, path, err))
	}
	return d, data, nil
}

// printVersion prints `nitrovin v1.2.0 (data 2026_10)`.
func printVersion(e *env) error {
	d, err := e.decoder()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(e.stdout, "nitrovin %s (data %s)\n", nitrovin.Version(), d.Info().DataMonth)
	return err
}

// writeObject writes one JSON value as the whole output: pretty-printed on
// a terminal, one compact line elsewhere.
func writeObject(e *env, v any) error {
	var b []byte
	var err error
	if e.terminal {
		b, err = json.MarshalIndent(v, "", "  ")
	} else {
		b, err = json.Marshal(v)
	}
	if err != nil {
		return err
	}
	_, err = e.stdout.Write(append(b, '\n'))
	return err
}
