package postgres

import (
	"context"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/jackc/pgx/v5"
)

// Batched keyset traversal keeps migration memory bounded and index updates atomic.
func reindexNormalization(ctx context.Context, tx pgx.Tx) error {
	platform, id := "", ""
	for {
		rows, err := tx.Query(ctx, `SELECT platform,id,data->>'title',coalesce(data->>'brandModel','') FROM listings WHERE (platform,id)>($1,$2) ORDER BY platform,id LIMIT 500`, platform, id)
		if err != nil {
			return err
		}
		type item struct{ p, id, title, model string }
		batch := []item{}
		for rows.Next() {
			var value item
			if err = rows.Scan(&value.p, &value.id, &value.title, &value.model); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, value)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, value := range batch {
			if _, err = tx.Exec(ctx, `UPDATE listings SET normalized_text=$3,normalization_version=$4 WHERE platform=$1 AND id=$2`, value.p, value.id, vehicle.NormalizeModelText(value.title+" "+value.model), vehicle.NormalizationVersion()); err != nil {
				return err
			}
			platform, id = value.p, value.id
		}
	}
}
