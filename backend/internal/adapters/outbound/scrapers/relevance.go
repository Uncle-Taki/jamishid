package scrapers

import (
	"regexp"
	"strings"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

var vehicleWords = regexp.MustCompile(`[\p{L}]+|[0-9]+`)

func titleWords(text string) []string {
	return vehicleWords.FindAllString(searchText(text), -1)
}

func containsWords(title, words []string) bool {
	for _, word := range words {
		found := false
		for _, candidate := range title {
			if word == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Only source title evidence is used: a search URL repeats the query even for
// unrelated recommendations. Bama's native vehicle filter is handled separately.
func matchesSelectedVehicle(title string, c vehicle.SearchCriteria) bool {
	text := searchText(title)
	compact := strings.ReplaceAll(text, " ", "")
	for _, accessory := range []string{"لوازمیدکی", "فروشقطعات", "قطعاتیدکی", "کلهچراغ"} {
		if strings.Contains(compact, accessory) {
			return false
		}
	}
	for _, accessory := range []string{"چراغ ", "اگزوز ", "باک ", "انجین ", "لاستیک ", "فلاپ "} {
		if strings.HasPrefix(text, accessory) {
			return false
		}
	}
	words := titleWords(title)
	// Query is the selected catalog label followed by optional free text.
	category := strings.TrimSpace(strings.TrimSuffix(c.Query, c.Keywords))
	terms := titleWords(category)
	if len(terms) == 0 {
		return false
	}
	brand := strings.SplitN(c.BamaCategory, "-", 2)[0]
	if !containsWords(words, terms[:1]) && !containsWords(words, titleWords(brand)) {
		return false
	}
	model := terms[1:]
	if c.Type == vehicle.VehicleTypeMotorcycle && c.BamaCategory == "benelli-tnt180s" {
		for _, otherModel := range []string{"اپاچی", "ایدیوی", "adv", "اسکوتر", "کافهنرو", "ستا", "طرح"} {
			if strings.Contains(compact, otherModel) {
				return false
			}
		}
		// Sellers commonly omit TNT and/or write S as «اس», or just «بنلی ۱۸۰».
		// Token matching keeps 180 distinct from 1800 and other engine sizes.
		model = []string{"180"}
	}
	return containsWords(words, model) && containsWords(words, titleWords(c.Keywords))
}
