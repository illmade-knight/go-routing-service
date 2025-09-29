# **go-routing-service**

The **go-routing-service** is a high-performance, secure, and scalable microservice responsible for real-time and asynchronous delivery of messages. It intelligently routes messages to online users via WebSockets and provides transient, Firestore-backed storage for offline users.

### **Architecture**

The service is built on a go-microservice-base library and features a robust, scalable architecture composed of two primary services that can be scaled independently.

* **Stateless API & Processing Service**: A standard HTTP server for message ingestion (/send), retrieval of offline messages (/messages), and observability. This service also runs the core background pipeline that processes all incoming messages.
* **Stateful WebSocket Service**: A dedicated server that manages persistent WebSocket connections, tracks real-time user presence in a shared cache (e.g., Redis, Firestore), and handles direct message delivery.

These services are decoupled via Google Cloud Pub/Sub, enabling high availability and horizontal scaling.

#### **Intelligent Message Routing**

The core processing pipeline intelligently routes messages based on the recipient's status and device type:

* **Online Users**: Messages are sent directly to the real-time **Delivery Bus** for immediate delivery via WebSocket.
* **Offline Mobile Users**: A push notification request is sent to the go-notification-service, and the message is stored in Firestore.
* **Offline Web Users**: A real-time event is sent to the **Delivery Bus**, allowing the go-notification-service to forward it for in-app browser notifications. The message is also stored in Firestore.

### **Key Features**

* **Dual-Service Architecture**: Cleanly separates stateless API/processing logic from stateful WebSocket management for independent scaling.
* **Horizontally Scalable**: Uses a Pub/Sub "Delivery Bus" and a shared Presence Cache for high-availability, multi-instance deployments.
* **Multi-Platform Delivery**: Implements distinct routing for online, offline mobile, and offline web clients.
* **Efficient Web Client Notifications**: Leverages the real-time delivery bus to notify offline web clients, avoiding the complexity of browser push services for a more efficient in-app notification experience.
* **Configuration-Driven**: A single entrypoint uses an embedded config.yaml and environment variables to wire up dependencies for different environments.
* **Selectable Backends**: The PresenceCache can be configured to use Redis, Firestore, or an in-memory store.
* **Secure & Standardized**: Hardened against sender spoofing and uses centralized JWT authentication and configurable CORS policies.
* **Resilient**: Utilizes Dead-Letter Queues (DLQs) to handle "poison pill" messages and prevent pipeline blockage.

### **Configuration**

The service is configured via config.yaml for non-secret values and environment variables for secrets. The run\_mode in the config file is critical:

* run\_mode: "local": The service starts with **in-memory fakes** for all external dependencies (Pub/Sub, Firestore, etc.). This is ideal for fast, local development without any cloud connectivity.
* run\_mode: "production": The service starts with **real clients** for all dependencies (GCP Pub/Sub, Firestore, etc.) and is ready to serve production traffic.

### **Running the Service**

1. **Configure config.yaml**: Set your project\_id and other desired values in cmd/runroutingservice/prod/config.yaml.
2. **Set Environment Variables**:  
   export JWT\_SECRET="a-very-secure-secret-key"

3. **Run the Service**:  
   \# For production mode (requires gcloud auth)  
   go run ./cmd/runroutingservice

   \# To run in local mode, edit config.yaml to set run\_mode: "local"

### **Testing**

The project contains a comprehensive test suite.

* **Run Unit Tests**:  
  go test ./...

* **Run Integration & E2E Tests**: These tests require Docker to run emulators for GCP services.  
  go test \-tags=integration ./...  
