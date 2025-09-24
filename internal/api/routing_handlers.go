package api

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-microservice-base/pkg/response"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/encoding/protojson"
)

// API holds the dependencies for the stateless HTTP handlers.
// It no longer manages WebSocket connections or JWT secrets.
type API struct {
	producer routing.IngestionProducer
	store    routing.MessageStore
	logger   zerolog.Logger
	wg       sync.WaitGroup
}

// NewAPI creates a new, stateless API handler.
func NewAPI(producer routing.IngestionProducer, store routing.MessageStore, logger zerolog.Logger) *API {
	return &API{
		producer: producer,
		store:    store,
		logger:   logger,
	}
}

// Wait will block until all background tasks are complete.
func (a *API) Wait() {
	a.wg.Wait()
}

// SendHandler ingests a message, enforces the sender's identity, and publishes it.
func (a *API) SendHandler(w http.ResponseWriter, r *http.Request) {
	authedUserID, ok := middleware.GetUserIDFromContext(r.Context())
	if !ok {
		response.WriteJSONError(w, http.StatusInternalServerError, "Internal server error: User ID not in context")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		response.WriteJSONError(w, http.StatusBadRequest, "Cannot read request body")
		return
	}

	var envelopePB transport.SecureEnvelopePb
	if err := protojson.Unmarshal(bodyBytes, &envelopePB); err != nil {
		response.WriteJSONError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	envelope, err := transport.FromProto(&envelopePB)
	if err != nil {
		response.WriteJSONError(w, http.StatusBadRequest, "Invalid envelope data: "+err.Error())
		return
	}

	// SECURITY FIX: Overwrite the SenderID with the identity from the validated JWT.
	// This prevents sender spoofing.
	senderURN, err := urn.New(urn.SecureMessaging, urn.EntityTypeUser, authedUserID)
	if err != nil {
		a.logger.Error().Err(err).Str("user_id", authedUserID).Msg("Failed to create sender URN from authenticated user")
		response.WriteJSONError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	envelope.SenderID = senderURN

	if err := a.producer.Publish(r.Context(), envelope); err != nil {
		a.logger.Error().Err(err).Msg("Failed to publish message to ingestion pipeline")
		response.WriteJSONError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// GetMessagesHandler retrieves stored messages for the authenticated user and then deletes them.
func (a *API) GetMessagesHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserIDFromContext(r.Context())
	if !ok {
		response.WriteJSONError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	recipientURN, err := urn.New(urn.SecureMessaging, urn.EntityTypeUser, userID)
	if err != nil {
		response.WriteJSONError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	logger := a.logger.With().Str("recipient_id", recipientURN.String()).Logger()
	idiomaticEnvelopes, err := a.store.RetrieveMessages(r.Context(), recipientURN)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to retrieve messages from store")
		response.WriteJSONError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if len(idiomaticEnvelopes) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 1. Prepare the response payload first.
	protoEnvelopes := make([]*transport.SecureEnvelopePb, len(idiomaticEnvelopes))
	messageIDs := make([]string, len(idiomaticEnvelopes))
	for i, env := range idiomaticEnvelopes {
		protoEnvelopes[i] = transport.ToProto(env)
		messageIDs[i] = env.MessageID
	}

	resp := &transport.SecureEnvelopeListPb{Envelopes: protoEnvelopes}
	jsonData, err := protojson.Marshal(resp)
	if err != nil {
		response.WriteJSONError(w, http.StatusInternalServerError, "Failed to marshal protobuf to JSON")
		return
	}

	// 2. Write the response to the client.
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(jsonData); err != nil {
		// If we fail to write the response, don't delete the messages.
		logger.Error().Err(err).Msg("Failed to write messages response to client")
		return
	}

	// 3. After a successful write, trigger deletion in the background.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		// Use a background context as the original request may have already timed out.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.store.DeleteMessages(ctx, recipientURN, messageIDs); err != nil {
			logger.Error().Err(err).Msg("Failed to delete messages from store after retrieval")
		} else {
			logger.Info().Int("count", len(messageIDs)).Msg("Successfully deleted messages after retrieval.")
		}
	}()
}
