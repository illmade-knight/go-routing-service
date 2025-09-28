package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestEnvelopeTransformer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	senderURN, err := urn.Parse("urn:sm:user:user-alice")
	require.NoError(t, err)
	recipientURN, err := urn.Parse("urn:sm:user:user-bob")
	require.NoError(t, err)

	validEnvelope := transport.SecureEnvelope{
		SenderID:    senderURN,
		RecipientID: recipientURN,
	}
	// The transformer expects a Protobuf JSON payload.
	validPayload, err := protojson.Marshal(transport.ToProto(&validEnvelope))
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
			expectedEnvelope: &validEnvelope,
			expectedSkip:     false,
			expectError:      false,
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
			expectedEnvelope: nil,
			expectedSkip:     true,
			expectError:      true,
			// CORRECTED: Assert against the actual error from protojson.
			expectedErrorContains: "failed to unmarshal secure envelope",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			actualEnvelope, actualSkip, actualErr := pipeline.EnvelopeTransformer(ctx, tc.inputMessage)

			// Assert
			// Use assert.EqualValues for comparing structs that might have different pointer identities
			assert.EqualValues(t, tc.expectedEnvelope, actualEnvelope)
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
