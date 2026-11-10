package ports

import (
	"context"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type UserRepository interface {
	CreateUser(ctx context.Context, u *user.User) error
	FindUserByID(ctx context.Context, id string) (*user.User, error)
	FindUserByIdentifier(ctx context.Context, identifier string) (*user.User, error)
}

type SavedListingRepository interface {
	SaveListing(ctx context.Context, userID string, platform vehicle.Platform, listingID string, rawData []byte) error
	RemoveSavedListing(ctx context.Context, userID string, platform vehicle.Platform, listingID string) error
	GetSavedListings(ctx context.Context, userID string, limit, offset int) ([]vehicle.NormalizedVehicleListing, int, error)
	GetSavedListingIDs(ctx context.Context, userID string) (map[string]bool, error)
}

type TokenManager interface {
	GenerateToken(userID string, duration time.Duration) (string, error)
	ValidateToken(token string) (string, error)
}
