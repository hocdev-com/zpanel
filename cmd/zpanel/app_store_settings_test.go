package main

import "testing"

func TestNormalizeAppStoreSettingsAcceptsCustomRelease(t *testing.T) {
	req := appStoreSettingsRequest{
		Downloads: map[string]map[string]string{
			"php": {
				"8.4.20": "https://example.com/php-8.4.20.zip",
			},
		},
		Titles: map[string]map[string]string{
			"php": {
				"8.4.20": "PHP 8.4.20 Custom",
			},
		},
		Instructions: map[string]map[string]string{
			"php": {
				"8.4.20": "Custom PHP build",
			},
		},
	}

	settings, err := normalizeAppStoreSettings(req)
	if err != nil {
		t.Fatalf("normalize settings: %v", err)
	}

	if got := valueAt(settings.Downloads, "php", "8.4.20"); got != "https://example.com/php-8.4.20.zip" {
		t.Fatalf("unexpected custom download url: %q", got)
	}
	if got := valueAt(settings.Titles, "php", "8.4.20"); got != "PHP 8.4.20 Custom" {
		t.Fatalf("unexpected custom title: %q", got)
	}
	if got := valueAt(settings.Instructions, "php", "8.4.20"); got != "Custom PHP build" {
		t.Fatalf("unexpected custom instructions: %q", got)
	}
}

func TestBuildAppStoreSettingsResponseIncludesCustomRelease(t *testing.T) {
	root := t.TempDir()
	settings := appStoreSettingsFile{
		Downloads: map[string]map[string]string{
			"php": {
				"8.4.20": "https://example.com/php-8.4.20.zip",
			},
		},
		Titles: map[string]map[string]string{
			"php": {
				"8.4.20": "PHP 8.4.20 Custom",
			},
		},
	}

	if err := saveAppStoreSettingsToDB(root, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	response := buildAppStoreSettingsResponse(root, "")
	found := false
	for _, group := range response.Groups {
		if group.ID != "php" {
			continue
		}
		for _, release := range group.Releases {
			if release.Version != "8.4.20" {
				continue
			}
			found = true
			if !release.IsCustom {
				t.Fatal("expected custom release to be marked as custom")
			}
			if release.URL != "https://example.com/php-8.4.20.zip" {
				t.Fatalf("unexpected custom release url: %q", release.URL)
			}
			if release.Title != "PHP 8.4.20 Custom" {
				t.Fatalf("unexpected custom release title: %q", release.Title)
			}
		}
	}

	if !found {
		t.Fatal("expected custom php release to appear in response")
	}
}

func TestAppStoreEffectiveReleasesIncludesCustomRelease(t *testing.T) {
	root := t.TempDir()
	settings := appStoreSettingsFile{
		Downloads: map[string]map[string]string{
			"apache": {
				"2.4.67": "https://example.com/httpd-2.4.67-win64.zip",
			},
		},
	}

	if err := saveAppStoreSettingsToDB(root, settings); err != nil {
		t.Fatalf("save settings: %v", err)
	}

	releases := appStoreEffectiveReleases(root, "apache")
	for _, release := range releases {
		if release.Version != "2.4.67" {
			continue
		}
		if release.URL != "https://example.com/httpd-2.4.67-win64.zip" {
			t.Fatalf("unexpected custom release url: %q", release.URL)
		}
		if release.FileName != "httpd-2.4.67-win64.zip" {
			t.Fatalf("unexpected custom release file name: %q", release.FileName)
		}
		return
	}

	t.Fatal("expected custom apache release in effective releases")
}

func TestBuildAppStoreSettingsResponseIncludesEmptyDatabaseAndOtherGroups(t *testing.T) {
	root := t.TempDir()
	response := buildAppStoreSettingsResponse(root, "")

	groupSet := map[string]bool{}
	for _, group := range response.Groups {
		groupSet[group.ID] = true
	}

	if !groupSet["database"] {
		t.Fatal("expected database group in app store settings response")
	}
	if !groupSet["other"] {
		t.Fatal("expected other group in app store settings response")
	}
}

func TestAppendSyntheticAppStoreAppsAddsConfiguredCustomGroup(t *testing.T) {
	settings := appStoreSettingsFile{
		Downloads: map[string]map[string]string{
			"other": {
				"1.0.0": "https://example.com/other-1.0.0.zip",
			},
		},
	}

	apps := appendSyntheticAppStoreApps(nil, settings)
	if len(apps) != 1 {
		t.Fatalf("expected one synthetic app, got %d", len(apps))
	}
	if apps[0].ID != "other" {
		t.Fatalf("expected synthetic app id other, got %q", apps[0].ID)
	}
	if apps[0].CanInstall {
		t.Fatal("expected synthetic app to be non-installable by default")
	}
	if len(apps[0].AvailableVersions) != 1 || apps[0].AvailableVersions[0] != "1.0.0" {
		t.Fatalf("unexpected synthetic versions: %#v", apps[0].AvailableVersions)
	}
}
