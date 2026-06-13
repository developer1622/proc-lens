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
	fmt.Printf("%s%s╔══════════════════════════════════════════════════════════════════════════════╗%s\n", Bold, GoogleBlue, Reset)
	fmt.Printf("%s%s║       PROCLENS — PROCESS OBSERVABILITY & OPTIMIZATION REPORT                ║%s\n", Bold, GoogleBlue, Reset)
	fmt.Printf("%s%s╚══════════════════════════════════════════════════════════════════════════════╝%s\n", Bold, GoogleBlue, Reset)

	fmt.Println()
	fmt.Printf("%s%s● Process Identity%s\n", Bold, BrightCyan, Reset)
	fmt.Printf("  %sPID%s       %s%d%s\n", Dim, Reset, BrightWhite+Bold, pred.PID, Reset)
	fmt.Printf("  %sName%s      %s%s%s\n", Dim, Reset, Bold, pred.Name, Reset)
	fmt.Printf("  %sCmdline%s   %s%s%s\n", Dim, Reset, GoogleGray, pred.Cmdline, Reset)
	fmt.Println()

	fmt.Printf("%s%s● Resource Telemetry Snapshot%s\n", Bold, BrightCyan, Reset)
	cpuColor := pickRangeColor(pred.Telemetry.CpuUsage, 5, 50, 150)
	memColor := pickRangeColor(memMb, 100, 2048, 8192)
	fmt.Printf("  %-26s %s%-12.2f%s  %s%% (≈%.2f cores)%s\n",
		"CPU Usage:", cpuColor+Bold, pred.Telemetry.CpuUsage, Reset, Dim, pred.Telemetry.CpuUsage/100.0, Reset)
	fmt.Printf("  %-26s %s%-12.1f%s  %sMB%s\n",
		"Resident Mem (RSS):", memColor+Bold, memMb, Reset, Dim, Reset)
	fmt.Printf("  %-26s %s%-12.1f%s  %sMB%s\n",
		"Virtual Memory (VM):", GoogleGray, virtMb, Reset, Dim, Reset)
	fmt.Printf("  %-26s %s%-12d%s  %sthreads%s\n",
		"OS Threads:", BrightWhite, pred.Telemetry.Threads, Reset, Dim, Reset)
	fmt.Printf("  %-26s %s%-12d%s  %sFDs%s\n",
		"Open File Descriptors:", BrightWhite, pred.Telemetry.FdCount, Reset, Dim, Reset)
	sockColor := pickRangeColor(float64(pred.Telemetry.SocketCount), 50, 300, 700)
	fmt.Printf("  %-26s %s%-12d%s  %ssockets%s\n",
		"Network Sockets:", sockColor+Bold, pred.Telemetry.SocketCount, Reset, Dim, Reset)
	fmt.Printf("  %-26s %s%-12.1f%s  %sKB/s%s\n",
		"Disk Read:", BrightGreen, readKbSec, Reset, Dim, Reset)
	fmt.Printf("  %-26s %s%-12.1f%s  %sKB/s%s\n",
		"Disk Write:", GoogleOrange, writeKbSec, Reset, Dim, Reset)
	fmt.Printf("  %-26s %s%-12.1f%s  %s/sec%s\n",
		"Context Switch Rate:", GoogleTeal, pred.Telemetry.CtxSwitchRate, Reset, Dim, Reset)
	fmt.Println()

	// ── Primary prediction badge ─────────────────────────────────────────────
	catColor := categoryColor(pred.PrimaryCategory)
	badge := fmt.Sprintf("  %s%s%s %-22s %s", Bold+catColor, "\033[7m", " ", pred.PrimaryCategory+" ", "\033[27m"+Reset)
	confBar := buildConfBar(pred.Confidence)
	fmt.Printf("%s%s● Workload Classification%s\n", Bold, BrightCyan, Reset)
	fmt.Printf("%s  Confidence  %s%s%.1f%%%s\n", badge, Reset+Bold+catColor, confBar+" ", pred.Confidence*100.0, Reset)
	fmt.Println()

	// ── Score bars ────────────────────────────────────────────────────────────
	fmt.Printf("%s%s● Archetype Match Profile%s  %s(cosine similarity — top 8 of %d)%s\n",
		Bold, BrightCyan, Reset, Dim, len(pred.Scores), Reset)
	fmt.Println()

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

	const barWidth = 30
	for idx, entry := range sortedScores {
		if idx >= 8 {
			break
		}
		filled := int(math.Round(entry.Score * float64(barWidth)))
		if filled < 0 {
			filled = 0
		}
		if filled > barWidth {
			filled = barWidth
		}
		empty := barWidth - filled

		var fillColor string
		switch {
		case entry.Score >= 0.70:
			fillColor = BarFillHigh
		case entry.Score >= 0.40:
			fillColor = BarFillMid
		case entry.Score >= 0.20:
			fillColor = BarFillLow
		default:
			fillColor = BarFillNone
		}

		rowColor := categoryColor(entry.Cat)
		marker := "   "
		if idx == 0 {
			marker = " ▶ "
		}

		filledStr := fillColor + strings.Repeat("█", filled) + Reset
		emptyStr := BarEmpty + strings.Repeat("░", empty) + Reset

		pct := entry.Score * 100.0
		var pctColor string
		switch {
		case pct >= 70:
			pctColor = BarFillHigh + Bold
		case pct >= 40:
			pctColor = BarFillMid + Bold
		case pct >= 20:
			pctColor = BarFillLow
		default:
			pctColor = GoogleGray
		}

		fmt.Printf("%s%s%s%-24s%s [%s%s] %s%5.1f%%%s\n",
			marker,
			rowColor+Bold, "", entry.Cat, Reset,
			filledStr, emptyStr,
			pctColor, pct, Reset,
		)
	}
	fmt.Println()

	// ── Heuristics ────────────────────────────────────────────────────────────
	if len(pred.RulesTriggered) > 0 {
		fmt.Printf("%s%s● Heuristics & Fingerprints Triggered%s\n", Bold, GoogleAmber, Reset)
		for _, rule := range pred.RulesTriggered {
			fmt.Printf("  %s⚡%s %s\n", GoogleAmber, Reset, rule)
		}
		fmt.Println()
	}

	// ── Recommendations ───────────────────────────────────────────────────────
	fmt.Printf("%s%s● Autonomous Optimization Engine%s\n", Bold, GoogleGreen, Reset)
	if len(pred.Recommendations) == 0 {
		fmt.Printf("  %s✓%s Workload resource consumption is stable. No tuning recommendations required.\n", GoogleGreen, Reset)
	} else {
		for _, rec := range pred.Recommendations {
			fmt.Printf("  %s→%s %s\n", BrightGreen, Reset, rec)
		}
	}
	fmt.Println()
}

// buildConfBar renders a compact 10-cell confidence bar coloured by level.
func buildConfBar(conf float64) string {
	const w = 10
	filled := int(math.Round(conf * float64(w)))
	if filled < 0 {
		filled = 0
	}
	if filled > w {
		filled = w
	}
	var fillColor string
	switch {
	case conf >= 0.70:
		fillColor = BarFillHigh
	case conf >= 0.40:
		fillColor = BarFillMid
	default:
		fillColor = BarFillLow
	}
	return fillColor + strings.Repeat("█", filled) + Reset +
		BarEmpty + strings.Repeat("░", w-filled) + Reset
}

// pickRangeColor returns a colour code scaled across low/mid/high thresholds.
func pickRangeColor(val, low, mid, high float64) string {
	switch {
	case val >= high:
		return GoogleRed
	case val >= mid:
		return GoogleYellow
	case val >= low:
		return BrightGreen
	default:
		return GoogleGray
	}
}

// categoryColor returns the Google-palette 256-colour code for a given archetype tier.
func categoryColor(cat classifier.Category) string {
	switch cat {
	// Network / Ingress — Google Blue
	case classifier.LoadBalancer, classifier.APIGateway, classifier.ServiceMesh, classifier.CDNEdgeNode:
		return GoogleBlue
	// Application — Bright Cyan
	case classifier.WebServer, classifier.Microservice, classifier.ServerlessWorker,
		classifier.JobWorker, classifier.SchedulerDaemon:
		return BrightCyan
	// Data stores — Google Green
	case classifier.RelationalDB, classifier.NoSQLDB, classifier.ColumnarDB,
		classifier.VectorDB, classifier.TimeSeriesDB, classifier.GraphDB, classifier.ObjectStore:
		return GoogleGreen
	// Cache — Bright Purple
	case classifier.CacheStore:
		return BrightPurple
	// Messaging / Streaming — Google Orange
	case classifier.MessageBroker, classifier.EventStreaming, classifier.StreamProcessor:
		return GoogleOrange
	// Search / Analytics — Google Yellow
	case classifier.SearchEngine, classifier.OLAPEngine:
		return GoogleYellow
	// AI Training — Google Red
	case classifier.AITraining:
		return GoogleRed
	// AI Inference / ML — Google Pink
	case classifier.AIInference, classifier.MLPipeline, classifier.FeatureStore:
		return GooglePink
	// Infrastructure — Google Indigo
	case classifier.OrchestratorAgent, classifier.OrchestratorPod,
		classifier.ServiceDiscovery, classifier.ConfigManager:
		return GoogleIndigo
	// Observability — Google Amber
	case classifier.MonitoringAgent, classifier.LogAggregator, classifier.TracingAgent:
		return GoogleAmber
	// Shell / Utility / Legacy — Grey
	case classifier.InteractiveShell, classifier.UtilityBatch,
		classifier.LegacySOAService, classifier.CIRunner:
		return GoogleGray
	default:
		return White
	}
}
