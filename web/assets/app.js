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
    currentJobId: "",
    currentJobKind: "",
    currentJobStatus: "",
    eventSource: null,
    sseErrorReported: false,
    pollTimer: 0,
  };

  const els = {};

  document.addEventListener("DOMContentLoaded", init);

  function init() {
    cacheElements();
    initTheme();
    renderImageTypes("exportImageTypes", "export");
    renderImageTypes("importImageTypes", "import");
    restoreServerUrl();
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
      "importDryRun",
      "importSkipImages",
      "importOverwrite",
      "importIncludePeopleImages",
      "importConcurrency",
      "startImportBtn",
      "importNotice",
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
    els.startImportBtn.addEventListener("click", handleStartImport);
    els.downloadLogsBtn.addEventListener("click", handleDownloadLogs);
    els.copyLogsBtn.addEventListener("click", handleCopyLogs);
    els.clearLogsBtn.addEventListener("click", () => {
      els.logWindow.textContent = "";
    });

    els.serverUrl.addEventListener("input", handleConnectionInputChanged);
    els.apiKey.addEventListener("input", handleConnectionInputChanged);

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

  function restoreServerUrl() {
    const storedUrl = window.localStorage.getItem("embyMigrator.serverUrl");
    if (storedUrl) {
      els.serverUrl.value = storedUrl;
    }
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
      window.localStorage.setItem("embyMigrator.serverUrl", connection.serverUrl);
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
      renderExports();
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
    updateControls();
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

  async function handleStartImport() {
    const exportPath = els.importPath.value.trim();
    if (!state.connected || !exportPath) {
      setNotice(els.importNotice, "请先连接目标 Emby，并选择或输入导出包。", "error");
      return;
    }

    const options = collectImportOptions();
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

    setButtonBusy("startImport", els.startImportBtn, true, "创建中");

    try {
      const data = await postJson("/api/jobs/import", payload);
      const jobId = readJobId(data);
      if (!jobId) {
        throw new Error("后端未返回任务 ID。");
      }

      beginJob(jobId, options.dryRun ? "导入 dry-run" : "导入", data);
      setNotice(
        els.importNotice,
        `${options.dryRun ? "导入 dry-run" : "导入"}任务已创建：${jobId}`,
        "ok",
      );
      appendSystemLog(
        `${options.dryRun ? "导入 dry-run" : "导入"}任务已创建：${jobId}。导出包 ${exportPath}，图片类型 ${formatImageOptions(options)}。`,
      );
    } catch (error) {
      setNotice(els.importNotice, `创建导入任务失败：${error.message}`, "error");
      appendSystemLog(`创建导入任务失败：${error.message}`);
    } finally {
      setButtonBusy("startImport", els.startImportBtn, false);
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

  function collectImportOptions() {
    const skipImages = els.importSkipImages.checked;
    return {
      dryRun: els.importDryRun.checked,
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
    const hasActiveJob = Boolean(state.currentJobId) && !TERMINAL_STATES.has(state.currentJobStatus);

    els.testConnectionBtn.disabled = locked || state.busy.has("testConnection");
    els.loadLibrariesBtn.disabled = locked || !state.connected || state.busy.has("loadLibraries");
    els.refreshLibrariesBtn.disabled =
      locked || !state.connected || state.busy.has("refreshLibraries");
    els.refreshExportsBtn.disabled = locked || state.busy.has("refreshExports");
    els.selectAllLibraries.disabled = locked || !hasLibraries;
    els.startExportBtn.disabled =
      locked || !state.connected || !hasSelectedLibraries || state.busy.has("startExport");
    els.refreshJobBtn.disabled = locked || !state.currentJobId || state.busy.has("refreshJob");
    els.stopJobBtn.disabled = locked || !hasActiveJob || state.busy.has("stopJob");
    els.downloadLogsBtn.disabled = locked || !state.currentJobId;
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
      handleLogEvent(event.data);
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
    updateControls();
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
    element.classList.remove("ok", "error");
    if (type) {
      element.classList.add(type);
    }
    element.textContent = message;
  }

  function appendSystemLog(message) {
    appendLog(`${formatBeijingTime(new Date().toISOString())} [system] ${message}`);
  }

  function appendLog(message) {
    const sanitized = sanitizeLogText(String(message || ""));
    const line = sanitized.endsWith("\n") ? sanitized : `${sanitized}\n`;
    els.logWindow.textContent += line;
    trimLogWindow();
    els.logWindow.scrollTop = els.logWindow.scrollHeight;
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
