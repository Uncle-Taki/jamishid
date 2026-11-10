package ports

import "context"

// EgressPool leases an operator-managed outbound proxy, shared by all source clients.
// An empty endpoint means direct access. A pool error must never silently fall back to direct access.
type EgressPool interface {
	AcquireEgress(context.Context) (id, endpoint string, err error)
	ReportEgress(context.Context, string, bool) error
}
