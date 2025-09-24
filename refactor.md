# **Routing Service: Refactor Plan**

The objective is to standardize the go-routing-service by integrating the go-microservice-base library and to harden its security by addressing findings from the security audit.

This refactor will bring the service in line with our common patterns, making it more robust, secure, and easier to operate.

## **Phase 1: Standardization & Boilerplate Reduction**

This phase focuses on replacing custom service lifecycle code with the standardized components from the base library.

1. **Integrate BaseServer**:
    * Modify routingservice/service.go.
    * The routingservice.Wrapper will embed microservice.BaseServer.
    * All custom HTTP server management, including Start, Shutdown, and GetHTTPPort, will be removed from the Wrapper. The Wrapper will gain a new Shutdown method to gracefully stop the background processing pipeline before calling the embedded BaseServer.Shutdown.
2. **Update Main Executable**:
    * Modify cmd/runroutingservice.go.
    * The main function will be refactored to match the standard pattern:
        1. Load configuration.
        2. Inject dependencies.
        3. Create the service Wrapper.
        4. Start the service in a goroutine.
        5. Listen for OS signals and manage the graceful shutdown sequence.
3. **Standardize Error Handling**:
    * Modify internal/api/routing\_handlers.go.
    * Replace all http.Error calls with the new response.WriteJSONError helper from the base library to ensure all API errors are structured JSON.
4. **Adopt Centralized Middleware**:
    * Delete internal/api/jwt\_middleware.go and internal/api/middleware.go from this project.
    * Update routingservice/service.go to import and use the new middleware.NewJWTAuthMiddleware and middleware.CorsMiddleware from the go-microservice-base library.

## **Phase 2: Security Hardening**

This phase addresses the critical vulnerability identified in the security audit.

1. **Fix Sender Spoofing Vulnerability**:
    * Modify internal/api/routing\_handlers.go \-\> SendHandler.
    * The handler **must** ignore the SenderID field from the client's request payload.
    * It will create a new URN for the sender based on the authedUserID retrieved from the JWT context (middleware.GetUserIDFromContext).
    * It will **overwrite** the SenderID on the SecureEnvelope with this newly created, trusted URN before publishing the message to the pipeline.
2. **Update Unit Tests**:
    * Modify internal/api/routing\_handlers\_test.go.
    * Update tests to reflect the new standardized JSON error responses.
    * Add a new test case for SendHandler to explicitly verify that the sender's ID is correctly overwritten, confirming the security fix.