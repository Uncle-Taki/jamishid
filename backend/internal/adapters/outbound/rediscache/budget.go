package rediscache

import (
	"context"
	"github.com/redis/go-redis/v9"
	"time"
)

var budgetWait = redis.NewScript(`local cool=redis.call('PTTL',KEYS[1]);if cool>0 then return cool end;local interval=tonumber(redis.call('GET',KEYS[3]) or '250');if redis.call('SET',KEYS[2],'1','NX','PX',interval) then return 0 end;return math.max(1,redis.call('PTTL',KEYS[2]))`)
var budgetBackoff = redis.NewScript(`local interval=math.min(4000,math.max(250,tonumber(redis.call('GET',KEYS[2]) or '250')*2));redis.call('SET',KEYS[2],interval);local delay=math.max(tonumber(ARGV[1]),redis.call('PTTL',KEYS[1]));redis.call('SET',KEYS[1],'1','PX',math.max(1,delay));return 1`)
var budgetSuccess = redis.NewScript(`local interval=math.max(250,math.floor(tonumber(redis.call('GET',KEYS[1]) or '250')*0.9));redis.call('SET',KEYS[1],interval);return 1`)

func budgetKeys(source string) []string {
	base := prefix + "budget:" + source
	return []string{base + ":cooldown", base + ":slot", base + ":interval"}
}
func (c *Cache) Wait(ctx context.Context, source string) error {
	for {
		delay, err := budgetWait.Run(ctx, c.client, budgetKeys(source)).Int64()
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
	return budgetBackoff.Run(ctx, c.client, []string{keys[0], keys[2]}, delay).Err()
}
func (c *Cache) Success(ctx context.Context, source string) error {
	return budgetSuccess.Run(ctx, c.client, []string{budgetKeys(source)[2]}).Err()
}
