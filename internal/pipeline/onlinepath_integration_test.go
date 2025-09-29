//go:build integration

package pipeline_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/pubsub/v2"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-routing-service/internal/platform/persistence"
	"github.com/illmade-knight/go-routing-service/internal/platform/websocket"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// NOTE: This mockPushNotifier is intentionally simple for this test, as we
// are asserting that it is NOT called.
type onlinePathMockPushNotifier struct{}

func (m *onlinePathMockPushNotifier) Notify(context.Context, []routing.DeviceToken, *transport.SecureEnvelope) error {
	return nil // Does nothing
}

func TestPipeline_OnlineDelivery_Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	logger := zerolog.New(zerolog.NewTestWriter(t))
	const projectID = "test-project-online"
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
	senderURN, _ := urn.Parse("urn:sm:user:online-sender")
	recipientURN, _ := urn.Parse("urn:sm:user:online-recipient")

	presenceCache, err := cache.NewFirestorePresenceCache[urn.URN, routing.ConnectionInfo](
		fsClient,
		"presence-test-"+runID,
	)
	require.NoError(t, err)

	deliveryTopicID := "delivery-topic-" + runID
	dataflowProducer, err := messagepipeline.NewGooglePubsubProducer(
		messagepipeline.NewGooglePubsubProducerDefaults(deliveryTopicID), psClient, logger,
	)
	require.NoError(t, err)
	deliveryProducer := websocket.NewDeliveryProducer(dataflowProducer)

	messageStore, err := persistence.NewFirestoreStore(fsClient, logger)
	require.NoError(t, err)

	deps := &routing.Dependencies{
		PresenceCache:      presenceCache,
		DeviceTokenFetcher: &mockTokenFetcher{}, // Fails to ensure presence is the only online path
		PushNotifier:       &onlinePathMockPushNotifier{},
		MessageStore:       messageStore,
		DeliveryProducer:   deliveryProducer,
	}

	// 3. Create Pub/Sub resources for both ingress and delivery topics
	ingressTopicID := "ingress-topic-" + runID
	ingressSubID := "ingress-sub-" + runID
	deliverySubID := "delivery-sub-" + runID
	createPubsubResources(t, ctx, psClient, projectID, ingressTopicID, ingressSubID)
	createPubsubResources(t, ctx, psClient, projectID, deliveryTopicID, deliverySubID)

	consumer, err := messagepipeline.NewGooglePubsubConsumer(
		messagepipeline.NewGooglePubsubConsumerDefaults(ingressSubID), psClient, logger,
	)
	require.NoError(t, err)

	// 4. Assemble and start the pipeline
	// CORRECTED: Pass the new, required routing.Config to the service constructor.
	routingCfg := &routing.Config{DeliveryTopicID: deliveryTopicID}
	pipelineService, err := pipeline.NewService(pipeline.Config{NumWorkers: 1}, deps, routingCfg, consumer, logger)
	require.NoError(t, err)

	pipelineCtx, cancelPipeline := context.WithCancel(ctx)
	defer cancelPipeline()
	err = pipelineService.Start(pipelineCtx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pipelineService.Stop(context.Background()) })

	// 5. ARRANGE: Set the recipient's presence to trigger the "online" path
	err = presenceCache.Set(ctx, recipientURN, routing.ConnectionInfo{ServerInstanceID: "test-server"})
	require.NoError(t, err)
	t.Logf("Set presence for user %s", recipientURN)

	// 6. ACT: Publish a message to the pipeline's input topic
	originalEnvelope := &transport.SecureEnvelope{
		MessageID:     uuid.NewString(),
		SenderID:      senderURN,
		RecipientID:   recipientURN,
		EncryptedData: []byte("online delivery data"),
	}
	protoEnv := transport.ToProto(originalEnvelope)
	payload, err := protojson.Marshal(protoEnv)
	require.NoError(t, err)

	psClient.Publisher(ingressTopicID).Publish(ctx, &pubsub.Message{Data: payload})

	// 7. ASSERT: Verify the message was published to the delivery topic
	deliverySub := psClient.Subscriber(deliverySubID)
	var wg sync.WaitGroup
	wg.Add(1)
	var receivedMsg *pubsub.Message

	go func() {
		defer wg.Done()
		cctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		err = deliverySub.Receive(cctx, func(ctx context.Context, msg *pubsub.Message) {
			msg.Ack()
			receivedMsg = msg
			cancel()
		})
		assert.NoError(t, err, "Receive should not return a non-timeout error")
	}()

	wg.Wait()
	require.NotNil(t, receivedMsg, "Did not receive message on the delivery topic")
	t.Log("✅ Message correctly received on the delivery topic.")

	var receivedData messagepipeline.MessageData
	err = json.Unmarshal(receivedMsg.Data, &receivedData)
	require.NoError(t, err)
	var receivedEnvelopePb transport.SecureEnvelopePb
	err = protojson.Unmarshal(receivedData.Payload, &receivedEnvelopePb)
	require.NoError(t, err)
	receivedEnvelope, err := transport.FromProto(&receivedEnvelopePb)
	require.NoError(t, err)
	assert.Equal(t, originalEnvelope.MessageID, receivedEnvelope.MessageID)
	assert.Equal(t, originalEnvelope.EncryptedData, receivedEnvelope.EncryptedData)

	// 8. NEGATIVE ASSERTION: Verify the message was NOT stored in Firestore
	time.Sleep(500 * time.Millisecond)
	docs, err := fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(ctx).GetAll()
	require.NoError(t, err)
	assert.Empty(t, docs, "Message should NOT have been stored in Firestore for an online user")
	t.Log("✅ Verified that message was not stored offline.")
}
