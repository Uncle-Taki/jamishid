package ports

import (
	"context"
	"errors"
	"net"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

// Observer keeps instrumentation dependencies outside application workflows.
type Observer interface {
	Start(context.Context, string, vehicle.Platform) (context.Context, func(Observation))
}

type Observation struct {
	Cache      string
	Outcome    string
	Route      string
	StatusCode int
	Listings   int
	Error      error
}

type NoopObserver struct{}

func (NoopObserver) Start(ctx context.Context, _ string, _ vehicle.Platform) (context.Context, func(Observation)) {
	return ctx, func(Observation) {}
}

// SourceError describes transport/validation failures without exposing request URLs.
type SourceError struct {
	Kind       string
	StatusCode int
	Err        error
}

func (e *SourceError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "source request failed: " + e.Kind
}
func (e *SourceError) Unwrap() error { return e.Err }

func FailureKind(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var source *SourceError
	if errors.As(err, &source) {
		return source.Kind
	}
	var network net.Error
	if errors.As(err, &network) {
		if network.Timeout() {
			return "timeout"
		}
		return "network"
	}
	return "other"
}
