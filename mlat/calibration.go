package mlat

import (
	"math"
	"sync"
)

const (
	maxClockOffsetNs      = 50_000_000.0
	maxClockDrift         = 1e-5
	clockOffsetBlend      = 0.2
	clockDriftBlend       = 0.15
	clockJitterBlend      = 0.2
	minDriftUpdateDeltaNs = 1_000_000_000.0
	maxResidualUpdateNs   = 8_000_000.0
	maxReliableJitterNs   = 2_500_000.0
	minReliableSamples    = 6
	minTrustedScore       = 35.0
)

type ClockCalibrator struct {
	mu      sync.RWMutex
	sensors map[int64]*ClockState
	pairs   map[sensorPairKey]*PairOffsetState
}

type sensorPairKey struct {
	A int64
	B int64
}

type PairOffsetState struct {
	OffsetNs float64
	JitterNs float64
	Samples  int
}

type ClockState struct {
	OffsetNs                 float64
	Drift                    float64
	ReferenceTimestampNs     float64
	LastTimestampNs          float64
	LastMeasuredCorrectionNs float64
	JitterNs                 float64
	LastResidualNs           float64
	ResidualEMAAbsNs         float64
	TrustScore               float64
	Samples                  int
}

type ClockDiagnostic struct {
	SensorID   int64   `json:"sensor_id"`
	OffsetNs   float64 `json:"offset_ns"`
	DriftPPM   float64 `json:"drift_ppm"`
	JitterNs   float64 `json:"jitter_ns"`
	Samples    int     `json:"samples"`
	Health     string  `json:"health"`
	Adjustment float64 `json:"adjustment_ns"`
	TrustScore float64 `json:"trust_score"`
	TrustLabel string  `json:"trust_label"`
}

func NewClockCalibrator() *ClockCalibrator {
	return &ClockCalibrator{
		sensors: make(map[int64]*ClockState),
		pairs:   make(map[sensorPairKey]*PairOffsetState),
	}
}

func (c *ClockCalibrator) Apply(obs []Observation) []Observation {
	c.mu.RLock()
	defer c.mu.RUnlock()

	calibrated := make([]Observation, len(obs))
	reliableIdx := make([]int, 0, len(obs))
	for i, observation := range obs {
		calibrated[i] = observation
		if state, ok := c.sensors[observation.SensorID]; ok {
			baseCorrection := state.correction(float64(observation.TimestampNs()))
			calibrated[i].TimestampAdjustmentNs = c.consensusCorrection(i, obs, baseCorrection)
			calibrated[i].SellerScore = state.score()
			if state.reliable() && state.score() >= minTrustedScore {
				reliableIdx = append(reliableIdx, i)
			}
			continue
		}
		calibrated[i].TimestampAdjustmentNs = c.consensusCorrection(i, obs, 0)
		calibrated[i].SellerScore = 70
		reliableIdx = append(reliableIdx, i)
	}

	if len(reliableIdx) >= MinSensors && len(reliableIdx) < len(calibrated) {
		filtered := make([]Observation, 0, len(reliableIdx))
		for _, idx := range reliableIdx {
			filtered = append(filtered, calibrated[idx])
		}
		return filtered
	}
	return calibrated
}

func (c *ClockCalibrator) Update(obs []Observation, result *MLATResult) {
	if result == nil || len(result.SensorResidualSec) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	refPos := GeodeticToECEF(result.Lat, result.Lon, result.Alt)

	for _, observation := range obs {
		residualSec, ok := result.SensorResidualSec[observation.SensorID]
		if !ok {
			continue
		}

		rawTimestampNs := float64(observation.TimestampNs())
		residualNs := residualSec * 1e9
		measuredCorrectionNs := observation.TimestampAdjustmentNs + residualNs
		measuredCorrectionNs = clampFloat(measuredCorrectionNs, -maxClockOffsetNs, maxClockOffsetNs)

		state, ok := c.sensors[observation.SensorID]
		if !ok {
			c.sensors[observation.SensorID] = &ClockState{
				OffsetNs:                 measuredCorrectionNs,
				ReferenceTimestampNs:     rawTimestampNs,
				LastTimestampNs:          rawTimestampNs,
				LastMeasuredCorrectionNs: measuredCorrectionNs,
				JitterNs:                 math.Abs(residualNs),
				LastResidualNs:           residualNs,
				ResidualEMAAbsNs:         math.Abs(residualNs),
				TrustScore:               70,
				Samples:                  1,
			}
			continue
		}

		state.LastResidualNs = residualNs
		predictedCorrectionNs := state.correction(rawTimestampNs)
		correctionErrorNs := measuredCorrectionNs - predictedCorrectionNs
		state.JitterNs = blend(state.JitterNs, math.Abs(correctionErrorNs), clockJitterBlend)
		state.ResidualEMAAbsNs = blend(state.ResidualEMAAbsNs, math.Abs(residualNs), 0.2)
		if math.Abs(residualNs) > maxResidualUpdateNs {
			state.TrustScore = state.computeTrust()
			continue
		}

		if dt := rawTimestampNs - state.LastTimestampNs; dt >= minDriftUpdateDeltaNs {
			measuredDrift := (measuredCorrectionNs - state.LastMeasuredCorrectionNs) / dt
			state.Drift = blend(state.Drift, clampFloat(measuredDrift, -maxClockDrift, maxClockDrift), clockDriftBlend)
		}

		offsetAtReference := measuredCorrectionNs - state.Drift*(rawTimestampNs-state.ReferenceTimestampNs)
		state.OffsetNs = blend(state.OffsetNs, clampFloat(offsetAtReference, -maxClockOffsetNs, maxClockOffsetNs), clockOffsetBlend)
		state.LastTimestampNs = rawTimestampNs
		state.LastMeasuredCorrectionNs = measuredCorrectionNs
		state.Samples++
		state.TrustScore = state.computeTrust()
	}

	c.updatePairOffsets(obs, result, refPos)
}

func (c *ClockCalibrator) DecorateContributors(contributors []SensorContribution) []SensorContribution {
	c.mu.RLock()
	defer c.mu.RUnlock()

	annotated := make([]SensorContribution, len(contributors))
	copy(annotated, contributors)
	for i, contributor := range annotated {
		state, ok := c.sensors[contributor.SensorID]
		if !ok {
			annotated[i].ClockHealth = "learning"
			continue
		}
		annotated[i].ClockAdjustmentNs = state.OffsetNs
		annotated[i].ClockJitterNs = state.JitterNs
		annotated[i].ClockSamples = state.Samples
		annotated[i].ClockHealth = state.health()
		annotated[i].SellerScore = state.score()
		annotated[i].TrustLabel = trustLabelForScore(state.score())
	}
	return annotated
}

func (c *ClockCalibrator) Snapshot(sensorIDs []int64) []ClockDiagnostic {
	c.mu.RLock()
	defer c.mu.RUnlock()

	diagnostics := make([]ClockDiagnostic, 0, len(sensorIDs))
	for _, sensorID := range sensorIDs {
		state, ok := c.sensors[sensorID]
		if !ok {
			diagnostics = append(diagnostics, ClockDiagnostic{
				SensorID: sensorID,
				Health:   "learning",
			})
			continue
		}
		diagnostics = append(diagnostics, ClockDiagnostic{
			SensorID:   sensorID,
			OffsetNs:   state.OffsetNs,
			DriftPPM:   state.Drift * 1e6,
			JitterNs:   state.JitterNs,
			Samples:    state.Samples,
			Health:     state.health(),
			Adjustment: state.OffsetNs,
			TrustScore: state.score(),
			TrustLabel: trustLabelForScore(state.score()),
		})
	}
	return diagnostics
}

func (c *ClockCalibrator) consensusCorrection(idx int, obs []Observation, baseCorrection float64) float64 {
	if idx < 0 || idx >= len(obs) {
		return baseCorrection
	}

	estimates := []float64{baseCorrection}
	weights := []float64{1.0}
	for j, peer := range obs {
		if j == idx {
			continue
		}
		peerState, ok := c.sensors[peer.SensorID]
		if !ok || !peerState.reliable() {
			continue
		}
		pair, ok := c.pairs[pairKey(obs[idx].SensorID, peer.SensorID)]
		if !ok || pair.Samples < 4 {
			continue
		}
		pairOffset := pair.OffsetNs
		if obs[idx].SensorID > peer.SensorID {
			pairOffset = -pairOffset
		}
		peerCorrection := peerState.correction(float64(peer.TimestampNs()))
		estimate := peerCorrection - pairOffset
		weight := clampFloat(float64(pair.Samples)/20.0, 0.2, 1.0) * clampFloat(1-pair.JitterNs/3_000_000, 0.2, 1.0)
		estimates = append(estimates, estimate)
		weights = append(weights, weight)
	}

	total := 0.0
	weightTotal := 0.0
	for i, estimate := range estimates {
		total += estimate * weights[i]
		weightTotal += weights[i]
	}
	if weightTotal == 0 {
		return baseCorrection
	}
	return total / weightTotal
}

func (c *ClockCalibrator) updatePairOffsets(obs []Observation, result *MLATResult, refPos Vec3) {
	for i := 0; i < len(obs); i++ {
		for j := i + 1; j < len(obs); j++ {
			a := obs[i]
			b := obs[j]
			aPos := GeodeticToECEF(a.SensorLat, a.SensorLon, a.SensorAlt)
			bPos := GeodeticToECEF(b.SensorLat, b.SensorLon, b.SensorAlt)
			expectedDeltaNs := ((refPos.Dist(aPos) - refPos.Dist(bPos)) / C) * 1e9
			observedDeltaNs := float64(a.TimestampNs()-b.TimestampNs()) + a.TimestampAdjustmentNs - b.TimestampAdjustmentNs
			offsetNs := observedDeltaNs - expectedDeltaNs

			key := pairKey(a.SensorID, b.SensorID)
			pair, ok := c.pairs[key]
			if !ok {
				c.pairs[key] = &PairOffsetState{
					OffsetNs: offsetNs,
					JitterNs: 0,
					Samples:  1,
				}
				continue
			}
			errNs := offsetNs - pair.OffsetNs
			pair.JitterNs = blend(pair.JitterNs, math.Abs(errNs), 0.2)
			pair.OffsetNs = blend(pair.OffsetNs, offsetNs, 0.18)
			pair.Samples++
		}
	}
}

func pairKey(a, b int64) sensorPairKey {
	if a < b {
		return sensorPairKey{A: a, B: b}
	}
	return sensorPairKey{A: b, B: a}
}

func (s *ClockState) correction(rawTimestampNs float64) float64 {
	return clampFloat(s.OffsetNs+s.Drift*(rawTimestampNs-s.ReferenceTimestampNs), -maxClockOffsetNs, maxClockOffsetNs)
}

func (s *ClockState) reliable() bool {
	return s.Samples < minReliableSamples || s.JitterNs <= maxReliableJitterNs
}

func (s *ClockState) score() float64 {
	if s.TrustScore == 0 {
		return 70
	}
	return s.TrustScore
}

func (s *ClockState) health() string {
	switch {
	case s.Samples < minReliableSamples:
		return "learning"
	case s.JitterNs < 750_000:
		return "stable"
	case s.JitterNs < maxReliableJitterNs:
		return "watch"
	default:
		return "unstable"
	}
}

func (s *ClockState) computeTrust() float64 {
	jitterScore := clampFloat(1-s.JitterNs/3_000_000, 0, 1)
	residualScore := clampFloat(1-s.ResidualEMAAbsNs/2_500_000, 0, 1)
	sampleScore := clampFloat(float64(s.Samples)/18.0, 0, 1)
	return 100 * (0.4*jitterScore + 0.4*residualScore + 0.2*sampleScore)
}

func trustLabelForScore(score float64) string {
	switch {
	case score >= 75:
		return "VERIFIED"
	case score >= 50:
		return "WATCH"
	default:
		return "LOW"
	}
}

func blend(current, measured, alpha float64) float64 {
	return current*(1-alpha) + measured*alpha
}

func clampFloat(value, minValue, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}
