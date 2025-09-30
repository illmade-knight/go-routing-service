//go:build integration

package e2e_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/internal/app"
	"github.com/illmade-knight/go-routing-service/internal/platform/persistence"
	psub "github.com/illmade-knight/go-routing-service/internal/platform/pubsub"
	"github.com/illmade-knight/go-routing-service/internal/platform/websocket"
	"github.com/illmade-knight/go-routing-service/internal/realtime"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice"
	"github.com/illmade-knight/go-routing-service/routingservice/config"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
)

// --- Test Helpers ---

type mockPushNotifier struct {
	handled chan urn.URN
}

func (m *mockPushNotifier) Notify(_ context.Context, _ []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	m.handled <- envelope.RecipientID
	return nil
}

func newJWKSTestServer(t *testing.T, privateKey *rsa.PrivateKey) *httptest.Server {
	t.Helper()
	publicKey, err := jwk.FromRaw(privateKey.Public())
	require.NoError(t, err)
	_ = publicKey.Set(jwk.KeyIDKey, "test-key-id")
	_ = publicKey.Set(jwk.AlgorithmKey, jwa.RS256)
	keySet := jwk.NewSet()
	_ = keySet.AddKey(publicKey)
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(keySet)
		require.NoError(t, err)
	})
	return httptest.NewServer(mux)
}

func createTestRS256Token(t *testing.T, privateKey *rsa.PrivateKey, userID string) string {
	t.Helper()

	// 1. Create a jwk.Key object from the raw RSA private key.
	// This is the idiomatic object for representing keys in the jwx ecosystem.
	jwkKey, err := jwk.FromRaw(privateKey)
	require.NoError(t, err)

	// 2. Attach the "kid" metadata directly to the key object.
	err = jwkKey.Set(jwk.KeyIDKey, "test-key-id")
	require.NoError(t, err)

	// 3. Build the token payload as before.
	token, err := jwt.NewBuilder().
		Subject(userID).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Build()
	require.NoError(t, err)

	// 4. Sign the token using the jwk.Key object. The library will now
	// automatically use the metadata attached to the key (like "kid")
	// to construct the protected header.
	signed, err := jwt.Sign(token, jwt.WithKey(jwa.RS256, jwkKey))
	require.NoError(t, err)

	return string(signed)
}

// --- Main Test ---

func TestFullStoreAndRetrieveFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	logger := zerolog.New(zerolog.NewTestWriter(t))
	const projectID = "test-project-e2e"
	runID := uuid.NewString()

	// --- 1. Setup Emulators & Auth ---
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	jwksServer := newJWKSTestServer(t, privateKey)
	t.Cleanup(jwksServer.Close)

	pubsubConn := emulators.SetupPubsubEmulator(t, ctx, emulators.GetDefaultPubsubConfig(projectID))
	psClient, err := pubsub.NewClient(ctx, projectID, pubsubConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = psClient.Close() })

	firestoreConn := emulators.SetupFirestoreEmulator(t, ctx, emulators.GetDefaultFirestoreConfig(projectID))
	fsClient, err := firestore.NewClient(context.Background(), projectID, firestoreConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsClient.Close() })

	// --- 2. Arrange service dependencies ---
	senderURN, _ := urn.Parse("urn:sm:user:user-alice")
	recipientURN, _ := urn.Parse("urn:sm:user:user-bob")

	testConfig := &config.AppConfig{
		ProjectID:          projectID,
		APIPort:            "0",
		WebSocketPort:      "0",
		NumPipelineWorkers: 2,
		Cors:               config.YamlCorsConfig{AllowedOrigins: []string{"*"}, Role: "admin"},
		IdentityServiceURL: jwksServer.URL,
		IngressTopicID:     "ingress-" + runID,
		DeliveryTopicID:    "delivery-" + runID,
	}

	createTopic(t, ctx, psClient, testConfig.ProjectID, testConfig.IngressTopicID)
	createTopic(t, ctx, psClient, testConfig.ProjectID, testConfig.DeliveryTopicID)

	_, err = fsClient.Collection("device-tokens").Doc(recipientURN.String()).Set(ctx, map[string]interface{}{
		"Tokens": []routing.DeviceToken{{Token: "persistent-device-token-123", Platform: "ios"}},
	})
	require.NoError(t, err)

	offlineHandled := make(chan urn.URN, 1)
	deps, err := assembleTestDependencies(ctx, psClient, fsClient, testConfig, &mockPushNotifier{handled: offlineHandled}, logger)
	require.NoError(t, err)

	// --- 3. Start the FULL Routing Service (API + ConnectionManager) ---
	authMiddleware, err := middleware.NewJWKSAuthMiddleware(jwksServer.URL + "/.well-known/jwks.json")
	require.NoError(t, err)

	apiService, err := routingservice.New(testConfig, deps, authMiddleware, logger)
	require.NoError(t, err)

	connManager, err := realtime.NewConnectionManager(
		":"+testConfig.WebSocketPort,
		authMiddleware,
		deps.PresenceCache,
		deps.DeliveryConsumer,
		logger,
	)
	require.NoError(t, err)

	serviceCtx, cancelService := context.WithCancel(context.Background())
	t.Cleanup(cancelService)

	go app.Run(serviceCtx, logger, apiService, connManager)

	var routingServerURL string
	require.Eventually(t, func() bool {
		port := apiService.GetHTTPPort()
		if port != "" && port != ":0" {
			routingServerURL = "http://localhost" + port
			return true
		}
		return false
	}, 10*time.Second, 100*time.Millisecond, "API service did not start and report a port")

	// --- PHASE 1: Send message ---
	t.Log("Phase 1: Sending message to offline user...")
	envelope := &transport.SecureEnvelope{MessageID: uuid.NewString(), SenderID: senderURN, RecipientID: recipientURN}
	protoEnvelope := transport.ToProto(envelope)
	envelopeBytes, err := protojson.Marshal(protoEnvelope)
	require.NoError(t, err)

	sendReq, _ := http.NewRequest(http.MethodPost, routingServerURL+"/send", bytes.NewReader(envelopeBytes))
	sendReq.Header.Set("Content-Type", "application/json")

	// --- Add these two lines to debug the token ---
	tokenString := createTestRS256Token(t, privateKey, senderURN.EntityID())
	fmt.Printf("\n\n--- DEBUG TOKEN ---\n%s\n-------------------\n\n", tokenString)

	sendReq.Header.Set("Authorization", "Bearer "+tokenString)
	sendResp, err := http.DefaultClient.Do(sendReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, sendResp.StatusCode)
	_ = sendResp.Body.Close()

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
	}, 10*time.Second, 100*time.Millisecond, "Expected message to be stored")
	t.Log("✅ Message correctly stored in Firestore.")

	// --- PHASE 2: Retrieve message ---
	t.Log("Phase 2: Retrieving message...")
	getReq, _ := http.NewRequest(http.MethodGet, routingServerURL+"/messages", nil)
	getReq.Header.Set("Authorization", "Bearer "+createTestRS256Token(t, privateKey, recipientURN.EntityID()))
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	respBody, err := io.ReadAll(getResp.Body)
	require.NoError(t, err)
	_ = getResp.Body.Close()

	var receivedList transport.SecureEnvelopeListPb
	err = protojson.Unmarshal(respBody, &receivedList)
	require.NoError(t, err)
	require.Len(t, receivedList.Envelopes, 1)
	require.Equal(t, envelope.MessageID, receivedList.Envelopes[0].MessageId)
	t.Log("✅ Correct message retrieved.")

	apiService.Wait()

	docsAfter, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
	require.NoError(t, err)
	require.Empty(t, docsAfter, "Expected message to be deleted after retrieval")
	t.Log("✅ Message correctly deleted after retrieval.")
}

// --- Test Setup Helpers ---

func assembleTestDependencies(ctx context.Context, psClient *pubsub.Client, fsClient *firestore.Client, cfg *config.AppConfig, notifier routing.PushNotifier, logger zerolog.Logger) (*routing.Dependencies, error) {
	ingressPublisher := psClient.Publisher(cfg.IngressTopicID)
	ingressProducer := psub.NewProducer(ingressPublisher)

	dataflowProducer, err := messagepipeline.NewGooglePubsubProducer(
		messagepipeline.NewGooglePubsubProducerDefaults(cfg.DeliveryTopicID), psClient, logger,
	)
	if err != nil {
		return nil, err
	}
	deliveryProducer := websocket.NewDeliveryProducer(dataflowProducer)

	ingressSubID := "e2e-ingress-sub-" + uuid.NewString()
	ingressSubPath := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, ingressSubID)
	_, err = psClient.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  ingressSubPath,
		Topic: fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.IngressTopicID),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create e2e ingress subscription: %w", err)
	}
	ingressConsumer, err := messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(ingressSubID), psClient, logger,
	)
	if err != nil {
		return nil, err
	}

	deliverySubID := "e2e-delivery-sub-" + uuid.NewString()
	deliverySubPath := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, deliverySubID)
	_, err = psClient.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:             deliverySubPath,
		Topic:            fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.DeliveryTopicID),
		ExpirationPolicy: &pubsubpb.ExpirationPolicy{Ttl: &durationpb.Duration{Seconds: int64((24 * time.Hour).Seconds())}},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create e2e delivery subscription: %w", err)
	}
	deliveryConsumer, err := messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(deliverySubID), psClient, logger,
	)
	if err != nil {
		return nil, err
	}

	messageStore, err := persistence.NewFirestoreStore(fsClient, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create firestore store for test: %w", err)
	}
	presenceCache := cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo]()

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
	deviceTokenFetcher := persistence.NewURNTokenFetcherAdapter(stringTokenFetcher)

	return &routing.Dependencies{
		IngestionProducer:  ingressProducer,
		IngestionConsumer:  ingressConsumer,
		DeliveryProducer:   deliveryProducer,
		DeliveryConsumer:   deliveryConsumer,
		MessageStore:       messageStore,
		PresenceCache:      presenceCache,
		DeviceTokenFetcher: deviceTokenFetcher,
		PushNotifier:       notifier,
	}, nil
}

func createTopic(t *testing.T, ctx context.Context, client *pubsub.Client, projectID, topicID string) {
	t.Helper()

	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	_, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.TopicAdminClient.DeleteTopic(context.Background(), &pubsubpb.DeleteTopicRequest{Topic: topicName})
	})
}

// TEMPORARY DIAGNOSTIC TEST// UPDATED DIAGNOSTIC TEST
// This version tests the idiomatic pattern of attaching the "kid" to the key itself.
// UPDATED DIAGNOSTIC TEST
// This version tests the idiomatic pattern of attaching the "kid" to the key itself.
func Test_JWSSignHeader(t *testing.T) {
	// 1. Setup a raw RSA key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// 2. Create a jwk.Key object from the raw key.
	jwkKey, err := jwk.FromRaw(privateKey)
	require.NoError(t, err)

	// 3. Attach the "kid" metadata directly to the key object.
	err = jwkKey.Set(jws.KeyIDKey, "test-key-id")
	require.NoError(t, err)

	// 4. Create a simple payload
	payload := []byte(`{"sub":"test-user"}`)

	// 5. Sign the payload using the jwk.Key object.
	// We are no longer trying to pass a separate headers option.
	signed, err := jws.Sign(payload, jws.WithKey(jwa.RS256, jwkKey))
	require.NoError(t, err)

	// 6. Parse the token we just created to inspect its headers
	message, err := jws.Parse(signed)
	require.NoError(t, err)
	require.Len(t, message.Signatures(), 1, "JWS should have exactly one signature")

	// 7. Check if the "kid" is present in the signature's protected headers
	kid, ok := message.Signatures()[0].ProtectedHeaders().Get(jws.KeyIDKey)

	// 8. Print the result for clarity
	fmt.Printf("\n--- DIAGNOSTIC TEST (v2) --- \n'kid' header found: %v\nValue: %v\n--------------------------\n", ok, kid)

	// 9. This assertion will now pass if the library correctly uses the key's metadata.
	assert.True(t, ok, "The 'kid' header was NOT added from the jwk.Key object")
	assert.Equal(t, "test-key-id", kid, "The 'kid' header has the wrong value")
}
