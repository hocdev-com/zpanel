//go:build windows

package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"net"

	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var runtimeApacheReleases = []runtimeRelease{
	{Version: "2.4.66", URL: "https://www.apachelounge.com/download/VS17/binaries/httpd-2.4.66-251206-Win64-VS17.zip", FileName: "httpd-2.4.66-251206-Win64-VS17.zip"},
	{Version: "2.4.65", URL: "https://www.apachelounge.com/download/VS17/binaries/httpd-2.4.65-250207-Win64-VS17.zip", FileName: "httpd-2.4.65-250207-Win64-VS17.zip"},
}

var runtimePHPReleases = []runtimeRelease{
	{Version: "8.4.19", URL: "https://windows.php.net/downloads/releases/php-8.4.19-Win32-vs17-x64.zip", FileName: "php-8.4.19-Win32-vs17-x64.zip"},
	{Version: "8.3.30", URL: "https://downloads.php.net/~windows/releases/archives/php-8.3.30-Win32-vs16-x64.zip", FileName: "php-8.3.30-Win32-vs16-x64.zip"},
}

var runtimeMySQLReleases = []runtimeRelease{
	{Version: "8.4.8", URL: "https://dev.mysql.com/get/Downloads/MySQL-8.4/mysql-8.4.8-winx64.zip", FileName: "mysql-8.4.8-winx64.zip"},
	{Version: "8.0.43", URL: "https://dev.mysql.com/get/Downloads/MySQL-8.0/mysql-8.0.43-winx64.zip", FileName: "mysql-8.0.43-winx64.zip"},
}

func appStoreReleaseCatalog() map[string][]runtimeRelease {
	return map[string][]runtimeRelease{
		"apache": runtimeApacheReleases,
		"php":    runtimePHPReleases,
		"mysql":  runtimeMySQLReleases,
	}
}

const apacheHTTPPort = 8081

var defaultPHPExtensions = []string{"mysqli", "pdo_mysql", "mbstring", "openssl", "curl"}
var websiteRoutingSyncMu sync.Mutex

type windowsRuntimeManager struct {
	projectRoot string
}

type runtimePaths struct {
	projectRoot      string
	runtimeRoot      string
	downloadsDir     string
	apacheExtractDir string
	phpRoot          string
	mysqlExtractDir  string
	mysqlDataDir     string
	mysqlTempDir     string
	myIniPath        string
	apacheZip        string
	phpZip           string
	mysqlZip         string
}

type runtimeInstallPlan struct {
	needApache bool
	needPHP    bool
	needMySQL  bool
}

type runtimeSelection struct {
	Apache runtimeRelease
	PHP    runtimeRelease
	MySQL  runtimeRelease
}

func newRuntimeManager(projectRoot string) runtimeManager {
	return &windowsRuntimeManager{projectRoot: projectRoot}
}

func (m *windowsRuntimeManager) Status() (runtimeAppsResponse, error) {
	return runtimeAppsResponse{Apps: m.listApps()}, nil
}

func (m *windowsRuntimeManager) Install(appID string, version string, onProgress func(appProgressEvent)) (runtimeAppsResponse, error) {
	if onProgress != nil {
		onProgress(appProgressEvent{Percent: 5, Message: "Preparing portable runtime..."})
	}

	plan := buildInstallPlan(appID)
	selection, err := m.buildRuntimeSelection(appID, version)
	if err != nil {
		return runtimeAppsResponse{}, err
	}
	if err := m.ensureProvisioned(plan, selection, onProgress); err != nil {
		return runtimeAppsResponse{}, err
	}

	if onProgress != nil {
		onProgress(appProgressEvent{Percent: 97, Message: "Finalizing install..."})
	}

	if onProgress != nil {
		onProgress(appProgressEvent{Percent: 100, Message: "Install completed."})
	}

	// Small delay to ensure file system is synced before listing
	time.Sleep(500 * time.Millisecond)

	return runtimeAppsResponse{
		Apps:    m.listApps(),
		Message: "Runtime provisioned successfully.",
	}, nil
}

func (m *windowsRuntimeManager) Start(appID string) (runtimeAppsResponse, error) {
	if !m.appInstalled(appID) {
		return runtimeAppsResponse{}, errors.New("application is not installed yet. Use Install first.")
	}

	baseID := appID
	version := ""
	if strings.Contains(appID, ":") {
		parts := strings.Split(appID, ":")
		baseID = parts[0]
		if len(parts) > 1 {
			version = strings.TrimSpace(parts[1])
		}
	}

	switch baseID {
	case "apache":
		if err := m.startApache(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "php":
		if version == "" {
			version = m.currentPHPVersion()
		}
		if version == "" {
			return runtimeAppsResponse{}, errors.New("no installed PHP version available to start")
		}
		if err := m.startPHPFastCGI(version); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "mysql":
		if err := m.startMySQL(); err != nil {
			return runtimeAppsResponse{}, err
		}
	}

	// Small delay to ensure status is updated
	time.Sleep(300 * time.Millisecond)

	return runtimeAppsResponse{
		Apps:    m.listApps(),
		Message: "Service started successfully.",
	}, nil
}

func (m *windowsRuntimeManager) Stop(appID string) (runtimeAppsResponse, error) {
	baseID := appID
	version := ""
	if strings.Contains(appID, ":") {
		parts := strings.Split(appID, ":")
		baseID = parts[0]
		if len(parts) > 1 {
			version = strings.TrimSpace(parts[1])
		}
	}

	switch baseID {
	case "apache":
		if err := m.stopApache(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "php":
		if version == "" {
			version = m.currentPHPVersion()
		}
		if version == "" {
			return runtimeAppsResponse{}, errors.New("no installed PHP version available to stop")
		}
		if err := m.stopPHPFastCGI(version); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "mysql":
		if err := m.stopMySQL(); err != nil {
			return runtimeAppsResponse{}, err
		}
	}

	// Small delay to ensure status is updated
	time.Sleep(300 * time.Millisecond)

	return runtimeAppsResponse{
		Apps:    m.listApps(),
		Message: "Service stopped successfully.",
	}, nil
}

func (m *windowsRuntimeManager) Uninstall(appID string) (runtimeAppsResponse, error) {
	baseID := appID
	version := ""
	if strings.Contains(appID, ":") {
		parts := strings.Split(appID, ":")
		baseID = parts[0]
		version = parts[1]
	}

	switch baseID {
	case "apache":
		if err := m.uninstallApache(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "php":
		if err := m.uninstallPHP(version); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "mysql":
		if err := m.uninstallMySQL(); err != nil {
			return runtimeAppsResponse{}, err
		}
	}

	// Small delay to ensure file system is synced before listing
	time.Sleep(500 * time.Millisecond)

	return runtimeAppsResponse{
		Apps:    m.listApps(),
		Message: "Application removed successfully.",
	}, nil
}

func (m *windowsRuntimeManager) paths() runtimePaths {
	runtimeRoot := filepath.Join(m.projectRoot, "data", "runtime")
	return runtimePaths{
		projectRoot:      m.projectRoot,
		runtimeRoot:      runtimeRoot,
		downloadsDir:     filepath.Join(runtimeRoot, "downloads"),
		apacheExtractDir: filepath.Join(runtimeRoot, "apache-dist"),
		phpRoot:          filepath.Join(runtimeRoot, "php"), // Base PHP dir
		mysqlExtractDir:  filepath.Join(runtimeRoot, "mysql-dist"),
		mysqlDataDir:     filepath.Join(runtimeRoot, "mysql-data"),
		mysqlTempDir:     filepath.Join(runtimeRoot, "mysql-tmp"),
		myIniPath:        filepath.Join(runtimeRoot, "my.ini"),
		apacheZip:        filepath.Join(runtimeRoot, "downloads", runtimeApacheReleases[0].FileName),
		phpZip:           filepath.Join(runtimeRoot, "downloads", runtimePHPReleases[0].FileName),
		mysqlZip:         filepath.Join(runtimeRoot, "downloads", runtimeMySQLReleases[0].FileName),
	}
}

func (m *windowsRuntimeManager) apacheRoot() string {
	return filepath.Join(m.paths().apacheExtractDir, "Apache24")
}

func (m *windowsRuntimeManager) mysqlRoot() string {
	root, _ := resolveSingleChildDirectory(m.paths().mysqlExtractDir)
	return root
}

func (m *windowsRuntimeManager) httpdConfPath() string {
	return filepath.Join(m.apacheRoot(), "conf", "httpd.conf")
}

func (m *windowsRuntimeManager) apacheVHostIncludePath() string {
	return quoteApachePath(toForwardPath(filepath.Join(m.projectRoot, "etc", "sites", "*", "apache-vhost.conf")))
}

func (m *windowsRuntimeManager) phpRoot(version string) string {
	if version == "" {
		version = m.currentPHPVersion()
	}
	return filepath.Join(m.projectRoot, "data", "runtime", "php", "v"+version)
}

func (m *windowsRuntimeManager) phpRealRoot(version string) string {
	root := m.phpRoot(version)
	real, _ := resolveSingleChildDirectory(root)
	return real
}

func (m *windowsRuntimeManager) phpExe(version string) string {
	return filepath.Join(m.phpRealRoot(version), "php.exe")
}

func (m *windowsRuntimeManager) phpCgiExe(version string) string {
	return filepath.Join(m.phpRealRoot(version), "php-cgi.exe")
}

func (m *windowsRuntimeManager) phpIniPath(version string) string {
	return filepath.Join(m.phpRealRoot(version), "php.ini")
}

func (m *windowsRuntimeManager) phpFastCGIPIDPath(version string) string {
	return filepath.Join(m.paths().phpRoot, "php-cgi-"+strings.ReplaceAll(version, ".", "-")+".pid")
}

func (m *windowsRuntimeManager) cleanupLegacyPHPExtensionSettings() {
	for _, path := range []string{
		filepath.Join(m.paths().phpRoot, "php-extension-settings.json"),
		filepath.Join(m.projectRoot, "data", "php-extension-settings.json"),
	} {
		_ = os.Remove(path)
	}
}

func (m *windowsRuntimeManager) legacyPHPFastCGIPIDPath(version string) string {
	return filepath.Join(m.paths().runtimeRoot, "php-cgi-"+strings.ReplaceAll(version, ".", "-")+".pid")
}

func (m *windowsRuntimeManager) phpInfoPath() string {
	return filepath.Join(m.apacheRoot(), "htdocs", "phpinfo.php")
}

func (m *windowsRuntimeManager) apachePIDPath() string {
	return filepath.Join(m.apacheRoot(), "logs", "httpd.pid")
}

func (m *windowsRuntimeManager) mysqlPIDPath() string {
	return filepath.Join(m.paths().mysqlDataDir, "mysqld.pid")
}

func (m *windowsRuntimeManager) apacheExe() string {
	return filepath.Join(m.apacheRoot(), "bin", "httpd.exe")
}

func (m *windowsRuntimeManager) mysqlExe() string {
	return filepath.Join(m.mysqlRoot(), "bin", "mysqld.exe")
}

func (m *windowsRuntimeManager) appInstalled(appID string) bool {
	baseID := appID
	version := ""
	if strings.Contains(appID, ":") {
		parts := strings.Split(appID, ":")
		baseID = parts[0]
		version = parts[1]
	}

	switch baseID {
	case "apache":
		return fileExists(m.apacheExe())
	case "php":
		if version == "" {
			version = m.currentPHPVersion()
		}
		if version == "" {
			return false
		}
		return fileExists(m.phpExe(version))
	case "mysql":
		return fileExists(m.mysqlExe())
	default:
		return false
	}
}

func (m *windowsRuntimeManager) getInstalledPHPVersions() []string {
	phpDir := filepath.Join(m.projectRoot, "data", "runtime", "php")
	entries, err := os.ReadDir(phpDir)
	if err != nil {
		return nil
	}

	var versions []string
	seen := make(map[string]struct{})
	for _, release := range runtimePHPReleases {
		if fileExists(m.phpExe(release.Version)) {
			versions = append(versions, release.Version)
			seen[release.Version] = struct{}{}
		}
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "v") {
			ver := strings.TrimPrefix(entry.Name(), "v")
			if _, ok := seen[ver]; ok {
				continue
			}
			if fileExists(m.phpExe(ver)) {
				versions = append(versions, ver)
			}
		}
	}
	return versions
}

func (m *windowsRuntimeManager) configuredApachePHPVersion() string {
	confBytes, err := os.ReadFile(m.httpdConfPath())
	if err != nil {
		return ""
	}

	matches := regexp.MustCompile(`(?im)^\s*PHPIniDir\s+"([^"]+/runtime/php/v([^"/]+)[^"]*)"\s*$`).FindStringSubmatch(string(confBytes))
	if len(matches) >= 3 {
		return strings.TrimSpace(matches[2])
	}
	return ""
}

func (m *windowsRuntimeManager) currentPHPVersion() string {
	configured := m.configuredApachePHPVersion()
	if configured != "" && fileExists(m.phpExe(configured)) {
		return configured
	}

	installed := m.getInstalledPHPVersions()
	if len(installed) > 0 {
		return installed[0]
	}
	return ""
}

func (m *windowsRuntimeManager) hasInstalledPHP() bool {
	return len(m.getInstalledPHPVersions()) > 0
}

func phpFastCGIPort(version string) int {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) != 3 {
		return 18900
	}

	major, _ := strconv.Atoi(parts[0])
	minor, _ := strconv.Atoi(parts[1])
	patch, _ := strconv.Atoi(parts[2])
	return 9000 + major*1000 + minor*100 + patch
}

func releaseURLs(releases []runtimeRelease) map[string]string {
	urls := make(map[string]string, len(releases))
	for _, r := range releases {
		urls[r.Version] = r.URL
	}
	return urls
}

func (m *windowsRuntimeManager) listApps() []runtimeApp {
	apacheReleases := appStoreEffectiveReleases(m.projectRoot, "apache")
	phpReleases := appStoreEffectiveReleases(m.projectRoot, "php")
	mysqlReleases := appStoreEffectiveReleases(m.projectRoot, "mysql")
	apacheInstalled := fileExists(m.apacheExe())
	mysqlInstalled := fileExists(m.mysqlExe())

	var apacheRunning, mysqlRunning bool
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if apacheInstalled {
			apacheRunning = testTCPPort("127.0.0.1", 8081, 120*time.Millisecond)
		}
	}()

	go func() {
		defer wg.Done()
		if mysqlInstalled {
			mysqlRunning = testTCPPort("127.0.0.1", 3307, 120*time.Millisecond)
		}
	}()

	wg.Wait()

	apacheInstallPath := ""
	if apacheInstalled {
		apacheInstallPath = m.apacheRoot()
	}
	mysqlInstallPath := ""
	if mysqlInstalled {
		mysqlInstallPath = m.mysqlRoot()
	}

	apps := []runtimeApp{
		newRuntimeApp("apache", "Apache", versionOrDefault(installedApacheVersion(m.apacheRoot()), apacheReleases[0].Version), availableVersions(apacheReleases), "Portable web server stored in data/runtime.", apacheInstallPath, apacheInstalled, apacheRunning, "8081", "http://127.0.0.1:8081/", releaseURLs(apacheReleases), apacheInstalled && !apacheRunning, apacheInstalled && apacheRunning, apacheRunning, apacheInstalled),
	}

	// Detect all installed PHP versions
	installedPHPs := m.getInstalledPHPVersions()
	runningPHPs := m.runningPHPVersions(installedPHPs)
	// If no versioned dirs found but old-style PHP dir exists, we might need a migration or just ignore it
	// For this task, we assume new-style vX.Y.Z dirs.

	mainPHPVersion := versionOrDefault(m.currentPHPVersion(), phpReleases[0].Version)
	phpInstalled := len(installedPHPs) > 0
	phpRunning := len(runningPHPs) > 0
	phpInstallPath := ""
	if phpInstalled {
		phpInstallPath = m.phpRoot(mainPHPVersion)
	}

	phpURL := ""
	if apacheRunning {
		phpURL = "http://127.0.0.1:8081/phpinfo.php"
	}
	phpApp := newRuntimeApp("php", "PHP", mainPHPVersion, availableVersions(phpReleases), "Portable PHP runtime. Multiple versions supported per website.", phpInstallPath, phpInstalled, phpRunning, "9000+", phpURL, releaseURLs(phpReleases), phpInstalled && !phpRunning, phpRunning, apacheRunning, phpInstalled)
	phpApp.InstalledVersions = installedPHPs
	phpApp.RunningVersions = runningPHPs
	apps = append(apps, phpApp)

	apps = append(apps, newRuntimeApp("mysql", "MySQL", versionOrDefault(installedMySQLVersion(m.mysqlRoot()), mysqlReleases[0].Version), availableVersions(mysqlReleases), "Portable MySQL server stored in data/runtime.", mysqlInstallPath, mysqlInstalled, mysqlRunning, "3307", "", releaseURLs(mysqlReleases), mysqlInstalled && !mysqlRunning, mysqlInstalled && mysqlRunning, false, mysqlInstalled))

	return apps
}

func (m *windowsRuntimeManager) runningPHPVersions(installed []string) []string {
	running := make([]string, 0, len(installed))
	for _, version := range installed {
		if testTCPPort("127.0.0.1", phpFastCGIPort(version), 120*time.Millisecond) {
			running = append(running, version)
		}
	}
	return running
}

func buildInstallPlan(appID string) runtimeInstallPlan {
	baseID := appID
	if strings.Contains(appID, ":") {
		baseID = strings.Split(appID, ":")[0]
	}

	switch baseID {
	case "apache":
		return runtimeInstallPlan{needApache: true}
	case "php":
		return runtimeInstallPlan{needPHP: true}
	case "mysql":
		return runtimeInstallPlan{needMySQL: true}
	case "stack":
		return runtimeInstallPlan{needApache: true, needPHP: true, needMySQL: true}
	default:
		return runtimeInstallPlan{needApache: true, needPHP: true, needMySQL: true}
	}
}

func (m *windowsRuntimeManager) ensureProvisioned(plan runtimeInstallPlan, selection runtimeSelection, onProgress func(appProgressEvent)) error {
	paths := m.paths()
	dirs := []string{paths.runtimeRoot, paths.downloadsDir}
	if plan.needMySQL {
		dirs = append(dirs, paths.mysqlDataDir, paths.mysqlTempDir)
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	if plan.needApache && shouldReplaceRuntime(installedApacheVersion(m.apacheRoot()), selection.Apache.Version, fileExists(m.apacheExe())) {
		if err := m.uninstallApache(); err != nil {
			return err
		}
	}
	if plan.needMySQL && shouldReplaceRuntime(installedMySQLVersion(m.mysqlRoot()), selection.MySQL.Version, fileExists(m.mysqlExe())) {
		if err := m.uninstallMySQL(); err != nil {
			return err
		}
	}

	if plan.needApache {
		paths.apacheZip = filepath.Join(paths.downloadsDir, selection.Apache.FileName)
		if err := downloadFile(selection.Apache.URL, paths.apacheZip, "Apache HTTP Server", 10, 28, onProgress); err != nil {
			return err
		}
	}
	if plan.needPHP {
		paths.phpZip = filepath.Join(paths.downloadsDir, selection.PHP.FileName)
		if err := downloadFile(selection.PHP.URL, paths.phpZip, "PHP Runtime", 30, 48, onProgress); err != nil {
			return err
		}
	}
	if plan.needMySQL {
		paths.mysqlZip = filepath.Join(paths.downloadsDir, selection.MySQL.FileName)
		if err := downloadFile(selection.MySQL.URL, paths.mysqlZip, "MySQL Community Server", 50, 68, onProgress); err != nil {
			return err
		}
	}

	if plan.needApache && !fileExists(m.apacheExe()) {
		reportProgress(onProgress, 82, "Extracting...")
		if err := expandZipFresh(paths.apacheZip, paths.apacheExtractDir); err != nil {
			return err
		}
	}
	if plan.needPHP && !fileExists(m.phpExe(selection.PHP.Version)) {
		reportProgress(onProgress, 86, "Extracting...")
		if err := expandZipFresh(paths.phpZip, m.phpRoot(selection.PHP.Version)); err != nil {
			return err
		}
	}
	if plan.needMySQL && !fileExists(m.mysqlExe()) {
		reportProgress(onProgress, 90, "Extracting...")
		if err := expandZipFresh(paths.mysqlZip, paths.mysqlExtractDir); err != nil {
			return err
		}
	}

	reportProgress(onProgress, 94, "Configuring...")
	if plan.needPHP {
		if err := m.configurePHP(selection.PHP.Version); err != nil {
			return err
		}
	}
	if plan.needApache {
		if err := m.configureApache(selection.PHP.Version); err != nil {
			return err
		}
	}
	if plan.needMySQL {
		if err := m.configureMySQL(); err != nil {
			return err
		}
	}
	return m.removeInstalledVersionsMetadata()
}

func availableVersions(releases []runtimeRelease) []string {
	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		versions = append(versions, release.Version)
	}
	return versions
}

func versionOrDefault(version string, fallback string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return fallback
	}
	return version
}

func shouldReplaceRuntime(currentVersion string, selectedVersion string, exists bool) bool {
	if !exists {
		return false
	}
	currentVersion = strings.TrimSpace(currentVersion)
	selectedVersion = strings.TrimSpace(selectedVersion)
	if selectedVersion == "" {
		return false
	}
	return currentVersion != selectedVersion
}

func pickRelease(releases []runtimeRelease, requestedVersion string) (runtimeRelease, error) {
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return releases[0], nil
	}
	for _, release := range releases {
		if release.Version == requestedVersion {
			return release, nil
		}
	}
	return runtimeRelease{}, fmt.Errorf("unsupported version: %s", requestedVersion)
}

func (m *windowsRuntimeManager) buildRuntimeSelection(appID string, version string) (runtimeSelection, error) {
	baseID := appID
	if strings.Contains(appID, ":") {
		parts := strings.Split(appID, ":")
		baseID = parts[0]
		if version == "" {
			version = parts[1]
		}
	}

	apacheReleases := appStoreEffectiveReleases(m.projectRoot, "apache")
	phpReleases := appStoreEffectiveReleases(m.projectRoot, "php")
	mysqlReleases := appStoreEffectiveReleases(m.projectRoot, "mysql")

	selection := runtimeSelection{
		Apache: apacheReleases[0],
		PHP:    phpReleases[0],
		MySQL:  mysqlReleases[0],
	}

	var err error
	switch baseID {
	case "apache":
		selection.Apache, err = pickRelease(apacheReleases, version)
	case "php":
		selection.PHP, err = pickRelease(phpReleases, version)
	case "mysql":
		selection.MySQL, err = pickRelease(mysqlReleases, version)
	default:
		err = nil
	}
	if err != nil {
		return runtimeSelection{}, err
	}
	return selection, nil
}

func (m *windowsRuntimeManager) installedVersionsPath() string {
	return filepath.Join(m.paths().runtimeRoot, "installed-versions.json")
}

func (m *windowsRuntimeManager) removeInstalledVersionsMetadata() error {
	if err := os.Remove(m.installedVersionsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (m *windowsRuntimeManager) ensureApacheLandingPage() error {
	htdocs := filepath.Join(m.apacheRoot(), "htdocs")
	if err := os.MkdirAll(htdocs, 0o755); err != nil {
		return err
	}
	indexHTML := "<!doctype html><html><head><meta charset=\"utf-8\"><title>zPanel Bundled Stack</title></head><body><h1>zPanel Bundled Stack</h1><p>Apache is running from data/runtime.</p></body></html>"
	return os.WriteFile(filepath.Join(htdocs, "index.html"), []byte(indexHTML), 0o644)
}

func (m *windowsRuntimeManager) configurePHP(version string) error {
	return m.configurePHPWithExtensions(version, defaultPHPExtensions)
}

func (m *windowsRuntimeManager) configurePHPWithExtensions(version string, enabled []string) error {
	templatePath := filepath.Join(m.phpRealRoot(version), "php.ini-production")
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return fmt.Errorf("php template not found: %w", err)
	}

	updated := string(content)
	extensionDir := toForwardPath(filepath.Join(m.phpRealRoot(version), "ext"))
	updated = regexp.MustCompile(`(?m)^;?\s*extension_dir\s*=\s*".*"$`).ReplaceAllString(updated, `extension_dir = "`+extensionDir+`"`)
	if !regexp.MustCompile(`(?mi)^extension_dir\s*=`).MatchString(updated) {
		updated += "\r\nextension_dir = \"" + extensionDir + "\"\r\n"
	}
	updated = m.applyPHPExtensions(version, updated, enabled)
	if !regexp.MustCompile(`(?m)^date\.timezone\s*=`).MatchString(updated) {
		updated += "\r\ndate.timezone = UTC\r\n"
	}
	if err := os.WriteFile(m.phpIniPath(version), []byte(updated), 0o644); err != nil {
		return err
	}
	m.cleanupLegacyPHPExtensionSettings()
	return nil
}

func normalizePHPExtensionName(value string) string {
	name := strings.TrimSpace(value)
	name = strings.Trim(name, `"'`)
	name = strings.TrimPrefix(strings.ToLower(name), "php_")
	name = strings.TrimSuffix(name, ".dll")
	name = strings.TrimSuffix(name, ".so")
	return strings.TrimSpace(name)
}

type phpExtensionDirective struct {
	Name     string
	Leading  string
	Trailing string
}

func splitPHPExtensionValue(value string) (string, string) {
	inSingleQuote := false
	inDoubleQuote := false
	for idx, char := range value {
		switch char {
		case '\'':
			if !inDoubleQuote {
				inSingleQuote = !inSingleQuote
			}
		case '"':
			if !inSingleQuote {
				inDoubleQuote = !inDoubleQuote
			}
		case ';':
			if inSingleQuote || inDoubleQuote {
				continue
			}
			commentStart := idx
			for commentStart > 0 {
				prev := value[commentStart-1]
				if prev != ' ' && prev != '\t' {
					break
				}
				commentStart--
			}
			return value[:commentStart], value[commentStart:]
		}
	}
	return value, ""
}

func parsePHPExtensionDirectiveDetails(line string) (phpExtensionDirective, bool) {
	leadingLen := 0
	for leadingLen < len(line) {
		char := line[leadingLen]
		if char != ' ' && char != '\t' {
			break
		}
		leadingLen++
	}

	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return phpExtensionDirective{}, false
	}
	trimmed = strings.TrimPrefix(trimmed, ";")
	trimmed = strings.TrimSpace(trimmed)
	if !strings.HasPrefix(strings.ToLower(trimmed), "extension") {
		return phpExtensionDirective{}, false
	}
	parts := strings.SplitN(trimmed, "=", 2)
	if len(parts) != 2 || strings.TrimSpace(strings.ToLower(parts[0])) != "extension" {
		return phpExtensionDirective{}, false
	}

	rawValue, trailing := splitPHPExtensionValue(parts[1])
	rawValue = strings.Trim(strings.TrimSpace(rawValue), `"'`)
	if strings.Contains(rawValue, "/") || strings.Contains(rawValue, `\`) {
		return phpExtensionDirective{}, false
	}

	name := normalizePHPExtensionName(rawValue)
	if name == "" {
		return phpExtensionDirective{}, false
	}

	return phpExtensionDirective{
		Name:     name,
		Leading:  line[:leadingLen],
		Trailing: trailing,
	}, true
}

func parsePHPExtensionDirective(line string) (string, bool) {
	directive, ok := parsePHPExtensionDirectiveDetails(line)
	if !ok {
		return "", false
	}
	return directive.Name, true
}

func uniqueSortedExtensions(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := normalizePHPExtensionName(value)
		if name == "" {
			continue
		}
		set[name] = struct{}{}
	}
	items := make([]string, 0, len(set))
	for value := range set {
		items = append(items, value)
	}
	sort.Strings(items)
	return items
}

func (m *windowsRuntimeManager) availablePHPExtensions(version string) ([]string, error) {
	if version == "" || !fileExists(m.phpExe(version)) {
		return nil, errors.New("php version is not installed")
	}

	extensions := append([]string{}, defaultPHPExtensions...)
	extDir := filepath.Join(m.phpRealRoot(version), "ext")
	entries, err := os.ReadDir(extDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".dll" {
				continue
			}
			extensions = append(extensions, normalizePHPExtensionName(entry.Name()))
		}
	}

	for _, iniPath := range []string{filepath.Join(m.phpRealRoot(version), "php.ini-production"), m.phpIniPath(version)} {
		content, readErr := os.ReadFile(iniPath)
		if readErr != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			if extension, ok := parsePHPExtensionDirective(line); ok {
				extensions = append(extensions, extension)
			}
		}
	}

	return uniqueSortedExtensions(extensions), nil
}

func (m *windowsRuntimeManager) enabledPHPExtensions(version string) ([]string, error) {
	if version == "" || !fileExists(m.phpExe(version)) {
		return nil, errors.New("php version is not installed")
	}

	iniPath := m.phpIniPath(version)
	if !fileExists(iniPath) {
		if err := m.configurePHP(version); err != nil {
			return nil, err
		}
	}

	content, err := os.ReadFile(iniPath)
	if err != nil {
		return nil, err
	}

	var enabled []string
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if extension, ok := parsePHPExtensionDirective(trimmed); ok {
			enabled = append(enabled, extension)
		}
	}
	return uniqueSortedExtensions(enabled), nil
}

func (m *windowsRuntimeManager) applyPHPExtensions(version string, content string, enabled []string) string {
	targets := make(map[string]struct{})
	for _, ext := range enabled {
		targets[normalizePHPExtensionName(ext)] = struct{}{}
	}

	trackList, _ := m.availablePHPExtensions(version)
	for _, ext := range trackList {
		targets[ext] = struct{}{}
	}
	for _, ext := range defaultPHPExtensions {
		if _, ok := targets[ext]; !ok {
			targets[ext] = struct{}{}
		}
	}

	tracked := make(map[string]struct{}, len(targets))
	for ext := range targets {
		if ext != "" {
			tracked[ext] = struct{}{}
		}
	}
	for _, line := range strings.Split(content, "\n") {
		if extension, ok := parsePHPExtensionDirective(line); ok {
			tracked[extension] = struct{}{}
		}
	}

	desired := make(map[string]bool, len(enabled))
	for _, ext := range enabled {
		name := normalizePHPExtensionName(ext)
		if name != "" {
			desired[name] = true
		}
	}

	lines := strings.Split(content, "\n")
	seen := make(map[string]bool, len(tracked))
	for idx, line := range lines {
		directive, ok := parsePHPExtensionDirectiveDetails(line)
		if !ok {
			continue
		}
		if _, shouldTrack := tracked[directive.Name]; !shouldTrack {
			continue
		}
		if desired[directive.Name] {
			lines[idx] = directive.Leading + "extension=" + directive.Name + directive.Trailing
			seen[directive.Name] = true
			continue
		}
		lines[idx] = directive.Leading + ";extension=" + directive.Name + directive.Trailing
		seen[directive.Name] = true
	}

	for extension := range desired {
		if !seen[extension] {
			lines = append(lines, "extension="+extension)
		}
	}

	return strings.Join(lines, "\n")
}

func (m *windowsRuntimeManager) PHPExtensions(version string) ([]string, []string, error) {
	available, err := m.availablePHPExtensions(version)
	if err != nil {
		return nil, nil, err
	}
	enabled, err := m.enabledPHPExtensions(version)
	if err != nil {
		return nil, nil, err
	}
	return available, enabled, nil
}

func (m *windowsRuntimeManager) SavePHPExtensions(version string, enabled []string) error {
	available, err := m.availablePHPExtensions(version)
	if err != nil {
		return err
	}

	allowed := make(map[string]struct{}, len(available))
	for _, ext := range available {
		allowed[ext] = struct{}{}
	}

	filtered := make([]string, 0, len(enabled))
	for _, ext := range uniqueSortedExtensions(enabled) {
		if _, ok := allowed[ext]; ok {
			filtered = append(filtered, ext)
		}
	}

	iniPath := m.phpIniPath(version)
	if !fileExists(iniPath) {
		return m.configurePHPWithExtensions(version, filtered)
	}

	content, err := os.ReadFile(iniPath)
	if err != nil {
		return err
	}
	updated := m.applyPHPExtensions(version, string(content), filtered)
	return os.WriteFile(iniPath, []byte(updated), 0o644)
}

func (m *windowsRuntimeManager) configureApache(phpVersion string) error {
	confBytes, err := os.ReadFile(m.httpdConfPath())
	if err != nil {
		return fmt.Errorf("apache config not found: %w", err)
	}

	apacheRootForward := toForwardPath(m.apacheRoot())
	conf := string(confBytes)
	conf = regexp.MustCompile(`(?m)^ServerRoot ".*"\r?$`).ReplaceAllString(conf, `ServerRoot "`+apacheRootForward+`"`)
	conf = regexp.MustCompile(`(?m)^Define SRVROOT ".*"\r?$`).ReplaceAllString(conf, `Define SRVROOT "`+apacheRootForward+`"`)
	conf = strings.ReplaceAll(conf, "C:/Apache24-64", apacheRootForward)
	conf = regexp.MustCompile(`(?m)^Listen\s+80\r?$`).ReplaceAllString(conf, "Listen 127.0.0.1:"+strconv.Itoa(apacheHTTPPort))
	conf = regexp.MustCompile(`(?m)^#?ServerName\s+.*\r?$`).ReplaceAllString(conf, "ServerName 127.0.0.1:"+strconv.Itoa(apacheHTTPPort))
	conf = regexp.MustCompile(`(?m)^DirectoryIndex .+\r?$`).ReplaceAllString(conf, "DirectoryIndex index.php index.html")
	conf = enableApacheModule(conf, "proxy_module", "mod_proxy.so")
	conf = enableApacheModule(conf, "proxy_fcgi_module", "mod_proxy_fcgi.so")
	conf = enableApacheModule(conf, "rewrite_module", "mod_rewrite.so")

	conf = regexp.MustCompile(`(?m)^\s*LoadModule php_module ".*"\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^\s*PHPIniDir ".*"\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^\s*AddHandler application/x-httpd-php \.php\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^\s*AddType application/x-httpd-php \.php\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?s)\r?\n# zPanel PHP BEGIN\r?\n.*?# zPanel PHP END\r?\n?`).ReplaceAllString(conf, "\r\n")
	conf = regexp.MustCompile(`(?m)^Include conf/extra/httpd-vhosts\.conf\r?$`).ReplaceAllString(conf, "#Include conf/extra/httpd-vhosts.conf")
	conf = regexp.MustCompile(`(?m)^Include conf/extra/zpanel-vhosts\.conf\r?$`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^IncludeOptional ".*?/etc/sites/\*/apache-vhost\.conf"\r?$`).ReplaceAllString(conf, "")
	conf += "\r\nIncludeOptional " + m.apacheVHostIncludePath() + "\r\n"

	if err := os.WriteFile(m.httpdConfPath(), []byte(conf), 0o644); err != nil {
		return err
	}

	if err := m.writeApacheVHostConfig(); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.apacheRoot(), "conf", "extra", "zpanel-vhosts.conf"))

	if err := m.ensureApacheLandingPage(); err != nil {
		return err
	}

	if fileExists(m.phpExe(phpVersion)) {
		defaultPort := phpFastCGIPort(phpVersion)
		indexPHP := "<?php\nheader('Content-Type: text/html; charset=utf-8');\necho '<h1>zPanel Bundled Stack</h1>';\necho '<p>Apache + PHP " + phpVersion + " is running from data/runtime.</p>';\necho '<p>PHP version: ' . PHP_VERSION . '</p>';\n"
		if err := os.WriteFile(filepath.Join(m.apacheRoot(), "htdocs", "index.php"), []byte(indexPHP), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(m.phpInfoPath(), []byte("<?php phpinfo();"), 0o644); err != nil {
			return err
		}

		confBytes, err = os.ReadFile(m.httpdConfPath())
		if err != nil {
			return err
		}
		conf = string(confBytes)
		phpBlock := "\r\n# zPanel PHP BEGIN\r\n" +
			"<Proxy \"fcgi://127.0.0.1:" + strconv.Itoa(defaultPort) + "/\" enablereuse=on max=10>\r\n" +
			"</Proxy>\r\n" +
			"ProxyFCGIBackendType GENERIC\r\n" +
			"ProxyFCGISetEnvIf \"true\" SCRIPT_FILENAME \"%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}\"\r\n" +
			"ProxyFCGISetEnvIf \"true\" PATH_TRANSLATED \"%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}\"\r\n" +
			"<Directory \"" + apacheRootForward + "/htdocs\">\r\n" +
			"    <FilesMatch \"\\.php$\">\r\n" +
			"        SetHandler \"proxy:fcgi://127.0.0.1:" + strconv.Itoa(defaultPort) + "/\"\r\n" +
			"    </FilesMatch>\r\n" +
			"</Directory>\r\n" +
			"# zPanel PHP END\r\n"
		conf += phpBlock
		return os.WriteFile(m.httpdConfPath(), []byte(conf), 0o644)
	}

	_ = os.Remove(filepath.Join(m.apacheRoot(), "htdocs", "index.php"))
	_ = os.Remove(m.phpInfoPath())
	return nil
}

func (m *windowsRuntimeManager) configureMySQL() error {
	root := m.mysqlRoot()
	myIni := fmt.Sprintf("[mysqld]\r\nbasedir=%s\r\ndatadir=%s\r\nport=3307\r\nbind-address=127.0.0.1\r\nmysqlx=0\r\ntmpdir=%s\r\npid-file=%s\r\nlog-error=%s\r\n\r\n[client]\r\nport=3307\r\n",
		toForwardPath(root),
		toForwardPath(m.paths().mysqlDataDir),
		toForwardPath(m.paths().mysqlTempDir),
		toForwardPath(m.mysqlPIDPath()),
		toForwardPath(filepath.Join(m.paths().mysqlDataDir, "mysqld.err")),
	)
	if err := os.WriteFile(m.paths().myIniPath, []byte(myIni), 0o644); err != nil {
		return err
	}

	if fileExists(filepath.Join(m.paths().mysqlDataDir, "mysql")) {
		return nil
	}

	return runHiddenProcess(m.mysqlExe(), []string{"--defaults-file=" + m.paths().myIniPath, "--initialize-insecure"}, root, nil, true)
}

func (m *windowsRuntimeManager) startApache() error {
	if !fileExists(m.apacheExe()) {
		return errors.New("apache is not installed")
	}

	activePHPVersion := m.currentPHPVersion()
	if activePHPVersion == "" || !fileExists(m.phpExe(activePHPVersion)) {
		return errors.New("Apache HTTP Server requires at least one PHP Runtime to be installed. Please install a PHP version in the App Store first.")
	}

	if testTCPPort("127.0.0.1", 8081, 350*time.Millisecond) {
		return nil
	}

	if pid, _ := clearStalePIDFile(m.apachePIDPath(), "httpd.exe"); pid > 0 {
		stopProcessTree(pid)
		_ = waitForTCPPortClosed("127.0.0.1", 8081, 10*time.Second)
	}

	if err := m.configureApache(activePHPVersion); err != nil {
		return err
	}
	if err := m.startRequiredPHPFastCGI(); err != nil {
		return err
	}

	env := append(os.Environ(), "PATH="+m.phpRealRoot(activePHPVersion)+";"+filepath.Join(m.apacheRoot(), "bin")+";"+os.Getenv("PATH"))
	if _, err := startHiddenProcess(m.apacheExe(), []string{"-d", m.apacheRoot(), "-f", m.httpdConfPath()}, m.apacheRoot(), env); err != nil {
		return err
	}

	if !waitForTCPPort("127.0.0.1", 8081, 20*time.Second) {
		return errors.New("apache did not start successfully on http://127.0.0.1:8081/")
	}
	return nil
}

func (m *windowsRuntimeManager) stopApache() error {
	pid, _ := clearStalePIDFile(m.apachePIDPath(), "httpd.exe")
	listeningPID, _ := getListeningPID(8081)
	if pid == 0 && listeningPID == 0 && !testTCPPort("127.0.0.1", 8081, 350*time.Millisecond) {
		_ = os.Remove(m.apachePIDPath())
		return nil
	}

	switch {
	case pid > 0:
		stopProcessTree(pid)
	case listeningPID > 0:
		stopProcessTree(listeningPID)
	}

	_ = waitForTCPPortClosed("127.0.0.1", 8081, 10*time.Second)
	_ = m.stopAllPHPFastCGI()
	_ = os.Remove(m.apachePIDPath())
	return nil
}

func (m *windowsRuntimeManager) startMySQL() error {
	if !fileExists(m.mysqlExe()) {
		return errors.New("mysql is not installed")
	}
	if testTCPPort("127.0.0.1", 3307, 350*time.Millisecond) {
		return nil
	}

	if pid, _ := clearStalePIDFile(m.mysqlPIDPath(), "mysqld.exe"); pid > 0 {
		stopProcess(pid)
		_ = waitForTCPPortClosed("127.0.0.1", 3307, 12*time.Second)
	}
	_ = os.Remove(m.mysqlPIDPath())

	if _, err := startHiddenProcess(m.mysqlExe(), []string{"--defaults-file=" + m.paths().myIniPath, "--console"}, m.mysqlRoot(), nil); err != nil {
		return err
	}
	if !waitForTCPPort("127.0.0.1", 3307, 35*time.Second) {
		return errors.New("mysql did not start successfully on 127.0.0.1:3307")
	}
	return nil
}

func (m *windowsRuntimeManager) stopMySQL() error {
	pid, _ := clearStalePIDFile(m.mysqlPIDPath(), "mysqld.exe")
	if pid == 0 && !testTCPPort("127.0.0.1", 3307, 350*time.Millisecond) {
		_ = os.Remove(m.mysqlPIDPath())
		return nil
	}
	if pid > 0 {
		stopProcess(pid)
	} else if listeningPID, _ := getListeningPID(3307); listeningPID > 0 {
		stopProcessTree(listeningPID)
	}
	_ = waitForTCPPortClosed("127.0.0.1", 3307, 12*time.Second)
	_ = os.Remove(m.mysqlPIDPath())
	return nil
}

func (m *windowsRuntimeManager) uninstallApache() error {
	if err := m.stopApache(); err != nil {
		return err
	}

	paths := m.paths()
	_ = os.RemoveAll(paths.apacheExtractDir)
	_ = os.Remove(paths.apacheZip)
	_ = m.removeInstalledVersionsMetadata()
	return nil
}

func (m *windowsRuntimeManager) uninstallPHP(version string) error {
	if version == "" {
		version = m.currentPHPVersion()
	}

	if version == "" {
		return nil
	}

	if fileExists(m.apacheExe()) {
		if err := m.stopApache(); err != nil {
			return err
		}
	}

	paths := m.paths()
	_ = os.RemoveAll(m.phpRoot(version))
	if archivePath := phpArchivePath(paths, version); archivePath != "" {
		_ = os.Remove(archivePath)
	}
	_ = m.removeInstalledVersionsMetadata()

	if fileExists(m.httpdConfPath()) {
		nextVersion := m.currentPHPVersion()
		if err := m.configureApache(nextVersion); err != nil {
			return err
		}
	}
	return nil
}

func (m *windowsRuntimeManager) uninstallMySQL() error {
	if err := m.stopMySQL(); err != nil {
		return err
	}

	paths := m.paths()
	_ = os.RemoveAll(paths.mysqlExtractDir)
	_ = os.RemoveAll(paths.mysqlDataDir)
	_ = os.RemoveAll(paths.mysqlTempDir)
	_ = os.Remove(paths.myIniPath)
	_ = os.Remove(paths.mysqlZip)
	_ = m.removeInstalledVersionsMetadata()
	return nil
}

func (m *windowsRuntimeManager) InstallFolderFor(id string) string {
	switch id {
	case "apache":
		return m.apacheRoot()
	case "php":
		return m.paths().phpRoot
	case "mysql":
		return m.mysqlRoot()
	}
	return m.paths().runtimeRoot
}

func (m *windowsRuntimeManager) InstallFolderForVersion(id string, version string) string {
	if id == "php" && version != "" {
		return m.phpRoot(version)
	}
	return m.InstallFolderFor(id)
}

func (m *windowsRuntimeManager) ConfigFileFor(id string) string {
	switch id {
	case "apache":
		return m.httpdConfPath()
	case "php":
		return m.phpIniPath("")
	case "mysql":
		return m.paths().myIniPath
	}
	return ""
}

func (m *windowsRuntimeManager) ConfigFileForVersion(id string, version string) string {
	if id == "php" && version != "" {
		return m.phpIniPath(version)
	}
	return m.ConfigFileFor(id)
}

func installedApacheVersion(apacheRoot string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(apacheRoot)))
	re := regexp.MustCompile(`(\d+\.\d+(?:\.\d+)*)`)
	if match := re.FindStringSubmatch(base); len(match) >= 2 {
		return match[1]
	}
	return ""
}

func installedMySQLVersion(mysqlRoot string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(mysqlRoot)))
	re := regexp.MustCompile(`(\d+\.\d+(?:\.\d+)*)`)
	if match := re.FindStringSubmatch(base); len(match) >= 2 {
		return match[1]
	}
	return ""
}

func phpArchivePath(paths runtimePaths, version string) string {
	for _, release := range runtimePHPReleases {
		if release.Version == strings.TrimSpace(version) {
			return filepath.Join(paths.downloadsDir, release.FileName)
		}
	}
	return ""
}

func enableApacheModule(conf string, moduleName string, moduleFile string) string {
	pattern := regexp.MustCompile(`(?m)^#\s*LoadModule\s+` + regexp.QuoteMeta(moduleName) + `\s+modules/` + regexp.QuoteMeta(moduleFile) + `\s*$`)
	if pattern.MatchString(conf) {
		return pattern.ReplaceAllString(conf, `LoadModule `+moduleName+` modules/`+moduleFile)
	}

	enabled := regexp.MustCompile(`(?m)^LoadModule\s+` + regexp.QuoteMeta(moduleName) + `\s+modules/` + regexp.QuoteMeta(moduleFile) + `\s*$`)
	if enabled.MatchString(conf) {
		return conf
	}
	return conf + "\r\nLoadModule " + moduleName + " modules/" + moduleFile + "\r\n"
}

func apacheLogSafeName(value string) string {
	replacer := strings.NewReplacer(".", "-", ":", "-", "/", "-", "\\", "-", " ", "-")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(value)))
}

func quoteApachePath(path string) string {
	return `"` + toForwardPath(path) + `"`
}

func (m *windowsRuntimeManager) activeWebsitePHPVersions() ([]string, error) {
	sites, err := listWebsites(m.projectRoot)
	if err != nil {
		return nil, err
	}

	versionsMap := map[string]struct{}{}
	current := m.currentPHPVersion()
	if current != "" && fileExists(m.phpExe(current)) {
		versionsMap[current] = struct{}{}
	}

	for _, site := range sites {
		if strings.ToLower(strings.TrimSpace(site.Status)) != "running" {
			continue
		}
		version := strings.TrimSpace(site.PHPVersion)
		if version == "" || !fileExists(m.phpCgiExe(version)) {
			continue
		}
		versionsMap[version] = struct{}{}
	}

	versions := make([]string, 0, len(versionsMap))
	for version := range versionsMap {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions, nil
}

func (m *windowsRuntimeManager) writeApacheVHostConfig() error {
	sites, err := listWebsites(m.projectRoot)
	if err != nil {
		return err
	}

	for _, site := range sites {
		version := strings.TrimSpace(site.PHPVersion)
		siteConfigDir := getSiteConfigDir(m.projectRoot, site.Domain)
		if err := os.MkdirAll(siteConfigDir, 0o755); err != nil {
			return err
		}

		var builder strings.Builder
		builder.WriteString("# This file is generated by zPanel.\r\n")
		builder.WriteString("# Site: " + site.Domain + "\r\n")
		status := strings.ToLower(strings.TrimSpace(site.Status))
		switch {
		case version == "":
			builder.WriteString("# Disabled because the site does not have a PHP version assigned.\r\n")
		case status != "running":
			builder.WriteString("# Disabled because the site is stopped.\r\n")
		case !fileExists(m.phpCgiExe(version)):
			builder.WriteString("# Disabled because the selected PHP runtime is unavailable.\r\n")
		default:
			port := phpFastCGIPort(version)
			logName := apacheLogSafeName(site.Domain)
			docRoot := quoteApachePath(site.Path)
			builder.WriteString("\r\n")
			builder.WriteString("<VirtualHost 127.0.0.1:" + strconv.Itoa(apacheHTTPPort) + ">\r\n")
			builder.WriteString("    ServerName " + site.Domain + "\r\n")
			builder.WriteString("    DocumentRoot " + docRoot + "\r\n")
			builder.WriteString("    DirectoryIndex index.php index.html\r\n")
			builder.WriteString("    ErrorLog \"logs/" + logName + "-error.log\"\r\n")
			builder.WriteString("    CustomLog \"logs/" + logName + "-access.log\" common\r\n")
			builder.WriteString("    <Directory " + docRoot + ">\r\n")
			builder.WriteString("        Options FollowSymLinks ExecCGI\r\n")
			builder.WriteString("        AllowOverride All\r\n")
			builder.WriteString("        Require all granted\r\n")
			builder.WriteString("    </Directory>\r\n")
			builder.WriteString("    <Proxy \"fcgi://127.0.0.1:" + strconv.Itoa(port) + "/\" enablereuse=on max=10>\r\n")
			builder.WriteString("    </Proxy>\r\n")
			builder.WriteString("    ProxyFCGIBackendType GENERIC\r\n")
			builder.WriteString("    ProxyFCGISetEnvIf \"true\" SCRIPT_FILENAME \"%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}\"\r\n")
			builder.WriteString("    ProxyFCGISetEnvIf \"true\" PATH_TRANSLATED \"%{reqenv:DOCUMENT_ROOT}%{reqenv:SCRIPT_NAME}\"\r\n")
			builder.WriteString("    <FilesMatch \"\\.php$\">\r\n")
			builder.WriteString("        SetHandler \"proxy:fcgi://127.0.0.1:" + strconv.Itoa(port) + "/\"\r\n")
			builder.WriteString("    </FilesMatch>\r\n")
			builder.WriteString("</VirtualHost>\r\n")
		}

		if err := os.WriteFile(getSiteVHostConfigPath(m.projectRoot, site.Domain), []byte(builder.String()), 0o644); err != nil {
			return err
		}
	}

	return nil
}

func (m *windowsRuntimeManager) startPHPFastCGI(version string) error {
	version = strings.TrimSpace(version)
	if version == "" || !fileExists(m.phpCgiExe(version)) {
		return nil
	}

	port := phpFastCGIPort(version)
	if testTCPPort("127.0.0.1", port, 350*time.Millisecond) {
		return nil
	}

	if pid, _ := clearStalePIDFile(m.phpFastCGIPIDPath(version), "php-cgi.exe"); pid > 0 {
		stopProcessTree(pid)
		_ = waitForTCPPortClosed("127.0.0.1", port, 8*time.Second)
	} else if legacyPID, _ := clearStalePIDFile(m.legacyPHPFastCGIPIDPath(version), "php-cgi.exe"); legacyPID > 0 {
		stopProcessTree(legacyPID)
		_ = waitForTCPPortClosed("127.0.0.1", port, 8*time.Second)
	}

	phpRoot := m.phpRealRoot(version)
	env := append(os.Environ(), "PATH="+phpRoot+";"+os.Getenv("PATH"))
	cmd, err := startHiddenProcess(m.phpCgiExe(version), []string{"-b", "127.0.0.1:" + strconv.Itoa(port), "-c", m.phpIniPath(version)}, phpRoot, env)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.phpFastCGIPIDPath(version)), 0o755); err != nil {
		return err
	}
	_ = os.WriteFile(m.phpFastCGIPIDPath(version), []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
	_ = os.Remove(m.legacyPHPFastCGIPIDPath(version))

	if !waitForTCPPort("127.0.0.1", port, 12*time.Second) {
		return fmt.Errorf("php-cgi %s did not start on 127.0.0.1:%d", version, port)
	}
	return nil
}

func (m *windowsRuntimeManager) stopPHPFastCGI(version string) error {
	port := phpFastCGIPort(version)
	pid, _ := clearStalePIDFile(m.phpFastCGIPIDPath(version), "php-cgi.exe")
	if pid == 0 {
		pid, _ = clearStalePIDFile(m.legacyPHPFastCGIPIDPath(version), "php-cgi.exe")
	}
	if pid == 0 {
		pid, _ = getListeningPID(port)
	}
	if pid > 0 {
		stopProcessTree(pid)
	}
	_ = waitForTCPPortClosed("127.0.0.1", port, 8*time.Second)
	_ = os.Remove(m.phpFastCGIPIDPath(version))
	_ = os.Remove(m.legacyPHPFastCGIPIDPath(version))
	return nil
}

func (m *windowsRuntimeManager) stopAllPHPFastCGI() error {
	versions := m.getInstalledPHPVersions()
	for _, version := range versions {
		_ = m.stopPHPFastCGI(version)
	}
	return nil
}

func (m *windowsRuntimeManager) startRequiredPHPFastCGI() error {
	versions, err := m.activeWebsitePHPVersions()
	if err != nil {
		return err
	}
	for _, version := range versions {
		if err := m.startPHPFastCGI(version); err != nil {
			return err
		}
	}
	return nil
}

func (m *windowsRuntimeManager) startPHPFastCGIVersions(versions []string) error {
	for _, version := range uniqueSortedExtensions(versions) {
		if err := m.startPHPFastCGI(version); err != nil {
			return err
		}
	}
	return nil
}

func (m *windowsRuntimeManager) SyncWebsiteRouting() error {
	websiteRoutingSyncMu.Lock()
	defer websiteRoutingSyncMu.Unlock()

	currentVersion := m.currentPHPVersion()
	if currentVersion != "" && fileExists(m.httpdConfPath()) {
		if err := m.configureApache(currentVersion); err != nil {
			return err
		}
	} else if err := m.writeApacheVHostConfig(); err != nil {
		return err
	}
	if !fileExists(m.apacheExe()) || !testTCPPort("127.0.0.1", apacheHTTPPort, 350*time.Millisecond) {
		return nil
	}
	runningBeforeRestart := m.runningPHPVersions(m.getInstalledPHPVersions())
	if err := m.stopApache(); err != nil {
		return err
	}
	if err := m.startApache(); err != nil {
		return err
	}
	return m.startPHPFastCGIVersions(runningBeforeRestart)
}

func expandZipFresh(zipPath string, destination string) error {
	_ = os.RemoveAll(destination)
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}

	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		targetPath := filepath.Join(destination, file.Name)
		if !strings.HasPrefix(filepath.Clean(targetPath), filepath.Clean(destination)) {
			return fmt.Errorf("invalid zip path: %s", file.Name)
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return err
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}

		source, err := file.Open()
		if err != nil {
			return err
		}

		target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			source.Close()
			return err
		}

		if _, err := io.Copy(target, source); err != nil {
			target.Close()
			source.Close()
			return err
		}
		target.Close()
		source.Close()
	}

	return nil
}

func toForwardPath(path string) string {
	return strings.ReplaceAll(path, `\`, `/`)
}

func startHiddenProcess(exe string, args []string, dir string, env []string) (*exec.Cmd, error) {
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

func runHiddenProcess(exe string, args []string, dir string, env []string, wait bool) error {
	cmd, err := startHiddenProcess(exe, args, dir, env)
	if err != nil {
		return err
	}
	if !wait {
		return nil
	}
	return cmd.Wait()
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return 0, errors.New("empty pid file")
	}
	return strconv.Atoi(value)
}

func clearStalePIDFile(path string, expectedProcess string) (int, error) {
	pid, err := readPIDFile(path)
	if err != nil {
		return 0, nil
	}
	if processMatches(pid, expectedProcess) {
		return pid, nil
	}
	_ = os.Remove(path)
	return 0, nil
}

func processMatches(pid int, expectedProcess string) bool {
	cmd := exec.Command("tasklist.exe", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	line := strings.TrimSpace(string(output))
	if line == "" || strings.HasPrefix(line, "INFO:") {
		return false
	}
	line = strings.Trim(line, "\r\n")
	line = strings.Trim(line, `"`)
	name := strings.Split(line, `","`)[0]
	return strings.EqualFold(name, expectedProcess)
}

func stopProcess(pid int) {
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
}

func stopProcessTree(pid int) {
	cmd := exec.Command("taskkill.exe", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		stopProcess(pid)
	}
}

func getListeningPID(port int) (int, error) {
	cmd := exec.Command("netstat.exe", "-ano", "-p", "tcp")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	output, err := cmd.Output()
	if err != nil {
		return 0, err
	}

	pattern := regexp.MustCompile(`(?m)^\s*TCP\s+\S+:` + strconv.Itoa(port) + `\s+\S+\s+LISTENING\s+(\d+)\s*$`)
	match := pattern.FindSubmatch(output)
	if len(match) < 2 {
		return 0, nil
	}
	return strconv.Atoi(string(match[1]))
}

func testTCPPort(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func waitForTCPPort(host string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if testTCPPort(host, port, 350*time.Millisecond) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func waitForTCPPortClosed(host string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !testTCPPort(host, port, 350*time.Millisecond) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func (m *windowsRuntimeManager) StopAll() error {
	var errs []string
	if err := m.stopApache(); err != nil {
		errs = append(errs, fmt.Sprintf("apache: %v", err))
	}
	if err := m.stopMySQL(); err != nil {
		errs = append(errs, fmt.Sprintf("mysql: %v", err))
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *windowsRuntimeManager) RunStartupChecks() error {
	return m.ensureVCRuntime(nil)
}

func (m *windowsRuntimeManager) ensureVCRuntime(onProgress func(appProgressEvent)) error {
	system32 := filepath.Join(os.Getenv("WINDIR"), "System32")
	dllPath := filepath.Join(system32, "vcruntime140.dll")
	if onProgress != nil {
		onProgress(appProgressEvent{Percent: 6, Message: "Checking Microsoft Visual C++ Runtime..."})
	}

	currentVersion := ""
	if fileExists(dllPath) {
		// Use PowerShell to get the version reliably
		cmd := exec.Command("powershell", "-NoProfile", "-Command", "(Get-Item '"+dllPath+"').VersionInfo.ProductVersion")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		out, err := cmd.Output()
		if err == nil {
			currentVersion = strings.TrimSpace(string(out))
		}
	}

	// We need 14.44+ for PHP 8.4
	// Compare versions. A simple string compare might not be enough for all cases,
	// but for VCRuntime "14.44..." vs "14.38..." it works.
	needsUpdate := currentVersion == "" || versionLess(currentVersion, "14.44")

	if !needsUpdate {
		if onProgress != nil {
			onProgress(appProgressEvent{Percent: 8, Message: "Microsoft Visual C++ Runtime already installed."})
		}
		return nil
	}

	if onProgress != nil {
		onProgress(appProgressEvent{Percent: 7, Message: "Microsoft Visual C++ Runtime is missing or outdated. Downloading vc_redist.x64.exe..."})
	}

	redistURL := "https://aka.ms/vs/17/release/vc_redist.x64.exe"
	redistPath := filepath.Join(m.paths().downloadsDir, "vc_redist.x64.exe")

	if err := downloadFile(redistURL, redistPath, "Visual C++ Redistributable", 7, 12, onProgress); err != nil {
		return fmt.Errorf("failed to download redist: %w", err)
	}

	if onProgress != nil {
		onProgress(appProgressEvent{Percent: 13, Message: "Installing Microsoft Visual C++ Runtime (vc_redist.x64.exe)..."})
	}

	// Run silent install
	// This might require admin, but we'll try.
	// If it fails with "access denied", the user will have to run zPanel as admin.
	cmd := exec.Command(redistPath, "/quiet", "/install", "/norestart")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to install redist (may require Administrator): %w", err)
	}

	if onProgress != nil {
		onProgress(appProgressEvent{Percent: 15, Message: "Microsoft Visual C++ Runtime installed."})
	}
	return nil
}

func versionLess(v1, v2 string) bool {
	p1 := strings.Split(v1, ".")
	p2 := strings.Split(v2, ".")
	for i := 0; i < len(p1) && i < len(p2); i++ {
		n1, _ := strconv.Atoi(p1[i])
		n2, _ := strconv.Atoi(p2[i])
		if n1 < n2 {
			return true
		}
		if n1 > n2 {
			return false
		}
	}
	return len(p1) < len(p2)
}
