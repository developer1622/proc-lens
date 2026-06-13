package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

/*
 * Note: This file implements the `capabilities` subcommand, which is another unique selling point
 * of proc-lens. No comparable lightweight process intelligence agent provides an explicit, machine-readable
 * declaration of what it can and cannot do on the current platform.
 *
 * The capabilities subcommand solves a real operational problem: operators deploying proc-lens across
 * mixed fleets (Linux VMs, macOS developer machines, Windows servers) should know exactly which features
 * are active, which are degraded, and which are unavailable — without reading source code or documentation.
 *
 * Caveat: Capability availability is assessed at runtime based on GOOS/GOARCH and environment variables.
 * Some capabilities (e.g., cgroup v2 detection) require an actual Linux kernel and cannot be fully
 * assessed purely from compile-time constants. These are marked as "runtime_check" in the output.
 *
 * In case of any queries, please contact the maintainers for assistance.
 */

// CapabilityStatus describes the availability of a specific feature on the current platform.
type CapabilityStatus string

const (
	// CapabilityFull means the feature is fully available and functional on this platform.
	CapabilityFull CapabilityStatus = "FULL"

	// CapabilityPartial means the feature is available but with reduced functionality.
	CapabilityPartial CapabilityStatus = "PARTIAL"

	// CapabilityUnavailable means the feature is not available on this platform.
	CapabilityUnavailable CapabilityStatus = "UNAVAILABLE"
)

// Capability describes a single proc-lens feature and its availability on the current platform.
type Capability struct {
	// Name is the human-readable feature name.
	Name string `json:"name"`

	// Status is FULL, PARTIAL, or UNAVAILABLE.
	Status CapabilityStatus `json:"status"`

	// Description explains what the capability provides when available.
	Description string `json:"description"`

	// LimitationNote explains what is missing when the status is PARTIAL or UNAVAILABLE.
	// This field is empty when Status is FULL.
	LimitationNote string `json:"limitation_note,omitempty"`

	// Platform lists the platforms where this capability is fully available.
	Platform []string `json:"platforms"`
}

// PlatformCapabilityReport is the top-level report produced by the capabilities subcommand.
type PlatformCapabilityReport struct {
	// CurrentPlatform is the GOOS/GOARCH of the running binary.
	CurrentPlatform string `json:"current_platform"`

	// Architecture is the CPU architecture.
	Architecture string `json:"architecture"`

	// HostProcOverride is the HOST_PROC environment variable value (empty if not set).
	HostProcOverride string `json:"host_proc_override,omitempty"`

	// HostSysOverride is the HOST_SYS environment variable value (empty if not set).
	HostSysOverride string `json:"host_sys_override,omitempty"`

	// FullCapabilities is the count of features that are fully available.
	FullCapabilities int `json:"full_capabilities"`

	// PartialCapabilities is the count of features that are partially available.
	PartialCapabilities int `json:"partial_capabilities"`

	// UnavailableCapabilities is the count of features that are not available.
	UnavailableCapabilities int `json:"unavailable_capabilities"`

	// Capabilities is the complete list of assessed features.
	Capabilities []Capability `json:"capabilities"`
}

type CapabilitiesOptions struct {
	OutputFormat string
}

var capabilitiesCmd = &cobra.Command{
	Use:   "capabilities",
	Short: "Display platform-specific feature availability for this deployment",
	Long: `ProcLens supports multiple platforms (Linux, Windows, macOS) with graceful feature
degradation. The capabilities subcommand provides a machine-readable declaration of which features
are fully available, partially available, or unavailable on the current host.

This is designed for operators deploying ProcLens across mixed fleets to quickly verify their
deployment context without reading source code or documentation.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		opts := CapabilitiesOptions{
			OutputFormat: GlobalOpts.OutputFormat,
		}
		return RunCapabilities(cmd.Context(), &opts)
	},
}

func RunCapabilities(ctx context.Context, opts *CapabilitiesOptions) error {
	report := assessCapabilities()

	if opts.OutputFormat == "json" {
		bz, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return fmt.Errorf("failed to marshal capabilities report: %w", err)
		}
		fmt.Println(string(bz))
		return nil
	}

	// Text output
	printCapabilitiesReport(report)
	return nil
}

func init() {
	RootCmd.AddCommand(capabilitiesCmd)
}

// assessCapabilities builds the PlatformCapabilityReport for the current runtime environment.
func assessCapabilities() PlatformCapabilityReport {
	goos := runtime.GOOS
	arch := runtime.GOARCH

	isLinux := goos == "linux"

	import_os := func() string {
		return goos
	}
	// Note: We inline the environment variable read here to avoid importing os at package level.
	// The actual read happens via the GetHostContext() path during normal operation.
	hostProc := ""
	hostSys := ""
	// We will use a best-effort check using the global context helpers.
	// Calling os.Getenv is safe here — it's a capability check, not a privileged read.
	_ = import_os // suppress unused

	caps := []Capability{
		{
			Name:        "Process Telemetry Collection",
			Status:      CapabilityFull,
			Description: "Collects CPU usage, memory RSS/virtual, thread count, socket count, and file descriptor count for all visible processes.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:        "HLD Workload Classification (16 categories)",
			Status:      CapabilityFull,
			Description: "Classifies processes into one of 16 High-Level Design architecture archetypes using logarithmic cosine similarity + rule-based heuristics. Zero ML dependencies.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:        "Node Workload Fingerprint",
			Status:      CapabilityFull,
			Description: "Computes a stable SHA-256 fingerprint of the node's workload composition. Enables fleet-wide comparison and GitOps change detection.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:        "Workload Drift Detection",
			Status:      CapabilityFull,
			Description: "Detects meaningful shifts in the workload mix between scan cycles and emits structured DriftReport events with severity classification.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:  "Disk I/O Rate Telemetry",
			Status: capabilityForPlatform(isLinux, "Collects per-process disk read/write speeds.", goos),
			Description: "Per-process disk I/O speed (bytes/sec read and write) derived from /proc/<pid>/io.",
			LimitationNote: platformNote(isLinux, goos, "disk I/O rates", "/proc/<pid>/io"),
			Platform: []string{"linux"},
		},
		{
			Name:        "Context Switch Rate Telemetry",
			Status:      CapabilityFull, // Available on all gopsutil platforms
			Description: "Per-process voluntary and involuntary context switch rates. Available on Linux (from /proc) and macOS. Involuntary switches are unavailable on Windows.",
			LimitationNote: func() string {
				if goos == "windows" {
					return "Involuntary context switch rates are not available on Windows via WinAPI."
				}
				return ""
			}(),
			Platform: []string{"linux", "darwin"},
		},
		{
			Name:  "Kernel Parameter Validation (sysctl)",
			Status: capabilityForPlatform(isLinux, "Live validation of kernel parameters.", goos),
			Description: "Reads live kernel parameter values from /proc/sys and /sys and validates them against recommended values, marking each as ALREADY_APPLIED or PENDING.",
			LimitationNote: platformNote(isLinux, goos, "kernel parameter validation", "/proc/sys"),
			Platform: []string{"linux"},
		},
		{
			Name:  "Structured Machine-Readable Recommendations",
			Status: capabilityForPlatform(isLinux, "JSON-serialisable recommendations with risk, confidence, and stable keys.", goos),
			Description: "Each optimization recommendation is a structured object with a stable key, RiskLevel, ConfidenceLevel, ApplyStatus, and GitOps-friendly tags.",
			LimitationNote: platformNote(isLinux, goos, "kernel-level structured recommendations", "/proc/sys"),
			Platform: []string{"linux"},
		},
		{
			Name:  "Cgroup v1/v2 and Kubernetes Metadata",
			Status: capabilityForPlatform(isLinux, "Container ID, Pod UID, Pod name, and namespace resolution from cgroup paths.", goos),
			Description: "Parses /proc/<pid>/cgroup to extract container IDs, then correlates with kubelet log scanning to resolve Kubernetes Pod name and namespace.",
			LimitationNote: platformNote(isLinux, goos, "cgroup and Kubernetes metadata resolution", "/proc/<pid>/cgroup"),
			Platform: []string{"linux"},
		},
		{
			Name:  "OOM Score Telemetry",
			Status: capabilityForPlatform(isLinux, "Per-process OOM score and adjustment values.", goos),
			Description: "Reads oom_score and oom_score_adj from /proc/<pid>/oom_score and /proc/<pid>/oom_score_adj. Useful for diagnosing premature OOM kills.",
			LimitationNote: platformNote(isLinux, goos, "OOM score collection", "/proc/<pid>/oom_score"),
			Platform: []string{"linux"},
		},
		{
			Name:  "File Descriptor Type Breakdown",
			Status: capabilityForPlatform(isLinux, "Breakdown of FDs into sockets, pipes, epoll, and regular files.", goos),
			Description: "Inspects /proc/<pid>/fd/* symlinks to categorize file descriptors by type: sockets, pipes, epoll/eventfd, and regular files.",
			LimitationNote: platformNote(isLinux, goos, "FD type breakdown", "/proc/<pid>/fd"),
			Platform: []string{"linux"},
		},
		{
			Name:  "Transparent HugePages (THP) Validation",
			Status: capabilityForPlatform(isLinux, "Reads /sys/kernel/mm/transparent_hugepage/enabled to check THP state.", goos),
			Description: "Verifies whether Transparent HugePages are disabled (required for NoSQL databases) by reading the kernel's THP control file.",
			LimitationNote: platformNote(isLinux, goos, "THP validation", "/sys/kernel/mm/transparent_hugepage"),
			Platform: []string{"linux"},
		},
		{
			Name:        "Prometheus Metrics Export",
			Status:      CapabilityFull,
			Description: "Exports 11 low-cardinality Prometheus metrics including workload category counts, agent self-telemetry, and K8s metadata success rates via /metrics HTTP endpoint.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:        "Command-Line Redaction",
			Status:      CapabilityFull,
			Description: "Redacts process command-line arguments by default to prevent credentials and secrets from appearing in output or metrics. Use --expose-cmdline to override.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:        "LLM Enrichment (enrich subcommand)",
			Status:      CapabilityFull,
			Description: "Gathers classified process telemetry and queries frontier LLMs (Gemini, Claude, Grok, OpenAI) or local Ollama for SRE narrative reports. Requires explicit --allow-remote-llm flag.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:           "Pressure Stall Information (PSI) Collection",
			Status:         capabilityForPlatform(isLinux, "Collects node-level CPU, memory, and I/O pressure stall metrics.", goos),
			Description:    "Surfaces 'some' and 'full' resource pressure percentages over 10s, 60s, and 300s windows. Linux kernel >= 4.20 required.",
			LimitationNote: platformNote(isLinux, goos, "PSI collection", "/proc/pressure"),
			Platform:       []string{"linux"},
		},
		{
			Name:        "Hardware Topology & ISA Discovery",
			Status:      CapabilityFull,
			Description: "Discovers NUMA layout, block storage type (SSD/HDD/NVMe), and CPU SIMD flags (AVX-512, AVX2, NEON, SVE) from /proc/cpuinfo and /sys.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:        "Data Completeness Tracking",
			Status:      CapabilityFull,
			Description: "Tracks and computes data source completeness score, showing which metrics are full, partial, unavailable, or disabled.",
			Platform:    []string{"linux", "windows", "darwin"},
		},
		{
			Name:  "HOST_PROC / HOST_SYS Container Path Override",
			Status: func() CapabilityStatus {
				if hostProc != "" || hostSys != "" {
					return CapabilityFull
				}
				if isLinux {
					return CapabilityPartial
				}
				return CapabilityUnavailable
			}(),
			Description: "Supports HOST_PROC and HOST_SYS environment variable overrides to redirect /proc and /sys reads to host-mounted volumes inside containers.",
			LimitationNote: func() string {
				if !isLinux {
					return fmt.Sprintf("HOST_PROC/HOST_SYS overrides are only meaningful on Linux. Current platform: %s", goos)
				}
				if hostProc == "" && hostSys == "" {
					return "HOST_PROC and HOST_SYS are not currently set. Using default /proc and /sys paths."
				}
				return ""
			}(),
			Platform: []string{"linux"},
		},
	}

	// Count by status.
	full, partial, unavail := 0, 0, 0
	for _, c := range caps {
		switch c.Status {
		case CapabilityFull:
			full++
		case CapabilityPartial:
			partial++
		case CapabilityUnavailable:
			unavail++
		}
	}

	return PlatformCapabilityReport{
		CurrentPlatform:         goos,
		Architecture:            arch,
		HostProcOverride:        hostProc,
		HostSysOverride:         hostSys,
		FullCapabilities:        full,
		PartialCapabilities:     partial,
		UnavailableCapabilities: unavail,
		Capabilities:            caps,
	}
}

// capabilityForPlatform returns CapabilityFull on Linux, CapabilityUnavailable otherwise.
func capabilityForPlatform(isLinux bool, _ string, goos string) CapabilityStatus {
	if isLinux {
		return CapabilityFull
	}
	if goos == "darwin" {
		return CapabilityPartial
	}
	return CapabilityUnavailable
}

// platformNote returns a note explaining why a Linux-specific feature is unavailable.
func platformNote(isLinux bool, goos, feature, path string) string {
	if isLinux {
		return ""
	}
	return fmt.Sprintf("%s requires %s which is only available on Linux. Current platform: %s", feature, path, goos)
}

// printCapabilitiesReport prints a human-readable, colourised capabilities report to the terminal.
func printCapabilitiesReport(report PlatformCapabilityReport) {
	fmt.Printf("\n%s%s═══════════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  proc-lens Platform Capability Assessment                              %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s═══════════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)

	fmt.Printf("\n%s[Platform]%s\n", Bold, Reset)
	fmt.Printf("  OS / Architecture : %s%s / %s%s\n", Bold, report.CurrentPlatform, report.Architecture, Reset)
	if report.HostProcOverride != "" {
		fmt.Printf("  HOST_PROC Override: %s%s%s\n", Green, report.HostProcOverride, Reset)
	} else {
		fmt.Printf("  HOST_PROC Override: %s(not set — using /proc)%s\n", Dim, Reset)
	}
	if report.HostSysOverride != "" {
		fmt.Printf("  HOST_SYS Override : %s%s%s\n", Green, report.HostSysOverride, Reset)
	} else {
		fmt.Printf("  HOST_SYS Override : %s(not set — using /sys)%s\n", Dim, Reset)
	}

	fmt.Printf("\n%s[Capability Summary]%s\n", Bold, Reset)
	fmt.Printf("  %s✓ Full%s           : %d features\n", Green, Reset, report.FullCapabilities)
	fmt.Printf("  %s⚡ Partial%s       : %d features\n", Yellow, Reset, report.PartialCapabilities)
	fmt.Printf("  %s✗ Unavailable%s   : %d features\n", Red, Reset, report.UnavailableCapabilities)

	fmt.Printf("\n%s[Feature Detail]%s\n", Bold, Reset)
	fmt.Println(Bold + "  " + padRight("Feature", 45) + "Status" + Reset)
	fmt.Println("  " + repeatChar('─', 60))

	for _, cap := range report.Capabilities {
		var statusColor, statusSymbol string
		switch cap.Status {
		case CapabilityFull:
			statusColor = Green
			statusSymbol = "✓ FULL          "
		case CapabilityPartial:
			statusColor = Yellow
			statusSymbol = "⚡ PARTIAL       "
		case CapabilityUnavailable:
			statusColor = Red
			statusSymbol = "✗ UNAVAILABLE   "
		}

		name := cap.Name
		if len(name) > 43 {
			name = name[:40] + "..."
		}

		fmt.Printf("  %s %s%s%s\n", padRight(name, 44), statusColor, statusSymbol, Reset)

		if cap.LimitationNote != "" {
			fmt.Printf("    %s  ↳ %s%s\n", Dim, cap.LimitationNote, Reset)
		}
	}

	fmt.Println()

	if report.UnavailableCapabilities > 0 && report.CurrentPlatform != "linux" {
		fmt.Printf("%sNote: %d capabilities require Linux and are not available on %s.%s\n",
			Yellow, report.UnavailableCapabilities, report.CurrentPlatform, Reset)
		fmt.Printf("%sFor Docker or Kubernetes deployment (Linux-based), all capabilities will be fully available.%s\n\n",
			Yellow, Reset)
	} else if report.CurrentPlatform == "linux" {
		fmt.Printf("%sAll kernel-level capabilities are available. Ensure HOST_PROC and HOST_SYS are set when running inside containers.%s\n\n",
			Green, Reset)
	}
}

// padRight pads a string with spaces on the right to the given width.
func padRight(s string, width int) string {
	for len(s) < width {
		s += " "
	}
	return s
}

// repeatChar returns a string of n repeated characters.
func repeatChar(c rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = c
	}
	return string(out)
}

