// REFACTOR: This file is updated to implement the "Dual Server" architecture
// while preserving the original, working dependency injection and configuration logic.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/internal/platform/persistence"
	psadapter "github.com/illmade-knight/go-routing-service/internal/platform/pubsub"
	"github.com/illmade-knight/go-routing-service/internal/platform/websocket"
	"github.com/illmade-knight/go-routing-service/internal/realtime"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// --- 1. Configuration (PRESERVED) ---
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		logger.Fatal().Msg("JWT_SECRET environment variable must be set.")
	}
	projectID := os.Getenv("GCP_PROJECT_ID")
	if projectID == "" {
		logger.Fatal().Msg("GCP_PROJECT_ID environment variable must be set.")
	}

	cfg := &routing.Config{
		ProjectID:             projectID,
		HTTPListenAddr:        ":8082",
		WebSocketListenAddr:   ":8083",
		IngressSubscriptionID: "ingress-sub",
		IngressTopicID:        "ingress-topic",
		NumPipelineWorkers:    10,
		JWTSecret:             jwtSecret,
		Cors: middleware.CorsConfig{
			AllowedOrigins: []string{"http://localhost:4200"}, // Example, should be configurable
			Role:           middleware.CorsRoleAdmin,
		},
	}

	// --- 2. Dependency Injection (PRESERVED & EXTENDED) ---
	psClient, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create pubsub client")
	}
	defer psClient.Close()

	fsClient, err := firestore.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create firestore client")
	}
	defer fsClient.Close()

	// Pub/Sub resource creation for development (PRESERVED)
	createPubsubResources(ctx, psClient, cfg.ProjectID, cfg.IngressTopicID, cfg.IngressSubscriptionID, logger)

	// Concrete adapters (PRESERVED)
	consumerConfig := messagepipeline.NewGooglePubsubConsumerDefaults(cfg.IngressSubscriptionID)
	consumer, err := messagepipeline.NewGooglePubsubConsumer(consumerConfig, psClient, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create pubsub consumer")
	}

	ingestionPublisher := psClient.Publisher(cfg.IngressTopicID)
	ingestionProducer := psadapter.NewProducer(ingestionPublisher)

	messageStore, err := persistence.NewFirestoreStore(fsClient, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create firestore message store")
	}

	// ADDED: Create the concrete WebSocket DeliveryProducer
	deliveryProducer := websocket.NewDeliveryProducer(nil, logger)

	deps := &routing.Dependencies{
		PresenceCache:      cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo](),
		DeviceTokenFetcher: &mockDeviceTokenFetcher{},
		DeliveryProducer:   deliveryProducer, // Inject the new producer
		PushNotifier:       &mockPushNotifier{},
		MessageStore:       messageStore,
	}

	// --- 3. Service Initialization (CHANGED for Dual Server) ---

	// A. The STATELESS API Service
	apiService, err := routingservice.New(cfg, deps, consumer, ingestionProducer, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create API service")
	}

	// B. The STATEFUL WebSocket Service
	connManager := realtime.NewConnectionManager(
		cfg.WebSocketListenAddr,
		cfg.JWTSecret,
		deps.PresenceCache,
		deliveryProducer,
		logger,
	)
	deliveryProducer.SetConnectionManager(connManager) // Complete wiring

	// --- 4. Concurrent Start (CHANGED for Dual Server) ---
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		logger.Info().Str("address", cfg.HTTPListenAddr).Msg("Starting API service...")
		if err := apiService.Start(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error().Err(err).Msg("API service failed")
			stop() // Signal other services to stop
		}
	}()

	go func() {
		defer wg.Done()
		logger.Info().Str("address", cfg.WebSocketListenAddr).Msg("Starting WebSocket service...")
		if err := connManager.Start(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error().Err(err).Msg("WebSocket service failed")
			stop() // Signal other services to stop
		}
	}()

	// --- 5. Graceful Shutdown (CHANGED for Dual Server) ---
	<-ctx.Done()
	logger.Info().Msg("Shutdown signal received...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Shutdown servers in parallel
	var shutdownWg sync.WaitGroup
	shutdownWg.Add(2)

	go func() {
		defer shutdownWg.Done()
		if err := apiService.Shutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("API service shutdown failed")
		}
	}()

	go func() {
		defer shutdownWg.Done()
		if err := connManager.Shutdown(shutdownCtx); err != nil {
			logger.Error().Err(err).Msg("Connection manager shutdown failed")
		}
	}()

	shutdownWg.Wait()
	wg.Wait()
	logger.Info().Msg("All services stopped gracefully.")
}

// --- Helper Functions and Mocks (PRESERVED) ---

func createPubsubResources(ctx context.Context, psClient *pubsub.Client, projectID, topicID, subID string, logger zerolog.Logger) {
	topicAdminClient := psClient.TopicAdminClient
	subAdminClient := psClient.SubscriptionAdminClient
	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	if _, err := topicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName}); err != nil {
		logger.Warn().Err(err).Msg("Failed to create topic, may already exist")
	}
	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	if _, err := subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subName, Topic: topicName}); err != nil {
		logger.Warn().Err(err).Msg("Failed to create subscription, may already exist")
	}
}

type mockDeviceTokenFetcher struct{}

func (m *mockDeviceTokenFetcher) Fetch(ctx context.Context, key urn.URN) ([]routing.DeviceToken, error) {
	return nil, errors.New("not implemented")
}
func (m *mockDeviceTokenFetcher) Close() error { return nil }

type mockPushNotifier struct{}

func (m *mockPushNotifier) Notify(_ context.Context, tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	log.Info().Msgf("MOCK: Pushing to %d devices for %s", len(tokens), envelope.RecipientID.String())
	return nil
}
