package embydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	sqlite "modernc.org/sqlite"
)

const emptyDateModified = int64(-62135596800)

type ItemPatch struct {
	StableKey    string           `json:"stableKey,omitempty"`
	TargetItemID int64            `json:"targetItemId"`
	TargetName   string           `json:"targetName,omitempty"`
	MediaSource  map[string]any   `json:"mediaSource"`
	MediaStreams []map[string]any `json:"mediaStreams"`
	Chapters     []map[string]any `json:"chapters"`
}

type ApplyOptions struct {
	DatabasePath  string
	SourceVersion string
	TargetVersion string
	Items         []ItemPatch
	Overwrite     bool
	Now           func() time.Time
}

type ApplyResult struct {
	DatabasePath    string `json:"databasePath"`
	BackupPath      string `json:"backupPath"`
	ItemsApplied    int    `json:"itemsApplied"`
	ItemsSkipped    int    `json:"itemsSkipped"`
	StreamsWritten  int    `json:"streamsWritten"`
	ChaptersWritten int    `json:"chaptersWritten"`
}

func Apply(ctx context.Context, options ApplyOptions) (ApplyResult, error) {
	if err := validateVersionPair(options.SourceVersion, options.TargetVersion); err != nil {
		return ApplyResult{}, err
	}
	databasePath, err := filepath.Abs(strings.TrimSpace(options.DatabasePath))
	if err != nil || databasePath == "" {
		return ApplyResult{}, fmt.Errorf("invalid Emby database path")
	}
	info, err := os.Stat(databasePath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("open Emby database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return ApplyResult{}, fmt.Errorf("Emby database is not a regular file: %s", databasePath)
	}
	if len(options.Items) == 0 {
		return ApplyResult{}, fmt.Errorf("media database plan has no items")
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?mode=rw&_pragma=busy_timeout(1000)")
	if err != nil {
		return ApplyResult{}, fmt.Errorf("open Emby database: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("connect to Emby database: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return ApplyResult{}, fmt.Errorf("target Emby database is locked; stop Emby before applying media information: %w", err)
	}
	if err := validateSchema(ctx, conn); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return ApplyResult{}, err
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return ApplyResult{}, fmt.Errorf("release Emby database validation lock: %w", err)
	}

	now := time.Now()
	if options.Now != nil {
		now = options.Now()
	}
	backupPath := databasePath + ".emby-migrator-" + now.Format("20060102-150405.000000000") + ".bak"
	if err := backupDatabase(conn, backupPath, info.Mode().Perm()); err != nil {
		return ApplyResult{}, fmt.Errorf("backup Emby database: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return ApplyResult{}, fmt.Errorf("target Emby database changed or became locked after backup; keep Emby stopped and retry: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	result := ApplyResult{DatabasePath: databasePath, BackupPath: backupPath}
	for _, item := range options.Items {
		applied, streams, chapters, err := applyItem(ctx, conn, item, options.Overwrite)
		if err != nil {
			return ApplyResult{}, err
		}
		if !applied {
			result.ItemsSkipped++
			continue
		}
		result.ItemsApplied++
		result.StreamsWritten += streams
		result.ChaptersWritten += chapters
	}
	var integrity string
	if err := conn.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return ApplyResult{}, fmt.Errorf("check Emby database integrity: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(integrity), "ok") {
		return ApplyResult{}, fmt.Errorf("Emby database integrity check failed: %s", integrity)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return ApplyResult{}, fmt.Errorf("commit Emby database update: %w", err)
	}
	committed = true
	return result, nil
}

func validateVersionPair(source, target string) error {
	sourceSeries := supportedSeries(source)
	targetSeries := supportedSeries(target)
	if sourceSeries == "" || targetSeries == "" || sourceSeries != targetSeries {
		return fmt.Errorf("media database restore only supports 4.8.11.x -> 4.8.11.x or 4.9.5.x -> 4.9.5.x: source %q, target %q", source, target)
	}
	return nil
}

func supportedSeries(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 3 {
		return ""
	}
	series := strings.Join(parts[:3], ".")
	if series == "4.8.11" || series == "4.9.5" {
		return series
	}
	return ""
}

func validateSchema(ctx context.Context, conn *sql.Conn) error {
	required := map[string][]string{
		"MediaItems":    {"Id", "Name", "RunTimeTicks", "TotalBitrate", "Width", "Height", "Size", "Container"},
		"MediaStreams2": {"ItemId", "StreamIndex", "StreamType", "Codec", "Height", "Width"},
		"Chapters3":     {"ItemId", "ChapterIndex", "StartPositionTicks", "Name", "MarkerType"},
	}
	for table, columns := range required {
		rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return fmt.Errorf("inspect %s schema: %w", table, err)
		}
		found := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull, primaryKey int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return fmt.Errorf("inspect %s schema: %w", table, err)
			}
			found[strings.ToLower(name)] = true
		}
		rows.Close()
		for _, column := range columns {
			if !found[strings.ToLower(column)] {
				return fmt.Errorf("unsupported Emby database schema: %s.%s is missing", table, column)
			}
		}
	}
	return nil
}

func applyItem(ctx context.Context, conn *sql.Conn, item ItemPatch, overwrite bool) (bool, int, int, error) {
	if item.TargetItemID <= 0 {
		return false, 0, 0, fmt.Errorf("invalid target item id for %s", item.StableKey)
	}
	var targetName sql.NullString
	if err := conn.QueryRowContext(ctx, "SELECT Name FROM MediaItems WHERE Id=?", item.TargetItemID).Scan(&targetName); err != nil {
		if err == sql.ErrNoRows {
			return false, 0, 0, fmt.Errorf("target item %d does not exist in Emby database", item.TargetItemID)
		}
		return false, 0, 0, fmt.Errorf("find target item %d: %w", item.TargetItemID, err)
	}
	if expected := strings.TrimSpace(item.TargetName); expected != "" && !equivalentTargetName(targetName.String, expected) {
		return false, 0, 0, fmt.Errorf("target item %d name mismatch: plan %q, database %q; verify the selected target database", item.TargetItemID, expected, targetName.String)
	}
	var existingStreams int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM MediaStreams2 WHERE ItemId=?", item.TargetItemID).Scan(&existingStreams); err != nil {
		return false, 0, 0, err
	}
	if existingStreams > 0 && !overwrite {
		return false, 0, 0, nil
	}
	if len(item.MediaStreams) == 0 {
		return false, 0, 0, fmt.Errorf("item %d has no media streams in plan", item.TargetItemID)
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM MediaStreams2 WHERE ItemId=?", item.TargetItemID); err != nil {
		return false, 0, 0, err
	}
	if _, err := conn.ExecContext(ctx, "DELETE FROM Chapters3 WHERE ItemId=?", item.TargetItemID); err != nil {
		return false, 0, 0, err
	}

	streamSQL := `INSERT INTO MediaStreams2 (
		ItemId, StreamIndex, StreamType, Codec, Language, ChannelLayout, Profile, AspectRatio, Path,
		IsInterlaced, BitRate, Channels, SampleRate, IsDefault, IsForced, IsHearingImpaired, IsExternal,
		Height, Width, AverageFrameRate, RealFrameRate, Level, PixelFormat, BitDepth, IsAnamorphic,
		RefFrames, Rotation, CodecTag, Comment, NalLengthSize, Title, TimeBase, ColorPrimaries, ColorSpace,
		ColorTransfer, Extradata, AttachmentSize, MimeType, ExtendedVideoType, ExtendedVideoSubtype
	) VALUES (` + strings.TrimRight(strings.Repeat("?,", 40), ",") + `)`
	for index, stream := range item.MediaStreams {
		streamIndex, ok := integerValue(valueFold(stream, "Index"))
		if !ok {
			streamIndex = int64(index)
		}
		streamType, err := streamTypeValue(valueFold(stream, "Type"))
		if err != nil {
			return false, 0, 0, fmt.Errorf("item %d stream %d: %w", item.TargetItemID, index, err)
		}
		values := []any{
			item.TargetItemID, streamIndex, streamType, valueFold(stream, "Codec"), valueFold(stream, "Language"),
			valueFold(stream, "ChannelLayout"), valueFold(stream, "Profile"), valueFold(stream, "AspectRatio"), nil,
			boolInteger(valueFold(stream, "IsInterlaced")), valueFold(stream, "BitRate"), valueFold(stream, "Channels"),
			valueFold(stream, "SampleRate"), boolInteger(valueFold(stream, "IsDefault")), boolInteger(valueFold(stream, "IsForced")),
			boolInteger(valueFold(stream, "IsHearingImpaired")), boolInteger(valueFold(stream, "IsExternal")),
			valueFold(stream, "Width"), valueFold(stream, "Height"), valueFold(stream, "AverageFrameRate"),
			valueFold(stream, "RealFrameRate"), valueFold(stream, "Level"), valueFold(stream, "PixelFormat"),
			valueFold(stream, "BitDepth"), boolInteger(valueFold(stream, "IsAnamorphic")), valueFold(stream, "RefFrames"),
			valueFold(stream, "Rotation"), valueFold(stream, "CodecTag"), valueFold(stream, "Comment"),
			valueFold(stream, "NalLengthSize"), valueFold(stream, "Title"), valueFold(stream, "TimeBase"),
			valueFold(stream, "ColorPrimaries"), valueFold(stream, "ColorSpace"), valueFold(stream, "ColorTransfer"),
			nil, nil, valueFold(stream, "MimeType"), 0, 0,
		}
		if _, err := conn.ExecContext(ctx, streamSQL, values...); err != nil {
			return false, 0, 0, fmt.Errorf("write target item %d stream %d: %w", item.TargetItemID, index, err)
		}
	}

	for index, chapter := range item.Chapters {
		chapterIndex, ok := integerValue(valueFold(chapter, "ChapterIndex"))
		if !ok {
			chapterIndex = int64(index)
		}
		start, _ := integerValue(valueFold(chapter, "StartPositionTicks"))
		marker := markerTypeValue(valueFold(chapter, "MarkerType"))
		if _, err := conn.ExecContext(ctx, `INSERT INTO Chapters3 (ItemId, ChapterIndex, StartPositionTicks, Name, ImagePath, ImageDateModified, MarkerType) VALUES (?, ?, ?, ?, NULL, ?, ?)`,
			item.TargetItemID, chapterIndex, start, valueFold(chapter, "Name"), emptyDateModified, marker); err != nil {
			return false, 0, 0, fmt.Errorf("write target item %d chapter %d: %w", item.TargetItemID, index, err)
		}
	}

	width, height := primaryVideoDimensions(item.MediaStreams)
	_, err := conn.ExecContext(ctx, `UPDATE MediaItems SET RunTimeTicks=?, TotalBitrate=?, Width=?, Height=?, Size=?, Container=? WHERE Id=?`,
		valueFold(item.MediaSource, "RunTimeTicks"), firstValue(item.MediaSource, "Bitrate", "BitRate"), width, height,
		valueFold(item.MediaSource, "Size"), valueFold(item.MediaSource, "Container"), item.TargetItemID)
	if err != nil {
		return false, 0, 0, fmt.Errorf("update target item %d media summary: %w", item.TargetItemID, err)
	}
	return true, len(item.MediaStreams), len(item.Chapters), nil
}

func valueFold(values map[string]any, key string) any {
	for candidate, value := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), key) {
			return normalizeSQLiteValue(value)
		}
	}
	return nil
}

func equivalentTargetName(databaseName, planName string) bool {
	databaseName = strings.TrimSpace(databaseName)
	planName = strings.TrimSpace(planName)
	if strings.EqualFold(databaseName, planName) {
		return true
	}
	databasePortable := portableTargetName(databaseName)
	return databasePortable != "" && databasePortable == portableTargetName(planName)
}

func portableTargetName(value string) string {
	var normalized strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func firstValue(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value := valueFold(values, key); value != nil {
			return value
		}
	}
	return nil
}

func normalizeSQLiteValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
	case uint:
		return int64(typed)
	case uint64:
		if typed <= uint64(^uint64(0)>>1) {
			return int64(typed)
		}
	}
	return value
}

func integerValue(value any) (int64, bool) {
	switch typed := normalizeSQLiteValue(value).(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func boolInteger(value any) any {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		if err == nil && parsed {
			return 1
		}
		if err == nil {
			return 0
		}
	}
	if integer, ok := integerValue(value); ok && integer != 0 {
		return 1
	}
	return 0
}

func streamTypeValue(value any) (int, error) {
	if integer, ok := integerValue(value); ok {
		return int(integer), nil
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(value))) {
	case "audio":
		return 1, nil
	case "video":
		return 2, nil
	case "subtitle":
		return 3, nil
	case "embeddedimage":
		return 4, nil
	case "data":
		return 5, nil
	default:
		return 0, fmt.Errorf("unsupported media stream type %q", value)
	}
}

func markerTypeValue(value any) int64 {
	if integer, ok := integerValue(value); ok {
		return integer
	}
	switch strings.ToLower(strings.TrimSpace(fmt.Sprint(value))) {
	case "introstart":
		return 1
	case "introend":
		return 2
	case "creditsstart":
		return 3
	default:
		return 0
	}
}

func primaryVideoDimensions(streams []map[string]any) (any, any) {
	var best map[string]any
	var bestPixels int64
	for _, stream := range streams {
		typ, err := streamTypeValue(valueFold(stream, "Type"))
		if err != nil || typ != 2 {
			continue
		}
		width, _ := integerValue(valueFold(stream, "Width"))
		height, _ := integerValue(valueFold(stream, "Height"))
		if width*height >= bestPixels {
			best = stream
			bestPixels = width * height
		}
	}
	if best == nil {
		return nil, nil
	}
	return valueFold(best, "Width"), valueFold(best, "Height")
}

func backupDatabase(conn *sql.Conn, target string, mode os.FileMode) error {
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("backup already exists: %s", target)
	} else if !os.IsNotExist(err) {
		return err
	}
	type backuper interface {
		NewBackup(string) (*sqlite.Backup, error)
	}
	err := conn.Raw(func(driverConn any) error {
		provider, ok := driverConn.(backuper)
		if !ok {
			return fmt.Errorf("SQLite driver does not support online backup")
		}
		backup, err := provider.NewBackup(target)
		if err != nil {
			return err
		}
		for more := true; more; {
			more, err = backup.Step(-1)
			if err != nil {
				_ = backup.Finish()
				return err
			}
		}
		return backup.Finish()
	})
	if err != nil {
		_ = os.Remove(target)
		return err
	}
	return os.Chmod(target, mode)
}
