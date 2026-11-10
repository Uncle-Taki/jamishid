package rediscache

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

var budgetWait = redis.NewScript(`local cool=redis.call('PTTL',KEYS[1]);if cool>0 then return cool end;local interval=math.max(tonumber(ARGV[1]),tonumber(redis.call('GET',KEYS[3]) or '250'));if redis.call('SET',KEYS[2],'1','NX','PX',interval) then return 0 end;return math.max(1,redis.call('PTTL',KEYS[2]))`)
var budgetBackoff = redis.NewScript(`local interval=math.min(4000,math.max(250,tonumber(redis.call('GET',KEYS[2]) or '250')*2));redis.call('SET',KEYS[2],interval);local delay=math.max(tonumber(ARGV[1]),redis.call('PTTL',KEYS[1]));redis.call('SET',KEYS[1],'1','PX',math.max(1,delay));return 1`)
var budgetSuccess = redis.NewScript(`local interval=math.max(250,math.floor(tonumber(redis.call('GET',KEYS[1]) or '250')*0.9));redis.call('SET',KEYS[1],interval);return 1`)

var budgetFloor atomic.Int64
var sourceFloors sync.Map

func SetBudgetFloor(interval time.Duration) { budgetFloor.Store(max(250, interval.Milliseconds())) }

func SetSourceBudgetFloor(source string, interval time.Duration) {
	sourceFloors.Store(source, max(250, interval.Milliseconds()))
}

func getBudgetFloor(source string) int64 {
	base := source
	if idx := strings.IndexByte(source, ':'); idx >= 0 {
		base = source[:idx]
	}
	if v, ok := sourceFloors.Load(base); ok {
		return v.(int64)
	}
	if global := budgetFloor.Load(); global > 0 {
		return global
	}
	switch base {
	case "divar":
		return 500
	case "bama":
		return 1200
	case "sheypoor":
		return 1500
	default:
		return 2000
	}
}

func budgetKeys(source string) []string {
	base := prefix + "budget:" + source
	return []string{base + ":cooldown", base + ":slot", base + ":interval"}
}
func (c *Cache) Wait(ctx context.Context, source string) error {
	floor := getBudgetFloor(source)
	for {
		delay, err := runScript(ctx, budgetWait, c.client, budgetKeys(source), floor).Int64()
		if err != nil {
			return err
		}
		if delay <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func (c *Cache) Backoff(ctx context.Context, source string, delay int64) error {
	keys := budgetKeys(source)
	return runScript(ctx, budgetBackoff, c.client, []string{keys[0], keys[2]}, delay).Err()
}
func (c *Cache) Success(ctx context.Context, source string) error {
	return runScript(ctx, budgetSuccess, c.client, []string{budgetKeys(source)[2]}).Err()
}
