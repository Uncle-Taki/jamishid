package ports

import (
	"context"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type ListingCacheRepository interface {
	Get(ctx context.Context, key string) ([]vehicle.ScoredListing, bool)
	Set(ctx context.Context, key string, listings []vehicle.ScoredListing) error
}
