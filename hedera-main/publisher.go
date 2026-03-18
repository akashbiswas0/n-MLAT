package hedera

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	hederasdk "github.com/hashgraph/hedera-sdk-go/v2"
)

type AuditRecord struct {
	EventType        string             `json:"event_type"`
	Version          string             `json:"version"`
	ICAO             string             `json:"icao,omitempty"`
	Lat              float64            `json:"lat,omitempty"`
	Lon              float64            `json:"lon,omitempty"`
	AltM             float64            `json:"alt_m,omitempty"`
	Cost             float64            `json:"cost,omitempty"`
	NumSensors       int                `json:"num_sensors,omitempty"`
	GroundSpeedMPS   float64            `json:"ground_speed_mps,omitempty"`
	TrackSamples     int                `json:"track_samples,omitempty"`
	RMSResidualM     float64            `json:"rms_residual_m,omitempty"`
	UncertaintyM     float64            `json:"uncertainty_m,omitempty"`
	GDOP             float64            `json:"gdop,omitempty"`
	QualityScore     float64            `json:"quality_score,omitempty"`
	QualityLabel     string             `json:"quality_label,omitempty"`
	TrustScore       float64            `json:"trust_score,omitempty"`
	TrustLabel       string             `json:"trust_label,omitempty"`
	ConnectedSellers int                `json:"connected_sellers,omitempty"`
	RawAircraft      int                `json:"raw_aircraft,omitempty"`
	TrackedAircraft  int                `json:"tracked_aircraft,omitempty"`
	TotalFixes       int                `json:"total_fixes,omitempty"`
	FixRatePerMin    float64            `json:"fix_rate_per_min,omitempty"`
	AvgSensors       float64            `json:"avg_sensors,omitempty"`
	AvgUncertaintyM  float64            `json:"avg_uncertainty_m,omitempty"`
	AvgQualityScore  float64            `json:"avg_quality_score,omitempty"`
	AvgQualityLabel  string             `json:"avg_quality_label,omitempty"`
	AvgTrustScore    float64            `json:"avg_trust_score,omitempty"`
	AvgTrustLabel    string             `json:"avg_trust_label,omitempty"`
	AvgGDOP          float64            `json:"avg_gdop,omitempty"`
	Contributors     []ContributorAudit `json:"contributors,omitempty"`
	Sellers          []SellerAudit      `json:"sellers,omitempty"`
	TimestampUTC     string             `json:"timestamp_utc"`
}

type ContributorAudit struct {
	SensorID          int64   `json:"sensor_id"`
	SensorName        string  `json:"sensor_name"`
	ResidualM         float64 `json:"residual_m"`
	ClockAdjustmentNs float64 `json:"clock_adjustment_ns"`
	ClockJitterNs     float64 `json:"clock_jitter_ns"`
	ClockSamples      int     `json:"clock_samples"`
	ClockHealth       string  `json:"clock_health"`
	SellerScore       float64 `json:"seller_score"`
	TrustLabel        string  `json:"trust_label"`
}

type SellerAudit struct {
	PeerID     string  `json:"peer_id"`
	Name       string  `json:"name"`
	TrustScore float64 `json:"trust_score"`
	TrustLabel string  `json:"trust_label"`
	Samples    int     `json:"samples"`
}

type Publisher struct {
	client  *hederasdk.Client
	topicID hederasdk.TopicID
}

func NewPublisher() (*Publisher, error) {
	accountID := os.Getenv("hedera_id")
	privateKey := os.Getenv("private_key")
	topicIDStr := os.Getenv("mlat_topic_id") // you'll create this topic once

	if accountID == "" || privateKey == "" {
		return nil, fmt.Errorf("hedera_id or private_key not set in environment")
	}

	client := hederasdk.ClientForTestnet()

	myAccountID, err := hederasdk.AccountIDFromString(accountID)
	if err != nil {
		return nil, fmt.Errorf("invalid hedera_id: %w", err)
	}

	myPrivateKey, err := hederasdk.PrivateKeyFromString(privateKey)
	if err != nil {
		return nil, fmt.Errorf("invalid private_key: %w", err)
	}

	client.SetOperator(myAccountID, myPrivateKey)

	var topicID hederasdk.TopicID
	if topicIDStr != "" {
		topicID, err = hederasdk.TopicIDFromString(topicIDStr)
		if err != nil {
			return nil, fmt.Errorf("invalid mlat_topic_id: %w", err)
		}
		log.Printf("Using existing HCS topic: %s", topicIDStr)
	} else {
		topicID, err = createTopic(client)
		if err != nil {
			return nil, fmt.Errorf("failed to create HCS topic: %w", err)
		}
		log.Printf("Created new HCS topic: %s — add this to your .buyer-env as mlat_topic_id", topicID)
	}

	return &Publisher{client: client, topicID: topicID}, nil
}

func (p *Publisher) Publish(record AuditRecord) {
	data, err := json.Marshal(record)
	if err != nil {
		log.Printf("HCS marshal error: %v", err)
		return
	}

	_, err = hederasdk.NewTopicMessageSubmitTransaction().
		SetTopicID(p.topicID).
		SetMessage(data).
		Execute(p.client)

	if err != nil {
		log.Printf("HCS publish error: %v", err)
		return
	}

	switch record.EventType {
	case "network_summary":
		log.Printf("HCS ✓ published network summary: sellers=%d tracked=%d total_fixes=%d avg_trust=%.0f",
			record.ConnectedSellers, record.TrackedAircraft, record.TotalFixes, record.AvgTrustScore)
	default:
		log.Printf("HCS ✓ published fix for %s → %.4f, %.4f @ %.0fm trust=%s quality=%s",
			record.ICAO, record.Lat, record.Lon, record.AltM, record.TrustLabel, record.QualityLabel)
	}
}

func createTopic(client *hederasdk.Client) (hederasdk.TopicID, error) {
	txResponse, err := hederasdk.NewTopicCreateTransaction().
		SetTopicMemo("4DSky MLAT audit log").
		Execute(client)
	if err != nil {
		return hederasdk.TopicID{}, err
	}
	receipt, err := txResponse.GetReceipt(client)
	if err != nil {
		return hederasdk.TopicID{}, err
	}
	return *receipt.TopicID, nil
}
