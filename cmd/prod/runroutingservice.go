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
	"github.com/illmade-knight/go-routing-service/internal/platform/push"
	"github.com/illmade-knight/go-routing-service/internal/platform/websocket"
	"github.com/illmade-knight/go-routing-service/internal/realtime"
	"github.com/illmade-knight/go-routing-service/internal/test/fakes"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"
)

// main wires up all production dependencies and starts the service.
func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx := context.Background()

	cfg := loadConfig(logger)

	psClient, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Failed to create Pub/Sub client: %v", err)
	}
	defer func() {
		_ = psClient.Close()
	}()

	fsClient, err := firestore.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}
	defer func() {
		_ = fsClient.Close()
	}()

	presenceCache, err := setupPresenceCache(ctx, cfg, fsClient, logger)
	if err != nil {
		log.Fatalf("Failed to create presence cache: %v", err)
	}
	defer func() {
		_ = presenceCache.Close()
	}()

	var deliveryProducer *websocket.DeliveryProducer
	var deliveryConsumer messagepipeline.MessageConsumer
	var ingressConsumer messagepipeline.MessageConsumer
	var cleanupDelivery, cleanupIngress func() = func() {}, func() {}

	if cfg.RunMode == "local" {
		logger.Warn().Msg("RUN_MODE=local DETECTED. Using IN-MEMORY components.")
		deliveryProducer, deliveryConsumer, ingressConsumer = setupLocalBus(logger)
	} else {
		logger.Info().Msg("Using PRODUCTION GCP Pub/Sub.")
		deliveryProducer, deliveryConsumer, cleanupDelivery, err = setupProductionDeliveryBus(ctx, cfg, psClient, logger)
		if err != nil {
			log.Fatalf("Failed to set up production delivery bus: %v", err)
		}
		ingressConsumer, cleanupIngress, err = setupProductionIngress(ctx, cfg, psClient, logger)
		if err != nil {
			log.Fatalf("Failed to set up production ingress: %v", err)
		}
	}
	defer cleanupDelivery()
	defer cleanupIngress()

	apiService, connManager := assembleServices(cfg, psClient, fsClient, presenceCache, deliveryProducer, deliveryConsumer, ingressConsumer, logger)
	app.Run(ctx, logger, apiService, connManager)
}

// --- Configuration & Setup ---

func loadConfig(logger zerolog.Logger) *cmd.AppConfig {
	var cfg cmd.YamlConfig
	if err := yaml.Unmarshal(cmd.ConfigYAML, &cfg); err != nil {
		logger.Fatal().Err(err).Msg("Failed to parse embedded config.yaml")
	}

	appCfg := &cmd.AppConfig{
		ProjectID:                cfg.ProjectID,
		RunMode:                  cfg.RunMode,
		APIPort:                  cfg.APIPort,
		WebSocketPort:            cfg.WebSocketPort,
		Cors:                     cfg.Cors,
		PresenceCache:            cfg.PresenceCache,
		IngressTopicID:           cfg.IngressTopicID,
		IngressSubscriptionID:    cfg.IngressSubscriptionID,
		IngressTopicDLQID:        cfg.IngressTopicDLQID,
		DeliveryTopicID:          cfg.DeliveryTopicID,
		PushNotificationsTopicID: cfg.PushNotificationsTopicID,
		JWTSecret:                os.Getenv("JWT_SECRET"),
	}

	if appCfg.ProjectID == "" || appCfg.ProjectID == "<your-gcp-project-id>" {
		logger.Fatal().Msg("FATAL: project_id is not set in config.yaml.")
	}
	if appCfg.JWTSecret == "" {
		logger.Fatal().Msg("FATAL: JWT_SECRET environment variable is not set.")
	}
	if appCfg.RunMode != "local" && appCfg.IngressTopicDLQID == "" {
		logger.Fatal().Msg("FATAL: ingress_topic_dlq_id must be set in production mode.")
	}

	logger.Info().Str("run_mode", appCfg.RunMode).Str("project_id", appCfg.ProjectID).Msg("Configuration loaded.")
	return appCfg
}

func setupPresenceCache(ctx context.Context, cfg *cmd.AppConfig, fsClient *firestore.Client, logger zerolog.Logger) (cache.PresenceCache[urn.URN, routing.ConnectionInfo], error) {
	cacheType := cfg.PresenceCache.Type
	logger.Info().Str("type", cacheType).Msg("Initializing presence cache...")

	switch cacheType {
	case "redis":
		return cache.NewRedisPresenceCache[urn.URN, routing.ConnectionInfo](
			ctx,
			&cache.RedisConfig{Addr: cfg.PresenceCache.Redis.Addr, CacheTTL: 24 * time.Hour},
			logger,
		)
	case "firestore":
		return cache.NewFirestorePresenceCache[urn.URN, routing.ConnectionInfo](
			fsClient,
			cfg.PresenceCache.Firestore.CollectionName,
		)
	case "inmemory":
		return cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo](), nil
	default:
		return nil, fmt.Errorf("invalid presence_cache type specified in config.yaml: %s", cacheType)
	}
}

// setupLocalBus creates in-memory message buses for local development.
func setupLocalBus(logger zerolog.Logger) (*websocket.DeliveryProducer, messagepipeline.MessageConsumer, messagepipeline.MessageConsumer) {
	// Ingress bus
	ingestionProducerFake := fakes.NewProducer(logger)
	ingressConsumer := fakes.NewInMemoryConsumer(100, logger)
	go func() {
		for msgData := range ingestionProducerFake.Published() {
			ingressConsumer.Publish(messagepipeline.Message{MessageData: msgData})
		}
	}()

	// Delivery bus
	deliveryProducerFake := fakes.NewProducer(logger)
	deliveryConsumer := fakes.NewInMemoryConsumer(100, logger)
	go func() {
		for msgData := range deliveryProducerFake.Published() {
			deliveryConsumer.Publish(messagepipeline.Message{MessageData: msgData})
		}
	}()
	deliveryProducer := websocket.NewDeliveryProducer(deliveryProducerFake)

	return deliveryProducer, deliveryConsumer, ingressConsumer
}

// setupProductionIngress creates the main ingress subscription with a Dead-Letter Queue policy.
func setupProductionIngress(ctx context.Context, cfg *cmd.AppConfig, client *pubsub.Client, logger zerolog.Logger) (messagepipeline.MessageConsumer, func(), error) {
	subAdminClient := client.SubscriptionAdminClient
	subPath := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, cfg.IngressSubscriptionID)
	topicPath := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.IngressTopicID)
	dlqTopicPath := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.IngressTopicDLQID)

	// Check if the subscription already exists.
	_, err := subAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: subPath})
	if err != nil {
		// If it doesn't exist, create it with the DLQ policy.
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			logger.Info().Str("subscription", subPath).Msg("Ingress subscription not found, creating it with DLQ policy...")
			_, err = subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
				Name:  subPath,
				Topic: topicPath,
				DeadLetterPolicy: &pubsubpb.DeadLetterPolicy{
					DeadLetterTopic:     dlqTopicPath,
					MaxDeliveryAttempts: 5,
				},
				AckDeadlineSeconds: 10,
			})
			if err != nil {
				return nil, func() {}, fmt.Errorf("failed to create ingress subscription with DLQ: %w", err)
			}
		} else {
			return nil, func() {}, fmt.Errorf("failed to get ingress subscription: %w", err)
		}
	} else {
		logger.Info().Str("subscription", subPath).Msg("Ingress subscription already exists.")
	}

	consumer, err := messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(cfg.IngressSubscriptionID), client, logger,
	)
	if err != nil {
		return nil, func() {}, fmt.Errorf("failed to create ingress consumer: %w", err)
	}

	return consumer, func() {}, nil // No cleanup needed for persistent subscription
}

// setupProductionDeliveryBus creates the producer and consumer for the real-time Pub/Sub bus.
func setupProductionDeliveryBus(ctx context.Context, cfg *cmd.AppConfig, client *pubsub.Client, logger zerolog.Logger) (*websocket.DeliveryProducer, messagepipeline.MessageConsumer, func(), error) {
	dataflowProducer, err := messagepipeline.NewGooglePubsubProducer(
		messagepipeline.NewGooglePubsubProducerDefaults(cfg.DeliveryTopicID), client, logger,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create delivery bus producer: %w", err)
	}
	deliveryProducer := websocket.NewDeliveryProducer(dataflowProducer)

	topicPath := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.DeliveryTopicID)
	subID := fmt.Sprintf("delivery-sub-%s", uuid.NewString())
	subPath := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, subID)
	subAdminClient := client.SubscriptionAdminClient

	sub, err := subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               subPath,
		Topic:              topicPath,
		AckDeadlineSeconds: 10,
		ExpirationPolicy:   &pubsubpb.ExpirationPolicy{Ttl: &duration.Duration{Seconds: 3600}},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create ephemeral subscription: %w", err)
	}
	cleanupFunc := func() {
		logger.Info().Str("subscription", sub.Name).Msg("Deleting ephemeral delivery subscription...")
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

// --- Service Assembly ---

func assembleServices(
	cfg *cmd.AppConfig,
	psClient *pubsub.Client,
	fsClient *firestore.Client,
	presenceCache cache.PresenceCache[urn.URN, routing.ConnectionInfo],
	deliveryProducer *websocket.DeliveryProducer,
	deliveryConsumer messagepipeline.MessageConsumer,
	ingressConsumer messagepipeline.MessageConsumer,
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

	pushProducer, err := messagepipeline.NewGooglePubsubProducer(
		messagepipeline.NewGooglePubsubProducerDefaults(cfg.PushNotificationsTopicID), psClient, logger,
	)
	if err != nil {
		log.Fatalf("Failed to create push notification producer: %v", err)
	}
	pushNotifier, err := push.NewPubSubNotifier(pushProducer, logger)
	if err != nil {
		log.Fatalf("Failed to create push notifier: %v", err)
	}

	deps := &routing.Dependencies{
		PresenceCache:      presenceCache,
		DeliveryProducer:   deliveryProducer,
		DeviceTokenFetcher: tokenFetcher,
		PushNotifier:       pushNotifier,
		MessageStore:       messageStore,
	}

	ingressProducer := psub.NewProducer(psClient.Publisher(cfg.IngressTopicID))

	var corsRole middleware.CorsRole
	if cfg.Cors.Role == "admin" {
		corsRole = middleware.CorsRoleAdmin
	}

	apiService, err := routingservice.New(
		&routing.Config{
			HTTPListenAddr:     ":" + cfg.APIPort,
			NumPipelineWorkers: 5,
			JWTSecret:          cfg.JWTSecret,
			CorsConfig:         middleware.CorsConfig{AllowedOrigins: cfg.Cors.AllowedOrigins, Role: corsRole},
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
	return persistence.NewURNTokenFetcherAdapter(stringTokenFetcher), nil
}
