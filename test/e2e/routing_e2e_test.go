//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/golang-jwt/jwt/v5"
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
	"google.golang.org/protobuf/encoding/protojson"
)

// mockPushNotifier is a test helper that signals when it has been called.
type mockPushNotifier struct {
	handled chan urn.URN
}

func (m *mockPushNotifier) Notify(_ context.Context, _ []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	m.handled <- envelope.RecipientID
	return nil
}

// generateJWT creates a signed JWT for a given user ID.
func generateJWT(t *testing.T, userID, secret string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Minute * 5).Unix(),
	})
	signedToken, err := token.SignedString([]byte(secret))
	require.NoError(t, err)
	return signedToken
}

func TestFullStoreAndRetrieveFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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
	senderURN, _ := urn.Parse("urn:sm:user:user-alice")
	recipientURN, _ := urn.Parse("urn:sm:user:user-bob")
	jwtSecret := "a-very-secret-e2e-key"

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

	// 3. Start the Routing Service in a Goroutine
	routingService, err := routingservice.New(&routing.Config{HTTPListenAddr: ":0", JWTSecret: jwtSecret}, deps, consumer, producer, logger)
	require.NoError(t, err)

	serviceCtx, cancelService := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancelService() // Tell the service to stop
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		_ = routingService.Shutdown(shutdownCtx)
	})

	go func() {
		if err := routingService.Start(serviceCtx); err != nil && err != http.ErrServerClosed {
			t.Logf("Routing service returned an error: %v", err)
		}
	}()

	// --- Wait for service to be ready ---
	var routingServerURL string
	require.Eventually(t, func() bool {
		port := routingService.GetHTTPPort()
		// THE FIX: Ensure the port is not empty AND not the initial ":0" value.
		if port != "" && port != ":0" {
			routingServerURL = "http://localhost" + port
			return true
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "Service did not become ready in time")

	// --- PHASE 1: Send message and verify storage ---
	t.Log("Phase 1: Sending message to offline user...")
	envelope := &transport.SecureEnvelope{
		MessageID:   uuid.NewString(),
		SenderID:    senderURN,
		RecipientID: recipientURN,
	}
	protoEnvelope := transport.ToProto(envelope)
	envelopeBytes, err := protojson.Marshal(protoEnvelope)
	require.NoError(t, err)

	sendReq, _ := http.NewRequest(http.MethodPost, routingServerURL+"/send", bytes.NewReader(envelopeBytes))
	sendReq.Header.Set("Content-Type", "application/json")
	senderToken := generateJWT(t, senderURN.EntityID(), jwtSecret)
	sendReq.Header.Set("Authorization", "Bearer "+senderToken)
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

	require.Eventually(t, func() bool {
		docs, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
		return err == nil && len(docs) == 1
	}, 10*time.Second, 100*time.Millisecond, "Expected exactly one message to be stored in firestore")
	t.Log("✅ Message correctly stored in Firestore.")

	// --- PHASE 2: Retrieve message and verify cleanup ---
	t.Log("Phase 2: Retrieving message as user comes online...")
	getReq, _ := http.NewRequest(http.MethodGet, routingServerURL+"/messages", nil)
	recipientToken := generateJWT(t, recipientURN.EntityID(), jwtSecret)
	getReq.Header.Set("Authorization", "Bearer "+recipientToken)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	respBody, err := io.ReadAll(getResp.Body)
	require.NoError(t, err)
	_ = getResp.Body.Close()

	var receivedEnvelopeList transport.SecureEnvelopeListPb
	err = protojson.Unmarshal(respBody, &receivedEnvelopeList)
	require.NoError(t, err)
	require.Len(t, receivedEnvelopeList.Envelopes, 1)

	retrievedNative, err := transport.FromProto(receivedEnvelopeList.Envelopes[0])
	require.NoError(t, err)
	assert.Equal(t, envelope.MessageID, retrievedNative.MessageID)
	t.Log("✅ Correct message retrieved from /messages endpoint.")

	require.Eventually(t, func() bool {
		docsAfter, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
		return err == nil && len(docsAfter) == 0
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
		_ = client.TopicAdminClient.DeleteTopic(context.Background(), &pubsubpb.DeleteTopicRequest{Topic: topicName})
	})

	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	_, err = client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{Topic: topicName, Name: subName})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.SubscriptionAdminClient.DeleteSubscription(context.Background(), &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})
	})
}
