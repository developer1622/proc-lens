package classifier

import "math"

/*
 * Note: This file defines the centroids (signature vectors) for various workloads.
 *
 * Caveat: The thresholds and physical counters mapped here are derived from benchmark data.
 * If you find any discrepancies under specific production workloads, kindly update the same to tune accuracy.
 */

// Centroids maps HLD categories to their canonical reference profiles.
// Note that the same contains the reference vectors mapped on logarithmic scale.
var Centroids = map[Category]FeatureVector{
	LoadBalancer:      makeCentroid(10.0, 64.0, 8.0, 1000.0, 1100.0, 10.0, 10.0, 5000.0),
	WebServer:         makeCentroid(15.0, 128.0, 24.0, 400.0, 450.0, 100.0, 100.0, 2000.0),
	CacheStore:        makeCentroid(8.0, 1024.0, 4.0, 800.0, 850.0, 0.1, 0.1, 6000.0),
	RelationalDB:      makeCentroid(20.0, 2048.0, 50.0, 60.0, 250.0, 1000.0, 1000.0, 1500.0),
	NoSQLDB:           makeCentroid(25.0, 4096.0, 128.0, 150.0, 500.0, 2000.0, 2000.0, 3000.0),
	ColumnarDB:        makeCentroid(80.0, 8192.0, 32.0, 10.0, 150.0, 20000.0, 1000.0, 1000.0),
	VectorDB:          makeCentroid(95.0, 6144.0, 48.0, 40.0, 200.0, 5000.0, 200.0, 4000.0),
	SearchEngine:      makeCentroid(30.0, 6144.0, 80.0, 80.0, 400.0, 1500.0, 1500.0, 2000.0),
	MessageBroker:     makeCentroid(12.0, 512.0, 32.0, 300.0, 400.0, 200.0, 200.0, 2500.0),
	EventStreaming:    makeCentroid(35.0, 4096.0, 90.0, 250.0, 600.0, 15000.0, 25000.0, 4000.0),
	AITraining:        makeCentroid(400.0, 16384.0, 64.0, 5.0, 80.0, 500.0, 100.0, 8000.0),
	AIInference:       makeCentroid(120.0, 8192.0, 40.0, 100.0, 150.0, 100.0, 10.0, 5000.0),
	OrchestratorAgent: makeCentroid(5.0, 256.0, 20.0, 15.0, 800.0, 50.0, 50.0, 1200.0),
	MonitoringAgent:   makeCentroid(8.0, 512.0, 16.0, 150.0, 300.0, 200.0, 4000.0, 800.0),
	InteractiveShell:  makeCentroid(0.1, 8.0, 1.0, 0.0, 5.0, 0.0, 0.0, 5.0),
	UtilityBatch:      makeCentroid(100.0, 128.0, 4.0, 0.0, 15.0, 10000.0, 10000.0, 400.0),
}

// makeCentroid takes physical telemetry profiles and maps them to logarithmic scales.
func makeCentroid(cpu, memMb, threads, sockets, fds, ioReadKb, ioWriteKb, ctxSw float64) FeatureVector {
	return FeatureVector{
		CPU:         math.Log1p(cpu),
		Memory:      math.Log1p(memMb),
		Threads:     math.Log1p(threads),
		Sockets:     math.Log1p(sockets),
		FDs:         math.Log1p(fds),
		IORead:      math.Log1p(ioReadKb),
		IOWrite:     math.Log1p(ioWriteKb),
		CtxSwitches: math.Log1p(ctxSw),
	}
}

