package main

import (
	"context"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/postgres"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/telemetry"
)

// snapshotSource composes the read-only PostgreSQL and Redis snapshot queries
// into one telemetry snapshot. When Redis fails but PostgreSQL answers, the
// last-good Redis values are retained and marked through redis_up=0; the same
// applies in reverse for PostgreSQL service stats. Queue/inventory state is
// only replaced by a fully successful poll (the telemetry collector keeps the
// previous snapshot and marks staleness on any overall error).
type snapshotSource struct {
	store *postgres.Store
	cache *rediscache.Cache
	// lastRedis retains the last-good Redis service values and budget cooldowns.
	lastRedis     telemetry.RedisService
	lastCooldowns []telemetry.AgeRow
}

func (s *snapshotSource) Snapshot(ctx context.Context) (telemetry.Snapshot, error) {
	snap := telemetry.Snapshot{}
	queues, err := s.store.QueueCounts(ctx)
	if err != nil {
		return snap, err
	}
	for _, row := range queues {
		snap.QueueTasks = append(snap.QueueTasks, telemetry.QueueRow{Queue: row.Queue, Source: row.Source, State: row.State, Mode: row.Mode, Purpose: row.Purpose, Value: row.Count})
	}
	ages, err := s.store.OldestReadyAges(ctx)
	if err != nil {
		return snap, err
	}
	// Empty queues use the documented zero-age convention.
	readySeen := map[string]bool{}
	for _, row := range ages {
		readySeen[row.Queue+":"+row.Source] = true
		snap.OldestReady = append(snap.OldestReady, telemetry.AgeRow(row))
	}
	for _, queue := range []string{"page", "detail"} {
		for _, source := range rediscache.Sources {
			if !readySeen[queue+":"+source] {
				snap.OldestReady = append(snap.OldestReady, telemetry.AgeRow{Queue: queue, Source: source, Seconds: 0})
			}
		}
	}
	due, err := s.store.RetryNextDue(ctx)
	if err != nil {
		return snap, err
	}
	for _, queue := range []string{"retry_page", "retry_detail"} {
		snap.RetryNextDue = append(snap.RetryNextDue, telemetry.AgeRow{Queue: queue, Seconds: due[queue]})
	}
	inventory, err := s.store.InventoryCounts(ctx)
	if err != nil {
		return snap, err
	}
	for _, row := range inventory {
		snap.Inventory = append(snap.Inventory, telemetry.InventoryRow{Source: row.Source, Type: row.Type, Availability: row.Availability, Freshness: row.Freshness, Value: row.Count})
	}
	coverage, err := s.store.Coverage(ctx)
	if err != nil {
		return snap, err
	}
	for _, row := range coverage {
		entry := telemetry.CoverageRow{
			Source: row.Platform, Type: row.Type,
			Discovered: float64(row.Partitions), Complete: float64(row.Complete),
			Overdue: float64(row.Overdue), Active: float64(row.Active),
			CatalogError: row.CatalogError != "",
		}
		if row.LastDiscovery != nil {
			entry.LastSuccess = float64(row.LastDiscovery.Unix())
		}
		snap.Coverage = append(snap.Coverage, entry)
	}
	proxies, err := s.store.ProxyCounts(ctx)
	if err != nil {
		return snap, err
	}
	for _, row := range proxies {
		snap.Proxies = append(snap.Proxies, telemetry.QueueRow{State: row.State, Value: row.Count})
	}
	stats, err := s.store.ServiceStats(ctx)
	if err != nil {
		return snap, err
	}
	snap.Postgres = telemetry.PostgresService{
		Up: true, Connections: stats.Connections,
		XactCommit: stats.XactCommit, XactRollback: stats.XactRollback,
	}
	sizes, err := s.store.StorageSizes(ctx)
	if err != nil {
		return snap, err
	}
	snap.Postgres.Storage = sizes

	if s.cache != nil {
		redisStats, redisErr := s.cache.MonitoringStats(ctx)
		if redisErr != nil {
			// Retain last-good values; availability marks the outage.
			snap.Redis = s.lastRedis
			snap.Redis.Up = false
			snap.BudgetCooldowns = s.lastCooldowns
		} else {
			snap.Redis = telemetry.RedisService{
				Up: true, UsedMemory: redisStats.UsedMemory, MaxMemory: redisStats.MaxMemory,
				Hits: redisStats.Hits, Misses: redisStats.Misses, Evicted: redisStats.Evicted, Clients: redisStats.Clients,
			}
			s.lastRedis = snap.Redis
			snap.BudgetCooldowns = nil
			for _, source := range rediscache.Sources {
				snap.BudgetCooldowns = append(snap.BudgetCooldowns, telemetry.AgeRow{Queue: "budget", Source: source, Seconds: redisStats.Cooldowns[source]})
			}
			s.lastCooldowns = snap.BudgetCooldowns
		}
	}
	return snap, nil
}
