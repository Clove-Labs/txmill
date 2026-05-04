package dbtest

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/clove-labs/txmill/migrations"
)

var (
	migrateOnce sync.Once
	migrateErr  error
)

func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TXMILL_TEST_DB_URL")
	if url == "" {
		t.Skip("TXMILL_TEST_DB_URL not set; skipping integration test")
	}

	migrateOnce.Do(func() {
		migrateErr = applyMigrations(url)
	})
	if migrateErr != nil {
		t.Fatalf("apply migrations: %v", migrateErr)
	}

	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool new: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("pgxpool ping: %v", err)
	}
	if err := truncateAll(context.Background(), pool); err != nil {
		pool.Close()
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func applyMigrations(url string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", 8675309); err != nil {
		return err
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", 8675309)
	}()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.UpContext(ctx, db, ".")
}

func truncateAll(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		DO $$ DECLARE r RECORD;
		BEGIN
		  FOR r IN
		    SELECT tablename FROM pg_tables
		    WHERE schemaname = 'public' AND tablename != 'goose_db_version'
		  LOOP
		    EXECUTE 'TRUNCATE TABLE ' || quote_ident(r.tablename) || ' RESTART IDENTITY CASCADE';
		  END LOOP;
		END $$;
	`)
	return err
}
