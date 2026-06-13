package optimizer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"
)

/*
 * Note: This file contains the optimization recommendation engine based on predicted workload categories.
 *
 * Caveat: Recommended kernel parameter settings (sysctls, ulimits, Transparent Huge Pages) are specific to Linux.
 * Running the same on non-Linux hosts will return nil recommendations.
 * Kindly apply recommendations with caution in staging environments before applying them to production systems.
 */

// Optimize analyzes a process prediction and appends context-aware system optimization recommendations.
// Note that the same verifies host kernel parameters against recommended values using context-aware host fs paths.
func Optimize(ctx context.Context, pred *classifier.Prediction) {
	if runtime.GOOS != "linux" {
		pred.Recommendations = nil
		return
	}

	var recs []string
	stats := pred.Telemetry

	// 1. Generic OS Resource Threshold Optimizations
	if stats.FdCount > 2000 {
		applied, limit := checkUlimitOpenFiles(ctx, stats.PID)
		msg := "OS Limits: High active file descriptors. Optimize limit: `ulimit -n 65535` to prevent exhaustion."
		if limit > 0 {
			recs = append(recs, formatRec(applied, msg, fmt.Sprintf("%d", limit)))
		} else {
			recs = append(recs, formatRec(false, msg, ""))
		}
	}
	if stats.CtxSwitchRate > 10000 {
		recs = append(recs, fmt.Sprintf("[RECOMMENDED] Scheduling: CPU context switch rate is elevated (%.1f/s). Configure thread affinity or investigate lock contention.", stats.CtxSwitchRate))
	}
	if float64(stats.SocketCount) > float64(stats.FdCount)*0.9 {
		recs = append(recs, "[RECOMMENDED] Resource Allocation: Network sockets consume >90% of file descriptors. Recommend increasing ulimit buffer limits.")
	}

	// 2. High-Level Design (HLD) Specific Optimizations
	switch pred.PrimaryCategory {
	case classifier.LoadBalancer:
		appliedReuse, curReuse := checkSysctlInt(ctx, "net.ipv4.tcp_tw_reuse", 1, ">=")
		recs = append(recs, formatRec(appliedReuse, "Gateway/LB: Configure TCP connection reuse: `sysctl -w net.ipv4.tcp_tw_reuse=1`", curReuse))

		recs = append(recs, "[RECOMMENDED] CPU Pinning: Bind gateway worker threads to separate CPU cores using `taskset`.")

		appliedConn, curConn := checkSysctlInt(ctx, "net.core.somaxconn", 32768, ">=")
		recs = append(recs, formatRec(appliedConn, "Socket Queue: Increase backlog connection limits: `sysctl -w net.core.somaxconn=32768`", curConn))

	case classifier.WebServer:
		recs = append(recs, "[RECOMMENDED] Web Server: Optimize connection timeouts to drop stale idle client connections early.")
		recs = append(recs, "[RECOMMENDED] Tuning: Enable TCP Keep-Alives and set max open files safe limit to >16384.")

	case classifier.CacheStore:
		appliedOvercommit, curOvercommit := checkSysctlInt(ctx, "vm.overcommit_memory", 1, "==")
		recs = append(recs, formatRec(appliedOvercommit, "Memory Safety: Prevent out-of-memory errors on Redis background writes: `sysctl -w vm.overcommit_memory=1`", curOvercommit))

		appliedSwap, curSwap := checkSysctlInt(ctx, "vm.swappiness", 0, "==")
		recs = append(recs, formatRec(appliedSwap, "Latency Safety: Disable swap partition swapoff or set swappiness to zero: `sysctl -w vm.swappiness=0`", curSwap))

		recs = append(recs, "[RECOMMENDED] Network: Increase socket receive/write queue bounds for high network packet workloads.")

	case classifier.RelationalDB:
		recs = append(recs, "[RECOMMENDED] Database: Enable OS Huge Pages (2MB or 1GB) to reduce CPU TLB cache overhead.")
		recs = append(recs, "[RECOMMENDED] Storage: Mount PostgreSQL/MySQL data directory using XFS with `noatime,nodiratime` options.")

		appliedDirtyBg, curDirtyBg := checkSysctlInt(ctx, "vm.dirty_background_ratio", 5, "<=")
		recs = append(recs, formatRec(appliedDirtyBg, "Kernel Writeback: Set dirty page flush parameters: `sysctl -w vm.dirty_background_ratio=5`", curDirtyBg))

		appliedDirty, curDirty := checkSysctlInt(ctx, "vm.dirty_ratio", 10, "<=")
		recs = append(recs, formatRec(appliedDirty, "Kernel Writeback: Set dirty page flush parameters: `sysctl -w vm.dirty_ratio=10`", curDirty))

	case classifier.NoSQLDB:
		appliedThp, curThp := checkTHPDisabled(ctx)
		recs = append(recs, formatRec(appliedThp, "NoSQL DB: Disable Transparent Huge Pages (THP) to avoid severe memory latency spikes: `echo never > /sys/kernel/mm/transparent_hugepage/enabled`", curThp))

		appliedMap, curMap := checkSysctlInt(ctx, "vm.max_map_count", 262144, ">=")
		recs = append(recs, formatRec(appliedMap, "Memory Mapping: Increase memory mapping limits: `sysctl -w vm.max_map_count=262144`", curMap))

	case classifier.ColumnarDB:
		recs = append(recs, "[RECOMMENDED] Analytics/OLAP: Set CPU scaling governor to high performance mode: `cpupower frequency-set -g performance`.")
		recs = append(recs, "[RECOMMENDED] Storage read: Increase block device read-ahead bounds (e.g. `blockdev --setra 4096 /dev/sdX`).")

	case classifier.VectorDB:
		recs = append(recs, "[RECOMMENDED] Vector search: Check SIMD compile instruction sets (AVX-512/NEON) to accelerate vector distance math.")
		recs = append(recs, "[RECOMMENDED] CPU/NUMA: Configure single node local allocator affinity to prevent memory bus latency.")

	case classifier.SearchEngine:
		recs = append(recs, "[RECOMMENDED] Search/Index: Enable JVM memory locking (`bootstrap.memory_lock: true`) to prevent garbage collector pauses from hitting swap disks.")

		appliedMap, curMap := checkSysctlInt(ctx, "vm.max_map_count", 262144, ">=")
		recs = append(recs, formatRec(appliedMap, "Memory Mapping: Configure virtual memory bounds: `sysctl -w vm.max_map_count=262144`", curMap))

	case classifier.MessageBroker:
		recs = append(recs, "[RECOMMENDED] Broker Queue: Set TCP network buffers optimization: `sysctl -w net.ipv4.tcp_rmem` and `tcp_wmem`.")
		recs = append(recs, "[RECOMMENDED] JVM tuning: Optimize garbage collector parameters (e.g. use G1GC or ZGC to limit stop-the-world pauses).")

	case classifier.EventStreaming:
		recs = append(recs, "[RECOMMENDED] Event Log: Separate Write-Ahead Logs (WAL) and index structures onto separate physical NVMe drives to prevent IO channel thrashing.")
		recs = append(recs, "[RECOMMENDED] IO Scheduler: Use `none` (for NVMe) or `deadline` I/O scheduler instead of fair queueing schedulers.")

	case classifier.AITraining:
		recs = append(recs, "[RECOMMENDED] AI/ML Training: Bind execution context to single NUMA sockets: `numactl --cpunodebind=0 --localalloc`.")
		recs = append(recs, "[RECOMMENDED] GPU transfers: Verify GPU-CPU transfer latency, check for CPU waiting loops.")

	case classifier.AIInference:
		recs = append(recs, "[RECOMMENDED] AI Serving: Configure Triton/TorchServe model parallel execution parameters to isolate worker thread pools.")
		recs = append(recs, "[RECOMMENDED] Tuning: Adjust model weights caching limits to maximize GPU/RAM occupancy.")

	case classifier.OrchestratorAgent:
		recs = append(recs, "[RECOMMENDED] Orchestration: Ensure cgroup driver is aligned with systemd defaults to avoid scheduling leaks.")

	case classifier.MonitoringAgent:
		recs = append(recs, "[RECOMMENDED] Metrics Collector: Optimize TSDB database writes: write metrics in batched formats to disk.")
		recs = append(recs, "[RECOMMENDED] Scraping: Adjust scrape intervals to reduce system network and socket connection spikes.")

	case classifier.InteractiveShell:
		recs = append(recs, "[RECOMMENDED] Interactive session: Keep session scheduling priority high (`nice -n -5`) for smooth responsiveness.")

	case classifier.UtilityBatch:
		recs = append(recs, "[RECOMMENDED] Batch task: Execute background batch workloads using nice and ionice priorities: `nice -n 19 ionice -c 3`.")
		recs = append(recs, "[RECOMMENDED] Storage: Use `tmpfs` (RAM disk) for writing short-lived temporary files to completely bypass slow storage drives.")
	}

	pred.Recommendations = recs
}

// formatRec returns a styled recommendation string depending on whether it is already applied.
func formatRec(applied bool, msg string, curValue string) string {
	if applied {
		if curValue != "" {
			return fmt.Sprintf("[ALREADY APPLIED] %s (current: %s)", msg, curValue)
		}
		return fmt.Sprintf("[ALREADY APPLIED] %s", msg)
	}

	if curValue != "" {
		return fmt.Sprintf("[PENDING] %s (current: %s)", msg, curValue)
	}
	return fmt.Sprintf("[PENDING] %s", msg)
}

// getHostProcPath resolves the base directory path of the /proc filesystem.
func getHostProcPath(ctx context.Context) string {
	return collector.GetHostProcPath(ctx)
}

// getHostSysPath resolves the base directory path of the /sys filesystem.
func getHostSysPath(ctx context.Context) string {
	return collector.GetHostSysPath(ctx)
}

// readSysctl reads a kernel parameter from the host's /proc/sys interface.
func readSysctl(ctx context.Context, name string) (string, error) {
	relPath := strings.ReplaceAll(name, ".", "/")
	hostProc := getHostProcPath(ctx)
	paramPath := filepath.Join(hostProc, "sys", relPath)
	data, err := readLimitFile(paramPath, 65536)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// checkSysctlInt verifies an integer sysctl parameter value on Linux.
func checkSysctlInt(ctx context.Context, name string, threshold int, op string) (bool, string) {
	if runtime.GOOS != "linux" {
		return false, ""
	}

	valStr, err := readSysctl(ctx, name)
	if err != nil {
		return false, ""
	}

	var val int
	if _, err := fmt.Sscanf(valStr, "%d", &val); err != nil {
		return false, valStr
	}

	applied := false
	switch op {
	case ">=":
		applied = val >= threshold
	case "<=":
		applied = val <= threshold
	case "==":
		applied = val == threshold
	}

	return applied, valStr
}

// checkUlimitOpenFiles reads the process limit file in /proc/<pid>/limits to check soft open file bounds.
func checkUlimitOpenFiles(ctx context.Context, pid int) (bool, int) {
	if runtime.GOOS != "linux" {
		return false, 0
	}

	hostProc := getHostProcPath(ctx)
	limitsPath := filepath.Join(hostProc, fmt.Sprintf("%d", pid), "limits")
	data, err := readLimitFile(limitsPath, 65536)
	if err != nil {
		return false, 0
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Max open files") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				var soft int
				if _, err := fmt.Sscanf(fields[3], "%d", &soft); err == nil {
					return soft >= 65535, soft
				}
			}
		}
	}

	return false, 0
}

// checkTHPDisabled reads transparent hugepage configuration to see if it is disabled on the host.
func checkTHPDisabled(ctx context.Context) (bool, string) {
	if runtime.GOOS != "linux" {
		return false, ""
	}

	hostSys := getHostSysPath(ctx)
	thpPath := filepath.Join(hostSys, "kernel", "mm", "transparent_hugepage", "enabled")
	data, err := readLimitFile(thpPath, 65536)
	if err != nil {
		return false, ""
	}

	valStr := strings.TrimSpace(string(data))
	return strings.Contains(valStr, "[never]"), valStr
}

// readLimitFile reads a file from the host filesystem with a size limit to prevent reading infinitely.
// Note that this is crucial for production stability when reading /proc files.
func readLimitFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(io.LimitReader(f, limit))
}

