package collector

import (
	"context"
	"testing"
	"time"
)

func TestGetRawStatsInvalidPID(t *testing.T) {
	// Querying stats for an invalid PID should fail gracefully
	_, err := GetRawStats(context.Background(), -9999, "", "")
	if err == nil {
		t.Error("Expected error for negative invalid PID, got nil")
	}
}

func TestCalculateRateStats(t *testing.T) {
	now := time.Now()
	s1 := RawStats{
		PID:          100,
		Name:         "test",
		Cmdline:      "test-cmd",
		CpuTime:      10.0,
		MemRss:       1024,
		MemVirt:      2048,
		Threads:      2,
		FdCount:      5,
		SocketCount:  2,
		IoReadBytes:  1000,
		IoWriteBytes: 2000,
		CtxSwitches:  50,
		SampleTime:   now,
	}

	s2 := RawStats{
		PID:          100,
		Name:         "test",
		Cmdline:      "test-cmd",
		CpuTime:      12.0, // +2 seconds of CPU
		MemRss:       1024,
		MemVirt:      2048,
		Threads:      2,
		FdCount:      5,
		SocketCount:  2,
		IoReadBytes:  2000, // +1000 bytes read
		IoWriteBytes: 4000, // +2000 bytes written
		CtxSwitches:  150,  // +100 context switches
		SampleTime:   now.Add(2 * time.Second), // 2 seconds elapsed
	}

	stats := CalculateRateStats(s1, s2)

	if stats.CpuUsage != 100.0 { // (2.0s CPU / 2.0s elapsed) * 100 = 100%
		t.Errorf("Expected CPU usage 100.0, got %f", stats.CpuUsage)
	}
	if stats.IoReadSpeed != 500.0 { // 1000 bytes / 2s = 500 B/s
		t.Errorf("Expected Read speed 500.0, got %f", stats.IoReadSpeed)
	}
	if stats.IoWriteSpeed != 1000.0 { // 2000 bytes / 2s = 1000 B/s
		t.Errorf("Expected Write speed 1000.0, got %f", stats.IoWriteSpeed)
	}
	if stats.CtxSwitchRate != 50.0 { // 100 switches / 2s = 50/s
		t.Errorf("Expected Ctx Switch Rate 50.0, got %f", stats.CtxSwitchRate)
	}
}

func TestCalculateRateStatsNegativeClamping(t *testing.T) {
	now := time.Now()
	s1 := RawStats{
		PID:          100,
		CpuTime:      10.0,
		IoReadBytes:  1000,
		IoWriteBytes: 2000,
		CtxSwitches:  50,
		SampleTime:   now,
	}

	// s2 has lower values simulating a counter wrap/reset
	s2 := RawStats{
		PID:          100,
		CpuTime:      5.0,
		IoReadBytes:  500,
		IoWriteBytes: 1000,
		CtxSwitches:  20,
		SampleTime:   now.Add(1 * time.Second),
	}

	stats := CalculateRateStats(s1, s2)

	if stats.CpuUsage != 0.0 || stats.IoReadSpeed != 0.0 || stats.IoWriteSpeed != 0.0 || stats.CtxSwitchRate != 0.0 {
		t.Errorf("Expected negative values to be clamped to 0, got: CPU=%f, Read=%f, Write=%f, CtxSw=%f",
			stats.CpuUsage, stats.IoReadSpeed, stats.IoWriteSpeed, stats.CtxSwitchRate)
	}
}

func TestCollectConcurrentRawStats(t *testing.T) {
	procs := []SimpleProcessInfo{
		{PID: -10, Name: "invalid-test-1", Cmdline: "invalid-cmd"},
	}

	var errorCount int
	errRecorder := func(reason string) {
		if reason == "get_raw_stats" {
			errorCount++
		}
	}

	var durationCount int
	durationRecorder := func(d time.Duration) {
		durationCount++
	}

	statsMap := CollectConcurrentRawStats(context.Background(), procs, errRecorder, durationRecorder)

	if len(statsMap) != 0 {
		t.Errorf("Expected statsMap to be empty, got size %d", len(statsMap))
	}
	if errorCount != 1 {
		t.Errorf("Expected errorCount to be 1, got %d", errorCount)
	}
	if durationCount != 0 {
		t.Errorf("Expected durationCount to be 0, got %d", durationCount)
	}
}

