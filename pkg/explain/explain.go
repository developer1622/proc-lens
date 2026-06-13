// Package explain provides the Explainable Classification feature for proc-lens.
//
// # Overview
//
// When an operator or developer asks "why did proc-lens call this process a VectorDB?",
// this package answers with a human-readable and machine-readable breakdown:
//   - Which telemetry features contributed most to the winning cosine score.
//   - Which heuristic rules were triggered and exactly what threshold was crossed.
//   - How far the process is from the top archetype vs the runner-up.
//   - Simple what-if hints: "if socket count dropped below 300, this would become a WebServer."
//
// # Design Principles
//
// Purely additive: zero changes to the classifier, optimizer, or collector.
// Derives everything from the already-populated Prediction struct.
// On any missing or unavailable field: the relevant section says "N/A on this platform"
// rather than panicking or omitting the whole explanation.
//
// Note: This package is intended for learning and trust-building, not for
// production alerting. The explanation is best-effort and reflects the model state
// at collection time. Re-running explain on the same PID at a different time may
// yield a different result as the process's resource profile changes.
//
// Caveat: The "what-if" hints are heuristic approximations based on the centroid
// definitions. They indicate the direction of change needed, not an exact threshold.
// Treat them as directional guidance rather than precise engineering targets.
package explain

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/developer1622/proc-lens/pkg/classifier"
)

// FeatureContribution describes how a single telemetry dimension contributed
// to the cosine similarity score for the predicted archetype.
type FeatureContribution struct {
	// FeatureName is the human-readable name of the telemetry dimension.
	FeatureName string `json:"feature_name"`

	// RawValue is the original (non-log-scaled) value of the telemetry dimension.
	RawValue float64 `json:"raw_value"`

	// LogScaledValue is the log1p-scaled value actually used in the cosine model.
	LogScaledValue float64 `json:"log_scaled_value"`

	// CentroidValue is the archetype centroid's value for this dimension.
	CentroidValue float64 `json:"centroid_value"`

	// ContributionPct is the approximate percentage of the dot-product contributed
	// by this dimension. Higher means this dimension "drove" the classification more.
	ContributionPct float64 `json:"contribution_pct"`

	// Status is "full", "zero" (the process had no activity on this dimension),
	// or "no data (platform limitation)".
	Status string `json:"status"`
}

// WhatIfHint is a directional suggestion about what would need to change
// for the process to be classified differently.
type WhatIfHint struct {
	// Dimension is the telemetry field this hint refers to.
	Dimension string `json:"dimension"`

	// CurrentValue describes the current value.
	CurrentValue string `json:"current_value"`

	// SuggestionText is a human-readable "what if" sentence.
	SuggestionText string `json:"suggestion_text"`

	// AlternativeCategory is the category this change would likely push toward.
	AlternativeCategory classifier.Category `json:"alternative_category"`
}

// Explanation is the complete, machine-readable explanation of a classification decision.
// Note that all fields are populated from the already-available Prediction data —
// no additional system calls or privileges are required.
type Explanation struct {
	// PID is the process identifier being explained.
	PID int `json:"pid"`

	// ProcessName is the name of the process.
	ProcessName string `json:"process_name"`

	// PrimaryCategory is the winning archetype label.
	PrimaryCategory classifier.Category `json:"primary_category"`

	// ConfidencePct is the model's confidence as a percentage [0, 100].
	ConfidencePct float64 `json:"confidence_pct"`

	// RunnerUpCategory is the second-highest scoring category (if any).
	RunnerUpCategory classifier.Category `json:"runner_up_category,omitempty"`

	// RunnerUpConfidencePct is the runner-up's confidence percentage.
	RunnerUpConfidencePct float64 `json:"runner_up_confidence_pct,omitempty"`

	// DistanceToRunnerUp is how much higher the primary category scored vs
	// the runner-up (in confidence percentage points). Larger = more decisive.
	DistanceToRunnerUp float64 `json:"distance_to_runner_up_pct"`

	// TopFeatures lists the top 3 telemetry dimensions that contributed most
	// to the winning cosine score, sorted by contribution descending.
	TopFeatures []FeatureContribution `json:"top_features"`

	// AllFeatures lists all 8 telemetry dimensions with their contribution data.
	AllFeatures []FeatureContribution `json:"all_features"`

	// TriggeredRules lists the heuristic rules that boosted or suppressed scores.
	// These come directly from pred.RulesTriggered, which are already human-readable.
	TriggeredRules []string `json:"triggered_rules"`

	// WhatIfHints provides directional suggestions about what would need to change
	// for the process to be classified differently.
	WhatIfHints []WhatIfHint `json:"what_if_hints"`

	// ModelSummary is a single human-readable paragraph describing the decision.
	ModelSummary string `json:"model_summary"`

	// LearningNote provides context for developers and SREs new to the model.
	LearningNote string `json:"learning_note"`
}

// centroidData mirrors the centroid definitions from pkg/classifier/centroids.go.
// Note: These are duplicated here to avoid a tight coupling between the
// explain package and classifier internals. If centroids are updated in the
// classifier, these should be updated accordingly.
// Caveat: These values are approximations for explanation purposes only.
// The actual classification uses the exact centroid values from classifier.Centroids.
type featureVector struct {
	CPU, Memory, Threads, Sockets, FDs, IORead, IOWrite, CtxSwitches float64
}

// featureNames enumerates all telemetry dimensions in order.
var featureNames = []string{
	"CPU Usage",
	"Memory (RSS)",
	"Thread Count",
	"Socket Count",
	"File Descriptors",
	"Disk Read Speed",
	"Disk Write Speed",
	"Context Switch Rate",
}

// Explain derives a complete Explanation from a populated classifier.Prediction.
// It is safe to call on any Prediction, including those collected on Windows or macOS
// where some fields may be zero (they are treated as "zero activity" rather than errors).
//
// Note: This function performs no system calls and requires no extra privileges.
// All data is derived from the already-populated Prediction struct.
func Explain(pred classifier.Prediction) Explanation {
	exp := Explanation{
		PID:            pred.PID,
		ProcessName:    pred.Name,
		PrimaryCategory: pred.PrimaryCategory,
		ConfidencePct:  pred.Confidence * 100.0,
		TriggeredRules: pred.RulesTriggered,
	}

	if len(exp.TriggeredRules) == 0 {
		exp.TriggeredRules = []string{"No additional heuristic rules were triggered for this process."}
	}

	// --- Find runner-up category ---
	type scorePair struct {
		Cat   classifier.Category
		Score float64
	}
	var scored []scorePair
	for cat, sc := range pred.Scores {
		scored = append(scored, scorePair{Cat: cat, Score: sc})
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})
	if len(scored) >= 2 {
		exp.RunnerUpCategory = scored[1].Cat
		exp.RunnerUpConfidencePct = scored[1].Score * 100.0
		exp.DistanceToRunnerUp = exp.ConfidencePct - exp.RunnerUpConfidencePct
	}

	// --- Compute feature contributions ---
	stats := pred.Telemetry
	memMb := float64(stats.MemRss) / (1024.0 * 1024.0)
	ioReadKb := stats.IoReadSpeed / 1024.0
	ioWriteKb := stats.IoWriteSpeed / 1024.0

	rawValues := []float64{
		stats.CpuUsage,
		memMb,
		float64(stats.Threads),
		float64(stats.SocketCount),
		float64(stats.FdCount),
		ioReadKb,
		ioWriteKb,
		stats.CtxSwitchRate,
	}

	logValues := make([]float64, len(rawValues))
	for i, v := range rawValues {
		logValues[i] = math.Log1p(v)
	}

	// Get the centroid for the primary category from the classifier's centroid map.
	// Note: We access classifier.Centroids directly. This is safe as the
	// centroids map is a package-level constant-like structure in the classifier.
	centroid, centroidFound := classifier.Centroids[pred.PrimaryCategory]
	centroidVals := []float64{0, 0, 0, 0, 0, 0, 0, 0}
	if centroidFound {
		centroidVals = []float64{
			centroid.CPU,
			centroid.Memory,
			centroid.Threads,
			centroid.Sockets,
			centroid.FDs,
			centroid.IORead,
			centroid.IOWrite,
			centroid.CtxSwitches,
		}
	}

	// Compute dot-product contributions.
	dotProducts := make([]float64, len(logValues))
	totalDot := 0.0
	for i := range logValues {
		dotProducts[i] = logValues[i] * centroidVals[i]
		totalDot += dotProducts[i]
	}

	allFeatures := make([]FeatureContribution, len(featureNames))
	for i, name := range featureNames {
		pct := 0.0
		if totalDot > 0 {
			pct = (dotProducts[i] / totalDot) * 100.0
		}
		status := "full"
		if rawValues[i] == 0 {
			status = "zero (no activity on this dimension)"
		}
		allFeatures[i] = FeatureContribution{
			FeatureName:     name,
			RawValue:        rawValues[i],
			LogScaledValue:  logValues[i],
			CentroidValue:   centroidVals[i],
			ContributionPct: math.Max(0, pct),
			Status:          status,
		}
	}
	exp.AllFeatures = allFeatures

	// Top 3 by contribution.
	sorted := make([]FeatureContribution, len(allFeatures))
	copy(sorted, allFeatures)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ContributionPct > sorted[j].ContributionPct
	})
	top := 3
	if len(sorted) < top {
		top = len(sorted)
	}
	exp.TopFeatures = sorted[:top]

	// --- What-if hints ---
	exp.WhatIfHints = buildWhatIfHints(pred)

	// --- Model Summary ---
	exp.ModelSummary = buildModelSummary(exp, pred, centroidFound)

	// --- Learning Note ---
	exp.LearningNote = buildLearningNote(pred.PrimaryCategory)

	return exp
}

// buildWhatIfHints generates directional hints about what changes would shift
// the classification toward an alternative archetype.
func buildWhatIfHints(pred classifier.Prediction) []WhatIfHint {
	var hints []WhatIfHint
	stats := pred.Telemetry

	// Socket-based hints
	if stats.SocketCount > 500 && pred.PrimaryCategory != classifier.LoadBalancer {
		hints = append(hints, WhatIfHint{
			Dimension:           "Socket Count",
			CurrentValue:        fmt.Sprintf("%d", stats.SocketCount),
			SuggestionText:      fmt.Sprintf("Socket count is very high (%d). If this drops below 200 and CPU drops too, the process would likely shift toward WebServer or UtilityBatch.", stats.SocketCount),
			AlternativeCategory: classifier.WebServer,
		})
	}
	if stats.SocketCount < 10 && pred.PrimaryCategory != classifier.UtilityBatch {
		hints = append(hints, WhatIfHint{
			Dimension:           "Socket Count",
			CurrentValue:        fmt.Sprintf("%d", stats.SocketCount),
			SuggestionText:      "Very low socket count. If CPU and I/O increase significantly, this might shift toward UtilityBatch or AITraining.",
			AlternativeCategory: classifier.AITraining,
		})
	}

	// CPU-based hints
	if stats.CpuUsage > 150 && pred.PrimaryCategory != classifier.AITraining {
		hints = append(hints, WhatIfHint{
			Dimension:           "CPU Usage",
			CurrentValue:        fmt.Sprintf("%.1f%%", stats.CpuUsage),
			SuggestionText:      fmt.Sprintf("CPU usage is very high (%.1f%%). If this process also shows torch/cuda in its cmdline, it would be classified as AITraining.", stats.CpuUsage),
			AlternativeCategory: classifier.AITraining,
		})
	}

	// Memory-based hints
	memMb := float64(stats.MemRss) / (1024 * 1024)
	if memMb > 4096 && pred.PrimaryCategory == classifier.Unknown {
		hints = append(hints, WhatIfHint{
			Dimension:           "Memory (RSS)",
			CurrentValue:        fmt.Sprintf("%.0f MB", memMb),
			SuggestionText:      fmt.Sprintf("High RSS (%.0f MB) but no matching name/cmdline signatures. If the process name matched 'postgres' or 'mysql', it would be classified as RelationalDB.", memMb),
			AlternativeCategory: classifier.RelationalDB,
		})
	}

	// Runner-up hint
	var runnerUp classifier.Category
	var runnerUpScore float64
	for cat, sc := range pred.Scores {
		if cat != pred.PrimaryCategory && sc > runnerUpScore {
			runnerUpScore = sc
			runnerUp = cat
		}
	}
	gap := (pred.Confidence - runnerUpScore) * 100.0
	if runnerUp != "" && gap < 15.0 {
		hints = append(hints, WhatIfHint{
			Dimension:           "Overall Score Gap",
			CurrentValue:        fmt.Sprintf("%.1f pp advantage over runner-up (%s)", gap, runnerUp),
			SuggestionText:      fmt.Sprintf("The margin over %s is only %.1f percentage points — this is a closely contested classification. Small changes in resource profile could flip the result.", runnerUp, gap),
			AlternativeCategory: runnerUp,
		})
	}

	if len(hints) == 0 {
		hints = append(hints, WhatIfHint{
			Dimension:      "Overall",
			CurrentValue:   "N/A",
			SuggestionText: "The classification is confident. No single-dimension change would easily shift the result to a different archetype.",
		})
	}

	return hints
}

// buildModelSummary creates a human-readable narrative of the classification decision.
func buildModelSummary(exp Explanation, pred classifier.Prediction, centroidFound bool) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Process '%s' (PID %d) was classified as %s with %.1f%% confidence. ",
		pred.Name, pred.PID, pred.PrimaryCategory, exp.ConfidencePct)

	if len(exp.TopFeatures) > 0 {
		top := exp.TopFeatures[0]
		fmt.Fprintf(&sb, "The dominant contributing feature was '%s' (%.1f%% of the cosine dot-product), ",
			top.FeatureName, top.ContributionPct)
	}

	if exp.RunnerUpCategory != "" {
		fmt.Fprintf(&sb, "with '%s' as the runner-up at %.1f%% confidence (%.1f pp gap). ",
			exp.RunnerUpCategory, exp.RunnerUpConfidencePct, exp.DistanceToRunnerUp)
	}

	if len(pred.RulesTriggered) > 0 {
		fmt.Fprintf(&sb, "%d heuristic rule(s) additionally boosted this classification: %s.",
			len(pred.RulesTriggered), pred.RulesTriggered[0])
	} else {
		fmt.Fprintf(&sb, "No additional heuristic rules were triggered; the decision is based purely on cosine similarity against the archetype centroid.")
	}

	if !centroidFound {
		fmt.Fprintf(&sb, " Note: The centroid for this category was not found in the current classifier — feature contribution percentages may not be accurate.")
	}

	return sb.String()
}

// buildLearningNote generates a category-specific educational note for developers
// and SREs who are learning how the classifier works.
func buildLearningNote(cat classifier.Category) string {
	notes := map[classifier.Category]string{
		classifier.RelationalDB:      "RelationalDB workloads typically have moderate-to-high memory (buffer pools), moderate threads, low socket count, and high dirty-page write patterns. If you see a process classified here that you believe is not a database, check if its memory + I/O profile accidentally matches the centroid.",
		classifier.CacheStore:        "CacheStore workloads are characterised by very high socket counts (many client connections), high memory for data structures, and very low disk I/O. Redis and Memcached are the canonical examples.",
		classifier.LoadBalancer:      "LoadBalancer archetypes have extremely high socket counts (accepting + proxying many connections), low CPU per connection, and minimal disk I/O. They often appear in front of WebServer or CacheStore workloads.",
		classifier.WebServer:         "WebServer workloads have moderate socket counts, variable CPU (request handling), and minimal persistent disk I/O. They differ from LoadBalancers by having more CPU per socket and less extreme connection counts.",
		classifier.AITraining:        "AITraining workloads are CPU-dominant (often > 200% on multi-core machines), have very high memory (model weights), and minimal network activity. Python processes with torch/cuda keywords are the primary triggers.",
		classifier.AIInference:       "AIInference workloads are similar to training but often have higher socket counts (serving API requests) and lower sustained CPU (inference is typically shorter-lived per request).",
		classifier.NoSQLDB:           "NoSQLDB workloads (MongoDB, Cassandra) have moderate memory, variable sockets, and often high write I/O for journaling. They are distinguished from RelationalDB by lower dirty-page ratios and higher map counts.",
		classifier.ColumnarDB:        "ColumnarDB workloads (ClickHouse, DuckDB) have very high read I/O (large sequential table scans), moderate-to-high memory, and typically lower socket counts than OLTP databases.",
		classifier.VectorDB:          "VectorDB workloads are memory-bandwidth intensive (large embedding vectors), often SIMD-dependent, and have variable socket counts depending on whether they serve an HTTP/gRPC API.",
		classifier.SearchEngine:      "SearchEngine workloads (Elasticsearch, OpenSearch) have very high memory map counts, high heap memory (JVM), and significant I/O for index reads. They are often mis-classified as NoSQLDB on early data points.",
		classifier.MessageBroker:     "MessageBroker workloads (RabbitMQ, NATS) have moderate sockets and I/O. JVM-based brokers (ActiveMQ) add high heap memory and GC pause context switches.",
		classifier.EventStreaming:     "EventStreaming workloads (Kafka, Pulsar) have very high write I/O (WAL), high socket counts, and significant memory. They are characterised by sequential append patterns to disk.",
		classifier.OrchestratorAgent: "OrchestratorAgent workloads (kubelet, containerd) have moderate CPU, variable memory (proportional to container count), and many short-lived child processes. They are recognised primarily by name matching.",
		classifier.MonitoringAgent:   "MonitoringAgent workloads (Prometheus, Datadog Agent) have moderate CPU (periodic scraping), significant memory (TSDB), and moderate socket counts. Their I/O is characterised by periodic batch writes.",
		classifier.InteractiveShell:  "InteractiveShell workloads (bash, sshd) have very low resource usage and are recognised primarily by name. If a shell process has high resource usage, it is likely a shell wrapper around a heavier process.",
		classifier.UtilityBatch:      "UtilityBatch workloads (compilers, gzip, tar) have burst CPU usage, low sustained memory, and minimal sockets. They often have a short lifespan and may not appear in repeated scans.",
		classifier.Unknown:           "The process did not match any archetype centroid with sufficient confidence and no name/cmdline signatures matched. This is common for custom application binaries, stub processes, or processes with very minimal resource usage.",
	}

	if note, ok := notes[cat]; ok {
		return note
	}
	return "No specific learning note is available for this category. Refer to the classifier documentation for centroid definitions."
}

// FormatTextExplanation renders an Explanation as a human-readable terminal report.
// This is called by the explain subcommand and by scan/analyze when --explain is active.
func FormatTextExplanation(exp Explanation) string {
	var sb strings.Builder

	sb.WriteString("\n")
	sb.WriteString("╔══════════════════════════════════════════════════════════════════════╗\n")
	sb.WriteString("║           CLASSIFICATION EXPLANATION (Learning Mode)                  ║\n")
	sb.WriteString("╚══════════════════════════════════════════════════════════════════════╝\n\n")

	fmt.Fprintf(&sb, "  Process : %s (PID %d)\n", exp.ProcessName, exp.PID)
	fmt.Fprintf(&sb, "  Decision: %s (%.1f%% confidence)\n", exp.PrimaryCategory, exp.ConfidencePct)
	if exp.RunnerUpCategory != "" {
		fmt.Fprintf(&sb, "  Runner-up: %s (%.1f%%) — %.1f pp gap\n",
			exp.RunnerUpCategory, exp.RunnerUpConfidencePct, exp.DistanceToRunnerUp)
	}

	sb.WriteString("\n  [Top Contributing Telemetry Dimensions]\n")
	for i, f := range exp.TopFeatures {
		bar := buildBar(f.ContributionPct, 30)
		fmt.Fprintf(&sb, "  %d. %-22s [%s] %5.1f%%  (raw=%.2f, log1p=%.3f, centroid=%.3f)\n",
			i+1, f.FeatureName, bar, f.ContributionPct, f.RawValue, f.LogScaledValue, f.CentroidValue)
	}

	if len(exp.TriggeredRules) > 0 && exp.TriggeredRules[0] != "No additional heuristic rules were triggered for this process." {
		sb.WriteString("\n  [Heuristic Rules Triggered]\n")
		for _, r := range exp.TriggeredRules {
			fmt.Fprintf(&sb, "  • %s\n", r)
		}
	}

	sb.WriteString("\n  [What-If Hints]\n")
	for _, h := range exp.WhatIfHints {
		fmt.Fprintf(&sb, "  → %s\n", h.SuggestionText)
	}

	sb.WriteString("\n  [Model Summary]\n")
	wrapped := wordWrap(exp.ModelSummary, 68)
	for _, line := range wrapped {
		fmt.Fprintf(&sb, "  %s\n", line)
	}

	sb.WriteString("\n  [Learning Note]\n")
	wrapped = wordWrap(exp.LearningNote, 68)
	for _, line := range wrapped {
		fmt.Fprintf(&sb, "  %s\n", line)
	}

	sb.WriteString("\n")
	return sb.String()
}

// buildBar creates a simple ASCII progress bar for a percentage [0, 100].
func buildBar(pct float64, width int) string {
	filled := int(pct / 100.0 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// wordWrap wraps a long string at word boundaries to the given column width.
func wordWrap(s string, width int) []string {
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

