package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"

	"github.com/spf13/cobra"
)

/*
 * Note: This file implements the `drift` subcommand, which analyzes historical workload
 * fingerprint logs (JSONL format) or state files to detect architectural and operational shifts on a node.
 *
 * Why this is a unique selling point (USP):
 *   - No other lightweight agent (e.g. node_exporter, osquery) tracks semantic workload shifts over time.
 *   - Most tools alert on CPU spikes; the drift engine explains which semantic category caused the spike.
 *
 * Caveat: Drift analysis requires at least two distinct scan cycles to compute delta changes.
 * If insufficient data is found, the command will exit cleanly (status 0) with a helpful "no data" message.
 */

type DriftOptions struct {
	FilePath     string
	StatePath    string
	OutputFormat string
}

var driftOpts DriftOptions

var driftCmd = &cobra.Command{
	Use:   "drift",
	Short: "Detect semantic workload composition shifts over time",
	Long: `The drift subcommand analyzes scan logs (in JSONL format) or persisted state files
to calculate shifts in the node's workload profile across consecutive scan cycles.
It provides a detailed breakdown of which application archetypes appeared, vanished, or changed.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := GetHostContext()
		opts := driftOpts
		opts.OutputFormat = GlobalOpts.OutputFormat
		return RunDrift(ctx, &opts)
	},
}

func RunDrift(ctx context.Context, opts *DriftOptions) error {
	// Assess target data source
	if opts.FilePath == "" && opts.StatePath == "" {
		opts.StatePath = "stability.json" // default local state file
	}

	var fingerprints []fingerprintWithTime
	var err error

	if opts.FilePath != "" {
		fingerprints, err = parseFingerprintsFromJSONL(opts.FilePath)
	} else {
		fingerprints, err = parseFingerprintsFromState(opts.StatePath)
	}

	if err != nil || len(fingerprints) < 2 {
		reason := "need at least two collection cycles to compute drift"
		if err != nil {
			reason = fmt.Sprintf("could not read source: %v", err)
		} else if len(fingerprints) == 1 {
			reason = "only one collection cycle found in history; need at least two to compute drift"
		}

		if opts.OutputFormat == "json" {
			fmt.Printf(`{"status": "no_data", "reason": "%s"}`+"\n", reason)
		} else {
			fmt.Printf("\n%sDrift analysis: no data found (%s).%s\n", Yellow, reason, Reset)
			fmt.Printf("Ensure that scan logs have been recorded via 'proc-lens scan --loop --format json > scan.jsonl'\n")
			fmt.Printf("or pass a valid file using '--file' parameter.\n\n")
			fmt.Printf("Note: Core ProcLens features (scan, analyze, capabilities) remain fully functional and unaffected.\n\n")
		}
		return nil
	}

	// Sort fingerprints chronologically
	sort.Slice(fingerprints, func(i, j int) bool {
		return fingerprints[i].Timestamp.Before(fingerprints[j].Timestamp)
	})

	// Compute consecutive drifts
	var reports []classifier.DriftReport
	for i := 0; i < len(fingerprints)-1; i++ {
		prev := fingerprints[i].Fingerprint
		curr := fingerprints[i+1].Fingerprint
		report := classifier.DetectDrift(prev, curr)
		if report != nil {
			// DetectDrift sets DetectedAt to current time, let's adjust it to the cycle timestamp
			report.DetectedAt = fingerprints[i+1].Timestamp
			reports = append(reports, *report)
		}
	}

	if opts.OutputFormat == "json" {
		bz, err := json.MarshalIndent(reports, "", "  ")
		if err != nil {
			PrintJSONError(err)
			return nil
		}
		fmt.Println(string(bz))
		return nil
	}

	// Text/Human output format
	fmt.Printf("\n")
	fmt.Printf("╔══════════════════════════════════════════════════════════════════════╗\n")
	fmt.Printf("║                     WORKLOAD DRIFT HISTORY REPORT                    ║\n")
	fmt.Printf("╚══════════════════════════════════════════════════════════════════════╝\n\n")

	fmt.Printf("  Analyzed %d scan cycles spanning from %s to %s.\n\n",
			len(fingerprints),
			fingerprints[0].Timestamp.Format(time.RFC3339),
			fingerprints[len(fingerprints)-1].Timestamp.Format(time.RFC3339),
		)

		if len(reports) == 0 {
			fmt.Printf("  %s✓ No significant workload drift was detected across the cycles.%s\n", Green, Reset)
			fmt.Printf("  The node's workload profile remained stable.\n\n")
			return nil
		}

		fmt.Printf("  Detected %d significant workload drift event(s):\n\n", len(reports))
		for idx, rep := range reports {
			var sevColor string
			switch rep.Severity {
			case classifier.DriftSeverityCritical:
				sevColor = Red
			case classifier.DriftSeverityWarn:
				sevColor = Yellow
			default:
				sevColor = Cyan
			}

			fmt.Printf("  %d. [%s] %s%s%s (Max delta: %.1f pp)\n",
				idx+1,
				rep.DetectedAt.Format("2006-01-02 15:04:05"),
				sevColor,
				rep.Summary,
				Reset,
				rep.MaxDeltaPct,
			)
			fmt.Printf("     Previous Fingerprint: %s\n", rep.PreviousFingerprint[:16]+"...")
			fmt.Printf("     Current Fingerprint : %s\n", rep.CurrentFingerprint[:16]+"...")

			fmt.Printf("     Category Changes:\n")
			for _, ch := range rep.Changes {
				var dirSymbol string
				if ch.DeltaPct > 0 {
					dirSymbol = "↑"
				} else {
					dirSymbol = "↓"
				}
				fmt.Printf("       • %-18s: %6.1f%% -> %6.1f%% (%s%s %.1f pp)%s\n",
					ch.Category,
					ch.PreviousPct,
					ch.CurrentPct,
					sevColor,
					dirSymbol,
					ch.DeltaPct,
					Reset,
				)
			}
			fmt.Println()
		}

		return nil
}

type fingerprintWithTime struct {
	Fingerprint classifier.NodeFingerprint
	Timestamp   time.Time
}

// parseFingerprintsFromJSONL scans a file and extracts fingerprints or predictions
func parseFingerprintsFromJSONL(path string) ([]fingerprintWithTime, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Temporary structure to parse fingerprint envelopes
	type fingerprintEnvelope struct {
		EventType   string                      `json:"event_type"`
		Fingerprint classifier.NodeFingerprint `json:"fingerprint"`
		NodeContext *collector.CollectionContext `json:"node_context"`
	}

	var results []fingerprintWithTime
	predictionsByTime := make(map[string][]classifier.Prediction)
	timestamps := make(map[string]time.Time)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// First, try to parse as a fingerprint envelope
		var env fingerprintEnvelope
		if err := json.Unmarshal(line, &env); err == nil && env.EventType == "node_fingerprint" {
			ts := time.Now()
			if env.NodeContext != nil {
				ts = env.NodeContext.Timestamp
			}
			results = append(results, fingerprintWithTime{
				Fingerprint: env.Fingerprint,
				Timestamp:   ts,
			})
			continue
		}

		// Second, try to parse as a Prediction object
		var pred classifier.Prediction
		if err := json.Unmarshal(line, &pred); err == nil && pred.PID > 0 {
			tsStr := time.Now().Format(time.RFC3339)
			ts := time.Now()
			if pred.NodeContext != nil {
				tsStr = pred.NodeContext.Timestamp.Format(time.RFC3339)
				ts = pred.NodeContext.Timestamp
			}
			predictionsByTime[tsStr] = append(predictionsByTime[tsStr], pred)
			timestamps[tsStr] = ts
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// If we successfully parsed raw prediction lines, group them and compute fingerprints
	if len(predictionsByTime) > 0 {
		for tsStr, preds := range predictionsByTime {
			fp := classifier.ComputeNodeFingerprint(preds)
			results = append(results, fingerprintWithTime{
				Fingerprint: fp,
				Timestamp:   timestamps[tsStr],
			})
		}
	}

	return results, nil
}

// parseFingerprintsFromState reads fingerprints from a stability state file
func parseFingerprintsFromState(path string) ([]fingerprintWithTime, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// State file contains a JSON list of past fingerprints
	type stateEntry struct {
		Fingerprint classifier.NodeFingerprint `json:"fingerprint"`
		Timestamp   time.Time                  `json:"timestamp"`
	}

	var entries []stateEntry
	if err := json.NewDecoder(file).Decode(&entries); err != nil {
		return nil, err
	}

	results := make([]fingerprintWithTime, len(entries))
	for i, e := range entries {
		results[i] = fingerprintWithTime{
			Fingerprint: e.Fingerprint,
			Timestamp:   e.Timestamp,
		}
	}
	return results, nil
}

func init() {
	driftCmd.Flags().StringVar(&driftOpts.FilePath, "file", "", "Path to the scan JSONL log file containing fingerprints or predictions")
	driftCmd.Flags().StringVar(&driftOpts.StatePath, "state-file", "", "Path to the stability state JSON file")

	RootCmd.AddCommand(driftCmd)
}

