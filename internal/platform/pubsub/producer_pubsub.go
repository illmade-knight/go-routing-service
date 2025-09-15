// Package pubsub contains concrete adapters for interacting with Google Cloud Pub/Sub.
package pubsub

import (
	"context"
	"encoding/json"
	"fmt"

	"cloud.google.com/go/pubsub/v2"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
)

// pubsubTopicClient defines the interface for the underlying pubsub.Topic.
// This allows us to use a mock for testing.
type pubsubTopicClient interface {
	Publish(ctx context.Context, msg *pubsub.Message) *pubsub.PublishResult
}

// Producer implements the routing.IngestionProducer interface.
// It acts as an adapter, serializing a SecureEnvelope and publishing it
// to a Google Cloud Pub/Sub topic.
type Producer struct {
	topic pubsubTopicClient
}

// NewProducer is the constructor for the Pub/Sub producer.
// It takes a topic client that it will publish messages to.
func NewProducer(topic pubsubTopicClient) *Producer {
	producer := &Producer{
		topic: topic,
	}
	return producer
}

// Publish serializes the SecureEnvelope and sends it to the message bus.
// It conforms to the routing.IngestionProducer interface.
func (p *Producer) Publish(ctx context.Context, envelope *transport.SecureEnvelope) error {
	var err error

	// REFACTOR: The original file had a struct literal that was not a pointer.
	// While not a style guide violation, using a pointer for the returned
	// struct is more idiomatic for larger structs or when methods might
	// need to modify the struct's state. Changed to return *Producer.

	// Serialize the envelope to a JSON byte slice for the message payload.
	payloadBytes, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope for publishing: %w", err)
	}

	// Create the pubsub.Message directly.
	message := &pubsub.Message{
		Data: payloadBytes,
	}

	// Publish the message and wait for the result.
	result := p.topic.Publish(ctx, message)
	_, err = result.Get(ctx)
	if err != nil {
		return fmt.Errorf("producer failed to publish message: %w", err)
	}

	return nil
}
