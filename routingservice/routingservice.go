package routingservice

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-routing-service/internal/api"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/rs/zerolog"
)

// Wrapper encapsulates all components of the running service.
type Wrapper struct {
	cfg               *routing.Config
	apiServer         *http.Server
	processingService *messagepipeline.StreamingService[transport.SecureEnvelope]
	logger            zerolog.Logger
}

// New creates and wires up the entire routing service.
func New(
	cfg *routing.Config,
	deps *routing.Dependencies,
	consumer messagepipeline.MessageConsumer,
	producer routing.IngestionProducer,
	logger zerolog.Logger,
) (*Wrapper, error) {
	var err error

	pipelineConfig := pipeline.Config{
		NumWorkers: cfg.NumPipelineWorkers,
	}

	processingService, err := pipeline.NewService(pipelineConfig, deps, consumer, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create pipeline service: %w", err)
	}

	apiHandler := api.NewAPI(producer, deps.MessageStore, logger, cfg.JWTSecret)
	mux := http.NewServeMux()

	// THE FIX:
	// We create a single, explicit handler for each path. This handler is
	// responsible for routing based on the HTTP method (POST, GET, OPTIONS).
	// This is a more robust pattern that avoids ambiguities in the default router.

	// Handler for the /send endpoint
	sendHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			// For POST requests, the chain is: JWT Auth -> SendHandler
			apiHandler.JwtAuthMiddleware(http.HandlerFunc(apiHandler.SendHandler)).ServeHTTP(w, r)
		} else {
			// For all other methods (including OPTIONS), we respond with 200 OK.
			// The CorsMiddleware has already set the necessary headers.
			w.WriteHeader(http.StatusOK)
		}
	})

	// Handler for the /messages endpoint
	messagesHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// For GET requests, the chain is: JWT Auth -> GetMessagesHandler
			apiHandler.JwtAuthMiddleware(http.HandlerFunc(apiHandler.GetMessagesHandler)).ServeHTTP(w, r)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})

	// We wrap our new, robust handlers in the CORS middleware. The CORS middleware
	// will run first for EVERY request to these paths.
	mux.Handle("/send", api.CorsMiddleware(sendHandler))
	mux.Handle("/messages", api.CorsMiddleware(messagesHandler))

	apiServer := &http.Server{Addr: cfg.HTTPListenAddr, Handler: mux}

	wrapper := &Wrapper{
		cfg:               cfg,
		apiServer:         apiServer,
		processingService: processingService,
		logger:            logger,
	}
	return wrapper, nil
}

// Start runs the service's background components.
func (w *Wrapper) Start(ctx context.Context) error {
	w.logger.Info().Msg("Core processing pipeline starting.")
	err := w.processingService.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start processing service: %w", err)
	}

	listener, err := net.Listen("tcp", w.cfg.HTTPListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on http address: %w", err)
	}
	w.apiServer.Addr = listener.Addr().String()

	go func() {
		w.logger.Info().Str("address", w.apiServer.Addr).Msg("HTTP server starting.")
		if err := w.apiServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			w.logger.Fatal().Err(err).Msg("HTTP server failed")
		}
	}()
	return nil
}

// GetHTTPPort returns the resolved address the HTTP server is listening on.
func (w *Wrapper) GetHTTPPort() string {
	if w.apiServer != nil && w.apiServer.Addr != "" {
		_, port, err := net.SplitHostPort(w.apiServer.Addr)
		if err == nil {
			return ":" + port
		}
	}
	return ""
}

// Shutdown gracefully stops all service components.
func (w *Wrapper) Shutdown(ctx context.Context) error {
	w.logger.Info().Msg("Shutting down service components...")
	var finalErr error

	if err := w.apiServer.Shutdown(ctx); err != nil {
		w.logger.Error().Err(err).Msg("HTTP server shutdown failed.")
		finalErr = err
	}
	if err := w.processingService.Stop(ctx); err != nil {
		w.logger.Error().Err(err).Msg("Processing service shutdown failed.")
		finalErr = err
	}
	w.logger.Info().Msg("Service shutdown complete.")
	return finalErr
}

// Handler returns the underlying http.Handler for the service.
func (w *Wrapper) Handler() http.Handler {
	return w.apiServer.Handler
}
