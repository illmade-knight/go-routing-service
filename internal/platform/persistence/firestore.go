// REFACTOR: This file is updated to store the canonical Protobuf message
// (transport.SecureEnvelopePb) directly in Firestore. This eliminates an
// intermediate struct and a potential source of data corruption, ensuring
// end-to-end serialization consistency.
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
	return &FirestoreStore{
		client: client,
		logger: logger,
	}, nil
}

// StoreMessages saves a slice of message envelopes for a specific recipient URN in Firestore.
func (s *FirestoreStore) StoreMessages(ctx context.Context, recipient urn.URN, envelopes []*transport.SecureEnvelope) error {
	recipientKey := recipient.String()
	collectionRef := s.client.Collection(usersCollection).Doc(recipientKey).Collection(messagesCollection)

	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		for _, env := range envelopes {
			// CORRECTED: Convert to the Protobuf struct and store it directly.
			protoEnv := transport.ToProto(env)
			if protoEnv.MessageId == "" {
				protoEnv.MessageId = uuid.NewString()
			}
			docRef := collectionRef.Doc(protoEnv.MessageId)
			if err := tx.Set(docRef, protoEnv); err != nil {
				return err // The transaction will be rolled back.
			}
		}
		return nil
	})
}

// RetrieveMessages fetches all stored messages for a recipient, returning them as canonical SecureEnvelopes.
func (s *FirestoreStore) RetrieveMessages(ctx context.Context, recipient urn.URN) ([]*transport.SecureEnvelope, error) {
	recipientKey := recipient.String()
	collectionRef := s.client.Collection(usersCollection).Doc(recipientKey).Collection(messagesCollection)

	var envelopes []*transport.SecureEnvelope
	err := s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		docSnaps, err := tx.Documents(collectionRef).GetAll()
		if err != nil {
			return err
		}

		envelopes = make([]*transport.SecureEnvelope, 0, len(docSnaps))
		for _, doc := range docSnaps {
			// CORRECTED: Deserialize directly into the Protobuf struct.
			var protoEnv transport.SecureEnvelopePb
			if err := doc.DataTo(&protoEnv); err != nil {
				// Log the error but continue, so one bad message doesn't fail the whole batch.
				s.logger.Error().Err(err).Str("doc_id", doc.Ref.ID).Msg("Failed to deserialize message from Firestore")
				continue
			}
			// CORRECTED: Convert from the Protobuf struct back to the native Go struct.
			nativeEnv, err := transport.FromProto(&protoEnv)
			if err != nil {
				s.logger.Error().Err(err).Str("doc_id", doc.Ref.ID).Msg("Failed to convert message from proto")
				continue
			}
			envelopes = append(envelopes, nativeEnv)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return envelopes, nil
}

// DeleteMessages removes a set of messages for a recipient by their IDs.
func (s *FirestoreStore) DeleteMessages(ctx context.Context, recipient urn.URN, messageIDs []string) error {
	recipientKey := recipient.String()
	collectionRef := s.client.Collection(usersCollection).Doc(recipientKey).Collection(messagesCollection)

	return s.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		for _, msgID := range messageIDs {
			docRef := collectionRef.Doc(msgID)
			if err := tx.Delete(docRef); err != nil {
				// Don't stop on a single not-found error; try to delete the rest.
				if status.Code(err) != codes.NotFound {
					return err
				}
			}
		}
		return nil
	})
}
