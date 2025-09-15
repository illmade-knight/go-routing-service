package pubsub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"
	ps "github.com/illmade-knight/go-routing-service/internal/platform/pubsub" // Aliased import
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestProducer_Publish(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	// Arrange: Set up the v2 pstest in-memory server
	srv := pstest.NewServer()

	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	// Arrange: Create v2 clients
	const projectID = "test-project"
	const topicID = "ingestion-topic"
	const subID = "ingestion-sub"

	client, err := pubsub.NewClient(ctx, projectID, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	// REFACTOR: Use the main client to get the admin clients, which is the
	// correct and idiomatic approach.
	topicAdminClient := client.TopicAdminClient
	subAdminClient := client.SubscriptionAdminClient

	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	_, err = topicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
	require.NoError(t, err)

	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	_, err = subAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  subName,
		Topic: topicName,
	})
	require.NoError(t, err)

	// Arrange: Create the producer to be tested
	publisher := client.Publisher(topicID)
	producer := ps.NewProducer(publisher)

	// Arrange: Create a valid URN-based SecureEnvelope for the test.
	senderURN, err := urn.Parse("urn:sm:user:user-alice")
	require.NoError(t, err)
	recipientURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	testEnvelope := &transport.SecureEnvelope{
		SenderID:              senderURN,
		RecipientID:           recipientURN,
		EncryptedData:         []byte("encrypted-payload"),
		EncryptedSymmetricKey: []byte("encrypted-key"),
		Signature:             []byte("signature"),
	}

	// Act: Publish the message using our producer
	err = producer.Publish(ctx, testEnvelope)
	require.NoError(t, err)

	// Assert: Verify the message was received by the in-memory server
	var wg sync.WaitGroup
	wg.Add(1)
	var receivedMsg *pubsub.Message

	sub := client.Subscriber(subID)
	// This goroutine will receive one message and then stop
	go func() {
		defer wg.Done()
		receiveCtx, cancelReceive := context.WithCancel(ctx)
		defer cancelReceive()

		err := sub.Receive(receiveCtx, func(ctx context.Context, msg *pubsub.Message) {
			msg.Ack()
			receivedMsg = msg
			cancelReceive() // Stop receiving after the first message
		})
		if err != nil && err != context.Canceled {
			t.Errorf("Receive returned an unexpected error: %v", err)
		}
	}()

	wg.Wait() // Wait for the receiver goroutine to finish

	require.NotNil(t, receivedMsg, "Did not receive a message from the subscription")

	var receivedEnvelope transport.SecureEnvelope
	err = json.Unmarshal(receivedMsg.Data, &receivedEnvelope)
	require.NoError(t, err)

	assert.Equal(t, testEnvelope.RecipientID, receivedEnvelope.RecipientID)
	assert.Equal(t, testEnvelope.SenderID, receivedEnvelope.SenderID)
	assert.Equal(t, testEnvelope.EncryptedData, receivedEnvelope.EncryptedData)
	assert.Equal(t, testEnvelope.Signature, receivedEnvelope.Signature)
}
