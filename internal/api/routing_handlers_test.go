// REFACTOR: This test is updated to correctly handle the asynchronous nature
// of the message deletion logic in GetMessagesHandler by using a sync.WaitGroup.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/illmade-knight/go-routing-service/internal/api"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// --- Mocks using testify/mock ---

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

// --- Test Suites ---

func TestSendHandler(t *testing.T) {
	senderURN, err := urn.New(urn.SecureMessaging, "user", "user-alice")
	require.NoError(t, err)
	recipientURN, err := urn.New(urn.SecureMessaging, "user", "user-bob")
	require.NoError(t, err)

	validEnvelope := transport.SecureEnvelope{
		SenderID:    senderURN,
		RecipientID: recipientURN,
	}
	validBody, err := json.Marshal(validEnvelope)
	require.NoError(t, err, "Setup: failed to marshal valid envelope")

	legacyEnvelope := map[string]string{
		"senderId":    "user-alice",
		"recipientId": "user-bob",
	}
	legacyBody, err := json.Marshal(legacyEnvelope)
	require.NoError(t, err, "Setup: failed to marshal legacy envelope")

	testCases := []struct {
		name               string
		requestBody        []byte
		setupMock          func(producer *mockIngestionProducer)
		expectedStatusCode int
	}{
		{
			name:        "Happy Path - Valid URN Request",
			requestBody: validBody,
			setupMock: func(producer *mockIngestionProducer) {
				producer.On("Publish", mock.Anything, mock.AnythingOfType("*transport.SecureEnvelope")).Return(nil)
			},
			expectedStatusCode: http.StatusAccepted,
		},
		{
			name:        "Happy Path - Legacy UserID Request",
			requestBody: legacyBody,
			setupMock: func(producer *mockIngestionProducer) {
				producer.On("Publish", mock.Anything, mock.AnythingOfType("*transport.SecureEnvelope")).Return(nil)
			},
			expectedStatusCode: http.StatusAccepted,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			producer := new(mockIngestionProducer)
			store := new(mockMessageStore)
			tc.setupMock(producer)

			apiHandler := api.NewAPI(producer, store, zerolog.Nop())
			request := httptest.NewRequest(http.MethodPost, "/send", bytes.NewReader(tc.requestBody))
			responseRecorder := httptest.NewRecorder()

			apiHandler.SendHandler(responseRecorder, request)

			assert.Equal(t, tc.expectedStatusCode, responseRecorder.Code)
			producer.AssertExpectations(t)
		})
	}
}

func TestGetMessagesHandler(t *testing.T) {
	testURN, err := urn.New(urn.SecureMessaging, "user", "user-bob")
	require.NoError(t, err)

	testMessages := []*transport.SecureEnvelope{
		{MessageID: "msg-1", RecipientID: testURN},
		{MessageID: "msg-2", RecipientID: testURN},
	}

	testCases := []struct {
		name               string
		userIDHeader       string
		setupMock          func(store *mockMessageStore, wg *sync.WaitGroup)
		expectedStatusCode int
	}{
		{
			name:         "Happy Path - Messages Found (URN Header)",
			userIDHeader: "urn:sm:user:user-bob",
			setupMock: func(store *mockMessageStore, wg *sync.WaitGroup) {
				store.On("RetrieveMessages", mock.Anything, testURN).Return(testMessages, nil).Once()
				store.On("DeleteMessages", mock.Anything, testURN, []string{"msg-1", "msg-2"}).Return(nil).Run(func(args mock.Arguments) {
					wg.Done() // Signal that the delete was called.
				}).Once()
			},
			expectedStatusCode: http.StatusOK,
		},
		{
			name:         "Happy Path - Messages Found (Legacy Header)",
			userIDHeader: "user-bob",
			setupMock: func(store *mockMessageStore, wg *sync.WaitGroup) {
				store.On("RetrieveMessages", mock.Anything, testURN).Return(testMessages, nil).Once()
				store.On("DeleteMessages", mock.Anything, testURN, []string{"msg-1", "msg-2"}).Return(nil).Run(func(args mock.Arguments) {
					wg.Done()
				}).Once()
			},
			expectedStatusCode: http.StatusOK,
		},
		{
			name:         "Happy Path - No Messages Found",
			userIDHeader: "user-bob",
			setupMock: func(store *mockMessageStore, wg *sync.WaitGroup) {
				store.On("RetrieveMessages", mock.Anything, testURN).Return([]*transport.SecureEnvelope{}, nil).Once()
				// No delete is expected, so wg is not used.
			},
			expectedStatusCode: http.StatusNoContent,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := new(mockMessageStore)
			producer := new(mockIngestionProducer)
			// Use a WaitGroup to handle the asynchronous delete call.
			var wg sync.WaitGroup

			// Add to the WaitGroup only if the test case expects a delete.
			if tc.name == "Happy Path - Messages Found (URN Header)" || tc.name == "Happy Path - Messages Found (Legacy Header)" {
				wg.Add(1)
			}
			tc.setupMock(store, &wg)

			apiHandler := api.NewAPI(producer, store, zerolog.Nop())
			request := httptest.NewRequest(http.MethodGet, "/messages", nil)
			if tc.userIDHeader != "" {
				request.Header.Set("X-User-ID", tc.userIDHeader)
			}
			responseRecorder := httptest.NewRecorder()

			apiHandler.GetMessagesHandler(responseRecorder, request)

			// Block until the async delete call is made, or timeout.
			wg.Wait()

			assert.Equal(t, tc.expectedStatusCode, responseRecorder.Code)
			store.AssertExpectations(t)
		})
	}
}
