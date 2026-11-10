package vocab

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/zeebo/blake3"
)

// Open reads the files at paths, a directory meaning every *.jsonl file
// directly in it, in name order, and links them (Link). Each File's Path is
// absolute. The error is Faults: every fault of every file, which refuses
// them all.
func Open(paths ...string) ([]*File, error) {
	var files []*File
	var faults Faults
	read := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			faults = append(faults, fault(path, 0, "%v", errorText(err)))
			return
		}
		f, err := parse(path, data)
		if fs, ok := err.(Faults); ok {
			faults = append(faults, fs...)
			return
		}
		files = append(files, f)
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			faults = append(faults, fault(p, 0, "%v", err))
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil {
			faults = append(faults, fault(abs, 0, "%v", errorText(err)))
			continue
		}
		if !fi.IsDir() {
			read(abs)
			continue
		}
		des, err := os.ReadDir(abs)
		if err != nil {
			faults = append(faults, fault(abs, 0, "%v", errorText(err)))
			continue
		}
		for _, de := range des {
			if !de.IsDir() && strings.HasSuffix(de.Name(), ".jsonl") {
				read(filepath.Join(abs, de.Name()))
			}
		}
	}
	if len(faults) > 0 {
		return nil, faults
	}
	if err := Link(files); err != nil {
		return nil, err
	}
	return files, nil
}

// errorText is a file system error without the path the finding already
// names.
func errorText(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}

// Read reads one file from r; name is its Path, the name findings carry.
// It checks the file alone: Link checks it against the others loaded with
// it. The error is Faults: every fault in the file.
func Read(name string, r io.Reader) (*File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, Faults{fault(name, 0, "%v", err)}
	}
	return parse(name, data)
}

// parse reads one file's bytes: a header, then lines of its kind. Blank
// lines (nothing but spaces, tabs and a \r) are skipped and counted.
func parse(name string, data []byte) (*File, error) {
	f := &File{Path: name, Hash: blake3.Sum256(data)}
	var faults Faults
	header := false
	for i, line := range bytes.Split(data, []byte("\n")) {
		n := i + 1
		if len(bytes.Trim(line, " \t\r")) == 0 {
			continue
		}
		var err error
		if !header {
			if err = f.header(line); err != nil {
				return nil, Faults{fault(name, n, "%v", err)}
			}
			header = true
			continue
		}
		switch f.Kind {
		case KindVocabulary:
			err = f.entry(n, line)
		case KindExtensionSet:
			err = f.extension(n, line)
		case KindSpine:
			err = f.spineEntry(n, line)
		}
		if err != nil {
			faults = append(faults, fault(name, n, "%v", err))
		}
	}
	if !header {
		return nil, Faults{fault(name, 1, "no header: the file is empty")}
	}
	if len(faults) > 0 {
		return nil, faults
	}
	return f, nil
}

// lineError is what is wrong with a line: at a JSON Pointer into it, or
// with the line as a whole.
type lineError struct{ ptr, msg string }

func (e *lineError) Error() string {
	if e.ptr == "" {
		return e.msg
	}
	return e.ptr + ": " + e.msg
}

func errAt(ptr, format string, a ...any) error {
	return &lineError{ptr, fmt.Sprintf(format, a...)}
}

// object reads a line as a JSON object.
func object(line []byte) (map[string]json.RawMessage, error) {
	if !json.Valid(line) {
		var v any
		err := json.Unmarshal(line, &v)
		return nil, errAt("", "malformed JSON: %v", err)
	}
	return objectAt(line, "")
}

// objectAt reads raw, at ptr, as a JSON object.
func objectAt(raw json.RawMessage, ptr string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return nil, errAt(ptr, "must be an object")
	}
	return m, nil
}

// closed refuses the fields of m, at ptr, that are not allowed.
func closed(m map[string]json.RawMessage, ptr string, allowed ...string) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if !slices.Contains(allowed, k) {
			return errAt(ptr, "unknown field %q", k)
		}
	}
	return nil
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// stringAt reads raw, at ptr, as a string.
func stringAt(raw json.RawMessage, ptr string) (string, error) {
	var s string
	if isNull(raw) || json.Unmarshal(raw, &s) != nil {
		return "", errAt(ptr, "must be a string")
	}
	return s, nil
}

// nonEmptyAt reads raw, at ptr, as a non-empty string.
func nonEmptyAt(raw json.RawMessage, ptr string) (string, error) {
	s, err := stringAt(raw, ptr)
	if err == nil && s == "" {
		err = errAt(ptr, "must not be empty")
	}
	return s, err
}

// integer reads raw as a JSON integer: digits, no fraction or exponent.
func integer(raw json.RawMessage) (int64, bool) {
	n, err := strconv.ParseInt(string(bytes.TrimSpace(raw)), 10, 64)
	return n, err == nil
}

// idAt reads raw, at ptr, as a vPIC id: an integer from 1 to 2^31-1.
func idAt(raw json.RawMessage, ptr string) (int32, error) {
	n, ok := integer(raw)
	if !ok || n < 1 || n > math.MaxInt32 {
		return 0, errAt(ptr, "must be an integer from 1 to %d", math.MaxInt32)
	}
	return int32(n), nil
}

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	monthPattern = regexp.MustCompile(`^[0-9]{4}_(0[1-9]|1[0-2])$`)
	extPattern   = regexp.MustCompile(`^ext:([a-z0-9][a-z0-9._-]*)/([^/\s]+)$`)
	idPattern    = regexp.MustCompile(`^[^/\s]+$`)
	// elementPattern is a qualifier's element id.
	elementPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// nameAt reads raw, at ptr, as a file name.
func nameAt(raw json.RawMessage, ptr string) (string, error) {
	s, err := stringAt(raw, ptr)
	switch {
	case err != nil:
		return "", err
	case !namePattern.MatchString(s):
		return "", errAt(ptr, "%q is not a name: lower-case letters, digits, '.', '_' and '-', starting with a letter or digit", s)
	case s == Reserved:
		return "", errAt(ptr, "%q is reserved for the vehicle spine", s)
	}
	return s, nil
}

// header reads line 1.
func (f *File) header(line []byte) error {
	m, err := object(line)
	if err != nil {
		return err
	}
	raw, ok := m["format"]
	if !ok {
		return errAt("", "missing field \"format\"")
	}
	if n, ok := integer(raw); !ok || n != Format {
		return errAt("/format", "unknown format %s: this nitrovin reads format %d", bytes.TrimSpace(raw), Format)
	}
	raw, ok = m["kind"]
	if !ok {
		return errAt("", "missing field \"kind\"")
	}
	kind, err := stringAt(raw, "/kind")
	if err != nil {
		return err
	}
	f.Kind = Kind(kind)
	allowed := []string{"format", "kind", "name", "version"}
	switch f.Kind {
	case KindVocabulary:
		allowed = append(allowed, "data_month", "subject", "fields", "spine")
	case KindExtensionSet:
		allowed = append(allowed, "data_month")
	case KindSpine:
	default:
		return errAt("/kind", "unknown kind %q: vocabulary, extension-set or spine", kind)
	}
	if err := closed(m, "", allowed...); err != nil {
		return err
	}
	required := []string{"name", "version"}
	if f.Kind == KindVocabulary {
		required = append(required, "subject", "fields")
	}
	for _, k := range required {
		if _, ok := m[k]; !ok {
			return errAt("", "missing field %q", k)
		}
	}
	if f.Name, err = nameAt(m["name"], "/name"); err != nil {
		return err
	}
	if f.Version, err = nonEmptyAt(m["version"], "/version"); err != nil {
		return err
	}
	if raw, ok := m["data_month"]; ok {
		if f.DataMonth, err = stringAt(raw, "/data_month"); err != nil {
			return err
		}
		if !monthPattern.MatchString(f.DataMonth) {
			return errAt("/data_month", "%q is not a data month, YYYY_MM", f.DataMonth)
		}
	}
	if f.Kind != KindVocabulary {
		switch f.Kind {
		case KindExtensionSet:
			f.extensions = map[string]int{}
		case KindSpine:
			f.spine = map[string]int{}
		}
		return nil
	}
	subject, err := stringAt(m["subject"], "/subject")
	if err != nil {
		return err
	}
	f.Subject = Subject(subject)
	if f.Subject != SubjectVehicle && f.Subject != SubjectPart {
		return errAt("/subject", "unknown subject %q: vehicle or part", subject)
	}
	var fields []json.RawMessage
	if isNull(m["fields"]) || json.Unmarshal(m["fields"], &fields) != nil {
		return errAt("/fields", "must be an array of strings")
	}
	if len(fields) == 0 {
		return errAt("/fields", "must name at least one field")
	}
	for i, raw := range fields {
		ptr := "/fields/" + strconv.Itoa(i)
		s, err := nonEmptyAt(raw, ptr)
		if err != nil {
			return err
		}
		if slices.Contains(f.Fields, s) {
			return errAt(ptr, "duplicate field %q", s)
		}
		f.Fields = append(f.Fields, s)
	}
	raw, hasSpine := m["spine"]
	switch {
	case f.Subject == SubjectPart && !hasSpine:
		return errAt("", "missing field \"spine\": a part vocabulary names the spine it maps onto")
	case f.Subject == SubjectVehicle && hasSpine:
		return errAt("/spine", "a vehicle vocabulary maps onto vPIC and names no spine")
	case hasSpine:
		if f.Spine, err = nameAt(raw, "/spine"); err != nil {
			return err
		}
	}
	f.entries = map[string]int{}
	f.targets = map[Target][]int{}
	return nil
}

// entry reads a vocabulary's line n.
func (f *File) entry(n int, line []byte) error {
	m, err := object(line)
	if err != nil {
		return err
	}
	if err := closed(m, "", "entry", "mappings", "note"); err != nil {
		return err
	}
	for _, k := range []string{"entry", "mappings"} {
		if _, ok := m[k]; !ok {
			return errAt("", "missing field %q", k)
		}
	}
	e := Entry{Line: n, Fields: make([]string, len(f.Fields))}
	fields, err := objectAt(m["entry"], "/entry")
	if err != nil {
		return err
	}
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		if !slices.Contains(f.Fields, k) {
			return errAt("/entry", "unknown field %q: the vocabulary's fields are %s", k, quoted(f.Fields))
		}
	}
	for i, k := range f.Fields {
		raw, ok := fields[k]
		if !ok {
			return errAt("/entry", "missing field %q", k)
		}
		if e.Fields[i], err = stringAt(raw, "/entry/"+pointerToken(k)); err != nil {
			return err
		}
	}
	var mappings []json.RawMessage
	if isNull(m["mappings"]) || json.Unmarshal(m["mappings"], &mappings) != nil {
		return errAt("/mappings", "must be an array")
	}
	for i, raw := range mappings {
		mp, err := f.mapping(raw, "/mappings/"+strconv.Itoa(i))
		if err != nil {
			return err
		}
		e.Mappings = append(e.Mappings, mp)
	}
	if raw, ok := m["note"]; ok {
		if e.Note, err = stringAt(raw, "/note"); err != nil {
			return err
		}
	}
	k := key(e.Fields)
	if j, dup := f.entries[k]; dup {
		return errAt("/entry", "duplicate entry: line %d names it too, after normalization", f.Entries[j].Line)
	}
	f.entries[k] = len(f.Entries)
	for _, mp := range e.Mappings {
		idx := f.targets[mp.Target]
		if len(idx) == 0 || idx[len(idx)-1] != len(f.Entries) {
			f.targets[mp.Target] = append(idx, len(f.Entries))
		}
	}
	f.Entries = append(f.Entries, e)
	return nil
}

// mapping reads one of a vocabulary entry's mappings, at ptr.
func (f *File) mapping(raw json.RawMessage, ptr string) (Mapping, error) {
	var mp Mapping
	m, err := objectAt(raw, ptr)
	if err != nil {
		return mp, err
	}
	if err := closed(m, ptr, "make", "model", "ext", "id", "first", "last", "qualifiers", "origin", "note"); err != nil {
		return mp, err
	}
	var targets []string
	for _, k := range []string{"make", "model", "ext", "id"} {
		if _, ok := m[k]; ok {
			targets = append(targets, k)
		}
	}
	switch {
	case len(targets) == 0:
		return mp, errAt(ptr, "names no spine entry: one of make, model, ext or id")
	case len(targets) > 1:
		return mp, errAt(ptr, "names more than one spine entry: %s", strings.Join(targets, ", "))
	}
	t := targets[0]
	switch {
	case f.Subject == SubjectVehicle && t == "id":
		return mp, errAt(ptr, "a vehicle vocabulary maps to make, model or ext, not id")
	case f.Subject == SubjectPart && t != "id":
		return mp, errAt(ptr, "a part vocabulary maps to an id of spine %q, not %s", f.Spine, t)
	}
	switch tp := ptr + "/" + t; t {
	case "make":
		mp.Make, err = idAt(m[t], tp)
	case "model":
		mp.Model, err = idAt(m[t], tp)
	case "ext":
		mp.Ext, err = extRefAt(m[t], tp)
	case "id":
		mp.ID, err = nonEmptyAt(m[t], tp)
	}
	if err != nil {
		return mp, err
	}
	for _, k := range []string{"first", "last"} {
		raw, ok := m[k]
		if !ok {
			continue
		}
		y, ok := integer(raw)
		if !ok || y < math.MinInt32 || y > math.MaxInt32 {
			return mp, errAt(ptr+"/"+k, "must be an integer year")
		}
		year := int(y)
		if k == "first" {
			mp.First = &year
		} else {
			mp.Last = &year
		}
	}
	if mp.First != nil && mp.Last != nil && *mp.First > *mp.Last {
		return mp, errAt(ptr, "first year %d is after last year %d", *mp.First, *mp.Last)
	}
	if raw, ok := m["qualifiers"]; ok {
		if mp.Ext != "" {
			return mp, errAt(ptr+"/qualifiers", "an extension mapping takes no qualifiers: no decode lands on an extension")
		}
		if mp.Qualifiers, err = qualifiersAt(raw, ptr+"/qualifiers"); err != nil {
			return mp, err
		}
	}
	if raw, ok := m["origin"]; ok {
		if mp.Origin, err = stringAt(raw, ptr+"/origin"); err != nil {
			return mp, err
		}
		if !slices.Contains([]string{"derived", "curated", "seed"}, mp.Origin) {
			return mp, errAt(ptr+"/origin", "unknown origin %q: derived, curated or seed", mp.Origin)
		}
	}
	if raw, ok := m["note"]; ok {
		if mp.Note, err = stringAt(raw, ptr+"/note"); err != nil {
			return mp, err
		}
	}
	return mp, nil
}

// qualifiersAt reads a mapping's qualifiers, at ptr: element id → values.
func qualifiersAt(raw json.RawMessage, ptr string) ([]Qualifier, error) {
	m, err := objectAt(raw, ptr)
	if err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, errAt(ptr, "must name at least one element: omit qualifiers instead")
	}
	var qs []Qualifier
	for _, k := range slices.Sorted(maps.Keys(m)) {
		raw := m[k]
		el, err := strconv.ParseInt(k, 10, 32)
		if !elementPattern.MatchString(k) || err != nil {
			return nil, errAt(ptr, "%q is not an element id", k)
		}
		vp := ptr + "/" + k
		var values []json.RawMessage
		if isNull(raw) || json.Unmarshal(raw, &values) != nil {
			return nil, errAt(vp, "must be an array of attribute ids and strings")
		}
		if len(values) == 0 {
			return nil, errAt(vp, "must list at least one value")
		}
		q := Qualifier{Element: int32(el)}
		for i, raw := range values {
			ip := vp + "/" + strconv.Itoa(i)
			var v Value
			if n, ok := integer(raw); ok {
				if n < math.MinInt32 || n > math.MaxInt32 {
					return nil, errAt(ip, "attribute id %d is out of range", n)
				}
				v.Attribute = int32(n)
			} else if s, err := stringAt(raw, ip); err == nil {
				v = Value{IsText: true, Text: s}
			} else {
				return nil, errAt(ip, "must be an attribute id (an integer) or a string")
			}
			if slices.Contains(q.Values, v) {
				return nil, errAt(ip, "duplicate value")
			}
			q.Values = append(q.Values, v)
		}
		qs = append(qs, q)
	}
	slices.SortFunc(qs, func(a, b Qualifier) int { return cmp.Compare(a.Element, b.Element) }) // "10" sorts before "9"
	return qs, nil
}

// extRefAt reads raw, at ptr, as an extension reference, ext:<set>/<id>.
func extRefAt(raw json.RawMessage, ptr string) (string, error) {
	s, err := stringAt(raw, ptr)
	if err == nil && !extPattern.MatchString(s) {
		err = errAt(ptr, "%q is not an extension reference, ext:<set>/<id>", s)
	}
	return s, err
}

// splitExt splits a valid extension reference into its set and id.
func splitExt(ref string) (set, id string) {
	m := extPattern.FindStringSubmatch(ref)
	return m[1], m[2]
}

// extension reads an extension set's line n.
func (f *File) extension(n int, line []byte) error {
	m, err := object(line)
	if err != nil {
		return err
	}
	if err := closed(m, "", "id", "make_name", "make", "name"); err != nil {
		return err
	}
	x := Extension{Line: n}
	raw, ok := m["id"]
	if !ok {
		return errAt("", "missing field \"id\"")
	}
	if x.ID, err = nonEmptyAt(raw, "/id"); err != nil {
		return err
	}
	if !idPattern.MatchString(x.ID) {
		return errAt("/id", "%q may contain neither '/' nor white space", x.ID)
	}
	_, hasMakeName := m["make_name"]
	_, hasMake := m["make"]
	_, hasName := m["name"]
	switch {
	case hasMakeName && (hasMake || hasName):
		return errAt("", "an extension is a make (make_name) or a model (make and name), not both")
	case hasMakeName:
		if x.MakeName, err = nonEmptyAt(m["make_name"], "/make_name"); err != nil {
			return err
		}
	case !hasMake || !hasName:
		return errAt("", "missing make_name, for an extension make, or make and name, for an extension model")
	default:
		raw := m["make"]
		if _, ok := integer(raw); ok {
			x.Make, err = idAt(raw, "/make")
		} else if x.MakeExt, err = extRefAt(raw, "/make"); err != nil {
			err = errAt("/make", "must be a vPIC make id or an extension reference, ext:<set>/<id>")
		}
		if err != nil {
			return err
		}
		if x.Name, err = nonEmptyAt(m["name"], "/name"); err != nil {
			return err
		}
	}
	if j, dup := f.extensions[x.ID]; dup {
		return errAt("/id", "duplicate id %q: line %d has it too", x.ID, f.Extensions[j].Line)
	}
	f.extensions[x.ID] = len(f.Extensions)
	f.Extensions = append(f.Extensions, x)
	return nil
}

// spineEntry reads a declared spine's line n.
func (f *File) spineEntry(n int, line []byte) error {
	m, err := object(line)
	if err != nil {
		return err
	}
	if err := closed(m, "", "id", "name"); err != nil {
		return err
	}
	e := SpineEntry{Line: n}
	raw, ok := m["id"]
	if !ok {
		return errAt("", "missing field \"id\"")
	}
	if e.ID, err = nonEmptyAt(raw, "/id"); err != nil {
		return err
	}
	if raw, ok := m["name"]; ok {
		if e.Name, err = nonEmptyAt(raw, "/name"); err != nil {
			return err
		}
	}
	if j, dup := f.spine[e.ID]; dup {
		return errAt("/id", "duplicate id %q: line %d has it too", e.ID, f.SpineEntries[j].Line)
	}
	f.spine[e.ID] = len(f.SpineEntries)
	f.SpineEntries = append(f.SpineEntries, e)
	return nil
}

// quoted lists names as "a", "b".
func quoted(names []string) string {
	q := make([]string, len(names))
	for i, s := range names {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}

// pointerToken escapes a JSON Pointer reference token (RFC 6901).
func pointerToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
