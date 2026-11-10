package memory

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func TestUserStore_Lifecycle(t *testing.T) {
	ctx := context.Background()
	store := NewUserStore()

	// 1. Find nonexistent user
	if _, err := store.FindUserByID(ctx, "nonexistent"); err != domainUser.ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
	u, err := store.FindUserByIdentifier(ctx, "alice")
	if err != nil || u != nil {
		t.Fatalf("expected (nil, nil), got (%v, %v)", u, err)
	}

	// 2. Create user
	user1 := &domainUser.User{
		ID:           "user-1",
		Identifier:   "alice",
		PasswordHash: "hashed-pass",
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	if err := store.CreateUser(ctx, user1); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Duplicate creation
	if err := store.CreateUser(ctx, user1); err != domainUser.ErrUserAlreadyExists {
		t.Fatalf("expected ErrUserAlreadyExists, got %v", err)
	}

	// 3. Find created user
	found, err := store.FindUserByID(ctx, "user-1")
	if err != nil || found.Identifier != "alice" {
		t.Fatalf("failed to find user by ID: %v", err)
	}
	foundByIdent, err := store.FindUserByIdentifier(ctx, "alice")
	if err != nil || foundByIdent.ID != "user-1" {
		t.Fatalf("failed to find user by identifier: %v", err)
	}

	// 4. Saved Listings: initially empty
	ids, err := store.GetSavedListingIDs(ctx, "user-1")
	if err != nil || len(ids) != 0 {
		t.Fatalf("expected empty ids, got %v", ids)
	}
	listings, total, err := store.GetSavedListings(ctx, "user-1", 10, 0)
	if err != nil || total != 0 || len(listings) != 0 {
		t.Fatalf("expected empty listings, got total=%d len=%d", total, len(listings))
	}

	// 5. Save Listing with json data
	sampleListing := vehicle.NormalizedVehicleListing{
		Title:      "پژو 206",
		PriceToman: 450000000,
	}
	raw, _ := json.Marshal(sampleListing)
	if err := store.SaveListing(ctx, "user-1", vehicle.PlatformBama, "listing-1", raw); err != nil {
		t.Fatalf("failed to save listing: %v", err)
	}

	// Save another without rawData
	time.Sleep(10 * time.Millisecond)
	if err := store.SaveListing(ctx, "user-1", vehicle.PlatformDivar, "listing-2", nil); err != nil {
		t.Fatalf("failed to save listing 2: %v", err)
	}

	// Verify IDs
	ids, err = store.GetSavedListingIDs(ctx, "user-1")
	if err != nil || len(ids) != 2 || !ids["bama:listing-1"] || !ids["divar:listing-2"] {
		t.Fatalf("unexpected saved IDs: %v", ids)
	}

	// Verify pagination & ordering (listing-2 saved later, so comes first)
	listings, total, err = store.GetSavedListings(ctx, "user-1", 1, 0)
	if err != nil || total != 2 || len(listings) != 1 {
		t.Fatalf("expected 1 listing, got %d (total=%d)", len(listings), total)
	}
	if listings[0].ID != "listing-2" {
		t.Fatalf("expected newest listing-2 first, got %s", listings[0].ID)
	}

	// Offset out of range
	listings, total, err = store.GetSavedListings(ctx, "user-1", 10, 5)
	if err != nil || total != 2 || len(listings) != 0 {
		t.Fatalf("expected 0 listings for out of range offset, got %d", len(listings))
	}

	// Second page (offset + limit > total)
	listings, total, err = store.GetSavedListings(ctx, "user-1", 10, 1)
	if err != nil || total != 2 || len(listings) != 1 || listings[0].ID != "listing-1" {
		t.Fatalf("expected listing-1 on second page, got %v", listings)
	}

	// 6. Remove listing
	if err := store.RemoveSavedListing(ctx, "user-1", vehicle.PlatformBama, "listing-1"); err != nil {
		t.Fatalf("failed to remove listing: %v", err)
	}
	// Removing again or for nonexistent user shouldn't error
	_ = store.RemoveSavedListing(ctx, "user-1", vehicle.PlatformBama, "listing-1")
	_ = store.RemoveSavedListing(ctx, "nonexistent-user", vehicle.PlatformBama, "listing-1")

	ids, err = store.GetSavedListingIDs(ctx, "user-1")
	if err != nil || len(ids) != 1 || ids["bama:listing-1"] {
		t.Fatalf("expected only listing-2 left, got %v", ids)
	}
}
