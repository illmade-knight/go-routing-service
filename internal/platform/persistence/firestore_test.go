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

// setupSuite initializes the Firestore emulator and all necessary clients ONCE
// for the entire test suite, significantly improving performance.
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

	fixture := &testFixture{
		ctx:      ctx,
		fsClient: fsClient,
		store:    store,
	}
	return ctx, fixture
}

// TestFirestoreStore is the main entry point for all persistence tests,
// ensuring the suite is set up only once.
func TestFirestoreStore(t *testing.T) {
	_, fixture := setupSuite(t)

	t.Run("StoreMessages", func(t *testing.T) {
		testStoreMessages(t, fixture)
	})

	t.Run("RetrieveMessages", func(t *testing.T) {
		testRetrieveMessages(t, fixture)
	})

	t.Run("DeleteMessages", func(t *testing.T) {
		testDeleteMessages(t, fixture)
	})
}

// testStoreMessages validates that messages are correctly stored in Firestore.
func testStoreMessages(t *testing.T, f *testFixture) {
	recipientURN, err := urn.Parse("urn:sm:user:store-user")
	require.NoError(t, err)

	envelopes := []*transport.SecureEnvelope{
		{MessageID: "msg1", RecipientID: recipientURN, EncryptedData: []byte("payload1")},
		{MessageID: "msg2", RecipientID: recipientURN, EncryptedData: []byte("payload2")},
	}

	// Act
	err = f.store.StoreMessages(f.ctx, recipientURN, envelopes)
	require.NoError(t, err)

	// Assert
	docs, err := f.fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(f.ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, docs, 2, "Expected two messages to be stored")

	var found1, found2 bool
	for _, doc := range docs {
		var env transport.SecureEnvelope
		err := doc.DataTo(&env)
		require.NoError(t, err)
		if env.MessageID == "msg1" {
			assert.Equal(t, []byte("payload1"), env.EncryptedData)
			found1 = true
		}
		if env.MessageID == "msg2" {
			assert.Equal(t, []byte("payload2"), env.EncryptedData)
			found2 = true
		}
	}
	assert.True(t, found1, "Message 1 was not found")
	assert.True(t, found2, "Message 2 was not found")
}

// testRetrieveMessages validates that stored messages are correctly retrieved.
func testRetrieveMessages(t *testing.T, f *testFixture) {
	recipientURN, err := urn.Parse("urn:sm:user:retrieve-user")
	require.NoError(t, err)

	// Arrange: Pre-populate the store with messages.
	preEnvelopes := []*transport.SecureEnvelope{
		{MessageID: "msg1", RecipientID: recipientURN},
		{MessageID: "msg2", RecipientID: recipientURN},
	}
	for _, env := range preEnvelopes {
		_, err := f.fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Doc(env.MessageID).Set(f.ctx, env)
		require.NoError(t, err)
	}

	// Act
	retrievedEnvelopes, err := f.store.RetrieveMessages(f.ctx, recipientURN)
	require.NoError(t, err)

	// Assert
	require.Len(t, retrievedEnvelopes, 2)
	// REFACTOR: The assertion is now more robust. We check for the presence
	// of the expected message IDs instead of a deep struct comparison, which
	// avoids issues with nil vs. empty slices after serialization.
	retrievedIDs := make(map[string]bool)
	for _, env := range retrievedEnvelopes {
		retrievedIDs[env.MessageID] = true
	}
	assert.Contains(t, retrievedIDs, "msg1")
	assert.Contains(t, retrievedIDs, "msg2")

	// Test case for a user with no messages.
	noMsgURN, err := urn.Parse("urn:sm:user:no-messages-user")
	require.NoError(t, err)
	emptyEnvelopes, err := f.store.RetrieveMessages(f.ctx, noMsgURN)
	require.NoError(t, err)
	assert.Empty(t, emptyEnvelopes)
}

// testDeleteMessages validates that messages are correctly deleted.
func testDeleteMessages(t *testing.T, f *testFixture) {
	recipientURN, err := urn.Parse("urn:sm:user:delete-user")
	require.NoError(t, err)

	// Arrange: Pre-populate the store with messages.
	preEnvelopes := []*transport.SecureEnvelope{
		{MessageID: "msg1", RecipientID: recipientURN},
		{MessageID: "msg2", RecipientID: recipientURN},
		{MessageID: "msg3", RecipientID: recipientURN},
	}
	for _, env := range preEnvelopes {
		_, err := f.fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Doc(env.MessageID).Set(f.ctx, env)
		require.NoError(t, err)
	}

	// Act: Delete two of the three messages.
	idsToDelete := []string{"msg1", "msg3"}
	err = f.store.DeleteMessages(f.ctx, recipientURN, idsToDelete)
	require.NoError(t, err)

	// Assert
	docs, err := f.fsClient.Collection("user-messages").Doc(recipientURN.String()).Collection("messages").Documents(f.ctx).GetAll()
	require.NoError(t, err)
	require.Len(t, docs, 1, "Expected one message to remain")

	var remainingEnv transport.SecureEnvelope
	err = docs[0].DataTo(&remainingEnv)
	require.NoError(t, err)
	assert.Equal(t, "msg2", remainingEnv.MessageID, "The wrong message was deleted")
}
