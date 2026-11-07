package rediscache

import (
	"context"
	"errors"
	"strings"

	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/adapters/outbound/errorlog"
	"github.com/Uncle-Taki/Jame_e_Jahan_Nama/backend/internal/domain/ports"
	"github.com/redis/go-redis/v9"
)

type diagnosticHook struct{}

func redisDiagnostic(operation string, err error) ports.Diagnostic {
	d := ports.Diagnostic{Component: "redis", Operation: operation}
	var server redis.Error
	if errors.As(err, &server) {
		// Only the protocol error code is safe; the remainder can contain keys.
		code := strings.SplitN(server.Error(), " ", 2)[0]
		switch code {
		case "WRONGTYPE", "NOAUTH", "WRONGPASS", "OOM", "READONLY", "LOADING", "BUSY", "NOSCRIPT", "ERR":
			d.ErrorClass = "redis_" + code
		default:
			d.ErrorClass = "redis_server_error"
		}
	}
	return d
}

type scriptFallbackKey struct{}

// runScript scopes NOSCRIPT suppression to calls that really perform the
// EVALSHA -> EVAL fallback. Direct EVALSHA and pipeline failures remain visible.
// The EVAL retry uses the same hooks, so any terminal error is still reported.
func runScript(ctx context.Context, script *redis.Script, client *redis.Client, keys []string, args ...interface{}) *redis.Cmd {
	return script.Run(context.WithValue(ctx, scriptFallbackKey{}, true), client, keys, args...)
}

// reportRedis uses next()'s returned error because cmd.Err() may not yet be set.
func reportRedis(ctx context.Context, cmd redis.Cmder, err error) {
	if err == nil || errors.Is(err, redis.Nil) {
		return
	}
	if ctx.Value(scriptFallbackKey{}) == true && strings.EqualFold(cmd.Name(), "evalsha") && redis.HasErrorPrefix(err, "NOSCRIPT") {
		return
	}
	ports.ReportDiagnosticOccurrence(ctx, redisDiagnostic(cmd.Name(), err), err)
}

func (diagnosticHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (diagnosticHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		ctx = errorlog.Context(ctx)
		err := next(ctx, cmd)
		reportRedis(ctx, cmd, err)
		return err
	}
}
func (diagnosticHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		ctx = errorlog.Context(ctx)
		err := next(ctx, cmds)
		for _, cmd := range cmds {
			reportRedis(ctx, cmd, cmd.Err())
		}
		return err
	}
}
