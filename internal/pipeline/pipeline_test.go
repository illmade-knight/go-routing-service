package pipeline_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"cloud.google.com/go/pubsub/v2/pstest"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	psadapter "github.com/illmade-knight/go-routing-service/internal/platform/pubsub"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestSerializationFlow ensures that the data serialized by the Pub/Sub producer
// can be correctly deserialized by the pipeline's envelope transformer.
// This test now uses a real in-memory Pub/Sub server to correctly handle the
// asynchronous nature of publishing.
func TestSerializationFlow(t *testing.T) {
	// Arrange
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	// 1. Set up the pstest in-memory server, which behaves like the real Pub/Sub.
	srv := pstest.NewServer()
	t.Cleanup(func() { srv.Close() })
	conn, err := grpc.NewClient(srv.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	const projectID = "test-project"
	const topicID = "serialization-topic"
	const subID = "serialization-sub"
	client, err := pubsub.NewClient(ctx, projectID, option.WithGRPCConn(conn))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	// Create topic and subscription on the in-memory server.
	topicName := fmt.Sprintf("projects/%s/topics/%s", projectID, topicID)
	_, err = client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
	require.NoError(t, err)
	subName := fmt.Sprintf("projects/%s/subscriptions/%s", projectID, subID)
	_, err = client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{Name: subName, Topic: topicName})
	require.NoError(t, err)

	// 2. Create the REAL producer pointing to the in-memory server's topic.
	publisher := client.Publisher(topicID)
	producer := psadapter.NewProducer(publisher)

	senderURN, _ := urn.Parse("urn:sm:user:sender")
	recipientURN, _ := urn.Parse("urn:sm:user:recipient")
	originalEnvelope := &transport.SecureEnvelope{
		MessageID:             "test-id-123",
		SenderID:              senderURN,
		RecipientID:           recipientURN,
		EncryptedData:         []byte("this is some secret data that must not be truncated"),
		EncryptedSymmetricKey: []byte("this is a secret key"),
		Signature:             []byte("this is a signature"),
	}

	// 3. Publish the message. Because we're using a real test server, this will complete without hanging.
	err = producer.Publish(ctx, originalEnvelope)
	require.NoError(t, err, "Producer failed to publish")

	// 4. Receive the message from the subscription to get the raw payload.
	var wg sync.WaitGroup
	wg.Add(1)
	var receivedMsg *pubsub.Message
	sub := client.Subscriber(subID)
	go func() {
		defer wg.Done()
		receiveCtx, cancelReceive := context.WithCancel(ctx)
		defer cancelReceive()
		_ = sub.Receive(receiveCtx, func(ctx context.Context, msg *pubsub.Message) {
			msg.Ack()
			receivedMsg = msg
			cancelReceive() // Stop after one message
		})
	}()
	wg.Wait()
	require.NotNil(t, receivedMsg, "Did not receive message from subscription")

	// 5. Create a pipeline message from the received payload.
	messageFromBus := &messagepipeline.Message{
		MessageData: messagepipeline.MessageData{
			ID:      "test-bus-id",
			Payload: receivedMsg.Data,
		},
	}

	// 6. Use the REAL transformer to deserialize the payload.
	deserializedEnvelope, skipped, err := pipeline.EnvelopeTransformer(ctx, messageFromBus)

	// Assert
	require.NoError(t, err, "EnvelopeTransformer failed to deserialize the producer's payload")
	require.False(t, skipped, "Message should not have been skipped")

	// The ultimate check: verify that the data is identical after the round trip.
	assert.Equal(t, originalEnvelope, deserializedEnvelope, "The envelope after deserialization does not match the original")
	assert.Equal(t, originalEnvelope.EncryptedData, deserializedEnvelope.EncryptedData, "EncryptedData was corrupted during serialization")
}
