//go:build linux

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Linux-specific release URLs (example versions)
var linuxApacheReleases = []runtimeRelease{
	{Version: "2.4.62", URL: "https://github.com/aaPanel/aaPanel/raw/master/install/src/httpd-2.4.62.tar.gz", FileName: "httpd-2.4.62.tar.gz"},
}

var linuxPHPReleases = []runtimeRelease{
	{Version: "8.3.12", URL: "https://github.com/aaPanel/aaPanel/raw/master/install/src/php-8.3.12.tar.gz", FileName: "php-8.3.12.tar.gz"},
}

var linuxMySQLReleases = []runtimeRelease{
	{Version: "8.0.39", URL: "https://dev.mysql.com/get/Downloads/MySQL-8.0/mysql-8.0.39-linux-glibc2.28-x86_64.tar.gz", FileName: "mysql-8.0.39-linux-glibc2.28-x86_64.tar.gz"},
}

func appStoreReleaseCatalog() map[string][]runtimeRelease {
	return map[string][]runtimeRelease{
		"apache": linuxApacheReleases,
		"php":    linuxPHPReleases,
		"mysql":  linuxMySQLReleases,
	}
}

type linuxRuntimePaths struct {
	runtimeRoot   string
	apacheRoot    string
	apacheExe     string
	apachePid     string
	phpRoot       string
	phpExe        string
	mysqlRoot     string
	mysqlExe      string
	mysqlPid      string
	downloadCache string
}

func (m *linuxRuntimeManager) paths() linuxRuntimePaths {
	runtime := filepath.Join(m.projectRoot, "data", "runtime")
	return linuxRuntimePaths{
		runtimeRoot:   runtime,
		apacheRoot:    filepath.Join(runtime, "apache"),
		apacheExe:     filepath.Join(runtime, "apache", "bin", "httpd"),
		apachePid:     filepath.Join(runtime, "apache", "logs", "httpd.pid"),
		phpRoot:       filepath.Join(runtime, "php"),
		phpExe:        filepath.Join(runtime, "php", "bin", "php"), // PHP 8.x
		mysqlRoot:     filepath.Join(runtime, "mysql"),
		mysqlExe:      filepath.Join(runtime, "mysql", "bin", "mysqld"),
		mysqlPid:      filepath.Join(runtime, "mysql", "data", "mysql.pid"),
		downloadCache: filepath.Join(runtime, "downloads"),
	}
}

type linuxRuntimeManager struct {
	projectRoot string
}

func newRuntimeManager(projectRoot string) runtimeManager {
	return &linuxRuntimeManager{projectRoot: projectRoot}
}

func (m *linuxRuntimeManager) Status() (runtimeAppsResponse, error) {
	return runtimeAppsResponse{Apps: m.listApps()}, nil
}

func (m *linuxRuntimeManager) listApps() []runtimeApp {
	p := m.paths()
	apacheReleases := appStoreEffectiveReleases(m.projectRoot, "apache")
	phpReleases := appStoreEffectiveReleases(m.projectRoot, "php")
	mysqlReleases := appStoreEffectiveReleases(m.projectRoot, "mysql")

	apacheInstalled := fileExists(p.apacheExe)
	apacheRunning := false
	if apacheInstalled {
		if pid, err := readPIDFile(p.apachePid); err == nil && pid > 0 {
			// Basic check: is process with this PID running?
			process, _ := os.FindProcess(pid)
			if process.Signal(syscall.Signal(0)) == nil {
				apacheRunning = true
			}
		}
	}

	phpInstalled := fileExists(p.phpExe)
	phpRunning := apacheRunning // PHP is often tied to Apache on these lite stacks

	mysqlInstalled := fileExists(p.mysqlExe)
	mysqlRunning := false
	if mysqlInstalled {
		if pid, err := readPIDFile(p.mysqlPid); err == nil && pid > 0 {
			process, _ := os.FindProcess(pid)
			if process.Signal(syscall.Signal(0)) == nil {
				mysqlRunning = true
			}
		}
	}

	return []runtimeApp{
		newRuntimeApp("apache", "Apache HTTP Server", apacheReleases[0].Version, []string{apacheReleases[0].Version}, "Portable web server for Linux.", p.apacheRoot, apacheInstalled, apacheRunning, "8081", "http://127.0.0.1:8081/", linuxReleaseURLs(apacheReleases), apacheInstalled && !apacheRunning, apacheInstalled && apacheRunning, apacheRunning, apacheInstalled),
		newRuntimeApp("php", "PHP Runtime", phpReleases[0].Version, []string{phpReleases[0].Version}, "PHP runtime for Linux.", p.phpRoot, phpInstalled, phpRunning, "", "", linuxReleaseURLs(phpReleases), false, false, false, phpInstalled),
		newRuntimeApp("mysql", "MySQL Community Server", mysqlReleases[0].Version, []string{mysqlReleases[0].Version}, "MySQL server for Linux.", p.mysqlRoot, mysqlInstalled, mysqlRunning, "3306", "", linuxReleaseURLs(mysqlReleases), mysqlInstalled && !mysqlRunning, mysqlInstalled && mysqlRunning, false, mysqlInstalled),
	}
}

func (m *linuxRuntimeManager) Install(appID string, version string, onProgress func(appProgressEvent)) (runtimeAppsResponse, error) {
	p := m.paths()
	var release runtimeRelease
	var targetDir string
	apacheReleases := appStoreEffectiveReleases(m.projectRoot, "apache")
	phpReleases := appStoreEffectiveReleases(m.projectRoot, "php")
	mysqlReleases := appStoreEffectiveReleases(m.projectRoot, "mysql")

	switch appID {
	case "apache":
		release = apacheReleases[0] // TODO: version selection
		targetDir = p.apacheRoot
	case "php":
		release = phpReleases[0]
		targetDir = p.phpRoot
	case "mysql":
		release = mysqlReleases[0]
		targetDir = p.mysqlRoot
	default:
		return runtimeAppsResponse{}, errors.New("unsupported app for installation on Linux")
	}

	downloadPath := filepath.Join(p.downloadCache, release.FileName)
	if err := downloadFile(release.URL, downloadPath, appID, 10, 80, onProgress); err != nil {
		return runtimeAppsResponse{}, err
	}

	reportProgress(onProgress, 85, "Extracting "+appID+"...")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return runtimeAppsResponse{}, err
	}

	if err := expandTarGz(downloadPath, targetDir); err != nil {
		return runtimeAppsResponse{}, err
	}

	reportProgress(onProgress, 100, appID+" installed successfully.")
	return runtimeAppsResponse{Apps: m.listApps(), Message: appID + " installed."}, nil
}

func (m *linuxRuntimeManager) Start(appID string) (runtimeAppsResponse, error) {
	p := m.paths()
	var cmd *exec.Cmd
	switch appID {
	case "apache":
		cmd = exec.Command(p.apacheExe, "-k", "start")
	case "mysql":
		cmd = exec.Command(p.mysqlExe, "--defaults-file="+filepath.Join(p.mysqlRoot, "my.cnf"), "&")
	default:
		return runtimeAppsResponse{}, errors.New("start not supported for this app on Linux")
	}

	if err := cmd.Start(); err != nil {
		return runtimeAppsResponse{}, err
	}

	return runtimeAppsResponse{Apps: m.listApps(), Message: appID + " started."}, nil
}

func (m *linuxRuntimeManager) Stop(appID string) (runtimeAppsResponse, error) {
	p := m.paths()
	var pidFile string
	switch appID {
	case "apache":
		pidFile = p.apachePid
	case "mysql":
		pidFile = p.mysqlPid
	default:
		return runtimeAppsResponse{}, errors.New("stop not supported for this app on Linux")
	}

	pid, err := readPIDFile(pidFile)
	if err != nil {
		return runtimeAppsResponse{}, err
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return runtimeAppsResponse{}, err
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		return runtimeAppsResponse{}, err
	}

	return runtimeAppsResponse{Apps: m.listApps(), Message: appID + " stopped."}, nil
}

func (m *linuxRuntimeManager) Uninstall(appID string) (runtimeAppsResponse, error) {
	p := m.paths()
	var targetDir string
	switch appID {
	case "apache":
		targetDir = p.apacheRoot
	case "php":
		targetDir = p.phpRoot
	case "mysql":
		targetDir = p.mysqlRoot
	}

	if targetDir != "" {
		_ = os.RemoveAll(targetDir)
	}

	return runtimeAppsResponse{Apps: m.listApps(), Message: appID + " uninstalled."}, nil
}

func (m *linuxRuntimeManager) StopAll() error {
	for _, appID := range []string{"apache", "mysql"} {
		if _, err := m.Stop(appID); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func linuxReleaseURLs(releases []runtimeRelease) map[string]string {
	urls := make(map[string]string, len(releases))
	for _, release := range releases {
		urls[release.Version] = release.URL
	}
	return urls
}

// Helper: Extract tar.gz
func expandTarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)
		cleanTarget := filepath.Clean(target)
		if cleanTarget != filepath.Clean(dest) && !strings.HasPrefix(cleanTarget, filepath.Clean(dest)+string(filepath.Separator)) {
			return errors.New("invalid tar entry path")
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(cleanTarget, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(cleanTarget, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(cleanTarget), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, cleanTarget); err != nil {
				// Non-critical if symlink fails on some systems
			}
		}
	}
	return nil
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
