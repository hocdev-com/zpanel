package main

import (
	"strings"
	"testing"
)

func TestRuntimeActionTargetAppendsVersionForVersionedApps(t *testing.T) {
	cases := []struct {
		appID   string
		version string
		want    string
	}{
		{appID: "mysql", version: "8.4.8", want: "mysql:8.4.8"},
		{appID: "phpmyadmin", version: "5.2.3", want: "phpmyadmin:5.2.3"},
		{appID: "php:8.3.30", version: "8.4.19", want: "php:8.3.30"},
		{appID: "stack", version: "1.0.0", want: "stack"},
		{appID: "apache", version: "", want: "apache"},
	}

	for _, tc := range cases {
		if got := runtimeActionTarget(tc.appID, tc.version); got != tc.want {
			t.Fatalf("runtimeActionTarget(%q, %q) = %q, want %q", tc.appID, tc.version, got, tc.want)
		}
	}
}

func TestStartOrGetInstallJobRejectsConflictingVersionWhileRunning(t *testing.T) {
	state := &appState{
		appJobs: map[string]*appInstallJob{},
	}
	existing := newAppInstallJob("apache", "2.4.66", "install")
	state.appJobs[appInstallJobKey("apache", "2.4.66")] = existing

	_, err := state.startOrGetInstallJob("apache", "2.4.65")
	if err == nil {
		t.Fatal("expected conflicting install to be rejected")
	}
	if !strings.Contains(err.Error(), "apache:2.4.66 is currently installing") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "apache:2.4.65") {
		t.Fatalf("expected requested version in error: %v", err)
	}
}

func TestStartOrGetInstallJobRejectsConcurrentPHPVersionInstall(t *testing.T) {
	state := &appState{
		appJobs: map[string]*appInstallJob{},
	}
	existing := newAppInstallJob("php:8.4.19", "8.4.19", "install")
	state.appJobs[appInstallJobKey("php:8.4.19", "8.4.19")] = existing

	if conflict := state.findConflictingInstallJobLocked("php:8.3.30", "8.3.30"); conflict != nil {
		t.Fatalf("expected php installs to stay allowed, got conflict: %+v", conflict.snapshot())
	}
}

func TestStartOrGetInstallJobReturnsExistingRunningJobForSameVersion(t *testing.T) {
	state := &appState{
		appJobs: map[string]*appInstallJob{},
	}
	existing := newAppInstallJob("apache", "2.4.66", "install")
	state.appJobs[appInstallJobKey("apache", "2.4.66")] = existing

	job, err := state.startOrGetInstallJob("apache", "2.4.66")
	if err != nil {
		t.Fatalf("expected same-version install to reuse job, got: %v", err)
	}
	if job != existing {
		t.Fatal("expected existing running job to be returned")
	}
}
