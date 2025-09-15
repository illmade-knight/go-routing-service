package pipeline_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnvelopeTransformer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	// REFACTOR: Use the correct, URN-based SecureEnvelope struct.
	senderURN, err := urn.Parse("urn:sm:user:user-alice")
	require.NoError(t, err)
	recipientURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	validEnvelope := transport.SecureEnvelope{
		SenderID:              senderURN,
		RecipientID:           recipientURN,
		EncryptedData:         []byte("encrypted-payload"),
		EncryptedSymmetricKey: []byte("encrypted-key"),
		Signature:             []byte("signature"),
	}
	validPayload, err := json.Marshal(validEnvelope)
	require.NoError(t, err, "Setup: failed to marshal valid envelope")

	testCases := []struct {
		name                  string
		inputMessage          *messagepipeline.Message
		expectedEnvelope      *transport.SecureEnvelope
		expectedSkip          bool
		expectError           bool
		expectedErrorContains string
	}{
		{
			name: "Happy Path - Valid Envelope",
			inputMessage: &messagepipeline.Message{
				MessageData: messagepipeline.MessageData{
					ID:      "msg-123",
					Payload: validPayload,
				},
			},
			expectedEnvelope:      &validEnvelope,
			expectedSkip:          false,
			expectError:           false,
			expectedErrorContains: "",
		},
		{
			name: "Failure - Malformed JSON Payload",
			inputMessage: &messagepipeline.Message{
				MessageData: messagepipeline.MessageData{
					ID:      "msg-456",
					Payload: []byte("{ not-valid-json }"),
				},
			},
			expectedEnvelope:      nil,
			expectedSkip:          true,
			expectError:           true,
			expectedErrorContains: "failed to unmarshal secure envelope",
		},
		{
			name: "Failure - Empty Payload",
			inputMessage: &messagepipeline.Message{
				MessageData: messagepipeline.MessageData{
					ID:      "msg-789",
					Payload: []byte{},
				},
			},
			expectedEnvelope:      nil,
			expectedSkip:          true,
			expectError:           true,
			expectedErrorContains: "unexpected end of JSON input",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			// REFACTOR: Correctly pass the inputMessage field from the test case struct.
			actualEnvelope, actualSkip, actualErr := pipeline.EnvelopeTransformer(ctx, tc.inputMessage)

			// Assert
			assert.Equal(t, tc.expectedEnvelope, actualEnvelope)
			assert.Equal(t, tc.expectedSkip, actualSkip)

			if tc.expectError {
				require.Error(t, actualErr)
				assert.Contains(t, actualErr.Error(), tc.expectedErrorContains)
			} else {
				assert.NoError(t, actualErr)
			}
		})
	}
}
