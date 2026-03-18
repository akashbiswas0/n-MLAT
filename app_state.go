package main

import (
	"math"
	"sort"
	"sync"
	"time"

	"quickstart/mlat"
)

const (
	waitingRetention = 45 * time.Second
	activeRetention  = 10 * time.Minute
	fixRateWindow    = 1 * time.Minute
)

type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type WaitingAircraft struct {
	ICAO         string  `json:"icao"`
	Sensors      int     `json:"sensors"`
	Frames       int     `json:"frames"`
	LastSeenUTC  string  `json:"last_seen_utc"`
	LastSeenUnix int64   `json:"last_seen_unix"`
	ApproxLat    float64 `json:"approx_lat"`
	ApproxLon    float64 `json:"approx_lon"`
}

type LiveState struct {
	ConnectedSellers int               `json:"connected_sellers"`
	RawAircraft      int               `json:"raw_aircraft"`
	Active           []TrackedAircraft `json:"active"`
	Sellers          []SellerSummary   `json:"sellers"`
	Analytics        AnalyticsSummary  `json:"analytics"`
	Waiting          []WaitingAircraft `json:"waiting"`
}

type sellerState struct {
	name        string
	connectedAt time.Time
	lastSeen    time.Time
	trustScore  float64
	trustLabel  string
	samples     int
	sensorIDs   map[int64]struct{}
}

type waitingState struct {
	icao      string
	sensors   int
	frames    int
	lastSeen  time.Time
	approxLat float64
	approxLon float64
}

type trackedState struct {
	fix      BroadcastMessage
	fixes    int
	lastSeen time.Time
}

type TrackedAircraft struct {
	BroadcastMessage
	Fixes        int    `json:"fixes"`
	LastSeenUTC  string `json:"last_seen_utc"`
	LastSeenUnix int64  `json:"last_seen_unix"`
}

type SellerSummary struct {
	PeerID         string  `json:"peer_id"`
	Name           string  `json:"name"`
	ConnectedSince string  `json:"connected_since"`
	TrustScore     float64 `json:"trust_score"`
	TrustLabel     string  `json:"trust_label"`
	Samples        int     `json:"samples"`
}

type AnalyticsSummary struct {
	TotalFixes      int     `json:"total_fixes"`
	FixRatePerMin   float64 `json:"fix_rate_per_min"`
	AvgSensors      float64 `json:"avg_sensors"`
	AvgUncertaintyM float64 `json:"avg_uncertainty_m"`
	AvgQualityScore float64 `json:"avg_quality_score"`
	AvgGDOP         float64 `json:"avg_gdop"`
	QualityLabel    string  `json:"quality_label"`
	AvgTrustScore   float64 `json:"avg_trust_score"`
	TrustLabel      string  `json:"trust_label"`
	TrackedAircraft int     `json:"tracked_aircraft"`
}

type AppState struct {
	mu            sync.Mutex
	sellers       map[string]sellerState
	waiting       map[string]*waitingState
	active        map[string]*trackedState
	fixWindow     []time.Time
	totalFixes    int
	totalSensors  float64
	totalQuality  float64
	totalGDOP     float64
	totalUncM     float64
	totalTrust    float64
	metricSamples int
}

func NewAppState() *AppState {
	return &AppState{
		sellers: make(map[string]sellerState),
		waiting: make(map[string]*waitingState),
		active:  make(map[string]*trackedState),
	}
}

func (s *AppState) SellerConnected(peerID, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	existing, ok := s.sellers[peerID]
	if ok {
		existing.name = name
		existing.connectedAt = now
		existing.lastSeen = now
		if existing.trustScore == 0 {
			existing.trustScore = 70
		}
		if existing.trustLabel == "" {
			existing.trustLabel = "WATCH"
		}
		if existing.sensorIDs == nil {
			existing.sensorIDs = make(map[int64]struct{})
		}
		s.sellers[peerID] = existing
		return
	}
	s.sellers[peerID] = sellerState{
		name:        name,
		connectedAt: now,
		lastSeen:    now,
		trustScore:  70,
		trustLabel:  "WATCH",
		sensorIDs:   make(map[int64]struct{}),
	}
}

func (s *AppState) SellerDisconnected(peerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sellers, peerID)
}

func (s *AppState) NoteSellerSensor(peerID, name string, sensorID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	seller, ok := s.sellers[peerID]
	if !ok {
		seller = sellerState{
			name:        name,
			connectedAt: now,
			lastSeen:    now,
			trustScore:  70,
			trustLabel:  "WATCH",
			sensorIDs:   make(map[int64]struct{}),
		}
	}
	if seller.sensorIDs == nil {
		seller.sensorIDs = make(map[int64]struct{})
	}
	seller.name = name
	seller.lastSeen = now
	seller.sensorIDs[sensorID] = struct{}{}
	s.sellers[peerID] = seller
}

func (s *AppState) RecordObservation(obs mlat.Observation, sensors int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state, ok := s.waiting[obs.ICAO]
	if !ok {
		state = &waitingState{icao: obs.ICAO}
		s.waiting[obs.ICAO] = state
	}
	state.frames++
	if sensors > state.sensors {
		state.sensors = sensors
	}
	state.lastSeen = time.Now().UTC()
	state.approxLat = obs.SensorLat
	state.approxLon = obs.SensorLon
}

func (s *AppState) RecordFix(fix BroadcastMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	entry, ok := s.active[fix.ICAO]
	if !ok {
		entry = &trackedState{}
		s.active[fix.ICAO] = entry
	}
	entry.fix = fix
	entry.fixes++
	entry.lastSeen = now

	s.totalFixes++
	s.totalSensors += float64(fix.NumSensors)
	s.totalQuality += fix.QualityScore
	s.totalUncM += fix.UncertaintyM
	s.totalTrust += fix.TrustScore
	if !math.IsNaN(fix.GDOP) && !math.IsInf(fix.GDOP, 0) {
		s.totalGDOP += fix.GDOP
	}
	s.metricSamples++
	s.fixWindow = append(s.fixWindow, now)

	for _, contributor := range fix.Contributors {
		for peerID, seller := range s.sellers {
			if _, ok := seller.sensorIDs[contributor.SensorID]; !ok {
				continue
			}
			score := contributor.SellerScore
			if score <= 0 {
				score = 70
			}
			if seller.samples == 0 {
				seller.trustScore = score
			} else {
				seller.trustScore = seller.trustScore*0.7 + score*0.3
			}
			seller.trustLabel = contributor.TrustLabel
			if seller.trustLabel == "" {
				seller.trustLabel = trustLabelForScore(seller.trustScore)
			}
			seller.samples++
			seller.lastSeen = now
			s.sellers[peerID] = seller
			break
		}
	}

	delete(s.waiting, fix.ICAO)
}

func (s *AppState) Snapshot() LiveState {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	cutoff := now.Add(-waitingRetention)
	fixCutoff := now.Add(-fixRateWindow)

	trimmedFixWindow := s.fixWindow[:0]
	for _, ts := range s.fixWindow {
		if ts.After(fixCutoff) {
			trimmedFixWindow = append(trimmedFixWindow, ts)
		}
	}
	s.fixWindow = trimmedFixWindow

	waiting := make([]WaitingAircraft, 0, len(s.waiting))
	for icao, state := range s.waiting {
		if state.lastSeen.Before(cutoff) {
			delete(s.waiting, icao)
			continue
		}
		waiting = append(waiting, WaitingAircraft{
			ICAO:         state.icao,
			Sensors:      state.sensors,
			Frames:       state.frames,
			LastSeenUTC:  state.lastSeen.Format(time.RFC3339),
			LastSeenUnix: state.lastSeen.Unix(),
			ApproxLat:    state.approxLat,
			ApproxLon:    state.approxLon,
		})
	}

	sort.Slice(waiting, func(i, j int) bool {
		if waiting[i].Sensors != waiting[j].Sensors {
			return waiting[i].Sensors > waiting[j].Sensors
		}
		if waiting[i].Frames != waiting[j].Frames {
			return waiting[i].Frames > waiting[j].Frames
		}
		return waiting[i].ICAO < waiting[j].ICAO
	})
	if len(waiting) > 20 {
		waiting = waiting[:20]
	}

	active := make([]TrackedAircraft, 0, len(s.active))
	for icao, state := range s.active {
		if now.Sub(state.lastSeen) > activeRetention {
			delete(s.active, icao)
			continue
		}
		active = append(active, TrackedAircraft{
			BroadcastMessage: state.fix,
			Fixes:            state.fixes,
			LastSeenUTC:      state.lastSeen.Format(time.RFC3339),
			LastSeenUnix:     state.lastSeen.Unix(),
		})
		delete(s.waiting, icao)
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].TrackSamples != active[j].TrackSamples {
			return active[i].TrackSamples > active[j].TrackSamples
		}
		if active[i].NumSensors != active[j].NumSensors {
			return active[i].NumSensors > active[j].NumSensors
		}
		return active[i].ICAO < active[j].ICAO
	})

	sellers := make([]SellerSummary, 0, len(s.sellers))
	for peerID, seller := range s.sellers {
		sellers = append(sellers, SellerSummary{
			PeerID:         peerID,
			Name:           seller.name,
			ConnectedSince: seller.connectedAt.Format(time.RFC3339),
			TrustScore:     seller.trustScore,
			TrustLabel:     seller.trustLabel,
			Samples:        seller.samples,
		})
	}
	sort.Slice(sellers, func(i, j int) bool { return sellers[i].Name < sellers[j].Name })

	analytics := AnalyticsSummary{
		TotalFixes:      s.totalFixes,
		FixRatePerMin:   float64(len(s.fixWindow)),
		TrackedAircraft: len(active),
	}
	if s.metricSamples > 0 {
		analytics.AvgSensors = s.totalSensors / float64(s.metricSamples)
		analytics.AvgUncertaintyM = s.totalUncM / float64(s.metricSamples)
		analytics.AvgQualityScore = s.totalQuality / float64(s.metricSamples)
		analytics.AvgGDOP = s.totalGDOP / float64(s.metricSamples)
		analytics.QualityLabel = analyticsQualityLabel(analytics.AvgQualityScore)
		analytics.AvgTrustScore = s.totalTrust / float64(s.metricSamples)
		analytics.TrustLabel = trustLabelForScore(analytics.AvgTrustScore)
	}

	return LiveState{
		ConnectedSellers: len(s.sellers),
		RawAircraft:      len(waiting),
		Active:           active,
		Sellers:          sellers,
		Analytics:        analytics,
		Waiting:          waiting,
	}
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

func analyticsQualityLabel(score float64) string {
	switch {
	case score >= 80:
		return "HIGH"
	case score >= 55:
		return "MED"
	default:
		return "LOW"
	}
}
