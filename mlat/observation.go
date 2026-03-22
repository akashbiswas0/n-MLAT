package mlat

import "encoding/hex"

type Observation struct {
	ICAO                  string // 6-char hex aircraft identifier e.g. "3C4521"
	SensorID              int64  // unique ID of the receiving sensor
	SensorName            string
	SensorLat             float64 // sensor latitude
	SensorLon             float64 // sensor longitude
	SensorAlt             float64 // sensor altitude in meters
	SecondsSinceMidnight  uint64  // coarse timestamp
	Nanoseconds           uint64  // fine timestamp (nanosecond precision)
	RawModeS              []byte  // raw Mode-S message bytes
	TimestampAdjustmentNs float64 // learned per-sensor clock correction
	SellerScore           float64
}

func (o *Observation) TimestampNs() int64 {
	return int64(o.SecondsSinceMidnight*1_000_000_000 + o.Nanoseconds)
}

func (o *Observation) CorrectedTimestampSeconds() float64 {
	return float64(o.TimestampNs())*1e-9 + o.TimestampAdjustmentNs*1e-9
}

type MLATResult struct {
	ICAO              string  // aircraft identifier
	Lat               float64 // estimated latitude
	Lon               float64 // estimated longitude
	Alt               float64 // estimated altitude in meters
	Cost              float64 // solver residual (lower = more confident)
	NumSensors        int     // how many sensors contributed
	TimestampSec      float64
	SensorResidualSec map[int64]float64
	GroundSpeedMPS    float64
	TrackSamples      int
	RMSResidualM      float64
	UncertaintyM      float64
	GDOP              float64
	QualityScore      float64
	QualityLabel      string
	TrustScore        float64
	TrustLabel        string
	Contributors      []SensorContribution
}

type SensorContribution struct {
	SensorID          int64   `json:"sensor_id"`
	SensorName        string  `json:"sensor_name"`
	Lat               float64 `json:"lat"`
	Lon               float64 `json:"lon"`
	Alt               float64 `json:"alt"`
	ResidualM         float64 `json:"residual_m"`
	ClockAdjustmentNs float64 `json:"clock_adjustment_ns"`
	ClockJitterNs     float64 `json:"clock_jitter_ns"`
	ClockSamples      int     `json:"clock_samples"`
	ClockHealth       string  `json:"clock_health"`
	SellerScore       float64 `json:"seller_score"`
	TrustLabel        string  `json:"trust_label"`
}

func (o Observation) CorrelationKey() string {
	return o.ICAO + "|" + hex.EncodeToString(o.RawModeS)
}
