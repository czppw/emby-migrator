package exporter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

func packageMediaInfoPayload(exportPath string, entry storage.ItemEntry) map[string]any {
	infoPath, err := safePackagePath(exportPath, entry.InfoPath)
	if err != nil {
		return nil
	}
	var info storage.ItemInfo
	if err := storage.ReadJSON(infoPath, &info); err != nil {
		return nil
	}
	return sanitizedMediaInfoPayload(info.Item, entry, exportPath)
}

func writeMediaDatabasePlan(exportPath string, manifest storage.Manifest, report ImportReport) (*MediaDatabasePlanRef, error) {
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
		payload := packageMediaInfoPayload(exportPath, entry)
		sources := objectSliceField(payload, "MediaSources")
		streams := objectSliceField(payload, "MediaStreams")
		chapters := objectSliceField(payload, "Chapters")
		if len(sources) == 0 || len(streams) == 0 {
			continue
		}
		primarySource := cloneAnyMap(sources[0])
		primarySource["MediaStreams"] = streams
		plan.Items = append(plan.Items, MediaDatabasePlanItem{
			StableKey:    match.StableKey,
			SourceName:   match.SourceName,
			TargetItemID: firstNonEmpty(match.TargetID, match.TargetEmbyID),
			TargetName:   match.TargetName,
			MediaSource:  primarySource,
			MediaStreams: streams,
			Chapters:     chapters,
		})
	}
	if len(plan.Items) == 0 {
		return nil, nil
	}
	binding, err := buildMediaDatabaseBinding(plan.Target.ServerID, plan.Items)
	if err != nil {
		return nil, fmt.Errorf("build media database target binding: %w", err)
	}
	plan.DatabaseBinding = binding

	targetKey := firstNonEmpty(report.Target.ServerID, report.Target.ServerName, report.Target.Version)
	fileName := "media-db-plan-" + storage.SafeName(targetKey) + ".json"
	planPath := filepath.Join(exportPath, fileName)
	if err := storage.WriteJSON(planPath, plan); err != nil {
		return nil, fmt.Errorf("write media database plan: %w", err)
	}
	return &MediaDatabasePlanRef{Path: planPath, Items: len(plan.Items), Status: "prepared"}, nil
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
	applyResult := MediaDatabaseApplyResult{PlanPath: planPath, Result: result}
	resultPath := filepath.Join(exportPath, "media-db-apply-"+time.Now().Format("20060102-150405.000000000")+".json")
	if err := storage.WriteJSON(resultPath, applyResult); err != nil {
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
	return exportPath, planPath, plan, nil
}

func validateMediaDatabasePlanBinding(plan MediaDatabasePlan) (MediaDatabaseBinding, error) {
	binding := plan.DatabaseBinding
	if plan.SchemaVersion == mediaDatabasePlanSchemaVersion {
		rebuilt, err := buildMediaDatabaseBinding(plan.Target.ServerID, plan.Items)
		if err != nil {
			return MediaDatabaseBinding{}, fmt.Errorf("validate media database target binding: %w", err)
		}
		if binding.TargetServerID != rebuilt.TargetServerID || binding.SchemaIdentity != rebuilt.SchemaIdentity ||
			binding.AnchorCount != rebuilt.AnchorCount || !strings.EqualFold(binding.AnchorDigest, rebuilt.AnchorDigest) {
			return MediaDatabaseBinding{}, fmt.Errorf("media database target binding is missing or does not match the plan contents")
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

func (s *Service) VerifyMediaDatabasePlan(ctx context.Context, exportName string, connection emby.Connection) (MediaDatabaseVerifyResult, error) {
	exportPath, _, err := s.ResolveExportPath(exportName)
	if err != nil {
		return MediaDatabaseVerifyResult{}, err
	}
	planPath, err := latestMediaDatabasePlanPath(exportPath)
	if err != nil {
		return MediaDatabaseVerifyResult{}, err
	}
	var plan MediaDatabasePlan
	if err := storage.ReadJSON(planPath, &plan); err != nil {
		return MediaDatabaseVerifyResult{}, fmt.Errorf("read media database plan: %w", err)
	}
	client, err := emby.NewClient(connection.BaseURL, connection.APIKey)
	if err != nil {
		return MediaDatabaseVerifyResult{}, err
	}
	result := MediaDatabaseVerifyResult{}
	for _, planned := range plan.Items {
		actual, err := client.Item(ctx, planned.TargetItemID)
		if err != nil {
			return result, fmt.Errorf("read back target item %s (%s): %w", planned.TargetName, planned.TargetItemID, err)
		}
		actualPayload := sanitizedMediaInfoPayload(actual, storage.ItemEntry{}, "")
		actualStreams := mediaDatabaseVerificationFields(objectSliceField(actualPayload, "MediaStreams"),
			"Index", "Type", "Codec", "Language", "Width", "Height", "Channels", "SampleRate", "IsDefault", "IsForced")
		expectedStreams := mediaDatabaseVerificationFields(planned.MediaStreams,
			"Index", "Type", "Codec", "Language", "Width", "Height", "Channels", "SampleRate", "IsDefault", "IsForced")
		actualChapters := mediaDatabaseVerificationFields(objectSliceField(actualPayload, "Chapters"),
			"ChapterIndex", "StartPositionTicks", "Name", "MarkerType")
		expectedChapters := mediaDatabaseVerificationFields(planned.Chapters,
			"ChapterIndex", "StartPositionTicks", "Name", "MarkerType")
		if len(expectedStreams) > 0 && !mediaInfoValueContains(actualStreams, expectedStreams) {
			return result, fmt.Errorf("target item %s (%s) media stream readback does not match the database plan", planned.TargetName, planned.TargetItemID)
		}
		if len(expectedChapters) > 0 && !mediaInfoValueContains(actualChapters, expectedChapters) {
			return result, fmt.Errorf("target item %s (%s) chapter readback does not match the database plan", planned.TargetName, planned.TargetItemID)
		}
		result.Items++
		result.Streams += len(planned.MediaStreams)
		result.Chapters += len(planned.Chapters)
	}
	return result, nil
}

func mediaDatabaseVerificationFields(values []map[string]any, fields ...string) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		item := make(map[string]any, len(fields))
		for _, field := range fields {
			if raw, ok := mapValueFold(value, field); ok && raw != nil {
				item[field] = raw
			}
		}
		if len(item) > 0 {
			out = append(out, item)
		}
	}
	return out
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
