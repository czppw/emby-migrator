package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const testAPIKey = "abc123SECRETxyz"

func TestNormalizeBaseURLPreservesSubpathAndMaskAPIKeyRedactsSecret(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "adds scheme", in: "emby.local:8096", want: "http://emby.local:8096"},
		{name: "trims trailing slash", in: "http://emby.local:8096/emby/", want: "http://emby.local:8096/emby"},
		{name: "drops query and fragment", in: "https://emby.local/emby/?api_key=leak#x", want: "https://emby.local/emby"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeBaseURL(tt.in)
			if err != nil {
				t.Fatalf("NormalizeBaseURL returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			assertNoSecret(t, "normalized url", got)
		})
	}

	masked := MaskAPIKey(testAPIKey)
	assertNoSecret(t, "masked key", masked)
	if masked == "" || masked == testAPIKey {
		t.Fatalf("MaskAPIKey should return a non-empty masked value, got %q", masked)
	}
	if short := MaskAPIKey("short"); strings.Contains(short, "short") {
		t.Fatalf("MaskAPIKey should fully hide short keys, got %q", short)
	}
}

func TestLibrariesParsesItemsResponseAndKeepsAPIKeyOutOfQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emby/Items" {
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Emby-Token") != testAPIKey {
			http.Error(w, "missing X-Emby-Token", http.StatusUnauthorized)
			return
		}
		if strings.Contains(r.URL.RawQuery, testAPIKey) {
			http.Error(w, "api key leaked into query string", http.StatusBadRequest)
			return
		}

		parentID := r.URL.Query().Get("ParentId")
		if parentID != "" {
			count := 0
			if parentID == "lib-movies" {
				count = 12
			}
			if parentID == "lib-tv" {
				count = 34
			}
			writeItemsPage(t, w, count, nil)
			return
		}

		writeJSON(t, w, map[string]interface{}{
			"Items": []map[string]interface{}{
				{
					"Id":             "lib-tv",
					"Name":           "TV Shows",
					"Type":           "CollectionFolder",
					"CollectionType": "tvshows",
				},
				{
					"Id":             "lib-movies",
					"Name":           "Movies",
					"Type":           "CollectionFolder",
					"CollectionType": "movies",
				},
			},
			"TotalRecordCount": 2,
		})
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/emby/", testAPIKey)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	libraries, err := client.Libraries(context.Background())
	if err != nil {
		t.Fatalf("Libraries returned error: %v", err)
	}
	if len(libraries) != 2 {
		t.Fatalf("Libraries returned %d libraries, want 2: %#v", len(libraries), libraries)
	}
	if libraries[0].ID != "lib-movies" || libraries[0].Name != "Movies" || libraries[0].Count != 12 {
		t.Fatalf("first library parsed incorrectly: %#v", libraries[0])
	}
	if libraries[1].ID != "lib-tv" || libraries[1].Name != "TV Shows" || libraries[1].Count != 34 {
		t.Fatalf("second library parsed incorrectly: %#v", libraries[1])
	}
}

func TestItemsPaginatesAndRequestsCompleteMetadataFields(t *testing.T) {
	var starts []int
	var fieldsSeen []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emby/Items" {
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Emby-Token") != testAPIKey {
			http.Error(w, "missing X-Emby-Token", http.StatusUnauthorized)
			return
		}
		if got := r.URL.Query().Get("ParentId"); got != "lib-movies" {
			http.Error(w, "wrong ParentId "+got, http.StatusBadRequest)
			return
		}
		if got := r.URL.Query().Get("Recursive"); got != "true" {
			http.Error(w, "wrong Recursive "+got, http.StatusBadRequest)
			return
		}
		if got := r.URL.Query().Get("Limit"); got != strconv.Itoa(DefaultLimit) {
			http.Error(w, "wrong Limit "+got, http.StatusBadRequest)
			return
		}
		if got := r.URL.Query().Get("IncludeItemTypes"); got != "Movie,Episode,Series,Season" {
			http.Error(w, "wrong IncludeItemTypes "+got, http.StatusBadRequest)
			return
		}

		fieldsSeen = append(fieldsSeen, r.URL.Query().Get("Fields"))
		start, err := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		if err != nil {
			http.Error(w, "missing StartIndex", http.StatusBadRequest)
			return
		}
		starts = append(starts, start)

		switch start {
		case 0:
			items := makeTestItems(start, DefaultLimit)
			items[0] = map[string]interface{}{
				"Id":              "item-0",
				"Name":            "First",
				"Type":            "Movie",
				"OriginalTitle":   "First Original",
				"Overview":        "overview",
				"OfficialRating":  "PG-13",
				"ProductionYear":  1999,
				"PremiereDate":    "1999-03-31T00:00:00.0000000Z",
				"CommunityRating": 8.7,
				"Genres":          []string{"Sci-Fi"},
				"Studios":         []map[string]string{{"Name": "Studio"}},
				"Tags":            []string{"tag"},
				"Taglines":        []string{"tagline"},
				"ProviderIds":     map[string]string{"Imdb": "tt001"},
				"People": []map[string]interface{}{
					{"Name": "Keanu Reeves", "Id": "old-person-1", "Type": "Actor", "Role": "Neo", "PrimaryImageTag": "avatar-tag"},
				},
			}
			writeItemsPage(t, w, 205, items)
		case 100:
			writeItemsPage(t, w, 205, makeTestItems(start, DefaultLimit))
		case 200:
			items := makeTestItems(start, 5)
			items[4] = map[string]interface{}{
				"Id":                "item-204",
				"Name":              "Last",
				"Type":              "Movie",
				"ImageTags":         map[string]string{"Primary": "tag-204"},
				"BackdropImageTags": []string{"bd-0"},
			}
			writeItemsPage(t, w, 205, items)
		default:
			http.Error(w, "unexpected StartIndex "+strconv.Itoa(start), http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/emby", testAPIKey)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	items, err := client.Items(context.Background(), "lib-movies")
	if err != nil {
		t.Fatalf("Items returned error: %v", err)
	}

	if len(items) != 205 {
		t.Fatalf("Items returned %d items, want 205", len(items))
	}
	if items[0].ID != "item-0" || items[204].ID != "item-204" {
		t.Fatalf("Items did not preserve first and last pages: first=%#v last=%#v", items[0], items[204])
	}
	if !reflect.DeepEqual(starts, []int{0, 100, 200}) {
		t.Fatalf("StartIndex sequence = %#v, want [0 100 200]", starts)
	}

	requiredFields := []string{
		"Path",
		"MediaSources",
		"MediaStreams",
		"Overview",
		"Genres",
		"Studios",
		"Tags",
		"Taglines",
		"ProviderIds",
		"OfficialRating",
		"ProductionYear",
		"PremiereDate",
		"CommunityRating",
		"People",
		"Chapters",
		"OriginalTitle",
		"ImageTags",
		"BackdropImageTags",
	}
	for _, field := range requiredFields {
		if !commaListContains(fieldsSeen[0], field) {
			t.Fatalf("Items Fields query missing %q in %q", field, fieldsSeen[0])
		}
	}
	if items[0].ProviderIDs["Imdb"] != "tt001" || items[0].OriginalTitle != "First Original" || items[0].ProductionYear != 1999 {
		t.Fatalf("core metadata fields were not parsed: %#v", items[0])
	}
	if len(items[0].People) != 1 || items[0].People[0].Name != "Keanu Reeves" || items[0].People[0].PrimaryImageTag != "avatar-tag" {
		t.Fatalf("people metadata and avatar tag were not parsed: %#v", items[0].People)
	}
	if items[204].ImageTags["Primary"] != "tag-204" || len(items[204].BackdropImageTags) != 1 {
		t.Fatalf("image metadata was not parsed: %#v", items[204])
	}
}

func TestItemUnmarshalToleratesNumericNestedIDs(t *testing.T) {
	payload := []byte(`{
		"Id": "item-1",
		"Name": "Mixed Types",
		"Type": "Movie",
		"Studios": [{"Name": "Studio A", "Id": 12345}],
		"People": [{"Name": "Actor A", "Id": 9007199254740993, "ProviderIds": {"Imdb": 24680}}],
		"ProviderIds": {"Tmdb": 9007199254740993}
	}`)

	var item Item
	if err := json.Unmarshal(payload, &item); err != nil {
		t.Fatalf("Item should tolerate numeric nested IDs: %v", err)
	}
	if item.Studios[0].Name != "Studio A" {
		t.Fatalf("studio was not parsed: %#v", item.Studios)
	}
	if item.People[0].ID != "9007199254740993" {
		t.Fatalf("person numeric ID was not converted to string: %#v", item.People[0].ID)
	}
	if item.ProviderIDs["Tmdb"] != "9007199254740993" {
		t.Fatalf("numeric ProviderIds should be preserved as strings: %#v", item.ProviderIDs)
	}
}

func TestFallbackImagesUsesConfiguredImageTypesAndBackdropIndexes(t *testing.T) {
	oldTypes := DefaultImageTypes
	DefaultImageTypes = []string{"Primary", "Logo", "Backdrop"}
	defer func() { DefaultImageTypes = oldTypes }()

	images := FallbackImages(Item{
		ID: "item-1",
		ImageTags: map[string]string{
			"Primary": "primary-tag",
			"Logo":    "logo-tag",
			"Thumb":   "thumb-tag",
		},
		BackdropImageTags: []string{"bd-0", "bd-1"},
	})

	got := imageSignatures(images)
	want := []string{
		"Primary:0:/Items/item-1/Images/Primary",
		"Logo:0:/Items/item-1/Images/Logo",
		"Backdrop:0:/Items/item-1/Images/Backdrop/0",
		"Backdrop:1:/Items/item-1/Images/Backdrop/1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FallbackImages returned %#v, want %#v", got, want)
	}
}

func TestPersonLookupAndAvatarDownloadUseNameLookupThenTargetPersonIDForUpload(t *testing.T) {
	var requested []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != testAPIKey {
			http.Error(w, "missing X-Emby-Token", http.StatusUnauthorized)
			return
		}
		requested = append(requested, r.URL.EscapedPath())
		switch r.URL.EscapedPath() {
		case "/emby/Persons/Keanu%20Reeves":
			if got := r.URL.Query().Get("Fields"); !strings.Contains(got, "ProviderIds") || !strings.Contains(got, "ImageTags") {
				http.Error(w, "person lookup missing import/avatar fields "+got, http.StatusBadRequest)
				return
			}
			writeJSON(t, w, map[string]interface{}{
				"Name":            "Keanu Reeves",
				"Id":              "target-person-id",
				"ProviderIds":     map[string]string{"Imdb": "nm0000206"},
				"PrimaryImageTag": "avatar-tag",
			})
		case "/emby/Persons/Keanu%20Reeves/Images/Primary":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png-bytes"))
		case "/emby/Items/target-person-id/Images/Primary":
			if r.Method != http.MethodPost {
				http.Error(w, "want POST", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected path "+r.URL.EscapedPath(), http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL+"/emby", testAPIKey)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	client.HTTPClient = server.Client()

	person, err := client.Person(context.Background(), "Keanu Reeves")
	if err != nil {
		t.Fatalf("Person returned error: %v", err)
	}
	if person.ProviderIDs["Imdb"] != "nm0000206" {
		t.Fatalf("person ProviderIds were not parsed: %#v", person)
	}

	data, ext, err := client.DownloadPersonImage(context.Background(), person.Name)
	if err != nil {
		t.Fatalf("DownloadPersonImage returned error: %v", err)
	}
	if string(data) != "png-bytes" || ext != ".png" {
		t.Fatalf("person avatar download = %q %q, want png-bytes .png", string(data), ext)
	}
	if err := client.UploadPersonImage(context.Background(), person.Name, []byte("new-avatar")); err != nil {
		t.Fatalf("UploadPersonImage returned error: %v", err)
	}

	if !reflect.DeepEqual(requested, []string{
		"/emby/Persons/Keanu%20Reeves",
		"/emby/Persons/Keanu%20Reeves/Images/Primary",
		"/emby/Persons/Keanu%20Reeves",
		"/emby/Items/target-person-id/Images/Primary",
	}) {
		t.Fatalf("person APIs should look up by name and upload by target person id, got requests %#v", requested)
	}
}

func assertNoSecret(t *testing.T, label string, value string) {
	t.Helper()
	if strings.Contains(value, testAPIKey) || strings.Contains(value, "SECRET") {
		t.Fatalf("%s leaked API key material: %q", label, value)
	}
}

func commaListContains(list string, want string) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.TrimSpace(part) == want {
			return true
		}
	}
	return false
}

func imageSignatures(images []ImageInfo) []string {
	out := make([]string, 0, len(images))
	for _, image := range images {
		out = append(out, image.ImageType+":"+strconv.Itoa(image.ImageIndex)+":"+image.DownloadPath)
	}
	return out
}

func writeItemsPage(t *testing.T, w http.ResponseWriter, total int, items []map[string]interface{}) {
	t.Helper()
	if items == nil {
		items = []map[string]interface{}{}
	}
	writeJSON(t, w, map[string]interface{}{
		"Items":            items,
		"TotalRecordCount": total,
	})
}

func makeTestItems(start int, count int) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, count)
	for i := 0; i < count; i++ {
		id := start + i
		items = append(items, map[string]interface{}{
			"Id":   "item-" + strconv.Itoa(id),
			"Name": "Item " + strconv.Itoa(id),
			"Type": "Movie",
		})
	}
	return items
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("failed to write JSON response: %v", err)
	}
}
