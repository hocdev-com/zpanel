package main

import (
	"testing"
	"time"
)

func TestRuntimeTargetsForAppID(t *testing.T) {
	tests := map[string][]string{
		"apache":       {"apache"},
		"php:8.4.19":   {"php"},
		"mysql":        {"mysql"},
		"stack":        {"apache", "mysql", "php"},
		"unknown":      nil,
		"STACK":        {"apache", "mysql", "php"},
		" apache:foo ": {"apache"},
	}

	for input, expected := range tests {
		got := runtimeTargetsForAppID(input)
		if len(got) != len(expected) {
			t.Fatalf("runtimeTargetsForAppID(%q) length mismatch: got %v want %v", input, got, expected)
		}
		for i := range got {
			if got[i] != expected[i] {
				t.Fatalf("runtimeTargetsForAppID(%q) = %v, want %v", input, got, expected)
			}
		}
	}
}

func TestLockRuntimeTargetsAllowsDifferentAppsInParallel(t *testing.T) {
	state := &appState{}
	releaseApache := state.lockRuntimeTargets("apache")
	defer releaseApache()

	acquired := make(chan struct{})
	go func() {
		releasePHP := state.lockRuntimeTargets("php")
		close(acquired)
		releasePHP()
	}()

	select {
	case <-acquired:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected php lock acquisition to proceed while apache is locked")
	}
}

func TestLockRuntimeTargetsSerializesSameRuntime(t *testing.T) {
	state := &appState{}
	releasePHP := state.lockRuntimeTargets("php")

	acquired := make(chan struct{})
	go func() {
		releaseOther := state.lockRuntimeTargets("php:8.4.19", "php")
		close(acquired)
		releaseOther()
	}()

	select {
	case <-acquired:
		t.Fatal("expected php lock acquisition to wait for the existing php lock")
	case <-time.After(150 * time.Millisecond):
	}

	releasePHP()

	select {
	case <-acquired:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("expected waiting php lock acquisition to continue after release")
	}
}
