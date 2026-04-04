package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type runtimeApp struct {
	ID                      string            `json:"id"`
	Name                    string            `json:"name"`
	Version                 string            `json:"version"`
	SelectedVersion         string            `json:"selected_version,omitempty"`
	AvailableVersions       []string          `json:"available_versions,omitempty"`
	VersionTitles           map[string]string `json:"version_titles,omitempty"`
	VersionInstructions     map[string]string `json:"version_instructions,omitempty"`
	VersionIcons            map[string]string `json:"version_icons,omitempty"`
	InstalledVersions       []string          `json:"installed_versions,omitempty"`
	RunningVersions         []string          `json:"running_versions,omitempty"`
	Description             string            `json:"description"`
	Icon                    string            `json:"icon,omitempty"`
	Developer               string            `json:"developer"`
	InstallPath             string            `json:"install_path,omitempty"`
	ShowOnDashboard         bool              `json:"show_on_dashboard"`
	ShowOnDashboardVersions map[string]bool   `json:"show_on_dashboard_versions,omitempty"`
	Status                  string            `json:"status"`
	StatusLabel             string            `json:"status_label"`
	Installed               bool              `json:"installed"`
	Running                 bool              `json:"running"`
	Port                    string            `json:"port,omitempty"`
	Progress                int               `json:"progress,omitempty"`
	URL                     string            `json:"url,omitempty"`
	DownloadURL             string            `json:"download_url,omitempty"`
	DownloadURLs            map[string]string `json:"download_urls,omitempty"`
	CanInstall              bool              `json:"can_install"`
	CanStart                bool              `json:"can_start"`
	CanStop                 bool              `json:"can_stop"`
	CanOpen                 bool              `json:"can_open"`
	CanUninstall            bool              `json:"can_uninstall"`
	Dependencies            []string          `json:"dependencies,omitempty"`
	MissingDependencies     []string          `json:"missing_dependencies,omitempty"`
	DependencyMessage       string            `json:"dependency_message,omitempty"`
	CompatibilityMessage    string            `json:"compatibility_message,omitempty"`
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

type runtimeDependencyError struct {
	AppID               string
	Message             string
	MissingDependencies []string
}

func (e *runtimeDependencyError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
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

func appInstallJobKey(appID string, version string) string {
	appID = strings.ToLower(strings.TrimSpace(appID))
	version = strings.TrimSpace(version)
	if appID == "" {
		return ""
	}
	if strings.Contains(appID, ":") {
		parts := strings.SplitN(appID, ":", 2)
		appID = parts[0]
		if version == "" && len(parts) == 2 {
			version = strings.TrimSpace(parts[1])
		}
	}
	if version == "" {
		return appID
	}
	return appID + ":" + version
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
	case "apache", "php", "mysql", "phpmyadmin", "stack":
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported app id: "+appID)
		return
	}

	if action == "install" {
		job := s.startOrGetInstallJob(appID, version)
		writeJSON(w, http.StatusOK, job.snapshot())
		return
	}

	response, err := s.runRuntimeAppsAction(action, runtimeActionTarget(appID, version), "", nil)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func runtimeActionTarget(appID string, version string) string {
	appID = strings.ToLower(strings.TrimSpace(appID))
	version = strings.TrimSpace(version)
	if appID == "" || version == "" || strings.Contains(appID, ":") {
		return appID
	}
	switch appID {
	case "apache", "php", "mysql", "phpmyadmin":
		return appID + ":" + version
	default:
		return appID
	}
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
		unlock := s.lockRuntimeTargets(runtimeTargetsForAppID(appID)...)
		defer unlock()
		return manager.Install(appID, version, onProgress)
	case "start":
		unlock := s.lockRuntimeTargets(runtimeTargetsForAppID(appID)...)
		defer unlock()
		return manager.Start(appID)
	case "stop":
		unlock := s.lockRuntimeTargets(runtimeTargetsForAppID(appID)...)
		defer unlock()
		return manager.Stop(appID)
	case "uninstall":
		unlock := s.lockRuntimeTargets(runtimeTargetsForAppID(appID)...)
		defer unlock()
		return manager.Uninstall(appID)
	default:
		return runtimeAppsResponse{}, errors.New("unsupported action")
	}
}

func (s *appState) startOrGetInstallJob(appID string, version string) *appInstallJob {
	s.appJobsMu.Lock()
	defer s.appJobsMu.Unlock()

	jobKey := appInstallJobKey(appID, version)

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

	jobKey := appInstallJobKey(appID, version)
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

func runtimeTargetsForAppID(appID string) []string {
	baseID := strings.ToLower(strings.TrimSpace(appID))
	if strings.Contains(baseID, ":") {
		baseID = strings.SplitN(baseID, ":", 2)[0]
	}

	switch baseID {
	case "stack":
		return []string{"apache", "mysql", "php"}
	case "phpmyadmin":
		return []string{"apache", "mysql", "php", "phpmyadmin"}
	case "apache", "php", "mysql":
		return []string{baseID}
	default:
		return nil
	}
}

func (s *appState) lockRuntimeTargets(targets ...string) func() {
	if len(targets) == 0 {
		return func() {}
	}

	seen := make(map[string]struct{}, len(targets))
	normalized := make([]string, 0, len(targets))
	for _, target := range targets {
		target = strings.ToLower(strings.TrimSpace(target))
		if strings.Contains(target, ":") {
			target = strings.SplitN(target, ":", 2)[0]
		}
		if target == "" {
			continue
		}
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		normalized = append(normalized, target)
	}
	sort.Strings(normalized)

	locks := make([]*sync.Mutex, 0, len(normalized))
	for _, target := range normalized {
		locks = append(locks, s.runtimeLock(target))
	}
	for _, lock := range locks {
		lock.Lock()
	}

	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

func (s *appState) runtimeLock(target string) *sync.Mutex {
	s.runtimeLocksMu.Lock()
	defer s.runtimeLocksMu.Unlock()

	if s.runtimeLocks == nil {
		s.runtimeLocks = map[string]*sync.Mutex{}
	}
	lock := s.runtimeLocks[target]
	if lock == nil {
		lock = &sync.Mutex{}
		s.runtimeLocks[target] = lock
	}
	return lock
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

const (
	downloadProbeTimeout    = 45 * time.Second
	downloadChunkTimeout    = 4 * time.Minute
	downloadChunkRetryLimit = 4
	downloadParallelMinSize = 8 << 20
	downloadParallelWorkers = 6
	downloadChunkSizeSmall  = 2 << 20
	downloadChunkSizeMedium = 4 << 20
	downloadChunkSizeLarge  = 8 << 20
)

type downloadProbeResult struct {
	SupportsRanges bool
	KnownSize      bool
	TotalSize      int64
}

type downloadByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type downloadResumeState struct {
	URL       string              `json:"url"`
	TotalSize int64               `json:"total_size"`
	Completed []downloadByteRange `json:"completed"`
}

type downloadProgressTracker struct {
	mu           sync.Mutex
	total        int64
	completed    int64
	startPercent int
	endPercent   int
	lastPercent  int
	onProgress   func(appProgressEvent)
}

func newDownloadProgressTracker(total int64, completed int64, startPercent int, endPercent int, onProgress func(appProgressEvent)) *downloadProgressTracker {
	tracker := &downloadProgressTracker{
		total:        total,
		completed:    completed,
		startPercent: startPercent,
		endPercent:   endPercent,
		lastPercent:  startPercent,
		onProgress:   onProgress,
	}
	if tracker.total > 0 && tracker.completed > 0 {
		tracker.lastPercent = tracker.percentLocked()
	}
	return tracker
}

func (t *downloadProgressTracker) percentLocked() int {
	if t.total <= 0 {
		return t.lastPercent
	}
	ratio := float64(t.completed) / float64(t.total)
	percent := t.startPercent + int(float64(t.endPercent-t.startPercent)*ratio)
	if percent > t.endPercent {
		percent = t.endPercent
	}
	if percent < t.startPercent {
		percent = t.startPercent
	}
	return percent
}

func (t *downloadProgressTracker) Report(message string) {
	if t.onProgress == nil {
		return
	}

	t.mu.Lock()
	percent := t.percentLocked()
	if percent > t.lastPercent {
		t.lastPercent = percent
	}
	emit := t.lastPercent
	t.mu.Unlock()

	reportProgress(t.onProgress, emit, message)
}

func (t *downloadProgressTracker) Add(delta int64, message string) {
	if delta <= 0 || t.onProgress == nil {
		return
	}

	t.mu.Lock()
	t.completed += delta
	percent := t.percentLocked()
	if percent <= t.lastPercent {
		t.mu.Unlock()
		return
	}
	t.lastPercent = percent
	t.mu.Unlock()

	reportProgress(t.onProgress, percent, message)
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
	metaPath := tempPath + ".meta"
	client := newDownloadHTTPClient()

	probe, err := probeDownload(client, url)
	if err != nil {
		return fmt.Errorf("failed to prepare download for %s: %w", label, err)
	}
	if probe.KnownSize && probe.TotalSize == 0 {
		if err := os.WriteFile(outPath, nil, 0o644); err != nil {
			return err
		}
		reportProgress(onProgress, endPercent, "Downloaded.")
		return nil
	}

	if probe.SupportsRanges && probe.KnownSize && probe.TotalSize >= downloadParallelMinSize {
		if err := downloadFileMultiPart(client, url, tempPath, metaPath, label, probe.TotalSize, startPercent, endPercent, onProgress); err != nil {
			return err
		}
	} else {
		if err := downloadFileSequential(client, url, tempPath, label, probe, startPercent, endPercent, onProgress); err != nil {
			return err
		}
	}

	if err := finalizeDownload(tempPath, outPath, metaPath); err != nil {
		return err
	}

	reportProgress(onProgress, endPercent, "Downloaded.")
	return nil
}

func newDownloadHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   8,
		},
	}
}

func probeDownload(client *http.Client, rawURL string) (downloadProbeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return downloadProbeResult{}, err
	}
	req.Header.Set("Range", "bytes=0-0")

	resp, err := client.Do(req)
	if err != nil {
		return downloadProbeResult{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusPartialContent:
		totalSize, known := parseContentRangeTotal(resp.Header.Get("Content-Range"))
		if !known && resp.ContentLength >= 0 {
			totalSize = resp.ContentLength
			known = true
		}
		return downloadProbeResult{
			SupportsRanges: true,
			KnownSize:      known,
			TotalSize:      totalSize,
		}, nil
	case http.StatusRequestedRangeNotSatisfiable:
		totalSize, known := parseContentRangeTotal(resp.Header.Get("Content-Range"))
		return downloadProbeResult{
			SupportsRanges: true,
			KnownSize:      known,
			TotalSize:      totalSize,
		}, nil
	case http.StatusOK:
		return downloadProbeResult{
			SupportsRanges: false,
			KnownSize:      resp.ContentLength >= 0,
			TotalSize:      resp.ContentLength,
		}, nil
	default:
		return downloadProbeResult{}, fmt.Errorf("server returned %s", resp.Status)
	}
}

func parseContentRangeTotal(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	slash := strings.LastIndex(value, "/")
	if slash < 0 || slash == len(value)-1 {
		return 0, false
	}

	totalPart := strings.TrimSpace(value[slash+1:])
	if totalPart == "*" {
		return 0, false
	}

	var total int64
	if _, err := fmt.Sscanf(totalPart, "%d", &total); err != nil {
		return 0, false
	}
	return total, true
}

func downloadFileSequential(client *http.Client, rawURL string, tempPath string, label string, probe downloadProbeResult, startPercent int, endPercent int, onProgress func(appProgressEvent)) error {
	if probe.SupportsRanges && probe.KnownSize {
		return downloadFileSequentialRange(client, rawURL, tempPath, label, probe.TotalSize, startPercent, endPercent, onProgress)
	}
	return downloadFileSingleRequest(client, rawURL, tempPath, label, probe, startPercent, endPercent, onProgress)
}

func downloadFileSequentialRange(client *http.Client, rawURL string, tempPath string, label string, totalSize int64, startPercent int, endPercent int, onProgress func(appProgressEvent)) error {
	resumeOffset := int64(0)
	if info, err := os.Stat(tempPath); err == nil {
		resumeOffset = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if resumeOffset > totalSize {
		if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		resumeOffset = 0
	}

	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Truncate(resumeOffset); err != nil {
		return err
	}

	tracker := newDownloadProgressTracker(totalSize, resumeOffset, startPercent, endPercent, onProgress)
	if resumeOffset > 0 {
		tracker.Report("Resuming...")
	} else {
		tracker.Report("Downloading...")
	}

	offset := resumeOffset
	chunkSize := downloadChunkSizeFor(totalSize)
	for offset < totalSize {
		end := offset + chunkSize - 1
		if end >= totalSize {
			end = totalSize - 1
		}
		if err := downloadChunkToWriter(client, rawURL, file, offset, offset, end, tracker, "Downloading..."); err != nil {
			return fmt.Errorf("failed to download %s: %w", label, err)
		}
		offset = end + 1
	}

	return file.Sync()
}

func downloadFileSingleRequest(client *http.Client, rawURL string, tempPath string, label string, probe downloadProbeResult, startPercent int, endPercent int, onProgress func(appProgressEvent)) error {
	if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	totalSize := int64(0)
	if probe.KnownSize {
		totalSize = probe.TotalSize
	}
	tracker := newDownloadProgressTracker(totalSize, 0, startPercent, endPercent, onProgress)
	tracker.Report("Downloading...")

	var lastErr error
	for attempt := 1; attempt <= downloadChunkRetryLimit; attempt++ {
		if _, err := file.Seek(0, 0); err != nil {
			return err
		}
		if err := file.Truncate(0); err != nil {
			return err
		}
		tracker = newDownloadProgressTracker(totalSize, 0, startPercent, endPercent, onProgress)
		tracker.Report("Downloading...")

		ctx, cancel := context.WithTimeout(context.Background(), downloadChunkTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return err
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}

		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("server returned %s", resp.Status)
			resp.Body.Close()
			cancel()
			continue
		}

		_, copyErr := copyResponseToWriter(resp.Body, file, tracker, "Downloading...")
		resp.Body.Close()
		cancel()
		if copyErr == nil {
			return file.Sync()
		}
		lastErr = copyErr
	}

	if lastErr == nil {
		lastErr = errors.New("download failed")
	}
	return fmt.Errorf("failed to download %s: %w", label, lastErr)
}

func downloadFileMultiPart(client *http.Client, rawURL string, tempPath string, metaPath string, label string, totalSize int64, startPercent int, endPercent int, onProgress func(appProgressEvent)) error {
	state, err := loadDownloadResumeState(metaPath)
	if err != nil {
		return err
	}
	if state == nil || state.URL != rawURL || state.TotalSize != totalSize {
		state = &downloadResumeState{
			URL:       rawURL,
			TotalSize: totalSize,
		}
	}

	if info, err := os.Stat(tempPath); err == nil && info.Size() > 0 && len(state.Completed) == 0 {
		size := info.Size()
		if size >= totalSize {
			if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			size = 0
		}
		if size > totalSize {
			size = totalSize
		}
		if size > 0 {
			state.Completed = append(state.Completed, downloadByteRange{Start: 0, End: size - 1})
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	state.Completed = mergeDownloadRanges(state.Completed)
	if err := saveDownloadResumeState(metaPath, state); err != nil {
		return err
	}

	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	if err := file.Truncate(totalSize); err != nil {
		return err
	}

	completedBytes := totalCompletedBytes(state.Completed)
	tracker := newDownloadProgressTracker(totalSize, completedBytes, startPercent, endPercent, onProgress)
	if completedBytes > 0 {
		tracker.Report("Resuming with multiple connections...")
	} else {
		tracker.Report("Downloading with multiple connections...")
	}

	segments := buildMissingDownloadSegments(totalSize, state.Completed, downloadChunkSizeFor(totalSize))
	if len(segments) == 0 {
		return file.Sync()
	}

	workers := downloadParallelWorkers
	if workers > len(segments) {
		workers = len(segments)
	}
	if workers < 1 {
		workers = 1
	}

	segmentCh := make(chan downloadByteRange, len(segments))
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	var stateMu sync.Mutex

	workerFn := func() {
		defer wg.Done()
		for segment := range segmentCh {
			if err := downloadRangeSegment(client, rawURL, file, segment, tracker, "Downloading with multiple connections..."); err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}

			stateMu.Lock()
			state.Completed = mergeDownloadRanges(append(state.Completed, segment))
			saveErr := saveDownloadResumeState(metaPath, state)
			stateMu.Unlock()
			if saveErr != nil {
				select {
				case errCh <- saveErr:
				default:
				}
				return
			}
		}
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go workerFn()
	}

	for _, segment := range segments {
		select {
		case err := <-errCh:
			close(segmentCh)
			wg.Wait()
			return fmt.Errorf("failed to download %s: %w", label, err)
		default:
		}
		segmentCh <- segment
	}
	close(segmentCh)
	wg.Wait()

	select {
	case err := <-errCh:
		return fmt.Errorf("failed to download %s: %w", label, err)
	default:
	}

	return file.Sync()
}

func downloadRangeSegment(client *http.Client, rawURL string, file *os.File, segment downloadByteRange, tracker *downloadProgressTracker, message string) error {
	for attempt := 1; attempt <= downloadChunkRetryLimit; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), downloadChunkTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", segment.Start, segment.End))

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			if attempt == downloadChunkRetryLimit {
				return err
			}
			continue
		}

		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("server returned %s", resp.Status)
			resp.Body.Close()
			cancel()
			if attempt == downloadChunkRetryLimit {
				return err
			}
			continue
		}

		offset := segment.Start
		if resp.StatusCode == http.StatusOK && (segment.Start != 0 || segment.End < segment.Start) {
			err = errors.New("server ignored range request")
			resp.Body.Close()
			cancel()
			if attempt == downloadChunkRetryLimit {
				return err
			}
			continue
		}

		_, err = copyResponseToWriterAt(resp.Body, file, offset, tracker, message)
		resp.Body.Close()
		cancel()
		if err == nil {
			return nil
		}
		if attempt == downloadChunkRetryLimit {
			return err
		}
	}

	return errors.New("segment download failed")
}

func downloadChunkToWriter(client *http.Client, rawURL string, file *os.File, writeOffset int64, rangeStart int64, rangeEnd int64, tracker *downloadProgressTracker, message string) error {
	var lastErr error
	for attempt := 1; attempt <= downloadChunkRetryLimit; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), downloadChunkTimeout)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd))

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		if resp.StatusCode != http.StatusPartialContent {
			lastErr = fmt.Errorf("server returned %s", resp.Status)
			resp.Body.Close()
			cancel()
			continue
		}

		_, err = copyResponseToWriterAt(resp.Body, file, writeOffset, tracker, message)
		resp.Body.Close()
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = errors.New("chunk download failed")
	}
	return lastErr
}

func copyResponseToWriter(body io.Reader, file *os.File, tracker *downloadProgressTracker, message string) (int64, error) {
	buf := make([]byte, 256*1024)
	var written int64
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, writeErr := file.Write(buf[:n]); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			tracker.Add(int64(n), message)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return written, nil
			}
			return written, err
		}
	}
}

func copyResponseToWriterAt(body io.Reader, file *os.File, offset int64, tracker *downloadProgressTracker, message string) (int64, error) {
	buf := make([]byte, 256*1024)
	var written int64
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, writeErr := file.WriteAt(buf[:n], offset+written); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			tracker.Add(int64(n), message)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return written, nil
			}
			return written, err
		}
	}
}

func downloadChunkSizeFor(totalSize int64) int64 {
	switch {
	case totalSize >= 512<<20:
		return downloadChunkSizeLarge
	case totalSize >= 64<<20:
		return downloadChunkSizeMedium
	default:
		return downloadChunkSizeSmall
	}
}

func buildMissingDownloadSegments(totalSize int64, completed []downloadByteRange, chunkSize int64) []downloadByteRange {
	if totalSize <= 0 {
		return nil
	}
	if chunkSize <= 0 {
		chunkSize = downloadChunkSizeSmall
	}

	completed = mergeDownloadRanges(completed)
	var segments []downloadByteRange
	cursor := int64(0)
	for _, done := range completed {
		if done.Start > cursor {
			segments = append(segments, splitDownloadRange(downloadByteRange{Start: cursor, End: done.Start - 1}, chunkSize)...)
		}
		if done.End+1 > cursor {
			cursor = done.End + 1
		}
	}
	if cursor < totalSize {
		segments = append(segments, splitDownloadRange(downloadByteRange{Start: cursor, End: totalSize - 1}, chunkSize)...)
	}
	return segments
}

func splitDownloadRange(segment downloadByteRange, chunkSize int64) []downloadByteRange {
	if segment.End < segment.Start {
		return nil
	}
	var segments []downloadByteRange
	for start := segment.Start; start <= segment.End; start += chunkSize {
		end := start + chunkSize - 1
		if end > segment.End {
			end = segment.End
		}
		segments = append(segments, downloadByteRange{Start: start, End: end})
	}
	return segments
}

func mergeDownloadRanges(ranges []downloadByteRange) []downloadByteRange {
	if len(ranges) == 0 {
		return nil
	}

	filtered := make([]downloadByteRange, 0, len(ranges))
	for _, item := range ranges {
		if item.Start < 0 || item.End < item.Start {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return nil
	}

	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].Start == filtered[j].Start {
			return filtered[i].End < filtered[j].End
		}
		return filtered[i].Start < filtered[j].Start
	})

	merged := []downloadByteRange{filtered[0]}
	for _, item := range filtered[1:] {
		last := &merged[len(merged)-1]
		if item.Start <= last.End+1 {
			if item.End > last.End {
				last.End = item.End
			}
			continue
		}
		merged = append(merged, item)
	}
	return merged
}

func totalCompletedBytes(ranges []downloadByteRange) int64 {
	var total int64
	for _, item := range mergeDownloadRanges(ranges) {
		total += item.End - item.Start + 1
	}
	return total
}

func loadDownloadResumeState(metaPath string) (*downloadResumeState, error) {
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var state downloadResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		_ = os.Remove(metaPath)
		return nil, nil
	}
	return &state, nil
}

func saveDownloadResumeState(metaPath string, state *downloadResumeState) error {
	if state == nil {
		return nil
	}

	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	tmpPath := metaPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, metaPath)
}

func finalizeDownload(tempPath string, outPath string, metaPath string) error {
	file, err := os.OpenFile(tempPath, os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Remove(outPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tempPath, outPath); err != nil {
		return err
	}
	_ = os.Remove(metaPath)
	_ = os.Remove(metaPath + ".tmp")
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
