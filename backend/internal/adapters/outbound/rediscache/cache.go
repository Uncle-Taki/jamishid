package rediscache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/redis/go-redis/v9"
)

const prefix = "jamshid:cache:v1:"
const leaseTTL = 30 * time.Second // Longer than the 15s page and 8s detail deadlines.

type Cache struct {
	client *redis.Client
	// probeClient is a separate, hook-free client used only by the per-poll
	// write probe. The diagnostic hook would otherwise report a probe OOM as a
	// synthetic developer-error event on every monitoring poll, polluting the
	// error groups the panel exists to show.
	probeClient *redis.Client
	ttl         time.Duration
	observer    ports.Observer
}

func New(rawURL string, ttl time.Duration, observer ports.Observer) (*Cache, error) {
	if ttl < time.Millisecond {
		return nil, errors.New("cache TTL must be positive")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_URL")
	}
	options.DialTimeout = time.Second
	options.ReadTimeout = time.Second
	options.WriteTimeout = time.Second
	options.ContextTimeoutEnabled = true
	options.MaxRetries = -1
	if observer == nil {
		observer = ports.NoopObserver{}
	}
	client := redis.NewClient(options)
	client.AddHook(diagnosticHook{})
	return &Cache{client: client, probeClient: redis.NewClient(options), ttl: ttl, observer: observer}, nil
}

func (c *Cache) Ping(ctx context.Context) error { return c.client.Ping(ctx).Err() }
func (c *Cache) Close() error {
	if err := c.client.Close(); err != nil {
		_ = c.probeClient.Close()
		return err
	}
	return c.probeClient.Close()
}

func cacheKey(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + kind + ":" + hex.EncodeToString(sum[:])
}

type envelope[T any] struct {
	Version int `json:"version"`
	Data    T   `json:"data"`
}

func read[T any](ctx context.Context, c *Cache, key string) (value T, found bool) {
	ctx, finish := c.observer.Start(ctx, "redis_read", "")
	raw, err := c.client.Get(ctx, key).Bytes()
	state := "miss"
	defer func() { finish(ports.Observation{Cache: state, Error: err}) }()
	if errors.Is(err, redis.Nil) {
		err = nil
		return value, false
	}
	if err != nil {
		return value, false
	}
	var stored envelope[T]
	if err = json.Unmarshal(raw, &stored); err != nil || stored.Version != 1 {
		if err == nil {
			err = errors.New("unsupported cache schema")
		}
		return value, false
	}
	var shape struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(raw, &shape)
	if len(shape.Data) == 0 || string(shape.Data) == "null" {
		err = errors.New("invalid cached data")
		return value, false
	}
	state = "hit"
	return stored.Data, true
}

func write[T any](ctx context.Context, c *Cache, key string, value T) error {
	ctx, finish := c.observer.Start(ctx, "redis_write", "")
	raw, err := json.Marshal(envelope[T]{Version: 1, Data: value})
	if err == nil {
		err = c.client.Set(ctx, key, raw, c.ttl).Err()
	}
	finish(ports.Observation{Error: err})
	return err
}

func (c *Cache) Get(ctx context.Context, key string) (vehicle.ListingPage, bool) {
	return read[vehicle.ListingPage](ctx, c, cacheKey("page", key))
}
func (c *Cache) Set(ctx context.Context, key string, page vehicle.ListingPage) error {
	return write(ctx, c, cacheKey("page", key), page)
}
func (c *Cache) GetDetail(ctx context.Context, key string) (vehicle.NormalizedVehicleListing, bool) {
	return read[vehicle.NormalizedVehicleListing](ctx, c, cacheKey("detail", key))
}

var releaseScript = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then return redis.call('DEL',KEYS[1]) else return 0 end`)

// Publish only while still owning the lease; a paused or expired owner cannot
// overwrite the result of a newer worker.
var publishScript = redis.NewScript(`if redis.call('GET',KEYS[1]) == ARGV[1] then redis.call('SET',KEYS[2],ARGV[2],'PX',ARGV[3]); return 1 else return 0 end`)

func load[T any](ctx context.Context, c *Cache, key string, fetch func(context.Context) (T, error), refresh ...bool) (zero T, cached bool, err error) {
	force := len(refresh) > 0 && refresh[0]
	for {
		if err = ctx.Err(); err != nil {
			return zero, false, err
		}
		if value, ok := read[T](ctx, c, key); ok && !force {
			return value, true, nil
		}
		var random [32]byte
		if _, err = rand.Read(random[:]); err != nil {
			return zero, false, err
		}
		token := hex.EncodeToString(random[:])
		lock := key + ":lease"
		acquired, lockErr := c.client.SetNX(ctx, lock, token, leaseTTL).Result()
		if lockErr != nil {
			return zero, false, &ports.SourceError{Kind: "cache_unavailable", Err: errors.New("shared cache unavailable")}
		}
		if !acquired {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, false, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		return func() (T, bool, error) {
			defer func() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_ = runScript(releaseCtx, releaseScript, c.client, []string{lock}, token).Err()
			}()
			// Close the race between observing a miss and acquiring the lease.
			if value, ok := read[T](ctx, c, key); ok && !force {
				return value, true, nil
			}
			value, err := fetch(ctx)
			if err != nil {
				return zero, false, err
			}
			if err = ctx.Err(); err != nil {
				return zero, false, err
			}
			raw, err := json.Marshal(envelope[T]{Version: 1, Data: value})
			if err != nil {
				return zero, false, err
			}
			_, finish := c.observer.Start(ctx, "redis_write", "")
			published, err := runScript(ctx, publishScript, c.client, []string{lock, key}, token, raw, c.ttl.Milliseconds()).Int()
			if err == nil && published != 1 {
				err = errors.New("shared fetch lease expired")
			}
			finish(ports.Observation{Error: err})
			if err != nil {
				return zero, false, &ports.SourceError{Kind: "cache_unavailable", Err: errors.New("shared cache publish failed")}
			}
			return value, false, nil
		}()
	}
}

func (c *Cache) LoadPage(ctx context.Context, key string, fetch func(context.Context) (vehicle.ListingPage, error)) (vehicle.ListingPage, bool, error) {
	return load(ctx, c, cacheKey("page", key), fetch)
}
func (c *Cache) LoadDetail(ctx context.Context, key string, fetch func(context.Context) (vehicle.NormalizedVehicleListing, error)) (vehicle.NormalizedVehicleListing, bool, error) {
	return load(ctx, c, cacheKey("detail", key), fetch)
}

func (c *Cache) RefreshPage(ctx context.Context, key string, fetch func(context.Context) (vehicle.ListingPage, error)) (vehicle.ListingPage, bool, error) {
	return load(ctx, c, cacheKey("page", key), fetch, true)
}
func (c *Cache) RefreshDetail(ctx context.Context, key string, fetch func(context.Context) (vehicle.NormalizedVehicleListing, error)) (vehicle.NormalizedVehicleListing, bool, error) {
	return load(ctx, c, cacheKey("detail", key), fetch, true)
}

// All instances use the same durable signing key, so cached continuation tokens
// remain valid on another instance and after an API restart.
const cursorSigningKeyName = prefix + "cursor-signing-key"

// CursorSigningKey reuses the shared durable key using a read-first path. A valid
// existing key is returned with a single read and no write, so a healthy Redis
// at its memory limit under noeviction does not block startup. Only a genuinely
// missing key triggers initialization (concurrent-safe: each process proposes a
// key with SetNX and then reads back the durable winner). A malformed stored key
// is refused rather than silently rotated, because rotation would invalidate
// cursors every instance already signed. Errors wrap the cause so an unreachable
// or unwritable Redis is distinguishable from a clean key lookup.
func (c *Cache) CursorSigningKey(ctx context.Context) ([]byte, error) {
	if encoded, err := c.client.Get(ctx, cursorSigningKeyName).Result(); err == nil {
		return validSigningKey(encoded)
	} else if !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("read shared cursor signing key: %w", err)
	}

	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, err
	}
	if err := c.client.SetNX(ctx, cursorSigningKeyName, hex.EncodeToString(key[:]), 0).Err(); err != nil {
		return nil, fmt.Errorf("initialize shared cursor signing key: %w", err)
	}
	encoded, err := c.client.Get(ctx, cursorSigningKeyName).Result()
	if err != nil {
		return nil, fmt.Errorf("read shared cursor signing key after init: %w", err)
	}
	return validSigningKey(encoded)
}

// validSigningKey decodes and length-checks a stored signing key. It does not
// replace a malformed key: callers treat rejection as a hard failure instead of
// silently re-keying the whole deployment.
func validSigningKey(encoded string) ([]byte, error) {
	decoded, err := hex.DecodeString(encoded)
	if err != nil || len(decoded) != 32 {
		return nil, errors.New("invalid shared cursor signing key")
	}
	return decoded, nil
}
