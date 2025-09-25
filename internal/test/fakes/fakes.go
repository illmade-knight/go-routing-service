// Package fakes provides in-memory test doubles (fakes) and test-specific
// adapters for the service's dependencies. These are used in the cmd/local
// entrypoint and in integration tests.
package fakes

import (
	"context"
	"sync"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
)

// --- Consumer ---
// No changes in this section.
type InMemoryConsumer struct {
	outputChan chan messagepipeline.Message
	logger     zerolog.Logger
	stopOnce   sync.Once
	doneChan   chan struct{}
}

func NewInMemoryConsumer(bufferSize int, logger zerolog.Logger) *InMemoryConsumer {
	return &InMemoryConsumer{
		outputChan: make(chan messagepipeline.Message, bufferSize),
		logger:     logger.With().Str("component", "InMemoryConsumer").Logger(),
		doneChan:   make(chan struct{}),
	}
}
func (c *InMemoryConsumer) Publish(msg messagepipeline.Message) {
	select {
	case c.outputChan <- msg:
	case <-c.doneChan:
	}
}
func (c *InMemoryConsumer) Messages() <-chan messagepipeline.Message { return c.outputChan }
func (c *InMemoryConsumer) Start(ctx context.Context) error          { return nil }
func (c *InMemoryConsumer) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		close(c.doneChan)
		close(c.outputChan)
	})
	return nil
}
func (c *InMemoryConsumer) Done() <-chan struct{} { return c.doneChan }

// --- Producer ---

// REFACTOR: Renamed struct back to Producer to avoid confusion.
type Producer struct {
	logger        zerolog.Logger
	publishedChan chan messagepipeline.MessageData
}

// REFACTOR: Renamed constructor back to NewProducer.
func NewProducer(logger zerolog.Logger) *Producer {
	return &Producer{
		logger:        logger,
		publishedChan: make(chan messagepipeline.MessageData, 100),
	}
}
func (p *Producer) Published() <-chan messagepipeline.MessageData { return p.publishedChan }

// REFACTOR: Renamed method from PublishEvent back to Publish to match the DeliveryEventProducer interface.
func (p *Producer) Publish(ctx context.Context, data messagepipeline.MessageData) (string, error) {
	p.logger.Info().Str("id", data.ID).Msg("[FAKES-PRODUCER] Publish called.")
	p.publishedChan <- data
	return data.ID, nil
}
func (p *Producer) Stop(ctx context.Context) error { close(p.publishedChan); return nil }

// --- Persistence & Notifications ---
// No changes in this section.
type PushNotifier struct{ logger zerolog.Logger }

func NewPushNotifier(logger zerolog.Logger) *PushNotifier { return &PushNotifier{logger: logger} }
func (m *PushNotifier) Notify(ctx context.Context, tokens []routing.DeviceToken, envelope *transport.SecureEnvelope) error {
	return nil
}

type TokenFetcher struct{}

func NewTokenFetcher() *TokenFetcher { return &TokenFetcher{} }
func (m *TokenFetcher) Fetch(ctx context.Context, key urn.URN) ([]routing.DeviceToken, error) {
	return nil, nil
}
func (m *TokenFetcher) Close() error { return nil }

type MessageStore struct{ logger zerolog.Logger }

func NewMessageStore(logger zerolog.Logger) *MessageStore { return &MessageStore{logger: logger} }
func (m *MessageStore) StoreMessages(ctx context.Context, r urn.URN, e []*transport.SecureEnvelope) error {
	return nil
}
func (m *MessageStore) RetrieveMessages(ctx context.Context, r urn.URN) ([]*transport.SecureEnvelope, error) {
	return nil, nil
}
func (m *MessageStore) DeleteMessages(ctx context.Context, r urn.URN, ids []string) error { return nil }
