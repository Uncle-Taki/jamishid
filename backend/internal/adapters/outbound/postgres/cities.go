package postgres

import (
	"context"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/jackc/pgx/v5"
)

// Previous scrapers stamped the requested city onto every Bama result. Repair
// current facts from source labels, retaining immutable historical snapshots.
func repairBamaCities(ctx context.Context, tx pgx.Tx) error {
	lastID := ""
	for {
		rows, err := tx.Query(ctx, `SELECT id,coalesce(data->>'neighborhoodFa','') FROM listings WHERE platform='bama' AND id>$1 ORDER BY id LIMIT 500`, lastID)
		if err != nil {
			return err
		}
		type entry struct{ id, location string }
		batch := []entry{}
		for rows.Next() {
			var item entry
			if err = rows.Scan(&item.id, &item.location); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, item)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, item := range batch {
			city := vehicle.CityFromLocation(item.location)
			if _, err = tx.Exec(ctx, `UPDATE listings SET city=$2,data=jsonb_set(data,'{cityId}',to_jsonb($2::text)),provenance=provenance-'cityId' WHERE platform='bama' AND id=$1`, item.id, city); err != nil {
				return err
			}
			lastID = item.id
		}
	}
}
