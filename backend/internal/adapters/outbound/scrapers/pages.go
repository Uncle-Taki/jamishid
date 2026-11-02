package scrapers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/net/html"
)

// PublicPage reads the same server-rendered search page a visitor sees. It never
// substitutes generated listings when a source is unavailable or changes format.
func PublicPage(ctx context.Context, client *SharedClient, base string, platform vehicle.Platform, criteria vehicle.SearchCriteria) ([]vehicle.NormalizedVehicleListing, error) {
	city := criteria.CitySlug
	if city == "" {
		city = "tehran"
	}
	if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(city) {
		return nil, fmt.Errorf("unsupported city slug")
	}
	category := "car"
	if criteria.Type == vehicle.VehicleTypeMotorcycle {
		category = "motorcycles"
	}
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	if platform == vehicle.PlatformBama {
		u.Path = "/car"
		if criteria.Type == vehicle.VehicleTypeMotorcycle {
			u.Path = "/motorcycle"
		}
		brand, err := bamaBrand(criteria.Query)
		if err != nil {
			return nil, err
		}
		u.Path += "/" + brand
	} else {
		u.Path = "/s/" + city + "/" + category
	}
	q := u.Query()
	q.Set("q", criteria.Query)
	if platform == vehicle.PlatformBama {
		q.Del("q")
		q.Set("region", city)
	}
	u.RawQuery = q.Encode()
	body, err := client.Fetch(ctx, "GET", u.String(), "")
	if err != nil {
		return nil, err
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parse search page: %w", err)
	}
	var listings []vehicle.NormalizedVehicleListing
	switch platform {
	case vehicle.PlatformDivar:
		listings, err = divarPage(doc)
	case vehicle.PlatformBama:
		listings, err = bamaPage(doc)
	case vehicle.PlatformSheypoor:
		listings, err = sheypoorPage(doc)
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	seen := map[string]bool{}
	canonicalHost := map[vehicle.Platform]string{vehicle.PlatformDivar: "divar.ir", vehicle.PlatformBama: "bama.ir", vehicle.PlatformSheypoor: "sheypoor.com"}[platform]
	result := make([]vehicle.NormalizedVehicleListing, 0, len(listings))
	for _, l := range listings {
		// Bama has no public free-text search parameter. Its brand route and
		// region filter narrow the page; filter remaining model/trim terms here.
		if platform == vehicle.PlatformBama {
			cityName := map[string]string{"tehran": "تهران", "karaj": "کرج", "mashhad": "مشهد", "isfahan": "اصفهان", "shiraz": "شیراز", "tabriz": "تبریز", "ahvaz": "اهواز", "qom": "قم", "rasht": "رشت"}[city]
			if cityName == "" {
				return nil, fmt.Errorf("Bama city is not supported")
			}
			if !strings.HasPrefix(searchText(l.NeighborhoodFa), searchText(cityName)) {
				continue
			}
			candidate := strings.ReplaceAll(searchText(l.Title), " ", "")
			matched := true
			for _, term := range strings.Fields(searchText(criteria.Query)) {
				if !strings.Contains(candidate, term) {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
		}
		link, e := url.Parse(l.URL)
		if e != nil || link.Scheme != "https" || strings.TrimPrefix(link.Hostname(), "www.") != canonicalHost {
			continue
		}
		if l.Title == "" || l.ID == "" || seen[l.ID] {
			continue
		}
		seen[l.ID] = true
		l.ID = string(platform) + "-" + l.ID
		l.Platform = platform
		l.CityID = city
		l.VehicleType = criteria.Type
		l.ScrapedAt = now
		if l.VehicleType == "" {
			l.VehicleType = vehicle.VehicleTypeCar
		}
		result = append(result, l)
	}
	return result, nil
}

func searchText(s string) string { return vehicle.NormalizeSearchText(s) }

func bamaBrand(query string) (string, error) {
	q := searchText(query)
	for _, entry := range []struct{ aliases, slug string }{
		{"پژو|peugeot|206|207|405", "peugeot"}, {"پراید|pride", "pride"}, {"سمند|samand", "samand"}, {"دنا|dena", "dena"},
		{"تیبا|tiba", "tiba"}, {"کوییک|quick", "quick"}, {"ساینا|saina", "saina"}, {"شاهین|shahin", "shahin"}, {"تارا|tara", "tara"},
		{"رنو|renault|تندر", "renault"}, {"تویوتا|toyota", "toyota"}, {"هیوندای|hyundai", "hyundai"}, {"کیا|kia", "kia"},
		{"بنز|benz|mercedes", "benz"}, {"ب ام و|bmw", "bmw"}, {"هوندا|honda|کلیک", "honda"}, {"یاماها|yamaha", "yamaha"},
		{"باجاج|bajaj|پالس", "bajaj"}, {"وسپا|vespa", "vespa"}, {"نیسان|nissan", "nissan"}, {"جک|jac", "jac"}, {"ام وی ام|mvm", "mvm"},
	} {
		for _, alias := range strings.Split(entry.aliases, "|") {
			if strings.Contains(q, alias) {
				return entry.slug, nil
			}
		}
	}
	return "", fmt.Errorf("Bama search needs a recognized vehicle brand; Divar and Sheypoor support free text")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}
func textContent(n *html.Node) string {
	var parts []string
	walk(n, func(c *html.Node) {
		if c.Type == html.TextNode && c.Parent.Data != "script" && c.Parent.Data != "style" {
			parts = append(parts, c.Data)
		}
	})
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}
func str(v any) string {
	if v == nil {
		return ""
	}
	if n, ok := v.(float64); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}
func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func number(v any) float64     { return ExtractNumber(str(v)) }
func missingListing() vehicle.NormalizedVehicleListing {
	return vehicle.NormalizedVehicleListing{MileageKm: -1, BodyStatus: vehicle.BodyStatusUnknown}
}

func divarPage(doc *html.Node) ([]vehicle.NormalizedVehicleListing, error) {
	out := []vehicle.NormalizedVehicleListing{}
	recognized := false
	explicitEmpty := false
	walk(doc, func(n *html.Node) {
		if n.Data != "script" || attr(n, "type") != "application/ld+json" || n.FirstChild == nil {
			return
		}
		var data any
		if json.Unmarshal([]byte(n.FirstChild.Data), &data) != nil {
			return
		}
		values, ok := data.([]any)
		if !ok {
			return
		}
		recognized = true
		if len(values) == 0 {
			explicitEmpty = true
		}
		for _, value := range values {
			m := obj(value)
			kind := str(m["@type"])
			if kind != "Car" && kind != "Motorcycle" && kind != "Vehicle" && kind != "Product" {
				continue
			}
			l := missingListing()
			l.Title = str(m["name"])
			l.URL = str(m["url"])
			l.ID = path.Base(l.URL)
			offer := obj(m["offers"])
			l.PriceToman = number(offer["price"])
			if str(offer["priceCurrency"]) == "IRR" {
				l.PriceToman /= 10
			} else if c := str(offer["priceCurrency"]); c != "IRT" && c != "TOMAN" {
				l.PriceToman = 0
			}
			l.Year = int(number(m["productionDate"]))
			if odo := obj(m["mileageFromOdometer"]); odo["value"] != nil {
				l.MileageKm = number(odo["value"])
			}
			switch str(m["knownVehicleDamages"]) {
			case "intact":
				l.BodyStatus = vehicle.BodyStatusPerfect
			case "some-scratches", "one-spot", "two-spots", "several-spots":
				l.BodyStatus = vehicle.BodyStatusPaintedParts
			case "all-paint", "full-paint":
				l.BodyStatus = vehicle.BodyStatusFullPaint
			case "crashed", "chassis-damage":
				l.BodyStatus = vehicle.BodyStatusChassisHit
			}
			l.ColorFa = str(m["color"])
			l.ImageURL = str(m["image"])
			l.NeighborhoodFa = str(obj(m["web_info"])["district_persian"])
			l.PublishedAt = validTime(str(m["datePosted"]))
			out = append(out, l)
		}
	})
	if !recognized || (len(out) == 0 && !explicitEmpty) {
		return nil, fmt.Errorf("Divar search page contains no recognized listing data")
	}
	return out, nil
}

// Nuxt serializes values in a flat table; object values are references into it.
func bamaPage(doc *html.Node) ([]vehicle.NormalizedVehicleListing, error) {
	var table []any
	walk(doc, func(n *html.Node) {
		if attr(n, "id") == "__NUXT_DATA__" && n.FirstChild != nil {
			_ = json.Unmarshal([]byte(n.FirstChild.Data), &table)
		}
	})
	if len(table) == 0 {
		return nil, fmt.Errorf("Bama search page contains no recognized listing data")
	}
	resolve := func(v any) any {
		i, ok := v.(float64)
		if !ok || i < 0 || int(i) >= len(table) {
			return nil
		}
		return table[int(i)]
	}
	get := func(m map[string]any, k string) any { return resolve(m[k]) }
	out := []vehicle.NormalizedVehicleListing{}
	explicitEmpty := false
	for _, v := range table {
		m := obj(v)
		if m["ads"] != nil {
			if ads, ok := get(m, "ads").([]any); ok && len(ads) == 0 {
				explicitEmpty = true
			}
		}
		if m["code"] == nil || m["mileage"] == nil || m["price"] == nil {
			continue
		}
		l := missingListing()
		l.ID = str(get(m, "code"))
		l.Title = str(get(m, "title")) + " " + str(get(m, "trim"))
		l.URL = "https://bama.ir" + str(get(m, "url"))
		l.Year = int(number(get(m, "year")))
		mileage := str(get(m, "mileage"))
		if regexp.MustCompile(`[0-9۰-۹٠-٩]`).MatchString(mileage) || strings.Contains(mileage, "صفر") {
			l.MileageKm = number(mileage)
		}
		l.PriceToman = number(get(obj(get(m, "price")), "price")) // Public Bama page prices are Toman.
		l.BodyStatus = MapBodyStatus(str(get(m, "body_status")))
		l.ColorFa = str(get(m, "body_color"))
		l.ImageURL = str(get(m, "image"))
		l.NeighborhoodFa = str(get(m, "location"))
		l.Gearbox = str(get(m, "transmission"))
		l.FuelType = str(get(m, "fuel"))
		l.PublishedAt = ListingTime(str(get(m, "time")), time.Now())
		out = append(out, l)
	}
	if len(out) == 0 && !explicitEmpty {
		return nil, fmt.Errorf("Bama state has no listing records or explicit empty collection")
	}
	return out, nil
}

func sheypoorPage(doc *html.Node) ([]vehicle.NormalizedVehicleListing, error) {
	out := []vehicle.NormalizedVehicleListing{}
	recognized := false
	walk(doc, func(n *html.Node) {
		if n.Data != "a" || !strings.HasPrefix(attr(n, "data-test-id"), "ad-item-") {
			return
		}
		recognized = true
		l := missingListing()
		l.ID = strings.TrimPrefix(attr(n, "data-test-id"), "ad-item-")
		l.URL = "https://www.sheypoor.com" + attr(n, "href")
		walk(n, func(c *html.Node) {
			if c.Data == "h2" || c.Data == "h3" {
				l.Title = textContent(c)
			}
			if c.Data == "img" && l.ImageURL == "" {
				l.ImageURL = attr(c, "src")
			}
			if attr(c, "data-test-id") == "icon-toman" && c.Parent != nil {
				for p := c.Parent.FirstChild; p != nil; p = p.NextSibling {
					if p.Type == html.ElementNode && p != c {
						l.PriceToman = number(textContent(p))
						break
					}
				}
			}
			if c.Data == "small" {
				l.NeighborhoodFa = textContent(c)
			}
		})
		out = append(out, l)
	})
	if !recognized {
		// An explicit empty-search marker is different from an unrecognized/broken page.
		if !strings.Contains(textContent(doc), "آگهی یافت نشد") {
			return nil, fmt.Errorf("Sheypoor search page contains no recognized listing data")
		}
	}
	return out, nil
}

func validTime(s string) string {
	if t, e := time.Parse(time.RFC3339, s); e == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// Keep numeric groups separate: stripping non-digits from a full description
// would concatenate a model year with its mileage.
var numericGroup = regexp.MustCompile(`[0-9۰-۹٠-٩][0-9۰-۹٠-٩,٬]*`)

func ParseYearMileage(text string) (int, float64) {
	year := 0
	mileage := -1.0
	for _, g := range numericGroup.FindAllString(text, -1) {
		v, _ := strconv.Atoi(NormalizePersianDigits(g))
		if year == 0 && v >= 1300 && v <= 1500 {
			year = v
		} else if strings.Contains(text, "کیلومتر") || strings.Contains(text, "km") {
			mileage = float64(v)
		}
	}
	return year, mileage
}
