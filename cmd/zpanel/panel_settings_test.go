package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDefaultSiteRootExistsCreatesDefaultFolder(t *testing.T) {
	appRoot := t.TempDir()

	if err := ensureDefaultSiteRootExists(appRoot, defaultPanelSettings()); err != nil {
		t.Fatalf("ensure default site root exists: %v", err)
	}

	target := filepath.Join(appRoot, defaultSiteFolder)
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat created folder: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", target)
	}
}

func TestRootSiteFolderDisplaysAndResolvesAsSlash(t *testing.T) {
	appRoot := t.TempDir()

	settings, err := normalizePanelSettings(panelSettings{DefaultSiteFolder: "/"})
	if err != nil {
		t.Fatalf("normalize panel settings: %v", err)
	}
	if settings.DefaultSiteFolder != "/" {
		t.Fatalf("expected root marker '/', got %q", settings.DefaultSiteFolder)
	}

	resolved := resolveDefaultSiteRoot(appRoot, settings)
	if resolved != filepath.Clean(appRoot) {
		t.Fatalf("expected resolved root %q, got %q", filepath.Clean(appRoot), resolved)
	}

	display := displaySiteFolderValue(appRoot, appRoot)
	if display != "/" {
		t.Fatalf("expected display path '/', got %q", display)
	}
}
