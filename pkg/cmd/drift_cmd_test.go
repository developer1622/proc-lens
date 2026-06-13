// Package cmd contains unit tests for commands in the cmd package.
//
// Note: This file contains unit tests for testing parsing and analysis functions in the drift command.
//
// Caveat: These tests create temporary files on disk to simulate scan logs and stability state files.
// verify that all temporary files are properly deleted at the end of each test to avoid polluting the workspace.
package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFingerprintsFromJSONL(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "drift-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	jsonlPath := filepath.Join(tempDir, "scan.jsonl")
	content := `{"event_type":"node_fingerprint","fingerprint":{"fingerprint_hash":"abc123hash","dominant_categories":[{"category":"RelationalDB","count":3,"percentage":100}],"workload_profile":"RelationalDB (dominant)","total_classified":3,"diversity_score":0},"node_context":{"timestamp":"2026-06-13T10:00:00Z"}}
{"event_type":"node_fingerprint","fingerprint":{"fingerprint_hash":"xyz456hash","dominant_categories":[{"category":"WebServer","count":2,"percentage":100}],"workload_profile":"WebServer (dominant)","total_classified":2,"diversity_score":0},"node_context":{"timestamp":"2026-06-13T10:05:00Z"}}
`
	if err := os.WriteFile(jsonlPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write scan.jsonl: %v", err)
	}

	results, err := parseFingerprintsFromJSONL(jsonlPath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 fingerprints, got %d", len(results))
	}

	if results[0].Fingerprint.Hash != "abc123hash" {
		t.Errorf("Expected first hash to be abc123hash, got %s", results[0].Fingerprint.Hash)
	}
	if results[1].Fingerprint.Hash != "xyz456hash" {
		t.Errorf("Expected second hash to be xyz456hash, got %s", results[1].Fingerprint.Hash)
	}

	expectedTime, _ := time.Parse(time.RFC3339, "2026-06-13T10:00:00Z")
	if !results[0].Timestamp.Equal(expectedTime) {
		t.Errorf("Expected first timestamp to be %v, got %v", expectedTime, results[0].Timestamp)
	}
}

func TestParseFingerprintsFromState(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "drift-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	statePath := filepath.Join(tempDir, "stability.json")
	content := `[
		{
			"fingerprint": {
				"fingerprint_hash": "statehash123",
				"dominant_categories": [{"category":"CacheStore","count":1,"percentage":100}],
				"workload_profile": "CacheStore (dominant)",
				"total_classified": 1,
				"diversity_score": 0
			},
			"timestamp": "2026-06-13T12:00:00Z"
		}
	]`
	if err := os.WriteFile(statePath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write stability.json: %v", err)
	}

	results, err := parseFingerprintsFromState(statePath)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 fingerprint, got %d", len(results))
	}

	if results[0].Fingerprint.Hash != "statehash123" {
		t.Errorf("Expected hash statehash123, got %s", results[0].Fingerprint.Hash)
	}
}

