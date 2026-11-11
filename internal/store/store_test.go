package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
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
