// Command synth writes the PROTOTYPE's synthetic eBay-MVL-scale vocabulary
// (#147): eBay's US entry shape (Year, Make, Model, Trim, Engine; one entry
// per year, docs/research/ebay-vocabulary.md on research/ebay-vocabulary),
// with made-up trims and engines over the embedded month's real vPIC makes
// and models, each entry mapped to its model for its one year, about a
// third with a Drive Type qualifier. No eBay data: the names are invented,
// only the scale and shape are eBay's.
//
//	go run ./prototypes/browser/cmd/synth -n 150000 -o prototypes/browser/data/ebay-synthetic.jsonl
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

func main() {
	n := flag.Int("n", 150000, "entries")
	out := flag.String("o", "ebay-synthetic.jsonl", "output file")
	flag.Parse()
	d, err := nitrovin.Embedded()
	if err != nil {
		log.Fatal(err)
	}
	m := d.Month()
	type model struct {
		make, name string
		id         int32
	}
	// Half the entries come from the makes eBay's research names (Ford,
	// Chevrolet, Ram, Dodge, BMW, Lexus) and Honda and Toyota, half from any.
	big := map[string]bool{"FORD": true, "CHEVROLET": true, "RAM": true, "DODGE": true, "BMW": true, "LEXUS": true, "HONDA": true, "TOYOTA": true}
	var models, popular []model
	for mk, mname := range m.Makes() {
		for id, name := range m.Models(mk) {
			models = append(models, model{mname, name, id})
			if big[strings.ToUpper(mname)] {
				popular = append(popular, model{mname, name, id})
			}
		}
	}
	var drives []int32
	for id := int32(1); id < 50; id++ {
		if m.Attribute(15, id) {
			drives = append(drives, id)
		}
	}
	trims := []string{"Base Sedan 4-Door", "LX Sedan 4-Door", "EX Coupe 2-Door", "Limited Sport Utility 4-Door", "SE Hatchback 4-Door", "Sport Crew Cab Pickup 4-Door", "XL Standard Cab Pickup 2-Door", "Touring Wagon 4-Door", "S Convertible 2-Door", "Premium Cargo Van 3-Door"}
	engines := []string{"2.0L 1998CC 122Cu. In. l4 GAS DOHC Naturally Aspirated", "2.5L 2488CC 152Cu. In. l4 GAS DOHC Naturally Aspirated", "3.5L 3456CC 211Cu. In. V6 GAS DOHC Naturally Aspirated", "5.7L 5654CC 345Cu. In. V8 GAS OHV Naturally Aspirated", "6.7L 6690CC 408Cu. In. l6 DIESEL OHV Turbocharged", "1.5L 1497CC 91Cu. In. l3 GAS DOHC Turbocharged"}

	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	fmt.Fprintf(w, `{"format":1,"kind":"vocabulary","name":"ebay-synthetic","version":"prototype-147","data_month":%q,"subject":"vehicle","fields":["year","make","model","trim","engine"]}`+"\n", m.Name())
	r := rand.New(rand.NewPCG(147, 117))
	seen := map[[5]string]bool{}
	for len(seen) < *n {
		md := models[r.IntN(len(models))]
		if r.IntN(2) == 0 {
			md = popular[r.IntN(len(popular))]
		}
		year := 1990 + r.IntN(37)
		key := [5]string{fmt.Sprint(year), md.make, md.name, trims[r.IntN(len(trims))], engines[r.IntN(len(engines))]}
		if seen[key] {
			continue
		}
		seen[key] = true
		mp := map[string]any{"model": md.id, "first": year, "last": year, "origin": "derived"}
		if len(drives) > 0 && r.IntN(3) == 0 {
			mp["qualifiers"] = map[string][]int32{"15": {drives[r.IntN(len(drives))]}}
		}
		if err := enc.Encode(map[string]any{
			"entry":    map[string]string{"year": key[0], "make": key[1], "model": key[2], "trim": key[3], "engine": key[4]},
			"mappings": []any{mp},
		}); err != nil {
			log.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}
