package api

import "net/http"

func CorsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// For production, you should restrict this to your frontend's domain.
		w.Header().Set("Access-Control-Allow-Origin", "http://localhost:4200")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")

		// THE FIX:
		// The browser's preflight OPTIONS request was failing because the
		// 'Authorization' header sent by our Angular interceptor was not
		// explicitly included in this list of allowed headers.
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-User-ID, Authorization")

		// This header is required when the frontend sends credentials (cookies).
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		// If it's a preflight (OPTIONS) request, respond with 200 OK and stop.
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Otherwise, pass control to the next handler in the chain (e.g., the JWT middleware).
		next.ServeHTTP(w, r)
	})
}
