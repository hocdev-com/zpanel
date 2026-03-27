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
	Downloads    map[string]map[string]string `json:"downloads,omitempty"`
	Titles       map[string]map[string]string `json:"titles,omitempty"`
	Descriptions map[string]map[string]string `json:"descriptions,omitempty"`
}

type appStoreSettingsEntry struct {
	Version            string `json:"version"`
	Title              string `json:"title"`
	DefaultTitle       string `json:"default_title"`
	Description        string `json:"description"`
	DefaultDescription string `json:"default_description"`
	URL                string `json:"url"`
	DefaultURL         string `json:"default_url"`
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
	Downloads    map[string]map[string]string `json:"downloads"`
	Titles       map[string]map[string]string `json:"titles"`
	Descriptions map[string]map[string]string `json:"descriptions"`
}

func ensureAppStoreSettingsTable(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS app_store_settings (
		app_id TEXT NOT NULL,
		version TEXT NOT NULL,
		download_url TEXT NOT NULL,
		title TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (app_id, version)
	)`); err != nil {
		return err
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

func legacyAppStoreSettingsPath(projectRoot string) string {
	return filepath.Join(projectRoot, "data", "app-store-settings.json")
}

func appStoreReleaseLabel(appID string) string {
	switch appID {
	case "apache":
		return "Apache HTTP Server"
	case "php":
		return "PHP Runtime"
	case "mysql":
		return "MySQL Community Server"
	default:
		return strings.ToUpper(appID)
	}
}

func defaultAppStoreReleaseTitle(appID string, version string) string {
	return strings.TrimSpace(appStoreReleaseLabel(appID) + " " + version)
}

func defaultAppStoreReleaseDescription(appID string, version string) string {
	return fmt.Sprintf("Download URL for %s version %s.", appStoreReleaseLabel(appID), version)
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
		Downloads:    map[string]map[string]string{},
		Titles:       map[string]map[string]string{},
		Descriptions: map[string]map[string]string{},
	}

	for appID, baseReleases := range catalog {
		validVersions := make(map[string]struct{}, len(baseReleases))
		defaultURLs := make(map[string]string, len(baseReleases))
		defaultTitles := make(map[string]string, len(baseReleases))
		defaultDescriptions := make(map[string]string, len(baseReleases))

		for _, release := range baseReleases {
			validVersions[release.Version] = struct{}{}
			defaultURLs[release.Version] = strings.TrimSpace(release.URL)
			defaultTitles[release.Version] = defaultAppStoreReleaseTitle(appID, release.Version)
			defaultDescriptions[release.Version] = defaultAppStoreReleaseDescription(appID, release.Version)
		}

		for version := range validVersions {
			downloadURL := strings.TrimSpace(valueAt(req.Downloads, appID, version))
			title := strings.TrimSpace(valueAt(req.Titles, appID, version))
			description := strings.TrimSpace(valueAt(req.Descriptions, appID, version))

			if downloadURL != "" && downloadURL != defaultURLs[version] {
				parsedURL, err := url.ParseRequestURI(downloadURL)
				if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
					return appStoreSettingsFile{}, fmt.Errorf("invalid download URL for %s %s", appStoreReleaseLabel(appID), version)
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

			if description != "" && description != defaultDescriptions[version] {
				if settings.Descriptions[appID] == nil {
					settings.Descriptions[appID] = map[string]string{}
				}
				settings.Descriptions[appID][version] = description
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

func migrateLegacyAppStoreSettings(projectRoot string, db *sql.DB) error {
	content, err := os.ReadFile(legacyAppStoreSettingsPath(projectRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var legacy appStoreSettingsFile
	if err := json.Unmarshal(content, &legacy); err != nil {
		return nil
	}

	settings := appStoreSettingsFile{
		Downloads:    legacy.Downloads,
		Titles:       legacy.Titles,
		Descriptions: legacy.Descriptions,
	}

	if len(settings.Downloads) == 0 && len(settings.Titles) == 0 && len(settings.Descriptions) == 0 {
		_ = os.Remove(legacyAppStoreSettingsPath(projectRoot))
		return nil
	}

	if err := saveAppStoreSettingsWithDB(db, settings); err != nil {
		return err
	}

	_ = os.Remove(legacyAppStoreSettingsPath(projectRoot))
	return nil
}

func loadAppStoreSettingsFromDB(projectRoot string) appStoreSettingsFile {
	settings := appStoreSettingsFile{
		Downloads:    map[string]map[string]string{},
		Titles:       map[string]map[string]string{},
		Descriptions: map[string]map[string]string{},
	}

	db, err := openAppStoreSettingsDB(projectRoot)
	if err != nil {
		return settings
	}
	defer db.Close()

	var rowCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM app_store_settings`).Scan(&rowCount); err == nil && rowCount == 0 {
		_ = migrateLegacyAppStoreSettings(projectRoot, db)
	}

	rows, err := db.Query(`SELECT app_id, version, download_url, title, description FROM app_store_settings`)
	if err != nil {
		return settings
	}
	defer rows.Close()

	for rows.Next() {
		var appID, version, downloadURL, title, description string
		if err := rows.Scan(&appID, &version, &downloadURL, &title, &description); err != nil {
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
			if settings.Descriptions[appID] == nil {
				settings.Descriptions[appID] = map[string]string{}
			}
			settings.Descriptions[appID][version] = description
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
			description := strings.TrimSpace(valueAt(settings.Descriptions, appID, release.Version))
			if downloadURL == "" && title == "" && description == "" {
				continue
			}
			if _, err := tx.Exec(
				`INSERT INTO app_store_settings (app_id, version, download_url, title, description) VALUES (?, ?, ?, ?, ?)`,
				appID,
				release.Version,
				downloadURL,
				title,
				description,
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

	_ = os.Remove(legacyAppStoreSettingsPath(projectRoot))
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

		group := appStoreSettingsGroup{
			ID:       appID,
			Name:     appStoreReleaseLabel(appID),
			Releases: make([]appStoreSettingsEntry, 0, len(baseReleases)),
		}

		for i := range baseReleases {
			version := baseReleases[i].Version
			group.Releases = append(group.Releases, appStoreSettingsEntry{
				Version:            version,
				Title:              strings.TrimSpace(valueAt(saved.Titles, appID, version)),
				DefaultTitle:       defaultAppStoreReleaseTitle(appID, version),
				Description:        strings.TrimSpace(valueAt(saved.Descriptions, appID, version)),
				DefaultDescription: defaultAppStoreReleaseDescription(appID, version),
				URL:                effectiveReleases[i].URL,
				DefaultURL:         baseReleases[i].URL,
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
