package metrics

import (
	"sync"
	"time"

	"github.com/developer1622/proc-lens/pkg/classifier"

	"github.com/prometheus/client_golang/prometheus"
)

/*
 * Note: This file defines the custom Prometheus metrics registry and vector definitions.
 *
 * Caveat: To prevent namespace pollution of the default global Prometheus registry, a custom registry
 * is instantiated here. Register additional telemetry metrics here as needed.
 */

var (
	// Registry is a custom Prometheus registry to avoid namespace pollution.
	// This isolates our metrics from default Go collectors.
	Registry = prometheus.NewRegistry()

	// 1. proc_lens_scans_total (Counter)
	ScansTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "proc_lens",
		Name:      "scans_total",
		Help:      "Total number of collection cycles completed by the agent",
	}, []string{"mode"})

	// 2. proc_lens_collection_errors_total (Counter)
	CollectionErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "proc_lens",
		Name:      "collection_errors_total",
		Help:      "Total errors encountered while collecting telemetry",
	}, []string{"reason"})

	// 3. proc_lens_processes_classified_total (Counter)
	ProcessesClassifiedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "proc_lens",
		Name:      "processes_classified_total",
		Help:      "Total processes that received a primary HLD classification over time",
	}, []string{"category"})

	// 4. proc_lens_k8s_metadata_enrichment_total (Counter)
	K8sMetadataEnrichmentTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "proc_lens",
		Name:      "k8s_metadata_enrichment_total",
		Help:      "Attempts to attach container/pod metadata via cgroup and log scanning",
	}, []string{"status"})

	// 5. proc_lens_processes_scanned (Gauge)
	ProcessesScanned = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proc_lens",
		Name:      "processes_scanned",
		Help:      "Number of processes discovered in the most recent collection cycle",
	})

	// 6. proc_lens_predictions (Gauge)
	PredictionsCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "proc_lens",
		Name:      "predictions",
		Help:      "Number of processes passing thresholds and classified in the last cycle",
	}, []string{"category"})

	// 7. proc_lens_agent_cpu_usage_percent (Gauge)
	AgentCpuUsagePercent = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proc_lens",
		Name:      "agent_cpu_usage_percent",
		Help:      "CPU usage percentage of the agent process itself",
	})

	// 8. proc_lens_agent_memory_rss_bytes (Gauge)
	AgentMemoryRssBytes = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proc_lens",
		Name:      "agent_memory_rss_bytes",
		Help:      "Resident Set Size (RSS) memory of the agent process in bytes",
	})

	// 9. proc_lens_k8s_metadata_success_rate (Gauge)
	K8sMetadataSuccessRate = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "proc_lens",
		Name:      "k8s_metadata_success_rate",
		Help:      "Fraction of containerized processes successfully mapped to pod metadata",
	})

	// 10. proc_lens_collection_duration_seconds (Histogram)
	CollectionDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "proc_lens",
		Name:      "collection_duration_seconds",
		Help:      "Wall time to complete one full collection + classification cycle",
		Buckets:   []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0},
	})

	// 11. proc_lens_per_process_collection_seconds (Histogram)
	PerProcessCollectionSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Namespace: "proc_lens",
		Name:      "per_process_collection_seconds",
		Help:      "Time spent collecting telemetry for a single process",
		Buckets:   []float64{0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5},
	})

	// 12. proc_lens_http_requests_total (Counter)
	HttpRequestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "proc_lens",
		Name:      "http_requests_total",
		Help:      "Total number of HTTP requests processed by the metrics/healthz server",
	}, []string{"path", "status"})
)

var registerOnce sync.Once

func init() {
	registerOnce.Do(func() {
		Registry.MustRegister(ScansTotal)
		Registry.MustRegister(CollectionErrorsTotal)
		Registry.MustRegister(ProcessesClassifiedTotal)
		Registry.MustRegister(K8sMetadataEnrichmentTotal)
		Registry.MustRegister(ProcessesScanned)
		Registry.MustRegister(PredictionsCount)
		Registry.MustRegister(AgentCpuUsagePercent)
		Registry.MustRegister(AgentMemoryRssBytes)
		Registry.MustRegister(K8sMetadataSuccessRate)
		Registry.MustRegister(CollectionDurationSeconds)
		Registry.MustRegister(PerProcessCollectionSeconds)
		Registry.MustRegister(HttpRequestsTotal)
	})
}

// RecordCollectionError increments the error counter for a specific reason.
func RecordCollectionError(reason string) {
	CollectionErrorsTotal.WithLabelValues(reason).Inc()
}

// RecordProcessCollectionDuration records telemetry collection duration for a single process.
func RecordProcessCollectionDuration(duration time.Duration) {
	PerProcessCollectionSeconds.Observe(duration.Seconds())
}

// RecordK8sMetadataEnrichment increments the counter for pod metadata mapping status.
func RecordK8sMetadataEnrichment(status string) {
	K8sMetadataEnrichmentTotal.WithLabelValues(status).Inc()
}

// RecordHttpRequest increments the HTTP request counter.
func RecordHttpRequest(path, status string) {
	HttpRequestsTotal.WithLabelValues(path, status).Inc()
}

// UpdateMetrics updates all registered Prometheus metrics with the latest scan metrics.
func UpdateMetrics(mode string, scannedCount int, predictions []classifier.Prediction, loopDuration time.Duration, agentCpu float64, agentRss uint64) {
	ScansTotal.WithLabelValues(mode).Inc()
	CollectionDurationSeconds.Observe(loopDuration.Seconds())
	ProcessesScanned.Set(float64(scannedCount))

	AgentCpuUsagePercent.Set(agentCpu)
	AgentMemoryRssBytes.Set(float64(agentRss))

	// Reset predictions gauge to prevent stale categories from lingering
	PredictionsCount.Reset()

	predCounts := make(map[string]float64)
	var k8sAttempts, k8sSuccesses float64

	for _, p := range predictions {
		catStr := string(p.PrimaryCategory)
		predCounts[catStr]++

		// Record cumulative counter of classifications by category
		ProcessesClassifiedTotal.WithLabelValues(catStr).Inc()

		// K8s metadata success metrics
		if p.Telemetry.ContainerID != "" {
			k8sAttempts++
			if p.Telemetry.PodName != "" {
				k8sSuccesses++
				K8sMetadataEnrichmentTotal.WithLabelValues("success").Inc()
			} else {
				K8sMetadataEnrichmentTotal.WithLabelValues("failed").Inc()
			}
		}
	}

	// Update predictions gauges
	for cat, count := range predCounts {
		PredictionsCount.WithLabelValues(cat).Set(count)
	}

	// Update K8s success rate
	if k8sAttempts > 0 {
		K8sMetadataSuccessRate.Set(k8sSuccesses / k8sAttempts)
	} else {
		K8sMetadataSuccessRate.Set(0.0)
	}
}

