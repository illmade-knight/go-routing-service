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
	authedUserURN, _ := urn.Parse("urn:sm:user:user-alice")
	recipientURN, _ := urn.Parse("urn:sm:user:user-bob")
	spoofedSenderURN, _ := urn.Parse("urn:sm:user:user-eve")

	envelopeWithSpoofedSender := transport.SecureEnvelope{
		SenderID:    spoofedSenderURN,
		RecipientID: recipientURN,
	}
	envelopeProto := transport.ToProto(&envelopeWithSpoofedSender)
	validBodyBytes, err := protojson.Marshal(envelopeProto)
	require.NoError(t, err)

	t.Run("Success and SenderID is overwritten", func(t *testing.T) {
		// Arrange
		producer := new(mockIngestionProducer)
		apiHandler := api.NewAPI(producer, nil, zerolog.Nop())

		var capturedEnvelope *transport.SecureEnvelope
		producer.On("Publish", mock.Anything, mock.AnythingOfType("*transport.SecureEnvelope")).
			Run(func(args mock.Arguments) {
				capturedEnvelope = args.Get(1).(*transport.SecureEnvelope)
			}).
			Return(nil)

		req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(validBodyBytes))
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.SendHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusAccepted, rr.Code)
		producer.AssertExpectations(t)
		require.NotNil(t, capturedEnvelope)
		assert.Equal(t, authedUserURN, capturedEnvelope.SenderID, "SenderID should be overwritten")
		assert.Equal(t, recipientURN, capturedEnvelope.RecipientID, "RecipientID should be preserved")
	})

	t.Run("Failure - Malformed JSON body", func(t *testing.T) {
		// Arrange
		producer := new(mockIngestionProducer)
		apiHandler := api.NewAPI(producer, nil, zerolog.Nop())

		invalidJSON := []byte(`{"recipientId": "urn:sm:user:user-bob",`) // Malformed JSON
		req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(invalidJSON))
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.SendHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		var errResp response.APIError
		err := json.Unmarshal(rr.Body.Bytes(), &errResp)
		require.NoError(t, err)
		assert.Contains(t, errResp.Error, "Invalid request body")
		producer.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
	})

	t.Run("Failure - Producer fails to publish", func(t *testing.T) {
		// Arrange
		producer := new(mockIngestionProducer)
		apiHandler := api.NewAPI(producer, nil, zerolog.Nop())

		producer.On("Publish", mock.Anything, mock.AnythingOfType("*transport.SecureEnvelope")).Return(assert.AnError)

		req := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(validBodyBytes))
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.SendHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusInternalServerError, rr.Code)
		producer.AssertExpectations(t)
	})
}

func TestGetMessagesHandler(t *testing.T) {
	authedUserURN, _ := urn.Parse("urn:sm:user:user-bob")
	testMessages := []*transport.SecureEnvelope{
		{MessageID: "msg-1", RecipientID: authedUserURN},
		{MessageID: "msg-2", RecipientID: authedUserURN},
	}

	t.Run("Success - Messages Found", func(t *testing.T) {
		// Arrange
		store := new(mockMessageStore)
		apiHandler := api.NewAPI(nil, store, zerolog.Nop())
		store.On("RetrieveMessages", mock.Anything, authedUserURN).Return(testMessages, nil)
		// THE FIRST FIX: Add the missing expectation for the background deletion call.
		store.On("DeleteMessages", mock.Anything, authedUserURN, []string{"msg-1", "msg-2"}).Return(nil)

		req := httptest.NewRequest(http.MethodGet, "/messages", nil)
		ctx := middleware.ContextWithUserID(context.Background(), authedUserURN.EntityID())
		req = req.WithContext(ctx)
		rr := httptest.NewRecorder()

		// Act
		apiHandler.GetMessagesHandler(rr, req)

		// Assert
		assert.Equal(t, http.StatusOK, rr.Code)
		var responseList transport.SecureEnvelopeListPb
		err := protojson.Unmarshal(rr.Body.Bytes(), &responseList)
		require.NoError(t, err)
		assert.Len(t, responseList.Envelopes, 2)

		// THE SECOND FIX: Wait for the background task to complete before finishing the test.
		apiHandler.Wait()
		store.AssertExpectations(t)
	})

	t.Run("Success - No Messages Found", func(t *testing.T) {
		// Arrange
		store := new(mockMessageStore)
		apiHandler := api.NewAPI(nil, store, zerolog.Nop())
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
		apiHandler := api.NewAPI(nil, store, zerolog.Nop())
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
		assert.Equal(t, "Internal server error", errResp.Error)
		store.AssertExpectations(t)
	})
}
