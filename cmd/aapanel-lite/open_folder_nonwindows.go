//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

func openFolder(path string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", path).Start()
	}

	if _, err := exec.LookPath("xdg-open"); err != nil {
		return fmt.Errorf("cannot open folder automatically: %w", err)
	}
	return exec.Command("xdg-open", path).Start()
}
