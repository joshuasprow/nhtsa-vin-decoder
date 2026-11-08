package model

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Extract runs COPY ... TO STDOUT for every table inside the oracle container
// (scripts/oracle.sh's convention: client tools come from the container).
func Extract(container, dir string, withWYVC bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	lookupSQL, err := lookupUnion(container)
	if err != nil {
		return err
	}
	for _, def := range Defs {
		if def.Optional && !withWYVC {
			continue
		}
		exprs := make([]string, len(def.Cols))
		for i, c := range def.Cols {
			e := c.Expr
			if e == "" {
				e = c.Name
			}
			exprs[i] = e
		}
		from := def.From
		if def.Name == "lookup" {
			from = "(" + lookupSQL + ") l"
		}
		q := fmt.Sprintf("copy (select %s from %s) to stdout", strings.Join(exprs, ", "), from)
		out, err := os.Create(filepath.Join(dir, def.Name+".tsv"))
		if err != nil {
			return err
		}
		cmd := exec.Command("docker", "exec", container, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "vpic", "-c", q)
		cmd.Stdout, cmd.Stderr = out, os.Stderr
		err = cmd.Run()
		out.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", def.Name, err)
		}
	}
	return nil
}

// lookupUnion is one select over every element.lookuptable that exists in the
// dump with integer-ish id and a name (some named tables are absent).
func lookupUnion(container string) (string, error) {
	q := `select string_agg(format('select %L as tag, id::int as id, name from vpic.%I', t, t), ' union all ' order by t)
from (select distinct lower(lookuptable) t from vpic.element where coalesce(lookuptable,'') <> '') e
where exists (select 1 from information_schema.columns c where c.table_schema = 'vpic' and c.table_name = e.t and c.column_name = 'name')
  and exists (select 1 from information_schema.columns c where c.table_schema = 'vpic' and c.table_name = e.t and c.column_name = 'id')`
	out, err := exec.Command("docker", "exec", container, "psql", "-X", "-tA", "-U", "postgres", "-d", "vpic", "-c", q).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
