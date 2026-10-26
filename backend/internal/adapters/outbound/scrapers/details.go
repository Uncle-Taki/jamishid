package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
)

type detailEntry struct {
	listing vehicle.NormalizedVehicleListing
	expires time.Time
}

var detailCache = struct {
	sync.Mutex
	entries map[string]detailEntry
}{entries: map[string]detailEntry{}}
var detailSlots = make(chan struct{}, 4)
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
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case detailSlots <- struct{}{}:
				defer func() { <-detailSlots }()
			case <-ctx.Done():
				rows[i].DetailsStatus = "partial"
				return
			}
			original := rows[i]
			if !validDetailURL(original.URL, original.Platform) {
				rows[i].DetailsStatus = "partial"
				return
			}
			detailCache.Lock()
			entry, ok := detailCache.entries[original.URL]
			detailCache.Unlock()
			if ok && time.Now().Before(entry.expires) {
				mergeDetails(&rows[i], entry.listing)
				return
			}
			if !waitDetailRequest(ctx, original.Platform) {
				rows[i].DetailsStatus = "partial"
				return
			}
			fetchURL := original.URL
			if original.Platform == vehicle.PlatformDivar {
				u, _ := url.Parse(original.URL)
				token := path.Base(u.Path)
				if !publicPostToken.MatchString(token) {
					rows[i].DetailsStatus = "partial"
					return
				}
				fetchURL = "https://api.divar.ir/v8/posts-v2/web/" + token
			}
			body, err := client.Fetch(ctx, "GET", fetchURL, "")
			if err != nil {
				rows[i].DetailsStatus = "partial"
				return
			}
			facts, err := parseListingDetails(body, original.Platform)
			if err != nil {
				rows[i].DetailsStatus = "partial"
				return
			}
			facts.DetailsStatus = "complete"
			detailCache.Lock()
			if len(detailCache.entries) > 5000 {
				for key, e := range detailCache.entries {
					if time.Now().After(e.expires) {
						delete(detailCache.entries, key)
					}
				}
			}
			if len(detailCache.entries) < 5000 {
				detailCache.entries[original.URL] = detailEntry{listing: facts, expires: time.Now().Add(30 * time.Minute)}
			}
			detailCache.Unlock()
			mergeDetails(&rows[i], facts)
		}(i)
	}
	wg.Wait()
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
