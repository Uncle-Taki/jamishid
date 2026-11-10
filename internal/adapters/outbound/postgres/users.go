package postgres

import (
	"context"
	"encoding/json"
	"errors"

	domainUser "github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/user"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Store) CreateUser(ctx context.Context, u *domainUser.User) error {
	_, err := s.Pool.Exec(ctx,
		`INSERT INTO users(id, identifier, password_hash, created_at, updated_at) VALUES($1, $2, $3, $4, $5)`,
		u.ID, u.Identifier, u.PasswordHash, u.CreatedAt, u.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domainUser.ErrUserAlreadyExists
		}
		return err
	}
	return nil
}

func (s *Store) FindUserByID(ctx context.Context, id string) (*domainUser.User, error) {
	var u domainUser.User
	err := s.Pool.QueryRow(ctx,
		`SELECT id, identifier, password_hash, created_at, updated_at FROM users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.Identifier, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domainUser.ErrUserNotFound
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) FindUserByIdentifier(ctx context.Context, identifier string) (*domainUser.User, error) {
	var u domainUser.User
	err := s.Pool.QueryRow(ctx,
		`SELECT id, identifier, password_hash, created_at, updated_at FROM users WHERE identifier = $1`,
		identifier,
	).Scan(&u.ID, &u.Identifier, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &u, nil
}

func (s *Store) SaveListing(ctx context.Context, userID string, platform vehicle.Platform, listingID string, rawData []byte) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM listings WHERE platform=$1 AND id=$2)`, platform, listingID).Scan(&exists); err != nil {
		return err
	}

	if !exists && len(rawData) > 0 {
		var row vehicle.NormalizedVehicleListing
		if err := json.Unmarshal(rawData, &row); err == nil && row.ID != "" {
			normalizedText := vehicle.NormalizeModelText(row.Title + " " + row.BrandModel)
			_, _ = tx.Exec(ctx, `INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash,details_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,CASE WHEN $12 THEN now() ELSE null END)
				ON CONFLICT(platform,id) DO UPDATE SET data=EXCLUDED.data, last_seen=now()`,
				platform, listingID, rawData, normalizedText, row.VehicleType, row.CityID, row.PriceToman, row.MileageKm, vehicle.ScoringYear(row.Year), row.BodyStatus, "saved", row.DetailsStatus == "complete",
			)
		}
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO user_saved_listings(user_id, platform, listing_id, saved_at)
		VALUES($1, $2, $3, now())
		ON CONFLICT(user_id, platform, listing_id) DO NOTHING`,
		userID, platform, listingID,
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func (s *Store) RemoveSavedListing(ctx context.Context, userID string, platform vehicle.Platform, listingID string) error {
	_, err := s.Pool.Exec(ctx,
		`DELETE FROM user_saved_listings WHERE user_id = $1 AND platform = $2 AND listing_id = $3`,
		userID, platform, listingID,
	)
	return err
}

func (s *Store) GetSavedListingIDs(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.Pool.Query(ctx,
		`SELECT platform, listing_id FROM user_saved_listings WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	res := make(map[string]bool)
	for rows.Next() {
		var plat, id string
		if err := rows.Scan(&plat, &id); err != nil {
			return nil, err
		}
		res[plat+":"+id] = true
	}
	return res, rows.Err()
}

func (s *Store) GetSavedListings(ctx context.Context, userID string, limit, offset int) ([]vehicle.NormalizedVehicleListing, int, error) {
	var total int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM user_saved_listings WHERE user_id = $1`, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 || offset >= total {
		return []vehicle.NormalizedVehicleListing{}, total, nil
	}

	rows, err := s.Pool.Query(ctx,
		`SELECT listing_payload(l)
		FROM user_saved_listings s
		JOIN listings l ON l.platform = s.platform AND l.id = s.listing_id
		WHERE s.user_id = $1
		ORDER BY s.saved_at DESC
		LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var result []vehicle.NormalizedVehicleListing
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, 0, err
		}
		var item vehicle.NormalizedVehicleListing
		if err := json.Unmarshal(raw, &item); err == nil {
			result = append(result, item)
		}
	}
	return result, total, rows.Err()
}
