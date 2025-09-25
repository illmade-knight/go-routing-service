// The local command is an entrypoint for running the routing service locally
// with in-memory components for development and testing.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/internal/app"
	"github.com/illmade-knight/go-routing-service/internal/platform/websocket"
	"github.com/illmade-knight/go-routing-service/internal/realtime"
	"github.com/illmade-knight/go-routing-service/internal/test/fakes"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
)

// localIngestionProducer is a small, local adapter to make our fake Producer
// satisfy the routing.IngestionProducer interface, which expects a SecureEnvelope.
type localIngestionProducer struct {
	producer *fakes.Producer
}

// Publish implements the routing.IngestionProducer interface.
func (p *localIngestionProducer) Publish(ctx context.Context, envelope *transport.SecureEnvelope) error {
	protoEnv := transport.ToProto(envelope)
	payload, err := json.Marshal(protoEnv)
	if err != nil {
		return err
	}
	_, err = p.producer.Publish(ctx, messagepipeline.MessageData{ID: envelope.MessageID, Payload: payload})
	return err
}

// main wires up the in-memory dependencies and starts the service.
func main() {
	logger := zerolog.New(zerolog.NewConsoleWriter())
	ctx := context.Background()

	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		logger.Fatal().Msg("FATAL: JWT_SECRET environment variable not set.")
	}

	presenceCache := cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo]()
	ingressConsumer := fakes.NewInMemoryConsumer(100, logger)
	deliveryConsumer := fakes.NewInMemoryConsumer(100, logger)
	ingestionEventProducer := fakes.NewProducer(logger)
	deliveryEventProducer := fakes.NewProducer(logger)

	deliveryProducer := websocket.NewDeliveryProducer(deliveryEventProducer)
	ingestionProducer := &localIngestionProducer{producer: ingestionEventProducer}

	deps := &routing.Dependencies{
		PresenceCache:      presenceCache,
		DeliveryProducer:   deliveryProducer,
		DeviceTokenFetcher: fakes.NewTokenFetcher(),
		PushNotifier:       fakes.NewPushNotifier(logger),
		MessageStore:       fakes.NewMessageStore(logger),
	}

	apiService, err := routingservice.New(
		&routing.Config{
			HTTPListenAddr:     ":8082",
			NumPipelineWorkers: 2,
			JWTSecret:          jwtSecret,
			// REFACTOR: The 'Role' field has been restored to the CORS configuration.
			Cors: middleware.CorsConfig{AllowedOrigins: []string{"*"}, Role: middleware.CorsRoleAdmin},
		},
		deps,
		ingressConsumer,
		ingestionProducer,
		logger,
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create local API service")
	}

	connManager, err := realtime.NewConnectionManager(
		":8083",
		jwtSecret,
		presenceCache,
		deliveryConsumer,
		logger,
	)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to create local Connection Manager")
	}

	go func() {
		for msgData := range ingestionEventProducer.Published() {
			ingressConsumer.Publish(messagepipeline.Message{MessageData: msgData})
		}
	}()

	go func() {
		for msgData := range deliveryEventProducer.Published() {
			deliveryConsumer.Publish(messagepipeline.Message{MessageData: msgData})
		}
	}()

	logger.Info().Msg("Local service configured with in-memory components.")
	app.Run(ctx, logger, apiService, connManager)
}
