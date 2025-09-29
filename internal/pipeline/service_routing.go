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
func NewService(
	cfg Config,
	deps *routing.Dependencies,
	routingCfg *routing.Config, // ADDED: Pass the main service config down.
	consumer messagepipeline.MessageConsumer,
	logger zerolog.Logger,
) (*messagepipeline.StreamingService[transport.SecureEnvelope], error) {

	// 1. Create the message handler (the processor), injecting its dependencies and config.
	processor := New(deps, routingCfg, logger)

	// 2. Assemble the pipeline using the generic StreamingService component.
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
