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

	t.Run("StoreAndRetrieveFullCycle", func(t *testing.T) {
		testStoreAndRetrieveFullCycle(t, fixture)
	})

	t.Run("RetrieveMessagesWithPreSeededData", func(t *testing.T) {
		testRetrieveMessagesWithPreSeededData(t, fixture)
	})

	t.Run("DeleteMessages", func(t *testing.T) {
		testDeleteMessages(t, fixture)
	})
}

// testStoreAndRetrieveFullCycle validates the full write/read cycle.
func testStoreAndRetrieveFullCycle(t *testing.T, f *testFixture) {
	// Arrange
	senderURN, err := urn.Parse("urn:sm:user:sender-user")
	require.NoError(t, err)
	recipientURN, err := urn.Parse("urn:sm:user:store-user")
	require.NoError(t, err)

	envelopes := []*transport.SecureEnvelope{
		{MessageID: "msg1", SenderID: senderURN, RecipientID: recipientURN, EncryptedData: []byte("payload1")},
		{MessageID: "msg2", SenderID: senderURN, RecipientID: recipientURN, EncryptedData: []byte("payload2")},
	}

	// Act: Store the messages
	err = f.store.StoreMessages(f.ctx, recipientURN, envelopes)
	require.NoError(t, err)

	// Assert: Retrieve the messages using the store and verify full data integrity
	retrieved, err := f.store.RetrieveMessages(f.ctx, recipientURN)
	require.NoError(t, err)
	require.Len(t, retrieved, 2, "Expected two messages to be retrieved")

	retrievedMap := make(map[string]*transport.SecureEnvelope)
	for _, env := range retrieved {
		retrievedMap[env.MessageID] = env
	}

	require.Contains(t, retrievedMap, "msg1")
	assert.Equal(t, senderURN, retrievedMap["msg1"].SenderID)
	assert.Equal(t, recipientURN, retrievedMap["msg1"].RecipientID)
	assert.Equal(t, []byte("payload1"), retrievedMap["msg1"].EncryptedData)

	require.Contains(t, retrievedMap, "msg2")
	assert.Equal(t, senderURN, retrievedMap["msg2"].SenderID)
	assert.Equal(t, recipientURN, retrievedMap["msg2"].RecipientID)
}

// testRetrieveMessagesWithPreSeededData validates that correctly formatted stored messages are retrieved.
func testRetrieveMessagesWithPreSeededData(t *testing.T, f *testFixture) {
	// Arrange: Create URNs for testing
	senderURN, err := urn.Parse("urn:sm:user:ret-sender-user")
	require.NoError(t, err)
	recipientURN, err := urn.Parse("urn:sm:user:retrieve-user")
	require.NoError(t, err)

	// NOTE: We seed the database with a map that mimics the 'firestoreEnvelope'
	// struct, storing URNs as strings, to simulate real-world stored data.
	preSeededMessages := []map[string]interface{}{
		{"messageId": "msg1", "senderId": senderURN.String(), "recipientId": recipientURN.String()},
		{"messageId": "msg2", "senderId": senderURN.String(), "recipientId": recipientURN.String()},
	}

	for _, msg := range preSeededMessages {
		_, err := f.fsClient.Collection("user-messages").
			Doc(recipientURN.String()).
			Collection("messages").
			Doc(msg["messageId"].(string)).
			Set(f.ctx, msg)
		require.NoError(t, err)
	}

	// Act
	retrievedEnvelopes, err := f.store.RetrieveMessages(f.ctx, recipientURN)
	require.NoError(t, err)

	// Assert: Check that the retrieved envelopes were correctly parsed back into their canonical form.
	require.Len(t, retrievedEnvelopes, 2)
	for _, env := range retrievedEnvelopes {
		assert.Equal(t, recipientURN, env.RecipientID, "RecipientID was not parsed correctly")
		assert.Equal(t, senderURN, env.SenderID, "SenderID was not parsed correctly")
	}

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
		// NOTE: We can still use the canonical struct here for simplicity,
		// as Firestore's client will convert it to the map format upon Set.
		// The important part is that the Delete logic only needs the ID.
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
