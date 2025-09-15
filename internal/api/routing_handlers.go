// REFACTOR: This file is updated to use the canonical transport.SecureEnvelope
// from the external repository, which now includes urn.URN fields. This
// simplifies the handler logic significantly.

package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
)

// API holds the dependencies for the HTTP handlers.
type API struct {
	producer routing.IngestionProducer
	store    routing.MessageStore
	logger   zerolog.Logger
}

// NewAPI creates a new API handler with the necessary dependencies.
func NewAPI(producer routing.IngestionProducer, store routing.MessageStore, logger zerolog.Logger) *API {
	return &API{
		producer: producer,
		store:    store,
		logger:   logger,
	}
}

// SendHandler handles the ingestion of new messages. It decodes the envelope
// and publishes it to the central ingestion pipeline.
func (a *API) SendHandler(w http.ResponseWriter, r *http.Request) {
	var envelope transport.SecureEnvelope
	err := json.NewDecoder(r.Body).Decode(&envelope)
	if err != nil {
		a.logger.Warn().Err(err).Msg("Failed to decode secure envelope")
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Because the canonical SecureEnvelope now has a urn.URN RecipientID field,
	// the custom UnmarshalJSON in the urn package handles backward
	// compatibility for us. We just need to validate that the result is valid.
	if envelope.RecipientID.IsZero() {
		a.logger.Warn().Msg("Recipient ID is missing or invalid")
		http.Error(w, "Recipient ID is missing or invalid", http.StatusBadRequest)
		return
	}

	logger := a.logger.With().Str("recipient_id", envelope.RecipientID.String()).Logger()

	err = a.producer.Publish(r.Context(), &envelope)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to publish message to ingestion topic")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	logger.Info().Msg("Message accepted for ingestion")
}

// GetMessagesHandler retrieves and clears any stored offline messages for a user.
func (a *API) GetMessagesHandler(w http.ResponseWriter, r *http.Request) {
	recipientHeader := r.Header.Get("X-User-ID")
	if recipientHeader == "" {
		a.logger.Warn().Msg("Missing X-User-ID header")
		http.Error(w, "Missing X-User-ID header", http.StatusBadRequest)
		return
	}

	// This endpoint is specifically for users retrieving their messages. We can
	// robustly determine the user's URN by re-using the UnmarshalJSON logic
	// on the header value. This correctly handles both legacy userIDs and full URNs.
	var recipientURN urn.URN
	// We wrap the header in quotes to make it a valid JSON string for unmarshaling.
	err := json.Unmarshal([]byte(`"`+recipientHeader+`"`), &recipientURN)
	if err != nil || recipientURN.IsZero() || recipientURN.EntityType() != urn.EntityTypeUser {
		a.logger.Warn().Err(err).Str("header_value", recipientHeader).Msg("Invalid or non-user X-User-ID format")
		http.Error(w, "Invalid X-User-ID format", http.StatusBadRequest)
		return
	}

	logger := a.logger.With().Str("recipient_id", recipientURN.String()).Logger()

	envelopes, err := a.store.RetrieveMessages(r.Context(), recipientURN)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to retrieve messages from store")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if len(envelopes) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	err = json.NewEncoder(w).Encode(envelopes)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to encode messages to response")
		return
	}

	// Asynchronously delete the messages now that they have been delivered.
	go func() {
		messageIDs := make([]string, 0, len(envelopes))
		for _, e := range envelopes {
			if e != nil {
				messageIDs = append(messageIDs, e.MessageID)
			}
		}

		// Only proceed if there are actual message IDs to delete.
		if len(messageIDs) == 0 {
			return
		}

		err := a.store.DeleteMessages(context.Background(), recipientURN, messageIDs)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to delete delivered messages from store")
		} else {
			logger.Info().Int("count", len(messageIDs)).Msg("Successfully deleted delivered messages")
		}
	}()
}
