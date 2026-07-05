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
		`./assets/styles.css?v=0.1.0-beta.4`,
		`./assets/app.js?v=0.1.0-beta.4`,
		"服务器地址簿",
		"保存服务器",
		"导出图片类型",
		"导入图片类型",
		"默认全选，可直接调整",
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

	for _, marker := range []string{
		"const DEFAULT_IMAGE_TYPES = Object.freeze([...IMAGE_TYPES]);",
		"checkbox.checked = true;",
		"const TASK_PREFS_SCHEMA_VERSION = 2;",
		"restoreImageTypes: false",
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
