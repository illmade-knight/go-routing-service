package routing

import (
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
)

// Config holds all necessary configuration for the routing service.
type Config struct {
	ProjectID             string
	HTTPListenAddr        string
	IngressSubscriptionID string
	IngressTopicID        string
	NumPipelineWorkers    int

	// ADDED: The secret key for validating JWTs. It will be loaded
	// from the "JWT_SECRET" environment variable.
	JWTSecret string `env:"JWT_SECRET,required"`
}

// Dependencies holds all the external services the routing service needs to operate.
type Dependencies struct {
	// REFACTOR: The generic caches now use urn.URN as the key for type safety.
	PresenceCache      cache.Fetcher[urn.URN, ConnectionInfo]
	DeviceTokenFetcher cache.Fetcher[urn.URN, []DeviceToken]
	DeliveryProducer   DeliveryProducer
	PushNotifier       PushNotifier
	MessageStore       MessageStore
}
