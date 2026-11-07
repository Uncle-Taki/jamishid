package postgres

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/application/crawljobs"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
)

// recordingMetrics captures store persistence transitions for assertions.
type recordingMetrics struct {
	mu          sync.Mutex
	scheduled   map[string]int64
	released    map[string]int64
	invalidated map[string]int64
	discarded   map[string]int64
	cacheWrites map[string]int64
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{
		scheduled:   map[string]int64{},
		released:    map[string]int64{},
		invalidated: map[string]int64{},
		discarded:   map[string]int64{},
		cacheWrites: map[string]int64{},
	}
}

func (r *recordingMetrics) RetryScheduled(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scheduled[kind]++
}
func (r *recordingMetrics) RetryReleased(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released[kind]++
}
func (r *recordingMetrics) RetryInvalidated(kind string, tickets int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated[kind] += tickets
}
func (r *recordingMetrics) RetryDiscarded(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.discarded[kind]++
}
func (r *recordingMetrics) InventoryCacheOutcome(string) {}
func (r *recordingMetrics) InventoryCacheWrite(outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cacheWrites[outcome]++
}
func (r *recordingMetrics) InventoryQuery() {}

// TestRetryInvalidatedCountsEveryTicket proves one page commit invalidating
// several detail tickets counts every removed ticket, not one per commit
// (review acceptance gap on retry counters).
func TestRetryInvalidatedCountsEveryTicket(t *testing.T) {
	s := coverageStore(t)
	metrics := newRecordingMetrics()
	s.Metrics = metrics
	ctx := context.Background()
	platform := vehicle.PlatformBama
	if _, err := s.Submit(ctx, vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar, Platform: &platform}, false); err != nil {
		t.Fatal(err)
	}
	page, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	// Two listings whose detail tasks are delayed with tickets on an old
	// summary hash; one page commit observes both with new hashes.
	seedListingWithTicket := func(id string) vehicle.NormalizedVehicleListing {
		row := vehicle.NormalizedVehicleListing{ID: id, Platform: platform, Title: "206", VehicleType: vehicle.VehicleTypeCar, PriceToman: 123, URL: "https://bama.ir/car/" + id}
		exec := func(sql string, args ...any) {
			t.Helper()
			if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
				t.Fatal(err)
			}
		}
		exec(`INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash) VALUES($1,$2,'{}',$3,'car','tehran',1,1,1400,'perfect',('old-'||$2))`, string(platform), id, id)
		exec(`INSERT INTO detail_tasks(platform,listing_id,summary_hash,status,purpose) VALUES($1,$2,$3,'delayed','enrich')`, string(platform), id, "old-"+id)
		exec(`INSERT INTO crawler_retries(kind,platform,subject_id,listing_id,summary_hash,error_kind,available_at) VALUES('detail',$1,$2,$2,$3,'http',now()-interval '1 minute')`, string(platform), id, "old-"+id)
		return row
	}
	observations := []crawljobs.Observation{}
	for _, id := range []string{"m1", "m2", "m3"} {
		row := seedListingWithTicket(id)
		observations = append(observations, crawljobs.Observation{Listing: row, SummaryHash: crawljobs.SummaryHash(row), NeedsDetails: true})
	}
	if err = s.Commit(ctx, page, observations, "", 1); err != nil {
		t.Fatal(err)
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.invalidated["detail"] != 3 {
		t.Fatalf("invalidated detail tickets = %d, want 3 (one per removed ticket, not one per commit)", metrics.invalidated["detail"])
	}
	var remaining int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM crawler_retries WHERE kind='detail'`).Scan(&remaining)
	if remaining != 0 {
		t.Fatalf("tickets not invalidated: %d", remaining)
	}
}

// TestRepeatedReleaseCountsOnce proves a repeated release attempt of the
// same due ticket counts nothing: the first attempt already reported the
// transition and the second deletes no row (review acceptance gap).
func TestRepeatedReleaseCountsOnce(t *testing.T) {
	s := coverageStore(t)
	metrics := newRecordingMetrics()
	s.Metrics = metrics
	ctx := context.Background()
	platform := vehicle.PlatformDivar
	job, err := s.Submit(ctx, vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar, Platform: &platform}, false)
	if err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.Claim(ctx)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.Commit(ctx, task, nil, "checkpoint", 1); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE crawl_tasks SET available_at=now(),attempts=2`)
	if task, ok, err = s.Claim(ctx); err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.Fail(ctx, task, "timeout"); err != nil {
		t.Fatal(err)
	}
	// Make the ticket due, then release it twice in a row.
	_, _ = s.Pool.Exec(ctx, `UPDATE crawler_retries SET available_at=now()-interval '1 second'`)
	for i := 0; i < 2; i++ {
		if err = s.releaseRetries(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// An orphaned ticket (its delayed task was requeued by a newer summary, so
	// no delayed task remains) is discarded once. The check constraint requires
	// page tickets to carry subject_id = job_id.
	if _, err = s.Pool.Exec(ctx, `INSERT INTO crawler_retries(kind,platform,subject_id,job_id,error_kind,available_at) VALUES('page',$1,$2,$2,'http',now()-interval '1 second')`, string(platform), job.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = s.releaseRetries(ctx); err != nil {
			t.Fatal(err)
		}
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.released["page"] != 1 {
		t.Fatalf("released page = %d, want exactly 1 for a repeated release", metrics.released["page"])
	}
	if metrics.discarded["page"] != 1 {
		t.Fatalf("discarded page = %d, want exactly 1 for a repeated orphaned release", metrics.discarded["page"])
	}
	var tickets int
	_ = s.Pool.QueryRow(ctx, `SELECT count(*) FROM crawler_retries`).Scan(&tickets)
	if tickets != 0 {
		t.Fatalf("tickets left behind: %d", tickets)
	}
}

// findCount sums the values of all rows matching the bounded label key, so the
// tests assert on exported values rather than internal row layout.
func sumBy(t *testing.T, rows []struct {
	Key string
	Val float64
}, key string) (float64, int) {
	t.Helper()
	var total float64
	matches := 0
	for _, r := range rows {
		if r.Key == key {
			total += r.Val
			matches++
		}
	}
	return total, matches
}

// TestBoundedLabelsMergeToSingleSeries proves two distinct raw stored values
// that normalize to the same bounded label set (for example two unknown
// vehicle types and two unknown availability states, or two unknown sources)
// are merged so the exported label set is unique. Duplicate label sets would
// fail the Prometheus registry gather and take down the whole /metrics
// response (review finding C2). The count must sum exactly once.
func TestBoundedLabelsMergeToSingleSeries(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`TRUNCATE crawl_searches,crawl_jobs,crawl_tasks,listings,search_listings,listing_observations,detail_tasks,listing_events,job_details,crawler_retries,egress_proxies CASCADE`)

	// Inventory: two distinct raw vehicle_type values ('scooter','truck') and
	// two distinct raw availability values ('sold','archived') all normalize to
	// 'other', collapsing to the same (source,type,availability,freshness) key.
	exec(`INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash,availability)
	VALUES('divar','a1','{}','t','scooter','tehran',1,1,1400,'perfect','ha','sold')`)
	exec(`INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash,availability)
	VALUES('divar','a2','{}','t','truck','tehran',1,1,1400,'perfect','hb','archived')`)
	inv, err := s.InventoryCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	total, matches := sumBy(t, toInvRows(inv), "divar|other|other|fresh")
	if matches != 1 {
		t.Fatalf("inventory must merge to a single (divar,other,other,fresh) series, got %d rows: %+v", matches, inv)
	}
	if total != 2 {
		t.Fatalf("inventory count must sum exactly once = 2, got %v", total)
	}

	// Queue: two distinct raw page sources that both normalize to 'other' must
	// merge into one (page,other,ready,incremental,none) row.
	exec(`INSERT INTO crawl_searches(id,criteria) VALUES('qm1','{"query":"x"}'),('qm2','{"query":"y"}')`)
	exec(`INSERT INTO crawl_jobs(id,search_id,mode,status) VALUES('jq1','qm1','incremental','running'),('jq2','qm2','incremental','running')`)
	past := time.Now().Add(-60 * time.Second)
	exec(`INSERT INTO crawl_tasks(job_id,platform,status,available_at) VALUES('jq1','divar-x','queued',$1)`, past)
	exec(`INSERT INTO crawl_tasks(job_id,platform,status,available_at) VALUES('jq2','bama-y','queued',$1)`, past)
	queues, err := s.QueueCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	qTotal, qMatches := sumBy(t, toQueueRows(queues), "page|other|ready|incremental|none")
	if qMatches != 1 {
		t.Fatalf("queue must merge to a single (page,other,ready,incremental,none) series, got %d rows: %+v", qMatches, queues)
	}
	if qTotal != 2 {
		t.Fatalf("queue count must sum exactly once = 2, got %v", qTotal)
	}

	// Detail: two distinct raw purposes ('x','y') that both normalize to
	// 'other' must merge into one (detail,divar,ready,none,other) row.
	exec(`INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash) VALUES('divar','d1','{}','t','car','tehran',1,1,1400,'perfect','h1'),('divar','d2','{}','t','car','tehran',1,1,1400,'perfect','h2')`)
	exec(`INSERT INTO detail_tasks(platform,listing_id,summary_hash,status,purpose,available_at) VALUES('divar','d1','h1','queued','x',$1),('divar','d2','h2','queued','y',$1)`, past)
	queues, err = s.QueueCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if dTotal, dMatches := sumBy(t, toQueueRows(queues), "detail|divar|ready|none|other"); dMatches != 1 || dTotal != 2 {
		t.Fatalf("detail must merge to a single (detail,divar,ready,none,other) row summing to 2, got matches=%d total=%v rows=%+v", dMatches, dTotal, queues)
	}
}

func toInvRows(rows []InventoryCount) []struct {
	Key string
	Val float64
} {
	out := make([]struct {
		Key string
		Val float64
	}, 0, len(rows))
	for _, r := range rows {
		out = append(out, struct {
			Key string
			Val float64
		}{Key: r.Source + "|" + r.Type + "|" + r.Availability + "|" + r.Freshness, Val: r.Count})
	}
	return out
}

func toQueueRows(rows []QueueCount) []struct {
	Key string
	Val float64
} {
	out := make([]struct {
		Key string
		Val float64
	}, 0, len(rows))
	for _, r := range rows {
		out = append(out, struct {
			Key string
			Val float64
		}{Key: r.Queue + "|" + r.Source + "|" + r.State + "|" + r.Mode + "|" + r.Purpose, Val: r.Count})
	}
	return out
}
