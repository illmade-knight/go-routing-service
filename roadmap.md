# **Roadmap to Production**

This document outlines the final engineering tasks required to make the `go-routing-service` fully production-ready. The core architecture is complete, and these steps focus on hardening the service, improving test coverage, and implementing the final concrete dependencies.

### **Phase 1: Code Health & Resilience (Highest Priority)**

* **1. Consolidate Application Entrypoints**
    * **Task**: Deprecate and remove the redundant `cmd/runlocalroutingservice` executable. Enhance the `"local"` run mode in `cmd/runroutingservice` to use the full suite of in-memory fakes, making it the single, canonical entrypoint for the service.
    * **Purpose**: To reduce code duplication, eliminate configuration drift, and simplify the developer experience.

* **2. Configure Dead-Letter Queues (DLQ)**
    * **Task**: Configure DLQs for the primary `IngressSubscription`.
    * **Purpose**: This is a **critical resilience pattern** to isolate and handle "poison pill" messages, ensuring a single malformed message cannot block the entire processing pipeline.

* **3. Enhance Test Coverage**
    * **Task**: Add the following missing tests:
        1.  **Unit tests** for the stateful `realtime.ConnectionManager` to validate its concurrency and lifecycle logic.
        2.  **Integration tests** for the "online" message delivery path via the real-time delivery bus.
        3.  **API failure-case tests** to verify correct `4xx` error handling for invalid input.
    * **Purpose**: To increase confidence in the correctness of stateful components and ensure the service behaves predictably under failure conditions.

### **Phase 2: Core Service Implementation**

* **4. Implement the Production `PushNotifier`**
    * **Task**: Create the concrete implementation for the `routing.PushNotifier` interface to integrate with a service like Firebase Cloud Messaging (FCM).
    * **Purpose**: To replace the current placeholder and enable the delivery of push notifications to offline users.

### **Phase 3: Observability & Deployment**

* **5. Integrate Distributed Tracing**
    * **Task**: Integrate OpenTelemetry throughout the service, including API middleware and Pub/Sub message propagation.
    * **Purpose**: To provide end-to-end visibility of a message's lifecycle for debugging latency and failures.

* **6. Secure CORS Policy**
    * **Task**: Update `config.yaml` with a restrictive list of `allowed_origins` for the production environment.
    * **Purpose**: To ensure that only authorized web clients can interact with the API.