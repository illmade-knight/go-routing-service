# **go-routing-service**

The **go-routing-service** is a high-performance, secure, and scalable microservice responsible for the real-time and asynchronous delivery of SecureEnvelope messages. It intelligently routes messages to online users via WebSockets and provides **transient storage for offline users**, with messages being deleted immediately upon successful retrieval. It is a core component of a secure messaging ecosystem.

---

## **\#\# Architecture**

The service is built on a standardized go-microservice-base library, ensuring consistent lifecycle management, observability, and security patterns. It features two key architectural patterns for maximum scalability and resilience.

### **The Dual Server Model**

The service runs two independent servers within a single binary to cleanly separate concerns:

1. **Stateless API Server (e.g., :8082)**: A standard HTTP server responsible for message ingestion, retrieval of offline messages, and standard observability endpoints (/healthz, /readyz, /metrics).
2. **Stateful WebSocket Server (e.g., :8083)**: A dedicated server that manages persistent WebSocket connections, real-time presence tracking, and direct, in-memory delivery of messages to currently connected users.

### **Asynchronous Processing Pipeline**

Incoming messages are not processed synchronously. Instead, they are published to a Google Pub/Sub topic and processed by a scalable, multi-stage pipeline, ensuring the API remains highly responsive.

---

## **\#\# Core Dataflows**

### **1\. Sending a Message**

This flow shows how a message is routed to either an online or offline user.

Code snippet

graph TD  
A\[Client App\] \-- JWT Auth \--\> B{API Server: POST /send};  
B \-- Publishes \--\> C\[Pub/Sub: Ingress Topic\];  
C \--\> D\[Processing Pipeline\];  
D \--\> E{Check PresenceCache};  
subgraph Real-time Delivery  
E \-- User Online \--\> F\[WebSocket Server\];  
F \-- Delivers to Client \--\> G\[Connected Client\];  
end  
subgraph Offline Delivery  
E \-- User Offline \--\> H{Store in Firestore};  
H \--\> I\[Send Push Notification\];  
end

### **2\. Client Connection & Message Retrieval**

This flow shows how clients connect and retrieve any messages stored while they were offline. Messages are deleted from the store immediately after being retrieved by the client.

Code snippet

graph TD  
subgraph Connection & Retrieval  
A\[Client App\] \-- JWT Auth \--\> B{WebSocket Server: GET /connect};  
B \-- Upgrades to WebSocket \--\> C\[Persistent Connection Established\];  
C \-- Updates \--\> D\[PresenceCache\];  
A \-- JWT Auth \--\> E{API Server: GET /messages};  
E \-- Retrieves and Deletes from \--\> F\[Firestore\];  
E \-- Returns messages \--\> A;  
end

---

## **\#\# Key Features**

* **Dual Server Architecture**: Cleanly separates stateless API and stateful WebSocket logic for scalability and resilience.
* **Real-time & Transient Offline Delivery**: Fully implements WebSocket delivery for online users and Firestore-backed transient storage for offline users, with messages deleted on retrieval.
* **Secure & Standardized**: Uses centralized JWT authentication and CORS middleware. It is hardened against sender spoofing by sourcing the sender's identity directly from the validated JWT token.
* **Robust Data Handling**: Standardizes on **Google Protobuf** as the canonical data model for all messages. This ensures end-to-end data consistency during transport (API, Pub/Sub, WebSockets) and persistence (Firestore).
* **High Observability**: Exposes standard endpoints for monitoring and health checks: GET /healthz, GET /readyz, and GET /metrics.
* **Asynchronous & Scalable**: Decouples message ingestion from processing using a highly scalable Pub/Sub pipeline with configurable worker pools.

---

## **\#\# Configuration**

The service is configured using environment variables.

| Variable | Description | Required | Default |
| :---- | :---- | :---- | :---- |
| GCP\_PROJECT\_ID | The Google Cloud project ID for Pub/Sub and Firestore. | **Yes** |  |
| JWT\_SECRET | The secret key for validating JWT tokens. | **Yes** |  |
| API\_PORT | The port for the stateless HTTP API server. | No | 8082 |
| WEBSOCKET\_PORT | The port for the stateful WebSocket server. | No | 8083 |

---

## **\#\# Running the Service**

1. **Set Environment Variables**:  
   Shell  
   export GCP\_PROJECT\_ID=my-gcp-project  
   export JWT\_SECRET=a-very-secure-secret-key

   \# Optional: Override default ports  
   \# export API\_PORT=8082  
   \# export WEBSOCKET\_PORT=8083

2. **Run the Service**:  
   Shell  
   go run cmd/runroutingservice.go

---

## **\#\# Testing**

The project includes a comprehensive, multi-layered testing strategy, including unit tests with mocks and full integration tests that use **Firestore and Pub/Sub emulators** to validate end-to-end dataflows in a realistic environment.