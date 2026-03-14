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
)

type ClockCalibrator struct {
	mu      sync.RWMutex
	sensors map[int64]*ClockState
}

type ClockState struct {
	OffsetNs                 float64
	Drift                    float64
	ReferenceTimestampNs     float64
	LastTimestampNs          float64
	LastMeasuredCorrectionNs float64
	JitterNs                 float64
	LastResidualNs           float64
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
}

func NewClockCalibrator() *ClockCalibrator {
	return &ClockCalibrator{
		sensors: make(map[int64]*ClockState),
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
			calibrated[i].TimestampAdjustmentNs = state.correction(float64(observation.TimestampNs()))
			if state.reliable() {
				reliableIdx = append(reliableIdx, i)
			}
			continue
		}
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
				Samples:                  1,
			}
			continue
		}

		state.LastResidualNs = residualNs
		predictedCorrectionNs := state.correction(rawTimestampNs)
		correctionErrorNs := measuredCorrectionNs - predictedCorrectionNs
		state.JitterNs = blend(state.JitterNs, math.Abs(correctionErrorNs), clockJitterBlend)
		if math.Abs(residualNs) > maxResidualUpdateNs {
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
	}
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
		})
	}
	return diagnostics
}

func (s *ClockState) correction(rawTimestampNs float64) float64 {
	return clampFloat(s.OffsetNs+s.Drift*(rawTimestampNs-s.ReferenceTimestampNs), -maxClockOffsetNs, maxClockOffsetNs)
}

func (s *ClockState) reliable() bool {
	return s.Samples < minReliableSamples || s.JitterNs <= maxReliableJitterNs
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

func blend(current, measured, alpha float64) float64 {
	return current*(1-alpha) + measured*alpha
}

func clampFloat(value, minValue, maxValue float64) float64 {
	return math.Max(minValue, math.Min(maxValue, value))
}
