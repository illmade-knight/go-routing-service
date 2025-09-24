//go:build integration

// REFACTOR: This is the final, fully corrected E2E test. It uses the canonical
// transport.SecureEnvelope and the corrected, URN-aware test helpers to
// validate the entire refactored service flow.

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	routingtest "github.com/illmade-knight/go-routing-service/test"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockPushNotifier is a test helper that signals when it has been called.
type mockPushNotifier struct {
	handled chan urn.URN
}

func (m *mockPushNotifier) Notify(_ context.Context, _ []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	m.handled <- envelope.RecipientID
	return nil
}

func TestFullStoreAndRetrieveFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	logger := zerolog.New(zerolog.NewTestWriter(t))
	const projectID = "test-project"
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
	senderURN, err := urn.Parse("urn:sm:user:user-alice")
	require.NoError(t, err)
	recipientURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	_, err = fsClient.Collection("device-tokens").Doc(recipientURN.String()).Set(ctx, map[string]interface{}{
		"Tokens": []routing.DeviceToken{{Token: "persistent-device-token-123", Platform: "ios"}},
	})
	require.NoError(t, err)

	tokenFetcher, err := routingtest.NewTestFirestoreTokenFetcher(context.Background(), fsClient, projectID, logger)
	require.NoError(t, err)

	messageStore, err := routingtest.NewTestMessageStore(fsClient, logger)
	require.NoError(t, err)

	offlineHandled := make(chan urn.URN, 1)
	deps := &routing.Dependencies{
		PresenceCache:      cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo](),
		DeviceTokenFetcher: tokenFetcher,
		PushNotifier:       &mockPushNotifier{handled: offlineHandled},
		MessageStore:       messageStore,
	}

	ingressTopicID := "ingress-topic-" + runID
	subID := "sub-" + runID
	createPubsubResources(t, ctx, psClient, projectID, ingressTopicID, subID)
	consumer, err := routingtest.NewTestConsumer(subID, psClient, logger)
	require.NoError(t, err)
	producer := routingtest.NewTestProducer(psClient.Publisher(ingressTopicID))

	// 3. Start the Routing Service
	routingService, err := routingservice.New(&routing.Config{HTTPListenAddr: ":0"}, deps, consumer, producer, logger)
	require.NoError(t, err)
	require.NoError(t, routingService.Start(ctx))
	t.Cleanup(func() { _ = routingService.Shutdown(context.Background()) })

	// --- PHASE 1: Send message and verify storage ---
	t.Log("Phase 1: Sending message to offline user...")
	envelope := transport.SecureEnvelope{
		MessageID:   uuid.NewString(),
		SenderID:    senderURN,
		RecipientID: recipientURN,
	}
	envelopeBytes, err := json.Marshal(envelope)
	require.NoError(t, err)

	var routingServerURL string
	require.Eventually(t, func() bool {
		port := routingService.GetHTTPPort()
		if port != "" {
			routingServerURL = "http://localhost" + port
			return true
		}
		return false
	}, 5*time.Second, 50*time.Millisecond)

	sendReq, _ := http.NewRequest(http.MethodPost, routingServerURL+"/send", bytes.NewReader(envelopeBytes))
	sendReq.Header.Set("Content-Type", "application/json")
	sendResp, err := http.DefaultClient.Do(sendReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, sendResp.StatusCode)

	select {
	case receivedRecipientURN := <-offlineHandled:
		require.Equal(t, recipientURN, receivedRecipientURN)
		t.Log("✅ Push notification correctly triggered.")
	case <-time.After(15 * time.Second):
		t.Fatal("Test timed out waiting for push notification")
	}

	// REFACTOR: Use require.Eventually to make the test robust. It will poll
	// Firestore until the message appears or the timeout is reached. This
	// correctly handles the asynchronous nature of the pipeline.
	require.Eventually(t, func() bool {
		docs, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
		if err != nil {
			return false // Keep trying if there's a transient error
		}
		return len(docs) == 1
	}, 5*time.Second, 100*time.Millisecond, "Expected exactly one message to be stored in firestore")
	t.Log("✅ Message correctly stored in Firestore.")

	// --- PHASE 2: Retrieve message and verify cleanup ---
	t.Log("Phase 2: Retrieving message as user comes online...")
	getReq, _ := http.NewRequest(http.MethodGet, routingServerURL+"/messages", nil)
	getReq.Header.Set("X-User-ID", recipientURN.String())
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	var receivedEnvelopes []*transport.SecureEnvelope
	err = json.NewDecoder(getResp.Body).Decode(&receivedEnvelopes)
	require.NoError(t, err)
	require.Len(t, receivedEnvelopes, 1)
	assert.Equal(t, envelope.MessageID, receivedEnvelopes[0].MessageID)
	t.Log("✅ Correct message retrieved from /messages endpoint.")

	require.Eventually(t, func() bool {
		docsAfter, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
		if err != nil {
			return false
		}
		return len(docsAfter) == 0
	}, 5*time.Second, 100*time.Millisecond, "Expected message to be deleted from firestore after retrieval")
	t.Log("✅ Message correctly deleted from Firestore after retrieval.")
}

// createPubsubResources is a test helper to provision pub/sub topics and subscriptions.
func createPubsubResources(t *testing.T, ctx context.Context, client *pubsub.Client, projectID, topicID, subID string) {
	t.Helper()

	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	_, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{
		Name: topicName,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.TopicAdminClient.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: topicName})
	})

	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	_, err = client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{Topic: topicName, Name: subName})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.SubscriptionAdminClient.DeleteSubscription(ctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})
	})
}
