package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// openTestStore connects to PROFILE_TEST_DATABASE_URL inside a throwaway
// schema, so the test never touches real tables. Without the variable the
// test is skipped: `go test ./...` needs no database.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	raw := os.Getenv("PROFILE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("PROFILE_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(ctx)
	})

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()

	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestMigrateAndGetProfile(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	applied, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("first Migrate applied nothing")
	}
	again, err := s.Migrate(ctx)
	if err != nil || len(again) != 0 {
		t.Fatalf("second Migrate = %v, %v; want nothing applied", again, err)
	}

	if _, err := s.GetProfile(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProfile(missing) error = %v, want ErrNotFound", err)
	}

	if _, err := s.pool.Exec(ctx,
		"INSERT INTO profiles (user_id, display_name, email) VALUES ($1, $2, $3)",
		"123", "Ada", "ada@example.com"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	p, err := s.GetProfile(ctx, "123")
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if p.UserID != "123" || p.DisplayName != "Ada" || p.Email != "ada@example.com" || p.CreatedAt.IsZero() {
		t.Errorf("unexpected profile %+v", p)
	}
}

func migratedTestStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s
}

func TestUpsertProfile(t *testing.T) {
	s := migratedTestStore(t)
	ctx := context.Background()

	// Moves the row's timestamps an hour back, so a later write gives a
	// strictly later updated_at however fast the test runs.
	backdate := func(t *testing.T, userID string) Profile {
		t.Helper()
		if _, err := s.pool.Exec(ctx, `UPDATE profiles SET created_at = created_at - interval '1 hour',
			updated_at = updated_at - interval '1 hour' WHERE user_id = $1`, userID); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		p, err := s.GetProfile(ctx, userID)
		if err != nil {
			t.Fatalf("GetProfile: %v", err)
		}
		return p
	}

	t.Run("create", func(t *testing.T) {
		p, created, err := s.UpsertProfile(ctx, "u-create", "Ada")
		if err != nil {
			t.Fatalf("UpsertProfile: %v", err)
		}
		if !created || p.UserID != "u-create" || p.DisplayName != "Ada" || p.Email != "" ||
			p.CreatedAt.IsZero() || !p.CreatedAt.Equal(p.UpdatedAt) {
			t.Errorf("got %+v, created=%v", p, created)
		}
	})

	t.Run("no-op update", func(t *testing.T) {
		if _, _, err := s.UpsertProfile(ctx, "u-noop", "Ada"); err != nil {
			t.Fatalf("UpsertProfile: %v", err)
		}
		before := backdate(t, "u-noop")
		p, created, err := s.UpsertProfile(ctx, "u-noop", "Ada")
		if err != nil {
			t.Fatalf("UpsertProfile: %v", err)
		}
		if created || !p.CreatedAt.Equal(before.CreatedAt) || !p.UpdatedAt.Equal(before.UpdatedAt) {
			t.Errorf("got %+v, created=%v; want timestamps of %+v", p, created, before)
		}
	})

	t.Run("changed update keeps email and created_at", func(t *testing.T) {
		if _, err := s.pool.Exec(ctx,
			"INSERT INTO profiles (user_id, display_name, email) VALUES ($1, $2, $3)",
			"u-change", "Ada", "ada@example.com"); err != nil {
			t.Fatalf("insert: %v", err)
		}
		before := backdate(t, "u-change")
		p, created, err := s.UpsertProfile(ctx, "u-change", "Ada Lovelace")
		if err != nil {
			t.Fatalf("UpsertProfile: %v", err)
		}
		if created || p.DisplayName != "Ada Lovelace" || p.Email != "ada@example.com" ||
			!p.CreatedAt.Equal(before.CreatedAt) || !p.UpdatedAt.After(before.UpdatedAt) {
			t.Errorf("got %+v, created=%v; before %+v", p, created, before)
		}
		got, err := s.GetProfile(ctx, "u-change")
		if err != nil || got != p {
			t.Errorf("GetProfile = %+v, %v; want %+v", got, err, p)
		}
	})

	t.Run("concurrent upserts of one user", func(t *testing.T) {
		const n = 16
		var (
			wg      sync.WaitGroup
			mu      sync.Mutex
			creates int
			errs    []error
		)
		for i := range n {
			wg.Go(func() {
				_, created, err := s.UpsertProfile(ctx, "u-race", fmt.Sprintf("name %d", i%2))
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, err)
				}
				if created {
					creates++
				}
			})
		}
		wg.Wait()
		if len(errs) > 0 || creates != 1 {
			t.Fatalf("errors %v, %d creates; want none and exactly 1", errs, creates)
		}
		var rows int
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM profiles WHERE user_id = $1", "u-race").Scan(&rows); err != nil || rows != 1 {
			t.Fatalf("rows = %d, %v; want 1", rows, err)
		}
	})
}
