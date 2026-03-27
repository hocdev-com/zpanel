package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func defaultDashboardSettings() map[string]bool {
	return map[string]bool{
		"apache": true,
		"php":    true,
		"mysql":  true,
	}
}

func (s *appState) appSettingsPath() string {
	return filepath.Join(s.appRoot, "data", "app-settings.json")
}

func normalizeDashboardSettingKey(key string) string {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "php:") {
		return key
	}

	parts := strings.Split(key, ":")
	if len(parts) >= 3 {
		version := strings.TrimSpace(parts[1])
		if version != "" {
			return "php:" + version
		}
	}
	return key
}

func (s *appState) migrateLegacyAppSettings() {
	content, err := os.ReadFile(s.appSettingsPath())
	if err != nil {
		return
	}

	var saved map[string]bool
	if err := json.Unmarshal(content, &saved); err != nil {
		return
	}

	settings := defaultDashboardSettings()
	for key, value := range saved {
		normalizedKey := normalizeDashboardSettingKey(key)
		if normalizedKey == "" {
			continue
		}
		settings[normalizedKey] = value
	}

	if err := s.saveAppSettings(settings); err == nil {
		_ = os.Remove(s.appSettingsPath())
	}
}

func (s *appState) loadAppSettings() map[string]bool {
	settings := defaultDashboardSettings()

	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM app_dashboard_settings`).Scan(&count); err == nil && count == 0 {
		s.migrateLegacyAppSettings()
	}

	rows, err := s.db.Query(`SELECT setting_key, enabled FROM app_dashboard_settings`)
	if err != nil {
		return settings
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var enabled bool
		if err := rows.Scan(&key, &enabled); err != nil {
			continue
		}
		normalizedKey := normalizeDashboardSettingKey(key)
		if normalizedKey == "" {
			continue
		}
		settings[normalizedKey] = enabled
	}

	_ = os.Remove(s.appSettingsPath())
	return settings
}

func (s *appState) saveAppSettings(settings map[string]bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM app_dashboard_settings`); err != nil {
		_ = tx.Rollback()
		return err
	}

	for key, enabled := range settings {
		normalizedKey := normalizeDashboardSettingKey(key)
		if normalizedKey == "" {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO app_dashboard_settings (setting_key, enabled) VALUES (?, ?)`,
			normalizedKey,
			enabled,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	_ = os.Remove(s.appSettingsPath())
	return nil
}
