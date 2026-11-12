package fitment

import (
	"iter"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// month knows Dodge (476) and its Ram (1938), and nothing else.
type month struct{}

func (month) Name() string { return "2026_10" }
func (month) Make(id int32) (string, bool) {
	return "Dodge", id == 476
}
func (month) Model(id int32) (int32, string, bool) {
	return 476, "Ram", id == 1938
}
func (month) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) { yield(476, "Dodge") }
}
func (month) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		if make == 476 {
			yield(1938, "Ram")
		}
	}
}
func (month) Element(int32) (bool, bool)  { return true, true }
func (month) Attribute(int32, int32) bool { return true }
func (month) Text(int32, string) bool     { return false }

// load reads files, each its lines, into a set.
func load(t *testing.T, files ...[]string) *vocab.Set {
	t.Helper()
	var fs []*vocab.File
	for i, lines := range files {
		f, err := vocab.Read(string(rune('a'+i))+".jsonl", strings.NewReader(strings.Join(lines, "\n")))
		if err != nil {
			t.Fatal(err)
		}
		fs = append(fs, f)
	}
	set, err := vocab.NewSet(month{}, fs)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

var spine = []string{`{"format":1,"kind":"spine","name":"parts","version":"1"}`, `{"id":"pads"}`}

// numbers is the candidates' numbers.
func numbers(r Reader, vehicles []vocab.Vehicle) []string {
	var out []string
	for _, c := range r.ByVehicle("pads", vehicles, nil, nil) {
		out = append(out, c.Fitment.Key.Number)
	}
	return out
}

// TestVehicles: the seam from a translation to a Reader's vehicles, for a
// catalog of a vocabulary's vehicles (from the vocabulary's entries the
// translation reached, or the from entry itself) and for a vpic catalog
// (from the spine entries reached).
func TestVehicles(t *testing.T) {
	set := load(t, spine,
		[]string{`{"format":1,"kind":"vocabulary","name":"models","version":"1","subject":"vehicle","fields":["model"]}`,
			`{"entry":{"model":"Ram"},"mappings":[{"model":1938}]}`, `{"entry":{"model":"Any Dodge"},"mappings":[{"make":476}]}`},
		[]string{`{"format":1,"kind":"catalog","name":"cat","version":"1","spine":"parts","vehicles":"models"}`,
			`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"model":"Ram"},"options":""}]}`,
			`{"fitment":{"type":"pads","number":"P2"},"applications":[{"vehicle":{"model":"Any Dodge"},"options":""}]}`},
	)
	var r Reader = set.Catalog()
	for _, tc := range []struct {
		name string
		from vocab.From
		want []string
	}{
		{"a vPIC model reaches the entries mapping to it and its make", vocab.From{Ref: vocab.Ref{Target: vocab.Target{Model: 1938}}}, []string{"P1", "P2"}},
		{"a vPIC make reaches only the make's entry", vocab.From{Ref: vocab.Ref{Target: vocab.Target{Make: 476}}}, []string{"P2"}},
		{"the catalog's own entry is used as it is", vocab.From{Vocabulary: "models", Entry: map[string]string{"model": "ram"}}, []string{"P1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, err := set.Translate(vocab.Query{From: tc.from, To: []string{r.Info().Vehicles}})
			if err != nil {
				t.Fatal(err)
			}
			if got := numbers(r, Vehicles(r.Info(), tr)); !slices.Equal(got, tc.want) {
				t.Errorf("candidates %v; want %v", got, tc.want)
			}
		})
	}

	vpic := load(t, spine,
		[]string{`{"format":1,"kind":"catalog","name":"cat","version":"1","spine":"parts","vehicles":"vpic"}`,
			`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"model":1938},"options":""}]}`,
			`{"fitment":{"type":"pads","number":"P2"},"applications":[{"vehicle":{"make":476},"options":""}]}`},
	)
	r = vpic.Catalog()
	tr, err := vpic.Translate(vocab.Query{From: vocab.From{Ref: vocab.Ref{Target: vocab.Target{Model: 1938}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := numbers(r, Vehicles(r.Info(), tr)); !slices.Equal(got, []string{"P1", "P2"}) {
		t.Errorf("a vpic catalog's candidates for model 1938: %v; want the model's and its make's", got)
	}
}
