// REFACTOR: This file is updated to add strict validation after unmarshaling.
// This ensures that no malformed or incomplete envelopes can enter the
// processing pipeline, making the service more robust.

package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
)

// EnvelopeTransformer is a dataflow Transformer stage that safely unmarshals
// and validates a raw message payload into a structured transport.SecureEnvelope.
func EnvelopeTransformer(ctx context.Context, msg *messagepipeline.Message) (*transport.SecureEnvelope, bool, error) {
	var envelope transport.SecureEnvelope

	err := json.Unmarshal(msg.Payload, &envelope)
	if err != nil {
		// If unmarshaling fails, we skip the message and return an error
		// so the StreamingService can Nack it.
		return nil, true, fmt.Errorf("failed to unmarshal secure envelope from message %s: %w", msg.ID, err)
	}

	// REFACTOR: Add a strict validation step. A valid envelope MUST have both
	// a sender and a recipient. This prevents malformed data, like that from
	// the previously broken test, from proceeding.
	if envelope.SenderID.IsZero() || envelope.RecipientID.IsZero() {
		return nil, true, fmt.Errorf("envelope from message %s is invalid: sender or recipient is missing", msg.ID)
	}

	// On success, we pass the structured envelope to the next stage (the Processor)
	// and indicate that the message should not be skipped.
	return &envelope, false, nil
}
