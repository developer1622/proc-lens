package classifier

import "math"

/*
 * Note: This file defines the centroids (signature vectors) for all supported workload archetypes.
 *
 * Each centroid is a FeatureVector whose components are the log-scaled "ideal" telemetry readings
 * for that archetype. The makeCentroid helper applies math.Log1p to each raw physical value so
 * that cosine similarity comparisons remain scale-invariant.
 *
 * Centroid constructor argument order:
 *   cpu (%), memMb (MB), threads (#), sockets (#), fds (#),
 *   ioReadKb (KB/s), ioWriteKb (KB/s), ctxSwitchesPerSec (#)
 *
 * Caveat: Values are derived from benchmark observations. Tune them for your specific hardware
 * and deployment density; the rule-based boosters in classifier.go compensate for outliers.
 *
 * When adding a new archetype:
 *   1. Add the Category constant in types.go
 *   2. Add its centroid here
 *   3. Add name/cmdline keyword rules in classifier.go
 *   4. Add a test case in classifier_test.go
 */

// Centroids maps every Category to its canonical reference FeatureVector.
var Centroids = map[Category]FeatureVector{

	// ─── Tier 1: Network Ingress / Egress ─────────────────────────────────
	// LoadBalancer: many sockets, low CPU, moderate fds (HAProxy, Envoy, Nginx frontend)
	LoadBalancer: makeCentroid(10.0, 64.0, 8.0, 1000.0, 1100.0, 10.0, 10.0, 5000.0),
	// APIGateway: higher CPU than plain LB (auth, routing logic), many fds
	APIGateway: makeCentroid(20.0, 256.0, 24.0, 600.0, 1500.0, 50.0, 50.0, 4000.0),
	// ServiceMesh: very high socket counts (sidecar per pod), low CPU, many context switches
	ServiceMesh: makeCentroid(8.0, 128.0, 12.0, 800.0, 900.0, 20.0, 20.0, 8000.0),
	// CDNEdgeNode: massive read I/O (disk cache), high sockets, low write
	CDNEdgeNode: makeCentroid(12.0, 512.0, 16.0, 700.0, 1200.0, 50000.0, 500.0, 3000.0),

	// ─── Tier 2: Application / Compute ───────────────────────────────────
	// WebServer: moderate sockets, low-moderate memory, high request throughput
	WebServer: makeCentroid(15.0, 128.0, 24.0, 400.0, 450.0, 100.0, 100.0, 2000.0),
	// Microservice: low memory, moderate sockets, frequent ctx switches (goroutines/threads)
	Microservice: makeCentroid(10.0, 96.0, 20.0, 200.0, 300.0, 30.0, 30.0, 3000.0),
	// ServerlessWorker: very short-lived, low memory, minimal sockets, spiky CPU
	ServerlessWorker: makeCentroid(50.0, 64.0, 4.0, 10.0, 50.0, 100.0, 50.0, 500.0),
	// JobWorker: steady CPU, moderate memory, low sockets (consumes from a queue)
	JobWorker: makeCentroid(30.0, 256.0, 8.0, 20.0, 100.0, 500.0, 500.0, 800.0),
	// SchedulerDaemon: very low CPU when idle, wakes up periodically
	SchedulerDaemon: makeCentroid(3.0, 64.0, 4.0, 5.0, 60.0, 50.0, 50.0, 200.0),

	// ─── Tier 3: Data Stores ─────────────────────────────────────────────
	// CacheStore: very high socket count, large memory, near-zero I/O
	CacheStore: makeCentroid(8.0, 1024.0, 4.0, 800.0, 850.0, 0.1, 0.1, 6000.0),
	// RelationalDB: moderate-high CPU (query planner), large memory, sequential I/O
	RelationalDB: makeCentroid(20.0, 2048.0, 50.0, 60.0, 250.0, 1000.0, 1000.0, 1500.0),
	// NoSQLDB: high memory, many threads/shards, high I/O
	NoSQLDB: makeCentroid(25.0, 4096.0, 128.0, 150.0, 500.0, 2000.0, 2000.0, 3000.0),
	// ColumnarDB: very high CPU + read I/O (column scans), huge memory
	ColumnarDB: makeCentroid(80.0, 8192.0, 32.0, 10.0, 150.0, 20000.0, 1000.0, 1000.0),
	// VectorDB: high CPU (SIMD distance), large memory, moderate I/O
	VectorDB: makeCentroid(95.0, 6144.0, 48.0, 40.0, 200.0, 5000.0, 200.0, 4000.0),
	// TimeSeriesDB: write-heavy I/O, moderate CPU, many fds (WAL segments)
	TimeSeriesDB: makeCentroid(15.0, 512.0, 16.0, 30.0, 400.0, 2000.0, 8000.0, 2000.0),
	// GraphDB: high CPU (traversal), moderate memory, low sockets
	GraphDB: makeCentroid(40.0, 3072.0, 32.0, 20.0, 180.0, 500.0, 500.0, 2000.0),
	// ObjectStore: high I/O (blob reads/writes), moderate sockets, many fds
	ObjectStore: makeCentroid(10.0, 512.0, 16.0, 100.0, 800.0, 30000.0, 30000.0, 1500.0),

	// ─── Tier 4: Messaging / Streaming ───────────────────────────────────
	// MessageBroker: moderate sockets, moderate I/O
	MessageBroker: makeCentroid(12.0, 512.0, 32.0, 300.0, 400.0, 200.0, 200.0, 2500.0),
	// EventStreaming: very high write I/O (log segments), many threads, high sockets
	EventStreaming: makeCentroid(35.0, 4096.0, 90.0, 250.0, 600.0, 15000.0, 25000.0, 4000.0),
	// StreamProcessor: high CPU (stateful ops), high memory (state store), high I/O
	StreamProcessor: makeCentroid(60.0, 4096.0, 64.0, 80.0, 400.0, 10000.0, 10000.0, 3000.0),

	// ─── Tier 5: Search / Analytics ──────────────────────────────────────
	// SearchEngine: high memory (Lucene index), high CPU (query), moderate I/O
	SearchEngine: makeCentroid(30.0, 6144.0, 80.0, 80.0, 400.0, 1500.0, 1500.0, 2000.0),
	// OLAPEngine: very high CPU + read I/O (distributed joins/aggregations)
	OLAPEngine: makeCentroid(90.0, 8192.0, 48.0, 40.0, 300.0, 25000.0, 2000.0, 2500.0),

	// ─── Tier 6: AI / ML ─────────────────────────────────────────────────
	// AITraining: very high CPU (multi-core), huge memory (model weights), low sockets
	AITraining: makeCentroid(400.0, 16384.0, 64.0, 5.0, 80.0, 500.0, 100.0, 8000.0),
	// AIInference: high CPU + memory (model loaded), moderate sockets (gRPC clients)
	AIInference: makeCentroid(120.0, 8192.0, 40.0, 100.0, 150.0, 100.0, 10.0, 5000.0),
	// MLPipeline: moderate CPU (orchestrating steps), moderate I/O (artifact r/w)
	MLPipeline: makeCentroid(20.0, 512.0, 8.0, 30.0, 200.0, 2000.0, 2000.0, 500.0),
	// FeatureStore: high I/O (batch feature materialization), moderate memory
	FeatureStore: makeCentroid(15.0, 1024.0, 16.0, 60.0, 300.0, 5000.0, 5000.0, 1000.0),

	// ─── Tier 7: Infrastructure / Orchestration ──────────────────────────
	// OrchestratorAgent: low CPU (polling), many fds (cgroup, netns), moderate ctx switches
	OrchestratorAgent: makeCentroid(5.0, 256.0, 20.0, 15.0, 800.0, 50.0, 50.0, 1200.0),
	// OrchestratorPod: placeholder process; very low resource usage
	OrchestratorPod: makeCentroid(1.0, 32.0, 2.0, 2.0, 30.0, 5.0, 5.0, 100.0),
	// ServiceDiscovery: low CPU, low memory, moderate sockets (gossip protocol)
	ServiceDiscovery: makeCentroid(4.0, 128.0, 8.0, 60.0, 150.0, 20.0, 20.0, 400.0),
	// ConfigManager: low baseline CPU, bursts during config apply
	ConfigManager: makeCentroid(5.0, 96.0, 4.0, 10.0, 80.0, 100.0, 100.0, 200.0),

	// ─── Tier 8: Observability ────────────────────────────────────────────
	// MonitoringAgent: moderate read I/O (scraping), moderate write (remote-write), many fds
	MonitoringAgent: makeCentroid(8.0, 512.0, 16.0, 150.0, 300.0, 200.0, 4000.0, 800.0),
	// LogAggregator: very high write I/O (forwarding log streams), many fds
	LogAggregator: makeCentroid(12.0, 256.0, 8.0, 60.0, 600.0, 500.0, 20000.0, 1000.0),
	// TracingAgent: low CPU, low memory, high sockets (trace data ingestion)
	TracingAgent: makeCentroid(5.0, 128.0, 4.0, 80.0, 200.0, 50.0, 200.0, 600.0),

	// ─── Tier 9: Developer / Legacy / Utility ────────────────────────────
	// InteractiveShell: near-zero everything (idle terminal / SSH session)
	InteractiveShell: makeCentroid(0.1, 8.0, 1.0, 0.0, 5.0, 0.0, 0.0, 5.0),
	// UtilityBatch: spiky high CPU + I/O (compilation, compression, rsync)
	UtilityBatch: makeCentroid(100.0, 128.0, 4.0, 0.0, 15.0, 10000.0, 10000.0, 400.0),
	// LegacySOAService: high JVM heap (2-4GB), moderate CPU, low sockets (SOAP)
	LegacySOAService: makeCentroid(25.0, 2048.0, 100.0, 20.0, 250.0, 300.0, 300.0, 1200.0),
	// CIRunner: bursty CPU (build/test), high I/O (artifact cache), low sockets
	CIRunner: makeCentroid(80.0, 1024.0, 16.0, 10.0, 200.0, 8000.0, 8000.0, 600.0),
}

// makeCentroid takes physical telemetry profiles and maps them to logarithmic scales.
func makeCentroid(cpu, memMb, threads, sockets, fds, ioReadKb, ioWriteKb, ctxSw float64) FeatureVector {
	return FeatureVector{
		CPU:         math.Log1p(cpu),
		Memory:      math.Log1p(memMb),
		Threads:     math.Log1p(threads),
		Sockets:     math.Log1p(sockets),
		FDs:         math.Log1p(fds),
		IORead:      math.Log1p(ioReadKb),
		IOWrite:     math.Log1p(ioWriteKb),
		CtxSwitches: math.Log1p(ctxSw),
	}
}
