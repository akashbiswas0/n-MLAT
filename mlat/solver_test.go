package mlat

import "testing"

func TestSolveSyntheticTDOA(t *testing.T) {
	sensors := []struct {
		id  int64
		lat float64
		lon float64
		alt float64
	}{
		{id: 1, lat: 50.0800, lon: -5.3200, alt: 120},
		{id: 2, lat: 50.0800, lon: -5.0800, alt: 80},
		{id: 3, lat: 50.3200, lon: -5.3200, alt: 95},
		{id: 4, lat: 50.3200, lon: -5.0800, alt: 60},
		{id: 5, lat: 50.2000, lon: -5.4500, alt: 110},
	}

	target := GeodeticToECEF(50.2000, -5.2000, 7000)
	emissionNs := int64(500_000_000_000)

	obs := make([]Observation, 0, len(sensors))
	for _, sensor := range sensors {
		rx := GeodeticToECEF(sensor.lat, sensor.lon, sensor.alt)
		arrivalNs := emissionNs + int64((target.Dist(rx)/C)*1e9)
		obs = append(obs, Observation{
			ICAO:                 "ABC123",
			SensorID:             sensor.id,
			SensorLat:            sensor.lat,
			SensorLon:            sensor.lon,
			SensorAlt:            sensor.alt,
			SecondsSinceMidnight: uint64(arrivalNs / 1_000_000_000),
			Nanoseconds:          uint64(arrivalNs % 1_000_000_000),
			RawModeS:             []byte{0x8d, 0xab, 0xc1, 0x23, 0x01},
		})
	}

	got, err := Solve("ABC123", obs)
	if err != nil {
		t.Fatalf("Solve returned error: %v", err)
	}

	if diff := absFloat(got.Lat - 50.2000); diff > 0.06 {
		t.Fatalf("latitude too far off: got %.6f diff %.6f", got.Lat, diff)
	}
	if diff := absFloat(got.Lon - (-5.2000)); diff > 0.06 {
		t.Fatalf("longitude too far off: got %.6f diff %.6f", got.Lon, diff)
	}
	if got.Alt < -500 || got.Alt > 15000 {
		t.Fatalf("altitude out of plausible airborne range: got %.2f", got.Alt)
	}
	if got.NumSensors != len(obs) {
		t.Fatalf("unexpected sensor count: got %d want %d", got.NumSensors, len(obs))
	}
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
