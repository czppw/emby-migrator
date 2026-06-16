package exporter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
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
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-1":
			writeExporterJSON(t, w, map[string]any{
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
			})
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
			People:          []emby.Person{{Name: "Actor One", ID: "old-person-id"}},
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
	if _, ok := current.Raw["People"]; ok {
		t.Fatalf("payload must not copy old People ids: %#v", current.Raw["People"])
	}
	studios, ok := current.Raw["Studios"].([]map[string]string)
	if !ok || len(studios) != 1 || studios[0]["Name"] != "Old Studio" {
		t.Fatalf("payload should keep studio names only, got %#v", current.Raw["Studios"])
	}
	if _, ok := studios[0]["Id"]; ok {
		t.Fatalf("payload leaked old studio id: %#v", studios[0])
	}
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

	match := NewService(exportPath).importItem(context.Background(), client, exportPath, storage.ItemEntry{
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

	match := NewService(exportPath).importItem(context.Background(), client, exportPath, storage.ItemEntry{
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
