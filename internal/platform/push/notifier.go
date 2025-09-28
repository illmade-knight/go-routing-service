// Package push contains the concrete implementation for the PushNotifier interface.
package push

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/encoding/protojson"
)

// PubSubNotifier is a production-ready implementation of routing.PushNotifier.
// It sends push notification requests to a dedicated Pub/Sub topic for the
// notification-service to consume.
type PubSubNotifier struct {
	producer *messagepipeline.GooglePubsubProducer
	logger   zerolog.Logger
}

// NewPubSubNotifier creates a new push notification publisher.
func NewPubSubNotifier(producer *messagepipeline.GooglePubsubProducer, logger zerolog.Logger) (*PubSubNotifier, error) {
	if producer == nil {
		return nil, fmt.Errorf("producer cannot be nil")
	}

	notifier := &PubSubNotifier{
		producer: producer,
		logger:   logger.With().Str("component", "PubSubNotifier").Logger(),
	}

	return notifier, nil
}

// Notify implements the routing.PushNotifier interface. It transforms the internal
// routing data into the public NotificationRequest contract and publishes it.
func (n *PubSubNotifier) Notify(ctx context.Context, tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	if len(tokens) == 0 {
		n.logger.Info().Str("recipient", envelope.RecipientID.String()).Msg("No device tokens found for user; skipping push notification.")
		return nil
	}

	// 1. Create the native Go representation of the notification request.
	notificationRequest := createNotificationRequest(tokens, envelope)

	// 2. Convert the native struct to its Protobuf representation.
	protoRequest := transport.NotificationRequestToProto(notificationRequest)

	// 3. Marshal the Protobuf message to JSON for the message payload.
	payloadBytes, err := protojson.Marshal(protoRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal notification request proto: %w", err)
	}

	// 4. Create the MessageData object required by the producer.
	messageData := messagepipeline.MessageData{
		ID:      uuid.NewString(), // Generate a new UUID for this Pub/Sub message.
		Payload: payloadBytes,
	}

	// 5. Publish the message. This is an asynchronous, fire-and-forget operation.
	_, err = n.producer.Publish(ctx, messageData)
	if err != nil {
		return fmt.Errorf("failed to publish push notification request: %w", err)
	}

	n.logger.Info().Str("recipient", envelope.RecipientID.String()).Msg("Push notification request published successfully.")
	return nil
}

// createNotificationRequest is a helper to build the public notification contract
// from the service's internal data structures.
func createNotificationRequest(tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) *transport.NotificationRequest {
	// Convert the routing.DeviceToken to the transport.DeviceToken.
	// In this case they are identical, but this ensures a clean separation.
	transportTokens := make([]transport.DeviceToken, len(tokens))
	for i, t := range tokens {
		transportTokens[i] = transport.DeviceToken{
			Token:    t.Token,
			Platform: t.Platform,
		}
	}

	return &transport.NotificationRequest{
		RecipientID: envelope.RecipientID,
		Tokens:      transportTokens,
		Content: transport.NotificationContent{
			Title: "New Message",
			Body:  "You have received a new secure message.",
			Sound: "default",
		},
		DataPayload: map[string]string{
			"message_id": envelope.MessageID,
			"sender_id":  envelope.SenderID.String(),
		},
	}
}
