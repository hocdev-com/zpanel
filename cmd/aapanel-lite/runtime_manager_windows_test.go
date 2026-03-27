//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestUninstallPHPCleansConfig(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	confPath := manager.httpdConfPath()
	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		t.Fatalf("mkdir conf dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(manager.apacheExe()), 0o755); err != nil {
		t.Fatalf("mkdir apache bin dir: %v", err)
	}
	if err := os.MkdirAll(manager.phpRoot("8.4.19"), 0o755); err != nil {
		t.Fatalf("mkdir php root: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(manager.paths().phpZip), 0o755); err != nil {
		t.Fatalf("mkdir downloads dir: %v", err)
	}

	conf := "ServerRoot \"C:/Apache24\"\nListen 80\nServerName 127.0.0.1:8081\nDirectoryIndex index.php index.html\n" +
		"LoadModule php_module \"C:/php/php8apache2_4.dll\"\nPHPIniDir \"C:/php\"\nAddHandler application/x-httpd-php .php\nAddType application/x-httpd-php .php\n"
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}
	if err := os.WriteFile(manager.apacheExe(), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write apache exe: %v", err)
	}
	if err := os.WriteFile(manager.phpExe("8.4.19"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write php exe: %v", err)
	}
	if err := os.WriteFile(manager.paths().phpZip, []byte("zip"), 0o644); err != nil {
		t.Fatalf("write php zip: %v", err)
	}

	if err := manager.uninstallPHP("8.4.19"); err != nil {
		t.Fatalf("uninstall php: %v", err)
	}

	if _, err := os.Stat(manager.phpRoot("8.4.19")); !os.IsNotExist(err) {
		t.Fatalf("expected php root removed, got err=%v", err)
	}
	if _, err := os.Stat(manager.paths().phpZip); !os.IsNotExist(err) {
		t.Fatalf("expected php zip removed, got err=%v", err)
	}

	updated, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read updated conf: %v", err)
	}
	text := string(updated)
	if strings.Contains(text, "php8apache2_4.dll") || strings.Contains(text, "PHPIniDir") {
		t.Fatalf("expected php directives removed from apache config: %s", text)
	}
}

func TestUninstallMySQLRemovesRuntimeFiles(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	paths := manager.paths()
	mysqlHome := filepath.Join(paths.mysqlExtractDir, "mysql-8.4.8-winx64")
	for _, dir := range []string{mysqlHome, paths.mysqlDataDir, paths.mysqlTempDir, filepath.Dir(paths.mysqlZip)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(mysqlHome, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir mysql exe dir: %v", err)
	}
	if err := os.WriteFile(manager.mysqlExe(), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write mysql exe: %v", err)
	}
	if err := os.WriteFile(paths.myIniPath, []byte("[mysqld]"), 0o644); err != nil {
		t.Fatalf("write my.ini: %v", err)
	}
	if err := os.WriteFile(paths.mysqlZip, []byte("zip"), 0o644); err != nil {
		t.Fatalf("write mysql zip: %v", err)
	}

	if err := manager.uninstallMySQL(); err != nil {
		t.Fatalf("uninstall mysql: %v", err)
	}

	for _, removed := range []string{paths.mysqlExtractDir, paths.mysqlDataDir, paths.mysqlTempDir, paths.myIniPath, paths.mysqlZip} {
		if _, err := os.Stat(removed); !os.IsNotExist(err) {
			t.Fatalf("expected %s removed, got err=%v", removed, err)
		}
	}
}

func TestBuildInstallPlan(t *testing.T) {
	cases := []struct {
		appID      string
		wantApache bool
		wantPHP    bool
		wantMySQL  bool
	}{
		{appID: "apache", wantApache: true},
		{appID: "php", wantPHP: true},
		{appID: "mysql", wantMySQL: true},
		{appID: "stack", wantApache: true, wantPHP: true, wantMySQL: true},
	}

	for _, tc := range cases {
		plan := buildInstallPlan(tc.appID)
		if plan.needApache != tc.wantApache || plan.needPHP != tc.wantPHP || plan.needMySQL != tc.wantMySQL {
			t.Fatalf("unexpected plan for %s: %+v", tc.appID, plan)
		}
	}
}

func TestConfigurePHPWritesAbsoluteExtensionDir(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	phpRoot := manager.phpRoot("8.4.19")
	if err := os.MkdirAll(phpRoot, 0o755); err != nil {
		t.Fatalf("mkdir php root: %v", err)
	}

	template := []byte(`;extension_dir = "ext"
;extension=mysqli
;extension=pdo_mysql
;extension=mbstring
;extension=openssl
;extension=curl
`)
	if err := os.WriteFile(filepath.Join(phpRoot, "php.ini-production"), template, 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	if err := manager.configurePHP("8.4.19"); err != nil {
		t.Fatalf("configure php: %v", err)
	}

	phpIni, err := os.ReadFile(filepath.Join(phpRoot, "php.ini"))
	if err != nil {
		t.Fatalf("read php.ini: %v", err)
	}

	text := string(phpIni)
	expectedDir := `extension_dir = "` + toForwardPath(filepath.Join(phpRoot, "ext")) + `"`
	if !strings.Contains(text, expectedDir) {
		t.Fatalf("expected absolute extension_dir, got: %s", text)
	}
	for _, ext := range []string{"mysqli", "pdo_mysql", "mbstring", "openssl", "curl"} {
		if !strings.Contains(text, "extension="+ext) {
			t.Fatalf("expected extension %s enabled, got: %s", ext, text)
		}
	}
}

func TestParsePHPExtensionDirectiveOnlyAcceptsShortNames(t *testing.T) {
	tests := map[string]string{
		`;extension=bz2`:                                                   "bz2",
		`;extension=curl`:                                                  "curl",
		`;extension=ffi`:                                                   "ffi",
		`;extension=ftp`:                                                   "ftp",
		`;extension=fileinfo`:                                              "fileinfo",
		`;extension=exif      ; Must be after mbstring as it depends on it`: "exif",
	}

	for line, expected := range tests {
		got, ok := parsePHPExtensionDirective(line)
		if !ok {
			t.Fatalf("expected directive to parse: %s", line)
		}
		if got != expected {
			t.Fatalf("expected %q from %q, got %q", expected, line, got)
		}
	}

	ignored := []string{
		`;extension=/path/to/extension/mysqli.so`,
		`extension="C:\php\ext\php_mbstring.dll"`,
		` extension = '/opt/php/ext/pdo_mysql.so' `,
		`; extension = "ext/php_opcache.dll"`,
	}

	for _, line := range ignored {
		if got, ok := parsePHPExtensionDirective(line); ok {
			t.Fatalf("expected path-based directive to be ignored: %q -> %q", line, got)
		}
	}
}

func TestApplyPHPExtensionsPreservesInlineComments(t *testing.T) {
	manager := &windowsRuntimeManager{}
	content := strings.Join([]string{
		";extension=mbstring",
		";extension=exif      ; Must be after mbstring as it depends on it",
		"",
	}, "\n")

	enabled := manager.applyPHPExtensions("8.4.19", content, []string{"mbstring", "exif"})
	if !strings.Contains(enabled, "extension=exif      ; Must be after mbstring as it depends on it") {
		t.Fatalf("expected exif comment preserved when enabling, got: %s", enabled)
	}

	disabled := manager.applyPHPExtensions("8.4.19", enabled, []string{"mbstring"})
	if !strings.Contains(disabled, ";extension=exif      ; Must be after mbstring as it depends on it") {
		t.Fatalf("expected exif comment preserved when disabling, got: %s", disabled)
	}
}

func TestPHPFastCGIPIDPathLivesUnderRuntimePHP(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	wantBase := filepath.Join(root, "data", "runtime", "php")
	if got := manager.phpFastCGIPIDPath("8.4.19"); got != filepath.Join(wantBase, "php-cgi-8-4-19.pid") {
		t.Fatalf("unexpected php fastcgi pid path: %s", got)
	}
}

func TestSavePHPExtensionsCreatesPHPIniWithoutJSONSettings(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	phpRoot := manager.phpRoot("8.4.19")
	if err := os.MkdirAll(filepath.Join(phpRoot, "ext"), 0o755); err != nil {
		t.Fatalf("mkdir php ext dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(phpRoot, "extras"), 0o755); err != nil {
		t.Fatalf("mkdir php extras dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phpRoot, "php.exe"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write php.exe: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phpRoot, "php.ini-production"), []byte(strings.Join([]string{
		`;extension=mbstring`,
		`;extension=exif      ; Must be after mbstring as it depends on it`,
		`;extension=ftp`,
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write php.ini-production: %v", err)
	}

	if err := manager.SavePHPExtensions("8.4.19", []string{"mbstring", "exif"}); err != nil {
		t.Fatalf("save php extensions: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(phpRoot, "php.ini"))
	if err != nil {
		t.Fatalf("read php.ini: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "extension=exif      ; Must be after mbstring as it depends on it") {
		t.Fatalf("expected exif enabled in php.ini, got: %s", text)
	}
	if !strings.Contains(text, ";extension=ftp") {
		t.Fatalf("expected ftp disabled in php.ini, got: %s", text)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "runtime", "php", "php-extension-settings.json")); !os.IsNotExist(err) {
		t.Fatalf("expected no php-extension-settings.json file, got err=%v", err)
	}
}

func TestConfigureApacheGeneratesFastCGIConfig(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	confPath := manager.httpdConfPath()
	if err := os.MkdirAll(filepath.Dir(confPath), 0o755); err != nil {
		t.Fatalf("mkdir conf dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(manager.apacheRoot(), "htdocs"), 0o755); err != nil {
		t.Fatalf("mkdir htdocs: %v", err)
	}
	if err := os.MkdirAll(manager.phpRoot("8.4.19"), 0o755); err != nil {
		t.Fatalf("mkdir php root: %v", err)
	}
	if err := os.WriteFile(manager.phpExe("8.4.19"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write php exe: %v", err)
	}
	if err := os.WriteFile(manager.phpCgiExe("8.4.19"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write php-cgi exe: %v", err)
	}

	conf := "ServerRoot \"C:/Apache24\"\nListen 80\nServerName localhost:80\nDirectoryIndex index.html\n" +
		"# LoadModule proxy_module modules/mod_proxy.so\n# LoadModule proxy_fcgi_module modules/mod_proxy_fcgi.so\n" +
		"LoadModule php_module \"C:/php/php8apache2_4.dll\"\nPHPIniDir \"C:/php\"\nAddHandler application/x-httpd-php .php\nAddType application/x-httpd-php .php\n"
	if err := os.WriteFile(confPath, []byte(conf), 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}

	if err := manager.configureApache("8.4.19"); err != nil {
		t.Fatalf("configure apache: %v", err)
	}

	updated, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatalf("read conf: %v", err)
	}
	text := string(updated)
	if strings.Contains(text, "php8apache2_4.dll") || strings.Contains(text, "PHPIniDir") {
		t.Fatalf("expected mod_php directives removed: %s", text)
	}
	if !strings.Contains(text, "LoadModule proxy_module modules/mod_proxy.so") || !strings.Contains(text, "LoadModule proxy_fcgi_module modules/mod_proxy_fcgi.so") {
		t.Fatalf("expected proxy modules enabled: %s", text)
	}
	if !strings.Contains(text, "SetHandler \"proxy:fcgi://127.0.0.1:") {
		t.Fatalf("expected fastcgi handler configured: %s", text)
	}
	if !strings.Contains(text, "IncludeOptional "+manager.apacheVHostIncludePath()) {
		t.Fatalf("expected generated vhost include: %s", text)
	}
}

func TestWriteApacheVHostConfigUsesSitePHPVersion(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	sitePath := filepath.Join(root, "www", "abc.test")
	if err := os.MkdirAll(sitePath, 0o755); err != nil {
		t.Fatalf("mkdir site path: %v", err)
	}
	if err := os.MkdirAll(manager.phpRoot("8.3.30"), 0o755); err != nil {
		t.Fatalf("mkdir php root: %v", err)
	}
	if err := os.WriteFile(manager.phpCgiExe("8.3.30"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write php-cgi: %v", err)
	}
	if err := saveWebsiteConfig(root, websiteConfig{
		Domain:     "abc.test",
		Path:       sitePath,
		PHPVersion: "8.3.30",
		Status:     "running",
	}); err != nil {
		t.Fatalf("save website config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(manager.httpdConfPath()), 0o755); err != nil {
		t.Fatalf("mkdir apache conf dir: %v", err)
	}
	if err := os.WriteFile(manager.httpdConfPath(), []byte("ServerRoot \"C:/Apache24\"\n"), 0o644); err != nil {
		t.Fatalf("write httpd conf: %v", err)
	}

	if err := manager.writeApacheVHostConfig(); err != nil {
		t.Fatalf("write vhost config: %v", err)
	}

	content, err := os.ReadFile(getSiteVHostConfigPath(root, "abc.test"))
	if err != nil {
		t.Fatalf("read vhost config: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "ServerName abc.test") {
		t.Fatalf("expected vhost for site: %s", text)
	}
	if !strings.Contains(text, "proxy:fcgi://127.0.0.1:"+strconv.Itoa(phpFastCGIPort("8.3.30"))) {
		t.Fatalf("expected php version specific fastcgi port: %s", text)
	}
	if _, err := os.Stat(getSiteConfigPath(root, "abc.test")); err != nil {
		t.Fatalf("expected site config stored in site directory: %v", err)
	}
}

func TestWriteApacheVHostConfigCreatesSiteFileWithoutApacheInstall(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	sitePath := filepath.Join(root, "www", "missing-php.test")
	if err := os.MkdirAll(sitePath, 0o755); err != nil {
		t.Fatalf("mkdir site path: %v", err)
	}
	if err := saveWebsiteConfig(root, websiteConfig{
		Domain:     "missing-php.test",
		Path:       sitePath,
		PHPVersion: "8.4.19",
		Status:     "running",
	}); err != nil {
		t.Fatalf("save website config: %v", err)
	}

	if err := manager.writeApacheVHostConfig(); err != nil {
		t.Fatalf("write vhost config: %v", err)
	}

	content, err := os.ReadFile(getSiteVHostConfigPath(root, "missing-php.test"))
	if err != nil {
		t.Fatalf("read site vhost config: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "# Disabled because the selected PHP runtime is unavailable.") {
		t.Fatalf("expected disabled site vhost comment, got: %s", text)
	}
}

func TestGetWebsiteMigratesLegacyConfigIntoSiteDirectory(t *testing.T) {
	root := t.TempDir()
	legacyConfig := websiteConfig{
		Domain:     "legacy.test",
		Path:       filepath.Join(root, "www", "legacy.test"),
		PHPVersion: "8.3.30",
		Status:     "running",
	}
	if err := os.MkdirAll(filepath.Dir(getLegacySiteConfigPath(root, legacyConfig.Domain)), 0o755); err != nil {
		t.Fatalf("mkdir legacy config dir: %v", err)
	}
	content, err := json.MarshalIndent(legacyConfig, "", "  ")
	if err != nil {
		t.Fatalf("marshal legacy config: %v", err)
	}
	if err := os.WriteFile(getLegacySiteConfigPath(root, legacyConfig.Domain), content, 0o644); err != nil {
		t.Fatalf("write legacy config: %v", err)
	}

	record, err := getWebsite(root, legacyConfig.Domain)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	if record.Domain != legacyConfig.Domain || record.PHPVersion != legacyConfig.PHPVersion {
		t.Fatalf("unexpected migrated record: %+v", record)
	}
	if _, err := os.Stat(getSiteConfigPath(root, legacyConfig.Domain)); err != nil {
		t.Fatalf("expected migrated site config in site directory: %v", err)
	}
	if _, err := os.Stat(getLegacySiteConfigPath(root, legacyConfig.Domain)); !os.IsNotExist(err) {
		t.Fatalf("expected legacy config removed, got err=%v", err)
	}
}

func TestListAppsIgnoresStaleInstalledVersionsMetadata(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	phpVersion := "8.3.30"
	phpDir := manager.phpRoot(phpVersion)
	if err := os.MkdirAll(phpDir, 0o755); err != nil {
		t.Fatalf("mkdir php dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phpDir, "php.exe"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write php.exe: %v", err)
	}

	stale := map[string]string{
		"php":        "8.4.19",
		"php:8.4.19": "8.4.19",
	}
	content, err := json.Marshal(stale)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(manager.installedVersionsPath()), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(manager.installedVersionsPath(), content, 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	apps := manager.listApps()

	var phpApp runtimeApp
	found := false
	for _, app := range apps {
		if app.ID == "php" {
			phpApp = app
			found = true
			break
		}
	}
	if !found {
		t.Fatal("php app not found")
	}

	if !phpApp.Installed {
		t.Fatal("expected php app to be installed from runtime directory")
	}
	if phpApp.Version != phpVersion {
		t.Fatalf("expected detected php version %s, got %s", phpVersion, phpApp.Version)
	}
	if len(phpApp.InstalledVersions) != 1 || phpApp.InstalledVersions[0] != phpVersion {
		t.Fatalf("expected installed versions [%s], got %#v", phpVersion, phpApp.InstalledVersions)
	}
	if manager.appInstalled("php:8.4.19") {
		t.Fatal("expected stale metadata version to be treated as not installed")
	}
}

func TestInstallMetadataIsRemovedOnUninstall(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	if err := os.MkdirAll(filepath.Dir(manager.installedVersionsPath()), 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	if err := os.WriteFile(manager.installedVersionsPath(), []byte(`{"php":"8.4.19"}`), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	phpDir := manager.phpRoot("8.4.19")
	if err := os.MkdirAll(phpDir, 0o755); err != nil {
		t.Fatalf("mkdir php dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(phpDir, "php.exe"), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write php.exe: %v", err)
	}

	if err := manager.uninstallPHP("8.4.19"); err != nil {
		t.Fatalf("uninstall php: %v", err)
	}

	if _, err := os.Stat(manager.installedVersionsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected metadata file removed, got err=%v", err)
	}
}

func TestApacheInstalledWithoutPHPStillShowsStart(t *testing.T) {
	root := t.TempDir()
	manager := &windowsRuntimeManager{projectRoot: root}

	if err := os.MkdirAll(filepath.Dir(manager.apacheExe()), 0o755); err != nil {
		t.Fatalf("mkdir apache exe dir: %v", err)
	}
	if err := os.WriteFile(manager.apacheExe(), []byte("stub"), 0o644); err != nil {
		t.Fatalf("write apache exe: %v", err)
	}

	apps := manager.listApps()

	var apacheApp runtimeApp
	found := false
	for _, app := range apps {
		if app.ID == "apache" {
			apacheApp = app
			found = true
			break
		}
	}
	if !found {
		t.Fatal("apache app not found")
	}

	if !apacheApp.Installed {
		t.Fatal("expected apache to be marked installed")
	}
	if !apacheApp.CanStart {
		t.Fatal("expected apache start to remain available after install")
	}
	if apacheApp.StatusLabel != "Installed" {
		t.Fatalf("unexpected apache status label: %s", apacheApp.StatusLabel)
	}
}
