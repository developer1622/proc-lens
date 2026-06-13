package classifier

/*
 * Note: This file defines all supported workload Category constants.
 *
 * The original 16 archetypes have been expanded to 32+ to reflect the modern cloud-native,
 * mixed-legacy, SoA, serverless, edge, and ML-ops reality of production environments.
 *
 * Caveat: Categories are deliberately broad. A single process may exhibit traits of multiple
 * categories; the classifier uses cosine similarity + rule boosts to resolve ambiguity.
 * New categories should always come with a corresponding centroid in centroids.go and
 * name-match keywords in classifier.go.
 */

import "github.com/developer1622/proc-lens/pkg/collector"

// Category represents a predicted HLD (High-Level Design) workload archetype.
type Category string

// ─── Tier 1: Network Ingress / Egress ────────────────────────────────────────
const (
	LoadBalancer  Category = "LoadBalancer"  // Envoy, HAProxy, Nginx (frontend L4/L7)
	APIGateway    Category = "APIGateway"    // Kong, Ambassador, AWS API GW sidecar
	ServiceMesh   Category = "ServiceMesh"   // Istio pilot/proxy, Linkerd proxy, Consul Connect
	CDNEdgeNode   Category = "CDNEdgeNode"   // Varnish, Squid, Cloudflare edge worker
)

// ─── Tier 2: Application / Compute ───────────────────────────────────────────
const (
	WebServer        Category = "WebServer"        // Apache, Caddy, Node.js, Go HTTP server
	Microservice     Category = "Microservice"      // gRPC/REST micro service (low memory, many fds)
	ServerlessWorker Category = "ServerlessWorker"  // Lambda runtime, OpenFaaS, Knative sandbox
	JobWorker        Category = "JobWorker"         // Celery, Sidekiq, BullMQ, task queue consumer
	SchedulerDaemon  Category = "SchedulerDaemon"   // Cron, Airflow worker, Temporal worker
)

// ─── Tier 3: Data Stores ─────────────────────────────────────────────────────
const (
	CacheStore  Category = "CacheStore"  // Redis, Memcached, Dragonfly
	RelationalDB Category = "RelationalDB" // PostgreSQL, MySQL, MariaDB, CockroachDB
	NoSQLDB     Category = "NoSQLDB"     // MongoDB, Cassandra, ScyllaDB, CouchDB
	ColumnarDB  Category = "ColumnarDB"  // ClickHouse, DuckDB, Apache Parquet reader
	VectorDB    Category = "VectorDB"    // Milvus, Qdrant, Weaviate, Pinecone sidecar
	TimeSeriesDB Category = "TimeSeriesDB" // InfluxDB, VictoriaMetrics, Prometheus TSDB
	GraphDB     Category = "GraphDB"     // Neo4j, JanusGraph, DGraph
	ObjectStore Category = "ObjectStore" // MinIO, SeaweedFS, Ceph RGW
)

// ─── Tier 4: Messaging / Streaming ───────────────────────────────────────────
const (
	MessageBroker  Category = "MessageBroker"  // RabbitMQ, NATS, ActiveMQ
	EventStreaming  Category = "EventStreaming"  // Kafka, Pulsar, Kinesis agent
	StreamProcessor Category = "StreamProcessor" // Flink, Spark Streaming, Kafka Streams JVM
)

// ─── Tier 5: Search / Analytics ──────────────────────────────────────────────
const (
	SearchEngine  Category = "SearchEngine"  // Elasticsearch, OpenSearch, Solr
	OLAPEngine    Category = "OLAPEngine"    // Presto, Trino, Spark Driver, Druid broker
)

// ─── Tier 6: AI / ML ─────────────────────────────────────────────────────────
const (
	AITraining   Category = "AITraining"   // PyTorch DDP, TensorFlow trainer, JAX
	AIInference  Category = "AIInference"  // Triton, TF Serving, vLLM, Ollama
	MLPipeline   Category = "MLPipeline"   // MLflow, Kubeflow pipeline runner, DVC
	FeatureStore Category = "FeatureStore" // Feast, Tecton sidecar
)

// ─── Tier 7: Infrastructure / Orchestration ───────────────────────────────────
const (
	OrchestratorAgent Category = "OrchestratorAgent" // Kubelet, Containerd, Dockerd, runc
	OrchestratorPod   Category = "OrchestratorPod"   // Kubernetes Pod wrapper process
	ServiceDiscovery  Category = "ServiceDiscovery"   // Consul agent, etcd, ZooKeeper
	ConfigManager     Category = "ConfigManager"      // Vault agent, Ansible, Chef client
)

// ─── Tier 8: Observability ────────────────────────────────────────────────────
const (
	MonitoringAgent Category = "MonitoringAgent" // Prometheus, Datadog agent, OTel collector
	LogAggregator   Category = "LogAggregator"   // Fluentd, Fluent Bit, Logstash, Vector
	TracingAgent    Category = "TracingAgent"     // Jaeger agent, Zipkin, OTel trace exporter
)

// ─── Tier 9: Developer / Legacy / Utility ────────────────────────────────────
const (
	InteractiveShell Category = "InteractiveShell" // Bash, SSH session, systemd
	UtilityBatch     Category = "UtilityBatch"     // Compilers, gzip, tar, rsync, cron scripts
	LegacySOAService Category = "LegacySOAService" // SOAP/XML webservice, EJB/JBoss, WAS, old J2EE
	CIRunner         Category = "CIRunner"         // GitHub Actions runner, Jenkins agent, GitLab runner
	Unknown          Category = "Unknown"
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
