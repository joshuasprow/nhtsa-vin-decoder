package bake

import (
	. "github.com/joshuasprow/nitrovin/prototypes/artifact-format/model"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
)

type Element struct {
	ID        int32
	Lookup    string
	Private   bool
	HasDecode bool
	Present   bool
}

// PatternRow is handed to the visitor by a Store; string fields may be views
// into the artifact and are valid for the store's lifetime.
type PatternRow struct {
	ID, ElementID int32
	Keys, Attr    string
	Created       int64
	Priority      int32 // band's yearfrom
}

// Store is the only thing that differs between candidates.
type Store interface {
	// Elements returns the element table indexed by id (loaded once at open).
	Elements() []Element
	// Candidates visits every pattern of every non-QC'd schema banded to an
	// available WMI row for the year (year 0 = every band): spvindecode_core
	// C1+C2 before the key match. vk (var_keys) is a hint a store may use to
	// pre-filter; the decoder still matches every row it is handed.
	Candidates(wmi, vk string, year int32, now int64, fn func(*PatternRow))
	Lookup(tag string, id int32) (string, bool)
	Close() error
}

type Item struct {
	ElementID, PatternID int32
	Priority             int32
	Created              int64
	Keys, Attr, Value    string
}

type Decoder struct {
	S       Store
	els     []Element
	items   []Item
	Scanned int // patterns visited, cumulative
}

func NewDecoder(s Store) *Decoder { return &Decoder{S: s, els: s.Elements()} }

func sanitize(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	for i := range len(v) {
		if v[i] >= 0x80 {
			b := []byte(v)
			for j := range b {
				if b[j] >= 0x80 {
					b[j] = '&'
				}
			}
			return string(b)
		}
	}
	return v
}

func wmiOf(v string) string {
	if len(v) < 3 {
		return v
	}
	if v[2] == '9' && len(v) >= 14 {
		return v[:3] + v[11:14]
	}
	return v[:3]
}

// varKeys is VIN positions 4-8 | 10-17, padded like the procedure's substring.
func varKeys(v string) string {
	p := v + strings.Repeat("*", max(0, 17-len(v)))
	return p[3:8] + "|" + p[9:17]
}

func rawYear(c byte) int32 {
	switch {
	case c >= 'A' && c <= 'H':
		return 2010 + int32(c-'A')
	case c >= 'J' && c <= 'N':
		return 2018 + int32(c-'J')
	case c == 'P':
		return 2023
	case c >= 'R' && c <= 'T':
		return 2024 + int32(c-'R')
	case c >= 'V' && c <= 'Y':
		return 2027 + int32(c-'V')
	case c >= '1' && c <= '9':
		return 2031 + int32(c-'1')
	}
	return 0
}

// match is the stub matcher: LIKE (keys with '*' → '_', implicit '%' suffix)
// when keys has no '[', else the anchored one-byte-per-token regex the dump's
// keys_regex encodes (port plan §7 row 3), including the oracle's '1-A' patch.
func match(keys, vk string) bool {
	j := 0
	for i := 0; i < len(keys); i++ {
		if j >= len(vk) {
			return false
		}
		c := vk[j]
		switch k := keys[i]; k {
		case '*':
		case '[':
			end := strings.IndexByte(keys[i:], ']')
			if end < 0 {
				return false
			}
			if !inClass(keys[i+1:i+end], c) {
				return false
			}
			i += end
		default:
			if k != c {
				return false
			}
		}
		j++
	}
	return true
}

func inClass(cls string, c byte) bool {
	cls = strings.ReplaceAll(cls, "1-A", "1A")
	neg := len(cls) > 0 && cls[0] == '^'
	if neg {
		cls = cls[1:]
	}
	in := false
	for i := 0; i < len(cls); i++ {
		if i+2 < len(cls) && cls[i+1] == '-' {
			if c >= cls[i] && c <= cls[i+2] {
				in = true
			}
			i += 2
			continue
		}
		if cls[i] == c {
			in = true
		}
	}
	return in != neg
}

var excluded = [...]int32{26, 27, 29, 39}

// Decode runs the stub hot path for one VIN and returns one item per element,
// ordered by element id. The slice is reused by the next call.
func (d *Decoder) Decode(vin string, now int64, currentYear int32) []Item {
	v := sanitize(vin)
	d.items = d.items[:0]
	if len(v) < 3 {
		return d.items
	}
	vk := varKeys(v)
	var year int32
	if len(v) >= 10 {
		year = rawYear(v[9])
		if year > currentYear+2 {
			year -= 30
		}
	}
	d.S.Candidates(wmiOf(v), vk, year, now, func(p *PatternRow) {
		d.Scanned++
		if slices.Contains(excluded[:], p.ElementID) {
			return
		}
		if p.ElementID < 0 || int(p.ElementID) >= len(d.els) {
			return
		}
		el := &d.els[p.ElementID]
		if !el.Present || !el.HasDecode || el.Private {
			return
		}
		if strings.IndexByte(p.Keys, '#') >= 0 || !match(p.Keys, vk) {
			return
		}
		d.items = append(d.items, Item{ElementID: p.ElementID, PatternID: p.ID, Priority: p.Priority, Created: p.Created, Keys: p.Keys, Attr: p.Attr})
	})
	// C8-shaped dedup: per element keep (priority desc, created desc, fewer
	// non-'*' key chars, keys asc, id asc).
	slices.SortFunc(d.items, func(a, b Item) int {
		if c := CmpInt(a.ElementID, b.ElementID); c != 0 {
			return c
		}
		if c := CmpInt(b.Priority, a.Priority); c != 0 {
			return c
		}
		if c := CmpInt(b.Created, a.Created); c != 0 {
			return c
		}
		if c := CmpInt(keyLen(a.Keys), keyLen(b.Keys)); c != 0 {
			return c
		}
		if c := strings.Compare(a.Keys, b.Keys); c != 0 {
			return c
		}
		return CmpInt(a.PatternID, b.PatternID)
	})
	out := d.items[:0]
	for i, it := range d.items {
		if i > 0 && it.ElementID == d.items[i-1].ElementID {
			continue
		}
		out = append(out, it)
	}
	for i := range out {
		it := &out[i]
		it.Value = it.Attr
		if lt := d.els[it.ElementID].Lookup; lt != "" {
			if id, err := strconv.ParseInt(it.Attr, 10, 32); err == nil {
				it.Value, _ = d.S.Lookup(lt, int32(id))
			}
		}
	}
	d.items = out
	return out
}

func keyLen(k string) int { return len(k) - strings.Count(k, "*") }

// Checksum folds a decode into a running hash so candidates can be compared.
func Checksum(h uint64, vin string, items []Item) uint64 {
	f := fnv.New64a()
	var b [8]byte
	for i := range b {
		b[i] = byte(h >> (8 * i))
	}
	f.Write(b[:])
	f.Write([]byte(vin))
	for _, it := range items {
		f.Write([]byte(strconv.Itoa(int(it.ElementID)) + ":" + strconv.Itoa(int(it.PatternID)) + "=" + it.Value + ";"))
	}
	return f.Sum64()
}

const vinChars = "ABCDEFGHJKLMNPRSTUVWXYZ0123456789"

const yearChars = "ABCDEFGHJKLMNPRSTVWXY123456789" // 2010..2039

// GenVINs synthesizes VINs that land on real schemas: pick a model pattern
// (element 28) without '[', one of its schema's bands and a year in it, fill
// the key template's wildcards at random, and compute the check digit.
func GenVINs(d *Data, n int, seed uint64) []string {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	pat := d.Table("pattern")
	pKeys, pEl, pSch := pat.Col("keys").S, pat.Col("elementid").I32, pat.Col("vinschemaid").I32
	ws := d.Table("wmi_vinschema")
	wsW, wsS, wsF, wsT := ws.Col("wmiid").I32, ws.Col("vinschemaid").I32, ws.Col("yearfrom").I32, ws.Col("yearto").I32
	bySchema := map[int32][]int{}
	for i := range ws.N {
		bySchema[wsS[i]] = append(bySchema[wsS[i]], i)
	}
	w := d.Table("wmi")
	wID, wStr := w.Col("id").I32, w.Col("wmi").S
	wmiByID := map[int32]string{}
	for i := range w.N {
		wmiByID[wID[i]] = wStr[i]
	}
	var cands []int
	for i := range pat.N {
		if pEl[i] == 28 && !strings.ContainsAny(pKeys[i], "[#") && len(bySchema[pSch[i]]) > 0 {
			cands = append(cands, i)
		}
	}
	var out []string
	for len(out) < n {
		p := cands[r.IntN(len(cands))]
		bands := bySchema[pSch[p]]
		b := bands[r.IntN(len(bands))]
		wmi := wmiByID[wsW[b]]
		lo, hi := max(wsF[b], 1981), min(wsT[b], 2027)
		if len(wmi) != 3 || lo > hi {
			continue
		}
		y := lo + r.Int32N(hi-lo+1)
		yi := y - 2010
		if yi < 0 {
			yi += 30
		}
		tmpl := []byte(pKeys[p])
		vk := make([]byte, 14)
		for i := range vk {
			switch {
			case i == 5:
				vk[i] = '|'
			case i < len(tmpl) && tmpl[i] != '*':
				vk[i] = tmpl[i]
			case i == 6:
				vk[i] = yearChars[yi]
			default:
				vk[i] = vinChars[r.IntN(len(vinChars))]
			}
		}
		vin := []byte(wmi + string(vk[:5]) + "0" + string(vk[6:]))
		if !validVINChars(vin) {
			continue
		}
		vin[8] = checkDigit(vin)
		out = append(out, string(vin))
	}
	return out
}

func validVINChars(v []byte) bool {
	for _, c := range v {
		if !strings.ContainsRune(vinChars, rune(c)) {
			return false
		}
	}
	return true
}

func checkDigit(v []byte) byte {
	const letters, values = "ABCDEFGHJKLMNPRSTUVWXYZ", "12345678123457923456789"
	weights := [17]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}
	sum := 0
	for i, c := range v {
		n := int(c - '0')
		if c > '9' {
			n = int(values[strings.IndexByte(letters, c)] - '0')
		}
		sum += n * weights[i]
	}
	if m := sum % 11; m == 10 {
		return 'X'
	} else {
		return byte('0' + m)
	}
}
