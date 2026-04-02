package main

import (
	"os"
	"testing"

	embeddedassets "zpanel"
)

func TestEnsureBundledPanelDBSeedsMissingFile(t *testing.T) {
	baseDir := t.TempDir()

	dbPath, err := ensureBundledPanelDB(baseDir)
	if err != nil {
		t.Fatalf("ensureBundledPanelDB returned error: %v", err)
	}

	content, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("failed to read seeded db: %v", err)
	}

	want := embeddedassets.BundledPanelDB()
	if len(content) != len(want) {
		t.Fatalf("seeded db size mismatch: got %d, want %d", len(content), len(want))
	}
}
