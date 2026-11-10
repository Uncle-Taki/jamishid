package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
)

// Native category IDs are paths relative to the provider's vehicle category.
// They must not be reconstructed from another provider's slugs.
var nativeCategoryPath = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*(/[a-zA-Z0-9][a-zA-Z0-9.-]*){0,4}$`)

type ProviderCatalog struct {
	Client *SharedClient
	Bases  map[vehicle.Platform]string
	mu     sync.Mutex
	cache  map[string]catalogEntry
}

func NewProviderCatalog(client *SharedClient) *ProviderCatalog {
	return &ProviderCatalog{Client: client, Bases: map[vehicle.Platform]string{
		vehicle.PlatformBama: "https://bama.ir", vehicle.PlatformDivar: "https://divar.ir", vehicle.PlatformSheypoor: "https://www.sheypoor.com",
	}, cache: map[string]catalogEntry{}}
}

var searchCatalog = NewProviderCatalog(NewSharedClient())

// DefaultProviderCatalog shares discoveries between category browsing and searches.
func DefaultProviderCatalog() *ProviderCatalog { return searchCatalog }

// LoadSnapshot reuses successful discoveries across restarts. An incomplete
// export is useful, but failed or missing branches must still be fetched live.
func (c *ProviderCatalog) LoadSnapshot(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var snapshot struct {
		UpdatedAt time.Time                     `json:"updatedAt"`
		Nodes     map[string][]vehicle.Category `json:"nodes"`
	}
	if err = json.Unmarshal(body, &snapshot); err != nil {
		return err
	}
	expires := snapshot.UpdatedAt.Add(24 * time.Hour)
	if snapshot.UpdatedAt.IsZero() || !time.Now().Before(expires) {
		return fmt.Errorf("category snapshot is stale")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, rows := range snapshot.Nodes {
		parts := strings.SplitN(key, ":", 3)
		if len(parts) != 3 {
			continue
		}
		platform, kind, parent := vehicle.Platform(parts[0]), vehicle.VehicleType(parts[1]), parts[2]
		base, ok := c.Bases[platform]
		if !ok || (kind != vehicle.VehicleTypeCar && kind != vehicle.VehicleTypeMotorcycle) || (parent != "" && !nativeCategoryPath.MatchString(parent)) {
			continue
		}
		valid := true
		for _, row := range rows {
			if row.Type != kind || row.ParentID != parent || !nativeCategoryPath.MatchString(row.ID) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		c.cache[base+":"+key] = catalogEntry{rows: rows, expires: expires}
	}
	return nil
}

func (c *ProviderCatalog) Categories(ctx context.Context, platform vehicle.Platform, kind vehicle.VehicleType, parent string) (result []vehicle.Category, resultErr error) {
	ctx, finish := observer.Start(ctx, "catalog", platform)
	cacheState := "miss"
	defer func() {
		if resultErr != nil && ports.FailureKind(resultErr) == "other" {
			resultErr = &ports.SourceError{Kind: "catalog", Err: resultErr}
		}
		finish(ports.Observation{Cache: cacheState, Error: resultErr})
	}()
	if kind != vehicle.VehicleTypeCar && kind != vehicle.VehicleTypeMotorcycle {
		return nil, fmt.Errorf("invalid vehicle type")
	}
	base, ok := c.Bases[platform]
	if !ok || (parent != "" && (!nativeCategoryPath.MatchString(parent) || len(parent) > 200)) {
		return nil, fmt.Errorf("invalid source category")
	}
	key := base + ":" + string(platform) + ":" + string(kind) + ":" + parent
	c.mu.Lock()
	entry, cached := c.cache[key]
	c.mu.Unlock()
	if cached && time.Now().Before(entry.expires) {
		cacheState = "hit"
		return entry.rows, nil
	}
	if platform == vehicle.PlatformBama {
		catalog := NewCategoryCatalog(c.Client)
		catalog.BaseURL = base
		rows, err := catalog.Categories(ctx, kind, parent)
		if err == nil {
			c.remember(key, rows)
		}
		return rows, err
	}
	root := categoryPath(vehicle.SearchCriteria{Type: kind})
	prefix := "/s/tehran/" + root + "/"
	requestPath := "/s/tehran/" + root
	if platform == vehicle.PlatformSheypoor {
		requestPath = "/api/v10.0.0/search/filters/tehran/" + root
	}
	if parent != "" {
		requestPath += "/" + parent
	}
	body, err := c.Client.Fetch(ctx, "GET", strings.TrimRight(base, "/")+requestPath, "")
	if err != nil {
		return nil, err
	}
	rows := []vehicle.Category{}
	seen := map[string]bool{}
	add := func(path, label string) {
		u, err := url.Parse(path)
		if err != nil || (u.Host != "" && strings.TrimPrefix(u.Hostname(), "www.") != strings.TrimPrefix(mustHost(base), "www.")) || !strings.HasPrefix(u.Path, prefix) {
			return
		}
		id := strings.TrimPrefix(u.Path, prefix)
		if !nativeCategoryPath.MatchString(id) || seen[id] {
			return
		}
		rel := id
		if parent != "" {
			if !strings.HasPrefix(id, parent+"/") {
				return
			}
			rel = strings.TrimPrefix(id, parent+"/")
		}
		if strings.Contains(rel, "/") || strings.TrimSpace(label) == "" {
			return
		}
		seen[id] = true
		label = strings.TrimSuffix(strings.TrimSpace(label), " در تهران")
		label = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(label, "موتورسیکلت"), "خودرو"))
		rows = append(rows, vehicle.Category{ID: id, Label: label, ParentID: parent, Type: kind})
	}
	if platform == vehicle.PlatformSheypoor {
		var response map[string]any
		if json.Unmarshal(body, &response) != nil || response["meta"] == nil {
			return nil, fmt.Errorf("invalid Sheypoor category catalog")
		}
		links, _ := obj(response["meta"])["links"].([]any)
		for _, link := range links {
			v := obj(link)
			add(str(v["url"]), str(v["name"]))
		}
	} else {
		doc, err := pageDocument(body)
		if err != nil {
			return nil, err
		}
		walk(doc, func(n *html.Node) {
			if n.Data == "a" {
				add(attr(n, "href"), textContent(n))
			}
		})
		// A source can return HTTP 200 with only its app shell. That is
		// unavailable data, not evidence that this category has no children.
		marker := "window.__PRELOADED_STATE__ ="
		index := strings.Index(string(body), marker)
		var state map[string]any
		if index < 0 || json.NewDecoder(strings.NewReader(string(body)[index+len(marker):])).Decode(&state) != nil || obj(obj(obj(state["search"])["searchData"])["formData"])["category"] == nil {
			return nil, fmt.Errorf("invalid Divar category catalog")
		}
	}
	if parent == "" && len(rows) == 0 {
		return nil, fmt.Errorf("source brand catalog is unavailable")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	c.remember(key, rows)
	return rows, nil
}

func mustHost(base string) string {
	u, _ := url.Parse(base)
	if u == nil {
		return ""
	}
	return u.Hostname()
}
func (c *ProviderCatalog) remember(key string, rows []vehicle.Category) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = catalogEntry{rows: rows, expires: time.Now().Add(time.Hour)}
}

func categoryKey(value string) string { return strings.Join(titleWords(value), "") }

func nativeCategoryMatches(path string, c vehicle.SearchCriteria) bool {
	parts := strings.SplitN(c.BamaCategory, "-", 2)
	if len(parts) == 1 {
		return !strings.Contains(path, "/")
	}
	native := strings.SplitN(path, "/", 2)
	if len(native) != 2 {
		return false
	}
	if categoryKey(native[1]) == categoryKey(parts[1]) {
		return true
	}
	words := titleWords(strings.TrimSpace(strings.TrimSuffix(c.Query, c.Keywords)))
	return len(words) > 1 && categoryKey(native[1]) == strings.Join(words[1:], "")
}

// Resolve follows only links actually returned by the source. A provider lacking
// an exact model is scoped to its verified brand, with relevance filtering kept.
func (c *ProviderCatalog) Resolve(ctx context.Context, platform vehicle.Platform, criteria vehicle.SearchCriteria) (string, error) {
	brands, err := c.Categories(ctx, platform, criteria.Type, "")
	if err != nil {
		return "", err
	}
	parts := strings.SplitN(criteria.BamaCategory, "-", 2)
	brandKey := categoryKey(parts[0])
	labelWords := titleWords(criteria.Query)
	var brand vehicle.Category
	for _, row := range brands {
		if categoryKey(row.ID) == brandKey || (len(labelWords) > 0 && categoryKey(row.Label) == labelWords[0]) {
			brand = row
			break
		}
	}
	if brand.ID == "" {
		return "", fmt.Errorf("selected brand is not in %s catalog", platform)
	}
	if len(parts) == 1 {
		return brand.ID, nil
	}
	target := categoryKey(parts[1])
	label := titleWords(strings.TrimSpace(strings.TrimSuffix(criteria.Query, criteria.Keywords)))
	labelModel := ""
	if len(label) > 1 {
		labelModel = strings.Join(label[1:], "")
	}
	parent := brand.ID
	for depth := 0; depth < 4; depth++ {
		children, err := c.Categories(ctx, platform, criteria.Type, parent)
		if err != nil {
			return "", err
		}
		next := ""
		for _, child := range children {
			model := categoryKey(strings.TrimPrefix(child.ID, brand.ID+"/"))
			if model == target || (labelModel != "" && model == labelModel) {
				return child.ID, nil
			}
			// The provider's English suffix may differ (206 vs 206ir). A
			// complete normalized Persian label is also acceptable, never a substring.
			label := categoryKey(strings.TrimSpace(strings.TrimSuffix(criteria.Query, criteria.Keywords)))
			if categoryKey(child.Label) == label {
				return child.ID, nil
			}
			if (strings.HasPrefix(target, model) || (labelModel != "" && strings.HasPrefix(labelModel, model))) && len(child.ID) > len(next) {
				next = child.ID
			}
		}
		if next == "" {
			break
		}
		parent = next
	}
	return parent, nil
}
