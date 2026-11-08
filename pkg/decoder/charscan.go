package decoder

// scanStart ports how spvindecode_core chooses where the invalid-character
// scan starts requiring digits (spvindecode_core.sql:415-425), and the
// car/MPV/LT flag it then hands fVINCheckDigit2. A '9' at position 3 wins
// over the WMI row: start 15 and flag false. carMpvLT is isCarMpvLT of the
// wmi row matching wmi(vin), false when there is none.
func scanStart(vin string, carMpvLT bool) (startPos int, checkDigitCarMpvLT bool) {
	switch {
	case len(vin) >= 3 && vin[2] == '9':
		return 15, false
	case carMpvLT:
		return 13, true
	default:
		return 14, false
	}
}

// invalidAt is the four position classes of the invalid-character scan
// (spvindecode_core.sql:434-437) for character c at 1-based position j,
// kept as the SQL's OR of four clauses. '*' passes every class except
// position 10's. Skipping position 9 for off-road and check-digit-exception
// VINs is the caller's.
func invalidAt(j int, c byte, startPos int) bool {
	return j != 9 && j < startPos && !inScanVINClass(c) ||
		j != 9 && j >= startPos && !('0' <= c && c <= '9' || c == '*') ||
		j == 9 && !('0' <= c && c <= '9' || c == 'X' || c == '*') ||
		j == 10 && !inScanModelYearClass(c)
}

// inScanVINClass is `c ~ '^[0-9ABCDEFGHJKLMNPRSTUVWXYZ*]$'`.
func inScanVINClass(c byte) bool {
	return c == '*' || '0' <= c && c <= '9' ||
		'A' <= c && c <= 'Z' && c != 'I' && c != 'O' && c != 'Q'
}

// inScanModelYearClass is `c ~ '^[1-9ABCDEFGHJKLMNPRSTVWXY]$'`.
func inScanModelYearClass(c byte) bool {
	return '1' <= c && c <= '9' ||
		'A' <= c && c <= 'Y' && c != 'I' && c != 'O' && c != 'Q' && c != 'U'
}
