//go:build unit

package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// mockMessageConsumer satisfies the MessageConsumer interface for the constructor,
// but is not used in these specific unit tests.
type mockMessageConsumer struct {
	mock.Mock
}

func (m *mockMessageConsumer) Messages() <-chan messagepipeline.Message {
	return make(chan messagepipeline.Message)
}
func (m *mockMessageConsumer) Start(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *mockMessageConsumer) Stop(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}
func (m *mockMessageConsumer) Done() <-chan struct{} {
	return make(chan struct{})
}

// --- Test Fixture ---

type testFixture struct {
	cm             *ConnectionManager
	presenceCache  cache.PresenceCache[urn.URN, routing.ConnectionInfo]
	messageHandler chan []byte
	wsServer       *httptest.Server
	wsClientConn   *websocket.Conn
	userURN        urn.URN
}

func setup(t *testing.T) *testFixture {
	t.Helper()

	// Use the actual in-memory implementation of the presence cache.
	presenceCache := cache.NewInMemoryPresenceCache[urn.URN, routing.ConnectionInfo]()

	consumer := new(mockMessageConsumer)
	consumer.On("Start", mock.Anything).Return(nil)
	consumer.On("Stop", mock.Anything).Return(nil)

	messageHandler := make(chan []byte, 1)

	upgrader := websocket.Upgrader{}
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		go func() {
			defer func() {
				_ = conn.Close()
			}()
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				messageHandler <- msg
			}
		}()
	}))

	wsURL := "ws" + strings.TrimPrefix(wsServer.URL, "http")
	wsClientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = wsClientConn.Close()
		wsServer.Close()
	})

	// REFACTORED: Create a mock auth middleware that simulates a successful login.
	// This aligns the test with the new dependency injection pattern.
	mockAuthMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The connect handler requires a user ID in the context.
			ctx := middleware.ContextWithUserID(r.Context(), "test-user")
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}

	cm, err := NewConnectionManager(":0", mockAuthMiddleware, presenceCache, consumer, zerolog.Nop())
	require.NoError(t, err)

	userURN, err := urn.Parse("urn:sm:user:test-user")
	require.NoError(t, err)

	return &testFixture{
		cm:             cm,
		presenceCache:  presenceCache,
		messageHandler: messageHandler,
		wsServer:       wsServer,
		wsClientConn:   wsClientConn,
		userURN:        userURN,
	}
}

// --- Tests ---

func TestConnectionManager_AddAndGet(t *testing.T) {
	fx := setup(t)

	// Act: Add the connection
	fx.cm.add(fx.userURN, fx.wsClientConn)

	// Assert: Check the connection manager's internal state
	conn, ok := fx.cm.Get(fx.userURN)
	assert.True(t, ok, "Expected to find connection for user")
	assert.Equal(t, fx.wsClientConn, conn, "Returned connection is not the one that was added")

	// Assert: Check the state of the real presence cache
	info, err := fx.presenceCache.Fetch(context.Background(), fx.userURN)
	require.NoError(t, err)
	assert.Equal(t, fx.cm.instanceID, info.ServerInstanceID)
}

func TestConnectionManager_Remove(t *testing.T) {
	fx := setup(t)

	// Arrange: Add a connection first
	fx.cm.add(fx.userURN, fx.wsClientConn)

	// Act: Remove the connection
	fx.cm.Remove(fx.userURN)

	// Assert: Check the connection manager's internal state
	_, ok := fx.cm.Get(fx.userURN)
	assert.False(t, ok, "Expected connection to be removed")

	// Assert: Check the state of the real presence cache
	_, err := fx.presenceCache.Fetch(context.Background(), fx.userURN)
	assert.Error(t, err, "Expected an error fetching a deleted user from presence cache")
}

func TestConnectionManager_DeliveryProcessor(t *testing.T) {
	t.Run("Success - delivers message to connected user", func(t *testing.T) {
		fx := setup(t)

		// Arrange: Add the user's connection to the manager
		fx.cm.add(fx.userURN, fx.wsClientConn)
		envelope := &transport.SecureEnvelope{
			MessageID:   "test-msg-123",
			RecipientID: fx.userURN,
		}

		// Act: Process the delivery event
		err := fx.cm.deliveryProcessor(context.Background(), messagepipeline.Message{}, envelope)
		require.NoError(t, err)

		// Assert: Check if the message was received on the client-side
		select {
		case receivedBytes := <-fx.messageHandler:
			var receivedPb transport.SecureEnvelopePb
			err := protojson.Unmarshal(receivedBytes, &receivedPb)
			require.NoError(t, err)
			assert.Equal(t, "test-msg-123", receivedPb.MessageId)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for message to be delivered")
		}
	})

	t.Run("NoOp - user not connected to this instance", func(t *testing.T) {
		fx := setup(t)
		otherUser, _ := urn.Parse("urn:sm:user:other-user")

		envelope := &transport.SecureEnvelope{
			MessageID:   "test-msg-456",
			RecipientID: otherUser,
		}

		// Act
		err := fx.cm.deliveryProcessor(context.Background(), messagepipeline.Message{}, envelope)

		// Assert
		assert.NoError(t, err, "Processing a message for an unconnected user should not be an error")
		assert.Len(t, fx.messageHandler, 0, "No message should have been sent")
	})

	t.Run("Failure - removes connection on write error", func(t *testing.T) {
		fx := setup(t)

		// Arrange: Add the connection, verify it's present, then close it to force a write error
		fx.cm.add(fx.userURN, fx.wsClientConn)
		_, err := fx.presenceCache.Fetch(context.Background(), fx.userURN)
		require.NoError(t, err, "Precondition failed: user should be in presence cache")
		_ = fx.wsClientConn.Close()

		envelope := &transport.SecureEnvelope{
			MessageID:   "test-msg-789",
			RecipientID: fx.userURN,
		}

		// Act: Process the delivery, which should fail and trigger cleanup
		err = fx.cm.deliveryProcessor(context.Background(), messagepipeline.Message{}, envelope)

		// Assert
		assert.Error(t, err, "Expected an error from a failed websocket write")
		_, ok := fx.cm.Get(fx.userURN)
		assert.False(t, ok, "Connection should have been removed after a write failure")

		_, err = fx.presenceCache.Fetch(context.Background(), fx.userURN)
		assert.Error(t, err, "User should have been removed from presence cache after write failure")
	})
}
