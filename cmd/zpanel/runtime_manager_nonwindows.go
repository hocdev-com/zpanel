//go:build !windows && !linux

package main

import "errors"

type unsupportedRuntimeManager struct{}

func newRuntimeManager(projectRoot string) runtimeManager {
	_ = projectRoot
	return &unsupportedRuntimeManager{}
}

func (m *unsupportedRuntimeManager) Status() (runtimeAppsResponse, error) {
	return runtimeAppsResponse{}, errors.New("app store is only supported on Windows")
}

func (m *unsupportedRuntimeManager) Install(appID string, version string, onProgress func(appProgressEvent)) (runtimeAppsResponse, error) {
	return runtimeAppsResponse{}, errors.New("app store is only supported on Windows")
}

func (m *unsupportedRuntimeManager) Start(appID string) (runtimeAppsResponse, error) {
	return runtimeAppsResponse{}, errors.New("app store is only supported on Windows")
}

func (m *unsupportedRuntimeManager) Stop(appID string) (runtimeAppsResponse, error) {
	return runtimeAppsResponse{}, errors.New("app store is only supported on Windows")
}

func (m *unsupportedRuntimeManager) Uninstall(appID string) (runtimeAppsResponse, error) {
	return runtimeAppsResponse{}, errors.New("app store is only supported on Windows")
}

func (m *unsupportedRuntimeManager) StopAll() error {
	return nil
}
