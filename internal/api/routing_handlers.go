package api

import (
	"io"
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
)

// API holds the dependencies for the HTTP handlers, including the JWT secret.
type API struct {
	producer  routing.IngestionProducer
	store     routing.MessageStore
	logger    zerolog.Logger
	jwtSecret string
}

// NewAPI creates a new API handler with the necessary dependencies.
func NewAPI(producer routing.IngestionProducer, store routing.MessageStore, logger zerolog.Logger, jwtSecret string) *API {
	return &API{
		producer:  producer,
		store:     store,
		logger:    logger,
		jwtSecret: jwtSecret,
	}
}

// SendHandler is now also secured by the JWT middleware.
func (a *API) SendHandler(w http.ResponseWriter, r *http.Request) {
	// --- START: NEW AUTHENTICATION LOGIC ---
	// 1. Get the authenticated user ID from the context set by the middleware.
	authedUserID, ok := GetUserIDFromContext(r.Context())
	if !ok {
		// This should not happen if the middleware is configured correctly.
		http.Error(w, "Internal server error: User ID not in context", http.StatusInternalServerError)
		return
	}
	// --- END: NEW AUTHENTICATION LOGIC ---

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		a.logger.Error().Err(err).Msg("Failed to read request body")
		http.Error(w, "Cannot read request body", http.StatusBadRequest)
		return
	}

	var envelopePB transport.SecureEnvelopePb
	if err := protojson.Unmarshal(bodyBytes, &envelopePB); err != nil {
		a.logger.Error().Err(err).Str("raw_body", string(bodyBytes)).Msg("Failed to decode request body using protojson")
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	envelope, err := transport.FromProto(&envelopePB) //
	if err != nil {
		http.Error(w, "Invalid envelope data: "+err.Error(), http.StatusBadRequest)
		return
	}

	// --- START: NEW SENDER ENFORCEMENT ---
	// 2. Create the sender URN from the authenticated user's ID.
	senderURN, err := urn.New(urn.SecureMessaging, urn.EntityTypeUser, authedUserID) //
	if err != nil {
		// This would indicate a bug in our own code, as the user ID should be valid.
		a.logger.Error().Err(err).Str("user_id", authedUserID).Msg("Failed to create sender URN from authenticated user")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// 3. OVERWRITE the SenderID on the envelope. We no longer trust the client's value.
	// This guarantees the sender is who they say they are and that the URN is valid.
	envelope.SenderID = senderURN
	// --- END: NEW SENDER ENFORCEMENT ---

	if err := a.producer.Publish(r.Context(), envelope); err != nil {
		a.logger.Error().Err(err).Msg("Failed to publish message to ingestion pipeline")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// GetMessagesHandler retrieves stored messages for a user identified by the JWT.
func (a *API) GetMessagesHandler(w http.ResponseWriter, r *http.Request) {
	userID, ok := GetUserIDFromContext(r.Context())
	if !ok {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	recipientURN, err := urn.New(urn.SecureMessaging, urn.EntityTypeUser, userID)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	logger := a.logger.With().Str("recipient_id", recipientURN.String()).Logger()
	// 1. Retrieve messages using the internal, idiomatic type.
	idiomaticEnvelopes, err := a.store.RetrieveMessages(r.Context(), recipientURN)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to retrieve messages from store")
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if len(idiomaticEnvelopes) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	//2. Convert the slice of idiomatic types to a slice of Protobuf types.
	protoEnvelopes := make([]*transport.SecureEnvelopePb, len(idiomaticEnvelopes))
	for i, env := range idiomaticEnvelopes {
		protoEnvelopes[i] = transport.ToProto(env)
	}

	response := &transport.SecureEnvelopeListPb{
		Envelopes: protoEnvelopes,
	}

	jsonData, err := protojson.Marshal(response)
	if err != nil {
		http.Error(w, "Failed to marshal protobuf to JSON", http.StatusInternalServerError)
		return
	}

	// 3. Set the Content-Type and write the JSON data to the response.
	w.Header().Set("Content-Type", "application/json")
	w.Write(jsonData)
}
