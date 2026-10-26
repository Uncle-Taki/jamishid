package scrapers

import (
	"context"
	"fmt"
	"io"
	"net/http"
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

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	// Divar may serve an empty SPA shell when HTML category requests also
	// advertise application/json. Negotiate JSON only for API requests.
	if method != "GET" || strings.Contains(req.URL.Path, "/api/") {
		req.Header.Set("Accept", "application/json")
	}
	if method != "GET" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	const maxBody = 8 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if len(data) > maxBody {
		return nil, fmt.Errorf("upstream response exceeds 8 MiB")
	}
	return data, err
}

func MapBodyStatus(persian string) vehicle.BodyStatus {
	switch {
	case strings.Contains(persian, "بی‌رنگ") || strings.Contains(persian, "بدون رنگ"):
		return vehicle.BodyStatusPerfect
	case strings.Contains(persian, "لکه رنگ") || strings.Contains(persian, "چند لکه"):
		return vehicle.BodyStatusPaintedParts
	case strings.Contains(persian, "دور رنگ") || strings.Contains(persian, "تمام رنگ"):
		return vehicle.BodyStatusFullPaint
	case strings.Contains(persian, "شاسی خورده") || strings.Contains(persian, "ضربه شاسی") || strings.Contains(persian, "شاسی ضربه"):
		return vehicle.BodyStatusChassisHit
	case searchText(persian) == "سالم" || searchText(persian) == "کاملا سالم" || searchText(persian) == "کاملاً سالم":
		return vehicle.BodyStatusPerfect
	default:
		return vehicle.BodyStatusUnknown
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
	return ParseYearMileage(text)
}
