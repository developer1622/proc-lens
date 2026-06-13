package classifier

import (
	"math"
	"github.com/developer1622/proc-lens/pkg/collector"
	"testing"
)

func TestExtractFeatures(t *testing.T) {
	stats := collector.ProcessStats{
		CpuUsage:      100.0,
		MemRss:        1024 * 1024 * 1024, // 1024 MB
		Threads:       10,
		SocketCount:   5,
		FdCount:       20,
		IoReadSpeed:   10240, // 10 KB/s
		IoWriteSpeed:  20480, // 20 KB/s
		CtxSwitchRate: 500,
	}

	fv := extractFeatures(stats)

	// Math checks: log1p(x) = ln(1 + x)
	expectedCPU := math.Log1p(100.0)
	if math.Abs(fv.CPU-expectedCPU) > 1e-9 {
		t.Errorf("Expected CPU feature %f, got %f", expectedCPU, fv.CPU)
	}

	expectedMem := math.Log1p(1024.0) // 1024 MB
	if math.Abs(fv.Memory-expectedMem) > 1e-9 {
		t.Errorf("Expected Memory feature %f, got %f", expectedMem, fv.Memory)
	}

	expectedThreads := math.Log1p(10)
	if math.Abs(fv.Threads-expectedThreads) > 1e-9 {
		t.Errorf("Expected Threads feature %f, got %f", expectedThreads, fv.Threads)
	}

	expectedSockets := math.Log1p(5)
	if math.Abs(fv.Sockets-expectedSockets) > 1e-9 {
		t.Errorf("Expected Sockets feature %f, got %f", expectedSockets, fv.Sockets)
	}

	expectedFDs := math.Log1p(20)
	if math.Abs(fv.FDs-expectedFDs) > 1e-9 {
		t.Errorf("Expected FDs feature %f, got %f", expectedFDs, fv.FDs)
	}

	expectedIORead := math.Log1p(10.0) // 10 KB/s
	if math.Abs(fv.IORead-expectedIORead) > 1e-9 {
		t.Errorf("Expected IORead feature %f, got %f", expectedIORead, fv.IORead)
	}

	expectedIOWrite := math.Log1p(20.0) // 20 KB/s
	if math.Abs(fv.IOWrite-expectedIOWrite) > 1e-9 {
		t.Errorf("Expected IOWrite feature %f, got %f", expectedIOWrite, fv.IOWrite)
	}

	expectedCtxSw := math.Log1p(500)
	if math.Abs(fv.CtxSwitches-expectedCtxSw) > 1e-9 {
		t.Errorf("Expected CtxSwitches feature %f, got %f", expectedCtxSw, fv.CtxSwitches)
	}
}

func TestCosineSimilarity(t *testing.T) {
	v1 := FeatureVector{CPU: 1, Memory: 1, Threads: 1}
	v2 := FeatureVector{CPU: 1, Memory: 1, Threads: 1}
	sim := cosineSimilarity(v1, v2)
	if math.Abs(sim-1.0) > 1e-9 {
		t.Errorf("Expected similarity 1.0, got %f", sim)
	}

	v3 := FeatureVector{CPU: 1, Memory: 0, Threads: 0}
	v4 := FeatureVector{CPU: 0, Memory: 1, Threads: 1}
	sim2 := cosineSimilarity(v3, v4)
	if math.Abs(sim2-0.0) > 1e-9 {
		t.Errorf("Expected orthogonal similarity 0.0, got %f", sim2)
	}
}

func TestPredictTableDriven(t *testing.T) {
	tests := []struct {
		desc             string
		stats            collector.ProcessStats
		expectedCategory Category
		minConfidence    float64
	}{
		{
			desc: "PostgreSQL relational database workload",
			stats: collector.ProcessStats{
				PID:           101,
				Name:          "postgres",
				Cmdline:       "/usr/bin/postgres -D /var/lib/postgresql/data",
				CpuUsage:      15.0,
				MemRss:        2 * 1024 * 1024 * 1024, // 2GB
				Threads:       20,
				SocketCount:   100,
				FdCount:       150,
				IoReadSpeed:   2 * 1024 * 1024,
				IoWriteSpeed:  3 * 1024 * 1024,
				CtxSwitchRate: 1500,
			},
			expectedCategory: RelationalDB,
			minConfidence:    0.6,
		},
		{
			desc: "Redis cache store workload",
			stats: collector.ProcessStats{
				PID:           102,
				Name:          "redis-server",
				Cmdline:       "redis-server *:6379",
				CpuUsage:      50.0,
				MemRss:        500 * 1024 * 1024,
				Threads:       4,
				SocketCount:   1000,
				FdCount:       1100,
				IoReadSpeed:   0,
				IoWriteSpeed:  10000,
				CtxSwitchRate: 8000,
			},
			expectedCategory: CacheStore,
			minConfidence:    0.6,
		},
		{
			desc: "Nginx load balancer / web server workload",
			stats: collector.ProcessStats{
				PID:           103,
				Name:          "nginx",
				Cmdline:       "nginx: worker process",
				CpuUsage:      8.0,
				MemRss:        32 * 1024 * 1024,
				Threads:       1,
				SocketCount:   550,
				FdCount:       600,
				IoReadSpeed:   0,
				IoWriteSpeed:  500,
				CtxSwitchRate: 300,
			},
			expectedCategory: LoadBalancer,
			minConfidence:    0.5,
		},
		{
			desc: "PyTorch Deep Learning model training",
			stats: collector.ProcessStats{
				PID:           104,
				Name:          "python3",
				Cmdline:       "python3 train.py --batch-size 64 --model resnet",
				CpuUsage:      600.0,
				MemRss:        12 * 1024 * 1024 * 1024,
				Threads:       32,
				SocketCount:   2,
				FdCount:       45,
				IoReadSpeed:   5 * 1024 * 1024,
				IoWriteSpeed:  1000,
				CtxSwitchRate: 12000,
			},
			expectedCategory: AITraining,
			minConfidence:    0.6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			pred := Predict(tt.stats)
			if pred.PrimaryCategory != tt.expectedCategory {
				t.Errorf("Expected category %s, got %s", tt.expectedCategory, pred.PrimaryCategory)
			}
			if pred.Confidence < tt.minConfidence {
				t.Errorf("Expected confidence >= %f, got %f", tt.minConfidence, pred.Confidence)
			}
		})
	}
}

func BenchmarkPredict(b *testing.B) {
	stats := collector.ProcessStats{
		PID:           999,
		Name:          "postgres",
		Cmdline:       "postgres -D /data",
		CpuUsage:      25.0,
		MemRss:        1024 * 1024 * 1024,
		Threads:       16,
		SocketCount:   80,
		FdCount:       100,
		IoReadSpeed:   5000,
		IoWriteSpeed:  5000,
		CtxSwitchRate: 1000,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Predict(stats)
	}
}

func FuzzPredict(f *testing.F) {
	f.Add("postgres", "postgres -D /data")
	f.Add("python3", "python3 train.py")
	f.Add("nginx", "nginx: worker process")

	f.Fuzz(func(t *testing.T, name, cmdline string) {
		stats := collector.ProcessStats{
			PID:           1000,
			Name:          name,
			Cmdline:       cmdline,
			CpuUsage:      50.0,
			MemRss:        1024 * 1024 * 1024,
			Threads:       10,
			SocketCount:   20,
			FdCount:       30,
			IoReadSpeed:   1000,
			IoWriteSpeed:  1000,
			CtxSwitchRate: 200,
		}

		pred := Predict(stats)
		if pred.PrimaryCategory == "" {
			t.Errorf("empty category for name=%q, cmdline=%q", name, cmdline)
		}
		if pred.Confidence < 0 || pred.Confidence > 1.0 {
			t.Errorf("invalid confidence %f for name=%q, cmdline=%q", pred.Confidence, name, cmdline)
		}
	})
}

