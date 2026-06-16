package exporter

import (
	"context"
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
	Concurrency         int             `json:"concurrency"`
	DryRun              bool            `json:"dryRun"`
	SkipImages          bool            `json:"skipImages"`
	IncludePeopleImages bool            `json:"includePeopleImages"`
	Overwrite           bool            `json:"overwrite"`
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

type ImportReport struct {
	StartedAt     time.Time       `json:"startedAt"`
	EndedAt       time.Time       `json:"endedAt"`
	DryRun        bool            `json:"dryRun"`
	Matches       []ImportMatch   `json:"matches"`
	PersonMatches []ImportMatch   `json:"personMatches,omitempty"`
	Summary       storage.Summary `json:"summary"`
	WritesSkipped int             `json:"writesSkipped,omitempty"`
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

type personImageTask struct {
	Name string
	Path string
}

type personImageResult struct {
	Name string
	Err  error
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
	exportName := exportDirectoryName(exportedAt, info.ServerName, libraries)
	exportDir := filepath.Join(s.ExportsDir(), exportName)
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

	people := newPeopleRegistry()
	for _, lib := range libraries {
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
		results, err := s.exportLibraryItems(ctx, j, client, exportDir, lib, tasks, imageTypeSet, req, people, concurrency)
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

func (s *Service) exportLibraryItems(ctx context.Context, j *job.Job, client *emby.Client, exportDir string, lib emby.Library, tasks []exportItemTask, imageTypeSet map[string]bool, req ExportRequest, people *peopleRegistry, concurrency int) ([]exportItemResult, error) {
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
				entry, err := s.exportItem(ctx, client, exportDir, lib, task.Item, task.Slug, imageTypeSet, req, people)
				resultCh <- exportItemResult{Index: task.Index, Item: task.Item, Entry: entry, Err: err}
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for _, task := range tasks {
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

func (s *Service) exportItem(ctx context.Context, client *emby.Client, exportDir string, lib emby.Library, item emby.Item, itemSlug string, imageTypeSet map[string]bool, req ExportRequest, people *peopleRegistry) (storage.ItemEntry, error) {
	item = s.enrichExportItem(ctx, client, item)
	stableKey := storage.StableItemKey(item)
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
		InfoPath:          infoRel,
		RawPath:           rawRel,
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

func (s *Service) Import(ctx context.Context, j *job.Job, req ImportRequest) (ImportResult, error) {
	client, err := emby.NewClient(req.Connection.BaseURL, req.Connection.APIKey)
	if err != nil {
		return ImportResult{}, err
	}
	if info, err := client.SystemInfo(ctx); err == nil {
		j.Log("info", "连接到目标 Emby %s (%s)", info.ServerName, info.Version)
	}
	exportPath := req.ExportPath
	if !filepath.IsAbs(exportPath) {
		exportPath = filepath.Join(s.ExportsDir(), exportPath)
	}
	var manifest storage.Manifest
	if err := storage.ReadJSON(filepath.Join(exportPath, "manifest.json"), &manifest); err != nil {
		return ImportResult{}, err
	}
	report := ImportReport{StartedAt: time.Now(), DryRun: req.DryRun}
	concurrency := normalizeConcurrency(req.Concurrency)
	j.Log("info", "读取导出包：%s，共 %d 个项目", exportPath, len(manifest.Items))
	j.Log("info", "导入并发数：%d", concurrency)
	if req.DryRun {
		j.Log("info", "[DRY] 本次只验证匹配，不会写入元数据和图片")
	}
	itemResults, err := s.importItems(ctx, j, client, exportPath, manifest.Items, req, concurrency)
	if err != nil {
		return ImportResult{}, err
	}
	for _, result := range itemResults {
		match := result.Match
		report.Matches = append(report.Matches, match)
		addImportMatchSummary(&report, match, req.DryRun)
	}
	if !req.SkipImages && req.IncludePeopleImages {
		if err := s.importPeopleImages(ctx, client, exportPath, manifest, &report, j, req.DryRun, concurrency); err != nil {
			return ImportResult{}, err
		}
	}
	report.EndedAt = time.Now()
	reportPath := filepath.Join(exportPath, "import-report-"+time.Now().Format("20060102-150405")+".json")
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

func addImportMatchSummary(report *ImportReport, match ImportMatch, dryRun bool) {
	switch match.Status {
	case "updated":
		report.Summary.Matched++
		report.Summary.MetadataUpdated++
	case "matched":
		report.Summary.Matched++
		if dryRun {
			report.WritesSkipped++
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

func (s *Service) importItems(ctx context.Context, j *job.Job, client *emby.Client, exportPath string, items []storage.ItemEntry, req ImportRequest, concurrency int) ([]importItemResult, error) {
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
				match := s.importItem(ctx, client, exportPath, task.Item, req)
				resultCh <- importItemResult{Index: task.Index, Match: match}
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for idx, item := range items {
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

func (s *Service) importItem(ctx context.Context, client *emby.Client, exportPath string, entry storage.ItemEntry, req ImportRequest) ImportMatch {
	match := ImportMatch{StableKey: entry.StableKey, SourceName: entry.Name}
	var target emby.Item
	var candidates []emby.Item
	var reason string
	err := retryWithTimeout(ctx, importRetryAttempts, importMatchTimeout, func(attemptCtx context.Context) error {
		var attemptErr error
		target, candidates, reason, attemptErr = FindMatch(attemptCtx, client, entry)
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
	metadataErr := retryWithTimeout(ctx, importRetryAttempts, itemMetadataTimeout, func(attemptCtx context.Context) error {
		return client.UpdateItem(attemptCtx, target.ID, current)
	})
	if !req.SkipImages {
		imageTypeSet := allowedImageTypes(req.ImageTypes)
		for _, img := range entry.Images {
			if len(imageTypeSet) > 0 && !imageTypeSet[strings.ToLower(img.Type)] {
				continue
			}
			data, err := os.ReadFile(filepath.Join(exportPath, filepath.FromSlash(img.Path)))
			if err != nil {
				match.ImageFailures++
				match.ImageErrors = append(match.ImageErrors, fmt.Sprintf("%s 读取失败: %v", img.Type, err))
				continue
			}
			err = retryWithTimeout(ctx, importRetryAttempts, itemImageUploadTimeout, func(attemptCtx context.Context) error {
				return client.UploadImage(attemptCtx, target.ID, img.Type, data)
			})
			if err == nil {
				match.ImagesPushed++
			} else {
				match.ImageFailures++
				match.ImageErrors = append(match.ImageErrors, fmt.Sprintf("%s 上传失败: %v", img.Type, err))
			}
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
	if err := storage.ReadJSON(filepath.Join(exportPath, filepath.FromSlash(entry.InfoPath)), &info); err != nil {
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
	copyPortableRawMetadata(payload, source.Raw)
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
	if len(source.ProviderIDs) > 0 {
		payload["ProviderIds"] = source.ProviderIDs
	} else if len(current.ProviderIDs) > 0 {
		payload["ProviderIds"] = current.ProviderIDs
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

func copyPortableRawMetadata(target map[string]any, source map[string]any) {
	if len(source) == 0 {
		return
	}
	keys := []string{
		"SortName",
		"ForcedSortName",
		"ShortOverview",
		"CustomRating",
		"EndDate",
		"CriticRating",
		"ProductionLocations",
		"Status",
		"DisplayOrder",
		"AirDays",
		"AirTime",
	}
	for _, key := range keys {
		if value, ok := portableRawValue(source[key]); ok {
			target[key] = value
		}
	}
}

func portableRawValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil:
		return nil, false
	case string:
		return v, strings.TrimSpace(v) != ""
	case bool:
		return v, true
	case int, int64, float64, json.Number:
		return v, true
	case []string:
		return v, len(v) > 0
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			switch scalar := item.(type) {
			case string:
				if strings.TrimSpace(scalar) != "" {
					out = append(out, scalar)
				}
			case bool, int, int64, float64, json.Number:
				out = append(out, scalar)
			}
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
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

func setIfNotEmpty(raw map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		raw[key] = value
	}
}

func (s *Service) importPeopleImages(ctx context.Context, client *emby.Client, exportPath string, manifest storage.Manifest, report *ImportReport, j *job.Job, dryRun bool, concurrency int) error {
	tasks := make([]personImageTask, 0)
	for _, person := range manifest.People {
		if person.Image == nil || person.Name == "" {
			continue
		}
		tasks = append(tasks, personImageTask{Name: person.Name, Path: person.Image.Path})
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
				resultCh <- s.importPersonImage(ctx, client, exportPath, task)
			}
		}()
	}

	go func() {
		defer close(taskCh)
		for _, task := range tasks {
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
				detailedFailures++
				if detailedFailures <= 10 {
					j.Log("warn", "人物头像回写失败：%s - %v", result.Name, result.Err)
				} else if detailedFailures == 11 {
					j.Log("warn", "人物头像失败较多，后续失败只在进度和总结中统计")
				}
			} else {
				report.Summary.PeopleImages++
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

func (s *Service) importPersonImage(ctx context.Context, client *emby.Client, exportPath string, task personImageTask) personImageResult {
	data, err := os.ReadFile(filepath.Join(exportPath, filepath.FromSlash(task.Path)))
	if err != nil {
		return personImageResult{Name: task.Name, Err: fmt.Errorf("读取头像失败: %w", err)}
	}
	if err := retryWithTimeout(ctx, importRetryAttempts, personImageUploadTimeout, func(attemptCtx context.Context) error {
		return client.UploadPersonImage(attemptCtx, task.Name, data)
	}); err != nil {
		return personImageResult{Name: task.Name, Err: err}
	}
	return personImageResult{Name: task.Name}
}

func FindMatch(ctx context.Context, client *emby.Client, entry storage.ItemEntry) (emby.Item, []emby.Item, string, error) {
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
	if stem := mediaPathStem(entry.Path); stem != "" && entry.Type != "Season" {
		items, err := client.SearchItems(ctx, searchPrefix(stem, 30), entry.Type, 50)
		if err == nil {
			matches := mediaStemMatches(stem, entry.Type, items)
			if len(matches) == 1 {
				return matches[0], matches, "media-file", nil
			}
			if len(matches) > 1 {
				rememberAmbiguous("media-file-ambiguous", matches)
			}
		} else {
			rememberSearchErr(err)
		}
	}

	providerValues := providerIDsForSearch(entry.ProviderIDs)
	for _, value := range providerValues {
		items, err := client.ItemsByProviderID(ctx, value)
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
		items, err := client.SearchItems(ctx, entry.SeriesName, "Episode", 300)
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
	if entry.Type == "Season" && strings.TrimSpace(entry.Name) != "" {
		items, err := client.SearchItems(ctx, entry.Name, "Season", 300)
		if err == nil {
			matches := seasonMatches(entry, items)
			if len(matches) == 1 {
				return matches[0], matches, "season-parent", nil
			}
			if len(matches) > 1 {
				rememberAmbiguous("season-parent-ambiguous", matches)
			}
		} else {
			rememberSearchErr(err)
		}
	}
	if strings.TrimSpace(entry.Name) != "" {
		items, err := client.SearchItems(ctx, entry.Name, entry.Type, 20)
		if err == nil {
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
		items, err := client.SearchItems(ctx, shortName, entry.Type, 20)
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
		items, err := client.SearchItems(ctx, entry.OriginalTitle, entry.Type, 20)
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
	matches := make([]emby.Item, 0)
	for _, item := range items {
		if item.SeriesName != entry.SeriesName {
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
