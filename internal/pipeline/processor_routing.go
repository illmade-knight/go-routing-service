package pipeline

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
)

// NewRoutingProcessor is a constructor that creates our main message handler.
// It accepts dependencies for caching, delivery, and offline storage, returning
// a configured messagepipeline.StreamProcessor.
func NewRoutingProcessor(
	// REFACTOR: The caches are now keyed by the type-safe urn.URN.
	presenceCache cache.Fetcher[urn.URN, routing.ConnectionInfo],
	deviceTokenFetcher cache.Fetcher[urn.URN, []routing.DeviceToken],
	deliveryProducer routing.DeliveryProducer,
	pushNotifier routing.PushNotifier,
	messageStore routing.MessageStore,
	logger zerolog.Logger,
) messagepipeline.StreamProcessor[transport.SecureEnvelope] {

	// The returned function is the StreamProcessor that will be executed by the pipeline.
	return func(ctx context.Context, original messagepipeline.Message, envelope *transport.SecureEnvelope) error {
		var err error

		// The envelope's RecipientID is now a urn.URN from the transport package.
		procLogger := logger.With().Str("recipientID", envelope.RecipientID.String()).Logger()

		// Strategy 1: Check if the user is online via the presence cache.
		// REFACTOR: Pass the urn.URN directly to the cache.
		connInfo, err := presenceCache.Fetch(ctx, envelope.RecipientID)
		if err == nil {
			// The user is online. Forward the envelope to the specific server
			// instance handling their connection.
			deliveryTopic := "delivery-" + connInfo.ServerInstanceID
			procLogger.Info().
				Str("deliveryTopic", deliveryTopic).
				Msg("User is online. Forwarding message.")

			err = deliveryProducer.Publish(ctx, deliveryTopic, envelope)
			if err != nil {
				return fmt.Errorf("failed to publish to delivery topic %s for user %s: %w", deliveryTopic, envelope.RecipientID.String(), err)
			}
			return nil // Success
		}

		// Strategy 2: User is offline. Persist the message and then attempt a push notification.
		procLogger.Info().Msg("User is offline. Storing message and attempting push notification.")

		// REFACTOR: Call the correct StoreMessages method from the updated interface.
		err = messageStore.StoreMessages(ctx, envelope.RecipientID, []*transport.SecureEnvelope{envelope})
		if err != nil {
			return fmt.Errorf("failed to store message for offline user %s: %w", envelope.RecipientID.String(), err)
		}

		// REFACTOR: Pass the urn.URN directly to the cache.
		deviceTokens, err := deviceTokenFetcher.Fetch(ctx, envelope.RecipientID)
		if err != nil {
			procLogger.Warn().
				Err(err).
				Msg("Could not retrieve device tokens for user. Message is stored.")
			return nil
		}

		err = pushNotifier.Notify(ctx, deviceTokens, envelope)
		if err != nil {
			// Not returning the error here, as the message has been successfully stored.
			// A failed push notification should not cause the entire message to be re-processed.
			procLogger.Error().
				Err(err).
				Msg("Failed to send push notification.")
		}

		return nil // Success
	}
}
