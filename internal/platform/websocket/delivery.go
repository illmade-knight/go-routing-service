// Package websocket provides a real-time message delivery implementation.
package websocket

import (
	"context"
	"fmt"

	"github.com/gorilla/websocket"
	"github.com/illmade-knight/go-routing-service/internal/realtime"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/encoding/protojson"
)

// DeliveryProducer implements the routing.DeliveryProducer interface using WebSockets.
// It acts as a bridge between the message processing pipeline and the real-time
// connection manager.
type DeliveryProducer struct {
	connManager *realtime.ConnectionManager
	logger      zerolog.Logger
}

// NewDeliveryProducer creates a new WebSocket delivery producer.
func NewDeliveryProducer(connManager *realtime.ConnectionManager, logger zerolog.Logger) *DeliveryProducer {
	return &DeliveryProducer{
		connManager: connManager,
		logger:      logger,
	}
}

// SetConnectionManager allows for setting the ConnectionManager after initialization,
// which is necessary to resolve a circular dependency in the test setup.
func (p *DeliveryProducer) SetConnectionManager(connManager *realtime.ConnectionManager) {
	p.connManager = connManager
}

// Publish finds the recipient's WebSocket connection and delivers the message.
func (p *DeliveryProducer) Publish(_ context.Context, _ string, envelope *transport.SecureEnvelope) error {
	recipientURN := envelope.RecipientID
	conn, ok := p.connManager.Get(recipientURN)
	if !ok {
		p.logger.Warn().Str("recipient", recipientURN.String()).Msg("Attempted to deliver to user, but no active WebSocket connection was found.")
		return nil
	}

	// CORRECTED: Convert the native Go envelope to its Protobuf representation first.
	protoEnvelope := transport.ToProto(envelope)
	// CORRECTED: Serialize using protojson to match the format the client expects.
	payload, err := protojson.Marshal(protoEnvelope)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope for websocket delivery: %w", err)
	}

	if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		p.logger.Error().Err(err).Str("recipient", recipientURN.String()).Msg("Failed to write message to WebSocket. Removing connection.")
		p.connManager.Remove(recipientURN)
		return err
	}

	p.logger.Info().Str("recipient", recipientURN.String()).Msg("Successfully delivered message via WebSocket.")
	return nil
}
