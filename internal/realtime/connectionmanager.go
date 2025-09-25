// Package realtime provides components for managing real-time client connections.
package realtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-dataflow/pkg/messagepipeline"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/internal/pipeline"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/transport"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/encoding/protojson"
)

// ConnectionManager manages all active WebSocket connections and user presence.
// It runs its own dedicated HTTP server and a message pipeline for consuming real-time
// delivery messages from the message bus.
type ConnectionManager struct {
	server           *http.Server
	upgrader         websocket.Upgrader
	presenceCache    cache.PresenceCache[urn.URN, routing.ConnectionInfo]
	deliveryPipeline *messagepipeline.StreamingService[transport.SecureEnvelope]
	logger           zerolog.Logger
	jwtSecret        string
	instanceID       string

	mu          sync.RWMutex
	connections map[string]*websocket.Conn
}

// NewConnectionManager creates a new, fully configured ConnectionManager.
func NewConnectionManager(
	listenAddr string,
	jwtSecret string,
	presenceCache cache.PresenceCache[urn.URN, routing.ConnectionInfo],
	deliveryConsumer messagepipeline.MessageConsumer,
	logger zerolog.Logger,
) (*ConnectionManager, error) {
	instanceID := uuid.NewString()
	cmLogger := logger.With().Str("component", "ConnectionManager").Str("instanceID", instanceID).Logger()

	cm := &ConnectionManager{
		presenceCache: presenceCache,
		logger:        cmLogger,
		jwtSecret:     jwtSecret,
		instanceID:    instanceID,
		connections:   make(map[string]*websocket.Conn),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			}, // Allow all for now
		},
	}

	// 1. Assemble the internal pipeline for processing real-time delivery messages.
	// This pipeline consumes messages from the delivery bus and processes them locally.
	deliveryPipeline, err := messagepipeline.NewStreamingService[transport.SecureEnvelope](
		messagepipeline.StreamingServiceConfig{
			NumWorkers: 1,
		}, // One worker is sufficient for local delivery processing.
		deliveryConsumer,
		pipeline.EnvelopeTransformer, // Reuse the existing transformer from the main pipeline.
		cm.deliveryProcessor,         // Use the new local delivery processor method.
		cmLogger,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create delivery pipeline for connection manager: %w", err)
	}
	cm.deliveryPipeline = deliveryPipeline

	// 2. Set up the HTTP server for WebSocket connections.
	mux := http.NewServeMux()
	jwtAuth := middleware.NewJWTAuthMiddleware(cm.jwtSecret)
	mux.Handle("/connect", jwtAuth(http.HandlerFunc(cm.connectHandler)))
	cm.server = &http.Server{
		Addr:    listenAddr,
		Handler: mux,
	}

	return cm, nil
}

// Start runs the WebSocket server and the internal delivery pipeline. This is a blocking call.
func (cm *ConnectionManager) Start(ctx context.Context) error {
	cm.logger.Info().Msg("Starting internal delivery pipeline...")
	err := cm.deliveryPipeline.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start delivery pipeline: %w", err)
	}

	cm.logger.Info().Str("address", cm.server.Addr).Msg("WebSocket server starting...")
	err = cm.server.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully disconnects clients and stops the server and pipeline.
func (cm *ConnectionManager) Shutdown(ctx context.Context) error {
	cm.logger.Info().Msg("Shutting down ConnectionManager...")

	// Stop the internal pipeline first to prevent processing new messages.
	err := cm.deliveryPipeline.Stop(ctx)
	if err != nil {
		cm.logger.Error().Err(err).Msg("Failed to gracefully stop delivery pipeline.")
	}

	cm.logger.Info().Msg("Shutting down WebSocket server...")
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for userStr := range cm.connections {
		userURN, _ := urn.Parse(userStr)
		// Use a background context for cleanup as the parent context may be expired.
		err = cm.presenceCache.Delete(context.Background(), userURN)
		if err != nil {
			cm.logger.Error().Err(err).Str("user", userURN.String()).Msg("Failed to delete presence cache on shutdown")
		}
	}
	return cm.server.Shutdown(ctx)
}

// Mux returns the underlying ServeMux, primarily for testing.
func (cm *ConnectionManager) Mux() *http.ServeMux {
	return cm.server.Handler.(*http.ServeMux)
}

// Get retrieves a connection for a user.
func (cm *ConnectionManager) Get(userURN urn.URN) (*websocket.Conn, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	conn, ok := cm.connections[userURN.String()]
	return conn, ok
}

// Remove unregisters a connection and deletes the user's presence.
func (cm *ConnectionManager) Remove(userURN urn.URN) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	delete(cm.connections, userURN.String())
	err := cm.presenceCache.Delete(context.Background(), userURN)
	if err != nil {
		cm.logger.Error().Err(err).Str("user", userURN.String()).Msg("Failed to delete presence cache on disconnect")
	}
}

// deliveryProcessor is the StreamProcessor for the internal delivery pipeline.
// It checks if the recipient is connected to this specific server instance and delivers the message.
func (cm *ConnectionManager) deliveryProcessor(ctx context.Context, original messagepipeline.Message, envelope *transport.SecureEnvelope) error {
	recipientURN := envelope.RecipientID
	conn, ok := cm.Get(recipientURN)
	if !ok {
		// This is not an error. It's expected that another instance will handle the delivery.
		cm.logger.Debug().Str("recipient", recipientURN.String()).Msg("Message received for user not connected to this instance.")
		return nil
	}

	protoEnvelope := transport.ToProto(envelope)
	payload, err := protojson.Marshal(protoEnvelope)
	if err != nil {
		// This is a processing error; returning it will cause the message to be Nacked.
		return fmt.Errorf("failed to marshal envelope for websocket delivery: %w", err)
	}

	err = conn.WriteMessage(websocket.TextMessage, payload)
	if err != nil {
		cm.logger.Error().Err(err).Str("recipient", recipientURN.String()).Msg("Failed to write message to WebSocket. Removing connection.")
		cm.Remove(recipientURN) // Clean up the stale connection.
		return err
	}

	cm.logger.Info().Str("recipient", recipientURN.String()).Msg("Successfully delivered message via WebSocket.")
	return nil
}

// connectHandler upgrades the HTTP connection to a WebSocket and manages its lifecycle.
func (cm *ConnectionManager) connectHandler(w http.ResponseWriter, r *http.Request) {
	authedUserID, ok := middleware.GetUserIDFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userURN, err := urn.New(urn.SecureMessaging, urn.EntityTypeUser, authedUserID)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	conn, err := cm.upgrader.Upgrade(w, r, nil)
	if err != nil {
		cm.logger.Error().Err(err).Msg("Failed to upgrade connection.")
		return
	}
	defer conn.Close()

	cm.add(userURN, conn)
	defer cm.Remove(userURN)

	cm.logger.Info().Str("user", userURN.String()).Msg("User connected via WebSocket.")

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

// add registers a new connection and sets the user's presence.
func (cm *ConnectionManager) add(userURN urn.URN, conn *websocket.Conn) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	cm.connections[userURN.String()] = conn
	info := routing.ConnectionInfo{
		ServerInstanceID: cm.instanceID,
		ConnectedAt:      time.Now().Unix(),
	}
	err := cm.presenceCache.Set(context.Background(), userURN, info)
	if err != nil {
		cm.logger.Error().Err(err).Str("user", userURN.String()).Msg("Failed to set presence cache on connect")
	}
}
