package decoder

// checkDigitWeights are the per-position weights of both fVINCheckDigit
// functions; position 9, the check digit itself, weighs nothing.
var checkDigitWeights = [17]int{8, 7, 6, 5, 4, 3, 2, 10, 0, 9, 8, 7, 6, 5, 4, 3, 2}

// checkDigit1 ports fVINCheckDigit (fvincheckdigit.sql), used by error
// code 3: positions 13 and 14 must be digits unless position 3 is '9'.
func checkDigit1(vin string) byte {
	return checkDigit(vin, true)
}

// checkDigit2 ports fVINCheckDigit2 (fvincheckdigit2.sql), used by error
// code 1: position 14 must be a digit unless position 3 is '9', position 13
// only when the WMI is a car, MPV or light truck as well.
func checkDigit2(vin string, carMpvLT bool) byte {
	return checkDigit(vin, carMpvLT)
}

// checkDigit is the body both SQL functions share. It returns 0 for a VIN
// that is not 17 characters (the SQL returns an empty string), '?' when a character fails
// its position's class, else '0'-'9' or 'X'.
//
// Bug-for-bug, in the SQL's terms:
//   - The classes are case-insensitive regexes written with commas,
//     [a-h,j-n,p,r-z,0-9], so ',' and lowercase letters pass them; the
//     transliteration CASE is case-sensitive and gives both -1.
//   - The REAL arithmetic `temp - floor(temp)` makes the remainder of a
//     negative sum non-negative: Euclidean modulo 11.
//   - '?' is a value, not an error: a VIN with '?' at position 9 equals it.
func checkDigit(vin string, numeric13 bool) byte {
	if len(vin) != 17 {
		return 0
	}
	sum := 0
	for i := range 17 {
		c := vin[i]
		var ok bool
		switch pos := i + 1; {
		case pos == 10:
			ok = inModelYearClass(c)
		case pos == 13 && vin[2] != '9' && numeric13,
			pos == 14 && vin[2] != '9',
			pos >= 15:
			ok = '0' <= c && c <= '9'
		default:
			ok = inDefaultClass(c)
		}
		if !ok {
			return '?'
		}
		sum += transliterate(c) * checkDigitWeights[i]
	}
	r := sum % 11
	if r < 0 {
		r += 11
	}
	if r == 10 {
		return 'X'
	}
	return '0' + byte(r)
}

// inDefaultClass is `c ~* '[a-h,j-n,p,r-z,0-9]'`.
func inDefaultClass(c byte) bool {
	c = asciiUpper(c)
	return c == ',' || '0' <= c && c <= '9' ||
		'A' <= c && c <= 'H' || 'J' <= c && c <= 'N' || c == 'P' || 'R' <= c && c <= 'Z'
}

// inModelYearClass is `c ~* '[a-h,j-n,p,r-t,v-y,1-9]'`.
func inModelYearClass(c byte) bool {
	c = asciiUpper(c)
	return c == ',' || '1' <= c && c <= '9' ||
		'A' <= c && c <= 'H' || 'J' <= c && c <= 'N' || c == 'P' ||
		'R' <= c && c <= 'T' || 'V' <= c && c <= 'Y'
}

func asciiUpper(c byte) byte {
	if 'a' <= c && c <= 'z' {
		return c - 'a' + 'A'
	}
	return c
}

// transliterate is the SQL's CASE: digits are themselves, letters per the
// VIN standard (I, O and Q have no value), anything else -1.
func transliterate(c byte) int {
	switch {
	case '0' <= c && c <= '9':
		return int(c - '0')
	case 'A' <= c && c <= 'H':
		return int(c-'A') + 1
	case 'J' <= c && c <= 'N':
		return int(c-'J') + 1
	case c == 'P':
		return 7
	case c == 'R':
		return 9
	case 'S' <= c && c <= 'Z':
		return int(c-'S') + 2
	}
	return -1
}
