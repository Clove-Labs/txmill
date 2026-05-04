package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clove-labs/txmill/internal/api"
	"github.com/clove-labs/txmill/internal/app"
	"github.com/clove-labs/txmill/internal/config"
	"github.com/clove-labs/txmill/internal/keystore"
	"github.com/clove-labs/txmill/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := config.Load()

	if cfg.DBURL == "" {
		logger.Error("TXMILL_DB_URL is required")
		os.Exit(2)
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, cfg.DBURL)
	if err != nil {
		logger.Error("db pool", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	ks, err := keystore.New(cfg.KeystoreDir, cfg.KeystorePassword)
	if err != nil {
		logger.Error("keystore", "err", err)
		os.Exit(1)
	}

	handlers := &api.Handlers{
		Apps: app.NewService(pool, ks),
	}
	e := api.NewRouter(logger, handlers)

	go func() {
		logger.Info("http server starting", "addr", cfg.APIListen)
		if err := e.Start(cfg.APIListen); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = e.Shutdown(shutdownCtx)
}
