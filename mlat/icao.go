package mlat

import "fmt"


func ExtractICAO(raw []byte) (string, bool) {
	if len(raw) < 4 {
		return "", false
	}

	df := (raw[0] >> 3) & 0x1F

	switch df {
	case 11, 17, 18, 19:
		icao := uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
		return fmt.Sprintf("%06X", icao), true
	case 20, 21:
		icao := uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
		return fmt.Sprintf("%06X", icao), true
	default:
	
		return "", false
	}
}
