package decoder

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

// The conversion stage (C10, spvindecode_core.sql:233-286) substitutes an
// item's AttributeId for '#x#' in vpic.conversion.formula, runs
//
//	select (<formula>)::varchar(500)
//
// as dynamic SQL and keeps the text, or '0' when the statement raises. convert
// is that statement for the shape every formula in the dump has, an operand
// '#x#', '*' or '/', and a numeric constant (2026_10: 16.387064, 1000.,
// 1000 and 0.016387064), reproducing Postgres's integer and numeric
// arithmetic. The operand is data: the AttributeId of a pattern or engine
// model pattern row for a conversion's FromElementId (the cursor runs before
// vehicle specs and defaults, and conversion results are not re-read).
//
// An operand convert evaluates is an unsigned decimal literal (digits, with
// at most one '.') or an identifier. An identifier is a column reference with
// no FROM clause, or a keyword, and raises in either case except NULL, whose
// result is NULL, and LOCALTIME, a time that casts to interval and so
// multiplies and divides: the result is the clock's, and convert declines
// it. Anything else (signs, spaces, exponents, other SQL) is
// errConversionUnsupported too: the statement could be any expression, and a
// guess would diverge silently. TestCorePostgres checks that no operand in
// the dump is declined; the oracle key checks every Postgres keyword.
var errConversionUnsupported = errors.New("decoder: conversion formula or operand outside the evaluated subset")

// convert returns what the cursor stores for formula with '#x#' replaced by
// value: the text, NULL (ok false) for a NULL result, "0" when Postgres
// raises.
func convert(formula, value string) (result string, ok bool, err error) {
	op, constant, err := parseFormula(formula)
	if err != nil {
		return "", false, err
	}
	c, isNum := parseLiteral(constant)
	if !isNum {
		return "", false, errConversionUnsupported
	}
	x, isNum := parseLiteral(value)
	if !isNum {
		if !isIdentifier(value) {
			return "", false, errConversionUnsupported
		}
		switch {
		case strings.EqualFold(value, "null"):
			return "", false, nil
		case strings.EqualFold(value, "localtime"):
			return "", false, errConversionUnsupported
		}
		return "0", true, nil
	}
	v, raised := apply(x, op, c)
	if raised {
		return "0", true, nil
	}
	// ::varchar(500) truncates to 500 characters; the text is ASCII.
	return v[:min(len(v), 500)], true, nil
}

// parseFormula splits "#x# <op> <constant>", spaces optional around the
// operator and the constant.
func parseFormula(formula string) (op byte, constant string, err error) {
	rest, found := strings.CutPrefix(formula, "#x#")
	if !found {
		return 0, "", errConversionUnsupported
	}
	rest = strings.TrimLeft(rest, " ")
	if rest == "" || rest[0] != '*' && rest[0] != '/' {
		return 0, "", errConversionUnsupported
	}
	return rest[0], strings.Trim(rest[1:], " "), nil
}

// isIdentifier reports an unquoted ASCII SQL identifier or keyword.
func isIdentifier(s string) bool {
	if s == "" || '0' <= s[0] && s[0] <= '9' || s[0] == '$' {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !isAlnum(c) && c != '_' && c != '$' {
			return false
		}
	}
	return true
}

// sqlNumber is a value of one of the three types a literal can have: an
// integer literal is int4 when it fits, else int8, else numeric; a literal
// with a '.' is numeric. A numeric is unscaled / 10^scale, with scale its
// display scale (dscale).
type sqlNumber struct {
	kind     numberKind
	unscaled big.Int // non-negative for literals; any sign for results
	scale    int
}

type numberKind uint8

const (
	int4 numberKind = iota
	int8
	numeric
)

// parseLiteral reads an unsigned Postgres decimal literal: digits, digits '.'
// digits, digits '.', or '.' digits.
func parseLiteral(s string) (sqlNumber, bool) {
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" && frac == "" || !allDigits(intPart) || !allDigits(frac) {
		return sqlNumber{}, false
	}
	var n sqlNumber
	n.unscaled.SetString(intPart+frac, 10)
	n.scale = len(frac)
	switch {
	case hasDot:
		n.kind = numeric
	case n.unscaled.IsInt64() && n.unscaled.Int64() <= 1<<31-1:
		n.kind = int4
	case n.unscaled.IsInt64():
		n.kind = int8
	default:
		n.kind = numeric
	}
	return n, true
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// apply evaluates x op c as Postgres does: integer operators when both are
// integers (the wider type, raising on overflow; '/' truncates), numeric
// otherwise. raised reports an error Postgres would raise.
func apply(x sqlNumber, op byte, c sqlNumber) (text string, raised bool) {
	if x.kind != numeric && c.kind != numeric {
		bits := 32
		if x.kind == int8 || c.kind == int8 {
			bits = 64
		}
		var r big.Int
		if op == '*' {
			r.Mul(&x.unscaled, &c.unscaled)
		} else {
			if c.unscaled.Sign() == 0 {
				return "", true
			}
			r.Quo(&x.unscaled, &c.unscaled)
		}
		if r.BitLen() > bits-1 {
			return "", true // integer out of range
		}
		return r.String(), false
	}
	var r big.Int
	scale := 0
	if op == '*' {
		// numeric_mul: the exact product at the sum of the display scales.
		r.Mul(&x.unscaled, &c.unscaled)
		scale = x.scale + c.scale
	} else {
		if c.unscaled.Sign() == 0 {
			return "", true // division by zero
		}
		scale = divScale(&x, &c)
		// x/c * 10^scale = X * 10^(sc+scale-sx) / C, rounded half away from
		// zero; scale >= sx, so the exponent is not negative.
		num := new(big.Int).Mul(&x.unscaled, pow10(c.scale+scale-x.scale))
		var rem big.Int
		r.QuoRem(num, &c.unscaled, &rem)
		rem.Abs(&rem).Lsh(&rem, 1)
		if rem.CmpAbs(&c.unscaled) >= 0 {
			if num.Sign()*c.unscaled.Sign() < 0 {
				r.Sub(&r, big.NewInt(1))
			} else {
				r.Add(&r, big.NewInt(1))
			}
		}
	}
	return formatNumeric(&r, scale), false
}

// divScale is numeric.c's select_div_scale: at least NUMERIC_MIN_SIG_DIGITS
// (16) significant digits, estimated from the base-10000 weight and first
// digit of each operand, and never below either operand's display scale.
func divScale(x, c *sqlNumber) int {
	w1, f1 := weightAndFirstDigit(x)
	w2, f2 := weightAndFirstDigit(c)
	qweight := w1 - w2
	if f1 <= f2 {
		qweight--
	}
	return min(max(16-qweight*4, x.scale, c.scale, 0), 1000)
}

// weightAndFirstDigit is the weight and first digit of a numeric in
// Postgres's base-10000 form, where digit group w holds the decimal powers
// 4w to 4w+3; (0, 0) for zero.
func weightAndFirstDigit(n *sqlNumber) (weight, first int) {
	digits := new(big.Int).Abs(&n.unscaled).String()
	if digits == "0" {
		return 0, 0
	}
	msd := len(digits) - 1 - n.scale // power of ten of the leading digit
	weight = msd >> 2                // floor division by 4
	take := msd - 4*weight + 1
	lead := digits[:min(take, len(digits))] + strings.Repeat("0", max(take-len(digits), 0))
	first, _ = strconv.Atoi(lead)
	return weight, first
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

// formatNumeric is numeric_out: fixed point with exactly scale fraction
// digits, '-' only for a non-zero negative value.
func formatNumeric(r *big.Int, scale int) string {
	digits := new(big.Int).Abs(r).String()
	if len(digits) <= scale {
		digits = strings.Repeat("0", scale-len(digits)+1) + digits
	}
	s := digits
	if scale > 0 {
		s = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if r.Sign() < 0 {
		s = "-" + s
	}
	return s
}
