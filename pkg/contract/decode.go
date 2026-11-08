package contract

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Projection is what the engine hands the contract for one decode: the
// winning pass after value resolution and spvindecode's projection (W10-W11)
// plus the three header fields only the engine can compute.
type Projection struct {
	// VIN is the VIN as decoded: trimmed, uppercased, non-ASCII → '&'.
	VIN string
	// WMI is positions 1-3, plus 12-14 when position 3 is '9' and the VIN
	// has at least 14 characters.
	WMI string
	// CheckDigitValid: at least 17 characters, and position 9 equals the
	// computed check digit (ultravin's rule; a vPIC VinException suppresses
	// error code 1 but leaves this false).
	CheckDigitValid bool
	// Items are every row the decoder emitted, in any order.
	Items []Item
}

// Decode is the full result for one VIN, as every surface emits it.
type Decode struct {
	VIN string `json:"vin"`
	WMI string `json:"wmi"`
	// ModelYear is element 29 of the winning pass.
	ModelYear Null[int32] `json:"model_year"`
	// ErrorCodes is element 143, ascending; [0] is a clean decode.
	ErrorCodes      []int `json:"error_codes"`
	CheckDigitValid bool  `json:"check_digit_valid"`
	// CorrectedVIN is element 142, NULL when that element is empty.
	CorrectedVIN Null[string] `json:"corrected_vin"`
	// ModelYearHint is the request's model year, echoed.
	ModelYearHint Null[int32] `json:"model_year_hint"`
	// Now is the clock the decode used, UTC whole seconds.
	Now            time.Time  `json:"now"`
	DataMonth      string     `json:"data_month"`
	DecoderVersion string     `json:"decoder_version"`
	Shape          Shape      `json:"shape"`
	Attributes     Attributes `json:"attributes"`
	// Items is set only for a full request: every item, in item order.
	Items []Item `json:"items,omitempty"`
}

// NewDecode assembles the decode of p for request req against the build
// and data info describes. req.Now must be resolved: the clock this decode
// used, not zero. With req.Full, p.Items is sorted in place and becomes
// Items. The result is a pure function of its arguments: the same inputs
// give the same bytes whatever the order of p.Items.
//
// An error is a fault in the decoder, never a property of the VIN.
func NewDecode(p Projection, req Request, info *Info) (Decode, error) {
	if req.Now.IsZero() {
		return Decode{}, errors.New("the request's clock is not resolved")
	}
	shape := req.Shape
	if shape == "" {
		shape = ShapeVPIC
	}
	d := Decode{
		VIN:             p.VIN,
		WMI:             p.WMI,
		CheckDigitValid: p.CheckDigitValid,
		ModelYearHint:   req.ModelYear,
		Now:             req.Now.UTC().Truncate(time.Second),
		DataMonth:       info.DataMonth,
		DecoderVersion:  info.DecoderVersion,
		Shape:           shape,
	}
	for _, it := range p.Items {
		if !it.Value.Valid {
			continue
		}
		switch it.ElementID {
		case elementModelYear:
			y, err := strconv.ParseInt(it.Value.V, 10, 32)
			if err != nil {
				return Decode{}, fmt.Errorf("model year (element 29) %q is not an integer", it.Value.V)
			}
			d.ModelYear = Value(int32(y))
		case elementSuggestedVIN:
			if it.Value.V != "" {
				d.CorrectedVIN = Value(it.Value.V)
			}
		case elementErrorCode:
			codes, err := errorCodes(it.Value.V)
			if err != nil {
				return Decode{}, err
			}
			d.ErrorCodes = codes
		}
	}
	if d.ErrorCodes == nil {
		return Decode{}, errors.New("no error code (element 143) among the items")
	}
	attrs, err := Layout(shape, p.Items, info.Elements)
	if err != nil {
		return Decode{}, err
	}
	d.Attributes = attrs
	if req.Full {
		SortItems(p.Items)
		d.Items = p.Items
	}
	return d, nil
}

// errorCodes reads element 143's value, a comma-separated code list, as
// distinct codes in ascending order.
func errorCodes(list string) ([]int, error) {
	codes := make([]int, 0, strings.Count(list, ",")+1)
	for s := range strings.SplitSeq(list, ",") {
		c, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("error code (element 143) %q is not a code list", list)
		}
		codes = append(codes, c)
	}
	slices.Sort(codes)
	return slices.Compact(codes), nil
}
