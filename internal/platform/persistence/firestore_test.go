//go:build integration

package persistence_test

import (
	"context"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/illmade-knight/go-routing-service/internal/platform/persistence"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/illmade-knight/go-test/emulators"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testFixture holds the shared resources for all tests in this file.
type testFixture struct {
	ctx      context.Context
	fsClient *firestore.Client
	store    routing.MessageStore
}

// setupSuite initializes the Firestore emulator and all necessary clients ONCE.
func setupSuite(t *testing.T) (context.Context, *testFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	const projectID = "test-project-persistence"
	firestoreConn := emulators.SetupFirestoreEmulator(t, ctx, emulators.GetDefaultFirestoreConfig(projectID))
	fsClient, err := firestore.NewClient(context.Background(), projectID, firestoreConn.ClientOptions...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsClient.Close() })

	logger := zerolog.New(zerolog.NewTestWriter(t))
	store, err := persistence.NewFirestoreStore(fsClient, logger)
	require.NoError(t, err)

	return ctx, &testFixture{
		ctx:      ctx,
		fsClient: fsClient,
		store:    store,
	}
}

// TestFirestoreStore is the main entry point for all persistence tests.
func TestFirestoreStore(t *testing.T) {
	_, fixture := setupSuite(t)

	t.Run("StoreAndRetrieveFullCycle", func(t *testing.T) {
		// Arrange
		senderURN, _ := urn.Parse("urn:sm:user:sender-user")
		recipientURN, _ := urn.Parse("urn:sm:user:store-user")
		envelopes := []*transport.SecureEnvelope{
			{MessageID: "msg1", SenderID: senderURN, RecipientID: recipientURN, EncryptedData: []byte("payload1")},
			{MessageID: "msg2", SenderID: senderURN, RecipientID: recipientURN, EncryptedData: []byte("payload2")},
		}

		// Act: Store the messages
		err := fixture.store.StoreMessages(fixture.ctx, recipientURN, envelopes)
		require.NoError(t, err)

		// Assert: Retrieve and verify data integrity
		retrieved, err := fixture.store.RetrieveMessages(fixture.ctx, recipientURN)
		require.NoError(t, err)
		require.Len(t, retrieved, 2)
		// Use assert.ElementsMatch to compare slices regardless of order.
		assert.ElementsMatch(t, envelopes, retrieved)
	})

	t.Run("DeleteMessages", func(t *testing.T) {
		// Arrange
		recipientURN, _ := urn.Parse("urn:sm:user:delete-user")
		preEnvelopes := []*transport.SecureEnvelope{
			{MessageID: "del1", RecipientID: recipientURN},
			{MessageID: "del2", RecipientID: recipientURN},
			{MessageID: "del3", RecipientID: recipientURN},
		}
		// CORRECTED: Seed the database with the actual Protobuf struct.
		for _, env := range preEnvelopes {
			protoEnv := transport.ToProto(env)
			_, err := fixture.fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Doc(env.MessageID).Set(fixture.ctx, protoEnv)
			require.NoError(t, err)
		}

		// Act: Delete two of the three messages.
		err := fixture.store.DeleteMessages(fixture.ctx, recipientURN, []string{"del1", "del3"})
		require.NoError(t, err)

		// Assert
		remaining, err := fixture.store.RetrieveMessages(fixture.ctx, recipientURN)
		require.NoError(t, err)
		require.Len(t, remaining, 1, "Expected one message to remain")
		assert.Equal(t, "del2", remaining[0].MessageID)
	})
}
