package vocab

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// indexed is f written as a stored index and read back.
func indexed(t *testing.T, f *File) *File {
	t.Helper()
	var b bytes.Buffer
	if err := WriteIndex(&b, f); err != nil {
		t.Fatal(err)
	}
	g, err := ReadIndex(b.Bytes())
	if err != nil {
		t.Fatalf("%s: %v", f.Name, err)
	}
	return g
}

// TestIndex: every fixture file, and one with what the fixture lacks (text
// that normalizes, empty and absent lists, every value kind), reads back
// from its stored index as the File Read made, lookups and list order
// included, and a Set over the indexes answers as one over the files.
func TestIndex(t *testing.T) {
	files, err := Open("../../testdata/vocab")
	if err != nil {
		t.Fatal(err)
	}
	odd, err := Read("odd.jsonl", strings.NewReader(strings.Join([]string{
		`{"format":1,"kind":"vocabulary","name":"odd","version":"1","subject":"vehicle","fields":["make","model","trim"]}`,
		`{"entry":{"make":"  DODGE ","model":"Ram\t1500","trim":"Ünïcode  Läramie"},"mappings":[{"model":1938,"first":-3,"last":2030,"qualifiers":{"15":[2,7],"34":["1500",""]},"origin":"curated","note":"n"}],"note":"entry note"}`,
		`{"entry":{"make":"Dodge","model":"Ram 1500","trim":""},"mappings":[]}`,
		`{"entry":{"make":"dodge","model":"ram 1500","trim":"x"},"mappings":[{"make":476},{"model":1938,"first":2001}]}`,
		``,
		`{"entry":{"make":"Chevrolet","model":"Silverado","trim":"LT"},"mappings":[{"model":1850,"last":1999}]}`,
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, odd)
	var back []*File
	for _, f := range files {
		if f.Kind == KindVocabulary {
			f.listOrder()
		}
		g := indexed(t, f)
		if !reflect.DeepEqual(f, g) {
			t.Errorf("%s: the index reads back as another file", f.Name)
		}
		back = append(back, g)
	}
	want, err := NewSet(fixtureMonth{}, files)
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewSet(fixtureMonth{}, back)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Kind == KindVocabulary {
			for _, e := range f.Entries {
				q := Query{From: From{Vocabulary: f.Name, Entry: f.FieldsOf(&e)}, To: []string{"yard", "odd"}}
				if f.Subject == SubjectPart {
					q.To = nil
				}
				a, errA := want.Translate(q)
				b, errB := got.Translate(q)
				if fmt.Sprint(errA) != fmt.Sprint(errB) || (errA == nil && fmt.Sprint(routeLines(a)) != fmt.Sprint(routeLines(b))) {
					t.Errorf("%s line %d: translates as %v (%v) from the index; want %v (%v)", f.Name, e.Line, routeLines(b), errB, routeLines(a), errA)
				}
			}
		}
		for limit := 1; limit <= 3; limit++ {
			q := ListQuery{Of: f.Name, Limit: limit}
			for {
				a, errA := want.List(q)
				b, errB := got.List(q)
				if errA != nil || errB != nil {
					t.Fatalf("%s: %v / %v", f.Name, errA, errB)
				}
				if fmt.Sprint(names(a)) != fmt.Sprint(names(b)) || a.Next != b.Next {
					t.Errorf("%s limit %d: lists %v from the index; want %v", f.Name, limit, names(b), names(a))
				}
				if a.Next == "" {
					break
				}
				q.After = a.Next
			}
		}
	}
}

func names(l *Listing) []string {
	var out []string
	for _, e := range l.Entries {
		out = append(out, strings.Join(e.Fields, "/"))
	}
	for _, r := range l.Spine {
		out = append(out, refText(r.Ref)+" "+r.Name)
	}
	return out
}

// TestReadIndexRefuses: an index cut short anywhere, another format or no
// index at all is an error, never a panic or a wrong file.
func TestReadIndexRefuses(t *testing.T) {
	files, err := Open("../../testdata/vocab")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		var b bytes.Buffer
		if err := WriteIndex(&b, f); err != nil {
			t.Fatal(err)
		}
		data := b.Bytes()
		for n := range len(data) {
			if _, err := ReadIndex(data[:n]); err == nil {
				t.Errorf("%s cut to %d of %d bytes: read", f.Name, n, len(data))
			}
		}
		other := bytes.Replace(bytes.Clone(data), []byte(indexMagic+"\x01"), []byte(indexMagic+"\x02"), 1)
		if _, err := ReadIndex(other); err == nil || !strings.Contains(err.Error(), "format 2") {
			t.Errorf("%s as format 2: %v", f.Name, err)
		}
	}
	if _, err := ReadIndex([]byte(`{"format":1}`)); err == nil {
		t.Error("JSON read as an index")
	}
}
