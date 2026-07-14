package emby

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	DefaultLimit = 100
)

var DefaultImageTypes = []string{
	"Primary",
	"Backdrop",
	"Logo",
	"Thumb",
	"Banner",
	"Art",
	"Disc",
	"Box",
	"Screenshot",
}

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type ClientConfig struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

type Connection struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
}

type SystemInfo struct {
	ServerName string `json:"ServerName"`
	Version    string `json:"Version"`
	ID         string `json:"Id"`
}

type ItemsResponse struct {
	Items            []Item `json:"Items"`
	TotalRecordCount int    `json:"TotalRecordCount"`
}

type PersonsResponse struct {
	Items            []Person `json:"Items"`
	TotalRecordCount int      `json:"TotalRecordCount"`
}

type Library struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	CollectionType string `json:"collectionType,omitempty"`
	Count          int    `json:"count"`
	ItemCount      int    `json:"itemCount"`
}

type Person struct {
	Name            string            `json:"Name"`
	ID              FlexibleString    `json:"Id,omitempty"`
	Type            string            `json:"Type,omitempty"`
	Role            string            `json:"Role,omitempty"`
	PrimaryImageTag string            `json:"PrimaryImageTag,omitempty"`
	ProviderIDs     map[string]string `json:"ProviderIds,omitempty"`
	Raw             map[string]any    `json:"Raw,omitempty"`
}

type Item struct {
	ID                string            `json:"Id"`
	Name              string            `json:"Name"`
	Type              string            `json:"Type"`
	Path              string            `json:"Path,omitempty"`
	OriginalTitle     string            `json:"OriginalTitle,omitempty"`
	Overview          string            `json:"Overview,omitempty"`
	OfficialRating    string            `json:"OfficialRating,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	PremiereDate      string            `json:"PremiereDate,omitempty"`
	CommunityRating   float64           `json:"CommunityRating,omitempty"`
	Genres            []string          `json:"Genres,omitempty"`
	Studios           []NameID          `json:"Studios,omitempty"`
	Tags              []string          `json:"Tags,omitempty"`
	Taglines          []string          `json:"Taglines,omitempty"`
	ProviderIDs       map[string]string `json:"ProviderIds,omitempty"`
	People            []Person          `json:"People,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	SeasonName        string            `json:"SeasonName,omitempty"`
	IndexNumber       int               `json:"IndexNumber,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"`
	ImageTags         map[string]string `json:"ImageTags,omitempty"`
	BackdropImageTags []string          `json:"BackdropImageTags,omitempty"`
	CollectionType    string            `json:"CollectionType,omitempty"`
	ChildCount        int               `json:"ChildCount,omitempty"`
	MediaSources      []map[string]any  `json:"MediaSources,omitempty"`
	MediaStreams      []map[string]any  `json:"MediaStreams,omitempty"`
	Chapters          []map[string]any  `json:"Chapters,omitempty"`
	Raw               map[string]any    `json:"Raw,omitempty"`
}

type NameID struct {
	Name string `json:"Name"`
	ID   any    `json:"Id,omitempty"`
}

type FlexibleString string

func (s *FlexibleString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*s = FlexibleString(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err == nil {
		*s = FlexibleString(number.String())
		return nil
	}
	return nil
}

type ImageInfo struct {
	ImageType    string `json:"ImageType"`
	ImageIndex   int    `json:"ImageIndex,omitempty"`
	Tag          string `json:"ImageTag,omitempty"`
	Width        int    `json:"Width,omitempty"`
	Height       int    `json:"Height,omitempty"`
	Size         int64  `json:"Size,omitempty"`
	IsFallback   bool   `json:"IsFallback,omitempty"`
	DisplayName  string `json:"DisplayName,omitempty"`
	DownloadPath string `json:"-"`
	FileName     string `json:"-"`
}

func (i *Item) UnmarshalJSON(data []byte) error {
	raw, err := objectFromJSON(data)
	if err != nil {
		return err
	}
	*i = Item{
		ID:                stringFromAny(raw["Id"]),
		Name:              stringFromAny(raw["Name"]),
		Type:              stringFromAny(raw["Type"]),
		Path:              stringFromAny(raw["Path"]),
		OriginalTitle:     stringFromAny(raw["OriginalTitle"]),
		Overview:          stringFromAny(raw["Overview"]),
		OfficialRating:    stringFromAny(raw["OfficialRating"]),
		ProductionYear:    intFromAny(raw["ProductionYear"]),
		PremiereDate:      stringFromAny(raw["PremiereDate"]),
		CommunityRating:   floatFromAny(raw["CommunityRating"]),
		Genres:            stringSliceFromAny(raw["Genres"]),
		Studios:           nameIDsFromAny(raw["Studios"]),
		Tags:              stringSliceFromAny(raw["Tags"]),
		Taglines:          stringSliceFromAny(raw["Taglines"]),
		ProviderIDs:       stringMapFromAny(raw["ProviderIds"]),
		People:            peopleFromAny(raw["People"]),
		SeriesName:        stringFromAny(raw["SeriesName"]),
		SeasonName:        stringFromAny(raw["SeasonName"]),
		IndexNumber:       intFromAny(raw["IndexNumber"]),
		ParentIndexNumber: intFromAny(raw["ParentIndexNumber"]),
		ImageTags:         stringMapFromAny(raw["ImageTags"]),
		BackdropImageTags: stringSliceFromAny(raw["BackdropImageTags"]),
		CollectionType:    stringFromAny(raw["CollectionType"]),
		ChildCount:        intFromAny(raw["ChildCount"]),
		MediaSources:      objectSliceFromAny(raw["MediaSources"]),
		MediaStreams:      objectSliceFromAny(raw["MediaStreams"]),
		Chapters:          objectSliceFromAny(raw["Chapters"]),
		Raw:               raw,
	}
	return nil
}

func (p *Person) UnmarshalJSON(data []byte) error {
	raw, err := objectFromJSON(data)
	if err != nil {
		return err
	}
	*p = Person{
		Name:            stringFromAny(raw["Name"]),
		ID:              FlexibleString(stringFromAny(raw["Id"])),
		Type:            stringFromAny(raw["Type"]),
		Role:            stringFromAny(raw["Role"]),
		PrimaryImageTag: stringFromAny(raw["PrimaryImageTag"]),
		ProviderIDs:     stringMapFromAny(raw["ProviderIds"]),
		Raw:             raw,
	}
	return nil
}

func objectFromJSON(data []byte) (map[string]any, error) {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		var out int
		fmt.Sscanf(v, "%d", &out)
		return out
	default:
		return 0
	}
}

func floatFromAny(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		var out float64
		fmt.Sscanf(v, "%f", &out)
		return out
	default:
		return 0
	}
}

func stringSliceFromAny(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text := stringFromAny(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func stringMapFromAny(value any) map[string]string {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for key, item := range raw {
		if text := stringFromAny(item); text != "" {
			out[key] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func nameIDsFromAny(value any) []NameID {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]NameID, 0, len(list))
	for _, item := range list {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, NameID{Name: stringFromAny(raw["Name"]), ID: raw["Id"]})
	}
	return out
}

func peopleFromAny(value any) []Person {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]Person, 0, len(list))
	for _, item := range list {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Person{
			Name:            stringFromAny(raw["Name"]),
			ID:              FlexibleString(stringFromAny(raw["Id"])),
			Type:            stringFromAny(raw["Type"]),
			Role:            stringFromAny(raw["Role"]),
			PrimaryImageTag: stringFromAny(raw["PrimaryImageTag"]),
			ProviderIDs:     stringMapFromAny(raw["ProviderIds"]),
			Raw:             raw,
		})
	}
	return out
}

func objectSliceFromAny(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		raw, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, raw)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type ItemsQuery struct {
	ParentID     string
	PageSize     int
	Recursive    bool
	Fields       string
	IncludeTypes string
}

func NewClient(args ...any) (*Client, error) {
	var cfg ClientConfig
	switch len(args) {
	case 1:
		value, ok := args[0].(ClientConfig)
		if !ok {
			return nil, fmt.Errorf("NewClient expects ClientConfig or baseURL, apiKey")
		}
		cfg = value
	case 2:
		baseURL, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("NewClient baseURL must be string")
		}
		apiKey, ok := args[1].(string)
		if !ok {
			return nil, fmt.Errorf("NewClient apiKey must be string")
		}
		cfg = ClientConfig{BaseURL: baseURL, APIKey: apiKey}
	default:
		return nil, fmt.Errorf("NewClient expects ClientConfig or baseURL, apiKey")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	normalized, err := NormalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:    normalized,
		APIKey:     cfg.APIKey,
		HTTPClient: cfg.HTTPClient,
	}, nil
}

func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("emby url is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func MaskAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + strings.Repeat("*", len(key)-8) + key[len(key)-4:]
}

func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := u.Query()
	for _, key := range []string{"api_key", "X-Emby-Token", "x-emby-token"} {
		if _, ok := query[key]; ok {
			query.Set(key, "***")
		}
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func (c *Client) BuildURL(endpoint string, params url.Values) string {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path.Clean("/"+endpoint), "/")
	if params != nil {
		u.RawQuery = params.Encode()
	}
	return u.String()
}

func (c *Client) SystemInfo(ctx context.Context) (SystemInfo, error) {
	var info SystemInfo
	err := c.JSON(ctx, http.MethodGet, "/System/Info", nil, nil, &info)
	return info, err
}

func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	var result ItemsResponse
	if err := c.JSON(ctx, http.MethodGet, "/Items", url.Values{"Limit": {"100"}}, nil, &result); err != nil {
		return nil, err
	}
	libraries := make([]Library, 0)
	for _, item := range result.Items {
		if item.Type != "CollectionFolder" && item.Type != "Folder" {
			continue
		}
		count := item.ChildCount
		if count == 0 {
			var countResult ItemsResponse
			err := c.JSON(ctx, http.MethodGet, "/Items", url.Values{
				"ParentId": {item.ID},
				"Limit":    {"0"},
			}, nil, &countResult)
			if err == nil {
				count = countResult.TotalRecordCount
			}
		}
		libraries = append(libraries, Library{
			ID:             item.ID,
			Name:           item.Name,
			Type:           item.Type,
			CollectionType: item.CollectionType,
			Count:          count,
			ItemCount:      count,
		})
	}
	sort.Slice(libraries, func(i, j int) bool {
		return libraries[i].Name < libraries[j].Name
	})
	return libraries, nil
}

func (c *Client) ListLibraries(ctx context.Context) ([]Library, error) {
	return c.Libraries(ctx)
}

func (c *Client) Items(ctx context.Context, libraryID string) ([]Item, error) {
	fields := strings.Join([]string{
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
		"SeriesName",
		"SeasonName",
		"IndexNumber",
		"ParentIndexNumber",
		"ImageTags",
		"BackdropImageTags",
	}, ",")
	items := make([]Item, 0)
	start := 0
	for {
		var result ItemsResponse
		err := c.JSON(ctx, http.MethodGet, "/Items", url.Values{
			"ParentId":         {libraryID},
			"StartIndex":       {fmt.Sprintf("%d", start)},
			"Limit":            {fmt.Sprintf("%d", DefaultLimit)},
			"Recursive":        {"true"},
			"Fields":           {fields},
			"IncludeItemTypes": {"Movie,Episode,Series,Season"},
		}, nil, &result)
		if err != nil {
			return items, err
		}
		items = append(items, result.Items...)
		if len(items) >= result.TotalRecordCount || len(result.Items) == 0 {
			break
		}
		start += DefaultLimit
	}
	return items, nil
}

func (c *Client) ListItemsPaged(ctx context.Context, query ItemsQuery) ([]Item, error) {
	if query.PageSize <= 0 {
		query.PageSize = DefaultLimit
	}
	if query.Fields == "" {
		query.Fields = "Path,ProviderIds,People,OriginalTitle,ImageTags,BackdropImageTags"
	}
	items := make([]Item, 0)
	start := 0
	for {
		params := url.Values{
			"ParentId":   {query.ParentID},
			"StartIndex": {fmt.Sprintf("%d", start)},
			"Limit":      {fmt.Sprintf("%d", query.PageSize)},
			"Fields":     {query.Fields},
		}
		if query.Recursive {
			params.Set("Recursive", "true")
		}
		if query.IncludeTypes != "" {
			params.Set("IncludeItemTypes", query.IncludeTypes)
		}
		var result ItemsResponse
		if err := c.JSON(ctx, http.MethodGet, "/Items", params, nil, &result); err != nil {
			return items, err
		}
		items = append(items, result.Items...)
		if len(items) >= result.TotalRecordCount || len(result.Items) == 0 {
			break
		}
		start += query.PageSize
	}
	return items, nil
}

func (c *Client) SearchItems(ctx context.Context, searchTerm, includeTypes string, limit int) ([]Item, error) {
	return c.SearchItemsInLibraries(ctx, searchTerm, includeTypes, limit, nil)
}

func (c *Client) SearchItemsInLibraries(ctx context.Context, searchTerm, includeTypes string, limit int, libraryIDs []string) ([]Item, error) {
	if limit <= 0 {
		limit = 20
	}
	parentIDs := normalizeLibraryIDs(libraryIDs)
	if len(parentIDs) > 1 {
		items := make([]Item, 0)
		for _, parentID := range parentIDs {
			scoped, err := c.SearchItemsInLibraries(ctx, searchTerm, includeTypes, limit, []string{parentID})
			if err != nil {
				return items, err
			}
			items = append(items, scoped...)
		}
		return items, nil
	}
	params := url.Values{
		"Recursive":  {"true"},
		"Fields":     {"Path,ProviderIds,People,OriginalTitle,IndexNumber,ParentIndexNumber,SeriesName,ImageTags"},
		"Limit":      {fmt.Sprintf("%d", limit)},
		"SearchTerm": {searchTerm},
	}
	if len(parentIDs) == 1 {
		params.Set("ParentId", parentIDs[0])
	}
	if includeTypes != "" {
		params.Set("IncludeItemTypes", includeTypes)
	}
	var result ItemsResponse
	if err := c.JSON(ctx, http.MethodGet, "/Items", params, nil, &result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (c *Client) ItemsByProviderID(ctx context.Context, providerID string) ([]Item, error) {
	return c.ItemsByProviderIDInLibraries(ctx, providerID, nil)
}

func (c *Client) ItemsByProviderIDInLibraries(ctx context.Context, providerID string, libraryIDs []string) ([]Item, error) {
	parentIDs := normalizeLibraryIDs(libraryIDs)
	if len(parentIDs) > 1 {
		items := make([]Item, 0)
		for _, parentID := range parentIDs {
			scoped, err := c.ItemsByProviderIDInLibraries(ctx, providerID, []string{parentID})
			if err != nil {
				return items, err
			}
			items = append(items, scoped...)
		}
		return items, nil
	}
	params := url.Values{
		"Recursive":           {"true"},
		"Limit":               {"10"},
		"Fields":              {"Path,ProviderIds,OriginalTitle,IndexNumber,ParentIndexNumber,SeriesName"},
		"AnyProviderIdEquals": {providerID},
	}
	if len(parentIDs) == 1 {
		params.Set("ParentId", parentIDs[0])
	}
	var result ItemsResponse
	err := c.JSON(ctx, http.MethodGet, "/Items", params, nil, &result)
	return result.Items, err
}

func normalizeLibraryIDs(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func (c *Client) Item(ctx context.Context, id string) (Item, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Item{}, fmt.Errorf("item id is required")
	}
	var result ItemsResponse
	err := c.JSON(ctx, http.MethodGet, "/Items", url.Values{
		"Ids":    {id},
		"Limit":  {"1"},
		"Fields": {"Path,MediaSources,MediaStreams,Overview,Genres,Studios,Tags,Taglines,ProviderIds,OfficialRating,ProductionYear,PremiereDate,CommunityRating,People,Chapters,OriginalTitle,SeriesName,SeasonName,IndexNumber,ParentIndexNumber,ImageTags,BackdropImageTags"},
	}, nil, &result)
	if err != nil {
		return Item{}, err
	}
	if len(result.Items) == 0 {
		return Item{}, fmt.Errorf("item %q not found", id)
	}
	return result.Items[0], nil
}

func (c *Client) Person(ctx context.Context, name string) (Person, error) {
	var person Person
	err := c.JSON(ctx, http.MethodGet, "/Persons/"+name, url.Values{
		"Fields": {"ProviderIds,ImageTags"},
	}, nil, &person)
	return person, err
}

func (c *Client) Persons(ctx context.Context, searchTerm string, limit int) ([]Person, error) {
	if limit <= 0 {
		limit = 20
	}
	params := url.Values{
		"Fields": {"ProviderIds,ImageTags"},
		"Limit":  {fmt.Sprintf("%d", limit)},
	}
	if strings.TrimSpace(searchTerm) != "" {
		params.Set("SearchTerm", searchTerm)
	}
	var result PersonsResponse
	err := c.JSON(ctx, http.MethodGet, "/Persons", params, nil, &result)
	return result.Items, err
}

func (c *Client) FindPersonByName(ctx context.Context, name string) (Person, error) {
	people, err := c.Persons(ctx, name, 20)
	if err != nil {
		return Person{}, err
	}
	name = strings.TrimSpace(name)
	for _, person := range people {
		if strings.EqualFold(strings.TrimSpace(person.Name), name) {
			return person, nil
		}
	}
	if len(people) == 1 {
		return people[0], nil
	}
	return Person{}, fmt.Errorf("target person %q not found", name)
}

func (c *Client) Images(ctx context.Context, itemID string) ([]ImageInfo, error) {
	var images []ImageInfo
	err := c.JSON(ctx, http.MethodGet, "/Items/"+url.PathEscape(itemID)+"/Images", nil, nil, &images)
	if err == nil {
		for idx := range images {
			images[idx].DownloadPath = imagePath(itemID, images[idx])
		}
	}
	return images, err
}

func FallbackImages(item Item) []ImageInfo {
	seen := map[string]bool{}
	images := make([]ImageInfo, 0)
	for _, typ := range DefaultImageTypes {
		if typ == "Backdrop" {
			if len(item.BackdropImageTags) > 0 {
				for idx, tag := range item.BackdropImageTags {
					key := fmt.Sprintf("Backdrop:%d", idx)
					seen[key] = true
					images = append(images, ImageInfo{ImageType: "Backdrop", ImageIndex: idx, Tag: tag, IsFallback: true})
				}
				continue
			}
		}
		if tag := item.ImageTags[typ]; tag != "" {
			key := typ + ":0"
			if !seen[key] {
				seen[key] = true
				images = append(images, ImageInfo{ImageType: typ, Tag: tag, IsFallback: true})
			}
		}
	}
	for idx := range images {
		images[idx].DownloadPath = imagePath(item.ID, images[idx])
	}
	return images
}

func DirectImageInfos(itemID string, imageTypes []string) []ImageInfo {
	if len(imageTypes) == 0 {
		imageTypes = DefaultImageTypes
	}
	images := make([]ImageInfo, 0, len(imageTypes))
	for _, typ := range imageTypes {
		typ = strings.TrimSpace(typ)
		if typ == "" {
			continue
		}
		images = append(images, ImageInfo{
			ImageType:    typ,
			IsFallback:   true,
			DownloadPath: "/Items/" + url.PathEscape(itemID) + "/Images/" + url.PathEscape(typ),
		})
	}
	return images
}

func imagePath(itemID string, image ImageInfo) string {
	base := "/Items/" + url.PathEscape(itemID) + "/Images/" + url.PathEscape(image.ImageType)
	if image.ImageType == "Backdrop" || image.ImageIndex > 0 {
		return base + "/" + fmt.Sprintf("%d", image.ImageIndex)
	}
	return base
}

func (c *Client) DownloadPath(ctx context.Context, endpoint string) ([]byte, string, error) {
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, "", fmt.Errorf("emby %s failed: HTTP %d %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	ext := extensionFromContentType(resp.Header.Get("Content-Type"))
	return data, ext, nil
}

func (c *Client) DownloadPersonImage(ctx context.Context, name string) ([]byte, string, error) {
	return c.DownloadPath(ctx, "/Persons/"+name+"/Images/Primary")
}

func (c *Client) UpdateItem(ctx context.Context, id string, item Item) error {
	return c.JSON(ctx, http.MethodPost, "/Items/"+url.PathEscape(id), nil, item.Raw, nil)
}

func (c *Client) UploadImage(ctx context.Context, itemID, imageType string, data []byte) error {
	return c.uploadImagePath(ctx, "/Items/"+url.PathEscape(itemID)+"/Images/"+url.PathEscape(imageType), data)
}

func (c *Client) UploadPersonImageByID(ctx context.Context, personID string, data []byte) error {
	id := strings.TrimSpace(personID)
	if id == "" {
		return fmt.Errorf("target person id is required")
	}
	if err := c.uploadImagePath(ctx, "/Items/"+url.PathEscape(id)+"/Images/Primary", data); err != nil {
		return fmt.Errorf("upload person image by target person id failed: %w", err)
	}
	return nil
}

func (c *Client) UploadPersonImage(ctx context.Context, name string, data []byte) error {
	person, err := c.FindPersonByName(ctx, name)
	if err != nil || strings.TrimSpace(string(person.ID)) == "" {
		searchErr := err
		person, err = c.Person(ctx, name)
		if err != nil && searchErr != nil {
			return fmt.Errorf("find target person by name failed: search: %v; direct: %w", searchErr, err)
		}
	}
	if err != nil {
		return fmt.Errorf("find target person by name failed: %w", err)
	}
	id := strings.TrimSpace(string(person.ID))
	if id == "" {
		return fmt.Errorf("target person %q has empty id", name)
	}
	return c.UploadPersonImageByID(ctx, id, data)
}

func (c *Client) uploadImagePath(ctx context.Context, endpoint string, data []byte) error {
	encoded := base64.StdEncoding.EncodeToString(data)
	req, err := c.newRequest(ctx, http.MethodPost, endpoint, nil, strings.NewReader(encoded))
	if err != nil {
		return err
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") {
		contentType = "image/jpeg"
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload image failed: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Client) JSON(ctx context.Context, method, endpoint string, params url.Values, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := c.newRequest(ctx, method, endpoint, params, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("emby %s %s failed: HTTP %d %s", method, endpoint, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, params url.Values, body io.Reader) (*http.Request, error) {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(path.Clean("/"+endpoint), "/")
	if params != nil {
		u.RawQuery = params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("X-Emby-Token", c.APIKey)
	}
	return req, nil
}

func extensionFromContentType(contentType string) string {
	if contentType == "" {
		return ".jpg"
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ".jpg"
	}
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".jpg"
	}
}
