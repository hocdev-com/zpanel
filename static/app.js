const websiteListEl = document.getElementById("website-list");
const databaseListEl = document.getElementById("database-list");
const appStoreListEl = document.getElementById("app-store-list");
const appStoreSettingsFormEl = document.getElementById("app-store-settings-form");
const appStoreSettingsPathEl = document.getElementById("app-store-settings-path");
const sectionTitleEl = document.getElementById("section-title");
const sectionCopyEl = document.getElementById("section-copy");
const toastStackEl = document.getElementById("toast-stack");
const websiteModalEl = document.getElementById("website-modal");
const websiteFormEl = document.getElementById("website-form");
const websiteDomainInputEl = document.getElementById("domain");
const websitePathInputEl = document.getElementById("path");
const websitePHPVersionEl = document.getElementById("php-version");
const websiteSubmitButtonEl = websiteFormEl.querySelector("button[type='submit']");
const sidebarEl = document.getElementById("sidebar");
const sidebarToggleEl = document.getElementById("sidebar-toggle");
const sidebarCloseEl = document.getElementById("sidebar-close");
const sidebarBackdropEl = document.getElementById("sidebar-backdrop");
const WEBSITE_CACHE_KEY = "zpanel:websites";
const STATUS_REFRESH_VISIBLE_MS = 5000;
const STATUS_REFRESH_HIDDEN_MS = 60000;
let statusRefreshTimer = null;
let statusRefreshInFlight = null;
let collectionRefreshInFlight = {
    apps: null,
    settings: null,
    websites: null,
    databases: null,
};
let collectionLoaded = {
    apps: false,
    settings: false,
    websites: false,
    databases: false,
};
let connectionState = "unknown";
let persistentToastByKey = new Map();
let appActionInFlight = new Set();
let latestAppsPayload = [];
let websitePathTouched = false;
let appInstallJobs = new Map();
let appInstallPollers = new Map();
let appInstallPollRequests = new Set();
let appRefreshRequestSeq = 0;
let appRefreshAppliedSeq = 0;
let activeView = "overview";

function isMobileSidebar() {
    return window.matchMedia("(max-width: 980px)").matches;
}

function syncSidebarAccessibility(isOpen) {
    if (!sidebarEl) {
        return;
    }

    const mobileSidebar = isMobileSidebar();
    const shouldHide = mobileSidebar && !isOpen;
    const activeElement = document.activeElement;

    if (shouldHide && activeElement instanceof HTMLElement && sidebarEl.contains(activeElement)) {
        activeElement.blur();
        sidebarToggleEl?.focus();
    }

    sidebarEl.setAttribute("aria-hidden", shouldHide ? "true" : "false");

    if ("inert" in sidebarEl) {
        sidebarEl.inert = shouldHide;
    }
}

function setSidebarOpen(isOpen) {
    const shouldOpen = Boolean(isOpen) && isMobileSidebar();
    document.body.classList.toggle("sidebar-open", shouldOpen);
    sidebarToggleEl?.setAttribute("aria-expanded", shouldOpen ? "true" : "false");
    syncSidebarAccessibility(shouldOpen);
}

function closeSidebar() {
    setSidebarOpen(false);
}

function toggleSidebar() {
    setSidebarOpen(!document.body.classList.contains("sidebar-open"));
}

function escapeHTML(value) {
    return String(value ?? "")
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/"/g, "&quot;")
        .replace(/'/g, "&#39;");
}

const viewConfig = {
    overview: {
        title: "Overview",
        copy: "Monitor system status and use the left menu to manage websites or databases.",
    },
    apps: {
        title: "App Store",
        copy: "Install and control the bundled Apache, PHP, and MySQL runtime from data/runtime.",
    },
    settings: {
        title: "Settings",
        copy: "Edit App Store values and keep download links stored in the data folder instead of hardcoded in code.",
    },
    websites: {
        title: "Websites",
        copy: "Create websites and manage hosted projects from this section.",
    },
    databases: {
        title: "Databases",
        copy: "Create databases and review available data services here.",
    },
};

function showResult(payload) {
    const isError = typeof payload === "string" && payload.toLowerCase().startsWith("error:");
    const title = isError ? "Action Failed" : "Completed";
    const message = typeof payload === "string"
        ? payload.replace(/^Error:\s*/i, "")
        : payload?.message || payload?.status || "Request completed.";

    const toast = document.createElement("div");
    toast.className = `toast ${isError ? "error" : "success"}`;
    toast.innerHTML = `
        <div class="toast-title">${title}</div>
        <div class="toast-message">${message}</div>
    `;

    toastStackEl.appendChild(toast);
    requestAnimationFrame(() => toast.classList.add("show"));

    setTimeout(() => {
        toast.classList.remove("show");
        setTimeout(() => toast.remove(), 180);
    }, 2600);
}

function showPersistentToast(key, title, message, variant = "error") {
    const existingToast = persistentToastByKey.get(key);
    if (existingToast) {
        existingToast.querySelector(".toast-title").textContent = title;
        existingToast.querySelector(".toast-message").textContent = message;
        return existingToast;
    }

    const toast = document.createElement("div");
    toast.className = `toast ${variant} persistent show`;
    toast.innerHTML = `
        <div class="toast-title">${title}</div>
        <div class="toast-message">${message}</div>
    `;

    persistentToastByKey.set(key, toast);
    toastStackEl.appendChild(toast);
    return toast;
}

function updatePersistentToast(key, title, message, variant = "error") {
    const toast = showPersistentToast(key, title, message, variant);
    toast.className = `toast ${variant} persistent show`;
}

function removePersistentToast(key) {
    const toast = persistentToastByKey.get(key);
    if (!toast) {
        return;
    }

    persistentToastByKey.delete(key);
    toast.classList.remove("show");
    setTimeout(() => toast.remove(), 180);
}

function showConnectionError(error) {
    const message = error?.message || "Failed to fetch";
    showPersistentToast("connection-status", "Action Failed", message, "error");
}

function setPanelOfflineState(error) {
    const message = error?.message || "Failed to fetch";
    connectionState = "offline";
    showConnectionError(error);
    document.getElementById("resource-system-label").textContent = "Panel offline";
    document.getElementById("resource-uptime").textContent = "Waiting for service";
    document.getElementById("website-count").textContent = "-";
    document.getElementById("ftp-count").textContent = "-";
    document.getElementById("database-count").textContent = "-";
    document.getElementById("cpu-usage").textContent = "-";
    document.getElementById("ram-usage").textContent = "-";
    document.getElementById("ram-copy").textContent = "Panel is not connected";
    document.getElementById("software-status-apache").textContent = "Unavailable";
    document.getElementById("software-status-php").textContent = "Unavailable";
    document.getElementById("software-status-mysql").textContent = "Unavailable";
    document.getElementById("upload-speed").textContent = "-";
    document.getElementById("download-speed").textContent = "-";
    document.getElementById("total-sent").textContent = "-";
    document.getElementById("total-received").textContent = "-";
    databaseListEl.replaceChildren();
    appStoreListEl.replaceChildren();
    renderCachedWebsites();
    updateNetworkChart([0, 0]);
}

function loadCachedWebsites() {
    try {
        const raw = window.localStorage.getItem(WEBSITE_CACHE_KEY);
        if (!raw) {
            return [];
        }
        const websites = JSON.parse(raw);
        return Array.isArray(websites) ? websites : [];
    } catch {
        return [];
    }
}

function saveCachedWebsites(websites) {
    try {
        window.localStorage.setItem(WEBSITE_CACHE_KEY, JSON.stringify(Array.isArray(websites) ? websites : []));
    } catch { }
}

function renderWebsiteList(websites) {
    const websiteItems = Array.isArray(websites) ? websites : [];
    websiteListEl.replaceChildren();

    if (websiteItems.length === 0) {
        websiteListEl.innerHTML = `<div class="website-empty">No websites created yet. Add your first domain to generate a local site instantly.</div>`;
        return;
    }

    websiteItems.forEach((website) => websiteListEl.appendChild(websiteCard(website)));
}

function renderCachedWebsites() {
    const cachedWebsites = loadCachedWebsites();
    if (cachedWebsites.length === 0) {
        return;
    }

    document.getElementById("website-count").textContent = cachedWebsites.length;
    renderWebsiteList(cachedWebsites);
}

function formatKilobytes(value) {
    return `${value.toFixed(2)} KB`;
}

function formatGigabytes(value) {
    return `${value.toFixed(2)} GB`;
}

function formatUptime(totalSeconds) {
    const seconds = Math.max(0, Number(totalSeconds) || 0);
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const remainingSeconds = Math.floor(seconds % 60);

    return `${days}:${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}:${String(remainingSeconds).padStart(2, "0")}`;
}

function formatMemorySummary(usedMb, totalMb) {
    const usedGb = usedMb / 1024;
    const totalGb = totalMb / 1024;
    return `${usedGb.toFixed(1)}/${totalGb.toFixed(1)} GB`;
}

function updateNetworkChart(history) {
    const networkHistory = Array.isArray(history) && history.length > 1
        ? history.map((value) => Math.max(0, Number(value) || 0))
        : [0, 0];
    const width = 360;
    const height = 140;
    const maxValue = Math.max(...networkHistory, 1);
    const step = width / (networkHistory.length - 1);
    const points = networkHistory.map((entry, index) => {
        const x = (step * index).toFixed(1);
        const y = (height - (entry / maxValue) * 110 - 12).toFixed(1);
        return `${x},${y}`;
    });

    const areaPoints = [`0,140`, ...points, `${width},140`].join(" ");
    document.getElementById("network-line").setAttribute("points", points.join(" "));
    document.getElementById("network-area").setAttribute("d", `M ${areaPoints} Z`);
}

async function api(path, options = {}) {
    const response = await fetch(path, {
        headers: {
            "Content-Type": "application/json",
            ...(options.headers || {}),
        },
        ...options,
    });

    const data = await response.json();
    if (!response.ok) {
        throw new Error(data.error || "request failed");
    }
    return data;
}

async function loadStatus() {
    const data = await api("/api/status");
    const websites = Number(data.websites ?? 0);
    const databases = Number(data.databases ?? 0);
    const ftpCount = data.status === "running" ? 1 : 0;
    const cpuUsagePercent = Number(data.cpu_usage_percent ?? 0);
    const ramUsedMb = Number(data.ram_used_mb ?? 0);
    const ramTotalMb = Number(data.ram_total_mb ?? data.max_memory_mb ?? 0);
    const ramUsagePercent = Number(data.ram_usage_percent ?? 0);
    const uploadSpeedKbps = Number(data.upload_speed_kbps ?? 0);
    const downloadSpeedKbps = Number(data.download_speed_kbps ?? 0);
    const totalSentGb = Number(data.total_sent_gb ?? 0);
    const totalReceivedGb = Number(data.total_received_gb ?? 0);

    document.getElementById("resource-system-label").textContent = data.os_label || "System Resource";
    document.getElementById("resource-uptime").textContent = formatUptime(data.uptime_seconds);
    document.getElementById("website-count").textContent = websites;
    document.getElementById("ftp-count").textContent = ftpCount;
    document.getElementById("database-count").textContent = databases;
    document.getElementById("cpu-usage").textContent = `${cpuUsagePercent}%`;
    document.getElementById("ram-usage").textContent = `${ramUsagePercent}%`;
    document.getElementById("ram-copy").textContent = formatMemorySummary(ramUsedMb, ramTotalMb);
    document.getElementById("upload-speed").textContent = formatKilobytes(uploadSpeedKbps);
    document.getElementById("download-speed").textContent = formatKilobytes(downloadSpeedKbps);
    document.getElementById("total-sent").textContent = formatGigabytes(totalSentGb);
    document.getElementById("total-received").textContent = formatGigabytes(totalReceivedGb);
    updateNetworkChart(data.network_history_kbps);
}

async function refreshStatusOnly() {
    if (statusRefreshInFlight) {
        return statusRefreshInFlight;
    }

    statusRefreshInFlight = loadStatus()
        .then((result) => {
            removePersistentToast("connection-status");
            const recoveredFromOffline = connectionState === "offline";
            if (recoveredFromOffline) {
                return ensureViewData(activeView, { force: true })
                    .then(() => {
                        connectionState = "online";
                        return result;
                    })
                    .catch((error) => {
                        connectionState = "offline";
                        throw error;
                    });
            }
            connectionState = "online";
            return result;
        })
        .catch((error) => {
            setPanelOfflineState(error);
            return null;
        })
        .finally(() => {
            statusRefreshInFlight = null;
        });

    return statusRefreshInFlight;
}

async function refreshCollections(views = ["apps", "websites", "databases"]) {
    const tasks = [];

    if (views.includes("apps")) {
        tasks.push(refreshApps());
    }
    if (views.includes("websites")) {
        tasks.push(refreshWebsites());
    }
    if (views.includes("databases")) {
        tasks.push(refreshDatabases());
    }

    return Promise.all(tasks);
}

async function ensureViewData(view, options = {}) {
    const force = Boolean(options.force);

    if (view === "overview") {
        if (!force && collectionLoaded.apps) {
            return;
        }
        await refreshApps();
        return;
    }

    if (view === "apps") {
        if (!force && collectionLoaded.apps) {
            return;
        }
        await refreshApps();
        return;
    }

    if (view === "settings") {
        if (!force && collectionLoaded.settings) {
            return;
        }
        await refreshAppStoreSettings();
        return;
    }

    if (view === "websites") {
        if (!force && collectionLoaded.websites) {
            return;
        }
        await refreshWebsites();
        return;
    }

    if (view === "databases") {
        if (!force && collectionLoaded.databases) {
            return;
        }
        await refreshDatabases();
    }
}

function websiteCard(website) {
    const wrapper = document.createElement("article");
    wrapper.className = "website-row";
    const websiteUrl = website.url || "";
    const isRunning = website.status === "running";
    const displayPath = String(website.path || "").replace(/\\/g, "/");
    const phpVersion = website.php_version || "8.4";
    const requestCount = isRunning ? "9" : "0";
    const websiteName = websiteUrl
        ? `<a class="website-domain-link" href="${websiteUrl}" target="_blank" rel="noreferrer">${website.domain}</a>`
        : `<span class="website-domain-link disabled">${website.domain}</span>`;
    wrapper.innerHTML = `
        <div class="website-check">
            <input type="checkbox" aria-label="Select ${website.domain}" />
        </div>
        <div class="website-site">
            ${websiteName}
            <small>${displayPath.replace(/^.*\/www\//i, "")}</small>
        </div>
        <button class="website-status ${isRunning ? "running" : "stopped"}" data-action="${isRunning ? "website-stop" : "website-start"}" data-domain="${website.domain}" title="${isRunning ? "Stop site" : "Start site"}" aria-label="${isRunning ? "Stop site" : "Start site"}" type="button"></button>
        <div class="website-php-version">PHP ${phpVersion}</div>
        <div class="website-expiration">Perpetual</div>
        <div class="website-ssl ${isRunning ? "ok" : "expired"}">${isRunning ? "Ready" : "Expired"}</div>
        <div class="website-requests">
            <span class="website-requests-chart" aria-label="Requests activity">
                <i></i><i></i><i></i><i></i><i></i><i></i>
            </span>
        </div>
        <div class="website-waf">--</div>
        <div class="website-actions website-operate">
            <button class="ghost" data-action="website-delete" data-domain="${website.domain}">Delete</button>
        </div>
    `;
    return wrapper;
}

function databaseCard(database) {
    const wrapper = document.createElement("article");
    wrapper.className = "card";
    wrapper.innerHTML = `
        <strong>${database.name}</strong>
        <div class="meta">Type: ${database.db_type}</div>
        <div class="meta">Status: ${database.status}</div>
    `;
    return wrapper;
}

function normalizeAppDisplayName(name) {
    // Backend now provides the display title from panel.db (e.g. "Apache Server", "PHP Runtime")
    const n = String(name || "").trim();
    if (n === "Apache HTTP Server") return "Apache Server";
    if (n === "MySQL Community Server") return "MySQL Server";
    return n;
}

function appStoreRow(app) {
    const wrapper = document.createElement("article");
    wrapper.className = "app-table-row";
    wrapper.dataset.appId = app.id;
    // For PHP per-version rows, track specific version via row-version
    if (app._rowVersion) wrapper.dataset.rowVersion = app._rowVersion;
    const displayVersion = app._rowVersion || app.version;
    const dashboardToggleId = app._rowVersion
        ? (String(app.id || "").includes(":") ? app.id : `${app.id}:${app._rowVersion}`)
        : app.id;
    const jobKey = app.id === "php" ? `${app.id}:${displayVersion}` : app.id;
    const installJob = appInstallJobs.get(jobKey);

    const badgeClass = app.id === "apache"
        ? "apache-badge"
        : app.id.startsWith("php")
            ? "php-badge"
            : "mysql-badge";
    const badgeLabel = app.id.startsWith("php") ? "PHP" : app.id === "mysql" ? "MY" : "AP";

    const statusClass = app.status === "running" ? "running" : app.status === "stopped" ? "stopped" : "not-installed";
    const statusLabel = app.status_label || "Not installed";

    // --- Operate buttons: install OR uninstall, never both ---
    let operateBtns = "";
    if (app.installed) {
        // Installed: show Start/Stop + Setting + Uninstall only
        const startStopBtn = app.can_start
            ? `<button class="op-btn op-start" data-app-action="start" data-app-id="${app.id}">Start</button>`
            : app.can_stop
                ? `<button class="op-btn op-stop warn" data-app-action="stop" data-app-id="${app.id}">Stop</button>`
                : "";
        const settingBtn = `<button class="op-btn op-setting" data-app-action="setting" data-app-id="${app.id}" data-app-version="${app._rowVersion || ""}">Setting</button>`;
        const uninstallBtn = app.can_uninstall
            ? `<button class="op-btn op-uninstall" data-app-action="uninstall" data-app-id="${app.id}">Uninstall</button>`
            : "";
        operateBtns = startStopBtn + settingBtn + uninstallBtn;
    } else {
        // Not installed: show Install combo only
        const installVersion = displayVersion || "";
        const installLabel = installJob ? "Install..." : "Install";
        const isDisabled = installJob ? "disabled" : "";
        // For PHP per-version rows, no version dropdown — just a single Install button
        if (app._rowVersion) {
            operateBtns = `<button class="alt op-btn" data-app-action="install" data-app-id="${app.id}" data-app-version="${installVersion}" ${isDisabled}>${installLabel}</button>`;
        } else {
            const availableVersions = Array.isArray(app.available_versions) ? app.available_versions : [];
            const versionOptions = availableVersions.map((v) => {
                const activeClass = String(v) === String(installVersion) ? " active" : "";
                return `<button class="app-version-option${activeClass}" type="button" data-app-version-option="${app.id}" data-version="${v}">${v}</button>`;
            }).join("");
            const versionDropdown = availableVersions.length > 1 ? `
                <button class="app-install-toggle" type="button" aria-label="Choose version" data-app-version-toggle="${app.id}" ${isDisabled}>▼</button>
                <div class="app-version-menu" data-app-version-menu="${app.id}" hidden>${versionOptions}</div>
            ` : "";
            operateBtns = `
                <div class="app-install-combo" data-app-install-combo="${app.id}">
                    <button class="alt app-install-main" data-app-action="install" data-app-id="${app.id}" data-app-version="${installVersion}" ${isDisabled}>${installLabel}</button>
                    ${versionDropdown}
                </div>
            `;
        }
    }

    wrapper.innerHTML = `
        <div class="app-table-cell app-name-cell">
            <div class="software-badge ${badgeClass}">${badgeLabel}</div>
            <div class="app-name-info">
                <strong>${normalizeAppDisplayName(app.name)}</strong>
                <span class="app-version-label">v${displayVersion}</span>
            </div>
        </div>
        <div class="app-table-cell">
            <span class="app-developer">${app.developer || "official"}</span>
        </div>
        <div class="app-table-cell app-instructions-cell">
            <span>${app.description}</span>
        </div>
        <div class="app-table-cell">
            <button class="app-folder-btn ${!app.installed ? "disabled" : ""}" data-app-action="open-folder" data-app-id="${app.id}" ${!app.installed ? "disabled" : ""} title="Open install folder">
                <span class="folder-icon">📁</span>
            </button>
        </div>
        <div class="app-table-cell app-status-cell">
            <span class="app-status-pill ${statusClass}">${statusLabel}</span>
        </div>
        <div class="app-table-cell app-toggle-cell">
            <label class="toggle-switch">
                <input type="checkbox" data-app-dashboard-toggle="${dashboardToggleId}" ${app.show_on_dashboard ? "checked" : ""}>
                <span class="toggle-track"><span class="toggle-thumb"></span></span>
            </label>
        </div>
        <div class="app-table-cell app-operate-cell">
            <div class="app-operate-row">
                ${operateBtns}
                ${(installJob && (!app._rowVersion || String(installJob.version) === String(app._rowVersion))) ? `
                    <div class="app-install-progress">
                        <div class="app-install-progress-head">
                            <strong>${Math.max(0, Math.min(100, Number(installJob.progress) || 0))}%</strong>
                        </div>
                        <div class="app-install-progress-track">
                            <div class="app-install-progress-bar" style="width:${Math.max(0, Math.min(100, Number(installJob.progress) || 0))}%"></div>
                        </div>
                    </div>
                ` : ""}
            </div>
        </div>
    `;
    wrapper.classList.toggle("busy", Boolean(installJob));
    return wrapper;
}

function buildPHPVersionRows(app) {
    const versionSet = new Set();
    const installedVersions = Array.isArray(app.installed_versions) ? app.installed_versions : [];
    const runningVersions = Array.isArray(app.running_versions) ? app.running_versions : [];
    const availableVersions = Array.isArray(app.available_versions) ? app.available_versions : [];
    const dashboardVersions = app.show_on_dashboard_versions ? Object.keys(app.show_on_dashboard_versions) : [];

    installedVersions.forEach((version) => versionSet.add(version));
    availableVersions.forEach((version) => versionSet.add(version));
    runningVersions.forEach((version) => versionSet.add(version));
    dashboardVersions.forEach((version) => versionSet.add(version));
    if (app.version) {
        versionSet.add(app.version);
    }

    return Array.from(versionSet).map((ver, idx) => {
        const isThisVersionInstalled = installedVersions.includes(ver);
        const isThisVersionBusy = app.status === "installing" && String(app.selected_version) === String(ver);
        const isThisVersionRunning = runningVersions.includes(ver);
        let status = "not-installed";
        let statusLabel = "Not installed";

        if (isThisVersionInstalled) {
            status = isThisVersionRunning ? "running" : "stopped";
            statusLabel = isThisVersionRunning ? "Running" : "Installed";
        }

        if (isThisVersionBusy) {
            status = "installing";
            statusLabel = app.status_label;
        }

        return {
            ...app,
            _rowVersion: ver,
            _isFirstPhpRow: idx === 0,
            name: app.version_titles?.[ver] || `${app.name} ${ver}`,
            version: ver,
            installed: isThisVersionInstalled,
            can_install: !isThisVersionInstalled,
            can_uninstall: isThisVersionInstalled,
            can_start: isThisVersionInstalled ? !isThisVersionRunning : false,
            can_stop: isThisVersionInstalled ? isThisVersionRunning : false,
            status,
            status_label: statusLabel,
            show_on_dashboard: app.show_on_dashboard_versions ? app.show_on_dashboard_versions[ver] : app.show_on_dashboard,
            id: `php:${ver}`,
        };
    });
}

function buildGenericVersionRows(app) {
    const availableVersions = Array.isArray(app.available_versions) ? app.available_versions : [];
    if (availableVersions.length === 0) return [app];
    
    return availableVersions.map((ver, idx) => {
        const isThisVersionInstalled = app.installed && String(app.version) === String(ver);
        const isShown = app.show_on_dashboard_versions ? app.show_on_dashboard_versions[ver] : (isThisVersionInstalled && app.show_on_dashboard);
        
        return {
            ...app,
            _rowVersion: ver,
            _isFirstRow: idx === 0,
            name: app.version_titles?.[ver] || `${app.name} ${ver}`,
            version: ver,
            installed: isThisVersionInstalled,
            can_install: !isThisVersionInstalled,
            can_uninstall: isThisVersionInstalled,
            can_start: isThisVersionInstalled ? app.can_start : false,
            can_stop: isThisVersionInstalled ? app.can_stop : false,
            status: isThisVersionInstalled ? app.status : "not-installed",
            status_label: isThisVersionInstalled ? app.status_label : "Not installed",
            show_on_dashboard: isShown,
            id: app.id, // Generic apps keep their base ID for API calls
        };
    });
}

function expandAppsForDisplay(apps) {
    const appItems = Array.isArray(apps) ? apps : [];
    return appItems.flatMap((app) => {
        if (app.id === "php") {
            return buildPHPVersionRows(app);
        }
        return buildGenericVersionRows(app);
    });
}

function updateSoftwareSnapshot(apps) {
    const listContainer = document.getElementById("dashboard-software-list");
    if (!listContainer) return;

    listContainer.innerHTML = "";
    const appItems = expandAppsForDisplay(apps);
    let count = 0;
    const MAX_CARDS = 6;

    for (const app of appItems) {
        if (count >= MAX_CARDS) break;
        if (!app.show_on_dashboard) continue;

        const cardId = String(app.id || "").startsWith("php:") ? "php" : app.id;
        const cardName = app.name;
        listContainer.appendChild(createSoftwareCard(cardId, cardName, app.version, app.status_label || "Running"));
        count++;
    }
}

function createSoftwareCard(id, name, version, status) {
    const card = document.createElement("article");
    card.className = "software-card";
    card.dataset.softwareId = id;

    const displayName = normalizeAppDisplayName(name);
    const statusClass = String(status || "").toLowerCase() === "not installed"
        ? "status-not-installed"
        : "status-installed";

    const badgeClass = id === "apache" ? "apache-badge" : id === "php" ? "php-badge" : "mysql-badge";
    let iconSvg = "";
    if (id === "apache") {
        iconSvg = `<svg viewBox="0 0 48 48" class="software-icon software-icon-apache"><path d="M12 36c8-10 16-10 24 0" /><path d="M10 28c10-8 18-8 28 0" /><path d="M14 20c7-6 13-6 20 0" /></svg>`;
    } else if (id === "php") {
        iconSvg = `<svg viewBox="0 0 48 48" class="software-icon software-icon-php"><ellipse cx="24" cy="24" rx="18" ry="12"></ellipse><text x="24" y="28" text-anchor="middle">PHP</text></svg>`;
    } else if (id === "mysql") {
        iconSvg = `<svg viewBox="0 0 48 48" class="software-icon software-icon-mysql"><path d="M10 29c4-7 11-11 18-11 5 0 8 2 11 5" /><path d="M18 29c4 0 7 2 10 5" /><path d="M28 23c1-3 4-5 7-6" /><path d="M36 16l2-4" /><path d="M36 16l4-1" /><circle cx="31" cy="21" r="1.5" fill="currentColor" stroke="none" /></svg>`;
    }

    card.innerHTML = `
        <div class="software-badge ${badgeClass}" aria-hidden="true">
            ${iconSvg}
        </div>
        <strong>${displayName}</strong>
        <span>v${version}</span>
        <small class="${statusClass}">${status}</small>
    `;
    return card;
}

function renderApps(apps) {
    const appItems = Array.isArray(apps) ? apps : [];
    latestAppsPayload = appItems;
    syncRunningInstallJobs(appItems);
    appStoreListEl.replaceChildren();

    if (appItems.length === 0) {
        appStoreListEl.textContent = "No portable apps available yet.";
        updateSoftwareSnapshot([]);
        return;
    }

    expandAppsForDisplay(appItems).forEach((app) => {
        appStoreListEl.appendChild(appStoreRow(app));
    });

    updateSoftwareSnapshot(appItems);
    syncPHPVersionOptions(appItems);
}

function getInstallJobKey(id, version = "") {
    return id === "php" ? `${id}:${version}` : id;
}

function syncRunningInstallJobs(apps) {
    const appItems = Array.isArray(apps) ? apps : [];

    appItems.forEach((app) => {
        if (app?.status !== "installing" || !app?.id) {
            return;
        }

        const version = app.id === "php"
            ? String(app.selected_version || app.version || "").trim()
            : "";
        const jobKey = getInstallJobKey(app.id, version);

        if (!appInstallJobs.has(jobKey)) {
            syncInstallJob({
                app_id: app.id,
                version,
                action: "install",
                status: "running",
                progress: Number(app.progress) || 0,
                message: app.status_label || "Installing...",
            }, jobKey);
        }

        if (!appInstallPollers.has(jobKey) && !appInstallPollRequests.has(jobKey)) {
            pollInstallJob(app.id, version).catch(() => { });
        }
    });
}

function getInstalledPHPVersions(apps) {
    const appItems = Array.isArray(apps) ? apps : [];
    const phpApp = appItems.find((app) => app.id === "php");
    if (phpApp && Array.isArray(phpApp.installed_versions)) {
        return phpApp.installed_versions;
    }
    // Fallback for older backend versions or non-PHP apps
    const versions = appItems
        .filter((app) => app.installed && (app.id === "php" || String(app.name || "").toLowerCase().includes("php")))
        .map((app) => String(app.version || "").trim())
        .filter(Boolean);

    return [...new Set(versions)];
}

function syncPHPVersionOptions(apps = latestAppsPayload) {
    const versions = getInstalledPHPVersions(apps);
    const previousValue = websitePHPVersionEl.value;
    websitePHPVersionEl.replaceChildren();

    if (versions.length === 0) {
        const option = document.createElement("option");
        option.value = "";
        option.textContent = "No PHP installed in App Store";
        websitePHPVersionEl.appendChild(option);
        websitePHPVersionEl.disabled = true;
        websiteSubmitButtonEl.disabled = true;
        return;
    }

    versions.forEach((version) => {
        const option = document.createElement("option");
        option.value = version;
        option.textContent = `PHP ${version}`;
        websitePHPVersionEl.appendChild(option);
    });

    websitePHPVersionEl.disabled = false;
    websiteSubmitButtonEl.disabled = false;

    if (versions.includes(previousValue)) {
        websitePHPVersionEl.value = previousValue;
    }
}

function renderAppStoreSettings(payload) {
    const groups = Array.isArray(payload?.groups) ? payload.groups : [];
    appStoreSettingsPathEl.textContent = payload?.file_path
        ? `Stored in ${payload.file_path}`
        : "";

    if (groups.length === 0) {
        appStoreSettingsFormEl.innerHTML = `<div class="card">No editable App Store values found.</div>`;
        return;
    }

    const rows = groups.flatMap((group) => {
        const releases = Array.isArray(group.releases) ? group.releases : [];
        return releases.map((release) => `
            <div class="app-store-settings-row">
                <span class="app-store-settings-title">
                    <input
                        type="text"
                        data-app-store-title
                        data-app-id="${escapeHTML(group.id || "")}"
                        data-app-version="${escapeHTML(release.version || "")}"
                        value="${escapeHTML(release.title || release.default_title || "")}"
                        placeholder="${escapeHTML(release.default_title || "")}"
                    />
                </span>
                <span class="app-store-settings-description">
                    <input
                        type="text"
                        data-app-store-description
                        data-app-id="${escapeHTML(group.id || "")}"
                        data-app-version="${escapeHTML(release.version || "")}"
                        value="${escapeHTML(release.description || release.default_description || "")}"
                        placeholder="${escapeHTML(release.default_description || "")}"
                    />
                </span>
                <span class="app-store-settings-link">
                    <input
                        type="url"
                        data-app-store-download
                        data-app-id="${escapeHTML(group.id || "")}"
                        data-app-version="${escapeHTML(release.version || "")}"
                        value="${escapeHTML(release.url || "")}"
                        placeholder="${escapeHTML(release.default_url || "")}"
                    />
                </span>
            </div>
        `);
    }).join("");

    appStoreSettingsFormEl.innerHTML = `
        <div class="app-store-settings-table">
            <div class="app-store-settings-head">
                <span>Title</span>
                <span>Description</span>
                <span>Link</span>
            </div>
            <div class="app-store-settings-body">${rows}</div>
        </div>
        <div class="app-store-settings-actions">
            <button type="submit">Save Settings</button>
        </div>
    `;
}

async function refreshAppStoreSettings(options = {}) {
    const force = Boolean(options.force);

    if (!force && collectionRefreshInFlight.settings) {
        return collectionRefreshInFlight.settings;
    }

    const request = api("/api/settings/app-store")
        .then((payload) => {
            renderAppStoreSettings(payload);
            collectionLoaded.settings = true;
            return payload;
        })
        .finally(() => {
            if (collectionRefreshInFlight.settings === request) {
                collectionRefreshInFlight.settings = null;
            }
        });

    collectionRefreshInFlight.settings = request;
    return request;
}

async function refreshApps(options = {}) {
    const force = Boolean(options.force);

    if (!force && collectionRefreshInFlight.apps) {
        return collectionRefreshInFlight.apps;
    }

    const requestSeq = ++appRefreshRequestSeq;
    const request = api("/api/apps")
        .then((payload) => {
            if (requestSeq >= appRefreshAppliedSeq) {
                appRefreshAppliedSeq = requestSeq;
                renderApps(payload.apps);
            }
            collectionLoaded.apps = true;
            return payload;
        })
        .finally(() => {
            if (collectionRefreshInFlight.apps === request) {
                collectionRefreshInFlight.apps = null;
            }
        });

    collectionRefreshInFlight.apps = request;
    return request;
}

async function refreshWebsites() {
    if (collectionRefreshInFlight.websites) {
        return collectionRefreshInFlight.websites;
    }

    collectionRefreshInFlight.websites = api("/api/websites")
        .then((websites) => {
            const websiteItems = Array.isArray(websites) ? websites : [];
            saveCachedWebsites(websiteItems);
            renderWebsiteList(websiteItems);
            collectionLoaded.websites = true;
            return websiteItems;
        })
        .finally(() => {
            collectionRefreshInFlight.websites = null;
        });

    return collectionRefreshInFlight.websites;
}

async function refreshDatabases() {
    if (collectionRefreshInFlight.databases) {
        return collectionRefreshInFlight.databases;
    }

    collectionRefreshInFlight.databases = api("/api/databases")
        .then((databases) => {
            const databaseItems = Array.isArray(databases) ? databases : [];
            databaseListEl.replaceChildren();

            if (databaseItems.length === 0) {
                databaseListEl.textContent = "No databases created yet.";
                collectionLoaded.databases = true;
                return databaseItems;
            }

            databaseItems.forEach((database) => databaseListEl.appendChild(databaseCard(database)));
            collectionLoaded.databases = true;
            return databaseItems;
        })
        .finally(() => {
            collectionRefreshInFlight.databases = null;
        });

    return collectionRefreshInFlight.databases;
}

async function handleWebsiteAction(action, domain) {
    if (action === "website-delete" && !window.confirm(`Delete website "${domain}"?`)) {
        return;
    }

    const route = action === "website-start"
        ? "/api/website/start"
        : action === "website-stop"
            ? "/api/website/stop"
            : "/api/website/delete";

    const data = await api(route, {
        method: "POST",
        body: JSON.stringify({ domain }),
    });

    showResult(data);
    await refreshAll();
}

async function handleAppStoreAction(action, id, version = "") {
    if (action === "install") {
        await startInstallJob(id, version);
        return;
    }

    appActionInFlight.add(id);
    updateAppCardPendingState(id, action, true);
    updatePersistentToast(`app-action-${id}`, "In Progress", formatAppActionLabel(action, id), "success");

    try {
        const data = await api("/api/apps/action", {
            method: "POST",
            body: JSON.stringify({ action, id, version }),
        });

        await new Promise(resolve => setTimeout(resolve, 300));
        await refreshApps();
        removePersistentToast(`app-action-${id}`);
        showResult(data.message || "Application updated.");
        await refreshStatusOnly();
    } catch (error) {
        removePersistentToast(`app-action-${id}`);
        showResult(`Error: ${error.message}`);
        if (latestAppsPayload.length > 0) {
            renderApps(latestAppsPayload);
        } else {
            await refreshApps().catch(() => { });
        }
    } finally {
        appActionInFlight.delete(id);
        updateAppCardPendingState(id, action, false);
    }
}

async function startInstallJob(id, version = "") {
    const job = await api("/api/apps/action", {
        method: "POST",
        body: JSON.stringify({ action: "install", id, version }),
    });

    const jobKey = id === "php" ? `${id}:${version}` : id;
    syncInstallJob(job, jobKey);
    renderApps(latestAppsPayload);
    pollInstallJob(id, version);
}

function formatAppActionLabel(action, id, version = "") {
    const appName = latestAppsPayload.find((app) => app.id === id)?.name || id.toUpperCase();
    if (action === "install") {
        return version ? `Installing ${appName} ${version}...` : `Installing ${appName}...`;
    }
    if (action === "start") {
        return `Starting ${appName}...`;
    }
    if (action === "uninstall") {
        return `Removing ${appName}...`;
    }
    return `Stopping ${appName}...`;
}

function syncInstallJob(job, customKey = "") {
    if (!job?.app_id) {
        return;
    }

    const key = customKey || getInstallJobKey(job.app_id, job.version || "");
    appInstallJobs.set(key, job);
}

function clearInstallJob(id, version = "") {
    const key = getInstallJobKey(id, version);
    appInstallJobs.delete(key);
    appInstallPollRequests.delete(key);
    const poller = appInstallPollers.get(key);
    if (poller) {
        clearTimeout(poller);
        appInstallPollers.delete(key);
    }
}

async function pollInstallJob(id, version = "") {
    const jobKey = getInstallJobKey(id, version);
    if (appInstallPollRequests.has(jobKey)) {
        return;
    }

    appInstallPollRequests.add(jobKey);

    const scheduleNext = (delay = 700) => {
        const timer = setTimeout(() => {
            pollInstallJob(id, version).catch(() => { });
        }, delay);
        appInstallPollers.set(jobKey, timer);
    };

    try {
        const job = await api(`/api/apps/progress?id=${encodeURIComponent(id)}${version ? `&version=${encodeURIComponent(version)}` : ""}`);
        syncInstallJob(job, jobKey);

        if (job.status === "running") {
            renderApps(latestAppsPayload);
            scheduleNext();
            return;
        }

        clearInstallJob(id, version);
        removePersistentToast(`app-install-${jobKey}`);

        if (job.status === "completed") {
            if (Array.isArray(job.apps) && job.apps.length > 0) {
                renderApps(job.apps);
            }
            await new Promise(resolve => setTimeout(resolve, 300));
            await refreshApps({ force: true });
            await refreshStatusOnly();
            return;
        }

        renderApps(latestAppsPayload);
        showResult(`Error: ${job.error || job.message || "Install failed."}`);
    } catch (error) {
        const message = String(error?.message || "").trim();
        if (/no install job found/i.test(message)) {
            clearInstallJob(id, version);
            removePersistentToast(`app-install-${jobKey}`);
            renderApps(latestAppsPayload);
            return;
        }

        updatePersistentToast(
            `app-install-${jobKey}`,
            "Connection interrupted",
            "Trying to reconnect to the running download...",
            "error",
        );
        renderApps(latestAppsPayload);
        scheduleNext(1500);
    } finally {
        appInstallPollRequests.delete(jobKey);
    }
}

function updateAppCardPendingState(id, action, pending) {
    const card = appStoreListEl.querySelector(`[data-app-id="${id}"]`);
    if (!card) {
        return;
    }

    card.classList.toggle("busy", pending);
    card.querySelectorAll("button[data-app-action]").forEach((button) => {
        button.disabled = pending;
        if (!button.dataset.originalLabel) {
            button.dataset.originalLabel = button.textContent;
        }

        if (!pending) {
            if (button.dataset.originalLabel) {
                button.textContent = button.dataset.originalLabel;
            }
            return;
        }

        if (button.dataset.appAction === action) {
            button.textContent = action === "install"
                ? "Installing..."
                : action === "start"
                    ? "Starting..."
                    : action === "uninstall"
                        ? "Removing..."
                        : "Stopping...";
        }
    });
}

function clearAppCardPendingState() {
    appActionInFlight.forEach((id) => updateAppCardPendingState(id, "", false));
    appActionInFlight.clear();
}

websiteFormEl.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!websitePHPVersionEl.value) {
        showResult("Error: Please install a PHP runtime in App Store first.");
        return;
    }

    websiteSubmitButtonEl.disabled = true;
    websiteSubmitButtonEl.textContent = "Saving...";

    const payload = {
        domain: websiteDomainInputEl.value,
        path: websitePathTouched ? websitePathInputEl.value : "",
        php_version: websitePHPVersionEl.value,
    };

    try {
        const data = await api("/api/website/create", {
            method: "POST",
            body: JSON.stringify(payload),
        });
        showResult(data);
        event.target.reset();
        websitePathTouched = false;
        closeWebsiteModal();
        await Promise.allSettled([
            refreshWebsites(),
            refreshStatusOnly(),
        ]);
    } catch (error) {
        showResult(`Error: ${error.message}`);
    } finally {
        websiteSubmitButtonEl.disabled = false;
        websiteSubmitButtonEl.textContent = "Confirm";
    }
});

document.getElementById("database-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload = {
        name: document.getElementById("db-name").value,
        type: document.getElementById("db-type").value,
    };

    try {
        const data = await api("/api/database/create", {
            method: "POST",
            body: JSON.stringify(payload),
        });
        showResult(data);
        event.target.reset();
        await refreshAll();
    } catch (error) {
        showResult(`Error: ${error.message}`);
    }
});

websiteListEl.addEventListener("click", async (event) => {
    const button = event.target.closest("button[data-action]");
    if (!button) {
        return;
    }

    try {
        await handleWebsiteAction(button.dataset.action, button.dataset.domain);
    } catch (error) {
        showResult(`Error: ${error.message}`);
    }
});

appStoreListEl.addEventListener("click", async (event) => {
    const versionToggle = event.target.closest("button[data-app-version-toggle]");
    if (versionToggle) {
        const row = versionToggle.closest(".app-table-row");
        const combo = versionToggle.closest("[data-app-install-combo]");
        const menu = combo?.querySelector("[data-app-version-menu]");
        const isHidden = menu?.hasAttribute("hidden");

        // Close all other menus first
        document.querySelectorAll("[data-app-version-menu]").forEach((item) => item.setAttribute("hidden", ""));
        document.querySelectorAll(".app-table-row.z-index-top").forEach((r) => r.classList.remove("z-index-top"));

        if (menu && isHidden) {
            menu.removeAttribute("hidden");
            row?.classList.add("z-index-top");
        }
        return;
    }

    const versionOption = event.target.closest("button[data-app-version-option]");
    if (versionOption) {
        const combo = versionOption.closest("[data-app-install-combo]");
        const mainButton = combo?.querySelector(".app-install-main");
        const version = versionOption.dataset.version || "";
        combo?.querySelectorAll("button[data-app-version-option]").forEach((item) => {
            item.classList.toggle("active", item === versionOption);
        });
        if (mainButton) {
            mainButton.dataset.appVersion = version;
            mainButton.textContent = "Install";
        }

        // Update the version label in the table row
        const row = versionOption.closest(".app-table-row");
        const versionLabel = row?.querySelector(".app-version-label");
        if (versionLabel) {
            versionLabel.textContent = `v${version}`;
        }

        combo?.querySelector("[data-app-version-menu]")?.setAttribute("hidden", "");
        row?.classList.remove("z-index-top");
        return;
    }

    const button = event.target.closest("button[data-app-action]");
    if (!button) {
        document.querySelectorAll("[data-app-version-menu]").forEach((item) => item.setAttribute("hidden", ""));
        return;
    }

    document.querySelectorAll("[data-app-version-menu]").forEach((item) => item.setAttribute("hidden", ""));

    const action = button.dataset.appAction;
    const appId = button.dataset.appId;

    if (action === "open-folder") {
        try {
            await api("/api/apps/open-folder", { method: "POST", body: JSON.stringify({ id: appId }) });
        } catch (error) {
            showResult(`Error: ${error.message}`);
        }
        return;
    }

    if (action === "setting") {
        await openAppSettingModal(appId, button.dataset.appVersion || "");
        return;
    }

    await handleAppStoreAction(action, appId, button.dataset.appVersion || "");
});

appStoreListEl.addEventListener("change", async (event) => {
    const toggle = event.target.closest("[data-app-dashboard-toggle]");
    if (!toggle) return;
    const appId = toggle.dataset.appDashboardToggle;
    const value = toggle.checked;
    try {
        await api("/api/apps/dashboard-toggle", {
            method: "POST",
            body: JSON.stringify({ id: appId, value }),
        });
        // Update local payload and refresh snapshot
        const updated = latestAppsPayload.map((app) => {
            if (appId.includes(":")) {
                const [baseId, version] = appId.split(":", 2);
                if (app.id === baseId) {
                    const versions = { ...(app.show_on_dashboard_versions || {}) };
                    versions[version] = value;
                    return { ...app, show_on_dashboard_versions: versions };
                }
            }
            return app.id === appId ? { ...app, show_on_dashboard: value } : app;
        });
        latestAppsPayload = updated;
        updateSoftwareSnapshot(updated);
    } catch (error) {
        toggle.checked = !value; // revert
        showResult(`Error: ${error.message}`);
    }
});

appStoreSettingsFormEl.addEventListener("submit", async (event) => {
    event.preventDefault();

    const submitButton = appStoreSettingsFormEl.querySelector("button[type='submit']");
    const downloads = {};
    const titles = {};
    const descriptions = {};

    appStoreSettingsFormEl.querySelectorAll("[data-app-store-download]").forEach((input) => {
        const appId = String(input.dataset.appId || "").trim();
        const version = String(input.dataset.appVersion || "").trim();
        const value = String(input.value || "").trim();
        if (!appId || !version) {
            return;
        }

        if (!downloads[appId]) {
            downloads[appId] = {};
        }
        downloads[appId][version] = value;
    });

    appStoreSettingsFormEl.querySelectorAll("[data-app-store-title]").forEach((input) => {
        const appId = String(input.dataset.appId || "").trim();
        const version = String(input.dataset.appVersion || "").trim();
        const value = String(input.value || "").trim();
        if (!appId || !version) {
            return;
        }
        if (!titles[appId]) {
            titles[appId] = {};
        }
        titles[appId][version] = value;
    });

    appStoreSettingsFormEl.querySelectorAll("[data-app-store-description]").forEach((input) => {
        const appId = String(input.dataset.appId || "").trim();
        const version = String(input.dataset.appVersion || "").trim();
        const value = String(input.value || "").trim();
        if (!appId || !version) {
            return;
        }
        if (!descriptions[appId]) {
            descriptions[appId] = {};
        }
        descriptions[appId][version] = value;
    });

    submitButton.disabled = true;
    submitButton.textContent = "Saving...";

    try {
        const payload = await api("/api/settings/app-store", {
            method: "POST",
            body: JSON.stringify({ downloads, titles, descriptions }),
        });
        renderAppStoreSettings(payload);
        await refreshApps({ force: true });
        showResult(payload.message || "App Store settings saved.");
    } catch (error) {
        showResult(`Error: ${error.message}`);
    } finally {
        const nextSubmitButton = appStoreSettingsFormEl.querySelector("button[type='submit']");
        if (nextSubmitButton) {
            nextSubmitButton.disabled = false;
            nextSubmitButton.textContent = "Save Settings";
        }
    }
});

async function openAppSettingModal(appId) {
    const modalEl = document.getElementById("app-setting-modal");
    const titleEl = document.getElementById("app-setting-modal-title");
    const contentEl = document.getElementById("app-setting-content");
    titleEl.textContent = "Loading...";
    contentEl.innerHTML = "";
    modalEl.hidden = false;
    document.body.classList.add("modal-open");
    try {
        const info = await api(`/api/apps/setting?id=${encodeURIComponent(appId)}`);
        titleEl.textContent = `${info.name} — Settings`;
        contentEl.innerHTML = `
            <div class="setting-row"><span class="setting-label">Version</span><code class="setting-value">${info.version || "—"}</code></div>
            <div class="setting-row"><span class="setting-label">Install Path</span><code class="setting-value">${info.install_path || "Not installed"}</code></div>
            <div class="setting-row"><span class="setting-label">Runtime Root</span><code class="setting-value">${info.runtime_root || "—"}</code></div>
            <div class="setting-row"><span class="setting-label">Port</span><code class="setting-value">${info.port || "—"}</code></div>
            ${info.config_file ? `<div class="setting-row"><span class="setting-label">Config file</span><code class="setting-value">${info.config_file}</code></div>` : ""}
        `;
    } catch (error) {
        contentEl.innerHTML = `<p style="color:#ef4444">Could not load settings: ${error.message}</p>`;
    }
}

function renderAppSettingContent(info) {
    const details = [
        `<div class="setting-row"><span class="setting-label">Version</span><code class="setting-value">${escapeHTML(info.version || "-")}</code></div>`,
        `<div class="setting-row"><span class="setting-label">Install Path</span><code class="setting-value">${escapeHTML(info.install_path || "Not installed")}</code></div>`,
        `<div class="setting-row"><span class="setting-label">Runtime Root</span><code class="setting-value">${escapeHTML(info.runtime_root || "-")}</code></div>`,
        `<div class="setting-row"><span class="setting-label">Port</span><code class="setting-value">${escapeHTML(info.port || "-")}</code></div>`,
    ];

    if (info.config_file) {
        details.push(`<div class="setting-row"><span class="setting-label">Config file</span><code class="setting-value">${escapeHTML(info.config_file)}</code></div>`);
    }

    const availableExtensions = Array.isArray(info.available_extensions) ? info.available_extensions : [];
    if (String(info.id) !== "php" || availableExtensions.length === 0) {
        return `<div class="app-setting-main">${details.join("")}</div>`;
    }

    const enabledExtensions = new Set((Array.isArray(info.enabled_extensions) ? info.enabled_extensions : []).map(String));
    const enabledCount = enabledExtensions.size;
    const extensionItems = availableExtensions.map((extension) => `
        <label class="php-extension-item">
            <input type="checkbox" data-php-extension-input value="${escapeHTML(extension)}" ${enabledExtensions.has(String(extension)) ? "checked" : ""}>
            <span>${escapeHTML(extension)}</span>
        </label>
    `).join("");

    return `
        <div class="app-setting-layout" data-app-setting-layout>
            <div class="app-setting-main">
                ${details.join("")}
                <button type="button" class="setting-link-card" data-open-php-extensions aria-expanded="false">
                    <span class="setting-link-copy">
                        <strong>PHP Extensions</strong>
                        <span>Manage modules for PHP ${escapeHTML(info.version || "")}</span>
                    </span>
                    <span class="setting-link-meta">
                        <span class="setting-chip">${enabledCount}/${availableExtensions.length} enabled</span>
                        <span class="setting-link-arrow">Open</span>
                    </span>
                </button>
            </div>
            <aside class="php-extension-panel" data-php-extension-panel hidden>
                <form class="app-setting-form" data-php-extension-form data-app-id="${escapeHTML(info.id)}" data-app-version="${escapeHTML(info.version || "")}">
                    <div class="setting-section-head">
                        <div>
                            <strong>PHP Extensions</strong>
                            <p>Select the modules enabled for PHP ${escapeHTML(info.version || "")}.</p>
                        </div>
                        <div class="php-extension-panel-actions">
                            <div class="setting-chip">${availableExtensions.length} items</div>
                            <button type="button" class="php-extension-panel-close" data-close-php-extensions aria-label="Close extensions">x</button>
                        </div>
                    </div>
                    <div class="php-extension-list">
                        ${extensionItems}
                    </div>
                    <div class="setting-actions-inline">
                        <button type="submit" class="app-setting-save">Save Extensions</button>
                    </div>
                </form>
            </aside>
        </div>
    `;
}

function setPHPExtensionPanelOpen(isOpen) {
    const modalEl = document.getElementById("app-setting-modal");
    const layoutEl = modalEl.querySelector("[data-app-setting-layout]");
    const panelEl = modalEl.querySelector("[data-php-extension-panel]");
    const triggerEl = modalEl.querySelector("[data-open-php-extensions]");
    if (!layoutEl || !panelEl || !triggerEl) {
        return;
    }

    panelEl.hidden = !isOpen;
    layoutEl.classList.toggle("panel-open", isOpen);
    modalEl.querySelector(".app-setting-modal-card")?.classList.toggle("has-side-panel", isOpen);
    triggerEl.setAttribute("aria-expanded", isOpen ? "true" : "false");
}

async function openAppSettingModal(appId, version = "") {
    const modalEl = document.getElementById("app-setting-modal");
    const titleEl = document.getElementById("app-setting-modal-title");
    const contentEl = document.getElementById("app-setting-content");
    titleEl.textContent = "Loading...";
    contentEl.innerHTML = "";
    modalEl.dataset.appId = appId;
    modalEl.dataset.appVersion = version;
    modalEl.hidden = false;
    document.body.classList.add("modal-open");
    try {
        const info = await api(`/api/apps/setting?id=${encodeURIComponent(appId)}${version ? `&version=${encodeURIComponent(version)}` : ""}`);
        titleEl.textContent = `${info.name} - Settings`;
        contentEl.innerHTML = renderAppSettingContent(info);
        setPHPExtensionPanelOpen(false);
    } catch (error) {
        contentEl.innerHTML = `<p style="color:#ef4444">Could not load settings: ${error.message}</p>`;
    }
}

document.getElementById("app-setting-content").addEventListener("click", (event) => {
    if (event.target.closest("[data-open-php-extensions]")) {
        setPHPExtensionPanelOpen(true);
        return;
    }

    if (event.target.closest("[data-close-php-extensions]")) {
        setPHPExtensionPanelOpen(false);
    }
});

document.getElementById("app-setting-content").addEventListener("submit", async (event) => {
    const form = event.target.closest("[data-php-extension-form]");
    if (!form) {
        return;
    }

    event.preventDefault();
    const submitButton = form.querySelector("button[type='submit']");
    const enabledExtensions = Array.from(form.querySelectorAll("[data-php-extension-input]:checked"))
        .map((input) => input.value)
        .filter(Boolean);

    submitButton.disabled = true;
    submitButton.textContent = "Saving...";

    try {
        const response = await api("/api/apps/setting", {
            method: "POST",
            body: JSON.stringify({
                id: form.dataset.appId || "php",
                version: form.dataset.appVersion || "",
                enabled_extensions: enabledExtensions,
            }),
        });
        showResult(response.message || "PHP extensions updated.");
        await refreshApps({ force: true });
        document.getElementById("app-setting-modal").hidden = true;
        document.body.classList.remove("modal-open");
        setPHPExtensionPanelOpen(false);
        submitButton.disabled = false;
        submitButton.textContent = "Save Extensions";
    } catch (error) {
        showResult(`Error: ${error.message}`);
        submitButton.disabled = false;
        submitButton.textContent = "Save Extensions";
    }
});

document.getElementById("app-setting-modal").addEventListener("click", (event) => {
    if (event.target.matches("[data-modal-close='app-setting']")) {
        document.getElementById("app-setting-modal").hidden = true;
        document.body.classList.remove("modal-open");
        setPHPExtensionPanelOpen(false);
    }
});

document.addEventListener("click", (event) => {
    if (event.target.closest("[data-app-install-combo]")) {
        return;
    }
    document.querySelectorAll("[data-app-version-menu]").forEach((item) => item.setAttribute("hidden", ""));
    document.querySelectorAll(".app-table-row.z-index-top").forEach((r) => r.classList.remove("z-index-top"));
});

async function refreshAll(options = {}) {
    const { force = false } = options;
    await refreshStatusOnly();

    await ensureViewData(activeView, { force });

    clearAppCardPendingState();
}

function viewPath(view) {
    const nextView = viewConfig[view] ? view : "overview";
    return `/${nextView}`;
}

function readViewFromLocation() {
    const path = window.location.pathname.replace(/\/+$/, "") || "/";
    const legacyHash = window.location.hash.replace(/^#/, "");

    if (path === "/") {
        return viewConfig[legacyHash] ? legacyHash : "overview";
    }

    const nextView = path.replace(/^\//, "");
    return viewConfig[nextView] ? nextView : "overview";
}

function setActiveView(view, options = {}) {
    const { updateHistory = true, replaceHistory = false, loadData = true } = options;
    const nextView = viewConfig[view] ? view : "overview";
    activeView = nextView;
    closeSidebar();

    document.querySelectorAll(".nav-item").forEach((link) => {
        link.classList.toggle("active", link.dataset.view === nextView);
    });

    document.querySelectorAll(".view-section").forEach((section) => {
        section.classList.toggle("active", section.dataset.view === nextView);
    });

    sectionTitleEl.textContent = viewConfig[nextView].title;
    sectionCopyEl.textContent = viewConfig[nextView].copy;

    if (updateHistory) {
        const nextURL = viewPath(nextView);
        if (window.location.pathname !== nextURL || window.location.hash) {
            const historyMethod = replaceHistory ? "replaceState" : "pushState";
            window.history[historyMethod]({ view: nextView }, "", nextURL);
        }
    }

    if (loadData) {
        ensureViewData(nextView).catch((error) => {
            showConnectionError(error);
        });
    }
}

function syncViewFromLocation(options = {}) {
    const nextView = readViewFromLocation();
    const shouldReplaceHistory = window.location.pathname === "/" || Boolean(window.location.hash);
    setActiveView(nextView, {
        updateHistory: true,
        replaceHistory: shouldReplaceHistory || options.replaceHistory === true,
        loadData: options.loadData !== false,
    });
}

// Initial data load
syncViewFromLocation({ replaceHistory: true, loadData: false });
renderCachedWebsites();
setSidebarOpen(false);

refreshAll().catch((error) => {
    setPanelOfflineState(error);
});

document.querySelectorAll(".nav-item").forEach((link) => {
    link.addEventListener("click", (event) => {
        event.preventDefault();
        setActiveView(link.dataset.view);
    });
});

sidebarToggleEl?.addEventListener("click", toggleSidebar);
sidebarCloseEl?.addEventListener("click", closeSidebar);
sidebarBackdropEl?.addEventListener("click", closeSidebar);

document.getElementById("website-open-modal").addEventListener("click", openWebsiteModal);
document.getElementById("website-refresh").addEventListener("click", () => {
    refreshWebsites().catch((error) => {
        showResult(`Error: ${error.message}`);
    });
});

websiteModalEl.addEventListener("click", (event) => {
    if (event.target.matches("[data-modal-close='website']")) {
        closeWebsiteModal();
    }
});

document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && document.body.classList.contains("sidebar-open")) {
        closeSidebar();
    }

    if (event.key === "Escape" && !websiteModalEl.hidden) {
        closeWebsiteModal();
    }
});

websiteDomainInputEl.addEventListener("input", () => {
    const normalized = websiteDomainInputEl.value.trim().toLowerCase().replace(/[^a-z0-9.-]/g, "-");
    websitePathInputEl.value = normalized ? `www/${normalized}` : "";
});

websitePathInputEl.addEventListener("input", () => {
    websitePathTouched = websitePathInputEl.value.trim() !== "";
});

window.addEventListener("popstate", () => {
    setActiveView(readViewFromLocation(), { updateHistory: false });
});

window.addEventListener("resize", () => {
    if (!isMobileSidebar()) {
        closeSidebar();
        syncSidebarAccessibility(false);
        return;
    }

    syncSidebarAccessibility(document.body.classList.contains("sidebar-open"));
});

window.addEventListener("online", () => {
    refreshAll({ force: true }).catch((error) => {
        setPanelOfflineState(error);
    });
});

function scheduleStatusRefresh(immediate = false) {
    if (statusRefreshTimer) {
        clearTimeout(statusRefreshTimer);
    }

    const delay = immediate
        ? 0
        : document.visibilityState === "visible"
            ? STATUS_REFRESH_VISIBLE_MS
            : STATUS_REFRESH_HIDDEN_MS;

    statusRefreshTimer = setTimeout(() => {
        refreshStatusOnly()
            .catch(() => { })
            .finally(() => {
                scheduleStatusRefresh();
            });
    }, delay);
}

document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") {
        scheduleStatusRefresh(true);
        return;
    }

    scheduleStatusRefresh();
});

scheduleStatusRefresh();

function openWebsiteModal() {
    websitePathTouched = false;
    websitePathInputEl.value = "";
    syncPHPVersionOptions();
    websiteModalEl.hidden = false;
    document.body.classList.add("modal-open");
    websiteDomainInputEl.focus();
}

function closeWebsiteModal() {
    websiteModalEl.hidden = true;
    document.body.classList.remove("modal-open");
}
