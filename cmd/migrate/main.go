package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/clove-labs/txmill/internal/config"
	"github.com/clove-labs/txmill/migrations"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: migrate <up|down|status|version|redo|reset> [args...]")
		os.Exit(2)
	}
	command := os.Args[1]
	args := os.Args[2:]

	cfg := config.Load()
	if cfg.DBURL == "" {
		fmt.Fprintln(os.Stderr, "TXMILL_DB_URL is required")
		os.Exit(2)
	}

	db, err := sql.Open("pgx", cfg.DBURL)
	if err != nil {
		fail("open db", err)
	}
	defer func() { _ = db.Close() }()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		fail("set dialect", err)
	}

	if err := goose.RunContext(context.Background(), command, db, ".", args...); err != nil {
		fail("migrate "+command, err)
	}
}

func fail(what string, err error) {
	slog.Error(what, "err", err)
	os.Exit(1)
}
