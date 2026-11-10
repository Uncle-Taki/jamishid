package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/jackc/pgx/v5"
)

type CatalogFetcher func(context.Context, vehicle.Platform, vehicle.VehicleType, string) ([]vehicle.Category, error)

// RefreshCoverageCatalog claims one native brand catalog. Network I/O happens outside the transaction.
// A durable lease prevents duplicate discovery when multiple workers are running.
func (s *Store) RefreshCoverageCatalog(ctx context.Context, fetch CatalogFetcher) error {
	var platform vehicle.Platform
	var kind vehicle.VehicleType
	token := id()
	err := s.Pool.QueryRow(ctx, `UPDATE coverage_catalogs SET token=$1,lease_until=now()+interval '90 seconds' WHERE (platform,vehicle_type)=(SELECT platform,vehicle_type FROM coverage_catalogs WHERE next_refresh<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_refresh,platform,vehicle_type FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING platform,vehicle_type`, token).Scan(&platform, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	categories, fetchErr := fetch(fetchCtx, platform, kind, "")
	cancel()
	if fetchErr != nil || len(categories) == 0 {
		_, err = s.Pool.Exec(ctx, `UPDATE coverage_catalogs SET token=NULL,lease_until=NULL,next_refresh=now()+interval '15 minutes',error_kind='catalog' WHERE token=$1`, token)
		if fetchErr != nil {
			return fetchErr
		}
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var current string
	if err = tx.QueryRow(ctx, `SELECT token FROM coverage_catalogs WHERE platform=$1 AND vehicle_type=$2 FOR UPDATE`, platform, kind).Scan(&current); err != nil {
		return err
	}
	if current != token {
		return nil
	}
	for _, category := range categories {
		if category.ID == "" {
			continue
		}
		criteria := vehicle.SearchCriteria{Type: kind, Platform: &platform, CitySlug: "iran", NativeCategory: category.ID}
		searchID, criteria := crawljobs.SearchIdentity(criteria)
		raw, _ := json.Marshal(criteria)
		hash := sha256.Sum256([]byte(searchID))
		// Stable staggering survives redeployments, independent of demand or user activity.
		offset := int(binary.BigEndian.Uint32(hash[:4]) % (48 * 3600))
		if _, err = tx.Exec(ctx, `INSERT INTO crawl_searches(id,criteria,coverage,next_refresh) VALUES($1,$2,true,now()+make_interval(secs=>$3)) ON CONFLICT(id) DO UPDATE SET coverage=true`, searchID, raw, offset); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(categories)
	_, err = tx.Exec(ctx, `UPDATE coverage_catalogs SET categories=$2,last_success=now(),next_refresh=now()+interval '48 hours',error_kind='',token=NULL,lease_until=NULL WHERE token=$1`, token, raw)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type CoverageStatus struct {
	Platform      string     `json:"platform"`
	Type          string     `json:"type"`
	Partitions    int        `json:"partitions"`
	Complete      int        `json:"complete"`
	Overdue       int        `json:"overdue"`
	Active        int        `json:"active"`
	LastDiscovery *time.Time `json:"lastDiscovery"`
	CatalogError  string     `json:"catalogError"`
}

func (s *Store) Coverage(ctx context.Context) ([]CoverageStatus, error) {
	rows, err := s.Pool.Query(ctx, `SELECT c.platform,c.vehicle_type,count(s.id),count(s.id) FILTER(WHERE s.last_full>now()-interval '48 hours'),count(s.id) FILTER(WHERE (s.last_full IS NULL AND s.requested_at<now()-interval '48 hours') OR (s.last_full IS NOT NULL AND s.last_full<now()-interval '48 hours')),count(s.id) FILTER(WHERE EXISTS(SELECT 1 FROM crawl_jobs j WHERE j.search_id=s.id AND j.status IN ('queued','running','paused'))),c.last_success,c.error_kind FROM coverage_catalogs c LEFT JOIN crawl_searches s ON s.coverage AND s.criteria->>'platform'=c.platform AND s.criteria->>'type'=c.vehicle_type GROUP BY c.platform,c.vehicle_type,c.last_success,c.error_kind ORDER BY c.platform,c.vehicle_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CoverageStatus{}
	for rows.Next() {
		var row CoverageStatus
		if err = rows.Scan(&row.Platform, &row.Type, &row.Partitions, &row.Complete, &row.Overdue, &row.Active, &row.LastDiscovery, &row.CatalogError); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
