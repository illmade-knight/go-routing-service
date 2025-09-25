# **Roadmap to Production**

This document outlines the final engineering tasks required to make the `go-routing-service` fully production-ready. The core architecture is complete, and these steps focus on implementing the final concrete dependencies and hardening the service for production traffic.

### ### Phase 1: Complete Core Service Implementations (Highest Priority)

This phase involves replacing the final placeholder components in `cmd/runroutingservice/main.go` with their concrete, production-ready implementations.

* **1. Implement the `PushNotifier`**
    * **Task**: Create the concrete implementation for the `routing.PushNotifier` interface, likely in a new `internal/platform/fcm/notifier.go` package. This will involve integrating with a real service like Firebase Cloud Messaging (FCM) to send notifications to mobile and web clients.
    * **Current State**: The service currently uses a `loggingPushNotifier` placeholder that only logs its activity.
    * **Acceptance Criteria**: The `loggingPushNotifier` is removed from `main.go` and replaced with the real implementation. Push notifications are successfully sent to devices when an offline user receives a message.

### ### Phase 2: Production Hardening & Observability

Once the core components are in place, the focus shifts to resilience and monitoring.

* **2. Configure Dead-Letter Queues (DLQ)**
    * **Task**: Configure DLQs for the primary `IngressSubscription` and the ephemeral `DeliverySubscription`s created by each server instance.
    * **Purpose**: This is a critical resilience pattern to isolate and handle "poison pill" messages (messages that repeatedly fail to process), ensuring the main and real-time delivery pipelines remain healthy.

* **3. Integrate Distributed Tracing**
    * **Task**: Integrate OpenTelemetry throughout the service. This involves adding tracing middleware to the API, injecting/extracting trace context from Pub/Sub messages, and adding spans within the processing pipelines and WebSocket components.
    * **Purpose**: To provide end-to-end visibility of a message's lifecycle, which is essential for debugging latency and failures in a distributed environment.

### ### Phase 3: Final Configuration & Deployment

* **4. Secure CORS Policy**
    * **Task**: Update the `config.yaml` with a restrictive list of `allowed_origins` for the production environment, replacing the permissive `"*"` default.
    * **Purpose**: To ensure that only authorized web clients can interact with the API.

* **5. Finalize Configuration Values**
    * **Task**: Review and set the final production values for all keys in `config.yaml` (e.g., `project_id`, topic names, cache settings) and prepare the corresponding `JWT_SECRET` for deployment.