package vehicle

import "strings"

// Comparison-only normalization; source labels and facts remain unchanged.
func scoringIdentity(row NormalizedVehicleListing) (model, trim string) {
	model, trim = NormalizeModelText(row.BrandModel), NormalizeModelText(row.TrimName)
	audi := NormalizeModelText("آئودی")
	if model == "audi" {
		model = audi
	} else if strings.HasPrefix(model, "audi ") {
		model = audi + strings.TrimPrefix(model, "audi")
	}
	for _, pair := range [][2]string{{" کیو 5 ", " q 5 "}, {" ای ترون ", " e tron "}, {" etron ", " e tron "}} {
		model = strings.TrimSpace(strings.ReplaceAll(" "+model+" ", pair[0], pair[1]))
	}
	q5 := audi + " q 5 e tron"
	for _, embedded := range []string{"40", "50"} {
		if model == q5+" "+embedded && (trim == "" || trim == embedded) {
			model, trim = q5, embedded
		}
	}
	return model, trim
}
