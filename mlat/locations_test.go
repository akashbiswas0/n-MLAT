package mlat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLocationOverridesRequiresFile(t *testing.T) {
	err := LoadLocationOverrides(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Fatal("expected error for missing location override file")
	}
}

func TestLoadLocationOverridesLoadsTrustedSellerGeometry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "location-override.json")
	data := `[
		{
			"public_key": "abcdef1234567890",
			"lat": 50.1,
			"lon": -5.2,
			"alt": 120.5,
			"name": "sensor-a"
		}
	]`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write override file: %v", err)
	}

	if err := LoadLocationOverrides(path); err != nil {
		t.Fatalf("LoadLocationOverrides returned error: %v", err)
	}

	override, ok := OverrideForKey("abcdef1234567890")
	if !ok {
		t.Fatal("expected override to be available")
	}
	if override.Name != "sensor-a" {
		t.Fatalf("unexpected override name: %q", override.Name)
	}
	if override.Lat != 50.1 || override.Lon != -5.2 || override.Alt != 120.5 {
		t.Fatalf("unexpected override contents: %+v", override)
	}
}
