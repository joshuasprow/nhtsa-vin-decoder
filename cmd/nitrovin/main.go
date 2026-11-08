// Command nitrovin decodes VINs the way NHTSA's vPIC does, offline, against
// one month of its data.
//
//	nitrovin [--data FILE] <command> [flags] [arguments]
//	nitrovin [--data FILE] --version
//
// The data is the artifact named by --data (on the root or any command),
// then $NITROVIN_DATA, then the one embedded in the binary. Run
// `nitrovin -h` for the commands and `nitrovin <command> -h` for their flags;
// docs/api/README.md is the contract they follow.
//
// Exit codes: 0 when every request was answered, whatever error codes the
// VINs decode to; 1 when nitrovin could not answer (no data, a refused stdin
// line, a write failure), with a problem on stderr; 2 for a mistake on the
// command line, a refused argument VIN included, with nothing on stdout.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
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
}

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
	b.WriteString("usage: nitrovin [--data FILE] <command> [flags] [arguments]\n       nitrovin [--data FILE] --version\n\ncommands:\n")
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

// decoder loads the data: --data, then $NITROVIN_DATA, then the artifact
// embedded in the binary.
func (e *env) decoder() (*nitrovin.Decoder, error) {
	path, from := e.data, "--data"
	if path == "" {
		path, from = e.getenv("NITROVIN_DATA"), "NITROVIN_DATA"
	}
	if path == "" {
		return nil, contract.NewProblem(contract.Internal, "",
			"this build embeds no data artifact: load one with --data FILE or NITROVIN_DATA=FILE")
	}
	d, err := nitrovin.Open(path)
	if err != nil {
		return nil, contract.NewProblem(contract.Internal, "", fmt.Sprintf("%s %s: %v", from, path, err))
	}
	return d, nil
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
