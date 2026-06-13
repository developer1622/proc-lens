package optimizer

import (
	"context"
	"github.com/developer1622/proc-lens/pkg/classifier"
	"runtime"
	"strings"
	"testing"
)

func TestOptimizeDatabase(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Skipping Linux-specific optimization test on non-Linux platform")
	}

	pred := classifier.Prediction{
		PID:             1234,
		Name:            "postgres",
		PrimaryCategory: classifier.RelationalDB,
		Confidence:      0.95,
	}

	Optimize(context.Background(), &pred)

	if len(pred.Recommendations) == 0 {
		t.Error("Expected recommendations for RelationalDB, got none")
	}

	foundHugePages := false
	for _, rec := range pred.Recommendations {
		if strings.Contains(rec, "Huge Pages") {
			foundHugePages = true
			break
		}
	}

	if !foundHugePages {
		t.Error("Expected relational database recommendation to include Huge Pages check")
	}
}

func TestOptimizeCacheStore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Skipping Linux-specific optimization test on non-Linux platform")
	}

	pred := classifier.Prediction{
		PID:             2020,
		Name:            "redis-server",
		PrimaryCategory: classifier.CacheStore,
		Confidence:      0.98,
	}

	Optimize(context.Background(), &pred)

	foundOvercommit := false
	for _, rec := range pred.Recommendations {
		if strings.Contains(rec, "overcommit_memory") {
			foundOvercommit = true
			break
		}
	}

	if !foundOvercommit {
		t.Error("Expected Redis cache recommendation to include overcommit_memory parameters")
	}
}

