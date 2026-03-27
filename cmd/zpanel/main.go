package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/host"
	memutil "github.com/shirou/gopsutil/v3/mem"
	netutil "github.com/shirou/gopsutil/v3/net"
	_ "modernc.org/sqlite"

	"net/http/cgi"

	embeddedassets "zpanel"
)

var version = "0.1.1"

const (
	defaultWebsitePHPVersion = "8.4"
	metricsSampleInterval    = 4 * time.Second
	metricsHistoryLimit      = 20
)

type appConfig struct {
	Port        int
	BaseDir     string
	MaxMemoryMB uint64
	LogLevel    string
}

type appState struct {
	db              *sql.DB
	appRoot         string
	staticFS        fs.FS
	baseDir         string
	maxMemoryMB     uint64
	websitePort     int
	processRegistry *processRegistry
	metrics         *metricsCache
	appStoreMu      sync.Mutex
	routingMu       sync.Mutex
	appJobsMu       sync.Mutex
	appJobs         map[string]*appInstallJob
}

type phpExtensionSettingsProvider interface {
	ConfigFileForVersion(id string, version string) string
	InstallFolderForVersion(id string, version string) string
	PHPExtensions(version string) ([]string, []string, error)
	SavePHPExtensions(version string, enabled []string) error
	SyncWebsiteRouting() error
}

type websiteRecord struct {
	ID          int64  `json:"id"`
	Domain      string `json:"domain"`
	Path        string `json:"path"`
	PHPVersion  string `json:"php_version,omitempty"`
	Status      string `json:"status"`
	Port        int64  `json:"port"`
	PID         *int64 `json:"pid"`
	ProxyConfig string `json:"proxy_config"`
	URL         string `json:"url,omitempty"`
	PreviewURL  string `json:"preview_url,omitempty"`
	StatusLabel string `json:"status_label,omitempty"`
	Message     string `json:"message,omitempty"`
}

type websiteConfig struct {
	Domain     string `json:"domain"`
	Path       string `json:"path"`
	PHPVersion string `json:"php_version"`
	Status     string `json:"status"`
}

type databaseRecord struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	DBType string `json:"db_type"`
	Status string `json:"status"`
}

type websiteCreateRequest struct {
	Domain     string `json:"domain"`
	Path       string `json:"path"`
	PHPVersion string `json:"php_version"`
}

type websiteActionRequest struct {
	Domain string `json:"domain"`
}

type databaseCreateRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type statusResponse struct {
	Status             string    `json:"status"`
	OSLabel            string    `json:"os_label"`
	UptimeSeconds      uint64    `json:"uptime_seconds"`
	Websites           int       `json:"websites"`
	Databases          int       `json:"databases"`
	MaxMemoryMB        uint64    `json:"max_memory_mb"`
	CPUUsagePercent    uint8     `json:"cpu_usage_percent"`
	RAMUsedMB          uint64    `json:"ram_used_mb"`
	RAMTotalMB         uint64    `json:"ram_total_mb"`
	RAMUsagePercent    uint8     `json:"ram_usage_percent"`
	UploadSpeedKBPS    float64   `json:"upload_speed_kbps"`
	DownloadSpeedKBPS  float64   `json:"download_speed_kbps"`
	TotalSentGB        float64   `json:"total_sent_gb"`
	TotalReceivedGB    float64   `json:"total_received_gb"`
	NetworkHistoryKBPS []float64 `json:"network_history_kbps"`
}

type systemMetrics struct {
	OSLabel            string
	UptimeSeconds      uint64
	CPUUsagePercent    uint8
	RAMUsedMB          uint64
	RAMTotalMB         uint64
	RAMUsagePercent    uint8
	UploadSpeedKBPS    float64
	DownloadSpeedKBPS  float64
	TotalSentGB        float64
	TotalReceivedGB    float64
	NetworkHistoryKBPS []float64
}

type metricsCache struct {
	mu      sync.RWMutex
	current systemMetrics
}

func newMetricsCache() *metricsCache {
	return &metricsCache{
		current: systemMetrics{
			OSLabel:            "Unknown OS",
			NetworkHistoryKBPS: make([]float64, metricsHistoryLimit),
		},
	}
}

func (m *metricsCache) get() systemMetrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *metricsCache) set(next systemMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current = next
}

type processRegistry struct {
	mu    sync.Mutex
	procs map[int64]*exec.Cmd
}

type serverController struct {
	mu                sync.Mutex
	addr              string
	handler           http.Handler
	readHeaderTimeout time.Duration
	server            *http.Server
	OnStop            func() error
}

func newProcessRegistry() *processRegistry {
	return &processRegistry{procs: map[int64]*exec.Cmd{}}
}

func (p *processRegistry) start(command []string, dir string) (int64, error) {
	if len(command) == 0 {
		return 0, errors.New("command cannot be empty")
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if runtime.GOOS == "windows" {
		setHideWindow(cmd)
	}

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	pid := int64(cmd.Process.Pid)
	p.mu.Lock()
	p.procs[pid] = cmd
	p.mu.Unlock()
	return pid, nil
}

func (p *processRegistry) stop(pid int64) error {
	p.mu.Lock()
	cmd, ok := p.procs[pid]
	if ok {
		delete(p.procs, pid)
	}
	p.mu.Unlock()

	if !ok {
		return fmt.Errorf("process %d is not managed by the panel", pid)
	}

	if cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	_, _ = cmd.Process.Wait()
	return nil
}

func newServerController(addr string, handler http.Handler) *serverController {
	return &serverController{
		addr:              addr,
		handler:           handler,
		readHeaderTimeout: 5 * time.Second,
	}
}

func (s *serverController) Start() error {
	s.mu.Lock()
	if s.server != nil {
		s.mu.Unlock()
		return nil
	}

	server := &http.Server{
		Addr:              s.addr,
		Handler:           s.handler,
		ReadHeaderTimeout: s.readHeaderTimeout,
	}
	s.server = server
	s.mu.Unlock()

	go func() {
		err := server.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server stopped unexpectedly: %v", err)
		}
		s.mu.Lock()
		if s.server == server {
			s.server = nil
		}
		s.mu.Unlock()
	}()

	if err := waitForServer(s.addr, 5*time.Second); err != nil {
		_ = s.Stop()
		return err
	}

	return nil
}

func (s *serverController) Stop() error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	onStop := s.OnStop
	s.mu.Unlock()

	if onStop != nil {
		if err := onStop(); err != nil {
			log.Printf("error during server controller stop callback: %v", err)
		}
	}

	if server == nil {
		return nil
	}

	if err := shutdownServer(server); err != nil {
		return err
	}
	return waitForServerDown(s.addr, 5*time.Second)
}

func (s *serverController) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.server != nil
}

func main() {
	showVersion := flag.Bool("v", false, "show version")
	flag.Parse()

	if *showVersion {
		fmt.Printf("zPanel v%s\n", version)
		return
	}

	appRoot, err := executableDir()
	if err != nil {
		log.Fatalf("failed to resolve executable dir: %v", err)
	}

	cfg, err := loadConfig(filepath.Join(appRoot, "config.toml"))
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	cfg.BaseDir = resolveAppPath(appRoot, cfg.BaseDir)

	if err := os.MkdirAll(cfg.BaseDir, 0o755); err != nil {
		log.Fatalf("failed to create base dir: %v", err)
	}

	setupLogging()

	db, err := initDB(cfg.BaseDir)
	if err != nil {
		log.Fatalf("failed to initialize database: %v", err)
	}
	defer db.Close()

	state := &appState{
		db:              db,
		appRoot:         appRoot,
		staticFS:        embeddedassets.StaticFS(),
		baseDir:         cfg.BaseDir,
		maxMemoryMB:     cfg.MaxMemoryMB,
		processRegistry: newProcessRegistry(),
		metrics:         newMetricsCache(),
		appJobs:         map[string]*appInstallJob{},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go runMetricsSampler(ctx, state.metrics)

	mux := http.NewServeMux()
	registerRoutes(mux, state)

	addr := "127.0.0.1:" + strconv.Itoa(cfg.Port)
	dashboardURL := "http://" + addr + "/overview"
	controller := newServerController(addr, logMiddleware(mux))
	controller.OnStop = func() error {
		log.Printf("gracefully stopping all background services...")
		manager := newRuntimeManager(appRoot)
		return manager.StopAll()
	}

	alreadyRunning, err := acquirePlatformInstance()
	if err != nil {
		stop()
		log.Fatalf("failed to initialize single-instance shell: %v", err)
	}
	if alreadyRunning {
		return
	}
	defer releasePlatformInstance()

	log.Printf("starting control panel on %s", dashboardURL)
	log.Printf("serving static assets from Go app")
	log.Printf("data directory is %s", cfg.BaseDir)
	state.websitePort = 8081

	if err := controller.Start(); err != nil {
		stop()
		log.Fatalf("server failed to start: %v", err)
	}
	go func() {
		if err := syncWebsiteRouting(appRoot); err != nil {
			log.Printf("website apache sync warning: %v", err)
		}
	}()
	log.Printf("website domains are served by Apache on http://127.0.0.1:%d/", state.websitePort)

	if err := runPlatformShell(ctx, stop, controller, appRoot, dashboardURL); err != nil {
		stop()
		_ = controller.Stop()
		log.Fatalf("application stopped unexpectedly: %v", err)
	}

	_ = controller.Stop()
}

func setupLogging() {
	log.SetOutput(os.Stdout)
}

func loadConfig(path string) (appConfig, error) {
	cfg := appConfig{
		Port:        8080,
		BaseDir:     "./data",
		MaxMemoryMB: 512,
		LogLevel:    "info",
	}

	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		value = strings.Trim(value, `"`)

		switch key {
		case "port":
			if parsed, parseErr := strconv.Atoi(value); parseErr == nil {
				cfg.Port = parsed
			}
		case "base_dir":
			cfg.BaseDir = value
		case "max_memory_mb":
			if parsed, parseErr := strconv.ParseUint(value, 10, 64); parseErr == nil {
				cfg.MaxMemoryMB = parsed
			}
		case "log_level":
			cfg.LogLevel = value
		}
	}

	return cfg, nil
}

func initDB(baseDir string) (*sql.DB, error) {
	dbPath := filepath.Join(baseDir, "panel.db")
	dsn := dbPath + "?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)

	statements := []string{
		`CREATE TABLE IF NOT EXISTS services (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS databases (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL UNIQUE,
			type TEXT NOT NULL,
			status TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS app_store_settings (
			app_id TEXT NOT NULL,
			version TEXT NOT NULL,
			download_url TEXT NOT NULL,
			PRIMARY KEY (app_id, version)
		)`,
		`CREATE TABLE IF NOT EXISTS app_dashboard_settings (
			setting_key TEXT NOT NULL PRIMARY KEY,
			enabled INTEGER NOT NULL
		)`,
		`INSERT INTO services (name, status) VALUES ('nginx', 'stopped') ON CONFLICT(name) DO NOTHING`,
		`INSERT INTO services (name, status) VALUES ('mysql', 'stopped') ON CONFLICT(name) DO NOTHING`,
		`INSERT INTO services (name, status) VALUES ('node', 'stopped') ON CONFLICT(name) DO NOTHING`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			_ = db.Close()
			return nil, err
		}
	}

	return db, nil
}

func registerRoutes(mux *http.ServeMux, state *appState) {
	mux.HandleFunc("/api/status", state.handleStatus)
	mux.HandleFunc("/api/apps", state.handleListApps)
	mux.HandleFunc("/api/apps/action", state.handleAppAction)
	mux.HandleFunc("/api/apps/progress", state.handleAppProgress)
	mux.HandleFunc("/api/apps/dashboard-toggle", state.handleDashboardToggle)
	mux.HandleFunc("/api/apps/open-folder", state.handleOpenFolder)
	mux.HandleFunc("/api/apps/setting", state.handleAppSetting)
	mux.HandleFunc("/api/settings/app-store", state.handleAppStoreSettings)
	mux.HandleFunc("/api/websites", state.handleListWebsites)
	mux.HandleFunc("/api/website/create", state.handleCreateWebsite)
	mux.HandleFunc("/api/website/start", state.handleStartWebsite)
	mux.HandleFunc("/api/website/stop", state.handleStopWebsite)
	mux.HandleFunc("/api/website/delete", state.handleDeleteWebsite)
	mux.HandleFunc("/api/databases", state.handleListDatabases)
	mux.HandleFunc("/api/database/create", state.handleCreateDatabase)
	mux.HandleFunc("/preview", state.handleWebsitePreview)
	mux.HandleFunc("/static/", state.serveStaticAsset)
	mux.HandleFunc("/", state.serveIndex)
}

func (s *appState) injectAppSettings(apps []runtimeApp) []runtimeApp {
	dashboardSettings := s.loadAppSettings()
	appStoreSettings := loadAppStoreSettingsFromDB(s.appRoot)

	for i := range apps {
		appID := apps[i].ID
		apps[i].VersionTitles = make(map[string]string)
		apps[i].ShowOnDashboardVersions = make(map[string]bool)

		apps[i].ShowOnDashboard = valueAtBool(appStoreSettings.ShowOnDashboard, appID, apps[i].Version)
		if legacyValue, ok := dashboardSettings[appID]; ok {
			apps[i].ShowOnDashboard = legacyValue || apps[i].ShowOnDashboard
		}

		versionSet := map[string]struct{}{}
		for _, ver := range apps[i].AvailableVersions {
			if strings.TrimSpace(ver) != "" {
				versionSet[ver] = struct{}{}
			}
		}
		for _, ver := range apps[i].InstalledVersions {
			if strings.TrimSpace(ver) != "" {
				versionSet[ver] = struct{}{}
			}
		}
		for _, ver := range apps[i].RunningVersions {
			if strings.TrimSpace(ver) != "" {
				versionSet[ver] = struct{}{}
			}
		}
		if version := strings.TrimSpace(apps[i].Version); version != "" {
			versionSet[version] = struct{}{}
		}
		if appStoreSettings.ShowOnDashboard != nil && appStoreSettings.ShowOnDashboard[appID] != nil {
			for ver := range appStoreSettings.ShowOnDashboard[appID] {
				if strings.TrimSpace(ver) != "" {
					versionSet[ver] = struct{}{}
				}
			}
		}

		for ver := range versionSet {
			if title := strings.TrimSpace(valueAt(appStoreSettings.Titles, appID, ver)); title != "" {
				apps[i].VersionTitles[ver] = title
			} else {
				apps[i].VersionTitles[ver] = defaultAppStoreReleaseTitle(appID, ver)
			}

			isShown := valueAtBool(appStoreSettings.ShowOnDashboard, appID, ver)
			if legacyValue, ok := dashboardSettings[appID+":"+ver]; ok {
				isShown = legacyValue || isShown
			}
			if !isShown {
				if legacyValue, ok := dashboardSettings[appID]; ok {
					isShown = legacyValue
				}
			}
			apps[i].ShowOnDashboardVersions[ver] = isShown
		}

		version := apps[i].Version
		if title, ok := apps[i].VersionTitles[version]; ok {
			apps[i].Name = title
		} else {
			apps[i].Name = defaultAppStoreReleaseTitle(appID, version)
		}

		if appID == "php" {
			for _, show := range apps[i].ShowOnDashboardVersions {
				if show {
					apps[i].ShowOnDashboard = true
					break
				}
			}
		}
	}
	return apps
}

func (s *appState) injectJobStatus(apps []runtimeApp) []runtimeApp {
	s.appJobsMu.Lock()
	defer s.appJobsMu.Unlock()

	for i := range apps {
		jobKey := apps[i].ID
		var activeJob *appInstallJob

		if apps[i].ID == "php" {
			// Find any active PHP job
			for key, job := range s.appJobs {
				if strings.HasPrefix(key, "php:") {
					snap := job.snapshot()
					if snap.Status == "running" {
						activeJob = job
						break
					}
				}
			}
		}

		if activeJob == nil {
			if job, ok := s.appJobs[jobKey]; ok {
				activeJob = job
			}
		}

		if activeJob != nil {
			snap := activeJob.snapshot()
			if snap.Status == "running" {
				apps[i].Status = "installing"
				apps[i].StatusLabel = snap.Message
				apps[i].Progress = snap.Progress
				apps[i].SelectedVersion = snap.Version
			}
		}
	}
	return apps
}

func (s *appState) handleDashboardToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID    string `json:"id"`
		Value bool   `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Preserve existing dashboard settings for compatibility
	dashboardSettings := s.loadAppSettings()
	dashboardSettings[req.ID] = req.Value
	_ = s.saveAppSettings(dashboardSettings)

	// Also save to panel.db for per-version precision
	appID := req.ID
	version := ""
	if strings.Contains(req.ID, ":") {
		parts := strings.SplitN(req.ID, ":", 2)
		appID = parts[0]
		version = parts[1]
	} else {
		// If only appID is provided, try to find a relevant version (e.g. current installed one)
		manager := newRuntimeManager(s.appRoot)
		if status, err := manager.Status(); err == nil {
			for _, app := range status.Apps {
				if app.ID == appID {
					version = app.Version
					break
				}
			}
		}
	}

	if version != "" {
		dbSettings := loadAppStoreSettingsFromDB(s.appRoot)
		if dbSettings.ShowOnDashboard == nil {
			dbSettings.ShowOnDashboard = map[string]map[string]bool{}
		}
		if dbSettings.ShowOnDashboard[appID] == nil {
			dbSettings.ShowOnDashboard[appID] = map[string]bool{}
		}
		dbSettings.ShowOnDashboard[appID][version] = req.Value
		if err := saveAppStoreSettingsToDB(s.appRoot, dbSettings); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"id": req.ID, "show_on_dashboard": req.Value})
}

func (s *appState) handleOpenFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.ID == "" {
		writeJSONError(w, http.StatusBadRequest, "id is required")
		return
	}

	s.appStoreMu.Lock()
	manager := newRuntimeManager(s.appRoot)
	s.appStoreMu.Unlock()

	type pathProvider interface {
		InstallFolderFor(id string) string
	}

	var folderPath string
	if pp, ok := manager.(pathProvider); ok {
		folderPath = pp.InstallFolderFor(req.ID)
	}

	if folderPath == "" {
		// Fallback: use runtime root
		folderPath = filepath.Join(s.appRoot, "data", "runtime")
	}

	if _, err := os.Stat(folderPath); err != nil {
		folderPath = filepath.Join(s.appRoot, "data", "runtime")
	}

	if err := openFolder(folderPath); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": folderPath})
}

func (s *appState) handleAppSetting(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetAppSetting(w, r)
	case http.MethodPost:
		s.handleSaveAppSetting(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func splitRuntimeAppID(rawID string, rawVersion string) (string, string) {
	appID := strings.ToLower(strings.TrimSpace(rawID))
	version := strings.TrimSpace(rawVersion)
	if strings.Contains(appID, ":") {
		parts := strings.SplitN(appID, ":", 2)
		appID = parts[0]
		if version == "" && len(parts) > 1 {
			version = parts[1]
		}
	}
	return appID, version
}

func (s *appState) handleGetAppSetting(w http.ResponseWriter, r *http.Request) {
	appID, requestedVersion := splitRuntimeAppID(r.URL.Query().Get("id"), r.URL.Query().Get("version"))
	if appID == "" {
		writeJSONError(w, http.StatusBadRequest, "id is required")
		return
	}

	manager := newRuntimeManager(s.appRoot)
	response, err := manager.Status()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var found *runtimeApp
	for i := range response.Apps {
		if response.Apps[i].ID == appID {
			found = &response.Apps[i]
			break
		}
	}
	if found == nil {
		writeJSONError(w, http.StatusNotFound, "app not found")
		return
	}

	runtimeRoot := filepath.Join(s.appRoot, "data", "runtime")
	type settingInfo struct {
		ID                  string   `json:"id"`
		Name                string   `json:"name"`
		Version             string   `json:"version"`
		InstallPath         string   `json:"install_path"`
		RuntimeRoot         string   `json:"runtime_root"`
		Port                string   `json:"port"`
		ConfigFile          string   `json:"config_file,omitempty"`
		AvailableExtensions []string `json:"available_extensions,omitempty"`
		EnabledExtensions   []string `json:"enabled_extensions,omitempty"`
	}

	info := settingInfo{
		ID:          found.ID,
		Name:        found.Name,
		Version:     found.Version,
		InstallPath: found.InstallPath,
		RuntimeRoot: runtimeRoot,
		Port:        found.Port,
	}

	type configProvider interface {
		ConfigFileFor(id string) string
	}
	if cp, ok := manager.(configProvider); ok {
		info.ConfigFile = cp.ConfigFileFor(appID)
	}

	if appID == "php" {
		version := requestedVersion
		if version == "" {
			version = found.Version
		}
		info.Version = version
		if phpProvider, ok := manager.(phpExtensionSettingsProvider); ok {
			info.InstallPath = phpProvider.InstallFolderForVersion(appID, version)
			info.ConfigFile = phpProvider.ConfigFileForVersion(appID, version)
			available, enabled, err := phpProvider.PHPExtensions(version)
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, err.Error())
				return
			}
			info.AvailableExtensions = available
			info.EnabledExtensions = enabled
		}
	}

	writeJSON(w, http.StatusOK, info)
}

func (s *appState) handleSaveAppSetting(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID                string   `json:"id"`
		Version           string   `json:"version"`
		EnabledExtensions []string `json:"enabled_extensions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	appID, version := splitRuntimeAppID(req.ID, req.Version)
	if appID != "php" {
		writeJSONError(w, http.StatusBadRequest, "only php settings are editable right now")
		return
	}
	if version == "" {
		writeJSONError(w, http.StatusBadRequest, "php version is required")
		return
	}

	manager := newRuntimeManager(s.appRoot)
	phpProvider, ok := manager.(phpExtensionSettingsProvider)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "php settings are not supported on this platform")
		return
	}

	s.appStoreMu.Lock()
	err := phpProvider.SavePHPExtensions(version, req.EnabledExtensions)
	if err == nil {
		err = phpProvider.SyncWebsiteRouting()
	}
	s.appStoreMu.Unlock()
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":                 appID,
		"version":            version,
		"enabled_extensions": req.EnabledExtensions,
		"message":            "PHP extensions updated.",
	})
}

func (s *appState) serveStaticAsset(w http.ResponseWriter, r *http.Request) {
	http.StripPrefix("/static/", http.FileServer(http.FS(s.staticFS))).ServeHTTP(w, r)
}

func (s *appState) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch r.URL.Path {
	case "/", "/overview", "/websites", "/databases", "/apps", "/settings":
	default:
		http.NotFound(w, r)
		return
	}

	content, err := fs.ReadFile(s.staticFS, "index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

func (s *appState) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	websites, err := countWebsites(s.appRoot)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	databases, err := countRows(s.db, "databases")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	metrics := s.metrics.get()
	writeJSON(w, http.StatusOK, statusResponse{
		Status:             "running",
		OSLabel:            metrics.OSLabel,
		UptimeSeconds:      metrics.UptimeSeconds,
		Websites:           websites,
		Databases:          databases,
		MaxMemoryMB:        s.maxMemoryMB,
		CPUUsagePercent:    metrics.CPUUsagePercent,
		RAMUsedMB:          metrics.RAMUsedMB,
		RAMTotalMB:         metrics.RAMTotalMB,
		RAMUsagePercent:    metrics.RAMUsagePercent,
		UploadSpeedKBPS:    metrics.UploadSpeedKBPS,
		DownloadSpeedKBPS:  metrics.DownloadSpeedKBPS,
		TotalSentGB:        metrics.TotalSentGB,
		TotalReceivedGB:    metrics.TotalReceivedGB,
		NetworkHistoryKBPS: metrics.NetworkHistoryKBPS,
	})
}

func (s *appState) handleListWebsites(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	items, err := listWebsites(s.appRoot)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.decorateWebsiteRecords(items))
}

func (s *appState) handleCreateWebsite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req websiteCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	record, err := createWebsite(s.appRoot, req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	record.Message = "Website created. Applying routing in background."
	writeJSON(w, http.StatusOK, s.decorateWebsiteRecord(record))

	go func(domain string) {
		s.routingMu.Lock()
		defer s.routingMu.Unlock()

		if err := syncWebsiteRouting(s.appRoot); err != nil {
			log.Printf("website routing sync warning for %s: %v", domain, err)
			return
		}
		if err := ensureLocalDomainMapping(domain); err != nil {
			log.Printf("local domain mapping warning for %s: %v", domain, err)
		}
	}(record.Domain)
}

func (s *appState) handleStartWebsite(w http.ResponseWriter, r *http.Request) {
	s.handleWebsiteRuntimeChange(w, r, "running")
}

func (s *appState) handleStopWebsite(w http.ResponseWriter, r *http.Request) {
	s.handleWebsiteRuntimeChange(w, r, "stopped")
}

func (s *appState) handleDeleteWebsite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req websiteActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := s.deleteWebsite(req.Domain); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := syncWebsiteRouting(s.appRoot); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": "Website deleted successfully."})
}

func (s *appState) handleWebsiteRuntimeChange(w http.ResponseWriter, r *http.Request, status string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req websiteActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	record, err := updateWebsiteRuntime(s.appRoot, req.Domain, status)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := syncWebsiteRouting(s.appRoot); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.decorateWebsiteRecord(record))
}

func (s *appState) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	items, err := listDatabases(s.db)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *appState) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req databaseCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	record, err := createDatabase(s.db, s.baseDir, req)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (s *appState) handleWebsitePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	domain := strings.TrimSpace(r.URL.Query().Get("domain"))
	if domain == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}

	record, err := getWebsite(s.appRoot, domain)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	s.serveWebsiteRecord(w, r, record)
}

func createWebsite(appRoot string, req websiteCreateRequest) (websiteRecord, error) {
	domain := strings.TrimSpace(req.Domain)
	if domain == "" {
		return websiteRecord{}, errors.New("domain is required")
	}

	if !isValidLocalDomain(domain) {
		return websiteRecord{}, errors.New("invalid domain name")
	}

	pathValue := filepath.Join(appRoot, "www", domain)
	if info, err := os.Stat(pathValue); err == nil && info.IsDir() {
		return websiteRecord{}, errors.New("website already exists")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return websiteRecord{}, err
	}

	if err := os.MkdirAll(pathValue, 0o755); err != nil {
		return websiteRecord{}, err
	}

	if err := ensureWebsiteBootstrap(pathValue); err != nil {
		return websiteRecord{}, err
	}

	config := websiteConfig{
		Domain:     domain,
		Path:       pathValue,
		PHPVersion: req.PHPVersion,
		Status:     "running",
	}

	if config.PHPVersion == "" {
		config.PHPVersion = defaultWebsitePHPVersion
	}

	if err := saveWebsiteConfig(appRoot, config); err != nil {
		return websiteRecord{}, err
	}

	return getWebsite(appRoot, domain)
}

func createDatabase(db *sql.DB, baseDir string, req databaseCreateRequest) (databaseRecord, error) {
	name := strings.TrimSpace(req.Name)
	dbType := strings.ToLower(strings.TrimSpace(req.Type))
	if name == "" {
		return databaseRecord{}, errors.New("database name is required")
	}

	status := ""
	switch dbType {
	case "sqlite":
		databasePath := filepath.Join(baseDir, "databases", name+".sqlite")
		if err := os.MkdirAll(filepath.Dir(databasePath), 0o755); err != nil {
			return databaseRecord{}, err
		}
		file, err := os.Create(databasePath)
		if err != nil {
			return databaseRecord{}, err
		}
		_ = file.Close()
		status = "ready"
	case "mysql", "postgres", "postgresql":
		status = "mocked"
	default:
		return databaseRecord{}, errors.New("unsupported database type")
	}

	if _, err := db.Exec(`INSERT INTO databases (name, type, status) VALUES (?, ?, ?)`, name, dbType, status); err != nil {
		return databaseRecord{}, err
	}

	row := db.QueryRow(`SELECT id, name, type, status FROM databases WHERE name = ?`, name)
	var out databaseRecord
	if err := row.Scan(&out.ID, &out.Name, &out.DBType, &out.Status); err != nil {
		return databaseRecord{}, err
	}
	return out, nil
}

func listWebsites(appRoot string) ([]websiteRecord, error) {
	configDir := getSiteConfigRoot(appRoot)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(configDir)
	if err != nil {
		return nil, err
	}

	items := make([]websiteRecord, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			if _, ok := seen[entry.Name()]; ok {
				continue
			}
			record, err := getWebsite(appRoot, entry.Name())
			if err != nil {
				log.Printf("Warning: failed to load website %s: %v", entry.Name(), err)
				continue
			}
			seen[entry.Name()] = struct{}{}
			items = append(items, record)
		case strings.HasSuffix(entry.Name(), ".json"):
			domain := strings.TrimSuffix(entry.Name(), ".json")
			if _, ok := seen[domain]; ok {
				continue
			}
			record, err := getWebsite(appRoot, domain)
			if err != nil {
				log.Printf("Warning: failed to load website %s: %v", domain, err)
				continue
			}
			seen[domain] = struct{}{}
			items = append(items, record)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Domain) < strings.ToLower(items[j].Domain)
	})

	return items, nil
}

func listDatabases(db *sql.DB) ([]databaseRecord, error) {
	rows, err := db.Query(`SELECT id, name, type, status FROM databases ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]databaseRecord, 0)
	for rows.Next() {
		var item databaseRecord
		if err := rows.Scan(&item.ID, &item.Name, &item.DBType, &item.Status); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func getWebsite(appRoot string, domain string) (websiteRecord, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return websiteRecord{}, errors.New("domain is required")
	}

	configPath := getSiteConfigPath(appRoot, domain)
	content, err := os.ReadFile(configPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return websiteRecord{}, err
		}

		legacyContent, legacyErr := os.ReadFile(getLegacySiteConfigPath(appRoot, domain))
		if legacyErr == nil {
			var legacyConfig websiteConfig
			if err := json.Unmarshal(legacyContent, &legacyConfig); err != nil {
				return websiteRecord{}, fmt.Errorf("invalid legacy website configuration: %v", err)
			}
			if legacyConfig.Domain == "" {
				legacyConfig.Domain = domain
			}
			if err := saveWebsiteConfig(appRoot, legacyConfig); err != nil {
				return websiteRecord{}, err
			}
			content = legacyContent
		} else {
			// Migration: try to load from legacy .zpanel- status files in www/{domain}
			legacyPath := filepath.Join(appRoot, "www", domain)
			if info, err := os.Stat(legacyPath); err == nil && info.IsDir() {
				status, _ := readWebsiteStatusLegacy(legacyPath)
				phpVersion, _ := readWebsitePHPVersionLegacy(legacyPath)
				config := websiteConfig{
					Domain:     domain,
					Path:       legacyPath,
					PHPVersion: phpVersion,
					Status:     status,
				}
				if err := saveWebsiteConfig(appRoot, config); err != nil {
					return websiteRecord{}, err
				}
				return websiteRecord{
					Domain:     domain,
					Path:       legacyPath,
					PHPVersion: phpVersion,
					Status:     status,
				}, nil
			}
			return websiteRecord{}, errors.New("website not found")
		}
	}

	var config websiteConfig
	if err := json.Unmarshal(content, &config); err != nil {
		return websiteRecord{}, fmt.Errorf("invalid website configuration: %v", err)
	}

	return websiteRecord{
		Domain:     config.Domain,
		Path:       config.Path,
		PHPVersion: config.PHPVersion,
		Status:     config.Status,
	}, nil
}

func updateWebsiteRuntime(appRoot string, domain string, status string) (websiteRecord, error) {
	record, err := getWebsite(appRoot, domain)
	if err != nil {
		return websiteRecord{}, err
	}

	status = strings.ToLower(strings.TrimSpace(status))
	if status != "running" && status != "stopped" {
		return websiteRecord{}, errors.New("invalid website status")
	}

	config := websiteConfig{
		Domain:     record.Domain,
		Path:       record.Path,
		PHPVersion: record.PHPVersion,
		Status:     status,
	}

	if err := saveWebsiteConfig(appRoot, config); err != nil {
		return websiteRecord{}, err
	}

	return getWebsite(appRoot, domain)
}

func startWebsiteController(state *appState) (*serverController, error) {
	candidates := []int{80, 8088}
	var failures []string

	for _, port := range candidates {
		addr := "127.0.0.1:" + strconv.Itoa(port)
		controller := newServerController(addr, logMiddleware(http.HandlerFunc(state.serveWebsiteRequest)))
		if err := controller.Start(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", addr, err))
			continue
		}

		state.websitePort = port
		return controller, nil
	}

	return nil, errors.New(strings.Join(failures, "; "))
}

func (s *appState) decorateWebsiteRecord(item websiteRecord) websiteRecord {
	item.PreviewURL = "/preview?domain=" + item.Domain
	item.StatusLabel = formatWebsiteStatus(item.Status)
	if s.websitePort == 0 {
		return item
	}

	if s.websitePort == 80 {
		item.URL = "http://" + item.Domain + "/"
		return item
	}

	item.URL = fmt.Sprintf("http://%s:%d/", item.Domain, s.websitePort)
	return item
}

func (s *appState) decorateWebsiteRecords(items []websiteRecord) []websiteRecord {
	out := make([]websiteRecord, 0, len(items))
	for _, item := range items {
		out = append(out, s.decorateWebsiteRecord(item))
	}
	return out
}

func (s *appState) deleteWebsite(domain string) error {
	record, err := getWebsite(s.appRoot, domain)
	if err != nil {
		return err
	}

	_ = os.RemoveAll(getSiteConfigDir(s.appRoot, domain))
	_ = os.Remove(getLegacySiteConfigPath(s.appRoot, domain))

	if err := os.RemoveAll(record.Path); err != nil {
		return err
	}

	_ = removeLocalDomainMapping(domain)

	return nil
}

func countWebsites(appRoot string) (int, error) {
	configDir := getSiteConfigRoot(appRoot)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		return 0, err
	}

	entries, err := os.ReadDir(configDir)
	if err != nil {
		return 0, err
	}

	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		switch {
		case entry.IsDir():
			seen[entry.Name()] = struct{}{}
		case strings.HasSuffix(entry.Name(), ".json"):
			domain := strings.TrimSuffix(entry.Name(), ".json")
			if domain != "" {
				seen[domain] = struct{}{}
			}
		}
	}

	return len(seen), nil
}

func getSiteConfigRoot(appRoot string) string {
	return filepath.Join(appRoot, "etc", "sites")
}

func getSiteConfigDir(appRoot string, domain string) string {
	return filepath.Join(getSiteConfigRoot(appRoot), domain)
}

func getLegacySiteConfigPath(appRoot string, domain string) string {
	return filepath.Join(getSiteConfigRoot(appRoot), domain+".json")
}

func getSiteConfigPath(appRoot string, domain string) string {
	return filepath.Join(getSiteConfigDir(appRoot, domain), "site.json")
}

func getSiteVHostConfigPath(appRoot string, domain string) string {
	return filepath.Join(getSiteConfigDir(appRoot, domain), "apache-vhost.conf")
}

func syncWebsiteRouting(appRoot string) error {
	manager := newRuntimeManager(appRoot)
	if syncer, ok := manager.(interface{ SyncWebsiteRouting() error }); ok {
		return syncer.SyncWebsiteRouting()
	}
	return nil
}

func saveWebsiteConfig(appRoot string, config websiteConfig) error {
	dir := getSiteConfigDir(appRoot, config.Domain)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(getSiteConfigPath(appRoot, config.Domain), content, 0o644); err != nil {
		return err
	}
	_ = os.Remove(getLegacySiteConfigPath(appRoot, config.Domain))
	return nil
}

func readWebsiteStatusLegacy(pathValue string) (string, error) {
	content, err := os.ReadFile(filepath.Join(pathValue, ".zpanel-status"))
	if err != nil {
		return "running", nil
	}

	status := strings.ToLower(strings.TrimSpace(string(content)))
	if status == "stopped" {
		return "stopped", nil
	}
	return "running", nil
}

func readWebsitePHPVersionLegacy(pathValue string) (string, error) {
	content, err := os.ReadFile(filepath.Join(pathValue, ".zpanel-php-version"))
	if err != nil {
		return defaultWebsitePHPVersion, nil
	}

	value := strings.TrimSpace(string(content))
	if value == "" {
		return defaultWebsitePHPVersion, nil
	}
	return value, nil
}

func formatWebsiteStatus(status string) string {
	switch status {
	case "running":
		return "Running"
	case "stopped":
		return "Stopped"
	default:
		return "Created"
	}
}

func countRows(db *sql.DB, table string) (int, error) {
	row := db.QueryRow(`SELECT COUNT(*) FROM ` + table)
	var count int
	if err := row.Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func generateProxyConfig(domain string, port uint16) string {
	return fmt.Sprintf(
		"server {\n    listen 80;\n    server_name %s;\n    location / {\n        proxy_pass http://127.0.0.1:%d;\n        proxy_set_header Host $host;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n    }\n}",
		domain,
		port,
	)
}

func detectNodeStartCommand(path string) ([]string, error) {
	packageJSON := filepath.Join(path, "package.json")
	serverJS := filepath.Join(path, "server.js")

	if content, err := os.ReadFile(packageJSON); err == nil {
		command := "npm install && npm start"
		if strings.Contains(string(content), `"build"`) {
			command = "npm install && npm run build && npm start"
		}

		if runtime.GOOS == "windows" {
			return []string{"cmd", "/C", command}, nil
		}
		return []string{"sh", "-lc", command}, nil
	}

	if _, err := os.Stat(serverJS); err == nil {
		return []string{"node", "server.js"}, nil
	}

	return nil, errors.New("no package.json or server.js found for this website")
}

func (s *appState) serveWebsiteRequest(w http.ResponseWriter, r *http.Request) {
	host := strings.TrimSpace(r.Host)
	if host == "" {
		http.NotFound(w, r)
		return
	}

	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}

	record, err := getWebsite(s.appRoot, host)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	s.serveWebsiteRecord(w, r, record)
}

func (s *appState) servePHPRequest(w http.ResponseWriter, r *http.Request, record websiteRecord, scriptPath string) {
	manager := newRuntimeManager(s.appRoot)
	var phpCgi string

	// Try to use platform-specific method to get PHP CGI path
	if win, ok := manager.(interface{ phpCgiExe(string) string }); ok {
		phpCgi = win.phpCgiExe(record.PHPVersion)
	} else if lin, ok := manager.(interface{ phpPath(string) string }); ok {
		// Placeholder for Linux if needed later, but Linux usually uses FPM
		phpCgi = lin.phpPath(record.PHPVersion)
	}

	if phpCgi == "" || !fileExists(phpCgi) {
		http.Error(w, fmt.Sprintf("PHP %s not found. Please install it in the App Store.", record.PHPVersion), http.StatusInternalServerError)
		return
	}

	handler := &cgi.Handler{
		Path: phpCgi,
		Dir:  record.Path,
		Env: []string{
			"REDIRECT_STATUS=200",
			"SCRIPT_FILENAME=" + scriptPath,
		},
	}
	handler.ServeHTTP(w, r)
}

func (s *appState) serveWebsiteRecord(w http.ResponseWriter, r *http.Request, record websiteRecord) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	if record.Status == "stopped" {
		http.Error(w, "website is stopped", http.StatusServiceUnavailable)
		return
	}

	targetPath := filepath.Join(record.Path, filepath.FromSlash(path.Clean("/"+r.URL.Path)))
	rootPath := filepath.Clean(record.Path)
	cleanTarget := filepath.Clean(targetPath)
	if cleanTarget != rootPath && !strings.HasPrefix(cleanTarget, rootPath+string(filepath.Separator)) {
		http.Error(w, "invalid path", http.StatusForbidden)
		return
	}

	info, err := os.Stat(cleanTarget)
	switch {
	case err == nil && info.IsDir():
		indexPath := filepath.Join(cleanTarget, "index.php")
		if _, err := os.Stat(indexPath); err == nil {
			s.servePHPRequest(w, r, record, indexPath)
			return
		}
		indexPath = filepath.Join(cleanTarget, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			http.ServeFile(w, r, indexPath)
			return
		}
	case err == nil:
		if strings.HasSuffix(strings.ToLower(cleanTarget), ".php") {
			s.servePHPRequest(w, r, record, cleanTarget)
			return
		}
		http.ServeFile(w, r, cleanTarget)
		return
	}

	fallback := filepath.Join(rootPath, "index.html")
	if _, err := os.Stat(fallback); err == nil {
		http.ServeFile(w, r, fallback)
		return
	}

	http.NotFound(w, r)
}

func isValidLocalDomain(domain string) bool {
	if len(domain) < 3 || len(domain) > 253 {
		return false
	}
	if strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return false
	}

	parts := strings.Split(domain, ".")
	if len(parts) < 2 {
		return false
	}

	for _, part := range parts {
		if part == "" || len(part) > 63 {
			return false
		}
		for i, char := range part {
			if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
				continue
			}
			if char == '-' && i > 0 && i < len(part)-1 {
				continue
			}
			return false
		}
	}

	return true
}

func sanitizeDomainPath(domain string) string {
	replacer := strings.NewReplacer(".", "-", ":", "-", "/", "-", "\\", "-")
	return replacer.Replace(strings.ToLower(domain))
}

func ensureWebsiteBootstrap(pathValue string) error {
	indexPath := filepath.Join(pathValue, "index.php")
	if _, err := os.Stat(indexPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	content := `<?php
// ==========================
// BASIC ENV DETECTION
// ==========================
$isLocal = in_array($_SERVER['REMOTE_ADDR'], ['127.0.0.1', '::1'], true);

// ==========================
// QUERY HANDLING (SAFE)
// ==========================
if (isset($_GET['q'])) {
    $query = $_GET['q'];

    // Allow-list approach
    if ($query === 'info') {

        // phpinfo allowed ONLY on localhost
        if ($isLocal) {
            phpinfo();
            exit;
        }

        http_response_code(403);
        exit('Forbidden');
    }

    // Unknown query
    http_response_code(404);
    exit('Invalid query parameter.');
}
?>

<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>zPanel</title>

    <link href="https://fonts.googleapis.com/css?family=Karla:400" rel="stylesheet">

    <style>
        html, body {
            height: 100%;
            margin: 0;
            padding: 0;
            font-family: 'Karla', sans-serif;
            background-color: #f9f9f9;
            color: #333;
        }

        .container {
            display: flex;
            justify-content: center;
            align-items: center;
            height: 100%;
            text-align: center;
        }

        .content {
            max-width: 800px;
            padding: 100px;
            background: #fff;
            border-radius: 8px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
        }

        .title {
            font-size: 60px;
            margin: 0;
        }

        .info {
            margin-top: 20px;
            font-size: 18px;
            line-height: 1.6;
        }

        .info a {
            color: #007bff;
            text-decoration: none;
        }

        .info a:hover {
            color: #0056b3;
            text-decoration: underline;
        }

        .opt {
            margin-top: 30px;
        }

        .opt a {
            font-size: 18px;
            color: #007bff;
            text-decoration: none;
        }

        .opt a:hover {
            color: #0056b3;
            text-decoration: underline;
        }
    </style>
</head>
<body>

<div class="container">
    <div class="content">
        <h1 class="title">zPanel</h1>

        <div class="info">
            <?php if ($isLocal): ?>
                <p><?= htmlspecialchars($_SERVER['SERVER_SOFTWARE'], ENT_QUOTES, 'UTF-8'); ?></p>
                <p>
                    PHP version: <?= htmlspecialchars(PHP_VERSION, ENT_QUOTES, 'UTF-8'); ?>
                    <a title="phpinfo()" href="/?q=info">info</a>
                </p>
                <p>
                    Document Root:
                    <?= htmlspecialchars($_SERVER['DOCUMENT_ROOT'], ENT_QUOTES, 'UTF-8'); ?>
                </p>
            <?php else: ?>
                <p>Server is running</p>
                <p>PHP is enabled</p>
            <?php endif; ?>
        </div>

        <div class="opt">
            <p>
                <a href="https://github.com/hocdev-com/zpanel" target="_blank" rel="noopener">
                    Documentation
                </a>
            </p>
        </div>
    </div>
</div>

</body>
</html>
`
	return os.WriteFile(indexPath, []byte(content), 0o644)
}

func runMetricsSampler(ctx context.Context, cache *metricsCache) {
	ticker := time.NewTicker(metricsSampleInterval)
	defer ticker.Stop()

	var lastCounters []netutil.IOCountersStat
	var lastAt time.Time
	history := make([]float64, metricsHistoryLimit)
	historyCount := 0
	osLabel := "Unknown OS"
	var bootAt time.Time

	// Perform initial collection immediately on startup
	sample := func() {
		now := time.Now()
		if bootAt.IsZero() || osLabel == "Unknown OS" {
			hostInfo, _ := host.InfoWithContext(ctx)
			if hostInfo != nil {
				osLabel = formatOSLabel(hostInfo.Platform, hostInfo.PlatformVersion, hostInfo.OS)
				switch {
				case hostInfo.BootTime > 0:
					bootAt = time.Unix(int64(hostInfo.BootTime), 0)
				case hostInfo.Uptime > 0:
					bootAt = now.Add(-time.Duration(hostInfo.Uptime) * time.Second)
				}
			}
		}

		virtualMem, _ := memutil.VirtualMemoryWithContext(ctx)
		cpuUsage, _ := cpu.PercentWithContext(ctx, 0, false)
		counters, _ := netutil.IOCountersWithContext(ctx, false)

		uploadKBPS := 0.0
		downloadKBPS := 0.0
		totalSentGB := 0.0
		totalReceivedGB := 0.0

		if len(counters) > 0 {
			totalSentGB = round2(float64(counters[0].BytesSent) / (1024 * 1024 * 1024))
			totalReceivedGB = round2(float64(counters[0].BytesRecv) / (1024 * 1024 * 1024))
		}

		if len(lastCounters) > 0 && len(counters) > 0 && !lastAt.IsZero() {
			elapsed := now.Sub(lastAt).Seconds()
			if elapsed > 0 {
				uploadKBPS = round2((float64(counters[0].BytesSent-lastCounters[0].BytesSent) / 1024) / elapsed)
				downloadKBPS = round2((float64(counters[0].BytesRecv-lastCounters[0].BytesRecv) / 1024) / elapsed)
			}
		}

		lastCounters = counters
		lastAt = now

		peak := uploadKBPS
		if downloadKBPS > peak {
			peak = downloadKBPS
		}
		if historyCount < metricsHistoryLimit {
			history[historyCount] = peak
			historyCount++
		} else {
			copy(history, history[1:])
			history[metricsHistoryLimit-1] = peak
		}

		ramUsedMB := uint64(0)
		ramTotalMB := uint64(0)
		ramUsagePercent := uint8(0)
		if virtualMem != nil {
			ramUsedMB = virtualMem.Used / (1024 * 1024)
			ramTotalMB = virtualMem.Total / (1024 * 1024)
			ramUsagePercent = uint8(clamp(virtualMem.UsedPercent, 0, 100))
		}

		uptimeSeconds := uint64(0)
		if !bootAt.IsZero() {
			uptimeSeconds = uint64(now.Sub(bootAt).Seconds())
		}

		cpuPercent := uint8(0)
		if len(cpuUsage) > 0 {
			cpuPercent = uint8(clamp(cpuUsage[0], 0, 100))
		}

		historyCopy := make([]float64, metricsHistoryLimit)
		if historyCount < metricsHistoryLimit {
			copy(historyCopy[metricsHistoryLimit-historyCount:], history[:historyCount])
		} else {
			copy(historyCopy, history)
		}
		cache.set(systemMetrics{
			OSLabel:            osLabel,
			UptimeSeconds:      uptimeSeconds,
			CPUUsagePercent:    cpuPercent,
			RAMUsedMB:          ramUsedMB,
			RAMTotalMB:         ramTotalMB,
			RAMUsagePercent:    ramUsagePercent,
			UploadSpeedKBPS:    uploadKBPS,
			DownloadSpeedKBPS:  downloadKBPS,
			TotalSentGB:        totalSentGB,
			TotalReceivedGB:    totalReceivedGB,
			NetworkHistoryKBPS: historyCopy,
		})
	}

	// Initial sample
	sample()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sample()
		}
	}
}

func formatOSLabel(platform string, version string, osName string) string {
	platform = strings.TrimSpace(platform)
	version = strings.TrimSpace(version)
	osName = strings.TrimSpace(osName)

	if strings.HasPrefix(strings.ToLower(platform), "microsoft ") {
		platform = strings.TrimSpace(platform[len("Microsoft "):])
	}

	if strings.Contains(strings.ToLower(platform), "windows") {
		return formatWindowsOSLabel(platform, version, osName)
	}

	if platform != "" && version != "" {
		return strings.TrimSpace(platform + " " + version)
	}
	if platform != "" {
		return platform
	}
	if osName != "" {
		return osName
	}
	return "Unknown OS"
}

func formatWindowsOSLabel(platform string, version string, osName string) string {
	shortVersion := shortWindowsVersion(version)
	if platform == "" {
		platform = "Windows"
	}
	if shortVersion != "" {
		return strings.TrimSpace(platform + " " + shortVersion)
	}
	if osName != "" {
		return strings.TrimSpace(platform + " " + osName)
	}
	return platform
}

func shortWindowsVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}

	fields := strings.Fields(version)
	if len(fields) == 0 {
		return ""
	}

	numeric := fields[0]
	parts := strings.Split(numeric, ".")
	if len(parts) >= 3 {
		return strings.Join(parts[:3], ".")
	}
	return numeric
}

func round2(value float64) float64 {
	return mathRound(value*100) / 100
}

func mathRound(value float64) float64 {
	if value < 0 {
		return float64(int64(value - 0.5))
	}
	return float64(int64(value + 0.5))
}

func clamp(value float64, min float64, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

func waitForServer(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	url := "http://" + addr + "/api/status"
	client := &http.Client{Timeout: 750 * time.Millisecond}

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}

	return fmt.Errorf("timed out waiting for %s", url)
}

func shutdownServer(server *http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	err := server.Shutdown(shutdownCtx)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return closeErr
		}
		return nil
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func waitForServerDown(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err != nil {
			return nil
		}
		_ = conn.Close()
		time.Sleep(120 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s to stop", addr)
}

func executableDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exePath), nil
}

func resolveAppPath(appRoot string, value string) string {
	if value == "" {
		return appRoot
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(appRoot, value)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
