(() => {
  "use strict";

  const IMAGE_TYPES = [
    "Primary",
    "Backdrop",
    "Logo",
    "Thumb",
    "Banner",
    "Art",
    "Disc",
    "Box",
    "Screenshot",
  ];
  const IMAGE_TYPE_LABELS = {
    Primary: "主海报",
    Backdrop: "背景图",
    Logo: "Logo",
    Thumb: "缩略图",
    Banner: "横幅图",
    Art: "艺术图",
    Disc: "光盘图",
    Box: "盒装图",
    Screenshot: "截图",
  };
  const DEFAULT_IMAGE_TYPES = Object.freeze([...IMAGE_TYPES]);
  const MAX_LOG_LINES = 800;
  const THEME_STORAGE_KEY = "embyMigratorTheme";
  const CONNECTION_STORAGE_KEY = "embyMigrator.connection";
  const TASK_PREFS_STORAGE_KEY = "embyMigrator.taskPrefs";
  const TASK_PREFS_SCHEMA_VERSION = 2;
  const LEGACY_SERVER_URL_KEY = "embyMigrator.serverUrl";
  const DEFAULT_CONCURRENCY = 4;
  const REPORT_FAILURE_SAMPLE_LIMIT = 4;
  const JOB_KIND_LABELS = {
    export: "导出",
    import: "导入",
    "import-precheck": "导入预检",
    "media-db-apply": "媒体信息写库",
  };
  const JOB_STATUS_LABELS = {
    queued: "排队中",
    created: "已创建",
    running: "运行中",
    paused: "已暂停",
    done: "已完成",
    completed: "已完成",
    complete: "已完成",
    succeeded: "已完成",
    success: "已完成",
    failed: "失败",
    error: "失败",
    stopped: "已停止",
    cancelled: "已取消",
    canceled: "已取消",
  };

  const TERMINAL_STATES = new Set([
    "done",
    "completed",
    "complete",
    "succeeded",
    "success",
    "failed",
    "error",
    "stopped",
    "cancelled",
    "canceled",
  ]);

  const state = {
    authEnabled: true,
    authenticated: false,
    user: "",
    role: "",
    busy: new Set(),
    connected: false,
    hasTested: false,
    savedConnection: {
      baseUrl: "",
      hasApiKey: false,
      apiKeyMasked: "",
    },
    profiles: [],
    currentSource: "",
    currentTarget: "",
    discoveredDatabases: [],
    databaseDiscoveryRequest: 0,
    dockerAvailable: false,
    libraries: [],
    targetLibraries: [],
    libraryGroups: [],
    selectedLibraryIds: new Set(),
    selectedTargetLibraryIds: new Set(),
    exports: [],
    jobs: [],
    telegram: {
      hasBotToken: false,
      botTokenMasked: "",
      chatId: "",
      proxyUrl: "",
    },
    currentJobId: "",
    currentJobKind: "",
    currentJobStatus: "",
    eventSource: null,
    sseErrorReported: false,
    pollTimer: 0,
    seenLogLines: new Set(),
  };

  const els = {};

  document.addEventListener("DOMContentLoaded", init);

  function init() {
    cacheElements();
    retireLegacyUserManagementUI();
    initTheme();
    renderImageTypes("exportImageTypes", "export");
    renderImageTypes("importImageTypes", "import");
    restoreConnection();
    restoreTaskPreferences();
    bindEvents();
    renderJobList();
    updateControls();
    checkAuth();
    checkLatestVersion();
  }

  function retireLegacyUserManagementUI() {
    document.querySelector(".security-details")?.remove();
  }

  function cacheElements() {
    [
      "connectionForm",
      "appShell",
      "authGate",
      "authForm",
      "authUsername",
      "authPassword",
      "authLoginBtn",
      "authNotice",
      "authWarning",
      "appVersion",
      "versionUpdate",
      "currentUserBadge",
      "logoutBtn",
      "changePasswordToggleBtn",
      "showPasswordFormBtn",
      "themeToggleBtn",
      "changePasswordForm",
      "oldPassword",
      "newPassword",
      "changePasswordBtn",
      "cancelChangePasswordBtn",
      "changePasswordNotice",
      "serverUrl",
      "apiKey",
      "apiKeyHint",
      "embyDatabasePath",
      "embyDatabaseHint",
      "refreshEmbyDatabasesBtn",
      "embyContainerName",
      "autoManageContainer",
      "rememberConnection",
      "sourceProfileSelect",
      "targetProfileSelect",
      "saveAppSettingsBtn",
      "telegramForm",
      "telegramBotToken",
      "telegramTokenHint",
      "telegramChatId",
      "telegramProxyUrl",
      "saveTelegramBtn",
      "testTelegramBtn",
      "telegramNotice",
      "testConnectionBtn",
      "loadLibrariesBtn",
      "refreshLibrariesBtn",
      "connectionDot",
      "connectionText",
      "connectionNotice",
      "librarySummary",
      "selectAllLibraries",
      "selectedCount",
      "libraryList",
      "exportSkipImages",
      "exportIncremental",
      "exportOverwrite",
      "exportIncludePeopleImages",
      "exportIncludeMediaInfo",
      "exportConcurrency",
      "startExportBtn",
      "refreshJobsBtn",
      "refreshJobBtn",
      "pauseJobBtn",
      "resumeJobBtn",
      "stopJobBtn",
      "jobKind",
      "jobState",
      "jobId",
      "jobProgress",
      "jobList",
      "refreshExportsBtn",
      "exportsSelect",
      "importSkipImages",
      "importOverwrite",
      "importResume",
      "importIncludePeopleImages",
      "importMediaInfo",
      "importConcurrency",
      "startPrecheckBtn",
      "startImportBtn",
      "applyMediaDatabaseBtn",
      "importNotice",
      "importReport",
      "logSummary",
      "downloadLogsBtn",
      "copyLogsBtn",
      "clearLogsBtn",
      "logWindow",
    ].forEach((id) => {
      els[id] = document.getElementById(id);
    });
  }

  async function checkLatestVersion() {
    try {
      const data = await fetchJson("/api/version");
      if (!data.checked || !data.updateAvailable || !data.latestVersion) {
        return;
      }
      const latestVersion = String(data.latestVersion).replace(/^v/i, "");
      els.versionUpdate.textContent = `有更新 v${latestVersion}`;
      if (data.releaseUrl) {
        els.versionUpdate.href = String(data.releaseUrl);
      }
      els.versionUpdate.title = `发现新版本 v${latestVersion}`;
      els.versionUpdate.classList.remove("is-hidden");
    } catch {
      // Version checks are optional and must never block the application.
    }
  }

  function bindEvents() {
    els.authForm.addEventListener("submit", handleLogin);
    els.logoutBtn.addEventListener("click", handleLogout);
    els.changePasswordToggleBtn?.addEventListener("click", showChangePasswordForm);
    els.showPasswordFormBtn?.addEventListener("click", showChangePasswordForm);
    els.cancelChangePasswordBtn.addEventListener("click", hideChangePasswordForm);
    els.changePasswordForm.addEventListener("submit", handleChangePassword);
    els.themeToggleBtn.addEventListener("click", handleThemeToggle);
    els.connectionForm.addEventListener("submit", handleConnectionTest);
    els.saveAppSettingsBtn.addEventListener("click", handleSaveAppSettings);
    els.telegramForm.addEventListener("submit", handleSaveTelegramSettings);
    els.testTelegramBtn.addEventListener("click", handleTestTelegramSettings);
    els.loadLibrariesBtn.addEventListener("click", handleLoadLibraries);
    els.refreshLibrariesBtn.addEventListener("click", handleLoadLibraries);
    els.selectAllLibraries.addEventListener("change", handleSelectAllLibraries);
    els.startExportBtn.addEventListener("click", handleStartExport);
    els.refreshJobsBtn.addEventListener("click", () => loadJobs(true));
    els.refreshJobBtn.addEventListener("click", () => refreshCurrentJob(true));
    els.pauseJobBtn.addEventListener("click", handlePauseJob);
    els.resumeJobBtn.addEventListener("click", handleResumeJob);
    els.stopJobBtn.addEventListener("click", handleStopJob);
    els.refreshExportsBtn.addEventListener("click", handleRefreshExports);
    els.exportsSelect.addEventListener("change", handleExportSelection);
    els.startPrecheckBtn.addEventListener("click", handleStartPrecheck);
    els.startImportBtn.addEventListener("click", handleStartImport);
    els.applyMediaDatabaseBtn.addEventListener("click", handleApplyMediaDatabase);
    els.downloadLogsBtn.addEventListener("click", handleDownloadLogs);
    els.copyLogsBtn.addEventListener("click", handleCopyLogs);
    els.clearLogsBtn.addEventListener("click", () => {
      els.logWindow.textContent = "";
      state.seenLogLines.clear();
    });

    els.serverUrl.addEventListener("input", handleConnectionInputChanged);
    els.apiKey.addEventListener("input", handleConnectionInputChanged);
    els.rememberConnection.addEventListener("change", handleRememberConnectionChanged);
    els.sourceProfileSelect.addEventListener("change", () => handleProfileSelectionChanged("source"));
    els.targetProfileSelect.addEventListener("change", () => handleProfileSelectionChanged("target"));
    els.refreshEmbyDatabasesBtn.addEventListener("click", () => loadEmbyDatabases());
    els.embyDatabasePath.addEventListener("change", handleDatabaseSelectionChanged);

    [
      els.exportSkipImages,
      els.exportIncremental,
      els.exportOverwrite,
      els.importSkipImages,
      els.importOverwrite,
      els.importResume,
      els.exportIncludePeopleImages,
      els.importIncludePeopleImages,
      els.exportIncludeMediaInfo,
      els.importMediaInfo,
    ].filter(Boolean).forEach((input) => input.addEventListener("change", handleTaskPreferenceChanged));
    [els.exportConcurrency, els.importConcurrency].forEach((input) => {
      input.addEventListener("change", handleTaskPreferenceChanged);
      input.addEventListener("blur", handleTaskPreferenceChanged);
    });
  }

  function initTheme() {
    const saved = window.localStorage.getItem(THEME_STORAGE_KEY);
    const preferred =
      saved ||
      (window.matchMedia && window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light");
    applyTheme(preferred, false);
  }

  function handleThemeToggle() {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    applyTheme(next, true);
  }

  function applyTheme(theme, persist) {
    const normalized = theme === "dark" ? "dark" : "light";
    document.documentElement.dataset.theme = normalized;
    document.documentElement.style.colorScheme = normalized;
    els.themeToggleBtn.textContent = normalized === "dark" ? "浅色" : "暗黑";
    els.themeToggleBtn.setAttribute("aria-pressed", normalized === "dark" ? "true" : "false");
    if (persist) {
      window.localStorage.setItem(THEME_STORAGE_KEY, normalized);
    }
  }

  async function checkAuth() {
    try {
      const data = await fetchJson("/api/auth/status", { skipAuthRedirect: true });
      state.authEnabled = Boolean(data.enabled);
      state.authenticated = Boolean(data.authenticated);
      state.user = String(readFirst(data, ["user", "username", "User", "Username"]) || "");
      state.role = normalizeRole(readFirst(data, ["role", "Role"]));
      setAppVersion(readFirst(data, ["toolVersion", "ToolVersion", "version", "Version"]));
      applyAuthState(data.warning || "");
      if (state.authenticated) {
        loadAppSettings();
        loadTelegramSettings();
        loadJobs(false);
      }
    } catch (error) {
      state.authEnabled = true;
      state.authenticated = false;
      state.user = "";
      state.role = "";
      applyAuthState(error.message || "无法读取登录状态。");
    } finally {
      updateControls();
    }
  }

  async function handleLogin(event) {
    event.preventDefault();
    const username = els.authUsername ? els.authUsername.value.trim() : "";
    const password = els.authPassword.value;
    if (!password) {
      setNotice(els.authNotice, "请输入访问密码。", "error");
      return;
    }
    setButtonBusy("authLogin", els.authLoginBtn, true, "登录中");
    try {
      const data = await postJson("/api/auth/login", { username, password });
      state.user = String(readFirst(data, ["user", "username", "User", "Username"]) || username || "admin");
      state.role = normalizeRole(readFirst(data, ["role", "Role"]) || "admin");
      if (els.authUsername) {
        els.authUsername.value = "";
      }
      els.authPassword.value = "";
      state.authenticated = true;
      applyAuthState("");
      loadAppSettings();
      loadTelegramSettings();
      loadJobs(false);
      appendSystemLog("登录成功。");
    } catch (error) {
      setNotice(els.authNotice, error.message || "登录失败。", "error");
    } finally {
      setButtonBusy("authLogin", els.authLoginBtn, false);
      updateControls();
    }
  }

  async function handleLogout() {
    try {
      await postJson("/api/auth/logout", {});
    } catch {
      // Ignore logout transport errors and lock the page locally.
    }
    state.authenticated = !state.authEnabled;
    state.user = "";
    state.role = "";
    state.connected = false;
    state.jobs = [];
    renderAppSettings({});
    renderTelegramSettings({});
    hideChangePasswordForm();
    renderJobList();
    closeLogStream();
    stopPolling();
    applyAuthState("");
    updateControls();
  }

  function showChangePasswordForm() {
    els.changePasswordForm.classList.remove("is-hidden");
    setNotice(els.changePasswordNotice, "");
    els.oldPassword.focus();
  }

  function hideChangePasswordForm() {
    if (!els.changePasswordForm) {
      return;
    }
    els.changePasswordForm.classList.add("is-hidden");
    if (els.oldPassword) {
      els.oldPassword.value = "";
    }
    if (els.newPassword) {
      els.newPassword.value = "";
    }
    setNotice(els.changePasswordNotice, "");
  }

  async function handleChangePassword(event) {
    event.preventDefault();
    const oldPassword = els.oldPassword.value;
    const newPassword = els.newPassword.value;
    if (!oldPassword || !newPassword) {
      setNotice(els.changePasswordNotice, "请输入当前密码和新密码。", "error");
      return;
    }

    setButtonBusy("changePassword", els.changePasswordBtn, true, "保存中");
    try {
      await postJson("/api/auth/password", { oldPassword, newPassword });
      setNotice(els.changePasswordNotice, "密码已修改。", "ok");
      els.oldPassword.value = "";
      els.newPassword.value = "";
    } catch (error) {
      setNotice(els.changePasswordNotice, `修改密码失败：${error.message}`, "error");
    } finally {
      setButtonBusy("changePassword", els.changePasswordBtn, false);
      updateControls();
    }
  }

  function applyAuthState(warning) {
    const locked = state.authEnabled && !state.authenticated;
    document.body.classList.toggle("auth-page-active", locked);
    els.authGate.classList.toggle("is-hidden", !locked);
    els.appShell.classList.toggle("is-locked", locked);
    els.logoutBtn.classList.toggle("is-hidden", !state.authEnabled || locked);
    els.changePasswordToggleBtn?.classList.toggle("is-hidden", true);
    if (!state.authEnabled || locked) {
      hideChangePasswordForm();
    }
    renderCurrentUser();
    els.authWarning.classList.toggle("is-hidden", !warning || locked);
    els.authWarning.textContent = warning || "";
    if (locked) {
      setNotice(els.authNotice, "请输入访问密码。");
      window.requestAnimationFrame(() => els.authPassword?.focus());
    }
  }

  function renderCurrentUser() {
    if (!els.currentUserBadge) {
      return;
    }
    els.currentUserBadge.classList.add("is-hidden");
    els.currentUserBadge.textContent = "";
  }

  function restoreConnection() {
    els.rememberConnection.checked = true;
    const stored = readStoredConnection();
    if (stored.serverUrl) {
      els.serverUrl.value = stored.serverUrl;
    }
  }

  function readStoredConnection() {
    try {
      const raw = window.localStorage.getItem(CONNECTION_STORAGE_KEY);
      if (raw) {
        const parsed = JSON.parse(raw);
        return {
          serverUrl: String(parsed.serverUrl || ""),
          apiKey: "",
        };
      }
    } catch {
      window.localStorage.removeItem(CONNECTION_STORAGE_KEY);
    }
    return {
      serverUrl: window.localStorage.getItem(LEGACY_SERVER_URL_KEY) || "",
      apiKey: "",
    };
  }

  function persistConnection(connection) {
    if (!els.rememberConnection.checked) {
      clearStoredConnection();
      return;
    }
    const value = {
      serverUrl: connection.serverUrl,
    };
    window.localStorage.setItem(CONNECTION_STORAGE_KEY, JSON.stringify(value));
    window.localStorage.setItem(LEGACY_SERVER_URL_KEY, connection.serverUrl);
  }

  function clearStoredConnection() {
    window.localStorage.removeItem(CONNECTION_STORAGE_KEY);
    window.localStorage.removeItem(LEGACY_SERVER_URL_KEY);
  }

  function handleRememberConnectionChanged() {
    if (els.rememberConnection.checked) {
      const connection = getConnection();
      if (connection.serverUrl) {
        persistConnection(connection);
      }
      return;
    }
    clearStoredConnection();
  }

  async function loadAppSettings() {
    const locked = state.authEnabled && !state.authenticated;
    if (locked) {
      return;
    }
    try {
      const data = await fetchJson("/api/settings/app");
      renderAppSettings(data);
      await loadEmbyDatabases({ quiet: true });
    } catch (error) {
      setNotice(els.connectionNotice, `读取保存配置失败：${error.message}`, "error");
    } finally {
      updateControls();
    }
  }

  function renderAppSettings(data) {
    const settings = safeObject(data);
    const connection = safeObject(readFirst(settings, ["connection", "Connection"]));
    const baseUrl = String(readFirst(connection, ["baseUrl", "BaseURL"]) || "");
    state.savedConnection = {
      baseUrl,
      hasApiKey: Boolean(readFirst(connection, ["hasApiKey", "HasAPIKey"])),
      apiKeyMasked: String(readFirst(connection, ["apiKeyMasked", "APIKeyMasked", "maskedKey"]) || ""),
    };
    state.profiles = Array.isArray(settings.profiles)
      ? settings.profiles
      : Array.isArray(settings.Profiles)
        ? settings.Profiles
        : [];
    state.currentSource = String(readFirst(settings, ["currentSource", "CurrentSource"]) || "");
    state.currentTarget = String(readFirst(settings, ["currentTarget", "CurrentTarget"]) || "");
    if (baseUrl) {
      els.serverUrl.value = baseUrl;
    }
    if (els.apiKey) {
      els.apiKey.value = "";
    }
    renderProfileSelects();
    const activeProfile = findProfileByBaseUrl(baseUrl);
    const targetProfile = findProfileById(state.currentTarget) || activeProfile;
    applyDatabaseProfileSettings(targetProfile);
    refreshApiKeyHint();

    if (Boolean(readFirst(settings, ["configured", "Configured"]))) {
      const defaults = safeObject(readFirst(settings, ["defaults", "Defaults"]));
      applyGroupPreferences("export", safeObject(readFirst(defaults, ["export", "Export"])), {
        restoreImageTypes: false,
      });
      applyGroupPreferences("import", safeObject(readFirst(defaults, ["import", "Import"])), {
        restoreImageTypes: false,
      });
    }
  }

  function renderProfileSelects() {
    renderProfileSelect(els.sourceProfileSelect, "source");
    renderProfileSelect(els.targetProfileSelect, "target");
  }

  function renderProfileSelect(select, role) {
    if (!select) {
      return;
    }
    const current = role === "source" ? state.currentSource : state.currentTarget;
    select.textContent = "";
    const empty = document.createElement("option");
    empty.value = "";
    empty.textContent = role === "source" ? "选择已保存的导出服务器" : "选择已保存的导入服务器";
    select.appendChild(empty);
    state.profiles.forEach((profile) => {
      const option = document.createElement("option");
      option.value = String(readFirst(profile, ["id", "ID"]) || "");
      option.textContent = formatServerProfileLabel(profile);
      select.appendChild(option);
    });
    select.value = current;
  }

  function refreshApiKeyHint() {
    if (!els.apiKeyHint || !els.apiKey) {
      return;
    }
    const profile = selectedProfileForCurrentAddress();
    const profileLabel = formatServerProfileLabel(profile);
    const canReuseSavedKey = hasReusableApiKey();
    els.apiKey.placeholder = canReuseSavedKey ? "不填则继续用已保存的 Key" : "输入 Emby API Key";
    els.apiKeyHint.textContent = canReuseSavedKey
      ? `已保存 Key（${savedKeyMaskForCurrentAddress() || "****"}），不改就留空。`
      : profileLabel
        ? `${profileLabel} 还没有保存 Key，请填一次。`
        : "还没有保存 API Key，请填一次。";
  }

  function selectedProfileForCurrentAddress() {
    const currentBaseUrl = String(els.serverUrl?.value || "").trim().replace(/\/+$/, "");
    return findProfileByBaseUrl(currentBaseUrl) || {};
  }

  function hasReusableApiKey() {
    const currentBaseUrl = String(els.serverUrl?.value || "").trim().replace(/\/+$/, "");
    const profile = findProfileByBaseUrl(currentBaseUrl);
    if (profile) {
      return Boolean(readFirst(profile, ["hasApiKey", "HasAPIKey"]));
    }
    return currentBaseUrl === state.savedConnection.baseUrl && Boolean(state.savedConnection.hasApiKey);
  }

  function savedKeyMaskForCurrentAddress() {
    const currentBaseUrl = String(els.serverUrl?.value || "").trim().replace(/\/+$/, "");
    const profile = findProfileByBaseUrl(currentBaseUrl);
    if (profile) {
      return String(readFirst(profile, ["apiKeyMasked", "APIKeyMasked"]) || "").trim();
    }
    return currentBaseUrl === state.savedConnection.baseUrl ? state.savedConnection.apiKeyMasked : "";
  }

  function profileIdForCurrentConnection() {
    const connection = getConnection();
    if (!connection.baseUrl) {
      return "";
    }
    const profile = findProfileByBaseUrl(connection.baseUrl);
    return String(readFirst(profile, ["id", "ID"]) || "").trim();
  }

  function selectedProfiles() {
    return [findProfileById(state.currentSource), findProfileById(state.currentTarget)].filter(Boolean);
  }

  function findProfileById(id) {
    const normalized = String(id || "").trim();
    if (!normalized) {
      return null;
    }
    return (
      state.profiles.find((profile) => String(readFirst(profile, ["id", "ID"]) || "").trim() === normalized) ||
      null
    );
  }

  function findProfileByBaseUrl(baseUrl) {
    const normalized = String(baseUrl || "").trim();
    if (!normalized) {
      return null;
    }
    return (
      state.profiles.find((profile) => {
        const profileBaseUrl = String(readFirst(profile, ["baseUrl", "BaseURL"]) || "").trim();
        return profileBaseUrl === normalized;
      }) || null
    );
  }

  function formatServerProfileLabel(profile) {
    if (!profile) {
      return "";
    }
    const baseUrl = String(readFirst(profile, ["baseUrl", "BaseURL"]) || "").trim();
    const name = String(readFirst(profile, ["name", "Name"]) || "").trim();
    if (!baseUrl) {
      return name;
    }
    if (!name || name === baseUrl || name === profileNameFromBaseUrl(baseUrl)) {
      return baseUrl;
    }
    return `${name} · ${baseUrl}`;
  }

  function targetProfileId() {
    return String(els.targetProfileSelect?.value || state.currentTarget || "").trim();
  }

  function applyDatabaseProfileSettings(profile) {
    const databasePath = String(readFirst(profile, ["databasePath", "DatabasePath"]) || "").trim();
    state.discoveredDatabases = [];
    state.dockerAvailable = false;
    renderDatabaseOptions([], databasePath, "请点击重新检测数据库。");
    els.embyContainerName.value = String(
      readFirst(profile, ["containerName", "ContainerName"]) || "",
    ).trim();
    els.autoManageContainer.checked = Boolean(
      readFirst(profile, ["autoManageContainer", "AutoManageContainer"]),
    );
  }

  async function loadEmbyDatabases({ quiet = false } = {}) {
    const profileId = targetProfileId();
    const requestId = ++state.databaseDiscoveryRequest;
    if (!profileId) {
      state.discoveredDatabases = [];
      state.dockerAvailable = false;
      renderDatabaseOptions([], els.embyDatabasePath.value, "请先选择并保存目标服务器。");
      updateControls();
      return;
    }

    setButtonBusy("refreshEmbyDatabases", els.refreshEmbyDatabasesBtn, true, "检测中");
    if (!quiet) {
      els.embyDatabaseHint.textContent = "正在检测目标服务器的数据库和容器...";
    }
    try {
      const data = safeObject(
        await fetchJson(`/api/emby-databases?profileId=${encodeURIComponent(profileId)}`),
      );
      if (requestId !== state.databaseDiscoveryRequest || profileId !== targetProfileId()) {
        return;
      }
      const databases = Array.isArray(data.databases)
        ? data.databases
        : Array.isArray(data.Databases)
          ? data.Databases
          : [];
      state.discoveredDatabases = databases
        .map((database) => ({
          path: String(readFirst(database, ["path", "Path"]) || "").trim(),
          containerName: String(
            readFirst(database, ["containerName", "ContainerName"]) || "",
          ).trim(),
          matched: Boolean(readFirst(database, ["matched", "Matched"])),
        }))
        .filter((database) => database.path);
      state.dockerAvailable = Boolean(
        readFirst(data, ["dockerAvailable", "DockerAvailable"]),
      );
      const selectedPath = String(
        readFirst(data, ["selectedPath", "SelectedPath"]) || els.embyDatabasePath.value || "",
      ).trim();
      const count = state.discoveredDatabases.length;
      const hint = state.dockerAvailable
        ? count > 0
          ? `已发现 ${count} 个数据库；选择后会同步容器名。`
          : "未发现数据库。远程 Emby 需在其主机部署本项目，或把 config 挂载到 /emby-dbs。"
        : count > 0
          ? `已发现 ${count} 个数据库；Docker 不可用，无法自动停启容器。`
          : "未发现数据库。当前只能扫描本机已挂载的 /emby-dbs，远程 Emby API 不包含数据库文件。";
      renderDatabaseOptions(state.discoveredDatabases, selectedPath, hint);
      syncContainerNameForSelectedDatabase();
    } catch (error) {
      if (requestId === state.databaseDiscoveryRequest) {
        state.discoveredDatabases = [];
        state.dockerAvailable = false;
        renderDatabaseOptions(
          [],
          els.embyDatabasePath.value,
          `数据库检测失败：${error.message}`,
        );
      }
    } finally {
      if (requestId === state.databaseDiscoveryRequest) {
        setButtonBusy("refreshEmbyDatabases", els.refreshEmbyDatabasesBtn, false);
        updateControls();
      }
    }
  }

  function renderDatabaseOptions(databases, selectedPath, hint) {
    const select = els.embyDatabasePath;
    const normalizedSelectedPath = String(selectedPath || "").trim();
    select.textContent = "";

    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = databases.length > 0 ? "选择目标数据库" : "未发现可用数据库";
    select.appendChild(placeholder);

    const seen = new Set();
    databases.forEach((database) => {
      if (seen.has(database.path)) {
        return;
      }
      seen.add(database.path);
      const option = document.createElement("option");
      option.value = database.path;
      const container = database.containerName ? ` · ${database.containerName}` : "";
      const matched = database.matched ? "（匹配目标服务器）" : "";
      option.textContent = `${database.path}${container}${matched}`;
      select.appendChild(option);
    });

    if (normalizedSelectedPath && !seen.has(normalizedSelectedPath)) {
      const saved = document.createElement("option");
      saved.value = normalizedSelectedPath;
      saved.textContent = `${normalizedSelectedPath}（档案已保存）`;
      select.appendChild(saved);
    }
    const matchedPath = databases.find((database) => database.matched)?.path || "";
    const fallback = matchedPath || (databases.length === 1 ? databases[0].path : "");
    select.value = normalizedSelectedPath || fallback;
    els.embyDatabaseHint.textContent = hint;
  }

  function handleDatabaseSelectionChanged() {
    syncContainerNameForSelectedDatabase();
    updateControls();
  }

  function syncContainerNameForSelectedDatabase() {
    const selected = state.discoveredDatabases.find(
      (database) => database.path === els.embyDatabasePath.value,
    );
    if (selected?.containerName) {
      els.embyContainerName.value = selected.containerName;
    }
  }

  function collectAppSettings() {
    const connection = getConnection();
    const importDefaults = collectImportOptions({ dryRun: false });
    delete importDefaults.dryRun;
    return {
      connection: {
        baseUrl: connection.baseUrl,
        apiKey: connection.apiKey || undefined,
      },
      currentSource: els.sourceProfileSelect.value || state.currentSource,
      currentTarget: els.targetProfileSelect.value || state.currentTarget,
      profile: {
        id: profileIdFromBaseUrl(connection.baseUrl),
        name: profileNameFromBaseUrl(connection.baseUrl),
        baseUrl: connection.baseUrl,
        apiKey: connection.apiKey || undefined,
        databasePath: String(els.embyDatabasePath?.value || "").trim(),
        containerName: String(els.embyContainerName?.value || "").trim(),
        autoManageContainer: Boolean(els.autoManageContainer?.checked),
        role: "source-target",
      },
      defaults: {
        export: collectExportOptions(),
        import: importDefaults,
      },
    };
  }

  function profileIdFromBaseUrl(baseUrl) {
    return String(baseUrl || "default")
      .toLowerCase()
      .replace(/^https?:\/\//, "")
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 48) || "default";
  }

  function profileNameFromBaseUrl(baseUrl) {
    const value = String(baseUrl || "").trim();
    if (!value) {
      return "Emby Server";
    }
    try {
      const parsed = new URL(value.includes("://") ? value : `http://${value}`);
      return parsed.host || value;
    } catch {
      return value;
    }
  }

  function collectProfileRequest() {
    const connection = getConnection();
    return {
      id: profileIdFromBaseUrl(connection.baseUrl),
      name: profileNameFromBaseUrl(connection.baseUrl),
      baseUrl: connection.baseUrl,
      apiKey: connection.apiKey || undefined,
      databasePath: String(els.embyDatabasePath?.value || "").trim(),
      containerName: String(els.embyContainerName?.value || "").trim(),
      autoManageContainer: Boolean(els.autoManageContainer?.checked),
      role: "source-target",
    };
  }

  async function handleProfileSelectionChanged(changedRole = "") {
    const source = els.sourceProfileSelect.value;
    const target = els.targetProfileSelect.value;
    const sourceProfile = findProfileById(source);
    const targetProfile = findProfileById(target);
    try {
      const data = await postJson("/api/settings/profiles/select", {
        currentSource: source,
        currentTarget: target,
      });
      renderAppSettings(data);
      clearLibraryState();
      applyProfileToConnection(changedRole === "target" ? targetProfile : sourceProfile);
      await loadEmbyDatabases();
      appendSystemLog("已切换要使用的服务器。");
    } catch (error) {
      setNotice(els.connectionNotice, `切换服务器失败：${error.message}`, "error");
    }
  }

  function applyProfileToConnection(profile) {
    const baseUrl = String(readFirst(profile, ["baseUrl", "BaseURL"]) || "").trim();
    if (!baseUrl) {
      return;
    }
    els.serverUrl.value = baseUrl;
    if (els.apiKey) {
      els.apiKey.value = "";
    }
    state.connected = false;
    setConnectionState("pending", "需重新测试");
    refreshApiKeyHint();
    updateControls();
  }

  function clearLibraryState() {
    state.libraries = [];
    state.targetLibraries = [];
    state.libraryGroups = [];
    state.selectedLibraryIds.clear();
    state.selectedTargetLibraryIds.clear();
    if (els.libraryList) {
      renderLibraries();
    }
  }

  function selectSavedProfile(baseUrl) {
    const profile = findProfileByBaseUrl(baseUrl);
    const profileId = String(readFirst(profile, ["id", "ID"]) || "").trim();
    if (!profileId) {
      return;
    }
    if (els.sourceProfileSelect && !els.sourceProfileSelect.value) {
      els.sourceProfileSelect.value = profileId;
    }
    if (els.targetProfileSelect && !els.targetProfileSelect.value) {
      els.targetProfileSelect.value = profileId;
    }
  }

  async function handleSaveAppSettings(event) {
    if (event) {
      event.preventDefault();
    }
    const connection = getConnection();
    if (!connection.serverUrl) {
      setNotice(els.connectionNotice, "请填写 Emby 地址。", "error");
      return;
    }
    const payload = collectAppSettings();
    setButtonBusy("saveAppSettings", els.saveAppSettingsBtn, true, "保存中");
    try {
      const data = await postJson("/api/settings/app", payload);
      renderAppSettings(data);
      selectSavedProfile(connection.baseUrl);
      await loadEmbyDatabases({ quiet: true });
      persistConnection(connection);
      setNotice(els.connectionNotice, "服务器地址和任务选项已保存。后续同一地址会自动使用已保存 Key。", "ok");
      appendSystemLog("服务器地址和任务选项已保存。");
    } catch (error) {
      setNotice(els.connectionNotice, `保存配置失败：${error.message}`, "error");
    } finally {
      setButtonBusy("saveAppSettings", els.saveAppSettingsBtn, false);
      updateControls();
    }
  }

  async function loadTelegramSettings() {
    const locked = state.authEnabled && !state.authenticated;
    if (locked) {
      return;
    }
    try {
      const data = await fetchJson("/api/settings/telegram");
      renderTelegramSettings(data);
    } catch (error) {
      setNotice(els.telegramNotice, `读取 Telegram 配置失败：${error.message}`, "error");
    } finally {
      updateControls();
    }
  }

  function renderTelegramSettings(data) {
    const settings = safeObject(data);
    state.telegram = {
      hasBotToken: Boolean(readFirst(settings, ["hasBotToken", "HasBotToken"])),
      botTokenMasked: String(readFirst(settings, ["botTokenMasked", "BotTokenMasked"]) || ""),
      chatId: String(readFirst(settings, ["chatId", "ChatID"]) || ""),
      proxyUrl: String(readFirst(settings, ["proxyUrl", "ProxyURL"]) || ""),
    };
    if (els.telegramBotToken) {
      els.telegramBotToken.value = "";
      els.telegramBotToken.placeholder = state.telegram.hasBotToken
        ? "已保存 Token，留空表示不修改"
        : "在 @BotFather 创建机器人后粘贴 Token";
    }
    if (els.telegramTokenHint) {
      els.telegramTokenHint.textContent = state.telegram.hasBotToken
        ? `已保存：${state.telegram.botTokenMasked || "****"}，留空保存会继续保留。`
        : "未保存 Token。";
    }
    if (els.telegramChatId) {
      els.telegramChatId.value = state.telegram.chatId;
    }
    if (els.telegramProxyUrl) {
      els.telegramProxyUrl.value = state.telegram.proxyUrl;
    }
  }

  function collectTelegramSettings() {
    return {
      botToken: els.telegramBotToken.value.trim(),
      chatId: els.telegramChatId.value.trim(),
      proxyUrl: els.telegramProxyUrl.value.trim(),
    };
  }

  async function handleSaveTelegramSettings(event) {
    event.preventDefault();
    const settings = collectTelegramSettings();
    setButtonBusy("saveTelegram", els.saveTelegramBtn, true, "保存中");
    try {
      const data = await postJson("/api/settings/telegram", settings);
      renderTelegramSettings(data);
      setNotice(els.telegramNotice, "Telegram 配置已保存。", "ok");
      appendSystemLog("Telegram 配置已保存。");
    } catch (error) {
      setNotice(els.telegramNotice, `保存 Telegram 配置失败：${error.message}`, "error");
    } finally {
      setButtonBusy("saveTelegram", els.saveTelegramBtn, false);
      updateControls();
    }
  }

  async function handleTestTelegramSettings() {
    const settings = collectTelegramSettings();
    setButtonBusy("testTelegram", els.testTelegramBtn, true, "发送中");
    try {
      await postJson("/api/settings/telegram/test", settings);
      setNotice(els.telegramNotice, "测试消息已发送。", "ok");
      appendSystemLog("Telegram 测试消息已发送。");
    } catch (error) {
      setNotice(els.telegramNotice, `Telegram 测试失败：${error.message}`, "error");
      appendSystemLog(`Telegram 测试失败：${error.message}`);
    } finally {
      setButtonBusy("testTelegram", els.testTelegramBtn, false);
      updateControls();
    }
  }

  function restoreTaskPreferences() {
    const prefs = readTaskPreferences();
    const restoreImageTypes = prefs.schemaVersion === TASK_PREFS_SCHEMA_VERSION;
    applyGroupPreferences("export", prefs.export || {}, { restoreImageTypes });
    applyGroupPreferences("import", prefs.import || {}, { restoreImageTypes });
  }

  function readTaskPreferences() {
    try {
      const raw = window.localStorage.getItem(TASK_PREFS_STORAGE_KEY);
      if (!raw) {
        return {};
      }
      const parsed = JSON.parse(raw);
      return parsed && typeof parsed === "object" ? parsed : {};
    } catch {
      window.localStorage.removeItem(TASK_PREFS_STORAGE_KEY);
      return {};
    }
  }

  function applyGroupPreferences(group, prefs, { restoreImageTypes = true } = {}) {
    if (!prefs || typeof prefs !== "object") {
      return;
    }
    const prefix = group === "export" ? "export" : "import";
    setChecked(`${prefix}SkipImages`, prefs.skipImages);
    if (group === "export") {
      setChecked("exportIncremental", prefs.incremental);
    } else {
      setChecked("importResume", prefs.resume);
    }
    setChecked(`${prefix}Overwrite`, prefs.overwrite);
    setChecked(`${prefix}IncludePeopleImages`, prefs.includePeopleImages);
    if (group === "export") {
      setChecked("exportIncludeMediaInfo", prefs.includeMediaInfo);
    } else {
      setChecked("importMediaInfo", prefs.importMediaInfo);
    }
    if (Number.isFinite(Number(prefs.concurrency))) {
      els[`${prefix}Concurrency`].value = String(Math.max(1, Number.parseInt(prefs.concurrency, 10)));
    }
    if (restoreImageTypes && Array.isArray(prefs.imageTypes)) {
      setCheckedImageTypes(group, prefs.imageTypes);
    }
  }

  function setChecked(id, value) {
    if (value === undefined || value === null || !els[id]) {
      return;
    }
    els[id].checked = Boolean(value);
  }

  function persistTaskPreferences() {
    const prefs = {
      schemaVersion: TASK_PREFS_SCHEMA_VERSION,
      export: collectExportOptions(),
      import: collectImportOptions({ dryRun: false }),
    };
    prefs.export.imageTypes = getCheckedImageTypes("export");
    prefs.import.imageTypes = getCheckedImageTypes("import");
    delete prefs.import.dryRun;
    try {
      window.localStorage.setItem(TASK_PREFS_STORAGE_KEY, JSON.stringify(prefs));
    } catch {
      // Preference persistence is a convenience; task execution must keep working.
    }
  }

  function handleConnectionInputChanged() {
    clearLibraryState();
    if (!state.connected && !state.hasTested) {
      refreshApiKeyHint();
      return;
    }

    state.connected = false;
    setConnectionState("pending", "需重新测试");
    setNotice(els.connectionNotice, "连接信息已变更，请重新测试。");
    refreshApiKeyHint();
    updateControls();
  }

  function handleTaskPreferenceChanged() {
    updateControls();
    persistTaskPreferences();
  }

  async function handleConnectionTest(event) {
    event.preventDefault();

    const connection = getConnection();
    if (!connection.serverUrl) {
      setNotice(els.connectionNotice, "请填写 Emby 地址。", "error");
      return;
    }

    setButtonBusy("testConnection", els.testConnectionBtn, true, "测试中");
    setConnectionState("pending", "测试中");
    state.hasTested = true;

    try {
      const data = await postJson("/api/connection/test", {
        ...connection,
        profileId: profileIdForCurrentConnection(),
      });
      const server = safeObject(data.server || data.Server || data.systemInfo);
      const version =
        readFirst(data, ["version", "serverVersion", "ServerVersion"]) ||
        readFirst(server, ["version", "serverVersion", "Version", "ServerVersion"]);
      const name =
        readFirst(data, ["serverName", "name", "Name"]) ||
        readFirst(server, ["serverName", "ServerName", "name", "Name"]);
      const label = [name, version ? `v${version}` : ""].filter(Boolean).join(" ");
      setAppVersion(readFirst(data, ["toolVersion", "ToolVersion"]));

      state.connected = true;
      persistConnection(connection);
      if (connection.apiKey) {
        state.savedConnection.apiKeyMasked = maskKey(connection.apiKey);
      }
      setConnectionState("ok", label || "连接成功");
      setNotice(
        els.connectionNotice,
        label ? `连接成功：${label}` : "连接成功，可以读取媒体库。",
        "ok",
      );
      appendSystemLog("连接测试成功。");
    } catch (error) {
      state.connected = false;
      setConnectionState("error", "连接失败");
      setNotice(els.connectionNotice, error.message || "连接测试失败。", "error");
      appendSystemLog(`连接测试失败：${error.message}`);
    } finally {
      setButtonBusy("testConnection", els.testConnectionBtn, false);
      updateControls();
    }
  }

  async function handleLoadLibraries() {
    const sourceProfile = selectedSourceProfile();
    const targetProfile = selectedTargetProfile();
    if (!sourceProfile && !targetProfile && !state.connected) {
      setNotice(els.connectionNotice, "请先测试连接，或在服务器地址簿中选择导出/导入服务器。", "error");
      return;
    }

    setButtonBusy("loadLibraries", els.loadLibrariesBtn, true, "读取中");
    setButtonBusy("refreshLibraries", els.refreshLibrariesBtn, true, "刷新中");

    try {
      const requests = libraryLoadRequests(sourceProfile, targetProfile);
      const groups = [];
      for (const request of requests) {
        const data = await postJson("/api/libraries", request.payload);
        groups.push({ ...request, libraries: normalizeLibraries(data) });
      }
      state.libraryGroups = groups;
      const sourceGroup =
        groups.find((group) => group.role === "source" || group.role === "both") || groups[0];
      const targetGroup =
        groups.find((group) => group.role === "target" || group.role === "both") || sourceGroup;
      state.libraries = sourceGroup ? sourceGroup.libraries : [];
      state.targetLibraries = targetGroup ? targetGroup.libraries : [];
      state.selectedLibraryIds = new Set(state.libraries.map((library) => library.id));
      state.selectedTargetLibraryIds = new Set(
        state.targetLibraries.map((library) => library.id),
      );
      renderLibraries();
      const total = groups.reduce((sum, group) => sum + group.libraries.length, 0);
      setNotice(els.connectionNotice, `读取媒体库完成：${groups.length} 个服务器，共 ${total} 个媒体库。`, "ok");
      appendSystemLog(`读取媒体库完成：${groups.length} 个服务器，共 ${total} 个媒体库。`);
    } catch (error) {
      clearLibraryState();
      renderLibraries();
      setNotice(els.connectionNotice, `读取媒体库失败：${error.message}`, "error");
      appendSystemLog(`读取媒体库失败：${error.message}`);
    } finally {
      setButtonBusy("loadLibraries", els.loadLibrariesBtn, false);
      setButtonBusy("refreshLibraries", els.refreshLibrariesBtn, false);
      updateControls();
    }
  }

  function selectedSourceProfile() {
    return findProfileById(els.sourceProfileSelect.value || state.currentSource);
  }

  function selectedTargetProfile() {
    return findProfileById(els.targetProfileSelect.value || state.currentTarget);
  }

  function canUseSourceConnection() {
    return Boolean(selectedSourceProfile()) || state.connected;
  }

  function canUseTargetConnection() {
    return Boolean(selectedTargetProfile()) || state.connected;
  }

  function libraryLoadRequests(sourceProfile, targetProfile) {
    const requests = [];
    const addProfile = (role, profile) => {
      if (!profile) {
        return;
      }
      const id = String(readFirst(profile, ["id", "ID"]) || "").trim();
      if (!id) {
        return;
      }
      requests.push({
        role,
        profileId: id,
        label: formatServerProfileLabel(profile) || (role === "source" ? "导出服务器" : "导入服务器"),
        payload: { profileId: id },
      });
    };
    const sourceID = String(readFirst(sourceProfile, ["id", "ID"]) || "").trim();
    const targetID = String(readFirst(targetProfile, ["id", "ID"]) || "").trim();
    if (sourceID && targetID && sourceID === targetID) {
      requests.push({
        role: "both",
        profileId: sourceID,
        label: formatServerProfileLabel(sourceProfile) || "导出/导入服务器",
        payload: { profileId: sourceID },
      });
    } else {
      addProfile("source", sourceProfile);
      addProfile("target", targetProfile);
    }
    if (requests.length === 0) {
      const connection = getConnection();
      requests.push({
        role: "source",
        profileId: "",
        label: "当前连接",
        payload: {
          ...connection,
          profileId: profileIdForCurrentConnection(),
        },
      });
    }
    return requests;
  }

  async function handleRefreshExports() {
    setButtonBusy("refreshExports", els.refreshExportsBtn, true, "刷新中");

    try {
      const data = await fetchJson("/api/exports");
      state.exports = normalizeExports(data);
      renderExports();
      const count = state.exports.length;
      setNotice(
        els.importNotice,
        count ? `已发现 ${count} 个导出包。` : "未发现导出包，请先刷新或完成导出任务。",
        count ? "ok" : "",
      );
    } catch (error) {
      setNotice(els.importNotice, `读取导出包失败：${error.message}`, "error");
      appendSystemLog(`读取导出包失败：${error.message}`);
    } finally {
      setButtonBusy("refreshExports", els.refreshExportsBtn, false);
      updateControls();
    }
  }

  function handleExportSelection() {
    updateControls();
  }

  async function handleStartExport() {
    const libraryIds = Array.from(state.selectedLibraryIds);
    if (!canUseSourceConnection() || libraryIds.length === 0) {
      setNotice(els.connectionNotice, "请先选择导出服务器，并勾选至少一个导出媒体库。", "error");
      return;
    }

    const options = collectExportOptions();
    persistTaskPreferences();
    const connection = getConnection();
    const payload = {
      connection,
      baseUrl: connection.baseUrl,
      serverUrl: connection.serverUrl,
      apiKey: connection.apiKey,
      sourceProfileId: els.sourceProfileSelect.value || state.currentSource,
      libraryIds,
      options,
      ...options,
    };

    setButtonBusy("startExport", els.startExportBtn, true, "创建中");

    try {
      const data = await postJson("/api/jobs/export", payload);
      const jobId = readJobId(data);
      if (!jobId) {
        throw new Error("后端未返回任务 ID。");
      }

      beginJob(jobId, "导出", data);
      loadJobs(false);
      appendSystemLog(
        `导出任务已创建：${jobId}。媒体库 ${libraryIds.length} 个，图片类型 ${formatImageOptions(options)}。`,
      );
    } catch (error) {
      appendSystemLog(`创建导出任务失败：${error.message}`);
      setNotice(els.connectionNotice, `创建导出任务失败：${error.message}`, "error");
    } finally {
      setButtonBusy("startExport", els.startExportBtn, false);
      updateControls();
    }
  }

  async function handleStartPrecheck() {
    await startImportLikeJob({
      endpoint: "/api/jobs/import/precheck",
      buttonKey: "startPrecheck",
      button: els.startPrecheckBtn,
      busyText: "预检中",
      dryRun: true,
      kind: "导入预检",
      actionName: "导入预检",
    });
  }

  async function handleStartImport() {
    await startImportLikeJob({
      endpoint: "/api/jobs/import",
      buttonKey: "startImport",
      button: els.startImportBtn,
      busyText: "创建中",
      dryRun: false,
      kind: "导入",
      actionName: "导入",
    });
  }

  async function handleApplyMediaDatabase() {
    const exportPath = selectedExportPath();
    const selectedTargetProfileId = targetProfileId();
    const databasePath = String(els.embyDatabasePath?.value || "").trim();
    const autoManageContainer = Boolean(els.autoManageContainer?.checked);
    if (!exportPath || !selectedTargetProfileId || !databasePath) {
      setNotice(els.importNotice, "请先选择导出包、目标服务器和数据库。", "error");
      return;
    }
    setButtonBusy("applyMediaDatabase", els.applyMediaDatabaseBtn, true, "应用中");
    try {
      const data = await postJson("/api/jobs/media-info/apply", {
        exportPath,
        targetProfileId: selectedTargetProfileId,
        databasePath,
        autoManageContainer,
      });
      const jobId = readJobId(data);
      if (!jobId) {
        throw new Error("后端未返回任务 ID。");
      }
      beginJob(jobId, "媒体信息写库", data);
      loadJobs(false);
      setNotice(els.importNotice, `媒体信息写库任务已创建：${jobId}`, "ok");
    } catch (error) {
      setNotice(els.importNotice, `创建媒体信息写库任务失败：${error.message}`, "error");
    } finally {
      setButtonBusy("applyMediaDatabase", els.applyMediaDatabaseBtn, false);
      updateControls();
    }
  }

  async function startImportLikeJob({ endpoint, buttonKey, button, busyText, dryRun, kind, actionName }) {
    const exportPath = selectedExportPath();
    if (!canUseTargetConnection() || !exportPath) {
      setNotice(els.importNotice, "请先选择导入服务器，并选择导出包。", "error");
      return;
    }
    const targetLibraryIds = Array.from(state.selectedTargetLibraryIds);
    if (state.targetLibraries.length > 0 && targetLibraryIds.length === 0) {
      setNotice(els.importNotice, "请至少勾选一个导入目标媒体库。", "error");
      return;
    }

    const options = collectImportOptions({ dryRun });
    persistTaskPreferences();
    const connection = getConnection();
    const payload = {
      connection,
      baseUrl: connection.baseUrl,
      serverUrl: connection.serverUrl,
      apiKey: connection.apiKey,
      targetProfileId: els.targetProfileSelect.value || state.currentTarget,
      targetLibraryIds,
      exportPath,
      options,
      ...options,
    };

    clearImportReport();
    setButtonBusy(buttonKey, button, true, busyText);

    try {
      const data = await postJson(endpoint, payload);
      const jobId = readJobId(data);
      if (!jobId) {
        throw new Error("后端未返回任务 ID。");
      }

      beginJob(jobId, kind, data);
      loadJobs(false);
      setNotice(els.importNotice, `${actionName}任务已创建：${jobId}`, "ok");
      appendSystemLog(
        `${actionName}任务已创建：${jobId}。导出包 ${exportPath}，图片类型 ${formatImageOptions(options)}。`,
      );
    } catch (error) {
      setNotice(els.importNotice, `创建${actionName}任务失败：${error.message}`, "error");
      appendSystemLog(`创建${actionName}任务失败：${error.message}`);
    } finally {
      setButtonBusy(buttonKey, button, false);
      updateControls();
    }
  }

  function selectedExportPath() {
    return String(els.exportsSelect?.value || "").trim();
  }

  async function handleStopJob() {
    const jobId = state.currentJobId;
    if (!jobId) {
      return;
    }

    setButtonBusy("stopJob", els.stopJobBtn, true, "中止中");
    try {
      const data = await postJson(`/api/jobs/${encodeURIComponent(jobId)}/stop`, {});
      const jobData = safeObject(data.job || data);
      renderJobStatus(jobData, state.currentJobKind);
      loadJobs(false);
      appendSystemLog(data.stopped === false ? `任务 ${jobId} 已经结束。` : `已中止任务 ${jobId}。`);
      stopPolling();
      setTimeout(closeLogStream, 300);
    } catch (error) {
      appendSystemLog(`中止任务失败：${error.message}`);
    } finally {
      setButtonBusy("stopJob", els.stopJobBtn, false);
      updateControls();
    }
  }

  async function handlePauseJob() {
    const jobId = state.currentJobId;
    if (!jobId) {
      return;
    }

    setButtonBusy("pauseJob", els.pauseJobBtn, true, "暂停中");
    try {
      const data = await postJson(`/api/jobs/${encodeURIComponent(jobId)}/pause`, {});
      renderJobStatus(safeObject(data.job || data), state.currentJobKind);
      loadJobs(false);
      appendSystemLog(data.paused === false ? `任务 ${jobId} 不能暂停。` : `已暂停任务 ${jobId}。`);
    } catch (error) {
      appendSystemLog(`暂停任务失败：${error.message}`);
    } finally {
      setButtonBusy("pauseJob", els.pauseJobBtn, false);
      updateControls();
    }
  }

  async function handleResumeJob() {
    const jobId = state.currentJobId;
    if (!jobId) {
      return;
    }

    setButtonBusy("resumeJob", els.resumeJobBtn, true, "继续中");
    try {
      const data = await postJson(`/api/jobs/${encodeURIComponent(jobId)}/resume`, {});
      renderJobStatus(safeObject(data.job || data), state.currentJobKind);
      loadJobs(false);
      appendSystemLog(data.resumed === false ? `任务 ${jobId} 不能继续。` : `已继续任务 ${jobId}。`);
    } catch (error) {
      appendSystemLog(`继续任务失败：${error.message}`);
    } finally {
      setButtonBusy("resumeJob", els.resumeJobBtn, false);
      updateControls();
    }
  }

  async function loadJobs(showNotice) {
    setButtonBusy("refreshJobs", els.refreshJobsBtn, true, "刷新中");
    try {
      const data = await fetchJson("/api/jobs");
      state.jobs = Array.isArray(data.jobs) ? data.jobs : [];
      renderJobList();
      if (showNotice) {
        appendSystemLog(`任务队列已刷新：${state.jobs.length} 个。`);
      }
    } catch (error) {
      if (showNotice) {
        appendSystemLog(`刷新任务队列失败：${error.message}`);
      }
    } finally {
      setButtonBusy("refreshJobs", els.refreshJobsBtn, false);
      updateControls();
    }
  }

  function handleSelectAllLibraries() {
    if (els.selectAllLibraries.checked) {
      state.selectedLibraryIds = new Set(state.libraries.map((library) => library.id));
      state.selectedTargetLibraryIds = new Set(
        state.targetLibraries.map((library) => library.id),
      );
    } else {
      state.selectedLibraryIds.clear();
      state.selectedTargetLibraryIds.clear();
    }
    renderLibraries();
    updateControls();
  }

  function handleDownloadLogs() {
    if (!state.currentJobId) {
      return;
    }
    const link = document.createElement("a");
    link.href = `/api/jobs/${encodeURIComponent(state.currentJobId)}/logs.txt`;
    link.download = `emby-migrator-job-${state.currentJobId}.log`;
    document.body.appendChild(link);
    link.click();
    link.remove();
  }

  async function handleCopyLogs() {
    const text = els.logWindow.textContent;
    if (!text) {
      return;
    }

    try {
      await navigator.clipboard.writeText(text);
      appendSystemLog("日志已复制到剪贴板。");
    } catch {
      appendSystemLog("浏览器拒绝剪贴板访问，请手动选择日志复制。");
    }
  }

  function collectExportOptions() {
    const skipImages = els.exportSkipImages.checked;
    return {
      skipImages,
      incremental: els.exportIncremental.checked,
      overwrite: els.exportOverwrite.checked,
      includePeopleImages: !skipImages && els.exportIncludePeopleImages.checked,
      includeMediaInfo: Boolean(els.exportIncludeMediaInfo?.checked),
      imageTypes: skipImages ? [] : getCheckedImageTypes("export"),
      concurrency: readConcurrency(els.exportConcurrency),
    };
  }

  function collectImportOptions({ dryRun = false } = {}) {
    const skipImages = els.importSkipImages.checked;
    return {
      dryRun,
      skipImages,
      overwrite: els.importOverwrite.checked,
      resume: Boolean(els.importResume?.checked),
      includePeopleImages: !skipImages && els.importIncludePeopleImages.checked,
      importMediaInfo: Boolean(els.importMediaInfo?.checked),
      mediaInfoMode: "database",
      imageTypes: skipImages ? [] : getCheckedImageTypes("import"),
      concurrency: readConcurrency(els.importConcurrency),
    };
  }

  function readConcurrency(input) {
    const value = Number.parseInt(input.value, 10);
    const normalized = Number.isFinite(value)
      ? Math.max(1, value)
      : DEFAULT_CONCURRENCY;
    input.value = String(normalized);
    return normalized;
  }

  function getConnection() {
    const baseUrl = els.serverUrl.value.trim().replace(/\/+$/, "");
    return {
      baseUrl,
      serverUrl: baseUrl,
      apiKey: els.apiKey.value.trim(),
    };
  }

  async function postJson(url, payload) {
    return fetchJson(url, {
      method: "POST",
      body: JSON.stringify(payload),
    });
  }

  async function fetchJson(url, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (options.body) {
      headers.set("Content-Type", "application/json");
    }

    const response = await fetch(url, { ...options, headers });
    const text = await response.text();
    const data = parseMaybeJson(text);

    if (response.status === 401 && !options.skipAuthRedirect) {
      state.authEnabled = true;
      state.authenticated = false;
      applyAuthState("登录已失效，请重新登录。");
      updateControls();
    }

    if (!response.ok) {
      const message =
        readFirst(data, ["error", "message", "detail", "title"]) ||
        text ||
        `请求失败：HTTP ${response.status}`;
      throw new Error(message);
    }

    return data || {};
  }

  function parseMaybeJson(text) {
    if (!text) {
      return null;
    }

    try {
      return JSON.parse(text);
    } catch {
      return text;
    }
  }

  function normalizeLibraries(data) {
    const list =
      (Array.isArray(data) && data) ||
      data.libraries ||
      data.items ||
      data.Items ||
      data.LibraryOptions ||
      [];

    return list.map((raw, index) => {
      const id = String(
        readFirst(raw, ["id", "Id", "itemId", "ItemId", "guid", "Guid"]) ||
          readFirst(raw, ["name", "Name"]) ||
          index,
      );
      const name = String(readFirst(raw, ["name", "Name"]) || `媒体库 ${index + 1}`);
      const type = readFirst(raw, ["type", "Type", "collectionType", "CollectionType"]) || "";
      const itemCount = readFirst(raw, [
        "itemCount",
        "ItemCount",
        "count",
        "Count",
        "childCount",
        "ChildCount",
      ]);

      return {
        id,
        name,
        type: String(type || ""),
        itemCount,
      };
    });
  }

  function normalizeExports(data) {
    const list =
      (Array.isArray(data) && data) ||
      data.exports ||
      data.items ||
      data.Items ||
      data.directories ||
      [];

    return list.map((raw, index) => {
      if (typeof raw === "string") {
        return {
          value: raw,
          label: raw,
          meta: "",
        };
      }

      const value = String(
        readFirst(raw, ["path", "Path", "directory", "Directory", "name", "Name", "id", "Id"]) ||
          index,
      );
      const label = String(readFirst(raw, ["name", "Name", "id", "Id"]) || value);
      const createdAt = readFirst(raw, ["createdAt", "CreatedAt", "time", "Time"]);
      const itemCount = readFirst(raw, ["itemCount", "ItemCount", "count", "Count"]);
      const source = readFirst(raw, ["source", "Source"]);
      const embyVersion = readFirst(raw, ["embyVersion", "EmbyVersion"]);
      const meta = [
        source,
        embyVersion ? `Emby ${embyVersion}` : "",
        createdAt,
        itemCount !== undefined ? `${itemCount} 项` : "",
      ]
        .filter(Boolean)
        .join(" · ");

      return {
        value,
        label,
        meta,
      };
    });
  }

  function renderLibraries() {
    els.libraryList.textContent = "";
    const groups =
      state.libraryGroups && state.libraryGroups.length
        ? state.libraryGroups
        : state.libraries.length
          ? [{ role: "source", label: "当前连接", libraries: state.libraries }]
          : [];

    if (!groups.length || groups.every((group) => !group.libraries.length)) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "暂无媒体库。请先测试连接，或选择已保存的导出/导入服务器后读取。";
      els.libraryList.appendChild(empty);
      els.librarySummary.textContent = "未读取到媒体库。";
      updateSelectedCount();
      return;
    }

    groups.forEach((group) => {
      const section = document.createElement("section");
      section.className = "library-group";
      const heading = document.createElement("div");
      heading.className = "library-group-heading";
      const title = document.createElement("strong");
      title.textContent = libraryGroupTitle(group.role);
      const sub = document.createElement("span");
      sub.textContent = `${group.label || "-"} · ${group.libraries.length} 个`;
      heading.append(title, sub);
      section.appendChild(heading);

      const grid = document.createElement("div");
      grid.className = "library-group-grid";
      group.libraries.forEach((library) => {
        grid.appendChild(renderLibraryItem(library, group.role));
      });
      section.appendChild(grid);
      els.libraryList.appendChild(section);
    });

    const total = groups.reduce((sum, group) => sum + group.libraries.length, 0);
    els.librarySummary.textContent = `已读取 ${groups.length} 个服务器，共 ${total} 个媒体库；导出使用导出库勾选项，导入/预检使用导入库勾选项。`;
    updateSelectedCount();
  }

  function libraryGroupTitle(role) {
    if (role === "target") {
      return "导入服务器媒体库";
    }
    if (role === "both") {
      return "导出/导入服务器媒体库";
    }
    return "导出服务器媒体库";
  }

  function renderLibraryItem(library, role) {
    const label = document.createElement("label");
    label.className = "library-item";

    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.value = library.id;
    checkbox.checked = isLibrarySelectedForRole(library.id, role);
    checkbox.addEventListener("change", () => {
      setLibrarySelectedForRole(library.id, role, checkbox.checked);
      updateControls();
    });

    const body = document.createElement("span");
    const name = document.createElement("strong");
    name.textContent = library.name;
    const meta = document.createElement("span");
    meta.className = "library-meta";
    addPill(meta, `ID: ${library.id}`);
    if (library.type) {
      addPill(meta, library.type);
    }
    if (library.itemCount !== undefined && library.itemCount !== null) {
      addPill(meta, `${library.itemCount} 项`);
    }
    if (role === "target") {
      addPill(meta, "导入目标");
    } else if (role === "both") {
      addPill(meta, "导出/导入");
    }

    body.append(name, meta);
    label.append(checkbox, body);
    return label;
  }

  function isLibrarySelectedForRole(id, role) {
    if (role === "target") {
      return state.selectedTargetLibraryIds.has(id);
    }
    if (role === "both") {
      return state.selectedLibraryIds.has(id) && state.selectedTargetLibraryIds.has(id);
    }
    return state.selectedLibraryIds.has(id);
  }

  function setLibrarySelectedForRole(id, role, selected) {
    const sets =
      role === "target"
        ? [state.selectedTargetLibraryIds]
        : role === "both"
          ? [state.selectedLibraryIds, state.selectedTargetLibraryIds]
          : [state.selectedLibraryIds];
    sets.forEach((set) => {
      if (selected) {
        set.add(id);
      } else {
        set.delete(id);
      }
    });
  }

  function renderExports() {
    els.exportsSelect.textContent = "";
    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = state.exports.length
      ? "选择一个导出包"
      : "先刷新并选择导出包";
    els.exportsSelect.appendChild(placeholder);

    state.exports.forEach((item) => {
      const option = document.createElement("option");
      option.value = item.value;
      option.textContent = item.meta ? `${item.label} (${item.meta})` : item.label;
      els.exportsSelect.appendChild(option);
    });
  }

  function addPill(parent, text) {
    const pill = document.createElement("span");
    pill.className = "pill";
    pill.textContent = text;
    parent.appendChild(pill);
  }

  function renderImageTypes(containerId, group) {
    const container = document.getElementById(containerId);
    container.textContent = "";

    IMAGE_TYPES.forEach((type) => {
      const label = document.createElement("label");
      label.className = "image-type";

      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.value = type;
      checkbox.checked = true;
      checkbox.dataset.imageGroup = group;
      checkbox.addEventListener("change", handleTaskPreferenceChanged);

      const text = document.createElement("span");
      text.textContent = `${IMAGE_TYPE_LABELS[type] || type} (${type})`;

      label.append(checkbox, text);
      container.appendChild(label);
    });
  }

  function getCheckedImageTypes(group) {
    return Array.from(
      document.querySelectorAll(`input[data-image-group="${group}"]:checked`),
    ).map((input) => input.value);
  }

  function setCheckedImageTypes(group, imageTypes) {
    const selectedValues = normalizeImageTypesForDefault(imageTypes);
    const selected = new Set(selectedValues.map((type) => String(type).toLowerCase()));
    document.querySelectorAll(`input[data-image-group="${group}"]`).forEach((input) => {
      input.checked = selected.has(String(input.value).toLowerCase());
    });
  }

  function normalizeImageTypesForDefault(imageTypes) {
    if (!Array.isArray(imageTypes)) {
      return [...DEFAULT_IMAGE_TYPES];
    }
    const known = new Set(DEFAULT_IMAGE_TYPES.map((type) => type.toLowerCase()));
    const seen = new Set();
    const normalized = [];
    imageTypes.forEach((value) => {
      const type = String(value || "").trim();
      const key = type.toLowerCase();
      if (!known.has(key) || seen.has(key)) {
        return;
      }
      seen.add(key);
      normalized.push(type);
    });
    return normalized.length > 0 ? normalized : [...DEFAULT_IMAGE_TYPES];
  }

  function updateSelectedCount() {
    const sourceSelected = state.selectedLibraryIds.size;
    const sourceTotal = state.libraries.length;
    const targetSelected = state.selectedTargetLibraryIds.size;
    const targetTotal = state.targetLibraries.length;
    const selected = sourceSelected + targetSelected;
    const total = sourceTotal + targetTotal;
    els.selectedCount.textContent =
      targetTotal > 0
        ? `已选 导出 ${sourceSelected}/${sourceTotal}，导入 ${targetSelected}/${targetTotal}`
        : `已选 ${sourceSelected} 个`;
    els.selectAllLibraries.checked = total > 0 && selected === total;
    els.selectAllLibraries.indeterminate = selected > 0 && selected < total;
  }

  function updateControls() {
    const locked = state.authEnabled && !state.authenticated;
    const operator = !locked && (!state.role || state.role === "operator" || state.role === "admin");
    const canLoadLibraries = canUseSourceConnection() || canUseTargetConnection();
    const hasLibraries = state.libraries.length > 0 || state.targetLibraries.length > 0;
    const hasSelectedLibraries = state.selectedLibraryIds.size > 0;
    const hasSelectedTargetLibraries =
      state.targetLibraries.length === 0 || state.selectedTargetLibraryIds.size > 0;
    const hasImportPath = Boolean(selectedExportPath());
    const hasActiveJob = Boolean(state.currentJobId) && !TERMINAL_STATES.has(state.currentJobStatus);
    const currentStatus = normalizeStatus(state.currentJobStatus);
    els.testConnectionBtn.disabled = locked || state.busy.has("testConnection");
    els.loadLibrariesBtn.disabled = locked || !canLoadLibraries || state.busy.has("loadLibraries");
    els.refreshLibrariesBtn.disabled =
      locked || !canLoadLibraries || state.busy.has("refreshLibraries");
    els.refreshExportsBtn.disabled = locked || state.busy.has("refreshExports");
    els.saveAppSettingsBtn.disabled = locked || state.busy.has("saveAppSettings");
    els.refreshEmbyDatabasesBtn.disabled =
      locked || !targetProfileId() || state.busy.has("refreshEmbyDatabases");
    els.embyDatabasePath.disabled = locked || state.busy.has("refreshEmbyDatabases");
    els.autoManageContainer.disabled =
      locked || !state.dockerAvailable || state.busy.has("refreshEmbyDatabases");
    els.saveTelegramBtn.disabled = locked || state.busy.has("saveTelegram");
    els.testTelegramBtn.disabled = locked || state.busy.has("testTelegram");
    els.changePasswordBtn.disabled = locked || !state.authenticated || state.busy.has("changePassword");
    els.selectAllLibraries.disabled = locked || !hasLibraries;
    els.startExportBtn.disabled =
      locked || !canUseSourceConnection() || !hasSelectedLibraries || state.busy.has("startExport");
    els.refreshJobsBtn.disabled = locked || state.busy.has("refreshJobs");
    els.refreshJobBtn.disabled = locked || !state.currentJobId || state.busy.has("refreshJob");
    els.pauseJobBtn.disabled =
      locked || !state.currentJobId || currentStatus !== "running" || state.busy.has("pauseJob");
    els.resumeJobBtn.disabled =
      locked || !state.currentJobId || currentStatus !== "paused" || state.busy.has("resumeJob");
    els.stopJobBtn.disabled = locked || !hasActiveJob || state.busy.has("stopJob");
    els.downloadLogsBtn.disabled = locked || !state.currentJobId;
    els.startPrecheckBtn.disabled =
      locked ||
      !canUseTargetConnection() ||
      !hasImportPath ||
      !hasSelectedTargetLibraries ||
      state.busy.has("startPrecheck");
    els.startImportBtn.disabled =
      locked ||
      !canUseTargetConnection() ||
      !hasImportPath ||
      !hasSelectedTargetLibraries ||
      state.busy.has("startImport");
    els.applyMediaDatabaseBtn.disabled =
      !operator ||
      !selectedExportPath() ||
      !targetProfileId() ||
      !String(els.embyDatabasePath?.value || "").trim() ||
      state.busy.has("applyMediaDatabase");

    setImageControlsDisabled("export", els.exportSkipImages.checked);
    setImageControlsDisabled("import", els.importSkipImages.checked);
    els.exportIncludePeopleImages.disabled = els.exportSkipImages.checked;
    els.importIncludePeopleImages.disabled = els.importSkipImages.checked;

    updateSelectedCount();
  }

  function setImageControlsDisabled(group, disabled) {
    document.querySelectorAll(`input[data-image-group="${group}"]`).forEach((input) => {
      input.disabled = disabled;
    });
  }

  function beginJob(jobId, kind, initialData) {
    state.currentJobId = String(jobId);
    state.currentJobKind = kind;
    state.seenLogLines.clear();
    clearImportReport();
    closeLogStream();
    stopPolling();
    renderJobStatus({ ...safeObject(initialData), id: jobId, status: "queued" }, kind);
    startLogStream(jobId);
    startPolling();
    updateControls();
  }

  function startLogStream(jobId) {
    state.sseErrorReported = false;
    const url = `/api/jobs/${encodeURIComponent(jobId)}/logs`;
    const source = new EventSource(url);
    state.eventSource = source;

    source.onopen = () => {
      appendSystemLog(`已连接任务 ${jobId} 日志流。`);
      els.logSummary.textContent = `正在接收任务 ${jobId} 的日志。`;
    };

    source.onmessage = (event) => {
      handleLogEvent(event.data);
    };

    source.addEventListener("log", (event) => {
      handleLogEvent(event.data);
    });

    source.addEventListener("status", (event) => {
      const data = parseMaybeJson(event.data);
      renderJobStatus(safeObject(data), state.currentJobKind);
      appendSystemLog(`任务状态更新：${formatStatusLine(data)}。`);
    });

    source.addEventListener("done", (event) => {
      const data = parseMaybeJson(event.data);
      if (data && typeof data === "object") {
        renderJobStatus(safeObject(data), state.currentJobKind);
      }
      stopPolling();
      closeLogStream();
    });

    source.onerror = () => {
      if (!state.sseErrorReported) {
        appendSystemLog("日志流暂时中断，状态轮询会继续。");
        state.sseErrorReported = true;
      }
    };
  }

  function handleLogEvent(raw) {
    const data = parseMaybeJson(raw);
    if (data && typeof data === "object" && looksLikeJobStatus(data)) {
      renderJobStatus(safeObject(data), state.currentJobKind);
    }

    appendLog(formatLogData(data));
  }

  function startPolling() {
    refreshCurrentJob(false);
    state.pollTimer = window.setInterval(() => refreshCurrentJob(false), 2000);
  }

  function stopPolling() {
    if (state.pollTimer) {
      window.clearInterval(state.pollTimer);
      state.pollTimer = 0;
    }
  }

  async function refreshCurrentJob(showErrors) {
    if (!state.currentJobId) {
      return;
    }

    setButtonBusy("refreshJob", els.refreshJobBtn, true, "刷新中");
    try {
      const data = await fetchJson(`/api/jobs/${encodeURIComponent(state.currentJobId)}`);
      renderJobStatus(safeObject(data), state.currentJobKind);

      const status = String(readFirst(data, ["status", "state", "Status", "State"]) || "")
        .toLowerCase()
        .trim();
      if (TERMINAL_STATES.has(status)) {
        stopPolling();
        setTimeout(closeLogStream, 800);
      }
    } catch (error) {
      if (showErrors) {
        appendSystemLog(`刷新任务状态失败：${error.message}`);
      }
    } finally {
      setButtonBusy("refreshJob", els.refreshJobBtn, false);
      updateControls();
    }
  }

  function renderJobStatus(data, fallbackKind) {
	const previousStatus = state.currentJobStatus;
    const statusValue =
      readFirst(data, ["status", "state", "Status", "State"]) ||
      readFirst(data, ["phase", "Phase"]);
    const jobId = readJobId(data) || state.currentJobId || "-";
    const kind =
      readFirst(data, ["kind", "type", "jobType", "Kind", "Type", "JobType"]) ||
      fallbackKind ||
      "-";
    const status = statusValue || "已创建";

    els.jobKind.textContent = formatJobKind(kind);
    els.jobState.textContent = formatJobStatus(status);
    els.jobId.textContent = jobId;
    els.jobProgress.textContent = formatProgress(data);
    state.currentJobStatus = normalizeStatus(status);
    if (
      state.currentJobStatus === "done" &&
      previousStatus !== "done" &&
      String(kind).toLowerCase().trim() === "export"
    ) {
      handleRefreshExports();
    }
    if (readJobId(data) || statusValue) {
      upsertJob({ ...safeObject(data), id: jobId, type: kind, status });
    }
    renderImportReport(data, kind);
    updateControls();
  }

  function looksLikeJobStatus(data) {
    return Boolean(
      readJobId(data) ||
        readFirst(data, ["status", "state", "Status", "State"]) ||
        readFirst(data, ["phase", "Phase"]),
    );
  }

  function upsertJob(job) {
    const jobId = readJobId(job);
    if (!jobId) {
      return;
    }
    const next = safeObject(job);
    const index = state.jobs.findIndex((item) => readJobId(item) === jobId);
    if (index >= 0) {
      state.jobs[index] = { ...state.jobs[index], ...next };
    } else {
      state.jobs.unshift(next);
    }
    state.jobs.sort((a, b) => {
      const aTime = new Date(readFirst(a, ["createdAt", "CreatedAt"]) || 0).getTime();
      const bTime = new Date(readFirst(b, ["createdAt", "CreatedAt"]) || 0).getTime();
      return bTime - aTime;
    });
    renderJobList();
  }

  function renderJobList() {
    if (!els.jobList) {
      return;
    }
    els.jobList.textContent = "";
    if (!state.jobs.length) {
      const empty = document.createElement("div");
      empty.className = "job-list-empty";
      empty.textContent = "暂无任务";
      els.jobList.appendChild(empty);
      return;
    }

    const groups = [
      { label: "运行中", jobs: [] },
      { label: "等待中", jobs: [] },
      { label: "已完成", jobs: [] },
      { label: "失败/中止", jobs: [] },
      { label: "其他", jobs: [] },
    ];
    state.jobs.slice(0, 24).forEach((job) => {
      const status = normalizeStatus(readFirst(job, ["status", "state", "Status", "State"]) || "");
      if (status === "running" || status === "paused") {
        groups[0].jobs.push(job);
      } else if (status === "queued") {
        groups[1].jobs.push(job);
      } else if (status === "done") {
        groups[2].jobs.push(job);
      } else if (status === "failed" || status === "stopped") {
        groups[3].jobs.push(job);
      } else {
        groups[4].jobs.push(job);
      }
    });

    groups
      .filter((group) => group.jobs.length > 0)
      .forEach((group) => {
        const section = document.createElement("div");
        section.className = "job-list-group";
        const heading = document.createElement("div");
        heading.className = "job-list-group-heading";
        heading.textContent = `${group.label} ${group.jobs.length}`;
        section.appendChild(heading);
        group.jobs.forEach((job) => section.appendChild(createJobListRow(job)));
        els.jobList.appendChild(section);
      });
  }

  function createJobListRow(job) {
    const jobId = readJobId(job);
    const row = document.createElement("div");
    row.className = "job-list-row";
    if (jobId && jobId === state.currentJobId) {
      row.classList.add("is-current");
    }

    const main = document.createElement("div");
    main.className = "job-list-main";

    const title = document.createElement("strong");
    title.textContent = [
      formatJobKind(readFirst(job, ["type", "kind", "Type", "Kind"]) || "-"),
      formatJobStatus(readFirst(job, ["status", "state", "Status", "State"]) || "-"),
    ]
      .filter(Boolean)
      .join(" / ");

    const meta = document.createElement("span");
    meta.textContent = [
      jobId,
      formatShortTime(readFirst(job, ["createdAt", "CreatedAt"])),
      readFirst(job, ["message", "Message", "error", "Error"]) || "",
    ]
      .filter(Boolean)
      .join(" · ");

    main.append(title, meta);

    const button = document.createElement("button");
    button.type = "button";
    button.className = "button small";
    button.textContent = "查看";
    button.addEventListener("click", () => {
      if (!jobId) {
        return;
      }
      beginJob(jobId, readFirst(job, ["type", "kind", "Type", "Kind"]) || state.currentJobKind, job);
    });

    row.append(main, button);
    return row;
  }

  function renderImportReport(data, kind) {
    if (!els.importReport) {
      return;
    }
    const rawReport = readImportReport(data);
    if (!rawReport) {
      if (isImportKind(kind)) {
        els.importReport.classList.add("is-hidden");
      }
      return;
    }

    const report = safeObject(rawReport);
    const summary = safeObject(readFirst(report, ["summary", "Summary"]));
    const compatibility = safeObject(readFirst(report, ["compatibility", "Compatibility"]));
    const matches = readReportArray(report, ["matches", "Matches"]);
    const failures = safeObject(readFirst(report, ["failures", "Failures"]));
    const failureState = reportFailureState(failures);
    const skipState = reportSkipState(report);
    const dryRun = Boolean(readFirst(report, ["dryRun", "DryRun"]));
    const total =
      matches.length ||
      numberValue(summary.items) ||
      numberValue(summary.matched) + numberValue(summary.unmatched) + numberValue(summary.ambiguous) + numberValue(summary.errors);
    const matched = numberValue(summary.matched);
    const unmatched = numberValue(summary.unmatched);
    const ambiguous = numberValue(summary.ambiguous);
    const errors = numberValue(summary.errors);
    const metadataUpdated = numberValue(summary.metadataUpdated);
    const itemImagesPushed = numberValue(summary.itemImagesPushed);
    const itemImagesFailed = numberValue(summary.itemImagesFailed);
    const peopleImages = numberValue(summary.peopleImages);
    const peopleImagesFailed = numberValue(summary.peopleImagesFailed);
    const hasRisk =
      unmatched > 0 ||
      ambiguous > 0 ||
      errors > 0 ||
      itemImagesFailed > 0 ||
      peopleImagesFailed > 0 ||
      failureState.total > 0;

    els.importReport.textContent = "";
    els.importReport.className = `import-report ${hasRisk ? "risk" : "ok"}`;

    const heading = document.createElement("div");
    heading.className = "report-heading";
    const title = document.createElement("strong");
    title.textContent = dryRun ? "导入预检结果" : "导入结果";
    const hint = document.createElement("span");
    hint.textContent = dryRun
      ? hasRisk
        ? "存在未匹配、歧义或错误，建议处理后再正式导入。"
        : "匹配结果正常，可以进行正式导入。"
      : hasRisk
        ? "导入完成，但存在需要复查的问题。"
        : "导入完成，未发现异常统计。";
    heading.append(title, hint);
    const profileName = readFirst(compatibility, ["name", "Name"]);
    if (profileName) {
      const profile = document.createElement("span");
      profile.textContent = `兼容策略：${profileName}`;
      heading.appendChild(profile);
    }
    els.importReport.appendChild(heading);

    const stats = document.createElement("div");
    stats.className = "report-stats";
    addReportStat(stats, "项目", total);
    addReportStat(stats, dryRun ? "匹配" : "元数据成功", dryRun ? matched : metadataUpdated);
    addReportStat(stats, "未匹配", unmatched);
    addReportStat(stats, "歧义", ambiguous);
    addReportStat(stats, "错误", errors);
    if (skipState.total > 0) {
      addReportStat(stats, "跳过写入", skipState.total);
    }
    if (!dryRun) {
      addReportStat(stats, "媒体图片", `${itemImagesPushed}/${itemImagesFailed}`);
      addReportStat(stats, "人物头像", `${peopleImages}/${peopleImagesFailed}`);
    }
    els.importReport.appendChild(stats);

    appendIncrementalSection(els.importReport, report);
    appendSkipsSection(els.importReport, skipState);
    appendFailureSection(els.importReport, failureState, matches);
    appendDiffSection(els.importReport, report);
  }

  function clearImportReport() {
    if (!els.importReport) {
      return;
    }
    els.importReport.textContent = "";
    els.importReport.className = "import-report is-hidden";
  }

  function readImportReport(data) {
    const result = safeObject(readFirst(data, ["result", "Result"]));
    return readFirst(result, ["report", "Report"]) || readFirst(data, ["report", "Report"]) || null;
  }

  function isImportKind(kind) {
    return String(kind || "").toLowerCase().includes("import") || String(kind || "").includes("导入");
  }

  function addReportStat(parent, label, value) {
    const item = document.createElement("div");
    const name = document.createElement("span");
    name.textContent = label;
    const number = document.createElement("strong");
    number.textContent = String(value ?? 0);
    item.append(name, number);
    parent.appendChild(item);
  }

  function appendIncrementalSection(parent, report) {
    const incremental = safeObject(readFirst(report, ["incremental", "Incremental"]));
    const details = document.createElement("div");
    details.className = "report-kv";
    let detailCount = 0;
    detailCount += addReportDetail(
      details,
      "来源",
      formatIncrementalSource(readFirst(incremental, ["source", "Source"])),
    );
    detailCount += addReportDetail(
      details,
      "变更项目",
      readFirst(incremental, ["changedItems", "ChangedItems"]),
    );
    detailCount += addReportDetail(
      details,
      "跳过项目",
      readFirst(incremental, ["skippedItems", "SkippedItems"]),
    );
    detailCount += addReportDetail(
      details,
      "基线包",
      readFirst(incremental, ["baselineExportName", "BaselineExportName"]),
    );

    const mode = formatIncrementalMode(readFirst(incremental, ["targetMode", "TargetMode"]));
    const note = formatIncrementalNote(readFirst(incremental, ["note", "Note"]));
    if (!detailCount && !mode && !note) {
      return;
    }

    const section = createReportSection("增量包");
    if (detailCount) {
      section.appendChild(details);
    }
    if (mode) {
      appendReportNote(section, `模式说明：${mode}`);
    }
    if (note && note !== mode) {
      appendReportNote(section, `说明：${note}`);
    }
    parent.appendChild(section);
  }

  function appendSkipsSection(parent, skipState) {
    if (skipState.total <= 0 && !skipState.sources.length) {
      return;
    }

    const section = createReportSection("跳过写入");
    const tags = document.createElement("div");
    tags.className = "report-tags";
    if (skipState.total > 0) {
      addReportTag(tags, `总计 ${skipState.total}`, true);
    }
    skipState.sources.forEach((source) => addReportTag(tags, `${source.label} ${source.value}`));
    section.appendChild(tags);
    if (!skipState.sources.length) {
      appendReportNote(section, "来源字段暂未提供，仅显示总数。");
    }
    parent.appendChild(section);
  }

  function appendFailureSection(parent, failureState, matches) {
    if (failureState.groups.length || failureState.total > 0 || failureState.truncated) {
      const section = createReportSection("失败示例");
      const tags = document.createElement("div");
      tags.className = "report-tags";
      if (failureState.total > 0) {
        addReportTag(tags, `总计 ${failureState.total}`, true);
      }
      failureState.groups.forEach((group) => addReportTag(tags, `${group.label} ${group.count}`));
      section.appendChild(tags);

      const list = document.createElement("div");
      list.className = "report-samples";
      let localTruncated = false;
      failureState.groups.forEach((group) => {
        const samples = group.items.slice(0, REPORT_FAILURE_SAMPLE_LIMIT);
        localTruncated = localTruncated || group.items.length > samples.length;
        samples.forEach((item) => appendReportSample(list, item, group.label));
      });
      if (!list.children.length) {
        reportProblemSamples(matches).forEach((item) => appendReportSample(list, item));
      }
      if (list.children.length) {
        section.appendChild(list);
      }
      if (failureState.truncated || localTruncated) {
        appendReportNote(section, "示例已截断，请查看导出包内的完整任务报告文件获取全部明细。");
      } else if (!list.children.length) {
        appendReportNote(section, "失败明细暂未提供，请查看导出包内的完整任务报告文件。");
      }
      parent.appendChild(section);
      return;
    }

    const samples = reportProblemSamples(matches);
    if (!samples.length) {
      return;
    }

    const section = createReportSection("需复查示例");
    const list = document.createElement("div");
    list.className = "report-samples";
    samples.forEach((item) => appendReportSample(list, item));
    section.appendChild(list);
    parent.appendChild(section);
  }

  function appendDiffSection(parent, report) {
    const diff = safeObject(readFirst(report, ["diff", "Diff"]));
    const expected = safeObject(
      readFirst(diff, ["expected", "Expected"]) || readFirst(diff, ["before", "Before"]),
    );
    const actual = safeObject(
      readFirst(diff, ["actual", "Actual"]) || readFirst(diff, ["after", "After"]),
    );
    const missing = safeObject(readFirst(diff, ["missing", "Missing"]));
    const expectedParts = exportSummaryParts(expected);
    const actualParts = importSummaryParts(actual);
    const missingParts = importGapParts(missing);
    if (!expectedParts.length && !actualParts.length && !missingParts.length) {
      return;
    }

    const section = createReportSection("对比/差异");
    const compare = document.createElement("div");
    compare.className = "report-compare";
    appendReportCompareBlock(compare, "导出包期望", expectedParts);
    appendReportCompareBlock(compare, "本次结果", actualParts);
    appendReportCompareBlock(compare, "缺口", missingParts);
    section.appendChild(compare);
    appendReportNote(section, readFirst(diff, ["note", "Note"]));
    parent.appendChild(section);
  }

  function createReportSection(title, meta = "") {
    const section = document.createElement("div");
    section.className = "report-section";
    const heading = document.createElement("div");
    heading.className = "report-section-heading";
    const label = document.createElement("span");
    label.className = "label";
    label.textContent = title;
    heading.appendChild(label);
    if (meta) {
      const note = document.createElement("span");
      note.className = "report-note";
      note.textContent = meta;
      heading.appendChild(note);
    }
    section.appendChild(heading);
    return section;
  }

  function addReportDetail(parent, label, value) {
    if (!hasDisplayValue(value)) {
      return 0;
    }

    const item = document.createElement("div");
    const name = document.createElement("span");
    name.textContent = label;
    const detail = document.createElement("strong");
    detail.textContent = formatReportValue(value);
    item.append(name, detail);
    parent.appendChild(item);
    return 1;
  }

  function addReportTag(parent, text, strong = false) {
    const tag = document.createElement("span");
    tag.className = strong ? "report-tag strong" : "report-tag";
    tag.textContent = text;
    parent.appendChild(tag);
  }

  function appendReportNote(parent, text) {
    if (!text) {
      return;
    }
    const note = document.createElement("span");
    note.className = "report-note";
    note.textContent = text;
    parent.appendChild(note);
  }

  function appendReportSample(parent, item, statusLabel = "") {
    const row = document.createElement("div");
    row.className = "report-sample";
    const name = document.createElement("strong");
    name.textContent =
      readFirst(item, ["sourceName", "SourceName"]) ||
      readFirst(item, ["stableKey", "StableKey"]) ||
      "未知项目";
    const detail = document.createElement("span");
    detail.textContent = [
      statusLabel || formatReportStatus(readFirst(item, ["status", "Status"])),
      readFirst(item, ["reason", "Reason"]) || readFirst(item, ["error", "Error"]),
      formatCandidates(readFirst(item, ["candidates", "Candidates"])),
    ]
      .filter(Boolean)
      .join(" · ");
    row.append(name, detail);
    parent.appendChild(row);
  }

  function appendReportCompareBlock(parent, title, parts) {
    if (!parts.length) {
      return;
    }

    const block = document.createElement("div");
    block.className = "report-compare-block";
    const label = document.createElement("strong");
    label.textContent = title;
    const body = document.createElement("span");
    body.textContent = parts.join(" · ");
    block.append(label, body);
    parent.appendChild(block);
  }

  function reportFailureState(failures) {
    const counts = safeObject(readFirst(failures, ["counts", "Counts"]));
    const groups = [
      {
        label: "未匹配",
        count: failureGroupCount(failures, counts, ["unmatched", "Unmatched"]),
        items: readReportArray(failures, ["unmatched", "Unmatched"]),
      },
      {
        label: "歧义",
        count: failureGroupCount(failures, counts, ["ambiguous", "Ambiguous"]),
        items: readReportArray(failures, ["ambiguous", "Ambiguous"]),
      },
      {
        label: "错误",
        count: failureGroupCount(failures, counts, ["failed", "Failed"]),
        items: readReportArray(failures, ["failed", "Failed"]),
      },
      {
        label: "媒体图片失败",
        count: failureGroupCount(failures, counts, ["imageFailed", "ImageFailed"]),
        items: readReportArray(failures, ["imageFailed", "ImageFailed"]),
      },
      {
        label: "人物头像失败",
        count: failureGroupCount(failures, counts, ["personImageFailed", "PersonImageFailed"]),
        items: readReportArray(failures, ["personImageFailed", "PersonImageFailed"]),
      },
    ].filter((group) => group.count > 0 || group.items.length > 0);
    const groupedTotal = groups.reduce((sum, group) => sum + group.count, 0);
    return {
      groups,
      total: numberValue(readFirst(failures, ["total", "Total"])) || groupedTotal,
      truncated: Boolean(readFirst(failures, ["truncated", "Truncated"])),
    };
  }

  function failureGroupCount(failures, counts, keys) {
    return numberValue(readFirst(counts, keys)) || readReportArray(failures, keys).length;
  }

  function reportSkipState(report) {
    const skips = safeObject(readFirst(report, ["skips", "Skips"]));
    const sources = [
      {
        label: "增量包",
        value: numberValue(readFirst(skips, ["incrementalManifest", "IncrementalManifest"])),
      },
      {
        label: "断点续跑",
        value: numberValue(readFirst(skips, ["resume", "Resume"])),
      },
      {
        label: "预检模式",
        value: numberValue(readFirst(skips, ["dryRunWrites", "DryRunWrites"])),
      },
    ].filter((source) => source.value > 0);
    const sourceTotal = sources.reduce((sum, source) => sum + source.value, 0);
    const total =
      numberValue(readFirst(skips, ["total", "Total"])) ||
      sourceTotal ||
      numberValue(readFirst(report, ["writesSkipped", "WritesSkipped"]));
    return { total, sources };
  }

  function reportProblemSamples(matches) {
    return matches
      .filter((item) => {
        const status = String(readFirst(item, ["status", "Status"]) || "").toLowerCase();
        return status && status !== "matched" && status !== "updated";
      })
      .slice(0, 6);
  }

  function formatCandidates(candidates) {
    if (!Array.isArray(candidates) || candidates.length === 0) {
      return "";
    }
    const preview = candidates.slice(0, 3).map((item) => String(item)).join("、");
    return candidates.length > 3 ? `候选：${preview} 等 ${candidates.length} 个` : `候选：${preview}`;
  }

  function formatReportStatus(status) {
    const normalized = String(status || "").toLowerCase();
    const labels = {
      matched: "已匹配",
      updated: "已写入",
      unmatched: "未匹配",
      ambiguous: "歧义",
      failed: "错误",
      "image-failed": "媒体图片失败",
      "person-image-failed": "人物头像失败",
      error: "错误",
      skipped: "已跳过",
    };
    return labels[normalized] || String(status || "");
  }

  function formatIncrementalSource(source) {
    const normalized = String(source || "").toLowerCase();
    if (normalized === "manifest") {
      return "导出清单";
    }
    return source;
  }

  function formatIncrementalMode(mode) {
    const normalized = String(mode || "").toLowerCase();
    if (normalized === "package-only") {
      return "仅按导出包标记处理，不对目标库做实时差异比较";
    }
    return mode;
  }

  function formatIncrementalNote(note) {
    const text = String(note || "").trim();
    if (!text) {
      return "";
    }
    if (text.toLowerCase().includes("does not perform target-side incremental comparison")) {
      return "导入会跳过导出清单标记未变化的项目，不会再对目标库做二次差异比较。";
    }
    return text;
  }

  function exportSummaryParts(summary) {
    const parts = [];
    addSummaryPart(parts, summary, "项目", ["items", "Items"]);
    addSummaryPart(parts, summary, "人物", ["people", "People"]);
    addSummaryPart(parts, summary, "媒体图", ["itemImages", "ItemImages"]);
    addSummaryPart(parts, summary, "头像", ["peopleImages", "PeopleImages"]);
    addSummaryPart(parts, summary, "包内错误", ["errors", "Errors"]);
    addSummaryPart(parts, summary, "已跳过", ["skippedItems", "SkippedItems"]);
    return parts;
  }

  function importSummaryParts(summary) {
    const parts = [];
    addSummaryPart(parts, summary, "匹配", ["matched", "Matched"]);
    addSummaryPart(parts, summary, "未匹配", ["unmatched", "Unmatched"]);
    addSummaryPart(parts, summary, "歧义", ["ambiguous", "Ambiguous"]);
    addSummaryPart(parts, summary, "错误", ["errors", "Errors"]);
    addSummaryPart(parts, summary, "元数据", ["metadataUpdated", "MetadataUpdated"]);
    addSummaryPair(
      parts,
      summary,
      "媒体图",
      ["itemImagesPushed", "ItemImagesPushed"],
      ["itemImagesFailed", "ItemImagesFailed"],
    );
    addSummaryPair(
      parts,
      summary,
      "头像",
      ["peopleImages", "PeopleImages"],
      ["peopleImagesFailed", "PeopleImagesFailed"],
    );
    return parts;
  }

  function importGapParts(gap) {
    const parts = [];
    addSummaryPart(parts, gap, "元数据缺口", ["metadata", "Metadata"]);
    addSummaryPart(parts, gap, "媒体图缺口", ["itemImages", "ItemImages"]);
    addSummaryPart(parts, gap, "头像缺口", ["peopleImages", "PeopleImages"]);
    addSummaryPart(parts, gap, "未匹配", ["unmatched", "Unmatched"]);
    addSummaryPart(parts, gap, "歧义", ["ambiguous", "Ambiguous"]);
    addSummaryPart(parts, gap, "错误", ["errors", "Errors"]);
    return parts;
  }

  function addSummaryPart(parts, summary, label, keys) {
    const value = numberValue(readFirst(summary, keys));
    if (value > 0) {
      parts.push(`${label} ${value}`);
    }
  }

  function addSummaryPair(parts, summary, label, successKeys, failedKeys) {
    const success = numberValue(readFirst(summary, successKeys));
    const failed = numberValue(readFirst(summary, failedKeys));
    if (success > 0 || failed > 0) {
      parts.push(`${label} ${success}/${failed}`);
    }
  }

  function readReportArray(object, keys) {
    const value = readFirst(object, keys);
    return Array.isArray(value) ? value : [];
  }

  function hasDisplayValue(value) {
    return value !== undefined && value !== null && String(value).trim() !== "";
  }

  function formatReportValue(value) {
    const text = String(value);
    const number = Number(text);
    return text.trim() !== "" && Number.isFinite(number) ? String(number) : text;
  }

  function closeLogStream() {
    if (state.eventSource) {
      state.eventSource.close();
      state.eventSource = null;
    }
  }

  function setConnectionState(status, text) {
    els.connectionDot.classList.remove("ok", "error", "pending");
    if (status) {
      els.connectionDot.classList.add(status);
    }
    els.connectionText.textContent = text;
  }

  function setAppVersion(version) {
    if (!els.appVersion) {
      return;
    }
    const normalized = String(version || "").trim();
    els.appVersion.textContent = normalized ? `v${normalized}` : "";
    els.appVersion.classList.toggle("is-hidden", !normalized);
  }

  function setButtonBusy(key, button, busy, busyText) {
    if (!button) {
      return;
    }

    if (!button.dataset.originalText) {
      button.dataset.originalText = button.textContent.trim();
    }

    if (busy) {
      state.busy.add(key);
      button.textContent = busyText || button.dataset.originalText;
    } else {
      state.busy.delete(key);
      button.textContent = button.dataset.originalText;
    }

    updateControls();
  }

  function setNotice(element, message, type = "") {
    if (!element) {
      return;
    }
    element.classList.remove("ok", "error");
    if (type) {
      element.classList.add(type);
    }
    const text = String(message || "");
    element.textContent = text;
    element.classList.toggle("is-hidden", text.trim() === "");
  }

  function appendSystemLog(message) {
    appendLog(`${formatBeijingTime(new Date().toISOString())} [system] ${message}`);
  }

  function appendLog(message) {
    const sanitized = sanitizeLogText(String(message || ""));
    const normalized = sanitized.trimEnd();
    if (!normalized || state.seenLogLines.has(normalized)) {
      return;
    }
    state.seenLogLines.add(normalized);
    compactSeenLogLines();
    const line = sanitized.endsWith("\n") ? sanitized : `${sanitized}\n`;
    els.logWindow.textContent += line;
    trimLogWindow();
    els.logWindow.scrollTop = els.logWindow.scrollHeight;
  }

  function compactSeenLogLines() {
    if (state.seenLogLines.size <= MAX_LOG_LINES * 3) {
      return;
    }
    state.seenLogLines = new Set(
      els.logWindow.textContent
        .split("\n")
        .map((line) => line.trimEnd())
        .filter(Boolean),
    );
  }

  function trimLogWindow() {
    const lines = els.logWindow.textContent.split("\n");
    if (lines.length <= MAX_LOG_LINES + 1) {
      return;
    }
    els.logWindow.textContent = lines.slice(-(MAX_LOG_LINES + 1)).join("\n");
  }

  function sanitizeLogText(text) {
    const { apiKey } = getConnection();
    let sanitized = text;
    if (apiKey && apiKey.length >= 4) {
      sanitized = sanitized.split(apiKey).join(maskKey(apiKey));
    }
    const telegramToken = els.telegramBotToken ? els.telegramBotToken.value.trim() : "";
    if (telegramToken && telegramToken.length >= 4) {
      sanitized = sanitized.split(telegramToken).join(maskKey(telegramToken));
    }
    return sanitized;
  }

  function maskKey(apiKey) {
    if (apiKey.length <= 8) {
      return "****";
    }
    return `${apiKey.slice(0, 3)}****${apiKey.slice(-3)}`;
  }

  function formatJobKind(value) {
    const normalized = String(value || "").toLowerCase().trim();
    return JOB_KIND_LABELS[normalized] || String(value || "-");
  }

  function formatJobStatus(value) {
    const normalized = normalizeStatus(value);
    return JOB_STATUS_LABELS[normalized] || String(value || "-");
  }

  function formatLogData(data) {
    if (data && typeof data === "object") {
      const message = readFirst(data, ["message", "log", "line", "Message", "Log", "Line"]);
      const level = readFirst(data, ["level", "Level"]) || "info";
      const time = readFirst(data, ["time", "Time", "updatedAt", "UpdatedAt"]);
      if (message) {
        return `${formatBeijingTime(time)} [${level}] ${message}`.trim();
      }
      const status = readFirst(data, ["status", "state", "Status", "State"]);
      if (status) {
        return `${formatBeijingTime(time)} [status] ${formatStatusLine(data)}`.trim();
      }
      return JSON.stringify(data);
    }
    return String(data || "");
  }

  function formatBeijingTime(value) {
    const date = value ? new Date(value) : new Date();
    if (Number.isNaN(date.getTime())) {
      return "";
    }
    const parts = new Intl.DateTimeFormat("zh-CN", {
      timeZone: "Asia/Shanghai",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      hour12: false,
    })
      .formatToParts(date)
      .reduce((acc, part) => {
        acc[part.type] = part.value;
        return acc;
      }, {});
    return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second} 北京时间`;
  }

  function formatShortTime(value) {
    if (!value) {
      return "";
    }
    const full = formatBeijingTime(value);
    return full.replace(" 北京时间", "");
  }

  function formatStatusLine(data) {
    if (!data || typeof data !== "object") {
      return String(data || "已更新");
    }
    return [
      formatJobStatus(readFirst(data, ["status", "state", "Status", "State"])),
      formatProgress(data),
    ]
      .filter(Boolean)
      .join(" / ");
  }

  function formatProgress(data) {
    const progress = readFirst(data, [
      "progress",
      "Progress",
      "progressPercent",
      "ProgressPercent",
      "percent",
      "Percent",
    ]);
    if (typeof progress === "number") {
      return progress <= 1 ? `${Math.round(progress * 100)}%` : `${Math.round(progress)}%`;
    }
    if (progress !== undefined && progress !== null && progress !== "") {
      return String(progress);
    }

    const current = readFirst(data, ["current", "Current", "processed", "Processed"]);
    const total = readFirst(data, ["total", "Total"]);
    if (current !== undefined && total !== undefined) {
      return `${current}/${total}`;
    }

    return "-";
  }

  function formatImageOptions(options) {
    if (options.skipImages) {
      return `跳过图片；并发 ${options.concurrency}`;
    }
    const people = options.includePeopleImages ? "含人物头像" : "不含人物头像";
    const mediaInfo = options.includeMediaInfo || options.importMediaInfo ? "含媒体技术信息" : "不含媒体技术信息";
    return `${options.imageTypes.join(", ")}；${people}；${mediaInfo}；并发 ${options.concurrency}`;
  }

  function readJobId(data) {
    if (!data) {
      return "";
    }
    if (typeof data === "string") {
      return data;
    }

    return String(
      readFirst(data, ["id", "jobId", "ID", "JobID"]) ||
        readFirst(data.job || {}, ["id", "jobId", "ID", "JobID"]) ||
        "",
    );
  }

  function normalizeStatus(value) {
    return String(value || "")
      .toLowerCase()
      .trim();
  }

  function normalizeRole(value) {
    const role = String(value || "").toLowerCase().trim();
    return ["viewer", "operator", "admin"].includes(role) ? role : "";
  }

  function numberValue(value) {
    const number = Number(value);
    return Number.isFinite(number) ? number : 0;
  }

  function readFirst(object, keys) {
    if (!object || typeof object !== "object") {
      return undefined;
    }

    for (const key of keys) {
      if (Object.prototype.hasOwnProperty.call(object, key)) {
        return object[key];
      }
    }
    return undefined;
  }

  function safeObject(value) {
    return value && typeof value === "object" ? value : {};
  }
})();
