//go:build integration

package pipeline_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-routing-service/internal/platform/persistence"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// mockPushNo
func TestPipeline_OfflineStorage_Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	logger := zerolog.New(zerolog.NewTestWriter(t))
	const projectID = "test-project-pipeline"
	runID := uuid.NewString()

	// 1. Setup Emulators
	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	firestoreConn := emulators.SetupFirestoreEmulator(t, ctx, emulators.GetDefaultFirestoreConfig(projectID))
	fsClient, err := firestore.NewClient(context.Background(), projectID, firestoreConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsClient.Close() })

	// 2. Arrange service dependencies
	recipientURN, _ := urn.Parse("urn:sm:user:pipeline-user")
	messageStore, err := persistence.NewFirestoreStore(fsClient, logger)
	require.NoError(t, err)

	deps := &routing.Dependencies{
		PresenceCache:      cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo](),
		DeviceTokenFetcher: &mockTokenFetcher{}, // Returns an error to force offline path
		PushNotifier:       &mockPushNotifier{notified: make(chan struct{})},
		MessageStore:       messageStore,
	}

	// 3. Create a real Pub/Sub consumer for the pipeline
	ingressTopicID := "ingress-topic-" + runID
	subID := "sub-" + runID
	createPubsubResources(t, ctx, psClient, projectID, ingressTopicID, subID)
	consumer, err := messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(subID), psClient, logger,
	)
	require.NoError(t, err)

	// 4. Assemble and start the ENTIRE processing pipeline
	pipelineService, err := pipeline.NewService(pipeline.Config{NumWorkers: 1}, deps, consumer, logger)
	require.NoError(t, err)

	pipelineCtx, cancelPipeline := context.WithCancel(ctx)
	defer cancelPipeline()
	err = pipelineService.Start(pipelineCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipelineService.Stop(context.Background()) })

	// 5. Publish a message to the pipeline's input topic
	senderURN, _ := urn.Parse("urn:sm:user:pipeline-sender")
	originalEnvelope := &transport.SecureEnvelope{
		MessageID:     uuid.NewString(),
		SenderID:      senderURN,
		RecipientID:   recipientURN,
		EncryptedData: []byte("this data must not be corrupted"),
	}
	protoEnv := transport.ToProto(originalEnvelope)
	payload, err := protojson.Marshal(protoEnv)
	require.NoError(t, err)

	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, ingressTopicID)

	publisher := psClient.Publisher(topicName)

	result := publisher.Publish(ctx, &pubsub.Message{Data: payload})
	_, err = result.Get(ctx)
	require.NoError(t, err)

	// 6. Assert: Wait for the pipeline to process and store the message.
	// Use require.Eventually to poll Firestore until the message appears.
	require.Eventually(t, func() bool {
		docs, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
		return err == nil && len(docs) == 1
	}, 10*time.Second, 100*time.Millisecond, "Message was not stored in Firestore")

	// 7. Final Verification: Retrieve the message and assert data integrity.
	docs, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
	require.NoError(t, err)
	var storedProto transport.SecureEnvelopePb
	err = docs[0].DataTo(&storedProto)
	require.NoError(t, err)
	storedEnvelope, err := transport.FromProto(&storedProto)
	require.NoError(t, err)

	assert.Equal(t, originalEnvelope.EncryptedData, storedEnvelope.EncryptedData, "EncryptedData was corrupted in the pipeline")
	t.Log("✅ Pipeline integration test passed. Data integrity verified.")
}

// --- Test Helpers ---

type mockTokenFetcher struct{}

func (m *mockTokenFetcher) Fetch(context.Context, urn.URN) ([]routing.DeviceToken, error) {
	return nil, fmt.Errorf("no tokens found") // Force offline path
}
func (m *mockTokenFetcher) Close() error { return nil }

func createPubsubResources(t *testing.T, ctx context.Context, client *pubsub.Client, projectID, topicID, subID string) {
	t.Helper()
	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	_, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
	require.NoError(t, err)
	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	_, err = client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{Topic: topicName, Name: subName})
	require.NoError(t, err)
}
