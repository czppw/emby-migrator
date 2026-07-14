package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"emby-migrator/internal/emby"
)

func TestSlugAndUniqueSlugPreventPathTraversalAndCollisions(t *testing.T) {
	got := Slug("  Movie: The/Final? Cut  ")
	if got != "movie-the-final-cut" {
		t.Fatalf("Slug returned %q, want movie-the-final-cut", got)
	}

	dirty := SafeName("../..//CON")
	if dirty == "" {
		t.Fatal("SafeName should never return an empty path segment")
	}
	if strings.Contains(dirty, "..") || strings.ContainsAny(dirty, "\\/:*?\"<>|") {
		t.Fatalf("SafeName returned unsafe path segment %q", dirty)
	}
	if got := SafeName("日韩电影"); got != "日韩电影" {
		t.Fatalf("SafeName should preserve readable CJK names, got %q", got)
	}
	if got := SafeName("CON"); got == "con" {
		t.Fatalf("SafeName should avoid Windows reserved device names, got %q", got)
	}

	used := map[string]int{}
	if first := UniqueSlug("movie-the-final-cut", used); first != "movie-the-final-cut" {
		t.Fatalf("first UniqueSlug returned %q, want movie-the-final-cut", first)
	}
	if second := UniqueSlug("movie-the-final-cut", used); second != "movie-the-final-cut-2" {
		t.Fatalf("second UniqueSlug returned %q, want movie-the-final-cut-2", second)
	}
	if third := UniqueSlug("movie-the-final-cut", used); third != "movie-the-final-cut-3" {
		t.Fatalf("third UniqueSlug returned %q, want movie-the-final-cut-3", third)
	}
}

func TestStableItemKeyProviderIDsOutrankOldEmbyID(t *testing.T) {
	first := emby.Item{
		ID:          "old-emby-id-1",
		Type:        "Movie",
		Name:        "The Matrix",
		ProviderIDs: map[string]string{"Tmdb": "603", "Imdb": "tt0133093"},
	}
	second := emby.Item{
		ID:          "old-emby-id-2",
		Type:        "Movie",
		Name:        "Matrix",
		ProviderIDs: map[string]string{"Imdb": "tt0133093", "Tmdb": "603"},
	}

	firstKey := StableItemKey(first)
	secondKey := StableItemKey(second)
	if firstKey == "" {
		t.Fatal("StableItemKey returned an empty key")
	}
	if firstKey != secondKey {
		t.Fatalf("items with the same ProviderIds should share a stable key: %q != %q", firstKey, secondKey)
	}
	if strings.Contains(firstKey, "old-emby-id") {
		t.Fatalf("stable key must not depend on old Emby IDs: %q", firstKey)
	}
	if !strings.Contains(firstKey, "imdb") || !strings.Contains(firstKey, "tt0133093") {
		t.Fatalf("provider-based key should expose the provider signal for debugging, got %q", firstKey)
	}
}

func TestStableItemKeyUsesEpisodeIdentityAndNameYearFallback(t *testing.T) {
	firstEpisode := emby.Item{
		ID:                "old-episode-1",
		Type:              "Episode",
		Name:              "Pilot",
		SeriesName:        "Firefly",
		ParentIndexNumber: 1,
		IndexNumber:       2,
	}
	secondEpisode := emby.Item{
		ID:                "new-episode-99",
		Type:              "Episode",
		Name:              "The Train Job",
		SeriesName:        "Firefly",
		ParentIndexNumber: 1,
		IndexNumber:       2,
	}

	if firstKey, secondKey := StableItemKey(firstEpisode), StableItemKey(secondEpisode); firstKey != secondKey {
		t.Fatalf("episode identity should use series+season+episode, got %q and %q", firstKey, secondKey)
	}

	fallback := emby.Item{
		Type:           "Movie",
		Name:           "Alien",
		OriginalTitle:  "Alien",
		ProductionYear: 1979,
	}
	key := StableItemKey(fallback)
	if key == "" {
		t.Fatal("StableItemKey returned an empty fallback key")
	}
	if !strings.Contains(key, "movie") || !strings.Contains(key, "alien") || !strings.Contains(key, "1979") {
		t.Fatalf("name/year/type fallback key lost important matching signals: %q", key)
	}
}

func TestStablePersonKeyProviderIDsOutrankOldEmbyInternalID(t *testing.T) {
	first := emby.Person{
		ID:          "old-person-id-1",
		Name:        "Keanu Reeves",
		Type:        "Actor",
		Role:        "Neo",
		ProviderIDs: map[string]string{"Imdb": "nm0000206"},
	}
	second := emby.Person{
		ID:          "old-person-id-2",
		Name:        "K. Reeves",
		Type:        "Actor",
		Role:        "Thomas Anderson",
		ProviderIDs: map[string]string{"Imdb": "nm0000206"},
	}

	firstKey := StablePersonKey(first)
	secondKey := StablePersonKey(second)
	if firstKey != secondKey {
		t.Fatalf("people with same ProviderIds should share a stable key: %q != %q", firstKey, secondKey)
	}
	if strings.Contains(firstKey, "old-person-id") {
		t.Fatalf("person stable key must not use old Emby person IDs: %q", firstKey)
	}
	if !strings.Contains(firstKey, "person-provider-imdb-nm0000206") {
		t.Fatalf("person stable key should preserve provider signal, got %q", firstKey)
	}
}

func TestManifestJSONKeepsCompleteMetadataPeopleAvatarsAndNoAPIKey(t *testing.T) {
	exportedAt := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	manifest := Manifest{
		ToolVersion:   "0.1.0-test",
		EmbyVersion:   "4.8.11.0",
		ExportedAt:    exportedAt,
		SchemaVersion: 1,
		Compatibility: "emby-4.8.11",
		Source:        "http://emby.local:8096/emby",
		Libraries: []LibraryEntry{
			{ID: "lib-movies", Name: "Movies", Slug: "movies", Count: 1},
		},
		Items: []ItemEntry{
			{
				StableKey:         "provider-imdb-tt0133093",
				Slug:              "the-matrix",
				LibraryID:         "lib-movies",
				LibraryName:       "Movies",
				ID:                "old-item-1",
				Name:              "The Matrix",
				Type:              "Movie",
				Path:              "/media/Movies/The Matrix.mkv",
				OriginalTitle:     "The Matrix",
				ProductionYear:    1999,
				SeriesName:        "",
				IndexNumber:       0,
				ParentIndexNumber: 0,
				ProviderIDs:       map[string]string{"Imdb": "tt0133093", "Tmdb": "603"},
				InfoPath:          "libraries/movies/items/provider-imdb-tt0133093/info.json",
				RawPath:           "libraries/movies/items/provider-imdb-tt0133093/raw.json",
				Images: []FileEntry{
					{Type: "Primary", Path: "libraries/movies/items/provider-imdb-tt0133093/primary.jpg", Size: 3, SHA256: "abc123"},
					{Type: "Backdrop", Index: 0, Path: "libraries/movies/items/provider-imdb-tt0133093/backdrop-0.jpg", Size: 4, SHA256: "def456"},
				},
				People:    []string{"person-provider-imdb-nm0000206"},
				MediaInfo: &MediaInfo{SourcesCount: 1, StreamsCount: 2, ChaptersCount: 1, Hash: "mediahash"},
			},
		},
		People: []PersonEntry{
			{
				StableKey:   "person-provider-imdb-nm0000206",
				Name:        "Keanu Reeves",
				Type:        "Actor",
				Role:        "Neo",
				ProviderIDs: map[string]string{"Imdb": "nm0000206"},
				Image:       &FileEntry{Type: "Primary", Path: "people/person-provider-imdb-nm0000206/primary.jpg", Size: 5, SHA256: "face123"},
				ReferencedBy: []string{
					"provider-imdb-tt0133093",
				},
				RawPath: "people/person-provider-imdb-nm0000206/raw.json",
			},
		},
		Errors: []ErrorEntry{
			{Scope: "image", ID: "bad-item", Message: "image 404"},
		},
		Summary: Summary{
			Libraries:          1,
			Items:              1,
			People:             1,
			ItemImages:         2,
			PeopleImages:       1,
			Errors:             1,
			ItemsWithMediaInfo: 1,
			MediaSources:       1,
			MediaStreams:       2,
			Chapters:           1,
		},
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("manifest should marshal as JSON: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, testManifestAPIKey) || strings.Contains(strings.ToLower(text), "api_key") || strings.Contains(strings.ToLower(text), "x-emby-token") {
		t.Fatalf("manifest leaked API key material: %s", text)
	}

	for _, want := range []string{
		"0.1.0-test",
		"4.8.11.0",
		"provider-imdb-tt0133093",
		"old-item-1",
		"tt0133093",
		"originalTitle",
		"productionYear",
		"info.json",
		"raw.json",
		"backdrop-0.jpg",
		"person-provider-imdb-nm0000206",
		"Keanu Reeves",
		"primary.jpg",
		"peopleImages",
		"face123",
		"image 404",
		"mediaInfo",
		"mediaStreams",
		"mediahash",
		exportedAt.Format(time.RFC3339),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest JSON missing %q: %s", want, text)
		}
	}
}

func TestManifestMediaInfoRoundTripAndSummary(t *testing.T) {
	mediaInfo := NewMediaInfo(
		[]map[string]any{{"Id": "source-1", "Protocol": "File"}},
		[]map[string]any{{"Type": "Video", "Codec": "hevc"}, {"Type": "Audio", "Codec": "aac"}},
		[]map[string]any{{"Name": "Intro", "StartPositionTicks": json.Number("0")}},
	)
	if mediaInfo == nil || mediaInfo.SourcesHash == "" || mediaInfo.StreamsHash == "" || mediaInfo.ChaptersHash == "" || mediaInfo.Hash == "" {
		t.Fatalf("NewMediaInfo did not compute hashes: %#v", mediaInfo)
	}

	manifest, err := BuildManifest(ManifestInput{
		ToolVersion:   "0.1.0-test",
		ServerVersion: "4.8.11.0",
		ExportedAt:    time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC),
		APIKey:        testManifestAPIKey,
		Libraries:     []ManifestLibrary{{ID: "lib-movies", Name: "Movies", Slug: "movies"}},
		Items: []ManifestItem{{
			StableKey:   "provider-tmdb-987654",
			EmbyID:      "item-media-1",
			Type:        "Movie",
			Name:        "Media Fixture",
			Path:        "/media/fixture.mkv",
			ProviderIDs: map[string]string{"Tmdb": "987654"},
			MediaInfo:   mediaInfo,
		}},
	})
	if err != nil {
		t.Fatalf("BuildManifest returned error: %v", err)
	}
	if manifest.Summary.ItemsWithMediaInfo != 1 || manifest.Summary.MediaSources != 1 || manifest.Summary.MediaStreams != 2 || manifest.Summary.Chapters != 1 {
		t.Fatalf("media summary not populated: %#v", manifest.Summary)
	}

	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("manifest should marshal: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, testManifestAPIKey) || strings.Contains(strings.ToLower(text), "api_key") || strings.Contains(strings.ToLower(text), "x-emby-token") {
		t.Fatalf("manifest leaked API key material: %s", text)
	}

	var decoded Manifest
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("manifest should roundtrip: %v", err)
	}
	if len(decoded.Items) != 1 || decoded.Items[0].MediaInfo == nil {
		t.Fatalf("roundtrip lost item mediaInfo: %#v", decoded.Items)
	}
	got := decoded.Items[0].MediaInfo
	if got.SourcesCount != 1 || got.StreamsCount != 2 || got.ChaptersCount != 1 || got.Hash != mediaInfo.Hash {
		t.Fatalf("roundtrip changed mediaInfo: got %#v want %#v", got, mediaInfo)
	}
	if decoded.Summary.MediaStreams != 2 || decoded.Summary.Chapters != 1 {
		t.Fatalf("roundtrip lost media summary: %#v", decoded.Summary)
	}
}

func TestItemInfoPreservesRawEmbyMetadataAndPeopleForImportValidation(t *testing.T) {
	info := ItemInfo{
		Item: emby.Item{
			ID:              "old-item-1",
			Name:            "The Matrix",
			Type:            "Movie",
			Path:            "/media/Movies/The Matrix.mkv",
			OriginalTitle:   "The Matrix",
			Overview:        "A computer hacker learns about the nature of reality.",
			OfficialRating:  "R",
			ProductionYear:  1999,
			PremiereDate:    "1999-03-31T00:00:00.0000000Z",
			CommunityRating: 8.7,
			Genres:          []string{"Action", "Sci-Fi"},
			Studios:         []emby.NameID{{Name: "Warner Bros."}},
			Tags:            []string{"cyberpunk"},
			Taglines:        []string{"Welcome to the Real World."},
			ProviderIDs:     map[string]string{"Imdb": "tt0133093"},
			People: []emby.Person{
				{Name: "Keanu Reeves", ID: "old-person-id", Type: "Actor", Role: "Neo", ProviderIDs: map[string]string{"Imdb": "nm0000206"}, PrimaryImageTag: "avatar-tag"},
			},
			ImageTags:         map[string]string{"Primary": "primary-tag"},
			BackdropImageTags: []string{"bd-0"},
			MediaSources:      []map[string]any{{"Id": "source-1", "Protocol": "File"}},
			MediaStreams:      []map[string]any{{"Type": "Video", "Codec": "hevc"}},
			Chapters:          []map[string]any{{"Name": "Intro", "StartPositionTicks": 0}},
			Raw: map[string]any{
				"Id":           "old-item-1",
				"ProviderIds":  map[string]any{"Imdb": "tt0133093"},
				"MediaSources": []map[string]any{{"Id": "source-1", "Protocol": "File"}},
				"MediaStreams": []map[string]any{{"Type": "Video", "Codec": "hevc"}},
				"Chapters":     []map[string]any{{"Name": "Intro", "StartPositionTicks": 0}},
			},
		},
		StableKey:  "provider-imdb-tt0133093",
		ExportedAt: time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC),
		Images: []FileEntry{
			{Type: "Primary", Path: "primary.jpg", Size: 3, SHA256: "abc123"},
		},
		People: []emby.Person{
			{Name: "Keanu Reeves", ID: "old-person-id", Type: "Actor", Role: "Neo", ProviderIDs: map[string]string{"Imdb": "nm0000206"}, PrimaryImageTag: "avatar-tag"},
		},
	}

	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("ItemInfo should marshal as JSON: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"Overview",
		"OfficialRating",
		"ProductionYear",
		"PremiereDate",
		"CommunityRating",
		"Genres",
		"Studios",
		"Tags",
		"Taglines",
		"ProviderIds",
		"OriginalTitle",
		"People",
		"PrimaryImageTag",
		"BackdropImageTags",
		"MediaSources",
		"MediaStreams",
		"Chapters",
		"hevc",
		"Raw",
		"nm0000206",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("ItemInfo JSON missing metadata field/value %q: %s", want, text)
		}
	}
}

const testManifestAPIKey = "MANIFESTSECRET-123456"
