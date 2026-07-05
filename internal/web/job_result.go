package web

import (
	"time"

	"emby-migrator/internal/exporter"
	"emby-migrator/internal/storage"
)

const compactFailureExampleLimit = 20

type compactExportResult struct {
	Path     string          `json:"path"`
	Manifest compactManifest `json:"manifest"`
}

type compactImportResult struct {
	Path     string              `json:"path"`
	Report   compactImportReport `json:"report"`
	Manifest compactManifest     `json:"manifest"`
}

type compactManifest struct {
	ToolVersion   string                 `json:"toolVersion,omitempty"`
	EmbyVersion   string                 `json:"embyVersion,omitempty"`
	ExportedAt    time.Time              `json:"exportedAt,omitempty"`
	Libraries     []storage.LibraryEntry `json:"libraries,omitempty"`
	Errors        []storage.ErrorEntry   `json:"errors,omitempty"`
	Summary       storage.Summary        `json:"summary"`
	Source        string                 `json:"source,omitempty"`
	Incremental   *storage.Incremental   `json:"incremental,omitempty"`
	SchemaVersion int                    `json:"schemaVersion,omitempty"`
	Compatibility string                 `json:"compatibility,omitempty"`
}

type compactImportReport struct {
	StartedAt     time.Time                     `json:"startedAt,omitempty"`
	EndedAt       time.Time                     `json:"endedAt,omitempty"`
	DryRun        bool                          `json:"dryRun"`
	Target        exporter.ImportTarget         `json:"target,omitempty"`
	Compatibility exporter.CompatibilityProfile `json:"compatibility"`
	Diff          exporter.ImportDiff           `json:"diff,omitempty"`
	Incremental   *exporter.ImportIncremental   `json:"incremental,omitempty"`
	Skips         *exporter.ImportSkipReport    `json:"skips,omitempty"`
	Failures      exporter.FailureReport        `json:"failures,omitempty"`
	Summary       storage.Summary               `json:"summary"`
	WritesSkipped int                           `json:"writesSkipped,omitempty"`
}

func compactJobResult(result any) any {
	switch value := result.(type) {
	case exporter.ExportResult:
		return compactExportResult{
			Path:     value.Path,
			Manifest: compactStorageManifest(value.Manifest),
		}
	case *exporter.ExportResult:
		if value == nil {
			return nil
		}
		return compactExportResult{
			Path:     value.Path,
			Manifest: compactStorageManifest(value.Manifest),
		}
	case exporter.ImportResult:
		return compactImportResult{
			Path:     value.Path,
			Report:   compactExporterImportReport(value.Report),
			Manifest: compactStorageManifest(value.Manifest),
		}
	case *exporter.ImportResult:
		if value == nil {
			return nil
		}
		return compactImportResult{
			Path:     value.Path,
			Report:   compactExporterImportReport(value.Report),
			Manifest: compactStorageManifest(value.Manifest),
		}
	default:
		return result
	}
}

func compactStorageManifest(manifest storage.Manifest) compactManifest {
	return compactManifest{
		ToolVersion:   manifest.ToolVersion,
		EmbyVersion:   manifest.EmbyVersion,
		ExportedAt:    manifest.ExportedAt,
		Libraries:     append([]storage.LibraryEntry(nil), manifest.Libraries...),
		Errors:        compactManifestErrors(manifest.Errors),
		Summary:       manifest.Summary,
		Source:        manifest.Source,
		Incremental:   cloneStorageIncremental(manifest.Incremental),
		SchemaVersion: manifest.SchemaVersion,
		Compatibility: manifest.Compatibility,
	}
}

func compactExporterImportReport(report exporter.ImportReport) compactImportReport {
	return compactImportReport{
		StartedAt:     report.StartedAt,
		EndedAt:       report.EndedAt,
		DryRun:        report.DryRun,
		Target:        report.Target,
		Compatibility: report.Compatibility,
		Diff:          report.Diff,
		Incremental:   cloneImportIncremental(report.Incremental),
		Skips:         cloneImportSkips(report.Skips),
		Failures:      compactFailureReport(report.Failures),
		Summary:       report.Summary,
		WritesSkipped: report.WritesSkipped,
	}
}

func compactManifestErrors(errors []storage.ErrorEntry) []storage.ErrorEntry {
	if len(errors) == 0 {
		return nil
	}
	limit := compactFailureExampleLimit
	if len(errors) < limit {
		limit = len(errors)
	}
	return append([]storage.ErrorEntry(nil), errors[:limit]...)
}

func compactFailureReport(report exporter.FailureReport) exporter.FailureReport {
	report.All = nil
	report.Unmatched = compactFailureExamples(report.Unmatched)
	report.Ambiguous = compactFailureExamples(report.Ambiguous)
	report.Failed = compactFailureExamples(report.Failed)
	report.ImageFailed = compactFailureExamples(report.ImageFailed)
	report.PersonImageFailed = compactFailureExamples(report.PersonImageFailed)
	return report
}

func compactFailureExamples(values []exporter.FailureExample) []exporter.FailureExample {
	if len(values) == 0 {
		return nil
	}
	limit := compactFailureExampleLimit
	if len(values) < limit {
		limit = len(values)
	}
	out := make([]exporter.FailureExample, limit)
	copy(out, values[:limit])
	return out
}

func cloneStorageIncremental(value *storage.Incremental) *storage.Incremental {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneImportIncremental(value *exporter.ImportIncremental) *exporter.ImportIncremental {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneImportSkips(value *exporter.ImportSkipReport) *exporter.ImportSkipReport {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
