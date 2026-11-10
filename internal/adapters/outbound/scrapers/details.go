package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
	"golang.org/x/sync/singleflight"
)

type detailEntry struct {
	listing vehicle.NormalizedVehicleListing
	expires time.Time
}

var detailCache = struct {
	sync.Mutex
	entries map[string]detailEntry
}{entries: map[string]detailEntry{}}
var sharedDetails ports.ListingDetailCache

// ConfigureDetailCache is called once before serving requests. Nil retains the
// bounded local cache for standalone use and fixture tests.
func ConfigureDetailCache(cache ports.ListingDetailCache) { sharedDetails = cache }
func readDetails(ctx context.Context, key string) (vehicle.NormalizedVehicleListing, bool) {
	if sharedDetails != nil {
		return sharedDetails.GetDetail(ctx, key)
	}
	return cachedDetails(key)
}

var detailSlots = make(chan struct{}, 4)
var detailRequests singleflight.Group

const detailCacheTTL = 24 * time.Hour
const maxDetailEntries = 5000

var publicPostToken = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)
var detailPace = struct {
	sync.Mutex
	next map[vehicle.Platform]time.Time
}{next: map[vehicle.Platform]time.Time{}}

func waitDetailRequest(ctx context.Context, platform vehicle.Platform) bool {
	detailPace.Lock()
	next := detailPace.next[platform]
	if now := time.Now(); next.Before(now) {
		next = now
	}
	detailPace.next[platform] = next.Add(250 * time.Millisecond)
	detailPace.Unlock()
	timer := time.NewTimer(time.Until(next))
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// Detail failures never discard an otherwise valid listing. The page deadline
// bounds work, while cached detail facts are reused across searches and cities.
func enrichListings(ctx context.Context, client *SharedClient, rows []vehicle.NormalizedVehicleListing) {
	platform := vehicle.Platform("")
	if len(rows) > 0 {
		platform = rows[0].Platform
	}
	ctx, finish := observer.Start(ctx, "enrichment", platform)
	defer func() { finish(ports.Observation{Listings: len(rows)}) }()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			facts, err := listingDetails(ctx, client, rows[i].Platform, rows[i].URL)
			if err != nil {
				rows[i].DetailsStatus = "partial"
				return
			}
			mergeDetails(&rows[i], facts)
		}(i)
	}
	wg.Wait()
}

func listingDetails(ctx context.Context, client *SharedClient, platform vehicle.Platform, rawURL string) (vehicle.NormalizedVehicleListing, error) {
	if err := ctx.Err(); err != nil {
		return missingListing(), err
	}
	if !validDetailURL(rawURL, platform) {
		return missingListing(), fmt.Errorf("invalid listing URL")
	}
	u, _ := url.Parse(rawURL)
	u.Host = strings.TrimPrefix(u.Hostname(), "www.")
	u.RawQuery, u.Fragment = "", ""
	key := string(platform) + ":" + u.String()
	fetchURL := u.String()
	if platform == vehicle.PlatformDivar {
		token := path.Base(u.Path)
		if !publicPostToken.MatchString(token) {
			return missingListing(), fmt.Errorf("invalid listing token")
		}
		key = string(platform) + ":" + token
		fetchURL = "https://api.divar.ir/v8/posts-v2/web/" + token
	}
	if facts, ok := readDetails(ctx, key); ok && !ports.CrawlPolicyFrom(ctx).RefreshDetails {
		_, done := observer.Start(ctx, "detail_cache", platform)
		done(ports.Observation{Cache: "hit"})
		return facts, nil
	}
	_, cacheDone := observer.Start(ctx, "detail_cache", platform)
	cacheDone(ports.Observation{Cache: "miss"})
	ready := detailRequests.DoChan(key, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer cancel()
		fetch := func(fetchCtx context.Context) (vehicle.NormalizedVehicleListing, error) {
			select {
			case detailSlots <- struct{}{}:
				defer func() { <-detailSlots }()
			case <-fetchCtx.Done():
				return missingListing(), fetchCtx.Err()
			}
			if !waitDetailRequest(fetchCtx, platform) {
				return missingListing(), fetchCtx.Err()
			}
			body, err := client.Fetch(fetchCtx, "GET", fetchURL, "")
			if err != nil {
				return missingListing(), err
			}
			_, parseDone := observer.Start(fetchCtx, "detail_parse", platform)
			facts, err := parseListingDetails(body, platform)
			if err != nil {
				err = &ports.SourceError{Kind: "parser", Err: err}
			}
			parseDone(ports.Observation{Error: err})
			if err != nil {
				return missingListing(), err
			}
			if err := fetchCtx.Err(); err != nil {
				return missingListing(), err
			}
			facts.DetailsStatus = "complete"
			return facts, nil
		}
		if refresher, ok := sharedDetails.(ports.ListingDetailRefresher); ok && ports.CrawlPolicyFrom(fetchCtx).RefreshDetails {
			facts, _, err := refresher.RefreshDetail(fetchCtx, key, fetch)
			return facts, err
		}
		if sharedDetails != nil {
			facts, _, err := sharedDetails.LoadDetail(fetchCtx, key, fetch)
			return facts, err
		}
		if facts, ok := cachedDetails(key); ok {
			return facts, nil
		}
		facts, err := fetch(fetchCtx)
		if err == nil {
			storeDetails(key, facts)
		}
		return facts, err
	})
	select {
	case <-ctx.Done():
		return missingListing(), ctx.Err()
	case result := <-ready:
		if result.Err != nil {
			return missingListing(), result.Err
		}
		facts := result.Val.(vehicle.NormalizedVehicleListing)
		facts.Attributes = slices.Clone(facts.Attributes)
		return facts, nil
	}
}

func cachedDetails(key string) (vehicle.NormalizedVehicleListing, bool) {
	detailCache.Lock()
	defer detailCache.Unlock()
	entry, ok := detailCache.entries[key]
	if !ok {
		return vehicle.NormalizedVehicleListing{}, false
	}
	if !time.Now().Before(entry.expires) {
		delete(detailCache.entries, key)
		return vehicle.NormalizedVehicleListing{}, false
	}
	entry.listing.Attributes = slices.Clone(entry.listing.Attributes)
	return entry.listing, true
}

func storeDetails(key string, facts vehicle.NormalizedVehicleListing) {
	detailCache.Lock()
	defer detailCache.Unlock()
	now := time.Now()
	if len(detailCache.entries) >= maxDetailEntries {
		for key, entry := range detailCache.entries {
			if !now.Before(entry.expires) {
				delete(detailCache.entries, key)
			}
		}
		if len(detailCache.entries) >= maxDetailEntries {
			var oldest string
			var expires time.Time
			for key, entry := range detailCache.entries {
				if expires.IsZero() || entry.expires.Before(expires) {
					oldest, expires = key, entry.expires
				}
			}
			delete(detailCache.entries, oldest)
		}
	}
	facts.Attributes = slices.Clone(facts.Attributes)
	detailCache.entries[key] = detailEntry{listing: facts, expires: now.Add(detailCacheTTL)}
}

func mergeDetails(l *vehicle.NormalizedVehicleListing, d vehicle.NormalizedVehicleListing) {
	if d.MileageKm >= 0 {
		l.MileageKm = d.MileageKm
	}
	if d.Year > 0 {
		l.Year = d.Year
	}
	if d.PriceToman > 0 {
		l.PriceToman = d.PriceToman
	}
	if d.BodyStatus != vehicle.BodyStatusUnknown {
		l.BodyStatus = d.BodyStatus
	}
	if d.ColorFa != "" {
		l.ColorFa = d.ColorFa
	}
	if d.Gearbox != "" {
		l.Gearbox = d.Gearbox
	}
	if d.FuelType != "" {
		l.FuelType = d.FuelType
	}
	for _, a := range d.Attributes {
		applyAttribute(l, a.Label, a.Value)
	}
	l.DetailsStatus = d.DetailsStatus
}

func parseListingDetails(body []byte, platform vehicle.Platform) (vehicle.NormalizedVehicleListing, error) {
	l := missingListing()
	if platform == vehicle.PlatformDivar {
		var data map[string]any
		if json.Unmarshal(body, &data) == nil && data["sections"] != nil {
			readDivarFields(data["sections"], &l)
			if len(l.Attributes) == 0 {
				return l, fmt.Errorf("listing has no vehicle fields")
			}
			return l, nil
		}
	}
	doc, err := pageDocument(body)
	if err != nil {
		return l, err
	}
	recognized := false
	// Read vehicle facts from JSON-LD, never seller prose or unrelated cards.
	var schema func(any)
	schema = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, v := range x {
				schema(v)
			}
		case map[string]any:
			kind := str(x["@type"])
			if kind == "Motorcycle" || kind == "Car" || kind == "Vehicle" || (kind == "Product" && x["mileageFromOdometer"] != nil) {
				recognized = true
				odo := obj(x["mileageFromOdometer"])
				if odo["value"] != nil && (str(odo["unitCode"]) == "KMT" || str(odo["unitText"]) == "km") {
					applyAttribute(&l, "کارکرد", str(odo["value"]))
				}
				for _, k := range []string{"productionDate", "vehicleModelDate"} {
					if x[k] != nil && number(x[k]) >= 1300 && number(x[k]) <= 2100 {
						applyAttribute(&l, "سال تولید", str(x[k]))
					}
				}
				if x["color"] != nil {
					applyAttribute(&l, "رنگ", str(x["color"]))
				}
				if x["vehicleTransmission"] != nil {
					applyAttribute(&l, "گیربکس", str(x["vehicleTransmission"]))
				}
				if x["fuelType"] != nil {
					applyAttribute(&l, "نوع سوخت", str(x["fuelType"]))
				}
				if platform == vehicle.PlatformSheypoor {
					offer := obj(x["offers"])
					price := offer["price"]
					if price == nil {
						price = offer["Price"]
					}
					if str(offer["priceCurrency"]) == "IRR" && number(price) > 0 {
						applyAttribute(&l, "قیمت", fmt.Sprintf("%.0f تومان", number(price)/10))
					}
				}
			}
			for _, k := range []string{"mainEntity", "@graph"} {
				if x[k] != nil {
					schema(x[k])
				}
			}
		}
	}
	walk(doc, func(n *html.Node) {
		if n.Data == "script" && attr(n, "type") == "application/ld+json" && n.FirstChild != nil {
			var v any
			if json.Unmarshal([]byte(n.FirstChild.Data), &v) == nil {
				schema(v)
			}
		}
	})
	if platform == vehicle.PlatformDivar {
		var root map[string]any
		walk(doc, func(n *html.Node) {
			if n.Data == "script" && n.FirstChild != nil {
				text := n.FirstChild.Data
				marker := "window.__PRELOADED_STATE__ ="
				if i := strings.Index(text, marker); i >= 0 {
					_ = json.NewDecoder(strings.NewReader(text[i+len(marker):])).Decode(&root)
				}
			}
		})
		post := obj(obj(root["currentPost"])["post"])
		var visit func(any)
		visit = func(v any) {
			switch x := v.(type) {
			case []any:
				for _, v := range x {
					visit(v)
				}
			case map[string]any:
				kind := str(x["widget_type"])
				if kind == "GROUP_INFO_ROW" {
					items, _ := obj(x["data"])["items"].([]any)
					for _, v := range items {
						m := obj(v)
						applyAttribute(&l, str(m["title"]), str(m["value"]))
					}
					recognized = true
				}
				if kind == "UNEXPANDABLE_ROW" {
					m := obj(x["data"])
					applyAttribute(&l, str(m["title"]), str(m["value"]))
					recognized = true
				}
				for _, v := range x {
					visit(v)
				}
			}
		}
		visit(post)
	}
	if platform == vehicle.PlatformSheypoor {
		// Visible field rows have a heading and value; keep extra specifications
		// that JSON-LD omits, such as ownership and payment type.
		walk(doc, func(n *html.Node) {
			if n.Data != "h3" || n.Parent == nil {
				return
			}
			label := textContent(n)
			for c := n.NextSibling; c != nil; c = c.NextSibling {
				if c.Data == "span" {
					applyAttribute(&l, label, textContent(c))
					break
				}
			}
		})
	}
	if platform == vehicle.PlatformBama {
		var table []any
		walk(doc, func(n *html.Node) {
			if attr(n, "id") == "__NUXT_DATA__" && n.FirstChild != nil {
				_ = json.Unmarshal([]byte(n.FirstChild.Data), &table)
			}
		})
		var resolve func(any, int) any
		resolve = func(v any, depth int) any {
			if depth > 16 {
				return nil
			}
			switch x := v.(type) {
			case float64:
				if int(x) >= 0 && int(x) < len(table) {
					value := table[int(x)]
					switch value.(type) {
					case float64, string, bool, nil:
						return value
					default:
						return resolve(value, depth+1)
					}
				}
			case map[string]any:
				r := map[string]any{}
				for k, v := range x {
					r[k] = resolve(v, depth+1)
				}
				return r
			case []any:
				r := []any{}
				for _, v := range x {
					r = append(r, resolve(v, depth+1))
				}
				return r
			default:
				return v
			}
			return nil
		}
		for _, v := range table {
			m := obj(v)
			if m["class"] != nil && m["engine"] != nil && m["weight"] != nil {
				d := obj(resolve(m, 0))
				for _, pair := range [][2]string{{"class", "کلاس"}, {"engine", "مشخصات پیشرانه"}, {"transmission", "تعداد دنده"}, {"weight", "وزن"}} {
					applyAttribute(&l, pair[1], str(obj(d[pair[0]])["text"]))
				}
			}
			if m["fixed"] != nil {
				fixed := obj(obj(resolve(m, 0))["fixed"])
				if strings.Contains(str(fixed["textKey"]), "Toman") {
					applyAttribute(&l, "قیمت", str(fixed["value"])+" تومان")
				}
			}
			if m["mileage"] == nil || m["brand"] == nil || m["year"] == nil {
				continue
			}
			d := obj(resolve(m, 0))
			recognized = true
			for _, pair := range [][2]string{{"mileage", "کارکرد"}, {"year", "سال تولید"}, {"transmission", "گیربکس"}, {"fuelType", "نوع سوخت"}} {
				value := obj(d[pair[0]])["value"]
				if value != nil {
					applyAttribute(&l, pair[1], str(value))
				}
			}
			applyAttribute(&l, "رنگ", str(obj(obj(d["color"])["body"])["value"]))
			applyAttribute(&l, "برند و مدل", strings.TrimSpace(str(obj(d["brand"])["text"])+" "+str(obj(d["model"])["text"])))
			applyAttribute(&l, "نوع بدنه", str(obj(d["bodyType"])["text"]))
		}
	}
	if !recognized {
		return l, fmt.Errorf("listing has no recognized vehicle detail data")
	}
	// The canonical detail URL is validated by the caller; no outbound URLs
	// discovered inside seller-controlled content are fetched.
	return l, nil
}

func readDivarFields(v any, l *vehicle.NormalizedVehicleListing) {
	switch x := v.(type) {
	case []any:
		for _, v := range x {
			readDivarFields(v, l)
		}
	case map[string]any:
		kind := str(x["widget_type"])
		if kind == "GROUP_INFO_ROW" {
			items, _ := obj(x["data"])["items"].([]any)
			for _, v := range items {
				m := obj(v)
				applyAttribute(l, str(m["title"]), str(m["value"]))
			}
		}
		if kind == "UNEXPANDABLE_ROW" {
			m := obj(x["data"])
			applyAttribute(l, str(m["title"]), str(m["value"]))
		}
		for _, v := range x {
			readDivarFields(v, l)
		}
	}
}

func validDetailURL(raw string, platform vehicle.Platform) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	host := map[vehicle.Platform]string{vehicle.PlatformDivar: "divar.ir", vehicle.PlatformBama: "bama.ir", vehicle.PlatformSheypoor: "sheypoor.com"}[platform]
	if host == "" || strings.TrimPrefix(u.Hostname(), "www.") != host {
		return false
	}
	if platform == vehicle.PlatformBama {
		return strings.HasPrefix(u.Path, "/motorcycle/detail-") || strings.HasPrefix(u.Path, "/car/detail-")
	}
	return strings.HasPrefix(u.Path, "/v/")
}

func FetchListingDetails(ctx context.Context, platform vehicle.Platform, rawURL string) (vehicle.NormalizedVehicleListing, error) {
	if !validDetailURL(rawURL, platform) {
		return missingListing(), fmt.Errorf("invalid listing URL")
	}
	rows := []vehicle.NormalizedVehicleListing{{URL: rawURL, Platform: platform, MileageKm: -1, BodyStatus: vehicle.BodyStatusUnknown}}
	enrichListings(ctx, NewSharedClient(), rows)
	if rows[0].DetailsStatus != "complete" {
		return rows[0], fmt.Errorf("listing details are unavailable")
	}
	return rows[0], nil
}

// EnrichListings is the source adapter used by durable workers for selected new/changed items.
func EnrichListings(ctx context.Context, rows []vehicle.NormalizedVehicleListing) {
	enrichListings(ctx, NewSharedClient(), rows)
}

// MergeDetails preserves identity and summary facts absent from the detail response.
func MergeDetails(row *vehicle.NormalizedVehicleListing, facts vehicle.NormalizedVehicleListing) {
	mergeDetails(row, facts)
}

// FetchListingFacts preserves typed upstream status codes for lifecycle verification.
func FetchListingFacts(ctx context.Context, platform vehicle.Platform, rawURL string) (vehicle.NormalizedVehicleListing, error) {
	return listingDetails(ctx, NewSharedClient(), platform, rawURL)
}
