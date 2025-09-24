package pubsub_test

import (
	"context"
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
	"google.golang.org/protobuf/encoding/protojson"
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

	senderURN, err := urn.Parse("urn:sm:user:user-alice")
	require.NoError(t, err)
	recipientURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	testEnvelope := &transport.SecureEnvelope{
		SenderID:      senderURN,
		RecipientID:   recipientURN,
		EncryptedData: []byte("encrypted-payload"),
	}

	// Act: Publish the message using our producer
	err = producer.Publish(ctx, testEnvelope)
	require.NoError(t, err)

	// Assert: Verify the message was received by the in-memory server
	var wg sync.WaitGroup
	wg.Add(1)
	var receivedMsg *pubsub.Message

	sub := client.Subscriber(subID)
	go func() {
		defer wg.Done()
		receiveCtx, cancelReceive := context.WithCancel(ctx)
		defer cancelReceive()

		err := sub.Receive(receiveCtx, func(ctx context.Context, msg *pubsub.Message) {
			msg.Ack()
			receivedMsg = msg
			cancelReceive()
		})
		if err != nil && err != context.Canceled {
			t.Errorf("Receive returned an unexpected error: %v", err)
		}
	}()

	wg.Wait()

	require.NotNil(t, receivedMsg, "Did not receive a message from the subscription")

	// CORRECTED: Unmarshal using protojson, which matches the producer.
	var receivedEnvelopePb transport.SecureEnvelopePb
	err = protojson.Unmarshal(receivedMsg.Data, &receivedEnvelopePb)
	require.NoError(t, err)

	receivedEnvelope, err := transport.FromProto(&receivedEnvelopePb)
	require.NoError(t, err)

	assert.Equal(t, testEnvelope.RecipientID, receivedEnvelope.RecipientID)
	assert.Equal(t, testEnvelope.SenderID, receivedEnvelope.SenderID)
	assert.Equal(t, testEnvelope.EncryptedData, receivedEnvelope.EncryptedData)
}
