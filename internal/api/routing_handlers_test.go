package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-microservice-base/pkg/response"
	"github.com/illmade-knight/go-routing-service/internal/api"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// --- Mocks ---
type mockIngestionProducer struct {
	mock.Mock
}

func (m *mockIngestionProducer) Publish(ctx context.Context, envelope *transport.SecureEnvelope) error {
	args := m.Called(ctx, envelope)
	return args.Error(0)
}

type mockMessageStore struct {
	mock.Mock
}

func (m *mockMessageStore) StoreMessages(ctx context.Context, r urn.URN, e []*transport.SecureEnvelope) error {
	return m.Called(ctx, r, e).Error(0)
}
func (m *mockMessageStore) RetrieveMessages(ctx context.Context, r urn.URN) ([]*transport.SecureEnvelope, error) {
	args := m.Called(ctx, r)
	var envelopes []*transport.SecureEnvelope
	if args.Get(0) != nil {
		envelopes = args.Get(0).([]*transport.SecureEnvelope)
	}
	return envelopes, args.Error(1)
}
func (m *mockMessageStore) DeleteMessages(ctx context.Context, r urn.URN, ids []string) error {
	return m.Called(ctx, r, ids).Error(0)
}

// --- Tests ---

func TestSendHandler(t *testing.T) {
	authedUserURN, err := urn.Parse("urn:sm:user:user-alice") // The real authenticated user
	require.NoError(t, err)

	recipientURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	// This envelope has a FAKE sender. We will test that it gets overwritten.
	spoofedSenderURN, err := urn.Parse("urn:sm:user:user-eve")
	require.NoError(t, err)

	envelopeWithSpoofedSender := transport.SecureEnvelope{
		SenderID:    spoofedSenderURN,
		RecipientID: recipientURN,
	}

	// Correctly marshal the idiomatic struct to its Protobuf representation first
	envelopeProto := transport.ToProto(&envelopeWithSpoofedSender)
	bodyBytes, err := protojson.Marshal(envelopeProto)
	require.NoError(t, err)

	t.Run("Success and SenderID is overwritten", func(t *testing.T) {
		// Arrange
		producer := new(mockIngestionProducer)
		store := new(mockMessageStore)
		apiHandler := api.NewAPI(producer, store, zerolog.Nop())

		// We use a capture to inspect the argument passed to Publish.
		var capturedEnvelope *transport.SecureEnvelope
		producer.On("Publish", mock.Anything, mock.AnythingOfType("*transport.SecureEnvelope")).
			Run(func(args mock.Arguments) {
				capturedEnvelope = args.Get(1).(*transport.SecureEnvelope)
			}).
			Return(nil)

		req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(bodyBytes))
		// Simulate authentication for "user-alice"
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.SendHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusAccepted, rr.Code)
		producer.AssertExpectations(t)
		require.NotNil(t, capturedEnvelope, "Producer.Publish was not called")

		// CRITICAL: Assert that the SenderID was overwritten with the authenticated user's ID.
		assert.Equal(t, authedUserURN, capturedEnvelope.SenderID, "SenderID should be overwritten with the ID from the JWT")
		assert.Equal(t, recipientURN, capturedEnvelope.RecipientID, "RecipientID should be preserved")
	})
}

func TestGetMessagesHandler(t *testing.T) {
	authedUserURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	testMessages := []*transport.SecureEnvelope{
		{MessageID: "msg-1", RecipientID: authedUserURN},
		{MessageID: "msg-2", RecipientID: authedUserURN},
	}

	t.Run("Success - Messages Found", func(t *testing.T) {
		// Arrange
		store := new(mockMessageStore)
		producer := new(mockIngestionProducer)
		apiHandler := api.NewAPI(producer, store, zerolog.Nop())

		store.On("RetrieveMessages", mock.Anything, authedUserURN).Return(testMessages, nil)
		// Note: The actual implementation deletes messages in a goroutine,
		// so we don't test the delete call in this unit test. That is better
		// suited for an E2E test.

		req := httptest.NewRequest(http.MethodGet, "/messages", nil)
		// Simulate authentication
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.GetMessagesHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusOK, rr.Code)
		store.AssertExpectations(t)

		var responseList transport.SecureEnvelopeListPb
		err = protojson.Unmarshal(rr.Body.Bytes(), &responseList)
		require.NoError(t, err)
		assert.Len(t, responseList.Envelopes, 2)
	})

	t.Run("Success - No Messages Found", func(t *testing.T) {
		// Arrange
		store := new(mockMessageStore)
		producer := new(mockIngestionProducer)
		apiHandler := api.NewAPI(producer, store, zerolog.Nop())

		store.On("RetrieveMessages", mock.Anything, authedUserURN).Return([]*transport.SecureEnvelope{}, nil)

		req := httptest.NewRequest(http.MethodGet, "/messages", nil)
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.GetMessagesHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusNoContent, rr.Code)
		store.AssertExpectations(t)
	})

	t.Run("Failure - Store fails", func(t *testing.T) {
		// Arrange
		store := new(mockMessageStore)
		producer := new(mockIngestionProducer)
		apiHandler := api.NewAPI(producer, store, zerolog.Nop())

		store.On("RetrieveMessages", mock.Anything, authedUserURN).Return(nil, assert.AnError)

		req := httptest.NewRequest(http.MethodGet, "/messages", nil)
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.GetMessagesHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusInternalServerError, rr.Code)
		var errResp response.APIError
		err := json.Unmarshal(rr.Body.Bytes(), &errResp)
		require.NoError(t, err)

		// THE FIX: Assert against the actual error message returned by the API.
		assert.Equal(t, "Internal server error", errResp.Error)
		store.AssertExpectations(t)
	})
}
