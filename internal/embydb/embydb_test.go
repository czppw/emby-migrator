package embydb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyWritesMediaInfoAndCreatesBackup(t *testing.T) {
	path := createFixtureDatabase(t)
	result, err := Apply(context.Background(), ApplyOptions{
		DatabasePath:  path,
		SourceVersion: "4.8.11.0",
		TargetVersion: "4.8.11.0",
		Items:         []ItemPatch{fixturePatch()},
		Now:           func() time.Time { return time.Date(2026, 7, 14, 12, 0, 0, 0, time.Local) },
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.ItemsApplied != 1 || result.StreamsWritten != 3 || result.ChaptersWritten != 2 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup was not created: %v", err)
	}
	backupDB := openFixtureDatabase(t, result.BackupPath)
	defer backupDB.Close()
	var backupStreams int
	if err := backupDB.QueryRow("SELECT COUNT(*) FROM MediaStreams2 WHERE ItemId=200").Scan(&backupStreams); err != nil {
		t.Fatalf("read backup database: %v", err)
	}
	if backupStreams != 0 {
		t.Fatalf("backup should contain the state before migration, got %d streams", backupStreams)
	}

	db := openFixtureDatabase(t, path)
	defer db.Close()
	var streams, chapters int
	if err := db.QueryRow("SELECT COUNT(*) FROM MediaStreams2 WHERE ItemId=200").Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM Chapters3 WHERE ItemId=200").Scan(&chapters); err != nil {
		t.Fatal(err)
	}
	if streams != 3 || chapters != 2 {
		t.Fatalf("rows streams=%d chapters=%d", streams, chapters)
	}
	var databaseHeight, databaseWidth int
	if err := db.QueryRow("SELECT Height, Width FROM MediaStreams2 WHERE ItemId=200 AND StreamIndex=0").Scan(&databaseHeight, &databaseWidth); err != nil {
		t.Fatal(err)
	}
	if databaseHeight != 640 || databaseWidth != 360 {
		t.Fatalf("Emby stream dimensions were not mapped to its reversed columns: Height=%d Width=%d", databaseHeight, databaseWidth)
	}
	var runtime, bitrate, width, height, size int64
	var container string
	if err := db.QueryRow("SELECT RunTimeTicks, TotalBitrate, Width, Height, Size, Container FROM MediaItems WHERE Id=200").Scan(&runtime, &bitrate, &width, &height, &size, &container); err != nil {
		t.Fatal(err)
	}
	if runtime != 120210000 || bitrate != 1865170 || width != 640 || height != 360 || size != 2802652 || container != "mkv" {
		t.Fatalf("unexpected media summary runtime=%d bitrate=%d %dx%d size=%d container=%s", runtime, bitrate, width, height, size, container)
	}
}

func TestApplySkipsExistingStreamsWithoutOverwrite(t *testing.T) {
	path := createFixtureDatabase(t)
	db := openFixtureDatabase(t, path)
	if _, err := db.Exec("INSERT INTO MediaStreams2 (ItemId, StreamIndex, StreamType, Codec) VALUES (200,0,2,'existing')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	result, err := Apply(context.Background(), ApplyOptions{
		DatabasePath: path, SourceVersion: "4.9.5.0", TargetVersion: "4.9.5.0", Items: []ItemPatch{fixturePatch()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ItemsApplied != 0 || result.ItemsSkipped != 1 {
		t.Fatalf("existing target should be skipped: %#v", result)
	}
}

func TestApplyRejectsCrossVersionAndLockedDatabase(t *testing.T) {
	path := createFixtureDatabase(t)
	_, err := Apply(context.Background(), ApplyOptions{
		DatabasePath: path, SourceVersion: "4.8.11.0", TargetVersion: "4.9.5.0", Items: []ItemPatch{fixturePatch()},
	})
	if err == nil || !strings.Contains(err.Error(), "only supports") {
		t.Fatalf("cross-version restore should be rejected: %v", err)
	}

	db := openFixtureDatabase(t, path)
	defer db.Close()
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("ROLLBACK")
	_, err = Apply(context.Background(), ApplyOptions{
		DatabasePath: path, SourceVersion: "4.8.11.0", TargetVersion: "4.8.11.0", Items: []ItemPatch{fixturePatch()},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Fatalf("locked database should be rejected clearly: %v", err)
	}
}

func TestApplyRejectsTargetNameMismatch(t *testing.T) {
	path := createFixtureDatabase(t)
	patch := fixturePatch()
	patch.TargetName = "A Different Movie"
	_, err := Apply(context.Background(), ApplyOptions{
		DatabasePath: path, SourceVersion: "4.9.5.0", TargetVersion: "4.9.5.0", Items: []ItemPatch{patch},
	})
	if err == nil || !strings.Contains(err.Error(), "name mismatch") {
		t.Fatalf("wrong target database should be rejected: %v", err)
	}
}

func TestApplyAcceptsPortableTargetName(t *testing.T) {
	path := createFixtureDatabase(t)
	db := openFixtureDatabase(t, path)
	if _, err := db.Exec("UPDATE MediaItems SET Name='Big.Buck.Bunny' WHERE Id=200"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	result, err := Apply(context.Background(), ApplyOptions{
		DatabasePath: path, SourceVersion: "4.9.5.0", TargetVersion: "4.9.5.0", Items: []ItemPatch{fixturePatch()},
	})
	if err != nil {
		t.Fatalf("portable target name should be accepted: %v", err)
	}
	if result.ItemsApplied != 1 {
		t.Fatalf("portable target name apply result = %#v", result)
	}
}

func createFixtureDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "library.db")
	db := openFixtureDatabase(t, path)
	defer db.Close()
	statements := []string{
		`CREATE TABLE MediaItems (Id INTEGER PRIMARY KEY, Name TEXT, RunTimeTicks BIGINT, TotalBitrate INT, Width INT, Height INT, Size INT, Container TEXT)`,
		`CREATE TABLE MediaStreams2 (
			ItemId INT, StreamIndex INT, StreamType INT, Codec TEXT, Language TEXT, ChannelLayout TEXT, Profile TEXT,
			AspectRatio TEXT, Path TEXT, IsInterlaced BIT, BitRate INT, Channels INT, SampleRate INT, IsDefault BIT,
			IsForced BIT, IsHearingImpaired BIT, IsExternal BIT, Height INT, Width INT, AverageFrameRate FLOAT,
			RealFrameRate FLOAT, Level FLOAT, PixelFormat TEXT, BitDepth INT, IsAnamorphic BIT, RefFrames INT,
			Rotation INT, CodecTag TEXT, Comment TEXT, NalLengthSize TEXT, Title TEXT, TimeBase TEXT, ColorPrimaries TEXT,
			ColorSpace TEXT, ColorTransfer TEXT, Extradata TEXT, AttachmentSize INT, MimeType TEXT,
			ExtendedVideoType INT, ExtendedVideoSubtype INT, PRIMARY KEY(ItemId, StreamIndex))`,
		`CREATE TABLE Chapters3 (ItemId INT, ChapterIndex INT, StartPositionTicks BIGINT, Name TEXT, ImagePath TEXT, ImageDateModified INT, MarkerType INT, PRIMARY KEY(ItemId, ChapterIndex))`,
		`INSERT INTO MediaItems (Id, Name) VALUES (200, 'Big Buck Bunny')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create fixture: %v", err)
		}
	}
	return path
}

func openFixtureDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func fixturePatch() ItemPatch {
	return ItemPatch{
		StableKey:    "big-buck-bunny",
		TargetItemID: 200,
		TargetName:   "Big Buck Bunny",
		MediaSource: map[string]any{
			"RunTimeTicks": int64(120210000), "Bitrate": 1865170, "Size": 2802652, "Container": "mkv",
		},
		MediaStreams: []map[string]any{
			{"Index": 0, "Type": "Video", "Codec": "h264", "Width": 640, "Height": 360, "BitRate": 1865170, "AverageFrameRate": 24.0},
			{"Index": 1, "Type": "Audio", "Codec": "aac", "Language": "eng", "Channels": 1, "SampleRate": 48000},
			{"Index": 2, "Type": "Subtitle", "Codec": "subrip", "Language": "chi"},
		},
		Chapters: []map[string]any{
			{"ChapterIndex": 0, "StartPositionTicks": int64(0), "Name": "Opening"},
			{"ChapterIndex": 1, "StartPositionTicks": int64(60000000), "Name": "Verification"},
		},
	}
}
