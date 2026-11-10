package vehicle

import "testing"

func TestSearchNormalization(t *testing.T) {
	for _, input := range []string{"کیا ۱۸۰", "كيا ١٨٠", "کِیا 180"} {
		if got := NormalizeSearchText(input); got != "کیا 180" {
			t.Errorf("%q => %q", input, got)
		}
	}
	if NormalizeSearchText("جم‌شید") != "جم شید" {
		t.Error("ZWNJ separator lost")
	}
}
