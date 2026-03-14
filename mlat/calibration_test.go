package mlat

import "testing"

func TestClockCalibratorLearnsSensorOffset(t *testing.T) {
	calibrator := NewClockCalibrator()
	obs := []Observation{
		{
			ICAO:                 "ABC123",
			SensorID:             42,
			SecondsSinceMidnight: 10,
			Nanoseconds:          500,
		},
	}

	result := &MLATResult{
		SensorResidualSec: map[int64]float64{
			42: 2e-6,
		},
	}

	calibrator.Update(obs, result)
	calibrated := calibrator.Apply(obs)
	if len(calibrated) != 1 {
		t.Fatalf("unexpected calibrated obs count: %d", len(calibrated))
	}
	if calibrated[0].TimestampAdjustmentNs < 1900 || calibrated[0].TimestampAdjustmentNs > 2100 {
		t.Fatalf("expected learned offset near 2000ns, got %.2f", calibrated[0].TimestampAdjustmentNs)
	}
}
