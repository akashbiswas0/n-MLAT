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

	hcs "quickstart/hedera"
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
	ICAO       string  `json:"icao"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Alt        float64 `json:"alt"`
	Cost       float64 `json:"cost"`
	NumSensors int     `json:"num_sensors"`
}

// pktCount is a simple atomic counter for diagnostic logging
var pktCount atomic.Int64

func main() {
	var NrnProtocol = protocol.ID("neuron/ADSB/0.0.2")

	// Load true sensor locations from override file
	mlat.LoadLocationOverrides("location-override.json")

	// Start HTTP/WebSocket server
	go server.Start(":8080", "./static")

	// Hedera HCS publisher (optional)
	publisher, err := hcs.NewPublisher()
	if err != nil {
		log.Printf("⚠  Hedera publisher disabled: %v", err)
		publisher = nil
	}

	// MLAT buffer — fires when 4+ sensors heard the same aircraft
	buf := mlat.NewBuffer(func(icao string, obs []mlat.Observation) {
		result, err := mlat.Solve(icao, obs)
		if err != nil {
			log.Printf("MLAT solve failed for %s: %v", icao, err)
			return
		}

		log.Printf("✈  %s → Lat=%.4f Lon=%.4f Alt=%.0fm (sensors=%d cost=%.2e)",
			result.ICAO, result.Lat, result.Lon, result.Alt,
			result.NumSensors, result.Cost)

		server.Broadcast(BroadcastMessage{
			ICAO:       result.ICAO,
			Lat:        result.Lat,
			Lon:        result.Lon,
			Alt:        result.Alt,
			Cost:       result.Cost,
			NumSensors: result.NumSensors,
		})

		if publisher != nil {
			publisher.Publish(hcs.AuditRecord{
				ICAO:         result.ICAO,
				Lat:          result.Lat,
				Lon:          result.Lon,
				AltM:         result.Alt,
				Cost:         result.Cost,
				NumSensors:   result.NumSensors,
				TimestampUTC: time.Now().UTC().Format(time.RFC3339),
			})
		}
	})

	neuronsdk.LaunchSDK(
		"0.1",
		NrnProtocol,
		nil,
		func(ctx context.Context, h host.Host, b *commonlib.NodeBuffers) {
			h.SetStreamHandler(NrnProtocol, func(streamHandler network.Stream) {
				defer streamHandler.Close()

				peerID := streamHandler.Conn().RemotePeer()
				b.SetStreamHandler(peerID, &streamHandler)

				pubKeyHex := mlat.PeerIDToPublicKey(peerID.String())
				sensorName := mlat.NameForKey(pubKeyHex)
				log.Printf("✅ Stream opened from seller: %s (%s)", sensorName, peerID)

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
					reportedLat := float64FromBytes(packet[offset : offset+sensorLatitudeSize])
					offset += sensorLatitudeSize
					reportedLon := float64FromBytes(packet[offset : offset+sensorLongitudeSize])
					offset += sensorLongitudeSize
					reportedAlt := float64FromBytes(packet[offset : offset+sensorAltitudeSize])
					offset += sensorAltitudeSize
					secsMidnight := uint64FromBytes(packet[offset : offset+secondsSinceMidnightSize])
					offset += secondsSinceMidnightSize
					nanos := uint64FromBytes(packet[offset : offset+nanosecondsSize])
					offset += nanosecondsSize
					rawModeS := packet[offset:]

					// *** Apply location override using public key ***
					// This replaces the seller's self-reported (intentionally wrong)
					// position with the true position from location-override.json
					trueLat, trueLon, trueAlt := mlat.ApplyOverride(
						pubKeyHex,
						reportedLat, reportedLon, reportedAlt,
					)

					icao, ok := mlat.ExtractICAO(rawModeS)
					if !ok {
						continue
					}

					obs := mlat.Observation{
						ICAO:                 icao,
						SensorID:             sensorID,
						SensorLat:            trueLat,
						SensorLon:            trueLon,
						SensorAlt:            trueAlt,
						SecondsSinceMidnight: secsMidnight,
						Nanoseconds:          nanos,
						RawModeS:             rawModeS,
					}

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
