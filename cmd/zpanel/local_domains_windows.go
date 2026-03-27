//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const windowsHostsFile = `C:\Windows\System32\drivers\etc\hosts`

func ensureLocalDomainMapping(domain string) error {
	content, err := os.ReadFile(windowsHostsFile)
	if err != nil {
		return fmt.Errorf("failed to read Windows hosts file: %w", err)
	}

	normalizedDomain := strings.ToLower(strings.TrimSpace(domain))
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		for _, field := range fields[1:] {
			if strings.EqualFold(field, normalizedDomain) {
				return nil
			}
		}
	}

	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	lines = append(lines, "127.0.0.1 "+normalizedDomain)

	updated := strings.Join(lines, "\r\n")
	if err := os.WriteFile(windowsHostsFile, []byte(updated), 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("cannot update Windows hosts file. Please run aaPanel Lite as Administrator to activate custom domains like " + normalizedDomain)
		}
		return fmt.Errorf("failed to update Windows hosts file: %w", err)
	}

	return nil
}

func removeLocalDomainMapping(domain string) error {
	content, err := os.ReadFile(windowsHostsFile)
	if err != nil {
		return fmt.Errorf("failed to read Windows hosts file: %w", err)
	}

	normalizedDomain := strings.ToLower(strings.TrimSpace(domain))
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	updatedLines := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			updatedLines = append(updatedLines, line)
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			updatedLines = append(updatedLines, line)
			continue
		}

		filteredDomains := make([]string, 0, len(fields)-1)
		for _, field := range fields[1:] {
			if !strings.EqualFold(field, normalizedDomain) {
				filteredDomains = append(filteredDomains, field)
			}
		}

		if len(filteredDomains) == 0 {
			continue
		}

		updatedLines = append(updatedLines, fields[0]+" "+strings.Join(filteredDomains, " "))
	}

	updated := strings.Join(updatedLines, "\r\n")
	if err := os.WriteFile(windowsHostsFile, []byte(updated), 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("cannot update Windows hosts file. Please run aaPanel Lite as Administrator to clean up custom domains like " + normalizedDomain)
		}
		return fmt.Errorf("failed to update Windows hosts file: %w", err)
	}

	return nil
}
