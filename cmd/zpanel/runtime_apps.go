package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type runtimeApp struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Version           string   `json:"version"`
	SelectedVersion   string   `json:"selected_version,omitempty"`
	AvailableVersions []string `json:"available_versions,omitempty"`
	InstalledVersions []string `json:"installed_versions,omitempty"`
	RunningVersions   []string `json:"running_versions,omitempty"`
	Description       string          `json:"description"`
	Developer         string          `json:"developer"`
	InstallPath       string          `json:"install_path,omitempty"`
	ShowOnDashboard   bool            `json:"show_on_dashboard"`
	ShowOnDashboardVersions map[string]bool `json:"show_on_dashboard_versions,omitempty"`
	Status            string          `json:"status"`
	StatusLabel       string   `json:"status_label"`
	Installed         bool     `json:"installed"`
	Running           bool     `json:"running"`
	Port              string   `json:"port,omitempty"`
	Progress          int      `json:"progress,omitempty"`
	URL               string            `json:"url,omitempty"`
	DownloadURL       string            `json:"download_url,omitempty"`
	DownloadURLs      map[string]string `json:"download_urls,omitempty"`
	CanInstall        bool              `json:"can_install"`
	CanStart          bool              `json:"can_start"`
	CanStop           bool              `json:"can_stop"`
	CanOpen           bool              `json:"can_open"`
	CanUninstall      bool              `json:"can_uninstall"`
}

type runtimeAppsResponse struct {
	Apps    []runtimeApp `json:"apps"`
	Message string       `json:"message,omitempty"`
}

type appActionRequest struct {
	ID      string `json:"id"`
	Action  string `json:"action"`
	Version string `json:"version,omitempty"`
}

type appInstallJob struct {
	mu       sync.RWMutex
	AppID    string       `json:"app_id"`
	Version  string       `json:"version,omitempty"`
	Action   string       `json:"action"`
	Status   string       `json:"status"`
	Progress int          `json:"progress"`
	Message  string       `json:"message"`
	Error    string       `json:"error,omitempty"`
	Apps     []runtimeApp `json:"apps,omitempty"`
}

type appInstallJobSnapshot struct {
	AppID    string       `json:"app_id"`
	Version  string       `json:"version,omitempty"`
	Action   string       `json:"action"`
	Status   string       `json:"status"`
	Progress int          `json:"progress"`
	Message  string       `json:"message"`
	Error    string       `json:"error,omitempty"`
	Apps     []runtimeApp `json:"apps,omitempty"`
}

type appProgressEvent struct {
	Percent int    `json:"percent"`
	Message string `json:"message"`
}

type runtimeManager interface {
	Status() (runtimeAppsResponse, error)
	Install(appID string, version string, onProgress func(appProgressEvent)) (runtimeAppsResponse, error)
	Start(appID string) (runtimeAppsResponse, error)
	Stop(appID string) (runtimeAppsResponse, error)
	Uninstall(appID string) (runtimeAppsResponse, error)
	StopAll() error
}

type runtimeStartupChecker interface {
	RunStartupChecks() error
}

func newAppInstallJob(appID string, version string, action string) *appInstallJob {
	return &appInstallJob{
		AppID:    appID,
		Version:  version,
		Action:   action,
		Status:   "running",
		Progress: 2,
		Message:  "Preparing install...",
	}
}

func (j *appInstallJob) snapshot() appInstallJobSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()

	return appInstallJobSnapshot{
		AppID:    j.AppID,
		Version:  j.Version,
		Action:   j.Action,
		Status:   j.Status,
		Progress: j.Progress,
		Message:  j.Message,
		Error:    j.Error,
		Apps:     append([]runtimeApp(nil), j.Apps...),
	}
}

func (j *appInstallJob) setProgress(progress int, message string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	if progress < j.Progress {
		progress = j.Progress
	}
	j.Progress = progress
	if message != "" {
		j.Message = message
	}
}

func (j *appInstallJob) complete(response runtimeAppsResponse) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.Status = "completed"
	j.Progress = 100
	j.Message = response.Message
	j.Error = ""
	j.Apps = response.Apps
}

func (j *appInstallJob) fail(message string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.Status = "failed"
	j.Error = message
	if j.Message == "" {
		j.Message = "Install failed."
	}
	if message != "" {
		j.Message = message
	}
}

func (s *appState) handleListApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	response, err := s.runRuntimeAppsAction("status", "stack", "", nil)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.Apps = s.injectAppSettings(response.Apps)
	response.Apps = s.injectJobStatus(response.Apps)
	writeJSON(w, http.StatusOK, response)
}

func (s *appState) handleAppAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req appActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	action := strings.ToLower(strings.TrimSpace(req.Action))
	appID := strings.ToLower(strings.TrimSpace(req.ID))
	version := strings.TrimSpace(req.Version)
	if action == "" || appID == "" {
		writeJSONError(w, http.StatusBadRequest, "id and action are required")
		return
	}

	switch action {
	case "install", "start", "stop", "uninstall":
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported action")
		return
	}

	baseID := appID
	if strings.Contains(appID, ":") {
		baseID = strings.Split(appID, ":")[0]
	}

	switch baseID {
	case "apache", "php", "mysql", "stack":
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported app id: "+appID)
		return
	}

	if action == "install" {
		job := s.startOrGetInstallJob(appID, version)
		writeJSON(w, http.StatusOK, job.snapshot())
		return
	}

	response, err := s.runRuntimeAppsAction(action, appID, "", nil)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *appState) handleAppProgress(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	appID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("id")))
	version := strings.TrimSpace(r.URL.Query().Get("version"))
	if appID == "" {
		writeJSONError(w, http.StatusBadRequest, "id is required")
		return
	}

	job := s.getInstallJob(appID, version)
	if job == nil {
		writeJSONError(w, http.StatusNotFound, "no install job found")
		return
	}

	writeJSON(w, http.StatusOK, job.snapshot())
}

func (s *appState) runRuntimeAppsAction(action string, appID string, version string, onProgress func(appProgressEvent)) (runtimeAppsResponse, error) {
	manager := newRuntimeManager(s.appRoot)
	switch action {
	case "status":
		return manager.Status()
	case "install":
		s.appStoreMu.Lock()
		defer s.appStoreMu.Unlock()
		return manager.Install(appID, version, onProgress)
	case "start":
		s.appStoreMu.Lock()
		defer s.appStoreMu.Unlock()
		return manager.Start(appID)
	case "stop":
		s.appStoreMu.Lock()
		defer s.appStoreMu.Unlock()
		return manager.Stop(appID)
	case "uninstall":
		s.appStoreMu.Lock()
		defer s.appStoreMu.Unlock()
		return manager.Uninstall(appID)
	default:
		return runtimeAppsResponse{}, errors.New("unsupported action")
	}
}

func (s *appState) startOrGetInstallJob(appID string, version string) *appInstallJob {
	s.appJobsMu.Lock()
	defer s.appJobsMu.Unlock()

	jobKey := appID
	if appID == "php" && version != "" {
		jobKey = appID + ":" + version
	}

	if existing := s.appJobs[jobKey]; existing != nil {
		snapshot := existing.snapshot()
		if snapshot.Status == "running" {
			return existing
		}
	}

	job := newAppInstallJob(appID, version, "install")
	s.appJobs[jobKey] = job

	go s.runInstallJob(job)
	return job
}

func (s *appState) getInstallJob(appID string, version string) *appInstallJob {
	s.appJobsMu.Lock()
	defer s.appJobsMu.Unlock()

	jobKey := appID
	if appID == "php" && version != "" {
		jobKey = appID + ":" + version
	}
	return s.appJobs[jobKey]
}

func (s *appState) runInstallJob(job *appInstallJob) {
	response, err := s.runRuntimeAppsAction("install", job.AppID, job.Version, func(event appProgressEvent) {
		job.setProgress(event.Percent, event.Message)
	})
	if err != nil {
		job.fail(err.Error())
		return
	}

	job.complete(response)
}

type runtimeRelease struct {
	Version  string
	URL      string
	FileName string
}

func newRuntimeApp(id string, name string, version string, availableVersions []string, description string, installPath string, installed bool, running bool, port string, url string, downloadURLs map[string]string, canStart bool, canStop bool, canOpen bool, canUninstall bool) runtimeApp {
	status := "not-installed"
	statusLabel := "Not installed"
	if installed && running {
		status = "running"
		statusLabel = "Running"
	} else if installed {
		status = "stopped"
		statusLabel = "Installed"
	}

	if id == "php" && installed && running {
		statusLabel = "Running"
	} else if id == "php" && installed {
		statusLabel = "Installed"
	}

	downloadURL := ""
	if v, ok := downloadURLs[version]; ok {
		downloadURL = v
	}

	return runtimeApp{
		ID:                id,
		Name:              name,
		Version:           version,
		SelectedVersion:   version,
		AvailableVersions: availableVersions,
		DownloadURL:       downloadURL,
		DownloadURLs:      downloadURLs,
		Description:       description,
		Developer:         "official",
		InstallPath:       installPath,
		Status:            status,
		StatusLabel:       statusLabel,
		Installed:         installed,
		Running:           running,
		Port:              port,
		URL:               url,
		CanInstall:        !installed,
		CanStart:          canStart,
		CanStop:           canStop,
		CanOpen:           canOpen,
		CanUninstall:      canUninstall,
	}
}

func reportProgress(onProgress func(appProgressEvent), percent int, message string) {
	if onProgress != nil {
		onProgress(appProgressEvent{Percent: percent, Message: message})
	}
}

func downloadFile(url string, outPath string, label string, startPercent int, endPercent int, onProgress func(appProgressEvent)) error {
	if fileExists(outPath) {
		reportProgress(onProgress, endPercent, "Already available.")
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}

	tempPath := outPath + ".part"
	resumeOffset := int64(0)
	if info, err := os.Stat(tempPath); err == nil {
		resumeOffset = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if resumeOffset > 0 {
		reportProgress(onProgress, startPercent, "Resuming...")
	} else {
		reportProgress(onProgress, startPercent, "Downloading...")
	}

	// Custom client with reasonable timeouts
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("network error: %w (check your connection)", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("failed to download %s: server returned %s", label, resp.Status)
	}

	fileFlags := os.O_CREATE | os.O_WRONLY
	switch {
	case resumeOffset > 0 && resp.StatusCode == http.StatusPartialContent:
		fileFlags |= os.O_APPEND
	case resumeOffset > 0 && resp.StatusCode == http.StatusOK:
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		resumeOffset = 0
		fileFlags |= os.O_TRUNC
	default:
		fileFlags |= os.O_TRUNC
	}

	file, err := os.OpenFile(tempPath, fileFlags, 0o644)
	if err != nil {
		return err
	}

	buf := make([]byte, 256*1024)
	written := resumeOffset
	totalSize := resp.ContentLength
	if totalSize > 0 {
		totalSize += resumeOffset
	}
	lastReported := startPercent
	if totalSize > 0 && written > 0 {
		ratio := float64(written) / float64(totalSize)
		lastReported = startPercent + int(float64(endPercent-startPercent)*ratio)
		if lastReported > endPercent {
			lastReported = endPercent
		}
		reportProgress(onProgress, lastReported, "Resuming...")
	}

	// Activity tracker: if no data for 60 seconds, abort
	timeout := 60 * time.Second

	for {
		// Set a deadline for the next read - Note: Body doesn't support SetReadDeadline directly generically
		// but we can simulate it with a timer to prevent infinite hang on Body.Read
		done := make(chan bool, 1)
		var n int
		var readErr error

		go func() {
			n, readErr = resp.Body.Read(buf)
			done <- true
		}()

		select {
		case <-done:
			// Read completed
		case <-time.After(timeout):
			return fmt.Errorf("download stalled: no data received for %v", timeout)
		}

		if n > 0 {
			if _, err := file.Write(buf[:n]); err != nil {
				return err
			}
			written += int64(n)
			if totalSize > 0 {
				ratio := float64(written) / float64(totalSize)
				progress := startPercent + int(float64(endPercent-startPercent)*ratio)
				if progress > endPercent {
					progress = endPercent
				}
				if progress > lastReported {
					lastReported = progress
					reportProgress(onProgress, progress, "Downloading...")
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return readErr
		}
	}

	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, outPath); err != nil {
		return err
	}

	reportProgress(onProgress, endPercent, "Downloaded.")
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveSingleChildDirectory(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return path, err
	}

	dirs := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, filepath.Join(path, entry.Name()))
		}
	}
	if len(dirs) == 1 {
		return dirs[0], nil
	}
	return path, nil
}
