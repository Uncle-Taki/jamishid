package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

type savedItem struct {
	platform  vehicle.Platform
	listingID string
	savedAt   time.Time
	data      vehicle.NormalizedVehicleListing
}

type UserStore struct {
	mu           sync.RWMutex
	usersByID    map[string]*domainUser.User
	usersByIdent map[string]*domainUser.User
	saved        map[string]map[string]savedItem // userID -> "platform:listingID" -> savedItem
}

func NewUserStore() *UserStore {
	return &UserStore{
		usersByID:    make(map[string]*domainUser.User),
		usersByIdent: make(map[string]*domainUser.User),
		saved:        make(map[string]map[string]savedItem),
	}
}

func (s *UserStore) CreateUser(_ context.Context, u *domainUser.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.usersByIdent[u.Identifier]; exists {
		return domainUser.ErrUserAlreadyExists
	}
	s.usersByID[u.ID] = u
	s.usersByIdent[u.Identifier] = u
	return nil
}

func (s *UserStore) FindUserByID(_ context.Context, id string) (*domainUser.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, exists := s.usersByID[id]
	if !exists {
		return nil, domainUser.ErrUserNotFound
	}
	return u, nil
}

func (s *UserStore) FindUserByIdentifier(_ context.Context, identifier string) (*domainUser.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	u, exists := s.usersByIdent[identifier]
	if !exists {
		return nil, nil
	}
	return u, nil
}

func (s *UserStore) SaveListing(_ context.Context, userID string, platform vehicle.Platform, listingID string, rawData []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	userSaved, exists := s.saved[userID]
	if !exists {
		userSaved = make(map[string]savedItem)
		s.saved[userID] = userSaved
	}

	key := fmt.Sprintf("%s:%s", platform, listingID)
	var listing vehicle.NormalizedVehicleListing
	if len(rawData) > 0 {
		_ = json.Unmarshal(rawData, &listing)
	}
	listing.Platform = platform
	listing.ID = listingID

	userSaved[key] = savedItem{
		platform:  platform,
		listingID: listingID,
		savedAt:   time.Now().UTC(),
		data:      listing,
	}
	return nil
}

func (s *UserStore) RemoveSavedListing(_ context.Context, userID string, platform vehicle.Platform, listingID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if userSaved, exists := s.saved[userID]; exists {
		key := fmt.Sprintf("%s:%s", platform, listingID)
		delete(userSaved, key)
	}
	return nil
}

func (s *UserStore) GetSavedListings(_ context.Context, userID string, limit, offset int) ([]vehicle.NormalizedVehicleListing, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	userSaved, exists := s.saved[userID]
	if !exists || len(userSaved) == 0 {
		return []vehicle.NormalizedVehicleListing{}, 0, nil
	}

	items := make([]savedItem, 0, len(userSaved))
	for _, it := range userSaved {
		items = append(items, it)
	}

	// Sort descending by savedAt
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].savedAt.After(items[i].savedAt) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}

	total := len(items)
	if offset >= total {
		return []vehicle.NormalizedVehicleListing{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}

	result := make([]vehicle.NormalizedVehicleListing, 0, end-offset)
	for _, it := range items[offset:end] {
		result = append(result, it.data)
	}
	return result, total, nil
}

func (s *UserStore) GetSavedListingIDs(_ context.Context, userID string) (map[string]bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	userSaved, exists := s.saved[userID]
	if !exists {
		return map[string]bool{}, nil
	}

	res := make(map[string]bool, len(userSaved))
	for k := range userSaved {
		res[k] = true
	}
	return res, nil
}
