package embymigrator_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type releaseBaseline struct {
	SchemaVersion           int `json:"schemaVersion"`
	CurrentAcceptedBaseline struct {
		GitCommit            string   `json:"gitCommit"`
		DockerImage          string   `json:"dockerImage"`
		Status               string   `json:"status"`
		AcceptedAt           string   `json:"acceptedAt"`
		RemoteTestReport     string   `json:"remoteTestReport"`
		VerifiedEmbyVersions []string `json:"verifiedEmbyVersions"`
		RemoteTestLibraries  []string `json:"remoteTestLibraries"`
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
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(current.GitCommit) {
		t.Fatalf("accepted baseline commit must be a full git SHA, got %q", current.GitCommit)
	}
	wantTag := "czppwa/emby-migrator:sha-" + current.GitCommit[:7]
	if current.DockerImage != wantTag {
		t.Fatalf("rollback image = %q, want %q derived from commit", current.DockerImage, wantTag)
	}
	if current.Status != "accepted-rollback-baseline" {
		t.Fatalf("unexpected baseline status: %q", current.Status)
	}
	if strings.TrimSpace(current.AcceptedAt) == "" {
		t.Fatalf("acceptedAt must be recorded")
	}
	if current.RemoteTestReport == "" {
		t.Fatalf("remoteTestReport must be recorded")
	}
	if !strings.Contains(current.KnownRollbackCommand, current.DockerImage) {
		t.Fatalf("rollback command %q does not reference %q", current.KnownRollbackCommand, current.DockerImage)
	}
	if len(current.Scope) == 0 {
		t.Fatalf("baseline scope must describe what was accepted")
	}
	for _, version := range []string{"4.8.11", "4.9.5"} {
		if !containsString(current.VerifiedEmbyVersions, version) {
			t.Fatalf("verifiedEmbyVersions must include %s, got %#v", version, current.VerifiedEmbyVersions)
		}
	}
	for _, library := range []string{"日韩剧集", "日韩电影"} {
		if !containsString(current.RemoteTestLibraries, library) {
			t.Fatalf("remoteTestLibraries must include %s, got %#v", library, current.RemoteTestLibraries)
		}
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
			"目标 Emby",
			"备份",
		},
		"docs/ROLLBACK.md": {
			current.DockerImage,
			current.GitCommit,
			"docs/release-baseline.json",
			"回滚 migrator 容器无法撤销",
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
		`"*_test.go"`,
		`"**/*_test.go"`,
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
		".github/workflows/dockerhub.yml",
	}
	forbiddenPatterns := []*regexp.Regexp{
		regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`),
		regexp.MustCompile(`(?i)\b(?:root|admin|administrator)@(?:\d{1,3}(?:\.\d{1,3}){3}|[a-z0-9.-]+)\b`),
		regexp.MustCompile(`(?i)\b[a-z]:/`),
		regexp.MustCompile(`(?i)\.pem\b`),
		regexp.MustCompile(`dckr_pat_[A-Za-z0-9_-]+`),
		regexp.MustCompile(`[0-9a-f]{32,}`),
	}
	for _, file := range files {
		text := readText(t, file)
		for _, pattern := range forbiddenPatterns {
			for _, match := range pattern.FindAllString(text, -1) {
				if allowedGovernanceMatch(match) {
					continue
				}
				t.Fatalf("%s contains forbidden sensitive-looking marker %q matched by %s", file, match, pattern.String())
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

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func allowedGovernanceMatch(match string) bool {
	allowed := map[string]bool{
		"8c42aed2ee34524165c9c69f2cbee5832de38c96": true,
	}
	return allowed[match]
}
