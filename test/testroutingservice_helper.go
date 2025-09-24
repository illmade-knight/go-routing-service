// REFACTOR: This file is completely updated to support the "Dual Server" model.
// It now starts both the stateless API service and the stateful WebSocket
// ConnectionManager as separate httptest servers and returns their URLs.
package test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/internal/platform/websocket"
	"github.com/illmade-knight/go-routing-service/internal/realtime"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestService holds the accessible URLs for the running test servers.
type TestService struct {
	API_URL       string
	WebSocket_URL string
}

// NewTestService creates and starts a fully-functional instance of the routing
// service for use in end-to-end tests.
func NewTestService(
	t *testing.T,
	ctx context.Context,
	psClient *pubsub.Client,
	fsClient *firestore.Client,
	deps *routing.Dependencies,
) *TestService {
	t.Helper()
	logger := zerolog.New(zerolog.NewTestWriter(t))
	projectID := "test-project" // Assumed project for emulators
	runID := uuid.NewString()

	// 1. Create Pub/Sub resources for this specific test run.
	ingressTopicID := "ingress-topic-" + runID
	ingressSubID := "ingress-sub-" + runID
	createPubsubResources(t, ctx, psClient, projectID, ingressTopicID, ingressSubID)

	// 2. Create concrete producers and consumers using the test helpers.
	consumer, err := NewTestConsumer(ingressSubID, psClient, logger)
	require.NoError(t, err)
	producer := NewTestProducer(psClient.Publisher(ingressTopicID))

	// 3. Create the configuration for the service.
	cfg := &routing.Config{
		// Let httptest choose the ports by leaving these blank.
		HTTPListenAddr:      "",
		WebSocketListenAddr: "",
		NumPipelineWorkers:  1, // Keep tests synchronous and predictable
		JWTSecret:           "test-secret",
		Cors: middleware.CorsConfig{
			AllowedOrigins: []string{"*"}, // Allow all for testing
			Role:           middleware.CorsRoleAdmin,
		},
	}

	// 4. Assemble and start the two separate servers.

	// A. Create the REAL WebSocket Delivery Producer that will be shared.
	deliveryProducer := websocket.NewDeliveryProducer(nil, logger)
	// Inject this real producer into the dependencies for the API service pipeline.
	deps.DeliveryProducer = deliveryProducer

	// B. The STATELESS API Service
	apiService, err := routingservice.New(cfg, deps, consumer, producer, logger)
	require.NoError(t, err)

	// C. The STATEFUL WebSocket Service
	connManager := realtime.NewConnectionManager(cfg.WebSocketListenAddr, cfg.JWTSecret, deps.PresenceCache, deliveryProducer, logger)
	deliveryProducer.SetConnectionManager(connManager) // Complete the circular dependency setup

	// Start background components in goroutines so they don't block.
	serviceCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() {
		if err := apiService.Start(serviceCtx); err != nil {
			if err.Error() != "http: Server closed" {
				t.Logf("API service returned an error: %v", err)
			}
		}
	}()
	go func() {
		if err := connManager.Start(serviceCtx); err != nil {
			if err.Error() != "http: Server closed" {
				t.Logf("Connection manager returned an error: %v", err)
			}
		}
	}()

	// Use httptest to manage the servers and get their dynamic URLs.
	apiTestServer := httptest.NewServer(apiService.Mux())
	t.Cleanup(apiTestServer.Close)

	wsTestServer := httptest.NewServer(connManager.Mux())
	t.Cleanup(wsTestServer.Close)

	return &TestService{
		API_URL:       apiTestServer.URL,
		WebSocket_URL: wsTestServer.URL,
	}
}

// createPubsubResources is a private helper to provision ephemeral pub/sub resources.
func createPubsubResources(t *testing.T, ctx context.Context, client *pubsub.Client, projectID, topicID, subID string) {
	t.Helper()
	topicAdminClient := client.TopicAdminClient
	subAdminClient := client.SubscriptionAdminClient

	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	_, err := topicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = topicAdminClient.DeleteTopic(context.Background(), &pubsubpb.DeleteTopicRequest{Topic: topicName})
	})

	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	_, err = subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  subName,
		Topic: topicName,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = subAdminClient.DeleteSubscription(context.Background(), &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})
	})
}
