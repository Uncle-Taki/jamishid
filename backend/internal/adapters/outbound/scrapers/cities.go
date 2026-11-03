package scrapers

import "fmt"

// Bama page routes use province-city; its search API uses province,city.
// A province alone (including "tehran") is not a city filter.
func bamaRegion(city string) (route, native string, err error) {
	regions := map[string][2]string{
		"iran": {"", ""}, "tehran": {"tehran-tehran", "tehran,tehran"},
		"karaj": {"alborz-karaj", "alborz,karaj"}, "mashhad": {"razavi_khorasan-mashhad", "razavi_khorasan,mashhad"},
		"isfahan": {"isfahan-isfahan", "isfahan,isfahan"}, "shiraz": {"fars-shiraz", "fars,shiraz"},
		"tabriz": {"azarbaijan_sharghi-tabriz", "azarbaijan_sharghi,tabriz"}, "ahvaz": {"khuzestan-ahvaz", "khuzestan,ahvaz"},
		"qom": {"qom-qom", "qom,qom"}, "rasht": {"gilan-rasht", "gilan,rasht"},
	}
	region, ok := regions[city]
	if !ok {
		return "", "", fmt.Errorf("unsupported Bama city")
	}
	return region[0], region[1], nil
}
