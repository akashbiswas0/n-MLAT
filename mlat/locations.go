package mlat

import (
	"encoding/json"
	"log"
	"os"
)

type LocationOverride struct {
	PublicKey string  `json:"public_key"`
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Alt       float64 `json:"alt"`
	Name      string  `json:"name"`
}

var overrideMap map[string]LocationOverride

func LoadLocationOverrides(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("⚠  No location overrides loaded: %v", err)
		overrideMap = make(map[string]LocationOverride)
		return
	}
	var overrides []LocationOverride
	if err := json.Unmarshal(data, &overrides); err != nil {
		log.Printf("⚠  Failed to parse location overrides: %v", err)
		overrideMap = make(map[string]LocationOverride)
		return
	}
	overrideMap = make(map[string]LocationOverride)
	for _, o := range overrides {
		overrideMap[o.PublicKey] = o
	}
	log.Printf("✅ Loaded %d location overrides", len(overrideMap))
}

func ApplyOverride(publicKey string, lat, lon, alt float64) (float64, float64, float64) {
	if o, ok := overrideMap[publicKey]; ok {
		return o.Lat, o.Lon, o.Alt
	}
	return lat, lon, alt
}

func NameForKey(publicKey string) string {
	if o, ok := overrideMap[publicKey]; ok {
		return o.Name
	}
	return publicKey[:8] + "..."
}
