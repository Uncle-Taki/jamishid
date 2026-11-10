package vehicle

import "strings"

// CityFromLocation uses the source's city label, never the requested search city.
// Unknown cities remain unknown instead of being labelled with the search scope.
func CityFromLocation(location string) string {
	name := location
	for _, separator := range []string{"،", ",", "|", "/"} {
		name = strings.SplitN(name, separator, 2)[0]
	}
	name = NormalizeSearchText(strings.TrimSpace(name))
	return map[string]string{"تهران": "tehran", "کرج": "karaj", "مشهد": "mashhad", "اصفهان": "isfahan", "شیراز": "shiraz", "تبریز": "tabriz", "اهواز": "ahvaz", "قم": "qom", "رشت": "rasht"}[name]
}
