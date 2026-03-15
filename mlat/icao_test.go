package mlat

import "testing"

func TestExtractICAORejectsDF20AndDF21(t *testing.T) {
	tests := [][]byte{
		{20 << 3, 0xaa, 0xbb, 0xcc},
		{21 << 3, 0xaa, 0xbb, 0xcc},
	}

	for _, raw := range tests {
		if icao, ok := ExtractICAO(raw); ok {
			t.Fatalf("expected DF %d to be rejected, got ICAO %s", raw[0]>>3, icao)
		}
	}
}

func TestExtractICAOAcceptsDF17(t *testing.T) {
	raw := []byte{17 << 3, 0xaa, 0xbb, 0xcc}
	icao, ok := ExtractICAO(raw)
	if !ok {
		t.Fatal("expected DF17 ICAO extraction to succeed")
	}
	if icao != "AABBCC" {
		t.Fatalf("unexpected ICAO: %s", icao)
	}
}
