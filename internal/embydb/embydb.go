package embydb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	DatabasePath           string
	SourceVersion          string
	TargetVersion          string
	TargetServerID         string
	TargetBindingDigest    string
	TargetAnchorCount      int
	ExpectedSchemaIdentity string
	Items                  []ItemPatch
	Overwrite              bool
	Now                    func() time.Time
}

type ApplyResult struct {
	DatabasePath         string `json:"databasePath"`
	TargetServerID       string `json:"targetServerId,omitempty"`
	TargetBindingDigest  string `json:"targetBindingDigest,omitempty"`
	SchemaIdentity       string `json:"schemaIdentity,omitempty"`
	BackupPath           string `json:"backupPath"`
	BackupsPruned        int    `json:"backupsPruned,omitempty"`
	BackupCleanupWarning string `json:"backupCleanupWarning,omitempty"`
	ItemsApplied         int    `json:"itemsApplied"`
	ItemsSkipped         int    `json:"itemsSkipped"`
	StreamsWritten       int    `json:"streamsWritten"`
	ChaptersWritten      int    `json:"chaptersWritten"`
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

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?mode=rw&_pragma=busy_timeout(5000)")
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
	schemaIdentity, err := validateSchema(ctx, conn)
	if err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return ApplyResult{}, err
	}
	if expected := strings.TrimSpace(options.ExpectedSchemaIdentity); expected != "" && expected != schemaIdentity {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return ApplyResult{}, fmt.Errorf("target Emby database schema identity mismatch: plan %q, database %q", expected, schemaIdentity)
	}
	expectedDigest := strings.TrimSpace(options.TargetBindingDigest)
	expectedCount := options.TargetAnchorCount
	if expectedDigest == "" {
		anchors := make([]TargetAnchor, 0, len(options.Items))
		for _, item := range options.Items {
			anchors = append(anchors, TargetAnchor{ItemID: item.TargetItemID, Name: item.TargetName})
		}
		expectedDigest, err = BuildTargetBindingDigest(options.TargetServerID, anchors)
		if err != nil {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
			return ApplyResult{}, err
		}
		expectedCount = len(anchors)
	}
	if err := validateTargetBinding(ctx, conn, options.TargetServerID, expectedDigest, expectedCount, options.Items); err != nil {
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
	if err := backupDatabase(ctx, conn, backupPath, info.Mode().Perm()); err != nil {
		return ApplyResult{}, fmt.Errorf("backup Emby database: %w", err)
	}
	result := ApplyResult{
		DatabasePath: databasePath, BackupPath: backupPath, TargetServerID: strings.TrimSpace(options.TargetServerID),
		TargetBindingDigest: expectedDigest, SchemaIdentity: schemaIdentity,
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

	stmts, err := prepareApplyStatements(ctx, conn)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("prepare Emby database statements: %w", err)
	}
	defer stmts.close()

	for _, item := range options.Items {
		applied, streams, chapters, err := applyItem(ctx, stmts, item, options.Overwrite)
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
	// Prune old backups only after the write transaction has committed, so a
	// failed apply keeps the full retention window intact.
	result.BackupsPruned, err = cleanupDatabaseBackups(databasePath, backupPath, 5, os.Remove)
	if err != nil {
		result.BackupCleanupWarning = err.Error()
	}
	return result, nil
}

var streamInsertSQL = `INSERT INTO MediaStreams2 (
	ItemId, StreamIndex, StreamType, Codec, Language, ChannelLayout, Profile, AspectRatio, Path,
	IsInterlaced, BitRate, Channels, SampleRate, IsDefault, IsForced, IsHearingImpaired, IsExternal,
	Height, Width, AverageFrameRate, RealFrameRate, Level, PixelFormat, BitDepth, IsAnamorphic,
	RefFrames, Rotation, CodecTag, Comment, NalLengthSize, Title, TimeBase, ColorPrimaries, ColorSpace,
	ColorTransfer, Extradata, AttachmentSize, MimeType, ExtendedVideoType, ExtendedVideoSubtype
) VALUES (` + strings.TrimRight(strings.Repeat("?,", 40), ",") + `)`

type applyStatements struct {
	findItem       *sql.Stmt
	countStreams   *sql.Stmt
	deleteStreams  *sql.Stmt
	deleteChapters *sql.Stmt
	insertStream   *sql.Stmt
	insertChapter  *sql.Stmt
	updateItem     *sql.Stmt
}

func prepareApplyStatements(ctx context.Context, conn *sql.Conn) (*applyStatements, error) {
	stmts := &applyStatements{}
	prepare := func(dst **sql.Stmt, query string) error {
		stmt, err := conn.PrepareContext(ctx, query)
		if err != nil {
			return err
		}
		*dst = stmt
		return nil
	}
	if err := prepare(&stmts.findItem, "SELECT Name FROM MediaItems WHERE Id=?"); err != nil {
		return nil, err
	}
	if err := prepare(&stmts.countStreams, "SELECT COUNT(*) FROM MediaStreams2 WHERE ItemId=?"); err != nil {
		return nil, err
	}
	if err := prepare(&stmts.deleteStreams, "DELETE FROM MediaStreams2 WHERE ItemId=?"); err != nil {
		return nil, err
	}
	if err := prepare(&stmts.deleteChapters, "DELETE FROM Chapters3 WHERE ItemId=?"); err != nil {
		return nil, err
	}
	if err := prepare(&stmts.insertStream, streamInsertSQL); err != nil {
		return nil, err
	}
	if err := prepare(&stmts.insertChapter, `INSERT INTO Chapters3 (ItemId, ChapterIndex, StartPositionTicks, Name, ImagePath, ImageDateModified, MarkerType) VALUES (?, ?, ?, ?, NULL, ?, ?)`); err != nil {
		return nil, err
	}
	if err := prepare(&stmts.updateItem, `UPDATE MediaItems SET RunTimeTicks=?, TotalBitrate=?, Width=?, Height=?, Size=?, Container=? WHERE Id=?`); err != nil {
		return nil, err
	}
	return stmts, nil
}

func (s *applyStatements) close() {
	for _, stmt := range []*sql.Stmt{s.findItem, s.countStreams, s.deleteStreams, s.deleteChapters, s.insertStream, s.insertChapter, s.updateItem} {
		if stmt != nil {
			_ = stmt.Close()
		}
	}
}

func validateVersionPair(source, target string) error {
	sourceSeries := supportedSeries(source)
	targetSeries := supportedSeries(target)
	if sourceSeries == "" || targetSeries == "" || sourceSeries != targetSeries {
		return fmt.Errorf("media database restore only supports %s: source %q, target %q", supportedSeriesSummary(), source, target)
	}
	return nil
}

// dbVersionAllowlistEnv lets operators declare additional Emby series known
// to share the library.db schema (e.g. "4.8.11,4.9.5,4.9.6").
const dbVersionAllowlistEnv = "EMBY_MIGRATOR_DB_VERSIONS"

var supportedSeriesSet = loadSupportedSeries()

func loadSupportedSeries() map[string]bool {
	allowlist := map[string]bool{"4.8.11": true, "4.9.5": true}
	for _, entry := range strings.Split(os.Getenv(dbVersionAllowlistEnv), ",") {
		if series := seriesOf(entry); series != "" {
			allowlist[series] = true
		}
	}
	return allowlist
}

func seriesOf(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:3], ".")
}

func supportedSeries(version string) string {
	series := seriesOf(version)
	if supportedSeriesSet[series] {
		return series
	}
	return ""
}

func supportedSeriesSummary() string {
	series := make([]string, 0, len(supportedSeriesSet))
	for entry := range supportedSeriesSet {
		series = append(series, entry)
	}
	sort.Strings(series)
	parts := make([]string, 0, len(series))
	for _, entry := range series {
		parts = append(parts, entry+".x -> "+entry+".x")
	}
	return strings.Join(parts, " or ")
}

func applyItem(ctx context.Context, stmts *applyStatements, item ItemPatch, overwrite bool) (bool, int, int, error) {
	if item.TargetItemID <= 0 {
		return false, 0, 0, fmt.Errorf("invalid target item id for %s", item.StableKey)
	}
	var targetName sql.NullString
	if err := stmts.findItem.QueryRowContext(ctx, item.TargetItemID).Scan(&targetName); err != nil {
		if err == sql.ErrNoRows {
			return false, 0, 0, fmt.Errorf("target item %d does not exist in Emby database", item.TargetItemID)
		}
		return false, 0, 0, fmt.Errorf("find target item %d: %w", item.TargetItemID, err)
	}
	if expected := strings.TrimSpace(item.TargetName); expected != "" && !equivalentTargetName(targetName.String, expected) {
		return false, 0, 0, fmt.Errorf("target item %d name mismatch: plan %q, database %q; verify the selected target database", item.TargetItemID, expected, targetName.String)
	}
	var existingStreams int
	if err := stmts.countStreams.QueryRowContext(ctx, item.TargetItemID).Scan(&existingStreams); err != nil {
		return false, 0, 0, err
	}
	if existingStreams > 0 && !overwrite {
		return false, 0, 0, nil
	}
	if len(item.MediaStreams) == 0 {
		return false, 0, 0, fmt.Errorf("item %d has no media streams in plan", item.TargetItemID)
	}
	if _, err := stmts.deleteStreams.ExecContext(ctx, item.TargetItemID); err != nil {
		return false, 0, 0, err
	}
	if _, err := stmts.deleteChapters.ExecContext(ctx, item.TargetItemID); err != nil {
		return false, 0, 0, err
	}

	// Plan payloads come from JSON with arbitrary key casing; fold each map's
	// keys once so the per-column lookups below stay O(1).
	foldedStreams := foldKeyMaps(item.MediaStreams)
	for index, stream := range foldedStreams {
		streamIndex, ok := integerValue(pick(stream, "index"))
		if !ok {
			streamIndex = int64(index)
		}
		streamType, err := streamTypeValue(pick(stream, "type"))
		if err != nil {
			return false, 0, 0, fmt.Errorf("item %d stream %d: %w", item.TargetItemID, index, err)
		}
		values := []any{
			item.TargetItemID, streamIndex, streamType, pick(stream, "codec"), pick(stream, "language"),
			pick(stream, "channellayout"), pick(stream, "profile"), pick(stream, "aspectratio"), nil,
			boolInteger(pick(stream, "isinterlaced")), pick(stream, "bitrate"), pick(stream, "channels"),
			pick(stream, "samplerate"), boolInteger(pick(stream, "isdefault")), boolInteger(pick(stream, "isforced")),
			boolInteger(pick(stream, "ishearingimpaired")), boolInteger(pick(stream, "isexternal")),
			pick(stream, "width"), pick(stream, "height"), pick(stream, "averageframerate"),
			pick(stream, "realframerate"), pick(stream, "level"), pick(stream, "pixelformat"),
			pick(stream, "bitdepth"), boolInteger(pick(stream, "isanamorphic")), pick(stream, "refframes"),
			pick(stream, "rotation"), pick(stream, "codectag"), pick(stream, "comment"),
			pick(stream, "nallengthsize"), pick(stream, "title"), pick(stream, "timebase"),
			pick(stream, "colorprimaries"), pick(stream, "colorspace"), pick(stream, "colortransfer"),
			nil, nil, pick(stream, "mimetype"), 0, 0,
		}
		if _, err := stmts.insertStream.ExecContext(ctx, values...); err != nil {
			return false, 0, 0, fmt.Errorf("write target item %d stream %d: %w", item.TargetItemID, index, err)
		}
	}

	for index, raw := range item.Chapters {
		chapter := foldKeys(raw)
		chapterIndex, ok := integerValue(pick(chapter, "chapterindex"))
		if !ok {
			chapterIndex = int64(index)
		}
		start, _ := integerValue(pick(chapter, "startpositionticks"))
		marker := markerTypeValue(pick(chapter, "markertype"))
		if _, err := stmts.insertChapter.ExecContext(ctx,
			item.TargetItemID, chapterIndex, start, pick(chapter, "name"), emptyDateModified, marker); err != nil {
			return false, 0, 0, fmt.Errorf("write target item %d chapter %d: %w", item.TargetItemID, index, err)
		}
	}

	width, height := primaryVideoDimensions(foldedStreams)
	source := foldKeys(item.MediaSource)
	if _, err := stmts.updateItem.ExecContext(ctx,
		pick(source, "runtimeticks"), firstValue(source, "bitrate"), width, height,
		pick(source, "size"), pick(source, "container"), item.TargetItemID); err != nil {
		return false, 0, 0, fmt.Errorf("update target item %d media summary: %w", item.TargetItemID, err)
	}
	return true, len(item.MediaStreams), len(item.Chapters), nil
}

// foldKeys returns a copy of values with keys lowercased and trimmed so that
// case-insensitive lookups become a single map access.
func foldKeys(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	folded := make(map[string]any, len(values))
	for key, value := range values {
		folded[strings.ToLower(strings.TrimSpace(key))] = value
	}
	return folded
}

func foldKeyMaps(values []map[string]any) []map[string]any {
	folded := make([]map[string]any, len(values))
	for i, value := range values {
		folded[i] = foldKeys(value)
	}
	return folded
}

// pick reads a value from a map produced by foldKeys; key must be lowercase.
func pick(folded map[string]any, key string) any {
	if folded == nil {
		return nil
	}
	return normalizeSQLiteValue(folded[key])
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

func firstValue(folded map[string]any, keys ...string) any {
	for _, key := range keys {
		if value := pick(folded, key); value != nil {
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

func primaryVideoDimensions(foldedStreams []map[string]any) (any, any) {
	var best map[string]any
	var bestPixels int64
	for _, stream := range foldedStreams {
		typ, err := streamTypeValue(pick(stream, "type"))
		if err != nil || typ != 2 {
			continue
		}
		width, _ := integerValue(pick(stream, "width"))
		height, _ := integerValue(pick(stream, "height"))
		if width*height >= bestPixels {
			best = stream
			bestPixels = width * height
		}
	}
	if best == nil {
		return nil, nil
	}
	return pick(best, "width"), pick(best, "height")
}

// backupStepPages keeps each online-backup step bounded so cancellation is
// observed mid-copy instead of only between whole databases.
const backupStepPages = 2048

func backupDatabase(ctx context.Context, conn *sql.Conn, target string, mode os.FileMode) error {
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
		for {
			remaining, err := backup.Step(backupStepPages)
			if err != nil {
				_ = backup.Finish()
				return err
			}
			if !remaining {
				break
			}
			if err := ctx.Err(); err != nil {
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

type databaseBackup struct {
	path      string
	timestamp time.Time
}

func cleanupDatabaseBackups(databasePath, currentBackupPath string, keep int, remove func(string) error) (int, error) {
	if keep < 1 {
		keep = 1
	}
	directory := filepath.Dir(databasePath)
	prefix := filepath.Base(databasePath) + ".emby-migrator-"
	const timestampLayout = "20060102-150405.000000000"
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("list old Emby database backups: %w", err)
	}

	currentBackupPath = filepath.Clean(currentBackupPath)
	backups := make([]databaseBackup, 0, len(entries))
	warnings := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".bak") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		timestampText := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".bak")
		timestamp, parseErr := time.ParseInLocation(timestampLayout, timestampText, time.Local)
		if parseErr != nil || timestamp.Format(timestampLayout) != timestampText {
			continue
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			warnings = append(warnings, fmt.Sprintf("inspect backup %s: %v", name, infoErr))
			continue
		}
		if !entryInfo.Mode().IsRegular() {
			continue
		}
		path := filepath.Clean(filepath.Join(directory, name))
		if path == currentBackupPath {
			continue
		}
		backups = append(backups, databaseBackup{path: path, timestamp: timestamp})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].timestamp.After(backups[j].timestamp)
	})
	deleteFrom := keep - 1 // The current backup always consumes one retention slot.
	if deleteFrom > len(backups) {
		deleteFrom = len(backups)
	}
	pruned := 0
	for _, backup := range backups[deleteFrom:] {
		if err := remove(backup.path); err != nil {
			warnings = append(warnings, fmt.Sprintf("remove old backup %s: %v", filepath.Base(backup.path), err))
			continue
		}
		pruned++
	}
	if len(warnings) > 0 {
		return pruned, fmt.Errorf("backup retention cleanup incomplete: %s", strings.Join(warnings, "; "))
	}
	return pruned, nil
}
