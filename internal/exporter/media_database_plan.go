package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"emby-migrator/internal/emby"
	"emby-migrator/internal/embydb"
	"emby-migrator/internal/job"
	"emby-migrator/internal/storage"
)

const mediaDatabasePlanSchemaVersion = 2

type MediaDatabaseBinding struct {
	TargetServerID string `json:"targetServerId"`
	SchemaIdentity string `json:"schemaIdentity"`
	AnchorCount    int    `json:"anchorCount"`
	AnchorDigest   string `json:"anchorDigest"`
}

type MediaDatabasePlan struct {
	SchemaVersion     int                     `json:"schemaVersion"`
	SourceEmbyVersion string                  `json:"sourceEmbyVersion"`
	TargetEmbyVersion string                  `json:"targetEmbyVersion"`
	Target            ImportTarget            `json:"target"`
	DatabaseBinding   MediaDatabaseBinding    `json:"databaseBinding,omitempty"`
	CreatedAt         time.Time               `json:"createdAt"`
	Items             []MediaDatabasePlanItem `json:"items"`
}

type MediaDatabasePlanItem struct {
	StableKey    string           `json:"stableKey"`
	SourceName   string           `json:"sourceName"`
	TargetItemID string           `json:"targetItemId"`
	TargetName   string           `json:"targetName"`
	MediaSource  map[string]any   `json:"mediaSource"`
	MediaSources []map[string]any `json:"mediaSources,omitempty"`
	MediaStreams []map[string]any `json:"mediaStreams"`
	Chapters     []map[string]any `json:"chapters"`
}

type MediaDatabaseApplyRequest struct {
	ExportPath   string `json:"exportPath"`
	DatabasePath string `json:"databasePath"`
	Overwrite    bool   `json:"overwrite"`
}

type MediaDatabaseApplyResult struct {
	PlanPath string             `json:"planPath"`
	Result   embydb.ApplyResult `json:"result"`
}

type MediaDatabaseVerifyResult struct {
	Items    int `json:"items"`
	Streams  int `json:"streams"`
	Chapters int `json:"chapters"`
}

type MediaDatabaseTargetPreflight struct {
	PlanPath      string       `json:"planPath"`
	PlannedTarget ImportTarget `json:"plannedTarget"`
	ActualTarget  ImportTarget `json:"actualTarget"`
}

func packageMediaInfoPayload(exportPath string, entry storage.ItemEntry, info *storage.ItemInfo) map[string]any {
	if info == nil {
		return nil
	}
	// Plan payloads keep source-bound fields (Id, Path, ETag, ...) so the
	// database plan stays complete for multi-source items and verification;
	// the stricter sanitization used for target update payloads drops them.
	return sanitizedMediaInfoPayloadWithKeys(info.Item, entry, exportPath, planMediaSourceAllowedKeys, planMediaStreamAllowedKeys)
}

func writeMediaDatabasePlan(exportPath string, manifest storage.Manifest, report ImportReport, j *job.Job) (*MediaDatabasePlanRef, error) {
	entries := make(map[string]storage.ItemEntry, len(manifest.Items))
	for _, entry := range manifest.Items {
		entries[entry.StableKey] = entry
	}

	plan := MediaDatabasePlan{
		SchemaVersion:     mediaDatabasePlanSchemaVersion,
		SourceEmbyVersion: strings.TrimSpace(manifest.EmbyVersion),
		TargetEmbyVersion: strings.TrimSpace(report.Target.Version),
		Target:            report.Target,
		CreatedAt:         time.Now(),
	}
	for _, match := range report.Matches {
		if match.MediaInfoPlanned == 0 || (match.Status != "updated" && match.Status != "matched") {
			continue
		}
		entry, ok := entries[match.StableKey]
		if !ok {
			continue
		}
		// Every planned item already had a readable info.json during import;
		// failing here means the package changed underneath us, which should
		// surface instead of silently dropping plan entries.
		info, err := readItemInfo(exportPath, entry)
		if err != nil {
			return nil, fmt.Errorf("media database plan for %s: %w", entry.StableKey, err)
		}
		payload := packageMediaInfoPayload(exportPath, entry, info)
		sources := objectSliceField(payload, "MediaSources")
		streams := objectSliceField(payload, "MediaStreams")
		chapters := objectSliceField(payload, "Chapters")
		if len(sources) == 0 || len(streams) == 0 {
			continue
		}
		// Preserve every media source instead of only the first one so the
		// plan keeps multi-source items (multiple versions, alternate files)
		// complete. MediaSource stays populated for compatibility with the
		// summary the database writer derives from the primary source.
		clonedSources := make([]map[string]any, 0, len(sources))
		for _, source := range sources {
			clonedSources = append(clonedSources, cloneAnyMap(source))
		}
		primarySource := cloneAnyMap(clonedSources[0])
		primarySource["MediaStreams"] = streams
		plan.Items = append(plan.Items, MediaDatabasePlanItem{
			StableKey:    match.StableKey,
			SourceName:   match.SourceName,
			TargetItemID: firstNonEmpty(match.TargetID, match.TargetEmbyID),
			TargetName:   match.TargetName,
			MediaSource:  primarySource,
			MediaSources: clonedSources,
			MediaStreams: streams,
			Chapters:     chapters,
		})
	}
	if len(plan.Items) == 0 {
		return nil, nil
	}

	targetKey := firstNonEmpty(report.Target.ServerID, report.Target.ServerName, report.Target.Version)
	fileName := "media-db-plan-" + storage.SafeName(targetKey) + ".json"
	planPath := filepath.Join(exportPath, fileName)

	// An incremental import only plans the items it matched this run. Merging
	// into any existing plan for the same target keeps an earlier, larger
	// import from being silently replaced by a later, smaller one, which would
	// otherwise leave previously planned media information unwritten after the
	// plan is applied to library.db.
	if previous, ok := readMediaDatabasePlanIfPresent(planPath); ok {
		merged := mergeMediaDatabasePlanItems(previous.Items, plan.Items)
		if j != nil {
			j.Log("warn", "已合并既有媒体技术信息计划：先前 %d 项，本次 %d 项，合并后共 %d 项", len(previous.Items), len(plan.Items), len(merged))
		}
		plan.Items = merged
	}

	binding, err := buildMediaDatabaseBinding(plan.Target.ServerID, plan.Items)
	if err != nil {
		return nil, fmt.Errorf("build media database target binding: %w", err)
	}
	plan.DatabaseBinding = binding

	// The plan is written into a published package and merged with any
	// existing plan on the next import, so a torn write must never leave a
	// file that silently replaces the previous plan.
	if err := storage.WriteJSONAtomic(planPath, plan); err != nil {
		return nil, fmt.Errorf("write media database plan: %w", err)
	}
	return &MediaDatabasePlanRef{Path: planPath, Items: len(plan.Items), Status: "prepared"}, nil
}

// readMediaDatabasePlanIfPresent loads an existing plan file, reporting false
// when none exists or the file is unreadable so that a stale or corrupt plan
// never blocks a fresh import.
func readMediaDatabasePlanIfPresent(path string) (MediaDatabasePlan, bool) {
	var plan MediaDatabasePlan
	if err := storage.ReadJSON(path, &plan); err != nil {
		return MediaDatabasePlan{}, false
	}
	return plan, true
}

// mergeMediaDatabasePlanItems combines prior and current plan items keyed by
// StableKey. Current items win, so a re-matched item keeps its latest target
// ID and media payload while items only present in prior imports are retained.
func mergeMediaDatabasePlanItems(previous, current []MediaDatabasePlanItem) []MediaDatabasePlanItem {
	merged := make([]MediaDatabasePlanItem, 0, len(previous)+len(current))
	index := make(map[string]int, len(previous))
	for _, item := range previous {
		index[item.StableKey] = len(merged)
		merged = append(merged, item)
	}
	for _, item := range current {
		if pos, ok := index[item.StableKey]; ok {
			merged[pos] = item
			continue
		}
		index[item.StableKey] = len(merged)
		merged = append(merged, item)
	}
	return merged
}

func cloneAnyMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func buildMediaDatabaseBinding(serverID string, items []MediaDatabasePlanItem) (MediaDatabaseBinding, error) {
	serverID = strings.TrimSpace(serverID)
	if serverID == "" {
		return MediaDatabaseBinding{}, fmt.Errorf("target Emby server id is missing")
	}
	anchors := make([]embydb.TargetAnchor, 0, len(items))
	for _, item := range items {
		itemID, err := strconv.ParseInt(strings.TrimSpace(item.TargetItemID), 10, 64)
		if err != nil || itemID <= 0 {
			return MediaDatabaseBinding{}, fmt.Errorf("invalid target item id %q for %s", item.TargetItemID, item.SourceName)
		}
		anchors = append(anchors, embydb.TargetAnchor{ItemID: itemID, Name: item.TargetName})
	}
	digest, err := embydb.BuildTargetBindingDigest(serverID, anchors)
	if err != nil {
		return MediaDatabaseBinding{}, err
	}
	return MediaDatabaseBinding{
		TargetServerID: serverID,
		SchemaIdentity: embydb.MediaSchemaIdentity,
		AnchorCount:    len(anchors),
		AnchorDigest:   digest,
	}, nil
}

// MediaDatabasePlanTarget returns the target identity stored in the latest
// media database plan, used for offline identity checks in manual-stop mode.
func (s *Service) MediaDatabasePlanTarget(exportName string) (ImportTarget, error) {
	_, _, plan, err := s.loadMediaDatabasePlan(exportName)
	if err != nil {
		return ImportTarget{}, err
	}
	return plan.Target, nil
}

func (s *Service) ApplyMediaDatabasePlan(ctx context.Context, j *job.Job, request MediaDatabaseApplyRequest) (MediaDatabaseApplyResult, error) {
	exportPath, planPath, plan, err := s.loadMediaDatabasePlan(request.ExportPath)
	if err != nil {
		return MediaDatabaseApplyResult{}, err
	}
	patches := make([]embydb.ItemPatch, 0, len(plan.Items))
	for _, item := range plan.Items {
		targetID, err := strconv.ParseInt(strings.TrimSpace(item.TargetItemID), 10, 64)
		if err != nil || targetID <= 0 {
			return MediaDatabaseApplyResult{}, fmt.Errorf("invalid target item id %q for %s", item.TargetItemID, item.SourceName)
		}
		patches = append(patches, embydb.ItemPatch{
			StableKey:    item.StableKey,
			TargetItemID: targetID,
			TargetName:   item.TargetName,
			MediaSource:  item.MediaSource,
			MediaStreams: item.MediaStreams,
			Chapters:     item.Chapters,
		})
	}
	binding, err := validateMediaDatabasePlanBinding(plan)
	if err != nil {
		return MediaDatabaseApplyResult{}, err
	}
	j.Log("info", "开始应用媒体技术信息数据库计划：%s，共 %d 个项目", planPath, len(patches))
	j.Log("info", "目标数据库：%s；源 Emby %s，目标 Emby %s", request.DatabasePath, plan.SourceEmbyVersion, plan.TargetEmbyVersion)
	result, err := embydb.Apply(ctx, embydb.ApplyOptions{
		DatabasePath:           request.DatabasePath,
		SourceVersion:          plan.SourceEmbyVersion,
		TargetVersion:          plan.TargetEmbyVersion,
		TargetServerID:         binding.TargetServerID,
		TargetBindingDigest:    binding.AnchorDigest,
		TargetAnchorCount:      binding.AnchorCount,
		ExpectedSchemaIdentity: binding.SchemaIdentity,
		Items:                  patches,
		Overwrite:              request.Overwrite,
	})
	if err != nil {
		return MediaDatabaseApplyResult{}, err
	}
	j.Log("info", "媒体技术信息数据库写入完成：项目成功 %d，跳过 %d，媒体流 %d，章节 %d；备份：%s",
		result.ItemsApplied, result.ItemsSkipped, result.StreamsWritten, result.ChaptersWritten, result.BackupPath)
	// Bind the plan to the concrete database schema fingerprint so a later
	// apply against a different Emby version's library.db is rejected instead
	// of silently writing into an incompatible schema.
	if result.SchemaIdentity != "" && plan.DatabaseBinding.SchemaIdentity != result.SchemaIdentity {
		plan.DatabaseBinding.SchemaIdentity = result.SchemaIdentity
		if err := storage.WriteJSONAtomic(planPath, plan); err != nil {
			j.Log("warn", "媒体技术信息写入成功，但记录数据库 schema 指纹失败：%v", err)
		}
	}
	applyResult := MediaDatabaseApplyResult{PlanPath: planPath, Result: result}
	resultPath := filepath.Join(exportPath, "media-db-apply-"+time.Now().Format("20060102-150405.000000000")+".json")
	if err := storage.WriteJSONAtomic(resultPath, applyResult); err != nil {
		return MediaDatabaseApplyResult{}, fmt.Errorf("write media database apply report: %w", err)
	}
	return applyResult, nil
}

func (s *Service) PreflightMediaDatabaseTarget(ctx context.Context, exportName string, connection emby.Connection) (MediaDatabaseTargetPreflight, error) {
	_, planPath, plan, err := s.loadMediaDatabasePlan(exportName)
	if err != nil {
		return MediaDatabaseTargetPreflight{}, err
	}
	if _, err := validateMediaDatabasePlanBinding(plan); err != nil {
		return MediaDatabaseTargetPreflight{}, err
	}
	plannedID := strings.TrimSpace(plan.Target.ServerID)
	if plannedID == "" {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("media database plan does not contain a target Emby ServerID; online identity cannot be verified")
	}
	plannedVersion := strings.TrimSpace(plan.TargetEmbyVersion)
	if plannedVersion == "" {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("media database plan does not contain a target Emby version")
	}
	if targetVersion := strings.TrimSpace(plan.Target.Version); targetVersion != "" && !sameEmbyMinorSeries(plannedVersion, targetVersion) {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("media database plan target version is inconsistent: target %q, database plan %q", targetVersion, plannedVersion)
	}

	client, err := emby.NewClient(connection.BaseURL, connection.APIKey)
	if err != nil {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("connect to target Emby for identity preflight: %w", err)
	}
	info, err := client.SystemInfo(ctx)
	if err != nil {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("read target Emby SystemInfo for identity preflight: %w", err)
	}
	actualID := strings.TrimSpace(info.ID)
	actualVersion := strings.TrimSpace(info.Version)
	if actualID == "" {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("target Emby SystemInfo did not return a ServerID")
	}
	if actualID != plannedID {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("target Emby ServerID mismatch: plan %q, actual %q", plannedID, actualID)
	}
	if !sameEmbyMinorSeries(plannedVersion, actualVersion) {
		return MediaDatabaseTargetPreflight{}, fmt.Errorf("target Emby version series mismatch: plan %q, actual %q", plannedVersion, actualVersion)
	}
	return MediaDatabaseTargetPreflight{
		PlanPath: planPath,
		PlannedTarget: ImportTarget{
			ServerID: plannedID, ServerName: strings.TrimSpace(plan.Target.ServerName), Version: plannedVersion,
		},
		ActualTarget: ImportTarget{
			ServerID: actualID, ServerName: strings.TrimSpace(info.ServerName), Version: actualVersion,
		},
	}, nil
}

func (s *Service) loadMediaDatabasePlan(exportName string) (string, string, MediaDatabasePlan, error) {
	exportPath, _, err := s.ResolveExportPath(exportName)
	if err != nil {
		return "", "", MediaDatabasePlan{}, err
	}
	planPath, err := latestMediaDatabasePlanPath(exportPath)
	if err != nil {
		return "", "", MediaDatabasePlan{}, err
	}
	var plan MediaDatabasePlan
	if err := storage.ReadJSON(planPath, &plan); err != nil {
		return "", "", MediaDatabasePlan{}, fmt.Errorf("read media database plan: %w", err)
	}
	if plan.SchemaVersion != 1 && plan.SchemaVersion != mediaDatabasePlanSchemaVersion {
		return "", "", MediaDatabasePlan{}, fmt.Errorf("unsupported media database plan schema %d", plan.SchemaVersion)
	}
	// Plans written before MediaSources existed carry only the primary source;
	// synthesize the slice so verification and consumers can treat both shapes
	// uniformly.
	for index := range plan.Items {
		if len(plan.Items[index].MediaSources) == 0 && plan.Items[index].MediaSource != nil {
			plan.Items[index].MediaSources = []map[string]any{plan.Items[index].MediaSource}
		}
	}
	return exportPath, planPath, plan, nil
}

func validateMediaDatabasePlanBinding(plan MediaDatabasePlan) (MediaDatabaseBinding, error) {
	binding := plan.DatabaseBinding
	if plan.SchemaVersion == mediaDatabasePlanSchemaVersion {
		rebuilt, err := buildMediaDatabaseBinding(plan.Target.ServerID, plan.Items)
		if err != nil {
			return MediaDatabaseBinding{}, fmt.Errorf("validate media database target binding: %w", err)
		}
		if binding.TargetServerID != rebuilt.TargetServerID || binding.AnchorCount != rebuilt.AnchorCount ||
			!strings.EqualFold(binding.AnchorDigest, rebuilt.AnchorDigest) {
			return MediaDatabaseBinding{}, fmt.Errorf("media database target binding is missing or does not match the plan contents")
		}
		// The identity may be the legacy constant (not yet bound to a concrete
		// database) or a fingerprint recorded after the first successful apply.
		if !embydb.IsKnownSchemaIdentity(binding.SchemaIdentity) {
			return MediaDatabaseBinding{}, fmt.Errorf("media database plan carries an invalid schema identity %q", binding.SchemaIdentity)
		}
		return binding, nil
	}
	// Schema 1 plans predate explicit ServerID binding. They remain readable, but every target item is still
	// content-bound and checked against the selected database before backup or mutation.
	legacy, err := buildLegacyMediaDatabaseBinding(plan.Target.ServerID, plan.Items)
	if err != nil {
		return MediaDatabaseBinding{}, fmt.Errorf("build legacy media database target binding: %w", err)
	}
	return legacy, nil
}

func buildLegacyMediaDatabaseBinding(serverID string, items []MediaDatabasePlanItem) (MediaDatabaseBinding, error) {
	anchors := make([]embydb.TargetAnchor, 0, len(items))
	for _, item := range items {
		itemID, err := strconv.ParseInt(strings.TrimSpace(item.TargetItemID), 10, 64)
		if err != nil || itemID <= 0 {
			return MediaDatabaseBinding{}, fmt.Errorf("invalid target item id %q for %s", item.TargetItemID, item.SourceName)
		}
		anchors = append(anchors, embydb.TargetAnchor{ItemID: itemID, Name: item.TargetName})
	}
	digest, err := embydb.BuildTargetBindingDigest(strings.TrimSpace(serverID), anchors)
	if err != nil {
		return MediaDatabaseBinding{}, err
	}
	return MediaDatabaseBinding{
		TargetServerID: strings.TrimSpace(serverID), SchemaIdentity: embydb.MediaSchemaIdentity,
		AnchorCount: len(anchors), AnchorDigest: digest,
	}, nil
}

var (
	// Fields the target server derives or regenerates locally (paths, ids,
	// scores, display titles); comparing them would fail on legitimate
	// source/target differences. Everything else both sides expose is
	// compared exactly.
	mediaDatabaseStreamReadbackExclusions  = stringSet("Path", "Score", "DisplayTitle", "SupportsExternalStream")
	mediaDatabaseChapterReadbackExclusions = stringSet("ImageTag")
	mediaDatabaseSourceReadbackExclusions  = stringSet("Id", "Path", "ETag", "Name", "IsRemote",
		"SupportsDirectPlay", "SupportsDirectStream", "SupportsTranscoding", "MediaStreams")
)

func (s *Service) VerifyMediaDatabasePlan(ctx context.Context, exportName string, connection emby.Connection) (MediaDatabaseVerifyResult, error) {
	_, _, plan, err := s.loadMediaDatabasePlan(exportName)
	if err != nil {
		return MediaDatabaseVerifyResult{}, err
	}
	client, err := emby.NewClient(connection.BaseURL, connection.APIKey)
	if err != nil {
		return MediaDatabaseVerifyResult{}, err
	}

	// Readbacks are independent HTTP fetches; run them through the same
	// bounded worker-pool shape the rest of the exporter uses.
	verifyCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	taskCh := make(chan int)
	workers := workerCount(len(plan.Items), defaultConcurrency)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		result   MediaDatabaseVerifyResult
		firstErr error
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range taskCh {
				if err := verifyMediaDatabasePlanItem(verifyCtx, client, plan.Items[index]); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					cancel()
					return
				}
				mu.Lock()
				result.Items++
				result.Streams += len(plan.Items[index].MediaStreams)
				result.Chapters += len(plan.Items[index].Chapters)
				mu.Unlock()
			}
		}()
	}
	go func() {
		defer close(taskCh)
		for index := range plan.Items {
			select {
			case <-verifyCtx.Done():
				return
			case taskCh <- index:
			}
		}
	}()
	wg.Wait()
	if firstErr != nil {
		return result, firstErr
	}
	return result, nil
}

func verifyMediaDatabasePlanItem(ctx context.Context, client *emby.Client, planned MediaDatabasePlanItem) error {
	actual, err := client.Item(ctx, planned.TargetItemID)
	if err != nil {
		return fmt.Errorf("read back target item %s (%s): %w", planned.TargetName, planned.TargetItemID, err)
	}
	actualPayload := sanitizedMediaInfoPayload(actual, storage.ItemEntry{}, "")
	actualStreams := objectSliceField(actualPayload, "MediaStreams")
	actualChapters := objectSliceField(actualPayload, "Chapters")
	actualSources := objectSliceField(actualPayload, "MediaSources")

	if err := verifyMediaDatabaseStreams(planned.MediaStreams, actualStreams); err != nil {
		return fmt.Errorf("target item %s (%s) media streams: %w", planned.TargetName, planned.TargetItemID, err)
	}
	if err := verifyMediaDatabaseChapters(planned.Chapters, actualChapters); err != nil {
		return fmt.Errorf("target item %s (%s) chapters: %w", planned.TargetName, planned.TargetItemID, err)
	}
	if err := verifyMediaDatabaseSources(planned.MediaSources, actualSources); err != nil {
		return fmt.Errorf("target item %s (%s) media sources: %w", planned.TargetName, planned.TargetItemID, err)
	}
	return nil
}

// verifyMediaDatabaseStreams compares every field both sides expose for each
// planned stream. Streams are matched by Index (falling back to order), and
// the target must expose exactly the planned streams because the database was
// rewritten from the plan.
func verifyMediaDatabaseStreams(planned, actual []map[string]any) error {
	if len(planned) == 0 {
		return nil
	}
	if len(actual) != len(planned) {
		return fmt.Errorf("stream count mismatch: plan %d, target %d", len(planned), len(actual))
	}
	byIndex, ordered := mediaReadbackIndexed(actual, "index")
	position := 0
	for _, stream := range planned {
		folded := foldMediaReadbackMap(stream)
		index, hasIndex := mediaReadbackIndex(folded["index"])
		var match map[string]any
		if hasIndex {
			match = byIndex[index]
		} else {
			if position < len(ordered) {
				match = ordered[position]
			}
		}
		position++
		if match == nil {
			return fmt.Errorf("stream %v missing on target", folded["index"])
		}
		if err := verifyMediaReadbackMaps(folded, match, mediaDatabaseStreamReadbackExclusions); err != nil {
			return fmt.Errorf("stream index %v: %w", folded["index"], err)
		}
	}
	return nil
}

// verifyMediaDatabaseChapters matches chapters by ChapterIndex and requires
// the target to expose exactly the planned chapters.
func verifyMediaDatabaseChapters(planned, actual []map[string]any) error {
	if len(planned) == 0 {
		return nil
	}
	if len(actual) != len(planned) {
		return fmt.Errorf("chapter count mismatch: plan %d, target %d", len(planned), len(actual))
	}
	byIndex, ordered := mediaReadbackIndexed(actual, "chapterindex")
	position := 0
	for _, chapter := range planned {
		folded := foldMediaReadbackMap(chapter)
		index, hasIndex := mediaReadbackIndex(folded["chapterindex"])
		var match map[string]any
		if hasIndex {
			match = byIndex[index]
		} else {
			if position < len(ordered) {
				match = ordered[position]
			}
		}
		position++
		if match == nil {
			return fmt.Errorf("chapter %v missing on target", folded["chapterindex"])
		}
		if err := verifyMediaReadbackMaps(folded, match, mediaDatabaseChapterReadbackExclusions); err != nil {
			return fmt.Errorf("chapter index %v: %w", folded["chapterindex"], err)
		}
	}
	return nil
}

// verifyMediaDatabaseSources checks that every planned media source has a
// matching source on the target. Sources are matched order-insensitively on
// their intrinsic media properties; the library database itself has no
// MediaSources table, so the target may legitimately expose extra or
// reordered sources and only the overlapping ones are compared.
func verifyMediaDatabaseSources(planned, actual []map[string]any) error {
	if len(planned) == 0 {
		return nil
	}
	if len(actual) < len(planned) {
		return fmt.Errorf("media source count mismatch: plan %d, target %d", len(planned), len(actual))
	}
	matched := make([]bool, len(actual))
	for _, source := range planned {
		folded := foldMediaReadbackMap(source)
		found := false
		for index, candidate := range actual {
			if matched[index] {
				continue
			}
			if verifyMediaReadbackMaps(folded, foldMediaReadbackMap(candidate), mediaDatabaseSourceReadbackExclusions) == nil {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("media source has no matching source on target")
		}
	}
	return nil
}

// verifyMediaReadbackMaps compares every field present on both sides (minus
// exclusions). Fields the target API does not expose are skipped here; the
// authoritative per-column check runs inside the database write transaction.
func verifyMediaReadbackMaps(planned, actual map[string]any, exclusions map[string]bool) error {
	for key, plannedValue := range planned {
		if exclusions[key] || plannedValue == nil {
			continue
		}
		actualValue, ok := actual[key]
		if !ok {
			continue
		}
		if !mediaReadbackValuesEqual(plannedValue, actualValue) {
			return fmt.Errorf("field %s: plan %v, target %v", key, plannedValue, actualValue)
		}
	}
	return nil
}

func mediaReadbackIndexed(values []map[string]any, indexKey string) (map[int64]map[string]any, []map[string]any) {
	byIndex := make(map[int64]map[string]any, len(values))
	ordered := make([]map[string]any, 0, len(values))
	for _, value := range values {
		folded := foldMediaReadbackMap(value)
		ordered = append(ordered, folded)
		if index, ok := mediaReadbackIndex(folded[indexKey]); ok {
			byIndex[index] = folded
		}
	}
	return byIndex, ordered
}

func foldMediaReadbackMap(values map[string]any) map[string]any {
	folded := make(map[string]any, len(values))
	for key, value := range values {
		folded[strings.ToLower(strings.TrimSpace(key))] = value
	}
	return folded
}

func mediaReadbackIndex(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed, true
		}
		if parsed, err := typed.Float64(); err == nil {
			return int64(parsed), true
		}
		return 0, false
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func mediaReadbackValuesEqual(planned, actual any) bool {
	if planned == nil || actual == nil {
		return planned == nil && actual == nil
	}
	if plannedFloat, ok := mediaReadbackNumeric(planned); ok {
		actualFloat, actualOK := mediaReadbackNumeric(actual)
		return actualOK && plannedFloat == actualFloat
	}
	if plannedString, ok := planned.(string); ok {
		actualString, actualStringOK := actual.(string)
		return actualStringOK && plannedString == actualString
	}
	return reflect.DeepEqual(planned, actual)
}

func mediaReadbackNumeric(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number, true
		}
	}
	return 0, false
}

func latestMediaDatabasePlanPath(exportPath string) (string, error) {
	entries, err := os.ReadDir(exportPath)
	if err != nil {
		return "", err
	}
	type planFile struct {
		name    string
		modTime time.Time
	}
	plans := make([]planFile, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "media-db-plan-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return "", fmt.Errorf("inspect media database plan %s: %w", entry.Name(), err)
		}
		plans = append(plans, planFile{name: entry.Name(), modTime: info.ModTime()})
	}
	if len(plans) == 0 {
		return "", fmt.Errorf("media database plan not found; run an online import with media information enabled first")
	}
	sort.Slice(plans, func(i, j int) bool {
		if plans[i].modTime.Equal(plans[j].modTime) {
			return plans[i].name < plans[j].name
		}
		return plans[i].modTime.Before(plans[j].modTime)
	})
	return filepath.Join(exportPath, plans[len(plans)-1].name), nil
}
