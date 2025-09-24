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
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/rs/zerolog"
)

// Wrapper now embeds BaseServer to get standard server functionality.
type Wrapper struct {
	*microservice.BaseServer
	cfg               *routing.Config
	processingService *messagepipeline.StreamingService[transport.SecureEnvelope]
	apiHandler        *api.API
	logger            zerolog.Logger
}

// New creates and wires up the entire routing service using the base server.
func New(
	cfg *routing.Config,
	deps *routing.Dependencies,
	consumer messagepipeline.MessageConsumer,
	producer routing.IngestionProducer,
	logger zerolog.Logger,
) (*Wrapper, error) {

	// 1. Create the standard base server. It includes /healthz, /readyz, /metrics.
	baseServer := microservice.NewBaseServer(logger, cfg.HTTPListenAddr)

	// 2. Create the core message processing pipeline.
	pipelineConfig := pipeline.Config{
		NumWorkers: cfg.NumPipelineWorkers,
	}
	processingService, err := pipeline.NewService(pipelineConfig, deps, consumer, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create pipeline service: %w", err)
	}

	// 3. Create service-specific API handlers.
	apiHandler := api.NewAPI(producer, deps.MessageStore, logger)

	// 4. Get the mux and middleware from the base library.
	mux := baseServer.Mux()
	jwtAuth := middleware.NewJWTAuthMiddleware(cfg.JWTSecret)
	cors := middleware.NewCorsMiddleware(cfg.Cors)

	// 5. Register service-specific routes with the centralized middleware.
	mux.Handle("POST /send", cors(jwtAuth(http.HandlerFunc(apiHandler.SendHandler))))
	mux.Handle("GET /messages", cors(jwtAuth(http.HandlerFunc(apiHandler.GetMessagesHandler))))

	// CORRECTED: Add explicit handlers for preflight OPTIONS requests.
	// These handlers only need to run the CORS middleware to return the correct headers.
	preflightHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("OPTIONS /send", cors(preflightHandler))
	mux.Handle("OPTIONS /messages", cors(preflightHandler))

	return &Wrapper{
		BaseServer:        baseServer,
		cfg:               cfg,
		processingService: processingService,
		apiHandler:        apiHandler,
		logger:            logger,
	}, nil
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

	if err := w.BaseServer.Shutdown(ctx); err != nil {
		w.logger.Error().Err(err).Msg("HTTP server shutdown failed.")
		finalErr = err
	}

	// Wait for any in-flight API goroutines (like message deletions) to complete.
	w.logger.Info().Msg("Waiting for background API tasks to finish...")
	w.apiHandler.Wait()
	w.logger.Info().Msg("Background tasks finished.")

	w.logger.Info().Msg("Service shutdown complete.")
	return finalErr
}
