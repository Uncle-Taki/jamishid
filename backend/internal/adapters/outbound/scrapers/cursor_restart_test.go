package scrapers

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/rediscache"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/vehicle"
	"github.com/alicebob/miniredis/v2"
)

// TestCursorSurvivesRedisRestartWithDurableKey is the real-cursor half of the
// "restart" acceptance contract (which the pressure harness cannot verify on its
// own): a continuation token issued under the durable key stored in Redis must
// still verify after a process restart re-reads that same key. It uses the real
// pagination cursor code and a real shared (miniredis) Redis for the key. The
// package-global signing key is saved and restored so other cursor tests are not
// affected.
func TestCursorSurvivesKeyReload(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	cache, err := rediscache.New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, err := cache.CursorSigningKey(ctx)
	if err != nil || len(key) != 32 {
		t.Fatalf("durable key: %v (%d bytes)", err, len(key))
	}

	savedKey := cursorKey
	defer func() { cursorKey = savedKey }()

	criteria := vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar}
	// Startup 1: configure with the durable key and issue a cursor.
	if err := ConfigureCursorSigningKey(key); err != nil {
		t.Fatal(err)
	}
	token := encodeCursor(vehicle.PlatformDivar, criteria, 2, divarContinuation{})

	// Restart: a fresh client against the same Redis reads back the same key and
	// reconfigures from it.
	cache.Close()
	restarted, err := rediscache.New("redis://"+server.Addr(), 24*time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	key2, err := restarted.CursorSigningKey(ctx)
	if err != nil {
		t.Fatalf("restart re-read: %v", err)
	}
	if !bytes.Equal(key, key2) {
		t.Fatal("restart read back a different signing key")
	}
	if err := ConfigureCursorSigningKey(key2); err != nil {
		t.Fatal(err)
	}

	// The previously issued cursor must still verify after the restart.
	if _, err := decodeCursor(token, vehicle.PlatformDivar, criteria); err != nil {
		t.Fatalf("previously issued cursor does not verify after restart: %v", err)
	}

	// Sanity: the signature is key-bound. Under a rotated key the same token must
	// be rejected, which is exactly what an unauthorized re-key would cause.
	rotated := make([]byte, len(key2))
	copy(rotated, key2)
	rotated[0] ^= 0xff
	if err := ConfigureCursorSigningKey(rotated); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCursor(token, vehicle.PlatformDivar, criteria); err == nil {
		t.Fatal("cursor verified under a rotated key; signature is not key-bound")
	}
}

// Two independent OS processes run the application's startup key loading and
// real cursor encoder/decoder. The parent keeps Redis and the token file alive.
func TestCursorSurvivesProcessRestart(t *testing.T) {
	server := miniredis.RunT(t)
	tokenPath := filepath.Join(t.TempDir(), "cursor")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"issue", "verify"} {
		cmd := exec.Command(binary, "-test.run=^TestCursorProcessHelper$")
		cmd.Env = append(os.Environ(), "JAM_CURSOR_TEST_MODE="+mode, "JAM_CURSOR_TEST_REDIS=redis://"+server.Addr(), "JAM_CURSOR_TEST_TOKEN="+tokenPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cursor child %s failed: %v\n%s", mode, err, output)
		}
	}
}

func TestCursorProcessHelper(t *testing.T) {
	mode := os.Getenv("JAM_CURSOR_TEST_MODE")
	if mode == "" {
		return
	}
	cache, err := rediscache.New(os.Getenv("JAM_CURSOR_TEST_REDIS"), time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	key, err := cache.CursorSigningKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := ConfigureCursorSigningKey(key); err != nil {
		t.Fatal(err)
	}
	criteria := vehicle.SearchCriteria{Query: "206", Type: vehicle.VehicleTypeCar}
	path := os.Getenv("JAM_CURSOR_TEST_TOKEN")
	if mode == "issue" {
		token := encodeCursor(vehicle.PlatformDivar, criteria, 2, divarContinuation{})
		if err := os.WriteFile(path, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		token, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeCursor(string(token), vehicle.PlatformDivar, criteria); err != nil {
			t.Fatal("previous process's cursor failed verification")
		}
	}
}
