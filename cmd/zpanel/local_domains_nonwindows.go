//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const linuxHostsFile = "/etc/hosts"

func ensureLocalDomainMapping(domain string) error {
	content, err := os.ReadFile(linuxHostsFile)
	if err != nil {
		return fmt.Errorf("failed to read Linux hosts file: %w", err)
	}

	normalizedDomain := strings.ToLower(strings.TrimSpace(domain))
	lines := strings.Split(string(content), "\n")
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

	// Add new mapping
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	lines = append(lines, "127.0.0.1 "+normalizedDomain)

	updated := strings.Join(lines, "\n")
	if err := os.WriteFile(linuxHostsFile, []byte(updated), 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("cannot update /etc/hosts. Please run zPanel as root or with sudo to activate custom domains")
		}
		return fmt.Errorf("failed to update /etc/hosts: %w", err)
	}

	return nil
}

func removeLocalDomainMapping(domain string) error {
	content, err := os.ReadFile(linuxHostsFile)
	if err != nil {
		return fmt.Errorf("failed to read Linux hosts file: %w", err)
	}

	normalizedDomain := strings.ToLower(strings.TrimSpace(domain))
	lines := strings.Split(string(content), "\n")
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
			// Skip line if it only contained this domain
			continue
		}

		updatedLines = append(updatedLines, fields[0]+" "+strings.Join(filteredDomains, " "))
	}

	updated := strings.Join(updatedLines, "\n")
	if err := os.WriteFile(linuxHostsFile, []byte(updated), 0o644); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("cannot update /etc/hosts. Please run zPanel as root or with sudo to clean up custom domains")
		}
		return fmt.Errorf("failed to update /etc/hosts: %w", err)
	}

	return nil
}
