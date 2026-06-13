// Package completeness provides the Data Completeness tracking feature for proc-lens.
//
// # Overview
//
// In mixed fleets (old kernels, minimal containers, non-Linux nodes), many monitoring
// agents either silently return zeros or crash when data sources are unavailable. This
// creates invisible blindspots that operators only discover during incidents.
//
// The completeness package provides a structured, machine-readable record of exactly which
// data sources are available, partial, or unavailable on the current host — and why.
// This "honesty layer" is attached to every JSON scan output and is queryable as a
// Prometheus gauge.
//
// # Design
//
// Each data source is recorded with:
//   - A stable key (e.g., "proc_basic", "psi", "numa", "hardware_cpu")
//   - A DataStatus (full, partial, no_data, disabled)
//   - A human-readable reason string
//
// The overall DataCompletenessScore is [0.0, 1.0] where:
//   - 1.0 = all registered sources returned "full" data
//   - 0.0 = all sources returned "no_data" or "disabled"
//
// Note: This package is always-on for JSON output. It adds negligible overhead
// (a few map lookups and a float64 computation). There is no flag needed to enable it.
//
// Caveat: The completeness score reflects what proc-lens *can see*, not the full picture
// of what the operating system provides. For example, a score of 0.8 means 80% of proc-lens's
// data sources are fully available — it does not mean 20% of system metrics are missing overall.
//
// In case of any queries, please contact the maintainers for assistance.
package completeness

import (
	"fmt"
	"sort"
	"strings"
)

// DataStatus describes the availability of a data source.
type DataStatus string

const (
	// StatusFull means the data source returned complete, usable data.
	StatusFull DataStatus = "full"

	// StatusPartial means the data source returned some but not all expected data.
	StatusPartial DataStatus = "partial"

	// StatusNoData means the data source is not available on this platform/configuration.
	StatusNoData DataStatus = "no_data"

	// StatusDisabled means the data source was intentionally disabled via a flag.
	StatusDisabled DataStatus = "disabled"
)

// SourceRecord records the completeness status of a single data source.
type SourceRecord struct {
	// Key is a stable, machine-readable identifier for this data source.
	Key string `json:"key"`

	// Status is "full", "partial", "no_data", or "disabled".
	Status DataStatus `json:"status"`

	// Reason is a human-readable explanation for any non-full status.
	// Empty when Status is "full".
	Reason string `json:"reason,omitempty"`

	// Category groups related sources (e.g., "core", "linux_extended", "hardware").
	Category string `json:"category"`
}

// Report is the complete data completeness snapshot for a collection cycle.
// It is designed to be JSON-serialised and attached to CollectionContext or
// emitted as a standalone "data_completeness" event in JSONL mode.
type Report struct {
	// Sources is an ordered list of all registered data source records.
	Sources []SourceRecord `json:"sources"`

	// Score is a normalised completeness score in [0.0, 1.0].
	// Calculation: full=1.0 per source, partial=0.5, no_data=0.0, disabled=0.0.
	Score float64 `json:"score"`

	// TotalSources is the count of all registered sources.
	TotalSources int `json:"total_sources"`

	// FullSources is the count of sources with StatusFull.
	FullSources int `json:"full_sources"`

	// PartialSources is the count of sources with StatusPartial.
	PartialSources int `json:"partial_sources"`

	// NoDataSources is the count of sources with StatusNoData.
	NoDataSources int `json:"no_data_sources"`

	// DisabledSources is the count of sources with StatusDisabled.
	DisabledSources int `json:"disabled_sources"`

	// ScoreSummary is a human-readable representation of the score.
	ScoreSummary string `json:"score_summary"`
}

// Tracker accumulates data source records during a collection cycle.
// It is not thread-safe by design — create one per goroutine/cycle.
// Note: Tracker is intentionally a simple value type. Create with NewTracker().
type Tracker struct {
	sources map[string]SourceRecord
}

// NewTracker creates a new Tracker pre-populated with the core proc-lens data sources
// set to their default (disabled) state. Each collector then calls Record() to update them.
func NewTracker() *Tracker {
	t := &Tracker{sources: make(map[string]SourceRecord)}

	// Pre-register all known data sources with "disabled" status as the default.
	// Collectors will call Record() to upgrade these to "full", "partial", or "no_data".
	coreDefaults := []struct {
		key      string
		category string
	}{
		{"proc_basic", "core"},
		{"proc_io", "core"},
		{"proc_fd_types", "core"},
		{"proc_ctx_switches", "core"},
		{"proc_oom", "linux_extended"},
		{"proc_cgroup", "linux_extended"},
		{"k8s_metadata", "linux_extended"},
		{"psi_cpu", "linux_extended"},
		{"psi_memory", "linux_extended"},
		{"psi_io", "linux_extended"},
		{"hardware_cpu", "hardware"},
		{"hardware_numa", "hardware"},
		{"hardware_storage", "hardware"},
	}

	for _, d := range coreDefaults {
		t.sources[d.key] = SourceRecord{
			Key:      d.key,
			Status:   StatusDisabled,
			Reason:   "Data source not yet registered by the collector.",
			Category: d.category,
		}
	}

	return t
}

// Record updates the status of a data source in the tracker.
// If the key does not exist, it is created automatically.
// Call this from within collector functions after each data collection attempt.
func (t *Tracker) Record(key, category string, status DataStatus, reason string) {
	t.sources[key] = SourceRecord{
		Key:      key,
		Status:   status,
		Reason:   reason,
		Category: category,
	}
}

// RecordFull marks a data source as fully available with no limitations.
func (t *Tracker) RecordFull(key, category string) {
	t.Record(key, category, StatusFull, "")
}

// RecordNoData marks a data source as unavailable with the given reason.
func (t *Tracker) RecordNoData(key, category string, reason string) {
	t.Record(key, category, StatusNoData, reason)
}

// RecordPartial marks a data source as partially available with the given reason.
func (t *Tracker) RecordPartial(key, category string, reason string) {
	t.Record(key, category, StatusPartial, reason)
}

// RecordDisabled marks a data source as disabled by configuration.
func (t *Tracker) RecordDisabled(key, category string) {
	t.Record(key, category, StatusDisabled, "Feature disabled by configuration. Refer to the --enable-* flags for enablement.")
}

// Build computes and returns the final Report from all registered sources.
// Call this once after all collectors have run.
func (t *Tracker) Build() Report {
	sources := make([]SourceRecord, 0, len(t.sources))
	for _, s := range t.sources {
		sources = append(sources, s)
	}

	// Sort by category then key for deterministic output.
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Category != sources[j].Category {
			return sources[i].Category < sources[j].Category
		}
		return sources[i].Key < sources[j].Key
	})

	report := Report{
		Sources:      sources,
		TotalSources: len(sources),
	}

	// Calculate score.
	totalWeight := 0.0
	scoredWeight := 0.0
	for _, s := range sources {
		switch s.Status {
		case StatusFull:
			totalWeight += 1.0
			scoredWeight += 1.0
			report.FullSources++
		case StatusPartial:
			totalWeight += 1.0
			scoredWeight += 0.5
			report.PartialSources++
		case StatusNoData:
			totalWeight += 1.0
			// no_data contributes 0.0 to the score.
			report.NoDataSources++
		case StatusDisabled:
			// Disabled sources are excluded from the score calculation entirely
			// to avoid penalising operators who have not opted into optional features.
			report.DisabledSources++
		}
	}

	if totalWeight > 0 {
		report.Score = scoredWeight / totalWeight
	} else {
		report.Score = 1.0 // No active sources = vacuously complete.
	}

	report.ScoreSummary = buildScoreSummary(report)
	return report
}

// buildScoreSummary creates a human-readable description of the completeness score.
func buildScoreSummary(r Report) string {
	level := "Good"
	switch {
	case r.Score < 0.4:
		level = "Limited — several key data sources are unavailable. check kernel version and /proc, /sys mounts."
	case r.Score < 0.7:
		level = "Partial — some data sources are unavailable. Core classification and recommendations are unaffected."
	case r.Score < 1.0:
		level = "Good — most data sources are available. Minor gaps noted above."
	default:
		level = "Full — all active data sources are available."
	}
	return fmt.Sprintf("%.0f%% (%s)", r.Score*100, level)
}

// FormatText renders the Report as a human-readable terminal section.
func FormatText(r Report) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "\n  [Data Completeness Score: %s]\n", r.ScoreSummary)
	fmt.Fprintf(&sb, "  %-30s %-12s %s\n", "Source", "Status", "Note")
	fmt.Fprintf(&sb, "  %s\n", strings.Repeat("─", 70))

	prevCategory := ""
	for _, s := range r.Sources {
		if s.Category != prevCategory {
			fmt.Fprintf(&sb, "\n  [%s]\n", strings.ToUpper(s.Category))
			prevCategory = s.Category
		}

		statusIcon := "✗"
		switch s.Status {
		case StatusFull:
			statusIcon = "✓"
		case StatusPartial:
			statusIcon = "⚡"
		case StatusDisabled:
			statusIcon = "○"
		}

		note := ""
		if s.Reason != "" && len(s.Reason) > 40 {
			note = s.Reason[:37] + "..."
		} else {
			note = s.Reason
		}

		fmt.Fprintf(&sb, "  %-30s %s %-10s %s\n", s.Key, statusIcon, s.Status, note)
	}

	fmt.Fprintf(&sb, "\n  Note: Core proc-lens features (scan, analyze, enrich) are never affected by optional data source availability.\n")
	return sb.String()
}

