package scrapers

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ListingTime converts the marketplace's displayed recency, not fetch time or
// modified_date. Relative labels have only the precision supplied by the site.
func ListingTime(label string, now time.Time) string {
	if exact := validTime(label); exact != "" {
		return exact
	}
	label = strings.ToLower(strings.ReplaceAll(label, "\u200c", " "))
	if strings.Contains(label, "دقایقی پیش") {
		return now.Add(-59 * time.Minute).UTC().Format(time.RFC3339)
	}
	if strings.Contains(label, "ساعاتی پیش") {
		return now.Add(-23*time.Hour - 59*time.Minute).UTC().Format(time.RFC3339)
	}
	if strings.Contains(label, "لحظاتی پیش") || strings.Contains(label, "همین الان") {
		return now.UTC().Format(time.RFC3339)
	}
	if strings.Contains(label, "دیروز") {
		return now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	}
	match := regexp.MustCompile(`([0-9۰-۹٠-٩]+)\s*(دقیقه|ساعت|روز|هفته|ماه|minute|hour|day|week)`).FindStringSubmatch(label)
	if len(match) == 0 {
		return ""
	}
	n, _ := strconv.Atoi(NormalizePersianDigits(match[1]))
	unit := time.Minute
	switch match[2] {
	case "ساعت", "hour":
		unit = time.Hour
	case "روز", "day":
		unit = 24 * time.Hour
	case "هفته", "week":
		unit = 7 * 24 * time.Hour
	case "ماه":
		unit = 30 * 24 * time.Hour
	}
	return now.Add(-time.Duration(n) * unit).UTC().Format(time.RFC3339)
}
