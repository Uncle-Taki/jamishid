package scrapers

import (
	"testing"
	"time"
)

func TestListingTime(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		label string
		age   time.Duration
	}{{"۲ ساعت پیش در تهران", 2 * time.Hour}, {"12 ساعت پیش", 12 * time.Hour}, {"۶ روز پیش", 144 * time.Hour}, {"لحظاتی پیش", 0}, {"دیروز", 24 * time.Hour}} {
		got, e := time.Parse(time.RFC3339, ListingTime(tc.label, now))
		if e != nil || now.Sub(got) != tc.age {
			t.Errorf("%s: %v %v", tc.label, got, e)
		}
	}
	if ListingTime("پله شده", now) != "" {
		t.Fatal("unknown date must remain unknown")
	}
}
