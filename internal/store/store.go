// Package store is the PostgreSQL persistence layer.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when the requested row does not exist.
var ErrNotFound = errors.New("not found")

// Profile is a user's profile. UserID is the ZITADEL subject.
type Profile struct {
	UserID      string    `json:"user_id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Store wraps a pgx connection pool.
type Store struct {
	pool *pgxpool.Pool
}

// Open creates a pool for databaseURL with OpenTelemetry query tracing
// (spans go to the global tracer provider, so set it up first) and pool
// metrics. It does not connect; the first query or Ping does.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("record pool stats: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close closes all connections in the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// GetProfile returns the profile of userID, or ErrNotFound.
func (s *Store) GetProfile(ctx context.Context, userID string) (Profile, error) {
	var p Profile
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, display_name, email, created_at, updated_at
		   FROM profiles WHERE user_id = $1`, userID,
	).Scan(&p.UserID, &p.DisplayName, &p.Email, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("get profile: %w", err)
	}
	return p, nil
}

// UpsertProfile creates the profile of userID or replaces its client-writable
// fields, in one statement, and returns the stored row and whether it was
// created. updated_at changes only when a value changes, so it is the time of
// the last real change. The created flag uses RETURNING old (PostgreSQL 18):
// old is NULL for an inserted row.
func (s *Store) UpsertProfile(ctx context.Context, userID, displayName string) (Profile, bool, error) {
	var (
		p       Profile
		created bool
	)
	err := s.pool.QueryRow(ctx,
		`INSERT INTO profiles (user_id, display_name) VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE
		    SET display_name = EXCLUDED.display_name,
		        updated_at = CASE WHEN profiles.display_name IS DISTINCT FROM EXCLUDED.display_name
		                          THEN now() ELSE profiles.updated_at END
		 RETURNING user_id, display_name, email, created_at, updated_at, (old.user_id IS NULL) AS created`,
		userID, displayName,
	).Scan(&p.UserID, &p.DisplayName, &p.Email, &p.CreatedAt, &p.UpdatedAt, &created)
	if err != nil {
		return Profile{}, false, fmt.Errorf("upsert profile: %w", err)
	}
	return p, created, nil
}
