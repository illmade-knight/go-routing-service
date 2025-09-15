// REFACTOR: This file is updated to correctly instantiate dependencies with the
// new urn.URN type, completing the URN migration. The mock implementations
// have also been updated to correctly handle the URN type in their logging.

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/platform/persistence"
	psadapter "github.com/illmade-knight/go-routing-service/internal/platform/pubsub"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
)

// main is the entry point for the routing service application.
// It initializes all components, starts the service, and handles graceful shutdown.
func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. Load configuration from environment variables.
	cfg := &routing.Config{
		ProjectID:             os.Getenv("GCP_PROJECT_ID"),
		HTTPListenAddr:        ":8082",
		IngressSubscriptionID: "ingress-sub",
		IngressTopicID:        "ingress-topic",
		NumPipelineWorkers:    10,
	}

	// 2. Initialize external clients.
	psClient, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Failed to create pubsub client: %v", err)
	}
	defer psClient.Close()

	fsClient, err := firestore.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Failed to create firestore client: %v", err)
	}
	defer fsClient.Close()

	// 2.a [Temporary] Ensure topic and subscription exist for development.
	topicAdminClient := psClient.TopicAdminClient
	subAdminClient := psClient.SubscriptionAdminClient
	topicID := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.IngressTopicID)
	_, err = topicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
		Name: topicID,
	})
	if err != nil {
		log.Printf("failed to create topic, may already exist: %v", err)
	}
	subID := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, cfg.IngressSubscriptionID)
	_, err = subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  subID,
		Topic: topicID,
	})
	if err != nil {
		log.Printf("failed to create subscription, may already exist: %v", err)
	}

	// 3. Instantiate CONCRETE adapters using the real clients.
	consumerConfig := messagepipeline.NewGooglePubsubConsumerDefaults(cfg.IngressSubscriptionID)
	consumer, err := messagepipeline.NewGooglePubsubConsumer(consumerConfig, psClient, logger)
	if err != nil {
		log.Fatalf("Failed to create pubsub consumer: %v", err)
	}

	ingestionPublisher := psClient.Publisher(cfg.IngressTopicID)
	ingestionProducer := psadapter.NewProducer(ingestionPublisher)

	messageStore, err := persistence.NewFirestoreStore(fsClient, logger)
	if err != nil {
		log.Fatalf("Failed to create firestore message store: %v", err)
	}

	// 4. Create other real dependencies.
	// REFACTOR: Instantiate caches with the correct urn.URN key type.
	deps := &routing.Dependencies{
		PresenceCache:      cache.NewInMemoryCache[urn.URN, routing.ConnectionInfo](nil),
		DeviceTokenFetcher: cache.NewInMemoryCache[urn.URN, []routing.DeviceToken](nil),
		DeliveryProducer:   &mockDeliveryProducer{}, // Placeholder
		PushNotifier:       &mockPushNotifier{},     // Placeholder
		MessageStore:       messageStore,
	}

	// 5. Create the service using the public wrapper, injecting all dependencies.
	service, err := routingservice.New(cfg, deps, consumer, ingestionProducer, logger)
	if err != nil {
		log.Fatalf("Failed to create service: %v", err)
	}

	// 6. Start the service and handle graceful shutdown.
	go func() {
		if err := service.Start(ctx); err != nil {
			logger.Error().Err(err).Msg("Service failed to start")
			stop()
		}
	}()

	logger.Info().Msg("Routing service started. Press Ctrl+C to shut down.")
	<-ctx.Done() // Wait for shutdown signal

	logger.Info().Msg("Shutdown signal received. Gracefully stopping service...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := service.Shutdown(shutdownCtx); err != nil {
		logger.Fatal().Err(err).Msg("Service shutdown failed.")
	}

	logger.Info().Msg("Service stopped gracefully.")
}

// mockDeliveryProducer and mockPushNotifier are placeholders for the main executable.
type mockDeliveryProducer struct{}

func (m *mockDeliveryProducer) Publish(ctx context.Context, topicID string, data *transport.SecureEnvelope) error {
	// REFACTOR: Use the .String() method to correctly log the URN.
	log.Printf("MOCK: Publishing to delivery topic %s for user %s", topicID, data.RecipientID.String())
	return nil
}

type mockPushNotifier struct{}

func (m *mockPushNotifier) Notify(_ context.Context, tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	// REFACTOR: Use the .String() method to correctly log the URN.
	log.Printf("MOCK: Sending push notification to %d devices for user %s", len(tokens), envelope.RecipientID.String())
	return nil
}
