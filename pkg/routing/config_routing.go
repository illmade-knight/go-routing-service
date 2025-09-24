package routing

import (
	"github.com/illmade-knight/go-dataflow/pkg/cache"
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
	NumPipelineWorkers    int
	JWTSecret             string `env:"JWT_SECRET,required"`
	Cors                  middleware.CorsConfig
}

// Dependencies holds all the external services the routing service needs to operate.
type Dependencies struct {
	PresenceCache      cache.PresenceCache[urn.URN, ConnectionInfo]
	DeviceTokenFetcher cache.Fetcher[urn.URN, []DeviceToken]
	DeliveryProducer   DeliveryProducer
	PushNotifier       PushNotifier
	MessageStore       MessageStore
}
