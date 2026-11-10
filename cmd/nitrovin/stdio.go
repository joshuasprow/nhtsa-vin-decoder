package main

import (
	"flag"

	"github.com/joshuasprow/nitrovin/pkg/stdio"
)

func init() {
	register(command{
		name:     "stdio",
		synopsis: "[flags]",
		summary:  `Answer JSON lines: one request per stdin line ({"id": 1, "vin": "…"}; "op": "translate" or "list" for the others), one {"id", "decode"|"translation"|"list"} or {"id", "error"} line back, in order, until EOF.`,
		setup:    stdioSetup,
	})
}

// stdioSetup has no flags beside --data: every request field comes on its
// line. A refused line is an error line, not an exit; the session ends at
// EOF with exit 0, or with exit 1 when stdin or stdout fails.
func stdioSetup(*flag.FlagSet) func(*env, []string) error {
	return func(e *env, args []string) error {
		if len(args) > 0 {
			return usageError("stdio takes no arguments: the requests come on stdin")
		}
		t, err := e.translator()
		if err != nil {
			return err
		}
		return stdio.Serve(t, e.stdin, e.stdout)
	}
}
