package embydb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const MediaSchemaIdentity = "emby-library-media-v1"

type TargetAnchor struct {
	ItemID int64
	Name   string
}

type schemaColumn struct {
	name string
}

var mediaSchemaColumns = map[string][]schemaColumn{
	"MediaItems": {
		{name: "Id"}, {name: "Name"}, {name: "RunTimeTicks"}, {name: "TotalBitrate"},
		{name: "Width"}, {name: "Height"}, {name: "Size"}, {name: "Container"},
	},
	"MediaStreams2": {
		{name: "ItemId"}, {name: "StreamIndex"}, {name: "StreamType"}, {name: "Codec"},
		{name: "Language"}, {name: "ChannelLayout"}, {name: "Profile"}, {name: "AspectRatio"}, {name: "Path"},
		{name: "IsInterlaced"}, {name: "BitRate"}, {name: "Channels"}, {name: "SampleRate"},
		{name: "IsDefault"}, {name: "IsForced"}, {name: "IsHearingImpaired"}, {name: "IsExternal"},
		{name: "Height"}, {name: "Width"}, {name: "AverageFrameRate"}, {name: "RealFrameRate"},
		{name: "Level"}, {name: "PixelFormat"}, {name: "BitDepth"}, {name: "IsAnamorphic"},
		{name: "RefFrames"}, {name: "Rotation"}, {name: "CodecTag"}, {name: "Comment"},
		{name: "NalLengthSize"}, {name: "Title"}, {name: "TimeBase"}, {name: "ColorPrimaries"},
		{name: "ColorSpace"}, {name: "ColorTransfer"}, {name: "Extradata"}, {name: "AttachmentSize"},
		{name: "MimeType"}, {name: "ExtendedVideoType"}, {name: "ExtendedVideoSubtype"},
	},
	"Chapters3": {
		{name: "ItemId"}, {name: "ChapterIndex"}, {name: "StartPositionTicks"},
		{name: "Name"}, {name: "ImagePath"}, {name: "ImageDateModified"}, {name: "MarkerType"},
	},
}

var mediaSchemaUniqueKeys = map[string][]string{
	"MediaItems":    {"Id"},
	"MediaStreams2": {"ItemId", "StreamIndex"},
	"Chapters3":     {"ItemId", "ChapterIndex"},
}

func BuildTargetBindingDigest(serverID string, anchors []TargetAnchor) (string, error) {
	if len(anchors) == 0 {
		return "", fmt.Errorf("target database binding has no item anchors")
	}
	canonical := make([]string, 0, len(anchors))
	seen := make(map[int64]struct{}, len(anchors))
	for _, anchor := range anchors {
		if anchor.ItemID <= 0 {
			return "", fmt.Errorf("target database binding has invalid item id %d", anchor.ItemID)
		}
		name := portableTargetName(anchor.Name)
		if name == "" {
			return "", fmt.Errorf("target database binding item %d has no usable name", anchor.ItemID)
		}
		if _, exists := seen[anchor.ItemID]; exists {
			return "", fmt.Errorf("target database binding contains duplicate item id %d", anchor.ItemID)
		}
		seen[anchor.ItemID] = struct{}{}
		canonical = append(canonical, strconv.FormatInt(anchor.ItemID, 10)+"\x00"+name)
	}
	sort.Strings(canonical)
	digest := sha256.Sum256([]byte(strings.TrimSpace(serverID) + "\n" + strings.Join(canonical, "\n")))
	return hex.EncodeToString(digest[:]), nil
}

func validateSchema(ctx context.Context, conn *sql.Conn) (string, error) {
	tables := make([]string, 0, len(mediaSchemaColumns))
	for table := range mediaSchemaColumns {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return "", fmt.Errorf("inspect %s schema: %w", table, err)
		}
		found := map[string]bool{}
		primaryKey := map[int]string{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull, primaryKeyPosition int
			var defaultValue any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKeyPosition); err != nil {
				rows.Close()
				return "", fmt.Errorf("inspect %s schema: %w", table, err)
			}
			found[strings.ToLower(name)] = true
			if primaryKeyPosition > 0 {
				primaryKey[primaryKeyPosition] = name
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", fmt.Errorf("inspect %s schema: %w", table, err)
		}
		rows.Close()
		for _, column := range mediaSchemaColumns[table] {
			if !found[strings.ToLower(column.name)] {
				return "", fmt.Errorf("unsupported Emby database schema: %s.%s is missing", table, column.name)
			}
		}
		if !sameOrderedColumns(primaryKeyColumns(primaryKey), mediaSchemaUniqueKeys[table]) {
			unique, err := hasUniqueIndex(ctx, conn, table, mediaSchemaUniqueKeys[table])
			if err != nil {
				return "", err
			}
			if !unique {
				return "", fmt.Errorf("unsupported Emby database schema: %s lacks unique key (%s)", table, strings.Join(mediaSchemaUniqueKeys[table], ", "))
			}
		}
	}
	return MediaSchemaIdentity, nil
}

func primaryKeyColumns(columns map[int]string) []string {
	positions := make([]int, 0, len(columns))
	for position := range columns {
		positions = append(positions, position)
	}
	sort.Ints(positions)
	out := make([]string, 0, len(positions))
	for _, position := range positions {
		out = append(out, columns[position])
	}
	return out
}

func hasUniqueIndex(ctx context.Context, conn *sql.Conn, table string, expected []string) (bool, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA index_list("+table+")")
	if err != nil {
		return false, fmt.Errorf("inspect %s indexes: %w", table, err)
	}
	var indexes []string
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return false, fmt.Errorf("inspect %s indexes: %w", table, err)
		}
		if unique != 0 && partial == 0 {
			indexes = append(indexes, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("inspect %s indexes: %w", table, err)
	}
	rows.Close()
	for _, index := range indexes {
		indexRows, err := conn.QueryContext(ctx, "PRAGMA index_info("+strconv.Quote(index)+")")
		if err != nil {
			return false, fmt.Errorf("inspect %s index %s: %w", table, index, err)
		}
		var columns []string
		for indexRows.Next() {
			var sequence, cid int
			var name string
			if err := indexRows.Scan(&sequence, &cid, &name); err != nil {
				indexRows.Close()
				return false, fmt.Errorf("inspect %s index %s: %w", table, index, err)
			}
			columns = append(columns, name)
		}
		if err := indexRows.Err(); err != nil {
			indexRows.Close()
			return false, fmt.Errorf("inspect %s index %s: %w", table, index, err)
		}
		indexRows.Close()
		if sameOrderedColumns(columns, expected) {
			return true, nil
		}
	}
	return false, nil
}

func sameOrderedColumns(actual, expected []string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range actual {
		if !strings.EqualFold(strings.TrimSpace(actual[index]), strings.TrimSpace(expected[index])) {
			return false
		}
	}
	return true
}

func validateTargetBinding(ctx context.Context, conn *sql.Conn, serverID, expectedDigest string, expectedCount int, items []ItemPatch) error {
	if expectedCount != len(items) {
		return fmt.Errorf("target database binding item count mismatch: plan %d, patches %d", expectedCount, len(items))
	}
	anchors := make([]TargetAnchor, 0, len(items))
	for _, item := range items {
		var name sql.NullString
		if err := conn.QueryRowContext(ctx, "SELECT Name FROM MediaItems WHERE Id=?", item.TargetItemID).Scan(&name); err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("target database binding failed for server %q: item %d does not exist; verify the selected library.db", serverID, item.TargetItemID)
			}
			return fmt.Errorf("target database binding failed for server %q: read item %d: %w", serverID, item.TargetItemID, err)
		}
		if expected := strings.TrimSpace(item.TargetName); expected != "" && !equivalentTargetName(name.String, expected) {
			return fmt.Errorf("target item %d name mismatch: plan %q, database %q; verify the selected target database", item.TargetItemID, expected, name.String)
		}
		anchors = append(anchors, TargetAnchor{ItemID: item.TargetItemID, Name: name.String})
	}
	actualDigest, err := BuildTargetBindingDigest(serverID, anchors)
	if err != nil {
		return fmt.Errorf("target database binding failed for server %q: %w", serverID, err)
	}
	if !strings.EqualFold(strings.TrimSpace(actualDigest), strings.TrimSpace(expectedDigest)) {
		return fmt.Errorf("target database binding mismatch for server %q; the selected library.db is not the database used to create this plan", serverID)
	}
	return nil
}
