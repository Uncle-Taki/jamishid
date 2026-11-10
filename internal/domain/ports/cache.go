package ports

import (
	"context"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type ListingCacheRepository interface {
	Get(ctx context.Context, key string) (vehicle.ListingPage, bool)
	Set(ctx context.Context, key string, page vehicle.ListingPage) error
}

// Shared loaders coordinate a cold fetch across API processes, then store only
// successful results. The callback retains the caller's bounded fetch context.
type ListingPageLoader interface {
	LoadPage(context.Context, string, func(context.Context) (vehicle.ListingPage, error)) (vehicle.ListingPage, bool, error)
}

type ListingDetailCache interface {
	GetDetail(context.Context, string) (vehicle.NormalizedVehicleListing, bool)
	LoadDetail(context.Context, string, func(context.Context) (vehicle.NormalizedVehicleListing, error)) (vehicle.NormalizedVehicleListing, bool, error)
}

type ListingPageRefresher interface {
	RefreshPage(context.Context, string, func(context.Context) (vehicle.ListingPage, error)) (vehicle.ListingPage, bool, error)
}
type ListingDetailRefresher interface {
	RefreshDetail(context.Context, string, func(context.Context) (vehicle.NormalizedVehicleListing, error)) (vehicle.NormalizedVehicleListing, bool, error)
}
