package decoder

// isCarMpvLT is the vehicle-type test fVinModelYear2 and spvindecode_core
// both apply to the WMI row: vehicle type 2 or 7, or 3 with truck type 1. A
// NULL truck type is any value other than 1.
func isCarMpvLT(vehicleTypeID, truckTypeID int) bool {
	return vehicleTypeID == 2 || vehicleTypeID == 7 || vehicleTypeID == 3 && truckTypeID == 1
}

// modelYearRaw ports fVinModelYear2 (fvinmodelyear2.sql) on a sanitized VIN.
// ok is false when position 10 maps to no year (I, O, Q, U, Z, 0, anything
// else, or a VIN shorter than 10); the SQL returns NULL. A negative year is
// inconclusive, as in the SQL.
//
// carLT is isCarMpvLT of the wmi row whose wmi equals wmi(vin), false when
// there is none; the SQL reads it with no availability filter. currentYear
// is the year of NOW() in UTC: a year more than two past it is taken as 30
// years earlier, even for an unknown WMI.
func modelYearRaw(vin string, carLT bool, currentYear int) (year int, ok bool) {
	if len(vin) < 10 {
		return 0, false
	}
	switch c := vin[9]; {
	case 'A' <= c && c <= 'H':
		year = 2010 + int(c-'A')
	case 'J' <= c && c <= 'N':
		year = 2010 + int(c-'A') - 1
	case c == 'P':
		year = 2023
	case 'R' <= c && c <= 'T':
		year = 2010 + int(c-'A') - 3
	case 'V' <= c && c <= 'Y':
		year = 2010 + int(c-'A') - 4
	case '1' <= c && c <= '9':
		year = 2031 + int(c-'1')
	default:
		return 0, false
	}
	conclusive := false
	if pos7 := vin[6]; carLT {
		if '0' <= pos7 && pos7 <= '9' {
			year -= 30
			conclusive = true
		} else if 'A' <= pos7 && pos7 <= 'Z' {
			conclusive = true
		}
	}
	if year > currentYear+2 {
		year -= 30
		conclusive = true
	}
	if !conclusive {
		year = -year
	}
	return year, true
}
