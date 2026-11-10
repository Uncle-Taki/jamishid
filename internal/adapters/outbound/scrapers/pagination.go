package scrapers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
)

// Cursor payloads never contain a fetch URL. Requests always go to the fixed
// provider endpoints below. Tokens are bound to the exact selected search.
type pageCursor struct {
	Search  string          `json:"s"`
	Page    int             `json:"p"`
	Expires int64           `json:"e"`
	Data    json.RawMessage `json:"d"`
}

var cursorKey = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

// ConfigureCursorSigningKey is called at startup before requests can decode tokens.
func ConfigureCursorSigningKey(key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("cursor signing key must contain 32 bytes")
	}
	cursorKey = append([]byte(nil), key...)
	return nil
}

var ErrInvalidCursor = errors.New("pagination cursor does not match this search or has expired")

// Continuations must remain usable when their source page is served from the
// 24-hour cache. Allow another day to finish traversing that cached search.
const cursorTTL = 48 * time.Hour

func searchScope(platform vehicle.Platform, c vehicle.SearchCriteria) string {
	scope := []string{string(platform), string(c.Type), c.CitySlug, c.Query, c.BamaCategory, c.Keywords}
	// Preserve signed continuation scopes created before native coverage partitions existed.
	if c.NativeCategory != "" {
		scope = append(scope, c.NativeCategory)
	}
	raw, _ := json.Marshal(scope)
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func encodeCursor(platform vehicle.Platform, c vehicle.SearchCriteria, page int, data any) string {
	payload, _ := json.Marshal(data)
	raw, _ := json.Marshal(pageCursor{Search: searchScope(platform, c), Page: page, Expires: time.Now().Add(cursorTTL).Unix(), Data: payload})
	mac := hmac.New(sha256.New, cursorKey)
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func decodeCursor(token string, platform vehicle.Platform, c vehicle.SearchCriteria) (pageCursor, error) {
	var out pageCursor
	if len(token) > 32768 {
		return out, ErrInvalidCursor
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return out, ErrInvalidCursor
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return out, ErrInvalidCursor
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return out, ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, cursorKey)
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) || json.Unmarshal(raw, &out) != nil || out.Search != searchScope(platform, c) || out.Expires < time.Now().Unix() || out.Page < 1 {
		return out, ErrInvalidCursor
	}
	return out, nil
}

func PublicSearchPage(ctx context.Context, client *SharedClient, base string, platform vehicle.Platform, c vehicle.SearchCriteria, token string) (page vehicle.ListingPage, resultErr error) {
	ctx, finish := observer.Start(ctx, "page", platform)
	defer func() { finish(ports.Observation{Error: resultErr, Listings: len(page.Listings)}) }()
	var err error
	cursor := pageCursor{Page: 1}
	if token != "" {
		cursor, err = decodeCursor(token, platform, c)
		if err != nil {
			return page, &ports.SourceError{Kind: "cursor", Err: err}
		}
	}
	switch platform {
	case vehicle.PlatformDivar:
		page, err = divarSearchPage(ctx, client, base, c, cursor, token != "")
	case vehicle.PlatformBama:
		page, err = bamaSearchPage(ctx, client, base, c, cursor, token != "")
	case vehicle.PlatformSheypoor:
		page, err = sheypoorSearchPage(ctx, client, base, c, cursor, token != "")
	default:
		return page, fmt.Errorf("unknown marketplace")
	}
	if err != nil {
		if ports.FailureKind(err) == "other" {
			err = &ports.SourceError{Kind: "parser", Err: err}
		}
		return page, err
	}
	if page.Parser.Valid == 0 && len(page.Listings) > 0 {
		page.Listings, page.Parser, err = validateCollection(ctx, platform, len(page.Listings), page.Listings)
		if err != nil {
			return page, err
		}
	}
	canonical := map[vehicle.Platform]string{vehicle.PlatformDivar: "divar.ir", vehicle.PlatformBama: "bama.ir", vehicle.PlatformSheypoor: "sheypoor.com"}[platform]
	rows := make([]vehicle.NormalizedVehicleListing, 0, len(page.Listings))
	seen := map[string]bool{}
	for _, l := range page.Listings {
		if platform == vehicle.PlatformBama {
			l.CityID = vehicle.CityFromLocation(l.NeighborhoodFa)
			if citySlug(c) != "iran" && l.CityID != citySlug(c) {
				continue
			}
		} else if citySlug(c) == "iran" {
			l.CityID = vehicle.CityFromLocation(l.NeighborhoodFa)
		} else {
			l.CityID = citySlug(c)
		}
		// These providers may broaden text searches to other models or recommendations.
		// Keep continuation state even when every listing on this page is rejected.
		if platform != vehicle.PlatformBama && !page.CategoryMatched && c.BamaCategory != "" && !matchesSelectedVehicle(l.Title, c) {
			continue
		}
		u, e := url.Parse(l.URL)
		if e != nil || u.Scheme != "https" || strings.TrimPrefix(u.Hostname(), "www.") != canonical || l.ID == "" || l.Title == "" || seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		if !strings.HasPrefix(l.ID, string(platform)+"-") {
			l.ID = string(platform) + "-" + l.ID
		}
		l.Platform = platform
		l.VehicleType = c.Type
		l.ScrapedAt = time.Now().UTC().Format(time.RFC3339)
		rows = append(rows, l)
	}
	page.Listings = rows
	if !ports.CrawlPolicyFrom(ctx).SkipDetails && (base == "https://divar.ir" || base == "https://bama.ir" || base == "https://www.sheypoor.com") {
		enrichListings(ctx, client, page.Listings)
	}
	page.Page = cursor.Page
	return page, nil
}

func pageDocument(body []byte) (*html.Node, error) { return html.Parse(bytes.NewReader(body)) }
func endpoint(base, productionHost, path string) string {
	// Tests inject a local base; production endpoints have fixed provider hosts.
	if strings.HasPrefix(base, "https://divar.ir") {
		return "https://" + productionHost + path
	}
	return strings.TrimRight(base, "/") + path
}
func citySlug(c vehicle.SearchCriteria) string {
	if c.CitySlug == "" {
		return "tehran"
	}
	return c.CitySlug
}
func categoryPath(c vehicle.SearchCriteria) string {
	if c.Type == vehicle.VehicleTypeMotorcycle {
		return "motorcycles"
	}
	return "car"
}

type divarContinuation struct {
	NativeExact bool           `json:"native_exact,omitempty"`
	Pagination  map[string]any `json:"pagination_data"`
	Search      map[string]any `json:"search_data"`
	Cities      []any          `json:"city_ids"`
	Location    any            `json:"user_selected_location"`
}

func divarSearchPage(ctx context.Context, client *SharedClient, base string, c vehicle.SearchCriteria, cursor pageCursor, more bool) (vehicle.ListingPage, error) {
	out := vehicle.ListingPage{}
	var state divarContinuation
	var next map[string]any
	hasMore := false
	if !more {
		path := categoryPath(c)
		query := c.Query
		if c.NativeCategory != "" {
			if !nativeCategoryPath.MatchString(c.NativeCategory) {
				return out, ErrInvalidCursor
			}
			path += "/" + c.NativeCategory
			state.NativeExact = true
		} else if c.BamaCategory != "" && base == "https://divar.ir" {
			native, err := searchCatalog.Resolve(ctx, vehicle.PlatformDivar, c)
			if err != nil {
				return out, fmt.Errorf("Divar category resolution: %w", err)
			}
			path += "/" + native
			state.NativeExact = nativeCategoryMatches(native, c)
			// The native model filter narrows the search without requiring
			// sellers to spell the full catalog label in their title.
			query = c.Keywords
		}
		searchURL := strings.TrimRight(base, "/") + "/s/" + url.PathEscape(citySlug(c)) + "/" + path + "?q=" + url.QueryEscape(query)
		body, err := client.Fetch(ctx, "GET", searchURL, "")
		if err != nil {
			return out, err
		}
		doc, err := pageDocument(body)
		if err != nil {
			return out, err
		}
		out.Listings, err = divarPage(doc)
		if err != nil {
			return out, err
		}
		var root map[string]any
		walk(doc, func(n *html.Node) {
			if n.Data != "script" || n.FirstChild == nil {
				return
			}
			text := n.FirstChild.Data
			i := strings.Index(text, "window.__PRELOADED_STATE__ =")
			if i < 0 {
				return
			}
			raw := strings.TrimSpace(strings.TrimPrefix(text[i:], "window.__PRELOADED_STATE__ ="))
			_ = json.Unmarshal([]byte(strings.TrimSuffix(raw, ";")), &root)
		})
		nb := obj(root["nb"])
		pagination := obj(nb["pagination"])
		next = obj(pagination["data"])
		hasMore, _ = pagination["hasMore"].(bool)
		search := obj(obj(root["search"])["searchData"])
		state.Search = map[string]any{"form_data": map[string]any{"data": search["formData"]}, "server_payload": search["serverPayload"], "query": query}
		widgets, _ := nb["listWidgets"].([]any)
		for _, w := range widgets {
			info := obj(obj(obj(w)["actionLog"])["server_side_info"])["info"]
			if str(obj(info)["search_layer"]) == "fulltext_t1" && state.NativeExact {
				out.CategoryMatched = true
				break
			}
		}
		for _, w := range widgets {
			data := obj(obj(obj(obj(w)["data"])["dto"])["data"])
			token := str(data["token"])
			for i := range out.Listings {
				if out.Listings[i].ID == token {
					out.Listings[i].PublishedAt = ListingTime(str(data["bottom_description_text"]), time.Now())
					// Displayed labelled card prices take precedence over JSON-LD,
					// whose numeric scale/currency can be inconsistent on Divar.
					for _, key := range []string{"top_description_text", "middle_description_text"} {
						text := str(data[key])
						if strings.Contains(text, "تومان") {
							applyAttribute(&out.Listings[i], "قیمت", text)
						}
					}
				}
			}
		}
		for _, w := range widgets {
			info := obj(obj(obj(w)["actionLog"])["server_side_info"])
			info = obj(info["info"])
			if cities, ok := info["cities"].([]any); ok && len(cities) > 0 {
				state.Cities = cities
				if location := obj(info["user_selected_location"]); location["places"] != nil {
					state.Location = location
				}
				break
			}
		}
		if state.Location == nil {
			state.Location = obj(root["unsafeUserSelectedLocation"])
		}
		if len(state.Cities) > 0 && obj(state.Location)["places"] == nil {
			places := []any{}
			for _, city := range state.Cities {
				places = append(places, map[string]any{"place_id": city})
			}
			state.Location = map[string]any{"places": places}
		}
		if hasMore && len(state.Cities) == 0 {
			return out, fmt.Errorf("Divar pagination is missing the selected city")
		}
	} else {
		if json.Unmarshal(cursor.Data, &state) != nil {
			return out, ErrInvalidCursor
		}
		req := map[string]any{"pagination_data": state.Pagination, "search_data": state.Search, "city_ids": state.Cities, "user_selected_location": state.Location, "disable_recommendation": true}
		raw, _ := json.Marshal(req)
		body, err := client.Fetch(ctx, "POST", endpoint(base, "api.divar.ir", "/v8/postlist/w/search"), string(raw))
		if err != nil {
			return out, err
		}
		var response map[string]any
		if json.Unmarshal(body, &response) != nil {
			return out, fmt.Errorf("invalid Divar continuation response")
		}
		layer := str(obj(obj(obj(response["action_log"])["server_side_info"])["info"])["search_layer"])
		if state.NativeExact && layer == "fuzzy_filter" {
			return out, nil
		}
		out.CategoryMatched = state.NativeExact && layer == "fulltext_t1"
		widgets, ok := response["list_widgets"].([]any)
		if !ok {
			// Divar omits the collection on some terminal pages. Only a
			// valid end-of-pagination response makes that a valid empty page.
			pagination := obj(response["pagination"])
			hasNext, exists := pagination["has_next_page"].(bool)
			// Protobuf JSON omits false/default fields. Its typed pagination
			// envelope plus the page-load action identifies a valid terminal
			// response even when has_next_page itself is absent.
			typedEnd := str(obj(pagination["data"])["@type"]) == "type.googleapis.com/post_list.PaginationData" && str(obj(obj(obj(response["action_log"])["server_side_info"])["item_type"])["type"]) == "POST_LIST_LOAD_PAGE"
			if hasNext || (!exists && !typedEnd) {
				return out, fmt.Errorf("Divar continuation has no listing collection")
			}
		}
		for _, w := range widgets {
			widget := obj(w)
			if state.NativeExact && str(widget["widget_type"]) == "SELECTOR_ROW" && strings.Contains(str(obj(widget["data"])["title"]), "آگهی‌های مشابه") {
				return out, nil
			}
			if str(widget["widget_type"]) != "POST_ROW" {
				continue
			}
			out.Parser.Candidates++
			data := obj(widget["data"])
			l := missingListing()
			l.ID = str(data["token"])
			if l.ID == "" {
				l.ID = str(obj(obj(data["action"])["payload"])["token"])
			}
			l.Title = str(data["title"])
			l.URL = "https://divar.ir/v/" + url.PathEscape(l.Title) + "/" + l.ID
			l.ImageURL = str(data["image_url"])
			l.PublishedAt = ListingTime(str(data["bottom_description_text"]), time.Now())
			top := str(data["top_description_text"])
			middle := str(data["middle_description_text"])
			for _, text := range []string{top, middle, str(data["bottom_description_text"])} {
				if strings.Contains(text, "تومان") {
					l.PriceToman = number(text)
				}
				if strings.Contains(text, "کیلومتر") {
					l.MileageKm = number(text)
				}
			}
			out.Listings = append(out.Listings, l)
		}
		pagination := obj(response["pagination"])
		next = obj(pagination["data"])
		hasMore, _ = pagination["has_next_page"].(bool)
	}
	var contractErr error
	candidates := out.Parser.Candidates
	if candidates == 0 {
		candidates = len(out.Listings)
	}
	out.Listings, out.Parser, contractErr = validateCollection(ctx, vehicle.PlatformDivar, candidates, out.Listings)
	if contractErr != nil {
		return out, contractErr
	}
	if hasMore && len(next) > 0 {
		state.Pagination = next
		out.NextCursor = encodeCursor(vehicle.PlatformDivar, c, cursor.Page+1, state)
	}
	return out, nil
}

type bamaContinuation struct {
	Vehicle string `json:"vehicle"`
	Index   int    `json:"index"`
}

func bamaSearchPage(ctx context.Context, client *SharedClient, base string, c vehicle.SearchCriteria, cursor pageCursor, more bool) (vehicle.ListingPage, error) {
	out := vehicle.ListingPage{}
	regionRoute, regionAPI, err := bamaRegion(citySlug(c))
	if err != nil {
		return out, err
	}
	state := bamaContinuation{}
	root := "/car"
	api := "/cad/api/search"
	if c.Type == vehicle.VehicleTypeMotorcycle {
		root = "/motorcycle"
		api = "/mad/api/search"
	}
	hasMore := false
	if !more {
		slug := c.NativeCategory
		if slug == "" {
			slug = c.BamaCategory
		}
		if slug == "" {
			var err error
			slug, err = bamaBrand(c.Query)
			if err != nil {
				return out, err
			}
		}
		if !categorySlug.MatchString(slug) {
			return out, fmt.Errorf("invalid marketplace category")
		}
		searchURL := strings.TrimRight(base, "/") + root + "/" + slug
		if regionRoute != "" {
			searchURL += "?region=" + url.QueryEscape(regionRoute)
		}
		body, err := client.Fetch(ctx, "GET", searchURL, "")
		if err != nil {
			return out, err
		}
		doc, err := pageDocument(body)
		if err != nil {
			return out, err
		}
		out.Listings, err = bamaPage(doc)
		if err != nil {
			return out, err
		}
		var table []any
		walk(doc, func(n *html.Node) {
			if attr(n, "id") == "__NUXT_DATA__" && n.FirstChild != nil {
				_ = json.Unmarshal([]byte(n.FirstChild.Data), &table)
			}
		})
		var resolve func(any, int) any
		resolve = func(v any, depth int) any {
			if depth > 14 {
				return nil
			}
			switch x := v.(type) {
			case float64:
				if int(x) >= 0 && int(x) < len(table) {
					return resolve(table[int(x)], depth+1)
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
			if m["filters"] != nil {
				filters := obj(resolve(m["filters"], 0))
				vehicles, _ := filters["vehicle"].([]any)
				if len(vehicles) > 0 {
					state.Vehicle = str(obj(vehicles[0])["value"])
				}
			}
			if m["has_next"] != nil {
				hasMore, _ = resolve(m["has_next"], 0).(bool)
			}
		}
		if state.Vehicle == "" && (c.BamaCategory != "" || c.NativeCategory != "") {
			return out, fmt.Errorf("Bama did not recognize the selected category")
		}
		state.Index = 1
		// The SSR page can contain promoted and incomplete records. Use the
		// native filtered collection for page zero as well as later pages.
		if state.Vehicle != "" {
			state.Index = 0
			raw, _ := json.Marshal(state)
			return bamaSearchPage(ctx, client, base, c, pageCursor{Page: cursor.Page, Data: raw}, true)
		}
	} else {
		if json.Unmarshal(cursor.Data, &state) != nil {
			return out, ErrInvalidCursor
		}
		q := url.Values{"vehicle": {state.Vehicle}, "pageIndex": {strconv.Itoa(state.Index)}, "pageSize": {"12"}}
		if regionAPI != "" {
			q.Set("region", regionAPI)
		}
		body, err := client.Fetch(ctx, "GET", strings.TrimRight(base, "/")+api+"?"+q.Encode(), "")
		if err != nil {
			return out, err
		}
		var response map[string]any
		if json.Unmarshal(body, &response) != nil {
			return out, fmt.Errorf("invalid Bama continuation response")
		}
		data := obj(response["data"])
		ads, ok := data["ads"].([]any)
		if !ok || response["status"] == false {
			return out, fmt.Errorf("Bama continuation has no listing collection")
		}
		out.Parser.Candidates = len(ads)
		for _, ad := range ads {
			a := obj(ad)
			d := obj(a["detail"])
			if d["code"] == nil {
				continue
			}
			l := missingListing()
			l.ID = str(d["code"])
			l.Title = str(d["title"]) + " " + str(d["trim"])
			l.BrandModel = str(d["title"])
			l.TrimName = str(d["trim"])
			l.URL = "https://bama.ir" + str(d["url"])
			l.PriceToman = number(obj(a["price"])["price"])
			l.Year = int(number(d["year"]))
			if mileage := str(d["mileage"]); numericGroup.MatchString(mileage) || strings.Contains(mileage, "صفر") {
				l.MileageKm = number(mileage)
			}
			l.BodyStatus = MapBodyStatus(str(d["body_status"]))
			l.ImageURL = str(d["image"])
			l.NeighborhoodFa = str(d["location"])
			l.ColorFa = str(d["body_color"])
			l.Gearbox = str(d["transmission"])
			l.FuelType = str(d["fuel"])
			for _, pair := range [][2]string{{"mileage", "کارکرد"}, {"year", "سال تولید"}, {"body_color", "رنگ"}, {"transmission", "گیربکس"}, {"fuel", "نوع سوخت"}, {"engine_volume", "حجم موتور"}, {"body_status", "وضعیت بدنه"}} {
				if d[pair[0]] != nil {
					applyAttribute(&l, pair[1], str(d[pair[0]]))
				}
			}
			l.PublishedAt = ListingTime(str(d["time"]), time.Now())
			out.Listings = append(out.Listings, l)
		}
		hasMore, _ = obj(response["metadata"])["has_next"].(bool)
		state.Index++
	}
	var contractErr error
	candidates := out.Parser.Candidates
	if candidates == 0 {
		candidates = len(out.Listings)
	}
	out.Listings, out.Parser, contractErr = validateCollection(ctx, vehicle.PlatformBama, candidates, out.Listings)
	if contractErr != nil {
		return out, contractErr
	}
	// Free text searches within the chosen brand/model; never infer another brand
	// or reject English brand aliases by comparing them with Persian titles.
	filtered := []vehicle.NormalizedVehicleListing{}
	for _, l := range out.Listings {
		if bamaMatches(l, c) {
			filtered = append(filtered, l)
		}
	}
	out.Listings = filtered
	if hasMore && state.Vehicle != "" {
		out.NextCursor = encodeCursor(vehicle.PlatformBama, c, cursor.Page+1, state)
	}
	return out, nil
}

func bamaMatches(l vehicle.NormalizedVehicleListing, c vehicle.SearchCriteria) bool {
	query := c.Query
	if c.BamaCategory != "" {
		// The user chose the provider's exact category. Reject promoted ads for
		// other models, then apply only the optional keywords inside it.
		// Native vehicle filters are authoritative: motorcycle route slugs
		// and listing URL model names are not always identical.
		if c.Type != vehicle.VehicleTypeMotorcycle && !strings.Contains(l.URL, "-"+c.BamaCategory+"-") {
			return false
		}
		query = c.Keywords
	}
	candidate := strings.ReplaceAll(searchText(l.Title+" "+l.URL), " ", "")
	for _, term := range strings.Fields(searchText(query)) {
		if term == "تیپ" {
			continue
		}
		if !strings.Contains(candidate, term) {
			return false
		}
	}
	return true
}

type sheypoorContinuation struct {
	Page     int    `json:"p"`
	Filter   string `json:"f"`
	Category string `json:"category,omitempty"`
}

func sheypoorSearchPage(ctx context.Context, client *SharedClient, base string, c vehicle.SearchCriteria, cursor pageCursor, more bool) (vehicle.ListingPage, error) {
	out := vehicle.ListingPage{}
	state := sheypoorContinuation{Page: 1}
	if more && json.Unmarshal(cursor.Data, &state) != nil {
		return out, ErrInvalidCursor
	}
	category := categoryPath(c)
	query := c.Query
	if state.Category != "" || c.NativeCategory != "" || (c.BamaCategory != "" && base == "https://www.sheypoor.com") {
		if !more {
			native, err := c.NativeCategory, error(nil)
			if native == "" {
				native, err = searchCatalog.Resolve(ctx, vehicle.PlatformSheypoor, c)
			}
			if err != nil {
				return out, fmt.Errorf("Sheypoor category resolution: %w", err)
			}
			state.Category = native
		}
		if state.Category == "" || !nativeCategoryPath.MatchString(state.Category) {
			return out, ErrInvalidCursor
		}
		category += "/" + state.Category
		query = c.Keywords
	}
	q := url.Values{"q": {query}, "p": {strconv.Itoa(state.Page)}}
	if state.Filter != "" {
		q.Set("f", state.Filter)
	}
	body, err := client.Fetch(ctx, "GET", strings.TrimRight(base, "/")+"/api/v10.0.0/search/"+url.PathEscape(citySlug(c))+"/"+category+"?"+q.Encode(), "")
	if err != nil {
		return out, err
	}
	var response map[string]any
	if json.Unmarshal(body, &response) != nil {
		return out, fmt.Errorf("invalid Sheypoor search response")
	}
	ads, ok := response["data"].([]any)
	if !ok {
		return out, fmt.Errorf("Sheypoor response has no listing collection")
	}
	out.Parser.Candidates = len(ads)
	for _, ad := range ads {
		a := obj(ad)
		d := obj(a["attributes"])
		if d["title"] == nil {
			continue
		}
		l := missingListing()
		l.ID = str(a["id"])
		l.Title = str(d["title"])
		l.PublishedAt = ListingTime(str(d["timePassedLabel"]), time.Now())
		l.URL = str(d["url"])
		l.NeighborhoodFa = str(d["location"])
		l.ImageURL = str(obj(obj(d["images"])["thumbnails"])["round"])
		prices, _ := d["price"].([]any)
		if len(prices) > 0 {
			p := obj(prices[0])
			if str(p["currency"]) == "تومان" {
				l.PriceToman = number(p["amount"])
			}
		}
		attrs, _ := a["fullAttributes"].([]any)
		for _, v := range attrs {
			m := obj(v)
			key, value := str(m["key"]), str(m["value"])
			applyAttribute(&l, key, value)
		}
		out.Listings = append(out.Listings, l)
	}
	var contractErr error
	out.Listings, out.Parser, contractErr = validateCollection(ctx, vehicle.PlatformSheypoor, len(ads), out.Listings)
	if contractErr != nil {
		return out, contractErr
	}
	meta := obj(response["meta"])
	filter := str(meta["f"])
	pageSize := number(meta["items_per_page"])
	if pageSize == 0 {
		pageSize = 24
	}
	total := number(meta["normal_count"])
	if len(ads) > 0 && filter != "" && filter != state.Filter && (total == 0 || float64(state.Page)*pageSize < total) {
		state.Page++
		state.Filter = filter
		out.NextCursor = encodeCursor(vehicle.PlatformSheypoor, c, cursor.Page+1, state)
	}
	return out, nil
}
