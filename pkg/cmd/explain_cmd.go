package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"
	"github.com/developer1622/proc-lens/pkg/explain"

	"github.com/spf13/cobra"
)

/*
 * Note: This file implements the `explain` subcommand which provides transparent,
 * educational explanations of why a process was classified into a specific HLD workload archetype.
 *
 * Caveat: The explain output is derived from already-collected Prediction data. It does not
 * perform any additional privileged system calls beyond what the standard analyze command uses.
 * If the process exits between collection and explanation, you will receive a graceful "no data" message.
 *
 * Design Principle: This subcommand NEVER breaks or affects `scan`, `analyze`, or any other command.
 * It is purely additive and exits cleanly (exit 0) even when data is unavailable.
 *
 * In case of any queries, please contact the maintainers for assistance.
 */

type ExplainOptions struct {
	Pid          int
	DurationSecs int
	OutputFormat string
	ExposeCmdline bool
}

var explainOpts ExplainOptions

var explainCmd = &cobra.Command{
	Use:   "explain",
	Short: "Explain why a process was classified into a specific workload archetype (Learning Mode)",
	Long: `The explain subcommand provides a transparent, educational breakdown of the classification decision.

This command helps you understand:
  • Which telemetry features (CPU, memory, sockets, I/O) contributed most to the classification.
  • Which heuristic rules were triggered and what thresholds were crossed.
  • How close the runner-up category was (the confidence gap).
  • What-if hints: what single change would likely shift the classification.
  • A learning note explaining the archetype's expected resource signature.

This is particularly useful for:
  • SREs learning the ProcLens classification model.
  • Debugging surprising classification results in production.
  • Building trust in the classifier for high-stakes workload placement decisions.

Note: All other ProcLens commands (scan, analyze, enrich) continue to work normally
regardless of whether this command is used. Data collection uses the same safe, read-only
mechanism as the analyze command.`,
	Example: `  # Explain the classification of a specific PID
  proc-lens explain --pid 1234

  # Explain with a longer profiling window for more accurate rates
  proc-lens explain --pid 5678 --duration 2

  # Output as JSON for pipeline use
  proc-lens explain --pid 1234 --format json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := GetHostContext()
		opts := explainOpts
		opts.OutputFormat = GlobalOpts.OutputFormat
		opts.ExposeCmdline = GlobalOpts.ExposeCmdline
		return RunExplain(ctx, &opts)
	},
}

func RunExplain(ctx context.Context, opts *ExplainOptions) error {
	if opts.Pid <= 0 {
		if opts.OutputFormat == "json" {
			PrintJSONError(fmt.Errorf("missing --pid flag: provide a valid process PID using --pid (e.g., --pid 1234)"))
		} else {
			fmt.Printf("%sError: Provide a valid PID using --pid (e.g., proc-lens explain --pid 1234)%s\n", Red, Reset)
			fmt.Printf("%sNote: All other ProcLens features continue to work normally.%s\n", Dim, Reset)
		}
		return fmt.Errorf("missing required --pid flag")
	}

	if opts.DurationSecs <= 0 {
		opts.DurationSecs = 1
	}
	duration := time.Duration(opts.DurationSecs) * time.Second

	if opts.OutputFormat != "json" {
		fmt.Printf("%sProfiling PID %d for %v to build explanation...%s\n",
			Dim, opts.Pid, duration, Reset)
	}

	stats, err := collector.MonitorProcess(ctx, opts.Pid, duration)
	if err != nil {
		if opts.OutputFormat == "json" {
			PrintJSONError(fmt.Errorf("could not profile PID %d: %v", opts.Pid, err))
		} else {
			fmt.Printf("\n%sExplain: no data for PID %d%s\n", Yellow, opts.Pid, Reset)
			fmt.Printf("Reason: %v\n", err)
			fmt.Printf("\n%sPossible causes:%s\n", Bold, Reset)
			fmt.Printf("  • The process has already exited.\n")
			fmt.Printf("  • You need elevated permissions (try with sudo).\n")
			fmt.Printf("  • PID %d does not exist on this host.\n\n", opts.Pid)
			fmt.Printf("%sNote: All other ProcLens commands (scan, analyze, enrich) continue to work normally.%s\n\n", Dim, Reset)
		}
		// Exit 0 — "no data" is a valid outcome, not an agent failure.
		return nil
	}

	pred := classifier.Predict(stats)
	pred.Cmdline = RedactCmdline(pred.Cmdline, opts.ExposeCmdline)
	pred.Telemetry.Cmdline = RedactCmdline(pred.Telemetry.Cmdline, opts.ExposeCmdline)

	exp := explain.Explain(pred)

	if opts.OutputFormat == "json" {
		type ExplainOutput struct {
			EventType   string               `json:"event_type"`
			classifier.Prediction
			Explanation explain.Explanation  `json:"explanation"`
		}
		out := ExplainOutput{
			EventType:   "explain",
			Prediction:  pred,
			Explanation: exp,
		}
		bz, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return nil
		}
		fmt.Println(string(bz))
	} else {
		formatted := explain.FormatTextExplanation(exp)
		fmt.Print(formatted)
		fmt.Print(Reset)
	}

	return nil
}

func init() {
	explainCmd.Flags().IntVarP(&explainOpts.Pid, "pid", "p", 0, "PID of the process to explain (required)")
	explainCmd.Flags().IntVarP(&explainOpts.DurationSecs, "duration", "d", 1, "Profiling window duration in seconds (e.g., 1, 2, 5)")
	RootCmd.AddCommand(explainCmd)
}

// explainStringsContains is a helper used by explain_cmd tests.
func explainStringsContains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// strconvAtoiSafe wraps strconv.Atoi with a default value for safety.
func strconvAtoiSafe(s string, defaultVal int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n
}

