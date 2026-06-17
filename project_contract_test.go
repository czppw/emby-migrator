package embymigrator_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type releaseBaseline struct {
	SchemaVersion           int `json:"schemaVersion"`
	CurrentAcceptedBaseline struct {
		GitCommit            string   `json:"gitCommit"`
		DockerImage          string   `json:"dockerImage"`
		Status               string   `json:"status"`
		Scope                []string `json:"scope"`
		KnownRollbackCommand string   `json:"knownRollbackCommand"`
	} `json:"currentAcceptedBaseline"`
	Documentation map[string]string `json:"documentation"`
}

func TestProjectGovernanceDocsAndBaseline(t *testing.T) {
	baseline := readReleaseBaseline(t)
	if baseline.SchemaVersion != 1 {
		t.Fatalf("release baseline schemaVersion = %d, want 1", baseline.SchemaVersion)
	}
	current := baseline.CurrentAcceptedBaseline
	if current.GitCommit != "8c42aed2ee34524165c9c69f2cbee5832de38c96" {
		t.Fatalf("unexpected accepted baseline commit: %q", current.GitCommit)
	}
	if current.DockerImage != "czppwa/emby-migrator:sha-8c42aed" {
		t.Fatalf("unexpected rollback image: %q", current.DockerImage)
	}
	if current.Status != "accepted-rollback-baseline" {
		t.Fatalf("unexpected baseline status: %q", current.Status)
	}
	if !strings.Contains(current.KnownRollbackCommand, current.DockerImage) {
		t.Fatalf("rollback command %q does not reference %q", current.KnownRollbackCommand, current.DockerImage)
	}
	if len(current.Scope) == 0 {
		t.Fatalf("baseline scope must describe what was accepted")
	}

	requiredDocs := map[string][]string{
		"docs/PROJECT_BLUEPRINT.md": {
			"目标模式",
			"Emby 4.8.11",
			"Emby 4.9.5",
			current.DockerImage,
			current.GitCommit,
			"docs/REMOTE_VERSION_TESTING.md",
			"docs/ROLLBACK.md",
		},
		"docs/DEVELOPMENT_WORKFLOW.md": {
			"docs/release-baseline.json",
			"docs/templates/REMOTE_TEST_REPORT_TEMPLATE.md",
			"远端实测",
		},
		"docs/REMOTE_VERSION_TESTING.md": {
			"Emby 4.8.11",
			"Emby 4.9.5",
			"日韩剧集",
			"日韩电影",
			"docs/templates/REMOTE_TEST_REPORT_TEMPLATE.md",
		},
		"docs/ROLLBACK.md": {
			current.DockerImage,
			current.GitCommit,
			"docs/release-baseline.json",
		},
		"docs/templates/REMOTE_TEST_REPORT_TEMPLATE.md": {
			"Git commit",
			"Docker 镜像标签",
			"源 Emby 版本",
			"目标 Emby 版本",
			"回滚基线",
		},
	}
	for path, markers := range requiredDocs {
		text := readText(t, path)
		for _, marker := range markers {
			if !strings.Contains(text, marker) {
				t.Fatalf("%s does not contain required marker %q", path, marker)
			}
		}
	}

	for label, path := range baseline.Documentation {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("baseline documentation %s path %s is not readable: %v", label, path, err)
		}
	}
}

func TestDockerWorkflowSkipsDocsOnlyChanges(t *testing.T) {
	workflow := readText(t, ".github/workflows/dockerhub.yml")
	for _, marker := range []string{
		"paths-ignore:",
		`"docs/**"`,
		`"*.md"`,
		"workflow_dispatch:",
		"type=raw,value=latest",
		"type=sha,prefix=sha-",
	} {
		if !strings.Contains(workflow, marker) {
			t.Fatalf("docker workflow missing marker %q", marker)
		}
	}
}

func TestGovernanceDocsDoNotExposeKnownSecrets(t *testing.T) {
	files := []string{
		"docs/PROJECT_BLUEPRINT.md",
		"docs/DEVELOPMENT_WORKFLOW.md",
		"docs/REMOTE_VERSION_TESTING.md",
		"docs/ROLLBACK.md",
		"docs/release-baseline.json",
		"docs/templates/REMOTE_TEST_REPORT_TEMPLATE.md",
	}
	for _, file := range files {
		text := readText(t, file)
		for _, forbidden := range []string{
			"81.71.",
			"xcc.pem",
			"root@",
			"dckr_pat_",
			"407ee8",
			"F:/",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden sensitive marker %q", file, forbidden)
			}
		}
	}
}

func readReleaseBaseline(t *testing.T) releaseBaseline {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash("docs/release-baseline.json"))
	if err != nil {
		t.Fatalf("read release baseline: %v", err)
	}
	var baseline releaseBaseline
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatalf("parse release baseline: %v", err)
	}
	return baseline
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
