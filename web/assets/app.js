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
  const MAX_LOG_LINES = 800;
  const THEME_STORAGE_KEY = "embyMigratorTheme";
  const CONNECTION_STORAGE_KEY = "embyMigrator.connection";
  const LEGACY_SERVER_URL_KEY = "embyMigrator.serverUrl";
  const DEFAULT_CONCURRENCY = 4;

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
    busy: new Set(),
    connected: false,
    hasTested: false,
    libraries: [],
    selectedLibraryIds: new Set(),
    exports: [],
    reports: [],
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
    initTheme();
    renderImageTypes("exportImageTypes", "export");
    renderImageTypes("importImageTypes", "import");
    restoreConnection();
    bindEvents();
    updateControls();
    checkAuth();
  }

  function cacheElements() {
    [
      "connectionForm",
      "appShell",
      "authGate",
      "authForm",
      "authPassword",
      "authLoginBtn",
      "authNotice",
      "authWarning",
      "appVersion",
      "logoutBtn",
      "themeToggleBtn",
      "serverUrl",
      "apiKey",
      "rememberConnection",
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
      "exportConcurrency",
      "startExportBtn",
      "refreshJobBtn",
      "stopJobBtn",
      "jobKind",
      "jobState",
      "jobId",
      "jobProgress",
      "refreshExportsBtn",
      "exportsSelect",
      "importPath",
      "refreshReportsBtn",
      "downloadReportBtn",
      "reportsSelect",
      "importSkipImages",
      "importOverwrite",
      "importIncludePeopleImages",
      "importConcurrency",
      "startPrecheckBtn",
      "startImportBtn",
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

  function bindEvents() {
    els.authForm.addEventListener("submit", handleLogin);
    els.logoutBtn.addEventListener("click", handleLogout);
    els.themeToggleBtn.addEventListener("click", handleThemeToggle);
    els.connectionForm.addEventListener("submit", handleConnectionTest);
    els.loadLibrariesBtn.addEventListener("click", handleLoadLibraries);
    els.refreshLibrariesBtn.addEventListener("click", handleLoadLibraries);
    els.selectAllLibraries.addEventListener("change", handleSelectAllLibraries);
    els.startExportBtn.addEventListener("click", handleStartExport);
    els.refreshJobBtn.addEventListener("click", () => refreshCurrentJob(true));
    els.stopJobBtn.addEventListener("click", handleStopJob);
    els.refreshExportsBtn.addEventListener("click", handleRefreshExports);
    els.exportsSelect.addEventListener("change", handleExportSelection);
    els.importPath.addEventListener("input", updateControls);
    els.refreshReportsBtn.addEventListener("click", handleRefreshReports);
    els.reportsSelect.addEventListener("change", updateControls);
    els.downloadReportBtn.addEventListener("click", handleDownloadReport);
    els.startPrecheckBtn.addEventListener("click", handleStartPrecheck);
    els.startImportBtn.addEventListener("click", handleStartImport);
    els.downloadLogsBtn.addEventListener("click", handleDownloadLogs);
    els.copyLogsBtn.addEventListener("click", handleCopyLogs);
    els.clearLogsBtn.addEventListener("click", () => {
      els.logWindow.textContent = "";
      state.seenLogLines.clear();
    });

    els.serverUrl.addEventListener("input", handleConnectionInputChanged);
    els.apiKey.addEventListener("input", handleConnectionInputChanged);
    els.rememberConnection.addEventListener("change", handleRememberConnectionChanged);

    [
      els.exportSkipImages,
      els.importSkipImages,
      els.exportIncludePeopleImages,
      els.importIncludePeopleImages,
    ].forEach((input) => input.addEventListener("change", updateControls));
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
      setAppVersion(readFirst(data, ["toolVersion", "ToolVersion", "version", "Version"]));
      applyAuthState(data.warning || "");
    } catch (error) {
      state.authEnabled = true;
      state.authenticated = false;
      applyAuthState(error.message || "无法读取登录状态。");
    } finally {
      updateControls();
    }
  }

  async function handleLogin(event) {
    event.preventDefault();
    const password = els.authPassword.value;
    if (!password) {
      setNotice(els.authNotice, "请输入访问密码。", "error");
      return;
    }
    setButtonBusy("authLogin", els.authLoginBtn, true, "登录中");
    try {
      await postJson("/api/auth/login", { password });
      els.authPassword.value = "";
      state.authenticated = true;
      applyAuthState("");
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
    state.connected = false;
    closeLogStream();
    stopPolling();
    applyAuthState("");
    updateControls();
  }

  function applyAuthState(warning) {
    const locked = state.authEnabled && !state.authenticated;
    els.authGate.classList.toggle("is-hidden", !locked);
    els.appShell.classList.toggle("is-locked", locked);
    els.logoutBtn.classList.toggle("is-hidden", !state.authEnabled || locked);
    els.authWarning.classList.toggle("is-hidden", !warning || locked);
    els.authWarning.textContent = warning || "";
    if (locked) {
      setNotice(els.authNotice, "请输入访问密码。");
    }
  }

  function restoreConnection() {
    els.rememberConnection.checked = true;
    const stored = readStoredConnection();
    if (stored.serverUrl) {
      els.serverUrl.value = stored.serverUrl;
    }
    if (stored.apiKey) {
      els.apiKey.value = stored.apiKey;
    }
  }

  function readStoredConnection() {
    try {
      const raw = window.localStorage.getItem(CONNECTION_STORAGE_KEY);
      if (raw) {
        const parsed = JSON.parse(raw);
        return {
          serverUrl: String(parsed.serverUrl || ""),
          apiKey: String(parsed.apiKey || ""),
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
      apiKey: connection.apiKey,
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
      if (connection.serverUrl && connection.apiKey) {
        persistConnection(connection);
      }
      return;
    }
    clearStoredConnection();
  }

  function handleConnectionInputChanged() {
    if (!state.connected && !state.hasTested) {
      return;
    }

    state.connected = false;
    setConnectionState("pending", "需重新测试");
    setNotice(els.connectionNotice, "连接信息已变更，请重新测试。");
    updateControls();
  }

  async function handleConnectionTest(event) {
    event.preventDefault();

    const connection = getConnection();
    if (!connection.serverUrl || !connection.apiKey) {
      setNotice(els.connectionNotice, "请填写 Emby 地址和 API Key。", "error");
      return;
    }

    setButtonBusy("testConnection", els.testConnectionBtn, true, "测试中");
    setConnectionState("pending", "测试中");
    state.hasTested = true;

    try {
      const data = await postJson("/api/connection/test", connection);
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
    if (!state.connected) {
      setNotice(els.connectionNotice, "请先通过连接测试。", "error");
      return;
    }

    setButtonBusy("loadLibraries", els.loadLibrariesBtn, true, "读取中");
    setButtonBusy("refreshLibraries", els.refreshLibrariesBtn, true, "刷新中");

    try {
      const data = await postJson("/api/libraries", getConnection());
      state.libraries = normalizeLibraries(data);
      state.selectedLibraryIds = new Set(state.libraries.map((library) => library.id));
      renderLibraries();
      appendSystemLog(`读取媒体库完成：${state.libraries.length} 个。`);
    } catch (error) {
      state.libraries = [];
      state.selectedLibraryIds.clear();
      renderLibraries();
      setNotice(els.connectionNotice, `读取媒体库失败：${error.message}`, "error");
      appendSystemLog(`读取媒体库失败：${error.message}`);
    } finally {
      setButtonBusy("loadLibraries", els.loadLibrariesBtn, false);
      setButtonBusy("refreshLibraries", els.refreshLibrariesBtn, false);
      updateControls();
    }
  }

  async function handleRefreshExports() {
    setButtonBusy("refreshExports", els.refreshExportsBtn, true, "刷新中");

    try {
      const data = await fetchJson("/api/exports");
      state.exports = normalizeExports(data);
      state.reports = [];
      renderExports();
      renderReports();
      const count = state.exports.length;
      setNotice(
        els.importNotice,
        count ? `已发现 ${count} 个导出包。` : "未发现导出包，可以手动输入目录名或路径。",
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
    const selected = state.exports.find((item) => item.value === els.exportsSelect.value);
    els.importPath.value = selected ? selected.value : "";
    state.reports = [];
    renderReports();
    updateControls();
  }

  async function handleRefreshReports() {
    const exportPath = els.importPath.value.trim();
    if (!exportPath) {
      setNotice(els.importNotice, "请先选择或输入导出包。", "error");
      return;
    }

    setButtonBusy("refreshReports", els.refreshReportsBtn, true, "刷新中");
    try {
      const data = await fetchJson(`/api/import-reports?exportPath=${encodeURIComponent(exportPath)}`);
      state.reports = normalizeReports(data);
      renderReports();
      setNotice(
        els.importNotice,
        state.reports.length ? `已发现 ${state.reports.length} 个历史报告。` : "该导出包还没有历史报告。",
        state.reports.length ? "ok" : "",
      );
    } catch (error) {
      state.reports = [];
      renderReports();
      setNotice(els.importNotice, `读取历史报告失败：${error.message}`, "error");
      appendSystemLog(`读取历史报告失败：${error.message}`);
    } finally {
      setButtonBusy("refreshReports", els.refreshReportsBtn, false);
      updateControls();
    }
  }

  function handleDownloadReport() {
    const exportPath = els.importPath.value.trim();
    const reportName = els.reportsSelect.value;
    if (!exportPath || !reportName) {
      return;
    }
    const link = document.createElement("a");
    link.href = `/api/import-reports/download?exportPath=${encodeURIComponent(exportPath)}&name=${encodeURIComponent(reportName)}`;
    link.download = reportName;
    document.body.appendChild(link);
    link.click();
    link.remove();
  }

  async function handleStartExport() {
    const libraryIds = Array.from(state.selectedLibraryIds);
    if (!state.connected || libraryIds.length === 0) {
      setNotice(els.connectionNotice, "请先连接并选择至少一个媒体库。", "error");
      return;
    }

    const options = collectExportOptions();
    const connection = getConnection();
    const payload = {
      connection,
      baseUrl: connection.baseUrl,
      serverUrl: connection.serverUrl,
      apiKey: connection.apiKey,
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

  async function startImportLikeJob({ endpoint, buttonKey, button, busyText, dryRun, kind, actionName }) {
    const exportPath = els.importPath.value.trim();
    if (!state.connected || !exportPath) {
      setNotice(els.importNotice, "请先连接目标 Emby，并选择或输入导出包。", "error");
      return;
    }

    const options = collectImportOptions({ dryRun });
    const connection = getConnection();
    const payload = {
      connection,
      baseUrl: connection.baseUrl,
      serverUrl: connection.serverUrl,
      apiKey: connection.apiKey,
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

  function handleSelectAllLibraries() {
    if (els.selectAllLibraries.checked) {
      state.selectedLibraryIds = new Set(state.libraries.map((library) => library.id));
    } else {
      state.selectedLibraryIds.clear();
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
      includePeopleImages: !skipImages && els.importIncludePeopleImages.checked,
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
      const meta = [createdAt, itemCount !== undefined ? `${itemCount} 项` : ""]
        .filter(Boolean)
        .join(" · ");

      return {
        value,
        label,
        meta,
      };
    });
  }

  function normalizeReports(data) {
    const list = (Array.isArray(data) && data) || data.reports || data.Reports || [];
    return list.map((raw, index) => {
      const name = String(readFirst(raw, ["name", "Name", "id", "Id"]) || `report-${index + 1}.json`);
      const summary = safeObject(readFirst(raw, ["summary", "Summary"]));
      return {
        name,
        label: name,
        dryRun: Boolean(readFirst(raw, ["dryRun", "DryRun"])),
        startedAt: readFirst(raw, ["startedAt", "StartedAt", "modifiedAt", "ModifiedAt"]),
        endedAt: readFirst(raw, ["endedAt", "EndedAt"]),
        summary,
      };
    });
  }

  function renderLibraries() {
    els.libraryList.textContent = "";

    if (!state.libraries.length) {
      const empty = document.createElement("div");
      empty.className = "empty-state";
      empty.textContent = "暂无媒体库。请先测试连接并读取列表。";
      els.libraryList.appendChild(empty);
      els.librarySummary.textContent = "未读取到媒体库。";
      updateSelectedCount();
      return;
    }

    state.libraries.forEach((library) => {
      const label = document.createElement("label");
      label.className = "library-item";

      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.value = library.id;
      checkbox.checked = state.selectedLibraryIds.has(library.id);
      checkbox.addEventListener("change", () => {
        if (checkbox.checked) {
          state.selectedLibraryIds.add(library.id);
        } else {
          state.selectedLibraryIds.delete(library.id);
        }
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

      body.append(name, meta);
      label.append(checkbox, body);
      els.libraryList.appendChild(label);
    });

    els.librarySummary.textContent = `已读取 ${state.libraries.length} 个媒体库。`;
    updateSelectedCount();
  }

  function renderExports() {
    els.exportsSelect.textContent = "";
    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = state.exports.length
      ? "选择一个导出包"
      : "手动输入或先刷新导出包";
    els.exportsSelect.appendChild(placeholder);

    state.exports.forEach((item) => {
      const option = document.createElement("option");
      option.value = item.value;
      option.textContent = item.meta ? `${item.label} (${item.meta})` : item.label;
      els.exportsSelect.appendChild(option);
    });
  }

  function renderReports() {
    if (!els.reportsSelect) {
      return;
    }
    els.reportsSelect.textContent = "";
    const placeholder = document.createElement("option");
    placeholder.value = "";
    placeholder.textContent = state.reports.length
      ? "选择一个历史报告"
      : els.importPath.value.trim()
        ? "该导出包暂无历史报告"
        : "选择导出包后可查看历史报告";
    els.reportsSelect.appendChild(placeholder);

    state.reports.forEach((report) => {
      const option = document.createElement("option");
      option.value = report.name;
      option.textContent = formatReportOption(report);
      els.reportsSelect.appendChild(option);
    });
  }

  function formatReportOption(report) {
    const summary = safeObject(report.summary);
    const mode = report.dryRun ? "预检" : "导入";
    const time = formatShortTime(report.endedAt || report.startedAt);
    const total = numberValue(summary.items) || numberValue(summary.matched) + numberValue(summary.unmatched) + numberValue(summary.ambiguous) + numberValue(summary.errors);
    const risks = [
      numberValue(summary.unmatched) ? `未匹配 ${numberValue(summary.unmatched)}` : "",
      numberValue(summary.ambiguous) ? `歧义 ${numberValue(summary.ambiguous)}` : "",
      numberValue(summary.errors) ? `错误 ${numberValue(summary.errors)}` : "",
    ].filter(Boolean);
    return [mode, time, total ? `${total} 项` : "", risks.join(" / ") || "无异常", report.name]
      .filter(Boolean)
      .join(" · ");
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
      checkbox.addEventListener("change", updateControls);

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

  function updateSelectedCount() {
    const selected = state.selectedLibraryIds.size;
    const total = state.libraries.length;
    els.selectedCount.textContent = `已选 ${selected} 个`;
    els.selectAllLibraries.checked = total > 0 && selected === total;
    els.selectAllLibraries.indeterminate = selected > 0 && selected < total;
  }

  function updateControls() {
    const locked = state.authEnabled && !state.authenticated;
    const hasLibraries = state.libraries.length > 0;
    const hasSelectedLibraries = state.selectedLibraryIds.size > 0;
    const hasImportPath = Boolean(els.importPath.value.trim());
    const hasSelectedReport = Boolean(els.reportsSelect && els.reportsSelect.value);
    const hasActiveJob = Boolean(state.currentJobId) && !TERMINAL_STATES.has(state.currentJobStatus);

    els.testConnectionBtn.disabled = locked || state.busy.has("testConnection");
    els.loadLibrariesBtn.disabled = locked || !state.connected || state.busy.has("loadLibraries");
    els.refreshLibrariesBtn.disabled =
      locked || !state.connected || state.busy.has("refreshLibraries");
    els.refreshExportsBtn.disabled = locked || state.busy.has("refreshExports");
    els.refreshReportsBtn.disabled =
      locked || !hasImportPath || state.busy.has("refreshReports");
    els.reportsSelect.disabled = locked || state.reports.length === 0;
    els.downloadReportBtn.disabled = locked || !hasSelectedReport;
    els.selectAllLibraries.disabled = locked || !hasLibraries;
    els.startExportBtn.disabled =
      locked || !state.connected || !hasSelectedLibraries || state.busy.has("startExport");
    els.refreshJobBtn.disabled = locked || !state.currentJobId || state.busy.has("refreshJob");
    els.stopJobBtn.disabled = locked || !hasActiveJob || state.busy.has("stopJob");
    els.downloadLogsBtn.disabled = locked || !state.currentJobId;
    els.startPrecheckBtn.disabled =
      locked || !state.connected || !hasImportPath || state.busy.has("startPrecheck");
    els.startImportBtn.disabled =
      locked || !state.connected || !hasImportPath || state.busy.has("startImport");

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
    if (data && typeof data === "object") {
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
    const jobId = readJobId(data) || state.currentJobId || "-";
    const kind =
      readFirst(data, ["kind", "type", "jobType", "Kind", "Type", "JobType"]) ||
      fallbackKind ||
      "-";
    const status =
      readFirst(data, ["status", "state", "Status", "State"]) ||
      readFirst(data, ["phase", "Phase"]) ||
      "已创建";

    els.jobKind.textContent = kind;
    els.jobState.textContent = status;
    els.jobId.textContent = jobId;
    els.jobProgress.textContent = formatProgress(data);
    state.currentJobStatus = normalizeStatus(status);
    renderImportReport(data, kind);
    updateControls();
  }

  function renderImportReport(data, kind) {
    if (!els.importReport) {
      return;
    }
    const report = readImportReport(data);
    if (!report) {
      if (isImportKind(kind)) {
        els.importReport.classList.add("is-hidden");
      }
      return;
    }

    const summary = safeObject(readFirst(report, ["summary", "Summary"]));
    const matches = Array.isArray(report.matches) ? report.matches : [];
    const dryRun = Boolean(readFirst(report, ["dryRun", "DryRun"]));
    const total = matches.length || numberValue(summary.items);
    const matched = numberValue(summary.matched);
    const unmatched = numberValue(summary.unmatched);
    const ambiguous = numberValue(summary.ambiguous);
    const errors = numberValue(summary.errors);
    const metadataUpdated = numberValue(summary.metadataUpdated);
    const itemImagesPushed = numberValue(summary.itemImagesPushed);
    const itemImagesFailed = numberValue(summary.itemImagesFailed);
    const peopleImages = numberValue(summary.peopleImages);
    const peopleImagesFailed = numberValue(summary.peopleImagesFailed);
    const hasRisk = unmatched > 0 || ambiguous > 0 || errors > 0 || itemImagesFailed > 0 || peopleImagesFailed > 0;

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
    els.importReport.appendChild(heading);

    const stats = document.createElement("div");
    stats.className = "report-stats";
    addReportStat(stats, "项目", total);
    addReportStat(stats, dryRun ? "匹配" : "元数据成功", dryRun ? matched : metadataUpdated);
    addReportStat(stats, "未匹配", unmatched);
    addReportStat(stats, "歧义", ambiguous);
    addReportStat(stats, "错误", errors);
    if (!dryRun) {
      addReportStat(stats, "媒体图片", `${itemImagesPushed}/${itemImagesFailed}`);
      addReportStat(stats, "人物头像", `${peopleImages}/${peopleImagesFailed}`);
    }
    els.importReport.appendChild(stats);

    const samples = reportProblemSamples(matches);
    if (samples.length) {
      const list = document.createElement("div");
      list.className = "report-samples";
      const label = document.createElement("span");
      label.className = "label";
      label.textContent = "需复查示例";
      list.appendChild(label);
      samples.forEach((item) => {
        const row = document.createElement("div");
        row.className = "report-sample";
        const name = document.createElement("strong");
        name.textContent = item.sourceName || item.stableKey || "未知项目";
        const detail = document.createElement("span");
        detail.textContent = [item.status, item.reason || item.error, formatCandidates(item.candidates)]
          .filter(Boolean)
          .join(" · ");
        row.append(name, detail);
        list.appendChild(row);
      });
      els.importReport.appendChild(list);
    }
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

  function reportProblemSamples(matches) {
    return matches
      .filter((item) => {
        const status = String(item.status || "").toLowerCase();
        return status && status !== "matched" && status !== "updated";
      })
      .slice(0, 6);
  }

  function formatCandidates(candidates) {
    if (!Array.isArray(candidates) || candidates.length === 0) {
      return "";
    }
    const preview = candidates.slice(0, 3).join("、");
    return candidates.length > 3 ? `候选：${preview} 等 ${candidates.length} 个` : `候选：${preview}`;
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
    if (!apiKey || apiKey.length < 4) {
      return text;
    }
    return text.split(apiKey).join(maskKey(apiKey));
  }

  function maskKey(apiKey) {
    if (apiKey.length <= 8) {
      return "****";
    }
    return `${apiKey.slice(0, 3)}****${apiKey.slice(-3)}`;
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
      readFirst(data, ["status", "state", "Status", "State"]),
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
    return `${options.imageTypes.join(", ")}；${people}；并发 ${options.concurrency}`;
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
