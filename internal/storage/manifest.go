package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"emby-migrator/internal/emby"
)

type ItemIdentity struct {
	EmbyID         string
	Type           string
	Name           string
	OriginalTitle  string
	ProductionYear int
	ProviderIDs    map[string]string
	SeriesName     string
	SeasonNumber   int
	EpisodeNumber  int
	Path           string
}

type ManifestInput struct {
	ToolVersion   string
	ServerVersion string
	ExportedAt    time.Time
	APIKey        string
	Libraries     []ManifestLibrary
	Items         []ManifestItem
	People        []ManifestPerson
	Errors        []ManifestError
}

type ManifestLibrary struct {
	ID   string
	Name string
	Slug string
}

type ManifestItem struct {
	StableKey   string
	EmbyID      string
	Type        string
	Name        string
	Path        string
	ProviderIDs map[string]string
	Images      []ManifestImage
}

type ManifestPerson struct {
	Name  string
	Image *ManifestImage
}

type ManifestImage struct {
	Type   string
	File   string
	Size   int64
	SHA256 string
}

type ManifestError struct {
	ItemID  string
	Message string
}

type Manifest struct {
	ToolVersion   string         `json:"toolVersion"`
	EmbyVersion   string         `json:"embyVersion,omitempty"`
	ExportedAt    time.Time      `json:"exportedAt"`
	Libraries     []LibraryEntry `json:"libraries"`
	Items         []ItemEntry    `json:"items"`
	People        []PersonEntry  `json:"people"`
	Errors        []ErrorEntry   `json:"errors,omitempty"`
	Summary       Summary        `json:"summary"`
	Source        string         `json:"source,omitempty"`
	Incremental   *Incremental   `json:"incremental,omitempty"`
	SchemaVersion int            `json:"schemaVersion"`
	Compatibility string         `json:"compatibility"`
}

type Incremental struct {
	Enabled            bool      `json:"enabled"`
	BaselineExportName string    `json:"baselineExportName,omitempty"`
	BaselineExportPath string    `json:"baselineExportPath,omitempty"`
	SkippedItems       int       `json:"skippedItems,omitempty"`
	ChangedItems       int       `json:"changedItems,omitempty"`
	CreatedAt          time.Time `json:"createdAt,omitempty"`
}

type LibraryEntry struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Count int    `json:"count"`
}

type ItemEntry struct {
	StableKey         string            `json:"stableKey"`
	Slug              string            `json:"slug"`
	LibraryID         string            `json:"libraryId"`
	LibraryName       string            `json:"libraryName"`
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	Path              string            `json:"path,omitempty"`
	OriginalTitle     string            `json:"originalTitle,omitempty"`
	ProductionYear    int               `json:"productionYear,omitempty"`
	SeriesName        string            `json:"seriesName,omitempty"`
	IndexNumber       int               `json:"indexNumber,omitempty"`
	ParentIndexNumber int               `json:"parentIndexNumber,omitempty"`
	ProviderIDs       map[string]string `json:"providerIds,omitempty"`
	Fingerprint       string            `json:"fingerprint,omitempty"`
	Skipped           bool              `json:"skipped,omitempty"`
	SkipReason        string            `json:"skipReason,omitempty"`
	InfoPath          string            `json:"infoPath"`
	RawPath           string            `json:"rawPath"`
	Images            []FileEntry       `json:"images,omitempty"`
	People            []string          `json:"people,omitempty"`
}

type PersonEntry struct {
	StableKey    string            `json:"stableKey"`
	Name         string            `json:"name"`
	Type         string            `json:"type,omitempty"`
	Role         string            `json:"role,omitempty"`
	ProviderIDs  map[string]string `json:"providerIds,omitempty"`
	Image        *FileEntry        `json:"image,omitempty"`
	ReferencedBy []string          `json:"referencedBy,omitempty"`
	RawPath      string            `json:"rawPath,omitempty"`
}

type FileEntry struct {
	Type   string `json:"type"`
	Index  int    `json:"index,omitempty"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type ErrorEntry struct {
	Scope   string `json:"scope"`
	Name    string `json:"name,omitempty"`
	ID      string `json:"id,omitempty"`
	Message string `json:"message"`
}

type Summary struct {
	Libraries          int `json:"libraries"`
	Items              int `json:"items"`
	People             int `json:"people"`
	ItemImages         int `json:"itemImages"`
	PeopleImages       int `json:"peopleImages"`
	Errors             int `json:"errors"`
	SkippedItems       int `json:"skippedItems,omitempty"`
	Matched            int `json:"matched,omitempty"`
	Unmatched          int `json:"unmatched,omitempty"`
	Ambiguous          int `json:"ambiguous,omitempty"`
	MetadataUpdated    int `json:"metadataUpdated,omitempty"`
	ItemImagesPushed   int `json:"itemImagesPushed,omitempty"`
	ItemImagesFailed   int `json:"itemImagesFailed,omitempty"`
	PeopleImagesFailed int `json:"peopleImagesFailed,omitempty"`
}

type ItemInfo struct {
	Item       emby.Item     `json:"item"`
	StableKey  string        `json:"stableKey"`
	ExportedAt time.Time     `json:"exportedAt"`
	Images     []FileEntry   `json:"images,omitempty"`
	People     []emby.Person `json:"people,omitempty"`
}

func StableItemKey(value any) string {
	switch item := value.(type) {
	case emby.Item:
		return stableEmbyItemKey(item)
	case ItemIdentity:
		return stableIdentityKey(item)
	default:
		return "unknown"
	}
}

func stableEmbyItemKey(item emby.Item) string {
	if len(item.ProviderIDs) > 0 {
		keys := make([]string, 0, len(item.ProviderIDs))
		for k := range item.ProviderIDs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if item.ProviderIDs[k] != "" {
				return "provider-" + Slug(k+"-"+item.ProviderIDs[k])
			}
		}
	}
	if item.Type == "Episode" && item.SeriesName != "" {
		return Slug("episode-" + item.SeriesName + "-" + intString(item.ParentIndexNumber) + "-" + intString(item.IndexNumber))
	}
	if item.Type == "Season" && item.Path != "" {
		return Slug(seasonPathIdentity(item.Path) + "-" + item.Type)
	}
	if item.Path != "" {
		return Slug(strings.TrimSuffix(filepath.Base(item.Path), filepath.Ext(item.Path)) + "-" + item.Type)
	}
	return Slug(item.Name + "-" + intString(item.ProductionYear) + "-" + item.Type)
}

func stableIdentityKey(item ItemIdentity) string {
	if len(item.ProviderIDs) > 0 {
		keys := make([]string, 0, len(item.ProviderIDs))
		for k := range item.ProviderIDs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if item.ProviderIDs[k] != "" {
				return "provider-" + Slug(k+"-"+item.ProviderIDs[k])
			}
		}
	}
	if item.Type == "Episode" && item.SeriesName != "" {
		return Slug("episode-" + item.SeriesName + "-" + intString(item.SeasonNumber) + "-" + intString(item.EpisodeNumber))
	}
	if item.Type == "Season" && item.Path != "" {
		return Slug(seasonPathIdentity(item.Path) + "-" + item.Type)
	}
	if item.Path != "" {
		return Slug(strings.TrimSuffix(filepath.Base(item.Path), filepath.Ext(item.Path)) + "-" + item.Type)
	}
	name := item.Name
	if name == "" {
		name = item.OriginalTitle
	}
	return Slug(item.Type + "-" + name + "-" + intString(item.ProductionYear))
}

func StablePersonKey(person emby.Person) string {
	if len(person.ProviderIDs) > 0 {
		keys := make([]string, 0, len(person.ProviderIDs))
		for k := range person.ProviderIDs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if person.ProviderIDs[k] != "" {
				return "person-provider-" + Slug(k+"-"+person.ProviderIDs[k])
			}
		}
	}
	return "person-" + Slug(person.Name)
}

func seasonPathIdentity(value string) string {
	normalized := strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
	if normalized == "" {
		return "season"
	}
	parts := strings.Split(normalized, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "-" + parts[len(parts)-1]
	}
	return parts[len(parts)-1]
}

func Slug(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unknown"
	}
	runes := []rune(out)
	if len(runes) > 120 {
		out = string(runes[:120])
		out = strings.Trim(out, "-")
	}
	return out
}

func SafeSlug(value string) string {
	return Slug(value)
}

func SafeName(value string) string {
	name := Slug(value)
	if isWindowsReservedName(name) {
		return "safe-" + name
	}
	return name
}

func isWindowsReservedName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "con", "prn", "aux", "nul",
		"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
		"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		return true
	default:
		return false
	}
}

func UniqueSlug(base string, used any) string {
	base = SafeName(base)
	switch m := used.(type) {
	case map[string]int:
		count := m[base]
		m[base] = count + 1
		if count == 0 {
			return base
		}
		return base + "-" + intString(count+1)
	case map[string]bool:
		if !m[base] {
			m[base] = true
			return base
		}
		for i := 2; ; i++ {
			candidate := base + "-" + intString(i)
			if !m[candidate] {
				m[candidate] = true
				return candidate
			}
		}
	default:
		return base
	}
}

func BuildManifest(input ManifestInput) (Manifest, error) {
	manifest := Manifest{
		ToolVersion:   input.ToolVersion,
		EmbyVersion:   input.ServerVersion,
		ExportedAt:    input.ExportedAt,
		SchemaVersion: 1,
		Compatibility: "emby-4.8.11-first",
	}
	for _, lib := range input.Libraries {
		manifest.Libraries = append(manifest.Libraries, LibraryEntry{ID: lib.ID, Name: lib.Name, Slug: lib.Slug})
	}
	for _, item := range input.Items {
		entry := ItemEntry{
			StableKey:   item.StableKey,
			ID:          item.EmbyID,
			Type:        item.Type,
			Name:        item.Name,
			Path:        item.Path,
			ProviderIDs: item.ProviderIDs,
		}
		for _, image := range item.Images {
			entry.Images = append(entry.Images, FileEntry{Type: image.Type, Path: image.File, Size: image.Size, SHA256: image.SHA256})
		}
		manifest.Items = append(manifest.Items, entry)
	}
	for _, person := range input.People {
		entry := PersonEntry{Name: person.Name, StableKey: "person-" + Slug(person.Name)}
		if person.Image != nil {
			entry.Image = &FileEntry{Type: person.Image.Type, Path: person.Image.File, Size: person.Image.Size, SHA256: person.Image.SHA256}
		}
		manifest.People = append(manifest.People, entry)
	}
	for _, errEntry := range input.Errors {
		manifest.Errors = append(manifest.Errors, ErrorEntry{ID: errEntry.ItemID, Message: errEntry.Message})
	}
	manifest.Summary.Libraries = len(manifest.Libraries)
	manifest.Summary.Items = len(manifest.Items)
	manifest.Summary.People = len(manifest.People)
	manifest.Summary.Errors = len(manifest.Errors)
	return manifest, nil
}

func uniqueSlugInt(base string, used map[string]int) string {
	base = SafeName(base)
	count := used[base]
	used[base] = count + 1
	if count == 0 {
		return base
	}
	return base + "-" + intString(count+1)
}

func WriteJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func ReadJSON(path string, out any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func WriteBytes(path string, data []byte) (FileEntry, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return FileEntry{}, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return FileEntry{}, err
	}
	sum := sha256.Sum256(data)
	return FileEntry{
		Path:   filepath.ToSlash(path),
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func intString(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	n := v
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
