package exporter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"emby-migrator/internal/emby"
	"emby-migrator/internal/job"
	"emby-migrator/internal/storage"
)

func TestItemDirectoryBasePrefersMediaFileStemAndKeepsStableKeySeparate(t *testing.T) {
	item := emby.Item{
		ID:          "old-internal-id",
		Type:        "Movie",
		Name:        "The Matrix",
		Path:        `D:\Movies\The.Matrix.1999.mkv`,
		ProviderIDs: map[string]string{"Imdb": "tt0133093"},
	}

	got := itemDirectoryBase(item)
	if got != "The.Matrix.1999" {
		t.Fatalf("itemDirectoryBase returned %q, want media file stem", got)
	}
	if key := storage.StableItemKey(item); key == got {
		t.Fatalf("directory name should be human-readable and separate from stable key %q", key)
	}
}

func TestExactNameMatchesTreatsPortablePunctuationAsEquivalent(t *testing.T) {
	items := []emby.Item{
		{ID: "target", Type: "Movie", Name: "Big Buck Bunny", ProductionYear: 2008},
		{ID: "wrong-type", Type: "Series", Name: "Big Buck Bunny", ProductionYear: 2008},
	}

	matches := exactNameMatches(items, "Big.Buck.Bunny", "Movie")
	if len(matches) != 1 || matches[0].ID != "target" {
		t.Fatalf("exactNameMatches = %#v, want the punctuation-normalized movie", matches)
	}
}

func TestExactNameMatchesPrefersStrictNameBeforePortableVariants(t *testing.T) {
	items := []emby.Item{
		{ID: "strict", Type: "Movie", Name: "Big.Buck.Bunny"},
		{ID: "portable", Type: "Movie", Name: "Big Buck Bunny"},
	}

	matches := exactNameMatches(items, "Big.Buck.Bunny", "Movie")
	if len(matches) != 1 || matches[0].ID != "strict" {
		t.Fatalf("exactNameMatches = %#v, want only the strict match", matches)
	}
}

func TestPortableNameMatchesRemainAmbiguousWithoutUniqueYear(t *testing.T) {
	items := []emby.Item{
		{ID: "old", Type: "Movie", Name: "Big Buck Bunny", ProductionYear: 2008},
		{ID: "new", Type: "Movie", Name: "Big-Buck-Bunny", ProductionYear: 2024},
	}

	matches := exactNameMatches(items, "Big.Buck.Bunny", "Movie")
	if len(matches) != 2 {
		t.Fatalf("exactNameMatches = %#v, want both normalized candidates", matches)
	}
	selected, narrowed, ok := chooseUniqueMatch(matches, 2008)
	if !ok || selected.ID != "old" || len(narrowed) != 1 {
		t.Fatalf("chooseUniqueMatch = (%#v, %#v, %v), want the unique 2008 item", selected, narrowed, ok)
	}
}

func TestSeasonStableKeyIncludesParentSeriesPath(t *testing.T) {
	first := storage.StableItemKey(emby.Item{
		Type: "Season",
		Name: "第 1 季",
		Path: `/media/请回答1988 (2015) {tmdb-64010}/Season 1`,
	})
	second := storage.StableItemKey(emby.Item{
		Type: "Season",
		Name: "第 1 季",
		Path: `/media/铁拳教育 (2026) {tmdb-276161}/Season 1`,
	})
	if first == second {
		t.Fatalf("different series season stable keys should not collide: %q", first)
	}
}

func TestExportDirectoryNameIncludesTimeServerAndLibrary(t *testing.T) {
	name := exportDirectoryName(
		time.Date(2026, 6, 16, 12, 30, 5, 0, time.Local),
		"影视库",
		[]emby.Library{{Name: "日韩电影"}},
	)
	if name != "20260616-123005-影视库-日韩电影" {
		t.Fatalf("exportDirectoryName returned %q", name)
	}
}

func TestResolveExportPathRejectsTraversal(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "pkg")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest.json"), storage.Manifest{}); err != nil {
		t.Fatal(err)
	}
	resolved, name, err := service.ResolveExportPath("pkg")
	if err != nil {
		t.Fatalf("ResolveExportPath valid package returned error: %v", err)
	}
	if name != "pkg" || resolved != exportDir {
		t.Fatalf("ResolveExportPath = (%q, %q), want (%q, pkg)", resolved, name, exportDir)
	}
	for _, input := range []string{"..", "../pkg", "..\\pkg", filepath.Join(dataDir, "outside")} {
		if _, _, err := service.ResolveExportPath(input); err == nil || !strings.Contains(err.Error(), "allowed package directories") {
			t.Fatalf("ResolveExportPath(%q) error = %v, want allowed package directories rejection", input, err)
		}
	}
}

func TestResolveExportPathAllowsConfiguredImportRoots(t *testing.T) {
	dataDir := t.TempDir()
	externalRoot := t.TempDir()
	service := NewService(dataDir, externalRoot)
	for _, packagePath := range []string{
		filepath.Join(dataDir, "imports", "copied-package"),
		filepath.Join(externalRoot, "mounted-package"),
	} {
		if err := os.MkdirAll(packagePath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := storage.WriteJSON(filepath.Join(packagePath, "manifest.json"), storage.Manifest{}); err != nil {
			t.Fatal(err)
		}
		resolved, _, err := service.ResolveExportPath(packagePath)
		if err != nil {
			t.Fatalf("ResolveExportPath(%q) returned error: %v", packagePath, err)
		}
		if resolved != packagePath {
			t.Fatalf("ResolveExportPath(%q) = %q", packagePath, resolved)
		}
	}

	outside := filepath.Join(t.TempDir(), "outside-package")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(outside, "manifest.json"), storage.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ResolveExportPath(outside); err == nil || !strings.Contains(err.Error(), "allowed package directories") {
		t.Fatalf("outside package error = %v", err)
	}
	if _, _, err := service.ResolveExportPath(filepath.Join("..", "imports", "copied-package")); err == nil {
		t.Fatal("relative traversal into imports directory must be rejected")
	}
}

func TestListImportPackagesIncludesLocalAndCopiedPackages(t *testing.T) {
	dataDir := t.TempDir()
	externalRoot := t.TempDir()
	service := NewService(dataDir, externalRoot)
	testPackages := []struct {
		path    string
		name    string
		source  string
		items   int
		version string
	}{
		{path: filepath.Join(service.ExportsDir(), "local"), name: "local", source: "本机导出", items: 1, version: "4.8.11.0"},
		{path: filepath.Join(dataDir, "imports", "copied"), name: "copied", source: "外部迁移包", items: 2, version: "4.9.5.0"},
		{path: filepath.Join(externalRoot, "mounted"), name: "mounted", source: "外部迁移包", items: 3, version: "4.9.5.0"},
	}
	for index, testPackage := range testPackages {
		if err := os.MkdirAll(testPackage.path, 0o755); err != nil {
			t.Fatal(err)
		}
		manifest := storage.Manifest{
			ExportedAt:  time.Date(2026, 7, 14, 10+index, 0, 0, 0, time.UTC),
			EmbyVersion: testPackage.version,
			Items:       make([]storage.ItemEntry, testPackage.items),
		}
		if err := storage.WriteJSON(filepath.Join(testPackage.path, "manifest.json"), manifest); err != nil {
			t.Fatal(err)
		}
	}

	packages, err := service.ListImportPackages()
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != len(testPackages) {
		t.Fatalf("ListImportPackages returned %d packages: %#v", len(packages), packages)
	}
	byName := make(map[string]ImportPackageInfo, len(packages))
	for _, packageInfo := range packages {
		byName[packageInfo.Name] = packageInfo
	}
	for _, want := range testPackages {
		got, ok := byName[want.name]
		if !ok || got.Source != want.source || got.ItemCount != want.items || got.EmbyVersion != want.version {
			t.Fatalf("package %s = %#v", want.name, got)
		}
	}
	if byName["local"].Path != "local" {
		t.Fatalf("local package path = %q", byName["local"].Path)
	}
	if !filepath.IsAbs(filepath.FromSlash(byName["copied"].Path)) || !filepath.IsAbs(filepath.FromSlash(byName["mounted"].Path)) {
		t.Fatalf("copied package paths must be absolute: %#v", byName)
	}
}

func TestListImportReportsReturnsSummaryAndIgnoresOtherFiles(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "pkg")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest.json"), storage.Manifest{}); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC)
	if err := storage.WriteJSON(filepath.Join(exportDir, "import-report-20260617-100000.json"), ImportReport{
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(time.Minute),
		DryRun:    true,
		Summary: storage.Summary{
			Matched:   2,
			Ambiguous: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest-copy.json"), map[string]any{"ignored": true}); err != nil {
		t.Fatal(err)
	}
	reports, err := service.ListImportReports("pkg")
	if err != nil {
		t.Fatalf("ListImportReports returned error: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("ListImportReports returned %#v, want one report", reports)
	}
	report := reports[0]
	if report.Name != "import-report-20260617-100000.json" || !report.DryRun || report.Summary.Matched != 2 || report.Summary.Ambiguous != 1 {
		t.Fatalf("unexpected report info: %#v", report)
	}
	if filepath.IsAbs(report.Path) {
		t.Fatalf("report list should not expose absolute path: %q", report.Path)
	}
}

func TestResumeSuccessfulItemsReadsLatestSuccessfulReport(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "pkg")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "import-report-20260617-100000.json"), ImportReport{
		DryRun: true,
		Matches: []ImportMatch{
			{StableKey: "dry", Status: "matched"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "import-report-20260617-110000.json"), ImportReport{
		Matches: []ImportMatch{
			{StableKey: "done", Status: "updated"},
			{StableKey: "bad", Status: "failed"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	done, reportName := service.resumeSuccessfulItems(exportDir, ImportTarget{})
	if reportName != "import-report-20260617-110000.json" {
		t.Fatalf("reportName = %q", reportName)
	}
	if !done["done"] || done["bad"] || done["dry"] {
		t.Fatalf("resume map = %#v", done)
	}
}

func TestResumeSuccessfulItemsMergesCheckpointAndReportsForSameTarget(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "pkg")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := ImportTarget{ServerID: "target-a", ServerName: "Target A", Version: "4.9.5"}
	checkpoint := newImportCheckpointStore(exportDir, target)
	if err := checkpoint.Record(ImportMatch{StableKey: "checkpoint", SourceName: "Checkpoint", TargetID: "item-1", Status: "updated"}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "import-report-20260617-100000.json"), ImportReport{
		Target: target,
		Matches: []ImportMatch{
			{StableKey: "older", Status: "updated"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "import-report-20260617-110000.json"), ImportReport{
		Target: ImportTarget{ServerID: "target-b"},
		Matches: []ImportMatch{
			{StableKey: "other-target", Status: "updated"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	done, reportName := service.resumeSuccessfulItems(exportDir, target)
	if !strings.Contains(reportName, "import-checkpoint.json") || !strings.Contains(reportName, "import-report-20260617-100000.json") {
		t.Fatalf("reportName = %q", reportName)
	}
	if !done["checkpoint"] || !done["older"] || done["other-target"] {
		t.Fatalf("resume map = %#v", done)
	}
}

func TestResumeSuccessfulItemsDoesNotResumeImageFailedItems(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "pkg")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := ImportTarget{ServerID: "target-a"}
	checkpoint := newImportCheckpointStore(exportDir, target)
	if err := checkpoint.Record(ImportMatch{
		StableKey:     "image-failed-checkpoint",
		SourceName:    "Image Failed Checkpoint",
		TargetID:      "item-1",
		Status:        "updated",
		ImageFailures: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := checkpoint.Record(ImportMatch{
		StableKey:  "clean-checkpoint",
		SourceName: "Clean Checkpoint",
		TargetID:   "item-2",
		Status:     "updated",
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "import-report-20260617-100000.json"), ImportReport{
		Target: target,
		Matches: []ImportMatch{
			{StableKey: "image-failed-report", Status: "updated", ImageFailures: 1},
			{StableKey: "clean-report", Status: "updated"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	done, _ := service.resumeSuccessfulItems(exportDir, target)
	if done["image-failed-checkpoint"] || done["image-failed-report"] {
		t.Fatalf("image-failed items should not resume as complete: %#v", done)
	}
	if !done["clean-checkpoint"] || !done["clean-report"] {
		t.Fatalf("clean successful items should resume: %#v", done)
	}
	if shouldCheckpointMatch(ImportMatch{StableKey: "item", Status: "updated", ImageFailures: 1}, false) {
		t.Fatalf("image-failed updated match should not be checkpointed")
	}
}

func TestResumeSuccessfulItemsRequiresCompletedMediaInfoWhenEnabled(t *testing.T) {
	dataDir := t.TempDir()
	service := NewService(dataDir)
	exportDir := filepath.Join(service.ExportsDir(), "pkg")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := ImportTarget{ServerID: "target-a"}
	checkpoint := newImportCheckpointStore(exportDir, target)
	if err := checkpoint.Record(ImportMatch{StableKey: "media-updated-checkpoint", Status: "updated", MediaInfoUpdated: 1}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportDir, "import-report-20260617-100000.json"), ImportReport{
		Target: target,
		Matches: []ImportMatch{
			{StableKey: "legacy-no-media", Status: "updated"},
			{StableKey: "media-failed", Status: "updated", MediaInfoFailed: 1},
			{StableKey: "media-updated", Status: "updated", MediaInfoUpdated: 1},
			{StableKey: "media-skipped", Status: "updated", MediaInfoSkipped: 1},
		},
	}); err != nil {
		t.Fatal(err)
	}

	done, _ := service.resumeSuccessfulItems(exportDir, target, true)
	for _, key := range []string{"media-updated-checkpoint", "media-updated", "media-skipped"} {
		if !done[key] {
			t.Fatalf("completed media info item %q should resume: %#v", key, done)
		}
	}
	for _, key := range []string{"legacy-no-media", "media-failed"} {
		if done[key] {
			t.Fatalf("incomplete media info item %q must be retried: %#v", key, done)
		}
	}
}

func TestImportCheckpointRecordClearsItemsWhenTargetSwitches(t *testing.T) {
	exportDir := t.TempDir()
	firstTarget := ImportTarget{ServerID: "target-a", ServerName: "Target A", Version: "4.9.5"}
	secondTarget := ImportTarget{ServerID: "target-b", ServerName: "Target B", Version: "4.9.5"}
	first := newImportCheckpointStore(exportDir, firstTarget)
	if err := first.Record(ImportMatch{StableKey: "old-item", SourceName: "Old", TargetID: "old-target", Status: "updated"}); err != nil {
		t.Fatal(err)
	}
	if err := first.RecordPersonAvatar(personImageResult{StableKey: "old-person", Name: "Old Person", TargetID: "old-person-target"}); err != nil {
		t.Fatal(err)
	}

	second := newImportCheckpointStore(exportDir, secondTarget)
	if err := second.Record(ImportMatch{StableKey: "new-item", SourceName: "New", TargetID: "new-target", Status: "updated"}); err != nil {
		t.Fatal(err)
	}

	checkpoint, ok := readImportCheckpoint(filepath.Join(exportDir, "import-checkpoint.json"), secondTarget)
	if !ok {
		t.Fatalf("checkpoint should read for second target")
	}
	if checkpoint.Items["old-item"].StableKey != "" || checkpoint.PersonAvatars["old-person"].StableKey != "" {
		t.Fatalf("target switch should clear old checkpoint entries: %#v", checkpoint)
	}
	if checkpoint.Items["new-item"].StableKey == "" {
		t.Fatalf("new target item missing after target switch: %#v", checkpoint)
	}
}

func TestSameImportTargetAllowsOnlyUnknownLegacyCompatibility(t *testing.T) {
	unknown := ImportTarget{}
	if !sameImportTarget(unknown, unknown) {
		t.Fatalf("empty legacy targets should be compatible")
	}
	if sameImportTarget(ImportTarget{ServerID: "target-a"}, unknown) {
		t.Fatalf("identified target should not match unknown target")
	}
	if sameImportTarget(ImportTarget{ServerID: "target-a"}, ImportTarget{ServerID: "target-b"}) {
		t.Fatalf("different target ids should not match")
	}
	if sameImportTarget(ImportTarget{ServerName: "Target A"}, ImportTarget{ServerName: "Target B"}) {
		t.Fatalf("partial target names should not match")
	}
	if !sameImportTarget(
		ImportTarget{ServerName: "Target A", Version: "4.9.5"},
		ImportTarget{ServerName: "Target A", Version: "4.9.5"},
	) {
		t.Fatalf("matching complete legacy name/version targets should match")
	}
}

func TestNextImportReportPathAvoidsSameSecondCollision(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 18, 10, 20, 30, 123456789, time.Local)
	first, err := nextImportReportPath(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(first, map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	second, err := nextImportReportPath(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("report path collision: %s", first)
	}
	if !strings.Contains(filepath.Base(first), "102030.123456789") || !strings.Contains(filepath.Base(second), "-002") {
		t.Fatalf("unexpected report names: first=%s second=%s", filepath.Base(first), filepath.Base(second))
	}
}

func TestImportableManifestItemsSkipsIncrementalUnchangedItems(t *testing.T) {
	items := []storage.ItemEntry{
		{StableKey: "changed", Name: "Changed"},
		{StableKey: "unchanged", Name: "Unchanged", Skipped: true},
	}
	got := importableManifestItems(items)
	if len(got) != 1 || got[0].StableKey != "changed" {
		t.Fatalf("importableManifestItems = %#v", got)
	}
}

func TestFailureReportGroupsLimitedExamples(t *testing.T) {
	var failures FailureReport
	for i := 0; i < 25; i++ {
		addFailureExample(&failures, ImportMatch{
			StableKey:  "missing",
			SourceName: "Missing",
			Status:     "unmatched",
			Reason:     "not found",
		})
	}
	addFailureExample(&failures, ImportMatch{StableKey: "amb", Status: "ambiguous", Candidates: []string{"A", "B"}})
	addFailureExample(&failures, ImportMatch{StableKey: "err", Status: "failed", Error: "boom"})
	if len(failures.Unmatched) != 20 || len(failures.Ambiguous) != 1 || len(failures.Failed) != 1 {
		t.Fatalf("failure groups = %#v", failures)
	}
}

func TestValidateExportPackageDetectsMissingAndEscapingFiles(t *testing.T) {
	exportPath := t.TempDir()
	manifest := storage.Manifest{
		Items: []storage.ItemEntry{
			{
				Name:     "Movie",
				InfoPath: "libraries/movies/items/movie/info.json",
				Images: []storage.FileEntry{
					{Type: "Primary", Path: "libraries/movies/items/movie/poster.jpg"},
					{Type: "Logo", Path: "libraries/movies/items/movie/missing-logo.png"},
				},
			},
			{
				Name:     "Escaping",
				InfoPath: "../outside.json",
			},
		},
		People: []storage.PersonEntry{
			{
				Name:  "Actor",
				Image: &storage.FileEntry{Type: "Primary", Path: "people/actor/primary.jpg"},
			},
		},
	}
	for _, rel := range []string{
		"manifest.json",
		"libraries/movies/items/movie/info.json",
		"libraries/movies/items/movie/poster.jpg",
		"people/actor/primary.jpg",
	} {
		if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(rel)), map[string]any{"ok": true}); err != nil {
			t.Fatal(err)
		}
	}
	validation := ValidateExportPackage(exportPath, manifest)
	if validation.OK() {
		t.Fatalf("validation should fail for missing and escaping paths: %#v", validation)
	}
	if validation.MissingFiles != 1 || validation.InvalidPaths != 1 {
		t.Fatalf("validation counts = %#v, want one missing and one invalid", validation)
	}
	if !strings.Contains(validation.Error(), "导出包校验失败") {
		t.Fatalf("validation error message = %q", validation.Error())
	}
}

func TestValidateExportPackagePassesCompletePackage(t *testing.T) {
	exportPath := t.TempDir()
	manifest := storage.Manifest{
		Items: []storage.ItemEntry{
			{
				Name:     "Movie",
				InfoPath: "libraries/movies/items/movie/info.json",
				RawPath:  "libraries/movies/items/movie/raw.json",
				Images:   []storage.FileEntry{{Type: "Primary", Path: "libraries/movies/items/movie/poster.jpg"}},
			},
		},
		People: []storage.PersonEntry{
			{Name: "Actor", Image: &storage.FileEntry{Type: "Primary", Path: "people/actor/primary.jpg"}},
		},
	}
	for _, rel := range []string{
		"manifest.json",
		"libraries/movies/items/movie/info.json",
		"libraries/movies/items/movie/raw.json",
		"libraries/movies/items/movie/poster.jpg",
		"people/actor/primary.jpg",
	} {
		if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(rel)), map[string]any{"ok": true}); err != nil {
			t.Fatal(err)
		}
	}
	validation := ValidateExportPackage(exportPath, manifest)
	if !validation.OK() {
		t.Fatalf("validation should pass complete package: %#v", validation)
	}
	if validation.CheckedFiles != 5 {
		t.Fatalf("checked files = %d, want 5", validation.CheckedFiles)
	}
}

func TestValidateExportPackageDetectsTamperedChecksumAndSize(t *testing.T) {
	exportPath := t.TempDir()
	imageRel := "libraries/movies/items/movie/poster.jpg"
	file, err := storage.WriteBytes(filepath.Join(exportPath, filepath.FromSlash(imageRel)), []byte("original-image"))
	if err != nil {
		t.Fatal(err)
	}
	file.Path = imageRel
	file.Type = "Primary"
	manifest := storage.Manifest{
		Items: []storage.ItemEntry{
			{
				Name:     "Movie",
				InfoPath: "libraries/movies/items/movie/info.json",
				Images:   []storage.FileEntry{file},
			},
		},
	}
	for _, rel := range []string{"manifest.json", "libraries/movies/items/movie/info.json"} {
		if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(rel)), map[string]any{"ok": true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(exportPath, filepath.FromSlash(imageRel)), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	validation := ValidateExportPackage(exportPath, manifest)
	if validation.OK() {
		t.Fatalf("validation should fail for tampered image: %#v", validation)
	}
	if validation.SizeMismatches != 1 || validation.ChecksumMismatches != 1 {
		t.Fatalf("validation mismatch counts = %#v, want one size and one checksum mismatch", validation)
	}
	if !strings.Contains(validation.Error(), "SHA256 不一致") {
		t.Fatalf("validation error should mention checksum: %q", validation.Error())
	}
}

func TestValidateExportPackageAllowsLegacyEntriesWithoutChecksum(t *testing.T) {
	exportPath := t.TempDir()
	manifest := storage.Manifest{
		Items: []storage.ItemEntry{
			{
				Name:     "Movie",
				InfoPath: "libraries/movies/items/movie/info.json",
				Images:   []storage.FileEntry{{Type: "Primary", Path: "libraries/movies/items/movie/poster.jpg"}},
			},
		},
	}
	for _, rel := range []string{
		"manifest.json",
		"libraries/movies/items/movie/info.json",
		"libraries/movies/items/movie/poster.jpg",
	} {
		if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(rel)), map[string]any{"ok": true}); err != nil {
			t.Fatal(err)
		}
	}
	validation := ValidateExportPackage(exportPath, manifest)
	if !validation.OK() {
		t.Fatalf("legacy package without checksum should pass existence validation: %#v", validation)
	}
}

func TestBuildCompatibilityProfileDetectsTargetVersion(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{target: "4.8.11.0", want: "emby-4.8-classic"},
		{target: "4.9.5.0", want: "emby-4.9-strict"},
		{target: "5.0.0", want: "emby-generic"},
		{target: "", want: "emby-generic"},
	}
	for _, tt := range tests {
		got := BuildCompatibilityProfile("4.8.11.0", tt.target)
		if got.Name != tt.want {
			t.Fatalf("BuildCompatibilityProfile target %q = %q, want %q", tt.target, got.Name, tt.want)
		}
		if got.SourceVersion != "4.8.11.0" || got.TargetVersion != strings.TrimSpace(tt.target) {
			t.Fatalf("profile versions not preserved: %#v", got)
		}
		if len(got.Notes) == 0 {
			t.Fatalf("profile should include strategy notes: %#v", got)
		}
	}
}

func TestSummaryLines(t *testing.T) {
	exportLine := exportSummaryLine(storage.Summary{
		Libraries:    2,
		Items:        10,
		ItemImages:   25,
		People:       6,
		PeopleImages: 4,
		Errors:       1,
	}, time.Minute+23*time.Second)
	if want := "导出总结：媒体库 2 个，项目 10 个，媒体图片 25 张，人物 6 个，人物头像 4 张，错误 1 个，用时 1分23秒。"; exportLine != want {
		t.Fatalf("exportSummaryLine = %q, want %q", exportLine, want)
	}

	startedAt := time.Date(2026, 6, 16, 18, 0, 0, 0, time.Local)
	importLine := importSummaryLine(ImportReport{
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(2*time.Hour + 3*time.Minute + 4*time.Second),
		Matches:   []ImportMatch{{}, {}, {}},
		Summary: storage.Summary{
			MetadataUpdated:    2,
			Unmatched:          1,
			ItemImagesPushed:   5,
			ItemImagesFailed:   1,
			PeopleImages:       3,
			PeopleImagesFailed: 2,
		},
	})
	if want := "导入总结：项目 3 个，元数据成功 2 个，未匹配 1 个，歧义 0 个，错误 0 个，媒体图片成功 5 张/失败 1 张，人物头像成功 3 张/失败 2 张，用时 2小时3分4秒。"; importLine != want {
		t.Fatalf("importSummaryLine = %q, want %q", importLine, want)
	}

	dryRunLine := importSummaryLine(ImportReport{
		StartedAt: startedAt,
		EndedAt:   startedAt.Add(900 * time.Millisecond),
		DryRun:    true,
		Matches:   []ImportMatch{{}, {}},
		Summary: storage.Summary{
			Matched:   1,
			Ambiguous: 1,
		},
	})
	if want := "导入验证总结：项目 2 个，匹配 1 个，未匹配 0 个，歧义 1 个，错误 0 个，用时 1秒；本次未写入元数据和图片。"; dryRunLine != want {
		t.Fatalf("dry-run importSummaryLine = %q, want %q", dryRunLine, want)
	}
}

func TestExportEnrichesItemsWhenListResponseOmitsImagesAndPeople(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeExporterJSON(t, w, map[string]any{"ServerName": "Mock Emby", "Version": "4.9.5.0", "Id": "mock"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("ParentId") == "lib-movies":
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{
						"Id":   "item-1",
						"Name": "Movie From List",
						"Type": "Movie",
						"Path": `D:\Movies\Movie From List.mkv`,
					},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "item-1":
			writeExporterJSON(t, w, map[string]any{"Items": []map[string]any{{
				"Id":          "item-1",
				"Name":        "Movie From Detail",
				"Type":        "Movie",
				"Path":        `D:\Movies\Movie From Detail.mkv`,
				"ProviderIds": map[string]string{"Tmdb": "123"},
				"People": []map[string]any{
					{
						"Name":            "Actor One",
						"Type":            "Actor",
						"Role":            "Lead",
						"ProviderIds":     map[string]string{"Tmdb": "456"},
						"PrimaryImageTag": "person-tag",
					},
				},
			}}, "TotalRecordCount": 1})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-1/Images":
			writeExporterJSON(t, w, []map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-1/Images/Primary":
			writeExporterImage(w)
		case r.Method == http.MethodGet && r.URL.Path == "/Persons/Actor One":
			writeExporterJSON(t, w, map[string]any{
				"Name":        "Actor One",
				"Id":          "person-1",
				"ProviderIds": map[string]string{"Tmdb": "456"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Persons/Actor One/Images/Primary":
			writeExporterImage(w)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := NewService(t.TempDir())
	manager := job.NewManager()
	j := manager.Create("export")
	j.Start()

	result, err := service.Export(context.Background(), j, ExportRequest{
		Connection:          emby.Connection{BaseURL: server.URL, APIKey: "test-key"},
		Libraries:           []emby.Library{{ID: "lib-movies", Name: "Movies"}},
		ImageTypes:          []string{"Primary"},
		IncludePeopleImages: true,
	})
	if err != nil {
		t.Fatalf("Export returned error: %v", err)
	}
	if result.Manifest.Summary.ItemImages != 1 {
		t.Fatalf("item images = %d, want 1", result.Manifest.Summary.ItemImages)
	}
	if result.Manifest.Summary.People != 1 {
		t.Fatalf("people = %d, want 1", result.Manifest.Summary.People)
	}
	if result.Manifest.Summary.PeopleImages != 1 {
		t.Fatalf("people images = %d, want 1", result.Manifest.Summary.PeopleImages)
	}
}

func TestExportWritesRawMediaSourcesAndStreamsToPackage(t *testing.T) {
	var detailFields string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeExporterJSON(t, w, map[string]any{"ServerName": "Mock Emby", "Version": "4.9.5.0", "Id": "mock"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("ParentId") == "lib-movies":
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{
						"Id":   "item-raw-media",
						"Name": "Raw Media Fixture",
						"Type": "Movie",
						"Path": `D:\Movies\Raw Media Fixture.mkv`,
					},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "item-raw-media":
			detailFields = r.URL.Query().Get("Fields")
			if !strings.Contains(detailFields, "MediaSources") || !strings.Contains(detailFields, "MediaStreams") {
				http.Error(w, "detail request missing media technical fields: "+detailFields, http.StatusBadRequest)
				return
			}
			writeExporterJSON(t, w, map[string]any{"Items": []map[string]any{rawItemWithMediaTechnicalFieldsFixture()}, "TotalRecordCount": 1})
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := NewService(t.TempDir())
	j := job.NewManager().Create("export")
	j.Start()

	result, err := service.Export(context.Background(), j, ExportRequest{
		Connection:       emby.Connection{BaseURL: server.URL, APIKey: "test-key"},
		Libraries:        []emby.Library{{ID: "lib-movies", Name: "Movies"}},
		SkipImages:       true,
		IncludeMediaInfo: true,
	})
	if err != nil {
		t.Fatalf("Export returned error: %v", err)
	}
	if detailFields == "" {
		t.Fatalf("export did not enrich the list item with a detail request")
	}
	if len(result.Manifest.Items) != 1 {
		t.Fatalf("manifest items = %#v, want one item", result.Manifest.Items)
	}
	entry := result.Manifest.Items[0]
	if entry.RawPath == "" || entry.InfoPath == "" {
		t.Fatalf("manifest item should point at raw and info files: %#v", entry)
	}
	if entry.MediaInfo == nil || entry.MediaInfo.SourcesCount != 1 || entry.MediaInfo.StreamsCount != 2 || entry.MediaInfo.ChaptersCount != 1 {
		t.Fatalf("manifest item should summarize media technical info: %#v", entry.MediaInfo)
	}
	if result.Manifest.Summary.ItemsWithMediaInfo != 1 || result.Manifest.Summary.MediaSources != 1 || result.Manifest.Summary.MediaStreams != 2 || result.Manifest.Summary.Chapters != 1 {
		t.Fatalf("manifest summary should count media technical info: %#v", result.Manifest.Summary)
	}

	var raw map[string]any
	if err := storage.ReadJSON(filepath.Join(result.Path, filepath.FromSlash(entry.RawPath)), &raw); err != nil {
		t.Fatalf("reading raw item returned error: %v", err)
	}
	assertRawMediaTechnicalFields(t, raw)

	var info storage.ItemInfo
	if err := storage.ReadJSON(filepath.Join(result.Path, filepath.FromSlash(entry.InfoPath)), &info); err != nil {
		t.Fatalf("reading item info returned error: %v", err)
	}
	assertRawMediaTechnicalFields(t, rawPayloadFromItemInfo(t, info))

	validation := ValidateExportPackage(result.Path, result.Manifest)
	if !validation.OK() {
		t.Fatalf("export package with raw media fields should validate: %#v", validation)
	}
}

func TestExportReportsRequiredMediaDetailReadFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeExporterJSON(t, w, map[string]any{"ServerName": "Mock Emby", "Version": "4.9.5.0", "Id": "mock"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("ParentId") == "lib-movies":
			writeExporterJSON(t, w, map[string]any{
				"Items":            []map[string]any{{"Id": "item-detail-fails", "Name": "Detail Fails", "Type": "Movie", "Path": `D:\Movies\Detail Fails.mkv`}},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "item-detail-fails":
			http.Error(w, "detail endpoint unavailable", http.StatusInternalServerError)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := NewService(t.TempDir())
	j := job.NewManager().Create("export")
	j.Start()
	result, err := service.Export(context.Background(), j, ExportRequest{
		Connection: emby.Connection{BaseURL: server.URL, APIKey: "test-key"},
		Libraries:  []emby.Library{{ID: "lib-movies", Name: "Movies"}},
		SkipImages: true, IncludeMediaInfo: true,
	})
	if err != nil {
		t.Fatalf("Export should finish with a structured item error: %v", err)
	}
	if len(result.Manifest.Items) != 0 || result.Manifest.Summary.Errors != 1 || len(result.Manifest.Errors) != 1 {
		t.Fatalf("detail failure should not create an empty media item: %#v", result.Manifest)
	}
	if !strings.Contains(result.Manifest.Errors[0].Message, "read media technical details") {
		t.Fatalf("detail failure diagnostic is unclear: %#v", result.Manifest.Errors[0])
	}
}

func TestExportDisabledOmitsMediaTechnicalFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeExporterJSON(t, w, map[string]any{"ServerName": "Mock Emby", "Version": "4.8.11.0", "Id": "mock"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			item := rawItemWithMediaTechnicalFieldsFixture()
			item["People"] = []map[string]any{{"Name": "Actor One", "Type": "Actor"}}
			item["ImageTags"] = map[string]string{"Primary": "poster"}
			writeExporterJSON(t, w, map[string]any{"Items": []map[string]any{item}, "TotalRecordCount": 1})
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	service := NewService(t.TempDir())
	j := job.NewManager().Create("export")
	j.Start()
	result, err := service.Export(context.Background(), j, ExportRequest{
		Connection: emby.Connection{BaseURL: server.URL, APIKey: "test-key"},
		Libraries:  []emby.Library{{ID: "lib", Name: "Movies"}},
		SkipImages: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry := result.Manifest.Items[0]
	if entry.MediaInfo != nil || result.Manifest.Summary.ItemsWithMediaInfo != 0 {
		t.Fatalf("disabled export should omit manifest media info: %#v", result.Manifest)
	}
	var raw map[string]any
	if err := storage.ReadJSON(filepath.Join(result.Path, filepath.FromSlash(entry.RawPath)), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"MediaSources", "MediaStreams", "Chapters"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("disabled export raw leaked %s: %#v", key, raw)
		}
	}
}

func TestMediaInfoDetailCompletenessAndRawMerge(t *testing.T) {
	partial := emby.Item{
		ID: "movie", Type: "Movie",
		MediaSources: []map[string]any{{"Container": "mkv"}},
		People:       []emby.Person{{Name: "Actor"}},
		ImageTags:    map[string]string{"Primary": "poster"},
	}
	if !needsExportItemDetails(partial, true) {
		t.Fatalf("a playable item without streams must fetch details")
	}
	partial.MediaSources[0]["MediaStreams"] = []map[string]any{{"Index": 0, "Type": "Video", "Codec": "hevc"}}
	if !needsExportItemDetails(partial, true) {
		t.Fatalf("an omitted Chapters field must fetch details")
	}
	partial.Raw = map[string]any{"Chapters": []map[string]any{}}
	if needsExportItemDetails(partial, true) {
		t.Fatalf("nested source streams plus an explicit Chapters field should satisfy media detail completeness")
	}

	base := emby.Item{MediaSources: partial.MediaSources, Raw: map[string]any{"MediaSources": partial.MediaSources}}
	full := emby.Item{ID: "movie", Type: "Movie", Raw: map[string]any{"Id": "movie", "Type": "Movie"}}
	merged := mergeExportItemDetails(base, full)
	if len(merged.MediaSources) != 1 || len(objectSliceField(merged.Raw, "MediaSources")) != 1 {
		t.Fatalf("merged structured fields and raw payload diverged: %#v", merged)
	}
}

func TestMediaInfoSummaryIncludesUniqueStreamsAcrossAllSources(t *testing.T) {
	shared := map[string]any{"Index": 0, "Type": "Video", "Codec": "hevc"}
	second := map[string]any{"Index": 1, "Type": "Audio", "Codec": "aac"}
	item := emby.Item{
		MediaStreams: []map[string]any{shared},
		MediaSources: []map[string]any{
			{"Container": "mkv", "MediaStreams": []map[string]any{shared}},
			{"Container": "mkv", "MediaStreams": []map[string]any{second}},
		},
	}
	info := mediaInfoFromItem(item)
	if info == nil || info.SourcesCount != 2 || info.StreamsCount != 2 {
		t.Fatalf("media info summary should count unique streams from every source: %#v", info)
	}
}

func TestItemFingerprintChangesWhenMediaTechnicalInfoChanges(t *testing.T) {
	base := emby.Item{
		ID:   "item-1",
		Name: "Movie",
		Type: "Movie",
		MediaStreams: []map[string]any{
			{"Index": 0, "Type": "Video", "Codec": "h264", "Width": 1920},
		},
	}
	changed := base
	changed.MediaStreams = []map[string]any{
		{"Index": 0, "Type": "Video", "Codec": "hevc", "Width": 1920},
	}
	if itemFingerprint(base) == itemFingerprint(changed) {
		t.Fatalf("fingerprint should change when media technical info changes")
	}
}

func TestIncrementalExportSkipsUnchangedItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			writeExporterJSON(t, w, map[string]any{"ServerName": "Mock Emby", "Version": "4.9.5.0", "Id": "mock"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("ParentId") == "lib-movies":
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{
						"Id":          "item-1",
						"Name":        "Movie",
						"Type":        "Movie",
						"Path":        `D:\Movies\Movie.mkv`,
						"ProviderIds": map[string]string{"Tmdb": "123"},
						"ImageTags":   map[string]string{"Primary": "poster-tag"},
					},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "item-1":
			writeExporterJSON(t, w, map[string]any{"Items": []map[string]any{{
				"Id":          "item-1",
				"Name":        "Movie",
				"Type":        "Movie",
				"Path":        `D:\Movies\Movie.mkv`,
				"ProviderIds": map[string]string{"Tmdb": "123"},
				"ImageTags":   map[string]string{"Primary": "poster-tag"},
			}}, "TotalRecordCount": 1})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-1/Images":
			writeExporterJSON(t, w, []map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-1/Images/Primary":
			writeExporterImage(w)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	service := NewService(t.TempDir())
	request := ExportRequest{
		Connection: emby.Connection{BaseURL: server.URL, APIKey: "test-key"},
		Libraries:  []emby.Library{{ID: "lib-movies", Name: "Movies"}},
		ImageTypes: []string{"Primary"},
	}

	firstJob := job.NewManager().Create("export")
	firstJob.Start()
	first, err := service.Export(context.Background(), firstJob, request)
	if err != nil {
		t.Fatalf("first export returned error: %v", err)
	}
	if first.Manifest.Summary.SkippedItems != 0 || len(first.Manifest.Items) != 1 {
		t.Fatalf("first manifest = %#v", first.Manifest)
	}

	secondJob := job.NewManager().Create("export")
	secondJob.Start()
	request.Incremental = true
	second, err := service.Export(context.Background(), secondJob, request)
	if err != nil {
		t.Fatalf("second export returned error: %v", err)
	}
	if second.Manifest.Incremental == nil || !second.Manifest.Incremental.Enabled {
		t.Fatalf("incremental metadata missing: %#v", second.Manifest.Incremental)
	}
	if second.Manifest.Incremental.BaselineExportName != filepath.Base(first.Path) {
		t.Fatalf("baseline = %q, want %q", second.Manifest.Incremental.BaselineExportName, filepath.Base(first.Path))
	}
	if second.Manifest.Summary.SkippedItems != 1 || len(second.Manifest.Items) != 1 || !second.Manifest.Items[0].Skipped {
		t.Fatalf("second manifest did not mark item skipped: %#v", second.Manifest)
	}
	validation := ValidateExportPackage(second.Path, second.Manifest)
	if !validation.OK() {
		t.Fatalf("incremental skipped package should validate: %#v", validation)
	}
}

func TestExportPlanUsesConfiguredImageTypesAndIncludesPeopleAvatars(t *testing.T) {
	item := ExportedItem{
		StableKey: "provider-imdb-tt0133093",
		EmbyID:    "old-item-1",
		Type:      "Movie",
		Name:      "The Matrix",
		ImageTags: map[string]string{
			"Primary": "primary-tag",
			"Logo":    "logo-tag",
			"Thumb":   "thumb-tag",
		},
		People: []ExportedPerson{
			{
				StableKey:       "person-provider-imdb-nm0000206",
				EmbyID:          "old-person-id",
				Name:            "Keanu Reeves",
				Type:            "Actor",
				Role:            "Neo",
				ProviderIDs:     map[string]string{"Imdb": "nm0000206"},
				PrimaryImageTag: "avatar-tag",
			},
		},
	}

	assets := PlanExportAssets(item, ExportOptions{
		ImageTypes:          []string{"Primary", "Logo"},
		IncludePeopleImages: true,
	})

	got := exportAssetSignatures(assets)
	want := []string{
		"item:old-item-1:Primary",
		"item:old-item-1:Logo",
		"person:person-provider-imdb-nm0000206:Primary",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanExportAssets returned %#v, want %#v", got, want)
	}
}

func TestMatchForImportPrefersProviderIDsAndIgnoresOldEmbyID(t *testing.T) {
	source := ExportedItem{
		StableKey:   "provider-imdb-tt0133093",
		EmbyID:      "old-emby-id",
		Type:        "Movie",
		Name:        "The Matrix",
		ProviderIDs: map[string]string{"Imdb": "tt0133093", "Tmdb": "603"},
	}
	candidates := []ImportCandidate{
		{
			EmbyID:      "old-emby-id",
			Type:        "Movie",
			Name:        "Wrong Movie With Reused Old ID",
			ProviderIDs: map[string]string{"Imdb": "tt0000001"},
		},
		{
			EmbyID:      "new-provider-match",
			Type:        "Movie",
			Name:        "Matrix",
			ProviderIDs: map[string]string{"Imdb": "tt0133093"},
		},
	}

	match := MatchForImport(source, candidates)
	if match.TargetEmbyID != "new-provider-match" {
		t.Fatalf("MatchForImport should choose provider match, got %#v", match)
	}
	if match.TargetEmbyID == source.EmbyID {
		t.Fatalf("old Emby ID must not be used for cross-server matching: %#v", match)
	}
	if match.Strategy != "provider_ids" {
		t.Fatalf("match strategy = %q, want provider_ids", match.Strategy)
	}
}

func TestMatchForImportUsesEpisodeIdentityBeforeNameFallback(t *testing.T) {
	source := ExportedItem{
		StableKey:     "episode-firefly-s01e02",
		EmbyID:        "old-episode-id",
		Type:          "Episode",
		Name:          "The Train Job",
		SeriesName:    "Firefly",
		SeasonNumber:  1,
		EpisodeNumber: 2,
	}
	candidates := []ImportCandidate{
		{
			EmbyID:        "old-episode-id",
			Type:          "Episode",
			Name:          "Wrong Episode With Reused Old ID",
			SeriesName:    "Firefly",
			SeasonNumber:  1,
			EpisodeNumber: 9,
		},
		{
			EmbyID:        "new-episode-match",
			Type:          "Episode",
			Name:          "Localized Episode Title",
			SeriesName:    "Firefly",
			SeasonNumber:  1,
			EpisodeNumber: 2,
		},
	}

	match := MatchForImport(source, candidates)
	if match.TargetEmbyID != "new-episode-match" {
		t.Fatalf("episode match should use SeriesName+season+episode, got %#v", match)
	}
	if match.Strategy != "episode" {
		t.Fatalf("match strategy = %q, want episode", match.Strategy)
	}
}

func TestMatchForImportFallsBackToNameAndOriginalTitle(t *testing.T) {
	source := ExportedItem{
		StableKey:      "movie-amelie-2001",
		EmbyID:         "old-amelie",
		Type:           "Movie",
		Name:           "Le Fabuleux Destin d'Amelie Poulain",
		OriginalTitle:  "Amelie",
		ProductionYear: 2001,
	}
	candidates := []ImportCandidate{
		{
			EmbyID:         "target-by-original-title",
			Type:           "Movie",
			Name:           "Amelie",
			OriginalTitle:  "Le Fabuleux Destin d'Amelie Poulain",
			ProductionYear: 2001,
		},
	}

	match := MatchForImport(source, candidates)
	if match.TargetEmbyID != "target-by-original-title" {
		t.Fatalf("name/original title fallback did not match: %#v", match)
	}
	if match.Strategy != "name" && match.Strategy != "original_title" {
		t.Fatalf("fallback strategy = %q, want name or original_title", match.Strategy)
	}
}

func TestFindMatchUsesEpisodePatternFromPathWhenIndexesAreMissing(t *testing.T) {
	client := newFindMatchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("SearchTerm"); got != "Firefly" {
			http.Error(w, "unexpected SearchTerm "+got, http.StatusBadRequest)
			return
		}
		writeExporterJSON(t, w, map[string]any{
			"Items": []map[string]any{
				{
					"Id":         "target-episode",
					"Type":       "Episode",
					"Name":       "Firefly.S01E02.1080p",
					"SeriesName": "Firefly",
					"Path":       `/media/Firefly/Firefly.S01E02.mkv`,
				},
			},
			"TotalRecordCount": 1,
		})
	})

	target, candidates, reason, err := FindMatch(context.Background(), client, storage.ItemEntry{
		Type:       "Episode",
		Name:       "The Train Job",
		SeriesName: "Firefly",
		Path:       `/old/Firefly.S01E02.mkv`,
	})
	if err != nil {
		t.Fatalf("FindMatch returned error: %v", err)
	}
	if target.ID != "target-episode" || reason != "episode-number" || len(candidates) != 1 {
		t.Fatalf("FindMatch did not use SxxExx path fallback: target=%#v reason=%q candidates=%#v", target, reason, candidates)
	}
}

func TestFindMatchUsesEpisodePathSeriesWhenEpisodeNamesCollide(t *testing.T) {
	client := newFindMatchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		writeExporterJSON(t, w, map[string]any{
			"Items": []map[string]any{
				{
					"Id":                "target-moving-episode-4",
					"Type":              "Episode",
					"Name":              "秘密",
					"Path":              `/new/日韩剧集/超异能族 (2023) {tmdb-126485}/Season 1/超异能族.2023.S01E04.strm`,
					"IndexNumber":       4,
					"ParentIndexNumber": 1,
				},
				{
					"Id":                "target-bet-episode-5",
					"Type":              "Episode",
					"Name":              "秘密",
					"Path":              `/new/日韩剧集/赌金 (2026) {tmdb-278113}/Season 1/赌金.2026.S01E05.strm`,
					"IndexNumber":       5,
					"ParentIndexNumber": 1,
				},
			},
			"TotalRecordCount": 2,
		})
	})

	target, candidates, reason, err := FindMatch(context.Background(), client, storage.ItemEntry{
		Type:              "Episode",
		Name:              "秘密",
		SeriesName:        "赌金",
		Path:              `/old/日韩剧集/赌金 (2026) {tmdb-278113}/Season 1/赌金.2026.S01E05.第5集.strm`,
		IndexNumber:       5,
		ParentIndexNumber: 1,
	})
	if err != nil {
		t.Fatalf("FindMatch returned error: %v", err)
	}
	if target.ID != "target-bet-episode-5" || reason != "episode-number" || len(candidates) != 1 {
		t.Fatalf("FindMatch should use episode path series before name fallback: target=%#v reason=%q candidates=%#v", target, reason, candidates)
	}
}

func TestFindMatchPrefersMediaFileStem(t *testing.T) {
	client := newFindMatchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("SearchTerm"); got != "寄生虫 (2019) - 2160p.HDR10.H.265" {
			http.Error(w, "unexpected SearchTerm "+got, http.StatusBadRequest)
			return
		}
		writeExporterJSON(t, w, map[string]any{
			"Items": []map[string]any{
				{
					"Id":   "target-by-file",
					"Type": "Movie",
					"Name": "Different Local Title",
					"Path": `/new/library/寄生虫 (2019) - 2160p.HDR10.H.265.strm`,
				},
			},
			"TotalRecordCount": 1,
		})
	})

	target, candidates, reason, err := FindMatch(context.Background(), client, storage.ItemEntry{
		Type: "Movie",
		Name: "寄生虫",
		Path: `/old/library/寄生虫 (2019) - 2160p.HDR10.H.265.strm`,
	})
	if err != nil {
		t.Fatalf("FindMatch returned error: %v", err)
	}
	if target.ID != "target-by-file" || reason != "media-file" || len(candidates) != 1 {
		t.Fatalf("FindMatch did not prefer media stem: target=%#v reason=%q candidates=%#v", target, reason, candidates)
	}
}

func TestFindMatchMarksFuzzyNameSearchAmbiguous(t *testing.T) {
	client := newFindMatchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		writeExporterJSON(t, w, map[string]any{
			"Items": []map[string]any{
				{"Id": "target-movie", "Type": "Movie", "Name": "The Matrix Reloaded"},
			},
			"TotalRecordCount": 1,
		})
	})

	target, candidates, reason, err := FindMatch(context.Background(), client, storage.ItemEntry{
		Type: "Movie",
		Name: "Matrix",
	})
	if err != nil {
		t.Fatalf("FindMatch returned error: %v", err)
	}
	if target.ID != "" || reason != "name-search-ambiguous" || len(candidates) != 1 {
		t.Fatalf("fuzzy name search should be ambiguous, got target=%#v reason=%q candidates=%#v", target, reason, candidates)
	}
}

func TestFindMatchUsesSeasonParentPathToAvoidFirstSeasonAmbiguity(t *testing.T) {
	client := newFindMatchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("SearchTerm"); got == "Season 1" {
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{
						"Id":   "target-reply-1988-season-1",
						"Type": "Season",
						"Name": "第 1 季",
						"Path": `/new/日韩剧集/请回答1988 (2015) {tmdb-64010}/Season 1`,
					},
					{
						"Id":   "target-iron-education-season-1",
						"Type": "Season",
						"Name": "第 1 季",
						"Path": `/new/日韩剧集/铁拳教育 (2026) {tmdb-276161}/Season 1`,
					},
				},
				"TotalRecordCount": 2,
			})
			return
		} else if got != "第 1 季" {
			http.Error(w, "unexpected SearchTerm "+got, http.StatusBadRequest)
			return
		}
		if got := r.URL.Query().Get("IncludeItemTypes"); got != "Season" {
			http.Error(w, "unexpected IncludeItemTypes "+got, http.StatusBadRequest)
			return
		}
		writeExporterJSON(t, w, map[string]any{
			"Items": []map[string]any{
				{
					"Id":   "target-reply-1988-season-1",
					"Type": "Season",
					"Name": "第 1 季",
					"Path": `/new/日韩剧集/请回答1988 (2015) {tmdb-64010}/Season 1`,
				},
				{
					"Id":   "target-iron-education-season-1",
					"Type": "Season",
					"Name": "第 1 季",
					"Path": `/new/日韩剧集/铁拳教育 (2026) {tmdb-276161}/Season 1`,
				},
			},
			"TotalRecordCount": 2,
		})
	})

	target, candidates, reason, err := FindMatch(context.Background(), client, storage.ItemEntry{
		Type: "Season",
		Name: "第 1 季",
		Path: `/old/日韩剧集/请回答1988 (2015) {tmdb-64010}/Season 1`,
	})
	if err != nil {
		t.Fatalf("FindMatch returned error: %v", err)
	}
	if target.ID != "target-reply-1988-season-1" || reason != "season-parent" || len(candidates) != 1 {
		t.Fatalf("season parent match failed: target=%#v reason=%q candidates=%#v", target, reason, candidates)
	}
}

func TestMergeItemMetadataBuildsPortablePayloadWithoutOldInternalIDs(t *testing.T) {
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "movie", "info.json"))
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{
			Name:            "Source Movie",
			Type:            "Movie",
			Overview:        "source overview",
			ProductionYear:  2026,
			CommunityRating: 8.5,
			Genres:          []string{"Drama"},
			Tags:            []string{"favorite"},
			Taglines:        []string{"tagline"},
			Studios:         []emby.NameID{{Name: "Old Studio", ID: "old-studio-id"}},
			ProviderIDs:     map[string]string{"Tmdb": "123"},
			People: []emby.Person{{
				Name:        "Actor One",
				ID:          "old-person-id",
				Type:        "Actor",
				Role:        "Lead",
				ProviderIDs: map[string]string{"Imdb": "nm0000001"},
			}},
			Raw: map[string]any{
				"SortName": "Source Movie",
				"People": []any{
					map[string]any{"Name": "Actor One", "Id": "old-person-id"},
				},
				"Studios": []any{
					map[string]any{"Name": "Old Studio", "Id": "old-studio-id"},
				},
			},
		},
	}); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}

	current := emby.Item{
		ID:   "target-id",
		Type: "Movie",
		Name: "Target Movie",
		Raw:  map[string]any{"Source": "Library"},
	}
	mergeItemMetadata(&current, storage.ItemEntry{Name: "Source Movie", InfoPath: infoRel}, exportPath)

	if current.Raw["Id"] != "target-id" {
		t.Fatalf("payload should keep target id, got %#v", current.Raw)
	}
	people, ok := current.Raw["People"].([]map[string]any)
	if !ok || len(people) != 1 || people[0]["Name"] != "Actor One" || people[0]["Role"] != "Lead" {
		t.Fatalf("payload should keep portable people fields, got %#v", current.Raw["People"])
	}
	if _, ok := people[0]["Id"]; ok {
		t.Fatalf("payload leaked old person id: %#v", people[0])
	}
	providers, ok := people[0]["ProviderIds"].(map[string]string)
	if !ok || providers["Imdb"] != "nm0000001" {
		t.Fatalf("payload should keep person provider ids, got %#v", people[0]["ProviderIds"])
	}
	studios, ok := current.Raw["Studios"].([]map[string]string)
	if !ok || len(studios) != 1 || studios[0]["Name"] != "Old Studio" {
		t.Fatalf("payload should keep studio names only, got %#v", current.Raw["Studios"])
	}
	if _, ok := studios[0]["Id"]; ok {
		t.Fatalf("payload leaked old studio id: %#v", studios[0])
	}
}

func TestMergeItemMetadataIncludesSanitizedMediaTechnicalFields(t *testing.T) {
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "movie", "info.json"))
	rawRel := filepath.ToSlash(filepath.Join("items", "movie", "raw.json"))
	raw := rawItemWithMediaTechnicalFieldsFixture()
	raw["Id"] = "source-item-id"
	raw["ServerId"] = "source-server"
	sources := raw["MediaSources"].([]map[string]any)
	sources[0]["ServerId"] = "source-server"
	sources[0]["MediaSourceId"] = "source-1"
	streams := raw["MediaStreams"].([]map[string]any)
	streams[0]["MediaSourceId"] = "source-1"
	streams[0]["Path"] = `D:\Movies\Raw Media Fixture.mkv`
	streams[0]["DeliveryUrl"] = "http://source-server/stream"
	streams[0]["UnknownFutureField"] = "read-only"
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{
			Name: "Raw Media Fixture",
			Type: "Movie",
			Raw:  raw,
		},
	}); err != nil {
		t.Fatalf("WriteJSON info returned error: %v", err)
	}
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(rawRel)), raw); err != nil {
		t.Fatalf("WriteJSON raw returned error: %v", err)
	}

	current := emby.Item{
		ID:   "target-id",
		Type: "Movie",
		Name: "Target Movie",
		Raw:  map[string]any{"Source": "Library", "ParentId": "target-parent"},
	}
	included := mergeItemMetadata(&current, storage.ItemEntry{Name: "Raw Media Fixture", InfoPath: infoRel, RawPath: rawRel}, exportPath, true)
	if !included {
		t.Fatalf("mergeItemMetadata should include media technical fields")
	}
	if current.Raw["Id"] != "target-id" || current.Raw["ParentId"] != "target-parent" {
		t.Fatalf("payload should preserve target context, got %#v", current.Raw)
	}
	sanitizedSources, ok := current.Raw["MediaSources"].([]map[string]any)
	if !ok || len(sanitizedSources) != 1 {
		t.Fatalf("payload MediaSources = %#v, want one sanitized source", current.Raw["MediaSources"])
	}
	if sanitizedSources[0]["Protocol"] != "File" || sanitizedSources[0]["Container"] != "mkv" {
		t.Fatalf("sanitized source lost technical fields: %#v", sanitizedSources[0])
	}
	for _, forbidden := range []string{"Id", "Path", "ServerId", "MediaSourceId"} {
		if _, ok := sanitizedSources[0][forbidden]; ok {
			t.Fatalf("sanitized source leaked %s: %#v", forbidden, sanitizedSources[0])
		}
	}
	sanitizedStreams, ok := current.Raw["MediaStreams"].([]map[string]any)
	if !ok || len(sanitizedStreams) != 2 {
		t.Fatalf("payload MediaStreams = %#v, want two sanitized streams", current.Raw["MediaStreams"])
	}
	if sanitizedStreams[0]["Codec"] != "hevc" || sanitizedStreams[0]["Width"] == nil {
		t.Fatalf("sanitized stream lost technical fields: %#v", sanitizedStreams[0])
	}
	if _, ok := sanitizedStreams[0]["DeliveryUrl"]; ok {
		t.Fatalf("sanitized stream leaked DeliveryUrl: %#v", sanitizedStreams[0])
	}
	if _, ok := sanitizedStreams[0]["UnknownFutureField"]; ok {
		t.Fatalf("sanitized stream kept an unknown field: %#v", sanitizedStreams[0])
	}
	sanitizedChapters, ok := current.Raw["Chapters"].([]map[string]any)
	if !ok || len(sanitizedChapters) != 1 || sanitizedChapters[0]["Name"] != "Intro" {
		t.Fatalf("payload Chapters = %#v, want one sanitized chapter", current.Raw["Chapters"])
	}
	if _, ok := sanitizedChapters[0]["Id"]; ok {
		t.Fatalf("sanitized chapter leaked source id: %#v", sanitizedChapters[0])
	}
	payloadJSON, _ := json.Marshal(current.Raw)
	for _, forbidden := range []string{"source-item-id", "source-server", `D:\\Movies\\Raw Media Fixture.mkv`, "source-1", "source-chapter"} {
		if strings.Contains(string(payloadJSON), forbidden) {
			t.Fatalf("payload leaked source-bound value %q: %s", forbidden, payloadJSON)
		}
	}
}

func TestImportItemFallsBackAfterMediaTechnicalPayload400AndStillUpdates(t *testing.T) {
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "movie", "info.json"))
	rawRel := filepath.ToSlash(filepath.Join("items", "movie", "raw.json"))
	raw := rawItemWithMediaTechnicalFieldsFixture()
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{Name: "Raw Media Fixture", Type: "Movie", Raw: raw},
	}); err != nil {
		t.Fatalf("WriteJSON info returned error: %v", err)
	}
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(rawRel)), raw); err != nil {
		t.Fatalf("WriteJSON raw returned error: %v", err)
	}

	mediaPayloadAttempts := 0
	degradedAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-id", "Type": "Movie", "Name": "Raw Media Fixture", "Source": "Library"},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode request body: %v", err)
			}
			if _, hasMediaSources := body["MediaSources"]; hasMediaSources {
				mediaPayloadAttempts++
				http.Error(w, "MediaSources is read-only", http.StatusBadRequest)
				return
			}
			if _, hasMediaStreams := body["MediaStreams"]; hasMediaStreams {
				mediaPayloadAttempts++
				http.Error(w, "MediaStreams is read-only", http.StatusBadRequest)
				return
			}
			degradedAttempts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name:     "Raw Media Fixture",
		Type:     "Movie",
		InfoPath: infoRel,
		RawPath:  rawRel,
	}, ImportRequest{ImportMediaInfo: true, MediaInfoMode: "legacy-http"})
	if match.Status != "updated" || match.Error != "" {
		t.Fatalf("media info fallback should still update metadata, got %#v", match)
	}
	if match.MediaInfoUpdated != 0 || match.MediaInfoFailed != 1 || !match.MediaInfoDegraded || match.MediaInfoError == "" {
		t.Fatalf("media info degradation should be reported, got %#v", match)
	}
	if mediaPayloadAttempts != 1 || degradedAttempts != 1 {
		t.Fatalf("unexpected attempts with media=%d degraded=%d", mediaPayloadAttempts, degradedAttempts)
	}
}

func TestImportItemVerifiesPersistedMediaTechnicalFields(t *testing.T) {
	exportPath, infoRel, rawRel := writeMediaInfoImportFixture(t)
	var stored map[string]any
	detailReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "target-id":
			detailReads++
			if stored == nil {
				writeExporterJSON(t, w, detailItemsResponse(map[string]any{"Id": "target-id", "Type": "Movie", "Name": "Raw Media Fixture", "Source": "Library"}))
				return
			}
			writeExporterJSON(t, w, detailItemsResponse(stored))
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, mediaInfoTargetSearchResponse())
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			if err := json.NewDecoder(r.Body).Decode(&stored); err != nil {
				t.Fatalf("failed to decode update body: %v", err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name: "Raw Media Fixture", Type: "Movie", InfoPath: infoRel, RawPath: rawRel,
	}, ImportRequest{ImportMediaInfo: true, MediaInfoMode: "legacy-http", SkipImages: true})
	if match.Status != "updated" || match.MediaInfoUpdated != 1 || match.MediaInfoFailed != 0 {
		t.Fatalf("persisted media info should verify successfully: %#v", match)
	}
	if detailReads != 2 {
		t.Fatalf("detail reads = %d, want before and after write", detailReads)
	}
}

func TestImportDisabledNeverPostsMediaTechnicalFields(t *testing.T) {
	exportPath, infoRel, rawRel := writeMediaInfoImportFixture(t)
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, mediaInfoTargetSearchResponse())
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = server.Client()
	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name: "Raw Media Fixture", Type: "Movie", InfoPath: infoRel, RawPath: rawRel,
	}, ImportRequest{SkipImages: true})
	if match.Status != "updated" {
		t.Fatalf("metadata-only import failed: %#v", match)
	}
	for _, key := range []string{"MediaSources", "MediaStreams", "Chapters"} {
		if _, ok := posted[key]; ok {
			t.Fatalf("disabled import posted %s: %#v", key, posted)
		}
	}
}

func TestImportItemReportsSilentMediaInfoDropInsteadOfFalseSuccess(t *testing.T) {
	exportPath, infoRel, rawRel := writeMediaInfoImportFixture(t)
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "target-id":
			writeExporterJSON(t, w, detailItemsResponse(map[string]any{"Id": "target-id", "Type": "Movie", "Name": "Raw Media Fixture", "Source": "Library"}))
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, mediaInfoTargetSearchResponse())
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			posts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name: "Raw Media Fixture", Type: "Movie", InfoPath: infoRel, RawPath: rawRel,
	}, ImportRequest{ImportMediaInfo: true, MediaInfoMode: "legacy-http", SkipImages: true})
	if match.Status != "updated" || match.MediaInfoUpdated != 0 || match.MediaInfoFailed != 1 || !match.MediaInfoDegraded {
		t.Fatalf("silent drop should be reported as degraded, not success: %#v", match)
	}
	if posts != 1 || match.MediaInfoError == "" {
		t.Fatalf("silent drop diagnostics missing: posts=%d match=%#v", posts, match)
	}
}

func TestImportItemDoesNotOverwriteCompleteTargetMediaInfo(t *testing.T) {
	exportPath, infoRel, rawRel := writeMediaInfoImportFixture(t)
	var posted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "target-id":
			target := rawItemWithMediaTechnicalFieldsFixture()
			target["Id"] = "target-id"
			target["Path"] = `E:\Target\Raw Media Fixture.mkv`
			writeExporterJSON(t, w, detailItemsResponse(target))
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, mediaInfoTargetSearchResponse())
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name: "Raw Media Fixture", Type: "Movie", InfoPath: infoRel, RawPath: rawRel,
	}, ImportRequest{ImportMediaInfo: true, MediaInfoMode: "legacy-http", SkipImages: true})
	if match.Status != "updated" || match.MediaInfoSkipped != 1 || match.MediaInfoUpdated != 0 {
		t.Fatalf("complete target media info should be preserved: %#v", match)
	}
	for _, key := range []string{"MediaSources", "MediaStreams", "Chapters"} {
		if _, ok := posted[key]; ok {
			t.Fatalf("metadata-only update leaked %s into complete target: %#v", key, posted)
		}
	}
}

func TestImportItemDoesNotWriteMediaInfoWhenTargetDetailUnavailable(t *testing.T) {
	exportPath, infoRel, rawRel := writeMediaInfoImportFixture(t)
	var posted map[string]any
	detailReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "target-id":
			detailReads++
			http.Error(w, "target detail unavailable", http.StatusInternalServerError)
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, mediaInfoTargetSearchResponse())
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name: "Raw Media Fixture", Type: "Movie", InfoPath: infoRel, RawPath: rawRel,
	}, ImportRequest{ImportMediaInfo: true, MediaInfoMode: "legacy-http", SkipImages: true})
	if match.Status != "updated" || match.MediaInfoFailed != 1 || !match.MediaInfoDegraded || match.MediaInfoError == "" {
		t.Fatalf("unreadable target media info should degrade safely: %#v", match)
	}
	if detailReads != importRetryAttempts {
		t.Fatalf("detail reads = %d, want %d retries", detailReads, importRetryAttempts)
	}
	for _, key := range []string{"MediaSources", "MediaStreams", "Chapters"} {
		if _, ok := posted[key]; ok {
			t.Fatalf("unreadable target detail must not post %s: %#v", key, posted)
		}
	}
}

func TestMediaInfoVerificationRequiresDistinctArrayMatches(t *testing.T) {
	actual := map[string]any{
		"MediaStreams": []map[string]any{{"Type": "Video", "Codec": "h264"}},
	}
	expected := map[string]any{
		"MediaStreams": []map[string]any{
			{"Type": "Video", "Codec": "h264"},
			{"Type": "Video", "Codec": "h264"},
		},
	}
	if mediaInfoPayloadContains(actual, expected) {
		t.Fatalf("one actual stream must not satisfy two expected streams")
	}
}

func TestDatabaseMediaInfoModeAndVersionGuard(t *testing.T) {
	if !databaseMediaInfoEnabled(ImportRequest{ImportMediaInfo: true}) {
		t.Fatalf("database mode should be the default when media info is enabled")
	}
	if databaseMediaInfoEnabled(ImportRequest{ImportMediaInfo: true, MediaInfoMode: "legacy-http"}) {
		t.Fatalf("legacy HTTP regression mode must not generate a database plan")
	}
	for _, versions := range [][2]string{{"4.8.11.0", "4.8.11.0"}, {"4.9.5.0", "4.9.5.1"}} {
		if !sameEmbyMinorSeries(versions[0], versions[1]) {
			t.Fatalf("same version series should be accepted: %#v", versions)
		}
	}
	if sameEmbyMinorSeries("4.8.11.0", "4.9.5.0") {
		t.Fatalf("cross-version media database restore must be rejected")
	}
	if sameEmbyMinorSeries("4.8.10.0", "4.8.11.0") {
		t.Fatalf("untested 4.8 database schemas must be rejected")
	}
}

func TestImportItemFallsBackAfterMediaTechnicalPayload500(t *testing.T) {
	exportPath, infoRel, rawRel := writeMediaInfoImportFixture(t)
	mediaAttempts := 0
	metadataAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("Ids") == "target-id":
			writeExporterJSON(t, w, detailItemsResponse(map[string]any{"Id": "target-id", "Type": "Movie", "Name": "Raw Media Fixture", "Source": "Library"}))
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, mediaInfoTargetSearchResponse())
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if _, ok := body["MediaSources"]; ok {
				mediaAttempts++
				http.Error(w, "temporary media payload failure", http.StatusInternalServerError)
				return
			}
			metadataAttempts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name: "Raw Media Fixture", Type: "Movie", InfoPath: infoRel, RawPath: rawRel,
	}, ImportRequest{ImportMediaInfo: true, MediaInfoMode: "legacy-http", SkipImages: true})
	if match.Status != "updated" || match.MediaInfoFailed != 1 || match.Error != "" {
		t.Fatalf("HTTP 500 media payload should degrade to metadata-only update: %#v", match)
	}
	if mediaAttempts != importRetryAttempts || metadataAttempts != 1 {
		t.Fatalf("attempts media=%d metadata=%d, want %d/1", mediaAttempts, metadataAttempts, importRetryAttempts)
	}
}

func writeMediaInfoImportFixture(t *testing.T) (string, string, string) {
	t.Helper()
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "movie", "info.json"))
	rawRel := filepath.ToSlash(filepath.Join("items", "movie", "raw.json"))
	raw := rawItemWithMediaTechnicalFieldsFixture()
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{Name: "Raw Media Fixture", Type: "Movie", Raw: raw},
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(rawRel)), raw); err != nil {
		t.Fatal(err)
	}
	return exportPath, infoRel, rawRel
}

func mediaInfoTargetSearchResponse() map[string]any {
	return map[string]any{
		"Items":            []map[string]any{{"Id": "target-id", "Type": "Movie", "Name": "Raw Media Fixture", "Source": "Library"}},
		"TotalRecordCount": 1,
	}
}

func detailItemsResponse(item map[string]any) map[string]any {
	return map[string]any{"Items": []map[string]any{item}, "TotalRecordCount": 1}
}

func TestImportItemStillUploadsImagesWhenMetadataUpdateFails(t *testing.T) {
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "movie", "info.json"))
	imageRel := filepath.ToSlash(filepath.Join("items", "movie", "primary.jpg"))
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{Name: "Movie With Poster", Type: "Movie", Overview: "source overview"},
	}); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}
	if _, err := storage.WriteBytes(filepath.Join(exportPath, filepath.FromSlash(imageRel)), []byte("fake-image-bytes")); err != nil {
		t.Fatalf("WriteBytes returned error: %v", err)
	}

	updateAttempts := 0
	imageUploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-id", "Type": "Movie", "Name": "Movie With Poster"},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			updateAttempts++
			http.Error(w, "SQLitePCL.pretty.SQLiteException", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id/Images/Primary":
			imageUploads++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name:     "Movie With Poster",
		Type:     "Movie",
		InfoPath: infoRel,
		Images:   []storage.FileEntry{{Type: "Primary", Path: imageRel}},
	}, ImportRequest{ImageTypes: []string{"Primary"}})
	if match.Status != "failed" || match.Error == "" {
		t.Fatalf("metadata failure should be reported, got %#v", match)
	}
	if match.ImagesPushed != 1 || imageUploads != 1 {
		t.Fatalf("image should still upload after metadata failure, match=%#v uploads=%d", match, imageUploads)
	}
	if updateAttempts != importRetryAttempts {
		t.Fatalf("metadata update attempts = %d, want %d", updateAttempts, importRetryAttempts)
	}
}

func TestImportItemFallsBackToMinimalMetadataPayloadOnSourceNullError(t *testing.T) {
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "episode", "info.json"))
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{
			Name:           "Episode With Strict Payload",
			Type:           "Episode",
			Overview:       "source overview",
			ProductionYear: 2026,
			Studios:        []emby.NameID{{Name: "Studio One"}},
		},
	}); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}

	fullAttempts := 0
	minimalAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-id", "Type": "Episode", "Name": "Episode With Strict Payload"},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("failed to decode request body: %v", err)
			}
			if _, hasStudios := body["Studios"]; hasStudios {
				fullAttempts++
				http.Error(w, "Value cannot be null. (Parameter 'source')", http.StatusBadRequest)
				return
			}
			minimalAttempts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name:     "Episode With Strict Payload",
		Type:     "Episode",
		InfoPath: infoRel,
	}, ImportRequest{})
	if match.Status != "updated" || match.Error != "" {
		t.Fatalf("fallback should make metadata update succeed, got %#v", match)
	}
	if fullAttempts != 1 || minimalAttempts != 1 {
		t.Fatalf("attempts full=%d minimal=%d, want 1/1", fullAttempts, minimalAttempts)
	}
}

func TestImportItemUsesTargetLibraryIDsForMatchSearch(t *testing.T) {
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "movie", "info.json"))
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{Name: "Scoped Provider Movie", Type: "Movie"},
	}); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}

	var parentIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			parentIDs = append(parentIDs, r.URL.Query().Get("ParentId"))
			if got := r.URL.Query().Get("AnyProviderIdEquals"); got != "Tmdb.123" {
				http.Error(w, "unexpected provider id "+got, http.StatusBadRequest)
				return
			}
			if got := r.URL.Query().Get("ParentId"); got != "lib-target" {
				http.Error(w, "unexpected ParentId "+got, http.StatusBadRequest)
				return
			}
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-id", "Type": "Movie", "Name": "Scoped Provider Movie", "ProviderIds": map[string]string{"Tmdb": "123"}},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name:        "Scoped Provider Movie",
		Type:        "Movie",
		InfoPath:    infoRel,
		ProviderIDs: map[string]string{"Tmdb": "123"},
	}, ImportRequest{TargetLibraryIDs: []string{"lib-target"}})
	if match.Status != "updated" || match.TargetID != "target-id" || match.Error != "" {
		t.Fatalf("importItem did not match/update scoped target: %#v", match)
	}
	if !reflect.DeepEqual(parentIDs, []string{"lib-target"}) {
		t.Fatalf("match search ParentId = %#v, want lib-target", parentIDs)
	}
}

func TestImportItemUsesLegacyLibraryIDsForMatchSearch(t *testing.T) {
	exportPath := t.TempDir()
	infoRel := filepath.ToSlash(filepath.Join("items", "movie", "info.json"))
	if err := storage.WriteJSON(filepath.Join(exportPath, filepath.FromSlash(infoRel)), storage.ItemInfo{
		Item: emby.Item{Name: "Legacy Scoped Provider Movie", Type: "Movie"},
	}); err != nil {
		t.Fatalf("WriteJSON returned error: %v", err)
	}

	var parentIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			parentIDs = append(parentIDs, r.URL.Query().Get("ParentId"))
			if got := r.URL.Query().Get("ParentId"); got != "legacy-lib" {
				http.Error(w, "unexpected ParentId "+got, http.StatusBadRequest)
				return
			}
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-id", "Type": "Movie", "Name": "Legacy Scoped Provider Movie", "ProviderIds": map[string]string{"Tmdb": "456"}},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/target-id":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	match := NewService(exportPath).importItem(context.Background(), client, newImportLookupCache(), exportPath, storage.ItemEntry{
		Name:        "Legacy Scoped Provider Movie",
		Type:        "Movie",
		InfoPath:    infoRel,
		ProviderIDs: map[string]string{"Tmdb": "456"},
	}, ImportRequest{LibraryIDs: []string{"legacy-lib"}})
	if match.Status != "updated" || match.TargetID != "target-id" || match.Error != "" {
		t.Fatalf("importItem did not match/update legacy scoped target: %#v", match)
	}
	if !reflect.DeepEqual(parentIDs, []string{"legacy-lib"}) {
		t.Fatalf("match search ParentId = %#v, want legacy-lib", parentIDs)
	}
}

func TestImportLookupCacheReusesProviderIDSearches(t *testing.T) {
	providerQueries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("AnyProviderIdEquals") == "Tmdb.123":
			providerQueries++
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-id", "Type": "Movie", "Name": "Cached Provider Movie", "ProviderIds": map[string]string{"Tmdb": "123"}},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	cache := newImportLookupCache()
	entry := storage.ItemEntry{
		Type:        "Movie",
		Name:        "Cached Provider Movie",
		ProviderIDs: map[string]string{"Tmdb": "123"},
	}
	for i := 0; i < 2; i++ {
		target, _, reason, err := findMatchWithCache(context.Background(), client, cache, entry)
		if err != nil {
			t.Fatalf("findMatchWithCache returned error: %v", err)
		}
		if target.ID != "target-id" || reason != "provider-id" {
			t.Fatalf("target=%#v reason=%q, want provider-id target", target, reason)
		}
	}
	if providerQueries != 1 {
		t.Fatalf("provider ID search count = %d, want 1", providerQueries)
	}
}

func TestImportLookupCacheSeparatesProviderIDSearchesByTargetLibraries(t *testing.T) {
	providerQueriesByParent := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("AnyProviderIdEquals") == "Tmdb.123":
			parentID := r.URL.Query().Get("ParentId")
			providerQueriesByParent[parentID]++
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-" + parentID, "Type": "Movie", "Name": "Cached Provider Movie", "ProviderIds": map[string]string{"Tmdb": "123"}},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	cache := newImportLookupCache()
	entry := storage.ItemEntry{
		Type:        "Movie",
		Name:        "Cached Provider Movie",
		ProviderIDs: map[string]string{"Tmdb": "123"},
	}
	target, _, reason, err := findMatchWithCache(context.Background(), client, cache, entry, []string{"lib-a"})
	if err != nil {
		t.Fatalf("findMatchWithCache lib-a returned error: %v", err)
	}
	if target.ID != "target-lib-a" || reason != "provider-id" {
		t.Fatalf("lib-a target=%#v reason=%q, want provider-id target", target, reason)
	}
	target, _, reason, err = findMatchWithCache(context.Background(), client, cache, entry, []string{"lib-b"})
	if err != nil {
		t.Fatalf("findMatchWithCache lib-b returned error: %v", err)
	}
	if target.ID != "target-lib-b" || reason != "provider-id" {
		t.Fatalf("lib-b target=%#v reason=%q, want provider-id target", target, reason)
	}
	target, _, reason, err = findMatchWithCache(context.Background(), client, cache, entry, []string{"lib-a"})
	if err != nil {
		t.Fatalf("findMatchWithCache cached lib-a returned error: %v", err)
	}
	if target.ID != "target-lib-a" || reason != "provider-id" {
		t.Fatalf("cached lib-a target=%#v reason=%q, want provider-id target", target, reason)
	}
	if !reflect.DeepEqual(providerQueriesByParent, map[string]int{"lib-a": 1, "lib-b": 1}) {
		t.Fatalf("provider queries by ParentId = %#v, want one per library", providerQueriesByParent)
	}
}

func TestFindMatchWithoutTargetLibrariesKeepsUnscopedProviderIDSearch(t *testing.T) {
	var parentIDs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Items" && r.URL.Query().Get("AnyProviderIdEquals") == "Tmdb.123":
			parentIDs = append(parentIDs, r.URL.Query().Get("ParentId"))
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "target-id", "Type": "Movie", "Name": "Unscoped Provider Movie", "ProviderIds": map[string]string{"Tmdb": "123"}},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	target, _, reason, err := findMatchWithCache(context.Background(), client, newImportLookupCache(), storage.ItemEntry{
		Type:        "Movie",
		Name:        "Unscoped Provider Movie",
		ProviderIDs: map[string]string{"Tmdb": "123"},
	})
	if err != nil {
		t.Fatalf("findMatchWithCache returned error: %v", err)
	}
	if target.ID != "target-id" || reason != "provider-id" {
		t.Fatalf("target=%#v reason=%q, want provider-id target", target, reason)
	}
	if !reflect.DeepEqual(parentIDs, []string{""}) {
		t.Fatalf("unscoped provider search should omit ParentId, got %#v", parentIDs)
	}
}

func TestImportPersonImageCachesPersonLookupByName(t *testing.T) {
	exportPath := t.TempDir()
	imageRel := "people/actor/primary.jpg"
	if _, err := storage.WriteBytes(filepath.Join(exportPath, filepath.FromSlash(imageRel)), []byte("avatar")); err != nil {
		t.Fatal(err)
	}
	personQueries := 0
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Persons":
			personQueries++
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "person-target-id", "Name": "Actor One"},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/person-target-id/Images/Primary":
			uploads++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	cache := newImportLookupCache()
	service := NewService(exportPath)
	for i := 0; i < 2; i++ {
		result := service.importPersonImage(context.Background(), client, cache, exportPath, personImageTask{Name: "Actor One", Path: imageRel})
		if result.Err != nil {
			t.Fatalf("importPersonImage returned error: %v", result.Err)
		}
	}
	if personQueries != 1 {
		t.Fatalf("person lookup count = %d, want 1", personQueries)
	}
	if uploads != 2 {
		t.Fatalf("uploads = %d, want 2", uploads)
	}
}

func TestImportPeopleImagesRecordsAndResumesPersonAvatarCheckpoint(t *testing.T) {
	exportPath := t.TempDir()
	firstImageRel := "people/actor-one/primary.jpg"
	secondImageRel := "people/actor-two/primary.jpg"
	for _, rel := range []string{firstImageRel, secondImageRel} {
		if _, err := storage.WriteBytes(filepath.Join(exportPath, filepath.FromSlash(rel)), []byte("avatar")); err != nil {
			t.Fatal(err)
		}
	}
	target := ImportTarget{ServerID: "target-a", ServerName: "Target A", Version: "4.9.5"}
	checkpoint := newImportCheckpointStore(exportPath, target)
	if err := checkpoint.RecordPersonAvatar(personImageResult{StableKey: "person-one", Name: "Actor One", TargetID: "person-target-one"}); err != nil {
		t.Fatal(err)
	}

	personQueries := 0
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/Persons":
			personQueries++
			if got := r.URL.Query().Get("SearchTerm"); got != "Actor Two" {
				http.Error(w, "unexpected person search "+got, http.StatusBadRequest)
				return
			}
			writeExporterJSON(t, w, map[string]any{
				"Items": []map[string]any{
					{"Id": "person-target-two", "Name": "Actor Two"},
				},
				"TotalRecordCount": 1,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Items/person-target-two/Images/Primary":
			uploads++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, r.Method+" "+r.URL.String(), http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	manager := job.NewManager()
	j := manager.Create("import")
	j.Start()
	report := ImportReport{Target: target}
	manifest := storage.Manifest{
		People: []storage.PersonEntry{
			{StableKey: "person-one", Name: "Actor One", Image: &storage.FileEntry{Type: "Primary", Path: firstImageRel}},
			{StableKey: "person-two", Name: "Actor Two", Image: &storage.FileEntry{Type: "Primary", Path: secondImageRel}},
		},
	}

	service := NewService(exportPath)
	err = service.importPeopleImages(context.Background(), client, newImportLookupCache(), checkpoint, exportPath, manifest, &report, j, true, false, 1)
	if err != nil {
		t.Fatalf("importPeopleImages returned error: %v", err)
	}
	if personQueries != 1 || uploads != 1 {
		t.Fatalf("resume should skip first avatar and upload second, queries=%d uploads=%d", personQueries, uploads)
	}
	if report.Skips == nil || report.Skips.Resume != 1 {
		t.Fatalf("resume skip should be counted, skips=%#v", report.Skips)
	}
	if report.Summary.PeopleImages != 1 || report.Summary.PeopleImagesFailed != 0 {
		t.Fatalf("unexpected people image summary: %#v", report.Summary)
	}
	stored, ok := readImportCheckpoint(filepath.Join(exportPath, "import-checkpoint.json"), target)
	if !ok {
		t.Fatalf("checkpoint should be readable")
	}
	if !shouldResumePersonAvatarCheckpoint(stored.PersonAvatars["person-one"]) ||
		!shouldResumePersonAvatarCheckpoint(stored.PersonAvatars["person-two"]) {
		t.Fatalf("successful avatar checkpoints not recorded: %#v", stored.PersonAvatars)
	}
}

func TestFindMatchReturnsSearchErrorInsteadOfFalseUnmatched(t *testing.T) {
	client := newFindMatchTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary upstream failure", http.StatusBadGateway)
	})

	target, candidates, reason, err := FindMatch(context.Background(), client, storage.ItemEntry{
		Type: "Movie",
		Name: "Network Sensitive Movie",
	})
	if err == nil {
		t.Fatalf("FindMatch should surface search errors instead of returning no-match")
	}
	if target.ID != "" || len(candidates) != 0 || reason != "search-error" {
		t.Fatalf("search failure should be reported as search-error, got target=%#v reason=%q candidates=%#v", target, reason, candidates)
	}
}

func TestRetryWithTimeoutRetriesTransientErrors(t *testing.T) {
	attempts := 0
	err := retryWithTimeout(context.Background(), 2, time.Second, func(ctx context.Context) error {
		attempts++
		if attempts == 1 {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryWithTimeout returned error after transient retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("retryWithTimeout attempts = %d, want 2", attempts)
	}
}

func TestRetryWithTimeoutDoesNotRetryPermanentErrors(t *testing.T) {
	attempts := 0
	err := retryWithTimeout(context.Background(), 2, time.Second, func(ctx context.Context) error {
		attempts++
		return context.Canceled
	})
	if err == nil {
		t.Fatalf("retryWithTimeout should return permanent error")
	}
	if attempts != 1 {
		t.Fatalf("permanent error should not be retried, attempts = %d", attempts)
	}
}

func TestShortNameMatchesOriginalScriptCleanup(t *testing.T) {
	if got := ShortName("Movie Name (2024) - 2160p WEB-DL"); got != "Movie Name" {
		t.Fatalf("ShortName returned %q", got)
	}
	if got := ShortName("Movie Name 2024 {tmdb-123}"); got != "Movie Name" {
		t.Fatalf("ShortName returned %q", got)
	}
}

func TestMatchPersonForImportUsesProviderOrNameNotOldEmbyInternalID(t *testing.T) {
	source := ExportedPerson{
		StableKey:   "person-provider-imdb-nm0000206",
		EmbyID:      "old-person-id",
		Name:        "Keanu Reeves",
		Type:        "Actor",
		Role:        "Neo",
		ProviderIDs: map[string]string{"Imdb": "nm0000206"},
	}
	candidates := []ImportPersonCandidate{
		{
			EmbyID:      "old-person-id",
			Name:        "Different Person With Reused Old ID",
			ProviderIDs: map[string]string{"Imdb": "nm9999999"},
		},
		{
			EmbyID:      "new-person-provider-match",
			Name:        "Keanu Reeves",
			ProviderIDs: map[string]string{"Imdb": "nm0000206"},
		},
	}

	match := MatchPersonForImport(source, candidates)
	if match.TargetEmbyID != "new-person-provider-match" {
		t.Fatalf("person import should choose provider/name match, got %#v", match)
	}
	if match.TargetEmbyID == source.EmbyID {
		t.Fatalf("old Emby person ID must not be used for cross-server matching: %#v", match)
	}
	if match.Strategy != "provider_ids" {
		t.Fatalf("person match strategy = %q, want provider_ids", match.Strategy)
	}
}

func TestRunImportDryRunDoesNotWriteMetadataItemImagesOrPeopleImages(t *testing.T) {
	client := &recordingImportClient{
		candidates: []ImportCandidate{
			{
				EmbyID:      "target-1",
				Type:        "Movie",
				Name:        "The Matrix",
				ProviderIDs: map[string]string{"Imdb": "tt0133093"},
			},
		},
		personCandidates: []ImportPersonCandidate{
			{
				EmbyID:      "target-person-1",
				Name:        "Keanu Reeves",
				ProviderIDs: map[string]string{"Imdb": "nm0000206"},
			},
		},
	}
	pkg := ExportPackage{
		Items: []ExportedItem{
			{
				StableKey:   "provider-imdb-tt0133093",
				EmbyID:      "old-1",
				Type:        "Movie",
				Name:        "The Matrix",
				ProviderIDs: map[string]string{"Imdb": "tt0133093"},
				Images: []ExportedImage{
					{Type: "Primary", File: "primary.jpg"},
				},
				People: []ExportedPerson{
					{
						StableKey:   "person-provider-imdb-nm0000206",
						EmbyID:      "old-person-1",
						Name:        "Keanu Reeves",
						Type:        "Actor",
						Role:        "Neo",
						ProviderIDs: map[string]string{"Imdb": "nm0000206"},
						Image:       &ExportedImage{Type: "Primary", File: "people/person-provider-imdb-nm0000206/primary.jpg"},
					},
				},
			},
		},
		People: []ExportedPerson{
			{
				StableKey:   "person-provider-imdb-nm0000206",
				EmbyID:      "old-person-1",
				Name:        "Keanu Reeves",
				Type:        "Actor",
				Role:        "Neo",
				ProviderIDs: map[string]string{"Imdb": "nm0000206"},
				Image:       &ExportedImage{Type: "Primary", File: "people/person-provider-imdb-nm0000206/primary.jpg"},
			},
		},
	}

	report, err := RunImport(context.Background(), pkg, client, ImportOptions{
		DryRun:              true,
		ImageTypes:          []string{"Primary"},
		IncludePeopleImages: true,
	})
	if err != nil {
		t.Fatalf("RunImport dry-run returned error: %v", err)
	}
	if client.searches != 1 {
		t.Fatalf("RunImport should search once, searched %d times", client.searches)
	}
	if client.personSearches != 1 {
		t.Fatalf("RunImport should search person once, searched %d times", client.personSearches)
	}
	if len(client.updated) != 0 {
		t.Fatalf("dry-run should not update metadata, updated %#v", client.updated)
	}
	if len(client.uploaded) != 0 {
		t.Fatalf("dry-run should not upload item images, uploaded %#v", client.uploaded)
	}
	if len(client.personUploaded) != 0 {
		t.Fatalf("dry-run should not upload people images, uploaded %#v", client.personUploaded)
	}
	if len(report.Matches) != 1 || report.Matches[0].TargetEmbyID != "target-1" {
		t.Fatalf("dry-run should still report the selected item match, got %#v", report)
	}
	if len(report.PersonMatches) != 1 || report.PersonMatches[0].TargetEmbyID != "target-person-1" {
		t.Fatalf("dry-run should still report the selected person match, got %#v", report)
	}
	if report.WritesSkipped == 0 {
		t.Fatalf("dry-run report should make skipped writes visible: %#v", report)
	}
}

type recordingImportClient struct {
	candidates       []ImportCandidate
	personCandidates []ImportPersonCandidate
	searches         int
	personSearches   int
	updated          []string
	uploaded         []string
	personUploaded   []string
}

func (c *recordingImportClient) SearchCandidates(ctx context.Context, item ExportedItem) ([]ImportCandidate, error) {
	c.searches++
	return c.candidates, nil
}

func (c *recordingImportClient) SearchPersonCandidates(ctx context.Context, person ExportedPerson) ([]ImportPersonCandidate, error) {
	c.personSearches++
	return c.personCandidates, nil
}

func (c *recordingImportClient) UpdateItem(ctx context.Context, targetEmbyID string, item ExportedItem) error {
	c.updated = append(c.updated, targetEmbyID)
	return nil
}

func (c *recordingImportClient) UploadImage(ctx context.Context, targetEmbyID string, image ExportedImage) error {
	c.uploaded = append(c.uploaded, targetEmbyID+":"+image.Type)
	return nil
}

func (c *recordingImportClient) UploadPersonImage(ctx context.Context, targetPersonEmbyID string, image ExportedImage) error {
	c.personUploaded = append(c.personUploaded, targetPersonEmbyID+":"+image.Type)
	return nil
}

func exportAssetSignatures(assets []ExportAsset) []string {
	out := make([]string, 0, len(assets))
	for _, asset := range assets {
		out = append(out, asset.Scope+":"+asset.OwnerID+":"+asset.ImageType)
	}
	return out
}

func newFindMatchTestClient(t *testing.T, handler http.HandlerFunc) *emby.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := emby.NewClient(server.URL, "test-key")
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()
	return client
}

func rawItemWithMediaTechnicalFieldsFixture() map[string]any {
	videoStream := map[string]any{
		"Index": 0,
		"Type":  "Video",
		"Codec": "hevc",
		"Width": 3840,
	}
	audioStream := map[string]any{
		"Index":    1,
		"Type":     "Audio",
		"Codec":    "aac",
		"Language": "jpn",
	}
	return map[string]any{
		"Id":          "item-raw-media",
		"Name":        "Raw Media Fixture",
		"Type":        "Movie",
		"Path":        `D:\Movies\Raw Media Fixture.mkv`,
		"ProviderIds": map[string]string{"Tmdb": "987654"},
		"MediaSources": []map[string]any{
			{
				"Id":        "source-1",
				"Path":      `D:\Movies\Raw Media Fixture.mkv`,
				"Protocol":  "File",
				"Container": "mkv",
				"MediaStreams": []map[string]any{
					videoStream,
					audioStream,
				},
			},
		},
		"MediaStreams": []map[string]any{
			videoStream,
			audioStream,
		},
		"Chapters": []map[string]any{
			{"StartPositionTicks": 0, "Name": "Intro", "Id": "source-chapter", "ImagePath": `D:\Movies\chapter.jpg`},
		},
	}
}

func assertRawMediaTechnicalFields(t *testing.T, raw map[string]any) {
	t.Helper()
	sources, ok := raw["MediaSources"].([]any)
	if !ok || len(sources) != 1 {
		t.Fatalf("Raw.MediaSources = %#v, want one media source", raw["MediaSources"])
	}
	source, ok := sources[0].(map[string]any)
	if !ok {
		t.Fatalf("Raw.MediaSources[0] = %#v, want object", sources[0])
	}
	if source["Id"] != "source-1" || source["Protocol"] != "File" || source["Container"] != "mkv" {
		t.Fatalf("Raw.MediaSources[0] lost scalar fields: %#v", source)
	}
	nestedStreams, ok := source["MediaStreams"].([]any)
	if !ok || len(nestedStreams) != 2 {
		t.Fatalf("Raw.MediaSources[0].MediaStreams = %#v, want two streams", source["MediaStreams"])
	}

	streams, ok := raw["MediaStreams"].([]any)
	if !ok || len(streams) != 2 {
		t.Fatalf("Raw.MediaStreams = %#v, want two streams", raw["MediaStreams"])
	}
	video, ok := streams[0].(map[string]any)
	if !ok {
		t.Fatalf("Raw.MediaStreams[0] = %#v, want object", streams[0])
	}
	audio, ok := streams[1].(map[string]any)
	if !ok {
		t.Fatalf("Raw.MediaStreams[1] = %#v, want object", streams[1])
	}
	if video["Type"] != "Video" || video["Codec"] != "hevc" {
		t.Fatalf("Raw.MediaStreams[0] lost video stream fields: %#v", video)
	}
	if audio["Type"] != "Audio" || audio["Codec"] != "aac" || audio["Language"] != "jpn" {
		t.Fatalf("Raw.MediaStreams[1] lost audio stream fields: %#v", audio)
	}
	chapters, ok := raw["Chapters"].([]any)
	if !ok || len(chapters) != 1 {
		t.Fatalf("Raw.Chapters = %#v, want one chapter", raw["Chapters"])
	}
}

func rawPayloadFromItemInfo(t *testing.T, info storage.ItemInfo) map[string]any {
	t.Helper()
	if _, ok := info.Item.Raw["MediaSources"]; ok {
		return info.Item.Raw
	}
	raw, ok := info.Item.Raw["Raw"].(map[string]any)
	if !ok {
		t.Fatalf("ItemInfo.Item.Raw did not preserve nested raw payload: %#v", info.Item.Raw)
	}
	return raw
}

func writeExporterJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("failed to write JSON response: %v", err)
	}
}

func writeExporterImage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write([]byte("fake-image-bytes"))
}
