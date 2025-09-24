// Package realtime provides components for managing real-time client connections.
package realtime

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/illmade-knight/go-dataflow/pkg/cache"
	"github.com/illmade-knight/go-microservice-base/pkg/middleware"
	"github.com/illmade-knight/go-routing-service/pkg/routing"
	"github.com/illmade-knight/go-secure-messaging/pkg/urn"
	"github.com/rs/zerolog"
)

// ConnectionManager manages all active WebSocket connections and user presence.
// It runs its own dedicated HTTP server.
type ConnectionManager struct {
	server           *http.Server
	upgrader         websocket.Upgrader
	presenceCache    cache.PresenceCache[urn.URN, routing.ConnectionInfo]
	deliveryProducer routing.DeliveryProducer
	logger           zerolog.Logger
	jwtSecret        string
	instanceID       string

	mu          sync.RWMutex
	connections map[string]*websocket.Conn
}

// NewConnectionManager creates a new, fully configured ConnectionManager.
func NewConnectionManager(
	listenAddr, jwtSecret string,
	presenceCache cache.PresenceCache[urn.URN, routing.ConnectionInfo],
	deliveryProducer routing.DeliveryProducer,
	logger zerolog.Logger,
) *ConnectionManager {
	cm := &ConnectionManager{
		presenceCache:    presenceCache,
		deliveryProducer: deliveryProducer,
		logger:           logger.With().Str("component", "ConnectionManager").Logger(),
		jwtSecret:        jwtSecret,
		instanceID:       uuid.NewString(), // Each instance gets a unique ID
		connections:      make(map[string]*websocket.Conn),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true }, // Allow all for now
		},
	}
	mux := http.NewServeMux()
	jwtAuth := middleware.NewJWTAuthMiddleware(cm.jwtSecret)
	mux.Handle("/connect", jwtAuth(http.HandlerFunc(cm.connectHandler)))
	cm.server = &http.Server{Addr: listenAddr, Handler: mux}
	return cm
}

// Start runs the WebSocket server. This is a blocking call.
func (cm *ConnectionManager) Start(ctx context.Context) error {
	cm.logger.Info().Str("address", cm.server.Addr).Msg("WebSocket server starting...")
	if err := cm.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully disconnects clients and stops the server.
func (cm *ConnectionManager) Shutdown(ctx context.Context) error {
	cm.logger.Info().Msg("Shutting down WebSocket server...")
	cm.mu.Lock()
	defer cm.mu.Unlock()

	for userStr := range cm.connections {
		userURN, _ := urn.Parse(userStr)
		if err := cm.presenceCache.Delete(context.Background(), userURN); err != nil {
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
	if err := cm.presenceCache.Delete(context.Background(), userURN); err != nil {
		cm.logger.Error().Err(err).Str("user", userURN.String()).Msg("Failed to delete presence cache on disconnect")
	}
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
		if _, _, err := conn.ReadMessage(); err != nil {
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
	if err := cm.presenceCache.Set(context.Background(), userURN, info); err != nil {
		cm.logger.Error().Err(err).Str("user", userURN.String()).Msg("Failed to set presence cache on connect")
	}
}
