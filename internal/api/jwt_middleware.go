package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// contextKey is a private type to prevent collisions with other context keys.
type contextKey string

// userContextKey is the key used to store the authenticated user's ID in the request context.
const userContextKey contextKey = "userID"

func (a *API) JwtAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Create a logger with context for this specific request.
		logger := a.logger.With().Str("path", r.URL.Path).Logger()
		logger.Info().Msg("[JWT Middleware] Executing...")

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// LOG THE FAILURE
			logger.Warn().Msg("[JWT Middleware] Unauthorized: Missing Authorization header")
			http.Error(w, "Unauthorized: Missing Authorization header", http.StatusUnauthorized)
			return
		}

		tokenString, found := strings.CutPrefix(authHeader, "Bearer ")
		if !found {
			// LOG THE FAILURE
			logger.Warn().Msg("[JWT Middleware] Unauthorized: Invalid token format, 'Bearer ' prefix not found.")
			http.Error(w, "Unauthorized: Invalid token format", http.StatusUnauthorized)
			return
		}

		logger.Debug().Msg("[JWT Middleware] Token extracted from header.")

		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(a.jwtSecret), nil
		})

		if err != nil {
			// LOG THE FAILURE, including the specific parsing error.
			logger.Warn().Err(err).Msg("[JWT Middleware] Unauthorized: Token parsing or validation failed.")
			http.Error(w, "Unauthorized: Invalid token", http.StatusUnauthorized)
			return
		}

		if claims, ok := token.Claims.(jwt.MapClaims); ok && token.Valid {
			userID, ok := claims["sub"].(string)
			if !ok || userID == "" {
				// LOG THE FAILURE
				logger.Warn().Interface("claims", claims).Msg("[JWT Middleware] Unauthorized: Invalid 'sub' claim in token.")
				http.Error(w, "Unauthorized: Invalid user ID in token", http.StatusUnauthorized)
				return
			}

			// LOG SUCCESS
			logger.Info().Str("userID", userID).Msg("[JWT Middleware] Token validated successfully.")
			ctx := context.WithValue(r.Context(), userContextKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		} else {
			// LOG THE FAILURE
			logger.Warn().Bool("token_valid", token.Valid).Interface("claims", token.Claims).Msg("[JWT Middleware] Unauthorized: Token claims are invalid or token is not valid.")
			http.Error(w, "Unauthorized: Invalid token claims", http.StatusUnauthorized)
		}
	})
}

// GetUserIDFromContext safely retrieves the user ID from the request context.
func GetUserIDFromContext(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userContextKey).(string)
	return userID, ok
}
