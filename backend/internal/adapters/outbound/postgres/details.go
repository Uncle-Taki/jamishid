package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/jackc/pgx/v5"
	"time"
)

// knownFacts records only explicit facts; unknown sentinel values never acquire provenance.
func knownFacts(row vehicle.NormalizedVehicleListing) map[string]any {
	raw, _ := json.Marshal(row)
	var facts map[string]any
	json.Unmarshal(raw, &facts)
	for key, value := range facts {
		if key == "lifecycle" || key == "provenance" || key == "scrapedAt" || key == "detailsStatus" || key == "attributes" || key == "publishedAt" {
			delete(facts, key)
			continue
		}
		switch v := value.(type) {
		case string:
			if v == "" || v == "unknown" {
				delete(facts, key)
			}
		case float64:
			if (key == "mileageKm" && v < 0) || (key != "mileageKm" && v <= 0) {
				delete(facts, key)
			}
		}
	}
	return facts
}
func recordEvent(ctx context.Context, tx pgx.Tx, row vehicle.NormalizedVehicleListing, kind string, code int, failure string, updateFacts bool, supplied ...vehicle.NormalizedVehicleListing) error {
	row.Lifecycle = nil
	row.Provenance = nil
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	var observation int64
	var observed time.Time
	if err = tx.QueryRow(ctx, `INSERT INTO listing_events(platform,listing_id,kind,status_code,error_kind,data) VALUES($1,$2,$3,$4,$5,$6) RETURNING observation_id,observed_at`, row.Platform, row.ID, kind, code, failure, raw).Scan(&observation, &observed); err != nil {
		return err
	}
	if !updateFacts {
		return nil
	}
	provenance := map[string]vehicle.FactProvenance{}
	factRow := row
	if len(supplied) > 0 {
		factRow = supplied[0]
	}
	for field := range knownFacts(factRow) {
		provenance[field] = vehicle.FactProvenance{ObservationID: observation, Source: row.Platform, URL: row.URL, ObservedAt: observed.UTC().Format(time.RFC3339Nano), Kind: kind}
	}
	payload, _ := json.Marshal(provenance)
	_, err = tx.Exec(ctx, `UPDATE listings SET provenance=CASE WHEN $4='summary' THEN $3::jsonb ELSE provenance||$3::jsonb END WHERE platform=$1 AND id=$2`, row.Platform, row.ID, payload, kind)
	return err
}
func recordSummary(ctx context.Context, tx pgx.Tx, jobID string, observation crawljobs.Observation) error {
	row := observation.Listing
	if _, err := tx.Exec(ctx, `UPDATE listings SET availability='active' WHERE platform=$1 AND id=$2`, row.Platform, row.ID); err != nil {
		return err
	}
	if err := recordEvent(ctx, tx, row, "summary", 200, "", observation.NeedsDetails); err != nil {
		return err
	}
	if !observation.NeedsDetails {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO detail_tasks(platform,listing_id,summary_hash) VALUES($1,$2,$3) ON CONFLICT(platform,listing_id) DO UPDATE SET summary_hash=EXCLUDED.summary_hash,status=CASE WHEN detail_tasks.status IN ('running','delayed') AND detail_tasks.summary_hash=EXCLUDED.summary_hash THEN detail_tasks.status ELSE 'queued' END,attempts=CASE WHEN detail_tasks.status IN ('running','delayed') AND detail_tasks.summary_hash=EXCLUDED.summary_hash THEN detail_tasks.attempts ELSE 0 END,token=CASE WHEN detail_tasks.summary_hash=EXCLUDED.summary_hash THEN detail_tasks.token ELSE null END,available_at=now(),purpose='enrich'`, row.Platform, row.ID, observation.SummaryHash)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM crawler_retries WHERE kind='detail' AND platform=$1 AND listing_id=$2 AND summary_hash<>$3`, row.Platform, row.ID, observation.SummaryHash); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_details VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, jobID, row.Platform, row.ID)
	return err
}
func (s *Store) ClaimDetail(ctx context.Context) (crawljobs.DetailTask, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return crawljobs.DetailTask{}, false, err
	}
	defer tx.Rollback(ctx)
	var task crawljobs.DetailTask
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT l.data,d.summary_hash,d.attempts,d.purpose FROM detail_tasks d JOIN listings l ON l.platform=d.platform AND l.id=d.listing_id WHERE (d.status IN ('queued','retry') AND d.available_at<=now()) OR(d.status='running' AND d.lease_until<now()) ORDER BY d.available_at FOR UPDATE OF d SKIP LOCKED LIMIT 1`).Scan(&raw, &task.Hash, &task.Attempts, &task.Purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return task, false, nil
	}
	if err != nil {
		return task, false, err
	}
	if err = json.Unmarshal(raw, &task.Listing); err != nil {
		return task, false, err
	}
	task.Token = id()
	task.Attempts++
	_, err = tx.Exec(ctx, `UPDATE detail_tasks SET status='running',attempts=$3,token=$4,lease_until=now()+interval '45 seconds' WHERE platform=$1 AND listing_id=$2`, task.Listing.Platform, task.Listing.ID, task.Attempts, task.Token)
	if err != nil {
		return task, false, err
	}
	return task, true, tx.Commit(ctx)
}
func (s *Store) CompleteDetail(ctx context.Context, task crawljobs.DetailTask, row vehicle.NormalizedVehicleListing, code int, failure string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM inventory_revision WHERE id=true FOR UPDATE`); err != nil {
		return err
	}

	var hash string
	if err = tx.QueryRow(ctx, `SELECT summary_hash FROM listings WHERE platform=$1 AND id=$2 FOR UPDATE`, task.Listing.Platform, task.Listing.ID).Scan(&hash); err != nil {
		return err
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT token=$3 AND status='running' AND lease_until>now() AND summary_hash=$4 FROM detail_tasks WHERE platform=$1 AND listing_id=$2 FOR UPDATE`, task.Listing.Platform, task.Listing.ID, task.Token, task.Hash).Scan(&valid); err != nil {
		return err
	}
	if !valid || hash != task.Hash {
		return crawljobs.ErrLeaseLost
	}
	deferred := failure == "source_wait" || failure == "budget_unavailable" || failure == "egress_unavailable" || code == 429
	if deferred {
		if code == 429 {
			if _, err = tx.Exec(ctx, `UPDATE listings SET last_checked=now(),check_error=$3 WHERE platform=$1 AND id=$2`, task.Listing.Platform, task.Listing.ID, failure); err != nil {
				return err
			}
			if err = recordEvent(ctx, tx, task.Listing, "check", code, failure, false, task.Listing); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE detail_tasks SET status='retry',attempts=greatest(0,attempts-1),token=NULL,lease_until=NULL,error_kind=$3,available_at=now()+interval '1 minute' WHERE platform=$1 AND listing_id=$2`, task.Listing.Platform, task.Listing.ID, failure)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	state := "complete"
	available := "active"
	if failure != "" {
		available = ""
		if code == 404 || code == 410 {
			available = "unavailable"
		} else if task.Attempts < 3 && (code == 0 || code == 429 || code >= 500) && failure != "parser" {
			state = "retry"
		} else {
			state = "failed"
		}
	}
	if state == "failed" && transientRetry(failure, code) {
		state = "delayed"
		if err = s.enqueueRetry(ctx, tx, "detail", string(task.Listing.Platform), task.Listing.ID, task.Hash, failure); err != nil {
			return err
		}
	}
	if failure == "" {
		row.Lifecycle = nil
		row.Provenance = nil
		row.DetailsStatus = "complete"
		raw, _ := json.Marshal(row)
		_, err = tx.Exec(ctx, `UPDATE listings SET data=$3,normalized_text=$4,vehicle_type=$5,city=$6,price=$7,mileage=$8,year=$9,body=$10,details_at=now(),last_verified=now(),last_checked=now(),check_error='',availability='active' WHERE platform=$1 AND id=$2`, row.Platform, row.ID, raw, vehicle.NormalizeModelText(row.Title+" "+row.BrandModel), row.VehicleType, row.CityID, row.PriceToman, row.MileageKm, vehicle.ScoringYear(row.Year), row.BodyStatus)
	} else {
		_, err = tx.Exec(ctx, `UPDATE listings SET last_checked=now(),check_error=CASE WHEN $4='unavailable' THEN '' ELSE $3 END,availability=CASE WHEN $4='' THEN availability ELSE $4 END,last_verified=CASE WHEN $4='unavailable' THEN now() ELSE last_verified END WHERE platform=$1 AND id=$2`, row.Platform, row.ID, failure, available)
	}
	if err != nil {
		return err
	}
	kind := "detail"
	if failure != "" {
		kind = "check"
		row = task.Listing
	}
	facts := row
	if task.Facts != nil {
		facts = *task.Facts
	}
	if err = recordEvent(ctx, tx, row, kind, code, failure, failure == "", facts); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE detail_tasks SET status=$3,token=null,lease_until=null,error_kind=$4,available_at=now()+make_interval(secs=>$5) WHERE platform=$1 AND listing_id=$2`, task.Listing.Platform, task.Listing.ID, state, failure, task.Attempts*10)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Reverification is bounded and never infers removal from absence in search pages.
func (s *Store) ScheduleVerification(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO detail_tasks(platform,listing_id,summary_hash,purpose) SELECT l.platform,l.id,l.summary_hash,'verify' FROM listings l WHERE NOT EXISTS (SELECT 1 FROM detail_tasks d WHERE d.platform=l.platform AND d.listing_id=l.id AND d.status IN ('queued','running','retry','delayed')) AND l.last_seen>now()-interval '7 days' AND (l.last_checked IS NULL OR l.last_checked<now()-interval '48 hours') ORDER BY l.last_checked NULLS FIRST LIMIT 100 ON CONFLICT(platform,listing_id) DO UPDATE SET status='queued',attempts=0,available_at=now(),purpose='verify',summary_hash=EXCLUDED.summary_hash WHERE detail_tasks.status IN ('complete','failed')`)
	return err
}
func (s *Store) History(ctx context.Context, platform vehicle.Platform, listingID string, offset int) (crawljobs.ListingHistory, error) {
	result := crawljobs.ListingHistory{Events: []crawljobs.HistoryEvent{}, Offset: offset}
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT listing_payload(l) FROM listings l WHERE platform=$1 AND id=$2`, platform, listingID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, crawljobs.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if err = json.Unmarshal(raw, &result.Listing); err != nil {
		return result, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT observation_id,observed_at,kind,status_code,error_kind,data FROM listing_events WHERE platform=$1 AND listing_id=$2 ORDER BY observation_id DESC LIMIT 50 OFFSET $3`, platform, listingID, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var event crawljobs.HistoryEvent
		if err = rows.Scan(&event.ID, &event.At, &event.Kind, &event.StatusCode, &event.Error, &raw); err != nil {
			return result, err
		}
		if err = json.Unmarshal(raw, &event.Data); err != nil {
			return result, err
		}
		result.Events = append(result.Events, event)
	}
	return result, rows.Err()
}

func (s *Store) Recheck(ctx context.Context, platform vehicle.Platform, listingID string) error {
	result, err := s.Pool.Exec(ctx, `INSERT INTO detail_tasks(platform,listing_id,summary_hash,purpose) SELECT platform,id,summary_hash,'verify' FROM listings WHERE platform=$1 AND id=$2 ON CONFLICT(platform,listing_id) DO UPDATE SET status=CASE WHEN detail_tasks.status IN ('running','delayed') THEN detail_tasks.status ELSE 'queued' END,attempts=CASE WHEN detail_tasks.status IN ('running','delayed') THEN detail_tasks.attempts ELSE 0 END,available_at=now(),purpose='verify'`, platform, listingID)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return crawljobs.ErrNotFound
	}
	return nil
}
