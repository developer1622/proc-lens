package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/developer1622/proc-lens/pkg/collector"
	"github.com/developer1622/proc-lens/pkg/pressure"

	"github.com/spf13/cobra"
)

/*
 * Note: This file implements the `psi` subcommand which displays Linux PSI
 * (Pressure Stall Information) — a production-grade signal for resource contention
 * that goes far beyond raw CPU% or memory usage.
 *
 * Caveat: PSI requires Linux kernel 4.20+. On older kernels or non-Linux platforms,
 * this command will print a clear "no data" message and exit cleanly (exit 0).
 * This design ensures it can be safely called in scripts and CI pipelines without
 * breaking automation.
 *
 * In case of any modifications, please reach out to the development team to do the needful.
 */

type PsiOptions struct {
	OutputFormat string
}

var psiCmd = &cobra.Command{
	Use:   "psi",
	Short: "Display Linux Pressure Stall Information (PSI) for CPU, memory, and I/O",
	Long: `This subcommand queries the node's /proc/pressure/{cpu,memory,io} files
to fetch resource pressure averages (over 10s, 60s, and 300s) and total stall times.
Its degradation is graceful: on platforms where PSI is not supported, it prints 'no data' and exits cleanly.

Note the distinction between pressure types:
  • "some" pressure: at least one task is stalling (early warning signal).
  • "full" pressure: all runnable tasks are stalling (critical — resource starvation).

This is a far more sensitive signal than raw CPU% or memory usage, because it tells
you not just that a resource is busy, but that other processes are being made to wait.

Real-world example:
  A node running both AITraining and RelationalDB workloads may show:
    Memory full=18% (all processes stalling on memory reclaim)
  This explains a database performance degradation that raw CPU% would never show.

Requirements:
  • Linux kernel 4.20 or later.
  • 'psi=1' on the kernel command line (or cgroup v2 with psi controller enabled).
  • The host /proc mounted (standard in Docker and Kubernetes DaemonSet deployments).

Note: If PSI is not available, this command prints a clear explanation and exits cleanly.
All other ProcLens commands (scan, analyze, enrich) continue to work normally.`,
	Example: `  # Display PSI in human-readable format
  proc-lens psi

  # Display PSI as JSON (for pipelines and monitoring)
  proc-lens psi --format json

  # Use with HOST_PROC for containerized deployments
  HOST_PROC=/host/proc proc-lens psi`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := GetHostContext()
		opts := PsiOptions{
			OutputFormat: GlobalOpts.OutputFormat,
		}
		return RunPsi(ctx, &opts)
	},
}

func RunPsi(ctx context.Context, opts *PsiOptions) error {
	// Resolve the host /proc path from the environment (for containerized runs).
	hostProcPath := collector.GetHostProcPath(ctx)

	data := pressure.Collect(ctx, hostProcPath)

	if opts.OutputFormat == "json" {
		bz, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return fmt.Errorf("failed to marshal PSI data: %w", err)
		}
		fmt.Println(string(bz))
	} else {
		printPSIReport(data)
	}

	// Always exit 0 — this is an informational command.
	// A "no data" result is a valid, expected outcome and not a failure.
	return nil
}

func init() {
	RootCmd.AddCommand(psiCmd)
}

// printPSIReport renders PSI data as a formatted terminal report.
func printPSIReport(data pressure.PressureData) {
	fmt.Printf("\n%s%s═══════════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s  Node Pressure Stall Information (PSI)                                  %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s═══════════════════════════════════════════════════════════════════════%s\n", Bold, Cyan, Reset)

	switch data.Status {
	case pressure.StatusNoData:
		fmt.Printf("\n  %sPSI: no data%s\n", Yellow, Reset)
		fmt.Printf("\n  Reason: %s\n", data.Reason)
		fmt.Printf("\n  %sNote: All other proc-lens features (scan, analyze, enrich, etc.) continue to work normally.%s\n\n", Dim, Reset)
		return

	case pressure.StatusDisabled:
		fmt.Printf("\n  %sPSI: disabled%s\n", Yellow, Reset)
		fmt.Printf("\n  %s\n\n", data.Reason)
		return

	case pressure.StatusPartial:
		fmt.Printf("\n  %sPSI: partial data%s — %s\n", Yellow, Reset, data.Reason)

	default:
		fmt.Printf("\n  %sPSI: fully available%s\n", Green, Reset)
	}

	fmt.Println()
	fmt.Printf("  %s%-12s  %s%s\n", Bold, "Resource", "some (≥1 task stalling)                            full (all tasks stalling)", Reset)
	fmt.Printf("  %s\n", repeatChar('─', 75))

	printPSIResourceRow("CPU", data.CPU)
	printPSIResourceRow("Memory", data.Memory)
	printPSIResourceRow("I/O", data.IO)

	if data.HighPressureAlert != "" {
		fmt.Printf("\n  %s%s⚠  %s%s\n", Bold, Red, data.HighPressureAlert, Reset)
	}

	fmt.Printf("\n  %sDefinitions:%s\n", Bold, Reset)
	fmt.Printf("  • some: fraction of time at least one runnable task was delayed (early warning).\n")
	fmt.Printf("  • full: fraction of time ALL runnable tasks were delayed (critical — resource starvation).\n")
	fmt.Printf("  • avg10/avg60/avg300: exponential moving average over 10s / 60s / 300s windows.\n")
	fmt.Println()
}

// printPSIResourceRow prints a single row of the PSI table.
func printPSIResourceRow(label string, res pressure.PSIResource) {
	if !res.Available {
		fmt.Printf("  %-12s  %sno data%s\n", label, Yellow, Reset)
		return
	}

	someColor := Green
	if res.Some.Avg10 >= 5.0 {
		someColor = Yellow
	}
	if res.Some.Avg10 >= 20.0 {
		someColor = Red
	}

	fullColor := Green
	if res.Full.Avg10 >= 1.0 {
		fullColor = Yellow
	}
	if res.Full.Avg10 >= 10.0 {
		fullColor = Red
	}

	fmt.Printf("  %-12s  some: %savg10=%5.2f%% avg60=%5.2f%% avg300=%5.2f%%%s  |  full: %savg10=%5.2f%%%s\n",
		label,
		someColor, res.Some.Avg10, res.Some.Avg60, res.Some.Avg300, Reset,
		fullColor, res.Full.Avg10, Reset,
	)
}

