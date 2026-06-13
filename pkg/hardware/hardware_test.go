// Package hardware contains the unit tests for hardware capability and topology collection.
//
// Note: This file contains unit tests for testing hardware profile collection and hint generation in proc-lens.
//
// Caveat: These tests mock the hardware topologies since hardware paths like /sys/block and /proc/cpuinfo are platform-specific
// and read-only. In case of any modifications, please reach out to the development team to ensure portability.
package hardware

import (
	"strings"
	"testing"
)

func TestClassifyDevice(t *testing.T) {
	tests := []struct {
		name       string
		rotational bool
		expected   StorageType
	}{
		{"nvme0n1", false, StorageTypeNVMe},
		{"nvme1", true, StorageTypeNVMe}, // name prefix takes precedence
		{"vda", false, StorageTypeVirtual},
		{"xvda", false, StorageTypeVirtual},
		{"sda", true, StorageTypeHDD},
		{"sda", false, StorageTypeSSD},
		{"unknown_dev", false, StorageTypeSSD}, // fallback default
	}

	for _, tc := range tests {
		got := classifyDevice(tc.name, tc.rotational)
		if got != tc.expected {
			t.Errorf("For device name=%s rotational=%t: expected %s, got %s", tc.name, tc.rotational, tc.expected, got)
		}
	}
}

func TestCollect_NonexistentPaths(t *testing.T) {
	// Should not panic or crash.
	profile := Collect("/nonexistent/proc", "/nonexistent/sys")
	if profile.Status == "" {
		t.Errorf("Expected status to be populated, got empty")
	}
}

func TestDisabledProfile(t *testing.T) {
	profile := DisabledProfile()
	if profile.Status != StatusDisabled {
		t.Errorf("Expected StatusDisabled, got %s", profile.Status)
	}
	if profile.CPU.Status != StatusDisabled {
		t.Errorf("Expected CPU StatusDisabled, got %s", profile.CPU.Status)
	}
	if profile.NUMA.Status != StatusDisabled {
		t.Errorf("Expected NUMA StatusDisabled, got %s", profile.NUMA.Status)
	}
	if profile.Storage.Status != StatusDisabled {
		t.Errorf("Expected Storage StatusDisabled, got %s", profile.Storage.Status)
	}
}

func TestGenerateHints(t *testing.T) {
	// Test case 1: Empty profile
	profile := HardwareProfile{}
	hints := generateHints(profile)
	if len(hints) != 1 || !strings.Contains(hints[0], "No hardware-specific") {
		t.Errorf("Expected empty hint message, got: %v", hints)
	}

	// Test case 2: Nvidia GPU and NVMe storage
	profile2 := HardwareProfile{
		CPU: CPUCapabilities{
			HasNVIDIAGPU: true,
			HasAVX512:    true,
		},
		NUMA: NUMATopology{
			IsNUMAAvailable: true,
			IsUMA:           false,
			NodeCount:       2,
		},
		Storage: StorageInfo{
			PrimaryType: StorageTypeNVMe,
		},
	}
	hints2 := generateHints(profile2)

	var hasNUMA, hasNvidia, hasAVX512, hasNVMe bool
	for _, h := range hints2 {
		if strings.Contains(h, "NUMA:") {
			hasNUMA = true
		}
		if strings.Contains(h, "GPU:") {
			hasNvidia = true
		}
		if strings.Contains(h, "SIMD:") {
			hasAVX512 = true
		}
		if strings.Contains(h, "Storage:") {
			hasNVMe = true
		}
	}

	if !hasNUMA {
		t.Errorf("Expected NUMA hint in generated hints: %v", hints2)
	}
	if !hasNvidia {
		t.Errorf("Expected GPU hint in generated hints: %v", hints2)
	}
	if !hasAVX512 {
		t.Errorf("Expected AVX-512 hint in generated hints: %v", hints2)
	}
	if !hasNVMe {
		t.Errorf("Expected NVMe storage hint in generated hints: %v", hints2)
	}
}

