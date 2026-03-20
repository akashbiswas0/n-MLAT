package mlat

import "testing"

func TestTrackerSmoothsSuccessiveFixes(t *testing.T) {
	tracker := NewTracker()

	first := tracker.Update(&MLATResult{
		ICAO:         "ABC123",
		Lat:          50.2000,
		Lon:          -5.2000,
		Alt:          7000,
		NumSensors:   4,
		TimestampSec: 100,
	})
	if first == nil || first.TrackSamples != 1 {
		t.Fatalf("unexpected first track result: %+v", first)
	}

	second := tracker.Update(&MLATResult{
		ICAO:         "ABC123",
		Lat:          50.2600,
		Lon:          -5.2600,
		Alt:          7100,
		NumSensors:   4,
		TimestampSec: 101,
	})
	if second == nil {
		t.Fatal("expected second tracked result")
	}
	if second.TrackSamples != 2 {
		t.Fatalf("expected second track sample count to be 2, got %d", second.TrackSamples)
	}
	if second.GroundSpeedMPS <= 0 {
		t.Fatalf("expected positive ground speed, got %.2f", second.GroundSpeedMPS)
	}
	if second.Lat == 50.2600 && second.Lon == -5.2600 {
		t.Fatal("expected tracker to smooth the second fix rather than pass it through unchanged")
	}
}
