# **Strategy for Handling "Poison Pill" Messages**

This document outlines the problem of "poison pill" messages in the go-routing-service's message processing pipeline and details the strategy to mitigate this critical reliability risk using a Dead-Letter Queue (DLQ).

### **1\. The Problem: What is a "Poison Pill"?**

In a message queue-based system like the go-routing-service, a "poison pill" is a message that a consumer cannot process successfully. This failure can be due to various reasons, such as:

* **Malformed Payload**: The message data is corrupted or does not conform to the expected schema (e.g., invalid JSON/Protobuf).
* **Application Bug**: A specific edge case in the message data triggers a panic or an unhandled error in the processing logic.
* **Downstream Dependency Failure**: A required external service (e.g., a database) is misconfigured or unavailable in a way that causes a persistent, message-specific error.

The current messagepipeline consumer is configured to negatively acknowledge (Nack) a message if the processing function returns an error. Google Cloud Pub/Sub, by design, will then attempt to redeliver that message. This creates a dangerous loop:

1. The consumer pulls the poison pill message.
2. The processing logic fails and returns an error.
3. The consumer Nacks the message.
4. Pub/Sub immediately redelivers the same message.
5. The cycle repeats, blocking the subscription.

The consequence is that a single malformed message can halt the entire pipeline, preventing any subsequent, valid messages from ever being processed. This is a single point of failure that is unacceptable for a production service.

### **2\. The Rationale: Why a Dead-Letter Queue is the Solution**

The industry-standard and most robust solution to the poison pill problem is to implement a **Dead-Letter Queue (DLQ)**, also known as a dead-letter topic in GCP.

A DLQ is a separate, dedicated topic where messages are automatically sent by Pub/Sub after they have failed a configurable number of delivery attempts.

This strategy provides three key benefits:

* **Service Reliability**: It immediately and automatically removes the poison pill message from the main ingress-sub subscription. This unblocks the pipeline, allowing it to continue processing healthy messages and ensuring the service remains available.
* **No Data Loss**: The problematic message is not discarded. It is safely moved to the DLQ, preserving the data for later analysis. This is crucial for debugging and potential manual reprocessing.
* **Asynchronous Investigation**: An on-call engineer can be alerted that a message has entered the DLQ. They can then inspect the message's content and attributes offline to diagnose the root cause of the failure without the pressure of a live service outage.

### **3\. The Execution Plan: How to Implement the DLQ**

Implementing the DLQ requires changes in both the cloud infrastructure and the application's startup code.

#### **Step 1: Infrastructure Provisioning**

A new Pub/Sub topic must be created to serve as the dead-letter topic.

1. **Create the DLQ Topic**: Create a new Pub/Sub topic named ingress-topic-dlq.
2. **Grant Permissions**: The Google-managed Pub/Sub service account (service-{PROJECT\_NUMBER}@gcp-sa-pubsub.iam.gserviceaccount.com) needs permissions to publish to this new DLQ topic. It must be granted the roles/pubsub.publisher IAM role on the ingress-topic-dlq topic.

#### **Step 2: Application Code Refactor**

The application code that creates the primary subscription must be modified to attach the dead-letter policy.

* **File to Refactor**: cmd/runroutingservice/main.go (within the setupProductionDeliveryBus or a similar setup function for the ingress subscription).
* **Logic Change**: When the SubscriptionAdminClient.CreateSubscription method is called for the ingress-subscription, the pubsubpb.Subscription request object must be configured with a DeadLetterPolicy.

**Example Code Snippet:**

// Inside cmd/runroutingservice/main.go

// ... client and topic creation ...

dlqTopicName := fmt.Sprintf("projects/%s/topics/ingress-topic-dlq", cfg.ProjectID)  
subName := fmt.Sprintf("projects/%s/subscriptions/%s", cfg.ProjectID, cfg.IngressSubscriptionID)  
topicName := fmt.Sprintf("projects/%s/topics/%s", cfg.ProjectID, cfg.IngressTopicID)

\_, err \= subAdminClient.CreateSubscription(ctx, \&pubsubpb.Subscription{  
Name:  subName,  
Topic: topicName,  
AckDeadlineSeconds: 10, // Or other appropriate value  
DeadLetterPolicy: \&pubsubpb.DeadLetterPolicy{  
DeadLetterTopic:     dlqTopicName,  
MaxDeliveryAttempts: 5, // A reasonable starting point  
},  
})  
if err \!= nil {  
// Handle error (e.g., if subscription already exists with a different policy)  
}

#### **Step 3: Monitoring and Alerting**

To make the solution effective, we must be notified when a message fails.

1. **Create a Metric-Based Alert**: In Google Cloud Monitoring, create an alerting policy that monitors the pubsub.googleapis.com/subscription/dead\_letter\_message\_count metric.
2. **Configure the Alert**:
    * **Resource**: pubsub\_subscription
    * **Filter**: subscription\_id \= "ingress-sub"
    * **Condition**: Trigger if sum of the metric is above a threshold of 0 for 1 minute.
    * **Notification**: Configure the alert to notify the on-call engineering team via their preferred channel (e.g., PagerDuty, Slack).