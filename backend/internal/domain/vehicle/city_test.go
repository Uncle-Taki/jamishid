package vehicle

import "testing"

func TestCityFromLocation(t *testing.T) {
	for location, want := range map[string]string{"تهران، آزادی": "tehran", "تهران / آزادی": "tehran", "شیراز": "shiraz", "كرج, گوهردشت": "karaj", "مشهد | سجاد": "mashhad", "تهرانسر": "", "پاکدشت": "", "": "", "ایران": ""} {
		if got := CityFromLocation(location); got != want {
			t.Errorf("%q: got %q want %q", location, got, want)
		}
	}
}
