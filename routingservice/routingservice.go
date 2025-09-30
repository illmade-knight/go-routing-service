package routingservice

import (
	"context"
	"fmt"
	"net/http"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/microservice"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/internal/api"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-routing-service/routingservice/config"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/rs/zerolog"
)

// Wrapper now embeds BaseServer to get standard server functionality.
type Wrapper struct {
	*microservice.BaseServer
	processingService *messagepipeline.StreamingService[transport.SecureEnvelope]
	apiHandler        *api.API
	logger            zerolog.Logger
}

// New creates and wires up the entire routing service using the base server.
func New(
	cfg *config.AppConfig,
	deps *routing.Dependencies,
	authMiddleware func(http.Handler) http.Handler,
	logger zerolog.Logger,
) (*Wrapper, error) {

	// 1. Create the standard base server.
	// CORRECTED: Use the APIPort field from the AppConfig.
	baseServer := microservice.NewBaseServer(logger, ":"+cfg.APIPort)

	// 2. Create the core message processing pipeline.
	processingService, err := newCoreProcessingPipeline(cfg, deps, logger)
	if err != nil {
		return nil, err
	}

	// 3. Create the API handlers.
	apiHandler := api.NewAPI(deps.IngestionProducer, deps.MessageStore, logger)

	// 4. Register routes and apply middleware.
	mux := baseServer.Mux()
	cors := middleware.NewCorsMiddleware(middleware.CorsConfig{
		AllowedOrigins: cfg.Cors.AllowedOrigins,
		Role:           middleware.CorsRole(cfg.Cors.Role),
	})

	mux.Handle("POST /send", cors(authMiddleware(http.HandlerFunc(apiHandler.SendHandler))))
	mux.Handle("GET /messages", cors(authMiddleware(http.HandlerFunc(apiHandler.GetMessagesHandler))))

	// Handle preflight requests
	preflightHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("OPTIONS /send", cors(preflightHandler))
	mux.Handle("OPTIONS /messages", cors(preflightHandler))

	return &Wrapper{
		BaseServer:        baseServer,
		processingService: processingService,
		apiHandler:        apiHandler,
		logger:            logger,
	}, nil
}

// newCoreProcessingPipeline assembles the background message processing service.
func newCoreProcessingPipeline(
	cfg *config.AppConfig,
	deps *routing.Dependencies,
	logger zerolog.Logger,
) (*messagepipeline.StreamingService[transport.SecureEnvelope], error) {
	processor := pipeline.NewRoutingProcessor(deps, cfg, logger)

	return messagepipeline.NewStreamingService[transport.SecureEnvelope](
		messagepipeline.StreamingServiceConfig{NumWorkers: cfg.NumPipelineWorkers},
		deps.IngestionConsumer,
		pipeline.EnvelopeTransformer,
		processor,
		logger,
	)
}

// Start runs the service's background components before starting the base HTTP server.
func (w *Wrapper) Start(ctx context.Context) error {
	w.logger.Info().Msg("Core processing pipeline starting...")
	if err := w.processingService.Start(ctx); err != nil {
		return fmt.Errorf("failed to start processing service: %w", err)
	}

	w.SetReady(true)
	w.logger.Info().Msg("Service is now ready.")

	return w.BaseServer.Start()
}

// Shutdown gracefully stops all service components in the correct order.
func (w *Wrapper) Shutdown(ctx context.Context) error {
	w.logger.Info().Msg("Shutting down service components...")
	var finalErr error

	if err := w.processingService.Stop(ctx); err != nil {
		w.logger.Error().Err(err).Msg("Processing service shutdown failed.")
		finalErr = err
	}

	w.apiHandler.Wait() // Wait for any background API tasks (e.g., message deletion) to finish.

	if err := w.BaseServer.Shutdown(ctx); err != nil {
		w.logger.Error().Err(err).Msg("HTTP server shutdown failed.")
		finalErr = err
	}

	w.logger.Info().Msg("All components shut down.")
	return finalErr
}

// Wait blocks until all background API tasks (e.g., message deletion) are complete.
func (w *Wrapper) Wait() {
	w.apiHandler.Wait()
}
