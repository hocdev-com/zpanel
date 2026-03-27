//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func setHideWindow(cmd *exec.Cmd) {}

func acquirePlatformInstance() (bool, error) {
	return false, nil
}

func releasePlatformInstance() {}

func runPlatformShell(ctx context.Context, stop context.CancelFunc, controller *serverController, appRoot string, dashboardURL string) error {
	_ = appRoot
	fmt.Printf("aaPanel Lite started. Dashboard available at: %s\n", dashboardURL)
	fmt.Println("Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
	case <-sigChan:
		stop()
	}

	return controller.Stop()
}
