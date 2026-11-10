package scrapers

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type sourceBudget struct {
	sync.Mutex
	next, cooldown time.Time
	interval       time.Duration
	failures       int
}

var budgets sync.Map
var observer ports.Observer = ports.NoopObserver{}
var sharedBudget ports.ProviderBudget

func ConfigureProviderBudget(budget ports.ProviderBudget) { sharedBudget = budget }

// ConfigureObserver is called once before the server starts.
func ConfigureObserver(o ports.Observer) { observer = o }

func sourceFor(raw string) vehicle.Platform {
	u, _ := url.Parse(raw)
	if u == nil {
		return "other"
	}
	host := strings.TrimPrefix(u.Hostname(), "www.")
	switch host {
	case "divar.ir", "api.divar.ir":
		return vehicle.PlatformDivar
	case "bama.ir":
		return vehicle.PlatformBama
	case "sheypoor.com":
		return vehicle.PlatformSheypoor
	}
	return "other"
}

func waitContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *sourceBudget) wait(ctx context.Context) error {
	for {
		b.Lock()
		now := time.Now()
		until := b.next
		if b.cooldown.After(until) {
			until = b.cooldown
		}
		if !until.After(now) {
			if b.interval == 0 {
				b.interval = 250 * time.Millisecond
			}
			b.next = now.Add(b.interval)
			b.Unlock()
			return ctx.Err()
		}
		b.Unlock()
		if err := waitContext(ctx, time.Until(until)); err != nil {
			return err
		}
	}
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 {
		if seconds > int64((time.Duration(1<<63-1))/time.Second) {
			seconds = int64((time.Duration(1<<63 - 1)) / time.Second)
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date.Sub(now)
	}
	return 0
}

type retryAfterKey struct{}
type responseHint struct {
	delay  time.Duration
	status int
}

func (s *SharedClient) Fetch(ctx context.Context, method, rawURL, body string) ([]byte, error) {
	platform := sourceFor(rawURL)
	key := string(platform)
	if platform == "other" {
		u, _ := url.Parse(rawURL)
		if u != nil {
			key = u.Host
		}
	}
	value, _ := budgets.LoadOrStore(key, &sourceBudget{})
	budget := value.(*sourceBudget)
	var last error
	// The only retried POST is Divar's read-only search endpoint.
	u, _ := url.Parse(rawURL)
	safe := method == "GET" || (method == "POST" && platform == vehicle.PlatformDivar && u.Path == "/v8/postlist/w/search")
	for attempt := 0; attempt < 3; attempt++ {
		var proxyID, endpoint string
		var egressErr error
		if egressPool != nil && platform != "other" {
			proxyID, endpoint, egressErr = egressPool.AcquireEgress(ctx)
			if egressErr != nil {
				return nil, &ports.SourceError{Kind: "egress_unavailable", Err: errors.New("egress pool unavailable")}
			}
		}
		budgetSource := string(platform)
		if proxyID != "" {
			budgetSource = string(platform) + ":" + proxyID
		}
		if platform != "other" {
			_, waitDone := observer.Start(ctx, "source_wait", platform)
			err := budget.wait(ctx)
			if err == nil && sharedBudget != nil {
				err = sharedBudget.Wait(ctx, budgetSource)
			}
			waitDone(ports.Observation{Error: err})
			if err != nil {
				var previous *ports.SourceError
				if errors.As(last, &previous) && previous.StatusCode == 429 {
					return nil, &ports.SourceError{Kind: "rate_limited", StatusCode: 429, Err: err}
				}
				return nil, &ports.SourceError{Kind: "source_wait", Err: err}
			}
		}
		attemptCtx, finish := observer.Start(ctx, "fetch", platform)
		hint := &responseHint{}
		fetchCtx := context.WithValue(attemptCtx, retryAfterKey{}, hint)
		if proxyID != "" {
			fetchCtx = withAcquiredProxy(fetchCtx, proxyID, endpoint)
		}
		data, err := s.fetchOnce(fetchCtx, method, rawURL, body)
		status := hint.status
		var source *ports.SourceError
		if errors.As(err, &source) {
			status = source.StatusCode
		} else if err != nil {
			status = 0
		}
		finish(ports.Observation{StatusCode: status, Error: err})
		if err == nil {
			budget.Lock()
			budget.failures = 0
			budget.interval = max(250*time.Millisecond, budget.interval*9/10)
			budget.Unlock()
			if sharedBudget != nil && platform != "other" {
				_ = sharedBudget.Success(ctx, budgetSource)
			}
			if attempt > 0 {
				_, recovered := observer.Start(ctx, "retry_recovered", platform)
				recovered(ports.Observation{Outcome: "recovered"})
			}
			return data, nil
		}
		last = err
		if status == 429 {
			last = &ports.SourceError{Kind: "rate_limited", StatusCode: 429, Err: errors.New("source rate limit")}
		}
		if status == 403 {
			last = &ports.SourceError{Kind: "blocked", StatusCode: 403, Err: errors.New("source access blocked")}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var network net.Error
		transient := (errors.As(err, &source) && (source.StatusCode == 429 || source.StatusCode == 502 || source.StatusCode == 503 || source.StatusCode == 504)) || errors.As(err, &network)
		if !transient || !safe {
			return nil, err
		}
		delay := time.Duration(1<<attempt)*250*time.Millisecond + time.Duration(rand.IntN(150))*time.Millisecond
		delay = max(delay, hint.delay)
		budget.Lock()
		budget.failures++
		budget.interval = min(4*time.Second, max(250*time.Millisecond, budget.interval*2))
		if budget.failures >= 3 {
			delay = max(delay, 5*time.Second)
		}
		until := time.Now().Add(delay)
		if until.After(budget.cooldown) {
			budget.cooldown = until
		}
		budget.Unlock()
		if sharedBudget != nil && platform != "other" {
			backoffCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			backoffErr := sharedBudget.Backoff(backoffCtx, budgetSource, delay.Milliseconds())
			cancel()
			if err := backoffErr; err != nil {
				return nil, &ports.SourceError{Kind: "budget_unavailable", Err: err}
			}
		}
		if attempt == 2 {
			break
		}
		_, done := observer.Start(ctx, "retry", platform)
		done(ports.Observation{Outcome: "scheduled"})
		if platform == "other" {
			if err := waitContext(ctx, delay); err != nil {
				return nil, err
			}
		}
	}
	return nil, last
}
