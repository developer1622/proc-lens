// Package explain contains the unit tests for classification explanation.
//
// Note: This file contains unit tests for testing classification explanation functionality of proc-lens.
//
// Caveat: These tests rely on mock prediction telemetry data and mock classification centroids for asserting correct
// score propagation. In case of any modifications, please reach out to the development team to avoid build regressions.
package explain

import (
	"strings"
	"testing"

	"github.com/developer1622/proc-lens/pkg/classifier"
	"github.com/developer1622/proc-lens/pkg/collector"
)

// mockPrediction is a helper function to build a mock classifier.Prediction
func mockPrediction(cat classifier.Category, confidence float64, sockets int, cpu float64, memMb float64) classifier.Prediction {
	return classifier.Prediction{
		PID:             1234,
		Name:            "test-process",
		Cmdline:         "test-process --flag",
		PrimaryCategory: cat,
		Confidence:      confidence,
		Scores: map[classifier.Category]float64{
			cat:                  confidence,
			classifier.WebServer: confidence * 0.7,
			classifier.Unknown:   0.1,
		},
		Telemetry: collector.ProcessStats{
			PID:           1234,
			Name:          "test-process",
			CpuUsage:      cpu,
			MemRss:        uint64(memMb * 1024 * 1024),
			SocketCount:   sockets,
			Threads:       8,
			FdCount:       100,
			IoReadSpeed:   1024 * 100,
			IoWriteSpeed:  1024 * 50,
			CtxSwitchRate: 500,
		},
		RulesTriggered:  []string{"Test rule: socket count > 500"},
		Recommendations: []string{},
	}
}

func TestExplain_BasicCacheStore(t *testing.T) {
	pred := mockPrediction(classifier.CacheStore, 0.85, 600, 10.0, 128.0)
	exp := Explain(pred)

	if exp.PID != pred.PID {
		t.Errorf("Expected PID %d, got %d", pred.PID, exp.PID)
	}
	if exp.ProcessName != pred.Name {
		t.Errorf("Expected process name %s, got %s", pred.Name, exp.ProcessName)
	}
	if exp.PrimaryCategory != pred.PrimaryCategory {
		t.Errorf("Expected primary category %s, got %s", pred.PrimaryCategory, exp.PrimaryCategory)
	}
	if exp.ConfidencePct <= 0 {
		t.Errorf("Expected confidence percentage > 0, got %f", exp.ConfidencePct)
	}
	if len(exp.AllFeatures) != 8 {
		t.Errorf("Expected 8 features, got %d", len(exp.AllFeatures))
	}
	if len(exp.TopFeatures) != 3 {
		t.Errorf("Expected 3 top features, got %d", len(exp.TopFeatures))
	}
	if len(exp.WhatIfHints) == 0 {
		t.Errorf("Expected WhatIfHints, got empty")
	}
	if exp.ModelSummary == "" {
		t.Errorf("Expected ModelSummary to be non-empty")
	}
	if exp.LearningNote == "" {
		t.Errorf("Expected LearningNote to be non-empty")
	}
}

func TestExplain_RunnerUpDetected(t *testing.T) {
	pred := mockPrediction(classifier.CacheStore, 0.85, 600, 10.0, 128.0)
	exp := Explain(pred)

	if exp.RunnerUpCategory != classifier.WebServer {
		t.Errorf("Expected runner up %s, got %s", classifier.WebServer, exp.RunnerUpCategory)
	}
	if exp.DistanceToRunnerUp <= 0 {
		t.Errorf("Expected distance to runner up > 0, got %f", exp.DistanceToRunnerUp)
	}
}

func TestExplain_UnknownCategory(t *testing.T) {
	pred := mockPrediction(classifier.Unknown, 0.40, 5, 2.0, 16.0)
	exp := Explain(pred)

	if !strings.Contains(strings.ToLower(exp.LearningNote), "did not match") {
		t.Errorf("Expected learning note to mention 'did not match', got: %s", exp.LearningNote)
	}
}

func TestExplain_ZeroResourceProcess(t *testing.T) {
	pred := classifier.Prediction{
		PID:             9999,
		Name:            "zero-process",
		PrimaryCategory: classifier.Unknown,
		Confidence:      0.2,
		Scores: map[classifier.Category]float64{
			classifier.Unknown: 0.2,
		},
		Telemetry: collector.ProcessStats{
			PID:  9999,
			Name: "zero-process",
		},
	}

	exp := Explain(pred)

	for _, f := range exp.AllFeatures {
		if !strings.Contains(f.Status, "zero") {
			t.Errorf("Expected status to contain 'zero' for feature %s, got: %s", f.FeatureName, f.Status)
		}
	}
}

func TestFormatTextExplanation_NotEmpty(t *testing.T) {
	pred := mockPrediction(classifier.CacheStore, 0.85, 600, 10.0, 128.0)
	exp := Explain(pred)
	output := FormatTextExplanation(exp)

	if output == "" {
		t.Errorf("Expected FormatTextExplanation output to be non-empty")
	}
	if !strings.Contains(output, "1234") {
		t.Errorf("Expected output to contain PID 1234, got: %s", output)
	}
}

func TestBuildBar(t *testing.T) {
	bar := buildBar(50.0, 20)
	// count runes, not bytes, as UTF-8 block chars are 3 bytes each
	runeCount := len([]rune(bar))
	if runeCount != 20 {
		t.Errorf("Expected bar to contain 20 runes, got %d", runeCount)
	}
	expectedFilled := strings.Repeat("█", 10)
	expectedEmpty := strings.Repeat("░", 10)
	if !strings.HasPrefix(bar, expectedFilled) || !strings.HasSuffix(bar, expectedEmpty) {
		t.Errorf("Expected bar to have 10 filled and 10 empty runes, got %s", bar)
	}
}

