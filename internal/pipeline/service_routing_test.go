package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/pipeline" // Updated import
	"github.com/illmade-knight/go-routing-service/pkg/routing"       // Updated import
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockMessageConsumer is a test double for the messagepipeline.MessageConsumer interface.
type mockMessageConsumer struct {
	StartFunc      func(ctx context.Context) error
	StopFunc       func(ctx context.Context) error
	messageChannel chan messagepipeline.Message
}

func newMockMessageConsumer() *mockMessageConsumer {
	return &mockMessageConsumer{
		messageChannel: make(chan messagepipeline.Message),
	}
}
func (m *mockMessageConsumer) Start(ctx context.Context) error          { return m.StartFunc(ctx) }
func (m *mockMessageConsumer) Stop(ctx context.Context) error           { return m.StopFunc(ctx) }
func (m *mockMessageConsumer) Messages() <-chan messagepipeline.Message { return m.messageChannel }
func (m *mockMessageConsumer) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func TestService_Lifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	// Arrange: Create nil dependencies as they aren't used in this lifecycle test.
	deps := &routing.Dependencies{}
	cfg := pipeline.Config{NumWorkers: 1}

	// Arrange: Create the mock consumer and track its method calls.
	startCalled := false
	stopCalled := false
	mockConsumer := newMockMessageConsumer()
	mockConsumer.StartFunc = func(ctx context.Context) error {
		startCalled = true
		return nil
	}
	mockConsumer.StopFunc = func(ctx context.Context) error {
		stopCalled = true
		close(mockConsumer.messageChannel) // Allows workers to shut down
		return nil
	}

	// Act: Create the service using the refactored constructor, injecting the mock.
	service, err := pipeline.NewService(cfg, deps, mockConsumer, zerolog.Nop())
	require.NoError(t, err)

	// Act & Assert for Start
	err = service.Start(ctx)
	require.NoError(t, err)
	assert.True(t, startCalled, "service.Start() should call consumer.Start()")

	// Act & Assert for Stop
	err = service.Stop(ctx)
	require.NoError(t, err, "service.Stop() should not return an error")
	assert.True(t, stopCalled, "service.Stop() should call consumer.Stop()")
}
