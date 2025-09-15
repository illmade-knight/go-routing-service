// REFACTOR: This file is corrected to accept a pre-configured routing.Dependencies
// struct. This allows the calling test (e.g., fullflow_test.go) to have full
// control over the state of the service's dependencies, such as seeding caches.

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
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// NewTestServer creates and starts a fully-functional instance of the routing
// service for use in end-to-end tests. It uses real Pub/Sub and Firestore
// clients connected to emulators, providing a high-fidelity test environment.
func NewTestServer(
	t *testing.T,
	ctx context.Context,
	psClient *pubsub.Client,
	fsClient *firestore.Client,
	// REFACTOR: Accept the full dependencies struct.
	deps *routing.Dependencies,
) *httptest.Server {
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

	cfg := &routing.Config{
		HTTPListenAddr:     ":0",
		NumPipelineWorkers: 1, // Keep tests synchronous and predictable
	}

	// 3. Assemble and start the real service using the provided dependencies.
	service, err := routingservice.New(cfg, deps, consumer, producer, logger)
	require.NoError(t, err)

	serviceCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(func() {
		_ = service.Shutdown(context.Background())
		cancel()
	})

	err = service.Start(serviceCtx)
	require.NoError(t, err)

	server := httptest.NewServer(service.Handler())
	t.Cleanup(server.Close)

	return server
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
