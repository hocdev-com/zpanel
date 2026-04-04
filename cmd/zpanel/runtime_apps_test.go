package main

import "testing"

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
