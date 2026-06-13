package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"
	"github.com/developer1622/proc-lens/pkg/optimizer"

	"github.com/spf13/cobra"
)

/*
 * Note: This file contains the analyze command which profiles a specific process by PID.
 *
 * Caveat 1: Profiling relies on delta calculations over a specified time window. If the window is too small,
 * rate calculations might lose precision. Configure --duration appropriately.
 *
 * Caveat 2: Querying processes owned by other users might return permission errors unless executed with
 * administrative/root privileges. Kindly execute the command with sudo/elevated access if needed.
 *
 * USP: The analyze command now also emits StructuredRecommendation objects (in JSON mode), providing
 * machine-readable, GitOps-friendly tuning advice with stable keys, risk levels, and confidence ratings.
 */

type AnalyzeOptions struct {
	Pid           int
	Duration      time.Duration
	OutputFormat  string
	ExposeCmdline bool
}

var analyzeOpts AnalyzeOptions

var analyzeCmd = &cobra.Command{
	Use:   "analyze",
	Short: "Profile and classify a specific running process PID",
	Long:  `Gathers resource usage telemetry for the given PID over a short window, predicts its system HLD box category, and lists optimizations.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := GetHostContext()
		opts := analyzeOpts
		opts.OutputFormat = GlobalOpts.OutputFormat
		opts.ExposeCmdline = GlobalOpts.ExposeCmdline
		return RunAnalyze(ctx, &opts)
	},
}

func RunAnalyze(ctx context.Context, opts *AnalyzeOptions) error {
	if opts.Pid <= 0 {
		err := fmt.Errorf("invalid PID: %d. Provide a valid PID using the --pid flag", opts.Pid)
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

	if opts.Duration <= 0 {
		err := fmt.Errorf("invalid duration: %v. Provide a positive profiling duration", opts.Duration)
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sError: %v%s\n", Red, err, Reset)
		}
		return err
	}

	if opts.OutputFormat != "json" {
		fmt.Printf("%sProfiling PID %d for %v...%s\n", Bold, opts.Pid, opts.Duration, Reset)
	}

	stats, err := collector.MonitorProcess(ctx, opts.Pid, opts.Duration)
	if err != nil {
		if opts.OutputFormat == "json" {
			PrintJSONError(err)
		} else {
			fmt.Printf("%sFailed to collect telemetry: %v. Check permissions/process status.%s\n", Red, err, Reset)
		}
		return err
	}

	pred := classifier.Predict(stats)
	pred.Cmdline = RedactCmdline(pred.Cmdline, opts.ExposeCmdline)
	pred.Telemetry.Cmdline = RedactCmdline(pred.Telemetry.Cmdline, opts.ExposeCmdline)
	optimizer.Optimize(ctx, &pred)

	// Compute structured recommendations for machine-readable JSON output.
	// In text mode, these are converted back to strings for display.
	structuredRecs := optimizer.OptimizeStructured(ctx, &pred)

	if opts.OutputFormat == "json" {
		envelope := analyzeOutputEnvelope{
			Prediction:                pred,
			StructuredRecommendations: structuredRecs,
		}
		bz, err := json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return err
		}
		fmt.Println(string(bz))
	} else {
		// In text mode, supplement the existing recommendations with structured data.
		// We merge both for a complete picture when either source has additional items.
		if len(structuredRecs) > 0 && len(pred.Recommendations) == 0 {
			pred.Recommendations = optimizer.StructuredRecommendationsToStrings(structuredRecs)
		}
		printPredictionReport(pred)
	}
	return nil
}

func init() {
	analyzeCmd.Flags().IntVarP(&analyzeOpts.Pid, "pid", "p", 0, "PID of the process to analyze (required)")
	analyzeCmd.Flags().DurationVarP(&analyzeOpts.Duration, "duration", "d", 1*time.Second, "Profiling window duration (e.g. 1s, 2s, 500ms)")
	
	RootCmd.AddCommand(analyzeCmd)
}

// analyzeOutputEnvelope wraps a Prediction with StructuredRecommendations for JSON output.
// This provides richer machine-readable output compared to the plain-text recommendations.
type analyzeOutputEnvelope struct {
	classifier.Prediction
	StructuredRecommendations []optimizer.StructuredRecommendation `json:"structured_recommendations,omitempty"`
}

func printPredictionReport(pred classifier.Prediction) {
	memMb := float64(pred.Telemetry.MemRss) / (1024.0 * 1024.0)
	virtMb := float64(pred.Telemetry.MemVirt) / (1024.0 * 1024.0)
	readKbSec := pred.Telemetry.IoReadSpeed / 1024.0
	writeKbSec := pred.Telemetry.IoWriteSpeed / 1024.0

	fmt.Println()
	fmt.Printf("%s%s================================================================================%s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s         PROCESS OBSERVABILITY & OPTIMIZATION REPORT                             %s\n", Bold, Cyan, Reset)
	fmt.Printf("%s%s================================================================================%s\n", Bold, Cyan, Reset)
	
	fmt.Printf("%s[Process Info]%s\n", Bold, Reset)
	fmt.Printf("  PID:      %s%d%s\n", Cyan, pred.PID, Reset)
	fmt.Printf("  Name:     %s%s%s\n", Bold, pred.Name, Reset)
	fmt.Printf("  Cmdline:  %s%s%s\n", Dim, pred.Cmdline, Reset)
	fmt.Println()

	fmt.Printf("%s[Resource Telemetry Profile]%s\n", Bold, Reset)
	fmt.Printf("  %-25s %s%-15.2f %-10s%s\n", "CPU Usage:", Cyan, pred.Telemetry.CpuUsage, "% (Cores saturated: "+fmt.Sprintf("%.2f", pred.Telemetry.CpuUsage/100.0)+")", Reset)
	fmt.Printf("  %-25s %s%-15.1f %-10s%s\n", "Resident Mem (RSS):", Cyan, memMb, "MB", Reset)
	fmt.Printf("  %-25s %s%-15.1f %-10s%s\n", "Virtual Memory (VM):", Cyan, virtMb, "MB", Reset)
	fmt.Printf("  %-25s %s%-15d %-10s%s\n", "OS Threads Count:", Cyan, pred.Telemetry.Threads, "threads", Reset)
	fmt.Printf("  %-25s %s%-15d %-10s%s\n", "Open File Descriptors:", Cyan, pred.Telemetry.FdCount, "FDs/Handles", Reset)
	fmt.Printf("  %-25s %s%-15d %-10s%s\n", "Network Sockets Count:", Cyan, pred.Telemetry.SocketCount, "sockets", Reset)
	fmt.Printf("  %-25s %s%-15.1f %-10s%s\n", "Disk Read Speed:", Cyan, readKbSec, "KB/sec", Reset)
	fmt.Printf("  %-25s %s%-15.1f %-10s%s\n", "Disk Write Speed:", Cyan, writeKbSec, "KB/sec", Reset)
	fmt.Printf("  %-25s %s%-15.1f %-10s%s\n", "Context Switches Rate:", Cyan, pred.Telemetry.CtxSwitchRate, "/sec", Reset)
	fmt.Println()

	fmt.Printf("%s[Workload Classification Predictor]%s\n", Bold, Reset)
	
	var catColor string
	switch pred.PrimaryCategory {
	case classifier.RelationalDB, classifier.NoSQLDB, classifier.ColumnarDB, classifier.VectorDB:
		catColor = Blue
	case classifier.CacheStore:
		catColor = Purple
	case classifier.WebServer, classifier.LoadBalancer:
		catColor = Green
	case classifier.AITraining, classifier.AIInference:
		catColor = Red
	case classifier.UtilityBatch:
		catColor = Yellow
	case classifier.InteractiveShell:
		catColor = Cyan
	default:
		catColor = White
	}

	fmt.Printf("  Primary Prediction: %s%s%s\n", Bold+catColor, pred.PrimaryCategory, Reset)
	fmt.Printf("  Prediction Confidence: %s%.1f%%%s\n", Bold, pred.Confidence*100.0, Reset)
	fmt.Println()
	fmt.Println("  Archetype Match Profile (Similarity Coefficients):")
	
	type scoreEntry struct {
		Cat   classifier.Category
		Score float64
	}
	var sortedScores []scoreEntry
	for k, v := range pred.Scores {
		sortedScores = append(sortedScores, scoreEntry{Cat: k, Score: v})
	}
	sort.Slice(sortedScores, func(i, j int) bool {
		return sortedScores[i].Score > sortedScores[j].Score
	})

	// Print top 8 categories to avoid spamming the terminal with all 16
	for idx, entry := range sortedScores {
		if idx >= 8 {
			break
		}
		barWidth := int(math.Round(entry.Score * 20))
		if barWidth < 0 {
			barWidth = 0
		}
		bar := strings.Repeat("█", barWidth) + strings.Repeat("░", 20-barWidth)
		fmt.Printf("    %-18s [%s] %5.1f%%\n", entry.Cat, bar, entry.Score*100.0)
	}
	fmt.Println()

	if len(pred.RulesTriggered) > 0 {
		fmt.Printf("%s[Heuristics & Fingerprints Triggered]%s\n", Bold, Reset)
		for _, rule := range pred.RulesTriggered {
			fmt.Printf("  • %s\n", rule)
		}
		fmt.Println()
	}

	fmt.Printf("%s%s[Autonomous Optimization Engine Recommendations]%s\n", Bold, Green, Reset)
	if len(pred.Recommendations) == 0 {
		fmt.Println("  • Workload resource consumption is stable. No tuning recommendations are required at this time.")
	} else {
		for _, rec := range pred.Recommendations {
			fmt.Printf("  • %s\n", rec)
		}
	}
	fmt.Println()
}

