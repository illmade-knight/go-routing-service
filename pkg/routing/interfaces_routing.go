// REFACTOR: This new file consolidates all public domain models for the
// routing service into a single location for clarity and maintainability.
// It also introduces the urn.URN type for all entity identifiers.

// Package routing contains the public "contract" for the routing service,
// including its domain models, interfaces, and configuration.
package routing

import (
	"context"

	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
)

// IngestionProducer defines the interface for publishing a message into the pipeline.
type IngestionProducer interface {
	Publish(ctx context.Context, envelope *transport.SecureEnvelope) error
}

// DeliveryProducer defines the interface for publishing a message to a specific delivery topic.
type DeliveryProducer interface {
	Publish(ctx context.Context, topicID string, envelope *transport.SecureEnvelope) error
}

// PushNotifier defines the interface for sending push notifications.
type PushNotifier interface {
	Notify(ctx context.Context, tokens []DeviceToken, envelope *transport.SecureEnvelope) error
}

// MessageStore defines the interface for persisting and retrieving messages
// for offline users.
type MessageStore interface {
	StoreMessages(ctx context.Context, recipient urn.URN, envelopes []*transport.SecureEnvelope) error
	RetrieveMessages(ctx context.Context, recipient urn.URN) ([]*transport.SecureEnvelope, error)
	DeleteMessages(ctx context.Context, recipient urn.URN, messageIDs []string) error
}
