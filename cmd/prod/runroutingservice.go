// The runroutingservice command is the main entrypoint for the routing service.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/golang/protobuf/ptypes/duration"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/cmd"
	"github.com/illmade-knight/go-routing-service/internal/app"
	"github.com/illmade-knight/go-routing-service/internal/platform/persistence"
	psub "github.com/illmade-knight/go-routing-service/internal/platform/pubsub"
	"github.com/illmade-knight/go-routing-service/internal/platform/websocket"
	"github.com/illmade-knight/go-routing-service/internal/realtime"
	"github.com/illmade-knight/go-routing-service/internal/test/fakes"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"gopkg.in/yaml.v3"
)

// loadConfig loads configuration from the embedded YAML file and environment variables.
func loadConfig(logger zerolog.Logger) *cmd.AppConfig {
	var cfg cmd.YamlConfig
	err := yaml.Unmarshal(cmd.ConfigYAML, &cfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to parse embedded config.yaml")
	}

	jwtSecret := os.Getenv("JWT_SECRET")

	finalConfig := &cmd.AppConfig{
		ProjectID:             cfg.ProjectID,
		RunMode:               cfg.RunMode,
		APIPort:               cfg.APIPort,
		WebSocketPort:         cfg.WebSocketPort,
		Cors:                  cfg.Cors,
		PresenceCache:         cfg.PresenceCache,
		IngressTopicID:        cfg.IngressTopicID,
		IngressSubscriptionID: cfg.IngressSubscriptionID,
		DeliveryTopicID:       cfg.DeliveryTopicID,
		JWTSecret:             jwtSecret,
	}

	if finalConfig.ProjectID == "" || finalConfig.ProjectID == "<your-gcp-project-id>" {
		logger.Fatal().Msg("FATAL: project_id is not set in config.yaml.")
	}
	if finalConfig.JWTSecret == "" {
		logger.Fatal().Msg("FATAL: JWT_SECRET environment variable is not set.")
	}

	logger.Info().Str("run_mode", finalConfig.RunMode).Str("project_id", finalConfig.ProjectID).Msg("Configuration loaded.")
	return finalConfig
}

// main wires up all production dependencies and starts the service.
func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx := context.Background()

	cfg := loadConfig(logger)

	psClient, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Failed to create Pub/Sub client: %v", err)
	}
	defer psClient.Close()

	fsClient, err := firestore.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}
	defer fsClient.Close()

	// REFACTOR: Instantiate the PresenceCache based on the type from config.yaml.
	var presenceCache cache.PresenceCache[urn.URN, routing.ConnectionInfo]
	cacheType := cfg.PresenceCache.Type
	logger.Info().Str("type", cacheType).Msg("Initializing presence cache...")

	switch cacheType {
	case "redis":
		presenceCache, err = cache.NewRedisPresenceCache[urn.URN, routing.ConnectionInfo](
			ctx,
			&cache.RedisConfig{Addr: cfg.PresenceCache.Redis.Addr, CacheTTL: 24 * time.Hour},
			logger,
		)
	case "firestore":
		presenceCache, err = cache.NewFirestorePresenceCache[urn.URN, routing.ConnectionInfo](
			fsClient,
			cfg.PresenceCache.Firestore.CollectionName,
		)
	case "inmemory":
		presenceCache = cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo]()
	default:
		log.Fatalf("Invalid presence_cache type specified in config.yaml: %s", cacheType)
	}

	if err != nil {
		log.Fatalf("Failed to create presence cache: %v", err)
	}
	defer presenceCache.Close()

	var deliveryProducer *websocket.DeliveryProducer
	var deliveryConsumer messagepipeline.MessageConsumer
	var cleanup func() = func() {}

	if cfg.RunMode == "local" {
		logger.Warn().Msg("RUN_MODE=local DETECTED. Using IN-MEMORY delivery bus.")
		deliveryProducer, deliveryConsumer = setupLocalDeliveryBus(logger)
	} else {
		logger.Info().Msg("Using PRODUCTION GCP Pub/Sub delivery bus.")
		deliveryProducer, deliveryConsumer, cleanup, err = setupProductionDeliveryBus(ctx, cfg, psClient, logger)
		if err != nil {
			log.Fatalf("Failed to set up production delivery bus: %v", err)
		}
	}
	defer cleanup()

	apiService, connManager := assembleServices(cfg, psClient, fsClient, presenceCache, deliveryProducer, deliveryConsumer, logger)
	app.Run(ctx, logger, apiService, connManager)
}

// setupLocalDeliveryBus creates a simple, in-memory message bus for local development.
func setupLocalDeliveryBus(logger zerolog.Logger) (*websocket.DeliveryProducer, messagepipeline.MessageConsumer) {
	fakeEventProducer := fakes.NewProducer(logger)
	deliveryConsumer := fakes.NewInMemoryConsumer(100, logger)

	// This goroutine directly pipes messages from the producer to the consumer,
	// simulating the bus without any external services.
	go func() {
		for msgData := range fakeEventProducer.Published() {
			deliveryConsumer.Publish(messagepipeline.Message{MessageData: msgData})
		}
	}()

	deliveryProducer := websocket.NewDeliveryProducer(fakeEventProducer)
	return deliveryProducer, deliveryConsumer
}

// setupProductionDeliveryBus creates the producer and consumer for the real-time Pub/Sub bus.
func setupProductionDeliveryBus(
	ctx context.Context,
	cfg *cmd.AppConfig,
	client *pubsub.Client,
	logger zerolog.Logger,
) (*websocket.DeliveryProducer, messagepipeline.MessageConsumer, func(), error) {
	dataflowProducer, err := messagepipeline.NewGooglePubsubProducer(
		messagepipeline.NewGooglePubsubProducerDefaults(cfg.DeliveryTopicID), client, logger,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create delivery bus producer: %w", err)
	}
	deliveryProducer := websocket.NewDeliveryProducer(dataflowProducer)

	topicName := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.DeliveryTopicID)
	subID := fmt.Sprintf("delivery-sub-%s", uuid.NewString())
	subAdminClient := client.SubscriptionAdminClient
	sub, err := subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, subID),
		Topic:              topicName,
		AckDeadlineSeconds: 10,
		ExpirationPolicy:   &pubsubpb.ExpirationPolicy{Ttl: &duration.Duration{Seconds: 3600}},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create ephemeral subscription: %w", err)
	}
	cleanupFunc := func() {
		_ = subAdminClient.DeleteSubscription(context.Background(), &pubsubpb.DeleteSubscriptionRequest{Subscription: sub.Name})
	}

	deliveryConsumer, err := messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(subID), client, logger,
	)
	if err != nil {
		cleanupFunc()
		return nil, nil, nil, fmt.Errorf("failed to create delivery bus consumer: %w", err)
	}

	return deliveryProducer, deliveryConsumer, cleanupFunc, nil
}

// --- Helper Functions (assembleServices, newFirestoreTokenFetcher, loadConfig) ---
// Note: These functions remain unchanged from the previous correct version.
func assembleServices(
	cfg *cmd.AppConfig,
	psClient *pubsub.Client,
	fsClient *firestore.Client,
	presenceCache cache.PresenceCache[urn.URN, routing.ConnectionInfo],
	deliveryProducer *websocket.DeliveryProducer,
	deliveryConsumer messagepipeline.MessageConsumer,
	logger zerolog.Logger,
) (*routingservice.Wrapper, *realtime.ConnectionManager) {
	messageStore, err := persistence.NewFirestoreStore(fsClient, logger)
	if err != nil {
		log.Fatalf("Failed to create Firestore message store: %v", err)
	}

	tokenFetcher, err := newFirestoreTokenFetcher(context.Background(), cfg, fsClient, logger)
	if err != nil {
		log.Fatalf("Failed to create Firestore token fetcher: %v", err)
	}

	pushNotifier := &loggingPushNotifier{logger: logger}

	deps := &routing.Dependencies{
		PresenceCache:      presenceCache,
		DeliveryProducer:   deliveryProducer,
		DeviceTokenFetcher: tokenFetcher,
		PushNotifier:       pushNotifier,
		MessageStore:       messageStore,
	}

	ingressConsumer, err := messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(cfg.IngressSubscriptionID), psClient, logger,
	)
	if err != nil {
		log.Fatalf("Failed to create ingress consumer: %v", err)
	}
	ingressProducer := psub.NewProducer(psClient.Publisher(cfg.IngressTopicID))

	var corsRole middleware.CorsRole
	switch cfg.Cors.Role {
	case "admin":
		corsRole = middleware.CorsRoleAdmin
	default:
		corsRole = middleware.CorsRoleDefault
		logger.Warn().Str("role", cfg.Cors.Role).Msg("Unknown CORS role specified in config.yaml, defaulting to none.")
	}

	apiService, err := routingservice.New(
		&routing.Config{
			HTTPListenAddr:     ":" + cfg.APIPort,
			NumPipelineWorkers: 5,
			JWTSecret:          cfg.JWTSecret,
			Cors: middleware.CorsConfig{
				AllowedOrigins: cfg.Cors.AllowedOrigins,
				Role:           corsRole,
			},
		},
		deps,
		ingressConsumer,
		ingressProducer,
		logger,
	)
	if err != nil {
		log.Fatalf("Failed to create API service: %v", err)
	}

	connManager, err := realtime.NewConnectionManager(
		":"+cfg.WebSocketPort,
		cfg.JWTSecret,
		presenceCache,
		deliveryConsumer,
		logger,
	)
	if err != nil {
		log.Fatalf("Failed to create Connection Manager: %v", err)
	}

	return apiService, connManager
}

type loggingPushNotifier struct {
	logger zerolog.Logger
}

func (n *loggingPushNotifier) Notify(ctx context.Context, tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	n.logger.Info().
		Str("recipient", envelope.RecipientID.String()).
		Int("token_count", len(tokens)).
		Msg("PushNotifier: would send push notification.")
	return nil
}

func newFirestoreTokenFetcher(
	ctx context.Context,
	cfg *cmd.AppConfig,
	fsClient *firestore.Client,
	logger zerolog.Logger,
) (cache.Fetcher[urn.URN, []routing.DeviceToken], error) {
	stringDocFetcher, err := cache.NewFirestore[string, persistence.DeviceTokenDoc](
		ctx,
		&cache.FirestoreConfig{ProjectID: cfg.ProjectID, CollectionName: "device-tokens"},
		fsClient,
		logger,
	)
	if err != nil {
		return nil, err
	}

	stringTokenFetcher := &persistence.FirestoreTokenAdapter{DocFetcher: stringDocFetcher}
	urnAdapter := persistence.NewURNTokenFetcherAdapter(stringTokenFetcher)

	return urnAdapter, nil
}
