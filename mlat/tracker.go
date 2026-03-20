package mlat

import (
	"math"
	"sync"
)

const (
	maxTrackGapSec = 120.0
	alphaMin       = 0.45
	alphaMax       = 0.8
	betaMin        = 0.08
	betaMax        = 0.25
)

type Tracker struct {
	mu     sync.Mutex
	tracks map[string]*TrackState
}

type TrackState struct {
	Position Vec3
	Velocity Vec3
	LastTime float64
	Updates  int
}

func NewTracker() *Tracker {
	return &Tracker{
		tracks: make(map[string]*TrackState),
	}
}

func (t *Tracker) Update(result *MLATResult) *MLATResult {
	if result == nil {
		return nil
	}

	measurement := GeodeticToECEF(result.Lat, result.Lon, result.Alt)

	t.mu.Lock()
	defer t.mu.Unlock()

	track, ok := t.tracks[result.ICAO]
	if !ok || result.TimestampSec <= 0 {
		t.tracks[result.ICAO] = &TrackState{
			Position: measurement,
			LastTime: result.TimestampSec,
			Updates:  1,
		}
		out := *result
		out.TrackSamples = 1
		out.GroundSpeedMPS = 0
		return &out
	}

	dt := result.TimestampSec - track.LastTime
	if dt <= 0 || dt > maxTrackGapSec {
		track.Position = measurement
		track.Velocity = Vec3{}
		track.LastTime = result.TimestampSec
		track.Updates = 1
		out := *result
		out.TrackSamples = track.Updates
		out.GroundSpeedMPS = 0
		return &out
	}

	alpha, beta := filterGains(result)
	predicted := track.Position.Add(track.Velocity.Scale(dt))
	residual := measurement.Sub(predicted)

	track.Position = predicted.Add(residual.Scale(alpha))
	track.Velocity = track.Velocity.Add(residual.Scale(beta / dt))
	track.LastTime = result.TimestampSec
	track.Updates++

	lat, lon, alt := ECEFToGeodetic(track.Position)
	out := *result
	out.Lat = lat
	out.Lon = lon
	out.Alt = alt
	out.TrackSamples = track.Updates
	out.GroundSpeedMPS = groundSpeed(track.Velocity)
	return &out
}

func filterGains(result *MLATResult) (float64, float64) {
	alpha := alphaMin + 0.08*float64(minInt(result.NumSensors, 6)-4)
	alpha = clampFloat(alpha, alphaMin, alphaMax)

	beta := betaMin + 0.04*float64(minInt(result.NumSensors, 6)-4)
	beta = clampFloat(beta, betaMin, betaMax)

	return alpha, beta
}

func groundSpeed(v Vec3) float64 {
	return math.Sqrt(v.X*v.X + v.Y*v.Y + v.Z*v.Z)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
