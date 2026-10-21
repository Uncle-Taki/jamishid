package scrapers

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
