package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestPostgresUserStore(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	s, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Clean tables
	_, err = s.Pool.Exec(ctx, "TRUNCATE user_saved_listings, users CASCADE")
	if err != nil {
		t.Fatalf("failed to truncate user tables: %v", err)
	}

	u := &domainUser.User{
		ID:           "test-user-uuid-1",
		Identifier:   "mehdi@example.com",
		PasswordHash: "hashed_pw_secret",
		CreatedAt:    time.Now().UTC().Truncate(time.Microsecond),
		UpdatedAt:    time.Now().UTC().Truncate(time.Microsecond),
	}

	// 1. CreateUser
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}

	// Duplicate CreateUser -> ErrUserAlreadyExists
	if err := s.CreateUser(ctx, u); !errors.Is(err, domainUser.ErrUserAlreadyExists) {
		t.Fatalf("expected ErrUserAlreadyExists, got %v", err)
	}

	// 2. FindUserByID
	found, err := s.FindUserByID(ctx, u.ID)
	if err != nil || found == nil || found.Identifier != u.Identifier {
		t.Fatalf("FindUserByID failed: %v, %+v", err, found)
	}

	// Not found ID -> ErrUserNotFound
	if _, err := s.FindUserByID(ctx, "nonexistent-id"); !errors.Is(err, domainUser.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}

	// 3. FindUserByIdentifier
	foundByIdent, err := s.FindUserByIdentifier(ctx, u.Identifier)
	if err != nil || foundByIdent == nil || foundByIdent.ID != u.ID {
		t.Fatalf("FindUserByIdentifier failed: %v, %+v", err, foundByIdent)
	}

	// Not found identifier -> returns nil, nil
	notfoundIdent, err := s.FindUserByIdentifier(ctx, "unknown@example.com")
	if err != nil || notfoundIdent != nil {
		t.Fatalf("expected nil, nil for unknown identifier; got %v, %+v", err, notfoundIdent)
	}

	// 4. SaveListing with rawData
	sampleListing := vehicle.NormalizedVehicleListing{
		ID:            "ad-101",
		Title:         "پژو 206 تیپ 2",
		BrandModel:    "peugeot 206",
		VehicleType:   vehicle.VehicleTypeCar,
		CityID:        "tehran",
		PriceToman:    350000000,
		MileageKm:     20000,
		Year:          1401,
		BodyStatus:    vehicle.BodyStatusPerfect,
		DetailsStatus: "complete",
	}
	rawData, _ := json.Marshal(sampleListing)

	if err := s.SaveListing(ctx, u.ID, vehicle.PlatformDivar, "ad-101", rawData); err != nil {
		t.Fatalf("SaveListing failed: %v", err)
	}

	// Save another listing
	sample2 := vehicle.NormalizedVehicleListing{
		ID:            "ad-102",
		Title:         "پراید 131",
		BrandModel:    "pride 131",
		VehicleType:   vehicle.VehicleTypeCar,
		CityID:        "tehran",
		PriceToman:    180000000,
		MileageKm:     50000,
		Year:          1398,
		BodyStatus:    vehicle.BodyStatusPerfect,
		DetailsStatus: "complete",
	}
	rawData2, _ := json.Marshal(sample2)
	if err := s.SaveListing(ctx, u.ID, vehicle.PlatformBama, "ad-102", rawData2); err != nil {
		t.Fatalf("SaveListing 2 failed: %v", err)
	}

	// 5. GetSavedListingIDs
	ids, err := s.GetSavedListingIDs(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetSavedListingIDs failed: %v", err)
	}
	if !ids["divar:ad-101"] || !ids["bama:ad-102"] {
		t.Fatalf("expected ids to contain divar:ad-101 and bama:ad-102, got %+v", ids)
	}

	// 6. GetSavedListings
	items, total, err := s.GetSavedListings(ctx, u.ID, 10, 0)
	if err != nil {
		t.Fatalf("GetSavedListings failed: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 items, got total=%d, len=%d", total, len(items))
	}

	// Pagination: offset >= total
	emptyItems, total2, err := s.GetSavedListings(ctx, u.ID, 10, 5)
	if err != nil || total2 != 2 || len(emptyItems) != 0 {
		t.Fatalf("expected empty list for high offset, got %v, %d, %d", err, total2, len(emptyItems))
	}

	// 7. RemoveSavedListing
	if err := s.RemoveSavedListing(ctx, u.ID, vehicle.PlatformDivar, "ad-101"); err != nil {
		t.Fatalf("RemoveSavedListing failed: %v", err)
	}

	idsAfterRemove, err := s.GetSavedListingIDs(ctx, u.ID)
	if err != nil || idsAfterRemove["divar:ad-101"] || !idsAfterRemove["bama:ad-102"] {
		t.Fatalf("expected ad-101 removed, got %+v", idsAfterRemove)
	}
}
