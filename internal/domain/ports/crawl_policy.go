package ports

import "context"

type CrawlPolicy struct {
	Refresh        bool
	SkipDetails    bool
	RefreshDetails bool
}
type crawlPolicyKey struct{}

func WithCrawlPolicy(ctx context.Context, policy CrawlPolicy) context.Context {
	return context.WithValue(ctx, crawlPolicyKey{}, policy)
}
func CrawlPolicyFrom(ctx context.Context) CrawlPolicy {
	policy, _ := ctx.Value(crawlPolicyKey{}).(CrawlPolicy)
	return policy
}

type ProviderBudget interface {
	Wait(context.Context, string) error
	Backoff(context.Context, string, int64) error
	Success(context.Context, string) error
}
