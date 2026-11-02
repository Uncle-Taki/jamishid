package scrapers

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
)

var categorySlug = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,100}$`)

type CategoryCatalog struct {
	client  *SharedClient
	BaseURL string
	mu      sync.Mutex
	cache   map[string]catalogEntry
}
type catalogEntry struct {
	rows    []vehicle.Category
	expires time.Time
}

func NewCategoryCatalog(client *SharedClient) *CategoryCatalog {
	return &CategoryCatalog{client: client, BaseURL: "https://bama.ir", cache: map[string]catalogEntry{}}
}

// Categories reads the actual brand/model links published by the marketplace.
// The IDs are provider slugs, rather than aliases guessed from free-text input.
func (c *CategoryCatalog) Categories(ctx context.Context, kind vehicle.VehicleType, parent string) (result []vehicle.Category, resultErr error) {
	ctx, finish := observer.Start(ctx, "catalog", vehicle.PlatformBama)
	cacheState := "miss"
	defer func() {
		if resultErr != nil && ports.FailureKind(resultErr) == "other" {
			resultErr = &ports.SourceError{Kind: "catalog", Err: resultErr}
		}
		finish(ports.Observation{Cache: cacheState, Error: resultErr})
	}()
	if kind != vehicle.VehicleTypeCar && kind != vehicle.VehicleTypeMotorcycle {
		return nil, fmt.Errorf("choose a vehicle type")
	}
	if parent != "" && !categorySlug.MatchString(parent) {
		return nil, fmt.Errorf("invalid parent category")
	}
	key := string(kind) + ":" + parent
	c.mu.Lock()
	entry, ok := c.cache[key]
	c.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		cacheState = "hit"
		return entry.rows, nil
	}
	root := "/car/"
	if kind == vehicle.VehicleTypeMotorcycle {
		root = "/motorcycle/"
	}
	body, err := c.client.Fetch(ctx, "GET", strings.TrimRight(c.BaseURL, "/")+root+parent, "")
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	rows := []vehicle.Category{}
	walk(doc, func(n *html.Node) {
		if n.Data != "a" {
			return
		}
		href := attr(n, "href")
		u, e := url.Parse(href)
		if e != nil || (u.Host != "" && u.Host != "bama.ir") {
			return
		}
		if !strings.HasPrefix(u.Path, root) {
			return
		}
		slug := strings.TrimPrefix(u.Path, root)
		if !categorySlug.MatchString(slug) || strings.HasPrefix(slug, "detail-") || seen[slug] {
			return
		}
		if parent == "" && strings.Contains(slug, "-") {
			return
		}
		if parent != "" && (!strings.HasPrefix(slug, parent+"-") || slug == parent) {
			return
		}
		label := strings.TrimSpace(strings.TrimPrefix(textContent(n), "خرید و فروش"))
		if label == "" {
			return
		}
		seen[slug] = true
		rows = append(rows, vehicle.Category{ID: slug, Label: label, ParentID: parent, Type: kind})
	})
	if len(rows) == 0 && parent == "" {
		return nil, fmt.Errorf("marketplace category catalog is unavailable")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Label < rows[j].Label })
	c.mu.Lock()
	c.cache[key] = catalogEntry{rows: rows, expires: time.Now().Add(time.Hour)}
	c.mu.Unlock()
	return rows, nil
}
