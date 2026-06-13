// Package pressure contains the unit tests for Pressure Stall Information (PSI) collection.
//
// Note: This file contains unit tests for testing PSI collection and parsing functionality of proc-lens.
//
// Caveat: These tests mock the /proc filesystem behavior since the actual /proc/pressure is only available
// on Linux environments with PSI enabled at boot time. In case of any modifications, please reach out to the development team.
package pressure

import (
	"context"
	"strings"
	"testing"
)

func TestCollect_MissingDirectory(t *testing.T) {
	data := Collect(context.Background(), "/nonexistent/path/abc")
	if data.Status != StatusNoData {
		t.Errorf("Expected StatusNoData, got %s", data.Status)
	}
	if data.Reason == "" {
		t.Errorf("Expected non-empty Reason explaining the missing directory")
	}
}

func TestCollect_EmptyProcPath(t *testing.T) {
	// On Windows/macOS, this will return StatusNoData. On Linux, it could be StatusFull or StatusNoData depending on PSI.
	// But it should always return a valid PressureData without crashing.
	data := Collect(context.Background(), "")
	if data.Status == "" {
		t.Errorf("Expected status to be set, got empty")
	}
}

func TestParsePSILine_ValidInput(t *testing.T) {
	fields := []string{"avg10=2.50", "avg60=1.20", "avg300=0.80", "total=12345"}
	line, err := parsePSILine(fields)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if line.Avg10 != 2.50 {
		t.Errorf("Expected Avg10 2.50, got %f", line.Avg10)
	}
	if line.Avg60 != 1.20 {
		t.Errorf("Expected Avg60 1.20, got %f", line.Avg60)
	}
	if line.Avg300 != 0.80 {
		t.Errorf("Expected Avg300 0.80, got %f", line.Avg300)
	}
	if line.TotalMicroseconds != 12345 {
		t.Errorf("Expected TotalMicroseconds 12345, got %d", line.TotalMicroseconds)
	}
}

func TestParsePSILine_EmptyInput(t *testing.T) {
	line, err := parsePSILine([]string{})
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if line.Avg10 != 0.0 || line.Avg60 != 0.0 || line.Avg300 != 0.0 || line.TotalMicroseconds != 0 {
		t.Errorf("Expected all fields to be 0, got %+v", line)
	}
}

func TestParsePSILine_MalformedInput(t *testing.T) {
	fields := []string{"badfield", "avg10=", "avg10=notanumber"}
	line, err := parsePSILine(fields)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if line.Avg10 != 0.0 {
		t.Errorf("Expected Avg10 0.0 on malformed input, got %f", line.Avg10)
	}
}

func TestDetectHighPressure_NoPressure(t *testing.T) {
	data := PressureData{
		CPU: PSIResource{
			Available: true,
			Full:      PSILine{Avg10: 0.5},
		},
	}
	alert := detectHighPressure(data)
	if alert != "" {
		t.Errorf("Expected empty alert, got: %s", alert)
	}
}

func TestDetectHighPressure_CriticalCPU(t *testing.T) {
	data := PressureData{
		CPU: PSIResource{
			Available: true,
			Full:      PSILine{Avg10: 15.0},
		},
	}
	alert := detectHighPressure(data)
	if !strings.Contains(alert, "CRITICAL") || !strings.Contains(alert, "CPU") {
		t.Errorf("Expected alert to contain 'CRITICAL' and 'CPU', got: %s", alert)
	}
}

func TestDisabledData(t *testing.T) {
	data := DisabledData()
	if data.Status != StatusDisabled {
		t.Errorf("Expected StatusDisabled, got %s", data.Status)
	}
	if data.Reason == "" {
		t.Errorf("Expected non-empty Reason for disabled status")
	}
}

func TestFormatText_NoData(t *testing.T) {
	data := PressureData{
		Status: StatusNoData,
		Reason: "PSI not enabled at boot",
	}
	output := FormatText(data)
	if !strings.Contains(output, "no data") {
		t.Errorf("Expected output to contain 'no data', got: %s", output)
	}
}

func TestFormatText_Disabled(t *testing.T) {
	data := DisabledData()
	output := FormatText(data)
	if !strings.Contains(output, "disabled") {
		t.Errorf("Expected output to contain 'disabled', got: %s", output)
	}
}

