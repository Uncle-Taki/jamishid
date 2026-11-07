package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

type ownership struct {
	dir, socket, pid, token, maxmemory string
}

func targetOptions(raw string, e ownership) (*redis.Options, error) {
	options, err := redis.ParseURL(raw)
	if err != nil || options.Network != "unix" || options.Addr != e.socket || options.DB != 0 {
		return nil, errors.New("only the launcher's private Unix socket, DB 0, is allowed")
	}
	return options, nil
}

func ownershipFromEnv() (ownership, error) {
	e := ownership{os.Getenv("PRESSURE_DIR"), os.Getenv("PRESSURE_SOCKET"), os.Getenv("PRESSURE_PID"), os.Getenv("PRESSURE_MARKER"), os.Getenv("PRESSURE_MAXMEMORY")}
	n, err := strconv.ParseInt(e.maxmemory, 10, 64)
	pid, pidErr := strconv.Atoi(e.pid)
	if err != nil || n < 8<<20 || n > 64<<20 || pidErr != nil || pid <= 0 || len(e.token) != 32 {
		return e, errors.New("launcher identity required; memory must be 8–64 MiB")
	}
	if !filepath.IsAbs(e.dir) || !strings.HasPrefix(filepath.Base(e.dir), "jamshid-redis-pressure.") || e.socket != filepath.Join(e.dir, "redis.sock") {
		return e, errors.New("launcher private directory/socket required")
	}
	info, err := os.Stat(e.dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return e, errors.New("launcher directory must be private (0700)")
	}
	return e, nil
}

// No writes are allowed here. The marker must already exist on the process
// started by the launcher, and it must be the only key in the selected DB.
func verifyOwnership(ctx context.Context, client *redis.Client, e ownership) error {
	server, err := client.Info(ctx, "server").Result()
	if err != nil {
		return err
	}
	if firstKV(server, "process_id") != e.pid {
		return errors.New("server process does not match launcher")
	}
	for key, value := range map[string]string{"dir": e.dir, "maxmemory": e.maxmemory, "maxmemory-policy": "noeviction", "appendonly": "no", "save": "", "port": "0", "unixsocket": e.socket} {
		config, err := client.ConfigGet(ctx, key).Result()
		if err != nil {
			return err
		}
		if got, ok := config[key]; !ok || got != value {
			return fmt.Errorf("disposable configuration mismatch: %s", key)
		}
	}
	value, err := client.Get(ctx, pressureMarkerPrefix+e.token).Result()
	if err != nil || value != e.token {
		return errors.New("launcher marker missing or mismatched")
	}
	size, err := client.DBSize(ctx).Result()
	if err != nil {
		return err
	}
	if size != 1 {
		return errors.New("target contains keys other than the launcher marker")
	}
	return nil
}
