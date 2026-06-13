package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/developer1622/proc-lens/pkg/collector"
	"github.com/developer1622/proc-lens/pkg/hardware"

	"github.com/spf13/cobra"
)

/*
 * Note: This file implements the `hardware` subcommand which displays hardware topology
 * information including CPU capabilities (SIMD extensions), NUMA layout, GPU presence, and
 * storage device characteristics.
 *
 * Caveat: Hardware profile collection is primarily Linux-based (reads /proc/cpuinfo, /sys/block,
 * /sys/devices/system/node). On Windows and macOS, this command will report "no data" for most
 * fields while still providing CPU architecture and logical core count from the Go runtime.
 *
 * Design Principle: This subcommand is purely additive. It has zero effect on scan, analyze,
 * enrich, or any other proc-lens command. It exits cleanly (exit 0) even on minimal containers
 * without full /sys access.
 *
 * In case of any queries, please contact the maintainers for assistance.
 */

type HardwareOptions struct {
	OutputFormat string
}

var hardwareCmd = &cobra.Command{
	Use:   "hardware",
	Short: "Display hardware topology: CPU capabilities, NUMA layout, GPU presence, and storage",
	Long: `The hardware subcommand profiles the host node's physical characteristics to provide context for ProcLens's workload optimization recommendations.

Hardware topology significantly affects which optimizations are most valuable:
  • A 4-NUMA-node server benefits from process pinning (numactl) for database and AI workloads.
  • A node with NVMe storage should use the 'none' I/O scheduler for maximum throughput.
  • A AI workload node without AVX-512 may be missing 2-4x SIMD performance.
  • GPU presence changes which optimization paths are relevant for AI training/inference.

Data sources (read-only, no extra privileges required):
  • /proc/cpuinfo — CPU model, SIMD flags (AVX-512, AVX2, NEON, SVE)
  • /sys/devices/system/node — NUMA topology
  • /sys/block — Storage devices, rotational flag, I/O scheduler
  • /sys/bus/pci/devices — GPU vendor detection

Requirements:
  • Linux with host /proc and /sys mounted (standard in Docker/K8s DaemonSet deployments).
  • No additional capabilities beyond the standard ProcLens security model.

Note: On non-Linux platforms or minimal containers, some data will be unavailable.
A clear "no data" message with the specific reason will be shown for each missing source.
All other ProcLens commands (scan, analyze, enrich) continue to work normally.`,
	Example: `  # Display hardware profile in human-readable format
  proc-lens hardware

  # Display hardware profile as JSON (for pipeline use)
  proc-lens hardware --format json

  # Use with mounted host paths (for containerized deployments)
  HOST_PROC=/host/proc HOST_SYS=/host/sys proc-lens hardware`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := GetHostContext()
		opts := HardwareOptions{
			OutputFormat: GlobalOpts.OutputFormat,
		}
		return RunHardware(ctx, &opts)
	},
}

func RunHardware(ctx context.Context, opts *HardwareOptions) error {
	hostProcPath := collector.GetHostProcPath(ctx)
	hostSysPath := collector.GetHostSysPath(ctx)

	profile := hardware.Collect(hostProcPath, hostSysPath)

	if opts.OutputFormat == "json" {
		bz, err := json.MarshalIndent(profile, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return fmt.Errorf("failed to marshal hardware profile: %w", err)
		}
		fmt.Println(string(bz))
	} else {
		printHardwareReport(profile)
	}

	// Always exit 0 — "no data" is a valid, expected outcome, not a failure.
	return nil
}

func init() {
	RootCmd.AddCommand(hardwareCmd)
}

// printHardwareReport renders a HardwareProfile as a formatted terminal report.
func printHardwareReport(profile hardware.HardwareProfile) {
	fmt.Printf("\n%s%s═══════════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Hardware Topology Profile                                              %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s═══════════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)

	if profile.Status == hardware.StatusNoData {
		fmt.Printf("\n  %sHardware profile: no data%s\n", Yellow, Reset)
		fmt.Printf("  %s\n", profile.Reason)
		fmt.Printf("\n  %sNote: All other proc-lens features continue to work normally.%s\n\n", Dim, Reset)
		return
	}

	// --- CPU Section ---
	fmt.Printf("\n%s[CPU Capabilities]%s\n", Bold, Reset)
	cpu := profile.CPU
	fmt.Printf("  Architecture   : %s%s%s\n", Cyan, cpu.Architecture, Reset)
	if cpu.ModelName != "" {
		fmt.Printf("  Model          : %s\n", cpu.ModelName)
	}
	fmt.Printf("  Logical Cores  : %d\n", cpu.LogicalCores)
	if cpu.PhysicalCores > 0 && cpu.PhysicalCores != cpu.LogicalCores {
		fmt.Printf("  Physical Cores : %d (logical / physical = %.1f HT ratio)\n",
			cpu.PhysicalCores, float64(cpu.LogicalCores)/float64(cpu.PhysicalCores))
	}

	fmt.Printf("  %sSIMD Extensions:%s\n", Bold, Reset)
	simdItems := []struct {
		label     string
		supported bool
		note      string
	}{
		{"AVX-512", cpu.HasAVX512, "Best for AI/ML on Intel/AMD. 2-4x over AVX2 on wide vector workloads."},
		{"AVX2", cpu.HasAVX2, "Good SIMD for AI/ML on most modern x86 CPUs."},
		{"SSE4", cpu.HasSSE4, "Basic SIMD, available on most x86 CPUs from 2008+."},
		{"ARM NEON", cpu.HasNEON, "Standard ARM SIMD. Available on all aarch64 CPUs."},
		{"ARM SVE/SVE2", cpu.HasSVE, "Scalable Vector Extension — high-performance ARM SIMD for HPC/AI."},
	}
	for _, s := range simdItems {
		icon := fmt.Sprintf("%s✗%s", Red, Reset)
		if s.supported {
			icon = fmt.Sprintf("%s✓%s", Green, Reset)
		}
		fmt.Printf("    %s %-14s %s%s%s\n", icon, s.label, Dim, s.note, Reset)
	}

	// GPU
	if cpu.HasNVIDIAGPU || cpu.HasAMDGPU {
		fmt.Printf("  %sGPU Detected:%s\n", Bold, Reset)
		if cpu.HasNVIDIAGPU {
			fmt.Printf("    %s✓%s NVIDIA GPU — verify CUDA drivers with 'nvidia-smi'\n", Green, Reset)
		}
		if cpu.HasAMDGPU {
			fmt.Printf("    %s✓%s AMD GPU — verify ROCm platform with 'rocm-smi'\n", Green, Reset)
		}
	} else {
		fmt.Printf("  GPU            : %sNo discrete GPU detected via PCI%s\n", Dim, Reset)
	}

	if cpu.Status == hardware.StatusNoData || cpu.Status == hardware.StatusPartial {
		fmt.Printf("  %sNote: %s%s\n", Yellow, cpu.Reason, Reset)
	}

	// --- NUMA Section ---
	fmt.Printf("\n%s[NUMA Topology]%s\n", Bold, Reset)
	numa := profile.NUMA
	switch numa.Status {
	case hardware.StatusNoData:
		fmt.Printf("  NUMA: %sno data%s — %s\n", Yellow, Reset, numa.Reason)
	default:
		if numa.IsUMA {
			fmt.Printf("  NUMA nodes: 1 %s(UMA — Uniform Memory Access)%s\n", Dim, Reset)
		} else {
			fmt.Printf("  NUMA nodes: %s%d%s — %sThis node is NUMA. Kindly bind latency-sensitive workloads with numactl.%s\n",
				Cyan+Bold, numa.NodeCount, Reset, Yellow, Reset)
		}
	}

	// --- Storage Section ---
	fmt.Printf("\n%s[Storage Devices]%s\n", Bold, Reset)
	stor := profile.Storage
	switch stor.Status {
	case hardware.StatusNoData:
		fmt.Printf("  Storage: %sno data%s — %s\n", Yellow, Reset, stor.Reason)
	default:
		fmt.Printf("  Primary storage type: %s%s%s\n", Cyan, stor.PrimaryType, Reset)
		fmt.Printf("  %-15s %-12s %-10s %-14s %s\n", "Device", "Type", "Rotational", "I/O Scheduler", "Queue Depth")
		fmt.Println("  " + strings.Repeat("─", 60))
		for _, dev := range stor.Devices {
			rotStr := "No (SSD/NVMe)"
			if dev.Rotational {
				rotStr = "Yes (HDD)"
			}
			fmt.Printf("  %-15s %-12s %-10s %-14s %d\n",
				dev.Name, dev.Type, rotStr, dev.IOScheduler, dev.QueueDepth)
		}
	}

	// --- Recommendation Hints ---
	if len(profile.RecommendationHints) > 0 {
		fmt.Printf("\n%s[Hardware-Aware Optimisation Hints]%s\n", Bold, Reset)
		for _, hint := range profile.RecommendationHints {
			lines := wordWrapStr(hint, 70)
			for i, line := range lines {
				if i == 0 {
					fmt.Printf("  • %s\n", line)
				} else {
					fmt.Printf("    %s\n", line)
				}
			}
		}
	}

	fmt.Printf("\n%sNote: Hardware profile data is read-only and requires no additional privileges.%s\n\n", Dim, Reset)
}




// wordWrapStr wraps a string to the given width, returning a slice of lines.
func wordWrapStr(s string, width int) []string {
	words := strings.Fields(s)
	var lines []string
	var current strings.Builder
	for _, w := range words {
		if current.Len()+len(w)+1 > width && current.Len() > 0 {
			lines = append(lines, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
		}
		current.WriteString(w)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

