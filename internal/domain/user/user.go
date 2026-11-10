package user

import (
	"errors"
	"strings"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

var (
	ErrInvalidIdentifier  = errors.New("identifier must be at least 3 characters")
	ErrInvalidPassword    = errors.New("password must be at least 6 characters")
	ErrUserAlreadyExists  = errors.New("user with this identifier already exists")
	ErrUserNotFound       = errors.New("user not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthorized       = errors.New("unauthorized")
)

type User struct {
	ID           string    `json:"id"`
	Identifier   string    `json:"identifier"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type SavedListing struct {
	UserID    string                            `json:"userId"`
	Platform  vehicle.Platform                  `json:"platform"`
	ListingID string                            `json:"listingId"`
	SavedAt   time.Time                         `json:"savedAt"`
	Listing   *vehicle.NormalizedVehicleListing `json:"listing,omitempty"`
}

func NormalizeIdentifier(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func ValidateIdentifier(identifier string) error {
	normalized := NormalizeIdentifier(identifier)
	if len(normalized) < 3 || len(normalized) > 254 {
		return ErrInvalidIdentifier
	}
	return nil
}

func ValidatePassword(password string) error {
	if len(password) < 6 || len(password) > 72 {
		return ErrInvalidPassword
	}
	return nil
}
