package classifier

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

/*
 * Note: This file implements the Workload Drift Detection engine — another genuine unique selling
 * point of proc-lens. No other lightweight node agent tracks semantic workload composition changes over
 * time and surfaces actionable drift alerts.
 *
 * The drift engine compares the current scan's NodeFingerprint against the previous one and emits
 * structured DriftReport objects when significant architectural changes are detected on the node.
 *
 * Why this matters:
 *   - A node_exporter alert tells you "CPU is at 90%". The drift engine tells you "the proportion of
 *     AITraining workloads on this node grew by 40 percentage points in the last 30 seconds — this
 *     is why CPU is at 90%."
 *   - This is the missing semantic bridge between raw metric spikes and architectural understanding.
 *   - The drift report is JSON-serialisable and emitted to stdout in JSONL mode, making it trivially
 *     ingestible by Loki, Elasticsearch, Splunk, or any structured log aggregator.
 *
 * Caveat: Drift detection requires at least two scan cycles to produce a meaningful report. The first
 * scan will produce a "baseline established" event. Configure a reasonable --interval duration
 * to avoid false drift alerts from transient process noise.
 *
 * In case of any queries, please contact the maintainers for assistance.
 */

// DriftSeverity indicates the magnitude of the detected workload change.
type DriftSeverity string

const (
	// DriftSeverityInfo means a minor shift in the workload mix (< 10 percentage points).
	DriftSeverityInfo DriftSeverity = "INFO"

	// DriftSeverityWarn means a moderate shift (10–25 percentage points) deserving attention.
	DriftSeverityWarn DriftSeverity = "WARN"

	// DriftSeverityCritical means a significant architectural shift (> 25 percentage points)
	// that warrants immediate investigation.
	DriftSeverityCritical DriftSeverity = "CRITICAL"
)

// CategoryDrift represents how a specific workload category's share changed between scans.
type CategoryDrift struct {
	Category      Category      `json:"category"`
	PreviousPct   float64       `json:"previous_pct"`
	CurrentPct    float64       `json:"current_pct"`
	DeltaPct      float64       `json:"delta_pct"`
	Direction     string        `json:"direction"` // "increased", "decreased", "appeared", "vanished"
}

// DriftReport summarises the workload composition change between two consecutive scan cycles.
// Note that this is a first-class output type emitted alongside predictions in JSONL mode.
type DriftReport struct {
	// EventType is always "workload_drift" to allow easy filtering in log aggregators.
	EventType string `json:"event_type"`

	// Severity is one of INFO, WARN, or CRITICAL.
	Severity DriftSeverity `json:"severity"`

	// PreviousFingerprint is the SHA-256 fingerprint hash from the previous scan cycle.
	PreviousFingerprint string `json:"previous_fingerprint"`

	// CurrentFingerprint is the SHA-256 fingerprint hash from the current scan cycle.
	CurrentFingerprint string `json:"current_fingerprint"`

	// FingerprintChanged is true when the bucketed workload mix has shifted enough to produce a new hash.
	FingerprintChanged bool `json:"fingerprint_changed"`

	// MaxDeltaPct is the largest single-category percentage point change observed.
	MaxDeltaPct float64 `json:"max_delta_pct"`

	// Changes lists each category that shifted by more than the detection threshold.
	Changes []CategoryDrift `json:"changes"`

	// Summary is a human-readable one-line description of the most notable change.
	Summary string `json:"summary"`

	// DetectedAt is the timestamp when the drift was detected.
	DetectedAt time.Time `json:"detected_at"`
}

// DriftThreshold is the minimum percentage point change for a category to be included in drift reports.
// Minor fluctuations below this threshold are intentionally ignored to avoid noise.
//
// Note: This is intentionally set at 5.0 percentage points. Please do not lower this below 2.0
// as it will generate excessive noise in environments with many short-lived utility processes.
const DriftThreshold = 5.0

// DetectDrift compares two NodeFingerprints and returns a DriftReport if meaningful changes are found.
// It returns nil when no significant drift is detected (i.e., no category changed by more than
// DriftThreshold percentage points). This nil-safe design prevents unnecessary allocations in stable
// production environments.
func DetectDrift(previous, current NodeFingerprint) *DriftReport {
	if previous.TotalClassified == 0 {
		// No baseline yet; this is the first scan. Return nil — no drift to report.
		return nil
	}

	// Build lookup maps for O(1) access.
	prevMap := make(map[Category]float64, len(previous.DominantCategories))
	for _, s := range previous.DominantCategories {
		prevMap[s.Category] = s.Percentage
	}

	currMap := make(map[Category]float64, len(current.DominantCategories))
	for _, s := range current.DominantCategories {
		currMap[s.Category] = s.Percentage
	}

	// Collect all categories mentioned in either fingerprint.
	allCats := make(map[Category]struct{})
	for k := range prevMap {
		allCats[k] = struct{}{}
	}
	for k := range currMap {
		allCats[k] = struct{}{}
	}

	var changes []CategoryDrift
	maxDelta := 0.0

	for cat := range allCats {
		prevPct := prevMap[cat]
		currPct := currMap[cat]
		delta := currPct - prevPct

		absD := delta
		if absD < 0 {
			absD = -absD
		}

		if absD < DriftThreshold {
			continue // Below noise threshold; kindly ignore.
		}

		direction := "increased"
		switch {
		case prevPct == 0:
			direction = "appeared"
		case currPct == 0:
			direction = "vanished"
		case delta < 0:
			direction = "decreased"
		}

		changes = append(changes, CategoryDrift{
			Category:    cat,
			PreviousPct: prevPct,
			CurrentPct:  currPct,
			DeltaPct:    delta,
			Direction:   direction,
		})

		if absD > maxDelta {
			maxDelta = absD
		}
	}

	if len(changes) == 0 {
		return nil // Workload mix is stable; no drift report needed.
	}

	// Sort changes by absolute delta (largest first) for readability.
	sort.Slice(changes, func(i, j int) bool {
		di := changes[i].DeltaPct
		if di < 0 {
			di = -di
		}
		dj := changes[j].DeltaPct
		if dj < 0 {
			dj = -dj
		}
		return di > dj
	})

	// Determine severity based on the maximum delta observed.
	severity := DriftSeverityInfo
	switch {
	case maxDelta > 25.0:
		severity = DriftSeverityCritical
	case maxDelta >= 10.0:
		severity = DriftSeverityWarn
	}

	// Compose a human-readable summary from the most significant change.
	top := changes[0]
	summary := composeDriftSummary(top, len(changes))

	return &DriftReport{
		EventType:           "workload_drift",
		Severity:            severity,
		PreviousFingerprint: previous.Hash,
		CurrentFingerprint:  current.Hash,
		FingerprintChanged:  previous.Hash != current.Hash,
		MaxDeltaPct:         maxDelta,
		Changes:             changes,
		Summary:             summary,
		DetectedAt:          time.Now().UTC(),
	}
}

// composeDriftSummary constructs a concise, operator-friendly drift summary sentence.
func composeDriftSummary(top CategoryDrift, totalChanges int) string {
	delta := top.DeltaPct
	if delta < 0 {
		delta = -delta
	}

	var sb strings.Builder
	switch top.Direction {
	case "appeared":
		fmt.Fprintf(&sb, "%s workloads appeared on this node (now %.1f%% of classified processes)", top.Category, top.CurrentPct)
	case "vanished":
		fmt.Fprintf(&sb, "%s workloads vanished from this node (were %.1f%% of classified processes)", top.Category, top.PreviousPct)
	case "increased":
		fmt.Fprintf(&sb, "%s workloads increased by %.1f pp (%.1f%% → %.1f%%)", top.Category, delta, top.PreviousPct, top.CurrentPct)
	case "decreased":
		fmt.Fprintf(&sb, "%s workloads decreased by %.1f pp (%.1f%% → %.1f%%)", top.Category, delta, top.PreviousPct, top.CurrentPct)
	default:
		fmt.Fprintf(&sb, "%s workloads shifted by %.1f percentage points", top.Category, delta)
	}

	if totalChanges > 1 {
		fmt.Fprintf(&sb, " (+%d other categories changed)", totalChanges-1)
	}

	return sb.String()
}

