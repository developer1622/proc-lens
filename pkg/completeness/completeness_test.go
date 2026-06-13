// Package completeness contains the unit tests for data completeness tracking.
//
// Note: This file contains unit tests for testing completeness tracking and reporting functionality of proc-lens.
//
// Caveat: These tests rely on simulated data source recordings and assert expected completeness score formulas.
// In case of any modifications, please reach out to the development team to ensure correctness of math.
package completeness

import (
	"math"
	"strings"
	"testing"
)

func TestNewTracker_HasDefaultSources(t *testing.T) {
	tracker := NewTracker()
	if len(tracker.sources) < 3 {
		t.Errorf("Expected at least 3 default sources pre-registered, got %d", len(tracker.sources))
	}
}

func TestRecord_UpdatesStatus(t *testing.T) {
	tracker := NewTracker()
	tracker.RecordFull("proc_basic", "core")
	report := tracker.Build()

	found := false
	for _, s := range report.Sources {
		if s.Key == "proc_basic" {
			found = true
			if s.Status != StatusFull {
				t.Errorf("Expected StatusFull, got %s", s.Status)
			}
		}
	}
	if !found {
		t.Errorf("Expected to find proc_basic in built report sources")
	}
}

func TestRecordNoData(t *testing.T) {
	tracker := NewTracker()
	tracker.RecordNoData("psi_cpu", "linux_extended", "PSI is disabled in kernel")
	report := tracker.Build()

	found := false
	for _, s := range report.Sources {
		if s.Key == "psi_cpu" {
			found = true
			if s.Status != StatusNoData {
				t.Errorf("Expected StatusNoData, got %s", s.Status)
			}
			if s.Reason != "PSI is disabled in kernel" {
				t.Errorf("Expected specific reason, got: %s", s.Reason)
			}
		}
	}
	if !found {
		t.Errorf("Expected to find psi_cpu in built report sources")
	}
}

func TestScore_AllFull(t *testing.T) {
	tracker := NewTracker()
	// Mark all pre-registered sources as Full
	for k, s := range tracker.sources {
		tracker.RecordFull(k, s.Category)
	}
	report := tracker.Build()
	if report.Score != 1.0 {
		t.Errorf("Expected score 1.0, got %f", report.Score)
	}
}

func TestScore_AllNoData(t *testing.T) {
	tracker := NewTracker()
	// Mark all pre-registered sources as NoData
	for k, s := range tracker.sources {
		tracker.RecordNoData(k, s.Category, "missing")
	}
	report := tracker.Build()
	if report.Score != 0.0 {
		t.Errorf("Expected score 0.0, got %f", report.Score)
	}
}

func TestScore_Mixed(t *testing.T) {
	// Create a new empty tracker and add specific weights
	tracker := &Tracker{sources: make(map[string]SourceRecord)}
	tracker.RecordFull("s1", "cat1")
	tracker.RecordFull("s2", "cat1")
	tracker.RecordNoData("s3", "cat1", "missing")
	tracker.RecordNoData("s4", "cat1", "missing")

	report := tracker.Build()
	// 2 full (2.0) and 2 no_data (0.0). Total weight = 4.0. Expected score = 0.5.
	if math.Abs(report.Score-0.5) > 1e-9 {
		t.Errorf("Expected score 0.5, got %f", report.Score)
	}
}

func TestScore_DisabledExcluded(t *testing.T) {
	tracker := &Tracker{sources: make(map[string]SourceRecord)}
	tracker.RecordFull("s1", "cat1")
	tracker.RecordDisabled("s2", "cat1")

	report := tracker.Build()
	// s2 is disabled, should be excluded. Total weight = 1.0. Score = 1.0 / 1.0 = 1.0.
	if report.Score != 1.0 {
		t.Errorf("Expected score 1.0 when disabled sources are present, got %f", report.Score)
	}
}

func TestBuild_SortedOutput(t *testing.T) {
	tracker := &Tracker{sources: make(map[string]SourceRecord)}
	tracker.RecordFull("b_source", "core")
	tracker.RecordFull("a_source", "core")
	tracker.RecordFull("c_source", "hardware")

	report := tracker.Build()
	if len(report.Sources) != 3 {
		t.Fatalf("Expected 3 sources, got %d", len(report.Sources))
	}
	// Sorted by category first, then key:
	// 1. core / a_source
	// 2. core / b_source
	// 3. hardware / c_source
	if report.Sources[0].Key != "a_source" || report.Sources[1].Key != "b_source" || report.Sources[2].Key != "c_source" {
		t.Errorf("Sources not sorted correctly: 0=%s, 1=%s, 2=%s", report.Sources[0].Key, report.Sources[1].Key, report.Sources[2].Key)
	}
}

func TestFormatText_ContainsScoreSummary(t *testing.T) {
	tracker := NewTracker()
	tracker.RecordFull("proc_basic", "core")
	report := tracker.Build()
	output := FormatText(report)

	if !strings.Contains(output, "%") {
		t.Errorf("Expected output to contain percent sign, got: %s", output)
	}
	if !strings.Contains(output, "Source") {
		t.Errorf("Expected output to contain 'Source' header, got: %s", output)
	}
}

func TestBuildScoreSummary_Levels(t *testing.T) {
	tests := []struct {
		score    float64
		expected string
	}{
		{0.3, "Limited"},
		{0.6, "Partial"},
		{0.85, "Good"},
		{1.0, "Full"},
	}

	for _, tc := range tests {
		r := Report{Score: tc.score}
		summary := buildScoreSummary(r)
		if !strings.Contains(summary, tc.expected) {
			t.Errorf("For score %f, expected summary to contain %s, got: %s", tc.score, tc.expected, summary)
		}
	}
}

