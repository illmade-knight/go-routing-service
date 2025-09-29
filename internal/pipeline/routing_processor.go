// Package pipeline contains the core message processing logic for the routing service.
package pipeline

import (
	"context"
	"fmt"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/rs/zerolog"
)

// New creates the main message handler (StreamProcessor) for the routing pipeline.
// It determines user presence and routes messages to the correct delivery channel:
// 1. Online Users: Publishes to the real-time delivery bus.
// 2. Offline Users (Mobile): Stores message and triggers a push notification.
// 3. Offline Users (Web): Publishes to the real-time delivery bus for in-app notification on next connect.
func New(deps *routing.Dependencies, cfg *routing.Config, logger zerolog.Logger) messagepipeline.StreamProcessor[transport.SecureEnvelope] {
	return func(ctx context.Context, msg messagepipeline.Message, envelope *transport.SecureEnvelope) error {
		recipientURN := envelope.RecipientID
		procLogger := logger.With().Str("recipient_id", recipientURN.String()).Str("message_id", envelope.MessageID).Logger()

		// 1. Check if the user is online via the presence cache.
		if _, err := deps.PresenceCache.Fetch(ctx, recipientURN); err == nil {
			procLogger.Info().Msg("User is online. Routing message to real-time delivery bus.")
			// ARCHITECTURE FIX: Publish to the single, shared delivery topic for any online instance to pick up.
			err := deps.DeliveryProducer.Publish(ctx, cfg.DeliveryTopicID, envelope)
			if err != nil {
				return fmt.Errorf("failed to publish to delivery bus for online user: %w", err)
			}
			return nil
		}

		// 2. User is offline. Fetch their device tokens.
		procLogger.Info().Msg("User is offline. Fetching device tokens.")
		tokens, err := deps.DeviceTokenFetcher.Fetch(ctx, recipientURN)
		if err != nil {
			procLogger.Error().Err(err).Msg("Failed to fetch device tokens for offline user. Storing message as fallback.")
			return storeMessage(ctx, deps.MessageStore, envelope, procLogger)
		}

		if len(tokens) == 0 {
			procLogger.Warn().Msg("No device tokens found for offline user. Storing message.")
			return storeMessage(ctx, deps.MessageStore, envelope, procLogger)
		}

		// 3. Separate tokens by platform for different dispatchers.
		mobileTokens := make([]routing.DeviceToken, 0)
		hasWebToken := false
		for _, token := range tokens {
			switch token.Platform {
			case "ios", "android":
				mobileTokens = append(mobileTokens, token)
			case "web":
				hasWebToken = true
			default:
				procLogger.Warn().Str("platform", token.Platform).Msg("Unknown device token platform.")
			}
		}

		var firstErr error

		// 4. FEATURE: Handle web notifications by publishing to the delivery bus.
		// The ConnectionManager will deliver this to the client when they next connect.
		if hasWebToken {
			procLogger.Info().Msg("Found 'web' token. Routing notification to real-time delivery bus.")
			err := deps.DeliveryProducer.Publish(ctx, cfg.DeliveryTopicID, envelope)
			if err != nil {
				procLogger.Error().Err(err).Msg("Failed to publish web notification to delivery bus.")
				firstErr = err
			}
		}

		// 5. Handle mobile notifications by publishing to the external push notification service.
		if len(mobileTokens) > 0 {
			procLogger.Info().Int("count", len(mobileTokens)).Msg("Routing notification to push notification service.")
			err := deps.PushNotifier.Notify(ctx, mobileTokens, envelope)
			if err != nil {
				procLogger.Error().Err(err).Msg("Push notifier failed.")
				if firstErr == nil {
					firstErr = err
				}
			}
		}

		// If all dispatching fails, the message has already been processed as much as possible for this attempt.
		// The message should be NACK'd so the DLQ policy can eventually take over. Returning an error achieves this.
		if firstErr != nil {
			procLogger.Error().Err(firstErr).Msg("One or more dispatch methods failed. The message will be NACK'd for retry.")
			return firstErr
		}

		// If we successfully dispatched notifications, we MUST also store the message for retrieval via the API.
		return storeMessage(ctx, deps.MessageStore, envelope, procLogger)
	}
}

// storeMessage is a helper to encapsulate the logic for storing a message offline.
func storeMessage(ctx context.Context, store routing.MessageStore, envelope *transport.SecureEnvelope, logger zerolog.Logger) error {
	logger.Info().Msg("Storing message for later retrieval.")
	err := store.StoreMessages(ctx, envelope.RecipientID, []*transport.SecureEnvelope{envelope})
	if err != nil {
		return fmt.Errorf("failed to store message in firestore: %w", err)
	}
	return nil
}
