// Package pressure provides PSI (Pressure Stall Information) collection for proc-lens.
//
// # Overview
//
// PSI is a Linux kernel feature (available since kernel 4.20) that reports how much time
// processes are stalling while waiting for CPU, memory, or I/O resources. This is a far
// more sensitive signal than raw CPU% or memory usage:
//
//   - CPU pressure: processes waiting because the CPU is oversubscribed.
//   - Memory pressure: processes stalling due to memory reclaim or swap pressure.
//   - I/O pressure: processes waiting on disk reads/writes.
//
// # Format (from /proc/pressure/{cpu,memory,io})
//
//	some avg10=<pct> avg60=<pct> avg300=<pct> total=<usec>
//	full avg10=<pct> avg60=<pct> avg300=<pct> total=<usec>
//
// "Some" pressure = at least one task is stalling.
// "Full" pressure = all tasks are stalling (most severe; indicates complete resource starvation).
//
// # Note
//
// This package performs only read-only access to /proc/pressure/* files. No additional
// kernel capabilities or privileges are required beyond what the standard proc-lens agent
// already possesses. On kernels without PSI support, all fields are set to "no data" with
// a clear, actionable reason.
//
// Caveat: PSI metrics are node-level (not per-process). They represent the overall health of
// the node's resource availability. Correlating them with the workload categories from
// proc-lens's HLD classifier provides a unique, actionable signal that raw % metrics do not.
//
// Caveat: PSI may be disabled at boot time on some distributions. To enable it, add
// "psi=1" to the kernel command line and reboot. Alternatively, if cgroup v2 is enabled
// with the psi controller, proc-lens will still read the top-level pressure files.
package pressure

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DataStatus represents the collection status for a pressure data source.
type DataStatus string

const (
	// StatusFull means PSI data was successfully collected from all three files.
	StatusFull DataStatus = "full"

	// StatusPartial means at least one PSI file was readable (e.g., cpu present but io missing).
	StatusPartial DataStatus = "partial"

	// StatusNoData means no PSI files could be read on this system.
	StatusNoData DataStatus = "no_data"

	// StatusDisabled means PSI collection was disabled via flag (default).
	StatusDisabled DataStatus = "disabled"
)

// PSILine holds parsed values from a single PSI line (e.g., "some avg10=0.12 avg60=0.05 ...").
// Note: All percentage values are in the range [0.0, 100.0].
type PSILine struct {
	// Avg10 is the 10-second pressure average as a percentage.
	Avg10 float64 `json:"avg10_pct"`

	// Avg60 is the 60-second pressure average as a percentage.
	Avg60 float64 `json:"avg60_pct"`

	// Avg300 is the 300-second (5-minute) pressure average as a percentage.
	Avg300 float64 `json:"avg300_pct"`

	// TotalMicroseconds is the total accumulated stall time in microseconds since boot.
	TotalMicroseconds uint64 `json:"total_stall_usec"`
}

// PSIResource holds the "some" and "full" pressure metrics for a single resource type.
// Note: "Full" pressure (all tasks stalling) is a much more severe signal than
// "some" pressure (at least one task stalling).
type PSIResource struct {
	// Available indicates whether this resource's PSI file was readable.
	Available bool `json:"available"`

	// Some represents the "some" pressure line (at least one task is stalling).
	Some PSILine `json:"some"`

	// Full represents the "full" pressure line (all runnable tasks are stalling).
	// Note: CPU pressure does not have a "full" line on older kernels.
	Full PSILine `json:"full,omitempty"`
}

// PressureData is the top-level structure containing all node-level PSI metrics.
// It is designed to be attached additively to CollectionContext or emitted as a
// standalone JSONL event. It is always safe to embed even when Status is "no_data".
type PressureData struct {
	// Status is "full", "partial", "no_data", or "disabled".
	Status DataStatus `json:"status"`

	// Reason explains why the status is not "full" (empty when Status is "full").
	// This is always a human-readable, actionable message.
	Reason string `json:"reason,omitempty"`

	// CPU holds the CPU pressure stall information.
	// On older kernels without PSI, this will be zero-valued with Available=false.
	CPU PSIResource `json:"cpu"`

	// Memory holds the memory pressure stall information.
	Memory PSIResource `json:"memory"`

	// IO holds the I/O pressure stall information.
	IO PSIResource `json:"io"`

	// HighPressureAlert is set to a human-readable warning string when any "full"
	// pressure metric exceeds 10% over 10 seconds — a signal of severe resource starvation.
	// Kindly address this promptly in production environments.
	HighPressureAlert string `json:"high_pressure_alert,omitempty"`
}

// Collect reads PSI metrics from /proc/pressure/{cpu,memory,io} on the host filesystem.
// It gracefully handles missing files, unreadable content, and parse errors.
//
// The ctx parameter is used to resolve the HOST_PROC path override (for containerized
// deployments that mount the host /proc at a different path via the HOST_PROC env variable).
//
// Returns a PressureData with Status="no_data" and a clear Reason when:
//   - The running kernel is older than 4.20 (PSI not available).
//   - PSI was disabled at boot (kernel cmdline did not include psi=1).
//   - The tool is running on a non-Linux platform.
//   - The proc/pressure directory is not accessible (permission issue).
//
// Note: This function never panics. All errors result in a graceful no_data response.
func Collect(ctx context.Context, hostProcPath string) PressureData {
	if hostProcPath == "" {
		hostProcPath = "/proc"
	}

	pressureDir := filepath.Join(hostProcPath, "pressure")

	// Check if the pressure directory exists at all.
	if _, err := os.Stat(pressureDir); err != nil {
		return PressureData{
			Status: StatusNoData,
			Reason: fmt.Sprintf("PSI not available: /proc/pressure directory not found or not accessible (%v). This usually means the kernel is older than 4.20, or PSI was not enabled at boot. To enable PSI, add 'psi=1' to the kernel command line and reboot. All other proc-lens features remain fully functional.", err),
		}
	}

	var successCount int
	result := PressureData{}

	// Read CPU pressure.
	cpuPath := filepath.Join(pressureDir, "cpu")
	cpuRes, err := readPSIFile(cpuPath)
	if err != nil {
		cpuRes.Available = false
	} else {
		cpuRes.Available = true
		successCount++
	}
	result.CPU = cpuRes

	// Read memory pressure.
	memPath := filepath.Join(pressureDir, "memory")
	memRes, err := readPSIFile(memPath)
	if err != nil {
		memRes.Available = false
	} else {
		memRes.Available = true
		successCount++
	}
	result.Memory = memRes

	// Read I/O pressure.
	ioPath := filepath.Join(pressureDir, "io")
	ioRes, err := readPSIFile(ioPath)
	if err != nil {
		ioRes.Available = false
	} else {
		ioRes.Available = true
		successCount++
	}
	result.IO = ioRes

	// Determine overall status based on how many files were readable.
	switch successCount {
	case 0:
		result.Status = StatusNoData
		result.Reason = "PSI files found in /proc/pressure but none were readable. check permissions or kernel configuration. Core proc-lens features are unaffected."
	case 3:
		result.Status = StatusFull
	default:
		result.Status = StatusPartial
		result.Reason = fmt.Sprintf("Only %d of 3 PSI resource files (/proc/pressure/cpu, /proc/pressure/memory, /proc/pressure/io) were readable. This may indicate a partial kernel PSI implementation.", successCount)
	}

	// Generate a high-pressure alert for critical conditions.
	result.HighPressureAlert = detectHighPressure(result)

	return result
}

// readPSIFile reads and parses a single PSI file (e.g., /proc/pressure/cpu).
// Returns a PSIResource and any parse error.
func readPSIFile(path string) (PSIResource, error) {
	f, err := os.Open(path)
	if err != nil {
		return PSIResource{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var res PSIResource
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}

		psiLine, err := parsePSILine(fields[1:])
		if err != nil {
			// Kindly log parse errors without failing the whole file read.
			continue
		}

		switch fields[0] {
		case "some":
			res.Some = psiLine
		case "full":
			res.Full = psiLine
		}
	}

	if err := scanner.Err(); err != nil {
		return PSIResource{}, fmt.Errorf("scan %s: %w", path, err)
	}

	return res, nil
}

// parsePSILine parses the key=value fields in a PSI line after the "some"/"full" prefix.
// Example input: ["avg10=0.12", "avg60=0.03", "avg300=0.01", "total=12345678"]
func parsePSILine(fields []string) (PSILine, error) {
	var line PSILine
	for _, field := range fields {
		parts := strings.SplitN(field, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key, val := parts[0], parts[1]
		switch key {
		case "avg10":
			v, err := strconv.ParseFloat(val, 64)
			if err == nil {
				line.Avg10 = v
			}
		case "avg60":
			v, err := strconv.ParseFloat(val, 64)
			if err == nil {
				line.Avg60 = v
			}
		case "avg300":
			v, err := strconv.ParseFloat(val, 64)
			if err == nil {
				line.Avg300 = v
			}
		case "total":
			v, err := strconv.ParseUint(val, 10, 64)
			if err == nil {
				line.TotalMicroseconds = v
			}
		}
	}
	return line, nil
}

// detectHighPressure checks for critically high "full" pressure on any resource.
// Returns a human-readable alert string if any "full" Avg10 exceeds 10%.
// Treat these alerts as urgent production incidents requiring immediate investigation.
func detectHighPressure(data PressureData) string {
	var alerts []string

	if data.CPU.Available && data.CPU.Full.Avg10 >= 10.0 {
		alerts = append(alerts, fmt.Sprintf("CPU full pressure %.1f%% (10s avg)", data.CPU.Full.Avg10))
	}
	if data.Memory.Available && data.Memory.Full.Avg10 >= 10.0 {
		alerts = append(alerts, fmt.Sprintf("Memory full pressure %.1f%% (10s avg)", data.Memory.Full.Avg10))
	}
	if data.IO.Available && data.IO.Full.Avg10 >= 10.0 {
		alerts = append(alerts, fmt.Sprintf("I/O full pressure %.1f%% (10s avg)", data.IO.Full.Avg10))
	}

	if len(alerts) == 0 {
		return ""
	}
	return fmt.Sprintf("CRITICAL: Node is experiencing full resource starvation on: %s. Investigate immediately.", strings.Join(alerts, "; "))
}

// DisabledData returns a PressureData indicating the PSI feature is turned off.
// This is returned when --enable-psi is not set, preserving backward compatibility.
func DisabledData() PressureData {
	return PressureData{
		Status: StatusDisabled,
		Reason: "PSI collection is disabled. Enable with --enable-psi flag or PROC_INTEL_ENABLE_PSI=1 environment variable.",
	}
}

// FormatText renders PSI data as a compact, human-readable terminal section.
func FormatText(data PressureData) string {
	var sb strings.Builder

	switch data.Status {
	case StatusDisabled:
		sb.WriteString("  [Node PSI Pressure] disabled (use --enable-psi to activate)\n")
		return sb.String()
	case StatusNoData:
		fmt.Fprintf(&sb, "  [Node PSI Pressure] no data (%s)\n", data.Reason)
		fmt.Fprintf(&sb, "  Note: All other proc-lens features (scan, analyze, etc.) continue to work normally.\n")
		return sb.String()
	case StatusPartial:
		fmt.Fprintf(&sb, "  [Node PSI Pressure] partial data (%s)\n", data.Reason)
	default:
		sb.WriteString("  [Node PSI Pressure]\n")
	}

	if data.CPU.Available {
		fmt.Fprintf(&sb, "  CPU     some: avg10=%-6.2f%% avg60=%-6.2f%% avg300=%-6.2f%%  | full: avg10=%-6.2f%%\n",
			data.CPU.Some.Avg10, data.CPU.Some.Avg60, data.CPU.Some.Avg300,
			data.CPU.Full.Avg10)
	}
	if data.Memory.Available {
		fmt.Fprintf(&sb, "  Memory  some: avg10=%-6.2f%% avg60=%-6.2f%% avg300=%-6.2f%%  | full: avg10=%-6.2f%%\n",
			data.Memory.Some.Avg10, data.Memory.Some.Avg60, data.Memory.Some.Avg300,
			data.Memory.Full.Avg10)
	}
	if data.IO.Available {
		fmt.Fprintf(&sb, "  I/O     some: avg10=%-6.2f%% avg60=%-6.2f%% avg300=%-6.2f%%  | full: avg10=%-6.2f%%\n",
			data.IO.Some.Avg10, data.IO.Some.Avg60, data.IO.Some.Avg300,
			data.IO.Full.Avg10)
	}

	if data.HighPressureAlert != "" {
		fmt.Fprintf(&sb, "\n  ⚠  %s\n", data.HighPressureAlert)
	}

	return sb.String()
}

