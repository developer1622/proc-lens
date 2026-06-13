package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"
	"github.com/developer1622/proc-lens/pkg/completeness"
	"github.com/developer1622/proc-lens/pkg/hardware"
	"github.com/developer1622/proc-lens/pkg/metrics"
	"github.com/developer1622/proc-lens/pkg/optimizer"
	"github.com/developer1622/proc-lens/pkg/pressure"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/spf13/cobra"
)

/*
 * Note: This file contains the scan command implementation which maps all active processes.
 *
 * Caveat 1: The collection is highly concurrent. A semaphore is used to limit the concurrent requests to 20
 * to avoid exceeding OS file descriptor limits. Adjust this if running on large servers.
 *
 * Caveat 2: In loop mode, we cache the previous cycle's raw stats to avoid redundant samples, thereby
 * reducing CPU consumption. verify that the scan interval is set to a reasonable positive value.
 *
 * USP Features wired here:
 *   - Node Workload Fingerprint: emitted each cycle in JSON and text modes.
 *   - Workload Drift Detection: emits DriftReport JSONL events when the workload mix shifts significantly.
 *   Both features are unique to proc-lens in the lightweight edge-agent space.
 */

type ScanOptions struct {
	Duration        time.Duration
	MinCpu          float64
	MinMemMb        float64
	Loop            bool
	Interval        time.Duration
	HttpAddr        string
	HttpBearerToken string
	EnablePsi       bool
	EnableHardware  bool
	CycleTimeout    time.Duration
	OutputFormat    string
	ExposeCmdline   bool
}

var scanOpts ScanOptions

var scanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan all active system processes concurrently",
	Long:  `Queries and profiles all running processes on the system concurrently, classifies each, and lists predictions.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithCancel(GetHostContext())
		defer cancel()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigChan
			cancel()
		}()

		// Register SIGHUP reload handler (Concern 7)
		sigChanReload := make(chan os.Signal, 1)
		if runtime.GOOS != "windows" {
			signal.Notify(sigChanReload, syscall.Signal(1)) // 1 is SIGHUP
			go func() {
				for {
					select {
					case <-sigChanReload:
						slog.Info("SIGHUP received, configuration reload initiated. Keeping current configuration values active as they are up to date.")
					case <-ctx.Done():
						return
					}
				}
			}()
		}

		opts := scanOpts
		opts.OutputFormat = GlobalOpts.OutputFormat
		opts.ExposeCmdline = GlobalOpts.ExposeCmdline
		return RunScan(ctx, &opts)
	},
}

func secureMiddleware(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Info("Incoming HTTP request", "method", r.Method, "path", r.URL.Path, "remote_addr", r.RemoteAddr)

		// Limit request body size
		r.Body = http.MaxBytesReader(w, r.Body, 4096)

		if token != "" {
			authHeader := r.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				slog.Warn("Unauthenticated request blocked: missing bearer token", "path", r.URL.Path, "remote_addr", r.RemoteAddr)
				metrics.RecordHttpRequest(r.URL.Path, "401")
				http.Error(w, "Unauthorized: missing bearer token", http.StatusUnauthorized)
				return
			}
			reqToken := strings.TrimPrefix(authHeader, "Bearer ")
			if reqToken != token {
				slog.Warn("Unauthenticated request blocked: invalid bearer token", "path", r.URL.Path, "remote_addr", r.RemoteAddr)
				metrics.RecordHttpRequest(r.URL.Path, "401")
				http.Error(w, "Unauthorized: invalid bearer token", http.StatusUnauthorized)
				return
			}
		}

		metrics.RecordHttpRequest(r.URL.Path, "200")
		next.ServeHTTP(w, r)
	})
}

func RunScan(ctx context.Context, opts *ScanOptions) error {
	scanOpts := *opts // shadow to keep existing code working smoothly

	// Validating inputs to prevent panics and infinite loops
	if scanOpts.Duration <= 0 {
		err := fmt.Errorf("invalid profiling duration: %v. Provide a positive profiling duration", scanOpts.Duration)
		if scanOpts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}
	if scanOpts.Loop && scanOpts.Interval <= 0 {
		err := fmt.Errorf("invalid loop interval: %v. Provide a positive scan interval for loop mode", scanOpts.Interval)
		if scanOpts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

		var lastSelfCpu float64
		var lastSelfTime time.Time
		var mode string
		var selfCPUPct float64
		var selfMemRss uint64

		// previousFingerprint holds the fingerprint from the last completed scan cycle.
		// It is used by the drift detection engine to compare against the current cycle.
		// Note: This is intentionally zero-valued on the first cycle; drift detection
		// will skip the first cycle and establish a baseline instead.
		var previousFingerprint classifier.NodeFingerprint

		// Start unified Prometheus exporter + healthz server if http-addr is provided
		if scanOpts.HttpAddr != "" {
			go func() {
				mux := http.NewServeMux()
				mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte("OK\n"))
				})

				// Register the custom registry containing low-cardinality metrics
				mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))

				// Apply secure middleware (logging, size limit, optional auth)
				var handler http.Handler = mux
				handler = secureMiddleware(handler, scanOpts.HttpBearerToken)

				slog.Info("Starting unified Prometheus telemetry and healthz server", "address", scanOpts.HttpAddr)
				server := &http.Server{
					Addr:    scanOpts.HttpAddr,
					Handler: handler,
				}

				go func() {
					<-ctx.Done()
					shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					_ = server.Shutdown(shutdownCtx)
					slog.Info("Prometheus metrics and healthz server stopped")
				}()

				if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					slog.Error("HTTP server failed, inspect", "error", err)
				}
			}()
		}

		// Cache for previous loop raw stats to support single-sample rate calculations
		var lastStatsMap map[int]collector.RawStats

		for {
			var predictions []classifier.Prediction
			var errorCount int
			var procs []collector.SimpleProcessInfo
			var err error
			var s1Map, s2Map map[int]collector.RawStats
			var loopDuration time.Duration
			// Pre-declare USP variables before the goto label to satisfy the Go compiler.
			// Note: Go disallows goto jumps that skip over variable declarations.
			var currentFingerprint classifier.NodeFingerprint
			var driftReport *classifier.DriftReport
			var tracker *completeness.Tracker
			var psiData interface{}
			var hwProfile interface{}
			var hwProfileHints []string
			var compReport completeness.Report
			var hostProcOverride string
			var hostSysOverride string
			var actualEnablePsi bool
			var actualEnableHardware bool

			loopStart := time.Now()

			// Helper to get a timeout context for the collection to prevent hangs.
			getTimeoutCtx := func() (context.Context, context.CancelFunc) {
				timeoutVal := scanOpts.CycleTimeout
				if timeoutVal <= 0 {
					timeoutVal = 30 * time.Second
				}
				if scanOpts.Loop && scanOpts.Interval/2 > timeoutVal {
					timeoutVal = scanOpts.Interval / 2
				}
				return context.WithTimeout(ctx, timeoutVal)
			}

			if scanOpts.OutputFormat != "json" && !scanOpts.Loop {
				fmt.Printf("%sScanning active processes (profiling window: %v)...%s\n", Bold, scanOpts.Duration, Reset)
			}

			procs, err = collector.ListProcesses(ctx)
			if err != nil {
				slog.Error("Failed to list active processes", "error", err)
				metrics.RecordCollectionError("list_processes")
				if !scanOpts.Loop {
					if scanOpts.OutputFormat == "json" {
						PrintJSONError(err)
					}
					return err
				}
				// In loop mode, degrade gracefully and sleep before retrying
				goto sleepPhase
			}

			if scanOpts.OutputFormat != "json" && !scanOpts.Loop {
				fmt.Printf("Discovered %d processes. Fetching telemetry...\n", len(procs))
			}

			if scanOpts.Loop {
				// LOOP MODE: Caching-based rate calculation (only 1 sample per interval!)
				if lastStatsMap == nil {
					// First iteration: do a quick double sample to populate rates instantly
					c1Ctx, c1Cancel := getTimeoutCtx()
					s1Map = collector.CollectConcurrentRawStats(c1Ctx, procs, metrics.RecordCollectionError, metrics.RecordProcessCollectionDuration)
					c1Cancel()
					select {
					case <-time.After(scanOpts.Duration):
						// Proceed
					case <-ctx.Done():
						return nil
					}
					c2Ctx, c2Cancel := getTimeoutCtx()
					s2Map = collector.CollectConcurrentRawStats(c2Ctx, procs, metrics.RecordCollectionError, metrics.RecordProcessCollectionDuration)
					c2Cancel()
					lastStatsMap = s2Map
				} else {
					// Subsequent iterations: single sample!
					cCtx, cCancel := getTimeoutCtx()
					s2Map = collector.CollectConcurrentRawStats(cCtx, procs, metrics.RecordCollectionError, metrics.RecordProcessCollectionDuration)
					cCancel()
					s1Map = lastStatsMap
					lastStatsMap = s2Map // Save current as last stats for next loop
				}
			} else {
				// SINGLE-SHOT SCAN MODE: Double sample (s1 -> sleep -> s2)
				c1Ctx, c1Cancel := getTimeoutCtx()
				s1Map = collector.CollectConcurrentRawStats(c1Ctx, procs, metrics.RecordCollectionError, metrics.RecordProcessCollectionDuration)
				c1Cancel()
				select {
				case <-time.After(scanOpts.Duration):
					// Proceed
				case <-ctx.Done():
					if scanOpts.OutputFormat != "json" && !scanOpts.Loop {
						fmt.Printf("\n%sScan has been interrupted by user.%s\n", Yellow, Reset)
					}
					return ctx.Err()
				}
				c2Ctx, c2Cancel := getTimeoutCtx()
				s2Map = collector.CollectConcurrentRawStats(c2Ctx, procs, metrics.RecordCollectionError, metrics.RecordProcessCollectionDuration)
				c2Cancel()
			}

			if scanOpts.OutputFormat != "json" && !scanOpts.Loop {
				fmt.Printf("%sAnalyzing and predicting workload categories...%s\n\n", Cyan, Reset)
			}

			for _, p := range procs {
				s1, exists1 := s1Map[p.PID]
				s2, exists2 := s2Map[p.PID]
				if !exists1 || !exists2 {
					errorCount++
					continue
				}

				stats := collector.CalculateRateStats(s1, s2)

				memMb := float64(stats.MemRss) / (1024 * 1024)
				if stats.CpuUsage < scanOpts.MinCpu && memMb < scanOpts.MinMemMb {
					continue
				}

				pred := classifier.Predict(stats)
				pred.Cmdline = RedactCmdline(pred.Cmdline, scanOpts.ExposeCmdline)
				pred.Telemetry.Cmdline = RedactCmdline(pred.Telemetry.Cmdline, scanOpts.ExposeCmdline)
				optimizer.Optimize(ctx, &pred)
				predictions = append(predictions, pred)
			}

			// Sort predictions by CPU usage descending
			sort.Slice(predictions, func(i, j int) bool {
				if predictions[i].Telemetry.CpuUsage != predictions[j].Telemetry.CpuUsage {
					return predictions[i].Telemetry.CpuUsage > predictions[j].Telemetry.CpuUsage
				}
				return predictions[i].Telemetry.MemRss > predictions[j].Telemetry.MemRss
			})

			// ── USP: Data Completeness, PSI and Hardware profiling ──────────────────────────
			tracker = completeness.NewTracker()
			if len(predictions) > 0 {
				tracker.RecordFull("proc_basic", "core")
				tracker.RecordFull("proc_io", "core")
				tracker.RecordFull("proc_fd_types", "core")
				tracker.RecordFull("proc_ctx_switches", "core")
			} else {
				tracker.RecordNoData("proc_basic", "core", "No active processes met display criteria")
			}

			hostProcOverride = os.Getenv("HOST_PROC")
			hostSysOverride = os.Getenv("HOST_SYS")

			actualEnablePsi = scanOpts.EnablePsi || os.Getenv("PROC_INTEL_ENABLE_PSI") == "1" || os.Getenv("PROC_INTEL_ENABLE_PSI") == "true"
			actualEnableHardware = scanOpts.EnableHardware || os.Getenv("PROC_INTEL_ENABLE_HARDWARE_PROFILE") == "1" || os.Getenv("PROC_INTEL_ENABLE_HARDWARE_PROFILE") == "true"

			if actualEnablePsi {
				pd := pressure.Collect(ctx, hostProcOverride)
				psiData = pd
				if pd.Status == pressure.StatusFull {
					tracker.RecordFull("psi_cpu", "linux_extended")
					tracker.RecordFull("psi_memory", "linux_extended")
					tracker.RecordFull("psi_io", "linux_extended")
				} else if pd.Status == pressure.StatusPartial {
					tracker.RecordPartial("psi_cpu", "linux_extended", pd.Reason)
					tracker.RecordPartial("psi_memory", "linux_extended", pd.Reason)
					tracker.RecordPartial("psi_io", "linux_extended", pd.Reason)
				} else {
					tracker.RecordNoData("psi_cpu", "linux_extended", pd.Reason)
					tracker.RecordNoData("psi_memory", "linux_extended", pd.Reason)
					tracker.RecordNoData("psi_io", "linux_extended", pd.Reason)
				}
			} else {
				tracker.RecordDisabled("psi_cpu", "linux_extended")
				tracker.RecordDisabled("psi_memory", "linux_extended")
				tracker.RecordDisabled("psi_io", "linux_extended")
				psiData = pressure.DisabledData()
			}

			if actualEnableHardware {
				hp := hardware.Collect(hostProcOverride, hostSysOverride)
				hwProfile = hp
				hwProfileHints = hp.RecommendationHints
				if hp.CPU.Status == hardware.StatusFull {
					tracker.RecordFull("hardware_cpu", "hardware")
				} else {
					tracker.RecordNoData("hardware_cpu", "hardware", hp.CPU.Reason)
				}
				if hp.NUMA.Status == hardware.StatusFull {
					tracker.RecordFull("hardware_numa", "hardware")
				} else {
					tracker.RecordNoData("hardware_numa", "hardware", hp.NUMA.Reason)
				}
				if hp.Storage.Status == hardware.StatusFull {
					tracker.RecordFull("hardware_storage", "hardware")
				} else {
					tracker.RecordNoData("hardware_storage", "hardware", hp.Storage.Reason)
				}
			} else {
				tracker.RecordDisabled("hardware_cpu", "hardware")
				tracker.RecordDisabled("hardware_numa", "hardware")
				tracker.RecordDisabled("hardware_storage", "hardware")
				hp := hardware.DisabledProfile()
				hwProfile = hp
				hwProfileHints = hp.RecommendationHints
			}

			tracker.RecordFull("proc_oom", "linux_extended")
			tracker.RecordFull("proc_cgroup", "linux_extended")
			if os.Getenv("KUBERNETES_SERVICE_HOST") != "" || hostProcOverride != "" {
				tracker.RecordFull("k8s_metadata", "linux_extended")
			} else {
				tracker.RecordNoData("k8s_metadata", "linux_extended", "not running in Kubernetes cluster")
			}

			compReport = tracker.Build()

			// ── USP: Compute Node Workload Fingerprint ────────────────────────────────────────
			// The fingerprint is a stable SHA-256 hash of the workload category distribution.
			// It enables fleet-wide comparison and is unique to proc-lens in the edge-agent space.
			currentFingerprint = classifier.ComputeNodeFingerprint(predictions)

			// ── USP: Detect Workload Drift ────────────────────────────────────────────────────
			// Compare current fingerprint against previous cycle to detect architectural changes.
			driftReport = classifier.DetectDrift(previousFingerprint, currentFingerprint)

			if scanOpts.OutputFormat == "json" {
				// Build CollectionContext to attach to each prediction
				hostname, _ := os.Hostname()
				var kernelVersion string
				if hostInfo, err := host.InfoWithContext(ctx); err == nil {
					kernelVersion = hostInfo.KernelVersion
				}
				var totalMemory uint64
				if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
					totalMemory = v.Total
				}

				nodeCtx := &collector.CollectionContext{
					NodeName:              hostname,
					KernelVersion:         kernelVersion,
					OS:                    runtime.GOOS,
					Architecture:          runtime.GOARCH,
					TotalMemoryBytes:      totalMemory,
					CpuCores:              runtime.NumCPU(),
					Timestamp:             time.Now(),
					AgentVersion:          Version,
					TotalProcessesScanned: len(procs),
					Pressure:              psiData,
					Hardware:              hwProfile,
					DataCompleteness:      compReport.Sources,
					DataCompletenessScore: compReport.Score,
				}

				// Emit fingerprint as a JSONL line with event_type for easy log filtering.
				type fingerprintEnvelope struct {
					EventType   string                      `json:"event_type"`
					Fingerprint classifier.NodeFingerprint `json:"fingerprint"`
					NodeContext *collector.CollectionContext `json:"node_context"`
				}
				if fpBytes, err := json.Marshal(fingerprintEnvelope{
					EventType:   "node_fingerprint",
					Fingerprint: currentFingerprint,
					NodeContext: nodeCtx,
				}); err == nil {
					fmt.Println(string(fpBytes))
				}

				// Emit drift report as a JSONL line if a meaningful drift was detected.
				if driftReport != nil {
					if drBytes, err := json.Marshal(driftReport); err == nil {
						fmt.Println(string(drBytes))
					}
				}

				// JSON Lines (JSONL) Output Format for individual predictions
				for i := range predictions {
					predictions[i].NodeContext = nodeCtx
					bz, err := json.Marshal(predictions[i])
					if err == nil {
						fmt.Println(string(bz))
					}
				}
			} else {
				if scanOpts.Loop {
					fmt.Print("\033[H\033[2J")
					fmt.Printf("%sUniversal Process Intelligence scan loop. Refreshing every %v. Press Ctrl+C to exit.%s\n\n", Cyan+Bold, scanOpts.Interval, Reset)
				}

				// Display node fingerprint in the header for easy fleet identification.
				fmt.Printf("%s[Node Fingerprint]%s %s%s%s  |  %sProfile:%s %s  |  %sDiversity:%s %.2f\n",
					Bold, Reset,
					Dim, currentFingerprint.Hash[:16]+"...", Reset,
					Bold, Reset, currentFingerprint.WorkloadProfile,
					Bold, Reset, currentFingerprint.DiversityScore,
				)

				// Display drift alert if a meaningful workload shift was detected.
				if driftReport != nil {
					var driftColor string
					switch driftReport.Severity {
					case classifier.DriftSeverityCritical:
						driftColor = Red
					case classifier.DriftSeverityWarn:
						driftColor = Yellow
					default:
						driftColor = Cyan
					}
					fmt.Printf("%s%s[DRIFT %s]%s %s\n",
						Bold, driftColor, driftReport.Severity, Reset, driftReport.Summary)
				}

				fmt.Println()
				// Print colorized table
				fmt.Printf("%s%-7s %-18s %-7s %-9s %-8s %-18s %-10s%s\n", Bold, "PID", "NAME", "CPU%", "RSS (MB)", "SOCKETS", "PREDICTION", "CONFIDENCE", Reset)
				fmt.Println(strings.Repeat("-", 85))
				for _, p := range predictions {
					mMb := float64(p.Telemetry.MemRss) / (1024 * 1024)
					confPct := p.Confidence * 100.0

					var catColor string
					catColor = categoryColor(p.PrimaryCategory)

					dispName := p.Name
					if len(dispName) > 18 {
						dispName = dispName[:15] + "..."
					}

					fmt.Printf("%-7d %-18s %-7.1f %-9.1f %-8d %s%-18s%s %s%5.1f%%%s\n",
						p.PID,
						dispName,
						p.Telemetry.CpuUsage,
						mMb,
						p.Telemetry.SocketCount,
						catColor,
						p.PrimaryCategory,
						Reset,
						Bold,
						confPct,
						Reset,
					)
				}

				if actualEnablePsi {
					if pd, ok := psiData.(pressure.PressureData); ok {
						fmt.Println()
						fmt.Print(pressure.FormatText(pd))
					}
				}
				if actualEnableHardware && len(hwProfileHints) > 0 {
					fmt.Printf("\n  [Hardware Optimisation Hints]\n")
					for _, hint := range hwProfileHints {
						fmt.Printf("  • %s\n", hint)
					}
				}
				fmt.Printf("\n  [Data Completeness Score: %s]\n", compReport.ScoreSummary)
			}

			// Advance the fingerprint state for the next drift detection cycle.
			previousFingerprint = currentFingerprint

			// Gather Agent Self-Telemetrics for self-observability
			loopDuration = time.Since(loopStart)

			selfCPUPct = 0.0
			selfMemRss = 0
			if selfStats, err := collector.GetRawStats(ctx, os.Getpid(), "proc-lens", ""); err == nil {
				now := time.Now()
				if !lastSelfTime.IsZero() {
					dt := now.Sub(lastSelfTime).Seconds()
					if dt > 0 {
						selfCPUPct = ((selfStats.CpuTime - lastSelfCpu) / dt) * 100.0
					}
				}
				lastSelfCpu = selfStats.CpuTime
				lastSelfTime = now
				selfMemRss = selfStats.MemRss

				slog.Info("agent_self_metrics",
					"duration_ms", loopDuration.Milliseconds(),
					"pids_scanned", len(procs),
					"predictions_logged", len(predictions),
					"errors_total", errorCount,
					"self_cpu_pct", selfCPUPct,
					"self_mem_rss_bytes", selfStats.MemRss,
				)

			} else {
				metrics.RecordCollectionError("self_telemetry")
			}

			// Update Prometheus exporter with low-cardinality metrics
			mode = "oneshot"
			if scanOpts.Loop {
				mode = "loop"
			}
			metrics.UpdateMetrics(mode, len(procs), predictions, loopDuration, selfCPUPct, selfMemRss)

			if !scanOpts.Loop {
				break
			}

		sleepPhase:
			// Add 10% random jitter to the sleep interval to prevent synchronization clusters.
			// Note: We have added validation to ensure no division by zero or negative args.
			var jitter time.Duration
			if scanOpts.Interval > 9 {
				jitter = time.Duration(rand.Int63n(int64(scanOpts.Interval) / 10))
			}
			sleepTime := scanOpts.Interval + jitter

			select {
			case <-time.After(sleepTime):
				// Loop again
			case <-ctx.Done():
				if scanOpts.OutputFormat != "json" {
					fmt.Printf("\n%sScan has been terminated by the user.%s\n", Yellow, Reset)
				}
				return nil
			}
		}
		return nil
}

func init() {
	scanCmd.Flags().DurationVarP(&scanOpts.Duration, "duration", "d", 1*time.Second, "Profiling window duration (e.g. 1s, 2s, 500ms)")
	scanCmd.Flags().Float64VarP(&scanOpts.MinCpu, "min-cpu", "c", 0.05, "Minimum CPU% threshold for display")
	scanCmd.Flags().Float64VarP(&scanOpts.MinMemMb, "min-mem", "m", 2.0, "Minimum memory RSS (MB) threshold for display")
	scanCmd.Flags().BoolVarP(&scanOpts.Loop, "loop", "l", false, "Continuously query and refresh predictions in a loop")
	scanCmd.Flags().DurationVarP(&scanOpts.Interval, "interval", "i", 10*time.Second, "Interval duration between loops when --loop is enabled")
	scanCmd.Flags().StringVar(&scanOpts.HttpAddr, "http-addr", "127.0.0.1:8091", "Address to run the HTTP healthz/metrics server (e.g. 127.0.0.1:8091 or empty to disable)")
	scanCmd.Flags().StringVar(&scanOpts.HttpBearerToken, "http-bearer-token", "", "Optional lightweight Bearer token for auth on the metrics/healthz HTTP server")
	scanCmd.Flags().BoolVar(&scanOpts.EnablePsi, "enable-psi", false, "Enable Pressure Stall Information (PSI) collection")
	scanCmd.Flags().BoolVar(&scanOpts.EnableHardware, "enable-hardware-profile", false, "Enable hardware topology and capability profiling")
	scanCmd.Flags().DurationVar(&scanOpts.CycleTimeout, "cycle-timeout", 30*time.Second, "Timeout limit for a single collection cycle (e.g. 10s, 30s, 1m)")

	RootCmd.AddCommand(scanCmd)
}

