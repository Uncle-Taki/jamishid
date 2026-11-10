package user

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	users         ports.UserRepository
	saved         ports.SavedListingRepository
	tokens        ports.TokenManager
	tokenDuration time.Duration
}

func NewService(users ports.UserRepository, saved ports.SavedListingRepository, tokens ports.TokenManager) *Service {
	return &Service{
		users:         users,
		saved:         saved,
		tokens:        tokens,
		tokenDuration: 30 * 24 * time.Hour, // 30 days
	}
}

func (s *Service) Register(ctx context.Context, identifier, password string) (*domainUser.User, string, error) {
	if err := domainUser.ValidateIdentifier(identifier); err != nil {
		return nil, "", err
	}
	if err := domainUser.ValidatePassword(password); err != nil {
		return nil, "", err
	}

	normalized := domainUser.NormalizeIdentifier(identifier)
	existing, err := s.users.FindUserByIdentifier(ctx, normalized)
	if err == nil && existing != nil {
		return nil, "", domainUser.ErrUserAlreadyExists
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", err
	}

	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, "", err
	}
	id := hex.EncodeToString(idBytes)

	now := time.Now().UTC()
	u := &domainUser.User{
		ID:           id,
		Identifier:   normalized,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	if err := s.users.CreateUser(ctx, u); err != nil {
		return nil, "", err
	}

	token, err := s.tokens.GenerateToken(u.ID, s.tokenDuration)
	if err != nil {
		return nil, "", err
	}

	return u, token, nil
}

func (s *Service) Login(ctx context.Context, identifier, password string) (*domainUser.User, string, error) {
	normalized := domainUser.NormalizeIdentifier(identifier)
	u, err := s.users.FindUserByIdentifier(ctx, normalized)
	if err != nil || u == nil {
		return nil, "", domainUser.ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, "", domainUser.ErrInvalidCredentials
	}

	token, err := s.tokens.GenerateToken(u.ID, s.tokenDuration)
	if err != nil {
		return nil, "", err
	}

	return u, token, nil
}

func (s *Service) GetProfile(ctx context.Context, userID string) (*domainUser.User, error) {
	u, err := s.users.FindUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, domainUser.ErrUserNotFound
	}
	return u, nil
}

func (s *Service) SaveListing(ctx context.Context, userID string, platform vehicle.Platform, listingID string, rawData []byte) error {
	if s.saved == nil {
		return domainUser.ErrUnauthorized
	}
	return s.saved.SaveListing(ctx, userID, platform, listingID, rawData)
}

func (s *Service) RemoveSavedListing(ctx context.Context, userID string, platform vehicle.Platform, listingID string) error {
	if s.saved == nil {
		return domainUser.ErrUnauthorized
	}
	return s.saved.RemoveSavedListing(ctx, userID, platform, listingID)
}

func (s *Service) GetSavedListings(ctx context.Context, userID string, limit, offset int) ([]vehicle.NormalizedVehicleListing, int, error) {
	if s.saved == nil {
		return []vehicle.NormalizedVehicleListing{}, 0, nil
	}
	if limit <= 0 {
		limit = 50
	}
	return s.saved.GetSavedListings(ctx, userID, limit, offset)
}

func (s *Service) GetSavedListingIDs(ctx context.Context, userID string) (map[string]bool, error) {
	if s.saved == nil {
		return map[string]bool{}, nil
	}
	return s.saved.GetSavedListingIDs(ctx, userID)
}
