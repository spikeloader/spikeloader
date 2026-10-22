// Package db is the PostgreSQL connection: the platform's records (projects, secrets, runs, panel
// jobs). Bulky files — git mirrors, job logs, artifacts and caches — stay on disk under .lab/ci/.
//
// The database is the `spikeloader-postgres` container (`spikeloader db start`); DATABASE_URL
// overrides the connection.
package db

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"spikeloader/internal/env"
)

const schema = `
CREATE TABLE IF NOT EXISTS projects (
  id         text PRIMARY KEY,
  created_at bigint NOT NULL,
  data       jsonb NOT NULL
);
CREATE TABLE IF NOT EXISTS secrets (
  scope text NOT NULL,
  name  text NOT NULL,
  value text NOT NULL,
  PRIMARY KEY (scope, name)
);
CREATE SEQUENCE IF NOT EXISTS run_id_seq;
ALTER TABLE secrets ADD COLUMN IF NOT EXISTS protected boolean NOT NULL DEFAULT true;
CREATE TABLE IF NOT EXISTS runs (
  id      bigint PRIMARY KEY,
  project text NOT NULL,
  status  text NOT NULL,
  data    jsonb NOT NULL
);
CREATE INDEX IF NOT EXISTS runs_project_idx ON runs (project, id DESC);
CREATE TABLE IF NOT EXISTS panel_jobs (
  id         uuid PRIMARY KEY,
  started_at bigint NOT NULL,
  data       jsonb NOT NULL
);
`

// Pool is the shared connection pool, ready after Open.
var Pool *pgxpool.Pool

func connString() (string, error) {
	if u := os.Getenv("DATABASE_URL"); u != "" {
		return u, nil
	}
	b, err := os.ReadFile(filepath.Join(env.State, "pg-password"))
	if err != nil {
		return "", fmt.Errorf("PostgreSQL is not set up: run `spikeloader db start` (or set DATABASE_URL)")
	}
	pw := url.QueryEscape(strings.TrimSpace(string(b)))
	return fmt.Sprintf("postgres://spikeloader:%s@127.0.0.1:%s/spikeloader", pw, env.Get("PG_PORT", "5432")), nil
}

// Open connects (retrying while the container starts) and creates the schema.
func Open(ctx context.Context) error {
	cs, err := connString()
	if err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(cs)
	if err != nil {
		return err
	}
	cfg.MaxConns = 5
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	for attempt := 1; ; attempt++ {
		_, err = pool.Exec(ctx, schema)
		if err == nil {
			break
		}
		if attempt >= 30 {
			pool.Close()
			return fmt.Errorf("PostgreSQL unreachable: %w", err)
		}
		time.Sleep(time.Second)
	}
	Pool = pool
	go writer()
	return nil
}

type write struct {
	sql  string
	args []any
	done chan error
}

var (
	writes  = make(chan write, 4096)
	pending atomic.Int64
)

// Write queues a statement: the engine keeps its working set in memory and persists changes in
// order, without making its callers wait. Failures are logged. Arguments must already be
// snapshots (e.g. JSON bytes), since they are used later.
func Write(sql string, args ...any) {
	pending.Add(1)
	writes <- write{sql: sql, args: args}
}

// WriteTracked queues a critical snapshot in order. Callers may enqueue under their lock,
// but must release it before waiting for the result.
func WriteTracked(sql string, args ...any) <-chan error {
	done := make(chan error, 1)
	pending.Add(1)
	writes <- write{sql: sql, args: args, done: done}
	return done
}

func writer() {
	for w := range writes {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := Pool.Exec(ctx, w.sql, w.args...)
		if err != nil {
			log.Printf("db: write failed: %v", err)
		}
		cancel()
		pending.Add(-1)
		if w.done != nil {
			w.done <- err
		}
	}
}

// Flush waits until the queued writes are applied (used on shutdown).
func Flush(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for pending.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
}
