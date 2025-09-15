# **Routing Service**

The Routing Service is a high-performance, secure, and scalable microservice responsible for the real-time delivery of SecureEnvelope messages between clients. It acts as a "dumb pipe," routing messages based on their unencrypted headers with zero knowledge of the encrypted payload, ensuring end-to-end security.

The architecture is built around a **Unified Ingestion Pipeline** using the go-dataflow library. This allows multiple transport protocols (e.g., HTTP, MQTT) to produce messages to a central message bus, which are then consumed by a single, scalable backend that performs the core routing logic.

---

## **Directory Structure**

The repository is organized using standard Go project layout conventions to maintain a clear separation between public APIs, internal logic, and the final executable.

├── cmd/    
│   └── routing-service/    
│       └── main.go              \# Assembles and runs the service    
├── e2e/    
│   └── routing\_e2e\_test.go      \# End-to-end integration tests    
├── internal/    
│   ├── api/                     \# Private HTTP API handlers    
│   ├── pipeline/                \# Core message processing stages (transform, process)    
│   └── platform/                \# Concrete adapters for external services (e.g., Pub/Sub, Firestore)  
├── pkg/    
│   └── routing/                 \# Public "contract" (domain models, interfaces, config)    
├── routingservice/    
│   └── service.go               \# The public, embeddable service library/wrapper    
└── test/    
└── e2e\_helpers.go           \# Public test helpers for E2E tests

* **cmd/**: Contains the executable entry point for the service.
* **e2e/**: Holds the end-to-end integration tests that treat the service as a black box.
* **internal/**: Contains all private application logic, including HTTP handlers, the core pipeline, and concrete platform adapters.
* **pkg/**: Holds the service's public contract, defining the data structures, interfaces, and configuration.
* **routingservice/**: The primary, public-facing service library that assembles the internal components into a runnable service.
* **test/**: Provides public helper functions for testing, allowing E2E tests to create dependencies without violating the internal package boundary.

---

## **Core Dataflow**

The service operates on two primary dataflows: sending a message into the pipeline and retrieving stored messages.

### **1\. Sending a Message (POST /send)**

This flow handles the ingestion and routing of a new message.

graph TD  
A\[Client App\] \--\> B{HTTP API: POST /send};  
B \--\> C\[Pub/Sub: Ingress Topic\];  
C \--\> D\[Processing Pipeline\];  
D \--\> E{Check Presence Cache};  
E \-- User Online \--\> F\[Pub/Sub: Delivery Topic\];  
E \-- User Offline \--\> G{Store Message in Firestore};  
G \--\> H\[Send Push Notification\];

### **2\. Retrieving Offline Messages (GET /messages)**

This flow allows a client to fetch messages that were stored while it was offline.

graph TD  
A\[Client App\] \--\> B{HTTP API: GET /messages};  
B \--\> C{Message Store: Firestore};  
C \-- Fetches Messages \--\> B;  
B \-- Returns Messages \--\> A;  
B \-- Marks as Delivered \--\> C;

---

## **Project Status & Roadmap**

The service now has a complete, end-to-end implementation for asynchronous, offline-first messaging. A significant portion of **Phase 1** is complete.

### **What's Complete (Phase 1\)**

* ✅ **HTTP Ingestion**: The POST /send endpoint is implemented and publishes messages to the central message bus.
* ✅ **Message Processing Pipeline**: A fully functional StreamingService is in place, using a GooglePubsubConsumer, a Transformer, and a Processor.
* ✅ **Core Routing Logic**: The Processor contains the complete logic for checking user presence and deciding between online and offline delivery, including **persisting messages for offline users**.
* ✅ **Offline Message Persistence**: The pipeline now uses a MessageStore interface, with a concrete **Firestore implementation**, to save messages for offline users.
* ✅ **Message Retrieval API**: The GET /messages endpoint is implemented, allowing clients to securely fetch and clear their stored messages after coming online.

### **Ready for Integration (Phase 1 Data Adapters)**

The next step is to replace the in-memory/mock data adapters with robust, scalable implementations from the go-dataflow library within cmd/routing-service/main.go.

* ➡️ **Redis for Presence**: The PresenceCache dependency can be satisfied by instantiating cache.NewRedisCache\[string, routing.ConnectionInfo\]. A cache miss definitively means the user is offline.
* ➡️ **Firestore for Device Tokens**: The DeviceTokenFetcher dependency can be satisfied by instantiating cache.NewFirestore\[string, \[\]routing.DeviceToken\]. This can be layered with a Redis or in-memory cache for better performance.

### **Future Roadmap**

* ❌ **MQTT Ingestion**: An MQTT broker needs to be added as another entry point to the unified ingestion topic.
* ❌ **Phase 2 \- Real-time Delivery**: The real-time connection manager, including the **WebSocket handler** for delivering messages to online clients, needs to be implemented.
* ❌ **Phase 2 \- Additional APIs**: The GET /connect (WebSocket upgrade) and POST /register-device endpoints are not yet created.
* ❌ **Phase 3/4 \- Production Hardening**: Features like JWT authentication, observability (metrics, tracing), and deployment configurations are pending.