package mlat

import (
	"testing"
	"time"
)

func TestBufferRequiresSameFrameAcrossSensors(t *testing.T) {
	ready := make(chan []Observation, 1)
	buf := NewBuffer(func(_ string, obs []Observation) {
		ready <- obs
	})

	base := uint64(100_000_000)
	frameA := []byte{0x8d, 0xaa, 0xbb, 0xcc, 0x01}
	frameB := []byte{0x8d, 0xaa, 0xbb, 0xcc, 0x02}

	for i := 0; i < 2; i++ {
		buf.Add(Observation{
			ICAO:                 "AABBCC",
			SensorID:             int64(i + 1),
			SecondsSinceMidnight: 0,
			Nanoseconds:          base + uint64(i)*500,
			RawModeS:             frameA,
		})
	}

	for i := 0; i < 2; i++ {
		buf.Add(Observation{
			ICAO:                 "AABBCC",
			SensorID:             int64(i + 3),
			SecondsSinceMidnight: 0,
			Nanoseconds:          base + uint64(i)*500,
			RawModeS:             frameB,
		})
	}

	select {
	case got := <-ready:
		t.Fatalf("unexpected MLAT trigger with mixed frames: got %d observations", len(got))
	case <-time.After(150 * time.Millisecond):
	}
}

func TestBufferSeparatesRepeatedSamePayloadByTimestamp(t *testing.T) {
	ready := make(chan []Observation, 1)
	buf := NewBuffer(func(_ string, obs []Observation) {
		ready <- obs
	})

	frame := []byte{0x8d, 0xaa, 0xbb, 0xcc, 0x03}
	base := int64(100_000_000)
	later := base + int64(CorrelationWindow) + int64(5*time.Millisecond)

	for i := 0; i < 2; i++ {
		buf.Add(Observation{
			ICAO:                 "AABBCC",
			SensorID:             int64(i + 1),
			SecondsSinceMidnight: 0,
			Nanoseconds:          uint64(base + int64(i)*500),
			RawModeS:             frame,
		})
	}

	for i := 0; i < 2; i++ {
		buf.Add(Observation{
			ICAO:                 "AABBCC",
			SensorID:             int64(i + 3),
			SecondsSinceMidnight: 0,
			Nanoseconds:          uint64(later + int64(i)*500),
			RawModeS:             frame,
		})
	}

	select {
	case got := <-ready:
		t.Fatalf("unexpected MLAT trigger with repeated payloads: got %d observations", len(got))
	case <-time.After(150 * time.Millisecond):
	}
}
