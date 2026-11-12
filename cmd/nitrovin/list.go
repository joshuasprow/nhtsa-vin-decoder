package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

func init() {
	register(command{
		name:     "list",
		synopsis: "[flags] [of]",
		summary:  "List what is loaded (the catalog: vpic, then every vocabulary, extension set and spine), or with of one page of vpic's makes, a make's models, a model's values for an element, a vocabulary's entries, a spine's or an extension set's.",
		setup:    listSetup,
	})
}

// listSetup is `nitrovin list`: one list request, of the argument and the
// flags, answered as one object.
func listSetup(fs *flag.FlagSet) func(*env, []string) error {
	var req contract.ListRequest
	fs.Func("where", "keep the entries whose `field=value`, matched after normalization (vpic takes make=<id>, listing that make's models, or model=<id> with element=<id>, listing the values a VIN of that model can decode the element to); repeatable", func(s string) error {
		field, value, ok := strings.Cut(s, "=")
		if !ok || field == "" {
			return errors.New("must be field=value")
		}
		if _, dup := req.Where[field]; dup {
			return fmt.Errorf("field %q is given twice", field)
		}
		if req.Where == nil {
			req.Where = map[string]string{}
		}
		req.Where[field] = value
		return nil
	})
	fs.Func("limit", fmt.Sprintf("at most `N` entries a page, 1 to %d (default %d)", contract.MaxListLimit, contract.DefaultListLimit), func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > contract.MaxListLimit {
			return fmt.Errorf("must be an integer from 1 to %d", contract.MaxListLimit)
		}
		req.Limit = n
		return nil
	})
	fs.Func("after", "continue after `cursor`, the previous page's next", func(s string) error {
		if s == "" {
			return errors.New("must be a page's next")
		}
		req.After = s
		return nil
	})
	return func(e *env, args []string) error {
		switch {
		case len(args) > 1:
			return usageError("list takes at most one of: vpic or a loaded file's name")
		case len(args) == 1 && args[0] == "":
			return usageError("of is vpic or a loaded file's name: leave it out for the catalog")
		case len(args) == 1:
			req.Of = args[0]
		}
		t, err := e.translator()
		if err != nil {
			return err
		}
		l, err := t.List(req)
		if err != nil {
			if p := contract.AsProblem(err); p.Type == contract.InvalidRequest {
				return refusal{p}
			}
			return err
		}
		return writeObject(e, l)
	}
}
