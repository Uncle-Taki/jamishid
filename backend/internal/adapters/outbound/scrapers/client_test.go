package scrapers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestMapBodyStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected vehicle.BodyStatus
	}{
		{"ماشین بی‌رنگ است", vehicle.BodyStatusPerfect},
		{"بدون رنگ واقعی", vehicle.BodyStatusPerfect},
		{"یک لکه رنگ روی در", vehicle.BodyStatusPaintedParts},
		{"چند لکه جزیی", vehicle.BodyStatusPaintedParts},
		{"دور رنگ میباشد", vehicle.BodyStatusFullPaint},
		{"تمام رنگ", vehicle.BodyStatusFullPaint},
		{"شاسی خورده", vehicle.BodyStatusChassisHit},
		{"نامشخص", vehicle.BodyStatusPerfect},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := MapBodyStatus(tt.input); got != tt.expected {
				t.Errorf("MapBodyStatus(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSharedClientFetch_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success": true}`))
	}))
	defer ts.Close()

	client := NewSharedClient()
	resp, err := client.Fetch(context.Background(), "GET", ts.URL, "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if string(resp) != `{"success": true}` {
		t.Errorf("unexpected response: %s", string(resp))
	}
}

func TestSharedClientFetch_ErrorStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	client := NewSharedClient()
	_, err := client.Fetch(context.Background(), "GET", ts.URL, "")
	if err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}

func TestSharedClientFetch_Timeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := NewSharedClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err := client.Fetch(ctx, "GET", ts.URL, "")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestNormalizePersianDigits(t *testing.T) {
	if got := NormalizePersianDigits("۵۰۰,۰۰۰ تومان"); got != "500000" {
		t.Errorf("expected 500000, got %s", got)
	}
}

func TestExtractNumber(t *testing.T) {
	if got := ExtractNumber("۵۰۰,۰۰۰ تومان"); got != 500000 {
		t.Errorf("expected 500000, got %v", got)
	}
	if got := ExtractNumber("invalid"); got != 0 {
		t.Errorf("expected 0, got %v", got)
	}
}

func TestExtractYearAndMileage(t *testing.T) {
	y, m := ExtractYearAndMileage("1402 | 15,000 km | بی‌رنگ")
	if y != 1402 || m != 15000 {
		t.Errorf("expected 1402 and 15000, got %d and %v", y, m)
	}

	y, m = ExtractYearAndMileage("بدون اطلاعات")
	if y != 1400 || m != 20000 {
		t.Errorf("expected default 1400 and 20000, got %d and %v", y, m)
	}
}
