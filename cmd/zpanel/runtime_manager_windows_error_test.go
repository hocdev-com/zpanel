//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFormatMySQLInitErrorIncludesLogTail(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	paths := manager.paths()
	if err := os.MkdirAll(paths.mysqlDataDir, 0o755); err != nil {
		t.Fatalf("mkdir mysql data dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(paths.mysqlDataDir, "mysqld.err"), []byte("InnoDB: initialization failed because datadir is invalid"), 0o644); err != nil {
		t.Fatalf("write mysqld.err: %v", err)
	}

	err := manager.formatMySQLInitError(errors.New("timed out after 2m0s"))
	if err == nil {
		t.Fatal("expected error")
	}
	message := err.Error()
	if !strings.Contains(message, "mysql initialization failed:") {
		t.Fatalf("unexpected message: %s", message)
	}
	if !strings.Contains(message, "timed out after 2m0s") {
		t.Fatalf("expected timeout detail in message: %s", message)
	}
	if !strings.Contains(message, "mysqld.err:") {
		t.Fatalf("expected mysqld.err detail in message: %s", message)
	}
	if !strings.Contains(message, "datadir is invalid") {
		t.Fatalf("expected log tail content in message: %s", message)
	}
}

func TestReadFileTailReturnsTrailingContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tail.log")

	if err := os.WriteFile(path, []byte("prefix line\nfinal line with useful error"), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	got := readFileTail(path, len("final line with useful error"))
	if !strings.Contains(got, "final line with useful error") {
		t.Fatalf("expected trailing content, got: %q", got)
	}
	if strings.Contains(got, "prefix line") {
		t.Fatalf("expected prefix to be trimmed, got: %q", got)
	}
}
