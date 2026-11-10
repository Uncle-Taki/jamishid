package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/memory"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/tokens"
	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

func setupTestService(t *testing.T) (*Service, *tokens.HMACTokenManager) {
	t.Helper()
	store := memory.NewUserStore()
	tm, err := tokens.NewHMACTokenManager([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("failed to create token manager: %v", err)
	}
	svc := NewService(store, store, tm)
	return svc, tm
}

func TestUserService_RegisterAndLogin(t *testing.T) {
	svc, tm := setupTestService(t)
	ctx := context.Background()

	// Short identifier
	if _, _, err := svc.Register(ctx, "ab", "secret123"); err != domainUser.ErrInvalidIdentifier {
		t.Errorf("expected ErrInvalidIdentifier, got %v", err)
	}

	// Long identifier (> 254 chars)
	longIdentifier := ""
	for i := 0; i < 255; i++ {
		longIdentifier += "a"
	}
	if _, _, err := svc.Register(ctx, longIdentifier, "secret123"); err != domainUser.ErrInvalidIdentifier {
		t.Errorf("expected ErrInvalidIdentifier for > 254 chars, got %v", err)
	}

	// Short password
	if _, _, err := svc.Register(ctx, "alice", "123"); err != domainUser.ErrInvalidPassword {
		t.Errorf("expected ErrInvalidPassword, got %v", err)
	}

	// Long password (> 72 chars)
	longPassword := ""
	for i := 0; i < 73; i++ {
		longPassword += "p"
	}
	if _, _, err := svc.Register(ctx, "alice", longPassword); err != domainUser.ErrInvalidPassword {
		t.Errorf("expected ErrInvalidPassword for > 72 chars, got %v", err)
	}

	// Valid registration
	u, token, err := svc.Register(ctx, "Alice", "password123")
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}
	if u.Identifier != "alice" {
		t.Errorf("expected normalized identifier alice, got %s", u.Identifier)
	}
	if token == "" {
		t.Errorf("expected non-empty token")
	}

	// Token should validate
	sub, err := tm.ValidateToken(token)
	if err != nil {
		t.Fatalf("token validation failed: %v", err)
	}
	if sub != u.ID {
		t.Errorf("expected subject %s, got %s", u.ID, sub)
	}

	// Duplicate registration
	if _, _, err := svc.Register(ctx, "ALICE", "otherpass"); err != domainUser.ErrUserAlreadyExists {
		t.Errorf("expected ErrUserAlreadyExists, got %v", err)
	}

	// Login with wrong password
	if _, _, err := svc.Login(ctx, "alice", "wrongpassword"); err != domainUser.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}

	// Login with nonexistent identifier
	if _, _, err := svc.Login(ctx, "bob", "password123"); err != domainUser.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}

	// Successful login
	loggedInUser, loginToken, err := svc.Login(ctx, "alice", "password123")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if loggedInUser.ID != u.ID {
		t.Errorf("expected user ID %s, got %s", u.ID, loggedInUser.ID)
	}
	if loginToken == "" {
		t.Errorf("expected non-empty login token")
	}

	// Profile check
	profile, err := svc.GetProfile(ctx, u.ID)
	if err != nil {
		t.Fatalf("profile lookup failed: %v", err)
	}
	if profile.ID != u.ID || profile.Identifier != "alice" {
		t.Errorf("unexpected profile: %+v", profile)
	}
}

func TestUserService_SavedListings(t *testing.T) {
	svc, _ := setupTestService(t)
	ctx := context.Background()

	u, _, err := svc.Register(ctx, "car_lover", "secretpass")
	if err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Empty saved listings
	ids, err := svc.GetSavedListingIDs(ctx, u.ID)
	if err != nil {
		t.Fatalf("failed to get ids: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 ids, got %d", len(ids))
	}

	rawListing := []byte(`{"id":"d123","platform":"divar","title":"پژو ۲۰۶ تیپ ۲","priceToman":350000000,"year":1398}`)
	if err := svc.SaveListing(ctx, u.ID, vehicle.PlatformDivar, "d123", rawListing); err != nil {
		t.Fatalf("failed to save listing: %v", err)
	}

	// Check ids
	ids, err = svc.GetSavedListingIDs(ctx, u.ID)
	if err != nil {
		t.Fatalf("failed to get ids: %v", err)
	}
	if !ids["divar:d123"] {
		t.Errorf("expected divar:d123 to be saved")
	}

	// Retrieve saved listings
	items, total, err := svc.GetSavedListings(ctx, u.ID, 10, 0)
	if err != nil {
		t.Fatalf("failed to get saved listings: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected 1 item, got total=%d len=%d", total, len(items))
	}
	if items[0].Title != "پژو ۲۰۶ تیپ ۲" {
		t.Errorf("unexpected title: %s", items[0].Title)
	}

	// Remove listing
	if err := svc.RemoveSavedListing(ctx, u.ID, vehicle.PlatformDivar, "d123"); err != nil {
		t.Fatalf("failed to remove listing: %v", err)
	}

	ids, _ = svc.GetSavedListingIDs(ctx, u.ID)
	if ids["divar:d123"] {
		t.Errorf("expected divar:d123 to be removed")
	}

	// Pagination default limit (limit <= 0)
	_ = svc.SaveListing(ctx, u.ID, vehicle.PlatformDivar, "d123", rawListing)
	items, total, err = svc.GetSavedListings(ctx, u.ID, 0, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("expected 1 item with default limit, got %v", items)
	}

	// Service without saved repository
	svcNoSaved := NewService(memory.NewUserStore(), nil, nil)
	if err := svcNoSaved.SaveListing(ctx, "user-1", vehicle.PlatformDivar, "id", nil); err != domainUser.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
	if err := svcNoSaved.RemoveSavedListing(ctx, "user-1", vehicle.PlatformDivar, "id"); err != domainUser.ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
	noSavedItems, totalNoSaved, err := svcNoSaved.GetSavedListings(ctx, "user-1", 10, 0)
	if err != nil || len(noSavedItems) != 0 || totalNoSaved != 0 {
		t.Errorf("expected empty results without saved repo")
	}
	noSavedIDs, err := svcNoSaved.GetSavedListingIDs(ctx, "user-1")
	if err != nil || len(noSavedIDs) != 0 {
		t.Errorf("expected empty IDs without saved repo")
	}

	// Profile not found
	if _, err := svc.GetProfile(ctx, "nonexistent"); err != domainUser.ErrUserNotFound {
		t.Errorf("expected ErrUserNotFound for missing user profile, got %v", err)
	}
}

type errUserStore struct {
	createErr   error
	findByIDErr error
}

func (m *errUserStore) CreateUser(ctx context.Context, u *domainUser.User) error {
	return m.createErr
}
func (m *errUserStore) FindUserByID(ctx context.Context, id string) (*domainUser.User, error) {
	return nil, m.findByIDErr
}
func (m *errUserStore) FindUserByIdentifier(ctx context.Context, identifier string) (*domainUser.User, error) {
	return nil, nil
}

type errTokenManager struct {
	tokenErr error
}

func (m *errTokenManager) GenerateToken(subject string, duration time.Duration) (string, error) {
	return "", m.tokenErr
}
func (m *errTokenManager) ValidateToken(token string) (string, error) {
	return "", m.tokenErr
}

func TestUserService_StoreAndTokenErrors(t *testing.T) {
	ctx := context.Background()
	expErr := errors.New("database connection down")

	// CreateUser error
	storeErr := &errUserStore{createErr: expErr}
	tm, _ := tokens.NewHMACTokenManager([]byte("0123456789abcdef0123456789abcdef"))
	svc := NewService(storeErr, nil, tm)
	if _, _, err := svc.Register(ctx, "validuser", "validpassword"); err != expErr {
		t.Errorf("expected %v, got %v", expErr, err)
	}

	// GenerateToken error on Register
	memStore := memory.NewUserStore()
	badTM := &errTokenManager{tokenErr: expErr}
	svcBadTM := NewService(memStore, nil, badTM)
	if _, _, err := svcBadTM.Register(ctx, "validuser2", "validpassword"); err != expErr {
		t.Errorf("expected %v, got %v", expErr, err)
	}

	// GenerateToken error on Login
	// First register in memStore
	validSvc := NewService(memStore, nil, tm)
	_, _, _ = validSvc.Register(ctx, "validuser3", "validpassword")
	svcBadLoginTM := NewService(memStore, nil, badTM)
	if _, _, err := svcBadLoginTM.Login(ctx, "validuser3", "validpassword"); err != expErr {
		t.Errorf("expected token error on login, got %v", err)
	}

	// FindUserByID error on GetProfile
	storeFindErr := &errUserStore{findByIDErr: expErr}
	svcFindErr := NewService(storeFindErr, nil, tm)
	if _, err := svcFindErr.GetProfile(ctx, "user-123"); err != expErr {
		t.Errorf("expected %v, got %v", expErr, err)
	}

	// FindUserByID returns nil, nil (store returns nil without error)
	storeNil := &errUserStore{findByIDErr: nil}
	svfNil := NewService(storeNil, nil, tm)
	if _, err := svfNil.GetProfile(ctx, "user-nil"); err != domainUser.ErrUserNotFound {
		t.Errorf("expected ErrUserNotFound when store returns nil user, got %v", err)
	}
}
