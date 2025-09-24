# **Roadmap**

* **1\. Horizontal Scaling for the WebSocket Server**
    * **What:** Enable running multiple instances of the stateful WebSocket server for high availability and load distribution.
    * **Why:** The current real-time delivery mechanism is local to each server instance. If you run more than one WebSocket server, a message processed by Server A cannot be delivered to a client connected to Server B. This makes the stateful layer a single point of failure and a scalability bottleneck.
    * **How:** Refactor the DeliveryProducer. Instead of delivering messages to its local in-memory ConnectionManager, it should publish them to a high-speed, brokered messaging system (like Redis Pub/Sub or a dedicated GCP Pub/Sub topic). Each WebSocket server instance would subscribe to this broker. When a message arrives, each instance checks if it holds the connection for the recipient and, if so, delivers the message.
* **2\. Dead-Letter Queue (DLQ) for the Message Pipeline**
    * **What:** Implement a Dead-Letter Queue for the main Pub/Sub subscription.
    * **Why:** A persistently malformed or problematic message (a "poison pill") could fail processing in the pipeline and get redelivered by Pub/Sub repeatedly. This can waste resources, trigger endless alerts, and potentially block valid messages from being processed.
    * **How:** Configure the IngressSubscription in GCP Pub/Sub to automatically forward any message that fails delivery a set number of times to a separate "dead-letter" topic. This isolates problematic messages for later inspection without halting the main pipeline.
* **3\. Distributed Tracing**
    * **What:** Integrate a distributed tracing framework like OpenTelemetry.
    * **Why:** The journey of a single message spans multiple components: the API server, Pub/Sub, the processing pipeline, and either Firestore or the WebSocket server. Without distributed tracing, diagnosing latency or finding the exact point of failure for a single message in a production environment is extremely difficult.
    * **How:** Add tracing middleware to the API handlers and inject trace context into Pub/Sub messages. The pipeline consumer would then extract this context to continue the trace, creating a single, unified view of a message's entire lifecycle.

These three items directly support the service's primary function of routing messages. They are foundational for achieving the availability, resilience, and observability required to run this service reliably in production.