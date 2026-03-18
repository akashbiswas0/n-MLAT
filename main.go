package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math"
	"sync/atomic"
	"time"

	neuronsdk "github.com/NeuronInnovations/neuron-go-hedera-sdk"
	commonlib "github.com/NeuronInnovations/neuron-go-hedera-sdk/common-lib"
	hederasdk "github.com/hashgraph/hedera-sdk-go/v2"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	hcs "quickstart/hedera-main"
	"quickstart/mlat"
	"quickstart/server"
)

const (
	sensorIDSize             = 8
	sensorLatitudeSize       = 8
	sensorLongitudeSize      = 8
	sensorAltitudeSize       = 8
	secondsSinceMidnightSize = 8
	nanosecondsSize          = 8
	minFixedSize             = sensorIDSize + sensorLatitudeSize + sensorLongitudeSize +
		sensorAltitudeSize + secondsSinceMidnightSize + nanosecondsSize
)

func readExact(s network.Stream, buf []byte) error {
	var total int
	for total < len(buf) {
		n, err := s.Read(buf[total:])
		if err != nil {
			return err
		}
		if n == 0 {
			return io.EOF
		}
		total += n
	}
	return nil
}

func float64FromBytes(b []byte) float64 {
	return math.Float64frombits(binary.BigEndian.Uint64(b))
}
func int64FromBytes(b []byte) int64   { return int64(binary.BigEndian.Uint64(b)) }
func uint64FromBytes(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

type BroadcastMessage struct {
	ICAO           string                    `json:"icao"`
	Lat            float64                   `json:"lat"`
	Lon            float64                   `json:"lon"`
	Alt            float64                   `json:"alt"`
	Cost           float64                   `json:"cost"`
	NumSensors     int                       `json:"num_sensors"`
	GroundSpeedMPS float64                   `json:"ground_speed_mps"`
	TrackSamples   int                       `json:"track_samples"`
	RMSResidualM   float64                   `json:"rms_residual_m"`
	UncertaintyM   float64                   `json:"uncertainty_m"`
	GDOP           float64                   `json:"gdop"`
	QualityScore   float64                   `json:"quality_score"`
	QualityLabel   string                    `json:"quality_label"`
	TrustScore     float64                   `json:"trust_score"`
	TrustLabel     string                    `json:"trust_label"`
	Contributors   []mlat.SensorContribution `json:"contributors"`
}

var pktCount atomic.Int64

func main() {
	var NrnProtocol = protocol.ID("neuron/ADSB/0.0.2")

	if err := mlat.LoadLocationOverrides("location-override.json"); err != nil {
		log.Fatalf("location overrides are required for MLAT: %v", err)
	}

	go server.Start(":8080", "./static")

	publisher, err := hcs.NewPublisher()
	if err != nil {
		log.Printf("⚠  Hedera publisher disabled: %v", err)
		publisher = nil
	}

	calibrator := mlat.NewClockCalibrator()
	tracker := mlat.NewTracker()
	state := NewAppState()

	buf := mlat.NewBuffer(func(icao string, obs []mlat.Observation) {
		calibratedObs := calibrator.Apply(obs)
		result, err := mlat.Solve(icao, calibratedObs)
		if err != nil {
			log.Printf("MLAT solve failed for %s: %v", icao, err)
			return
		}
		trackedResult := tracker.Update(result)
		if trackedResult == nil {
			return
		}
		calibrator.Update(calibratedObs, trackedResult)
		trackedResult.Contributors = calibrator.DecorateContributors(trackedResult.Contributors)

		broadcast := BroadcastMessage{
			ICAO:           trackedResult.ICAO,
			Lat:            trackedResult.Lat,
			Lon:            trackedResult.Lon,
			Alt:            trackedResult.Alt,
			Cost:           trackedResult.Cost,
			NumSensors:     trackedResult.NumSensors,
			GroundSpeedMPS: trackedResult.GroundSpeedMPS,
			TrackSamples:   trackedResult.TrackSamples,
			RMSResidualM:   trackedResult.RMSResidualM,
			UncertaintyM:   trackedResult.UncertaintyM,
			GDOP:           trackedResult.GDOP,
			QualityScore:   trackedResult.QualityScore,
			QualityLabel:   trackedResult.QualityLabel,
			TrustScore:     trackedResult.TrustScore,
			TrustLabel:     trackedResult.TrustLabel,
			Contributors:   trackedResult.Contributors,
		}
		state.RecordFix(broadcast)

		log.Printf("✈  %s → Lat=%.4f Lon=%.4f Alt=%.0fm (sensors=%d track=%d speed=%.1fm/s uncertainty=%.0fm gdop=%.2f quality=%s trust=%s cost=%.2e)",
			trackedResult.ICAO, trackedResult.Lat, trackedResult.Lon, trackedResult.Alt,
			trackedResult.NumSensors, trackedResult.TrackSamples, trackedResult.GroundSpeedMPS,
			trackedResult.UncertaintyM, trackedResult.GDOP, trackedResult.QualityLabel, trackedResult.TrustLabel, trackedResult.Cost)

		server.Broadcast(WSMessage{
			Type: "fix",
			Data: broadcast,
		})

		if publisher != nil {
			publisher.Publish(hcs.AuditRecord{
				EventType:      "mlat_fix",
				Version:        "0.2",
				ICAO:           trackedResult.ICAO,
				Lat:            trackedResult.Lat,
				Lon:            trackedResult.Lon,
				AltM:           trackedResult.Alt,
				Cost:           trackedResult.Cost,
				NumSensors:     trackedResult.NumSensors,
				GroundSpeedMPS: trackedResult.GroundSpeedMPS,
				TrackSamples:   trackedResult.TrackSamples,
				RMSResidualM:   trackedResult.RMSResidualM,
				UncertaintyM:   trackedResult.UncertaintyM,
				GDOP:           trackedResult.GDOP,
				QualityScore:   trackedResult.QualityScore,
				QualityLabel:   trackedResult.QualityLabel,
				TrustScore:     trackedResult.TrustScore,
				TrustLabel:     trackedResult.TrustLabel,
				Contributors:   contributorAudit(trackedResult.Contributors),
				TimestampUTC:   time.Now().UTC().Format(time.RFC3339),
			})
		}
	})
	buf.OnObservation = state.RecordObservation

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			server.Broadcast(WSMessage{
				Type: "state",
				Data: state.Snapshot(),
			})
		}
	}()

	if publisher != nil {
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				snapshot := state.Snapshot()
				publisher.Publish(hcs.AuditRecord{
					EventType:        "network_summary",
					Version:          "0.2",
					ConnectedSellers: snapshot.ConnectedSellers,
					RawAircraft:      snapshot.RawAircraft,
					TrackedAircraft:  snapshot.Analytics.TrackedAircraft,
					TotalFixes:       snapshot.Analytics.TotalFixes,
					FixRatePerMin:    snapshot.Analytics.FixRatePerMin,
					AvgSensors:       snapshot.Analytics.AvgSensors,
					AvgUncertaintyM:  snapshot.Analytics.AvgUncertaintyM,
					AvgQualityScore:  snapshot.Analytics.AvgQualityScore,
					AvgQualityLabel:  snapshot.Analytics.QualityLabel,
					AvgTrustScore:    snapshot.Analytics.AvgTrustScore,
					AvgTrustLabel:    snapshot.Analytics.TrustLabel,
					AvgGDOP:          snapshot.Analytics.AvgGDOP,
					Sellers:          sellerAudit(snapshot.Sellers),
					TimestampUTC:     time.Now().UTC().Format(time.RFC3339),
				})
			}
		}()
	}

	neuronsdk.LaunchSDK(
		"0.1",
		NrnProtocol,
		nil,
		func(ctx context.Context, h host.Host, b *commonlib.NodeBuffers) {
			h.SetStreamHandler(NrnProtocol, func(streamHandler network.Stream) {
				defer streamHandler.Close()

				peerID := streamHandler.Conn().RemotePeer()
				if b != nil {
					if _, exists := b.GetBuffer(peerID); exists {
						b.SetStreamHandler(peerID, &streamHandler)
					} else {
						log.Printf("⚠  inbound stream from unregistered seller %s; skipping SDK buffer registration", peerID)
					}
				}

				pubKeyHex := mlat.PeerIDToPublicKey(peerID.String())
				override, ok := mlat.OverrideForKey(pubKeyHex)
				if !ok {
					log.Printf("❌ Rejecting seller without trusted location override: %s (%s)", mlat.NameForKey(pubKeyHex), peerID)
					return
				}
				sensorName := override.Name
				if sensorName == "" {
					sensorName = mlat.NameForKey(pubKeyHex)
				}
				state.SellerConnected(peerID.String(), sensorName)
				log.Printf("✅ Stream opened from seller: %s (%s)", sensorName, peerID)
				defer state.SellerDisconnected(peerID.String())

				for {
					if streamHandler.Conn().IsClosed() {
						log.Printf("Stream closed: %s", sensorName)
						break
					}

					streamHandler.SetReadDeadline(time.Now().Add(5 * time.Second))

					// Read 1-byte length prefix
					lengthBuf := make([]byte, 1)
					if err := readExact(streamHandler, lengthBuf); err != nil {
						if err != io.EOF {
							log.Printf("Read length error from %s: %v", sensorName, err)
						}
						break
					}

					totalSize := int(lengthBuf[0])
					if totalSize == 0 {
						continue
					}

					// Read packet body
					packet := make([]byte, totalSize)
					if err := readExact(streamHandler, packet); err != nil {
						if err != io.EOF {
							log.Printf("Read packet error from %s: %v", sensorName, err)
						}
						break
					}

					if len(packet) < minFixedSize {
						continue
					}

					offset := 0
					sensorID := int64FromBytes(packet[offset : offset+sensorIDSize])
					offset += sensorIDSize
					_ = float64FromBytes(packet[offset : offset+sensorLatitudeSize])
					offset += sensorLatitudeSize
					_ = float64FromBytes(packet[offset : offset+sensorLongitudeSize])
					offset += sensorLongitudeSize
					_ = float64FromBytes(packet[offset : offset+sensorAltitudeSize])
					offset += sensorAltitudeSize
					secsMidnight := uint64FromBytes(packet[offset : offset+secondsSinceMidnightSize])
					offset += secondsSinceMidnightSize
					nanos := uint64FromBytes(packet[offset : offset+nanosecondsSize])
					offset += nanosecondsSize
					rawModeS := packet[offset:]

					icao, ok := mlat.ExtractICAO(rawModeS)
					if !ok {
						continue
					}

					obs := mlat.Observation{
						ICAO:                 icao,
						SensorID:             sensorID,
						SensorName:           sensorName,
						SensorLat:            override.Lat,
						SensorLon:            override.Lon,
						SensorAlt:            override.Alt,
						SecondsSinceMidnight: secsMidnight,
						Nanoseconds:          nanos,
						RawModeS:             rawModeS,
					}
					state.NoteSellerSensor(peerID.String(), sensorName, sensorID)

					n := pktCount.Add(1)
					if n%100 == 0 {
						log.Printf("[diag] packets received: %d (last from %s, icao=%s)", n, sensorName, icao)
					}
					buf.Add(obs)
				}
			})
		},
		func(msg hederasdk.TopicMessage) {
			fmt.Println(msg)
		},
		func(ctx context.Context, h host.Host, b *commonlib.NodeBuffers) {},
		func(msg hederasdk.TopicMessage) {},
	)
}

func contributorAudit(contributors []mlat.SensorContribution) []hcs.ContributorAudit {
	out := make([]hcs.ContributorAudit, 0, len(contributors))
	for _, contributor := range contributors {
		out = append(out, hcs.ContributorAudit{
			SensorID:          contributor.SensorID,
			SensorName:        contributor.SensorName,
			ResidualM:         contributor.ResidualM,
			ClockAdjustmentNs: contributor.ClockAdjustmentNs,
			ClockJitterNs:     contributor.ClockJitterNs,
			ClockSamples:      contributor.ClockSamples,
			ClockHealth:       contributor.ClockHealth,
			SellerScore:       contributor.SellerScore,
			TrustLabel:        contributor.TrustLabel,
		})
	}
	return out
}

func sellerAudit(sellers []SellerSummary) []hcs.SellerAudit {
	out := make([]hcs.SellerAudit, 0, len(sellers))
	for _, seller := range sellers {
		out = append(out, hcs.SellerAudit{
			PeerID:     seller.PeerID,
			Name:       seller.Name,
			TrustScore: seller.TrustScore,
			TrustLabel: seller.TrustLabel,
			Samples:    seller.Samples,
		})
	}
	return out
}
