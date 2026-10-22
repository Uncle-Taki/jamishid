package scrapers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type SharedClient struct {
	client *http.Client
}

func NewSharedClient() *SharedClient {
	return &SharedClient{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (s *SharedClient) Fetch(ctx context.Context, method, url, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func MapBodyStatus(persian string) vehicle.BodyStatus {
	switch {
	case strings.Contains(persian, "بی‌رنگ") || strings.Contains(persian, "بدون رنگ"):
		return vehicle.BodyStatusPerfect
	case strings.Contains(persian, "لکه رنگ") || strings.Contains(persian, "چند لکه"):
		return vehicle.BodyStatusPaintedParts
	case strings.Contains(persian, "دور رنگ") || strings.Contains(persian, "تمام رنگ"):
		return vehicle.BodyStatusFullPaint
	case strings.Contains(persian, "شاسی"):
		return vehicle.BodyStatusChassisHit
	default:
		return vehicle.BodyStatusPerfect // Fallback
	}
}

var persianDigitMap = map[rune]rune{
	'۰': '0', '۱': '1', '۲': '2', '۳': '3', '۴': '4',
	'۵': '5', '۶': '6', '۷': '7', '۸': '8', '۹': '9',
	'٠': '0', '١': '1', '٢': '2', '٣': '3', '٤': '4',
	'٥': '5', '٦': '6', '٧': '7', '٨': '8', '٩': '9',
}

func NormalizePersianDigits(s string) string {
	var builder strings.Builder
	for _, r := range s {
		if val, ok := persianDigitMap[r]; ok {
			builder.WriteRune(val)
		} else if (r >= '0' && r <= '9') || r == '.' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func ExtractNumber(s string) float64 {
	norm := NormalizePersianDigits(s)
	if norm == "" {
		return 0
	}
	val, err := strconv.ParseFloat(norm, 64)
	if err != nil {
		return 0
	}
	return val
}

func ExtractYearAndMileage(text string) (int, float64) {
	year := 1400
	mileage := 20000.0

	norm := NormalizePersianDigits(text)

	// Basic regex for year (1370 - 1405)
	yearRegex := regexp.MustCompile(`(13[7-9]\d|140[0-5])`)
	match := yearRegex.FindString(norm)
	if match != "" {
		y, _ := strconv.Atoi(match)
		year = y
	}

	// Remove the year to not confuse mileage
	textNoYear := strings.Replace(norm, match, "", 1)

	// Regex for mileage (just the largest number left)
	numRegex := regexp.MustCompile(`\d+`)
	matches := numRegex.FindAllString(textNoYear, -1)
	for _, m := range matches {
		val, _ := strconv.ParseFloat(m, 64)
		if val > 100 { // Assuming mileage is > 100 to differentiate from random small numbers
			mileage = val
		}
	}

	return year, mileage
}
