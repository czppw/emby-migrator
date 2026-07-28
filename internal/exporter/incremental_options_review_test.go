package exporter

import (
	"path/filepath"
	"reflect"
	"testing"

	"emby-migrator/internal/emby"
	"emby-migrator/internal/storage"
)

func TestNormalizedExportOptions(t *testing.T) {
	t.Run("sorts deduplicates and normalizes image types", func(t *testing.T) {
		got := normalizedExportOptions(ExportRequest{
			ImageTypes:          []string{" Logo ", "primary", "PRIMARY", "", " backdrop "},
			IncludePeopleImages: true,
			IncludeMediaInfo:    true,
		})
		want := storage.ExportOptions{
			ImageTypes:          []string{"backdrop", "logo", "primary"},
			IncludePeopleImages: true,
			IncludeMediaInfo:    true,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("normalizedExportOptions() = %#v, want %#v", got, want)
		}
	})

	t.Run("uses all default image types", func(t *testing.T) {
		got := normalizedExportOptions(ExportRequest{})
		want := []string{
			"art",
			"backdrop",
			"banner",
			"box",
			"disc",
			"logo",
			"primary",
			"screenshot",
			"thumb",
		}
		if !reflect.DeepEqual(got.ImageTypes, want) {
			t.Fatalf("default image types = %#v, want %#v (Emby defaults: %#v)", got.ImageTypes, want, emby.DefaultImageTypes)
		}
	})

	t.Run("skip images removes irrelevant image options", func(t *testing.T) {
		got := normalizedExportOptions(ExportRequest{
			SkipImages:          true,
			ImageTypes:          []string{"Primary", "Logo"},
			IncludePeopleImages: true,
			IncludeMediaInfo:    true,
		})
		want := storage.ExportOptions{
			SkipImages:       true,
			IncludeMediaInfo: true,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("normalized skip-images options = %#v, want %#v", got, want)
		}
	})
}

func TestLatestBaselineManifestRequiresMatchingExportOptions(t *testing.T) {
	baseRequest := ExportRequest{
		ImageTypes:          []string{"Primary", "Backdrop"},
		IncludePeopleImages: true,
		IncludeMediaInfo:    true,
	}
	baseOptions := normalizedExportOptions(baseRequest)

	tests := []struct {
		name            string
		manifestOptions *storage.ExportOptions
		request         ExportRequest
		wantBaseline    bool
	}{
		{
			name:            "identical normalized options reuse baseline",
			manifestOptions: &baseOptions,
			request: ExportRequest{
				ImageTypes:          []string{" backdrop ", "PRIMARY", "primary"},
				IncludePeopleImages: true,
				IncludeMediaInfo:    true,
			},
			wantBaseline: true,
		},
		{
			name:            "different skip images does not reuse baseline",
			manifestOptions: &baseOptions,
			request: ExportRequest{
				SkipImages:       true,
				IncludeMediaInfo: true,
			},
		},
		{
			name:            "different image types do not reuse baseline",
			manifestOptions: &baseOptions,
			request: ExportRequest{
				ImageTypes:          []string{"Primary"},
				IncludePeopleImages: true,
				IncludeMediaInfo:    true,
			},
		},
		{
			name:            "different people image option does not reuse baseline",
			manifestOptions: &baseOptions,
			request: ExportRequest{
				ImageTypes:       []string{"Primary", "Backdrop"},
				IncludeMediaInfo: true,
			},
		},
		{
			name:            "different media info option does not reuse baseline",
			manifestOptions: &baseOptions,
			request: ExportRequest{
				ImageTypes:          []string{"Primary", "Backdrop"},
				IncludePeopleImages: true,
			},
		},
		{
			name:            "legacy manifest without options does not reuse baseline",
			manifestOptions: nil,
			request:         baseRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewService(t.TempDir())
			const (
				source       = "http://emby.example:8096"
				baselineName = "20260728-120000-server-Movies"
			)
			manifest := storage.Manifest{
				Source:        source,
				ExportOptions: tt.manifestOptions,
				Libraries: []storage.LibraryEntry{
					{ID: "old-library-id", Name: "Movies"},
				},
			}
			manifestPath := filepath.Join(service.ExportsDir(), baselineName, "manifest.json")
			if err := storage.WriteJSON(manifestPath, manifest); err != nil {
				t.Fatalf("write baseline manifest: %v", err)
			}

			gotManifest, gotName, gotPath, ok := service.latestBaselineManifest(
				source+"/",
				[]emby.Library{{ID: "new-library-id", Name: "Movies"}},
				normalizedExportOptions(tt.request),
				"",
			)
			if ok != tt.wantBaseline {
				t.Fatalf("latestBaselineManifest() ok = %v, want %v", ok, tt.wantBaseline)
			}
			if !tt.wantBaseline {
				if gotName != "" || gotPath != "" {
					t.Fatalf("rejected baseline returned name %q and path %q", gotName, gotPath)
				}
				return
			}
			if gotName != baselineName {
				t.Fatalf("baseline name = %q, want %q", gotName, baselineName)
			}
			if gotPath != filepath.Dir(manifestPath) {
				t.Fatalf("baseline path = %q, want %q", gotPath, filepath.Dir(manifestPath))
			}
			if gotManifest.Source != source {
				t.Fatalf("baseline manifest source = %q, want %q", gotManifest.Source, source)
			}
		})
	}
}
