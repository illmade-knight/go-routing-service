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

type mockPresenceCache[K comparable, V any] struct {
	mock.Mock
}

func (m *mockPresenceCache[K, V]) Set(ctx context.Context, key K, value V) error {
	args := m.Called(ctx, key, value)
	return args.Error(0)
}

func (m *mockPresenceCache[K, V]) Fetch(ctx context.Context, key K) (V, error) {
	args := m.Called(ctx, key)
	var result V
	if val, ok := args.Get(0).(V); ok {
		result = val
	}
	return result, args.Error(1)
}

func (m *mockPresenceCache[K, V]) Delete(ctx context.Context, key K) error {
	args := m.Called(ctx, key)
	return args.Error(0)
}

func (m *mockPresenceCache[K, V]) Close() error {
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
	notified chan struct{}
}

func (m *mockPushNotifier) Notify(ctx context.Context, tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	args := m.Called(ctx, tokens, envelope)
	return args.Error(0)
}

type mockMessageStore struct {
	mock.Mock
}

func (m *mockMessageStore) StoreMessages(ctx context.Context, recipient urn.URN, envelopes []*transport.SecureEnvelope) error {
	args := m.Called(ctx, recipient, envelopes)
	return args.Error(0)
}

func (m *mockMessageStore) RetrieveMessages(ctx context.Context, recipient urn.URN) ([]*transport.SecureEnvelope, error) {
	args := m.Called(ctx, recipient)
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
	testURN, _ := urn.Parse("urn:sm:user:user-bob")
	testEnvelope := &transport.SecureEnvelope{RecipientID: testURN}
	testMessage := messagepipeline.Message{}
	testConfig := &routing.Config{DeliveryTopicID: "shared-delivery-topic"}

	t.Run("Online User - Publishes to Shared Delivery Topic", func(t *testing.T) {
		// Arrange
		presenceCache := new(mockPresenceCache[urn.URN, routing.ConnectionInfo])
		deliveryProducer := new(mockDeliveryProducer)
		deps := &routing.Dependencies{
			PresenceCache:    presenceCache,
			DeliveryProducer: deliveryProducer,
		}

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{}, nil)
		// CORRECTED: Assert that it publishes to the shared topic from the config.
		deliveryProducer.On("Publish", mock.Anything, testConfig.DeliveryTopicID, testEnvelope).Return(nil)

		processor := pipeline.New(deps, testConfig, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.NoError(t, err)
		deliveryProducer.AssertExpectations(t)
	})

	t.Run("Offline User - Mobile Token - Notifies and Stores", func(t *testing.T) {
		// Arrange
		presenceCache := new(mockPresenceCache[urn.URN, routing.ConnectionInfo])
		deviceTokenFetcher := new(mockFetcher[urn.URN, []routing.DeviceToken])
		messageStore := new(mockMessageStore)
		pushNotifier := new(mockPushNotifier)
		deps := &routing.Dependencies{
			PresenceCache:      presenceCache,
			DeviceTokenFetcher: deviceTokenFetcher,
			MessageStore:       messageStore,
			PushNotifier:       pushNotifier,
		}
		mobileTokens := []routing.DeviceToken{{Token: "ios-device-abc", Platform: "ios"}}

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{}, errors.New("not found"))
		deviceTokenFetcher.On("Fetch", mock.Anything, testURN).Return(mobileTokens, nil)
		pushNotifier.On("Notify", mock.Anything, mobileTokens, testEnvelope).Return(nil)
		messageStore.On("StoreMessages", mock.Anything, testURN, []*transport.SecureEnvelope{testEnvelope}).Return(nil)

		processor := pipeline.New(deps, testConfig, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.NoError(t, err)
		mock.AssertExpectationsForObjects(t, presenceCache, deviceTokenFetcher, pushNotifier, messageStore)
	})

	t.Run("Offline User - Web Token - Publishes to Delivery Bus and Stores", func(t *testing.T) {
		// Arrange
		presenceCache := new(mockPresenceCache[urn.URN, routing.ConnectionInfo])
		deviceTokenFetcher := new(mockFetcher[urn.URN, []routing.DeviceToken])
		deliveryProducer := new(mockDeliveryProducer)
		messageStore := new(mockMessageStore)
		deps := &routing.Dependencies{
			PresenceCache:      presenceCache,
			DeviceTokenFetcher: deviceTokenFetcher,
			DeliveryProducer:   deliveryProducer,
			MessageStore:       messageStore,
			PushNotifier:       new(mockPushNotifier), // Ensure it's not called
		}
		webTokens := []routing.DeviceToken{{Token: "web-subscription-xyz", Platform: "web"}}

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{}, errors.New("not found"))
		deviceTokenFetcher.On("Fetch", mock.Anything, testURN).Return(webTokens, nil)
		deliveryProducer.On("Publish", mock.Anything, testConfig.DeliveryTopicID, testEnvelope).Return(nil)
		messageStore.On("StoreMessages", mock.Anything, testURN, []*transport.SecureEnvelope{testEnvelope}).Return(nil)

		processor := pipeline.New(deps, testConfig, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.NoError(t, err)
		mock.AssertExpectationsForObjects(t, presenceCache, deviceTokenFetcher, deliveryProducer, messageStore)
		deps.PushNotifier.(*mockPushNotifier).AssertNotCalled(t, "Notify", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("Offline User - No Tokens - Stores Message", func(t *testing.T) {
		// Arrange
		presenceCache := new(mockPresenceCache[urn.URN, routing.ConnectionInfo])
		deviceTokenFetcher := new(mockFetcher[urn.URN, []routing.DeviceToken])
		messageStore := new(mockMessageStore)
		deps := &routing.Dependencies{
			PresenceCache:      presenceCache,
			DeviceTokenFetcher: deviceTokenFetcher,
			MessageStore:       messageStore,
		}

		presenceCache.On("Fetch", mock.Anything, testURN).Return(routing.ConnectionInfo{}, errors.New("not found"))
		deviceTokenFetcher.On("Fetch", mock.Anything, testURN).Return(nil, errors.New("not found"))
		messageStore.On("StoreMessages", mock.Anything, testURN, []*transport.SecureEnvelope{testEnvelope}).Return(nil)

		processor := pipeline.New(deps, testConfig, nopLogger)

		// Act
		err := processor(ctx, testMessage, testEnvelope)

		// Assert
		require.NoError(t, err)
		mock.AssertExpectationsForObjects(t, presenceCache, deviceTokenFetcher, messageStore)
	})
}
