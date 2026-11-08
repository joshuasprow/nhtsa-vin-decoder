// Package model is PROTOTYPE code for the artifact format bake-off: the
// logical schema and the TSV loader. Throwaway.
package model

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type ColType uint8

const (
	I32 ColType = iota
	I64
	Bool
	Str
)

type Col struct {
	Name string
	Type ColType
	Expr string // SQL expression; defaults to Name
}

type TableDef struct {
	Name     string
	From     string // SQL FROM clause (or full select for lookup)
	Cols     []Col
	Sort     []string // sort key; Unique means it identifies a row
	Unique   bool
	Optional bool // only built with -wyvc
}

func us(col string) string {
	return fmt.Sprintf("(extract(epoch from %s)*1000000)::bigint", col)
}

var created = Col{"created", I64, us("coalesce(updatedon, createdon)")}

func i32(n string) Col { return Col{Name: n, Type: I32} }
func str(n string) Col { return Col{Name: n, Type: Str} }
func bl(n string) Col  { return Col{Name: n, Type: Bool} }

// Defs is the logical schema of the port plan (§2), in a fixed order: the
// order fixes string interning, so it is part of the byte format.
var Defs = []*TableDef{
	{Name: "wmi", From: "vpic.wmi", Cols: []Col{i32("id"), str("wmi"), i32("manufacturerid"), i32("makeid"), i32("vehicletypeid"), i32("trucktypeid"),
		{"pad", I64, "coalesce(" + us("publicavailabilitydate") + ", 9000000000000000000)"}, created}, Sort: []string{"wmi", "id"}, Unique: true},
	{Name: "wmi_vinschema", From: "vpic.wmi_vinschema", Cols: []Col{i32("id"), i32("wmiid"), i32("vinschemaid"), i32("yearfrom"), {"yearto", I32, "coalesce(yearto, 2999)"}}, Sort: []string{"wmiid", "id"}, Unique: true},
	{Name: "vinschema", From: "vpic.vinschema", Cols: []Col{i32("id"), bl("tobeqced")}, Sort: []string{"id"}, Unique: true},
	{Name: "pattern", From: "vpic.pattern", Cols: []Col{i32("id"), i32("vinschemaid"), str("keys"), i32("elementid"), str("attributeid"), created}, Sort: []string{"vinschemaid", "id"}, Unique: true},
	{Name: "element", From: "vpic.element", Cols: []Col{i32("id"), str("name"), str("code"), {"lookuptable", Str, "lower(lookuptable)"}, bl("isprivate"), str("groupname"), str("datatype"), str("decode"), {"hasdecode", Bool, "decode is not null"}, i32("weight")}, Sort: []string{"id"}, Unique: true},
	{Name: "make_model", From: "vpic.make_model", Cols: []Col{i32("id"), i32("makeid"), i32("modelid")}, Sort: []string{"modelid", "makeid", "id"}, Unique: true},
	{Name: "wmi_make", From: "vpic.wmi_make", Cols: []Col{i32("wmiid"), i32("makeid")}, Sort: []string{"wmiid", "makeid"}},
	{Name: "enginemodel", From: "vpic.enginemodel", Cols: []Col{i32("id"), str("name")}, Sort: []string{"id"}, Unique: true},
	{Name: "enginemodelpattern", From: "vpic.enginemodelpattern", Cols: []Col{i32("id"), i32("enginemodelid"), i32("elementid"), str("attributeid"), created}, Sort: []string{"enginemodelid", "id"}, Unique: true},
	{Name: "defaultvalue", From: "vpic.defaultvalue", Cols: []Col{i32("id"), i32("elementid"), i32("vehicletypeid"), str("defaultvalue"), created}, Sort: []string{"vehicletypeid", "id"}, Unique: true},
	{Name: "vinexception", From: "vpic.vinexception", Cols: []Col{i32("id"), str("vin"), bl("checkdigit")}, Sort: []string{"vin", "id"}, Unique: true},
	{Name: "conversion", From: "vpic.conversion", Cols: []Col{i32("id"), i32("fromelementid"), i32("toelementid"), str("formula")}, Sort: []string{"id"}, Unique: true},
	{Name: "vehiclespecschema", From: "vpic.vehiclespecschema", Cols: []Col{i32("id"), i32("makeid"), i32("vehicletypeid"), bl("tobeqced")}, Sort: []string{"id"}, Unique: true},
	{Name: "vspecschemapattern", From: "vpic.vspecschemapattern", Cols: []Col{i32("id"), i32("schemaid")}, Sort: []string{"schemaid", "id"}, Unique: true},
	{Name: "vehiclespecpattern", From: "vpic.vehiclespecpattern", Cols: []Col{i32("id"), i32("vspecschemapatternid"), bl("iskey"), i32("elementid"), str("attributeid"), created}, Sort: []string{"vspecschemapatternid", "id"}, Unique: true},
	{Name: "vehiclespecschema_model", From: "vpic.vehiclespecschema_model", Cols: []Col{i32("id"), i32("vehiclespecschemaid"), i32("modelid")}, Sort: []string{"vehiclespecschemaid", "id"}, Unique: true},
	{Name: "vehiclespecschema_year", From: "vpic.vehiclespecschema_year", Cols: []Col{i32("id"), i32("vehiclespecschemaid"), i32("year")}, Sort: []string{"vehiclespecschemaid", "id"}, Unique: true},
	{Name: "errorcode", From: "vpic.errorcode", Cols: []Col{i32("id"), str("name"), str("additionalerrortext"), i32("weight")}, Sort: []string{"id"}, Unique: true},
	{Name: "lookup", Cols: []Col{str("tag"), i32("id"), str("name")}, Sort: []string{"tag", "id"}, Unique: true}, // From filled by extract
	{Name: "wmiyearvalidchars", From: "vpic.wmiyearvalidchars", Cols: []Col{str("wmi"), i32("year"), i32("position"), str("char")}, Sort: []string{"wmi", "year", "position", "char"}, Optional: true},
}

func DefByName(name string) *TableDef {
	for _, d := range Defs {
		if d.Name == name {
			return d
		}
	}
	return nil
}

func (d *TableDef) ColIndex(name string) int {
	for i, c := range d.Cols {
		if c.Name == name {
			return i
		}
	}
	panic("no column " + d.Name + "." + name)
}

type Column struct {
	I32 []int32
	I64 []int64
	B   []bool
	S   []string
}

type Table struct {
	Def  *TableDef
	N    int
	Cols []Column
}

func (t *Table) Col(name string) *Column { return &t.Cols[t.Def.ColIndex(name)] }

// Data is every loaded table in Defs order.
type Data struct{ Tables []*Table }

func (d *Data) Table(name string) *Table {
	for _, t := range d.Tables {
		if t.Def.Name == name {
			return t
		}
	}
	return nil
}

// LoadTSV reads COPY text-format files written by Extract and sorts each table
// by its sort key, then by every column: the row order is a pure function of
// the rows, never of Postgres' scan order.
func LoadTSV(dir string, withWYVC bool) (*Data, error) {
	d := &Data{}
	for _, def := range Defs {
		if def.Optional && !withWYVC {
			continue
		}
		t, err := loadTable(filepath.Join(dir, def.Name+".tsv"), def)
		if err != nil {
			return nil, err
		}
		sortTable(t)
		d.Tables = append(d.Tables, t)
	}
	return d, nil
}

func loadTable(path string, def *TableDef) (*Table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	t := &Table{Def: def, Cols: make([]Column, len(def.Cols))}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	fields := make([][]byte, 0, len(def.Cols))
	for sc.Scan() {
		fields = fields[:0]
		line := sc.Bytes()
		for {
			i := bytes.IndexByte(line, '\t')
			if i < 0 {
				fields = append(fields, line)
				break
			}
			fields = append(fields, line[:i])
			line = line[i+1:]
		}
		if len(fields) != len(def.Cols) {
			return nil, fmt.Errorf("%s row %d: %d fields, want %d", def.Name, t.N+1, len(fields), len(def.Cols))
		}
		for i, c := range def.Cols {
			raw := fields[i]
			null := string(raw) == `\N`
			col := &t.Cols[i]
			switch c.Type {
			case I32:
				v := int64(math.MinInt32)
				if !null {
					if v, err = strconv.ParseInt(string(raw), 10, 32); err != nil {
						return nil, fmt.Errorf("%s.%s: %w", def.Name, c.Name, err)
					}
				}
				col.I32 = append(col.I32, int32(v))
			case I64:
				v := int64(math.MinInt64)
				if !null {
					if v, err = strconv.ParseInt(string(raw), 10, 64); err != nil {
						return nil, fmt.Errorf("%s.%s: %w", def.Name, c.Name, err)
					}
				}
				col.I64 = append(col.I64, v)
			case Bool:
				col.B = append(col.B, !null && raw[0] == 't')
			case Str:
				if null {
					col.S = append(col.S, "")
				} else {
					col.S = append(col.S, unescapeCopy(raw))
				}
			}
		}
		t.N++
	}
	return t, sc.Err()
}

func unescapeCopy(b []byte) string {
	if bytes.IndexByte(b, '\\') < 0 {
		return string(b)
	}
	var sb strings.Builder
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' || i+1 == len(b) {
			sb.WriteByte(b[i])
			continue
		}
		i++
		switch b[i] {
		case 't':
			sb.WriteByte('\t')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		default:
			sb.WriteByte(b[i])
		}
	}
	return sb.String()
}

func cmpCell(c *Column, typ ColType, a, b int) int {
	switch typ {
	case I32:
		return CmpInt(c.I32[a], c.I32[b])
	case I64:
		return CmpInt(c.I64[a], c.I64[b])
	case Bool:
		return CmpInt(B2i(c.B[a]), B2i(c.B[b]))
	default:
		return strings.Compare(c.S[a], c.S[b])
	}
}

func CmpInt[T int32 | int64 | int](a, b T) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func B2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func sortTable(t *Table) {
	order := make([]int, 0, len(t.Def.Cols))
	for _, s := range t.Def.Sort {
		order = append(order, t.Def.ColIndex(s))
	}
	for i := range t.Def.Cols {
		if !slices.Contains(order, i) {
			order = append(order, i)
		}
	}
	perm := make([]int, t.N)
	for i := range perm {
		perm[i] = i
	}
	slices.SortFunc(perm, func(a, b int) int {
		for _, ci := range order {
			if c := cmpCell(&t.Cols[ci], t.Def.Cols[ci].Type, a, b); c != 0 {
				return c
			}
		}
		return 0
	})
	for i, c := range t.Def.Cols {
		col := &t.Cols[i]
		switch c.Type {
		case I32:
			col.I32 = permute(col.I32, perm)
		case I64:
			col.I64 = permute(col.I64, perm)
		case Bool:
			col.B = permute(col.B, perm)
		case Str:
			col.S = permute(col.S, perm)
		}
	}
}

func permute[T any](s []T, perm []int) []T {
	out := make([]T, len(s))
	for i, p := range perm {
		out[i] = s[p]
	}
	return out
}

func FBTypeName(table string) string {
	var sb strings.Builder
	for _, p := range strings.Split(table, "_") {
		sb.WriteString(strings.ToUpper(p[:1]) + p[1:])
	}
	return sb.String()
}

// FBS renders the flatbuffers schema from the same model; `go generate`
// (flatc --go) turns it into nvfb/.
func FBS() string {
	var sb strings.Builder
	sb.WriteString("// PROTOTYPE, generated by `bakeoff fbs`. Throwaway.\nnamespace nvfb;\n\n")
	for _, d := range Defs {
		fmt.Fprintf(&sb, "table %s {\n", FBTypeName(d.Name))
		for _, c := range d.Cols {
			t := map[ColType]string{I32: "int", I64: "long", Bool: "bool", Str: "string"}[c.Type]
			fmt.Fprintf(&sb, "  %s:%s;\n", c.Name, t)
		}
		sb.WriteString("}\n\n")
	}
	sb.WriteString("table Artifact {\n")
	for _, d := range Defs {
		fmt.Fprintf(&sb, "  %s:[%s];\n", d.Name, FBTypeName(d.Name))
	}
	sb.WriteString("}\n\nroot_type Artifact;\nfile_identifier \"NVFB\";\n")
	return sb.String()
}
