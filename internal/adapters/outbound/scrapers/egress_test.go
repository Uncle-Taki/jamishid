package scrapers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type testEgress struct {
	endpoint string
	err      error
	success  bool
}

func (e *testEgress) AcquireEgress(context.Context) (string, string, error) {
	return "test", e.endpoint, e.err
}
func (e *testEgress) ReportEgress(_ context.Context, _ string, success bool) error {
	e.success = success
	return nil
}
func TestSharedClientUsesManagedProxyAndDoesNotFallback(t *testing.T) {
	var requests int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Host != "divar.ir" {
			t.Errorf("wrong target %s", r.URL.Host)
		}
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	pool := &testEgress{endpoint: proxy.URL}
	previous := egressPool
	ConfigureEgressPool(pool)
	defer ConfigureEgressPool(previous)
	data, err := NewSharedClient().fetchOnce(context.Background(), "GET", "http://divar.ir/s/iran/car", "")
	if err != nil || string(data) != "proxied" || !pool.success || requests != 1 {
		t.Fatalf("proxy: %s %v success=%t", data, err, pool.success)
	}
	pool.err = errors.New("pool unavailable")
	if _, err = NewSharedClient().fetchOnce(context.Background(), "GET", "http://divar.ir/s/iran/car", ""); err == nil || requests != 1 {
		t.Fatal("unavailable pool did not fail closed")
	}
}
