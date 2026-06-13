package classifier

import (
	"fmt"
	"math"
	"github.com/developer1622/proc-lens/pkg/collector"
	"strings"
)

/*
 * Note: This file contains the core prediction algorithm combining cosine similarity
 * against archetype centroids with rule-based boost heuristics.
 *
 * Caveat: Cosine similarity alone might misclassify processes with similar relative ratios
 * but different scales. Therefore, rule-based boosters are employed to adjust scores.
 * Ensure any new rules are tested thoroughly using the provided unit tests.
 */

// Predict analyzes process telemetry and predicts its category using cosine similarity and rule adjustments.
// Note that the same returns the predicted Category along with confidence scores.
func Predict(stats collector.ProcessStats) Prediction {
	fv := extractFeatures(stats)
	scores := make(map[Category]float64)

	// 1. Calculate base Cosine Similarity scores against centroids
	for cat, centroid := range Centroids {
		scores[cat] = cosineSimilarity(fv, centroid)
	}

	// 2. Apply rules and heuristics (using independent IF statements)
	rulesTriggered := []string{}
	
	// Sockets rules
	if stats.SocketCount > 500 {
		scores[LoadBalancer] += 0.1
		scores[CacheStore] += 0.05
		scores[WebServer] += 0.05
		scores[InteractiveShell] -= 0.4
		scores[UtilityBatch] -= 0.4
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Heuristic: Sockets count > 500 (%d sockets; strongly suggests a public network gateway/balancer)", stats.SocketCount))
	} else if stats.SocketCount == 0 {
		scores[LoadBalancer] -= 0.4
		scores[WebServer] -= 0.4
		scores[CacheStore] -= 0.3
		scores[InteractiveShell] += 0.05
		scores[UtilityBatch] += 0.05
	}

	// CPU rules
	if stats.CpuUsage > 150.0 {
		scores[AITraining] += 0.15
		scores[AIInference] += 0.05
		scores[InteractiveShell] -= 0.4
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Heuristic: CPU usage > 150%% (%.1f%% CPU; indicates multi-core AI execution)", stats.CpuUsage))
	}

	// RAM rules
	memMb := float64(stats.MemRss) / (1024 * 1024)
	if memMb > 8192 {
		scores[AITraining] += 0.05
		scores[ColumnarDB] += 0.05
		scores[NoSQLDB] += 0.05
		scores[RelationalDB] += 0.05
		scores[InteractiveShell] -= 0.3
		rulesTriggered = append(rulesTriggered, "Heuristic: Memory RSS > 8GB (Highly indicative of large database buffers or model weights)")
	}

	// Read/Write storage speeds rules
	readKbSec := stats.IoReadSpeed / 1024.0
	writeKbSec := stats.IoWriteSpeed / 1024.0
	if readKbSec > 10000.0 {
		scores[ColumnarDB] += 0.1
		scores[EventStreaming] += 0.05
		scores[UtilityBatch] += 0.05
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Heuristic: Disk read > 10MB/s (%.1f MB/s; matches columnar table scans or batch files)", readKbSec/1024.0))
	}
	if writeKbSec > 10000.0 {
		scores[EventStreaming] += 0.1
		scores[MonitoringAgent] += 0.05
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Heuristic: Disk write > 10MB/s (%.1f MB/s; indicates sequential logging or TSDB updates)", writeKbSec/1024.0))
	}

	// Process name matching keywords (strong boosters using independent IFs)
	name := strings.ToLower(stats.Name)
	cmdline := strings.ToLower(stats.Cmdline)

	if containsAny(name, "postgres", "postgres:", "mysql", "mysqld", "mariadb", "sqlite", "cockroach") {
		scores[RelationalDB] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Relational database keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "mongo", "mongod", "cassandra", "scylla", "couchdb") {
		scores[NoSQLDB] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: NoSQL database keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "clickhouse", "duckdb", "clickhouse-server") {
		scores[ColumnarDB] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Columnar database keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "milvus", "qdrant", "pinecone", "weaviate") {
		scores[VectorDB] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Vector database keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "redis", "redis-server", "memcached") {
		scores[CacheStore] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: In-memory cache keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "nginx", "haproxy", "envoy", "traefik", "caddy") {
		scores[LoadBalancer] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Load balancer / Reverse proxy keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "elasticsearch", "elastic", "opensearch", "solr") {
		scores[SearchEngine] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Search engine keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "rabbitmq", "nats", "activemq", "broker") {
		scores[MessageBroker] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Message broker keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "kafka", "pulsar") {
		scores[EventStreaming] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Event streaming broker keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "kubelet", "containerd", "dockerd", "runc") {
		scores[OrchestratorAgent] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Container orchestrator runtime keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "prometheus", "otelcol", "datadog-agent", "agent") {
		scores[MonitoringAgent] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Monitoring agent keyword found in process name '%s'", stats.Name))
	}
	if containsAny(name, "bash", "zsh", "sh", "sshd", "systemd", "login") {
		scores[InteractiveShell] += 0.25
		rulesTriggered = append(rulesTriggered, fmt.Sprintf("Signature: Shell/daemon control process found in name '%s'", stats.Name))
	}
	if containsAny(cmdline, "python") && containsAny(cmdline, "train", "torch", "cuda", "fit") {
		scores[AITraining] += 0.3
		rulesTriggered = append(rulesTriggered, "Signature: Python script matches AI model training parameters")
	}
	if containsAny(cmdline, "python") && containsAny(cmdline, "serve", "predict", "model-server", "triton") {
		scores[AIInference] += 0.3
		rulesTriggered = append(rulesTriggered, "Signature: Python script matches AI model inference serving patterns")
	}

	// 3. Select primary category USING UN-CLAMPED SCORES to preserve differences
	var primary Category = Unknown
	bestScore := -100.0
	for cat, score := range scores {
		if score > bestScore {
			bestScore = score
			primary = cat
		}
	}

	// 4. Normalize and clamp scores between 0.0 and 1.0 for reporting
	clampedScores := make(map[Category]float64)
	for cat, score := range scores {
		if score < 0 {
			clampedScores[cat] = 0
		} else if score > 1.0 {
			clampedScores[cat] = 1.0
		} else {
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
		Recommendations: []string{}, // Will be populated by the optimizer package
	}
}

// extractFeatures maps physical raw telemetry values to logarithmic feature representations.
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

// cosineSimilarity calculates similarity metric between two feature vectors.
func cosineSimilarity(v1, v2 FeatureVector) float64 {
	dotProduct := v1.CPU*v2.CPU +
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
	return dotProduct / (norm1 * norm2)
}

// containsAny checks if source contains any of the target substrings.
func containsAny(s string, targets ...string) bool {
	for _, t := range targets {
		if strings.Contains(s, t) {
			return true
		}
	}
	return false
}

