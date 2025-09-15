package pipeline

import (
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/rs/zerolog"
)

// Config holds all the necessary configuration for the pipeline service.
type Config struct {
	NumWorkers int
}

// NewService creates and assembles the entire message processing pipeline.
// It accepts a generic consumer and wires it up with our application-specific
// transformer and processor.
func NewService(
	cfg Config,
	deps *routing.Dependencies,
	consumer messagepipeline.MessageConsumer, // Now accepts a generic consumer
	logger zerolog.Logger,
) (*messagepipeline.StreamingService[transport.SecureEnvelope], error) {

	// 1. Create the message handler (the processor), injecting its dependencies.
	processor := NewRoutingProcessor(
		deps.PresenceCache,
		deps.DeviceTokenFetcher,
		deps.DeliveryProducer,
		deps.PushNotifier,
		deps.MessageStore, // REFACTOR: Pass the MessageStore dependency.
		logger,            // REFACTOR: Pass the structured logger.
	)

	// 2. Assemble the pipeline using the generic StreamingService component.
	// The consumer is now passed in as a dependency.
	streamingService, err := messagepipeline.NewStreamingService[transport.SecureEnvelope](
		messagepipeline.StreamingServiceConfig{NumWorkers: cfg.NumWorkers},
		consumer,
		EnvelopeTransformer,
		processor,
		logger,
	)
	if err != nil {
		return nil, err
	}

	return streamingService, nil
}
