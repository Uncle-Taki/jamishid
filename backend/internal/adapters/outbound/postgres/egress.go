package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type EgressProxy struct {
	ID            string     `json:"id"`
	Address       string     `json:"address"`
	Enabled       bool       `json:"enabled"`
	Failures      int        `json:"failures"`
	CooldownUntil time.Time  `json:"cooldownUntil"`
	LastSuccess   *time.Time `json:"lastSuccess"`
}

func ValidateProxy(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || !strings.Contains("|http|https|socks5|socks5h|", "|"+u.Scheme+"|") {
		return errors.New("proxy must be an http, https, socks5 or socks5h URL with host and port")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid proxy port")
	}
	return nil
}
func (s *Store) PutProxy(ctx context.Context, proxyID, endpoint string, enabled bool) (string, error) {
	if err := ValidateProxy(endpoint); err != nil {
		return "", err
	}
	if proxyID == "" {
		proxyID = id()
	}
	_, err := s.Pool.Exec(ctx, `INSERT INTO egress_proxies(id,endpoint,enabled) VALUES($1,$2,$3) ON CONFLICT(id) DO UPDATE SET endpoint=$2,enabled=$3,cooldown_until=now(),failures=0`, proxyID, endpoint, enabled)
	return proxyID, err
}
func (s *Store) DeleteProxy(ctx context.Context, proxyID string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM egress_proxies WHERE id=$1`, proxyID)
	return err
}
func (s *Store) Proxies(ctx context.Context) ([]EgressProxy, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,endpoint,enabled,failures,cooldown_until,last_success FROM egress_proxies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []EgressProxy{}
	for rows.Next() {
		var p EgressProxy
		var endpoint string
		if err = rows.Scan(&p.ID, &endpoint, &p.Enabled, &p.Failures, &p.CooldownUntil, &p.LastSuccess); err != nil {
			return nil, err
		}
		u, _ := url.Parse(endpoint)
		if u != nil {
			p.Address = u.Scheme + "://" + u.Host
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
func (s *Store) AcquireEgress(ctx context.Context) (string, string, error) {
	var proxyID, endpoint string
	err := s.Pool.QueryRow(ctx, `UPDATE egress_proxies SET last_used=now() WHERE id=(SELECT id FROM egress_proxies WHERE enabled AND cooldown_until<=now() ORDER BY last_used NULLS FIRST,id FOR UPDATE SKIP LOCKED LIMIT 1) RETURNING id,endpoint`).Scan(&proxyID, &endpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		var configured bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM egress_proxies)`).Scan(&configured); err != nil {
			return "", "", err
		}
		if configured {
			return "", "", errors.New("no healthy enabled egress proxy")
		}
		return "", "", nil
	}
	return proxyID, endpoint, err
}
func (s *Store) ReportEgress(ctx context.Context, proxyID string, success bool) error {
	if proxyID == "" {
		return nil
	}
	_, err := s.Pool.Exec(ctx, `UPDATE egress_proxies SET failures=CASE WHEN $2 THEN 0 ELSE failures+1 END,last_success=CASE WHEN $2 THEN now() ELSE last_success END,cooldown_until=CASE WHEN $2 THEN cooldown_until ELSE now()+make_interval(secs=>least(900,30*(failures+1))) END WHERE id=$1`, proxyID, success)
	return err
}
