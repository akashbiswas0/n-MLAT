package hedera

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	hederasdk "github.com/hashgraph/hedera-sdk-go/v2"
)

// AuditRecord is what we write to Hedera HCS for each MLAT fix
type AuditRecord struct {
	ICAO         string  `json:"icao"`
	Lat          float64 `json:"lat"`
	Lon          float64 `json:"lon"`
	AltM         float64 `json:"alt_m"`
	Cost         float64 `json:"cost"`
	NumSensors   int     `json:"num_sensors"`
	TimestampUTC string  `json:"timestamp_utc"`
}

// Publisher writes MLAT results to a Hedera HCS topic
type Publisher struct {
	client  *hederasdk.Client
	topicID hederasdk.TopicID
}

// NewPublisher creates a publisher using credentials from environment variables
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
		// Create a new topic if none configured
		topicID, err = createTopic(client)
		if err != nil {
			return nil, fmt.Errorf("failed to create HCS topic: %w", err)
		}
		log.Printf("Created new HCS topic: %s — add this to your .buyer-env as mlat_topic_id", topicID)
	}

	return &Publisher{client: client, topicID: topicID}, nil
}

// Publish writes a single MLAT fix to Hedera HCS
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

	log.Printf("HCS ✓ published fix for %s → %.4f, %.4f @ %.0fm",
		record.ICAO, record.Lat, record.Lon, record.AltM)
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
