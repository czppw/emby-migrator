package web

import (
	"encoding/json"
	"strings"
	"testing"

	"emby-migrator/internal/exporter"
	"emby-migrator/internal/storage"
)

func TestCompactJobResultDropsHeavyExportManifestSections(t *testing.T) {
	result := compactJobResult(exporter.ExportResult{
		Path: "/data/exports/pkg",
		Manifest: storage.Manifest{
			Source:      "http://source.example:8096",
			ToolVersion: "test",
			Items: []storage.ItemEntry{
				{StableKey: "item-1", Name: "Item 1"},
			},
			People: []storage.PersonEntry{
				{StableKey: "person-1", Name: "Person 1"},
			},
			Summary: storage.Summary{Items: 1, People: 1},
		},
	})

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	manifest, ok := decoded["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing: %s", text)
	}
	if _, ok := manifest["items"]; ok {
		t.Fatalf("compact export manifest still contains full items: %s", text)
	}
	if _, ok := manifest["people"]; ok {
		t.Fatalf("compact export manifest still contains full people: %s", text)
	}
	if !strings.Contains(text, `"summary"`) || !strings.Contains(text, `"source"`) || !strings.Contains(text, `"path"`) {
		t.Fatalf("compact export result lost required summary fields: %s", text)
	}
}

func TestCompactJobResultDropsHeavyImportReportSections(t *testing.T) {
	matches := make([]exporter.ImportMatch, 50)
	failures := make([]exporter.FailureExample, 50)
	for i := range matches {
		matches[i] = exporter.ImportMatch{StableKey: "item", SourceName: "Item", Status: "updated"}
		failures[i] = exporter.FailureExample{StableKey: "item", SourceName: "Item", Status: "failed"}
	}
	result := compactJobResult(exporter.ImportResult{
		Path: "/data/exports/pkg",
		Manifest: storage.Manifest{
			Items:   []storage.ItemEntry{{StableKey: "item-1", Name: "Item 1"}},
			People:  []storage.PersonEntry{{StableKey: "person-1", Name: "Person 1"}},
			Summary: storage.Summary{Items: 1, People: 1},
			Source:  "http://source.example:8096",
		},
		Report: exporter.ImportReport{
			Target: exporter.ImportTarget{BaseURL: "http://target.example:8096"},
			Failures: exporter.FailureReport{
				All:       failures,
				Failed:    failures,
				Counts:    &exporter.FailureCounts{Failed: len(failures)},
				Total:     len(failures),
				Truncated: true,
			},
			Matches:       matches,
			PersonMatches: matches,
			Summary:       storage.Summary{Matched: 1, MetadataUpdated: 1},
		},
	})

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	report, ok := decoded["report"].(map[string]any)
	if !ok {
		t.Fatalf("report missing: %s", text)
	}
	manifest, ok := decoded["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("manifest missing: %s", text)
	}
	for _, forbidden := range []string{"matches", "personMatches"} {
		if _, ok := report[forbidden]; ok {
			t.Fatalf("compact import report still contains %s: %s", forbidden, text)
		}
	}
	for _, forbidden := range []string{"items", "people"} {
		if _, ok := manifest[forbidden]; ok {
			t.Fatalf("compact import manifest still contains %s: %s", forbidden, text)
		}
	}
	for _, forbidden := range []string{`"all"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("compact import result still contains %s: %s", forbidden, text)
		}
	}
	if strings.Count(text, `"stableKey"`) > compactFailureExampleLimit {
		t.Fatalf("compact import result retained too many failure examples: %s", text)
	}
	if !strings.Contains(text, `"summary"`) || !strings.Contains(text, `"target"`) || !strings.Contains(text, `"manifest"`) {
		t.Fatalf("compact import result lost required summary fields: %s", text)
	}
}
