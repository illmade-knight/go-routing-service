# **go-routing-service**

[![Build Status](https://img.shields.io/badge/build-passing-brightgreen)](https://github.com/illmade-knight/go-routing-service)
[![Go Report Card](https://goreportcard.com/badge/github.com/illmade-knight/go-routing-service)](https://goreportcard.com/report/github.com/illmade-knight/go-routing-service)

The **go-routing-service** is a high-performance, secure, and scalable microservice responsible for real-time and asynchronous delivery of messages. It intelligently routes messages to online users via WebSockets and provides transient, Firestore-backed storage for offline users.

### ### Architecture

The service is built on a standardized `go-microservice-base` library and features a robust, scalable architecture.

#### **Dual Server Model**
The service runs two independent servers to cleanly separate concerns:
* **Stateless API Server**: A standard HTTP server for message ingestion (`/send`), retrieval of offline messages (`/messages`), and observability endpoints (`/healthz`, `/metrics`).
* **Stateful WebSocket Server**: A dedicated server that manages persistent WebSocket connections, tracks real-time user presence, and handles direct message delivery.

#### **Scalable Real-Time Delivery**
To support high availability and horizontal scaling, the WebSocket servers are decoupled from the main processing pipeline via a **Pub/Sub "Delivery Bus"**. When a message needs to be delivered in real-time, it's published to a broadcast topic. Each WebSocket server instance consumes from this topic and delivers the message only if it is managing that specific user's connection.

### ### Key Features
* **Dual Server Architecture**: Cleanly separates stateless API and stateful WebSocket logic.
* **Horizontally Scalable WebSocket Layer**: Uses a Pub/Sub "Delivery Bus" for high-availability, multi-instance deployments.
* **Real-time & Transient Offline Delivery**: Implements both WebSocket delivery and Firestore-backed storage for offline messages.
* **Declarative Configuration**: Uses an embedded `config.yaml` for non-secret configuration and environment variables for secrets, providing secure and consistent deployments.
* **Selectable Backends**: The `PresenceCache` can be configured to use Redis, Firestore, or an in-memory store to fit different deployment needs.
* **Secure & Standardized**: Hardened against sender spoofing and uses centralized JWT authentication and configurable CORS policies.

### ### Configuration
The service is configured through two primary sources:
1.  **`cmd/runroutingservice/config.yaml`**: This embedded file contains non-secret configuration like port numbers, topic names, and backend choices.
2.  **Environment Variables**: Secrets, primarily `JWT_SECRET`, must be provided via the environment.

The `RUN_MODE` variable in `config.yaml` controls the wiring of internal components:
* `local`: The real-time delivery bus runs in-memory for simple, single-instance local testing against real cloud dependencies.
* `production`: The real-time delivery bus uses GCP Pub/Sub for multi-instance scalability.

### ### Running the Service
For all runs, ensure you are authenticated to Google Cloud (e.g., `gcloud auth application-default login`).

1.  **Configure `config.yaml`**: Set your `project_id` and other desired values.
2.  **Set Environment Variables**:
    ```shell
    export JWT_SECRET="a-very-secure-secret-key"
    ```
3.  **Run the Service**:
    ```shell
    go run ./cmd/runroutingservice
    ```