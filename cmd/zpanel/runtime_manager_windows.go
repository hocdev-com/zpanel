//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
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

const mysqlInitializeTimeout = 2 * time.Minute

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

const apacheHTTPPort = 80
const phpMyAdminMountPath = "/phpmyadmin/"

var apacheHTTPFallbackPorts = []int{8081, 8088}

var defaultPHPExtensions = []string{"mysqli", "pdo_mysql", "mbstring", "openssl", "curl"}
var websiteRoutingSyncMu sync.Mutex

type windowsRuntimeManager struct {
	projectRoot string
}

type runtimePaths struct {
	projectRoot       string
	dataRoot          string
	runtimeRoot       string
	downloadsDir      string
	apacheExtractDir  string
	phpRoot           string
	mysqlExtractDir   string
	mysqlDataDir      string
	mysqlTempDir      string
	phpMyAdminDir     string
	phpMyAdminTempDir string
	myIniPath         string
	apacheZip         string
	phpZip            string
	mysqlZip          string
	phpMyAdminZip     string
}

type runtimeInstallPlan struct {
	needApache     bool
	needPHP        bool
	needMySQL      bool
	needPHPMyAdmin bool
}

type runtimeSelection struct {
	Apache     runtimeRelease
	PHP        runtimeRelease
	MySQL      runtimeRelease
	PHPMyAdmin runtimeRelease
}

type installedRuntimeVersions map[string]string

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

	response, err := m.autoStartAfterInstall(appID, version)
	if err != nil {
		return runtimeAppsResponse{}, err
	}
	return response, nil
}

func (m *windowsRuntimeManager) Start(appID string) (runtimeAppsResponse, error) {
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
		if !m.appInstalled("apache") {
			return runtimeAppsResponse{}, errors.New("application is not installed yet. Use Install first.")
		}
		if err := validateSingleRuntimeVersion("Apache", m.installedApacheVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		if err := m.startApache(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "php":
		if !m.appInstalled(appID) {
			return runtimeAppsResponse{}, errors.New("application is not installed yet. Use Install first.")
		}
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
		if !m.appInstalled("mysql") {
			return runtimeAppsResponse{}, errors.New("application is not installed yet. Use Install first.")
		}
		if err := validateSingleRuntimeVersion("MySQL", m.installedMySQLVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		if err := m.startMySQL(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "phpmyadmin":
		if !m.appInstalled("phpmyadmin") {
			return runtimeAppsResponse{}, errors.New("application is not installed yet. Use Install first.")
		}
		if err := validateSingleRuntimeVersion("phpMyAdmin", m.installedPHPMyAdminVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		if err := m.startPHPMyAdmin(); err != nil {
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
		if err := validateSingleRuntimeVersion("Apache", m.installedApacheVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
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
		if err := validateSingleRuntimeVersion("MySQL", m.installedMySQLVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		if err := m.stopMySQL(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "phpmyadmin":
		if err := validateSingleRuntimeVersion("phpMyAdmin", m.installedPHPMyAdminVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		return runtimeAppsResponse{}, errors.New("phpmyadmin is served by Apache and cannot be stopped separately")
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
		if err := validateSingleRuntimeVersion("Apache", m.installedApacheVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		if err := m.uninstallApache(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "php":
		if err := m.uninstallPHP(version); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "mysql":
		if err := validateSingleRuntimeVersion("MySQL", m.installedMySQLVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		if err := m.uninstallMySQL(); err != nil {
			return runtimeAppsResponse{}, err
		}
	case "phpmyadmin":
		if err := validateSingleRuntimeVersion("phpMyAdmin", m.installedPHPMyAdminVersionResolved(), version); err != nil {
			return runtimeAppsResponse{}, err
		}
		if err := m.uninstallPHPMyAdmin(); err != nil {
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
	dataRoot := filepath.Join(m.projectRoot, "data")
	runtimeRoot := filepath.Join(dataRoot, "runtime")
	return runtimePaths{
		projectRoot:       m.projectRoot,
		dataRoot:          dataRoot,
		runtimeRoot:       runtimeRoot,
		downloadsDir:      filepath.Join(dataRoot, "downloads"),
		apacheExtractDir:  filepath.Join(runtimeRoot, "apache-dist"),
		phpRoot:           filepath.Join(runtimeRoot, "php"), // Base PHP dir
		mysqlExtractDir:   filepath.Join(runtimeRoot, "mysql-dist"),
		mysqlDataDir:      filepath.Join(runtimeRoot, "mysql-dist", "data"),
		mysqlTempDir:      filepath.Join(runtimeRoot, "mysql-dist", "tmp"),
		phpMyAdminDir:     filepath.Join(runtimeRoot, "phpmyadmin"),
		phpMyAdminTempDir: filepath.Join(runtimeRoot, "tmp", "phpmyadmin"),
		myIniPath:         filepath.Join(runtimeRoot, "mysql-dist", "my.ini"),
		apacheZip:         filepath.Join(dataRoot, "downloads", runtimeApacheReleases[0].FileName),
		phpZip:            filepath.Join(dataRoot, "downloads", runtimePHPReleases[0].FileName),
		mysqlZip:          filepath.Join(dataRoot, "downloads", runtimeMySQLReleases[0].FileName),
		phpMyAdminZip:     filepath.Join(dataRoot, "downloads", "phpmyadmin.zip"),
	}
}

func (m *windowsRuntimeManager) apacheRoot() string {
	return filepath.Join(m.paths().apacheExtractDir, "Apache24")
}

func (m *windowsRuntimeManager) mysqlRoot() string {
	root := m.paths().mysqlExtractDir
	entries, err := os.ReadDir(root)
	if err != nil {
		return root
	}

	candidates := make([]string, 0, len(entries))
	binCandidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(root, entry.Name())
		if fileExists(filepath.Join(candidate, "bin", "mysqld.exe")) {
			return candidate
		}
		if fileExists(filepath.Join(candidate, "bin")) || strings.HasPrefix(strings.ToLower(entry.Name()), "mysql-") {
			binCandidates = append(binCandidates, candidate)
		}
		candidates = append(candidates, candidate)
	}

	if len(binCandidates) == 1 {
		return binCandidates[0]
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return root
}

func (m *windowsRuntimeManager) phpMyAdminRoot() string {
	root, _ := resolveSingleChildDirectory(m.paths().phpMyAdminDir)
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

func (m *windowsRuntimeManager) phpMyAdminConfigPath() string {
	return filepath.Join(m.phpMyAdminRoot(), "config.inc.php")
}

func (m *windowsRuntimeManager) installedApacheVersionResolved() string {
	if version := strings.TrimSpace(m.installedVersionFromMetadata("apache")); version != "" {
		return version
	}
	return strings.TrimSpace(installedApacheVersion(m.apacheRoot()))
}

func (m *windowsRuntimeManager) installedMySQLVersionResolved() string {
	if version := strings.TrimSpace(m.installedVersionFromMetadata("mysql")); version != "" {
		return version
	}
	return strings.TrimSpace(installedMySQLVersion(m.mysqlRoot()))
}

func (m *windowsRuntimeManager) installedPHPMyAdminVersionResolved() string {
	if version := strings.TrimSpace(m.installedVersionFromMetadata("phpmyadmin")); version != "" {
		return version
	}

	base := strings.ToLower(filepath.Base(filepath.Clean(m.phpMyAdminRoot())))
	re := regexp.MustCompile(`(\d+\.\d+(?:\.\d+)*)`)
	if match := re.FindStringSubmatch(base); len(match) >= 2 {
		return strings.TrimSpace(match[1])
	}
	return ""
}

func validateSingleRuntimeVersion(appLabel string, installedVersion string, requestedVersion string) error {
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return nil
	}
	installedVersion = strings.TrimSpace(installedVersion)
	if installedVersion == "" {
		return fmt.Errorf("%s is not installed", appLabel)
	}
	if installedVersion != requestedVersion {
		return fmt.Errorf("%s %s is not installed. Installed version is %s", appLabel, requestedVersion, installedVersion)
	}
	return nil
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
		if version != "" {
			return fileExists(m.apacheExe()) && strings.TrimSpace(m.installedVersionFromMetadata("apache")) == version
		}
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
		if version != "" {
			return fileExists(m.mysqlExe()) && strings.TrimSpace(m.installedVersionFromMetadata("mysql")) == version
		}
		return fileExists(m.mysqlExe())
	case "phpmyadmin":
		if version != "" {
			return fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php")) && strings.TrimSpace(m.installedVersionFromMetadata("phpmyadmin")) == version
		}
		return fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php"))
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

func (m *windowsRuntimeManager) configuredHTTPPort() int {
	confBytes, err := os.ReadFile(m.httpdConfPath())
	if err != nil {
		return 0
	}

	match := regexp.MustCompile(`(?im)^\s*Listen\s+(?:127\.0\.0\.1:)?(\d+)\s*$`).FindStringSubmatch(string(confBytes))
	if len(match) < 2 {
		return 0
	}

	port, _ := strconv.Atoi(strings.TrimSpace(match[1]))
	return port
}

func apachePortCandidates() []int {
	ports := []int{apacheHTTPPort}
	ports = append(ports, apacheHTTPFallbackPorts...)
	return ports
}

func (m *windowsRuntimeManager) selectApacheHTTPPort() int {
	for _, port := range apachePortCandidates() {
		if !testTCPPort("127.0.0.1", port, 120*time.Millisecond) {
			return port
		}
	}

	if configured := m.configuredHTTPPort(); configured > 0 {
		return configured
	}

	return apacheHTTPPort
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

type runtimeDependencyState struct {
	Dependencies        []string
	MissingDependencies []string
	Message             string
}

type runtimeCompatibilitySummary struct {
	ApacheVersions     []string
	PHPVersions        []string
	MySQLVersions      []string
	PHPMyAdminVersions []string
}

func phpMyAdminSupportsPHPVersion(version string) bool {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return false
	}

	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return false
	}

	if major < 7 {
		return false
	}
	if major == 7 {
		return minor >= 2
	}
	if major == 8 {
		return minor < 4
	}
	return false
}

func phpMyAdminSupportsPHPVersionForRelease(phpMyAdminVersion string, phpVersion string) bool {
	phpMyAdminVersion = strings.TrimSpace(phpMyAdminVersion)
	if compareVersionStrings(phpMyAdminVersion, "6.0.0") >= 0 {
		return compareVersionStrings(strings.TrimSpace(phpVersion), "8.1.0") >= 0
	}
	return phpMyAdminSupportsPHPVersion(phpVersion)
}

func phpMyAdminSupportsMySQLVersion(phpMyAdminVersion string, mysqlVersion string) bool {
	phpMyAdminVersion = strings.TrimSpace(phpMyAdminVersion)
	mysqlVersion = strings.TrimSpace(mysqlVersion)
	if phpMyAdminVersion == "" || mysqlVersion == "" {
		return false
	}
	if compareVersionStrings(phpMyAdminVersion, "6.0.0") >= 0 {
		return compareVersionStrings(mysqlVersion, "8.0.0") >= 0
	}
	return compareVersionStrings(mysqlVersion, "8.0.0") >= 0
}

func runtimeVersionsMatching(releases []runtimeRelease, match func(string) bool) []string {
	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		version := strings.TrimSpace(release.Version)
		if version == "" {
			continue
		}
		if match != nil && !match(version) {
			continue
		}
		versions = append(versions, version)
	}
	return versions
}

func uniqueSortedVersionsDesc(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	versions := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		versions = append(versions, value)
	}
	sort.Slice(versions, func(i, j int) bool {
		return compareVersionStrings(versions[i], versions[j]) > 0
	})
	return versions
}

func formatVersionList(versions []string) string {
	versions = uniqueSortedVersionsDesc(versions)
	if len(versions) == 0 {
		return ""
	}
	return strings.Join(versions, ", ")
}

func formatCompatibilityMessage(summary runtimeCompatibilitySummary) string {
	parts := make([]string, 0, 4)
	if versions := formatVersionList(summary.ApacheVersions); versions != "" {
		parts = append(parts, "Apache "+versions)
	}
	if versions := formatVersionList(summary.PHPVersions); versions != "" {
		parts = append(parts, "PHP "+versions)
	}
	if versions := formatVersionList(summary.MySQLVersions); versions != "" {
		parts = append(parts, "MySQL "+versions)
	}
	if versions := formatVersionList(summary.PHPMyAdminVersions); versions != "" {
		parts = append(parts, "phpMyAdmin "+versions)
	}
	if len(parts) == 0 {
		return ""
	}
	return "Compatible with " + strings.Join(parts, "; ") + "."
}

func containsString(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func compareVersionStrings(left string, right string) int {
	leftParts := strings.Split(strings.TrimSpace(left), ".")
	rightParts := strings.Split(strings.TrimSpace(right), ".")
	limit := len(leftParts)
	if len(rightParts) > limit {
		limit = len(rightParts)
	}

	for i := 0; i < limit; i++ {
		leftValue := 0
		rightValue := 0
		if i < len(leftParts) {
			leftValue, _ = strconv.Atoi(leftParts[i])
		}
		if i < len(rightParts) {
			rightValue, _ = strconv.Atoi(rightParts[i])
		}
		switch {
		case leftValue < rightValue:
			return -1
		case leftValue > rightValue:
			return 1
		}
	}
	return 0
}

func (m *windowsRuntimeManager) compatibilitySummaryForApp(appID string, version string, apacheReleases []runtimeRelease, phpReleases []runtimeRelease, mysqlReleases []runtimeRelease, phpMyAdminReleases []runtimeRelease) runtimeCompatibilitySummary {
	version = strings.TrimSpace(version)
	switch strings.ToLower(strings.TrimSpace(appID)) {
	case "mysql":
		return runtimeCompatibilitySummary{
			ApacheVersions:     runtimeVersionsMatching(apacheReleases, nil),
			PHPVersions:        runtimeVersionsMatching(phpReleases, nil),
			PHPMyAdminVersions: runtimeVersionsMatching(phpMyAdminReleases, func(candidate string) bool { return phpMyAdminSupportsMySQLVersion(candidate, version) }),
		}
	case "phpmyadmin":
		return runtimeCompatibilitySummary{
			ApacheVersions: runtimeVersionsMatching(apacheReleases, nil),
			PHPVersions: runtimeVersionsMatching(phpReleases, func(candidate string) bool {
				return phpMyAdminSupportsPHPVersionForRelease(version, candidate)
			}),
			MySQLVersions: runtimeVersionsMatching(mysqlReleases, func(candidate string) bool {
				return phpMyAdminSupportsMySQLVersion(version, candidate)
			}),
		}
	case "php":
		return runtimeCompatibilitySummary{
			ApacheVersions: runtimeVersionsMatching(apacheReleases, nil),
			MySQLVersions:  runtimeVersionsMatching(mysqlReleases, nil),
			PHPMyAdminVersions: runtimeVersionsMatching(phpMyAdminReleases, func(candidate string) bool {
				return phpMyAdminSupportsPHPVersionForRelease(candidate, version)
			}),
		}
	case "apache":
		return runtimeCompatibilitySummary{
			PHPVersions:        runtimeVersionsMatching(phpReleases, nil),
			MySQLVersions:      runtimeVersionsMatching(mysqlReleases, nil),
			PHPMyAdminVersions: runtimeVersionsMatching(phpMyAdminReleases, nil),
		}
	default:
		return runtimeCompatibilitySummary{}
	}
}

func (m *windowsRuntimeManager) compatibilityMessageForApp(appID string, version string, apacheReleases []runtimeRelease, phpReleases []runtimeRelease, mysqlReleases []runtimeRelease, phpMyAdminReleases []runtimeRelease) string {
	return formatCompatibilityMessage(m.compatibilitySummaryForApp(appID, version, apacheReleases, phpReleases, mysqlReleases, phpMyAdminReleases))
}

func (m *windowsRuntimeManager) phpMyAdminCompatiblePHPVersions(installed []string, phpMyAdminVersion string) []string {
	compatible := make([]string, 0, len(installed))
	current := strings.TrimSpace(m.currentPHPVersion())
	if current != "" && phpMyAdminSupportsPHPVersionForRelease(phpMyAdminVersion, current) && fileExists(m.phpCgiExe(current)) {
		compatible = append(compatible, current)
	}
	for _, version := range installed {
		version = strings.TrimSpace(version)
		if version == "" || !phpMyAdminSupportsPHPVersionForRelease(phpMyAdminVersion, version) || !fileExists(m.phpCgiExe(version)) {
			continue
		}
		compatible = append(compatible, version)
	}
	return uniqueSortedVersionsDesc(compatible)
}

func (m *windowsRuntimeManager) phpMyAdminCompatiblePHPVersion(installed []string, phpMyAdminVersion string) string {
	compatible := m.phpMyAdminCompatiblePHPVersions(installed, phpMyAdminVersion)
	if len(compatible) == 0 {
		return ""
	}
	return compatible[0]
}

func (m *windowsRuntimeManager) phpMyAdminDependencyState(apacheInstalled bool, apacheRunning bool, apacheVersion string, mysqlInstalled bool, mysqlRunning bool, mysqlVersion string, installedPHPs []string, runningPHPs []string, phpMyAdminVersion string, apacheReleases []runtimeRelease, mysqlReleases []runtimeRelease, phpReleases []runtimeRelease) runtimeDependencyState {
	compatibility := m.compatibilitySummaryForApp("phpmyadmin", phpMyAdminVersion, apacheReleases, phpReleases, mysqlReleases, nil)
	state := runtimeDependencyState{
		Dependencies: []string{
			"Apache " + formatVersionList(compatibility.ApacheVersions),
			"MySQL " + formatVersionList(compatibility.MySQLVersions),
			"PHP " + formatVersionList(compatibility.PHPVersions),
		},
	}

	switch {
	case !apacheInstalled:
		state.MissingDependencies = append(state.MissingDependencies, "Install Apache")
	case apacheVersion != "" && len(compatibility.ApacheVersions) > 0 && !containsString(compatibility.ApacheVersions, apacheVersion):
		state.MissingDependencies = append(state.MissingDependencies, "Switch Apache to "+formatVersionList(compatibility.ApacheVersions))
	case !apacheRunning:
		state.MissingDependencies = append(state.MissingDependencies, "Start Apache")
	}

	switch {
	case !mysqlInstalled:
		state.MissingDependencies = append(state.MissingDependencies, "Install MySQL")
	case mysqlVersion != "" && len(compatibility.MySQLVersions) > 0 && !containsString(compatibility.MySQLVersions, mysqlVersion):
		state.MissingDependencies = append(state.MissingDependencies, "Switch MySQL to "+formatVersionList(compatibility.MySQLVersions))
	case !mysqlRunning:
		state.MissingDependencies = append(state.MissingDependencies, "Start MySQL")
	}

	compatibleInstalledPHPs := m.phpMyAdminCompatiblePHPVersions(installedPHPs, phpMyAdminVersion)
	switch {
	case len(compatibleInstalledPHPs) == 0:
		state.MissingDependencies = append(state.MissingDependencies, "Install or switch PHP to "+formatVersionList(compatibility.PHPVersions))
	default:
		runningCompatiblePHPs := make([]string, 0, len(runningPHPs))
		for _, version := range runningPHPs {
			if containsString(compatibleInstalledPHPs, version) {
				runningCompatiblePHPs = append(runningCompatiblePHPs, version)
			}
		}
		if len(runningCompatiblePHPs) == 0 {
			state.MissingDependencies = append(state.MissingDependencies, "Start PHP "+compatibleInstalledPHPs[0])
		}
	}

	compatibilityMessage := formatCompatibilityMessage(compatibility)
	if len(state.MissingDependencies) == 0 {
		state.Message = compatibilityMessage
		return state
	}

	if compatibilityMessage == "" {
		state.Message = "Requires a compatible Apache, MySQL, and PHP stack. Missing: " + strings.Join(state.MissingDependencies, "; ") + "."
		return state
	}
	state.Message = "Requires a compatible Apache, MySQL, and PHP stack. Missing: " + strings.Join(state.MissingDependencies, "; ") + ". " + compatibilityMessage
	return state
}

func (m *windowsRuntimeManager) apacheConfigHasPHPMyAdminAlias() bool {
	content, err := os.ReadFile(m.httpdConfPath())
	if err != nil {
		return false
	}
	return strings.Contains(string(content), "# zPanel Local Tools BEGIN")
}

func (m *windowsRuntimeManager) apacheConfigHasPHPMyAdminPort(port int) bool {
	content, err := os.ReadFile(m.httpdConfPath())
	if err != nil {
		return false
	}
	needle := `SetHandler "proxy:fcgi://127.0.0.1:` + strconv.Itoa(port) + `//./"`
	return strings.Contains(string(content), "# zPanel Local Tools BEGIN") && strings.Contains(string(content), needle)
}

func (m *windowsRuntimeManager) listApps() []runtimeApp {
	apacheReleases := appStoreEffectiveReleases(m.projectRoot, "apache")
	phpReleases := appStoreEffectiveReleases(m.projectRoot, "php")
	mysqlReleases := appStoreEffectiveReleases(m.projectRoot, "mysql")
	phpMyAdminReleases := appStoreEffectiveReleases(m.projectRoot, "phpmyadmin")
	apacheInstalled := fileExists(m.apacheExe())
	mysqlInstalled := fileExists(m.mysqlExe())
	phpMyAdminInstalled := fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php"))
	apacheVersion := versionOrDefault(m.installedVersionFromMetadata("apache"), firstReleaseVersion(apacheReleases))
	mysqlVersion := versionOrDefault(m.installedVersionFromMetadata("mysql"), firstReleaseVersion(mysqlReleases))
	phpMyAdminVersion := versionOrDefault(m.installedVersionFromMetadata("phpmyadmin"), firstReleaseVersion(phpMyAdminReleases))
	apachePort := m.configuredHTTPPort()
	if apachePort == 0 {
		apachePort = apacheHTTPPort
	}

	var apacheRunning, mysqlRunning bool
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if apacheInstalled {
			apacheRunning = apacheListeningOnPort(apachePort)
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
		newRuntimeApp("apache", "Apache", apacheVersion, availableVersions(apacheReleases), "Portable web server stored in data/runtime.", apacheInstallPath, apacheInstalled, apacheRunning, strconv.Itoa(apachePort), formatPortURL("127.0.0.1", apachePort, "/"), releaseURLs(apacheReleases), apacheInstalled && !apacheRunning, apacheInstalled && apacheRunning, apacheRunning, apacheInstalled),
	}
	apps[0].CompatibilityMessage = m.compatibilityMessageForApp("apache", apacheVersion, apacheReleases, phpReleases, mysqlReleases, phpMyAdminReleases)

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
		phpURL = formatPortURL("127.0.0.1", apachePort, "/phpinfo.php")
	}
	phpApp := newRuntimeApp("php", "PHP", mainPHPVersion, availableVersions(phpReleases), "Portable PHP runtime. Multiple versions supported per website.", phpInstallPath, phpInstalled, phpRunning, "9000+", phpURL, releaseURLs(phpReleases), phpInstalled && !phpRunning, phpRunning, apacheRunning, phpInstalled)
	phpApp.InstalledVersions = installedPHPs
	phpApp.RunningVersions = runningPHPs
	phpApp.CompatibilityMessage = m.compatibilityMessageForApp("php", mainPHPVersion, apacheReleases, phpReleases, mysqlReleases, phpMyAdminReleases)
	apps = append(apps, phpApp)

	mysqlApp := newRuntimeApp("mysql", "MySQL", mysqlVersion, availableVersions(mysqlReleases), "Portable MySQL server stored in data/runtime.", mysqlInstallPath, mysqlInstalled, mysqlRunning, "3307", "", releaseURLs(mysqlReleases), mysqlInstalled && !mysqlRunning, mysqlInstalled && mysqlRunning, false, mysqlInstalled)
	mysqlApp.CompatibilityMessage = m.compatibilityMessageForApp("mysql", mysqlVersion, apacheReleases, phpReleases, mysqlReleases, phpMyAdminReleases)
	apps = append(apps, mysqlApp)

	phpMyAdminDeps := m.phpMyAdminDependencyState(apacheInstalled, apacheRunning, apacheVersion, mysqlInstalled, mysqlRunning, mysqlVersion, installedPHPs, runningPHPs, phpMyAdminVersion, apacheReleases, mysqlReleases, phpReleases)
	phpMyAdminReady := phpMyAdminInstalled &&
		len(phpMyAdminDeps.MissingDependencies) == 0 &&
		m.apacheConfigHasPHPMyAdminAlias() &&
		fileExists(filepath.Join(m.phpMyAdminPublicPath(), "index.php"))
	phpMyAdminInstallPath := ""
	if phpMyAdminInstalled {
		phpMyAdminInstallPath = m.phpMyAdminRoot()
	}
	phpMyAdminURL := ""
	if phpMyAdminInstalled {
		phpMyAdminURL = formatPortURL("127.0.0.1", apachePort, phpMyAdminMountPath)
	}
	if phpMyAdminInstalled || len(phpMyAdminReleases) > 0 {
		phpMyAdminApp := newRuntimeApp("phpmyadmin", "phpMyAdmin", phpMyAdminVersion, availableVersions(phpMyAdminReleases), "phpMyAdmin web client served through the bundled Apache and PHP stack.", phpMyAdminInstallPath, phpMyAdminInstalled, phpMyAdminReady, strconv.Itoa(apachePort), phpMyAdminURL, releaseURLs(phpMyAdminReleases), phpMyAdminInstalled && !phpMyAdminReady && len(phpMyAdminDeps.MissingDependencies) == 0, false, phpMyAdminReady, phpMyAdminInstalled)
		phpMyAdminApp.Dependencies = append([]string(nil), phpMyAdminDeps.Dependencies...)
		phpMyAdminApp.MissingDependencies = append([]string(nil), phpMyAdminDeps.MissingDependencies...)
		phpMyAdminApp.DependencyMessage = phpMyAdminDeps.Message
		phpMyAdminApp.CompatibilityMessage = m.compatibilityMessageForApp("phpmyadmin", phpMyAdminVersion, apacheReleases, phpReleases, mysqlReleases, phpMyAdminReleases)
		if phpMyAdminInstalled && len(phpMyAdminDeps.MissingDependencies) > 0 {
			phpMyAdminApp.Status = "stopped"
			phpMyAdminApp.StatusLabel = "Required"
		}
		apps = append(apps, phpMyAdminApp)
	}

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
	case "phpmyadmin":
		return runtimeInstallPlan{needPHPMyAdmin: true}
	case "stack":
		return runtimeInstallPlan{needApache: true, needPHP: true, needMySQL: true}
	default:
		return runtimeInstallPlan{needApache: true, needPHP: true, needMySQL: true}
	}
}

func (m *windowsRuntimeManager) ensureProvisioned(plan runtimeInstallPlan, selection runtimeSelection, onProgress func(appProgressEvent)) error {
	paths := m.paths()
	if err := m.normalizeRuntimeLayout(); err != nil {
		return err
	}
	if plan.needApache {
		if err := validateSingleVersionInstall("Apache", m.installedApacheVersionResolved(), selection.Apache.Version, fileExists(m.apacheExe()), apacheListeningOnPort(m.configuredHTTPPortOrDefault())); err != nil {
			return err
		}
	}
	if plan.needMySQL {
		if err := validateSingleVersionInstall("MySQL", m.installedMySQLVersionResolved(), selection.MySQL.Version, fileExists(m.mysqlExe()), testTCPPort("127.0.0.1", 3307, 120*time.Millisecond)); err != nil {
			return err
		}
	}
	if plan.needPHPMyAdmin {
		if err := validateSingleVersionInstall("phpMyAdmin", m.installedPHPMyAdminVersionResolved(), selection.PHPMyAdmin.Version, fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php")), false); err != nil {
			return err
		}
	}
	dirs := []string{paths.runtimeRoot, paths.downloadsDir}
	if plan.needMySQL {
		dirs = append(dirs, paths.mysqlDataDir, paths.mysqlTempDir)
	}
	if plan.needPHPMyAdmin {
		dirs = append(dirs, paths.phpMyAdminDir, paths.phpMyAdminTempDir)
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
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
	if plan.needPHPMyAdmin {
		paths.phpMyAdminZip = filepath.Join(paths.downloadsDir, selection.PHPMyAdmin.FileName)
		if err := downloadFile(selection.PHPMyAdmin.URL, paths.phpMyAdminZip, "phpMyAdmin", 70, 78, onProgress); err != nil {
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
	if plan.needPHPMyAdmin && !fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php")) {
		reportProgress(onProgress, 92, "Extracting...")
		if err := expandZipFresh(paths.phpMyAdminZip, paths.phpMyAdminDir); err != nil {
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
		if err := m.configureApache(selection.PHP.Version, m.selectApacheHTTPPort()); err != nil {
			return err
		}
	}
	if plan.needMySQL {
		if err := m.configureMySQL(); err != nil {
			return err
		}
	}
	if plan.needPHPMyAdmin {
		if err := m.ensurePHPMyAdminConfig(); err != nil {
			return err
		}
		if err := m.ensurePHPMyAdminPublicPath(); err != nil {
			return err
		}
		if fileExists(m.httpdConfPath()) {
			httpPort := m.configuredHTTPPort()
			if httpPort == 0 {
				httpPort = apacheHTTPPort
			}
			if err := m.configureApache(m.currentPHPVersion(), httpPort); err != nil {
				return err
			}
		}
	}
	return m.recordInstalledVersions(plan, selection)
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

func firstReleaseVersion(releases []runtimeRelease) string {
	if len(releases) == 0 {
		return ""
	}
	return strings.TrimSpace(releases[0].Version)
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

func validateSingleVersionInstall(appLabel string, installedVersion string, selectedVersion string, installed bool, running bool) error {
	if !installed {
		return nil
	}
	installedVersion = strings.TrimSpace(installedVersion)
	selectedVersion = strings.TrimSpace(selectedVersion)
	if selectedVersion == "" || installedVersion == "" || installedVersion == selectedVersion {
		return nil
	}
	if running {
		return fmt.Errorf("%s %s is running. Uninstall it before installing %s %s", appLabel, installedVersion, appLabel, selectedVersion)
	}
	return fmt.Errorf("%s %s is already installed. Uninstall it before installing %s %s", appLabel, installedVersion, appLabel, selectedVersion)
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
	phpMyAdminReleases := appStoreEffectiveReleases(m.projectRoot, "phpmyadmin")

	selection := runtimeSelection{
		Apache: apacheReleases[0],
		PHP:    phpReleases[0],
		MySQL:  mysqlReleases[0],
	}
	if len(phpMyAdminReleases) > 0 {
		selection.PHPMyAdmin = phpMyAdminReleases[0]
	}

	var err error
	switch baseID {
	case "apache":
		selection.Apache, err = pickRelease(apacheReleases, version)
	case "php":
		selection.PHP, err = pickRelease(phpReleases, version)
	case "mysql":
		selection.MySQL, err = pickRelease(mysqlReleases, version)
	case "phpmyadmin":
		if len(phpMyAdminReleases) == 0 {
			err = errors.New("phpmyadmin is not configured in panel.db")
			break
		}
		selection.PHPMyAdmin, err = pickRelease(phpMyAdminReleases, version)
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

func moveDirectoryContents(srcDir string, dstDir string) error {
	if !fileExists(srcDir) {
		return nil
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())
		if fileExists(dstPath) {
			continue
		}
		if err := os.Rename(srcPath, dstPath); err != nil {
			return err
		}
	}
	return os.Remove(srcDir)
}

func (m *windowsRuntimeManager) normalizeRuntimeLayout() error {
	paths := m.paths()
	if err := os.MkdirAll(paths.dataRoot, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.runtimeRoot, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.downloadsDir, 0o755); err != nil {
		return err
	}

	legacyDownloads := filepath.Join(paths.runtimeRoot, "downloads")
	legacyMySQLTemp := filepath.Join(paths.runtimeRoot, "mysql-tmp")
	legacyMySQLData := filepath.Join(paths.runtimeRoot, "mysql-data")
	legacyMyIni := filepath.Join(paths.runtimeRoot, "my.ini")
	legacyPHPMyAdminTemp := filepath.Join(paths.runtimeRoot, "phpmyadmin-tmp")

	if err := moveDirectoryContents(legacyDownloads, paths.downloadsDir); err != nil {
		return err
	}
	if err := moveDirectoryContents(legacyMySQLTemp, paths.mysqlTempDir); err != nil {
		return err
	}
	if err := moveDirectoryContents(legacyMySQLData, paths.mysqlDataDir); err != nil {
		return err
	}
	if err := moveDirectoryContents(legacyPHPMyAdminTemp, paths.phpMyAdminTempDir); err != nil {
		return err
	}
	if fileExists(legacyMyIni) && !fileExists(paths.myIniPath) {
		if err := os.MkdirAll(filepath.Dir(paths.myIniPath), 0o755); err != nil {
			return err
		}
		if err := os.Rename(legacyMyIni, paths.myIniPath); err != nil {
			return err
		}
	}
	return nil
}

func (m *windowsRuntimeManager) loadInstalledVersionsMetadata() installedRuntimeVersions {
	content, err := os.ReadFile(m.installedVersionsPath())
	if err != nil {
		return installedRuntimeVersions{}
	}

	var metadata installedRuntimeVersions
	if err := json.Unmarshal(content, &metadata); err != nil {
		return installedRuntimeVersions{}
	}
	if metadata == nil {
		return installedRuntimeVersions{}
	}
	return metadata
}

func (m *windowsRuntimeManager) saveInstalledVersionsMetadata(metadata installedRuntimeVersions) error {
	cleaned := installedRuntimeVersions{}
	for appID, version := range metadata {
		appID = strings.ToLower(strings.TrimSpace(appID))
		version = strings.TrimSpace(version)
		if appID == "" || version == "" {
			continue
		}
		cleaned[appID] = version
	}

	if len(cleaned) == 0 {
		if err := os.Remove(m.installedVersionsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(m.installedVersionsPath()), 0o755); err != nil {
		return err
	}

	content, err := json.Marshal(cleaned)
	if err != nil {
		return err
	}
	return os.WriteFile(m.installedVersionsPath(), content, 0o644)
}

func (m *windowsRuntimeManager) updateInstalledVersionsMetadata(update func(installedRuntimeVersions)) error {
	metadata := m.loadInstalledVersionsMetadata()
	if metadata == nil {
		metadata = installedRuntimeVersions{}
	}
	update(metadata)
	return m.saveInstalledVersionsMetadata(metadata)
}

func (m *windowsRuntimeManager) installedVersionFromMetadata(appID string) string {
	appID = strings.ToLower(strings.TrimSpace(appID))
	version := strings.TrimSpace(m.loadInstalledVersionsMetadata()[appID])
	if version == "" {
		return ""
	}
	switch appID {
	case "apache":
		if !fileExists(m.apacheExe()) {
			return ""
		}
	case "mysql":
		if !fileExists(m.mysqlExe()) {
			return ""
		}
	case "phpmyadmin":
		if !fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php")) {
			return ""
		}
	}
	return version
}

func (m *windowsRuntimeManager) recordInstalledVersions(plan runtimeInstallPlan, selection runtimeSelection) error {
	return m.updateInstalledVersionsMetadata(func(metadata installedRuntimeVersions) {
		for key := range metadata {
			if key == "php" || strings.HasPrefix(key, "php:") {
				delete(metadata, key)
			}
		}
		if plan.needApache && fileExists(m.apacheExe()) {
			metadata["apache"] = strings.TrimSpace(selection.Apache.Version)
		}
		if plan.needMySQL && fileExists(m.mysqlExe()) {
			metadata["mysql"] = strings.TrimSpace(selection.MySQL.Version)
		}
		if plan.needPHPMyAdmin && fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php")) {
			metadata["phpmyadmin"] = strings.TrimSpace(selection.PHPMyAdmin.Version)
		}
	})
}

func (m *windowsRuntimeManager) removeInstalledVersion(appID string) error {
	return m.updateInstalledVersionsMetadata(func(metadata installedRuntimeVersions) {
		delete(metadata, strings.ToLower(strings.TrimSpace(appID)))
	})
}

func (m *windowsRuntimeManager) downloadArchivePathFor(appID string, version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	for _, release := range appStoreEffectiveReleases(m.projectRoot, appID) {
		if strings.TrimSpace(release.Version) != version {
			continue
		}
		if strings.TrimSpace(release.FileName) == "" {
			return ""
		}
		return filepath.Join(m.paths().downloadsDir, release.FileName)
	}
	return ""
}

func (m *windowsRuntimeManager) removeLegacyPHPInstalledVersionsMetadata() error {
	return m.updateInstalledVersionsMetadata(func(metadata installedRuntimeVersions) {
		for key := range metadata {
			if key == "php" || strings.HasPrefix(key, "php:") {
				delete(metadata, key)
			}
		}
	})
}

func (m *windowsRuntimeManager) ensureApacheLandingPage() error {
	htdocs := filepath.Join(m.apacheRoot(), "htdocs")
	if err := os.MkdirAll(htdocs, 0o755); err != nil {
		return err
	}
	indexHTML := "<!doctype html><html><head><meta charset=\"utf-8\"><title>zPanel Bundled Stack</title></head><body><h1>zPanel Bundled Stack</h1><p>Apache is running from data/runtime.</p></body></html>"
	return os.WriteFile(filepath.Join(htdocs, "index.html"), []byte(indexHTML), 0o644)
}

func randomHexString(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}
	buf := make([]byte, (length+1)/2)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	value := fmt.Sprintf("%x", buf)
	if len(value) > length {
		value = value[:length]
	}
	return value, nil
}

func (m *windowsRuntimeManager) ensurePHPMyAdminConfig() error {
	if err := m.normalizeRuntimeLayout(); err != nil {
		return err
	}
	root := m.phpMyAdminRoot()
	if !fileExists(filepath.Join(root, "index.php")) {
		return nil
	}
	if err := os.MkdirAll(m.paths().phpMyAdminTempDir, 0o755); err != nil {
		return err
	}
	if fileExists(m.phpMyAdminConfigPath()) {
		return nil
	}

	secret, err := randomHexString(32)
	if err != nil {
		return err
	}

	config := strings.Join([]string{
		"<?php",
		"$cfg['blowfish_secret'] = '" + secret + "';",
		"$i = 0;",
		"$i++;",
		"$cfg['Servers'][$i]['auth_type'] = 'cookie';",
		"$cfg['Servers'][$i]['host'] = '127.0.0.1';",
		"$cfg['Servers'][$i]['port'] = '3307';",
		"$cfg['Servers'][$i]['compress'] = false;",
		"$cfg['Servers'][$i]['AllowNoPassword'] = true;",
		"$cfg['TempDir'] = '" + addPHPStringSlashes(toForwardPath(m.paths().phpMyAdminTempDir)) + "';",
		"",
	}, "\r\n")

	return os.WriteFile(m.phpMyAdminConfigPath(), []byte(config), 0o644)
}

func addPHPStringSlashes(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return replacer.Replace(value)
}

func (m *windowsRuntimeManager) phpMyAdminPublicPath() string {
	return filepath.Join(m.apacheRoot(), "htdocs", "phpmyadmin")
}

func (m *windowsRuntimeManager) ensurePHPMyAdminPublicPath() error {
	root := m.phpMyAdminRoot()
	if !fileExists(filepath.Join(root, "index.php")) {
		return nil
	}

	publicPath := m.phpMyAdminPublicPath()
	if err := os.MkdirAll(filepath.Dir(publicPath), 0o755); err != nil {
		return err
	}

	if fileExists(publicPath) {
		if resolved, err := filepath.EvalSymlinks(publicPath); err == nil && strings.EqualFold(filepath.Clean(resolved), filepath.Clean(root)) {
			return nil
		}
		if err := os.RemoveAll(publicPath); err != nil {
			return err
		}
	}

	cmd := exec.Command("cmd", "/c", "mklink", "/J", publicPath, root)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("create phpmyadmin public mount: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *windowsRuntimeManager) phpMyAdminApacheBlock(httpPort int) string {
	root := m.phpMyAdminRoot()
	publicRoot := filepath.Join(m.apacheRoot(), "htdocs")
	publicPHPMyAdmin := m.phpMyAdminPublicPath()
	if !fileExists(filepath.Join(root, "index.php")) || !fileExists(filepath.Join(publicPHPMyAdmin, "index.php")) {
		return ""
	}

	phpVersion := m.phpMyAdminCompatiblePHPVersion(m.getInstalledPHPVersions(), m.installedPHPMyAdminVersionResolved())
	if phpVersion == "" {
		return ""
	}

	rootForward := toForwardPath(root)
	publicRootForward := toForwardPath(publicRoot)
	port := phpFastCGIPort(phpVersion)
	return "\r\n# zPanel Local Tools BEGIN\r\n" +
		"<VirtualHost 127.0.0.1:" + strconv.Itoa(httpPort) + ">\r\n" +
		"    ServerName 127.0.0.1\r\n" +
		"    DocumentRoot \"" + publicRootForward + "\"\r\n" +
		"    DirectoryIndex index.php index.html\r\n" +
		"    <Directory \"" + publicRootForward + "\">\r\n" +
		"        Options FollowSymLinks ExecCGI\r\n" +
		"        AllowOverride None\r\n" +
		"        Require all granted\r\n" +
		"        <FilesMatch \"\\.php$\">\r\n" +
		"            SetHandler \"proxy:fcgi://127.0.0.1:" + strconv.Itoa(port) + "//./\"\r\n" +
		"        </FilesMatch>\r\n" +
		"    </Directory>\r\n" +
		"    <Directory \"" + rootForward + "\">\r\n" +
		"    Options FollowSymLinks ExecCGI\r\n" +
		"    AllowOverride None\r\n" +
		"    Require all granted\r\n" +
		"    </Directory>\r\n" +
		"</VirtualHost>\r\n" +
		"# zPanel Local Tools END\r\n"
}

func (m *windowsRuntimeManager) startPHPMyAdmin() error {
	if !m.appInstalled("phpmyadmin") {
		return errors.New("phpmyadmin is not installed yet. Use Install first.")
	}

	apacheReleases := appStoreEffectiveReleases(m.projectRoot, "apache")
	mysqlReleases := appStoreEffectiveReleases(m.projectRoot, "mysql")
	phpReleases := appStoreEffectiveReleases(m.projectRoot, "php")
	phpMyAdminVersion := m.installedPHPMyAdminVersionResolved()
	installedPHPs := m.getInstalledPHPVersions()
	runningPHPs := m.runningPHPVersions(installedPHPs)
	phpVersion := m.phpMyAdminCompatiblePHPVersion(installedPHPs, phpMyAdminVersion)
	dependencies := m.phpMyAdminDependencyState(
		fileExists(m.apacheExe()),
		apacheListeningOnPort(m.configuredHTTPPortOrDefault()),
		m.installedApacheVersionResolved(),
		fileExists(m.mysqlExe()),
		testTCPPort("127.0.0.1", 3307, 120*time.Millisecond),
		m.installedMySQLVersionResolved(),
		installedPHPs,
		runningPHPs,
		phpMyAdminVersion,
		apacheReleases,
		mysqlReleases,
		phpReleases,
	)
	if len(dependencies.MissingDependencies) > 0 {
		return &runtimeDependencyError{
			AppID:               "phpmyadmin",
			Message:             dependencies.Message,
			MissingDependencies: append([]string(nil), dependencies.MissingDependencies...),
		}
	}

	if err := m.ensurePHPMyAdminConfig(); err != nil {
		return err
	}
	if err := m.ensurePHPMyAdminPublicPath(); err != nil {
		return err
	}
	if phpVersion != "" {
		if err := m.startPHPFastCGI(phpVersion); err != nil {
			return err
		}
	}

	httpPort := m.configuredHTTPPortOrDefault()
	if apacheListeningOnPort(httpPort) &&
		m.apacheConfigHasPHPMyAdminPort(phpFastCGIPort(phpVersion)) &&
		fileExists(filepath.Join(m.phpMyAdminPublicPath(), "index.php")) {
		return nil
	}
	if err := m.configureApache(m.currentPHPVersion(), httpPort); err != nil {
		return err
	}
	if err := m.stopApache(); err != nil {
		return err
	}
	return m.startApache()
}

func (m *windowsRuntimeManager) configuredHTTPPortOrDefault() int {
	httpPort := m.configuredHTTPPort()
	if httpPort == 0 {
		return apacheHTTPPort
	}
	return httpPort
}

func (m *windowsRuntimeManager) autoStartAfterInstall(appID string, version string) (runtimeAppsResponse, error) {
	startTarget := strings.ToLower(strings.TrimSpace(appID))
	if strings.Contains(startTarget, ":") {
		startTarget = strings.SplitN(startTarget, ":", 2)[0]
	}
	if startTarget == "php" && strings.TrimSpace(version) != "" {
		startTarget = "php:" + strings.TrimSpace(version)
	}

	response, err := m.Start(startTarget)
	if err != nil {
		var dependencyErr *runtimeDependencyError
		if errors.As(err, &dependencyErr) {
			time.Sleep(500 * time.Millisecond)
			return runtimeAppsResponse{
				Apps:    m.listApps(),
				Message: "Installed, but not started. " + dependencyErr.Error(),
			}, nil
		}
		return runtimeAppsResponse{}, err
	}

	response.Message = "Application installed and started successfully."
	return response, nil
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
	timezone := defaultPanelTimezone
	if settings, err := loadPanelSettingsAtProjectRoot(m.projectRoot); err == nil && strings.TrimSpace(settings.Timezone) != "" {
		timezone = settings.Timezone
	}
	updated = regexp.MustCompile(`(?mi)^;?\s*date\.timezone\s*=.*$`).ReplaceAllString(updated, "date.timezone = "+timezone)
	if !regexp.MustCompile(`(?m)^date\.timezone\s*=`).MatchString(updated) {
		updated += "\r\ndate.timezone = " + timezone + "\r\n"
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

func (m *windowsRuntimeManager) configureApache(phpVersion string, httpPort int) error {
	confBytes, err := os.ReadFile(m.httpdConfPath())
	if err != nil {
		return fmt.Errorf("apache config not found: %w", err)
	}

	apacheRootForward := toForwardPath(m.apacheRoot())
	conf := string(confBytes)
	conf = regexp.MustCompile(`(?m)^ServerRoot ".*"\r?$`).ReplaceAllString(conf, `ServerRoot "`+apacheRootForward+`"`)
	conf = regexp.MustCompile(`(?m)^Define SRVROOT ".*"\r?$`).ReplaceAllString(conf, `Define SRVROOT "`+apacheRootForward+`"`)
	conf = strings.ReplaceAll(conf, "C:/Apache24-64", apacheRootForward)
	conf = regexp.MustCompile(`(?m)^Listen\s+\S+\r?$`).ReplaceAllString(conf, "Listen 127.0.0.1:"+strconv.Itoa(httpPort))
	conf = regexp.MustCompile(`(?m)^#?ServerName\s+.*\r?$`).ReplaceAllString(conf, "ServerName 127.0.0.1:"+strconv.Itoa(httpPort))
	conf = regexp.MustCompile(`(?m)^DirectoryIndex .+\r?$`).ReplaceAllString(conf, "DirectoryIndex index.php index.html")
	conf = enableApacheModule(conf, "proxy_module", "mod_proxy.so")
	conf = enableApacheModule(conf, "proxy_fcgi_module", "mod_proxy_fcgi.so")
	conf = enableApacheModule(conf, "rewrite_module", "mod_rewrite.so")

	conf = regexp.MustCompile(`(?m)^\s*LoadModule php_module ".*"\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^\s*PHPIniDir ".*"\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^\s*AddHandler application/x-httpd-php \.php\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^\s*AddType application/x-httpd-php \.php\r?\n?`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?s)\r?\n# zPanel PHP BEGIN\r?\n.*?# zPanel PHP END\r?\n?`).ReplaceAllString(conf, "\r\n")
	conf = regexp.MustCompile(`(?s)\r?\n# zPanel phpMyAdmin BEGIN\r?\n.*?# zPanel phpMyAdmin END\r?\n?`).ReplaceAllString(conf, "\r\n")
	conf = regexp.MustCompile(`(?s)\r?\n# zPanel Local Tools BEGIN\r?\n.*?# zPanel Local Tools END\r?\n?`).ReplaceAllString(conf, "\r\n")
	conf = regexp.MustCompile(`(?m)^Include conf/extra/httpd-vhosts\.conf\r?$`).ReplaceAllString(conf, "#Include conf/extra/httpd-vhosts.conf")
	conf = regexp.MustCompile(`(?m)^Include conf/extra/zpanel-vhosts\.conf\r?$`).ReplaceAllString(conf, "")
	conf = regexp.MustCompile(`(?m)^IncludeOptional ".*?/etc/sites/\*/apache-vhost\.conf"\r?$`).ReplaceAllString(conf, "")
	vhostInclude := "IncludeOptional " + m.apacheVHostIncludePath()
	conf += "\r\n" + vhostInclude + "\r\n"
	if fileExists(filepath.Join(m.phpMyAdminRoot(), "index.php")) {
		if err := m.ensurePHPMyAdminPublicPath(); err != nil {
			return err
		}
		if phpMyAdminBlock := m.phpMyAdminApacheBlock(httpPort); phpMyAdminBlock != "" {
			conf = strings.Replace(conf, vhostInclude, strings.TrimRight(phpMyAdminBlock, "\r\n")+"\r\n"+vhostInclude, 1)
		}
	}

	if err := os.WriteFile(m.httpdConfPath(), []byte(conf), 0o644); err != nil {
		return err
	}

	if err := m.writeApacheVHostConfig(httpPort); err != nil {
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
	}

	_ = os.Remove(filepath.Join(m.apacheRoot(), "htdocs", "index.php"))
	_ = os.Remove(m.phpInfoPath())
	return os.WriteFile(m.httpdConfPath(), []byte(conf), 0o644)
}

func (m *windowsRuntimeManager) configureMySQL() error {
	if err := m.normalizeRuntimeLayout(); err != nil {
		return err
	}
	root := m.mysqlRoot()
	if err := os.MkdirAll(filepath.Dir(m.paths().myIniPath), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(m.paths().mysqlDataDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(m.paths().mysqlTempDir, 0o755); err != nil {
		return err
	}
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

	_ = os.Remove(m.mysqlPIDPath())
	_ = os.Remove(filepath.Join(m.paths().mysqlDataDir, "mysqld.err"))

	if err := runHiddenProcessWithTimeout(
		m.mysqlExe(),
		[]string{"--defaults-file=" + m.paths().myIniPath, "--initialize-insecure", "--console"},
		root,
		nil,
		mysqlInitializeTimeout,
	); err != nil {
		return m.formatMySQLInitError(err)
	}
	return nil
}

func (m *windowsRuntimeManager) startApache() error {
	if !fileExists(m.apacheExe()) {
		return errors.New("apache is not installed")
	}

	activePHPVersion := m.currentPHPVersion()
	configuredPort := m.configuredHTTPPort()
	if configuredPort > 0 && apacheListeningOnPort(configuredPort) {
		return nil
	}

	if pid, _ := clearStalePIDFile(m.apachePIDPath(), "httpd.exe"); pid > 0 {
		stopProcessTree(pid)
		if configuredPort > 0 {
			_ = waitForTCPPortClosed("127.0.0.1", configuredPort, 10*time.Second)
		}
	}

	selectedPort := m.selectApacheHTTPPort()
	if err := m.configureApache(activePHPVersion, selectedPort); err != nil {
		return err
	}
	pathParts := []string{filepath.Join(m.apacheRoot(), "bin")}
	if activePHPVersion != "" && fileExists(m.phpExe(activePHPVersion)) {
		pathParts = append([]string{m.phpRealRoot(activePHPVersion)}, pathParts...)
	}
	pathParts = append(pathParts, os.Getenv("PATH"))
	env := append(os.Environ(), "PATH="+strings.Join(pathParts, ";"))
	if _, err := startHiddenProcess(m.apacheExe(), []string{"-d", m.apacheRoot(), "-f", m.httpdConfPath()}, m.apacheRoot(), env); err != nil {
		return err
	}

	if !waitForTCPPort("127.0.0.1", selectedPort, 20*time.Second) {
		return fmt.Errorf("apache did not start successfully on %s", formatPortURL("127.0.0.1", selectedPort, "/"))
	}
	return nil
}

func (m *windowsRuntimeManager) stopApache() error {
	configuredPort := m.configuredHTTPPort()
	if configuredPort == 0 {
		configuredPort = apacheHTTPPort
	}
	pid, _ := clearStalePIDFile(m.apachePIDPath(), "httpd.exe")
	listeningPID, _ := getListeningPID(configuredPort)
	if !processMatches(listeningPID, "httpd.exe") {
		listeningPID = 0
	}
	if pid == 0 && listeningPID == 0 && !apacheListeningOnPort(configuredPort) {
		_ = os.Remove(m.apachePIDPath())
		return nil
	}

	switch {
	case pid > 0:
		stopProcessTree(pid)
	case listeningPID > 0:
		stopProcessTree(listeningPID)
	}

	_ = waitForTCPPortClosed("127.0.0.1", configuredPort, 10*time.Second)
	_ = os.Remove(m.apachePIDPath())
	return nil
}

func (m *windowsRuntimeManager) startMySQL() error {
	if !fileExists(m.mysqlExe()) {
		return errors.New("mysql is not installed")
	}
	if err := m.normalizeRuntimeLayout(); err != nil {
		return err
	}
	if err := m.configureMySQL(); err != nil {
		return err
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
	_ = m.removeInstalledVersion("apache")
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
	if err := m.stopPHPFastCGI(version); err != nil {
		return err
	}

	paths := m.paths()
	_ = os.RemoveAll(m.phpRoot(version))
	if archivePath := phpArchivePath(paths, version); archivePath != "" {
		_ = os.Remove(archivePath)
	}
	_ = m.removeLegacyPHPInstalledVersionsMetadata()

	if fileExists(m.httpdConfPath()) {
		nextVersion := m.currentPHPVersion()
		httpPort := m.configuredHTTPPort()
		if httpPort == 0 {
			httpPort = apacheHTTPPort
		}
		if err := m.configureApache(nextVersion, httpPort); err != nil {
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
	_ = m.removeInstalledVersion("mysql")
	return nil
}

func (m *windowsRuntimeManager) uninstallPHPMyAdmin() error {
	paths := m.paths()
	if archive := m.downloadArchivePathFor("phpmyadmin", m.installedPHPMyAdminVersionResolved()); archive != "" {
		_ = os.Remove(archive)
	}
	_ = os.RemoveAll(m.phpMyAdminPublicPath())
	_ = os.RemoveAll(paths.phpMyAdminDir)
	_ = os.RemoveAll(paths.phpMyAdminTempDir)
	_ = m.removeInstalledVersion("phpmyadmin")

	if fileExists(m.httpdConfPath()) {
		httpPort := m.configuredHTTPPortOrDefault()
		if err := m.configureApache(m.currentPHPVersion(), httpPort); err != nil {
			return err
		}
	}
	return nil
}

func (m *windowsRuntimeManager) InstallFolderFor(id string) string {
	baseID := strings.ToLower(strings.TrimSpace(id))
	if strings.Contains(baseID, ":") {
		baseID = strings.SplitN(baseID, ":", 2)[0]
	}
	switch baseID {
	case "apache":
		return m.apacheRoot()
	case "php":
		return m.paths().phpRoot
	case "mysql":
		return m.mysqlRoot()
	case "phpmyadmin":
		return m.phpMyAdminRoot()
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
	baseID := strings.ToLower(strings.TrimSpace(id))
	if strings.Contains(baseID, ":") {
		baseID = strings.SplitN(baseID, ":", 2)[0]
	}
	switch baseID {
	case "apache":
		return m.httpdConfPath()
	case "php":
		return m.phpIniPath("")
	case "mysql":
		return m.paths().myIniPath
	case "phpmyadmin":
		return m.phpMyAdminConfigPath()
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

func (m *windowsRuntimeManager) writeApacheVHostConfig(httpPort int) error {
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
			builder.WriteString("<VirtualHost 127.0.0.1:" + strconv.Itoa(httpPort) + ">\r\n")
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
	httpPort := m.configuredHTTPPort()
	if httpPort == 0 {
		httpPort = apacheHTTPPort
	}
	if currentVersion != "" && fileExists(m.httpdConfPath()) {
		if err := m.configureApache(currentVersion, httpPort); err != nil {
			return err
		}
	} else if err := m.writeApacheVHostConfig(httpPort); err != nil {
		return err
	}
	if !fileExists(m.apacheExe()) || !apacheListeningOnPort(httpPort) {
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

func runHiddenProcessWithTimeout(exe string, args []string, dir string, env []string, timeout time.Duration) error {
	if timeout <= 0 {
		return runHiddenProcess(exe, args, dir, env, true)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if summary := summarizeCommandOutput(output.String()); summary != "" {
			return fmt.Errorf("timed out after %s: %s", timeout, summary)
		}
		return fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		if summary := summarizeCommandOutput(output.String()); summary != "" {
			return fmt.Errorf("%v: %s", err, summary)
		}
		return err
	}
	return nil
}

func summarizeCommandOutput(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	output = strings.Join(strings.Fields(output), " ")
	if len(output) > 320 {
		return output[:317] + "..."
	}
	return output
}

func readFileTail(path string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}

	content, err := os.ReadFile(path)
	if err != nil || len(content) == 0 {
		return ""
	}
	if len(content) > maxBytes {
		content = content[len(content)-maxBytes:]
	}
	return summarizeCommandOutput(string(content))
}

func (m *windowsRuntimeManager) formatMySQLInitError(err error) error {
	details := make([]string, 0, 2)
	if err != nil {
		if summary := summarizeCommandOutput(err.Error()); summary != "" {
			details = append(details, summary)
		}
	}
	if logTail := readFileTail(filepath.Join(m.paths().mysqlDataDir, "mysqld.err"), 2048); logTail != "" {
		details = append(details, "mysqld.err: "+logTail)
	}
	if len(details) == 0 {
		return errors.New("mysql initialization failed")
	}
	return fmt.Errorf("mysql initialization failed: %s", strings.Join(details, " | "))
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

func apacheListeningOnPort(port int) bool {
	pid, err := getListeningPID(port)
	if err != nil || pid == 0 {
		return false
	}
	return processMatches(pid, "httpd.exe")
}

func formatPortURL(host string, port int, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if port == 80 {
		return "http://" + host + path
	}
	return fmt.Sprintf("http://%s:%d%s", host, port, path)
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
	if err := m.stopAllPHPFastCGI(); err != nil {
		errs = append(errs, fmt.Sprintf("php: %v", err))
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
	if err := m.normalizeRuntimeLayout(); err != nil {
		return err
	}
	system32 := filepath.Join(os.Getenv("WINDIR"), "System32")
	dllPath := filepath.Join(system32, "vcruntime140.dll")
	redistPath := filepath.Join(m.paths().downloadsDir, "vc_redist.x64.exe")
	defer func() {
		_ = os.Remove(redistPath)
	}()
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
