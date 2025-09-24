# **go-routing-service**

The Routing Service is a high-performance, secure, and scalable microservice responsible for the real-time and asynchronous delivery of SecureEnvelope messages. It is a core component of the secure messaging ecosystem, intelligently routing messages to online users via WebSockets or persisting them for offline retrieval.

The service is built on the standardized go-microservice-base library, ensuring consistent lifecycle management, observability, and security patterns. It features a **"Dual Server" architecture** that cleanly separates stateless API handling from stateful, real-time WebSocket connections for maximum scalability and resilience.

## **Architecture: The Dual Server Model**

The service runs two independent servers within a single binary to cleanly separate concerns:

1. **Stateless API Server (e.g., port 8082):** A standard HTTP server responsible for:
    * Message ingestion (POST /send).
    * Retrieval of offline messages (GET /messages).
    * Standard observability endpoints (/healthz, /readyz, /metrics).
2. **Stateful WebSocket Server (e.g., port 8083):** A dedicated server that manages:
    * Persistent WebSocket connections from clients (GET /connect).
    * Real-time presence tracking.
    * Direct, in-memory delivery of messages to currently connected users.

## **Directory Structure**

The repository follows standard Go project layout conventions.

.  
├── cmd/  
│   └── runroutingservice.go     \# Assembles and runs BOTH servers  
├── internal/  
│   ├── api/                     \# Stateless HTTP API handlers (/send, /messages)  
│   ├── pipeline/                \# Core async message processing logic  
│   ├── platform/                \# Concrete adapters for Pub/Sub, Firestore, etc.  
│   └── realtime/                \# Stateful WebSocket connection manager & server  
├── pkg/  
│   └── routing/                 \# Public "contract" (domain models, interfaces, config)  
└── routingservice/  
└── service.go               \# The public library wrapper for the STATELSS service

## **Core Dataflows**

### **1\. Sending a Message**

This flow shows how a message is routed to either an online or offline user.

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

### **2\. Client Connection Lifecycle**

This flow shows how clients connect and retrieve messages.

graph TD  
subgraph Connection & Retrieval  
A\[Client App\] \-- JWT Auth \--\> B{WebSocket Server: GET /connect};  
B \-- Upgrades to WebSocket \--\> C\[Persistent Connection Established\];  
C \-- Updates \--\> D\[PresenceCache\];  
A \-- JWT Auth \--\> E{API Server: GET /messages};  
E \-- Retrieves from \--\> F\[Firestore\];  
F \-- Deletes stored messages \--\> E;  
E \-- Returns messages \--\> A;  
end

## **Project Status: Production Ready**

The service is feature-complete according to its development roadmap and is ready for production deployment.

### **Features Implemented**

* ✅ **Standardized Service Base**: Uses go-microservice-base for robust lifecycle management, graceful shutdowns, and consistent configuration.
* ✅ **Dual Server Architecture**: Cleanly separates stateless API and stateful WebSocket logic for scalability and resilience.
* ✅ **Full Observability**: Exposes standard endpoints: GET /healthz, GET /readyz, GET /metrics.
* ✅ **Centralized Security**: Uses standard, configurable JWT and CORS middleware from the base library.
* ✅ **Sender Spoofing Prevention**: The /send endpoint is secure and cannot be used to impersonate other users.
* ✅ **Real-time & Offline Delivery**: Fully implements both WebSocket delivery for online users and Firestore-backed storage for offline users.
* ✅ **Standardized Caching**: Uses the robust PresenceCache and Fetcher components from the go-dataflow library.
* ✅ **Structured Logging & Errors**: All logs and API errors are in a structured JSON format.

## **Running the Service**

1. **Set Environment Variables**:  
   export GCP\_PROJECT\_ID=my-gcp-project  
   export JWT\_SECRET=a-very-secure-secret-key  
   \# Optional: Override default ports  
   \# export API\_PORT=8082  
   \# export WEBSOCKET\_PORT=8083

2. **Run the Service**:  
   go run cmd/runroutingservice.go  
