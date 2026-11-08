package decoder

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// Engine decodes VINs as vpic.spvindecode does, against one artifact. It is
// safe for concurrent use.
type Engine struct{ e *engine }

// NewEngine builds the engine over artifact a, which it keeps and reads at
// every decode.
func NewEngine(a *artifact.Artifact) *Engine { return &Engine{newEngine(a)} }

// Project decodes vin as spvindecode(vin, year => modelYear) does at
// instant now (only its microseconds count; the engine never reads the
// clock): the rows the procedure returns, with the header fields only the
// engine computes. The items are in the procedure's group-rank order, and
// within a group in the pass's (Postgres's is the executor's, unstable).
//
// An error is where the procedure raises: a VIN of more than 17 characters
// after trimming spaces, a pattern key Postgres's regex engine rejects, a
// value too long for the procedure's varchar columns, an integer cast of
// text that is not one (see decode). It is never a property of a VIN
// spvindecode answers for.
func (x *Engine) Project(vin string, modelYear contract.Null[int32], now time.Time) (contract.Projection, error) {
	return x.e.project(vin, yearHint{int(modelYear.V), modelYear.Valid}, now.UnixMicro())
}

// projection is the element table as W10-W11 read it, by element row.
type projection struct {
	name, code, group, dataType, decode artifact.Strings
	// public: a non-empty Decode and not private, the projection's filter
	// with includeAll NULL and includePrivate false (:230-232).
	public []bool
	rank   []uint8 // groupRank of the row's GroupName
	// lookup is the relation fElementAttributeValue reads for the row's
	// element, valid when it has a CASE arm.
	lookup []lookupTable
	valid  []bool
}

func newProjection(a *artifact.Artifact, lookups *artifact.Table, tables artifact.Strings) projection {
	t := a.Table("element")
	p := projection{
		name: t.Strings("name"), code: t.Strings("code"), group: t.Strings("groupname"),
		dataType: t.Strings("datatype"), decode: t.Strings("decode"),
	}
	n := t.Rows()
	p.public, p.rank = make([]bool, n), make([]uint8, n)
	p.lookup, p.valid = make([]lookupTable, n), make([]bool, n)
	private := t.Bools("isprivate")
	for r, id := range t.Int32s("id") {
		d, _ := p.decode.At(r)
		priv, _ := private.At(r)
		p.public[r] = d != "" && !priv
		g, _ := p.group.At(r)
		p.rank[r] = groupRank(g)
		if table, ok := attributeLookups[id]; ok {
			p.lookup[r], p.valid[r] = newLookupTable(lookups, tables, table), true
		}
	}
	return p
}

// project is spvindecode from the decode to its result: W10, the
// resolution of every 'XXX' value (:204-207), then W11, the projection
// (:209-263). The resolution and the projection both join DecItem to
// Element, so an item of an element the table lacks is neither.
func (e *engine) project(input string, hint yearHint, now int64) (contract.Projection, error) {
	items, _, err := e.decode(input, hint, now)
	if err != nil {
		return contract.Projection{}, err
	}
	vin := sanitize(input)
	p := contract.Projection{VIN: vin, WMI: wmi(vin), CheckDigitValid: e.checkDigitValid(vin)}
	out := &e.out

	// W10, in place on the decode's own items; slot is each projected
	// item's group rank plus one, 0 for an item the projection drops.
	slot := make([]uint8, len(items))
	var next [maxGroupRank + 2]int
	for i := range items {
		it := &items[i]
		if !e.elementExists(it.elementID) {
			continue
		}
		r := e.element.row[it.elementID]
		if it.value.valid && it.value.s == "XXX" {
			if it.value, err = out.resolve(r, it.attributeID); err != nil {
				return contract.Projection{}, err
			}
		}
		if out.public[r] {
			slot[i] = out.rank[r] + 1
			next[slot[i]]++
		}
	}

	// W11, ORDER BY the group rank (:233-263) as a counting sort, stable so
	// that a group keeps the pass's item order: next[s] becomes the first
	// index of slot s.
	n := 0
	for s, c := range next[1:] {
		next[s+1] = n
		n += c
	}
	p.Items = make([]contract.Item, n)
	for i := range items {
		if slot[i] == 0 {
			continue
		}
		it, r := &items[i], int(e.element.row[items[i].elementID])
		p.Items[next[slot[i]]] = contract.Item{
			GroupName:   nullString(out.group.At(r)),
			Variable:    must(out.name.At(r)),
			Value:       nullText(sqlText{scrub(it.value.s), it.value.valid}),
			PatternID:   nullInt(it.patternID),
			VinSchemaID: nullInt(it.vinSchemaID),
			Keys:        nullText(it.keys),
			ElementID:   it.elementID,
			AttributeID: nullText(it.attributeID),
			CreatedOn:   timestampText(it.createdOn),
			WmiID:       nullInt(it.wmiID),
			Code:        nullString(out.code.At(r)),
			DataType:    nullString(out.dataType.At(r)),
			Decode:      nullString(out.decode.At(r)),
			Source:      contract.Value(it.source),
			ToBeQCed:    nullBool(it.toBeQCed),
		}
		next[slot[i]]++
	}
	return p, nil
}

// errVarchar2000 is Postgres's error storing a lookup name longer than
// fElementAttributeValue's varchar(2000) variable.
var errVarchar2000 = errors.New("value too long for type character varying(2000)")

// resolve ports the UPDATE's value (:205) for an item of element row r with
// attribute attr. Its `CASE e.LookupTable WHEN NULL` never matches, NULL
// equalling nothing, so every value goes through fElementAttributeValue
// (felementattributevalue.sql): an element with a CASE arm reads its
// relation's Name where `cast(Id as varchar) = AttributeId`, NULL when no
// row matches (SELECT INTO leaves NULL), and any other element keeps its
// AttributeId. A name longer than the function's varchar(2000) or the
// item's varchar(500) raises.
func (p *projection) resolve(r int32, attr sqlText) (sqlText, error) {
	if !p.valid[r] {
		return attr, nil
	}
	if !attr.valid {
		return sqlText{}, nil
	}
	id, err := strconv.ParseInt(attr.s, 10, 32)
	if err != nil || strconv.FormatInt(id, 10) != attr.s {
		return sqlText{}, nil // no id casts to this text
	}
	name, ok := p.lookup[r].nameOf(int32(id))
	if !ok {
		return sqlText{}, nil
	}
	switch n := utf8.RuneCountInString(name); {
	case n > 2000:
		return sqlText{}, errVarchar2000
	case n > 500:
		return sqlText{}, errVarchar500
	}
	return text(name), nil
}

// checkDigitValid is the header's check_digit_valid, ultravin's rule: 17
// characters, and position 9 equal to fVINCheckDigit2's digit as C17
// computes it, before any VinException.
func (e *engine) checkDigitValid(vin string) bool {
	if len(vin) < 17 {
		return false
	}
	wlo, whi := e.wmi.wmi.EqualRange(0, len(e.wmi.id), wmi(vin))
	_, carMpvLT := scanStart(vin, e.carMpvLT(wlo, whi))
	return vin[8] == checkDigit2(vin, carMpvLT)
}

// scrub is the projection's REPLACE of tab, CR and LF by a space (:213).
func scrub(s string) string {
	if !strings.ContainsAny(s, "\t\r\n") {
		return s
	}
	return scrubber.Replace(s)
}

var scrubber = strings.NewReplacer("\t", " ", "\r", " ", "\n", " ")

// groupRank is the projection's ORDER BY CASE coalesce(e.GroupName, ”)
// (:234-263). The labels are the procedure's, with spaces around '/'; the
// data's group names have none ("Exterior/Body"), so those rank 99.
func groupRank(g string) uint8 {
	switch g {
	case "":
		return 0
	case "General":
		return 1
	case "Exterior / Body":
		return 2
	case "Exterior / Dimension":
		return 3
	case "Exterior / Truck":
		return 4
	case "Exterior / Trailer":
		return 5
	case "Exterior / Wheel tire":
		return 6
	case "Exterior / Motorcycle":
		return 7
	case "Exterior / Bus":
		return 8
	case "Interior":
		return 9
	case "Interior / Seat":
		return 10
	case "Mechanical / Transmission":
		return 11
	case "Mechanical / Drivetrain":
		return 12
	case "Mechanical / Brake":
		return 13
	case "Mechanical / Battery":
		return 14
	case "Mechanical / Battery / Charger":
		return 15
	case "Engine":
		return 16
	case "Passive Safety System":
		return 17
	case "Passive Safety System / Air Bag Location":
		return 18
	case "Active Safety System":
		return 19
	case "Active Safety System / Maintaining Safe Distance":
		return 20
	case "Active Safety System / Forward Collision Prevention":
		return 21
	case "Active Safety System / Lane and Side Assist":
		return 22
	case "Active Safety System / Backing Up and Parking":
		return 23
	case "Active Safety System / 911 Notification":
		return 24
	case "Active Safety System / Lighting Technologies":
		return 25
	case "Internal":
		return 26
	}
	return maxGroupRank
}

// maxGroupRank is the CASE's ELSE.
const maxGroupRank = 99

// attributeLookups is fElementAttributeValue's CASE (felementattributevalue
// .sql): the relation each element's AttributeId names a row of, lowercase
// as the artifact's lookup union names it. It is the procedure's code, not
// the data's: elements whose Element.LookupTable names a relation without
// an arm here (8, 16, 45, 82, 89-91, 140, 141, 187 in 2026_10) keep their
// AttributeId. TestElementAttributeValuePostgres holds it to the dump's.
var attributeLookups = map[int32]string{
	2: "batterytype", 3: "bedtype", 4: "bodycab", 5: "bodystyle",
	10: "destinationmarket", 15: "drivetype", 23: "entertainmentsystem", 24: "fueltype",
	25: "grossvehicleweightrating", 26: "make", 27: "manufacturer", 28: "model",
	36: "steering", 37: "transmission", 39: "vehicletype", 42: "brakesystem",
	55: "airbaglocations", 56: "airbaglocations", 60: "wheelbasetype", 62: "valvetraindesign",
	64: "engineconfiguration", 65: "airbaglocfront", 66: "fueltype", 67: "fueldeliverytype",
	69: "airbaglocknee", 72: "evdriveunit", 75: "country", 78: "pretensioner",
	79: "seatbeltsall", 81: "adaptivecruisecontrol", 86: "abs", 87: "autobrake",
	88: "blindspotmonitoring", 96: "vncsabodytype", 97: "vncsamake", 98: "vncsamodel",
	99: "ecs", 100: "tractioncontrol", 101: "forwardcollisionwarning", 102: "lanedeparturewarning",
	103: "lanekeepsystem", 104: "rearvisibilitycamera", 105: "parkassist", 107: "airbaglocations",
	116: "trailertype", 117: "trailerbodytype", 122: "coolingtype", 126: "electrificationlevel",
	127: "chargerlevel", 135: "turbo", 143: "errorcode", 145: "axleconfiguration",
	148: "busfloorconfigtype", 149: "bustype", 151: "custommotorcycletype", 152: "motorcyclesuspensiontype",
	153: "motorcyclechassistype", 168: "tpms", 170: "dynamicbrakesupport", 171: "pedestrianautomaticemergencybraking",
	172: "autoreversesystem", 173: "automaticpedestrainalertingsound", 174: "can_aacn", 175: "edr",
	176: "keylessignition", 177: "daytimerunninglight", 178: "lowerbeamheadlamplightsource", 179: "semiautomaticheadlampbeamswitching",
	180: "adaptivedrivingbeam", 183: "rearcrosstrafficalert", 184: "grossvehicleweightrating", 185: "grossvehicleweightrating",
	190: "grossvehicleweightrating", 192: "rearautomaticemergencybraking", 193: "blindspotintervention", 194: "lanecenteringassistance",
	195: "nonlanduse", 200: "fueltanktype", 201: "fueltankmaterial", 202: "combinedbrakingsystem",
	203: "wheeliemitigation",
}

// timestampText renders a timestamp without time zone, µs since 1970-01-01
// read as UTC, as Postgres outputs one under DateStyle ISO: at least four
// year digits, the fraction with its trailing zeros dropped and no point
// when it is zero, " BC" for years before 1. NULL stays NULL.
func timestampText(us int64) contract.Null[string] {
	if us == artifact.NullInt64 {
		return contract.Null[string]{}
	}
	t := time.UnixMicro(us).UTC()
	year, bc := t.Year(), false
	if year <= 0 {
		year, bc = 1-year, true
	}
	var buf [40]byte
	b := buf[:0]
	for y := year; y < 1000; y *= 10 {
		b = append(b, '0')
	}
	b = strconv.AppendInt(b, int64(year), 10)
	b = append2(append(b, '-'), int(t.Month()))
	b = append2(append(b, '-'), t.Day())
	b = append2(append(b, ' '), t.Hour())
	b = append2(append(b, ':'), t.Minute())
	b = append2(append(b, ':'), t.Second())
	if frac := t.Nanosecond() / 1000; frac != 0 {
		n := len(b)
		b = strconv.AppendInt(b, int64(frac+1_000_000), 10) // "1" and six digits
		b[n] = '.'
		for b[len(b)-1] == '0' {
			b = b[:len(b)-1]
		}
	}
	if bc {
		b = append(b, " BC"...)
	}
	return contract.Value(string(b))
}

func append2(b []byte, n int) []byte { return append(b, byte('0'+n/10), byte('0'+n%10)) }

func nullText(s sqlText) contract.Null[string] {
	return contract.Null[string]{V: s.s, Valid: s.valid}
}

func nullString(s string, ok bool) contract.Null[string] {
	return contract.Null[string]{V: s, Valid: ok}
}

func nullInt(n int32) contract.Null[int32] {
	if n == artifact.NullInt32 {
		return contract.Null[int32]{}
	}
	return contract.Value(n)
}

func nullBool(b uint8) contract.Null[bool] {
	if b == artifact.NullBool {
		return contract.Null[bool]{}
	}
	return contract.Value(b == 1)
}

// must is a public element's Name, which PublicElements requires.
func must(s string, _ bool) string { return s }
