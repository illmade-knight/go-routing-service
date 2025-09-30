package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
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
	"github.com/illmade-knight/go-routing-service/routingservice/config"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

func main() {
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()
	ctx := context.Background()

	cfg := loadConfig(logger)

	authMiddleware, err := newAuthMiddleware(cfg, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create auth middleware")
	}

	// --- Client Initialization ---
	psClient, err := pubsub.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Pub/Sub client")
	}
	defer func() {
		_ = psClient.Close()
	}()

	fsClient, err := firestore.NewClient(ctx, cfg.ProjectID)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Firestore client")
	}
	defer func() {
		_ = fsClient.Close()
	}()

	// --- Dependency Assembly ---
	dependencies, cleanup, err := assembleDependencies(ctx, cfg, psClient, fsClient, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to assemble service dependencies")
	}
	defer cleanup()

	// --- Service Assembly ---
	apiService, err := routingservice.New(cfg, dependencies, authMiddleware, logger)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create API service")
	}

	connManager, err := realtime.NewConnectionManager(
		":"+cfg.WebSocketPort,
		authMiddleware,
		dependencies.PresenceCache,
		dependencies.DeliveryConsumer,
		logger,
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create Connection Manager")
	}

	// --- Run Application ---
	app.Run(ctx, logger, apiService, connManager)
}

func assembleDependencies(ctx context.Context, cfg *config.AppConfig, psClient *pubsub.Client, fsClient *firestore.Client, logger zerolog.Logger) (*routing.Dependencies, func(), error) {
	// Build all real dependencies
	ingressProducer := psub.NewProducer(psClient.Publisher(cfg.IngressTopicID))
	ingressConsumer, err := setupProductionIngress(ctx, cfg, psClient, logger)
	if err != nil {
		return nil, nil, err
	}
	deliveryProducer, deliveryConsumer, deliveryCleanup, err := setupProductionDeliveryBus(ctx, cfg, psClient, logger)
	if err != nil {
		return nil, nil, err
	}
	messageStore, err := persistence.NewFirestoreStore(fsClient, logger)
	if err != nil {
		deliveryCleanup()
		return nil, nil, err
	}
	presenceCache, err := setupPresenceCache(ctx, cfg, fsClient, logger)
	if err != nil {
		deliveryCleanup()
		return nil, nil, err
	}
	tokenFetcher, err := newFirestoreTokenFetcher(ctx, cfg, fsClient, logger)
	if err != nil {
		deliveryCleanup()
		return nil, nil, err
	}

	// --- Conditional PushNotifier ---
	var pushNotifier routing.PushNotifier
	if cfg.RunMode == "local" {
		logger.Warn().Msg("Using FAKE push notifier for local development.")
		pushNotifier = fakes.NewPushNotifier(logger)
	} else {
		logger.Info().Msg("Using PRODUCTION Pub/Sub push notifier.")
		pushNotifier, err = newPushNotifier(ctx, cfg, psClient, logger)
		if err != nil {
			deliveryCleanup()
			return nil, nil, err
		}
	}

	deps := &routing.Dependencies{
		IngestionProducer:  ingressProducer,
		IngestionConsumer:  ingressConsumer,
		DeliveryProducer:   deliveryProducer,
		DeliveryConsumer:   deliveryConsumer,
		MessageStore:       messageStore,
		PresenceCache:      presenceCache,
		DeviceTokenFetcher: tokenFetcher,
		PushNotifier:       pushNotifier,
	}

	cleanup := func() {
		deliveryCleanup()
		_ = presenceCache.Close()
	}

	return deps, cleanup, nil
}

// --- Configuration & Setup Helpers ---

func loadConfig(logger zerolog.Logger) *config.AppConfig {
	cfg, err := cmd.Load()
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to load embedded config")
	}
	if idURL := os.Getenv("IDENTITY_SERVICE_URL"); idURL != "" {
		cfg.IdentityServiceURL = idURL
	}
	if port := os.Getenv("PORT"); port != "" {
		cfg.APIPort = port
	}
	if cfg.ProjectID == "" {
		logger.Fatal().Msg("FATAL: project_id is not set in config.yaml.")
	}
	if cfg.IdentityServiceURL == "" {
		logger.Fatal().Msg("FATAL: identity_service_url is not set in config.yaml or IDENTITY_SERVICE_URL env var.")
	}
	if cfg.RunMode != "local" && cfg.IngressTopicDLQID == "" {
		logger.Fatal().Msg("FATAL: ingress_topic_dlq_id must be set in production mode for the Dead-Letter Queue.")
	}
	if cfg.DeliveryBusSubscriptionExpiration == "" {
		cfg.DeliveryBusSubscriptionExpiration = "24h"
	} else {
		logger.Info().Str("expires", cfg.DeliveryBusSubscriptionExpiration).Msg("EXPIRES")
	}
	logger.Info().Str("run_mode", cfg.RunMode).Str("project_id", cfg.ProjectID).Msg("Configuration loaded.")
	return cfg
}

func newAuthMiddleware(cfg *config.AppConfig, logger zerolog.Logger) (func(http.Handler) http.Handler, error) {
	sanitizedBaseURL := strings.Trim(cfg.IdentityServiceURL, "\"")
	jwksURL := sanitizedBaseURL + "/.well-known/jwks.json"
	logger.Info().Str("url", jwksURL).Msg("Configuring JWKS client")
	return middleware.NewJWKSAuthMiddleware(jwksURL)
}

func setupProductionIngress(ctx context.Context, cfg *config.AppConfig, client *pubsub.Client, logger zerolog.Logger) (messagepipeline.MessageConsumer, error) {
	subAdminClient := client.SubscriptionAdminClient
	subPath := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, cfg.IngressSubscriptionID)
	topicPath := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.IngressTopicID)
	dlqTopicPath := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.IngressTopicDLQID)

	_, err := subAdminClient.GetSubscription(ctx, &pubsubpb.GetSubscriptionRequest{Subscription: subPath})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			logger.Info().Str("subscription", subPath).Msg("Ingress subscription not found, creating it with DLQ policy...")
			_, err = subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
				Name:               subPath,
				Topic:              topicPath,
				DeadLetterPolicy:   &pubsubpb.DeadLetterPolicy{DeadLetterTopic: dlqTopicPath, MaxDeliveryAttempts: 5},
				AckDeadlineSeconds: 10,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create ingress subscription with DLQ: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to get ingress subscription: %w", err)
		}
	} else {
		logger.Info().Str("subscription", subPath).Msg("Ingress subscription already exists.")
	}

	return messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(cfg.IngressSubscriptionID), client, logger,
	)
}

func setupProductionDeliveryBus(ctx context.Context, cfg *config.AppConfig, client *pubsub.Client, logger zerolog.Logger) (*websocket.DeliveryProducer, messagepipeline.MessageConsumer, func(), error) {
	dataflowProducer, err := messagepipeline.NewGooglePubsubProducer(
		messagepipeline.NewGooglePubsubProducerDefaults(cfg.DeliveryTopicID), client, logger,
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to create delivery bus producer: %w", err)
	}
	deliveryProducer := websocket.NewDeliveryProducer(dataflowProducer)

	subID := fmt.Sprintf("delivery-sub-%s", uuid.NewString())
	subPath := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, subID)
	topicPath := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.DeliveryTopicID)

	_, err = client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicPath})
	if err != nil {
		// Use status.Code() to get the gRPC code from the error.
		st, ok := status.FromError(err)
		if ok && st.Code() == codes.AlreadyExists {
			// If the topic already exists, it's not a failure for our use case.
			fmt.Printf("Topic %s already exists. All good! 👍\n", topicPath)
		} else {
			// For any other error, we return it.
			return nil, nil, nil, fmt.Errorf("failed to create topic: %w", err)
		}
	}

	subAdminClient := client.SubscriptionAdminClient

	exp, err := time.ParseDuration(cfg.DeliveryBusSubscriptionExpiration)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("invalid delivery_bus_subscription_expiration: %w", err)
	}

	sub, err := subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:               subPath,
		Topic:              topicPath,
		AckDeadlineSeconds: 10,
		ExpirationPolicy:   &pubsubpb.ExpirationPolicy{Ttl: durationpb.New(exp)},
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

func setupPresenceCache(ctx context.Context, cfg *config.AppConfig, fsClient *firestore.Client, logger zerolog.Logger) (cache.PresenceCache[urn.URN, routing.ConnectionInfo], error) {
	cacheType := cfg.PresenceCache.Type
	logger.Info().Str("type", cacheType).Msg("Initializing presence cache...")
	switch cacheType {
	case "firestore":
		return cache.NewFirestorePresenceCache[urn.URN, routing.ConnectionInfo](
			fsClient,
			cfg.PresenceCache.Firestore.CollectionName,
		)
	default:
		return nil, fmt.Errorf("invalid presence_cache type: %s", cacheType)
	}
}

func newFirestoreTokenFetcher(ctx context.Context, cfg *config.AppConfig, fsClient *firestore.Client, logger zerolog.Logger) (cache.Fetcher[urn.URN, []routing.DeviceToken], error) {
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

func newPushNotifier(ctx context.Context, cfg *config.AppConfig, psClient *pubsub.Client, logger zerolog.Logger) (routing.PushNotifier, error) {
	pushProducer, err := messagepipeline.NewGooglePubsubProducer(
		messagepipeline.NewGooglePubsubProducerDefaults(cfg.PushNotificationsTopicID), psClient, logger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create push notification producer: %w", err)
	}
	return push.NewPubSubNotifier(pushProducer, logger)
}
