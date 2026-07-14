package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrontendUITelegramRegressionMarkers(t *testing.T) {
	index := readFrontendFile(t, "index.html")
	styles := readFrontendFile(t, "assets", "styles.css")
	app := readFrontendFile(t, "assets", "app.js")

	for _, marker := range []string{
		`./assets/styles.css?v=1.1.1`,
		`./assets/app.js?v=1.1.1`,
		"https://github.com/czppw/emby-migrator",
		"https://hub.docker.com/r/czppwa/emby-migrator",
		"License: AGPL-3.0-or-later",
		"服务器地址簿",
		"保存服务器",
		"导出图片类型",
		"导入图片类型",
		"默认全选，可直接调整",
		`id="exportIncludeMediaInfo" checked`,
		`id="importMediaInfo" checked`,
		"保存 MediaSources、MediaStreams 和 Chapters。",
		"失败会自动降级且不影响普通元数据",
		`id="embyDatabasePath"`,
		`id="refreshEmbyDatabasesBtn"`,
		`id="embyContainerName"`,
		`id="autoManageContainer"`,
		"自动停启",
		"从旧设备迁移导出包",
		"/opt/emby-migrator/data/imports/",
		`id="versionUpdate"`,
	} {
		if !strings.Contains(index, marker) {
			t.Fatalf("index.html missing regression marker %q", marker)
		}
	}

	for _, forbidden := range []string{"档案名称", "保存为档案"} {
		if strings.Contains(index, forbidden) || strings.Contains(app, forbidden) {
			t.Fatalf("server address book UI still contains forbidden marker %q", forbidden)
		}
	}
	for _, forbidden := range []string{"ensureMediaInfoOptions", "ensureMediaInfoOption("} {
		if strings.Contains(app, forbidden) {
			t.Fatalf("media info controls must be static HTML; app.js contains %q", forbidden)
		}
	}

	for _, marker := range []string{
		"const DEFAULT_IMAGE_TYPES = Object.freeze([...IMAGE_TYPES]);",
		"checkbox.checked = true;",
		"const TASK_PREFS_SCHEMA_VERSION = 2;",
		"restoreImageTypes: false",
		"includeMediaInfo: Boolean(els.exportIncludeMediaInfo?.checked)",
		"importMediaInfo: Boolean(els.importMediaInfo?.checked)",
		`/api/emby-databases?profileId=${encodeURIComponent(profileId)}`,
		"targetProfileId: selectedTargetProfileId",
		"autoManageContainer: Boolean(els.autoManageContainer?.checked)",
		"handleRefreshExports();",
		`fetchJson("/api/version")`,
	} {
		if !strings.Contains(app, marker) {
			t.Fatalf("app.js missing image-type default marker %q", marker)
		}
	}

	for _, marker := range []string{
		".task-layout",
		"min-width: 0;",
		"grid-template-columns: minmax(0, 1.7fr) minmax(132px, 0.7fr);",
		"grid-template-columns: repeat(auto-fit, minmax(min(100%, 118px), 1fr));",
		"overflow: visible;",
		".app-footer",
		".database-picker",
		".import-package-copy-note",
		".version-update",
		".switch-control input:checked + .switch-track",
	} {
		if !strings.Contains(styles, marker) {
			t.Fatalf("styles.css missing layout guard marker %q", marker)
		}
	}
}

func readFrontendFile(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "web"}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
