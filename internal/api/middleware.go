package api

import "net/http"

func CorsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// For development, you can allow any origin with "*"
		// For production, you should restrict it to your frontend's domain.
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:4200")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		// REFACTOR: Add 'X-User-ID' to the list of allowed headers.
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User-ID")

		// If it's a preflight (OPTIONS) request, send a 200 OK and stop.
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Otherwise, call the next handler in the chain.
		next.ServeHTTP(w, r)
	})
}
