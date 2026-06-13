package classifier

import (
	"fmt"
	"math"
	"strings"

	"github.com/developer1622/proc-lens/pkg/collector"
)

/*
 * Note: This file contains the core prediction algorithm combining cosine similarity
 * against archetype centroids with rule-based boost heuristics.
 *
 * The algorithm runs in three phases:
 *   1. Feature extraction  – map raw /proc telemetry to log-scaled FeatureVector
 *   2. Cosine similarity   – score each of the 36 archetype centroids
 *   3. Rule boosters       – threshold-based score adjustments for strong signals
 *
 * Caveat: Cosine similarity alone may misclassify processes with similar relative ratios
 * but different absolute scales. Rule-based boosters compensate for known edge cases.
 * Always add tests when introducing new rule branches.
 *
 * Robustness guarantees:
 *   - No panics on nil / zero-value ProcessStats
 *   - All map accesses are through the scores variable (never the Centroids map directly at runtime)
 *   - Scores are clamped to [0.0, 1.0] before reporting
 *   - If all cosine similarities are 0 (zero-vector input), Unknown is returned with confidence 0
 */

// Predict analyzes process telemetry and predicts its archetype category.
// It is safe to call with a zero-value collector.ProcessStats — it will return Unknown.
func Predict(stats collector.ProcessStats) Prediction {
	fv := extractFeatures(stats)
	scores := make(map[Category]float64, len(Centroids))

	// Phase 1: Cosine similarity against every registered centroid.
	for cat, centroid := range Centroids {
		scores[cat] = cosineSimilarity(fv, centroid)
	}

	// Phase 2: Rule-based boosters using independent IF statements.
	// Using independent IFs (not else-if) so that multiple rules can fire simultaneously.
	rulesTriggered := []string{}

	// ── Socket count rules ──────────────────────────────────────────────
	if stats.SocketCount > 500 {
		scores[LoadBalancer] += 0.10
		scores[CacheStore] += 0.05
		scores[WebServer] += 0.05
		scores[APIGateway] += 0.08
		scores[ServiceMesh] += 0.10
		scores[InteractiveShell] -= 0.40
		scores[UtilityBatch] -= 0.40
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Heuristic: socket count > 500 (%d sockets; strongly suggests a public network gateway/balancer)", stats.SocketCount))
	} else if stats.SocketCount == 0 {
		scores[LoadBalancer] -= 0.40
		scores[WebServer] -= 0.40
		scores[CacheStore] -= 0.30
		scores[ServiceMesh] -= 0.30
		scores[InteractiveShell] += 0.05
		scores[UtilityBatch] += 0.05
		scores[CIRunner] += 0.05
	}

	// ── CPU rules ───────────────────────────────────────────────────────
	if stats.CpuUsage > 150.0 {
		scores[AITraining] += 0.15
		scores[AIInference] += 0.05
		scores[ColumnarDB] += 0.05
		scores[OLAPEngine] += 0.08
		scores[StreamProcessor] += 0.05
		scores[InteractiveShell] -= 0.40
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Heuristic: CPU > 150%% (%.1f%%; indicates multi-core compute — AI, OLAP, or stream processing)", stats.CpuUsage))
	}
	if stats.CpuUsage > 50.0 && stats.CpuUsage <= 150.0 {
		scores[CIRunner] += 0.05
		scores[UtilityBatch] += 0.05
		scores[StreamProcessor] += 0.03
	}
	if stats.CpuUsage < 2.0 {
		scores[InteractiveShell] += 0.05
		scores[SchedulerDaemon] += 0.05
		scores[OrchestratorPod] += 0.05
	}

	// ── Memory rules ─────────────────────────────────────────────────────
	memMb := float64(stats.MemRss) / (1024 * 1024)
	if memMb > 8192 {
		scores[AITraining] += 0.05
		scores[ColumnarDB] += 0.05
		scores[NoSQLDB] += 0.05
		scores[RelationalDB] += 0.05
		scores[OLAPEngine] += 0.05
		scores[VectorDB] += 0.05
		scores[InteractiveShell] -= 0.30
		rulesTriggered = append(rulesTriggered,
			"Heuristic: Memory RSS > 8 GB (indicative of large database buffers or ML model weights)")
	}
	if memMb > 2048 && memMb <= 8192 {
		scores[RelationalDB] += 0.03
		scores[SearchEngine] += 0.03
		scores[GraphDB] += 0.03
		scores[LegacySOAService] += 0.03
		scores[StreamProcessor] += 0.03
	}
	if memMb < 32 {
		scores[InteractiveShell] += 0.05
		scores[TracingAgent] += 0.03
		scores[SchedulerDaemon] += 0.03
		scores[OrchestratorPod] += 0.05
	}

	// ── I/O rules ────────────────────────────────────────────────────────
	readKbSec := stats.IoReadSpeed / 1024.0
	writeKbSec := stats.IoWriteSpeed / 1024.0

	if readKbSec > 10000.0 {
		scores[ColumnarDB] += 0.10
		scores[OLAPEngine] += 0.08
		scores[EventStreaming] += 0.05
		scores[ObjectStore] += 0.08
		scores[CDNEdgeNode] += 0.08
		scores[UtilityBatch] += 0.05
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Heuristic: disk read > 10 MB/s (%.1f MB/s; matches columnar scans, CDN cache, or object store reads)", readKbSec/1024.0))
	}
	if writeKbSec > 10000.0 {
		scores[EventStreaming] += 0.10
		scores[LogAggregator] += 0.12
		scores[TimeSeriesDB] += 0.08
		scores[ObjectStore] += 0.05
		scores[MonitoringAgent] += 0.05
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Heuristic: disk write > 10 MB/s (%.1f MB/s; indicates log forwarding, TSDB WAL, or event log segments)", writeKbSec/1024.0))
	}

	// ── Thread count rules ───────────────────────────────────────────────
	if stats.Threads > 200 {
		scores[NoSQLDB] += 0.05
		scores[LegacySOAService] += 0.08
		scores[SearchEngine] += 0.05
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Heuristic: thread count > 200 (%d threads; suggests JVM or thread-per-connection server)", stats.Threads))
	}
	if stats.Threads == 1 {
		scores[InteractiveShell] += 0.05
		scores[UtilityBatch] += 0.05
	}

	// ── Context-switch rate rules ────────────────────────────────────────
	if stats.CtxSwitchRate > 5000 {
		scores[CacheStore] += 0.05
		scores[LoadBalancer] += 0.05
		scores[ServiceMesh] += 0.05
		scores[AITraining] += 0.03
	}

	// ── FD count rules ───────────────────────────────────────────────────
	if stats.FdCount > 1000 {
		scores[LogAggregator] += 0.05
		scores[ObjectStore] += 0.05
		scores[EventStreaming] += 0.03
	}

	// Phase 3: Name and cmdline keyword matching (strong boosters).
	name := strings.ToLower(stats.Name)
	cmdline := strings.ToLower(stats.Cmdline)

	// ── Tier 1 name boosters ─────────────────────────────────────────────
	if containsAny(name, "nginx", "haproxy", "envoy", "traefik") {
		scores[LoadBalancer] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: load balancer keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "caddy") && !containsAny(cmdline, "reverse_proxy", "proxy") {
		// Caddy alone → web server
		scores[WebServer] += 0.20
	} else if containsAny(name, "caddy") {
		scores[LoadBalancer] += 0.15
		scores[APIGateway] += 0.10
	}
	if containsAny(name, "kong", "ambassador", "apisix", "krakend") {
		scores[APIGateway] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: API gateway keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "pilot", "istiod", "linkerd", "consul") {
		scores[ServiceMesh] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: service mesh control plane keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "varnish", "squid") {
		scores[CDNEdgeNode] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: CDN/cache edge keyword in process name '%s'", stats.Name))
	}

	// ── Tier 2 name boosters ─────────────────────────────────────────────
	if containsAny(name, "nginx", "apache", "httpd", "caddy", "node", "gunicorn", "uvicorn", "uwsgi") {
		scores[WebServer] += 0.15
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: web server keyword in process name '%s'", stats.Name))
	}
	if containsAny(cmdline, "celery", "sidekiq", "bullmq", "resque", "dramatiq") {
		scores[JobWorker] += 0.25
		rulesTriggered = append(rulesTriggered, "Signature: job-queue worker keyword in cmdline")
	}
	if containsAny(name, "cron", "crond") || containsAny(cmdline, "airflow", "temporal", "prefect", "dagster") {
		scores[SchedulerDaemon] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: scheduler/orchestration keyword in name/cmdline '%s'", stats.Name))
	}
	if containsAny(cmdline, "lambda", "openfaas", "knative", "faas", "handler.") {
		scores[ServerlessWorker] += 0.25
		rulesTriggered = append(rulesTriggered, "Signature: serverless runtime keyword in cmdline")
	}

	// ── Tier 3 name boosters ─────────────────────────────────────────────
	if containsAny(name, "postgres", "mysql", "mysqld", "mariadb", "sqlite", "cockroach") {
		scores[RelationalDB] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: relational DB keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "mongo", "mongod", "cassandra", "scylla", "couchdb", "dynamodb") {
		scores[NoSQLDB] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: NoSQL DB keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "clickhouse", "duckdb", "druid") {
		scores[ColumnarDB] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: columnar DB keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "milvus", "qdrant", "weaviate") || containsAny(cmdline, "vectordb", "pinecone") {
		scores[VectorDB] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: vector DB keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "redis", "redis-server", "memcached", "dragonfly") {
		scores[CacheStore] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: in-memory cache keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "influxd", "influx", "victoria", "m3db", "timescaledb") {
		scores[TimeSeriesDB] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: time-series DB keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "neo4j", "janusgraph", "dgraph", "tigergraph") {
		scores[GraphDB] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: graph DB keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "minio", "seaweedfs", "ceph") || containsAny(cmdline, "object-store", "s3") {
		scores[ObjectStore] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: object store keyword in process name '%s'", stats.Name))
	}

	// ── Tier 4 name boosters ─────────────────────────────────────────────
	if containsAny(name, "rabbitmq", "nats", "activemq", "broker") {
		scores[MessageBroker] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: message broker keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "kafka", "pulsar") {
		scores[EventStreaming] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: event streaming keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "flink", "spark") || containsAny(cmdline, "kafka-streams", "stream-processor") {
		scores[StreamProcessor] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: stream processor keyword in name/cmdline '%s'", stats.Name))
	}

	// ── Tier 5 name boosters ─────────────────────────────────────────────
	if containsAny(name, "elasticsearch", "elastic", "opensearch", "solr") {
		scores[SearchEngine] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: search engine keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "trino", "presto", "hive") || containsAny(cmdline, "olap", "coordinator", "druid-broker") {
		scores[OLAPEngine] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: OLAP engine keyword in name/cmdline '%s'", stats.Name))
	}

	// ── Tier 6 name boosters ─────────────────────────────────────────────
	if containsAny(cmdline, "python") && containsAny(cmdline, "train", "torch", "cuda", "fit", "jax") {
		scores[AITraining] += 0.30
		rulesTriggered = append(rulesTriggered, "Signature: Python script matches AI model training parameters")
	}
	if containsAny(cmdline, "python") && containsAny(cmdline, "serve", "predict", "model-server", "triton", "vllm", "ollama") {
		scores[AIInference] += 0.30
		rulesTriggered = append(rulesTriggered, "Signature: Python script matches AI model inference patterns")
	}
	if containsAny(name, "triton", "torchserve") || containsAny(cmdline, "tritonserver", "bentoml") {
		scores[AIInference] += 0.20
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: inference server keyword in name/cmdline '%s'", stats.Name))
	}
	if containsAny(cmdline, "mlflow", "kubeflow", "dvc", "zenml", "prefect") {
		scores[MLPipeline] += 0.25
		rulesTriggered = append(rulesTriggered, "Signature: ML pipeline orchestrator keyword in cmdline")
	}
	if containsAny(cmdline, "feast", "tecton", "hopsworks") {
		scores[FeatureStore] += 0.25
		rulesTriggered = append(rulesTriggered, "Signature: feature store keyword in cmdline")
	}

	// ── Tier 7 name boosters ─────────────────────────────────────────────
	if containsAny(name, "kubelet", "containerd", "dockerd", "runc", "crio") {
		scores[OrchestratorAgent] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: container runtime keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "pause", "init") && memMb < 16 {
		// Kubernetes pause / init container — placeholder process
		scores[OrchestratorPod] += 0.30
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: orchestrator placeholder process '%s' with very low memory", stats.Name))
	}
	if containsAny(name, "etcd", "zookeeper", "consul") {
		scores[ServiceDiscovery] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: service discovery keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "vault", "ansible", "chef", "puppet") {
		scores[ConfigManager] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: config management keyword in process name '%s'", stats.Name))
	}

	// ── Tier 8 name boosters ─────────────────────────────────────────────
	if containsAny(name, "prometheus", "otelcol", "datadog-agent", "node_exporter") {
		scores[MonitoringAgent] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: monitoring agent keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "fluentd", "fluent-bit", "logstash", "vector") {
		scores[LogAggregator] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: log aggregator keyword in process name '%s'", stats.Name))
	}
	if containsAny(name, "jaeger", "zipkin") || containsAny(cmdline, "otel", "otlp", "trace") {
		scores[TracingAgent] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: tracing agent keyword in name/cmdline '%s'", stats.Name))
	}

	// ── Tier 9 name boosters ─────────────────────────────────────────────
	if containsAny(name, "bash", "zsh", "sh", "sshd", "login", "fish") {
		scores[InteractiveShell] += 0.25
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: shell/login process found in name '%s'", stats.Name))
	}
	if containsAny(name, "systemd") && stats.Threads > 1 {
		// systemd itself is not interactive — treat as scheduler daemon
		scores[SchedulerDaemon] += 0.15
		scores[InteractiveShell] -= 0.10
	}
	if containsAny(cmdline, "jboss", "weblogic", "websphere", "glassfish", "wildfly", "tomcat") {
		scores[LegacySOAService] += 0.30
		rulesTriggered = append(rulesTriggered,
			fmt.Sprintf("Signature: legacy J2EE/SOA application server keyword in cmdline '%s'", stats.Name))
	}
	if containsAny(name, "runner", "agent") && containsAny(cmdline, "github-actions", "gitlab-runner", "jenkins", "buildkite", "circleci") {
		scores[CIRunner] += 0.30
		rulesTriggered = append(rulesTriggered, "Signature: CI/CD runner keyword in cmdline")
	}

	// Phase 4: Select primary category using un-clamped scores to preserve rank ordering.
	var primary Category = Unknown
	bestScore := -100.0
	for cat, score := range scores {
		if score > bestScore {
			bestScore = score
			primary = cat
		}
	}

	// Phase 5: Normalise and clamp scores to [0.0, 1.0] for reporting.
	clampedScores := make(map[Category]float64, len(scores))
	for cat, score := range scores {
		switch {
		case score < 0:
			clampedScores[cat] = 0
		case score > 1.0:
			clampedScores[cat] = 1.0
		default:
			clampedScores[cat] = score
		}
	}

	confidence := clampedScores[primary]

	return Prediction{
		PID:             stats.PID,
		Name:            stats.Name,
		Cmdline:         stats.Cmdline,
		PrimaryCategory: primary,
		Confidence:      confidence,
		Scores:          clampedScores,
		Telemetry:       stats,
		RulesTriggered:  rulesTriggered,
		Recommendations: []string{}, // populated by the optimizer package
	}
}

// extractFeatures maps raw /proc telemetry to logarithmic feature representations.
// All conversions are safe for zero-value inputs (math.Log1p(0) == 0).
func extractFeatures(stats collector.ProcessStats) FeatureVector {
	cpuVal := math.Log1p(stats.CpuUsage)

	memMb := float64(stats.MemRss) / (1024.0 * 1024.0)
	memVal := math.Log1p(memMb)

	threadsVal := math.Log1p(float64(stats.Threads))
	socketsVal := math.Log1p(float64(stats.SocketCount))
	fdsVal := math.Log1p(float64(stats.FdCount))

	ioReadKb := stats.IoReadSpeed / 1024.0
	ioReadVal := math.Log1p(ioReadKb)

	ioWriteKb := stats.IoWriteSpeed / 1024.0
	ioWriteVal := math.Log1p(ioWriteKb)

	ctxSwVal := math.Log1p(stats.CtxSwitchRate)

	return FeatureVector{
		CPU:         cpuVal,
		Memory:      memVal,
		Threads:     threadsVal,
		Sockets:     socketsVal,
		FDs:         fdsVal,
		IORead:      ioReadVal,
		IOWrite:     ioWriteVal,
		CtxSwitches: ctxSwVal,
	}
}

// cosineSimilarity returns the cosine similarity between two feature vectors.
// Returns 0 if either vector has zero magnitude (safe — no division by zero).
func cosineSimilarity(v1, v2 FeatureVector) float64 {
	dot := v1.CPU*v2.CPU +
		v1.Memory*v2.Memory +
		v1.Threads*v2.Threads +
		v1.Sockets*v2.Sockets +
		v1.FDs*v2.FDs +
		v1.IORead*v2.IORead +
		v1.IOWrite*v2.IOWrite +
		v1.CtxSwitches*v2.CtxSwitches

	norm1 := math.Sqrt(v1.CPU*v1.CPU +
		v1.Memory*v1.Memory +
		v1.Threads*v1.Threads +
		v1.Sockets*v1.Sockets +
		v1.FDs*v1.FDs +
		v1.IORead*v1.IORead +
		v1.IOWrite*v1.IOWrite +
		v1.CtxSwitches*v1.CtxSwitches)

	norm2 := math.Sqrt(v2.CPU*v2.CPU +
		v2.Memory*v2.Memory +
		v2.Threads*v2.Threads +
		v2.Sockets*v2.Sockets +
		v2.FDs*v2.FDs +
		v2.IORead*v2.IORead +
		v2.IOWrite*v2.IOWrite +
		v2.CtxSwitches*v2.CtxSwitches)

	if norm1 == 0 || norm2 == 0 {
		return 0
	}
	return dot / (norm1 * norm2)
}

// containsAny returns true when s contains any of the provided substrings.
// Safe for empty s and empty targets list.
func containsAny(s string, targets ...string) bool {
	for _, t := range targets {
		if t != "" && strings.Contains(s, t) {
			return true
		}
	}
	return false
}
