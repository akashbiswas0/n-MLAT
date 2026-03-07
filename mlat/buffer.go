package mlat

import (
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

const (
	
	WindowSize = 2 * time.Second

	
	CorrelationWindow = 10 * time.Millisecond

	MinSensors = 4
)

type observationGroup struct {
	id         string
	key        string
	anchorTsNs int64
	obs        []Observation
}


type Buffer struct {
	mu        sync.Mutex
	groups    map[string]*observationGroup // groupID → observations for one transmitted frame
	index     map[string][]string          // correlation key → candidate groupIDs
	timers    map[string]*time.Timer       // groupID → flush timer
	nextGroup atomic.Uint64
	OnReady   func(icao string, obs []Observation) // called when group is ready to solve
}

func NewBuffer(onReady func(icao string, obs []Observation)) *Buffer {
	return &Buffer{
		groups:  make(map[string]*observationGroup),
		index:   make(map[string][]string),
		timers:  make(map[string]*time.Timer),
		OnReady: onReady,
	}
}

func (b *Buffer) Add(obs Observation) {
	b.mu.Lock()
	defer b.mu.Unlock()

	group := b.findOrCreateGroup(obs)
	group.add(obs)
	log.Printf("[buf] ICAO %s: frame %s now has %d sensor(s) (sensorID=%d)",
		obs.ICAO, group.id, len(group.obs), obs.SensorID)

	if t, ok := b.timers[group.id]; ok {
		t.Stop()
	}

	if len(group.obs) >= MinSensors {
		ready := append([]Observation(nil), group.obs...)
		b.removeGroupLocked(group.id)
		log.Printf("[buf] ICAO %s: firing MLAT for frame %s with %d sensors", obs.ICAO, group.id, len(ready))
		go b.OnReady(obs.ICAO, ready)
		return
	}

	groupID := group.id
	icao := obs.ICAO
	b.timers[groupID] = time.AfterFunc(WindowSize, func() {
		b.mu.Lock()
		group, ok := b.groups[groupID]
		if ok {
			b.removeGroupLocked(groupID)
		}
		b.mu.Unlock()

		if !ok {
			return
		}

		if len(group.obs) >= MinSensors {
			log.Printf("[buf] ICAO %s: firing MLAT on timer for frame %s with %d sensors", icao, groupID, len(group.obs))
			go b.OnReady(icao, append([]Observation(nil), group.obs...))
		} else {
			log.Printf("[buf] ICAO %s: discarded frame %s — only %d/%d sensors in window", icao, groupID, len(group.obs), MinSensors)
		}
	})
}

func (b *Buffer) findOrCreateGroup(obs Observation) *observationGroup {
	key := obs.CorrelationKey()
	ts := obs.TimestampNs()

	bestID := ""
	bestDelta := int64(math.MaxInt64)
	for _, id := range b.index[key] {
		group, ok := b.groups[id]
		if !ok {
			continue
		}
		delta := absInt64(ts - group.anchorTsNs)
		if delta <= CorrelationWindow.Nanoseconds() && delta < bestDelta {
			bestDelta = delta
			bestID = id
		}
	}

	if bestID != "" {
		return b.groups[bestID]
	}

	id := fmt.Sprintf("%s#%d", obs.ICAO, b.nextGroup.Add(1))
	group := &observationGroup{
		id:         id,
		key:        key,
		anchorTsNs: ts,
		obs:        []Observation{},
	}
	b.groups[id] = group
	b.index[key] = append(b.index[key], id)
	return group
}

func (g *observationGroup) add(obs Observation) {
	for i, existing := range g.obs {
		if existing.SensorID != obs.SensorID {
			continue
		}
		if absInt64(obs.TimestampNs()-g.anchorTsNs) < absInt64(existing.TimestampNs()-g.anchorTsNs) {
			g.obs[i] = obs
		}
		return
	}

	if obs.TimestampNs() < g.anchorTsNs {
		g.anchorTsNs = obs.TimestampNs()
	}
	g.obs = append(g.obs, obs)
}

func (b *Buffer) removeGroupLocked(id string) {
	group, ok := b.groups[id]
	if !ok {
		return
	}
	delete(b.groups, id)
	if t, ok := b.timers[id]; ok {
		t.Stop()
		delete(b.timers, id)
	}

	ids := b.index[group.key]
	filtered := ids[:0]
	for _, candidate := range ids {
		if candidate != id {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		delete(b.index, group.key)
		return
	}
	b.index[group.key] = filtered
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
