# **go-routing-service**

[![Build Status](https://img.shields.io/badge/build-passing-brightgreen)](https://github.com/illmade-knight/go-routing-service)
[![Go Report Card](https://goreportcard.com/badge/github.com/illmade-knight/go-routing-service)](https://goreportcard.com/report/github.com/illmade-knight/go-routing-service)

The **go-routing-service** is a high-performance, secure, and scalable microservice responsible for real-time and asynchronous delivery of messages. It intelligently routes messages to online users via WebSockets and provides transient, Firestore-backed storage for offline users.

### **Architecture**

The service is built on a `go-microservice-base` library and features a robust, scalable architecture composed of two primary services that can be scaled independently.

* **Stateless API & Processing Service**: A standard HTTP server for message ingestion (`/send`), retrieval of offline messages (`/messages`), and observability. This service also runs the core background pipeline that processes all incoming messages.
* **Stateful WebSocket Service**: A dedicated server that manages persistent WebSocket connections, tracks real-time user presence in a shared cache (e.g., Redis, Firestore), and handles direct message delivery.

These services are decoupled via Google Cloud Pub/Sub, enabling high availability and horizontal scaling.


### **Key Features**

* **Dual-Service Architecture**: Cleanly separates stateless API/processing logic from stateful WebSocket management for independent scaling.
* **Horizontally Scalable**: Uses a Pub/Sub "Delivery Bus" and a shared Presence Cache for high-availability, multi-instance deployments.
* **Real-time & Offline Delivery**: Implements both WebSocket delivery for online users and Firestore-backed storage for offline users.
* **Configuration-Driven**: A single entrypoint uses an embedded `config.yaml` and environment variables to wire up dependencies for different environments.
* **Selectable Backends**: The `PresenceCache` can be configured to use Redis, Firestore, or an in-memory store.
* **Secure & Standardized**: Hardened against sender spoofing and uses centralized JWT authentication and configurable CORS policies.

### **Configuration**

The service is configured via `config.yaml` for non-secret values and environment variables for secrets. The `run_mode` in the config file is critical:

* `run_mode: "local"`: The service starts with **in-memory fakes** for all external dependencies (Pub/Sub, Firestore, etc.). This is ideal for fast, local development without any cloud connectivity.
* `run_mode: "production"`: The service starts with **real clients** for all dependencies (GCP Pub/Sub, Firestore, etc.) and is ready to serve production traffic.

### **Running the Service**

1.  **Configure `config.yaml`**: Set your `project_id` and other desired values in `cmd/runroutingservice/prod/config.yaml`.
2.  **Set Environment Variables**:
    ```shell
    export JWT_SECRET="a-very-secure-secret-key"
    ```
3.  **Run the Service**:
    ```shell
    # For production mode (requires gcloud auth)
    go run ./cmd/runroutingservice

    # To run in local mode, edit config.yaml to set run_mode: "local"
    ```

### **Testing**

The project contains a comprehensive test suite.

* **Run Unit Tests**:
    ```shell
    go test ./...
    ```
* **Run Integration & E2E Tests**: These tests require Docker to run emulators for GCP services.
    ```shell
    go test -tags=integration ./...
    ```