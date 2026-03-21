package mlat

import (
	"encoding/json"
	"fmt"
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

func LoadLocationOverrides(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		overrideMap = make(map[string]LocationOverride)
		return fmt.Errorf("read location overrides: %w", err)
	}
	var overrides []LocationOverride
	if err := json.Unmarshal(data, &overrides); err != nil {
		overrideMap = make(map[string]LocationOverride)
		return fmt.Errorf("parse location overrides: %w", err)
	}
	overrideMap = make(map[string]LocationOverride)
	for _, o := range overrides {
		if o.PublicKey == "" {
			return fmt.Errorf("location override missing public_key")
		}
		overrideMap[o.PublicKey] = o
	}
	if len(overrideMap) == 0 {
		return fmt.Errorf("no location overrides loaded")
	}
	log.Printf("✅ Loaded %d location overrides", len(overrideMap))
	return nil
}

func OverrideForKey(publicKey string) (LocationOverride, bool) {
	o, ok := overrideMap[publicKey]
	return o, ok
}

func NameForKey(publicKey string) string {
	if o, ok := overrideMap[publicKey]; ok {
		return o.Name
	}
	if len(publicKey) < 8 {
		return publicKey
	}
	return publicKey[:8] + "..."
}
