package nitrovin

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// compiledTwin is tr with its catalog compiled (ADR 0021) against tr's
// data month and loaded beside the same files.
func compiledTwin(t *testing.T, tr *Translator) *Translator {
	t.Helper()
	cat := tr.core.Set.Catalog()
	if cat == nil {
		t.Fatal("no catalog to compile")
	}
	files := append(tr.core.Set.Files(), cat.File())
	var b bytes.Buffer
	if _, err := vocab.Compile(&b, tr.d.month, files, Version()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.compiled")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range tr.core.Set.Files() {
		paths = append(paths, f.Path)
	}
	loaded, err := vocab.Open(append(paths, path)...)
	if err != nil {
		t.Fatal(err)
	}
	twin, err := NewTranslator(tr.d, loaded...)
	if err != nil {
		t.Fatal(err)
	}
	if twin.core.Set.Catalog().File().Compiled() == nil {
		t.Fatal("the twin's catalog is not compiled")
	}
	return twin
}

// sameFit asks req of both translators, and of every page after the
// first, and fails unless they answer alike: the same JSON, or the same
// refusal. It returns how many pages answered found.
func sameFit(t *testing.T, jsonl, compiled *Translator, req contract.FitRequest) int {
	t.Helper()
	req.Now = fitNow
	found := 0
	for page := 1; ; page++ {
		want, werr := jsonl.Fit(req)
		got, gerr := compiled.Fit(req)
		wb, _ := json.Marshal(want)
		gb, _ := json.Marshal(got)
		if (werr == nil) != (gerr == nil) || werr != nil && werr.Error() != gerr.Error() || !bytes.Equal(wb, gb) {
			rb, _ := json.Marshal(req)
			t.Fatalf("fit %s, page %d:\ncompiled %s (%v)\njson lines %s (%v)", rb, page, gb, gerr, wb, werr)
		}
		if werr == nil && want.Status == contract.FitFound {
			found++
		}
		if werr != nil || want.Next == "" {
			return found
		}
		req.After = want.Next
	}
}

// TestFitCompiled: fit answers from a compiled catalog exactly as from its
// JSON Lines source, on both the fixture catalog and the catalog of two
// models: every kind of request (part types, candidates directly and
// through a redirect, related vehicles, a fitment's vehicles directly and
// through a retired number), from VINs, vocabulary entries and spine
// entries, in and across years, on every side, named in a vocabulary or as
// spine entries, a page at a time and whole; and list's pages of the
// catalog.
func TestFitCompiled(t *testing.T) {
	type source struct {
		name string
		tr   *Translator
		from []contract.FitFrom
		to   [][]string
	}
	entry := func(vocabulary string, fields ...string) contract.FitFrom {
		e := map[string]string{}
		for i := 0; i < len(fields); i += 2 {
			e[fields[i]] = fields[i+1]
		}
		return contract.FitFrom{Vocabulary: vocabulary, Entry: e}
	}
	model := func(id int32) contract.FitFrom {
		return contract.FitFrom{SpineRef: contract.SpineRef{Model: id}}
	}
	for _, s := range []source{
		{"fixture", catalogTranslator(t), []contract.FitFrom{
			{VIN: "1D7RV1GT5AS123456"}, {VIN: "1GCEK19J07Z123456"}, model(1938), model(1850), model(13620),
			entry("catalog-models", "make", "Dodge", "model", "Ram 1500"), entry("catalog-models", "make", "Chevrolet", "model", "Silverado 1500"),
			entry("catalog-models", "make", "Dodge", "model", "Dakota"),
		}, [][]string{nil, {"catalog-models"}}},
		{"two models", modelsTranslator(t), []contract.FitFrom{
			model(1938), model(1850), entry("cm", "model", "Ram A"), entry("cm", "model", "Ram B"), entry("cm", "model", "Other"),
		}, [][]string{nil, {"cm"}}},
	} {
		t.Run(s.name, func(t *testing.T) {
			compiled := compiledTwin(t, s.tr)
			parts := []*contract.FitPart{nil}
			var keys []contract.FitmentKey
			cat := s.tr.Catalog()
			spine := cat.Info().Spine
			for _, f := range cat.Fitments("", nil, 0) {
				keys = append(keys, contract.FitmentKey{Type: f.Key.Type, Number: f.Key.Number, Side: f.Key.Side.String()})
				if n := len(parts); parts[n-1] == nil || parts[n-1].ID != f.Key.Type {
					parts = append(parts, &contract.FitPart{Spine: spine, ID: f.Key.Type})
				}
			}
			parts = append(parts, &contract.FitPart{Spine: spine, ID: "brake-rotor-rear"}, &contract.FitPart{Vocabulary: "part-codes", Entry: map[string]string{"code": "ROT"}})
			for _, rt := range s.tr.core.Set.Catalog().File().Retired {
				keys = append(keys, contract.FitmentKey{Type: rt.Key.Type, Number: rt.Key.Number, Side: rt.Key.Side.String()})
			}
			keys = append(keys, contract.FitmentKey{Type: "mirror", Number: "none"})
			years := []contract.Null[int32]{{}, contract.Value[int32](2008), contract.Value[int32](2010), contract.Value[int32](2013)}
			found := 0
			for _, from := range s.from {
				for _, year := range years {
					for _, to := range s.to {
						for _, part := range parts {
							for _, side := range []string{"", "left", "right", "both"} {
								for _, limit := range []int{0, 1} {
									found += sameFit(t, s.tr, compiled, contract.FitRequest{From: from, ModelYear: year, Part: part, Side: side, To: to, Limit: limit})
								}
							}
						}
						for _, limit := range []int{0, 1} {
							found += sameFit(t, s.tr, compiled, contract.FitRequest{From: from, ModelYear: year, To: to, Related: true, Limit: limit})
						}
					}
				}
			}
			for _, k := range keys {
				for _, year := range years {
					for _, to := range s.to {
						for _, limit := range []int{0, 1} {
							found += sameFit(t, s.tr, compiled, contract.FitRequest{From: contract.FitFrom{Fitment: &k}, ModelYear: year, To: to, Limit: limit})
						}
					}
				}
			}
			if found < 100 {
				t.Fatalf("%d pages answered found; want many", found)
			}
			t.Logf("%d pages answered found", found)

			// list of the catalog, a page at a time and by part type.
			for _, where := range []map[string]string{nil, {"type": "mirror"}, {"type": "none"}} {
				req := contract.ListRequest{Of: cat.Info().Name, Where: where, Limit: 1}
				for page := 1; ; page++ {
					want, werr := s.tr.List(req)
					got, gerr := compiled.List(req)
					wb, _ := json.Marshal(want)
					gb, _ := json.Marshal(got)
					if (werr == nil) != (gerr == nil) || !bytes.Equal(wb, gb) {
						t.Fatalf("list %v, page %s: %s (%v); want %s (%v)", where, strconv.Itoa(page), gb, gerr, wb, werr)
					}
					if werr != nil || want.Next == "" {
						break
					}
					req.After = want.Next
				}
			}
		})
	}
}
