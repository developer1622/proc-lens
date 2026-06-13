package classifier

import (
	"math"
	"testing"

	"github.com/developer1622/proc-lens/pkg/collector"
)

/*
 * Note: This file contains unit, benchmark, and fuzz tests for the classifier package.
 *
 * Test strategy:
 *   - Table-driven tests for the core Predict function (both original and new archetypes)
 *   - Zero-value safety test to ensure no panics on empty ProcessStats
 *   - Confidence bounds test (all outputs must be in [0, 1])
 *   - Benchmark for throughput measurement
 *   - Fuzz harness for property-based testing
 *
 * Caveat: Expected categories in table tests are based on the name/cmdline keyword boosters.
 * If centroid values or booster weights are tuned, some minConfidence thresholds may need
 * adjustment. Do NOT lower them below 0.5 without a clear justification.
 */

// ─── Feature extraction ───────────────────────────────────────────────────────

func TestExtractFeatures(t *testing.T) {
	stats := collector.ProcessStats{
		CpuUsage:      100.0,
		MemRss:        1024 * 1024 * 1024, // 1024 MB
		Threads:       10,
		SocketCount:   5,
		FdCount:       20,
		IoReadSpeed:   10240, // 10 KB/s
		IoWriteSpeed:  20480, // 20 KB/s
		CtxSwitchRate: 500,
	}

	fv := extractFeatures(stats)

	cases := []struct {
		name     string
		got      float64
		expected float64
	}{
		{"CPU", fv.CPU, math.Log1p(100.0)},
		{"Memory", fv.Memory, math.Log1p(1024.0)},
		{"Threads", fv.Threads, math.Log1p(10)},
		{"Sockets", fv.Sockets, math.Log1p(5)},
		{"FDs", fv.FDs, math.Log1p(20)},
		{"IORead", fv.IORead, math.Log1p(10.0)},  // 10240/1024 = 10 KB/s
		{"IOWrite", fv.IOWrite, math.Log1p(20.0)}, // 20480/1024 = 20 KB/s
		{"CtxSwitches", fv.CtxSwitches, math.Log1p(500)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if math.Abs(tc.got-tc.expected) > 1e-9 {
				t.Errorf("feature %s: expected %f, got %f", tc.name, tc.expected, tc.got)
			}
		})
	}
}

func TestExtractFeatures_ZeroValue(t *testing.T) {
	// Should not panic on a completely zero ProcessStats.
	fv := extractFeatures(collector.ProcessStats{})
	if fv.CPU != 0 || fv.Memory != 0 || fv.Threads != 0 {
		t.Errorf("expected zero FeatureVector for zero ProcessStats, got %+v", fv)
	}
}

// ─── Cosine similarity ────────────────────────────────────────────────────────

func TestCosineSimilarity(t *testing.T) {
	t.Run("identical vectors -> 1.0", func(t *testing.T) {
		v := FeatureVector{CPU: 1, Memory: 1, Threads: 1}
		sim := cosineSimilarity(v, v)
		if math.Abs(sim-1.0) > 1e-9 {
			t.Errorf("expected 1.0, got %f", sim)
		}
	})
	t.Run("orthogonal vectors -> 0.0", func(t *testing.T) {
		v1 := FeatureVector{CPU: 1}
		v2 := FeatureVector{Memory: 1}
		if cosineSimilarity(v1, v2) != 0 {
			t.Errorf("expected 0.0 for orthogonal vectors")
		}
	})
	t.Run("zero vector -> 0.0 (no panic)", func(t *testing.T) {
		if cosineSimilarity(FeatureVector{}, FeatureVector{CPU: 1}) != 0 {
			t.Errorf("expected 0.0 when v1 is zero vector")
		}
	})
	t.Run("both zero vectors -> 0.0 (no panic)", func(t *testing.T) {
		if cosineSimilarity(FeatureVector{}, FeatureVector{}) != 0 {
			t.Errorf("expected 0.0 when both vectors are zero")
		}
	})
}

// ─── Predict — original archetypes ───────────────────────────────────────────

func TestPredictTableDriven(t *testing.T) {
	tests := []struct {
		desc             string
		stats            collector.ProcessStats
		expectedCategory Category
		minConfidence    float64
	}{
		{
			desc: "PostgreSQL relational database workload",
			stats: collector.ProcessStats{
				PID:           101,
				Name:          "postgres",
				Cmdline:       "/usr/bin/postgres -D /var/lib/postgresql/data",
				CpuUsage:      15.0,
				MemRss:        2 * 1024 * 1024 * 1024,
				Threads:       20,
				SocketCount:   100,
				FdCount:       150,
				IoReadSpeed:   2 * 1024 * 1024,
				IoWriteSpeed:  3 * 1024 * 1024,
				CtxSwitchRate: 1500,
			},
			expectedCategory: RelationalDB,
			minConfidence:    0.6,
		},
		{
			desc: "Redis cache store workload",
			stats: collector.ProcessStats{
				PID:           102,
				Name:          "redis-server",
				Cmdline:       "redis-server *:6379",
				CpuUsage:      50.0,
				MemRss:        500 * 1024 * 1024,
				Threads:       4,
				SocketCount:   1000,
				FdCount:       1100,
				IoReadSpeed:   0,
				IoWriteSpeed:  10000,
				CtxSwitchRate: 8000,
			},
			expectedCategory: CacheStore,
			minConfidence:    0.6,
		},
		{
			desc: "Nginx load balancer / web server workload",
			stats: collector.ProcessStats{
				PID:           103,
				Name:          "nginx",
				Cmdline:       "nginx: worker process",
				CpuUsage:      8.0,
				MemRss:        32 * 1024 * 1024,
				Threads:       1,
				SocketCount:   550,
				FdCount:       600,
				IoReadSpeed:   0,
				IoWriteSpeed:  500,
				CtxSwitchRate: 300,
			},
			expectedCategory: LoadBalancer,
			minConfidence:    0.5,
		},
		{
			desc: "PyTorch Deep Learning model training",
			stats: collector.ProcessStats{
				PID:           104,
				Name:          "python3",
				Cmdline:       "python3 train.py --batch-size 64 --model resnet",
				CpuUsage:      600.0,
				MemRss:        12 * 1024 * 1024 * 1024,
				Threads:       32,
				SocketCount:   2,
				FdCount:       45,
				IoReadSpeed:   5 * 1024 * 1024,
				IoWriteSpeed:  1000,
				CtxSwitchRate: 12000,
			},
			expectedCategory: AITraining,
			minConfidence:    0.6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			pred := Predict(tt.stats)
			if pred.PrimaryCategory != tt.expectedCategory {
				t.Errorf("expected %s, got %s (confidence=%.3f)", tt.expectedCategory, pred.PrimaryCategory, pred.Confidence)
			}
			if pred.Confidence < tt.minConfidence {
				t.Errorf("expected confidence >= %.2f, got %.3f", tt.minConfidence, pred.Confidence)
			}
		})
	}
}

// ─── Predict — new archetypes ─────────────────────────────────────────────────

func TestPredictNewArchetypes(t *testing.T) {
	tests := []struct {
		desc             string
		stats            collector.ProcessStats
		expectedCategory Category
		minConfidence    float64
	}{
		// ── Kafka event streaming ─────────────────────────────────────────
		{
			desc: "Kafka event streaming broker",
			stats: collector.ProcessStats{
				PID:           200,
				Name:          "kafka",
				Cmdline:       "java -jar kafka/bin/kafka-server-start.sh",
				CpuUsage:      35.0,
				MemRss:        4 * 1024 * 1024 * 1024,
				Threads:       90,
				SocketCount:   250,
				FdCount:       600,
				IoReadSpeed:   15 * 1024 * 1024,
				IoWriteSpeed:  25 * 1024 * 1024,
				CtxSwitchRate: 4000,
			},
			expectedCategory: EventStreaming,
			minConfidence:    0.6,
		},
		// ── Kong API gateway ─────────────────────────────────────────────
		{
			desc: "Kong API gateway",
			stats: collector.ProcessStats{
				PID:           201,
				Name:          "kong",
				Cmdline:       "kong start --conf /etc/kong/kong.conf",
				CpuUsage:      20.0,
				MemRss:        256 * 1024 * 1024,
				Threads:       24,
				SocketCount:   600,
				FdCount:       1500,
				IoReadSpeed:   50 * 1024,
				IoWriteSpeed:  50 * 1024,
				CtxSwitchRate: 4000,
			},
			expectedCategory: APIGateway,
			minConfidence:    0.5,
		},
		// ── Istio service mesh sidecar ────────────────────────────────────
		{
			desc: "Istio pilot-agent service mesh",
			stats: collector.ProcessStats{
				PID:           202,
				Name:          "pilot-agent",
				Cmdline:       "/usr/local/bin/pilot-agent proxy sidecar",
				CpuUsage:      8.0,
				MemRss:        128 * 1024 * 1024,
				Threads:       12,
				SocketCount:   800,
				FdCount:       900,
				IoReadSpeed:   20 * 1024,
				IoWriteSpeed:  20 * 1024,
				CtxSwitchRate: 8000,
			},
			expectedCategory: ServiceMesh,
			minConfidence:    0.5,
		},
		// ── InfluxDB time-series database ────────────────────────────────
		{
			desc: "InfluxDB time-series database",
			stats: collector.ProcessStats{
				PID:           203,
				Name:          "influxd",
				Cmdline:       "influxd --config /etc/influxdb/influxdb.conf",
				CpuUsage:      15.0,
				MemRss:        512 * 1024 * 1024,
				Threads:       16,
				SocketCount:   30,
				FdCount:       400,
				IoReadSpeed:   2 * 1024 * 1024,
				IoWriteSpeed:  8 * 1024 * 1024,
				CtxSwitchRate: 2000,
			},
			expectedCategory: TimeSeriesDB,
			minConfidence:    0.5,
		},
		// ── MinIO object store ────────────────────────────────────────────
		{
			desc: "MinIO object store",
			stats: collector.ProcessStats{
				PID:           204,
				Name:          "minio",
				Cmdline:       "minio server /data",
				CpuUsage:      10.0,
				MemRss:        512 * 1024 * 1024,
				Threads:       16,
				SocketCount:   100,
				FdCount:       800,
				IoReadSpeed:   30 * 1024 * 1024,
				IoWriteSpeed:  30 * 1024 * 1024,
				CtxSwitchRate: 1500,
			},
			expectedCategory: ObjectStore,
			minConfidence:    0.5,
		},
		// ── Fluent Bit log aggregator ─────────────────────────────────────
		{
			desc: "Fluent Bit log aggregator",
			stats: collector.ProcessStats{
				PID:           205,
				Name:          "fluent-bit",
				Cmdline:       "/usr/bin/fluent-bit -c /etc/fluent-bit/fluent-bit.conf",
				CpuUsage:      12.0,
				MemRss:        256 * 1024 * 1024,
				Threads:       8,
				SocketCount:   60,
				FdCount:       600,
				IoReadSpeed:   500 * 1024,
				IoWriteSpeed:  20 * 1024 * 1024,
				CtxSwitchRate: 1000,
			},
			expectedCategory: LogAggregator,
			minConfidence:    0.5,
		},
		// ── etcd service discovery ────────────────────────────────────────
		{
			desc: "etcd service discovery",
			stats: collector.ProcessStats{
				PID:           206,
				Name:          "etcd",
				Cmdline:       "/usr/local/bin/etcd --data-dir /var/lib/etcd",
				CpuUsage:      4.0,
				MemRss:        128 * 1024 * 1024,
				Threads:       8,
				SocketCount:   60,
				FdCount:       150,
				IoReadSpeed:   20 * 1024,
				IoWriteSpeed:  20 * 1024,
				CtxSwitchRate: 400,
			},
			expectedCategory: ServiceDiscovery,
			minConfidence:    0.5,
		},
		// ── Celery job worker ─────────────────────────────────────────────
		{
			desc: "Celery job worker",
			stats: collector.ProcessStats{
				PID:           207,
				Name:          "python3",
				Cmdline:       "celery -A myapp worker --loglevel=info",
				CpuUsage:      30.0,
				MemRss:        256 * 1024 * 1024,
				Threads:       8,
				SocketCount:   20,
				FdCount:       100,
				IoReadSpeed:   500 * 1024,
				IoWriteSpeed:  500 * 1024,
				CtxSwitchRate: 800,
			},
			expectedCategory: JobWorker,
			minConfidence:    0.5,
		},
		// ── JBoss legacy SOA ──────────────────────────────────────────────
		{
			desc: "JBoss legacy SOA application server",
			stats: collector.ProcessStats{
				PID:           208,
				Name:          "java",
				Cmdline:       "java -jar jboss-modules.jar -mp modules org.jboss.as.standalone",
				CpuUsage:      25.0,
				MemRss:        2 * 1024 * 1024 * 1024,
				Threads:       200,
				SocketCount:   20,
				FdCount:       250,
				IoReadSpeed:   300 * 1024,
				IoWriteSpeed:  300 * 1024,
				CtxSwitchRate: 1200,
			},
			expectedCategory: LegacySOAService,
			minConfidence:    0.5,
		},
		// ── GitHub Actions CI runner ──────────────────────────────────────
		{
			desc: "GitHub Actions CI runner",
			stats: collector.ProcessStats{
				PID:           209,
				Name:          "runner",
				Cmdline:       "github-actions runner --url https://github.com/myorg --token xxx",
				CpuUsage:      80.0,
				MemRss:        1024 * 1024 * 1024,
				Threads:       16,
				SocketCount:   10,
				FdCount:       200,
				IoReadSpeed:   8 * 1024 * 1024,
				IoWriteSpeed:  8 * 1024 * 1024,
				CtxSwitchRate: 600,
			},
			expectedCategory: CIRunner,
			minConfidence:    0.5,
		},
		// ── Kubernetes pause container (OrchestratorPod) ──────────────────
		{
			desc: "Kubernetes pause placeholder container",
			stats: collector.ProcessStats{
				PID:           210,
				Name:          "pause",
				Cmdline:       "/pause",
				CpuUsage:      0.0,
				MemRss:        512 * 1024, // 0.5 MB
				Threads:       1,
				SocketCount:   0,
				FdCount:       5,
				IoReadSpeed:   0,
				IoWriteSpeed:  0,
				CtxSwitchRate: 0,
			},
			expectedCategory: OrchestratorPod,
			minConfidence:    0.0, // pause is ambiguous; just confirm no panic
		},
		// ── Neo4j graph database ──────────────────────────────────────────
		{
			desc: "Neo4j graph database",
			stats: collector.ProcessStats{
				PID:           211,
				Name:          "neo4j",
				Cmdline:       "java -server -Neo4jServer neo4j start",
				CpuUsage:      40.0,
				MemRss:        3 * 1024 * 1024 * 1024,
				Threads:       32,
				SocketCount:   20,
				FdCount:       180,
				IoReadSpeed:   500 * 1024,
				IoWriteSpeed:  500 * 1024,
				CtxSwitchRate: 2000,
			},
			expectedCategory: GraphDB,
			minConfidence:    0.5,
		},
		// ── AI inference (Triton) ─────────────────────────────────────────
		{
			desc: "Triton AI inference server",
			stats: collector.ProcessStats{
				PID:           212,
				Name:          "tritonserver",
				Cmdline:       "tritonserver --model-repository=/models",
				CpuUsage:      120.0,
				MemRss:        8 * 1024 * 1024 * 1024,
				Threads:       40,
				SocketCount:   100,
				FdCount:       150,
				IoReadSpeed:   100 * 1024,
				IoWriteSpeed:  10 * 1024,
				CtxSwitchRate: 5000,
			},
			expectedCategory: AIInference,
			minConfidence:    0.6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			pred := Predict(tt.stats)
			if pred.PrimaryCategory != tt.expectedCategory {
				t.Errorf("expected %s, got %s (confidence=%.3f)", tt.expectedCategory, pred.PrimaryCategory, pred.Confidence)
			}
			if pred.Confidence < tt.minConfidence {
				t.Errorf("expected confidence >= %.2f, got %.3f", tt.minConfidence, pred.Confidence)
			}
			// Universal invariants
			if pred.Confidence < 0 || pred.Confidence > 1.0 {
				t.Errorf("confidence out of [0,1]: %.3f", pred.Confidence)
			}
			if pred.PrimaryCategory == "" {
				t.Error("primary category must not be empty")
			}
		})
	}
}

// ─── Zero-value safety ────────────────────────────────────────────────────────

func TestPredict_ZeroValue(t *testing.T) {
	// Completely empty ProcessStats must not panic and must return a valid prediction.
	pred := Predict(collector.ProcessStats{})
	if pred.PrimaryCategory == "" {
		t.Error("expected a non-empty category for zero-value ProcessStats")
	}
	if pred.Confidence < 0 || pred.Confidence > 1.0 {
		t.Errorf("confidence out of bounds: %f", pred.Confidence)
	}
	if pred.Scores == nil {
		t.Error("scores map must not be nil")
	}
	if pred.RulesTriggered == nil {
		t.Error("rules triggered must not be nil")
	}
}

// ─── Confidence bounds ────────────────────────────────────────────────────────

func TestPredict_ConfidenceBounds(t *testing.T) {
	// Run a range of extreme telemetry profiles and confirm [0,1] confidence always.
	extremes := []collector.ProcessStats{
		{CpuUsage: 9999.0, MemRss: 1<<62 - 1, Threads: 65535, SocketCount: 65535, FdCount: 65535, IoReadSpeed: 1e12, IoWriteSpeed: 1e12, CtxSwitchRate: 1e9},
		{CpuUsage: 0, MemRss: 0, Threads: 0, SocketCount: 0, FdCount: 0, IoReadSpeed: 0, IoWriteSpeed: 0, CtxSwitchRate: 0},
		{Name: "bash", CpuUsage: 0.01, MemRss: 4 * 1024 * 1024, Threads: 1},
		{Name: "postgres", Cmdline: "postgres -D /data", CpuUsage: 20.0, MemRss: 2 * 1024 * 1024 * 1024, Threads: 50},
	}
	for i, s := range extremes {
		pred := Predict(s)
		if pred.Confidence < 0 || pred.Confidence > 1.0 {
			t.Errorf("extreme case %d: confidence out of bounds: %.4f", i, pred.Confidence)
		}
	}
}

// ─── ContainsAny helper ───────────────────────────────────────────────────────

func TestContainsAny(t *testing.T) {
	cases := []struct {
		s        string
		targets  []string
		expected bool
	}{
		{"postgres", []string{"postgres", "mysql"}, true},
		{"nginx", []string{"apache", "caddy"}, false},
		{"", []string{"any"}, false},
		{"anything", []string{}, false},
		{"anything", []string{""}, false}, // empty target should not match
	}
	for _, tc := range cases {
		got := containsAny(tc.s, tc.targets...)
		if got != tc.expected {
			t.Errorf("containsAny(%q, %v) = %v, want %v", tc.s, tc.targets, got, tc.expected)
		}
	}
}

// ─── Centroids completeness ───────────────────────────────────────────────────

func TestCentroids_AllCategoriesHaveCentroid(t *testing.T) {
	// Every category exported from types.go should have a centroid entry.
	allCategories := []Category{
		LoadBalancer, APIGateway, ServiceMesh, CDNEdgeNode,
		WebServer, Microservice, ServerlessWorker, JobWorker, SchedulerDaemon,
		CacheStore, RelationalDB, NoSQLDB, ColumnarDB, VectorDB, TimeSeriesDB, GraphDB, ObjectStore,
		MessageBroker, EventStreaming, StreamProcessor,
		SearchEngine, OLAPEngine,
		AITraining, AIInference, MLPipeline, FeatureStore,
		OrchestratorAgent, OrchestratorPod, ServiceDiscovery, ConfigManager,
		MonitoringAgent, LogAggregator, TracingAgent,
		InteractiveShell, UtilityBatch, LegacySOAService, CIRunner,
	}
	for _, cat := range allCategories {
		if _, ok := Centroids[cat]; !ok {
			t.Errorf("category %s has no centroid in Centroids map", cat)
		}
	}
}

// ─── Benchmark ────────────────────────────────────────────────────────────────

func BenchmarkPredict(b *testing.B) {
	stats := collector.ProcessStats{
		PID:           999,
		Name:          "postgres",
		Cmdline:       "postgres -D /data",
		CpuUsage:      25.0,
		MemRss:        1024 * 1024 * 1024,
		Threads:       16,
		SocketCount:   80,
		FdCount:       100,
		IoReadSpeed:   5000,
		IoWriteSpeed:  5000,
		CtxSwitchRate: 1000,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Predict(stats)
	}
}

// ─── Fuzz harness ─────────────────────────────────────────────────────────────

func FuzzPredict(f *testing.F) {
	f.Add("postgres", "postgres -D /data")
	f.Add("python3", "python3 train.py")
	f.Add("nginx", "nginx: worker process")
	f.Add("kafka", "java -jar kafka.jar")
	f.Add("", "")

	f.Fuzz(func(t *testing.T, name, cmdline string) {
		stats := collector.ProcessStats{
			PID:           1000,
			Name:          name,
			Cmdline:       cmdline,
			CpuUsage:      50.0,
			MemRss:        1024 * 1024 * 1024,
			Threads:       10,
			SocketCount:   20,
			FdCount:       30,
			IoReadSpeed:   1000,
			IoWriteSpeed:  1000,
			CtxSwitchRate: 200,
		}

		pred := Predict(stats)
		if pred.PrimaryCategory == "" {
			t.Errorf("empty category for name=%q, cmdline=%q", name, cmdline)
		}
		if pred.Confidence < 0 || pred.Confidence > 1.0 {
			t.Errorf("invalid confidence %f for name=%q, cmdline=%q", pred.Confidence, name, cmdline)
		}
		if pred.Scores == nil {
			t.Errorf("nil scores for name=%q, cmdline=%q", name, cmdline)
		}
	})
}
