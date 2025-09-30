package routing

import (
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
)

// Config holds all necessary configuration for the routing service.
type Config struct {
	ProjectID             string
	HTTPListenAddr        string
	WebSocketListenAddr   string
	IngressSubscriptionID string
	IngressTopicID        string
	DeliveryTopicID       string // ADDED
	NumPipelineWorkers    int
	JWTSecret             string `env:"JWT_SECRET,required"`
	CorsConfig            middleware.CorsConfig
}

// Dependencies holds all the external services the routing service needs to operate.
type Dependencies struct {
	// --- Producers ---
	// For publishing messages to the initial ingestion topic.
	IngestionProducer IngestionProducer
	// For publishing messages to the real-time delivery topic for online users.
	DeliveryProducer DeliveryProducer

	// --- Consumers ---
	// For the main pipeline to consume from the ingestion topic.
	IngestionConsumer messagepipeline.MessageConsumer
	// For the WebSocket manager to consume from the delivery topic.
	DeliveryConsumer messagepipeline.MessageConsumer

	// --- Storage & Caches ---
	MessageStore       MessageStore
	PresenceCache      cache.PresenceCache[urn.URN, ConnectionInfo]
	DeviceTokenFetcher cache.Fetcher[urn.URN, []DeviceToken]

	// --- External Services ---
	PushNotifier PushNotifier
}
