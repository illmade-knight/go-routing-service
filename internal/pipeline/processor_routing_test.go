package pipeline_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Mocks using testify/mock ---

type mockFetcher[K comparable, V any] struct {
	mock.Mock
}

func (m *mockFetcher[K, V]) Fetch(ctx context.Context, key K) (V, error) {
	args := m.Called(ctx, key)
	var result V
	if val, ok := args.Get(0).(V); ok {
		result = val
	}
	return result, args.Error(1)
}

func (m *mockFetcher[K, V]) Close() error {
	args := m.Called()
	return args.Error(0)
}

type mockDeliveryProducer struct {
	mock.Mock
}

func (m *mockDeliveryProducer) Publish(ctx context.Context, topicID string, data *transport.SecureEnvelope) error {
	args := m.Called(ctx, topicID, data)
	return args.Error(0)
}

type mockPushNotifier struct {
	mock.Mock
}

func (m *mockPushNotifier) Notify(ctx context.Context, tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	args := m.Called(ctx, tokens, envelope)
	return args.Error(0)
}

type mockMessageStore struct {
	mock.Mock
}

// REFACTOR: Implement the new routing.MessageStore interface.
func (m *mockMessageStore) StoreMessages(ctx context.Context, recipient urn.URN, envelopes []*transport.SecureEnvelope) error {
	args := m.Called(ctx, recipient, envelopes)
	return args.Error(0)
}

func (m *mockMessageStore) RetrieveMessages(ctx context.Context, recipient urn.URN) ([]*transport.SecureEnvelope, error) {
	args := m.Called(ctx, recipient)
	// Type assertion to handle the slice of pointers.
	var result []*transport.SecureEnvelope
	if val, ok := args.Get(0).([]*transport.SecureEnvelope); ok {
		result = val
	}
	return result, args.Error(1)
}

func (m *mockMessageStore) DeleteMessages(ctx context.Context, recipient urn.URN, messageIDs []string) error {
	args := m.Called(ctx, recipient, messageIDs)
	return args.Error(0)
}

// --- Test Suite ---

func TestRoutingProcessor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	nopLogger := zerolog.Nop()
	// REFACTOR: Use a valid URN for the test envelope.
	testURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	testEnvelope := &transport.SecureEnvelope{
		RecipientID: testURN,
	}
	testMessage := messagepipeline.Message{}

	t.Run("Happy Path - User is Online", func(t *testing.T) {
		// Arrange
		// REFACTOR: Mocks are now parameterized with the urn.URN type.
		presenceCache := new(mockFetcher[urn.URN, routing.ConnectionInfo])
		deliveryProducer := new(mockDeliveryProducer)
		// These mocks are not used in this path.
		deviceTokenFetcher := new(mockFetcher[urn.URN, []routing.DeviceToken])
		pushNotifier := new(mockPushNotifier)
		messageStore := new(mockMessageStore)

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{ServerInstanceID: "pod-123"}, nil)
		deliveryProducer.On("Publish", mock.Anything, "delivery-pod-123", testEnvelope).Return(nil)

		processor := pipeline.NewRoutingProcessor(presenceCache, deviceTokenFetcher, deliveryProducer, pushNotifier, messageStore, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.NoError(t, err)
		presenceCache.AssertExpectations(t)
		deliveryProducer.AssertExpectations(t)
	})

	t.Run("Happy Path - User is Offline with Device Tokens", func(t *testing.T) {
		// Arrange
		presenceCache := new(mockFetcher[urn.URN, routing.ConnectionInfo])
		deviceTokenFetcher := new(mockFetcher[urn.URN, []routing.DeviceToken])
		messageStore := new(mockMessageStore)
		pushNotifier := new(mockPushNotifier)
		deliveryProducer := new(mockDeliveryProducer) // Not used
		deviceTokens := []routing.DeviceToken{{Token: "device-abc"}}

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{}, errors.New("not found"))
		// REFACTOR: Expect a call to StoreMessages with the correct arguments.
		messageStore.On("StoreMessages", mock.Anything, testURN, []*transport.SecureEnvelope{testEnvelope}).Return(nil)
		deviceTokenFetcher.On("Fetch", mock.Anything, testURN).Return(deviceTokens, nil)
		pushNotifier.On("Notify", mock.Anything, deviceTokens, testEnvelope).Return(nil)

		processor := pipeline.NewRoutingProcessor(presenceCache, deviceTokenFetcher, deliveryProducer, pushNotifier, messageStore, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.NoError(t, err)
		mock.AssertExpectationsForObjects(t, presenceCache, messageStore, deviceTokenFetcher, pushNotifier)
	})

	t.Run("Offline - No Device Tokens Found", func(t *testing.T) {
		// Arrange
		presenceCache := new(mockFetcher[urn.URN, routing.ConnectionInfo])
		deviceTokenFetcher := new(mockFetcher[urn.URN, []routing.DeviceToken])
		messageStore := new(mockMessageStore)
		pushNotifier := new(mockPushNotifier) // Not called

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{}, errors.New("not found"))
		messageStore.On("StoreMessages", mock.Anything, testURN, []*transport.SecureEnvelope{testEnvelope}).Return(nil)
		deviceTokenFetcher.On("Fetch", mock.Anything, testURN).Return([]routing.DeviceToken(nil), errors.New("no tokens"))

		processor := pipeline.NewRoutingProcessor(presenceCache, deviceTokenFetcher, nil, pushNotifier, messageStore, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.NoError(t, err, "Failing to find tokens should not be a processing error")
		mock.AssertExpectationsForObjects(t, presenceCache, messageStore, deviceTokenFetcher)
		pushNotifier.AssertNotCalled(t, "Notify", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("Offline - Message Store Fails", func(t *testing.T) {
		// Arrange
		presenceCache := new(mockFetcher[urn.URN, routing.ConnectionInfo])
		messageStore := new(mockMessageStore)
		expectedErr := "db is down"

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{}, errors.New("not found"))
		messageStore.On("StoreMessages", mock.Anything, testURN, []*transport.SecureEnvelope{testEnvelope}).Return(errors.New(expectedErr))

		processor := pipeline.NewRoutingProcessor(presenceCache, nil, nil, nil, messageStore, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.Error(t, err)
		require.ErrorContains(t, err, expectedErr)
		mock.AssertExpectationsForObjects(t, presenceCache, messageStore)
	})
}
