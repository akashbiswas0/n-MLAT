package mlat

import (
	"math"
	"sync"
)

const (
	maxTrackGapSec      = 120.0
	defaultMeasNoiseM   = 250.0
	processAccelSigmaMS = 18.0
)

type Tracker struct {
	mu     sync.Mutex
	tracks map[string]*TrackState
}

type TrackState struct {
	State    [6]float64
	Cov      [6][6]float64
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
		t.tracks[result.ICAO] = newTrackState(measurement, measurementNoiseVariance(result))
		out := *result
		out.TrackSamples = 1
		out.GroundSpeedMPS = 0
		return &out
	}

	dt := result.TimestampSec - track.LastTime
	if dt <= 0 || dt > maxTrackGapSec {
		resetTrackState(track, measurement, result.TimestampSec, measurementNoiseVariance(result))
		out := *result
		out.TrackSamples = track.Updates
		out.GroundSpeedMPS = 0
		return &out
	}

	predictTrack(track, dt)
	updateTrack(track, measurement, measurementNoiseVariance(result))
	track.LastTime = result.TimestampSec
	track.Updates++

	filtered := Vec3{track.State[0], track.State[1], track.State[2]}
	velocity := Vec3{track.State[3], track.State[4], track.State[5]}
	lat, lon, alt := ECEFToGeodetic(filtered)

	out := *result
	out.Lat = lat
	out.Lon = lon
	out.Alt = alt
	out.TrackSamples = track.Updates
	out.GroundSpeedMPS = groundSpeed(velocity)
	return &out
}

func newTrackState(pos Vec3, measurementVariance float64) *TrackState {
	track := &TrackState{}
	resetTrackState(track, pos, 0, measurementVariance)
	return track
}

func resetTrackState(track *TrackState, pos Vec3, timestampSec float64, measurementVariance float64) {
	track.State = [6]float64{pos.X, pos.Y, pos.Z, 0, 0, 0}
	track.Cov = [6][6]float64{}
	for axis := 0; axis < 3; axis++ {
		track.Cov[axis][axis] = measurementVariance
		track.Cov[axis+3][axis+3] = 250_000
	}
	track.LastTime = timestampSec
	track.Updates = 1
}

func measurementNoiseVariance(result *MLATResult) float64 {
	uncertainty := result.UncertaintyM
	if uncertainty <= 0 {
		uncertainty = defaultMeasNoiseM
	}
	if result.QualityScore > 0 {
		qualityFactor := clampFloat(1.25-result.QualityScore/140.0, 0.55, 1.4)
		uncertainty *= qualityFactor
	}
	return uncertainty * uncertainty
}

func predictTrack(track *TrackState, dt float64) {
	state := track.State
	for axis := 0; axis < 3; axis++ {
		state[axis] += state[axis+3] * dt
	}
	track.State = state

	var F [6][6]float64
	for i := 0; i < 6; i++ {
		F[i][i] = 1
	}
	F[0][3] = dt
	F[1][4] = dt
	F[2][5] = dt

	var predicted [6][6]float64
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			sum := 0.0
			for k := 0; k < 6; k++ {
				sum += F[i][k] * track.Cov[k][j]
			}
			predicted[i][j] = sum
		}
	}

	var propagated [6][6]float64
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			sum := 0.0
			for k := 0; k < 6; k++ {
				sum += predicted[i][k] * F[j][k]
			}
			propagated[i][j] = sum
		}
	}

	q := processAccelSigmaMS * processAccelSigmaMS
	dt2 := dt * dt
	dt3 := dt2 * dt
	dt4 := dt2 * dt2
	for axis := 0; axis < 3; axis++ {
		p := axis
		v := axis + 3
		propagated[p][p] += q * dt4 / 4
		propagated[p][v] += q * dt3 / 2
		propagated[v][p] += q * dt3 / 2
		propagated[v][v] += q * dt2
	}
	track.Cov = propagated
}

func updateTrack(track *TrackState, measurement Vec3, measurementVariance float64) {
	z := [3]float64{measurement.X, measurement.Y, measurement.Z}
	var residual [3]float64
	for axis := 0; axis < 3; axis++ {
		residual[axis] = z[axis] - track.State[axis]
	}

	var S [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			S[i][j] = track.Cov[i][j]
		}
		S[i][i] += measurementVariance
	}

	SInv, ok := invert3x3(S)
	if !ok {
		return
	}

	var K [6][3]float64
	for i := 0; i < 6; i++ {
		for j := 0; j < 3; j++ {
			sum := 0.0
			for k := 0; k < 3; k++ {
				sum += track.Cov[i][k] * SInv[k][j]
			}
			K[i][j] = sum
		}
	}

	for i := 0; i < 6; i++ {
		correction := 0.0
		for j := 0; j < 3; j++ {
			correction += K[i][j] * residual[j]
		}
		track.State[i] += correction
	}

	var KH [6][6]float64
	for i := 0; i < 6; i++ {
		for j := 0; j < 3; j++ {
			KH[i][j] = K[i][j]
		}
	}

	var IminusKH [6][6]float64
	for i := 0; i < 6; i++ {
		IminusKH[i][i] = 1
		for j := 0; j < 6; j++ {
			IminusKH[i][j] -= KH[i][j]
		}
	}

	var updated [6][6]float64
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			sum := 0.0
			for k := 0; k < 6; k++ {
				sum += IminusKH[i][k] * track.Cov[k][j]
			}
			updated[i][j] = sum
		}
	}
	track.Cov = updated
}

func invert3x3(m [3][3]float64) ([3][3]float64, bool) {
	det :=
		m[0][0]*(m[1][1]*m[2][2]-m[1][2]*m[2][1]) -
			m[0][1]*(m[1][0]*m[2][2]-m[1][2]*m[2][0]) +
			m[0][2]*(m[1][0]*m[2][1]-m[1][1]*m[2][0])
	if math.Abs(det) < 1e-12 {
		return [3][3]float64{}, false
	}
	invDet := 1 / det
	return [3][3]float64{
		{
			(m[1][1]*m[2][2] - m[1][2]*m[2][1]) * invDet,
			(m[0][2]*m[2][1] - m[0][1]*m[2][2]) * invDet,
			(m[0][1]*m[1][2] - m[0][2]*m[1][1]) * invDet,
		},
		{
			(m[1][2]*m[2][0] - m[1][0]*m[2][2]) * invDet,
			(m[0][0]*m[2][2] - m[0][2]*m[2][0]) * invDet,
			(m[0][2]*m[1][0] - m[0][0]*m[1][2]) * invDet,
		},
		{
			(m[1][0]*m[2][1] - m[1][1]*m[2][0]) * invDet,
			(m[0][1]*m[2][0] - m[0][0]*m[2][1]) * invDet,
			(m[0][0]*m[1][1] - m[0][1]*m[1][0]) * invDet,
		},
	}, true
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
