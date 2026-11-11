package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLockID is the key of the session-level advisory lock that keeps
// replicas starting at the same time from applying migrations concurrently.
const migrationLockID int64 = 0x70726f66696c65 // "profile"

// Migrate applies the embedded migrations that are not yet recorded in
// schema_migrations, in file name order, each in its own transaction. It
// returns the versions it applied.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return nil, fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// Use a fresh context: ctx may be cancelled, and the lock must not outlive this call.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text        PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	var applied []string
	for _, name := range names {
		version := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
		done, err := s.applyMigration(ctx, conn.Conn(), name, version)
		if err != nil {
			return applied, fmt.Errorf("migration %s: %w", version, err)
		}
		if done {
			applied = append(applied, version)
		}
	}
	return applied, nil
}

func (s *Store) applyMigration(ctx context.Context, conn *pgx.Conn, name, version string) (bool, error) {
	var exists bool
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version,
	).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	sql, err := migrationFiles.ReadFile(name)
	if err != nil {
		return false, err
	}
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// No arguments: pgx uses the simple protocol, so a file may hold several statements.
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version)
		return err
	})
	return err == nil, err
}
