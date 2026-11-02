package scrapers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestRetryAfterFormats(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tt := range []struct {
		value string
		want  time.Duration
	}{{"2", 2 * time.Second}, {now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second}, {"bad", 0}, {"-1", 0}, {"999999999", 999999999 * time.Second}} {
		if got := retryAfter(tt.value, now); got != tt.want {
			t.Errorf("%q: %v want %v", tt.value, got, tt.want)
		}
	}
}

func TestRetryTransientAndDoNotRetryPermanentFailures(t *testing.T) {
	for _, tt := range []struct {
		status int
		calls  int
	}{{429, 3}, {502, 3}, {503, 3}, {504, 3}, {400, 1}, {401, 1}, {403, 1}, {404, 1}, {500, 1}} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			var calls atomic.Int32
			client := &SharedClient{client: &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{StatusCode: tt.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("failure"))}, nil
			})}}
			_, err := client.Fetch(context.Background(), "GET", "https://fixture-"+strings.ReplaceAll(http.StatusText(tt.status), " ", "-")+".test/item", "")
			if err == nil || calls.Load() != int32(tt.calls) {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}

func TestRecoverableResponseRecoversAndUnsafePostDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	client := &SharedClient{client: &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		if calls.Add(1) == 1 {
			status = 503
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}}
	if _, err := client.Fetch(context.Background(), "GET", "https://recovery.test/item", ""); err != nil || calls.Load() != 2 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
	calls.Store(0)
	if _, err := client.Fetch(context.Background(), "POST", "https://unsafe.test/write", ""); err == nil || calls.Load() != 1 {
		t.Fatalf("unsafe POST retried: %d %v", calls.Load(), err)
	}
}

func TestSourceBudgetCooldownAndCancel(t *testing.T) {
	budget := &sourceBudget{cooldown: time.Now().Add(time.Hour)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := budget.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cooldown ignored: %v", err)
	}
}

func TestRetryAfterStopsRequestsBeyondDeadline(t *testing.T) {
	budgets.Delete("bama")
	defer budgets.Delete("bama")
	var calls atomic.Int32
	client := &SharedClient{client: &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"60"}}, Body: io.NopCloser(strings.NewReader("limited"))}, nil
	})}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := client.Fetch(ctx, "GET", "https://bama.ir/car", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline ignored: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel2()
	_, _ = NewSharedClient().Fetch(ctx2, "GET", "https://bama.ir/car", "")
	if calls.Load() != 1 {
		t.Fatalf("Retry-After not shared: %d", calls.Load())
	}
}

func TestFailureCategories(t *testing.T) {
	if ports.FailureKind(&ports.SourceError{Kind: "parser", Err: errors.New("invalid page")}) != "parser" {
		t.Fatal("parser classified as transport")
	}
}

func TestSharedBudgetSpacesConcurrentRequestStarts(t *testing.T) {
	budget := &sourceBudget{interval: 20 * time.Millisecond}
	var mu sync.Mutex
	var times []time.Time
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := budget.wait(context.Background()); err != nil {
				t.Error(err)
			}
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
		}()
	}
	wg.Wait()
	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) < 15*time.Millisecond {
			t.Fatal("concurrent callers exceeded source pacing")
		}
	}
}

func TestCanceledRequestDoesNotReachTransport(t *testing.T) {
	var calls atomic.Int32
	client := &SharedClient{client: &http.Client{Transport: detailTransport(func(r *http.Request) (*http.Response, error) { calls.Add(1); return nil, context.Canceled })}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Fetch(ctx, "GET", "https://bama.ir/car", "")
	if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("canceled call reached transport: %d %v", calls.Load(), err)
	}
}

type observedEvents struct {
	mu         sync.Mutex
	operations []string
	events     []ports.Observation
}

func (o *observedEvents) Start(ctx context.Context, operation string, _ vehicle.Platform) (context.Context, func(ports.Observation)) {
	return ctx, func(event ports.Observation) {
		o.mu.Lock()
		defer o.mu.Unlock()
		o.operations = append(o.operations, operation)
		o.events = append(o.events, event)
	}
}

func TestParserFailureIsObservedWithoutRetryingHTTP200(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, "<html>empty app shell</html>")
	}))
	defer server.Close()
	events := &observedEvents{}
	ConfigureObserver(events)
	defer ConfigureObserver(ports.NoopObserver{})
	_, err := PublicSearchPage(context.Background(), NewSharedClient(), server.URL, vehicle.PlatformBama, vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar}, "")
	if err == nil || ports.FailureKind(err) != "parser" || calls.Load() != 1 {
		t.Fatalf("parser failure retried or misclassified: calls=%d error=%v", calls.Load(), err)
	}
	seen := false
	for i, operation := range events.operations {
		if operation == "page" && ports.FailureKind(events.events[i].Error) == "parser" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("page parser failure missing from telemetry")
	}
}
