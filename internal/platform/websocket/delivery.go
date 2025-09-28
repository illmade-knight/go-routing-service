// Package websocket provides a real-time message delivery implementation.
package websocket

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"google.golang.org/protobuf/encoding/protojson"
)

// DeliveryEventProducer defines the interface for publishing a message to the delivery bus.
// This allows for a mockable, generic producer.
type DeliveryEventProducer interface {
	Publish(ctx context.Context, data messagepipeline.MessageData) (string, error)
}

// DeliveryProducer implements the routing.DeliveryProducer interface. It now acts as a
// bridge between the message processing pipeline and the real-time delivery message bus,
// publishing messages for any interested WebSocket server to consume.
type DeliveryProducer struct {
	producer DeliveryEventProducer
}

// NewDeliveryProducer creates a new WebSocket delivery producer that publishes to the bus.
func NewDeliveryProducer(producer DeliveryEventProducer) *DeliveryProducer {
	adapter := &DeliveryProducer{
		producer: producer,
	}
	return adapter
}

// Publish serializes the envelope and publishes it to the delivery bus.
// The topicID parameter from the interface is now ignored, as the underlying
// producer is pre-configured with its specific topic.
func (p *DeliveryProducer) Publish(ctx context.Context, _ string, envelope *transport.SecureEnvelope) error {
	protoEnvelope := transport.ToProto(envelope)
	payloadBytes, err := protojson.Marshal(protoEnvelope)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope for delivery bus: %w", err)
	}

	// Create the MessageData payload expected by the generic go-dataflow producer.
	// We use the MessageID from the envelope as the unique ID for the data payload.
	deliveryMessage := messagepipeline.MessageData{
		ID:      envelope.MessageID,
		Payload: payloadBytes,
	}

	_, err = p.producer.Publish(ctx, deliveryMessage)
	if err != nil {
		return fmt.Errorf("failed to publish message to delivery bus: %w", err)
	}

	return nil
}
