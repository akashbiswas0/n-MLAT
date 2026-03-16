package mlat

import "encoding/hex"

type Observation struct {
	ICAO                 string  // 6-char hex aircraft identifier e.g. "3C4521"
	SensorID             int64   // unique ID of the receiving sensor
	SensorLat            float64 // sensor latitude
	SensorLon            float64 // sensor longitude
	SensorAlt            float64 // sensor altitude in meters
	SecondsSinceMidnight uint64  // coarse timestamp
	Nanoseconds          uint64  // fine timestamp (nanosecond precision)
	RawModeS             []byte  // raw Mode-S message bytes
}

func (o *Observation) TimestampNs() int64 {
	return int64(o.SecondsSinceMidnight*1_000_000_000 + o.Nanoseconds)
}

type MLATResult struct {
	ICAO       string  // aircraft identifier
	Lat        float64 // estimated latitude
	Lon        float64 // estimated longitude
	Alt        float64 // estimated altitude in meters
	Cost       float64 // solver residual (lower = more confident)
	NumSensors int     // how many sensors contributed
}

func (o Observation) CorrelationKey() string {
	return o.ICAO + "|" + hex.EncodeToString(o.RawModeS)
}
