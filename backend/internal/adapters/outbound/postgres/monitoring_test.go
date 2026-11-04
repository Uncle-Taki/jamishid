package postgres

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestQueueSnapshotDerivation seeds every durable queue category and proves the
// monitoring SQL matches the real claim/release semantics. Run against a
// dedicated, disposable database; never point this at application data.
func TestQueueSnapshotDerivation(t *testing.T) {
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
	exec("TRUNCATE crawl_searches,crawl_jobs,crawl_tasks,listings,search_listings,listing_observations,detail_tasks,listing_events,job_details,crawler_retries,coverage_catalogs,egress_proxies CASCADE")

	seed := func(id string) {
		exec(`INSERT INTO crawl_searches(id,criteria) VALUES($1,'{"query":"x"}')`, id)
	}
	job := func(id, search, status string) {
		exec(`INSERT INTO crawl_jobs(id,search_id,mode,status) VALUES($1,$2,'incremental',$3)`, id, search, status)
	}
	task := func(jobID, platform, status string, availableOffsetSeconds, leaseSeconds *int) {
		var available, lease any
		// available_at is NOT NULL; running and terminal rows carry a past
		// eligibility time that no derivation reads.
		offset := -60
		if availableOffsetSeconds != nil {
			offset = *availableOffsetSeconds
		}
		available = time.Now().Add(time.Duration(offset) * time.Second)
		if leaseSeconds != nil {
			lease = time.Now().Add(time.Duration(*leaseSeconds) * time.Second)
		}
		exec(`INSERT INTO crawl_tasks(job_id,platform,status,available_at,lease_until) VALUES($1,$2,$3,$4,$5)`, jobID, platform, status, available, lease)
	}
	past, future := -60, 3600

	// Page fixtures. crawl_tasks' primary key is (job_id, platform), so each
	// job contributes at most one task per platform.
	seed("s1")
	job("j1", "s1", "running")
	task("j1", "divar", "queued", &past, nil)       // ready
	task("j1", "bama", "queued", &future, nil)      // scheduled
	task("j1", "sheypoor", "running", nil, &future) // running_valid
	seed("s2")
	job("j2", "s2", "running")
	task("j2", "divar", "running", nil, &past)  // running_expired (reclaimable)
	task("j2", "bama", "complete", nil, nil)    // terminal
	task("j2", "sheypoor", "limited", nil, nil) // terminal
	seed("s5")
	job("j5", "s5", "running")
	task("j5", "divar", "unavailable", nil, nil) // terminal

	seed("s3")
	job("j3", "s3", "running")
	task("j3", "sheypoor", "delayed", &past, nil) // delayed + due ticket
	exec(`INSERT INTO crawler_retries(kind,platform,subject_id,job_id,error_kind,available_at) VALUES('page','sheypoor','j3','j3','http',now()-interval '5 minutes')`)
	task("j3", "divar", "delayed", &past, nil) // delayed + future ticket
	exec(`INSERT INTO crawler_retries(kind,platform,subject_id,job_id,error_kind,available_at) VALUES('page','divar','j3','j3','http',now()+interval '30 minutes')`)

	seed("s4")
	job("j4", "s4", "paused")
	task("j4", "bama", "queued", &past, nil)      // paused parent: not runnable
	task("j4", "sheypoor", "delayed", &past, nil) // paused parent: ticket blocked
	exec(`INSERT INTO crawler_retries(kind,platform,subject_id,job_id,error_kind,available_at) VALUES('page','sheypoor','j4','j4','http',now()-interval '5 minutes')`)

	// Orphaned page ticket: the task was requeued by a newer summary, so the
	// ticket no longer matches any delayed task.
	seed("s6")
	job("j6", "s6", "running")
	task("j6", "bama", "queued", &past, nil)
	exec(`INSERT INTO crawler_retries(kind,platform,subject_id,job_id,error_kind,available_at) VALUES('page','bama','j6','j6','http',now()-interval '5 minutes')`)

	// Detail fixtures.
	exec(`INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash) VALUES
('divar','d1','{}','t','car','tehran',1,1,1400,'perfect','h1'),
('divar','d2','{}','t','car','tehran',1,1,1400,'perfect','h2'),
('divar','d3','{}','t','car','tehran',1,1,1400,'perfect','h3'),
('bama','d4','{}','t','car','tehran',1,1,1400,'perfect','h4'),
('bama','d5','{}','t','car','tehran',1,1,1400,'perfect','h5'),
('sheypoor','d6','{}','t','car','tehran',1,1,1400,'perfect','h6'),
('sheypoor','d10','{}','t','car','tehran',1,1,1400,'perfect','h10')`)
	detail := func(platform, listing, status, purpose, hash string, availableOffsetSeconds, leaseSeconds *int) {
		var available, lease any
		offset := -60
		if availableOffsetSeconds != nil {
			offset = *availableOffsetSeconds
		}
		available = time.Now().Add(time.Duration(offset) * time.Second)
		if leaseSeconds != nil {
			lease = time.Now().Add(time.Duration(*leaseSeconds) * time.Second)
		}
		exec(`INSERT INTO detail_tasks(platform,listing_id,summary_hash,status,purpose,available_at,lease_until) VALUES($1,$2,$3,$4,$5,$6,$7)`, platform, listing, hash, status, purpose, available, lease)
	}
	detail("divar", "d1", "queued", "enrich", "h1", &past, nil)    // ready
	detail("divar", "d2", "queued", "verify", "h2", &future, nil)  // scheduled
	detail("divar", "d3", "running", "enrich", "h3", nil, &future) // running_valid
	detail("bama", "d4", "running", "verify", "h4", nil, &past)    // running_expired
	detail("bama", "d5", "delayed", "enrich", "h5", &past, nil)    // delayed + due ticket
	exec(`INSERT INTO crawler_retries(kind,platform,subject_id,listing_id,summary_hash,error_kind,available_at) VALUES('detail','bama','d5','d5','h5','http',now()-interval '5 minutes')`)
	detail("sheypoor", "d6", "complete", "verify", "h6", &past, nil) // terminal

	// Orphaned detail ticket: the task's summary hash changed after a newer
	// page observation, so the ticket can never requeue it.
	detail("sheypoor", "d10", "delayed", "enrich", "h10-new", &past, nil)
	exec(`INSERT INTO crawler_retries(kind,platform,subject_id,listing_id,summary_hash,error_kind,available_at) VALUES('detail','sheypoor','d10','d10','h10-old','parse',now()-interval '5 minutes')`)

	// Future detail ticket with its matching delayed task.
	exec(`INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash) VALUES('divar','d11','{}','t','car','tehran',1,1,1400,'perfect','h11')`)
	detail("divar", "d11", "delayed", "enrich", "h11", &past, nil)
	exec(`INSERT INTO crawler_retries(kind,platform,subject_id,listing_id,summary_hash,error_kind,available_at) VALUES('detail','divar','d11','d11','h11','http',now()+interval '30 minutes')`)

	// Inventory fixture: fresh, stale and unavailable listings.
	exec(`INSERT INTO listings(platform,id,data,normalized_text,vehicle_type,city,price,mileage,year,body,summary_hash,availability,last_seen) VALUES
('divar','d7','{}','t','car','tehran',1,1,1400,'perfect','h7','active',now()),
('divar','d8','{}','t','car','tehran',1,1,1400,'perfect','h8','unavailable',now()),
('bama','d9','{}','t','motorcycle','tehran',1,1,1400,'perfect','h9','active',now()-interval '9 days')`)

	queues, err := s.QueueCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, row := range queues {
		counts[row.Queue+"/"+row.Source+"/"+row.State+"/"+row.Mode+"/"+row.Purpose] += row.Count
	}
	expected := map[string]float64{
		// Page queue.
		"page/divar/ready/incremental/none":            1,
		"page/bama/scheduled/incremental/none":         1,
		"page/sheypoor/running_valid/incremental/none": 1,
		"page/divar/running_expired/incremental/none":  1,
		"page/bama/complete/incremental/none":          1,
		"page/sheypoor/limited/incremental/none":       1,
		"page/divar/unavailable/incremental/none":      1,
		"page/sheypoor/delayed/incremental/none":       1, // j3 delayed + due ticket
		"page/divar/delayed/incremental/none":          1, // j3 delayed + future ticket
		"page/bama/paused/incremental/none":            1, // j4 queued task under paused parent
		"page/sheypoor/paused/incremental/none":        1, // j4 delayed task under paused parent
		"page/bama/ready/incremental/none":             1, // j6 requeued task
		// Retry tickets. A delayed task and its ticket are the same pending
		// work; the fixture proves both sides of the relationship.
		"retry_page/sheypoor/due/none/none":        1, // j3 ticket
		"retry_page/divar/future/none/none":        1, // j3 ticket
		"retry_page/sheypoor/paused/none/none":     1, // j4 ticket blocked by paused parent
		"retry_page/bama/orphaned/none/none":       1, // j6 ticket without a delayed task
		"retry_detail/bama/due/none/none":          1,
		"retry_detail/divar/future/none/none":      1,
		"retry_detail/sheypoor/orphaned/none/none": 1, // stale hash can never requeue
		// Detail queue.
		"detail/divar/ready/none/enrich":          1,
		"detail/divar/scheduled/none/verify":      1,
		"detail/divar/running_valid/none/enrich":  1,
		"detail/bama/running_expired/none/verify": 1,
		"detail/bama/delayed/none/enrich":         1,
		"detail/divar/delayed/none/enrich":        1,
		"detail/sheypoor/complete/none/verify":    1,
		"detail/sheypoor/delayed/none/enrich":     1,
	}
	for key, want := range expected {
		if counts[key] != want {
			t.Errorf("queue %s = %v, want %v", key, counts[key], want)
		}
	}
	for key, got := range counts {
		if _, ok := expected[key]; !ok {
			t.Errorf("unexpected queue series %s = %v", key, got)
		}
	}

	// Oldest ready ages: only genuinely eligible work counts; paused parents
	// are excluded and empty (source, queue) pairs are zero by convention.
	ages, err := s.OldestReadyAges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ageBy := map[string]float64{}
	for _, row := range ages {
		ageBy[row.Queue+"/"+row.Source] = row.Seconds
	}
	if age, ok := ageBy["page/divar"]; !ok || age < 50 || age > 70 {
		t.Errorf("page/divar ready age %v (%v)", age, ok)
	}
	if age, ok := ageBy["page/bama"]; !ok || age < 50 || age > 70 {
		t.Errorf("page/bama ready age %v (%v)", age, ok)
	}
	if _, ok := ageBy["page/sheypoor"]; ok {
		t.Error("paused sheypoor parent must not appear as ready")
	}
	if age, ok := ageBy["detail/divar"]; !ok || age < 50 || age > 70 {
		t.Errorf("detail/divar ready age %v (%v)", age, ok)
	}

	// Next due timestamps for future tickets only.
	due, err := s.RetryNextDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := due["retry_detail"]; !ok || v <= 0 {
		t.Errorf("retry_detail next due %v (%v)", v, ok)
	}
	if v, ok := due["retry_page"]; !ok || v <= 0 {
		t.Errorf("retry_page next due %v (%v)", v, ok)
	}

	// Inventory counts: availability and freshness classification.
	inventory, err := s.InventoryCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inventoryBy := map[string]float64{}
	for _, row := range inventory {
		inventoryBy[row.Source+"/"+row.Type+"/"+row.Availability+"/"+row.Freshness] += row.Count
	}
	if inventoryBy["divar/car/active/fresh"] != 5 { // d1-d3 and d11 defaults plus d7
		t.Errorf("divar fresh active = %v", inventoryBy["divar/car/active/fresh"])
	}
	if inventoryBy["divar/car/unavailable/fresh"] != 1 {
		t.Errorf("divar unavailable fresh = %v", inventoryBy["divar/car/unavailable/fresh"])
	}
	if inventoryBy["bama/motorcycle/active/stale"] != 1 {
		t.Errorf("bama stale = %v", inventoryBy["bama/motorcycle/active/stale"])
	}
}
