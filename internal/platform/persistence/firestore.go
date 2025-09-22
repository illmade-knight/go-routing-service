// REFACTOR: This file now uses Firestore Transactions (client.RunTransaction)
// for all batch write operations. This is the correct, modern, non-deprecated
// approach for ensuring synchronous, atomic writes, which is required by the
// service's transactional logic.

package persistence

import (
	"context"
	"fmt"

	"cloud.google.com/go/firestore"
	"github.com/google/uuid"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	usersCollection    = "user-messages"
	messagesCollection = "messages"
)

// FirestoreStore implements the routing.MessageStore interface using Google Cloud Firestore.
type FirestoreStore struct {
	client *firestore.Client
	logger zerolog.Logger
}

// NewFirestoreStore is the constructor for the FirestoreStore.
func NewFirestoreStore(client *firestore.Client, logger zerolog.Logger) (routing.MessageStore, error) {
	if client == nil {
		return nil, fmt.Errorf("firestore client cannot be nil")
	}
	store := &FirestoreStore{
		client: client,
		logger: logger,
	}
	return store, nil
}

// StoreMessages saves a slice of message envelopes for a specific recipient URN in Firestore.
func (s *FirestoreStore) StoreMessages(ctx context.Context, recipient urn.URN, envelopes []*transport.SecureEnvelope) error {
	if len(envelopes) == 0 {
		return nil
	}
	recipientKey := recipient.String()

	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		colRef := s.client.Collection(usersCollection).Doc(recipientKey).Collection(messagesCollection)

		for _, envelope := range envelopes {
			if envelope.MessageID == "" {
				envelope.MessageID = uuid.NewString()
				s.logger.Warn(). // ...
							Msg("Generated new MessageID for incoming envelope.")
			} //
			if envelope.MessageID == "" {
				envelope.MessageID = uuid.NewString()
				s.logger.Warn().
					Str("newId", envelope.MessageID).
					Str("recipientID", recipientKey).
					Msg("Generated new MessageID for incoming envelope.")
			}
			docRef := colRef.Doc(envelope.MessageID)
			// Use the transaction to perform the Set operation.
			firestoreEnv := toFirestore(envelope)
			err := tx.Set(docRef, firestoreEnv)
			if err != nil {
				return err // This will cause the transaction to roll back.
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("transaction failed for user %s: %w", recipientKey, err)
	}
	return nil
}

// RetrieveMessages retrieves all messages for a recipient that have not yet been delivered.
// in: persistence/firestore.go

// RetrieveMessages retrieves all messages for a recipient that have not yet been delivered.
func (s *FirestoreStore) RetrieveMessages(ctx context.Context, recipient urn.URN) ([]*transport.SecureEnvelope, error) {
	var envelopes []*transport.SecureEnvelope
	recipientKey := recipient.String()

	// --- ADDED DEBUGGING ---
	s.logger.Info().Str("recipientKey", recipientKey).Msg("[RetrieveMessages] Starting retrieval for recipient")
	// -------------------------

	colRef := s.client.Collection(usersCollection).Doc(recipientKey).Collection(messagesCollection)
	docs, err := colRef.Documents(ctx).GetAll()
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// --- ADDED DEBUGGING ---
			s.logger.Warn().Str("recipientKey", recipientKey).Msg("[RetrieveMessages] No message collection found for recipient (normal).")
			// -------------------------
			return envelopes, nil
		}
		return nil, fmt.Errorf("failed to fetch documents for user %s: %w", recipientKey, err)
	}

	// --- ADDED DEBUGGING ---
	s.logger.Info().Int("documentCount", len(docs)).Str("recipientKey", recipientKey).Msg("[RetrieveMessages] Fetched documents from Firestore")
	// -------------------------

	for _, doc := range docs {
		// --- ADDED DEBUGGING ---
		// Log the raw data from Firestore before we try to parse it.
		// This will show us EXACTLY what is in the database.
		s.logger.Debug().
			Str("documentID", doc.Ref.ID).
			Interface("rawData", doc.Data()).
			Msg("[RetrieveMessages] Processing document")
		// -------------------------

		var env firestoreEnvelope
		err = doc.DataTo(&env)
		if err != nil {
			s.logger.Error().
				Err(err).
				Str("documentID", doc.Ref.ID).
				Str("recipientID", recipientKey).
				Msg("Failed to decode message from firestore")
			continue
		}
		envelope, err := fromFirestore(&env)
		if err != nil {
			s.logger.Error().
				Err(err).
				Str("documentID", doc.Ref.ID).
				Str("recipientID", recipientKey).
				Msg("Failed to convert message from firestore format") // Corrected log message
			continue
		}
		envelopes = append(envelopes, envelope)
	}

	s.logger.Info().Int("envelopeCount", len(envelopes)).Msg("[RetrieveMessages] Finished processing messages")
	return envelopes, nil
}

// DeleteMessages marks a set of messages as delivered by deleting them from Firestore.
func (s *FirestoreStore) DeleteMessages(ctx context.Context, recipient urn.URN, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	recipientKey := recipient.String()

	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		colRef := s.client.Collection(usersCollection).Doc(recipientKey).Collection(messagesCollection)

		for _, msgID := range messageIDs {
			if msgID == "" {
				s.logger.Warn().Str("recipientID", recipientKey).Msg("Encountered empty message ID during DeleteMessages")
				continue
			}
			docRef := colRef.Doc(msgID)
			// Use the transaction to perform the Delete operation.
			err := tx.Delete(docRef)
			if err != nil {
				return err // This will cause the transaction to roll back.
			}
		}
		return nil
	})

	if err != nil {
		return fmt.Errorf("transaction failed for batch delete for user %s: %w", recipientKey, err)
	}

	s.logger.Info().
		Str("recipientID", recipientKey).
		Int("count", len(messageIDs)).
		Msg("Successfully deleted delivered messages")

	return nil
}

// firestoreEnvelope is a private struct representing how a SecureEnvelope
// is stored in Firestore. URNs are stored as strings.
type firestoreEnvelope struct {
	SenderID    string `firestore:"senderId"`
	RecipientID string `firestore:"recipientId"`
	MessageID   string `firestore:"messageId"`

	EncryptedData         []byte `firestore:"encryptedData"`
	EncryptedSymmetricKey []byte `firestore:"encryptedSymmetricKey"`
	Signature             []byte `firestore:"signature"`
}

// firestore.go

// toFirestore converts the canonical SecureEnvelope to the firestore-specific struct.
func toFirestore(env *transport.SecureEnvelope) *firestoreEnvelope {
	return &firestoreEnvelope{
		SenderID:              env.SenderID.String(),
		RecipientID:           env.RecipientID.String(),
		MessageID:             env.MessageID,
		EncryptedData:         env.EncryptedData,
		EncryptedSymmetricKey: env.EncryptedSymmetricKey,
		Signature:             env.Signature,
	}
}

// fromFirestore converts the firestore-specific struct back to the canonical SecureEnvelope.
func fromFirestore(fenv *firestoreEnvelope) (*transport.SecureEnvelope, error) {
	senderURN, err := urn.Parse(fenv.SenderID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sender URN '%s': %w", fenv.SenderID, err)
	}

	recipientURN, err := urn.Parse(fenv.RecipientID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse recipient URN '%s': %w", fenv.RecipientID, err)
	}

	return &transport.SecureEnvelope{
		SenderID:              senderURN,
		RecipientID:           recipientURN,
		MessageID:             fenv.MessageID,
		EncryptedData:         fenv.EncryptedData,
		EncryptedSymmetricKey: fenv.EncryptedSymmetricKey,
		Signature:             fenv.Signature,
	}, nil
}
