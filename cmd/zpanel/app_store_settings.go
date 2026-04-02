package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type appStoreSettingsFile struct {
	Downloads       map[string]map[string]string `json:"downloads,omitempty"`
	Titles          map[string]map[string]string `json:"titles,omitempty"`
	Instructions    map[string]map[string]string `json:"instructions,omitempty"`
	Icons           map[string]map[string]string `json:"icons,omitempty"`
	ShowOnDashboard map[string]map[string]bool   `json:"show_on_dashboard,omitempty"`
}

type appStoreSettingsEntry struct {
	Version             string `json:"version"`
	Title               string `json:"title"`
	DefaultTitle        string `json:"default_title"`
	Instructions        string `json:"instructions"`
	DefaultInstructions string `json:"default_instructions"`
	Icon                string `json:"icon"`
	URL                 string `json:"url"`
	DefaultURL          string `json:"default_url"`
	ShowOnDashboard     bool   `json:"show_on_dashboard"`
}

type appStoreSettingsGroup struct {
	ID       string                  `json:"id"`
	Name     string                  `json:"name"`
	Releases []appStoreSettingsEntry `json:"releases"`
}

type appStoreSettingsResponse struct {
	FilePath string                  `json:"file_path"`
	Groups   []appStoreSettingsGroup `json:"groups"`
	Message  string                  `json:"message,omitempty"`
}

type appStoreSettingsRequest struct {
	Downloads       map[string]map[string]string `json:"downloads"`
	Titles          map[string]map[string]string `json:"titles"`
	Instructions    map[string]map[string]string `json:"instructions"`
	Descriptions    map[string]map[string]string `json:"descriptions"`
	Icons           map[string]map[string]string `json:"icons"`
	ShowOnDashboard map[string]map[string]bool   `json:"show_on_dashboard"`
}

func ensureAppStoreSettingsTable(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS app_store_settings (
		app_id TEXT NOT NULL,
		version TEXT NOT NULL,
		download_url TEXT NOT NULL,
		PRIMARY KEY (app_id, version)
	)`); err != nil {
		return err
	}

	// Migration: add title, description and icon data if they don't exist
	var hasTitle, hasDescription, hasIconData bool
	rows, err := db.Query("PRAGMA table_info(app_store_settings)")
	if err == nil {
		for rows.Next() {
			var cid int
			var name, dtype string
			var notnull, pk int
			var dfltValue interface{}
			if err := rows.Scan(&cid, &name, &dtype, &notnull, &dfltValue, &pk); err == nil {
				if name == "title" {
					hasTitle = true
				}
				if name == "description" {
					hasDescription = true
				}
				if name == "icon_data" {
					hasIconData = true
				}
			}
		}
		rows.Close()
	}

	if !hasTitle {
		if _, err := db.Exec(`ALTER TABLE app_store_settings ADD COLUMN title TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !hasDescription {
		if _, err := db.Exec(`ALTER TABLE app_store_settings ADD COLUMN description TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if !hasIconData {
		if _, err := db.Exec(`ALTER TABLE app_store_settings ADD COLUMN icon_data TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	var hasShowOnDashboard bool
	rows, err = db.Query("PRAGMA table_info(app_store_settings)")
	if err == nil {
		for rows.Next() {
			var cid int
			var name, dtype string
			var notnull, pk int
			var dfltValue interface{}
			if err := rows.Scan(&cid, &name, &dtype, &notnull, &dfltValue, &pk); err == nil {
				if name == "show_on_dashboard" {
					hasShowOnDashboard = true
				}
			}
		}
		rows.Close()
	}

	if !hasShowOnDashboard {
		if _, err := db.Exec(`ALTER TABLE app_store_settings ADD COLUMN show_on_dashboard INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	return nil
}

func appStoreSettingsDBPath(projectRoot string) string {
	cfg, err := loadConfig(filepath.Join(projectRoot, "config.toml"))
	if err != nil {
		return filepath.Join(projectRoot, "data", "panel.db")
	}
	return filepath.Join(resolveAppPath(projectRoot, cfg.BaseDir), "panel.db")
}

// Removed hardcoded appIDName helper

func defaultAppStoreReleaseTitle(appID string, version string) string {
	return strings.TrimSpace(strings.Title(appID) + " " + version)
}

func defaultAppStoreReleaseInstructions(appID string, version string) string {
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "apache":
		return "Portable web server stored in data/runtime."
	case "php":
		return "Portable PHP runtime. Multiple versions supported per website."
	case "mysql":
		return "Portable MySQL server stored in data/runtime."
	default:
		return fmt.Sprintf("Instructions for %s %s.", strings.Title(appID), version)
	}
}

func openAppStoreSettingsDB(projectRoot string) (*sql.DB, error) {
	dbPath := appStoreSettingsDBPath(projectRoot)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}

	dsn := dbPath + "?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := ensureAppStoreSettingsTable(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return db, nil
}

func normalizeAppStoreSettings(req appStoreSettingsRequest) (appStoreSettingsFile, error) {
	catalog := appStoreReleaseCatalog()
	settings := appStoreSettingsFile{
		Downloads:       map[string]map[string]string{},
		Titles:          map[string]map[string]string{},
		Instructions:    map[string]map[string]string{},
		Icons:           map[string]map[string]string{},
		ShowOnDashboard: map[string]map[string]bool{},
	}

	for appID, baseReleases := range catalog {
		validVersions := make(map[string]struct{}, len(baseReleases))
		defaultURLs := make(map[string]string, len(baseReleases))
		defaultTitles := make(map[string]string, len(baseReleases))
		defaultInstructions := make(map[string]string, len(baseReleases))

		for _, release := range baseReleases {
			validVersions[release.Version] = struct{}{}
			defaultURLs[release.Version] = strings.TrimSpace(release.URL)
			defaultTitles[release.Version] = defaultAppStoreReleaseTitle(appID, release.Version)
			defaultInstructions[release.Version] = defaultAppStoreReleaseInstructions(appID, release.Version)
		}

		for version := range validVersions {
			downloadURL := strings.TrimSpace(valueAt(req.Downloads, appID, version))
			title := strings.TrimSpace(valueAt(req.Titles, appID, version))
			instructions := strings.TrimSpace(valueAt(req.Instructions, appID, version))
			if instructions == "" {
				instructions = strings.TrimSpace(valueAt(req.Descriptions, appID, version))
			}
			showOnDashboard := valueAtBool(req.ShowOnDashboard, appID, version)

			if downloadURL != "" && downloadURL != defaultURLs[version] {
				parsedURL, err := url.ParseRequestURI(downloadURL)
				if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
					return appStoreSettingsFile{}, fmt.Errorf("invalid download URL for %s %s", strings.Title(appID), version)
				}
				if settings.Downloads[appID] == nil {
					settings.Downloads[appID] = map[string]string{}
				}
				settings.Downloads[appID][version] = downloadURL
			}

			if title != "" && title != defaultTitles[version] {
				if settings.Titles[appID] == nil {
					settings.Titles[appID] = map[string]string{}
				}
				settings.Titles[appID][version] = title
			}

			if instructions != "" && instructions != defaultInstructions[version] {
				if settings.Instructions[appID] == nil {
					settings.Instructions[appID] = map[string]string{}
				}
				settings.Instructions[appID][version] = instructions
			}

			icon := strings.TrimSpace(valueAt(req.Icons, appID, version))
			if icon != "" {
				if !strings.HasPrefix(icon, "data:image/") {
					return appStoreSettingsFile{}, fmt.Errorf("invalid icon data for %s %s", strings.Title(appID), version)
				}
				if settings.Icons[appID] == nil {
					settings.Icons[appID] = map[string]string{}
				}
				settings.Icons[appID][version] = icon
			}

			if showOnDashboard {
				if settings.ShowOnDashboard[appID] == nil {
					settings.ShowOnDashboard[appID] = map[string]bool{}
				}
				settings.ShowOnDashboard[appID][version] = true
			}
		}
	}

	return settings, nil
}

func valueAt(values map[string]map[string]string, appID string, version string) string {
	if values == nil || values[appID] == nil {
		return ""
	}
	return values[appID][version]
}

func valueAtBool(values map[string]map[string]bool, appID string, version string) bool {
	if values == nil || values[appID] == nil {
		return false
	}
	return values[appID][version]
}

// Removed legacy migration function

func loadAppStoreSettingsFromDB(projectRoot string) appStoreSettingsFile {
	settings := appStoreSettingsFile{
		Downloads:       map[string]map[string]string{},
		Titles:          map[string]map[string]string{},
		Instructions:    map[string]map[string]string{},
		Icons:           map[string]map[string]string{},
		ShowOnDashboard: map[string]map[string]bool{},
	}

	db, err := openAppStoreSettingsDB(projectRoot)
	if err != nil {
		return settings
	}
	defer db.Close()

	// Cleanup: migrateLegacyAppStoreSettings removal

	rows, err := db.Query(`SELECT app_id, version, download_url, title, description, icon_data, show_on_dashboard FROM app_store_settings`)
	if err != nil {
		return settings
	}
	defer rows.Close()

	for rows.Next() {
		var appID, version, downloadURL, title, description, iconData string
		var showOnDashboard int
		if err := rows.Scan(&appID, &version, &downloadURL, &title, &description, &iconData, &showOnDashboard); err != nil {
			continue
		}

		if strings.TrimSpace(downloadURL) != "" {
			if settings.Downloads[appID] == nil {
				settings.Downloads[appID] = map[string]string{}
			}
			settings.Downloads[appID][version] = downloadURL
		}
		if strings.TrimSpace(title) != "" {
			if settings.Titles[appID] == nil {
				settings.Titles[appID] = map[string]string{}
			}
			settings.Titles[appID][version] = title
		}
		if strings.TrimSpace(description) != "" {
			if settings.Instructions[appID] == nil {
				settings.Instructions[appID] = map[string]string{}
			}
			settings.Instructions[appID][version] = description
		}
		if strings.TrimSpace(iconData) != "" {
			if settings.Icons[appID] == nil {
				settings.Icons[appID] = map[string]string{}
			}
			settings.Icons[appID][version] = iconData
		}
		if showOnDashboard != 0 {
			if settings.ShowOnDashboard[appID] == nil {
				settings.ShowOnDashboard[appID] = map[string]bool{}
			}
			settings.ShowOnDashboard[appID][version] = true
		}
	}

	return settings
}

func saveAppStoreSettingsWithDB(db *sql.DB, settings appStoreSettingsFile) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM app_store_settings`); err != nil {
		_ = tx.Rollback()
		return err
	}

	catalog := appStoreReleaseCatalog()
	for appID, releases := range catalog {
		for _, release := range releases {
			downloadURL := strings.TrimSpace(valueAt(settings.Downloads, appID, release.Version))
			title := strings.TrimSpace(valueAt(settings.Titles, appID, release.Version))
			description := strings.TrimSpace(valueAt(settings.Instructions, appID, release.Version))
			iconData := strings.TrimSpace(valueAt(settings.Icons, appID, release.Version))
			showOnDashboard := 0
			if settings.ShowOnDashboard != nil && settings.ShowOnDashboard[appID] != nil && settings.ShowOnDashboard[appID][release.Version] {
				showOnDashboard = 1
			}

			if downloadURL == "" && title == "" && description == "" && iconData == "" && showOnDashboard == 0 {
				continue
			}
			if _, err := tx.Exec(
				`INSERT INTO app_store_settings (app_id, version, download_url, title, description, icon_data, show_on_dashboard) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				appID,
				release.Version,
				downloadURL,
				title,
				description,
				iconData,
				showOnDashboard,
			); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}

	return tx.Commit()
}

func saveAppStoreSettingsToDB(projectRoot string, settings appStoreSettingsFile) error {
	db, err := openAppStoreSettingsDB(projectRoot)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := saveAppStoreSettingsWithDB(db, settings); err != nil {
		return err
	}

	return nil
}

func appStoreEffectiveReleases(projectRoot string, appID string) []runtimeRelease {
	catalog := appStoreReleaseCatalog()
	base := catalog[appID]
	out := make([]runtimeRelease, len(base))
	copy(out, base)

	overrides := loadAppStoreSettingsFromDB(projectRoot).Downloads[appID]
	for i := range out {
		if overrideURL := strings.TrimSpace(overrides[out[i].Version]); overrideURL != "" {
			out[i].URL = overrideURL
		}
	}

	return out
}

func buildAppStoreSettingsResponse(projectRoot string, message string) appStoreSettingsResponse {
	groupOrder := []string{"apache", "php", "mysql"}
	groups := make([]appStoreSettingsGroup, 0, len(groupOrder))
	catalog := appStoreReleaseCatalog()
	saved := loadAppStoreSettingsFromDB(projectRoot)

	for _, appID := range groupOrder {
		baseReleases := catalog[appID]
		effectiveReleases := appStoreEffectiveReleases(projectRoot, appID)
		if len(baseReleases) == 0 {
			continue
		}

		groupName := strings.Title(appID)
		if len(baseReleases) > 0 {
			if t := strings.TrimSpace(valueAt(saved.Titles, appID, baseReleases[0].Version)); t != "" {
				groupName = t
			}
		}

		group := appStoreSettingsGroup{
			ID:       appID,
			Name:     groupName,
			Releases: make([]appStoreSettingsEntry, 0, len(baseReleases)),
		}

		for i := range baseReleases {
			version := baseReleases[i].Version
			group.Releases = append(group.Releases, appStoreSettingsEntry{
				Version:             version,
				Title:               strings.TrimSpace(valueAt(saved.Titles, appID, version)),
				DefaultTitle:        defaultAppStoreReleaseTitle(appID, version),
				Instructions:        strings.TrimSpace(valueAt(saved.Instructions, appID, version)),
				DefaultInstructions: defaultAppStoreReleaseInstructions(appID, version),
				Icon:                strings.TrimSpace(valueAt(saved.Icons, appID, version)),
				URL:                 effectiveReleases[i].URL,
				DefaultURL:          baseReleases[i].URL,
				ShowOnDashboard:     valueAtBool(saved.ShowOnDashboard, appID, version),
			})
		}

		groups = append(groups, group)
	}

	return appStoreSettingsResponse{
		FilePath: appStoreSettingsDBPath(projectRoot),
		Groups:   groups,
		Message:  message,
	}
}

func (s *appState) handleAppStoreSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, buildAppStoreSettingsResponse(s.appRoot, ""))
	case http.MethodPost:
		var req appStoreSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		settings, err := normalizeAppStoreSettings(req)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := saveAppStoreSettingsToDB(s.appRoot, settings); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, buildAppStoreSettingsResponse(s.appRoot, "App Store settings saved to panel.db."))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
