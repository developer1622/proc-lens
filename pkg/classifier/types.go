package classifier

import "github.com/developer1622/proc-lens/pkg/collector"

/*
 * Note: This file defines the types and structures used by the classifier package.
 *
 * Caveat: Workload category categories are hardcoded. In case of any new category requirements or updates,
 * kindly revert back to the architecture team to do the needful.
 */

// Category represents a predicted HLD (High-Level Design) workload component.
// Note that the same represents the primary bucket classification.
type Category string

const (
	LoadBalancer      Category = "LoadBalancer"      // Envoy, HAProxy, Nginx (frontend)
	WebServer         Category = "WebServer"         // Apache, Caddy, Node.js backend
	CacheStore        Category = "CacheStore"        // Redis, Memcached
	RelationalDB      Category = "RelationalDB"      // PostgreSQL, MySQL
	NoSQLDB           Category = "NoSQLDB"           // MongoDB, Cassandra
	ColumnarDB        Category = "ColumnarDB"        // ClickHouse, DuckDB
	VectorDB          Category = "VectorDB"          // Milvus, Qdrant, Pinecone
	SearchEngine      Category = "SearchEngine"      // Elasticsearch, OpenSearch
	MessageBroker     Category = "MessageBroker"     // RabbitMQ, NATS
	EventStreaming    Category = "EventStreaming"    // Kafka, Pulsar
	AITraining        Category = "AITraining"        // PyTorch Training, TensorFlow
	AIInference       Category = "AIInference"       // Triton Inference Server, TF Serving
	OrchestratorAgent Category = "OrchestratorAgent" // Kubelet, Containerd
	OrchestratorPod   Category = "OrchestratorPod"   // Kubernetes Pod / Container workload wrapper
	MonitoringAgent   Category = "MonitoringAgent"   // Prometheus, Datadog Agent, OpenTelemetry
	InteractiveShell  Category = "InteractiveShell"  // Bash, SSH session, systemd
	UtilityBatch      Category = "UtilityBatch"      // Compilers, compression tools (gzip)
	Unknown           Category = "Unknown"
)

// FeatureVector represents logarithmic-scaled features of a process.
type FeatureVector struct {
	CPU         float64
	Memory      float64
	Threads     float64
	Sockets     float64
	FDs         float64
	IORead      float64
	IOWrite     float64
	CtxSwitches float64
}

// Prediction contains the classification result, confidence scores, and reasoning.
type Prediction struct {
	PID             int                          `json:"pid"`
	Name            string                       `json:"name"`
	Cmdline         string                       `json:"cmdline"`
	PrimaryCategory Category                     `json:"primary_category"`
	Confidence      float64                      `json:"confidence"`
	Scores          map[Category]float64         `json:"scores"`
	Telemetry       collector.ProcessStats       `json:"telemetry"`
	RulesTriggered  []string                     `json:"rules_triggered"`
	Recommendations []string                     `json:"recommendations"`
	NodeContext     *collector.CollectionContext `json:"node_context,omitempty"`
}

