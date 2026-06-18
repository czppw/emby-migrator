package exporter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"emby-migrator/internal/emby"
	"emby-migrator/internal/job"
	"emby-migrator/internal/storage"
)

type Service struct {
	DataDir string
}

type ExportRequest struct {
	Connection          emby.Connection `json:"connection"`
	Libraries           []emby.Library  `json:"libraries"`
	LibraryIDs          []string        `json:"libraryIds"`
	Concurrency         int             `json:"concurrency"`
	SkipImages          bool            `json:"skipImages"`
	IncludePeopleImages bool            `json:"includePeopleImages"`
	Incremental         bool            `json:"incremental"`
	Overwrite           bool            `json:"overwrite"`
	ImageTypes          []string        `json:"imageTypes"`
	ToolVersion         string          `json:"toolVersion"`
}

type ImportRequest struct {
	Connection          emby.Connection `json:"connection"`
	ExportPath          string          `json:"exportPath"`
	TargetLibraryIDs    []string        `json:"targetLibraryIds,omitempty"`
	LibraryIDs          []string        `json:"libraryIds,omitempty"`
	Concurrency         int             `json:"concurrency"`
	DryRun              bool            `json:"dryRun"`
	SkipImages          bool            `json:"skipImages"`
	IncludePeopleImages bool            `json:"includePeopleImages"`
	Overwrite           bool            `json:"overwrite"`
	Resume              bool            `json:"resume"`
	ImageTypes          []string        `json:"imageTypes"`
	ToolVersion         string          `json:"toolVersion"`
}

type ExportResult struct {
	Path     string           `json:"path"`
	Manifest storage.Manifest `json:"manifest"`
}

type ImportResult struct {
	Path     string           `json:"path"`
	Report   ImportReport     `json:"report"`
	Manifest storage.Manifest `json:"manifest"`
}

type ImportReportInfo struct {
	ExportName    string               `json:"exportName"`
	Name          string               `json:"name"`
	Path          string               `json:"path"`
	Size          int64                `json:"size"`
	ModifiedAt    time.Time            `json:"modifiedAt"`
	DryRun        bool                 `json:"dryRun"`
	StartedAt     time.Time            `json:"startedAt,omitempty"`
	EndedAt       time.Time            `json:"endedAt,omitempty"`
	Target        ImportTarget         `json:"target,omitempty"`
	Compatibility CompatibilityProfile `json:"compatibility"`
	Summary       storage.Summary      `json:"summary"`
}

type PackageValidation struct {
	CheckedFiles       int      `json:"checkedFiles"`
	MissingFiles       int      `json:"missingFiles"`
	InvalidPaths       int      `json:"invalidPaths"`
	SizeMismatches     int      `json:"sizeMismatches"`
	ChecksumMismatches int      `json:"checksumMismatches"`
	Errors             []string `json:"errors,omitempty"`
}

type CompatibilityProfile struct {
	Name          string   `json:"name"`
	SourceVersion string   `json:"sourceVersion,omitempty"`
	TargetVersion string   `json:"targetVersion,omitempty"`
	Notes         []string `json:"notes,omitempty"`
}

type ImportReport struct {
	StartedAt     time.Time            `json:"startedAt"`
	EndedAt       time.Time            `json:"endedAt"`
	DryRun        bool                 `json:"dryRun"`
	Target        ImportTarget         `json:"target,omitempty"`
	Compatibility CompatibilityProfile `json:"compatibility"`
	Diff          ImportDiff           `json:"diff,omitempty"`
	Incremental   *ImportIncremental   `json:"incremental,omitempty"`
	Skips         *ImportSkipReport    `json:"skips,omitempty"`
	Failures      FailureReport        `json:"failures,omitempty"`
	Matches       []ImportMatch        `json:"matches"`
	PersonMatches []ImportMatch        `json:"personMatches,omitempty"`
	Summary       storage.Summary      `json:"summary"`
	WritesSkipped int                  `json:"writesSkipped,omitempty"`
}

type ImportTarget struct {
	ServerName string `json:"serverName,omitempty"`
	ServerID   string `json:"serverId,omitempty"`
	Version    string `json:"version,omitempty"`
	BaseURL    string `json:"baseUrl,omitempty"`
}

type ImportDiff struct {
	Mode     string          `json:"mode,omitempty"`
	Note     string          `json:"note,omitempty"`
	Expected storage.Summary `json:"expected,omitempty"`
	Actual   storage.Summary `json:"actual,omitempty"`
	Missing  ImportDiffGap   `json:"missing,omitempty"`
	Before   storage.Summary `json:"before,omitempty"`
	After    storage.Summary `json:"after,omitempty"`
}

type ImportDiffGap struct {
	Metadata     int `json:"metadata,omitempty"`
	ItemImages   int `json:"itemImages,omitempty"`
	PeopleImages int `json:"peopleImages,omitempty"`
	Unmatched    int `json:"unmatched,omitempty"`
	Ambiguous    int `json:"ambiguous,omitempty"`
	Errors       int `json:"errors,omitempty"`
}

type ImportIncremental struct {
	Source             string `json:"source,omitempty"`
	BaselineExportName string `json:"baselineExportName,omitempty"`
	BaselineExportPath string `json:"baselineExportPath,omitempty"`
	ChangedItems       int    `json:"changedItems,omitempty"`
	SkippedItems       int    `json:"skippedItems,omitempty"`
	TargetMode         string `json:"targetMode,omitempty"`
	Note               string `json:"note,omitempty"`
}

type ImportSkipReport struct {
	Total               int `json:"total,omitempty"`
	IncrementalManifest int `json:"incrementalManifest,omitempty"`
	Resume              int `json:"resume,omitempty"`
	DryRunWrites        int `json:"dryRunWrites,omitempty"`
}

type FailureReport struct {
	Severity          string           `json:"severity,omitempty"`
	All               []FailureExample `json:"all,omitempty"`
	Unmatched         []FailureExample `json:"unmatched,omitempty"`
	Ambiguous         []FailureExample `json:"ambiguous,omitempty"`
	Failed            []FailureExample `json:"failed,omitempty"`
	ImageFailed       []FailureExample `json:"imageFailed,omitempty"`
	PersonImageFailed []FailureExample `json:"personImageFailed,omitempty"`
	Counts            *FailureCounts   `json:"counts,omitempty"`
	Total             int              `json:"total,omitempty"`
	Truncated         bool             `json:"truncated,omitempty"`
}

type FailureCounts struct {
	Unmatched         int `json:"unmatched,omitempty"`
	Ambiguous         int `json:"ambiguous,omitempty"`
	Failed            int `json:"failed,omitempty"`
	ImageFailed       int `json:"imageFailed,omitempty"`
	PersonImageFailed int `json:"personImageFailed,omitempty"`
}

type FailureExample struct {
	StableKey  string   `json:"stableKey,omitempty"`
	SourceName string   `json:"sourceName,omitempty"`
	Status     string   `json:"status,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Error      string   `json:"error,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
}

type importCheckpoint struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Target        ImportTarget                `json:"target,omitempty"`
	UpdatedAt     time.Time                   `json:"updatedAt"`
	Items         map[string]ImportCheckpoint `json:"items,omitempty"`
	PersonAvatars map[string]ImportCheckpoint `json:"personAvatars,omitempty"`
}

type ImportCheckpoint struct {
	StableKey     string    `json:"stableKey"`
	SourceName    string    `json:"sourceName,omitempty"`
	TargetID      string    `json:"targetId,omitempty"`
	TargetName    string    `json:"targetName,omitempty"`
	Status        string    `json:"status"`
	ImageFailures int       `json:"imageFailures,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type ImportMatch struct {
	StableKey     string   `json:"stableKey"`
	SourceName    string   `json:"sourceName"`
	TargetID      string   `json:"targetId,omitempty"`
	TargetEmbyID  string   `json:"targetEmbyId,omitempty"`
	TargetName    string   `json:"targetName,omitempty"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	Strategy      string   `json:"strategy,omitempty"`
	Candidates    []string `json:"candidates,omitempty"`
	ImagesPushed  int      `json:"imagesPushed,omitempty"`
	ImageFailures int      `json:"imageFailures,omitempty"`
	ImageErrors   []string `json:"imageErrors,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type ExportedItem struct {
	StableKey      string
	EmbyID         string
	Type           string
	Name           string
	OriginalTitle  string
	ProductionYear int
	ProviderIDs    map[string]string
	SeriesName     string
	SeasonNumber   int
	EpisodeNumber  int
	ImageTags      map[string]string
	Images         []ExportedImage
	People         []ExportedPerson
}

type ExportedImage struct {
	Type string
	File string
}

type ExportedPerson struct {
	StableKey       string
	EmbyID          string
	Name            string
	Type            string
	Role            string
	ProviderIDs     map[string]string
	PrimaryImageTag string
	Image           *ExportedImage
}

type ExportAsset struct {
	Scope     string
	OwnerID   string
	ImageType string
	File      string
}

type peopleRegistry struct {
	mu      sync.Mutex
	entries map[string]*storage.PersonEntry
}

type exportItemTask struct {
	Index int
	Item  emby.Item
	Slug  string
}

type exportItemResult struct {
	Index int
	Item  emby.Item
	Entry storage.ItemEntry
	Err   error
}

type exportPersonImageTask struct {
	StableKey string
	Name      string
	RawPath   string
}

type exportPersonImageResult struct {
	Name     string
	Exported bool
	Skipped  bool
	Err      error
}

type importItemTask struct {
	Index int
	Item  storage.ItemEntry
}

type importItemResult struct {
	Index int
	Match ImportMatch
}

type importLookupCache struct {
	mu          sync.Mutex
	itemCalls   map[string]*itemLookupCall
	personCalls map[string]*personLookupCall
}

type itemLookupCall struct {
	ready chan struct{}
	items []emby.Item
	err   error
}

type personLookupCall struct {
	ready  chan struct{}
	person emby.Person
	err    error
}

type personImageTask struct {
	StableKey string
	Name      string
	Path      string
}

type personImageResult struct {
	StableKey string
	Name      string
	TargetID  string
	Err       error
}

func newImportLookupCache() *importLookupCache {
	return &importLookupCache{
		itemCalls:   map[string]*itemLookupCall{},
		personCalls: map[string]*personLookupCall{},
	}
}

func (c *importLookupCache) searchItems(ctx context.Context, client *emby.Client, searchTerm, includeTypes string, limit int, libraryIDs []string) ([]emby.Item, error) {
	libraryIDs = normalizeTargetLibraryIDs(libraryIDs)
	key := fmt.Sprintf("search:%s:%s:%d:%s", strings.ToLower(strings.TrimSpace(searchTerm)), strings.ToLower(strings.TrimSpace(includeTypes)), limit, targetLibraryCacheKey(libraryIDs))
	return c.itemLookup(ctx, key, func() ([]emby.Item, error) {
		return client.SearchItemsInLibraries(ctx, searchTerm, includeTypes, limit, libraryIDs)
	})
}

func (c *importLookupCache) itemsByProviderID(ctx context.Context, client *emby.Client, providerID string, libraryIDs []string) ([]emby.Item, error) {
	libraryIDs = normalizeTargetLibraryIDs(libraryIDs)
	key := "provider:" + strings.ToLower(strings.TrimSpace(providerID)) + ":" + targetLibraryCacheKey(libraryIDs)
	return c.itemLookup(ctx, key, func() ([]emby.Item, error) {
		return client.ItemsByProviderIDInLibraries(ctx, providerID, libraryIDs)
	})
}

func (c *importLookupCache) findPersonByName(ctx context.Context, client *emby.Client, name string) (emby.Person, error) {
	key := "person:" + strings.ToLower(strings.TrimSpace(name))
	c.mu.Lock()
	if call := c.personCalls[key]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return emby.Person{}, ctx.Err()
		case <-call.ready:
			return call.person, call.err
		}
	}
	call := &personLookupCall{ready: make(chan struct{})}
	c.personCalls[key] = call
	c.mu.Unlock()

	call.person, call.err = findPersonByNameCached(ctx, client, name)
	close(call.ready)
	return call.person, call.err
}

func (c *importLookupCache) itemLookup(ctx context.Context, key string, fetch func() ([]emby.Item, error)) ([]emby.Item, error) {
	c.mu.Lock()
	if call := c.itemCalls[key]; call != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-call.ready:
			return cloneItems(call.items), call.err
		}
	}
	call := &itemLookupCall{ready: make(chan struct{})}
	c.itemCalls[key] = call
	c.mu.Unlock()

	call.items, call.err = fetch()
	close(call.ready)
	return cloneItems(call.items), call.err
}

func cloneItems(items []emby.Item) []emby.Item {
	if len(items) == 0 {
		return nil
	}
	out := make([]emby.Item, len(items))
	copy(out, items)
	return out
}

func findPersonByNameCached(ctx context.Context, client *emby.Client, name string) (emby.Person, error) {
	person, err := client.FindPersonByName(ctx, name)
	if err == nil && strings.TrimSpace(string(person.ID)) != "" {
		return person, nil
	}
	searchErr := err
	person, err = client.Person(ctx, name)
	if err != nil && searchErr != nil {
		return emby.Person{}, fmt.Errorf("find target person by name failed: search: %v; direct: %w", searchErr, err)
	}
	if err != nil {
		return emby.Person{}, fmt.Errorf("find target person by name failed: %w", err)
	}
	if strings.TrimSpace(string(person.ID)) == "" {
		return emby.Person{}, fmt.Errorf("target person %q has empty id", name)
	}
	return person, nil
}

type ExportOptions struct {
	ImageTypes          []string
	IncludePeopleImages bool
}

type ImportCandidate struct {
	EmbyID         string
	Type           string
	Name           string
	OriginalTitle  string
	ProductionYear int
	ProviderIDs    map[string]string
	SeriesName     string
	SeasonNumber   int
	EpisodeNumber  int
}

type ImportPersonCandidate struct {
	EmbyID      string
	Name        string
	ProviderIDs map[string]string
}

type ExportPackage struct {
	Items  []ExportedItem
	People []ExportedPerson
}

type ImportOptions struct {
	DryRun              bool
	ImageTypes          []string
	IncludePeopleImages bool
}

type ImportClient interface {
	SearchCandidates(ctx context.Context, item ExportedItem) ([]ImportCandidate, error)
	SearchPersonCandidates(ctx context.Context, person ExportedPerson) ([]ImportPersonCandidate, error)
	UpdateItem(ctx context.Context, targetEmbyID string, item ExportedItem) error
	UploadImage(ctx context.Context, targetEmbyID string, image ExportedImage) error
	UploadPersonImage(ctx context.Context, targetPersonEmbyID string, image ExportedImage) error
}

func NewService(dataDir string) *Service {
	return &Service{DataDir: dataDir}
}

func (s *Service) ExportsDir() string {
	return filepath.Join(s.DataDir, "exports")
}

func normalizeConcurrency(value int) int {
	if value <= 0 {
		return defaultConcurrency
	}
	return value
}

func workerCount(total int, concurrency int) int {
	if total <= 0 {
		return 0
	}
	concurrency = normalizeConcurrency(concurrency)
	if concurrency > total {
		return total
	}
	return concurrency
}

var (
	episodePattern        = regexp.MustCompile(`(?i)\bS(\d{1,2})E(\d{1,3})\b`)
	seasonPattern         = regexp.MustCompile(`(?i)(?:^|\b)season\s*0*(\d{1,3})(?:\b|$)|^s0*(\d{1,3})$|第\s*0*(\d{1,3})\s*季`)
	seriesYearBraceTail   = regexp.MustCompile(`\s+\(\d{4}\)\s*\{.*$`)
	seriesYearMetaTail    = regexp.MustCompile(`\s+\d{4}\s*\{.*$`)
	seriesYearTail        = regexp.MustCompile(`\s+\(\d{4}\)\s*$`)
	shortNameYearDashTail = regexp.MustCompile(`\s*\(?\d{4}\)?\s*-.*$`)
	shortNameYearMetaTail = regexp.MustCompile(`\s*\(?\d{4}\)?\s*\{.*$`)
)

const (
	defaultConcurrency       = 4
	importRetryAttempts      = 2
	importMatchTimeout       = 30 * time.Second
	itemMetadataTimeout      = 30 * time.Second
	itemImageUploadTimeout   = 15 * time.Second
	personImageUploadTimeout = 15 * time.Second
	exportHeartbeatInterval  = 10 * time.Second
	exportProgressEvery      = 25
	importHeartbeatInterval  = 10 * time.Second
	peopleImageProgressEvery = 25
)

func exportDirectoryName(exportedAt time.Time, serverName string, libraries []emby.Library) string {
	parts := []string{exportedAt.Format("20060102-150405")}
	if strings.TrimSpace(serverName) != "" {
		parts = append(parts, serverName)
	}
	switch len(libraries) {
	case 0:
	case 1:
		parts = append(parts, libraries[0].Name)
	default:
		parts = append(parts, fmt.Sprintf("%s等%d库", libraries[0].Name, len(libraries)))
	}
	return storage.SafeName(strings.Join(parts, "-"))
}

func (s *Service) uniqueExportDirectory(baseName string) (string, string) {
	baseName = storage.SafeName(baseName)
	if baseName == "" {
		baseName = time.Now().Format("20060102-150405")
	}
	for i := 0; ; i++ {
		name := baseName
		if i > 0 {
			name = fmt.Sprintf("%s-%d", baseName, i+1)
		}
		dir := filepath.Join(s.ExportsDir(), name)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return name, dir
		}
	}
}

func (s *Service) ListExports() ([]string, error) {
	dir := s.ExportsDir()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, entry.Name(), "manifest.json")); err == nil {
			out = append(out, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

func (s *Service) latestBaselineManifest(source string, libraries []emby.Library, excludeName string) (storage.Manifest, string, string, bool) {
	exports, err := s.ListExports()
	if err != nil {
		return storage.Manifest{}, "", "", false
	}
	wantLibraries := libraryNameSet(libraries)
	for _, name := range exports {
		if name == excludeName {
			continue
		}
		path := filepath.Join(s.ExportsDir(), name, "manifest.json")
		var manifest storage.Manifest
		if err := storage.ReadJSON(path, &manifest); err != nil {
			continue
		}
		if strings.TrimRight(manifest.Source, "/") != strings.TrimRight(source, "/") {
			continue
		}
		if !sameLibrarySet(wantLibraries, manifest.Libraries) {
			continue
		}
		return manifest, name, filepath.Dir(path), true
	}
	return storage.Manifest{}, "", "", false
}

func libraryNameSet(libraries []emby.Library) map[string]bool {
	out := map[string]bool{}
	for _, lib := range libraries {
		if strings.TrimSpace(lib.Name) != "" {
			out[strings.TrimSpace(lib.Name)] = true
		}
	}
	return out
}

func sameLibrarySet(want map[string]bool, got []storage.LibraryEntry) bool {
	if len(want) != len(got) {
		return false
	}
	for _, lib := range got {
		if !want[strings.TrimSpace(lib.Name)] {
			return false
		}
	}
	return true
}

func manifestItemsByStableKey(manifest storage.Manifest) map[string]storage.ItemEntry {
	out := map[string]storage.ItemEntry{}
	for _, item := range manifest.Items {
		if strings.TrimSpace(item.StableKey) != "" {
			out[item.StableKey] = item
		}
	}
	return out
}

func (s *Service) ResolveExportPath(exportPath string) (string, string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(exportPath))
	if cleaned == "." || cleaned == "" {
		return "", "", fmt.Errorf("exportPath is required")
	}
	exportsDir, err := filepath.Abs(s.ExportsDir())
	if err != nil {
		return "", "", err
	}
	var resolved string
	if filepath.IsAbs(cleaned) {
		resolved, err = filepath.Abs(cleaned)
	} else {
		resolved, err = filepath.Abs(filepath.Join(exportsDir, cleaned))
	}
	if err != nil {
		return "", "", err
	}
	rel, err := filepath.Rel(exportsDir, resolved)
	if err != nil {
		return "", "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("exportPath must stay under exports directory")
	}
	if _, err := os.Stat(filepath.Join(resolved, "manifest.json")); err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("export package not found: %s", exportPath)
		}
		return "", "", err
	}
	return resolved, filepath.Base(resolved), nil
}

func (s *Service) ListImportReports(exportPath string) ([]ImportReportInfo, error) {
	resolved, exportName, err := s.ResolveExportPath(exportPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	out := make([]ImportReportInfo, 0)
	for _, entry := range entries {
		if entry.IsDir() || !isImportReportFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		reportPath := filepath.Join(resolved, entry.Name())
		var report ImportReport
		if err := storage.ReadJSON(reportPath, &report); err != nil {
			return nil, fmt.Errorf("read import report %s: %w", entry.Name(), err)
		}
		out = append(out, ImportReportInfo{
			ExportName:    exportName,
			Name:          entry.Name(),
			Path:          filepath.ToSlash(filepath.Join(exportName, entry.Name())),
			Size:          info.Size(),
			ModifiedAt:    info.ModTime(),
			DryRun:        report.DryRun,
			StartedAt:     report.StartedAt,
			EndedAt:       report.EndedAt,
			Target:        report.Target,
			Compatibility: report.Compatibility,
			Summary:       report.Summary,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name > out[j].Name
	})
	return out, nil
}

func (s *Service) ImportReportPath(exportPath, reportName string) (string, error) {
	resolved, _, err := s.ResolveExportPath(exportPath)
	if err != nil {
		return "", err
	}
	name := filepath.Base(strings.TrimSpace(reportName))
	if name != strings.TrimSpace(reportName) || !isImportReportFile(name) {
		return "", fmt.Errorf("invalid import report name")
	}
	path := filepath.Join(resolved, name)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("import report not found: %s", name)
		}
		return "", err
	}
	return path, nil
}

func isImportReportFile(name string) bool {
	return strings.HasPrefix(name, "import-report-") && strings.HasSuffix(name, ".json")
}

func BuildCompatibilityProfile(sourceVersion, targetVersion string) CompatibilityProfile {
	profile := CompatibilityProfile{
		Name:          "emby-generic",
		SourceVersion: strings.TrimSpace(sourceVersion),
		TargetVersion: strings.TrimSpace(targetVersion),
		Notes: []string{
			"导入匹配不依赖旧 Emby 内部 ID",
			"人物字段回写会移除旧服务器人物 ID",
		},
	}
	major, minor, ok := parseMajorMinor(targetVersion)
	switch {
	case ok && major == 4 && minor >= 9:
		profile.Name = "emby-4.9-strict"
		profile.Notes = append(profile.Notes,
			"目标 Emby 4.9+ 使用严格元数据兼容策略",
			"元数据回写失败时会降级为最小安全 payload 重试",
			"人物头像优先通过 /Persons 搜索目标人物 ID 后上传",
		)
	case ok && major == 4 && minor <= 8:
		profile.Name = "emby-4.8-classic"
		profile.Notes = append(profile.Notes,
			"目标 Emby 4.8 使用经典兼容策略",
			"保留 4.8 可接受的元数据字段清洗逻辑",
		)
	default:
		profile.Notes = append(profile.Notes,
			"目标版本无法识别，使用通用兼容策略",
		)
	}
	return profile
}

func parseMajorMinor(version string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return 0, 0, false
	}
	return major, minor, true
}

func ValidateExportPackage(exportPath string, manifest storage.Manifest) PackageValidation {
	validation := PackageValidation{}
	check := func(scope string, file storage.FileEntry) {
		if strings.TrimSpace(file.Path) == "" {
			validation.InvalidPaths++
			validation.addError("%s 路径为空", scope)
			return
		}
		validation.CheckedFiles++
		path, err := safePackagePath(exportPath, file.Path)
		if err != nil {
			validation.InvalidPaths++
			validation.addError("%s 路径无效：%s", scope, file.Path)
			return
		}
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				validation.MissingFiles++
				validation.addError("%s 文件缺失：%s", scope, file.Path)
				return
			}
			validation.InvalidPaths++
			validation.addError("%s 无法读取：%s", scope, err)
			return
		}
		if info.IsDir() {
			validation.InvalidPaths++
			validation.addError("%s 指向目录：%s", scope, file.Path)
			return
		}
		if file.Size > 0 && info.Size() != file.Size {
			validation.SizeMismatches++
			validation.addError("%s 文件大小不一致：%s", scope, file.Path)
		}
		if strings.TrimSpace(file.SHA256) != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				validation.InvalidPaths++
				validation.addError("%s 无法读取校验：%s", scope, err)
				return
			}
			sum := sha256.Sum256(data)
			if !strings.EqualFold(hex.EncodeToString(sum[:]), strings.TrimSpace(file.SHA256)) {
				validation.ChecksumMismatches++
				validation.addError("%s SHA256 不一致：%s", scope, file.Path)
			}
		}
	}
	check("manifest", storage.FileEntry{Path: "manifest.json"})
	for _, item := range manifest.Items {
		if item.Skipped {
			continue
		}
		label := item.Name
		if strings.TrimSpace(label) == "" {
			label = item.StableKey
		}
		check("项目元数据 "+label, storage.FileEntry{Path: item.InfoPath})
		if strings.TrimSpace(item.RawPath) != "" {
			check("项目原始数据 "+label, storage.FileEntry{Path: item.RawPath})
		}
		for _, image := range item.Images {
			check(fmt.Sprintf("媒体图片 %s/%s", label, image.Type), image)
		}
	}
	for _, person := range manifest.People {
		if person.Image != nil {
			check("人物头像 "+person.Name, *person.Image)
		}
	}
	return validation
}

func (v PackageValidation) OK() bool {
	return v.MissingFiles == 0 && v.InvalidPaths == 0 && v.SizeMismatches == 0 && v.ChecksumMismatches == 0
}

func (v PackageValidation) Error() string {
	if v.OK() {
		return ""
	}
	return fmt.Sprintf("导出包校验失败：检查 %d 个文件，缺失 %d 个，路径无效 %d 个，大小不一致 %d 个，SHA256 不一致 %d 个。%s",
		v.CheckedFiles,
		v.MissingFiles,
		v.InvalidPaths,
		v.SizeMismatches,
		v.ChecksumMismatches,
		strings.Join(v.Errors, "；"),
	)
}

func (v *PackageValidation) addError(format string, args ...any) {
	if len(v.Errors) >= 8 {
		return
	}
	v.Errors = append(v.Errors, fmt.Sprintf(format, args...))
}

func safePackagePath(root, relPath string) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", fmt.Errorf("package path is required")
	}
	cleaned := filepath.Clean(filepath.FromSlash(relPath))
	if filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("package path must be relative: %s", relPath)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.Abs(filepath.Join(rootAbs, cleaned))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, resolved)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("package path escapes export package: %s", relPath)
	}
	return resolved, nil
}

func (s *Service) Export(ctx context.Context, j *job.Job, req ExportRequest) (ExportResult, error) {
	startedAt := time.Now()
	client, err := emby.NewClient(req.Connection.BaseURL, req.Connection.APIKey)
	if err != nil {
		return ExportResult{}, err
	}
	info, err := client.SystemInfo(ctx)
	if err != nil {
		return ExportResult{}, err
	}
	j.Log("info", "连接到 Emby %s (%s)", info.ServerName, info.Version)

	imageTypes := req.ImageTypes
	if len(imageTypes) == 0 {
		imageTypes = emby.DefaultImageTypes
	}
	imageTypeSet := map[string]bool{}
	for _, typ := range imageTypes {
		imageTypeSet[strings.ToLower(typ)] = true
	}
	concurrency := normalizeConcurrency(req.Concurrency)
	j.Log("info", "导出并发数：%d", concurrency)

	libraries := req.Libraries
	if len(libraries) == 0 {
		libraries, err = client.Libraries(ctx)
		if err != nil {
			return ExportResult{}, err
		}
	}
	if len(req.LibraryIDs) > 0 {
		allowed := map[string]bool{}
		for _, id := range req.LibraryIDs {
			allowed[id] = true
		}
		filtered := libraries[:0]
		for _, lib := range libraries {
			if allowed[lib.ID] {
				filtered = append(filtered, lib)
			}
		}
		libraries = filtered
	}
	if len(libraries) == 0 {
		return ExportResult{}, fmt.Errorf("no libraries selected")
	}

	exportedAt := time.Now()
	exportName, exportDir := s.uniqueExportDirectory(exportDirectoryName(exportedAt, info.ServerName, libraries))
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return ExportResult{}, err
	}
	manifest := storage.Manifest{
		ToolVersion:   req.ToolVersion,
		EmbyVersion:   info.Version,
		ExportedAt:    exportedAt,
		SchemaVersion: 1,
		Compatibility: "emby-4.8.11-first",
		Source:        client.BaseURL,
	}
	baselineItems := map[string]storage.ItemEntry{}
	if req.Incremental {
		manifest.Incremental = &storage.Incremental{Enabled: true, CreatedAt: time.Now()}
		if baseline, baselineName, baselinePath, ok := s.latestBaselineManifest(client.BaseURL, libraries, exportName); ok {
			baselineItems = manifestItemsByStableKey(baseline)
			manifest.Incremental.BaselineExportName = baselineName
			manifest.Incremental.BaselineExportPath = filepath.ToSlash(baselinePath)
			j.Log("info", "增量导出基线：%s", baselineName)
		} else {
			j.Log("info", "增量导出未找到可用基线，本次按全量导出处理")
		}
	}

	people := newPeopleRegistry()
	for _, lib := range libraries {
		if err := j.WaitIfPaused(ctx); err != nil {
			return ExportResult{}, err
		}
		select {
		case <-ctx.Done():
			return ExportResult{}, ctx.Err()
		default:
		}
		libSlug := storage.SafeName(lib.Name)
		manifest.Libraries = append(manifest.Libraries, storage.LibraryEntry{ID: lib.ID, Name: lib.Name, Slug: libSlug, Count: lib.Count})
		j.Log("info", "读取媒体库：%s", lib.Name)
		usedItemSlugs := map[string]int{}
		items, err := client.Items(ctx, lib.ID)
		if err != nil {
			manifest.Errors = append(manifest.Errors, storage.ErrorEntry{Scope: "library", ID: lib.ID, Name: lib.Name, Message: err.Error()})
			j.Log("error", "读取媒体库失败：%s - %v", lib.Name, err)
			continue
		}
		j.Log("info", "媒体库 %s 共 %d 个项目", lib.Name, len(items))
		tasks := make([]exportItemTask, 0, len(items))
		for idx, item := range items {
			tasks = append(tasks, exportItemTask{
				Index: idx,
				Item:  item,
				Slug:  storage.UniqueSlug(itemDirectoryBase(item), usedItemSlugs),
			})
		}
		results, err := s.exportLibraryItems(ctx, j, client, exportDir, lib, tasks, imageTypeSet, req, people, concurrency, baselineItems)
		if err != nil {
			return ExportResult{}, err
		}
		for _, result := range results {
			if result.Err != nil {
				manifest.Errors = append(manifest.Errors, storage.ErrorEntry{Scope: "item", ID: result.Item.ID, Name: result.Item.Name, Message: result.Err.Error()})
				j.Log("warn", "导出项目失败：%s - %v", result.Item.Name, result.Err)
				continue
			}
			entry := result.Entry
			manifest.Items = append(manifest.Items, entry)
			if entry.Skipped {
				manifest.Summary.SkippedItems++
				if manifest.Incremental != nil {
					manifest.Incremental.SkippedItems++
				}
			} else if manifest.Incremental != nil {
				manifest.Incremental.ChangedItems++
			}
			manifest.Summary.ItemImages += len(entry.Images)
		}
	}
	if !req.SkipImages && req.IncludePeopleImages {
		if err := s.exportPeopleImages(ctx, j, client, exportDir, people, concurrency); err != nil {
			return ExportResult{}, err
		}
	}
	for _, p := range people.entriesSorted() {
		manifest.People = append(manifest.People, p)
		if p.Image != nil {
			manifest.Summary.PeopleImages++
		}
	}
	manifest.Summary.Libraries = len(manifest.Libraries)
	manifest.Summary.Items = len(manifest.Items)
	manifest.Summary.People = len(manifest.People)
	manifest.Summary.Errors = len(manifest.Errors)
	if err := storage.WriteJSON(filepath.Join(exportDir, "manifest.json"), manifest); err != nil {
		return ExportResult{}, err
	}
	j.Log("info", "导出完成：%s", exportDir)
	j.Log("info", exportSummaryLine(manifest.Summary, time.Since(startedAt)))
	return ExportResult{Path: exportDir, Manifest: manifest}, nil
}

func exportSummaryLine(summary storage.Summary, elapsed time.Duration) string {
	return fmt.Sprintf(
		"导出总结：媒体库 %d 个，项目 %d 个，媒体图片 %d 张，人物 %d 个，人物头像 %d 张，错误 %d 个，用时 %s。",
		summary.Libraries,
		summary.Items,
		summary.ItemImages,
		summary.People,
		summary.PeopleImages,
		summary.Errors,
		formatElapsed(elapsed),
	)
}

func formatElapsed(elapsed time.Duration) string {
	elapsed = elapsed.Round(time.Second)
	if elapsed < time.Second {
		return "不足1秒"
	}
	hours := int(elapsed / time.Hour)
	elapsed -= time.Duration(hours) * time.Hour
	minutes := int(elapsed / time.Minute)
	elapsed -= time.Duration(minutes) * time.Minute
	seconds := int(elapsed / time.Second)

	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d小时", hours))
	}
	if minutes > 0 {
		parts = append(parts, fmt.Sprintf("%d分", minutes))
	}
	if seconds > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d秒", seconds))
	}
	return strings.Join(parts, "")
}

func newPeopleRegistry() *peopleRegistry {
	return &peopleRegistry{entries: map[string]*storage.PersonEntry{}}
}

func (r *peopleRegistry) entriesSorted() []storage.PersonEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]storage.PersonEntry, 0, len(r.entries))
	for _, p := range r.entries {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *peopleRegistry) add(itemStableKey string, person emby.Person) {
	key := storage.StablePersonKey(person)
	personSlug := storage.SafeName(key)
	rawPath := filepath.ToSlash(filepath.Join("people", personSlug, "raw.json"))

	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.entries[key]
	if !ok {
		p = &storage.PersonEntry{
			StableKey:   key,
			Name:        person.Name,
			Type:        person.Type,
			Role:        person.Role,
			ProviderIDs: person.ProviderIDs,
			RawPath:     rawPath,
		}
		r.entries[key] = p
	}
	p.ReferencedBy = append(p.ReferencedBy, itemStableKey)
}

func (r *peopleRegistry) imageTasks() []exportPersonImageTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	tasks := make([]exportPersonImageTask, 0, len(r.entries))
	for _, p := range r.entries {
		if strings.TrimSpace(p.Name) == "" {
			continue
		}
		tasks = append(tasks, exportPersonImageTask{StableKey: p.StableKey, Name: p.Name, RawPath: p.RawPath})
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	return tasks
}

func (r *peopleRegistry) update(stableKey string, update func(*storage.PersonEntry)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current := r.entries[stableKey]; current != nil {
		update(current)
	}
}

func (s *Service) exportPeopleImages(ctx context.Context, j *job.Job, client *emby.Client, exportDir string, people *peopleRegistry, concurrency int) error {
	tasks := people.imageTasks()
	if len(tasks) == 0 {
		return nil
	}

	taskCh := make(chan exportPersonImageTask)
	resultCh := make(chan exportPersonImageResult, len(tasks))
	workers := workerCount(len(tasks), concurrency)
	j.Log("info", "开始导出人物头像：%d 个，并发 %d", len(tasks), workers)
	for i := 0; i < workers; i++ {
		go func() {
			for task := range taskCh {
				resultCh <- s.exportPersonImage(ctx, client, exportDir, people, task)
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for _, task := range tasks {
			if err := j.WaitIfPaused(ctx); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case taskCh <- task:
			}
		}
	}()

	done := 0
	exported := 0
	skipped := 0
	failed := 0
	detailedFailures := 0
	ticker := time.NewTicker(exportHeartbeatInterval)
	defer ticker.Stop()
	for done < len(tasks) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultCh:
			done++
			switch {
			case result.Err != nil:
				failed++
				detailedFailures++
				if detailedFailures <= 10 {
					j.Log("warn", "人物头像导出失败：%s - %v", result.Name, result.Err)
				} else if detailedFailures == 11 {
					j.Log("warn", "人物头像导出失败较多，后续失败只在进度和总结中统计")
				}
			case result.Exported:
				exported++
			default:
				skipped++
			}
			if done == 1 || done%peopleImageProgressEvery == 0 || done == len(tasks) {
				j.Log("info", "人物头像导出进度：%d/%d，成功 %d，跳过 %d，失败 %d", done, len(tasks), exported, skipped, failed)
			}
		case <-ticker.C:
			if done < len(tasks) {
				j.Log("info", "人物头像导出等待中：已完成 %d/%d，剩余 %d 个；正在读取 Emby 人物信息或头像图片", done, len(tasks), len(tasks)-done)
			}
		}
	}
	j.Log("info", "人物头像导出完成：成功 %d，跳过 %d，失败 %d", exported, skipped, failed)
	return nil
}

func (s *Service) exportPersonImage(ctx context.Context, client *emby.Client, exportDir string, people *peopleRegistry, task exportPersonImageTask) exportPersonImageResult {
	if full, err := client.Person(ctx, task.Name); err == nil {
		if err := storage.WriteJSON(filepath.Join(exportDir, task.RawPath), full); err != nil {
			return exportPersonImageResult{Name: task.Name, Err: err}
		}
		people.update(task.StableKey, func(current *storage.PersonEntry) {
			current.ProviderIDs = mergeProviderIDs(current.ProviderIDs, full.ProviderIDs)
		})
	} else if ctx.Err() != nil {
		return exportPersonImageResult{Name: task.Name, Err: ctx.Err()}
	}

	data, ext, err := client.DownloadPersonImage(ctx, task.Name)
	if err != nil || len(data) == 0 {
		if ctx.Err() != nil {
			return exportPersonImageResult{Name: task.Name, Err: ctx.Err()}
		}
		return exportPersonImageResult{Name: task.Name, Skipped: true}
	}
	personSlug := storage.SafeName(task.StableKey)
	rel := filepath.ToSlash(filepath.Join("people", personSlug, "primary"+ext))
	file, err := storage.WriteBytes(filepath.Join(exportDir, rel), data)
	if err != nil {
		return exportPersonImageResult{Name: task.Name, Err: err}
	}
	file.Type = "Primary"
	file.Path = rel

	people.update(task.StableKey, func(current *storage.PersonEntry) {
		current.Image = &file
	})
	return exportPersonImageResult{Name: task.Name, Exported: true}
}

func (s *Service) exportLibraryItems(ctx context.Context, j *job.Job, client *emby.Client, exportDir string, lib emby.Library, tasks []exportItemTask, imageTypeSet map[string]bool, req ExportRequest, people *peopleRegistry, concurrency int, baselineItems map[string]storage.ItemEntry) ([]exportItemResult, error) {
	results := make([]exportItemResult, len(tasks))
	if len(tasks) == 0 {
		return results, nil
	}

	taskCh := make(chan exportItemTask)
	resultCh := make(chan exportItemResult, len(tasks))
	workers := workerCount(len(tasks), concurrency)
	j.Log("info", "开始处理媒体库：%s，共 %d 个项目，并发 %d", lib.Name, len(tasks), workers)
	for i := 0; i < workers; i++ {
		go func() {
			for task := range taskCh {
				entry, err := s.exportItem(ctx, client, exportDir, lib, task.Item, task.Slug, imageTypeSet, req, people, baselineItems)
				resultCh <- exportItemResult{Index: task.Index, Item: task.Item, Entry: entry, Err: err}
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for _, task := range tasks {
			if err := j.WaitIfPaused(ctx); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case taskCh <- task:
			}
		}
	}()

	done := 0
	ticker := time.NewTicker(exportHeartbeatInterval)
	defer ticker.Stop()
	for done < len(tasks) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-resultCh:
			done++
			results[result.Index] = result
			if done == 1 || done%exportProgressEvery == 0 || done == len(tasks) {
				j.Log("info", "处理 %s：%d/%d", lib.Name, done, len(tasks))
			}
		case <-ticker.C:
			if done < len(tasks) {
				j.Log("info", "导出项目等待中：%s 已完成 %d/%d，剩余 %d 个；正在读取媒体图片或等待远程响应", lib.Name, done, len(tasks), len(tasks)-done)
			}
		}
	}
	return results, nil
}

func (s *Service) exportItem(ctx context.Context, client *emby.Client, exportDir string, lib emby.Library, item emby.Item, itemSlug string, imageTypeSet map[string]bool, req ExportRequest, people *peopleRegistry, baselineItems map[string]storage.ItemEntry) (storage.ItemEntry, error) {
	item = s.enrichExportItem(ctx, client, item)
	stableKey := storage.StableItemKey(item)
	fingerprint := itemFingerprint(item)
	itemDir := filepath.Join(exportDir, "libraries", storage.SafeName(lib.Name), "items", itemSlug)
	infoRel := filepath.ToSlash(filepath.Join("libraries", storage.SafeName(lib.Name), "items", itemSlug, "info.json"))
	rawRel := filepath.ToSlash(filepath.Join("libraries", storage.SafeName(lib.Name), "items", itemSlug, "raw.json"))
	info := storage.ItemInfo{Item: item, StableKey: stableKey, ExportedAt: time.Now(), People: item.People}

	entry := storage.ItemEntry{
		StableKey:         stableKey,
		Slug:              itemSlug,
		LibraryID:         lib.ID,
		LibraryName:       lib.Name,
		ID:                item.ID,
		Name:              item.Name,
		Type:              item.Type,
		Path:              item.Path,
		OriginalTitle:     item.OriginalTitle,
		ProductionYear:    item.ProductionYear,
		SeriesName:        item.SeriesName,
		IndexNumber:       item.IndexNumber,
		ParentIndexNumber: item.ParentIndexNumber,
		ProviderIDs:       item.ProviderIDs,
		Fingerprint:       fingerprint,
		InfoPath:          infoRel,
		RawPath:           rawRel,
	}
	if req.Incremental {
		if baseline, ok := baselineItems[stableKey]; ok && baseline.Fingerprint != "" && baseline.Fingerprint == fingerprint {
			entry.Skipped = true
			entry.SkipReason = "unchanged"
			return entry, nil
		}
	}

	if !req.SkipImages {
		images, err := client.Images(ctx, item.ID)
		if err != nil || len(images) == 0 {
			images = emby.FallbackImages(item)
			if len(images) == 0 {
				images = emby.DirectImageInfos(item.ID, imageTypesForDirectFallback(req.ImageTypes))
			}
		}
		for _, image := range images {
			if len(imageTypeSet) > 0 && !imageTypeSet[strings.ToLower(image.ImageType)] {
				continue
			}
			data, ext, err := client.DownloadPath(ctx, image.DownloadPath)
			if err != nil || len(data) == 0 {
				continue
			}
			fileName := imageFileName(image, ext)
			rel := filepath.ToSlash(filepath.Join("libraries", storage.SafeName(lib.Name), "items", itemSlug, fileName))
			file, err := storage.WriteBytes(filepath.Join(exportDir, rel), data)
			if err != nil {
				continue
			}
			file.Type = image.ImageType
			file.Index = image.ImageIndex
			file.Path = rel
			entry.Images = append(entry.Images, file)
			info.Images = append(info.Images, file)
		}
	}

	for _, person := range item.People {
		if strings.TrimSpace(person.Name) == "" {
			continue
		}
		entry.People = append(entry.People, person.Name)
		people.add(stableKey, person)
	}

	if err := storage.WriteJSON(filepath.Join(itemDir, "info.json"), info); err != nil {
		return entry, err
	}
	if err := storage.WriteJSON(filepath.Join(itemDir, "raw.json"), item.Raw); err != nil {
		return entry, err
	}
	return entry, nil
}

func (s *Service) enrichExportItem(ctx context.Context, client *emby.Client, item emby.Item) emby.Item {
	if strings.TrimSpace(item.ID) == "" || !needsExportItemDetails(item) {
		return item
	}
	full, err := client.Item(ctx, item.ID)
	if err != nil || strings.TrimSpace(full.ID) == "" {
		return item
	}
	return mergeExportItemDetails(item, full)
}

func needsExportItemDetails(item emby.Item) bool {
	return len(item.People) == 0 ||
		(len(item.ImageTags) == 0 && len(item.BackdropImageTags) == 0) ||
		item.SeriesName == "" && item.Type == "Episode" ||
		item.IndexNumber == 0 && (item.Type == "Episode" || item.Type == "Season")
}

func mergeExportItemDetails(base, full emby.Item) emby.Item {
	if full.ID == "" {
		full.ID = base.ID
	}
	if full.Name == "" {
		full.Name = base.Name
	}
	if full.Type == "" {
		full.Type = base.Type
	}
	if full.Path == "" {
		full.Path = base.Path
	}
	if full.OriginalTitle == "" {
		full.OriginalTitle = base.OriginalTitle
	}
	if full.ProductionYear == 0 {
		full.ProductionYear = base.ProductionYear
	}
	if full.SeriesName == "" {
		full.SeriesName = base.SeriesName
	}
	if full.SeasonName == "" {
		full.SeasonName = base.SeasonName
	}
	if full.IndexNumber == 0 {
		full.IndexNumber = base.IndexNumber
	}
	if full.ParentIndexNumber == 0 {
		full.ParentIndexNumber = base.ParentIndexNumber
	}
	if len(full.ProviderIDs) == 0 {
		full.ProviderIDs = base.ProviderIDs
	}
	if len(full.People) == 0 {
		full.People = base.People
	}
	if len(full.ImageTags) == 0 {
		full.ImageTags = base.ImageTags
	}
	if len(full.BackdropImageTags) == 0 {
		full.BackdropImageTags = base.BackdropImageTags
	}
	if full.Raw == nil {
		full.Raw = base.Raw
	}
	return full
}

func imageTypesForDirectFallback(imageTypes []string) []string {
	if len(imageTypes) == 0 {
		return emby.DefaultImageTypes
	}
	return imageTypes
}

func itemDirectoryBase(item emby.Item) string {
	if strings.TrimSpace(item.Path) != "" {
		stem := mediaPathStem(item.Path)
		if strings.TrimSpace(stem) != "" {
			return stem
		}
	}
	if strings.TrimSpace(item.Name) != "" {
		return item.Name
	}
	if strings.TrimSpace(item.OriginalTitle) != "" {
		return item.OriginalTitle
	}
	if strings.TrimSpace(item.ID) != "" {
		return "item-" + item.ID
	}
	return "unknown"
}

func mediaPathStem(value string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	base := path.Base(normalized)
	return strings.TrimSuffix(base, path.Ext(base))
}

func imageFileName(image emby.ImageInfo, ext string) string {
	name := strings.ToLower(image.ImageType)
	if image.ImageType == "Backdrop" || image.ImageIndex > 0 {
		name = fmt.Sprintf("%s-%d", name, image.ImageIndex)
	}
	return name + ext
}

func mergeProviderIDs(a, b map[string]string) map[string]string {
	if a == nil {
		a = map[string]string{}
	}
	for k, v := range b {
		if _, ok := a[k]; !ok && v != "" {
			a[k] = v
		}
	}
	return a
}

func itemFingerprint(item emby.Item) string {
	raw := map[string]any{
		"name":              item.Name,
		"type":              item.Type,
		"path":              item.Path,
		"originalTitle":     item.OriginalTitle,
		"overview":          item.Overview,
		"officialRating":    item.OfficialRating,
		"productionYear":    item.ProductionYear,
		"premiereDate":      item.PremiereDate,
		"communityRating":   item.CommunityRating,
		"genres":            sortedStrings(item.Genres),
		"studios":           sortedNameIDs(item.Studios),
		"tags":              sortedStrings(item.Tags),
		"taglines":          sortedStrings(item.Taglines),
		"providerIds":       sortedStringMap(item.ProviderIDs),
		"people":            sortedPeopleFingerprint(item.People),
		"seriesName":        item.SeriesName,
		"seasonName":        item.SeasonName,
		"indexNumber":       item.IndexNumber,
		"parentIndexNumber": item.ParentIndexNumber,
		"imageTags":         sortedStringMap(item.ImageTags),
		"backdropTags":      sortedStrings(item.BackdropImageTags),
	}
	data, _ := json.Marshal(raw)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sortedStringMap(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, strings.ToLower(key)+"="+value)
	}
	sort.Strings(out)
	return out
}

func sortedNameIDs(values []emby.NameID) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value.Name)
	}
	sort.Strings(out)
	return out
}

func importTargetLibraryIDs(req ImportRequest) []string {
	if ids := normalizeTargetLibraryIDs(req.TargetLibraryIDs); len(ids) > 0 {
		return ids
	}
	return normalizeTargetLibraryIDs(req.LibraryIDs)
}

func normalizeTargetLibraryIDs(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func targetLibraryCacheKey(libraryIDs []string) string {
	libraryIDs = normalizeTargetLibraryIDs(libraryIDs)
	if len(libraryIDs) == 0 {
		return "all"
	}
	return "libraries:" + strings.Join(libraryIDs, ",")
}

func sortedPeopleFingerprint(values []emby.Person) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.Join([]string{
			value.Name,
			value.Type,
			value.Role,
			strings.Join(sortedStringMap(value.ProviderIDs), "|"),
		}, "/"))
	}
	sort.Strings(out)
	return out
}

func (s *Service) Import(ctx context.Context, j *job.Job, req ImportRequest) (ImportResult, error) {
	client, err := emby.NewClient(req.Connection.BaseURL, req.Connection.APIKey)
	if err != nil {
		return ImportResult{}, err
	}
	var targetInfo emby.SystemInfo
	if info, err := client.SystemInfo(ctx); err == nil {
		targetInfo = info
		j.Log("info", "连接到目标 Emby %s (%s)", info.ServerName, info.Version)
	} else {
		j.Log("warn", "读取目标 Emby 版本失败，使用通用兼容策略：%v", err)
	}
	exportPath, _, err := s.ResolveExportPath(req.ExportPath)
	if err != nil {
		return ImportResult{}, err
	}
	var manifest storage.Manifest
	if err := storage.ReadJSON(filepath.Join(exportPath, "manifest.json"), &manifest); err != nil {
		return ImportResult{}, err
	}
	validation := ValidateExportPackage(exportPath, manifest)
	if !validation.OK() {
		return ImportResult{}, errors.New(validation.Error())
	}
	profile := BuildCompatibilityProfile(manifest.EmbyVersion, targetInfo.Version)
	report := ImportReport{
		StartedAt:     time.Now(),
		DryRun:        req.DryRun,
		Target:        importTargetFromSystemInfo(targetInfo),
		Compatibility: profile,
	}
	report.Target.BaseURL = strings.TrimRight(req.Connection.BaseURL, "/")
	report.Diff = initialImportDiff(manifest.Summary)
	report.Incremental = importIncrementalReport(manifest)
	concurrency := normalizeConcurrency(req.Concurrency)
	items := importableManifestItems(manifest.Items)
	if skipped := len(manifest.Items) - len(items); skipped > 0 {
		addSkippedWrites(&report, "incremental-manifest", skipped)
		j.Log("info", "增量导出包跳过未变化项目：%d 个", skipped)
	}
	resumeDone := map[string]bool{}
	if req.Resume {
		if done, reportName := s.resumeSuccessfulItems(exportPath, report.Target); len(done) > 0 {
			resumeDone = done
			j.Log("info", "断点续跑：读取上次报告 %s，跳过已成功项目 %d 个", reportName, len(done))
		}
	}
	if len(resumeDone) > 0 {
		filtered := items[:0]
		for _, item := range items {
			if resumeDone[item.StableKey] {
				addSkippedWrites(&report, "resume", 1)
				continue
			}
			filtered = append(filtered, item)
		}
		items = filtered
	}
	j.Log("info", "读取导出包：%s，共 %d 个项目，本次处理 %d 个项目", exportPath, len(manifest.Items), len(items))
	j.Log("info", "导出包校验通过：检查 %d 个文件", validation.CheckedFiles)
	j.Log("info", "兼容策略：%s（源 Emby %s -> 目标 Emby %s）", profile.Name, emptyDash(profile.SourceVersion), emptyDash(profile.TargetVersion))
	j.Log("info", "导入并发数：%d", concurrency)
	if req.DryRun {
		j.Log("info", "[DRY] 本次只验证匹配，不会写入元数据和图片")
	}
	cache := newImportLookupCache()
	checkpoint := newImportCheckpointStore(exportPath, report.Target)
	itemResults, err := s.importItems(ctx, j, client, cache, exportPath, items, req, concurrency, func(match ImportMatch) {
		if shouldCheckpointMatch(match, req.DryRun) {
			if err := checkpoint.Record(match); err != nil {
				j.Log("warn", "写入断点状态失败：%v", err)
			}
		}
	})
	if err != nil {
		return ImportResult{}, err
	}
	for _, result := range itemResults {
		match := result.Match
		report.Matches = append(report.Matches, match)
		addImportMatchSummary(&report, match, req.DryRun)
		addFailureExample(&report.Failures, match)
		addImageFailureExamples(&report.Failures, match)
	}
	if !req.SkipImages && req.IncludePeopleImages {
		if err := s.importPeopleImages(ctx, client, cache, checkpoint, exportPath, manifest, &report, j, req.Resume, req.DryRun, concurrency); err != nil {
			return ImportResult{}, err
		}
	}
	if report.Skips != nil {
		j.Log("info", "Import skipped writes: incremental-manifest %d, resume %d, dry-run %d, total %d",
			report.Skips.IncrementalManifest,
			report.Skips.Resume,
			report.Skips.DryRunWrites,
			report.Skips.Total,
		)
	}
	report.EndedAt = time.Now()
	report.Diff = finalizeImportDiff(report.Diff, report.Summary)
	reportPath, err := nextImportReportPath(exportPath, time.Now())
	if err != nil {
		return ImportResult{}, err
	}
	if err := storage.WriteJSON(reportPath, report); err != nil {
		return ImportResult{}, err
	}
	j.Log("info", "导入报告已写入：%s", reportPath)
	j.Log("info", importSummaryLine(report))
	return ImportResult{Path: exportPath, Report: report, Manifest: manifest}, nil
}

func importSummaryLine(report ImportReport) string {
	total := len(report.Matches)
	elapsed := formatElapsed(reportElapsed(report))
	if report.DryRun {
		return fmt.Sprintf(
			"导入验证总结：项目 %d 个，匹配 %d 个，未匹配 %d 个，歧义 %d 个，错误 %d 个，用时 %s；本次未写入元数据和图片。",
			total,
			report.Summary.Matched,
			report.Summary.Unmatched,
			report.Summary.Ambiguous,
			report.Summary.Errors,
			elapsed,
		)
	}
	return fmt.Sprintf(
		"导入总结：项目 %d 个，元数据成功 %d 个，未匹配 %d 个，歧义 %d 个，错误 %d 个，媒体图片成功 %d 张/失败 %d 张，人物头像成功 %d 张/失败 %d 张，用时 %s。",
		total,
		report.Summary.MetadataUpdated,
		report.Summary.Unmatched,
		report.Summary.Ambiguous,
		report.Summary.Errors,
		report.Summary.ItemImagesPushed,
		report.Summary.ItemImagesFailed,
		report.Summary.PeopleImages,
		report.Summary.PeopleImagesFailed,
		elapsed,
	)
}

func importTargetFromSystemInfo(info emby.SystemInfo) ImportTarget {
	return ImportTarget{
		ServerName: strings.TrimSpace(info.ServerName),
		ServerID:   strings.TrimSpace(info.ID),
		Version:    strings.TrimSpace(info.Version),
	}
}

func initialImportDiff(expected storage.Summary) ImportDiff {
	return ImportDiff{
		Mode:     "package-vs-run",
		Note:     "对比导出包期望数量与本次导入实际写入结果；不是目标 Emby 数据库的完整前后快照。",
		Expected: expected,
		Before:   expected,
	}
}

func finalizeImportDiff(diff ImportDiff, actual storage.Summary) ImportDiff {
	diff.Actual = actual
	diff.After = actual
	diff.Missing = ImportDiffGap{
		Metadata:     positiveDiff(diff.Expected.Items-diff.Expected.SkippedItems, actual.MetadataUpdated),
		ItemImages:   positiveDiff(diff.Expected.ItemImages, actual.ItemImagesPushed),
		PeopleImages: positiveDiff(diff.Expected.PeopleImages, actual.PeopleImages),
		Unmatched:    actual.Unmatched,
		Ambiguous:    actual.Ambiguous,
		Errors:       actual.Errors + actual.ItemImagesFailed + actual.PeopleImagesFailed,
	}
	return diff
}

func positiveDiff(expected, actual int) int {
	if expected <= actual {
		return 0
	}
	return expected - actual
}

func reportElapsed(report ImportReport) time.Duration {
	if report.StartedAt.IsZero() {
		return 0
	}
	end := report.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	if end.Before(report.StartedAt) {
		return 0
	}
	return end.Sub(report.StartedAt)
}

func importableManifestItems(items []storage.ItemEntry) []storage.ItemEntry {
	out := make([]storage.ItemEntry, 0, len(items))
	for _, item := range items {
		if item.Skipped {
			continue
		}
		out = append(out, item)
	}
	return out
}

func nextImportReportPath(exportPath string, now time.Time) (string, error) {
	base := "import-report-" + now.Format("20060102-150405.000000000")
	for i := 0; i < 1000; i++ {
		name := base + ".json"
		if i > 0 {
			name = fmt.Sprintf("%s-%03d.json", base, i+1)
		}
		reportPath := filepath.Join(exportPath, name)
		if _, err := os.Stat(reportPath); err != nil {
			if os.IsNotExist(err) {
				return reportPath, nil
			}
			return "", err
		}
	}
	return "", fmt.Errorf("cannot allocate unique import report name")
}

func importIncrementalReport(manifest storage.Manifest) *ImportIncremental {
	if manifest.Incremental == nil {
		return nil
	}
	report := &ImportIncremental{
		Source:             "manifest",
		BaselineExportName: manifest.Incremental.BaselineExportName,
		BaselineExportPath: manifest.Incremental.BaselineExportPath,
		ChangedItems:       manifest.Incremental.ChangedItems,
		SkippedItems:       manifest.Incremental.SkippedItems,
		TargetMode:         "package-only",
		Note:               "Import skips items marked unchanged by the export manifest; it does not perform target-side incremental comparison.",
	}
	if report.SkippedItems == 0 {
		report.SkippedItems = countSkippedManifestItems(manifest.Items)
	}
	if report.ChangedItems == 0 && len(manifest.Items) > 0 {
		report.ChangedItems = len(manifest.Items) - report.SkippedItems
	}
	return report
}

func countSkippedManifestItems(items []storage.ItemEntry) int {
	count := 0
	for _, item := range items {
		if item.Skipped {
			count++
		}
	}
	return count
}

func addSkippedWrites(report *ImportReport, source string, count int) {
	if count <= 0 {
		return
	}
	if report.Skips == nil {
		report.Skips = &ImportSkipReport{}
	}
	report.WritesSkipped += count
	report.Skips.Total += count
	switch source {
	case "incremental-manifest":
		report.Skips.IncrementalManifest += count
	case "resume":
		report.Skips.Resume += count
	case "dry-run":
		report.Skips.DryRunWrites += count
	}
}

func (s *Service) resumeSuccessfulItems(exportPath string, target ImportTarget) (map[string]bool, string) {
	entries, err := os.ReadDir(exportPath)
	if err != nil {
		return nil, ""
	}
	done := map[string]bool{}
	sources := make([]string, 0)
	if checkpoint, ok := readImportCheckpoint(filepath.Join(exportPath, "import-checkpoint.json"), target); ok {
		for key, item := range checkpoint.Items {
			if shouldResumeItemCheckpoint(item) {
				done[key] = true
			}
		}
		if len(done) > 0 {
			sources = append(sources, "import-checkpoint.json")
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !isImportReportFile(entry.Name()) {
			continue
		}
		var report ImportReport
		if err := storage.ReadJSON(filepath.Join(exportPath, entry.Name()), &report); err != nil {
			continue
		}
		if !sameImportTarget(report.Target, target) {
			continue
		}
		added := 0
		for _, match := range report.Matches {
			if shouldResumeMatch(match, report.DryRun) {
				done[match.StableKey] = true
				added++
			}
		}
		if added > 0 {
			sources = append(sources, entry.Name())
		}
	}
	if len(done) == 0 {
		return nil, ""
	}
	return done, strings.Join(sources, ",")
}

func readImportCheckpoint(path string, target ImportTarget) (importCheckpoint, bool) {
	var checkpoint importCheckpoint
	if err := storage.ReadJSON(path, &checkpoint); err != nil {
		return importCheckpoint{}, false
	}
	if !sameImportTarget(checkpoint.Target, target) {
		return importCheckpoint{}, false
	}
	return checkpoint, true
}

func shouldResumeStatus(status string, dryRun bool) bool {
	switch status {
	case "updated":
		return true
	case "matched":
		return !dryRun
	default:
		return false
	}
}

func shouldResumeMatch(match ImportMatch, dryRun bool) bool {
	if match.ImageFailures > 0 {
		return false
	}
	return shouldResumeStatus(match.Status, dryRun)
}

func shouldResumeItemCheckpoint(item ImportCheckpoint) bool {
	if item.ImageFailures > 0 {
		return false
	}
	return shouldResumeStatus(item.Status, false)
}

func shouldResumePersonAvatarCheckpoint(item ImportCheckpoint) bool {
	return item.Status == "uploaded"
}

func sameImportTarget(a, b ImportTarget) bool {
	aID := strings.TrimSpace(a.ServerID)
	bID := strings.TrimSpace(b.ServerID)
	if aID != "" && bID != "" {
		return aID == bID
	}
	if aID != "" || bID != "" {
		return false
	}
	aName := strings.TrimSpace(a.ServerName)
	bName := strings.TrimSpace(b.ServerName)
	aVersion := strings.TrimSpace(a.Version)
	bVersion := strings.TrimSpace(b.Version)
	if aName == "" && aVersion == "" && bName == "" && bVersion == "" {
		return true
	}
	if aName == "" || bName == "" || aVersion == "" || bVersion == "" {
		return false
	}
	return aName == bName && aVersion == bVersion
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

type importCheckpointStore struct {
	path   string
	target ImportTarget
	mu     sync.Mutex
}

func newImportCheckpointStore(exportPath string, target ImportTarget) *importCheckpointStore {
	return &importCheckpointStore{
		path:   filepath.Join(exportPath, "import-checkpoint.json"),
		target: target,
	}
}

func (s *importCheckpointStore) Record(match ImportMatch) error {
	if s == nil || strings.TrimSpace(match.StableKey) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint := importCheckpoint{SchemaVersion: 1, Target: s.target, Items: map[string]ImportCheckpoint{}}
	if err := storage.ReadJSON(s.path, &checkpoint); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !sameImportTarget(checkpoint.Target, s.target) {
		checkpoint.Items = map[string]ImportCheckpoint{}
		checkpoint.PersonAvatars = map[string]ImportCheckpoint{}
	}
	if checkpoint.Items == nil {
		checkpoint.Items = map[string]ImportCheckpoint{}
	}
	now := time.Now()
	checkpoint.SchemaVersion = 1
	checkpoint.Target = s.target
	checkpoint.UpdatedAt = now
	checkpoint.Items[match.StableKey] = ImportCheckpoint{
		StableKey:     match.StableKey,
		SourceName:    match.SourceName,
		TargetID:      firstNonEmpty(match.TargetID, match.TargetEmbyID),
		TargetName:    match.TargetName,
		Status:        match.Status,
		ImageFailures: match.ImageFailures,
		UpdatedAt:     now,
	}
	return storage.WriteJSON(s.path, checkpoint)
}

func (s *importCheckpointStore) RecordPersonAvatar(result personImageResult) error {
	if s == nil || strings.TrimSpace(result.StableKey) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	checkpoint := importCheckpoint{SchemaVersion: 1, Target: s.target, Items: map[string]ImportCheckpoint{}, PersonAvatars: map[string]ImportCheckpoint{}}
	if err := storage.ReadJSON(s.path, &checkpoint); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !sameImportTarget(checkpoint.Target, s.target) {
		checkpoint.Items = map[string]ImportCheckpoint{}
		checkpoint.PersonAvatars = map[string]ImportCheckpoint{}
	}
	if checkpoint.Items == nil {
		checkpoint.Items = map[string]ImportCheckpoint{}
	}
	if checkpoint.PersonAvatars == nil {
		checkpoint.PersonAvatars = map[string]ImportCheckpoint{}
	}
	now := time.Now()
	checkpoint.SchemaVersion = 1
	checkpoint.Target = s.target
	checkpoint.UpdatedAt = now
	checkpoint.PersonAvatars[result.StableKey] = ImportCheckpoint{
		StableKey:  result.StableKey,
		SourceName: result.Name,
		TargetID:   result.TargetID,
		Status:     "uploaded",
		UpdatedAt:  now,
	}
	return storage.WriteJSON(s.path, checkpoint)
}

func emptyDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func shouldCheckpointMatch(match ImportMatch, dryRun bool) bool {
	if dryRun || strings.TrimSpace(match.StableKey) == "" {
		return false
	}
	if match.ImageFailures > 0 {
		return false
	}
	switch match.Status {
	case "updated":
		return true
	case "matched":
		return true
	default:
		return false
	}
}

func addImportMatchSummary(report *ImportReport, match ImportMatch, dryRun bool) {
	switch match.Status {
	case "updated":
		report.Summary.Matched++
		report.Summary.MetadataUpdated++
	case "matched":
		report.Summary.Matched++
		if dryRun {
			addSkippedWrites(report, "dry-run", 1)
		}
	case "ambiguous":
		report.Summary.Ambiguous++
	case "failed":
		report.Summary.Errors++
	default:
		report.Summary.Unmatched++
	}
	report.Summary.ItemImagesPushed += match.ImagesPushed
	report.Summary.ItemImagesFailed += match.ImageFailures
}

func addFailureExample(report *FailureReport, match ImportMatch) {
	example := FailureExample{
		StableKey:  match.StableKey,
		SourceName: match.SourceName,
		Status:     match.Status,
		Reason:     match.Reason,
		Error:      match.Error,
		Candidates: append([]string(nil), match.Candidates...),
	}
	switch match.Status {
	case "unmatched":
		counts := failureCounts(report)
		counts.Unmatched++
		report.Total++
		report.All = append(report.All, example)
		report.Unmatched = appendLimitedFailure(report.Unmatched, example)
	case "ambiguous":
		counts := failureCounts(report)
		counts.Ambiguous++
		report.Total++
		report.All = append(report.All, example)
		report.Ambiguous = appendLimitedFailure(report.Ambiguous, example)
	case "failed":
		counts := failureCounts(report)
		counts.Failed++
		report.Total++
		report.All = append(report.All, example)
		report.Failed = appendLimitedFailure(report.Failed, example)
	}
	finalizeFailureReport(report)
}

func addImageFailureExamples(report *FailureReport, match ImportMatch) {
	if match.ImageFailures <= 0 {
		return
	}
	counts := failureCounts(report)
	for _, errText := range match.ImageErrors {
		example := FailureExample{
			StableKey:  match.StableKey,
			SourceName: match.SourceName,
			Status:     "image-failed",
			Reason:     "item-image",
			Error:      errText,
		}
		counts.ImageFailed++
		report.Total++
		report.All = append(report.All, example)
		report.ImageFailed = appendLimitedFailure(report.ImageFailed, example)
	}
	if len(match.ImageErrors) == 0 {
		example := FailureExample{
			StableKey:  match.StableKey,
			SourceName: match.SourceName,
			Status:     "image-failed",
			Reason:     "item-image",
			Error:      fmt.Sprintf("%d image(s) failed", match.ImageFailures),
		}
		counts.ImageFailed += match.ImageFailures
		report.Total += match.ImageFailures
		report.All = append(report.All, example)
		report.ImageFailed = appendLimitedFailure(report.ImageFailed, example)
	}
	finalizeFailureReport(report)
}

func addPersonImageFailureExample(report *FailureReport, name string, err error) {
	if err == nil {
		return
	}
	counts := failureCounts(report)
	example := FailureExample{
		SourceName: strings.TrimSpace(name),
		Status:     "person-image-failed",
		Reason:     "person-image",
		Error:      err.Error(),
	}
	counts.PersonImageFailed++
	report.Total++
	report.All = append(report.All, example)
	report.PersonImageFailed = appendLimitedFailure(report.PersonImageFailed, example)
	finalizeFailureReport(report)
}

func finalizeFailureReport(report *FailureReport) {
	if report == nil || report.Counts == nil {
		return
	}
	report.Truncated = report.Counts.Unmatched > len(report.Unmatched) ||
		report.Counts.Ambiguous > len(report.Ambiguous) ||
		report.Counts.Failed > len(report.Failed) ||
		report.Counts.ImageFailed > len(report.ImageFailed) ||
		report.Counts.PersonImageFailed > len(report.PersonImageFailed)
	switch {
	case report.Counts.Failed > 0 || report.Counts.ImageFailed > 0 || report.Counts.PersonImageFailed > 0:
		report.Severity = "error"
	case report.Counts.Unmatched > 0 || report.Counts.Ambiguous > 0:
		report.Severity = "warning"
	default:
		report.Severity = ""
	}
}

func failureCounts(report *FailureReport) *FailureCounts {
	if report.Counts == nil {
		report.Counts = &FailureCounts{}
	}
	return report.Counts
}

func appendLimitedFailure(values []FailureExample, value FailureExample) []FailureExample {
	if len(values) >= 20 {
		return values
	}
	return append(values, value)
}

func (s *Service) importItems(ctx context.Context, j *job.Job, client *emby.Client, cache *importLookupCache, exportPath string, items []storage.ItemEntry, req ImportRequest, concurrency int, onResult func(ImportMatch)) ([]importItemResult, error) {
	results := make([]importItemResult, len(items))
	if len(items) == 0 {
		return results, nil
	}

	taskCh := make(chan importItemTask)
	resultCh := make(chan importItemResult, len(items))
	workers := workerCount(len(items), concurrency)
	if req.DryRun {
		j.Log("info", "开始验证项目匹配：%d 个，并发 %d，匹配超时 %s", len(items), workers, importMatchTimeout)
	} else {
		j.Log("info", "开始导入项目：%d 个，并发 %d，匹配超时 %s，元数据超时 %s，图片超时 %s，临时错误最多尝试 %d 次", len(items), workers, importMatchTimeout, itemMetadataTimeout, itemImageUploadTimeout, importRetryAttempts)
	}
	for i := 0; i < workers; i++ {
		go func() {
			for task := range taskCh {
				match := s.importItem(ctx, client, cache, exportPath, task.Item, req)
				resultCh <- importItemResult{Index: task.Index, Match: match}
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for idx, item := range items {
			if err := j.WaitIfPaused(ctx); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case taskCh <- importItemTask{Index: idx, Item: item}:
			}
		}
	}()

	done := 0
	ticker := time.NewTicker(importHeartbeatInterval)
	defer ticker.Stop()
	for done < len(items) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-resultCh:
			done++
			results[result.Index] = result
			match := result.Match
			if onResult != nil {
				onResult(match)
			}
			if match.Error != "" {
				j.Log("warn", "%s：%s", match.SourceName, match.Error)
			} else {
				j.Log("info", "%s：%s -> %s", match.SourceName, match.Status, match.TargetName)
			}
			if match.ImageFailures > 0 {
				j.Log("warn", "%s：媒体图片失败 %d 张：%s", match.SourceName, match.ImageFailures, strings.Join(match.ImageErrors, "；"))
			}
			if done == 1 || done%50 == 0 || done == len(items) {
				j.Log("info", "导入项目进度：%d/%d", done, len(items))
			}
		case <-ticker.C:
			if done < len(items) {
				j.Log("info", "导入项目等待中：已完成 %d/%d，剩余 %d 个；正在等待远程响应或慢请求超时", done, len(items), len(items)-done)
			}
		}
	}
	return results, nil
}

func (s *Service) importItem(ctx context.Context, client *emby.Client, cache *importLookupCache, exportPath string, entry storage.ItemEntry, req ImportRequest) ImportMatch {
	match := ImportMatch{StableKey: entry.StableKey, SourceName: entry.Name}
	var target emby.Item
	var candidates []emby.Item
	var reason string
	err := retryWithTimeout(ctx, importRetryAttempts, importMatchTimeout, func(attemptCtx context.Context) error {
		var attemptErr error
		target, candidates, reason, attemptErr = findMatchWithCache(attemptCtx, client, cache, entry, importTargetLibraryIDs(req))
		return attemptErr
	})
	if err != nil {
		match.Status = "failed"
		match.Reason = reason
		match.Error = err.Error()
		return match
	}
	for _, c := range candidates {
		match.Candidates = append(match.Candidates, c.Name)
	}
	if isAmbiguousReason(reason) {
		match.Status = "ambiguous"
		match.Reason = reason
		match.Strategy = reason
		return match
	}
	if target.ID == "" {
		match.Status = "unmatched"
		match.Reason = reason
		match.Strategy = reason
		return match
	}
	match.TargetID = target.ID
	match.TargetEmbyID = target.ID
	match.TargetName = target.Name
	match.Reason = reason
	match.Strategy = reason
	if req.DryRun {
		match.Status = "matched"
		return match
	}
	current := target
	mergeItemMetadata(&current, entry, exportPath)
	metadataErr := updateItemMetadata(ctx, client, target.ID, current)
	if !req.SkipImages {
		imageTypeSet := allowedImageTypes(req.ImageTypes)
		for _, img := range entry.Images {
			if len(imageTypeSet) > 0 && !imageTypeSet[strings.ToLower(img.Type)] {
				continue
			}
			imagePath, err := safePackagePath(exportPath, img.Path)
			if err == nil {
				data, err := os.ReadFile(imagePath)
				if err == nil {
					err = retryWithTimeout(ctx, importRetryAttempts, itemImageUploadTimeout, func(attemptCtx context.Context) error {
						return client.UploadImage(attemptCtx, target.ID, img.Type, data)
					})
				}
			}
			if err != nil {
				match.ImageFailures++
				match.ImageErrors = append(match.ImageErrors, fmt.Sprintf("%s 处理失败: %v", img.Type, err))
				continue
			}
			match.ImagesPushed++
		}
	}
	if metadataErr != nil {
		match.Status = "failed"
		match.Error = metadataErr.Error()
		return match
	}
	match.Status = "updated"
	return match
}

func updateItemMetadata(ctx context.Context, client *emby.Client, targetID string, item emby.Item) error {
	err := retryWithTimeout(ctx, importRetryAttempts, itemMetadataTimeout, func(attemptCtx context.Context) error {
		return client.UpdateItem(attemptCtx, targetID, item)
	})
	if err == nil || !shouldRetryWithMinimalMetadata(err) {
		return err
	}
	compatibility := item
	compatibility.Raw = compatibilityMetadataPayload(item.Raw)
	err = retryWithTimeout(ctx, importRetryAttempts, itemMetadataTimeout, func(attemptCtx context.Context) error {
		return client.UpdateItem(attemptCtx, targetID, compatibility)
	})
	if err == nil || !shouldRetryWithMinimalMetadata(err) {
		return err
	}
	fallback := item
	fallback.Raw = minimalMetadataPayload(item.Raw)
	return retryWithTimeout(ctx, importRetryAttempts, itemMetadataTimeout, func(attemptCtx context.Context) error {
		return client.UpdateItem(attemptCtx, targetID, fallback)
	})
}

func shouldRetryWithMinimalMetadata(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "http 400") ||
		strings.Contains(text, "value cannot be null") ||
		strings.Contains(text, "parameter 'source'")
}

func compatibilityMetadataPayload(raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for key, value := range raw {
		if strings.EqualFold(key, "Studios") {
			continue
		}
		out[key] = value
	}
	if _, ok := out["Source"]; !ok {
		out["Source"] = "Unknown"
	}
	if _, ok := out["ProviderIds"]; !ok {
		out["ProviderIds"] = map[string]string{}
	}
	return out
}

func minimalMetadataPayload(raw map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"Id", "Type", "Name", "Source", "ProviderIds"} {
		if value, ok := raw[key]; ok && value != nil {
			out[key] = value
		}
	}
	if _, ok := out["Source"]; !ok {
		out["Source"] = "Unknown"
	}
	if _, ok := out["ProviderIds"]; !ok {
		out["ProviderIds"] = map[string]string{}
	}
	return out
}

func retryWithTimeout(ctx context.Context, attempts int, timeout time.Duration, operation func(context.Context) error) error {
	if attempts <= 0 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		err := operation(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = err
		if attempt == attempts || !isTransientImportError(err) {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * 500 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	if attempts > 1 && isTransientImportError(lastErr) {
		return fmt.Errorf("重试 %d 次后仍失败: %w", attempts, lastErr)
	}
	return lastErr
}

func isTransientImportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	text := strings.ToLower(err.Error())
	transientMarkers := []string{
		"timeout",
		"deadline exceeded",
		"use of closed network connection",
		"server closed idle connection",
		"connection reset",
		"connection refused",
		"unexpected eof",
		"tls handshake timeout",
		"i/o timeout",
	}
	for _, marker := range transientMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	for _, code := range []string{"http 429", "http 500", "http 502", "http 503", "http 504"} {
		if strings.Contains(text, code) {
			return true
		}
	}
	return false
}

func allowedImageTypes(imageTypes []string) map[string]bool {
	allowed := map[string]bool{}
	for _, typ := range imageTypes {
		typ = strings.TrimSpace(strings.ToLower(typ))
		if typ != "" {
			allowed[typ] = true
		}
	}
	return allowed
}

func mergeItemMetadata(current *emby.Item, entry storage.ItemEntry, exportPath string) {
	var info storage.ItemInfo
	infoPath, err := safePackagePath(exportPath, entry.InfoPath)
	if err != nil {
		return
	}
	if err := storage.ReadJSON(infoPath, &info); err != nil {
		return
	}
	source := info.Item
	payload := map[string]any{}
	setIfNotEmpty(payload, "Id", current.ID)
	setIfNotEmpty(payload, "Type", current.Type)
	copyCurrentUpdateContext(payload, current.Raw)
	if _, ok := payload["Source"]; !ok {
		payload["Source"] = "Unknown"
	}
	if source.Name != "" {
		setIfNotEmpty(payload, "Name", source.Name)
	} else if entry.Name != "" {
		setIfNotEmpty(payload, "Name", entry.Name)
	} else {
		setIfNotEmpty(payload, "Name", current.Name)
	}
	setIfNotEmpty(payload, "OriginalTitle", source.OriginalTitle)
	setIfNotEmpty(payload, "Overview", source.Overview)
	setIfNotEmpty(payload, "OfficialRating", source.OfficialRating)
	setIfNotEmpty(payload, "PremiereDate", source.PremiereDate)
	if source.ProductionYear != 0 {
		payload["ProductionYear"] = source.ProductionYear
	}
	if source.CommunityRating != 0 {
		payload["CommunityRating"] = source.CommunityRating
	}
	if len(source.Genres) > 0 {
		payload["Genres"] = source.Genres
	}
	if len(source.Tags) > 0 {
		payload["Tags"] = source.Tags
	}
	if len(source.Taglines) > 0 {
		payload["Taglines"] = source.Taglines
	}
	if studios := portableNameIDs(source.Studios); len(studios) > 0 {
		payload["Studios"] = studios
	}
	if people := portablePeople(source.People); len(people) > 0 {
		payload["People"] = people
	}
	if len(source.ProviderIDs) > 0 {
		payload["ProviderIds"] = source.ProviderIDs
	} else if len(current.ProviderIDs) > 0 {
		payload["ProviderIds"] = current.ProviderIDs
	} else {
		payload["ProviderIds"] = map[string]string{}
	}
	current.Raw = payload
}

func copyCurrentUpdateContext(target map[string]any, current map[string]any) {
	if len(current) == 0 {
		return
	}
	for _, key := range []string{"Source", "ParentId"} {
		if value, ok := current[key]; ok && value != nil {
			target[key] = value
		}
	}
}

func portableNameIDs(values []emby.NameID) []map[string]string {
	out := make([]map[string]string, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value.Name)
		if name == "" {
			continue
		}
		out = append(out, map[string]string{"Name": name})
	}
	return out
}

func portablePeople(values []emby.Person) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value.Name)
		if name == "" {
			continue
		}
		person := map[string]any{
			"Name":        name,
			"ProviderIds": map[string]string{},
		}
		if typ := strings.TrimSpace(value.Type); typ != "" {
			person["Type"] = typ
		}
		if role := strings.TrimSpace(value.Role); role != "" {
			person["Role"] = role
		}
		if len(value.ProviderIDs) > 0 {
			person["ProviderIds"] = value.ProviderIDs
		}
		out = append(out, person)
	}
	return out
}

func setIfNotEmpty(raw map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		raw[key] = value
	}
}

func (s *Service) importPeopleImages(ctx context.Context, client *emby.Client, cache *importLookupCache, checkpoint *importCheckpointStore, exportPath string, manifest storage.Manifest, report *ImportReport, j *job.Job, resume bool, dryRun bool, concurrency int) error {
	tasks := make([]personImageTask, 0)
	resumeDone := map[string]bool{}
	if resume {
		if existing, ok := readImportCheckpoint(filepath.Join(exportPath, "import-checkpoint.json"), report.Target); ok {
			for key, avatar := range existing.PersonAvatars {
				if shouldResumePersonAvatarCheckpoint(avatar) {
					resumeDone[key] = true
				}
			}
		}
	}
	for _, person := range manifest.People {
		if person.Image == nil || person.Name == "" {
			continue
		}
		if resumeDone[person.StableKey] {
			addSkippedWrites(report, "resume", 1)
			continue
		}
		tasks = append(tasks, personImageTask{StableKey: person.StableKey, Name: person.Name, Path: person.Image.Path})
	}
	if dryRun {
		if len(tasks) > 0 {
			j.Log("info", "[DRY] 跳过人物头像写入验证：%d 个头像会在实际导入写入元数据后再匹配", len(tasks))
		}
		return nil
	}
	if len(tasks) == 0 {
		return nil
	}

	workers := workerCount(len(tasks), concurrency)
	j.Log("info", "开始导入人物头像：%d 个，并发 %d，单次超时 %s，临时错误最多尝试 %d 次", len(tasks), workers, personImageUploadTimeout, importRetryAttempts)

	taskCh := make(chan personImageTask)
	resultCh := make(chan personImageResult, len(tasks))
	for i := 0; i < workers; i++ {
		go func() {
			for task := range taskCh {
				resultCh <- s.importPersonImage(ctx, client, cache, exportPath, task)
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for _, task := range tasks {
			if err := j.WaitIfPaused(ctx); err != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case taskCh <- task:
			}
		}
	}()

	done := 0
	detailedFailures := 0
	ticker := time.NewTicker(importHeartbeatInterval)
	defer ticker.Stop()
	for done < len(tasks) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultCh:
			done++
			if result.Err != nil {
				report.Summary.PeopleImagesFailed++
				addPersonImageFailureExample(&report.Failures, result.Name, result.Err)
				detailedFailures++
				if detailedFailures <= 10 {
					j.Log("warn", "人物头像回写失败：%s - %v", result.Name, result.Err)
				} else if detailedFailures == 11 {
					j.Log("warn", "人物头像失败较多，后续失败只在进度和总结中统计")
				}
			} else {
				report.Summary.PeopleImages++
				if checkpoint != nil {
					if err := checkpoint.RecordPersonAvatar(result); err != nil {
						j.Log("warn", "写入人物头像断点状态失败：%v", err)
					}
				}
			}
			if done == 1 || done%peopleImageProgressEvery == 0 || done == len(tasks) {
				j.Log("info", "人物头像进度：%d/%d，成功 %d，失败 %d", done, len(tasks), report.Summary.PeopleImages, report.Summary.PeopleImagesFailed)
			}
		case <-ticker.C:
			if done < len(tasks) {
				j.Log("info", "人物头像等待中：已完成 %d/%d，剩余 %d 个；正在等待远程响应或慢请求超时", done, len(tasks), len(tasks)-done)
			}
		}
	}
	j.Log("info", "人物头像导入完成：成功 %d，失败 %d", report.Summary.PeopleImages, report.Summary.PeopleImagesFailed)
	return nil
}

func (s *Service) importPersonImage(ctx context.Context, client *emby.Client, cache *importLookupCache, exportPath string, task personImageTask) personImageResult {
	imagePath, err := safePackagePath(exportPath, task.Path)
	if err != nil {
		return personImageResult{StableKey: task.StableKey, Name: task.Name, Err: fmt.Errorf("头像路径无效: %w", err)}
	}
	data, err := os.ReadFile(imagePath)
	if err != nil {
		return personImageResult{StableKey: task.StableKey, Name: task.Name, Err: fmt.Errorf("读取头像失败: %w", err)}
	}
	person, err := cache.findPersonByName(ctx, client, task.Name)
	if err != nil {
		return personImageResult{StableKey: task.StableKey, Name: task.Name, Err: err}
	}
	personID := strings.TrimSpace(string(person.ID))
	if personID == "" {
		return personImageResult{StableKey: task.StableKey, Name: task.Name, Err: fmt.Errorf("target person %q has empty id", task.Name)}
	}
	if err := retryWithTimeout(ctx, importRetryAttempts, personImageUploadTimeout, func(attemptCtx context.Context) error {
		return client.UploadPersonImageByID(attemptCtx, personID, data)
	}); err != nil {
		return personImageResult{StableKey: task.StableKey, Name: task.Name, TargetID: personID, Err: err}
	}
	return personImageResult{StableKey: task.StableKey, Name: task.Name, TargetID: personID}
}

func FindMatch(ctx context.Context, client *emby.Client, entry storage.ItemEntry) (emby.Item, []emby.Item, string, error) {
	return findMatchWithCache(ctx, client, newImportLookupCache(), entry)
}

func findMatchWithCache(ctx context.Context, client *emby.Client, cache *importLookupCache, entry storage.ItemEntry, libraryIDs ...[]string) (emby.Item, []emby.Item, string, error) {
	targetLibraryIDs := []string(nil)
	if len(libraryIDs) > 0 {
		targetLibraryIDs = normalizeTargetLibraryIDs(libraryIDs[0])
	}
	var firstSearchErr error
	var firstAmbiguous []emby.Item
	var firstAmbiguousReason string
	rememberSearchErr := func(err error) {
		if err != nil && firstSearchErr == nil {
			firstSearchErr = err
		}
	}
	rememberAmbiguous := func(reason string, items []emby.Item) {
		if firstAmbiguousReason == "" && len(items) > 0 {
			firstAmbiguousReason = reason
			firstAmbiguous = items
		}
	}
	if stem := mediaPathStem(entry.Path); stem != "" {
		items, err := cache.searchItems(ctx, client, searchPrefix(stem, 30), entry.Type, 50, targetLibraryIDs)
		if err == nil {
			if entry.Type == "Season" {
				matches := seasonMatches(entry, items)
				if len(matches) == 1 {
					return matches[0], matches, "season-parent", nil
				}
				if len(matches) > 1 {
					rememberAmbiguous("season-parent-ambiguous", matches)
				}
			} else {
				matches := mediaStemMatches(stem, entry.Type, items)
				if len(matches) == 1 {
					return matches[0], matches, "media-file", nil
				}
				if len(matches) > 1 {
					rememberAmbiguous("media-file-ambiguous", matches)
				}
			}
		} else {
			rememberSearchErr(err)
		}
	}

	providerValues := providerIDsForSearch(entry.ProviderIDs)
	for _, value := range providerValues {
		items, err := cache.itemsByProviderID(ctx, client, value, targetLibraryIDs)
		if err == nil && len(items) == 1 {
			return items[0], items, "provider-id", nil
		}
		if err == nil && len(items) > 1 {
			rememberAmbiguous("provider-id-ambiguous", items)
		}
		if err != nil {
			rememberSearchErr(err)
		}
	}
	if entry.Type == "Episode" && entry.SeriesName != "" {
		items, err := cache.searchItems(ctx, client, entry.SeriesName, "Episode", 300, targetLibraryIDs)
		if err == nil {
			matches := episodeMatches(entry, items)
			if len(matches) == 1 {
				return matches[0], matches, "episode-number", nil
			}
			if len(matches) > 1 {
				rememberAmbiguous("episode-ambiguous", matches)
			}
		} else {
			rememberSearchErr(err)
		}
	}
	if entry.Type == "Season" {
		for _, term := range seasonSearchTerms(entry) {
			items, err := cache.searchItems(ctx, client, term, "Season", 300, targetLibraryIDs)
			if err != nil {
				rememberSearchErr(err)
				continue
			}
			matches := seasonMatches(entry, items)
			if len(matches) == 1 {
				return matches[0], matches, "season-parent", nil
			}
			if len(matches) > 1 {
				rememberAmbiguous("season-parent-ambiguous", matches)
			}
		}
	}
	if strings.TrimSpace(entry.Name) != "" {
		items, err := cache.searchItems(ctx, client, entry.Name, entry.Type, 20, targetLibraryIDs)
		if err == nil {
			if entry.Type == "Episode" {
				matches := episodeMatches(entry, items)
				if len(matches) == 1 {
					return matches[0], matches, "episode-number", nil
				}
				if len(matches) > 1 {
					rememberAmbiguous("episode-ambiguous", matches)
				}
			}
			exact := exactNameMatches(items, entry.Name, entry.Type)
			if item, matches, ok := chooseUniqueMatch(exact, entry.ProductionYear); ok {
				return item, matches, "name-exact", nil
			}
			if len(exact) > 0 {
				rememberAmbiguous("name-ambiguous", exact)
			}
			if len(items) > 0 {
				rememberAmbiguous("name-search-ambiguous", items)
			}
		} else {
			rememberSearchErr(err)
		}
	}
	shortName := ShortName(entry.Name)
	if shortName != "" && shortName != entry.Name {
		items, err := cache.searchItems(ctx, client, shortName, entry.Type, 20, targetLibraryIDs)
		if err == nil && len(items) > 0 {
			exact := exactNameMatches(items, shortName, entry.Type)
			if item, matches, ok := chooseUniqueMatch(exact, entry.ProductionYear); ok {
				return item, matches, "short-name-exact", nil
			}
			if len(exact) > 0 {
				rememberAmbiguous("short-name-ambiguous", exact)
			}
			rememberAmbiguous("short-name-search-ambiguous", items)
		} else if err != nil {
			rememberSearchErr(err)
		}
	}
	if entry.OriginalTitle != "" && entry.OriginalTitle != entry.Name {
		items, err := cache.searchItems(ctx, client, entry.OriginalTitle, entry.Type, 20, targetLibraryIDs)
		if err == nil && len(items) > 0 {
			exact := originalTitleMatches(items, entry.OriginalTitle, entry.Type)
			if item, matches, ok := chooseUniqueMatch(exact, entry.ProductionYear); ok {
				return item, matches, "original-title", nil
			}
			if len(exact) > 0 {
				rememberAmbiguous("original-title-ambiguous", exact)
			}
			rememberAmbiguous("original-title-search-ambiguous", items)
		} else if err != nil {
			rememberSearchErr(err)
		}
	}
	if firstAmbiguousReason != "" {
		return emby.Item{}, firstAmbiguous, firstAmbiguousReason, nil
	}
	if firstSearchErr != nil {
		return emby.Item{}, nil, "search-error", firstSearchErr
	}
	return emby.Item{}, nil, "no-match", nil
}

func mediaStemMatches(sourceStem string, itemType string, items []emby.Item) []emby.Item {
	sourceStem = strings.TrimSpace(sourceStem)
	matches := make([]emby.Item, 0)
	for _, item := range items {
		if itemType != "" && item.Type != "" && item.Type != itemType {
			continue
		}
		if strings.EqualFold(mediaPathStem(item.Path), sourceStem) {
			matches = append(matches, item)
		}
	}
	return matches
}

func searchPrefix(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

type episodeNumber struct {
	Season  int
	Episode int
}

func episodeMatches(entry storage.ItemEntry, items []emby.Item) []emby.Item {
	source, ok := episodeNumberFromEntry(entry)
	if !ok {
		return nil
	}
	sourceSeries := normalizedSeasonSeriesCandidates(entry.SeriesName, entry.Path)
	matches := make([]emby.Item, 0)
	for _, item := range items {
		targetSeries := normalizedSeasonSeriesCandidates(item.SeriesName, item.Path)
		if len(sourceSeries) > 0 {
			if len(targetSeries) == 0 || !stringSetsOverlap(sourceSeries, targetSeries) {
				continue
			}
		} else if item.SeriesName != entry.SeriesName {
			continue
		}
		target, ok := episodeNumberFromItem(item)
		if !ok {
			continue
		}
		if target.Episode != source.Episode {
			continue
		}
		if source.Season != 0 && target.Season != 0 && source.Season != target.Season {
			continue
		}
		matches = append(matches, item)
	}
	return matches
}

func seasonMatches(entry storage.ItemEntry, items []emby.Item) []emby.Item {
	sourceSeason, hasSeason := seasonNumberFromEntry(entry)
	sourceSeries := normalizedSeasonSeriesCandidates(entry.SeriesName, entry.Path)
	if len(sourceSeries) == 0 && !hasSeason {
		return nil
	}
	matches := make([]emby.Item, 0)
	for _, item := range items {
		if item.Type != "" && item.Type != "Season" {
			continue
		}
		if hasSeason {
			targetSeason, ok := seasonNumberFromItem(item)
			if ok && targetSeason != sourceSeason {
				continue
			}
		}
		targetSeries := normalizedSeasonSeriesCandidates(item.SeriesName, item.Path)
		if len(sourceSeries) > 0 {
			if len(targetSeries) == 0 || !stringSetsOverlap(sourceSeries, targetSeries) {
				continue
			}
		}
		matches = append(matches, item)
	}
	return matches
}

func seasonSearchTerms(entry storage.ItemEntry) []string {
	terms := make([]string, 0, 3)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range terms {
			if strings.EqualFold(existing, value) {
				return
			}
		}
		terms = append(terms, value)
	}
	add(entry.Name)
	if season, ok := seasonNumberFromEntry(entry); ok {
		add(fmt.Sprintf("Season %d", season))
		add(fmt.Sprintf("第 %d 季", season))
	}
	return terms
}

func episodeNumberFromEntry(entry storage.ItemEntry) (episodeNumber, bool) {
	if entry.IndexNumber != 0 {
		return episodeNumber{Season: entry.ParentIndexNumber, Episode: entry.IndexNumber}, true
	}
	return episodeNumberFromText(entry.Path, entry.Name)
}

func episodeNumberFromItem(item emby.Item) (episodeNumber, bool) {
	if item.IndexNumber != 0 {
		return episodeNumber{Season: item.ParentIndexNumber, Episode: item.IndexNumber}, true
	}
	return episodeNumberFromText(item.Path, item.Name)
}

func episodeNumberFromText(values ...string) (episodeNumber, bool) {
	for _, value := range values {
		match := episodePattern.FindStringSubmatch(value)
		if len(match) != 3 {
			continue
		}
		season, seasonErr := strconv.Atoi(match[1])
		episode, episodeErr := strconv.Atoi(match[2])
		if seasonErr == nil && episodeErr == nil {
			return episodeNumber{Season: season, Episode: episode}, true
		}
	}
	return episodeNumber{}, false
}

func seasonNumberFromEntry(entry storage.ItemEntry) (int, bool) {
	if entry.IndexNumber != 0 {
		return entry.IndexNumber, true
	}
	return seasonNumberFromText(entry.Path, entry.Name)
}

func seasonNumberFromItem(item emby.Item) (int, bool) {
	if item.IndexNumber != 0 {
		return item.IndexNumber, true
	}
	return seasonNumberFromText(item.Path, item.Name)
}

func seasonNumberFromText(values ...string) (int, bool) {
	for _, value := range values {
		match := seasonPattern.FindStringSubmatch(strings.TrimSpace(value))
		if len(match) == 0 {
			continue
		}
		for _, group := range match[1:] {
			if group == "" {
				continue
			}
			season, err := strconv.Atoi(group)
			if err == nil {
				return season, true
			}
		}
	}
	return 0, false
}

func normalizedSeasonSeriesCandidates(seriesName string, itemPath string) map[string]bool {
	values := []string{seriesName, seasonParentDirectory(itemPath)}
	out := map[string]bool{}
	for _, value := range values {
		if normalized := normalizeSeriesCandidate(value); normalized != "" {
			out[normalized] = true
		}
	}
	return out
}

func seasonParentDirectory(value string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	if normalized == "" {
		return ""
	}
	dir := path.Dir(normalized)
	if dir == "." || dir == "/" {
		return ""
	}
	return path.Base(dir)
}

func normalizeSeriesCandidate(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = seriesYearBraceTail.ReplaceAllString(value, "")
	value = seriesYearMetaTail.ReplaceAllString(value, "")
	value = seriesYearTail.ReplaceAllString(value, "")
	if idx := strings.Index(value, "{"); idx > 0 {
		value = strings.TrimSpace(value[:idx])
	}
	if value == "" {
		return ""
	}
	normalized := storage.Slug(value)
	if normalized == "unknown" {
		return ""
	}
	return normalized
}

func stringSetsOverlap(a map[string]bool, b map[string]bool) bool {
	for value := range a {
		if b[value] {
			return true
		}
	}
	return false
}

func exactNameMatches(items []emby.Item, name string, itemType string) []emby.Item {
	name = strings.TrimSpace(name)
	matches := make([]emby.Item, 0)
	for _, item := range items {
		if itemType != "" && item.Type != "" && item.Type != itemType {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Name), name) {
			matches = append(matches, item)
		}
	}
	return matches
}

func originalTitleMatches(items []emby.Item, title string, itemType string) []emby.Item {
	title = strings.TrimSpace(title)
	matches := make([]emby.Item, 0)
	for _, item := range items {
		if itemType != "" && item.Type != "" && item.Type != itemType {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.Name), title) ||
			strings.EqualFold(strings.TrimSpace(item.OriginalTitle), title) {
			matches = append(matches, item)
		}
	}
	return matches
}

func chooseUniqueMatch(matches []emby.Item, productionYear int) (emby.Item, []emby.Item, bool) {
	if productionYear != 0 {
		yearMatches := make([]emby.Item, 0, len(matches))
		for _, item := range matches {
			if item.ProductionYear == productionYear {
				yearMatches = append(yearMatches, item)
			}
		}
		if len(yearMatches) == 1 {
			return yearMatches[0], yearMatches, true
		}
		if len(yearMatches) > 1 {
			return emby.Item{}, yearMatches, false
		}
	}
	if len(matches) == 1 {
		return matches[0], matches, true
	}
	return emby.Item{}, matches, false
}

func isAmbiguousReason(reason string) bool {
	return strings.Contains(reason, "ambiguous")
}

func providerIDsForSearch(ids map[string]string) []string {
	values := make([]string, 0, len(ids))
	for provider, id := range ids {
		if id == "" {
			continue
		}
		values = append(values, provider+"."+id)
	}
	sort.Strings(values)
	return values
}

func ShortName(name string) string {
	name = strings.TrimSpace(name)
	name = shortNameYearDashTail.ReplaceAllString(name, "")
	name = shortNameYearMetaTail.ReplaceAllString(name, "")
	if idx := strings.Index(name, "{"); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}
	if idx := strings.Index(name, " - "); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}
	name = strings.TrimSpace(strings.TrimSuffix(name, "(1080p)"))
	return name
}

func MatchForImport(source ExportedItem, candidates []ImportCandidate) ImportMatch {
	match := ImportMatch{StableKey: source.StableKey, SourceName: source.Name, Status: "unmatched", Reason: "no-match"}
	for _, candidate := range candidates {
		if providerIDsOverlap(source.ProviderIDs, candidate.ProviderIDs) {
			return importMatchFromCandidate(source, candidate, "provider_ids")
		}
	}
	if source.Type == "Episode" && source.SeriesName != "" && source.EpisodeNumber != 0 {
		for _, candidate := range candidates {
			if candidate.Type == "Episode" &&
				candidate.SeriesName == source.SeriesName &&
				candidate.SeasonNumber == source.SeasonNumber &&
				candidate.EpisodeNumber == source.EpisodeNumber {
				return importMatchFromCandidate(source, candidate, "episode")
			}
		}
	}
	for _, candidate := range candidates {
		if candidate.Type != "" && source.Type != "" && candidate.Type != source.Type {
			continue
		}
		if strings.EqualFold(candidate.Name, source.Name) {
			return importMatchFromCandidate(source, candidate, "name")
		}
		if source.OriginalTitle != "" && strings.EqualFold(candidate.Name, source.OriginalTitle) {
			return importMatchFromCandidate(source, candidate, "original_title")
		}
		if candidate.OriginalTitle != "" && strings.EqualFold(candidate.OriginalTitle, source.Name) {
			return importMatchFromCandidate(source, candidate, "original_title")
		}
	}
	return match
}

func PlanExportAssets(item ExportedItem, options ExportOptions) []ExportAsset {
	imageTypes := options.ImageTypes
	if len(imageTypes) == 0 {
		imageTypes = emby.DefaultImageTypes
	}
	allowed := map[string]bool{}
	for _, typ := range imageTypes {
		allowed[strings.ToLower(typ)] = true
	}
	assets := make([]ExportAsset, 0)
	for _, typ := range imageTypes {
		if item.ImageTags[typ] == "" {
			continue
		}
		assets = append(assets, ExportAsset{Scope: "item", OwnerID: item.EmbyID, ImageType: typ})
	}
	if options.IncludePeopleImages {
		for _, person := range item.People {
			if person.StableKey == "" || person.PrimaryImageTag == "" {
				continue
			}
			if len(allowed) > 0 && !allowed["primary"] {
				continue
			}
			assets = append(assets, ExportAsset{Scope: "person", OwnerID: person.StableKey, ImageType: "Primary"})
		}
	}
	return assets
}

func MatchPersonForImport(source ExportedPerson, candidates []ImportPersonCandidate) ImportMatch {
	for _, candidate := range candidates {
		if providerIDsOverlap(source.ProviderIDs, candidate.ProviderIDs) {
			return ImportMatch{
				StableKey:    source.StableKey,
				SourceName:   source.Name,
				TargetID:     candidate.EmbyID,
				TargetEmbyID: candidate.EmbyID,
				TargetName:   candidate.Name,
				Status:       "matched",
				Reason:       "provider_ids",
				Strategy:     "provider_ids",
			}
		}
	}
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.Name, source.Name) {
			return ImportMatch{
				StableKey:    source.StableKey,
				SourceName:   source.Name,
				TargetID:     candidate.EmbyID,
				TargetEmbyID: candidate.EmbyID,
				TargetName:   candidate.Name,
				Status:       "matched",
				Reason:       "name",
				Strategy:     "name",
			}
		}
	}
	return ImportMatch{StableKey: source.StableKey, SourceName: source.Name, Status: "unmatched", Reason: "no-match"}
}

func RunImport(ctx context.Context, pkg ExportPackage, client ImportClient, options ImportOptions) (ImportReport, error) {
	report := ImportReport{StartedAt: time.Now(), DryRun: options.DryRun}
	for _, item := range pkg.Items {
		candidates, err := client.SearchCandidates(ctx, item)
		if err != nil {
			return report, err
		}
		match := MatchForImport(item, candidates)
		report.Matches = append(report.Matches, match)
		if match.TargetEmbyID == "" {
			report.Summary.Unmatched++
			continue
		}
		report.Summary.Matched++
		if options.DryRun {
			report.WritesSkipped++
			continue
		}
		if err := client.UpdateItem(ctx, match.TargetEmbyID, item); err != nil {
			return report, err
		}
		report.Summary.MetadataUpdated++
		for _, image := range item.Images {
			if err := client.UploadImage(ctx, match.TargetEmbyID, image); err != nil {
				return report, err
			}
			report.Summary.ItemImagesPushed++
		}
	}
	if options.IncludePeopleImages {
		for _, person := range pkg.People {
			if person.Image == nil {
				continue
			}
			candidates, err := client.SearchPersonCandidates(ctx, person)
			if err != nil {
				return report, err
			}
			match := MatchPersonForImport(person, candidates)
			report.PersonMatches = append(report.PersonMatches, match)
			if match.TargetEmbyID == "" {
				report.Summary.Unmatched++
				continue
			}
			report.Summary.Matched++
			if options.DryRun {
				report.WritesSkipped++
				continue
			}
			if err := client.UploadPersonImage(ctx, match.TargetEmbyID, *person.Image); err != nil {
				return report, err
			}
			report.Summary.PeopleImages++
		}
	}
	report.EndedAt = time.Now()
	return report, nil
}

func importMatchFromCandidate(source ExportedItem, candidate ImportCandidate, strategy string) ImportMatch {
	return ImportMatch{
		StableKey:    source.StableKey,
		SourceName:   source.Name,
		TargetID:     candidate.EmbyID,
		TargetEmbyID: candidate.EmbyID,
		TargetName:   candidate.Name,
		Status:       "matched",
		Reason:       strategy,
		Strategy:     strategy,
	}
}

func providerIDsOverlap(a, b map[string]string) bool {
	for provider, id := range a {
		if id == "" {
			continue
		}
		if b[provider] == id {
			return true
		}
	}
	return false
}
