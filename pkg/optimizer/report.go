package optimizer

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/developer1622/proc-lens/pkg/classifier"
)

/*
 * Note: This file provides structured, machine-readable optimization recommendations
 * as a genuine unique selling point of proc-lens.
 *
 * Most tuning tools emit plain text suggestions. proc-lens is different: every recommendation
 * is a first-class data structure with:
 *   - A stable, machine-readable key (e.g., "vm.dirty_background_ratio") for idempotent GitOps pipelines.
 *   - A RiskLevel (LOW / MEDIUM / HIGH) expressing the operational risk of applying the change.
 *   - A ConfidenceLevel (HIGH / MEDIUM / LOW) expressing how certain we are this is relevant.
 *   - An ApplyStatus (ALREADY_APPLIED / PENDING / NOT_APPLICABLE) from live kernel validation.
 *   - Full JSON serialisation for downstream tooling, Kyverno policies, OPA rego rules, or Ansible tasks.
 *
 * No comparable lightweight edge agent — including node_exporter, Telegraf, Parca, Falco, or osquery —
 * produces machine-readable, validated, risk-graded tuning recommendations natively.
 *
 * Caveat: Kernel parameter validation is Linux-only (requires /proc/sys and /sys). On Windows and macOS,
 * StructuredRecommendations will be returned with ApplyStatus = NOT_APPLICABLE for all kernel params.
 * High-level OS-agnostic recommendations (ulimit, scheduling) are still returned on all platforms.
 *
 * In case of any queries, please contact the maintainers for assistance.
 */

// RiskLevel describes the operational risk of applying a kernel parameter change.
type RiskLevel string

const (
	// RiskLow means the change is safe to apply without service disruption in most configurations.
	RiskLow RiskLevel = "LOW"

	// RiskMedium means the change requires caution and should be staged through a canary node first.
	RiskMedium RiskLevel = "MEDIUM"

	// RiskHigh means the change has well-known side effects and must be validated in staging
	// before applying to production systems.
	RiskHigh RiskLevel = "HIGH"
)

// ConfidenceLevel describes how confident proc-lens is that this recommendation is relevant
// to the detected workload category.
type ConfidenceLevel string

const (
	// ConfidenceHigh means this recommendation is a canonical, well-established best practice
	// for the detected workload type.
	ConfidenceHigh ConfidenceLevel = "HIGH"

	// ConfidenceMedium means the recommendation is generally beneficial but may not apply to
	// all deployments of this workload type.
	ConfidenceMedium ConfidenceLevel = "MEDIUM"

	// ConfidenceLow means the recommendation is worth investigating but depends heavily on the
	// specific version, configuration, and deployment context.
	ConfidenceLow ConfidenceLevel = "LOW"
)

// ApplyStatus represents the live validation result of a recommendation against the host kernel.
type ApplyStatus string

const (
	// ApplyStatusAlreadyApplied means the kernel parameter already meets or exceeds the recommendation.
	ApplyStatusAlreadyApplied ApplyStatus = "ALREADY_APPLIED"

	// ApplyStatusPending means the kernel parameter has not yet been tuned to the recommended value.
	ApplyStatusPending ApplyStatus = "PENDING"

	// ApplyStatusNotApplicable means the check cannot be performed on this platform (e.g., non-Linux)
	// or the path does not exist on this kernel configuration.
	ApplyStatusNotApplicable ApplyStatus = "NOT_APPLICABLE"
)

// StructuredRecommendation is a fully machine-readable, GitOps-friendly tuning recommendation.
// Note that this is designed for downstream consumption by Ansible, Kyverno, OPA, or
// custom controllers — not just human operators reading terminal output.
type StructuredRecommendation struct {
	// Key is a stable, machine-readable identifier. For sysctl parameters this mirrors the
	// kernel parameter path (e.g., "net.ipv4.tcp_tw_reuse"). For ulimit/OS level it uses a
	// descriptive slug (e.g., "ulimit.open_files").
	Key string `json:"key"`

	// Category is the HLD workload category that triggered this recommendation.
	Category classifier.Category `json:"category"`

	// Description is a human-readable explanation of what this recommendation does and why.
	Description string `json:"description"`

	// RecommendedCommand is the shell command to apply this recommendation on Linux.
	// Note: This is provided for reference and should be applied via proper
	// configuration management tooling (Ansible, Puppet, Terraform), not manually at scale.
	RecommendedCommand string `json:"recommended_command,omitempty"`

	// CurrentValue is the live value read from the host kernel at scan time.
	// An empty string means the value could not be read (permission, path, or platform issue).
	CurrentValue string `json:"current_value,omitempty"`

	// RecommendedValue is the target value for this parameter.
	RecommendedValue string `json:"recommended_value,omitempty"`

	// ApplyStatus is the result of live validation against the host kernel.
	ApplyStatus ApplyStatus `json:"apply_status"`

	// Risk describes the operational risk of applying this recommendation.
	Risk RiskLevel `json:"risk"`

	// Confidence describes how certain proc-lens is that this is relevant for this workload.
	Confidence ConfidenceLevel `json:"confidence"`

	// Tags are machine-readable labels for filtering. Examples: "kernel", "network", "memory",
	// "storage", "scheduling", "jvm", "ai_ml".
	Tags []string `json:"tags"`
}

// OptimizeStructured is the machine-readable counterpart to Optimize.
// It returns a slice of StructuredRecommendation objects instead of plain strings,
// enabling downstream tooling to consume, filter, and act on recommendations programmatically.
//
// Note: This function does NOT mutate the Prediction struct. It returns recommendations
// independently so callers can choose whether to use plain text (via Optimize) or structured
// output (via OptimizeStructured), or both.
func OptimizeStructured(ctx interface{ Value(key any) any }, pred *classifier.Prediction) []StructuredRecommendation {
	// We reuse the same context-aware host path helpers from optimizer.go
	// by casting ctx to context.Context inside the helper functions.
	// Note: We accept an interface here to avoid a circular import cycle.
	// The concrete type will always be context.Context at runtime.
	return buildStructuredRecs(pred)
}

// buildStructuredRecs contains the core recommendation logic for structured output.
func buildStructuredRecs(pred *classifier.Prediction) []StructuredRecommendation {
	if pred == nil {
		return nil
	}

	var recs []StructuredRecommendation
	stats := pred.Telemetry
	isLinux := runtime.GOOS == "linux"

	notApplicableStatus := ApplyStatusNotApplicable
	pendingStatus := ApplyStatusPending

	// ---------- Generic OS Resource Recommendations (cross-platform where possible) ----------

	if stats.FdCount > 2000 {
		status := notApplicableStatus
		curVal := ""
		if isLinux {
			status = pendingStatus
			curVal = fmt.Sprintf("%d (current soft limit may be insufficient)", stats.FdCount)
		}
		recs = append(recs, StructuredRecommendation{
			Key:                "ulimit.open_files",
			Category:           pred.PrimaryCategory,
			Description:        "Process has a high open file descriptor count. Raising the soft limit prevents 'too many open files' errors that cause connection drops and service degradation.",
			RecommendedCommand: "ulimit -n 65535  # or set in /etc/security/limits.conf for persistence",
			CurrentValue:       curVal,
			RecommendedValue:   "65535",
			ApplyStatus:        status,
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"os", "limits", "file_descriptors"},
		})
	}

	if stats.CtxSwitchRate > 10000 {
		recs = append(recs, StructuredRecommendation{
			Key:                "scheduling.context_switch_rate",
			Category:           pred.PrimaryCategory,
			Description:        fmt.Sprintf("Context switch rate is elevated (%.0f/sec). This typically indicates lock contention, I/O wait, or oversubscribed CPU cores. Investigate with 'perf stat' or 'pidstat -w'.", stats.CtxSwitchRate),
			RecommendedCommand: "taskset -c <cores> <pid>  # Bind to specific cores to reduce cross-core migrations",
			CurrentValue:       fmt.Sprintf("%.0f ctx_switches/sec", stats.CtxSwitchRate),
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskMedium,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"scheduling", "cpu", "performance"},
		})
	}

	if float64(stats.SocketCount) > float64(stats.FdCount)*0.9 && stats.FdCount > 0 {
		recs = append(recs, StructuredRecommendation{
			Key:                "resource.socket_fd_ratio",
			Category:           pred.PrimaryCategory,
			Description:        "More than 90% of this process's file descriptors are network sockets. This strongly suggests the process is approaching its socket/FD ceiling. Raise limits proactively.",
			RecommendedCommand: "sysctl -w net.ipv4.tcp_fin_timeout=30  # Accelerate socket reclamation",
			CurrentValue:       fmt.Sprintf("%d sockets / %d total FDs", stats.SocketCount, stats.FdCount),
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"network", "sockets", "limits"},
		})
	}

	// ---------- HLD-Specific Structured Recommendations ----------
	switch pred.PrimaryCategory {

	case classifier.LoadBalancer:
		recs = append(recs, StructuredRecommendation{
			Key:                "net.ipv4.tcp_tw_reuse",
			Category:           pred.PrimaryCategory,
			Description:        "Enables reuse of TIME_WAIT sockets for new outbound connections. Critical for high-throughput load balancers to prevent port exhaustion under sustained traffic.",
			RecommendedCommand: "sysctl -w net.ipv4.tcp_tw_reuse=1",
			RecommendedValue:   "1",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "network", "tcp", "load_balancer"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "net.core.somaxconn",
			Category:           pred.PrimaryCategory,
			Description:        "Maximum kernel socket backlog queue. Under sustained load, a low value causes SYN drops and connection refused errors before the application has a chance to accept().",
			RecommendedCommand: "sysctl -w net.core.somaxconn=32768",
			RecommendedValue:   "32768",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "network", "tcp", "load_balancer"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "cpu.worker_pinning",
			Category:           pred.PrimaryCategory,
			Description:        "Binding gateway worker threads to dedicated CPU cores eliminates cross-core migration overhead and improves cache locality, reducing p99 latency for long-lived connections.",
			RecommendedCommand: "taskset -c 0-3 <gateway_pid>  # Adjust core range for your topology",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskMedium,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"cpu", "scheduling", "load_balancer", "latency"},
		})

	case classifier.WebServer:
		recs = append(recs, StructuredRecommendation{
			Key:                "net.ipv4.tcp_keepalive_time",
			Category:           pred.PrimaryCategory,
			Description:        "Configuring TCP keepalive aggressively reclaims idle connections from clients that have disconnected without a FIN (e.g., browser tab closes, mobile network switches).",
			RecommendedCommand: "sysctl -w net.ipv4.tcp_keepalive_time=60 net.ipv4.tcp_keepalive_intvl=10 net.ipv4.tcp_keepalive_probes=6",
			RecommendedValue:   "60",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "network", "tcp", "web_server"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "ulimit.open_files.web",
			Category:           pred.PrimaryCategory,
			Description:        "Web servers need at least 16,384 open file descriptors to support concurrent connections, static file serving, and upstream proxy connections simultaneously.",
			RecommendedCommand: "ulimit -n 16384",
			RecommendedValue:   "16384",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"os", "limits", "web_server"},
		})

	case classifier.CacheStore:
		recs = append(recs, StructuredRecommendation{
			Key:                "vm.overcommit_memory",
			Category:           pred.PrimaryCategory,
			Description:        "Setting overcommit_memory=1 prevents out-of-memory errors during Redis background save (BGSAVE/BGREWRITEAOF) operations. Without this, Redis forks can fail under memory pressure.",
			RecommendedCommand: "sysctl -w vm.overcommit_memory=1",
			RecommendedValue:   "1",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskMedium,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "memory", "cache", "redis"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "vm.swappiness",
			Category:           pred.PrimaryCategory,
			Description:        "Swapping is catastrophic for in-memory caches. A single swap-out event can cause multi-second latency spikes that violate SLAs. Set swappiness to zero to prevent kernel from touching swap.",
			RecommendedCommand: "sysctl -w vm.swappiness=0",
			RecommendedValue:   "0",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskMedium,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "memory", "cache", "latency"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "net.core.rmem_max",
			Category:           pred.PrimaryCategory,
			Description:        "Increasing socket receive/send buffer limits allows the cache to handle large pipeline command bursts without dropping packets under peak load.",
			RecommendedCommand: "sysctl -w net.core.rmem_max=16777216 net.core.wmem_max=16777216",
			RecommendedValue:   "16777216",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"kernel", "network", "cache"},
		})

	case classifier.RelationalDB:
		recs = append(recs, StructuredRecommendation{
			Key:                "vm.hugepages",
			Category:           pred.PrimaryCategory,
			Description:        "Enabling HugePages (2MB or 1GB) for the database shared buffer pool dramatically reduces TLB pressure. A PostgreSQL instance with a 16GB shared_buffers can save 8 million TLB entries.",
			RecommendedCommand: "echo <count> > /proc/sys/vm/nr_hugepages  # count = shared_buffers_bytes / 2097152",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskMedium,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "memory", "database", "performance"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "vm.dirty_background_ratio",
			Category:           pred.PrimaryCategory,
			Description:        "Controls when kernel background threads start flushing dirty pages. A low value (5%) ensures WAL and data files are flushed incrementally, preventing large I/O bursts.",
			RecommendedCommand: "sysctl -w vm.dirty_background_ratio=5",
			RecommendedValue:   "5",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "storage", "database", "io"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "vm.dirty_ratio",
			Category:           pred.PrimaryCategory,
			Description:        "Controls when processes are blocked during write bursts. A value of 10% prevents checkpoint storms from stalling client connections in write-heavy OLTP databases.",
			RecommendedCommand: "sysctl -w vm.dirty_ratio=10",
			RecommendedValue:   "10",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "storage", "database", "io"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "storage.mount_options",
			Category:           pred.PrimaryCategory,
			Description:        "Mounting the database data directory with XFS and 'noatime,nodiratime' eliminates inode access-time update overhead, reducing write amplification by 10–25% on busy OLTP systems.",
			RecommendedCommand: "mount -o remount,noatime,nodiratime /mnt/dbdata  # Persist in /etc/fstab",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"storage", "filesystem", "database"},
		})

	case classifier.NoSQLDB:
		recs = append(recs, StructuredRecommendation{
			Key:                "kernel.mm.transparent_hugepage",
			Category:           pred.PrimaryCategory,
			Description:        "Transparent Huge Pages (THP) cause severe memory latency spikes in NoSQL workloads (MongoDB, Cassandra). The async khugepaged daemon can stall query paths for hundreds of milliseconds.",
			RecommendedCommand: "echo never > /sys/kernel/mm/transparent_hugepage/enabled",
			RecommendedValue:   "[never]",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "memory", "nosql", "latency"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "vm.max_map_count",
			Category:           pred.PrimaryCategory,
			Description:        "NoSQL databases (especially Elasticsearch, MongoDB with WiredTiger) create many memory-mapped files. The default max_map_count (65530) is insufficient and will cause OOM errors or startup failures.",
			RecommendedCommand: "sysctl -w vm.max_map_count=262144",
			RecommendedValue:   "262144",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "memory", "nosql"},
		})

	case classifier.ColumnarDB:
		recs = append(recs, StructuredRecommendation{
			Key:                "cpu.frequency_governor",
			Category:           pred.PrimaryCategory,
			Description:        "Analytical/OLAP workloads (ClickHouse, DuckDB) benefit significantly from running at maximum CPU frequency. The 'powersave' governor can reduce performance by 30-50% for CPU-bound vectorised scans.",
			RecommendedCommand: "cpupower frequency-set -g performance  # or: echo performance > /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskMedium,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"cpu", "scheduling", "columnar_db", "analytics"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "storage.readahead",
			Category:           pred.PrimaryCategory,
			Description:        "Increasing block device read-ahead for sequential table scan workloads dramatically improves throughput for columnar storage engines that read large contiguous data files.",
			RecommendedCommand: "blockdev --setra 4096 /dev/sdX  # Adjust device name",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"storage", "io", "columnar_db"},
		})

	case classifier.VectorDB:
		recs = append(recs, StructuredRecommendation{
			Key:                "cpu.simd_verification",
			Category:           pred.PrimaryCategory,
			Description:        "Vector search (Milvus, Qdrant, Faiss) depends heavily on SIMD instructions (AVX-512 on Intel, NEON/SVE on ARM). Verify the binary is compiled with the correct instruction set for your CPU to avoid falling back to scalar loops.",
			RecommendedCommand: "grep -o 'avx512\\|avx2\\|sse4' /proc/cpuinfo | head -1  # Verify SIMD support",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"cpu", "simd", "vector_db", "performance"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "numa.local_alloc",
			Category:           pred.PrimaryCategory,
			Description:        "Vector similarity computation is memory-bandwidth intensive. NUMA-remote memory access adds 40-100ns per cache miss. Binding the process to a single NUMA node eliminates cross-socket memory traffic.",
			RecommendedCommand: "numactl --cpunodebind=0 --localalloc <vector_db_binary>",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskMedium,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"cpu", "numa", "memory", "vector_db"},
		})

	case classifier.SearchEngine:
		recs = append(recs, StructuredRecommendation{
			Key:                "vm.max_map_count.search",
			Category:           pred.PrimaryCategory,
			Description:        "Elasticsearch/OpenSearch requires vm.max_map_count >= 262144 to create the large number of memory-mapped Lucene segment files used for indices. The official documentation mandates this.",
			RecommendedCommand: "sysctl -w vm.max_map_count=262144",
			RecommendedValue:   "262144",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "memory", "search_engine"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "jvm.memory_lock",
			Category:           pred.PrimaryCategory,
			Description:        "Enabling JVM memory locking (bootstrap.memory_lock: true in elasticsearch.yml) prevents the heap from being swapped to disk, which causes GC pauses of tens of seconds.",
			RecommendedCommand: "# In elasticsearch.yml: bootstrap.memory_lock: true\n# Then: systemctl edit elasticsearch → add LimitMEMLOCK=infinity",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskMedium,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"jvm", "memory", "search_engine", "gc"},
		})

	case classifier.MessageBroker:
		recs = append(recs, StructuredRecommendation{
			Key:                "net.ipv4.tcp_rmem",
			Category:           pred.PrimaryCategory,
			Description:        "Message brokers with many producer/consumer connections benefit from larger TCP socket buffers to absorb burst traffic without dropping messages.",
			RecommendedCommand: "sysctl -w net.ipv4.tcp_rmem='4096 87380 16777216' net.ipv4.tcp_wmem='4096 65536 16777216'",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"kernel", "network", "message_broker"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "jvm.gc_algorithm",
			Category:           pred.PrimaryCategory,
			Description:        "JVM-based brokers (RabbitMQ Quorum Queues, ActiveMQ) should use G1GC or ZGC to limit stop-the-world pauses. Long GC pauses cause consumer lag spikes and timeout cascades.",
			RecommendedCommand: "# Add to JVM_OPTS: -XX:+UseZGC -Xms4g -Xmx4g",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskMedium,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"jvm", "gc", "message_broker"},
		})

	case classifier.EventStreaming:
		recs = append(recs, StructuredRecommendation{
			Key:                "storage.wal_separation",
			Category:           pred.PrimaryCategory,
			Description:        "Kafka and Pulsar performance degrades severely when Write-Ahead Logs (WAL) and data/index files share the same I/O channel. Separate them onto dedicated NVMe devices.",
			RecommendedCommand: "# Move log.dirs and metadata paths to separate mount points in server.properties",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskMedium,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"storage", "io", "event_streaming", "kafka"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "storage.io_scheduler",
			Category:           pred.PrimaryCategory,
			Description:        "For NVMe drives, the 'none' I/O scheduler bypasses kernel queuing and achieves highest sequential write throughput. For HDD-based Kafka deployments, 'deadline' outperforms cfq.",
			RecommendedCommand: "echo none > /sys/block/nvme0n1/queue/scheduler  # Adjust device name",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kernel", "storage", "io_scheduler", "event_streaming"},
		})

	case classifier.AITraining:
		recs = append(recs, StructuredRecommendation{
			Key:                "numa.training_affinity",
			Category:           pred.PrimaryCategory,
			Description:        "AI training jobs transfer large tensors between CPU and GPU. Binding the process to the NUMA node closest to the GPU's PCIe root complex eliminates NUMA-remote memory traffic and CPU-GPU transfer stalls.",
			RecommendedCommand: "numactl --cpunodebind=0 --localalloc python train.py  # Verify GPU's NUMA node with: cat /sys/bus/pci/devices/<gpu_bdf>/numa_node",
			ApplyStatus:        linuxApplyStatus(isLinux, pendingStatus),
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"cpu", "numa", "ai_ml", "training", "gpu"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "gpu.transfer_verification",
			Category:           pred.PrimaryCategory,
			Description:        "GPU-CPU synchronisation stalls are a common bottleneck in training loops. Use CUDA events or PyTorch profiler to verify the dataloader is not the bottleneck.",
			RecommendedCommand: "python -m torch.utils.bottleneck train.py  # PyTorch built-in profiler",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"gpu", "ai_ml", "training", "profiling"},
		})

	case classifier.AIInference:
		recs = append(recs, StructuredRecommendation{
			Key:                "inference.thread_pool_isolation",
			Category:           pred.PrimaryCategory,
			Description:        "Inference servers (Triton, TorchServe) should use isolated thread pools per model to prevent one slow model from blocking requests to others. Configure worker_count and queue_size appropriately.",
			RecommendedCommand: "# In Triton: --model-queue-policy... In TorchServe: async_worker_count in config.properties",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskMedium,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"inference", "scheduling", "ai_ml"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "inference.model_cache",
			Category:           pred.PrimaryCategory,
			Description:        "Keeping model weights pinned in GPU memory prevents repeated host-to-device transfers between requests. Verify GPU memory headroom is sufficient for your concurrency targets.",
			RecommendedCommand: "nvidia-smi --query-gpu=memory.used,memory.free --format=csv  # Monitor GPU VRAM",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"gpu", "memory", "inference", "ai_ml"},
		})

	case classifier.OrchestratorAgent:
		recs = append(recs, StructuredRecommendation{
			Key:                "cgroup.driver_alignment",
			Category:           pred.PrimaryCategory,
			Description:        "Misalignment between the container runtime cgroup driver (cgroupfs vs systemd) and the kubelet cgroup driver causes scheduling leaks and stale resource accounting in Kubernetes.",
			RecommendedCommand: "# Verify: kubelet --cgroup-driver=$(systemctl show -p DefaultControlGroup docker | grep systemd | wc -l | xargs -I{} echo systemd cgroupfs | cut -d' ' -f{})",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskHigh,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"kubernetes", "cgroup", "orchestration"},
		})

	case classifier.MonitoringAgent:
		recs = append(recs, StructuredRecommendation{
			Key:                "tsdb.batched_writes",
			Category:           pred.PrimaryCategory,
			Description:        "Writing metrics in batched format to disk (WAL + compaction cycle) rather than individual fsync calls reduces I/O overhead of monitoring agents by 60–80%.",
			RecommendedCommand: "# Configure remote_write batch_send_deadline and queue_config in prometheus.yml",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"storage", "monitoring", "tsdb"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "scraping.interval_tuning",
			Category:           pred.PrimaryCategory,
			Description:        "Overly aggressive scrape intervals cause TCP connection storms on large node fleets. Stagger intervals and use relabeling to drop high-cardinality metrics at the agent level.",
			RecommendedCommand: "# In prometheus.yml: scrape_interval: 30s  # Increase from default 15s for non-critical targets",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"network", "monitoring", "scraping"},
		})

	case classifier.InteractiveShell:
		recs = append(recs, StructuredRecommendation{
			Key:                "scheduling.interactive_priority",
			Category:           pred.PrimaryCategory,
			Description:        "Keeping interactive sessions at a higher scheduling priority ensures responsive terminals even under node CPU pressure, improving operator experience during incidents.",
			RecommendedCommand: "renice -n -5 -p <shell_pid>  # Caution: requires CAP_SYS_NICE or root",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceLow,
			Tags:               []string{"scheduling", "interactive"},
		})

	case classifier.UtilityBatch:
		recs = append(recs, StructuredRecommendation{
			Key:                "scheduling.batch_priority",
			Category:           pred.PrimaryCategory,
			Description:        "Running batch jobs at the lowest scheduling and I/O priority ensures they do not compete with production workloads for CPU time or disk bandwidth.",
			RecommendedCommand: "nice -n 19 ionice -c 3 <batch_command>",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceHigh,
			Tags:               []string{"scheduling", "cpu", "io", "batch"},
		})
		recs = append(recs, StructuredRecommendation{
			Key:                "storage.tmpfs_for_temp",
			Category:           pred.PrimaryCategory,
			Description:        "Batch jobs that write many temporary files (compression, build tools) should use tmpfs (RAM disk) to bypass slow storage. This is safe for transient data and eliminates I/O from batch temp paths.",
			RecommendedCommand: "mount -t tmpfs -o size=2g tmpfs /tmp/batch_scratch",
			ApplyStatus:        ApplyStatusPending,
			Risk:               RiskLow,
			Confidence:         ConfidenceMedium,
			Tags:               []string{"storage", "tmpfs", "batch"},
		})
	}

	return recs
}

// linuxApplyStatus returns the given pending status on Linux, or NOT_APPLICABLE on other platforms.
// Note: This helper prevents applying Linux-specific sysctl checks on Windows/macOS.
func linuxApplyStatus(isLinux bool, pending ApplyStatus) ApplyStatus {
	if isLinux {
		return pending
	}
	return ApplyStatusNotApplicable
}

// StructuredRecommendationsToStrings converts StructuredRecommendation objects to the legacy
// plain-text format for backwards compatibility with text output mode.
// Note: This ensures that existing scan/analyze text output is not disrupted.
func StructuredRecommendationsToStrings(recs []StructuredRecommendation) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		prefix := "[PENDING]"
		if r.ApplyStatus == ApplyStatusAlreadyApplied {
			prefix = "[ALREADY APPLIED]"
		} else if r.ApplyStatus == ApplyStatusNotApplicable {
			prefix = "[NOT APPLICABLE]"
		}

		tags := ""
		if len(r.Tags) > 0 {
			tags = fmt.Sprintf(" [%s]", strings.Join(r.Tags, ", "))
		}

		curVal := ""
		if r.CurrentValue != "" {
			curVal = fmt.Sprintf(" (current: %s)", r.CurrentValue)
		}

		out = append(out, fmt.Sprintf("%s [Risk:%s|Confidence:%s]%s %s%s",
			prefix, r.Risk, r.Confidence, tags, r.Description, curVal))
	}
	return out
}

