package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// readStream parses the stream writeSQL's script prints: for every table of
// artifact.Schema, in order, a marker line and then the table's rows in COPY
// text format, closed by the end marker.
func readStream(r io.Reader) ([]artifact.TableData, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	schema := artifact.Schema()
	tables := make([]artifact.TableData, len(schema))
	ti := -1 // table being read
	line := 0
	for {
		raw, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			// A row longer than the buffer: gather the rest.
			long := append([]byte(nil), raw...)
			for errors.Is(err, bufio.ErrBufferFull) {
				raw, err = br.ReadSlice('\n')
				long = append(long, raw...)
			}
			raw = long
		}
		if err == io.EOF && len(raw) == 0 {
			return nil, fmt.Errorf("stream ended without %q: truncated extract", endMarker)
		}
		if err != nil && err != io.EOF {
			return nil, err
		}
		line++
		if len(raw) == 0 || raw[len(raw)-1] != '\n' {
			return nil, fmt.Errorf("line %d: no newline: truncated extract", line)
		}
		raw = raw[:len(raw)-1]

		if bytes.HasPrefix(raw, []byte(marker)) {
			name := string(raw[len(marker):])
			if ti+1 == len(schema) {
				if name != "end" {
					return nil, fmt.Errorf("line %d: table %q after the last table", line, name)
				}
				if rest, _ := br.Peek(1); len(rest) > 0 {
					return nil, fmt.Errorf("line %d: data after %q", line, endMarker)
				}
				return tables, nil
			}
			ti++
			if name != schema[ti].Name {
				return nil, fmt.Errorf("line %d: table %q where %q belongs", line, name, schema[ti].Name)
			}
			tables[ti] = artifact.TableData{Name: name, Columns: make([]artifact.Column, len(schema[ti].Columns))}
			continue
		}
		if ti < 0 {
			return nil, fmt.Errorf("line %d: data before the first table marker", line)
		}
		if err := appendRow(schema[ti], &tables[ti], raw); err != nil {
			return nil, fmt.Errorf("line %d (%s): %w", line, schema[ti].Name, err)
		}
	}
}

// appendRow parses one COPY text row into the table's columns.
func appendRow(ts artifact.TableSchema, td *artifact.TableData, raw []byte) error {
	fields := bytes.Split(raw, []byte{'\t'})
	if len(fields) != len(ts.Columns) {
		return fmt.Errorf("%d fields, want %d", len(fields), len(ts.Columns))
	}
	for ci, c := range ts.Columns {
		f := fields[ci]
		col := &td.Columns[ci]
		null := string(f) == `\N`
		if c.Type == artifact.TypeString {
			s := ""
			if !null {
				var err error
				if s, err = unescape(f); err != nil {
					return fmt.Errorf("%s: %w", c.Name, err)
				}
			}
			if null && col.Null == nil {
				col.Null = make([]bool, len(col.String), cap(col.String))
			}
			col.String = append(col.String, s)
			if col.Null != nil {
				col.Null = append(col.Null, null)
			}
			continue
		}
		v, err := parseScalar(c.Type, string(f), null)
		if err != nil {
			return fmt.Errorf("%s: %w", c.Name, err)
		}
		switch c.Type {
		case artifact.TypeInt32:
			col.Int32 = append(col.Int32, int32(v))
		case artifact.TypeInt64:
			col.Int64 = append(col.Int64, v)
		case artifact.TypeBool:
			col.Bool = append(col.Bool, uint8(v))
		}
	}
	return nil
}

// parseScalar parses a non-string field into its stored value, NULL included.
// A real value equal to the type's NULL value is refused: stored, it would
// read back as NULL.
func parseScalar(t artifact.Type, f string, null bool) (int64, error) {
	switch t {
	case artifact.TypeInt32:
		if null {
			return int64(artifact.NullInt32), nil
		}
		v, err := strconv.ParseInt(f, 10, 32)
		if err == nil && int32(v) == artifact.NullInt32 {
			err = fmt.Errorf("%d is the int32 NULL value", v)
		}
		return v, err
	case artifact.TypeInt64:
		if null {
			return artifact.NullInt64, nil
		}
		v, err := strconv.ParseInt(f, 10, 64)
		if err == nil && v == artifact.NullInt64 {
			err = fmt.Errorf("%d is the int64 NULL value", v)
		}
		return v, err
	case artifact.TypeBool:
		switch {
		case null:
			return int64(artifact.NullBool), nil
		case f == "t":
			return 1, nil
		case f == "f":
			return 0, nil
		}
		return 0, fmt.Errorf("bool %q", f)
	}
	return 0, fmt.Errorf("type %s", t)
}

// unescape decodes one COPY text field (not \N): \b \f \n \r \t \v, \\ and
// any other escaped character as itself, \ooo octal and \xhh hex bytes.
func unescape(f []byte) (string, error) {
	i := bytes.IndexByte(f, '\\')
	if i < 0 {
		return string(f), nil
	}
	var b strings.Builder
	b.Grow(len(f))
	b.Write(f[:i])
	for ; i < len(f); i++ {
		c := f[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i == len(f) {
			return "", errors.New("field ends in a backslash")
		}
		switch c = f[i]; c {
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case 'x':
			v, n := 0, 0
			for n < 2 && i+1 < len(f) && isHex(f[i+1]) {
				i++
				v = v*16 + hexVal(f[i])
				n++
			}
			if n == 0 {
				b.WriteByte('x') // \x without digits is a plain x
			} else {
				b.WriteByte(byte(v))
			}
		case '0', '1', '2', '3', '4', '5', '6', '7':
			v := int(c - '0')
			for n := 1; n < 3 && i+1 < len(f) && f[i+1] >= '0' && f[i+1] <= '7'; n++ {
				i++
				v = v*8 + int(f[i]-'0')
			}
			b.WriteByte(byte(v))
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexVal(c byte) int {
	switch {
	case c <= '9':
		return int(c - '0')
	case c >= 'a':
		return int(c-'a') + 10
	}
	return int(c-'A') + 10
}
