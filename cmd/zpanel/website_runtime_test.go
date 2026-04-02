package main

import "testing"

func TestResolveWebsiteDisplayStatus(t *testing.T) {
	tests := []struct {
		name          string
		configStatus  string
		runtimeStatus websiteRuntimeStatus
		wantStatus    string
		wantLabel     string
	}{
		{
			name:         "running when apache is up",
			configStatus: "running",
			runtimeStatus: websiteRuntimeStatus{
				ApacheInstalled: true,
				ApacheRunning:   true,
			},
			wantStatus: "running",
			wantLabel:  "Running",
		},
		{
			name:         "stopped when apache is down",
			configStatus: "running",
			runtimeStatus: websiteRuntimeStatus{
				ApacheInstalled: true,
				ApacheRunning:   false,
			},
			wantStatus: "stopped",
			wantLabel:  "Apache stopped",
		},
		{
			name:         "stopped when apache is missing",
			configStatus: "running",
			runtimeStatus: websiteRuntimeStatus{
				ApacheInstalled: false,
				ApacheRunning:   false,
			},
			wantStatus: "stopped",
			wantLabel:  "Apache not installed",
		},
		{
			name:         "manual stop remains stopped",
			configStatus: "stopped",
			runtimeStatus: websiteRuntimeStatus{
				ApacheInstalled: true,
				ApacheRunning:   true,
			},
			wantStatus: "stopped",
			wantLabel:  "Stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, gotLabel := resolveWebsiteDisplayStatus(tt.configStatus, tt.runtimeStatus)
			if gotStatus != tt.wantStatus || gotLabel != tt.wantLabel {
				t.Fatalf("resolveWebsiteDisplayStatus(%q) = (%q, %q), want (%q, %q)", tt.configStatus, gotStatus, gotLabel, tt.wantStatus, tt.wantLabel)
			}
		})
	}
}
